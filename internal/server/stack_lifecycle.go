package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/suparcloud/suparship/internal/domain"
)

// Stack batch lifecycle (Phase 3).
//
// These endpoints act on a whole stack by fanning out over its member apps
// (apps whose Spec.Stack == the stack) using the existing per-app flows —
// republish, promote, preview, delete. There are no new ArgoCD/Kargo
// generators: a stack stays a coordination layer over independent apps. Every
// op is best-effort and returns a per-app result summary so a partial failure
// is visible rather than silently swallowed.

// stackOpResult is one app's outcome in a batch operation. Skipped rows are a
// third outcome distinct from success/failure: the op did not apply to this
// member (e.g. previews disabled, not a pipeline app) — not an error to fix.
type stackOpResult struct {
	App     string `json:"app"`
	OK      bool   `json:"ok"`
	Skipped bool   `json:"skipped,omitempty"`
	Message string `json:"message,omitempty"`
	Error   string `json:"error,omitempty"`
}

// stackBatchResponse is the wire shape of a batch op: the stack + per-app rows.
type stackBatchResponse struct {
	Project string          `json:"project"`
	Stack   string          `json:"stack"`
	Action  string          `json:"action"`
	Results []stackOpResult `json:"results"`
}

func okResult(app, msg string) stackOpResult { return stackOpResult{App: app, OK: true, Message: msg} }
func errResult(app string, err error) stackOpResult {
	return stackOpResult{App: app, OK: false, Error: err.Error()}
}

// skipResult marks a member the op did not apply to (OK, but not acted on).
func skipResult(app, reason string) stackOpResult {
	return stackOpResult{App: app, OK: true, Skipped: true, Message: reason}
}

// stackMemberApps returns the member apps of a stack (Spec.Stack == stackName).
func (rh *rbacHandler) stackMemberApps(ctx context.Context, project, stackName string) []*domain.App {
	if rh.appHandler == nil {
		return nil
	}
	apps, _ := rh.appHandler.appStore.ListApps(ctx, project)
	var members []*domain.App
	for _, a := range apps {
		if a.Spec.Stack == stackName {
			members = append(members, a)
		}
	}
	return members
}

// selectStackMembers narrows a stack's members to an optional subset by name.
// An empty subset selects all members (the default). Names that are not members
// of the stack are returned as an error so a caller mistake is surfaced rather
// than silently acting on fewer apps than intended.
func selectStackMembers(members []*domain.App, subset []string) ([]*domain.App, error) {
	if len(subset) == 0 {
		return members, nil
	}
	byName := make(map[string]*domain.App, len(members))
	for _, a := range members {
		byName[a.Name] = a
	}
	selected := make([]*domain.App, 0, len(subset))
	var unknown []string
	for _, name := range subset {
		if a, ok := byName[name]; ok {
			selected = append(selected, a)
		} else {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		return nil, fmt.Errorf("apps not in stack: %v", unknown)
	}
	return selected, nil
}

// handleSyncStack republishes every member app of a stack.
func (rh *rbacHandler) handleSyncStack(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	name := r.PathValue("stack")
	if rh.appHandler == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "app store not configured"})
		return
	}
	if _, err := rh.stackStore.GetStack(r.Context(), project, name); err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "stack not found: " + name})
		return
	}
	members := rh.stackMemberApps(r.Context(), project, name)
	results := make([]stackOpResult, 0, len(members))
	for _, a := range members {
		if err := rh.appHandler.republishApp(r.Context(), a); err != nil {
			results = append(results, errResult(a.Name, err))
			continue
		}
		results = append(results, okResult(a.Name, "synced"))
	}
	writeJSON(w, http.StatusOK, stackBatchResponse{Project: project, Stack: name, Action: "sync", Results: results})
}

// handlePromoteStack promotes every member app to the target environment.
func (rh *rbacHandler) handlePromoteStack(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	name := r.PathValue("stack")
	if rh.appHandler == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "app store not configured"})
		return
	}
	var req AppPromoteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}
	if req.TargetEnvironment == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "targetEnvironment is required"})
		return
	}
	if _, err := rh.stackStore.GetStack(r.Context(), project, name); err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "stack not found: " + name})
		return
	}
	members := rh.stackMemberApps(r.Context(), project, name)
	results := make([]stackOpResult, 0, len(members))
	for _, a := range members {
		res, err := rh.appHandler.promoteAppEnv(r.Context(), project, a.Name, req.TargetEnvironment)
		if err != nil {
			results = append(results, errResult(a.Name, err))
			continue
		}
		results = append(results, okResult(a.Name, res.Message))
	}
	writeJSON(w, http.StatusOK, stackBatchResponse{Project: project, Stack: name, Action: "promote", Results: results})
}

// stackPreviewRequest is the body for creating a stack preview. It mirrors the
// per-app CreateAppPreviewRequest (name/baseEnv/imageTag) and adds an optional
// Apps subset so a caller can preview only some members (default: all
// previewable members). A single imageTag applies to every member — the common
// monorepo case where all apps are built from one commit SHA.
type stackPreviewRequest struct {
	Name     string   `json:"name"`
	BaseEnv  string   `json:"baseEnv,omitempty"`
	ImageTag string   `json:"imageTag,omitempty"`
	Apps     []string `json:"apps,omitempty"`
}

// handleCreateStackPreview brings up a preview of the whole stack in one shared
// namespace so the previewed collection is co-located and reaches itself by
// in-cluster DNS. The namespace comes from the project's PreviewNamespacePattern
// (stack name in the {app} slot), defaulting to "{project}-{stack}-preview-{name}".
// It is an upsert (like the per-app preview) so CI can re-point every member at a
// freshly built image on each PR push in one call. Members with previews disabled
// are skipped (not failed) so a mixed stack previews cleanly.
//
// A pattern without {name} (e.g. "{project}-preview") puts every preview of every
// stack in ONE namespace. Members must then disambiguate their workload names with
// ((platform.previewName)) in fullnameOverride, or concurrent PRs overwrite each
// other's Deployments — suparship only suffixes the platform ConfigMap/Secret.
func (rh *rbacHandler) handleCreateStackPreview(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	name := r.PathValue("stack")
	if rh.appHandler == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "app store not configured"})
		return
	}
	var req stackPreviewRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}
	preview := domain.SanitizePreviewName(req.Name)
	if err := domain.ValidatePreviewName(preview); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid preview name: " + err.Error()})
		return
	}
	if _, err := rh.stackStore.GetStack(r.Context(), project, name); err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "stack not found: " + name})
		return
	}
	members, err := selectStackMembers(rh.stackMemberApps(r.Context(), project, name), req.Apps)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	// Resolve the base env once for the whole stack so every member clones the
	// same stable env (cluster + vault + per-env config). Envs are project-level,
	// so any member resolves the same set; use the first to default/validate.
	baseEnv := strings.TrimSpace(req.BaseEnv)
	if len(members) > 0 {
		stableEnvs := rh.appHandler.stableEnvsFromOrg(r.Context(), members[0])
		if baseEnv == "" {
			if len(stableEnvs) > 0 {
				baseEnv = stableEnvs[0].EnvName
			}
		} else {
			valid := false
			for _, e := range stableEnvs {
				if e.EnvName == baseEnv {
					valid = true
					break
				}
			}
			if !valid {
				writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: "invalid baseEnv \"" + baseEnv + "\": not a stable environment of this stack"})
				return
			}
		}
	}
	if baseEnv == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "no stable environment available to base the preview on"})
		return
	}

	// Resolve the namespace once, from the project's preview namespace pattern, and
	// pass it to every member: the whole stack preview lands in one namespace.
	ns := stackPreviewNamespace(project, name, preview, rh.appHandler.previewNamespacePattern(r.Context(), project))
	imageTag := strings.TrimSpace(req.ImageTag)
	// The prep + batched publish is the slow part; defer it when the caller opts
	// into async (Prefer: respond-async / ?async=1) so a large stack doesn't 504.
	// Validation above already ran synchronously.
	op := func(ctx context.Context) (int, any, error) {
		return http.StatusCreated, rh.createStackPreviewExec(ctx, project, name, members, preview, ns, baseEnv, imageTag), nil
	}
	dispatchOp(w, r, rh.appHandler.async, "preview-stack", project, op)
}

// createStackPreviewExec builds each member's preview spec + store record
// concurrently (bounded, git-free), then publishes them all in ONE
// clone/commit/push. Mirrors pinStackExec: prep can't 504 (no git) and the single
// batched publish collapses N members' clone/commit/push into one git round-trip.
// On a batch publish failure nothing was pushed, so every prepared member's store
// record is rolled back.
func (rh *rbacHandler) createStackPreviewExec(ctx context.Context, project, name string, members []*domain.App, preview, ns, baseEnv, imageTag string) stackBatchResponse {
	ah := rh.appHandler
	baseDomain, secure := ah.previewRoutingForEnv(ctx, baseEnv)

	type previewPrep struct {
		app     *domain.App
		inst    *domain.EnvironmentInstance
		prior   *domain.AppEnvironment
		existed bool
		skip    string
		err     error
	}
	prepStart := time.Now()
	preps := prepMembers(members, func(a *domain.App) previewPrep {
		if !a.Spec.PreviewsEnabled {
			return previewPrep{app: a, skip: "previews disabled"}
		}
		inst, _, prior, existed, err := ah.prepAppPreview(ctx, a, preview, baseEnv, imageTag, baseDomain, "", ns, secure)
		return previewPrep{app: a, inst: inst, prior: prior, existed: existed, err: err}
	})
	prepDur := time.Since(prepStart)

	results := make([]stackOpResult, 0, len(members))
	var targets []PreviewPublishTarget
	var published []previewPrep
	for _, p := range preps {
		switch {
		case p.skip != "":
			results = append(results, skipResult(p.app.Name, p.skip))
		case p.err != nil:
			results = append(results, errResult(p.app.Name, p.err))
		default:
			targets = append(targets, PreviewPublishTarget{App: p.app, Preview: p.inst, BaseEnv: baseEnv, ImageTag: imageTag})
			published = append(published, p)
		}
	}

	pubStart := time.Now()
	if len(targets) > 0 {
		if err := ah.publishPreviewsBatch(ctx, targets); err != nil {
			for _, p := range published {
				ah.rollbackPreviewPrep(ctx, p.app, preview, p.prior, p.existed)
				results = append(results, errResult(p.app.Name, err))
			}
		} else {
			for _, p := range published {
				results = append(results, okResult(p.app.Name, "preview "+preview+" → "+ns))
			}
		}
	}
	slog.Info("stack preview timing",
		"project", project, "stack", name,
		"members", len(members), "published", len(targets),
		"prep", prepDur.Round(time.Millisecond).String(),
		"publish", time.Since(pubStart).Round(time.Millisecond).String(),
	)
	return stackBatchResponse{Project: project, Stack: name, Action: "preview", Results: results}
}

// handleDeleteStackPreview tears down a stack preview by removing each member's
// preview environment and pruning its gitops files (via DeleteAppPreview) so
// ArgoCD prunes the generated Applications. The shared preview namespace itself
// is left for ArgoCD/stack-delete reclaim (mirrors the per-app preview delete).
// Members that have no such preview are skipped silently.
func (rh *rbacHandler) handleDeleteStackPreview(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	name := r.PathValue("stack")
	// Sanitize to match the name create stored/published under (create applies
	// SanitizePreviewName), so a raw "PR-42" resolves the stored "pr-42".
	preview := domain.SanitizePreviewName(r.PathValue("name"))
	if rh.appHandler == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "app store not configured"})
		return
	}
	if _, err := rh.stackStore.GetStack(r.Context(), project, name); err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "stack not found: " + name})
		return
	}
	members := rh.stackMemberApps(r.Context(), project, name)
	// Per-member gitops prune (clone/commit/push) is the slow part; defer it when
	// the caller opts into async so tearing down a large stack preview can't 504.
	op := func(ctx context.Context) (int, any, error) {
		results := make([]stackOpResult, 0, len(members))
		for _, a := range members {
			env, err := rh.appHandler.appStore.GetAppEnvironment(ctx, project, a.Name, preview)
			if err != nil || env.EnvType != domain.AppEnvPreview {
				continue // app has no such preview — skip silently
			}
			// baseEnv locates the preview's gitops tree (previews/{baseEnv}/...).
			// Fall back to the app's first stable env for records created before
			// BaseEnv was persisted, so the prune still targets a real path.
			baseEnv := env.BaseEnv
			if baseEnv == "" {
				if stable := rh.appHandler.stableEnvsFromOrg(ctx, a); len(stable) > 0 {
					baseEnv = stable[0].EnvName
				}
			}
			// Prune gitops first (so ArgoCD prunes the Application), then drop the
			// store record — mirrors the per-app preview delete ordering.
			if d, ok := rh.appHandler.gitOpsPublisher.(AppPreviewDeleter); ok {
				if err := d.DeleteAppPreview(ctx, project, preview, a.Name, baseEnv); err != nil {
					results = append(results, errResult(a.Name, err))
					continue
				}
			}
			if err := rh.appHandler.appStore.DeleteAppEnvironment(ctx, project, a.Name, preview); err != nil {
				results = append(results, errResult(a.Name, err))
				continue
			}
			results = append(results, okResult(a.Name, "preview "+preview+" deleted"))
		}
		return http.StatusOK, stackBatchResponse{Project: project, Stack: name, Action: "preview-delete", Results: results}, nil
	}
	dispatchOp(w, r, rh.appHandler.async, "preview-stack-delete", project, op)
}

// validTargetEnv reports whether envName is a real stable env of the project
// (any member resolves the same project env set, so the first is representative).
// Empty members → true (nothing to act on). This lets the fan-out reject a
// mistyped targetEnv with a 4xx instead of silently skipping every member and
// reporting success.
func (rh *rbacHandler) validTargetEnv(ctx context.Context, members []*domain.App, envName string) bool {
	if len(members) == 0 || rh.appHandler == nil {
		return true
	}
	for _, e := range rh.appHandler.stableEnvsFromOrg(ctx, members[0]) {
		if e.EnvName == envName {
			return true
		}
	}
	return false
}

// stackPrepConcurrency bounds how many members' spec-prep steps run at once in a
// fan-out. Prep does a bounded live cluster read per member (pre-pin capture /
// Kargo freight lookup); running them concurrently keeps one slow/unreachable
// cluster from serializing the whole request into a gateway timeout.
const stackPrepConcurrency = 8

// prepMembers runs prep for each member concurrently (bounded), returning the
// results in member order. prep must be safe for concurrent use across members —
// each touches a distinct app, and the app store + live readers are goroutine-safe.
func prepMembers[T any](members []*domain.App, prep func(a *domain.App) T) []T {
	out := make([]T, len(members))
	sem := make(chan struct{}, stackPrepConcurrency)
	var wg sync.WaitGroup
	for i, a := range members {
		wg.Add(1)
		go func(i int, a *domain.App) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out[i] = prep(a)
		}(i, a)
	}
	wg.Wait()
	return out
}

// stackPinRequest pins a PR preview group to a stable env across the stack. Each
// pipeline member resolves its OWN image tag from its preview named fromPreview
// (a single tag can't apply — members have different image repos). Members that
// are direct-delivery, not deployed to targetEnv, or lack the named preview are
// skipped, not failed. Apps optionally narrows to a subset (default: all).
type stackPinRequest struct {
	FromPreview string   `json:"fromPreview"`
	TargetEnv   string   `json:"targetEnv"`
	Apps        []string `json:"apps,omitempty"`
}

// handlePinStack pins a stack's PR preview to a stable env across member apps.
func (rh *rbacHandler) handlePinStack(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	name := r.PathValue("stack")
	if rh.appHandler == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "app store not configured"})
		return
	}
	var req stackPinRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}
	req.FromPreview = strings.TrimSpace(req.FromPreview)
	req.TargetEnv = strings.TrimSpace(req.TargetEnv)
	if req.FromPreview == "" || req.TargetEnv == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "fromPreview and targetEnv are required"})
		return
	}
	if _, err := rh.stackStore.GetStack(r.Context(), project, name); err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "stack not found: " + name})
		return
	}
	members, err := selectStackMembers(rh.stackMemberApps(r.Context(), project, name), req.Apps)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	if !rh.validTargetEnv(r.Context(), members, req.TargetEnv) {
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: "unknown target environment: " + req.TargetEnv})
		return
	}
	// The prep + single batched publish is the slow part; defer it when the caller
	// opts into async (Prefer: respond-async / ?async=1) so a large stack doesn't
	// 504. Validation above already ran synchronously.
	op := func(ctx context.Context) (int, any, error) {
		return http.StatusOK, rh.pinStackExec(ctx, project, name, members, req), nil
	}
	dispatchOp(w, r, rh.appHandler.async, "pin-stack", project, op)
}

// pinStackExec runs the two-phase pin for the resolved members and returns the
// per-member batch result. Phase 1 mutates each member's spec concurrently
// (bounded), phase 2 publishes every pinned member in ONE clone/commit/push.
func (rh *rbacHandler) pinStackExec(ctx context.Context, project, name string, members []*domain.App, req stackPinRequest) stackBatchResponse {
	// Phase 1 (concurrent): mutate each member's spec — each prep does a bounded
	// live-status read (pre-pin capture), so run them in parallel to avoid a
	// gateway timeout on a large stack. No git yet.
	type pinPrep struct {
		app       *domain.App
		targetEnv *domain.AppEnvironment
		tag       string
		err       error
	}
	prepStart := time.Now()
	preps := prepMembers(members, func(a *domain.App) pinPrep {
		app, targetEnv, tag, err := rh.appHandler.pinAppEnvSpec(ctx, project, a.Name, req.TargetEnv, req.FromPreview)
		return pinPrep{app: app, targetEnv: targetEnv, tag: tag, err: err}
	})
	prepDur := time.Since(prepStart)
	results := make([]stackOpResult, 0, len(members))
	var items []appFocusPublish
	tags := map[string]string{}
	var pubNames []string
	for i, a := range members {
		p := preps[i]
		switch {
		case p.err == nil:
			items = append(items, appFocusPublish{app: p.app, focusEnv: p.targetEnv})
			tags[a.Name] = p.tag
			pubNames = append(pubNames, a.Name)
		case pinIsSkippable(p.err):
			results = append(results, skipResult(a.Name, p.err.Error()))
		default:
			results = append(results, errResult(a.Name, p.err))
		}
	}
	// Phase 2: publish every pinned member (tree + Kargo pause + target env) in
	// ONE clone/commit/push — the 504 fix.
	pubStart := time.Now()
	if len(items) > 0 {
		if err := rh.appHandler.republishAppsFocus(ctx, items); err != nil {
			for _, n := range pubNames {
				results = append(results, errResult(n, err))
			}
		} else {
			for _, n := range pubNames {
				results = append(results, okResult(n, "pinned "+req.TargetEnv+" → "+tags[n]))
			}
		}
	}
	// Endpoint-level split so a 504 is attributable to the concurrent per-member
	// prep (Kargo freight reads) vs the single batched publish.
	slog.Info("stack pin timing",
		"project", project, "stack", name,
		"members", len(members), "published", len(items),
		"prep", prepDur.Round(time.Millisecond).String(),
		"publish", time.Since(pubStart).Round(time.Millisecond).String(),
	)
	return stackBatchResponse{Project: project, Stack: name, Action: "pin", Results: results}
}

// handleUnpinStack clears a stack's pin on a stable env across member apps,
// restoring each member's pre-pin image (per-member freight-restore). Takes a
// JSON body {targetEnv, apps?} — symmetric with handlePinStack — where targetEnv
// is required and apps optionally narrows to a subset. Members not pinned on that
// env are skipped.
func (rh *rbacHandler) handleUnpinStack(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	name := r.PathValue("stack")
	if rh.appHandler == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "app store not configured"})
		return
	}
	var req stackSuspendRequest // {targetEnv, apps} — same shape as suspend
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}
	req.TargetEnv = strings.TrimSpace(req.TargetEnv)
	if req.TargetEnv == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "targetEnv is required"})
		return
	}
	if _, err := rh.stackStore.GetStack(r.Context(), project, name); err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "stack not found: " + name})
		return
	}
	members, err := selectStackMembers(rh.stackMemberApps(r.Context(), project, name), req.Apps)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	if !rh.validTargetEnv(r.Context(), members, req.TargetEnv) {
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: "unknown target environment: " + req.TargetEnv})
		return
	}
	op := func(ctx context.Context) (int, any, error) {
		return http.StatusOK, rh.unpinStackExec(ctx, project, name, members, req), nil
	}
	dispatchOp(w, r, rh.appHandler.async, "unpin-stack", project, op)
}

// unpinStackExec runs the two-phase unpin for the resolved members and returns
// the per-member batch result.
func (rh *rbacHandler) unpinStackExec(ctx context.Context, project, name string, members []*domain.App, req stackSuspendRequest) stackBatchResponse {
	// Phase 1 (concurrent): clear each member's pin in the spec — each prep does a
	// bounded Kargo freight read per member, so run them in parallel to avoid a
	// gateway timeout on a large stack. No git yet.
	type unpinPrep struct {
		app       *domain.App
		targetEnv *domain.AppEnvironment
		restore   string
		wasPinned bool
		err       error
	}
	preps := prepMembers(members, func(a *domain.App) unpinPrep {
		app, targetEnv, restore, wasPinned, err := rh.appHandler.unpinAppEnvSpec(ctx, project, a.Name, req.TargetEnv)
		return unpinPrep{app: app, targetEnv: targetEnv, restore: restore, wasPinned: wasPinned, err: err}
	})
	results := make([]stackOpResult, 0, len(members))
	var items []appFocusPublish
	type unpinned struct {
		app     *domain.App
		restore string
	}
	pending := map[string]unpinned{}
	var pubNames []string
	for i, a := range members {
		p := preps[i]
		switch {
		case p.err != nil:
			results = append(results, errResult(a.Name, p.err))
		case !p.wasPinned:
			results = append(results, skipResult(a.Name, "not pinned"))
		default:
			items = append(items, appFocusPublish{app: p.app, focusEnv: p.targetEnv})
			pending[a.Name] = unpinned{app: p.app, restore: p.restore}
			pubNames = append(pubNames, a.Name)
		}
	}
	// Phase 2: publish every unpinned member in ONE commit, then clear the
	// one-shot restore flag per member.
	if len(items) > 0 {
		if err := rh.appHandler.republishAppsFocus(ctx, items); err != nil {
			for _, n := range pubNames {
				results = append(results, errResult(n, err))
			}
		} else {
			for _, n := range pubNames {
				u := pending[n]
				rh.appHandler.unpinClearRestoreFlag(ctx, u.app, req.TargetEnv, u.restore)
				if u.restore != "" {
					results = append(results, okResult(n, "unpinned; restored "+u.restore))
				} else {
					results = append(results, okResult(n, "unpinned; normal delivery resumed"))
				}
			}
		}
	}
	return stackBatchResponse{Project: project, Stack: name, Action: "unpin", Results: results}
}

// stackSuspendRequest suspends/resumes a stack's env across member apps. Apps
// optionally narrows to a subset (default: all members).
type stackSuspendRequest struct {
	TargetEnv string   `json:"targetEnv"`
	Apps      []string `json:"apps,omitempty"`
}

// handleSuspendStack suspends a stack's env across member apps; handleResumeStack
// resumes it. Both fan out over the (optionally subset-narrowed) members; a
// member not deployed to targetEnv is skipped.
// stackTargetClustersRequest sets which of a stable env's clusters the stack's
// members deploy to, across member apps. clusters is ["*"] (all env clusters),
// an explicit subset, or [] to clear (each member inherits the env DeployMode
// default). Same env for every member, so validation runs once. Apps optionally
// narrows to a subset (default: all). Members not deployed to targetEnv are
// skipped, not failed.
type stackTargetClustersRequest struct {
	TargetEnv string   `json:"targetEnv"`
	Clusters  []string `json:"clusters"`
	Apps      []string `json:"apps,omitempty"`
}

// handleSetStackTargetClusters sets the per-env target clusters across a stack's
// member apps in ONE batched publish. Mirrors handlePinStack (full-tree focus
// republish, since changing targets rewrites each app's _targets/ files and its
// Kargo Stage argocd-update list).
func (rh *rbacHandler) handleSetStackTargetClusters(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	name := r.PathValue("stack")
	if rh.appHandler == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "app store not configured"})
		return
	}
	var req stackTargetClustersRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}
	req.TargetEnv = strings.TrimSpace(req.TargetEnv)
	if req.TargetEnv == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "targetEnv is required"})
		return
	}
	if _, err := rh.stackStore.GetStack(r.Context(), project, name); err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "stack not found: " + name})
		return
	}
	members, err := selectStackMembers(rh.stackMemberApps(r.Context(), project, name), req.Apps)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	if !rh.validTargetEnv(r.Context(), members, req.TargetEnv) {
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: "unknown target environment: " + req.TargetEnv})
		return
	}
	// Same env for every member — validate the cluster selection once.
	if err := rh.appHandler.validateTargetClusters(r.Context(), map[string][]string{req.TargetEnv: req.Clusters}); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: err.Error()})
		return
	}
	// Phase 1 (concurrent): mutate each member's spec. No git yet.
	type targetPrep struct {
		app       *domain.App
		targetEnv *domain.AppEnvironment
		err       error
	}
	preps := prepMembers(members, func(a *domain.App) targetPrep {
		app, targetEnv, err := rh.appHandler.setAppEnvTargetClustersSpec(r.Context(), project, a.Name, req.TargetEnv, req.Clusters)
		return targetPrep{app: app, targetEnv: targetEnv, err: err}
	})
	results := make([]stackOpResult, 0, len(members))
	var items []appFocusPublish
	var pubNames []string
	for i, a := range members {
		p := preps[i]
		switch {
		case p.err == nil:
			items = append(items, appFocusPublish{app: p.app, focusEnv: p.targetEnv})
			pubNames = append(pubNames, a.Name)
		case targetClustersIsSkippable(p.err):
			results = append(results, skipResult(a.Name, p.err.Error()))
		default:
			results = append(results, errResult(a.Name, p.err))
		}
	}
	// Phase 2: publish every mutated member's full tree + target env in ONE
	// clone/commit/push.
	msg := req.TargetEnv + " → " + targetClustersLabel(req.Clusters)
	if len(items) > 0 {
		if err := rh.appHandler.republishAppsFocus(r.Context(), items); err != nil {
			for _, n := range pubNames {
				results = append(results, errResult(n, err))
			}
		} else {
			for _, n := range pubNames {
				results = append(results, okResult(n, msg))
			}
		}
	}
	writeJSON(w, http.StatusOK, stackBatchResponse{Project: project, Stack: name, Action: "target-clusters", Results: results})
}

// targetClustersLabel renders a human summary of a target-clusters selection.
func targetClustersLabel(clusters []string) string {
	if len(clusters) == 0 {
		return "env default"
	}
	if len(clusters) == 1 && clusters[0] == domain.AllClustersSentinel {
		return "all clusters"
	}
	return strings.Join(clusters, ", ")
}

func (rh *rbacHandler) handleSuspendStack(w http.ResponseWriter, r *http.Request) {
	rh.serveStackSuspend(w, r, true)
}

func (rh *rbacHandler) handleResumeStack(w http.ResponseWriter, r *http.Request) {
	rh.serveStackSuspend(w, r, false)
}

func (rh *rbacHandler) serveStackSuspend(w http.ResponseWriter, r *http.Request, suspend bool) {
	project := r.PathValue("project")
	name := r.PathValue("stack")
	if rh.appHandler == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "app store not configured"})
		return
	}
	var req stackSuspendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}
	req.TargetEnv = strings.TrimSpace(req.TargetEnv)
	if req.TargetEnv == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "targetEnv is required"})
		return
	}
	if _, err := rh.stackStore.GetStack(r.Context(), project, name); err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "stack not found: " + name})
		return
	}
	members, err := selectStackMembers(rh.stackMemberApps(r.Context(), project, name), req.Apps)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	if !rh.validTargetEnv(r.Context(), members, req.TargetEnv) {
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: "unknown target environment: " + req.TargetEnv})
		return
	}
	action := "resume"
	verb := "resumed"
	if suspend {
		action, verb = "suspend", "suspended"
	}
	// Phase 1: mutate each member's spec (no git), collecting the publish
	// targets. Skips/errors are recorded now; the git work is batched below.
	results := make([]stackOpResult, 0, len(members))
	var targets []AppEnvTarget
	var pubNames []string
	for _, a := range members {
		app, env, err := rh.appHandler.suspendAppEnvSpec(r.Context(), project, a.Name, req.TargetEnv, suspend)
		switch {
		case err == nil:
			targets = append(targets, AppEnvTarget{App: app, Env: env})
			pubNames = append(pubNames, a.Name)
		case suspendIsSkippable(err):
			results = append(results, skipResult(a.Name, err.Error()))
		default:
			results = append(results, errResult(a.Name, err))
		}
	}
	// Phase 2: publish every mutated member's target env in ONE clone/commit/push
	// (batched) — the fix for the 504 on large stacks. All-or-nothing per commit.
	if len(targets) > 0 {
		if err := rh.appHandler.republishAppsEnv(r.Context(), targets); err != nil {
			for _, n := range pubNames {
				results = append(results, errResult(n, err))
			}
		} else {
			for _, n := range pubNames {
				results = append(results, okResult(n, req.TargetEnv+" "+verb))
			}
		}
	}
	writeJSON(w, http.StatusOK, stackBatchResponse{Project: project, Stack: name, Action: action, Results: results})
}

// stackPreviewNamespace is the shared namespace a stack preview deploys into.
//
// It resolves the project's PreviewNamespacePattern with the stack name in the
// {app} slot, so a stack preview honours the same project setting an app preview
// does. An empty pattern falls back to domain.DefaultPreviewNamespacePattern
// ("{project}-{app}-preview-{name}"), which reproduces the historical
// "{project}-{stack}-preview-{name}" namespace exactly.
//
// Every member of the stack preview shares this one namespace so the previewed
// collection reaches itself by in-cluster DNS. When the pattern omits {name} the
// namespace is shared across previews too — see handleCreateStackPreview.
func stackPreviewNamespace(project, stack, preview, pattern string) string {
	return domain.GeneratePreviewNamespaceFromPattern(stack, preview, project, pattern)
}

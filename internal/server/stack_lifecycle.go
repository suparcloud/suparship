package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

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
// namespace ({project}-{stack}-preview-{name}) so the previewed collection is
// co-located and reaches itself by in-cluster DNS. It is an upsert (like the
// per-app preview) so CI can re-point every member at a freshly built image on
// each PR push in one call. Members with previews disabled are skipped (not
// failed) so a mixed stack previews cleanly.
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

	ns := stackPreviewNamespace(project, name, preview)
	imageTag := strings.TrimSpace(req.ImageTag)
	results := make([]stackOpResult, 0, len(members))
	for _, a := range members {
		if !a.Spec.PreviewsEnabled {
			results = append(results, skipResult(a.Name, "previews disabled"))
			continue
		}
		if err := rh.appHandler.createStackPreview(r.Context(), a, preview, ns, baseEnv, imageTag); err != nil {
			results = append(results, errResult(a.Name, err))
			continue
		}
		results = append(results, okResult(a.Name, "preview "+preview+" → "+ns))
	}
	writeJSON(w, http.StatusCreated, stackBatchResponse{Project: project, Stack: name, Action: "preview", Results: results})
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
	results := make([]stackOpResult, 0, len(members))
	for _, a := range members {
		env, err := rh.appHandler.appStore.GetAppEnvironment(r.Context(), project, a.Name, preview)
		if err != nil || env.EnvType != domain.AppEnvPreview {
			continue // app has no such preview — skip silently
		}
		// baseEnv locates the preview's gitops tree (previews/{baseEnv}/...).
		// Fall back to the app's first stable env for records created before
		// BaseEnv was persisted, so the prune still targets a real path.
		baseEnv := env.BaseEnv
		if baseEnv == "" {
			if stable := rh.appHandler.stableEnvsFromOrg(r.Context(), a); len(stable) > 0 {
				baseEnv = stable[0].EnvName
			}
		}
		// Prune gitops first (so ArgoCD prunes the Application), then drop the
		// store record — mirrors the per-app preview delete ordering.
		if d, ok := rh.appHandler.gitOpsPublisher.(AppPreviewDeleter); ok {
			if err := d.DeleteAppPreview(r.Context(), project, preview, a.Name, baseEnv); err != nil {
				results = append(results, errResult(a.Name, err))
				continue
			}
		}
		if err := rh.appHandler.appStore.DeleteAppEnvironment(r.Context(), project, a.Name, preview); err != nil {
			results = append(results, errResult(a.Name, err))
			continue
		}
		results = append(results, okResult(a.Name, "preview "+preview+" deleted"))
	}
	writeJSON(w, http.StatusOK, stackBatchResponse{Project: project, Stack: name, Action: "preview-delete", Results: results})
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
	results := make([]stackOpResult, 0, len(members))
	for _, a := range members {
		tag, err := rh.appHandler.pinAppEnv(r.Context(), project, a.Name, req.TargetEnv, req.FromPreview)
		switch {
		case err == nil:
			results = append(results, okResult(a.Name, "pinned "+req.TargetEnv+" → "+tag))
		case pinIsSkippable(err):
			results = append(results, skipResult(a.Name, err.Error()))
		default:
			results = append(results, errResult(a.Name, err))
		}
	}
	writeJSON(w, http.StatusOK, stackBatchResponse{Project: project, Stack: name, Action: "pin", Results: results})
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
	results := make([]stackOpResult, 0, len(members))
	for _, a := range members {
		restore, wasPinned, err := rh.appHandler.unpinAppEnv(r.Context(), project, a.Name, req.TargetEnv)
		switch {
		case err != nil:
			results = append(results, errResult(a.Name, err))
		case !wasPinned:
			results = append(results, skipResult(a.Name, "not pinned"))
		case restore != "":
			results = append(results, okResult(a.Name, "unpinned; restored "+restore))
		default:
			results = append(results, okResult(a.Name, "unpinned; normal delivery resumed"))
		}
	}
	writeJSON(w, http.StatusOK, stackBatchResponse{Project: project, Stack: name, Action: "unpin", Results: results})
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
	results := make([]stackOpResult, 0, len(members))
	for _, a := range members {
		err := rh.appHandler.suspendAppEnv(r.Context(), project, a.Name, req.TargetEnv, suspend)
		switch {
		case err == nil:
			results = append(results, okResult(a.Name, req.TargetEnv+" "+verb))
		case suspendIsSkippable(err):
			results = append(results, skipResult(a.Name, err.Error()))
		default:
			results = append(results, errResult(a.Name, err))
		}
	}
	writeJSON(w, http.StatusOK, stackBatchResponse{Project: project, Stack: name, Action: action, Results: results})
}

// stackPreviewNamespace is the shared namespace a stack preview deploys into.
func stackPreviewNamespace(project, stack, preview string) string {
	return project + "-" + stack + "-preview-" + preview
}

// createStackPreview upserts a preview for one member into the shared stack
// preview namespace, so every member co-locates and reaches itself by in-cluster
// DNS. It delegates to the shared upsertAppPreview core with a namespace override
// (baseDomain from the base env, no per-app namespace pattern).
func (ah *appHandler) createStackPreview(ctx context.Context, a *domain.App, preview, namespace, baseEnv, imageTag string) error {
	_, _, err := ah.upsertAppPreview(ctx, a, preview, baseEnv, imageTag,
		ah.baseDomainForEnv(ctx, baseEnv), "", namespace)
	return err
}

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	domainapp "github.com/suparcloud/suparship/internal/app"
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

// stackOpResult is one app's outcome in a batch operation.
type stackOpResult struct {
	App     string `json:"app"`
	OK      bool   `json:"ok"`
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

func okResult(app, msg string) stackOpResult    { return stackOpResult{App: app, OK: true, Message: msg} }
func errResult(app string, err error) stackOpResult {
	return stackOpResult{App: app, OK: false, Error: err.Error()}
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

// handleCreateStackPreview brings up a preview of the whole stack in one shared
// namespace ({project}-{stack}-preview-{name}) so the previewed collection is
// co-located and reaches itself by in-cluster DNS.
func (rh *rbacHandler) handleCreateStackPreview(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	name := r.PathValue("stack")
	if rh.appHandler == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "app store not configured"})
		return
	}
	var req CreateAppPreviewRequest
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
	ns := stackPreviewNamespace(project, name, preview)
	members := rh.stackMemberApps(r.Context(), project, name)
	results := make([]stackOpResult, 0, len(members))
	for _, a := range members {
		if err := rh.appHandler.createStackPreview(r.Context(), a, preview, ns); err != nil {
			results = append(results, errResult(a.Name, err))
			continue
		}
		results = append(results, okResult(a.Name, "preview "+preview+" → "+ns))
	}
	writeJSON(w, http.StatusCreated, stackBatchResponse{Project: project, Stack: name, Action: "preview", Results: results})
}

// handleDeleteStackPreview tears down a stack preview by removing each member's
// preview environment. Mirrors the per-app preview delete (the gitops files are
// pruned on the members' next publish).
func (rh *rbacHandler) handleDeleteStackPreview(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	name := r.PathValue("stack")
	preview := r.PathValue("name")
	if rh.appHandler == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "app store not configured"})
		return
	}
	members := rh.stackMemberApps(r.Context(), project, name)
	results := make([]stackOpResult, 0, len(members))
	for _, a := range members {
		env, err := rh.appHandler.appStore.GetAppEnvironment(r.Context(), project, a.Name, preview)
		if err != nil || env.EnvType != domain.AppEnvPreview {
			continue // app has no such preview — skip silently
		}
		if err := rh.appHandler.appStore.DeleteAppEnvironment(r.Context(), project, a.Name, preview); err != nil {
			results = append(results, errResult(a.Name, err))
			continue
		}
		_ = rh.appHandler.republishApp(r.Context(), a)
		results = append(results, okResult(a.Name, "preview "+preview+" deleted"))
	}
	// The shared preview namespace is pruned by ArgoCD when its Applications are
	// removed (mirrors per-app preview delete, which also leaves namespace cleanup
	// to ArgoCD). Full reclaim happens on stack delete via deleteOwnedStackNamespaces.
	writeJSON(w, http.StatusOK, stackBatchResponse{Project: project, Stack: name, Action: "preview-delete", Results: results})
}

// stackPreviewNamespace is the shared namespace a stack preview deploys into.
func stackPreviewNamespace(project, stack, preview string) string {
	return project + "-" + stack + "-preview-" + preview
}

// createStackPreview creates a preview environment for one app and publishes it
// into the shared stack preview namespace. Mirrors handleCreateAppPreview but
// overrides the namespace so every member co-locates (and reaches itself by
// in-cluster DNS). Namespace creation is left to ArgoCD's CreateNamespace — as
// with per-app previews — because a preview env name does not map to an org
// environment, so its workload cluster can't be resolved reliably here.
func (ah *appHandler) createStackPreview(ctx context.Context, a *domain.App, preview, namespace string) error {
	if _, err := ah.appStore.GetAppEnvironment(ctx, a.ProjectName, a.Name, preview); err == nil {
		return fmt.Errorf("preview %q already exists", preview)
	}
	res, err := domainapp.CreatePreview(domainapp.PreviewRequest{App: a, PreviewName: preview})
	if err != nil {
		return err
	}
	inst := res.Instance
	env := &domain.AppEnvironment{
		AppName:     inst.AppName,
		ProjectName: inst.ProjectName,
		EnvName:     inst.EnvName,
		EnvType:     inst.EnvType,
		Namespace:   namespace, // override: co-locate the whole stack preview
		URLs:        []string{inst.URL},
		Status:      inst.Status,
	}
	if err := ah.appStore.SaveAppEnvironment(ctx, a.ProjectName, env); err != nil {
		return err
	}
	if ah.gitOpsPublisher != nil {
		return ah.gitOpsPublisher.PublishAppEnv(ctx, a, env)
	}
	return nil
}

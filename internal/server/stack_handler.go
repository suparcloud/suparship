package server

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/envconfig"
)

// StackDTO is the wire shape of a stack: its metadata + shared overrides +
// the names of its member apps. Secrets are managed via the stack secret routes,
// not here.
type StackDTO struct {
	Name             string                    `json:"name"`
	Project          string                    `json:"project"`
	DisplayName      string                    `json:"displayName,omitempty"`
	Description      string                    `json:"description,omitempty"`
	SharedNamespace  bool                      `json:"sharedNamespace,omitempty"`
	NamespacePattern string                    `json:"namespacePattern,omitempty"`
	RawValues        map[string]any            `json:"rawValues,omitempty"`
	EnvRawValues     map[string]map[string]any `json:"envRawValues,omitempty"`
	EnvConfig        EnvConfigDTO              `json:"envConfig,omitempty"`
	// Apps are the member app names (apps whose Spec.Stack == this stack).
	Apps []string `json:"apps"`
}

// createStackRequest / patchStackRequest carry stack metadata + overrides.
// PATCH fields are pointers so only provided ones are applied.
type createStackRequest struct {
	Name             string                    `json:"name"`
	DisplayName      string                    `json:"displayName,omitempty"`
	Description      string                    `json:"description,omitempty"`
	SharedNamespace  bool                      `json:"sharedNamespace,omitempty"`
	NamespacePattern string                    `json:"namespacePattern,omitempty"`
	RawValues        map[string]any            `json:"rawValues,omitempty"`
	EnvRawValues     map[string]map[string]any `json:"envRawValues,omitempty"`
	EnvConfig        *EnvConfigDTO             `json:"envConfig,omitempty"`
}

type patchStackRequest struct {
	DisplayName      *string                   `json:"displayName,omitempty"`
	Description      *string                   `json:"description,omitempty"`
	SharedNamespace  *bool                     `json:"sharedNamespace,omitempty"`
	NamespacePattern *string                   `json:"namespacePattern,omitempty"`
	RawValues        map[string]any            `json:"rawValues,omitempty"`
	EnvRawValues     map[string]map[string]any `json:"envRawValues,omitempty"`
	EnvConfig        *EnvConfigDTO             `json:"envConfig,omitempty"`
}

type setAppStackRequest struct {
	// Stack is the target stack name; empty removes the app from any stack.
	Stack string `json:"stack"`
}

func stackToDTO(s *domain.Stack, appNames []string) StackDTO {
	if appNames == nil {
		appNames = []string{} // marshal as [] not null so the UI can map over it
	}
	sort.Strings(appNames)
	return StackDTO{
		Name:             s.Name,
		Project:          s.ProjectName,
		DisplayName:      s.Spec.DisplayName,
		Description:      s.Spec.Description,
		SharedNamespace:  s.Spec.SharedNamespace,
		NamespacePattern: s.Spec.NamespacePattern,
		RawValues:        s.Spec.RawValues,
		EnvRawValues:     s.Spec.EnvRawValues,
		EnvConfig:        toEnvConfigDTO(s.Spec.EnvConfig),
		Apps:             appNames,
	}
}

// stackMemberNames returns the names of apps whose Spec.Stack == stackName.
func (rh *rbacHandler) stackMemberNames(ctx context.Context, project, stackName string) []string {
	var names []string
	if rh.appHandler == nil {
		return names
	}
	apps, _ := rh.appHandler.appStore.ListApps(ctx, project)
	for _, a := range apps {
		if a.Spec.Stack == stackName {
			names = append(names, a.Name)
		}
	}
	return names
}

func (rh *rbacHandler) handleListStacks(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	stacks, err := rh.stackStore.ListStacks(r.Context(), project)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "project not found: " + project})
		return
	}
	dtos := make([]StackDTO, 0, len(stacks))
	for _, s := range stacks {
		dtos = append(dtos, stackToDTO(s, rh.stackMemberNames(r.Context(), project, s.Name)))
	}
	sort.Slice(dtos, func(i, j int) bool { return dtos[i].Name < dtos[j].Name })
	writeJSON(w, http.StatusOK, map[string]any{"project": project, "stacks": dtos})
}

func (rh *rbacHandler) handleGetStack(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	name := r.PathValue("stack")
	s, err := rh.stackStore.GetStack(r.Context(), project, name)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "stack not found: " + name})
		return
	}
	writeJSON(w, http.StatusOK, stackToDTO(s, rh.stackMemberNames(r.Context(), project, name)))
}

func (rh *rbacHandler) handleCreateStack(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	var req createStackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}
	if err := domain.ValidateAppName(req.Name); err != nil { // same DNS-label rule as apps
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: "invalid stack name: " + err.Error()})
		return
	}
	if _, err := rh.stackStore.GetStack(r.Context(), project, req.Name); err == nil {
		writeJSON(w, http.StatusConflict, errorResponse{Error: "stack \"" + req.Name + "\" already exists in project \"" + project + "\""})
		return
	}
	ec := envconfig.EnvConfig{}
	if req.EnvConfig != nil {
		ec = fromEnvConfigDTO(*req.EnvConfig)
	}
	stack := &domain.Stack{
		Name:        req.Name,
		ProjectName: project,
		Spec: domain.StackSpec{
			DisplayName:      req.DisplayName,
			Description:      req.Description,
			SharedNamespace:  req.SharedNamespace,
			NamespacePattern: req.NamespacePattern,
			RawValues:        req.RawValues,
			EnvRawValues:     req.EnvRawValues,
			EnvConfig:        ec,
		},
	}
	if err := rh.stackStore.SaveStack(r.Context(), stack); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to save stack"})
		return
	}
	writeJSON(w, http.StatusCreated, stackToDTO(stack, nil))
}

func (rh *rbacHandler) handlePatchStack(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	name := r.PathValue("stack")
	s, err := rh.stackStore.GetStack(r.Context(), project, name)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "stack not found: " + name})
		return
	}
	var req patchStackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}
	if req.DisplayName != nil {
		s.Spec.DisplayName = *req.DisplayName
	}
	if req.Description != nil {
		s.Spec.Description = *req.Description
	}
	if req.SharedNamespace != nil {
		s.Spec.SharedNamespace = *req.SharedNamespace
	}
	if req.NamespacePattern != nil {
		s.Spec.NamespacePattern = *req.NamespacePattern
	}
	if req.RawValues != nil {
		s.Spec.RawValues = req.RawValues
	}
	if req.EnvRawValues != nil {
		s.Spec.EnvRawValues = req.EnvRawValues
	}
	if req.EnvConfig != nil {
		s.Spec.EnvConfig = fromEnvConfigDTO(*req.EnvConfig)
	}
	if err := rh.stackStore.SaveStack(r.Context(), s); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to save stack"})
		return
	}
	// Override changes cascade to members on their next publish — republish now
	// so the new stack config takes effect without waiting for an unrelated edit.
	rh.republishStackMembers(r.Context(), project, name)
	writeJSON(w, http.StatusOK, stackToDTO(s, rh.stackMemberNames(r.Context(), project, name)))
}

// handleDeleteStack removes the stack record. By default its member apps are
// detached (Spec.Stack cleared + republished as loose project apps) and kept.
// With ?deleteApps=true it instead deletes every member app and reclaims the
// stack's shared namespaces — a full teardown of the collection.
func (rh *rbacHandler) handleDeleteStack(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	name := r.PathValue("stack")
	if _, err := rh.stackStore.GetStack(r.Context(), project, name); err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "stack not found: " + name})
		return
	}

	if r.URL.Query().Get("deleteApps") == "true" {
		results := rh.deleteStackApps(r.Context(), project, name)
		if err := rh.stackStore.DeleteStack(r.Context(), project, name); err != nil {
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to delete stack"})
			return
		}
		// Reclaim the stack's owned shared namespaces after its apps are pruned.
		if rh.appHandler != nil {
			rh.appHandler.deleteOwnedStackNamespaces(r.Context(), project, name)
		}
		writeJSON(w, http.StatusOK, stackBatchResponse{Project: project, Stack: name, Action: "delete", Results: results})
		return
	}

	// Default: detach members, keep the apps.
	if rh.appHandler != nil {
		apps, _ := rh.appHandler.appStore.ListApps(r.Context(), project)
		for _, a := range apps {
			if a.Spec.Stack != name {
				continue
			}
			a.Spec.Stack = ""
			if err := rh.appHandler.appStore.SaveApp(r.Context(), project, a); err != nil {
				continue
			}
			// relocateApp moves the app out of a shared stack namespace back to
			// its own, reclaiming the (now app-exclusive) old namespace.
			_ = rh.appHandler.relocateApp(r.Context(), a)
		}
	}
	if err := rh.stackStore.DeleteStack(r.Context(), project, name); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to delete stack"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// deleteStackApps deletes every member app of a stack (store + gitops) and
// reclaims each app's previous namespaces. Best-effort, returning a per-app
// summary. The shared stack namespace is reclaimed by the caller afterwards.
func (rh *rbacHandler) deleteStackApps(ctx context.Context, project, name string) []stackOpResult {
	members := rh.stackMemberApps(ctx, project, name)
	results := make([]stackOpResult, 0, len(members))
	for _, a := range members {
		envs, _ := rh.appHandler.appStore.ListAppEnvironments(ctx, project, a.Name)
		if err := rh.appHandler.appStore.DeleteApp(ctx, project, a.Name); err != nil {
			results = append(results, errResult(a.Name, err))
			continue
		}
		if rh.appHandler.gitOpsPublisher != nil {
			_ = rh.appHandler.gitOpsPublisher.UnpublishApp(ctx, project, a.Name)
		}
		// Reclaim app-exclusive namespaces; the shared stack namespace is left
		// for deleteOwnedStackNamespaces (it carries the stack ownership label).
		for _, e := range envs {
			rh.appHandler.reclaimAppExclusiveNamespace(ctx, project, e.EnvName, e.Namespace)
		}
		results = append(results, okResult(a.Name, "deleted"))
	}
	return results
}

// handleSetAppStack adds an app to a stack (or removes it when stack is empty),
// then republishes the app so the stack's override layer takes effect.
func (rh *rbacHandler) handleSetAppStack(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	appName := r.PathValue("app")
	var req setAppStackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}
	if rh.appHandler == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "app store not configured"})
		return
	}
	app, err := rh.appHandler.appStore.GetApp(r.Context(), project, appName)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "app not found: " + appName})
		return
	}
	if req.Stack != "" {
		if _, err := rh.stackStore.GetStack(r.Context(), project, req.Stack); err != nil {
			writeJSON(w, http.StatusNotFound, errorResponse{Error: "stack not found: " + req.Stack})
			return
		}
	}
	app.Spec.Stack = req.Stack
	if err := rh.appHandler.appStore.SaveApp(r.Context(), project, app); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to update app"})
		return
	}
	// Membership may relocate the app (joining/leaving a shared-namespace stack)
	// and changes its override layer — relocateApp handles namespace recompute,
	// republish, and old-namespace reclaim.
	if err := rh.appHandler.relocateApp(r.Context(), app); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "stack membership saved but publish failed: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"app": appName, "stack": req.Stack})
}

func (rh *rbacHandler) republishStackMembers(ctx context.Context, project, stackName string) {
	if rh.appHandler == nil {
		return
	}
	apps, _ := rh.appHandler.appStore.ListApps(ctx, project)
	for _, a := range apps {
		if a.Spec.Stack == stackName {
			// relocateApp (not republishApp) so toggling SharedNamespace
			// actually moves members to/from the shared namespace.
			_ = rh.appHandler.relocateApp(ctx, a)
		}
	}
}

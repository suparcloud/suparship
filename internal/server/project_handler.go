package server

// project_handler.go — project CRUD handlers
//
// Endpoints:
//
//	POST /api/v1/projects         — create a new project (org_admin only)
//	DELETE /api/v1/projects/{p}   — delete a project    (org_admin only)
//
// List and detail are served by org.go (handleGetProjects / handleGetProject).

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"github.com/suparcloud/suparship/internal/project"
	"github.com/suparcloud/suparship/internal/rbac"
)

// projectNameRE allows lowercase letters, digits, and hyphens; must start with
// a letter; max 48 characters (fits safely in Kubernetes label values and
// namespace names after adding environment suffixes).
var projectNameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,47}$`)

// ── POST /api/v1/projects ─────────────────────────────────────────────────────

type createProjectRequest struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName,omitempty"`
	Description string `json:"description,omitempty"`
}

func (rh *rbacHandler) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	if rh.projectStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "project store not configured"})
		return
	}

	var req createProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}

	if req.Name == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "name is required"})
		return
	}
	if !projectNameRE.MatchString(req.Name) {
		writeJSON(w, http.StatusBadRequest, errorResponse{
			Error: "name must start with a lowercase letter and contain only lowercase letters, digits, or hyphens (max 48 chars)",
		})
		return
	}

	// Reject duplicates.
	if _, err := rh.projectStore.Get(r.Context(), req.Name); err == nil {
		writeJSON(w, http.StatusConflict, errorResponse{Error: "project already exists"})
		return
	}

	p := &project.Project{
		APIVersion: project.CurrentAPIVersion,
		Kind:       project.ProjectKind,
		Metadata:   project.ProjectMeta{Name: req.Name},
		Spec: project.ProjectSpec{
			DisplayName: req.DisplayName,
			Description: req.Description,
			// Environments intentionally empty — inherited from org defaults.
			Environments: []project.Environment{},
			Services:     []project.Service{},
		},
	}

	if err := rh.projectStore.Save(r.Context(), p); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to create project: " + err.Error()})
		return
	}

	writeJSON(w, http.StatusCreated, ProjectDTO{
		Name:        p.Metadata.Name,
		DisplayName: p.Spec.DisplayName,
		Description: p.Spec.Description,
	})
}

// ── DELETE /api/v1/projects/{project} ────────────────────────────────────────

func (rh *rbacHandler) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	if rh.projectStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "project store not configured"})
		return
	}

	projectName := r.PathValue("project")

	if err := rh.projectStore.Delete(r.Context(), projectName); err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "project not found"})
		return
	}

	// Also remove any project-scoped role bindings from the org config so the
	// deleted project doesn't linger in the RBAC list.
	org, err := rh.orgStore.GetOrg(r.Context())
	if err == nil {
		bindings := org.RoleBindings[:0]
		for _, rb := range org.RoleBindings {
			if rb.Project != projectName {
				bindings = append(bindings, rb)
			}
		}
		org.RoleBindings = bindings
		_ = rh.orgStore.SaveOrg(r.Context(), org) // best-effort; project is already deleted
	}

	// Best-effort: remove the project's GitOps files so they don't dangle in
	// the repo — ArgoCD then prunes the corresponding live resources. Runs in
	// two phases to avoid an ordering race: pruning a generated Application
	// cascade-deletes its resources via a finalizer that must resolve the
	// AppProject — removing both in one commit leaves Applications stuck in
	// Terminating ("appproject not found"). So: (1) remove the app sources,
	// (2) wait for the project's Applications to disappear, (3) remove the
	// AppProject. On timeout the AppProject is kept — a dangling AppProject
	// is benign, stuck Applications are not.
	if rh.appHandler != nil && rh.appHandler.gitOpsPublisher != nil {
		pub := rh.appHandler.gitOpsPublisher
		counter := rh.projectAppCounter
		ah := rh.appHandler
		// afterPrune runs only once the project's workloads are gone (phase 2
		// complete), so reclaiming namespaces can't orphan running pods. Only
		// namespaces suparship created (ownership-labelled) are removed; adopted
		// ones are left in place.
		afterPrune := func() { ah.deleteOwnedProjectNamespaces(context.Background(), projectName) }
		go unpublishProjectTwoPhase(context.Background(), pub, counter, projectName, afterPrune)
	}

	w.WriteHeader(http.StatusNoContent)
}

// Two-phase unpublish tuning. Vars (not consts) so tests can shorten them.
var (
	// unpublishPollInterval is how often phase 2 checks whether the project's
	// generated Applications are gone.
	unpublishPollInterval = 10 * time.Second
	// unpublishPruneTimeout bounds the phase-2 wait. ApplicationSet prune +
	// cascade delete is usually done within a couple of sync cycles.
	unpublishPruneTimeout = 10 * time.Minute
	// unpublishGraceDelay is the fallback wait when no ProjectAppCounter is
	// wired (no ArgoCD reader) — long enough for a typical prune cycle.
	unpublishGraceDelay = 90 * time.Second
)

// unpublishProjectTwoPhase removes a deleted project's gitops files in two
// commits: app sources first, then — once ArgoCD has pruned the project's
// generated Applications (or after a grace delay when no counter is wired) —
// the AppProject. All best-effort; failures are logged, never retried here.
// afterPrune, when non-nil, runs after phase 2 completes (the project's
// AppProject and all its Applications/workloads are pruned) — the safe point to
// reclaim owned namespaces.
func unpublishProjectTwoPhase(ctx context.Context, pub GitOpsPublisher, counter ProjectAppCounter, projectName string, afterPrune func()) {
	if err := pub.UnpublishProjectApps(ctx, projectName); err != nil {
		slog.Warn("project delete: gitops app cleanup failed; AppProject kept",
			"project", projectName, "error", err)
		return
	}

	if counter != nil {
		deadline := time.Now().Add(unpublishPruneTimeout)
		for {
			n, err := counter.CountProjectApplications(ctx, projectName)
			if err == nil && n == 0 {
				break
			}
			if err != nil {
				slog.Debug("project delete: app count failed; will retry", "project", projectName, "error", err)
			}
			if time.Now().After(deadline) {
				slog.Warn("project delete: applications still present after wait — keeping AppProject so they can finish deleting; remove _infra/{project}-appproject.yaml manually once they are gone",
					"project", projectName, "remaining", n)
				return
			}
			time.Sleep(unpublishPollInterval)
		}
	} else {
		// No ArgoCD reader available — give the ApplicationSets a prune cycle.
		time.Sleep(unpublishGraceDelay)
	}

	if err := pub.UnpublishProjectInfra(ctx, projectName); err != nil {
		slog.Warn("project delete: gitops appproject cleanup failed", "project", projectName, "error", err)
		return
	}
	slog.Info("project delete: gitops cleanup complete", "project", projectName)

	// Workloads are gone — safe to reclaim the namespaces suparship created.
	if afterPrune != nil {
		afterPrune()
	}
}

// ── requireOrgAdminForProject ─────────────────────────────────────────────────
// Middleware used by project-create and project-delete which are org-admin ops.

func (rh *rbacHandler) orgAdminOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess := sessionFromContext(r.Context())
		org, err := rh.orgStore.GetOrg(r.Context())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to load org"})
			return
		}
		if !org.HasPermissionForIdentity(sess.Username, sess.Groups, "*", rbac.RoleOrgAdmin) {
			writeJSON(w, http.StatusForbidden, errorResponse{Error: "org_admin role required"})
			return
		}
		next(w, r)
	}
}

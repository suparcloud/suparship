package server

// org_env_handler.go — org-level environment CRUD (canonical pipeline definition)
//
// Endpoints (all require authentication; writes require org_admin role):
//
//	GET    /api/v1/org/environments
//	POST   /api/v1/org/environments
//	PUT    /api/v1/org/environments/{env}
//	DELETE /api/v1/org/environments/{env}
//
// The org environments define the canonical deployment pipeline shared by all
// projects. Projects may store per-environment overrides via the project
// environment endpoints (/api/v1/projects/{project}/environments).

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sort"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/project"
	"github.com/suparcloud/suparship/internal/rbac"
)

// ── GET /api/v1/org/environments ─────────────────────────────────────────────

// OrgEnvironmentDTO is the JSON representation of an org-level environment.
type OrgEnvironmentDTO struct {
	Name             string `json:"name"`
	DisplayName      string `json:"displayName,omitempty"`
	Order            int    `json:"order"`
	ClusterRef       string `json:"clusterRef,omitempty"`
	BaseDomain       string `json:"baseDomain,omitempty"`
	NamespacePattern string `json:"namespacePattern,omitempty"`
}

func orgEnvToDTO(e rbac.OrgEnvironment) OrgEnvironmentDTO {
	return OrgEnvironmentDTO{
		Name:             e.Name,
		DisplayName:      e.DisplayName,
		Order:            e.Order,
		ClusterRef:       e.ClusterRef,
		BaseDomain:       e.BaseDomain,
		NamespacePattern: e.NamespacePattern,
	}
}

func (rh *rbacHandler) handleListOrgEnvironments(w http.ResponseWriter, r *http.Request) {
	org, err := rh.orgStore.GetOrg(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to load org"})
		return
	}
	dtos := make([]OrgEnvironmentDTO, len(org.Environments))
	for i, e := range org.Environments {
		dtos[i] = orgEnvToDTO(e)
	}
	writeJSON(w, http.StatusOK, map[string]any{"environments": dtos})
}

// ── POST /api/v1/org/environments ────────────────────────────────────────────

type upsertOrgEnvRequest struct {
	Name             string  `json:"name"`
	DisplayName      string  `json:"displayName,omitempty"`
	Order            int     `json:"order"`
	ClusterRef       string  `json:"clusterRef,omitempty"`
	BaseDomain       string  `json:"baseDomain,omitempty"`
	// NamespacePattern when present (including empty string) replaces the stored
	// per-environment namespace pattern. Use "" to clear an existing override
	// and fall back to the org-wide ResourceNaming.AppNamespace default.
	NamespacePattern *string `json:"namespacePattern,omitempty"`
}

func (rh *rbacHandler) handleCreateOrgEnvironment(w http.ResponseWriter, r *http.Request) {
	var req upsertOrgEnvRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}
	if req.Name == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "name is required"})
		return
	}

	org, err := rh.orgStore.GetOrg(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to load org"})
		return
	}

	for _, e := range org.Environments {
		if e.Name == req.Name {
			writeJSON(w, http.StatusConflict, errorResponse{Error: "environment already exists"})
			return
		}
	}

	order := req.Order
	if order == 0 {
		order = len(org.Environments) + 1
	}

	newEnv := rbac.OrgEnvironment{
		Name:        req.Name,
		DisplayName: req.DisplayName,
		Order:       order,
		ClusterRef:  req.ClusterRef,
		BaseDomain:  req.BaseDomain,
	}
	if req.NamespacePattern != nil {
		newEnv.NamespacePattern = *req.NamespacePattern
	}
	org.Environments = append(org.Environments, newEnv)
	sortOrgEnvs(org.Environments)

	if err := rh.orgStore.SaveOrg(r.Context(), org); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to save org: " + err.Error()})
		return
	}

	writeJSON(w, http.StatusCreated, orgEnvToDTO(newEnv))
}

// ── PUT /api/v1/org/environments/{env} ───────────────────────────────────────

func (rh *rbacHandler) handleUpdateOrgEnvironment(w http.ResponseWriter, r *http.Request) {
	envName := r.PathValue("env")

	var req upsertOrgEnvRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}

	org, err := rh.orgStore.GetOrg(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to load org"})
		return
	}

	found := false
	for i, e := range org.Environments {
		if e.Name == envName {
			if req.DisplayName != "" {
				org.Environments[i].DisplayName = req.DisplayName
			}
			if req.ClusterRef != "" {
				org.Environments[i].ClusterRef = req.ClusterRef
			}
			if req.BaseDomain != "" {
				org.Environments[i].BaseDomain = req.BaseDomain
			}
			// NamespacePattern is a pointer: nil = don't touch, "" = clear.
			if req.NamespacePattern != nil {
				org.Environments[i].NamespacePattern = *req.NamespacePattern
			}
			if req.Order > 0 {
				org.Environments[i].Order = req.Order
			}
			found = true
			break
		}
	}
	if !found {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "org environment not found"})
		return
	}
	sortOrgEnvs(org.Environments)

	if err := rh.orgStore.SaveOrg(r.Context(), org); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to save org: " + err.Error()})
		return
	}

	// Best-effort backfill: when a ClusterRef is being set on an existing env,
	// upsert vault items for all apps so their ExternalSecrets can resolve.
	// Run in a background goroutine to not delay the API response.
	if req.ClusterRef != "" && rh.vaultItemWriter != nil && rh.vaultAppStore != nil {
		orgName := org.Name
		if orgName == "" {
			orgName = "default"
		}
		go func() {
			backfillVaultItems(context.Background(), rh.vaultAppStore, rh.vaultItemWriter, rh.projectStore, orgName, envName)
		}()
	}

	for _, e := range org.Environments {
		if e.Name == envName {
			writeJSON(w, http.StatusOK, orgEnvToDTO(e))
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ── DELETE /api/v1/org/environments/{env} ────────────────────────────────────

func (rh *rbacHandler) handleDeleteOrgEnvironment(w http.ResponseWriter, r *http.Request) {
	envName := r.PathValue("env")

	org, err := rh.orgStore.GetOrg(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to load org"})
		return
	}

	filtered := org.Environments[:0]
	found := false
	for _, e := range org.Environments {
		if e.Name == envName {
			found = true
		} else {
			filtered = append(filtered, e)
		}
	}
	if !found {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "org environment not found"})
		return
	}
	org.Environments = filtered

	if err := rh.orgStore.SaveOrg(r.Context(), org); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to save org: " + err.Error()})
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ── helpers ───────────────────────────────────────────────────────────────────

func sortOrgEnvs(envs []rbac.OrgEnvironment) {
	sort.Slice(envs, func(i, j int) bool {
		if envs[i].Order != envs[j].Order {
			return envs[i].Order < envs[j].Order
		}
		return envs[i].Name < envs[j].Name
	})
}

// backfillVaultItems iterates over all apps in all projects and upserts a
// vault item skeleton for the given env. Called as a background goroutine when
// a cluster is first bound to an org environment.
func backfillVaultItems(
	ctx context.Context,
	appStore domain.AppStore,
	vaultWriter VaultItemWriter,
	projectStore project.Store,
	orgName, envName string,
) {
	if appStore == nil || vaultWriter == nil {
		return
	}
	projects := []string{}
	if projectStore != nil {
		if projs, err := projectStore.List(ctx); err == nil {
			for _, p := range projs {
				projects = append(projects, p.Metadata.Name)
			}
		}
	}
	if len(projects) == 0 {
		projects = []string{"default"}
	}
	for _, projName := range projects {
		apps, err := appStore.ListApps(ctx, projName)
		if err != nil {
			continue
		}
		for _, app := range apps {
			if uErr := vaultWriter.UpsertAppItem(ctx, orgName, projName, app.Name, envName); uErr != nil {
				slog.Error("vault backfill: failed to upsert item",
					"org", orgName, "project", projName, "app", app.Name, "env", envName, "error", uErr)
			}
		}
	}
	slog.Info("vault backfill: complete", "org", orgName, "env", envName)
}

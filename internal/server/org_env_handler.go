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
	"encoding/json"
	"fmt"
	"net/http"
	"sort"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/rbac"
)

// ── GET /api/v1/org/environments ─────────────────────────────────────────────

// OrgEnvironmentDTO is the JSON representation of an org-level environment.
type OrgEnvironmentDTO struct {
	Name             string   `json:"name"`
	DisplayName      string   `json:"displayName,omitempty"`
	Order            int      `json:"order"`
	ClusterRefs      []string `json:"clusterRefs,omitempty"`
	ActiveClusterRef string   `json:"activeClusterRef,omitempty"`
	BaseDomain       string   `json:"baseDomain,omitempty"`
	NamespacePattern string   `json:"namespacePattern,omitempty"`
	// RoutingProfiles is a sparse override map keyed by ExposeMode name.
	// Entries here replace the org-level profile of the same name; absent
	// names inherit the org-level profile.
	RoutingProfiles domain.RoutingProfiles `json:"routingProfiles,omitempty"`
	// AddonProfiles is a sparse override map keyed by addon type. Entries
	// replace the org-level addon profile of the same type; absent types
	// inherit the org default. Use this for per-env provider swaps
	// (staging on valkey-operator, prod on Crossplane ElastiCache, …).
	AddonProfiles domain.AddonProfiles `json:"addonProfiles,omitempty"`
}

func orgEnvToDTO(e rbac.OrgEnvironment) OrgEnvironmentDTO {
	return OrgEnvironmentDTO{
		Name:             e.Name,
		DisplayName:      e.DisplayName,
		Order:            e.Order,
		ClusterRefs:      e.ClusterRefs,
		ActiveClusterRef: e.ActiveClusterRef,
		BaseDomain:       e.BaseDomain,
		NamespacePattern: e.NamespacePattern,
		RoutingProfiles:  e.RoutingProfiles,
		AddonProfiles:    e.AddonProfiles,
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
	Name        string `json:"name"`
	DisplayName string `json:"displayName,omitempty"`
	Order       int    `json:"order"`
	// ClusterRefs when non-nil replaces the registered cluster set. Send
	// nil on update to leave it unchanged; an empty slice ([]) unbinds
	// the env from every cluster.
	ClusterRefs *[]string `json:"clusterRefs,omitempty"`
	// ActiveClusterRef is the deploy target. Must be a member of
	// ClusterRefs (or empty to unset, which falls back to ClusterRefs[0]
	// at read time).
	ActiveClusterRef *string `json:"activeClusterRef,omitempty"`
	BaseDomain       string  `json:"baseDomain,omitempty"`
	// NamespacePattern when present (including empty string) replaces the stored
	// per-environment namespace pattern. Use "" to clear an existing override
	// and fall back to the org-wide ResourceNaming.AppNamespace default.
	NamespacePattern *string `json:"namespacePattern,omitempty"`
	// RoutingProfiles when non-nil replaces the stored sparse override map
	// wholesale. Send an empty map ({}) to clear all overrides; omit the
	// field (nil) on update to leave the stored map untouched. Each entry's
	// name must be one of "internal" or "external" and IngressClassName is
	// required — Org.Validate enforces both.
	RoutingProfiles *domain.RoutingProfiles `json:"routingProfiles,omitempty"`
	// AddonProfiles when non-nil replaces the stored sparse override map
	// wholesale. Same nil/empty semantics as RoutingProfiles. Each entry's
	// type must match a registered connection contract — the org-level
	// PUT validates this; per-env entries are accepted as-is and resolved
	// at publish time.
	AddonProfiles *domain.AddonProfiles `json:"addonProfiles,omitempty"`
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
		BaseDomain:  req.BaseDomain,
	}
	if req.ClusterRefs != nil {
		newEnv.ClusterRefs = *req.ClusterRefs
	}
	if req.ActiveClusterRef != nil {
		newEnv.ActiveClusterRef = *req.ActiveClusterRef
	}
	if req.NamespacePattern != nil {
		newEnv.NamespacePattern = *req.NamespacePattern
	}
	if req.RoutingProfiles != nil {
		newEnv.RoutingProfiles = *req.RoutingProfiles
	}
	if req.AddonProfiles != nil {
		newEnv.AddonProfiles = *req.AddonProfiles
	}
	if err := validateActiveInRefs(newEnv); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: err.Error()})
		return
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
			// ClusterRefs is a pointer: nil = don't touch, [] = unbind.
			if req.ClusterRefs != nil {
				org.Environments[i].ClusterRefs = *req.ClusterRefs
			}
			// ActiveClusterRef is a pointer: nil = don't touch, "" = unset
			// (EffectiveClusterRef will fall back to ClusterRefs[0]).
			if req.ActiveClusterRef != nil {
				org.Environments[i].ActiveClusterRef = *req.ActiveClusterRef
			}
			if req.BaseDomain != "" {
				org.Environments[i].BaseDomain = req.BaseDomain
			}
			// NamespacePattern is a pointer: nil = don't touch, "" = clear.
			if req.NamespacePattern != nil {
				org.Environments[i].NamespacePattern = *req.NamespacePattern
			}
			// RoutingProfiles is a pointer: nil = don't touch, {} = clear.
			if req.RoutingProfiles != nil {
				org.Environments[i].RoutingProfiles = *req.RoutingProfiles
			}
			// AddonProfiles is a pointer: nil = don't touch, {} = clear.
			if req.AddonProfiles != nil {
				org.Environments[i].AddonProfiles = *req.AddonProfiles
			}
			if req.Order > 0 {
				org.Environments[i].Order = req.Order
			}
			if err := validateActiveInRefs(org.Environments[i]); err != nil {
				writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: err.Error()})
				return
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

	// Env/cluster vault provisioning (and ClusterSecretStore publishing) is
	// handled by the secret-backend lifecycle hooks, not here.

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

// validateActiveInRefs ensures the env's ActiveClusterRef (if non-empty) is
// a member of its registered ClusterRefs. Org.Validate enforces the same
// rule at save time; this handler-level check returns a 422 before we even
// reach Org.Validate so the user sees a tight error message.
func validateActiveInRefs(e rbac.OrgEnvironment) error {
	if e.ActiveClusterRef == "" {
		return nil
	}
	for _, c := range e.ClusterRefs {
		if c == e.ActiveClusterRef {
			return nil
		}
	}
	return fmt.Errorf("activeClusterRef %q must be present in clusterRefs %v", e.ActiveClusterRef, e.ClusterRefs)
}

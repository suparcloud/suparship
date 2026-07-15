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
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"slices"
	"sort"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/rbac"
	"github.com/suparcloud/suparship/internal/secrets"
)

// ── GET /api/v1/org/environments ─────────────────────────────────────────────

// OrgEnvironmentDTO is the JSON representation of an org-level environment.
type OrgEnvironmentDTO struct {
	Name             string   `json:"name"`
	DisplayName      string   `json:"displayName,omitempty"`
	Order            int      `json:"order"`
	ClusterRefs      []string `json:"clusterRefs,omitempty"`
	ActiveClusterRef string   `json:"activeClusterRef,omitempty"`
	// DeployMode: "active" (default — deploy to the active cluster only) or
	// "all" (fan out to every clusterRef).
	DeployMode       string `json:"deployMode,omitempty"`
	BaseDomain       string `json:"baseDomain,omitempty"`
	NamespacePattern string `json:"namespacePattern,omitempty"`
	// RoutingProfiles is a sparse override map keyed by ExposeMode name.
	// Entries here replace the org-level profile of the same name; absent
	// names inherit the org-level profile.
	RoutingProfiles domain.RoutingProfiles `json:"routingProfiles,omitempty"`
	// Warning is set on an update response only when the change relocates
	// workloads (active cluster or namespace pattern moved) and was therefore
	// saved but not auto-applied. Empty on list/get.
	Warning string `json:"warning,omitempty"`
}

func orgEnvToDTO(e rbac.OrgEnvironment) OrgEnvironmentDTO {
	return OrgEnvironmentDTO{
		Name:             e.Name,
		DisplayName:      e.DisplayName,
		Order:            e.Order,
		ClusterRefs:      e.ClusterRefs,
		ActiveClusterRef: e.ActiveClusterRef,
		DeployMode:       e.DeployMode,
		BaseDomain:       e.BaseDomain,
		NamespacePattern: e.NamespacePattern,
		RoutingProfiles:  e.RoutingProfiles,
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
	// DeployMode when non-nil sets "active" or "all". Omit to leave unchanged.
	DeployMode *string `json:"deployMode,omitempty"`
	BaseDomain string  `json:"baseDomain,omitempty"`
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
	if req.DeployMode != nil {
		newEnv.DeployMode = *req.DeployMode
	}
	if req.NamespacePattern != nil {
		newEnv.NamespacePattern = *req.NamespacePattern
	}
	if req.RoutingProfiles != nil {
		newEnv.RoutingProfiles = *req.RoutingProfiles
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

	rh.reconcileSecretStores("env-create:" + newEnv.Name)

	writeJSON(w, http.StatusCreated, orgEnvToDTO(newEnv))
}

// reconcileSecretStores best-effort republishes ESO ClusterSecretStores in the
// background after an environment change, so the new env's store exists in
// gitops (k8s backend), and re-seals every cluster's unified store (1Password
// backend — its vault list depends on env↔cluster bindings). No-op when no
// reconciler/sealer is wired.
func (rh *rbacHandler) reconcileSecretStores(reason string) {
	if rh.storeReconciler != nil {
		go func() {
			if err := rh.storeReconciler.ReconcileSecretStores(context.Background()); err != nil {
				slog.Warn("reconcile secret stores failed", "reason", reason, "error", err)
			}
		}()
	}
	if sh := rh.secretsHandler; sh != nil && sh.orgStore != nil {
		go func() {
			ctx := context.Background()
			org, err := sh.orgStore.GetOrg(ctx)
			if err != nil || org == nil {
				return
			}
			if org.SecretBackend.Effective() != secrets.Backend1Password {
				return
			}
			sh.resealAllClusters(ctx, org, reason)
		}()
	}
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
	relocated := false
	deployChanged := false
	for i, e := range org.Environments {
		if e.Name == envName {
			before := e // range copy holds the pre-mutation env definition
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
			if req.DeployMode != nil {
				org.Environments[i].DeployMode = *req.DeployMode
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
			if req.Order > 0 {
				org.Environments[i].Order = req.Order
			}
			if err := validateActiveInRefs(org.Environments[i]); err != nil {
				writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: err.Error()})
				return
			}
			relocated = envWorkloadRelocated(before, org.Environments[i])
			deployChanged = envDeployConfigChanged(before, org.Environments[i])
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

	// Bindings may have changed: republish k8s stores and re-seal the 1Password
	// unified stores (their vault lists embed the env↔cluster bindings).
	rh.reconcileSecretStores("env-update:" + envName)

	// A deploy-affecting change propagates to apps. Split by risk:
	//   - relocating (active cluster or namespace moved): the workload's home
	//     changes, so an auto-publish would prune the old cluster/namespace and
	//     recreate it empty (data loss for stateful apps). Save it, but do NOT
	//     auto-apply — warn so the operator migrates deliberately (drain/backup,
	//     then republish the affected apps).
	//   - additive / in-place (add a cluster, active→all, base domain, routing,
	//     addons, order, remove a non-active cluster): safe to auto-republish so
	//     apps re-resolve their targets / re-render values immediately.
	var warning string
	switch {
	case relocated:
		warning = "This change relocates workloads (active cluster or namespace pattern): " +
			"their old cluster/namespace is pruned and recreated empty — data-bearing for " +
			"stateful apps. It was saved but NOT auto-applied. Republish the affected apps " +
			"deliberately (after draining/backing up) to migrate them."
	case deployChanged && rh.envConfigHandler != nil:
		rh.envConfigHandler.scheduleRepublishEnvApps(envName)
	}

	for _, e := range org.Environments {
		if e.Name == envName {
			dto := orgEnvToDTO(e)
			dto.Warning = warning
			writeJSON(w, http.StatusOK, dto)
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

	rh.reconcileSecretStores("env-delete:" + envName)

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

// envWorkloadRelocated reports whether an env change moves an app's workload
// HOME — its target cluster or its namespace — between two revisions. Such a
// change is data-bearing: the generated ArgoCD Application (Prune=true) would
// delete the old cluster/namespace's resources and recreate them empty. Signals:
//   - EffectiveClusterRef changed (an active-cluster switch, or removing the
//     active cluster so the ClusterRefs[0] fallback moves).
//   - The effective namespace pattern (app- or project-scope) changed.
func envWorkloadRelocated(a, b rbac.OrgEnvironment) bool {
	return a.EffectiveClusterRef() != b.EffectiveClusterRef() ||
		a.EffectiveAppNamespacePattern() != b.EffectiveAppNamespacePattern() ||
		a.EffectiveProjectNamespacePattern() != b.EffectiveProjectNamespacePattern()
}

// envDeployConfigChanged reports whether any field that affects an env's
// published app manifests differs between two revisions — the trigger for an
// auto-republish. Covers cluster binding (ClusterRefs order-sensitive, active,
// mode), namespace patterns, and the in-place render inputs (base domain,
// routing/addon profiles, promotion order). DisplayName is excluded so a
// cosmetic edit doesn't fleet-republish.
func envDeployConfigChanged(a, b rbac.OrgEnvironment) bool {
	return !slices.Equal(a.ClusterRefs, b.ClusterRefs) ||
		a.ActiveClusterRef != b.ActiveClusterRef ||
		a.DeployMode != b.DeployMode ||
		a.BaseDomain != b.BaseDomain ||
		a.Order != b.Order ||
		a.EffectiveAppNamespacePattern() != b.EffectiveAppNamespacePattern() ||
		a.EffectiveProjectNamespacePattern() != b.EffectiveProjectNamespacePattern() ||
		!reflect.DeepEqual(a.RoutingProfiles, b.RoutingProfiles)
}

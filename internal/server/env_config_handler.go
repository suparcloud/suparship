// Package server — env_config_handler.go
//
// Implements the five-level env-var/secret-ref hierarchy endpoints:
//
//	GET  /api/v1/org/envconfig                                           — Org level
//	PUT  /api/v1/org/envconfig                                           — Org level (org_admin)
//	GET  /api/v1/org/envconfig/{envtype}                                 — Environment level
//	PUT  /api/v1/org/envconfig/{envtype}                                 — Environment level (org_admin)
//	GET  /api/v1/projects/{project}/envconfig                            — Project level
//	PUT  /api/v1/projects/{project}/envconfig                            — Project level (project_admin)
//	GET  /api/v1/projects/{project}/apps/{app}/envconfig                 — App level
//	PUT  /api/v1/projects/{project}/apps/{app}/envconfig                 — App level (developer); 202 async
//	GET  /api/v1/projects/{project}/apps/{app}/envs/{env}/envconfig      — App-Env level
//	PUT  /api/v1/projects/{project}/apps/{app}/envs/{env}/envconfig      — App-Env level (developer); 202 async
//	GET  /api/v1/projects/{project}/apps/{app}/envs/{env}/envconfig/resolved — merged view with attribution
//
// # Hierarchy and override order
//
//	Org < Environment < Project < App < AppEnv
//
// Each level overrides the previous one. At runtime containers receive all
// five levels via envFrom (lower levels first, higher levels last); the
// resolved endpoint shows the final merged view with source attribution.
//
// # Write semantics
//
// Org, Environment and Project writes are synchronous (200 OK): the domain
// store is updated and the Kubernetes ConfigMap is patched via
// UpperLevelEnvWriter (when configured) in the same request.
//
// App and App-Env writes are asynchronous (202 Accepted): the domain store is
// updated immediately and a background goroutine triggers GitOps re-publish.
// The publisher re-renders values.yaml, which picks up the new EnvLayers.
package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/envconfig"
	"github.com/suparcloud/suparship/internal/platform"
	"github.com/suparcloud/suparship/internal/project"
	"github.com/suparcloud/suparship/internal/rbac"
)

// ── DTOs ─────────────────────────────────────────────────────────────────────

// SecretRefDTO is the JSON representation of an envconfig.SecretRef.
type SecretRefDTO struct {
	Provider string `json:"provider"`
	Name     string `json:"name"`
	Key      string `json:"key"`
	EnvKey   string `json:"envKey"`
}

// EnvConfigDTO is the JSON body for GET and PUT envconfig endpoints.
type EnvConfigDTO struct {
	// Vars holds plaintext environment variable key/value pairs.
	Vars map[string]string `json:"vars,omitempty"`
	// SecretRefs holds references to external secrets that will be injected as
	// environment variables. No plaintext secret values are stored here.
	SecretRefs []SecretRefDTO `json:"secretRefs,omitempty"`
}

// ResolvedEnvVarDTO is one entry in the resolved endpoint response.
type ResolvedEnvVarDTO struct {
	// Key is the environment variable name.
	Key string `json:"key"`
	// Value is the resolved plaintext value (empty string for secret refs).
	Value string `json:"value,omitempty"`
	// Source is the hierarchy level that provides the winning value.
	// One of: "org", "environment", "project", "app", "app-environment".
	Source string `json:"source"`
	// IsSecret is true when the winning value comes from a SecretRef rather
	// than a plain Var. Secret values are never included in this response.
	IsSecret bool `json:"isSecret,omitempty"`
}

// ResolvedEnvConfigResponse is the JSON body for GET .../envconfig/resolved.
type ResolvedEnvConfigResponse struct {
	// Vars lists all resolved environment variables with source attribution.
	// Entries are sorted by key for stable output.
	Vars []ResolvedEnvVarDTO `json:"vars"`
}

// ── Handler ───────────────────────────────────────────────────────────────────

// envConfigHandler serves the five-level env config endpoints. All fields are
// optional (nil-guarded at runtime) so the handler can be wired incrementally.
type envConfigHandler struct {
	orgStore         rbac.OrgStore
	projectStore     project.Store
	appStore         domain.AppStore
	stackStore       domain.StackStore              // optional: enables stack-scope env layers
	upperLevelWriter *envconfig.UpperLevelEnvWriter // optional: writes k8s ConfigMaps
	publisher        GitOpsPublisher                // optional: async re-publish
	logger           *slog.Logger
}

// ── Conversion helpers ────────────────────────────────────────────────────────

func toEnvConfigDTO(cfg envconfig.EnvConfig) EnvConfigDTO {
	dto := EnvConfigDTO{Vars: cfg.Vars}
	if len(cfg.SecretRefs) > 0 {
		refs := make([]SecretRefDTO, len(cfg.SecretRefs))
		for i, r := range cfg.SecretRefs {
			refs[i] = SecretRefDTO{
				Provider: r.Provider,
				Name:     r.Name,
				Key:      r.Key,
				EnvKey:   r.EnvKey,
			}
		}
		dto.SecretRefs = refs
	}
	return dto
}

func fromEnvConfigDTO(dto EnvConfigDTO) envconfig.EnvConfig {
	cfg := envconfig.EnvConfig{Vars: dto.Vars}
	if len(dto.SecretRefs) > 0 {
		refs := make([]envconfig.SecretRef, len(dto.SecretRefs))
		for i, r := range dto.SecretRefs {
			refs[i] = envconfig.SecretRef{
				Provider: r.Provider,
				Name:     r.Name,
				Key:      r.Key,
				EnvKey:   r.EnvKey,
			}
		}
		cfg.SecretRefs = refs
	}
	return cfg
}

func decodeEnvConfigDTO(r *http.Request) (EnvConfigDTO, bool) {
	var dto EnvConfigDTO
	if err := json.NewDecoder(r.Body).Decode(&dto); err != nil {
		return EnvConfigDTO{}, false
	}
	return dto, true
}

// ── Level 1 — Org ─────────────────────────────────────────────────────────────

// handleGetOrgEnvConfig serves GET /api/v1/org/envconfig.
// Returns the Org-level EnvConfig from the org domain object.
func (h *envConfigHandler) handleGetOrgEnvConfig(w http.ResponseWriter, r *http.Request) {
	org, err := h.orgStore.GetOrg(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to load org"})
		return
	}
	writeJSON(w, http.StatusOK, toEnvConfigDTO(org.EnvConfig))
}

// handlePutOrgEnvConfig serves PUT /api/v1/org/envconfig.
// Replaces the Org-level EnvConfig, persists to the domain store and (when
// configured) updates the k8s ConfigMap via UpperLevelEnvWriter.
func (h *envConfigHandler) handlePutOrgEnvConfig(w http.ResponseWriter, r *http.Request) {
	dto, ok := decodeEnvConfigDTO(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}

	cfg := fromEnvConfigDTO(dto)
	if err := envconfig.ValidateEnvConfig(cfg); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: err.Error()})
		return
	}

	org, err := h.orgStore.GetOrg(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to load org"})
		return
	}

	org.EnvConfig = cfg
	if err := h.orgStore.SaveOrg(r.Context(), org); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to save org"})
		return
	}

	if h.upperLevelWriter != nil {
		if err := h.upperLevelWriter.WriteOrgEnvConfig(r.Context(), cfg); err != nil {
			h.logger.Error("failed to write org env configmap", "err", err)
			// Non-fatal: domain store is already updated; k8s will be reconciled on next write.
		}
	}

	h.scheduleRepublishAllApps("org-envconfig")

	writeJSON(w, http.StatusOK, toEnvConfigDTO(cfg))
}

// ── Level 2 — Environment ─────────────────────────────────────────────────────

// handleGetEnvTypeEnvConfig serves GET /api/v1/org/envconfig/{envtype}.
func (h *envConfigHandler) handleGetEnvTypeEnvConfig(w http.ResponseWriter, r *http.Request) {
	envType := r.PathValue("envtype")
	if envType == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "envtype path parameter is required"})
		return
	}

	org, err := h.orgStore.GetOrg(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to load org"})
		return
	}

	for _, e := range org.Environments {
		if e.Name == envType {
			writeJSON(w, http.StatusOK, toEnvConfigDTO(e.EnvConfig))
			return
		}
	}
	writeJSON(w, http.StatusNotFound, errorResponse{Error: "environment type not found: " + envType})
}

// handlePutEnvTypeEnvConfig serves PUT /api/v1/org/envconfig/{envtype}.
func (h *envConfigHandler) handlePutEnvTypeEnvConfig(w http.ResponseWriter, r *http.Request) {
	envType := r.PathValue("envtype")
	if envType == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "envtype path parameter is required"})
		return
	}

	dto, ok := decodeEnvConfigDTO(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}

	cfg := fromEnvConfigDTO(dto)
	if err := envconfig.ValidateEnvConfig(cfg); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: err.Error()})
		return
	}

	org, err := h.orgStore.GetOrg(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to load org"})
		return
	}

	found := false
	for i, e := range org.Environments {
		if e.Name == envType {
			org.Environments[i].EnvConfig = cfg
			found = true
			break
		}
	}
	if !found {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "environment type not found: " + envType})
		return
	}

	if err := h.orgStore.SaveOrg(r.Context(), org); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to save org"})
		return
	}

	if h.upperLevelWriter != nil {
		if err := h.upperLevelWriter.WriteEnvTypeEnvConfig(r.Context(), envType, cfg); err != nil {
			h.logger.Error("failed to write env-type env configmap", "envType", envType, "err", err)
		}
	}

	h.scheduleRepublishAllApps("envtype=" + envType)

	writeJSON(w, http.StatusOK, toEnvConfigDTO(cfg))
}

// ── Level 2.5 — Cluster (escape hatch) ────────────────────────────────────────

// handleGetClusterEnvConfig serves GET /api/v1/clusters/{cluster}/envconfig.
// Returns the cluster-level EnvConfig stored in suparship-system as a runtime
// ConfigMap. When no writer is configured or the ConfigMap does not exist,
// returns an empty EnvConfig.
func (h *envConfigHandler) handleGetClusterEnvConfig(w http.ResponseWriter, r *http.Request) {
	cluster := r.PathValue("cluster")
	if cluster == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "cluster path parameter is required"})
		return
	}

	if h.upperLevelWriter == nil {
		writeJSON(w, http.StatusOK, toEnvConfigDTO(envconfig.EnvConfig{}))
		return
	}
	// ?env=<name> selects the per-(cluster, env) override set; absent selects the
	// cluster-global set (every env on this cluster).
	env := strings.TrimSpace(r.URL.Query().Get("env"))
	var cfg envconfig.EnvConfig
	var err error
	if env != "" {
		cfg, err = h.upperLevelWriter.ReadClusterEnvScopedConfig(r.Context(), cluster, env)
	} else {
		cfg, err = h.upperLevelWriter.ReadClusterEnvConfig(r.Context(), cluster)
	}
	if err != nil {
		h.logger.Error("failed to read cluster env configmap", "cluster", cluster, "env", env, "err", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to read cluster env config"})
		return
	}
	writeJSON(w, http.StatusOK, toEnvConfigDTO(cfg))
}

// handlePutClusterEnvConfig serves PUT /api/v1/clusters/{cluster}/envconfig.
// Replaces the cluster-level EnvConfig in the suparship-system ConfigMap.
// The ConfigMap is the source of truth for cluster-scope env vars; there is
// no domain-object duplication.
func (h *envConfigHandler) handlePutClusterEnvConfig(w http.ResponseWriter, r *http.Request) {
	cluster := r.PathValue("cluster")
	if cluster == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "cluster path parameter is required"})
		return
	}

	dto, ok := decodeEnvConfigDTO(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}

	cfg := fromEnvConfigDTO(dto)
	if err := envconfig.ValidateEnvConfig(cfg); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: err.Error()})
		return
	}

	if h.upperLevelWriter == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "cluster env config writer not configured"})
		return
	}
	// ?env=<name> writes the per-(cluster, env) override set; absent writes the
	// cluster-global set.
	env := strings.TrimSpace(r.URL.Query().Get("env"))
	var err error
	if env != "" {
		err = h.upperLevelWriter.WriteClusterEnvScopedConfig(r.Context(), cluster, env, cfg)
	} else {
		err = h.upperLevelWriter.WriteClusterEnvConfig(r.Context(), cluster, cfg)
	}
	if err != nil {
		h.logger.Error("failed to write cluster env configmap", "cluster", cluster, "env", env, "err", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to save cluster env config"})
		return
	}

	h.scheduleRepublishClusterApps(cluster)

	writeJSON(w, http.StatusOK, toEnvConfigDTO(cfg))
}

// readClusterEnvConfig resolves the cluster bound to envName via the org's
// Environments list, then reads the runtime ConfigMap for that cluster.
// Returns an empty EnvConfig when the env is unbound, no writer is configured,
// or the read fails (best-effort — resolved view should not 500 on cluster
// read failures).
func (h *envConfigHandler) readClusterEnvConfig(ctx context.Context, envs []rbac.OrgEnvironment, envName string) envconfig.EnvConfig {
	if h.upperLevelWriter == nil {
		return envconfig.EnvConfig{}
	}
	var clusterRef string
	for _, e := range envs {
		if e.Name == envName {
			clusterRef = e.EffectiveClusterRef()
			break
		}
	}
	if clusterRef == "" {
		return envconfig.EnvConfig{}
	}
	cfg, err := h.upperLevelWriter.ReadClusterEnvConfig(ctx, clusterRef)
	if err != nil {
		h.logger.Warn("failed to read cluster env configmap during resolve — treating as empty",
			"cluster", clusterRef, "env", envName, "err", err)
		return envconfig.EnvConfig{}
	}
	// Fold the per-(cluster, env) override on top so the resolved preview matches
	// what the publisher merges: cluster-env wins over cluster-global.
	if envCfg, err := h.upperLevelWriter.ReadClusterEnvScopedConfig(ctx, clusterRef, envName); err == nil {
		if len(envCfg.Vars) > 0 && cfg.Vars == nil {
			cfg.Vars = map[string]string{}
		}
		for k, v := range envCfg.Vars {
			cfg.Vars[k] = v
		}
	}
	return cfg
}

// ── Level 3 — Project ─────────────────────────────────────────────────────────

// handleGetProjectEnvConfig serves GET /api/v1/projects/{project}/envconfig.
func (h *envConfigHandler) handleGetProjectEnvConfig(w http.ResponseWriter, r *http.Request) {
	projectName := r.PathValue("project")

	proj, err := h.projectStore.Get(r.Context(), projectName)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "project not found: " + projectName})
		return
	}

	writeJSON(w, http.StatusOK, toEnvConfigDTO(proj.Spec.EnvConfig))
}

// handlePutProjectEnvConfig serves PUT /api/v1/projects/{project}/envconfig.
func (h *envConfigHandler) handlePutProjectEnvConfig(w http.ResponseWriter, r *http.Request) {
	projectName := r.PathValue("project")

	dto, ok := decodeEnvConfigDTO(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}

	cfg := fromEnvConfigDTO(dto)
	if err := envconfig.ValidateEnvConfig(cfg); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: err.Error()})
		return
	}

	proj, err := h.projectStore.Get(r.Context(), projectName)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "project not found: " + projectName})
		return
	}

	proj.Spec.EnvConfig = cfg
	if err := h.projectStore.Save(r.Context(), proj); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to save project"})
		return
	}

	if h.upperLevelWriter != nil {
		if err := h.upperLevelWriter.WriteProjectEnvConfig(r.Context(), projectName, cfg); err != nil {
			h.logger.Error("failed to write project env configmap", "project", projectName, "err", err)
		}
	}

	h.scheduleRepublishProjectApps(projectName, "project-envconfig")

	writeJSON(w, http.StatusOK, toEnvConfigDTO(cfg))
}

// handleGetProjectEnvEnvConfig serves
// GET /api/v1/projects/{project}/envconfig/env/{env} — a project's variables for
// one specific environment (the project-env level), mirroring project-env secrets.
func (h *envConfigHandler) handleGetProjectEnvEnvConfig(w http.ResponseWriter, r *http.Request) {
	projectName := r.PathValue("project")
	envName := r.PathValue("env")

	proj, err := h.projectStore.Get(r.Context(), projectName)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "project not found: " + projectName})
		return
	}
	writeJSON(w, http.StatusOK, toEnvConfigDTO(proj.Spec.EnvConfigByEnv[envName]))
}

// handlePutProjectEnvEnvConfig serves
// PUT /api/v1/projects/{project}/envconfig/env/{env}. The values are baked into
// each app's per-env ConfigMap on the next publish (scheduled below).
func (h *envConfigHandler) handlePutProjectEnvEnvConfig(w http.ResponseWriter, r *http.Request) {
	projectName := r.PathValue("project")
	envName := r.PathValue("env")

	dto, ok := decodeEnvConfigDTO(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}
	cfg := fromEnvConfigDTO(dto)
	if err := envconfig.ValidateEnvConfig(cfg); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: err.Error()})
		return
	}

	proj, err := h.projectStore.Get(r.Context(), projectName)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "project not found: " + projectName})
		return
	}
	if proj.Spec.EnvConfigByEnv == nil {
		proj.Spec.EnvConfigByEnv = map[string]envconfig.EnvConfig{}
	}
	proj.Spec.EnvConfigByEnv[envName] = cfg
	if err := h.projectStore.Save(r.Context(), proj); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to save project"})
		return
	}

	h.scheduleRepublishProjectApps(projectName, "project-env-envconfig")

	writeJSON(w, http.StatusOK, toEnvConfigDTO(cfg))
}

// ── Level 4 — App ─────────────────────────────────────────────────────────────

// handleGetAppEnvConfig serves GET /api/v1/projects/{project}/apps/{app}/envconfig.
func (h *envConfigHandler) handleGetAppEnvConfig(w http.ResponseWriter, r *http.Request) {
	projectName := r.PathValue("project")
	appName := r.PathValue("app")

	app, err := h.appStore.GetApp(r.Context(), projectName, appName)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "app not found: " + appName})
		return
	}

	writeJSON(w, http.StatusOK, toEnvConfigDTO(app.Spec.EnvConfig))
}

// handlePutAppEnvConfig serves PUT /api/v1/projects/{project}/apps/{app}/envconfig.
// Returns 202 Accepted; the GitOps re-publish runs asynchronously.
func (h *envConfigHandler) handlePutAppEnvConfig(w http.ResponseWriter, r *http.Request) {
	projectName := r.PathValue("project")
	appName := r.PathValue("app")

	dto, ok := decodeEnvConfigDTO(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}

	cfg := fromEnvConfigDTO(dto)
	if err := envconfig.ValidateEnvConfig(cfg); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: err.Error()})
		return
	}

	app, err := h.appStore.GetApp(r.Context(), projectName, appName)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "app not found: " + appName})
		return
	}

	app.Spec.EnvConfig = cfg
	if err := h.appStore.SaveApp(r.Context(), projectName, app); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to save app"})
		return
	}

	h.scheduleRepublish(projectName, appName)

	writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
}

// ── Level 5 — App-Env ─────────────────────────────────────────────────────────

// handleGetAppEnvEnvConfig serves
// GET /api/v1/projects/{project}/apps/{app}/envs/{env}/envconfig.
func (h *envConfigHandler) handleGetAppEnvEnvConfig(w http.ResponseWriter, r *http.Request) {
	projectName := r.PathValue("project")
	appName := r.PathValue("app")
	envName := r.PathValue("env")

	app, err := h.appStore.GetApp(r.Context(), projectName, appName)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "app not found: " + appName})
		return
	}

	cfg := appEnvConfig(app, envName)
	writeJSON(w, http.StatusOK, toEnvConfigDTO(cfg))
}

// handlePutAppEnvEnvConfig serves
// PUT /api/v1/projects/{project}/apps/{app}/envs/{env}/envconfig.
// Returns 202 Accepted; the GitOps re-publish runs asynchronously.
func (h *envConfigHandler) handlePutAppEnvEnvConfig(w http.ResponseWriter, r *http.Request) {
	projectName := r.PathValue("project")
	appName := r.PathValue("app")
	envName := r.PathValue("env")

	dto, ok := decodeEnvConfigDTO(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}

	cfg := fromEnvConfigDTO(dto)
	if err := envconfig.ValidateEnvConfig(cfg); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: err.Error()})
		return
	}

	app, err := h.appStore.GetApp(r.Context(), projectName, appName)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "app not found: " + appName})
		return
	}

	setAppEnvConfig(app, envName, cfg)

	if err := h.appStore.SaveApp(r.Context(), projectName, app); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to save app"})
		return
	}

	h.scheduleRepublish(projectName, appName)

	writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
}

// ── Resolved view ─────────────────────────────────────────────────────────────

// handleGetResolvedEnvConfig serves
// GET /api/v1/projects/{project}/apps/{app}/envs/{env}/envconfig/resolved.
//
// It reads all six hierarchy levels, runs them through envconfig.ResolveEnvLayers,
// and returns the merged view with source attribution. Secret values are never
// included; only the key, source, and isSecret flag are returned for secret refs.
func (h *envConfigHandler) handleGetResolvedEnvConfig(w http.ResponseWriter, r *http.Request) {
	projectName := r.PathValue("project")
	appName := r.PathValue("app")
	envName := r.PathValue("env")
	ctx := r.Context()

	org, err := h.orgStore.GetOrg(ctx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to load org"})
		return
	}
	orgCfg := org.EnvConfig

	// Environment level: find the OrgEnvironment entry whose name matches envName.
	// For preview environments (e.g. "pr-42") we fall back to the "preview" entry.
	envCfg := envCfgForEnvironment(org.Environments, envName)

	proj, err := h.projectStore.Get(ctx, projectName)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "project not found: " + projectName})
		return
	}
	projectCfg := proj.Spec.EnvConfig
	projectEnvCfg := proj.Spec.EnvConfigByEnv[envName]

	app, err := h.appStore.GetApp(ctx, projectName, appName)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "app not found: " + appName})
		return
	}
	appCfg := app.Spec.EnvConfig
	appEnvCfg := appEnvConfig(app, envName)

	// Stack levels: a member app inherits its stack's global + per-env config
	// (between project-env and app). Empty when the app is not in a stack.
	var stackCfg, stackEnvCfg envconfig.EnvConfig
	if app.Spec.Stack != "" && h.stackStore != nil {
		if st, serr := h.stackStore.GetStack(ctx, projectName, app.Spec.Stack); serr == nil && st != nil {
			stackCfg = st.Spec.EnvConfig
			stackEnvCfg = st.Spec.EnvConfigByEnv[envName]
		}
	}

	// Cluster level: read the runtime ConfigMap for the cluster bound to envName.
	// Empty when the env is unbound or no writer is configured.
	clusterCfg := h.readClusterEnvConfig(ctx, org.Environments, envName)

	_, resolved := envconfig.ResolveEnvLayers(orgCfg, envCfg, projectCfg, projectEnvCfg, stackCfg, stackEnvCfg, appCfg, appEnvCfg, clusterCfg)

	// Build a lookup of plain-text values by merging Vars in hierarchy order
	// (lower levels first, higher wins). Secret values are not included.
	plainValues := make(map[string]string)
	for _, cfg := range []envconfig.EnvConfig{orgCfg, envCfg, projectCfg, projectEnvCfg, stackCfg, stackEnvCfg, appCfg, appEnvCfg, clusterCfg} {
		for k, v := range cfg.Vars {
			plainValues[k] = v
		}
	}

	// Build a deterministically ordered list of resolved entries.
	keys := make([]string, 0, len(resolved))
	for k := range resolved {
		keys = append(keys, k)
	}
	sortStrings(keys)

	entries := make([]ResolvedEnvVarDTO, 0, len(resolved))
	for _, k := range keys {
		rv := resolved[k]
		entry := ResolvedEnvVarDTO{
			Key:      k,
			Source:   rv.Source,
			IsSecret: rv.IsSecret,
		}
		if !rv.IsSecret {
			entry.Value = plainValues[k]
		}
		entries = append(entries, entry)
	}

	writeJSON(w, http.StatusOK, ResolvedEnvConfigResponse{Vars: entries})
}

// ── Internal helpers ──────────────────────────────────────────────────────────

// appEnvConfig returns the EnvConfig stored in app.Spec.EnvironmentDefaults[envName].
// Returns an empty EnvConfig when no override exists for envName.
func appEnvConfig(app *domain.App, envName string) envconfig.EnvConfig {
	if app.Spec.EnvironmentDefaults == nil {
		return envconfig.EnvConfig{}
	}
	override, ok := app.Spec.EnvironmentDefaults[envName]
	if !ok {
		return envconfig.EnvConfig{}
	}
	return override.EnvConfig
}

// setAppEnvConfig stores cfg into app.Spec.EnvironmentDefaults[envName].EnvConfig,
// creating the EnvironmentDefaults map and override entry if needed.
func setAppEnvConfig(app *domain.App, envName string, cfg envconfig.EnvConfig) {
	if app.Spec.EnvironmentDefaults == nil {
		app.Spec.EnvironmentDefaults = make(map[string]domain.EnvironmentOverride)
	}
	override := app.Spec.EnvironmentDefaults[envName]
	override.EnvConfig = cfg
	app.Spec.EnvironmentDefaults[envName] = override
}

// envCfgForEnvironment returns the EnvConfig for the OrgEnvironment whose name
// matches envName. For preview environment instances (e.g. "pr-42") it falls
// back to an entry named "preview". Returns an empty EnvConfig when nothing matches.
func envCfgForEnvironment(envs []rbac.OrgEnvironment, envName string) envconfig.EnvConfig {
	var previewFallback envconfig.EnvConfig
	for _, e := range envs {
		if e.Name == envName {
			return e.EnvConfig
		}
		if e.Name == "preview" {
			previewFallback = e.EnvConfig
		}
	}
	return previewFallback
}

// scheduleRepublish fetches the app and its environments in a background
// goroutine and calls the GitOps publisher. Errors are logged; the caller
// has already responded 202 Accepted so there is no further HTTP feedback.
// When no publisher is configured the function is a no-op.
func (h *envConfigHandler) scheduleRepublish(projectName, appName string) {
	if h.publisher == nil {
		return
	}
	go func() {
		ctx := context.Background()
		app, err := h.appStore.GetApp(ctx, projectName, appName)
		if err != nil {
			h.logger.Error("republish: failed to reload app", "project", projectName, "app", appName, "err", err)
			return
		}
		envs, err := h.appStore.ListAppEnvironments(ctx, projectName, appName)
		if err != nil {
			h.logger.Error("republish: failed to list app environments", "project", projectName, "app", appName, "err", err)
			return
		}
		if err := h.publisher.PublishApp(ctx, app, envs); err != nil {
			h.logger.Error("republish: publisher failed", "project", projectName, "app", appName, "err", err)
		}
	}()
}

// scheduleRepublishAllApps republishes every app in every project. Used after
// org-level or env-type-level env-var changes since those scopes reach every
// app. The publisher's commitAndPush is a no-op when nothing changed, so apps
// untouched by the change produce no commit. Errors per app are logged but do
// not stop the fan-out.
func (h *envConfigHandler) scheduleRepublishAllApps(reason string) {
	if h.publisher == nil || h.projectStore == nil {
		return
	}
	go func() {
		ctx := context.Background()
		projects, err := h.projectStore.List(ctx)
		if err != nil {
			h.logger.Error("republish: failed to list projects", "reason", reason, "err", err)
			return
		}
		for _, p := range projects {
			h.republishProjectApps(ctx, p.Metadata.Name, nil, reason)
		}
	}()
}

// scheduleRepublishProjectApps republishes every app in one project. Used
// after a project-level env-var change.
func (h *envConfigHandler) scheduleRepublishProjectApps(projectName, reason string) {
	if h.publisher == nil {
		return
	}
	go func() {
		h.republishProjectApps(context.Background(), projectName, nil, reason)
	}()
}

// scheduleRepublishClusterApps republishes apps whose environment list
// references one of the env names bound to clusterName. Used after a
// cluster-level env-var change so the fan-out only touches apps actually
// deployed to that cluster.
//
// The cluster → env-name mapping is resolved from the org's Environments list
// at fan-out time, so the cluster's current bindings are always honoured.
// scheduleRepublishEnvApps republishes, in the background, every app that
// deploys to envName, so an env cluster-binding change (ClusterRefs /
// ActiveClusterRef / DeployMode) re-resolves each app's per-cluster targets:
// newly-selected clusters get an Application, de-selected ones are pruned. Env
// binding edits are rare admin actions, so the fleet-scale fan-out is fine; it's
// best-effort (a per-app publish failure is logged, not surfaced). No-op when the
// publisher/projectStore isn't wired.
func (h *envConfigHandler) scheduleRepublishEnvApps(envName string) {
	if h.publisher == nil || h.projectStore == nil {
		return
	}
	go func() {
		ctx := context.Background()
		projects, err := h.projectStore.List(ctx)
		if err != nil {
			h.logger.Error("republish: failed to list projects for env fan-out", "env", envName, "err", err)
			return
		}
		filter := map[string]bool{envName: true}
		reason := "env=" + envName
		for _, p := range projects {
			h.republishProjectApps(ctx, p.Metadata.Name, filter, reason)
		}
	}()
}

func (h *envConfigHandler) scheduleRepublishClusterApps(clusterName string) {
	if h.publisher == nil || h.projectStore == nil || h.orgStore == nil {
		return
	}
	go func() {
		ctx := context.Background()
		org, err := h.orgStore.GetOrg(ctx)
		if err != nil {
			h.logger.Error("republish: failed to load org for cluster fan-out", "cluster", clusterName, "err", err)
			return
		}
		// Any env that has this cluster in its registered ClusterRefs is
		// affected by a cluster-scoped config change, regardless of whether
		// it's the active deploy target right now.
		envNames := make(map[string]bool)
		for _, e := range org.Environments {
			for _, c := range e.ClusterRefs {
				if c == clusterName {
					envNames[e.Name] = true
					break
				}
			}
		}
		if len(envNames) == 0 {
			h.logger.Info("republish: no envs bound to cluster, nothing to republish", "cluster", clusterName)
			return
		}
		projects, err := h.projectStore.List(ctx)
		if err != nil {
			h.logger.Error("republish: failed to list projects for cluster fan-out", "cluster", clusterName, "err", err)
			return
		}
		reason := "cluster=" + clusterName
		for _, p := range projects {
			h.republishProjectApps(ctx, p.Metadata.Name, envNames, reason)
		}
	}()
}

// republishProjectApps re-publishes every app in projectName. When envFilter
// is non-nil, only apps owning at least one matching env are republished —
// used by the cluster-scope fan-out.
func (h *envConfigHandler) republishProjectApps(ctx context.Context, projectName string, envFilter map[string]bool, reason string) {
	apps, err := h.appStore.ListApps(ctx, projectName)
	if err != nil {
		h.logger.Error("republish: failed to list apps", "project", projectName, "reason", reason, "err", err)
		return
	}
	for _, app := range apps {
		envs, err := h.appStore.ListAppEnvironments(ctx, projectName, app.Name)
		if err != nil {
			h.logger.Error("republish: failed to list app envs", "project", projectName, "app", app.Name, "reason", reason, "err", err)
			continue
		}
		if envFilter != nil {
			match := false
			for _, env := range envs {
				if envFilter[env.EnvName] {
					match = true
					break
				}
			}
			if !match {
				continue
			}
		}
		if err := h.publisher.PublishApp(ctx, app, envs); err != nil {
			h.logger.Error("republish: publisher failed", "project", projectName, "app", app.Name, "reason", reason, "err", err)
		}
	}
}

// ── Config-variable catalog (for the UI variable picker) ─────────────────────

// VarTokenDTO is one referenceable ((vars.NAME)) token, scope-labelled.
type VarTokenDTO struct {
	Token string `json:"token"`
	Name  string `json:"name"`
	Scope string `json:"scope"`
}

// ConfigVariablesResponse is the JSON body for
// GET /api/v1/projects/{project}/config-variables. It powers the UI's "Insert
// variable" picker: platform identity/routing tokens (static) plus the
// non-secret env-var tokens drawn from the org/env/project/cluster scopes.
// Secrets are NEVER included.
type ConfigVariablesResponse struct {
	Platform []platform.TokenInfo `json:"platform"`
	Vars     []VarTokenDTO        `json:"vars"`
}

// handleGetConfigVariables returns the variable catalog for the given project:
// the static ((platform.*)) tokens and the union of non-secret ((vars.*)) keys from
// org, each environment, the project, and each bound cluster. Resolution of the
// actual values happens per (env, cluster) at publish time; this only advertises
// the names. Project-read authorized (registered with viewProject).
func (h *envConfigHandler) handleGetConfigVariables(w http.ResponseWriter, r *http.Request) {
	projectName := r.PathValue("project")
	ctx := r.Context()

	org, err := h.orgStore.GetOrg(ctx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to load org"})
		return
	}
	includeProject := ""
	if proj, err := h.projectStore.Get(ctx, projectName); err == nil && proj != nil {
		includeProject = projectName
	}
	resp := ConfigVariablesResponse{
		Platform: platform.PlatformTokens(),
		Vars:     h.collectConfigVars(ctx, org, includeProject),
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleGetPlatformConfigVariables returns the project-agnostic variable catalog:
// the static ((platform.*)) tokens plus org/env/cluster-scoped ((vars.*)) (no
// project/app scope). Powers the "Insert variable" picker in the template-level
// platform-overrides editor, which has no project context. Authenticated.
func (h *envConfigHandler) handleGetPlatformConfigVariables(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	org, err := h.orgStore.GetOrg(ctx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to load org"})
		return
	}
	resp := ConfigVariablesResponse{
		Platform: platform.PlatformTokens(),
		Vars:     h.collectConfigVars(ctx, org, ""),
	}
	writeJSON(w, http.StatusOK, resp)
}

// collectConfigVars unions the non-secret ((vars.*)) names across scopes (org, each
// env, optionally the project, each bound cluster), deduped by first-seen so the
// picker shows where each name comes from. A non-empty projectName adds the
// project scope; "" omits it (project-agnostic / template-level catalog).
func (h *envConfigHandler) collectConfigVars(ctx context.Context, org *rbac.Org, projectName string) []VarTokenDTO {
	out := []VarTokenDTO{}
	seen := map[string]bool{}
	add := func(scope string, vars map[string]string) {
		names := make([]string, 0, len(vars))
		for k := range vars {
			names = append(names, k)
		}
		sortStrings(names)
		for _, name := range names {
			if seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, VarTokenDTO{Token: "((vars." + name + "))", Name: name, Scope: scope})
		}
	}

	add("org", org.EnvConfig.Vars)
	for _, e := range org.Environments {
		add("env:"+e.Name, e.EnvConfig.Vars)
	}
	if projectName != "" {
		if proj, err := h.projectStore.Get(ctx, projectName); err == nil && proj != nil {
			add("project", proj.Spec.EnvConfig.Vars)
			for _, e := range org.Environments {
				add("project:"+e.Name, proj.Spec.EnvConfigByEnv[e.Name].Vars)
			}
		}
		// Stack scope: each stack in the project contributes its global +
		// per-env vars, scope-labelled so the picker shows where each comes from.
		if h.stackStore != nil {
			if stacks, err := h.stackStore.ListStacks(ctx, projectName); err == nil {
				for _, st := range stacks {
					add("stack:"+st.Name, st.Spec.EnvConfig.Vars)
					for _, e := range org.Environments {
						add("stack:"+st.Name+":"+e.Name, st.Spec.EnvConfigByEnv[e.Name].Vars)
					}
				}
			}
		}
	}
	// Cluster scope: read each distinct bound cluster's runtime env ConfigMap.
	if h.upperLevelWriter != nil {
		clusterSeen := map[string]bool{}
		for _, e := range org.Environments {
			for _, c := range clusterRefsOf(e) {
				if c == "" {
					continue
				}
				// Per-(cluster, env) vars: read once per (cluster, env) pair.
				if cfg, err := h.upperLevelWriter.ReadClusterEnvScopedConfig(ctx, c, e.Name); err == nil {
					add("cluster:"+c+":"+e.Name, cfg.Vars)
				}
				if clusterSeen[c] {
					continue
				}
				clusterSeen[c] = true
				if cfg, err := h.upperLevelWriter.ReadClusterEnvConfig(ctx, c); err == nil {
					add("cluster:"+c, cfg.Vars)
				}
			}
		}
	}
	return out
}

// clusterRefsOf returns the cluster refs an environment is bound to, covering
// both the fan-out list and the active ref.
func clusterRefsOf(e rbac.OrgEnvironment) []string {
	refs := append([]string{}, e.ClusterRefs...)
	if e.ActiveClusterRef != "" {
		refs = append(refs, e.ActiveClusterRef)
	}
	return refs
}

// sortStrings sorts a string slice in-place (imported from sort package via
// a thin wrapper to avoid polluting handler logic with sort import boilerplate).
func sortStrings(ss []string) {
	// Using a simple insertion sort avoids importing "sort" solely for this.
	// For the small slices expected here (env vars, typically < 200) this is fine.
	for i := 1; i < len(ss); i++ {
		for j := i; j > 0 && ss[j] < ss[j-1]; j-- {
			ss[j], ss[j-1] = ss[j-1], ss[j]
		}
	}
}

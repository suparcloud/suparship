package server

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/rbac"
	"github.com/suparcloud/suparship/internal/secrets"
)

// ── DTOs ─────────────────────────────────────────────────────────────────────

// SecretBackendDTO is the JSON body for GET/PUT /api/v1/org/secrets-backend.
type SecretBackendDTO struct {
	Type string `json:"type"`
}

// SecretKeyDTO is one entry in the key-only list returned by GET .../secrets.
type SecretKeyDTO struct {
	Key string `json:"key"`
}

// SecretKeysResponse is the JSON body for GET .../secrets.
type SecretKeysResponse struct {
	Keys       []SecretKeyDTO `json:"keys"`
	SecretName string         `json:"secretName"`
}

// UpsertSecretsRequest is the JSON body for POST .../secrets.
type UpsertSecretsRequest struct {
	// Entries maps env-var names to their plaintext values.
	Entries map[string]string `json:"entries"`
}

// ResolvedSecretDTO is one entry in the resolved secrets response.
type ResolvedSecretDTO struct {
	Key    string `json:"key"`
	Source string `json:"source"`
}

// ResolvedSecretsResponse is the JSON body for GET .../secrets/resolved.
type ResolvedSecretsResponse struct {
	Secrets []ResolvedSecretDTO `json:"secrets"`
}

// ── Handler ───────────────────────────────────────────────────────────────────

type secretsHandler struct {
	orgStore    rbac.OrgStore
	appStore    domain.AppStore
	backend     secrets.Backend
	upperWriter secrets.UpperLevelWriter
	logger      *slog.Logger
}

// ── Org backend config ────────────────────────────────────────────────────────

func (h *secretsHandler) handleGetSecretsBackend(w http.ResponseWriter, r *http.Request) {
	org, err := h.orgStore.GetOrg(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to load org"})
		return
	}
	writeJSON(w, http.StatusOK, SecretBackendDTO{
		Type: string(org.SecretBackend.Effective()),
	})
}

func (h *secretsHandler) handlePutSecretsBackend(w http.ResponseWriter, r *http.Request) {
	var dto SecretBackendDTO
	if err := json.NewDecoder(r.Body).Decode(&dto); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}

	bt := secrets.BackendType(dto.Type)
	if !secrets.ValidBackendTypes[bt] {
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: "unsupported backend type: " + dto.Type})
		return
	}

	org, err := h.orgStore.GetOrg(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to load org"})
		return
	}

	org.SecretBackend = secrets.BackendConfig{Type: bt}
	if err := h.orgStore.SaveOrg(r.Context(), org); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to save org"})
		return
	}

	writeJSON(w, http.StatusOK, SecretBackendDTO{Type: dto.Type})
}

// ── Org-level secrets CRUD ──────────────────────────────────────────────────

func (h *secretsHandler) handleListOrgSecrets(w http.ResponseWriter, r *http.Request) {
	entries, err := h.upperWriter.ReadOrgSecretKeys(r.Context())
	if err != nil {
		h.logger.Error("failed to list org secret keys", "err", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to list secrets"})
		return
	}
	writeJSON(w, http.StatusOK, secretKeysResponseFromEntries(entries, secrets.OrgSecretName()))
}

func (h *secretsHandler) handleUpsertOrgSecrets(w http.ResponseWriter, r *http.Request) {
	req, ok := h.decodeUpsertRequest(w, r)
	if !ok {
		return
	}
	if err := h.upperWriter.WriteOrgSecrets(r.Context(), toByteMap(req.Entries)); err != nil {
		h.logger.Error("failed to upsert org secrets", "err", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to save secrets"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *secretsHandler) handleDeleteOrgSecret(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if err := h.upperWriter.DeleteOrgSecretKey(r.Context(), key); err != nil {
		h.logger.Error("failed to delete org secret key", "key", key, "err", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to delete secret"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ── Env-type-level secrets CRUD ─────────────────────────────────────────────

func (h *secretsHandler) handleListEnvTypeSecrets(w http.ResponseWriter, r *http.Request) {
	envType := r.PathValue("envtype")
	entries, err := h.upperWriter.ReadEnvTypeSecretKeys(r.Context(), envType)
	if err != nil {
		h.logger.Error("failed to list env-type secret keys", "envtype", envType, "err", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to list secrets"})
		return
	}
	writeJSON(w, http.StatusOK, secretKeysResponseFromEntries(entries, secrets.EnvTypeSecretName(envType)))
}

func (h *secretsHandler) handleUpsertEnvTypeSecrets(w http.ResponseWriter, r *http.Request) {
	envType := r.PathValue("envtype")
	req, ok := h.decodeUpsertRequest(w, r)
	if !ok {
		return
	}
	if err := h.upperWriter.WriteEnvTypeSecrets(r.Context(), envType, toByteMap(req.Entries)); err != nil {
		h.logger.Error("failed to upsert env-type secrets", "envtype", envType, "err", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to save secrets"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *secretsHandler) handleDeleteEnvTypeSecret(w http.ResponseWriter, r *http.Request) {
	envType := r.PathValue("envtype")
	key := r.PathValue("key")
	if err := h.upperWriter.DeleteEnvTypeSecretKey(r.Context(), envType, key); err != nil {
		h.logger.Error("failed to delete env-type secret key", "envtype", envType, "key", key, "err", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to delete secret"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ── Project-level secrets CRUD ──────────────────────────────────────────────

func (h *secretsHandler) handleListProjectSecrets(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	entries, err := h.upperWriter.ReadProjectSecretKeys(r.Context(), project)
	if err != nil {
		h.logger.Error("failed to list project secret keys", "project", project, "err", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to list secrets"})
		return
	}
	writeJSON(w, http.StatusOK, secretKeysResponseFromEntries(entries, secrets.ProjectSecretName(project)))
}

func (h *secretsHandler) handleUpsertProjectSecrets(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	req, ok := h.decodeUpsertRequest(w, r)
	if !ok {
		return
	}
	if err := h.upperWriter.WriteProjectSecrets(r.Context(), project, toByteMap(req.Entries)); err != nil {
		h.logger.Error("failed to upsert project secrets", "project", project, "err", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to save secrets"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *secretsHandler) handleDeleteProjectSecret(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	key := r.PathValue("key")
	if err := h.upperWriter.DeleteProjectSecretKey(r.Context(), project, key); err != nil {
		h.logger.Error("failed to delete project secret key", "project", project, "key", key, "err", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to delete secret"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ── App-level secrets CRUD ──────────────────────────────────────────────────

func (h *secretsHandler) handleListAppSecrets(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	appName := r.PathValue("app")

	ns, err := h.resolveAnyAppNamespace(r, project, appName)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: err.Error()})
		return
	}

	secretName := secrets.AppLevelSecretName(project, appName)
	entries, err := h.backend.ListKeys(r.Context(), ns, secretName)
	if err != nil {
		h.logger.Error("failed to list app secret keys", "project", project, "app", appName, "err", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to list secrets"})
		return
	}
	writeJSON(w, http.StatusOK, secretKeysResponseFromEntries(entries, secretName))
}

func (h *secretsHandler) handleUpsertAppSecrets(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	appName := r.PathValue("app")

	req, ok := h.decodeUpsertRequest(w, r)
	if !ok {
		return
	}

	ns, err := h.resolveAnyAppNamespace(r, project, appName)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: err.Error()})
		return
	}

	secretName := secrets.AppLevelSecretName(project, appName)
	if err := h.backend.Upsert(r.Context(), ns, secretName, toByteMap(req.Entries)); err != nil {
		h.logger.Error("failed to upsert app secrets", "project", project, "app", appName, "err", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to save secrets"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *secretsHandler) handleDeleteAppSecret(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	appName := r.PathValue("app")
	key := r.PathValue("key")

	ns, err := h.resolveAnyAppNamespace(r, project, appName)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: err.Error()})
		return
	}

	secretName := secrets.AppLevelSecretName(project, appName)
	if err := h.backend.DeleteKey(r.Context(), ns, secretName, key); err != nil {
		h.logger.Error("failed to delete app secret key", "project", project, "app", appName, "key", key, "err", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to delete secret"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ── App-env secrets CRUD (existing) ─────────────────────────────────────────

func (h *secretsHandler) handleListSecrets(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	appName := r.PathValue("app")
	envName := r.PathValue("env")

	ns, err := h.resolveNamespace(r, project, appName, envName)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: err.Error()})
		return
	}

	secretName := secrets.AppEnvSecretName(project, appName, envName)

	entries, err := h.backend.ListKeys(r.Context(), ns, secretName)
	if err != nil {
		h.logger.Error("failed to list secret keys", "ns", ns, "secret", secretName, "err", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to list secrets"})
		return
	}
	writeJSON(w, http.StatusOK, secretKeysResponseFromEntries(entries, secretName))
}

func (h *secretsHandler) handleUpsertSecrets(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	appName := r.PathValue("app")
	envName := r.PathValue("env")

	req, ok := h.decodeUpsertRequest(w, r)
	if !ok {
		return
	}

	ns, err := h.resolveNamespace(r, project, appName, envName)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: err.Error()})
		return
	}

	secretName := secrets.AppEnvSecretName(project, appName, envName)

	if err := h.backend.Upsert(r.Context(), ns, secretName, toByteMap(req.Entries)); err != nil {
		h.logger.Error("failed to upsert secrets", "ns", ns, "secret", secretName, "err", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to save secrets"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *secretsHandler) handleDeleteSecret(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	appName := r.PathValue("app")
	envName := r.PathValue("env")
	key := r.PathValue("key")

	ns, err := h.resolveNamespace(r, project, appName, envName)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: err.Error()})
		return
	}

	secretName := secrets.AppEnvSecretName(project, appName, envName)

	if err := h.backend.DeleteKey(r.Context(), ns, secretName, key); err != nil {
		h.logger.Error("failed to delete secret key", "ns", ns, "secret", secretName, "key", key, "err", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to delete secret"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ── Resolved secrets ────────────────────────────────────────────────────────

func (h *secretsHandler) handleGetResolvedSecrets(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	appName := r.PathValue("app")
	envName := r.PathValue("env")

	ctx := r.Context()

	// Collect keys from all 5 levels.
	orgKeys, err := h.upperWriter.ReadOrgSecretKeys(ctx)
	if err != nil {
		h.logger.Error("failed to read org secret keys", "err", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to resolve secrets"})
		return
	}

	// Determine env type for env-type level lookup.
	envType := h.resolveEnvType(r, project, appName, envName)
	envTypeKeys, err := h.upperWriter.ReadEnvTypeSecretKeys(ctx, envType)
	if err != nil {
		h.logger.Error("failed to read env-type secret keys", "envtype", envType, "err", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to resolve secrets"})
		return
	}

	projectKeys, err := h.upperWriter.ReadProjectSecretKeys(ctx, project)
	if err != nil {
		h.logger.Error("failed to read project secret keys", "project", project, "err", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to resolve secrets"})
		return
	}

	// App-level secrets are stored in the app namespace.
	appNs, _ := h.resolveAnyAppNamespace(r, project, appName)
	var appKeys []secrets.SecretEntry
	if appNs != "" {
		appKeys, err = h.backend.ListKeys(ctx, appNs, secrets.AppLevelSecretName(project, appName))
		if err != nil {
			h.logger.Error("failed to read app secret keys", "project", project, "app", appName, "err", err)
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to resolve secrets"})
			return
		}
	}

	// App-env-level secrets.
	ns, _ := h.resolveNamespace(r, project, appName, envName)
	var appEnvKeys []secrets.SecretEntry
	if ns != "" {
		appEnvKeys, err = h.backend.ListKeys(ctx, ns, secrets.AppEnvSecretName(project, appName, envName))
		if err != nil {
			h.logger.Error("failed to read app-env secret keys", "project", project, "app", appName, "env", envName, "err", err)
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to resolve secrets"})
			return
		}
	}

	resolved := secrets.ResolveSecretLayers(
		entriesToKeys(orgKeys),
		entriesToKeys(envTypeKeys),
		entriesToKeys(projectKeys),
		entriesToKeys(appKeys),
		entriesToKeys(appEnvKeys),
	)

	// Build sorted response.
	sortedKeys := make([]string, 0, len(resolved))
	for k := range resolved {
		sortedKeys = append(sortedKeys, k)
	}
	sort.Strings(sortedKeys)

	dtos := make([]ResolvedSecretDTO, len(sortedKeys))
	for i, k := range sortedKeys {
		dtos[i] = ResolvedSecretDTO{Key: k, Source: resolved[k].Source}
	}

	writeJSON(w, http.StatusOK, ResolvedSecretsResponse{Secrets: dtos})
}

// ── Helpers ─────────────────────────────────────────────────────────────────

func (h *secretsHandler) resolveNamespace(r *http.Request, project, appName, envName string) (string, error) {
	env, err := h.appStore.GetAppEnvironment(r.Context(), project, appName, envName)
	if err != nil {
		return "", err
	}
	return env.Namespace, nil
}

// resolveAnyAppNamespace returns a namespace for app-level secrets by picking
// the first available environment's namespace.
func (h *secretsHandler) resolveAnyAppNamespace(r *http.Request, project, appName string) (string, error) {
	envs, err := h.appStore.ListAppEnvironments(r.Context(), project, appName)
	if err != nil {
		return "", err
	}
	if len(envs) == 0 {
		return "", fmt.Errorf("no environments found for app %s/%s", project, appName)
	}
	return envs[0].Namespace, nil
}

// resolveEnvType determines the env type string for env-type-level lookups.
func (h *secretsHandler) resolveEnvType(r *http.Request, project, appName, envName string) string {
	env, err := h.appStore.GetAppEnvironment(r.Context(), project, appName, envName)
	if err != nil {
		return envName
	}
	return string(env.EnvType)
}

func (h *secretsHandler) decodeUpsertRequest(w http.ResponseWriter, r *http.Request) (UpsertSecretsRequest, bool) {
	var req UpsertSecretsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return req, false
	}
	if len(req.Entries) == 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "entries must not be empty"})
		return req, false
	}
	return req, true
}

func secretKeysResponseFromEntries(entries []secrets.SecretEntry, secretName string) SecretKeysResponse {
	keys := make([]SecretKeyDTO, len(entries))
	for i, e := range entries {
		keys[i] = SecretKeyDTO{Key: e.Key}
	}
	return SecretKeysResponse{Keys: keys, SecretName: secretName}
}

func toByteMap(entries map[string]string) map[string][]byte {
	data := make(map[string][]byte, len(entries))
	for k, v := range entries {
		data[k] = []byte(v)
	}
	return data
}

func entriesToKeys(entries []secrets.SecretEntry) []string {
	keys := make([]string, len(entries))
	for i, e := range entries {
		keys[i] = e.Key
	}
	return keys
}

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"k8s.io/client-go/kubernetes"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/gitops"
	"github.com/suparcloud/suparship/internal/rbac"
	"github.com/suparcloud/suparship/internal/seal"
	"github.com/suparcloud/suparship/internal/secrets"
	"github.com/suparcloud/suparship/internal/secrets/onepassword"
)

// ── DTOs ─────────────────────────────────────────────────────────────────────

// SecretBackendDTO is the JSON body for GET/PUT /api/v1/org/secrets-backend.
type SecretBackendDTO struct {
	Type        string                     `json:"type"`
	OnePassword *secrets.OnePasswordConfig `json:"onePassword,omitempty"`
}

// SecretKeyDTO is one entry in the key-only list returned by GET .../secrets.
type SecretKeyDTO struct {
	Key string `json:"key"`
}

// SecretKeysResponse is the JSON body for GET .../secrets.
type SecretKeysResponse struct {
	Keys       []SecretKeyDTO `json:"keys"`
	SecretName string         `json:"secretName"`
	Version    string         `json:"version,omitempty"`
}

// UpsertSecretsRequest is the JSON body for POST .../secrets.
type UpsertSecretsRequest struct {
	Entries map[string]string `json:"entries"`
}

// ResolvedSecretDTO is one entry in the resolved secrets response.
type ResolvedSecretDTO struct {
	Key    string `json:"key"`
	Source string `json:"source"`
	Tier   string `json:"tier,omitempty"`
}

// ResolvedSecretsResponse is the JSON body for GET .../secrets/resolved.
type ResolvedSecretsResponse struct {
	Secrets []ResolvedSecretDTO `json:"secrets"`
}

// ── Collaborators ────────────────────────────────────────────────────────────

// SATokenStore abstracts the persistence of the SA token K8s Secret.
type SATokenStore interface {
	SaveToken(ctx context.Context, token string) error
	LoadToken(ctx context.Context) (string, error)
}

// SAClientFactory creates an SAClient from a token. Used for validation on paste.
type SAClientFactory func(ctx context.Context, token string) (onepassword.SAClient, error)

// SealedTokenPublisher publishes sealed Connect tokens to the GitOps repo.
type SealedTokenPublisher interface {
	PublishSealedReadToken(ctx context.Context, params gitops.SealedReadTokenPublishParams) error
	DeleteSealedReadToken(ctx context.Context, params gitops.DeleteSealedReadTokenParams) error
	RefreshSecretStore(ctx context.Context, params gitops.RefreshSecretStoreParams) error
}

// ClusterKubeBuilder builds a Kubernetes client for a registered cluster.
type ClusterKubeBuilder interface {
	BuildClient(ctx context.Context, clusterName string) (interface{ CoreV1() interface{} }, error)
}

// sealClientPool returns a Kubernetes client for a registered cluster so cert
// fetching can auto-fetch the sealed-secrets certificate on cache miss.
type sealClientPool interface {
	GetKubeClient(ctx context.Context, clusterName string) (kubernetes.Interface, error)
}

type secretsHandler struct {
	orgStore        rbac.OrgStore
	appStore        domain.AppStore
	vault           secrets.VaultStore
	auditor         *secrets.Auditor
	logger          *slog.Logger
	saTokenStore    SATokenStore
	saClientFactory SAClientFactory
	clusterStore    domain.ClusterStore
	certCache       seal.CertCache
	sealPublisher   SealedTokenPublisher
	clusterPool     sealClientPool
	kubeClient      kubernetes.Interface
}

// ── Org backend config ────────────────────────────────────────────────────────

func (h *secretsHandler) handleGetSecretsBackend(w http.ResponseWriter, r *http.Request) {
	org, err := h.orgStore.GetOrg(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to load org"})
		return
	}
	writeJSON(w, http.StatusOK, SecretBackendDTO{
		Type:        string(org.SecretBackend.Effective()),
		OnePassword: org.SecretBackend.OnePassword,
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
	newCfg := secrets.BackendConfig{Type: bt, OnePassword: dto.OnePassword}
	if err := newCfg.Validate(); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: err.Error()})
		return
	}
	org, err := h.orgStore.GetOrg(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to load org"})
		return
	}
	org.SecretBackend = newCfg
	if err := h.orgStore.SaveOrg(r.Context(), org); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to save org"})
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

func (h *secretsHandler) handleGetSecretsBackendFull(w http.ResponseWriter, r *http.Request) {
	org, err := h.orgStore.GetOrg(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to load org"})
		return
	}
	writeJSON(w, http.StatusOK, org.SecretBackend)
}

func (h *secretsHandler) handlePutSecretsBackendFull(w http.ResponseWriter, r *http.Request) {
	var cfg secrets.BackendConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}
	if err := cfg.Validate(); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: err.Error()})
		return
	}
	org, err := h.orgStore.GetOrg(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to load org"})
		return
	}
	org.SecretBackend = cfg
	if err := h.orgStore.SaveOrg(r.Context(), org); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to save org"})
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}

// ── SA token management ─────────────────────────────────────────────────────

// SATokenRequest is the JSON body for POST /api/v1/org/secret-backend/sa-token.
type SATokenRequest struct {
	Token string `json:"token"`
}

// SATokenResponse is the response after saving an SA token.
type SATokenResponse struct {
	Valid      bool   `json:"valid"`
	VaultCount int    `json:"vaultCount,omitempty"`
	Error      string `json:"error,omitempty"`
}

func (h *secretsHandler) handlePostSAToken(w http.ResponseWriter, r *http.Request) {
	var req SATokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}
	if req.Token == "" {
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: "token is required"})
		return
	}
	if h.saTokenStore != nil {
		if err := h.saTokenStore.SaveToken(r.Context(), req.Token); err != nil {
			h.logger.Error("failed to save SA token", "err", err)
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to persist token"})
			return
		}
	}
	if h.saClientFactory != nil {
		client, err := h.saClientFactory(r.Context(), req.Token)
		if err != nil {
			writeJSON(w, http.StatusOK, SATokenResponse{Valid: false, Error: err.Error()})
			return
		}
		count, err := client.Probe(r.Context())
		if err != nil {
			writeJSON(w, http.StatusOK, SATokenResponse{Valid: false, Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, SATokenResponse{Valid: true, VaultCount: count})
		return
	}
	writeJSON(w, http.StatusOK, SATokenResponse{Valid: true})
}

// ── Vault listing ───────────────────────────────────────────────────────────

// VaultInfoDTO is a single vault returned by the list-vaults endpoint.
type VaultInfoDTO struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

func (h *secretsHandler) handleListVaults(w http.ResponseWriter, r *http.Request) {
	if h.saTokenStore == nil || h.saClientFactory == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "SA token not configured"})
		return
	}
	token, err := h.saTokenStore.LoadToken(r.Context())
	if err != nil || token == "" {
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: "SA token not saved yet; paste it in Settings first"})
		return
	}
	client, err := h.saClientFactory(r.Context(), token)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to create 1Password client: " + err.Error()})
		return
	}
	vaults, err := client.ListVaults(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to list vaults: " + err.Error()})
		return
	}
	dtos := make([]VaultInfoDTO, len(vaults))
	for i, v := range vaults {
		dtos[i] = VaultInfoDTO{ID: v.ID, Title: v.Title}
	}
	writeJSON(w, http.StatusOK, dtos)
}

// ── Global vault selection ────────────────────────────────────────────────────

// SetGlobalVaultRequest is the JSON body for PUT .../secret-backend/global-vault.
type SetGlobalVaultRequest struct {
	VaultID   string `json:"vaultId"`
	VaultName string `json:"vaultName,omitempty"`
}

// handleSetGlobalVault persists the operator's choice of global vault (the
// 1Password vault holding global-scope shared + per-app items). The vault must
// already exist and be visible to the stored SA token.
func (h *secretsHandler) handleSetGlobalVault(w http.ResponseWriter, r *http.Request) {
	var req SetGlobalVaultRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}
	if req.VaultID == "" {
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: "vaultId is required"})
		return
	}
	ctx := r.Context()
	org, err := h.orgStore.GetOrg(ctx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to load org"})
		return
	}
	if org.SecretBackend.Effective() != secrets.Backend1Password {
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: "global vault can only be set when the 1Password backend is selected"})
		return
	}
	if h.saTokenStore == nil || h.saClientFactory == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "1Password client not configured"})
		return
	}
	token, err := h.saTokenStore.LoadToken(ctx)
	if err != nil || token == "" {
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: "SA token not saved yet — paste it first"})
		return
	}
	client, err := h.saClientFactory(ctx, token)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to create 1Password client: " + err.Error()})
		return
	}
	info, err := client.GetVault(ctx, req.VaultID)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: "vault not accessible to the stored SA token: " + err.Error()})
		return
	}
	resolvedName := info.Title
	if req.VaultName != "" {
		resolvedName = req.VaultName
	}
	org.SecretBackend.UpsertVault(secrets.GlobalScope(), secrets.VaultRef{
		VaultID:   info.ID,
		VaultName: resolvedName,
	})
	if err := h.orgStore.SaveOrg(ctx, org); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to persist org"})
		return
	}
	h.logger.Info("global vault set", "vaultID", info.ID, "vaultTitle", resolvedName)
	writeJSON(w, http.StatusOK, map[string]string{"vaultId": info.ID, "vaultName": resolvedName})
}

// ── Env/cluster vault provisioning (1Password) ──────────────────────────────

// RegisterVaultRequest registers a 1Password vault for an env or cluster scope
// and (when implemented) seals its Connect token for the target cluster.
type RegisterVaultRequest struct {
	VaultID         string `json:"vaultId"`
	VaultName       string `json:"vaultName"`
	ConnectToken    string `json:"connectToken"`
	ConnectEndpoint string `json:"connectEndpoint,omitempty"`
}

// handleRegisterEnvVault is the env-scope vault provisioning entrypoint.
//
// TODO(5b): seal the Connect token with the target cluster's sealing cert and
// publish the per-cluster ClusterSecretStore + ArgoCD Application via
// sealPublisher, mirroring the legacy binding flow but for the env-scope vault.
func (h *secretsHandler) handleRegisterEnvVault(w http.ResponseWriter, r *http.Request) {
	h.stubProvisioning(w, "env", r.PathValue("env"))
}

// handleRegisterClusterVault is the cluster-scope vault provisioning entrypoint.
//
// TODO(5b): seal + publish the cluster-scope vault's Connect token. Cluster-scope
// sealing is new in the 3-scope model and has no legacy equivalent.
func (h *secretsHandler) handleRegisterClusterVault(w http.ResponseWriter, r *http.Request) {
	h.stubProvisioning(w, "cluster", r.PathValue("cluster"))
}

func (h *secretsHandler) stubProvisioning(w http.ResponseWriter, scopeKind, key string) {
	h.logger.Warn("1Password vault provisioning not yet implemented", "scope", scopeKind, "key", key)
	writeJSON(w, http.StatusNotImplemented, errorResponse{
		Error: "1Password Connect-token sealing/provisioning is not yet implemented in the 3-scope model (tracked as a follow-up); the k8s backend is fully supported",
	})
}

// ── Generic secret CRUD across scopes/tiers ──────────────────────────────────

// scopeFromPath derives the secret Scope from the request path variables.
func scopeFromPath(r *http.Request) secrets.Scope {
	if c := r.PathValue("cluster"); c != "" {
		return secrets.ClusterScope(c)
	}
	if e := r.PathValue("env"); e != "" {
		return secrets.EnvScope(e)
	}
	return secrets.GlobalScope()
}

// tierAndApp derives the Tier and app name: app tier when {app} is present in
// the path, shared tier otherwise.
func tierAndApp(r *http.Request) (secrets.Tier, string) {
	if a := r.PathValue("app"); a != "" {
		return secrets.TierApp, a
	}
	return secrets.TierShared, ""
}

func (h *secretsHandler) handleListSecrets(w http.ResponseWriter, r *http.Request) {
	scope := scopeFromPath(r)
	tier, app := tierAndApp(r)
	entries, err := h.vault.ListKeys(r.Context(), scope, tier, app)
	if err != nil {
		h.logger.Error("failed to list secret keys", "scope", scope.Kind, "tier", tier, "err", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to list secrets: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, secretKeysResponseFromEntries(entries, secrets.ItemName(scope, tier, app)))
}

func (h *secretsHandler) handleUpsertSecrets(w http.ResponseWriter, r *http.Request) {
	scope := scopeFromPath(r)
	tier, app := tierAndApp(r)
	req, ok := h.decodeUpsertRequest(w, r)
	if !ok {
		return
	}
	if err := h.vault.Upsert(r.Context(), scope, tier, app, toByteMap(req.Entries)); err != nil {
		h.logger.Error("failed to upsert secrets", "scope", scope.Kind, "tier", tier, "err", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to save secrets: " + err.Error()})
		return
	}
	h.audit(r, secrets.AuditActionUpsert, scope, tier, app, keysOf(req.Entries))
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *secretsHandler) handleDeleteSecret(w http.ResponseWriter, r *http.Request) {
	scope := scopeFromPath(r)
	tier, app := tierAndApp(r)
	key := r.PathValue("key")
	if err := h.vault.DeleteKey(r.Context(), scope, tier, app, key); err != nil {
		h.logger.Error("failed to delete secret key", "scope", scope.Kind, "tier", tier, "key", key, "err", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to delete secret: " + err.Error()})
		return
	}
	h.audit(r, secrets.AuditActionDelete, scope, tier, app, []string{key})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ── Resolved view ────────────────────────────────────────────────────────────

func (h *secretsHandler) handleGetResolvedSecrets(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	appName := r.PathValue("app")
	envName := r.PathValue("env")
	ctx := r.Context()

	read := func(scope secrets.Scope) secrets.ScopeKeys {
		shared, err := h.vault.ListKeys(ctx, scope, secrets.TierShared, "")
		if err != nil {
			h.logger.Warn("resolved: shared read failed", "scope", scope.Kind, "err", err)
		}
		app, err := h.vault.ListKeys(ctx, scope, secrets.TierApp, appName)
		if err != nil {
			h.logger.Warn("resolved: app read failed", "scope", scope.Kind, "err", err)
		}
		return secrets.ScopeKeys{Shared: entriesToKeys(shared), App: entriesToKeys(app)}
	}

	global := read(secrets.GlobalScope())
	env := read(secrets.EnvScope(envName))
	var cluster secrets.ScopeKeys
	if clusterRef := h.resolveClusterRef(r, envName); clusterRef != "" {
		cluster = read(secrets.ClusterScope(clusterRef))
	}
	_ = project

	resolved := secrets.ResolveScopes(global, env, cluster)
	sortedKeys := make([]string, 0, len(resolved))
	for k := range resolved {
		sortedKeys = append(sortedKeys, k)
	}
	sort.Strings(sortedKeys)
	dtos := make([]ResolvedSecretDTO, len(sortedKeys))
	for i, k := range sortedKeys {
		dtos[i] = ResolvedSecretDTO{Key: k, Source: resolved[k].Source, Tier: resolved[k].Tier}
	}
	writeJSON(w, http.StatusOK, ResolvedSecretsResponse{Secrets: dtos})
}

// ── Force-sync endpoint ─────────────────────────────────────────────────────

func (h *secretsHandler) handleSecretSync(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	appName := r.PathValue("app")
	token := fmt.Sprintf("%d", time.Now().UnixNano())
	if h.auditor != nil {
		h.auditor.Log(secrets.AuditEvent{
			Timestamp: time.Now(),
			Actor:     sessionFromContext(r.Context()).Username,
			Action:    "sync",
			App:       appName,
			Result:    "ok",
		})
	}
	h.logger.Info("secrets sync triggered", "project", project, "app", appName, "syncToken", token)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "syncToken": token})
}

// ── Cert helpers (used by 1P provisioning, 5b) ───────────────────────────────

func (h *secretsHandler) fetchFreshCert(ctx context.Context, clusterName string) ([]byte, error) {
	if h.clusterPool != nil && h.certCache != nil {
		kubeClient, err := h.clusterPool.GetKubeClient(ctx, clusterName)
		if err == nil {
			pemBytes, ferr := seal.FetchAndCache(ctx, h.certCache, kubeClient, clusterName, seal.FetchOptions{})
			if ferr == nil {
				return pemBytes, nil
			}
			h.logger.Warn("live cert fetch failed, falling back to cache", "cluster", clusterName, "err", ferr)
		}
	}
	return h.fetchOrLoadCert(ctx, clusterName)
}

func (h *secretsHandler) fetchOrLoadCert(ctx context.Context, clusterName string) ([]byte, error) {
	if h.certCache == nil {
		return nil, fmt.Errorf("cert cache not configured")
	}
	pemBytes, err := h.certCache.Get(ctx, clusterName)
	if err == nil {
		return pemBytes, nil
	}
	if h.clusterPool == nil {
		return nil, fmt.Errorf("sealed-secrets certificate not cached for cluster %q and no cluster pool available", clusterName)
	}
	kubeClient, err := h.clusterPool.GetKubeClient(ctx, clusterName)
	if err != nil {
		return nil, fmt.Errorf("building kube client for cluster %q to fetch sealing cert: %w", clusterName, err)
	}
	pemBytes, err = seal.FetchAndCache(ctx, h.certCache, kubeClient, clusterName, seal.FetchOptions{})
	if err != nil {
		return nil, fmt.Errorf("fetching sealing cert from cluster %q: %w", clusterName, err)
	}
	return pemBytes, nil
}

// resolveConnectEndpoint returns the Connect server URL to use for sealing.
func resolveConnectEndpoint(reqEndpoint string, org *rbac.Org) string {
	if reqEndpoint != "" {
		return reqEndpoint
	}
	if org.SecretBackend.OnePassword != nil {
		return org.SecretBackend.OnePassword.Connect.Endpoint
	}
	return ""
}

// ── Helpers ─────────────────────────────────────────────────────────────────

func (h *secretsHandler) resolveClusterRef(r *http.Request, envName string) string {
	if h.orgStore == nil {
		return ""
	}
	org, err := h.orgStore.GetOrg(r.Context())
	if err != nil || org == nil {
		return ""
	}
	for _, e := range org.Environments {
		if e.Name == envName {
			return e.EffectiveClusterRef()
		}
	}
	return ""
}

func (h *secretsHandler) audit(r *http.Request, action secrets.AuditAction, scope secrets.Scope, tier secrets.Tier, app string, keys []string) {
	if h.auditor == nil {
		return
	}
	var actor string
	if sess := sessionFromContext(r.Context()); sess != nil {
		actor = sess.Username
	}
	h.auditor.Log(secrets.AuditEvent{
		Timestamp: time.Now(),
		Actor:     actor,
		Action:    action,
		Scope:     scope,
		Tier:      tier,
		App:       app,
		Keys:      keys,
		Result:    "ok",
	})
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

func keysOf(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

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
}

// ResolvedSecretsResponse is the JSON body for GET .../secrets/resolved.
type ResolvedSecretsResponse struct {
	Secrets []ResolvedSecretDTO `json:"secrets"`
}

// ── Handler ───────────────────────────────────────────────────────────────────

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
}

// ClusterKubeBuilder builds a Kubernetes client for a registered cluster.
type ClusterKubeBuilder interface {
	BuildClient(ctx context.Context, clusterName string) (interface{ CoreV1() interface{} }, error)
}

// sealClientPool returns a Kubernetes client for a registered cluster so that
// fetchOrLoadCert can auto-fetch the sealed-secrets certificate on cache miss.
// Implemented by *k8s.ClusterClientPool via the adapter in server.go.
type sealClientPool interface {
	GetKubeClient(ctx context.Context, clusterName string) (kubernetes.Interface, error)
}

type secretsHandler struct {
	orgStore        rbac.OrgStore
	appStore        domain.AppStore
	backend         secrets.Backend
	upperWriter     secrets.UpperLevelWriter
	auditor         *secrets.Auditor
	logger          *slog.Logger
	saTokenStore    SATokenStore
	saClientFactory SAClientFactory
	clusterStore    domain.ClusterStore
	certCache       seal.CertCache
	sealPublisher   SealedTokenPublisher
	// clusterPool is used to build per-cluster Kubernetes clients for cert
	// fetching. When set, fetchOrLoadCert will auto-fetch the sealing cert
	// from the target cluster on cache miss instead of returning an error.
	clusterPool sealClientPool
}

// ── Org backend config ────────────────────────────────────────────────────────

func (h *secretsHandler) handleGetSecretsBackend(w http.ResponseWriter, r *http.Request) {
	org, err := h.orgStore.GetOrg(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to load org"})
		return
	}
	dto := SecretBackendDTO{
		Type:        string(org.SecretBackend.Effective()),
		OnePassword: org.SecretBackend.OnePassword,
	}
	writeJSON(w, http.StatusOK, dto)
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

	writeJSON(w, http.StatusOK, SecretBackendDTO{Type: dto.Type, OnePassword: dto.OnePassword})
}

// handleGetSecretsBackendFull returns the full backend config including
// 1Password state and env bindings. Used by the Settings UI.
func (h *secretsHandler) handleGetSecretsBackendFull(w http.ResponseWriter, r *http.Request) {
	org, err := h.orgStore.GetOrg(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to load org"})
		return
	}
	writeJSON(w, http.StatusOK, org.SecretBackend)
}

// handlePutSecretsBackendFull replaces the full backend config.
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
	Valid       bool   `json:"valid"`
	VaultCount int    `json:"vaultCount,omitempty"`
	Error      string `json:"error,omitempty"`
}

// handlePostSAToken stores the SA token and validates scope.
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

	// Persist the token to K8s Secret (delegated to the SA token store).
	if h.saTokenStore != nil {
		if err := h.saTokenStore.SaveToken(r.Context(), req.Token); err != nil {
			h.logger.Error("failed to save SA token", "err", err)
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to persist token"})
			return
		}
	}

	// Validate: probe the token to count accessible vaults.
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

// handleListVaults returns vaults visible to the stored SA token.
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

// ── Add / Remove binding ────────────────────────────────────────────────────

// AddBindingRequest is the JSON body for POST /api/v1/org/secret-backend/bindings.
type AddBindingRequest struct {
	Env             string `json:"env"`
	VaultID         string `json:"vaultId"`
	VaultName       string `json:"vaultName"`
	ConnectToken    string `json:"connectToken"`
	// ConnectEndpoint overrides the 1Password Connect server URL for this binding.
	// When empty, the stored org-level Connect endpoint is used, falling back to
	// the default (http://onepassword-connect.onepassword-connect.svc.cluster.local:8080).
	ConnectEndpoint string `json:"connectEndpoint,omitempty"`
}

// BindingResponse is the JSON response after adding or rotating a binding.
type BindingResponse struct {
	Env                    string `json:"env"`
	VaultID                string `json:"vaultId"`
	VaultName              string `json:"vaultName"`
	ClusterSecretStoreName string `json:"clusterSecretStoreName"`
	Provisioned            bool   `json:"provisioned"`
	Rotated                bool   `json:"rotated"`
	Error                  string `json:"error,omitempty"`
}

// handleAddBinding seals a Connect token and publishes GitOps files for an env.
func (h *secretsHandler) handleAddBinding(w http.ResponseWriter, r *http.Request) {
	var req AddBindingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}
	if req.Env == "" || req.VaultID == "" || req.ConnectToken == "" {
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: "env, vaultId, and connectToken are required"})
		return
	}

	ctx := r.Context()
	org, err := h.orgStore.GetOrg(ctx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to load org"})
		return
	}

	if org.SecretBackend.Effective() != secrets.Backend1Password {
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: "binding is only available for 1Password backend"})
		return
	}

	// Validate env exists in org environments.
	var orgEnv *rbac.OrgEnvironment
	for i := range org.Environments {
		if org.Environments[i].Name == req.Env {
			orgEnv = &org.Environments[i]
			break
		}
	}
	if orgEnv == nil {
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{
			Error: fmt.Sprintf("environment %q not found in org; create it in Settings > Environments first", req.Env),
		})
		return
	}

	rotated := org.SecretBackend.FindBinding(req.Env) != nil

	// Compute ClusterSecretStore name.
	naming := org.ResourceNaming
	storeName := naming.RenderClusterSecretStore(secrets.NamingParams{
		Provider: string(secrets.Backend1Password),
		Env:      req.Env,
		Org:      org.Name,
	})

	// Seal and publish to GitOps if publisher and cert cache are wired.
	if h.sealPublisher != nil && h.certCache != nil {
		clusterName := orgEnv.ClusterRef
		if clusterName == "" {
			writeJSON(w, http.StatusUnprocessableEntity, errorResponse{
				Error: fmt.Sprintf("environment %q has no cluster assigned; set clusterRef in Settings > Environments", req.Env),
			})
			return
		}

		// Always re-fetch the cert live from the target cluster at bind/rotate time.
		// The cache may be stale or hold a cert from the wrong cluster; a key mismatch
		// causes "could not decrypt" on the target regardless of whether the cert looked
		// correct. Fetching live guarantees we seal with a key the controller holds.
		cert, err := h.fetchFreshCert(ctx, clusterName)
		if err != nil {
			h.logger.Error("failed to get sealing cert", "cluster", clusterName, "err", err)
			writeJSON(w, http.StatusInternalServerError, errorResponse{
				Error: fmt.Sprintf("failed to fetch sealed-secrets certificate from cluster %q: %s", clusterName, err),
			})
			return
		}

		if h.clusterStore == nil {
			writeJSON(w, http.StatusInternalServerError, errorResponse{
				Error: "cluster store not configured; cannot resolve destination",
			})
			return
		}
		cluster, cerr := h.clusterStore.GetCluster(ctx, clusterName)
		if cerr != nil {
			h.logger.Error("cluster not found in registry", "cluster", clusterName, "err", cerr)
			writeJSON(w, http.StatusUnprocessableEntity, errorResponse{
				Error: fmt.Sprintf("cluster %q not found in registry; check Settings > Clusters", clusterName),
			})
			return
		}
		if cluster.APIServer == "" {
			h.logger.Error("cluster has no apiServer configured", "cluster", clusterName)
			writeJSON(w, http.StatusUnprocessableEntity, errorResponse{
				Error: fmt.Sprintf("cluster %q has no apiServer configured; update it in Settings > Clusters", clusterName),
			})
			return
		}

		publishErr := h.sealPublisher.PublishSealedReadToken(ctx, gitops.SealedReadTokenPublishParams{
			Env:               req.Env,
			VaultID:           req.VaultID,
			OrgName:           org.Name,
			Token:             []byte(req.ConnectToken),
			Cert:              cert,
			ArgoCDDestination: cluster.APIServer,
			ClusterName:       clusterName,
			ESONamespace:      cluster.EffectiveESONamespace(),
			ConnectEndpoint:   resolveConnectEndpoint(req.ConnectEndpoint, org),
		})
		if publishErr != nil {
			h.logger.Error("failed to publish sealed token", "env", req.Env, "err", publishErr)
			writeJSON(w, http.StatusInternalServerError, errorResponse{
				Error: "failed to publish sealed token to GitOps: " + publishErr.Error(),
			})
			return
		}
	}

	vaultName := req.VaultName
	if vaultName == "" {
		vaultName = req.VaultID
	}

	org.SecretBackend.UpsertBinding(secrets.EnvBinding{
		Env:                    req.Env,
		VaultID:                req.VaultID,
		VaultName:              vaultName,
		Provisioned:            true,
		LastProvisioned:        time.Now(),
		ClusterSecretStoreName: storeName,
		ConnectEndpoint:        req.ConnectEndpoint,
	})
	if err := h.orgStore.SaveOrg(ctx, org); err != nil {
		h.logger.Error("failed to save org after binding", "err", err)
	}

	if h.auditor != nil {
		h.auditor.Log(secrets.AuditEvent{
			Timestamp: time.Now(),
			Actor:     sessionFromContext(ctx).Username,
			Action:    secrets.AuditAction("bind"),
			Scope:     secrets.Scope{Level: "env", Env: req.Env},
			Result:    "ok",
			Keys:      []string{req.VaultID},
		})
	}

	writeJSON(w, http.StatusOK, BindingResponse{
		Env:                    req.Env,
		VaultID:                req.VaultID,
		VaultName:              vaultName,
		ClusterSecretStoreName: storeName,
		Provisioned:            true,
		Rotated:                rotated,
	})
}

// RemoveBindingResponse is the JSON response for DELETE .../bindings/{env}.
type RemoveBindingResponse struct {
	Env       string `json:"env"`
	Removed   bool   `json:"removed"`
	VaultKept bool   `json:"vaultKept"`
	Error     string `json:"error,omitempty"`
}

// handleRemoveBinding removes a binding and its GitOps files.
func (h *secretsHandler) handleRemoveBinding(w http.ResponseWriter, r *http.Request) {
	env := r.PathValue("env")
	if env == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "environment name is required"})
		return
	}

	ctx := r.Context()
	org, err := h.orgStore.GetOrg(ctx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to load org"})
		return
	}

	binding := org.SecretBackend.FindBinding(env)
	if binding == nil {
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: fmt.Sprintf("no binding for environment %q", env)})
		return
	}

	if h.sealPublisher != nil {
		// Resolve cluster name for the ArgoCD app filename.
		var clusterName string
		for i := range org.Environments {
			if org.Environments[i].Name == env {
				clusterName = org.Environments[i].ClusterRef
				break
			}
		}
		if clusterName == "" {
			writeJSON(w, http.StatusUnprocessableEntity, errorResponse{
				Error: fmt.Sprintf("environment %q has no cluster assigned; cannot remove secret-store files", env),
			})
			return
		}
		if err := h.sealPublisher.DeleteSealedReadToken(ctx, gitops.DeleteSealedReadTokenParams{
			Env:         env,
			ClusterName: clusterName,
		}); err != nil {
			h.logger.Error("failed to delete sealed token from gitops", "env", env, "err", err)
			writeJSON(w, http.StatusInternalServerError, errorResponse{
				Error: fmt.Sprintf("failed to remove secret-store files from GitOps repo: %v", err),
			})
			return
		}
	}

	org.SecretBackend.RemoveBinding(env)
	if err := h.orgStore.SaveOrg(ctx, org); err != nil {
		h.logger.Error("failed to save org after unbind", "err", err)
	}

	if h.auditor != nil {
		h.auditor.Log(secrets.AuditEvent{
			Timestamp: time.Now(),
			Actor:     sessionFromContext(ctx).Username,
			Action:    secrets.AuditAction("unbind"),
			Scope:     secrets.Scope{Level: "env", Env: env},
			Result:    "ok",
		})
	}

	writeJSON(w, http.StatusOK, RemoveBindingResponse{
		Env:       env,
		Removed:   true,
		VaultKept: true,
	})
}

// fetchFreshCert fetches the sealing certificate PEM directly from the target
// cluster's sealed-secrets controller, updates the cache, and returns the PEM.
// Falls back to the cache if the cluster pool is not configured.
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

// fetchOrLoadCert returns the sealing certificate PEM for clusterName.
//
// Resolution order:
//  1. Try the cert cache (ConfigMap in suparship-system).
//  2. On cache miss, if a clusterPool is configured, fetch live from the
//     target cluster's sealed-secrets controller and populate the cache.
//  3. If neither is available, return a descriptive error.
func (h *secretsHandler) fetchOrLoadCert(ctx context.Context, clusterName string) ([]byte, error) {
	if h.certCache == nil {
		return nil, fmt.Errorf("cert cache not configured")
	}

	pemBytes, err := h.certCache.Get(ctx, clusterName)
	if err == nil {
		return pemBytes, nil
	}

	// Cache miss — fetch live from the target cluster if pool is available.
	if h.clusterPool == nil {
		return nil, fmt.Errorf("sealed-secrets certificate not cached for cluster %q and no cluster pool available; ensure sealed-secrets is installed on the target cluster", clusterName)
	}

	h.logger.Info("sealing cert not cached — fetching from target cluster", "cluster", clusterName)

	kubeClient, err := h.clusterPool.GetKubeClient(ctx, clusterName)
	if err != nil {
		return nil, fmt.Errorf("building kube client for cluster %q to fetch sealing cert: %w", clusterName, err)
	}

	pemBytes, err = seal.FetchAndCache(ctx, h.certCache, kubeClient, clusterName, seal.FetchOptions{})
	if err != nil {
		return nil, fmt.Errorf("fetching sealing cert from cluster %q: %w", clusterName, err)
	}

	h.logger.Info("sealing cert fetched and cached", "cluster", clusterName)
	return pemBytes, nil
}

// resolveConnectEndpoint returns the Connect server URL to use for sealing.
// Priority: request body override → org-stored endpoint → empty (publisher uses default).
func resolveConnectEndpoint(reqEndpoint string, org *rbac.Org) string {
	if reqEndpoint != "" {
		return reqEndpoint
	}
	if org.SecretBackend.OnePassword != nil {
		return org.SecretBackend.OnePassword.Connect.Endpoint
	}
	return ""
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

// ── Cluster-level secrets CRUD ──────────────────────────────────────────────

func (h *secretsHandler) handleListClusterSecrets(w http.ResponseWriter, r *http.Request) {
	cluster := r.PathValue("cluster")
	entries, err := h.upperWriter.ReadClusterSecretKeys(r.Context(), cluster)
	if err != nil {
		h.logger.Error("failed to list cluster secret keys", "cluster", cluster, "err", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to list secrets"})
		return
	}
	writeJSON(w, http.StatusOK, secretKeysResponseFromEntries(entries, secrets.ClusterSecretName(cluster)))
}

func (h *secretsHandler) handleUpsertClusterSecrets(w http.ResponseWriter, r *http.Request) {
	cluster := r.PathValue("cluster")
	req, ok := h.decodeUpsertRequest(w, r)
	if !ok {
		return
	}
	if err := h.upperWriter.WriteClusterSecrets(r.Context(), cluster, toByteMap(req.Entries)); err != nil {
		h.logger.Error("failed to upsert cluster secrets", "cluster", cluster, "err", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to save secrets"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *secretsHandler) handleDeleteClusterSecret(w http.ResponseWriter, r *http.Request) {
	cluster := r.PathValue("cluster")
	key := r.PathValue("key")
	if err := h.upperWriter.DeleteClusterSecretKey(r.Context(), cluster, key); err != nil {
		h.logger.Error("failed to delete cluster secret key", "cluster", cluster, "key", key, "err", err)
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

// ── App-env secrets CRUD ────────────────────────────────────────────────────

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

	orgKeys, err := h.upperWriter.ReadOrgSecretKeys(ctx)
	if err != nil {
		h.logger.Error("failed to read org secret keys", "err", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to resolve secrets"})
		return
	}

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

	var clusterKeys []secrets.SecretEntry
	if clusterRef := h.resolveClusterRef(r, envName); clusterRef != "" {
		clusterKeys, err = h.upperWriter.ReadClusterSecretKeys(ctx, clusterRef)
		if err != nil {
			h.logger.Error("failed to read cluster secret keys", "cluster", clusterRef, "err", err)
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
		entriesToKeys(clusterKeys),
	)

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
			Scope: secrets.Scope{
				Level:   "sync",
				Project: project,
				App:     appName,
			},
			Keys:   []string{},
			Result: "ok",
		})
	}

	h.logger.Info("secrets sync triggered",
		"project", project,
		"app", appName,
		"syncToken", token,
	)

	writeJSON(w, http.StatusOK, map[string]string{
		"status":    "ok",
		"syncToken": token,
	})
}

// ── Helpers ─────────────────────────────────────────────────────────────────

func (h *secretsHandler) resolveNamespace(r *http.Request, project, appName, envName string) (string, error) {
	env, err := h.appStore.GetAppEnvironment(r.Context(), project, appName, envName)
	if err != nil {
		return "", err
	}
	return env.Namespace, nil
}

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

func (h *secretsHandler) resolveEnvType(r *http.Request, project, appName, envName string) string {
	env, err := h.appStore.GetAppEnvironment(r.Context(), project, appName, envName)
	if err != nil {
		return envName
	}
	return string(env.EnvType)
}

// resolveClusterRef returns the registered cluster name bound to envName via
// the org config, or "" when the env is unbound or the org cannot be loaded.
// The returned name is the key used by cluster-scope secret/env-var endpoints.
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
			return e.ClusterRef
		}
	}
	return ""
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

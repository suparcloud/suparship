package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
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

// SealedTokenPublisher publishes a cluster's sealed Connect token + unified
// ClusterSecretStore to the GitOps repo (one ArgoCD app per cluster).
type SealedTokenPublisher interface {
	PublishClusterSecretStore(ctx context.Context, params gitops.ClusterSealParams) error
	DeleteClusterSecretStores(ctx context.Context, clusterName string) error
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
	auditor         secrets.SecretAuditor
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
	if newCfg.OnePassword != nil {
		newCfg.OnePassword.Connect.Endpoint = strings.TrimSpace(newCfg.OnePassword.Connect.Endpoint)
		if err := domain.ValidateConnectEndpoint(newCfg.OnePassword.Connect.Endpoint); err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: err.Error()})
			return
		}
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
	// Config changes (e.g. the org-level Connect endpoint) are baked into each
	// cluster's unified store — republish them best-effort in the background.
	h.resealAllClustersAsync("backend-config-updated")
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
	if cfg.OnePassword != nil {
		cfg.OnePassword.Connect.Endpoint = strings.TrimSpace(cfg.OnePassword.Connect.Endpoint)
		if err := domain.ValidateConnectEndpoint(cfg.OnePassword.Connect.Endpoint); err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: err.Error()})
			return
		}
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
	// Config changes (e.g. the org-level Connect endpoint) are baked into each
	// cluster's unified store — republish them best-effort in the background.
	h.resealAllClustersAsync("backend-config-updated")
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
// Registration records the vault ID only — Connect tokens are pasted per
// cluster (one token per cluster, covering every vault it reads).
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
		VaultID:     info.ID,
		VaultName:   resolvedName,
		Provisioned: true,
	})
	if err := h.orgStore.SaveOrg(ctx, org); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to persist org"})
		return
	}

	// The unified store on every cluster lists this vault, so republish each
	// cluster's store with the new vault set. Best-effort — clusters without a
	// pasted Connect token are skipped until one arrives.
	h.resealAllClusters(ctx, org, "global-vault-set")

	h.logger.Info("global vault set", "vaultID", info.ID, "vaultTitle", resolvedName)
	writeJSON(w, http.StatusOK, map[string]string{"vaultId": info.ID, "vaultName": resolvedName})
}

// ── Env vault registration (1Password) ──────────────────────────────────────

// RegisterVaultRequest registers a 1Password vault for an env scope. It only
// records which vault backs the env — Connect tokens are pasted per cluster
// (one token per cluster, covering the global vault + its bound env vaults).
// Cluster overrides are items inside the env vault, so clusters need no vault
// registration of their own.
type RegisterVaultRequest struct {
	VaultID   string `json:"vaultId"`
	VaultName string `json:"vaultName"`
}

// handleRegisterEnvVault registers the env-scope vault and republishes the
// unified store of the env's bound cluster (its vault list changed).
func (h *secretsHandler) handleRegisterEnvVault(w http.ResponseWriter, r *http.Request) {
	env := r.PathValue("env")
	h.registerVault(w, r, secrets.EnvScope(env))
}

// registerVault validates a 1Password vault, persists its VaultRef, and
// republishes the unified store of the affected cluster(s) — for an env vault,
// the env's bound cluster (skipped until that cluster has a pasted token).
func (h *secretsHandler) registerVault(w http.ResponseWriter, r *http.Request, scope secrets.Scope) {
	var req RegisterVaultRequest
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
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: "vault registration requires the 1Password backend"})
		return
	}

	// Validate the vault is reachable by the stored SA token.
	if err := h.validateVault(ctx, req.VaultID); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: err.Error()})
		return
	}

	// Persist the vault ref.
	org.SecretBackend.UpsertVault(scope, secrets.VaultRef{
		VaultID:     req.VaultID,
		VaultName:   req.VaultName,
		Provisioned: true,
	})
	if err := h.orgStore.SaveOrg(ctx, org); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to persist org"})
		return
	}

	// The bound cluster's unified store now lists one more vault — republish
	// it. Skipped (pending) until the cluster has a pasted Connect token.
	if scope.Kind == secrets.ScopeEnv {
		if c := orgEnvCluster(org, scope.Env); c != "" {
			if err := h.sealCluster(ctx, org, c); err != nil {
				h.logger.Warn("register vault: store republish pending", "cluster", c, "error", err)
				writeJSON(w, http.StatusOK, map[string]string{
					"vaultId": req.VaultID,
					"status":  "saved (store republish pending: " + err.Error() + ")",
				})
				return
			}
			_ = h.orgStore.SaveOrg(ctx, org) // persist updated cluster seal status
			writeJSON(w, http.StatusOK, map[string]string{"vaultId": req.VaultID, "status": "provisioned"})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"vaultId": req.VaultID,
		"status":  "saved (no bound cluster yet; the store will be published when a cluster is assigned)",
	})
}

// ── Per-cluster Connect token (1Password) ───────────────────────────────────

// SetClusterConnectTokenRequest is the JSON body for
// POST .../secret-backend/clusters/{cluster}/connect-token. The token must
// have access to every vault the cluster reads: the global vault plus the env
// vaults of the environments bound to this cluster.
type SetClusterConnectTokenRequest struct {
	ConnectToken    string `json:"connectToken"`
	ConnectEndpoint string `json:"connectEndpoint,omitempty"`
}

// handleSetClusterConnectToken stashes the cluster's single Connect token,
// seals it with the cluster's cert, and publishes the unified
// ClusterSecretStore (suparship-store) listing every vault the cluster reads.
func (h *secretsHandler) handleSetClusterConnectToken(w http.ResponseWriter, r *http.Request) {
	clusterName := r.PathValue("cluster")
	var req SetClusterConnectTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}
	if req.ConnectToken == "" {
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: "connectToken is required"})
		return
	}
	req.ConnectEndpoint = strings.TrimSpace(req.ConnectEndpoint)
	if err := domain.ValidateConnectEndpoint(req.ConnectEndpoint); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: err.Error()})
		return
	}
	ctx := r.Context()
	org, err := h.orgStore.GetOrg(ctx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to load org"})
		return
	}
	if org.SecretBackend.Effective() != secrets.Backend1Password {
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: "connect tokens require the 1Password backend"})
		return
	}
	if h.sealPublisher == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "gitops publisher not configured"})
		return
	}
	if h.clusterStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "cluster store not configured"})
		return
	}
	if _, err := h.clusterStore.GetCluster(ctx, clusterName); err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "cluster not found: " + clusterName})
		return
	}

	// Stash the token so later vault/binding changes can re-seal without a
	// re-paste. Best-effort — sealing below still uses the in-memory token.
	if h.kubeClient != nil {
		if err := secrets.StashConnectToken(ctx, h.kubeClient, secrets.ClusterStashKey(clusterName), []byte(req.ConnectToken)); err != nil {
			h.logger.Warn("cluster token: stash failed (re-seal degraded)", "cluster", clusterName, "error", err)
		}
	}
	org.SecretBackend.UpsertClusterToken(secrets.ClusterTokenRef{
		Cluster:         clusterName,
		ConnectEndpoint: req.ConnectEndpoint,
	})

	sealErr := h.sealCluster(ctx, org, clusterName)

	// Persist just this cluster's token ref (status set by sealCluster) with a
	// conflict-retried write so a concurrent reseal/edit isn't clobbered.
	if ref := org.SecretBackend.FindClusterToken(clusterName); ref != nil {
		refCopy := *ref
		if perr := rbac.MutateOrg(ctx, h.orgStore, func(o *rbac.Org) error {
			o.SecretBackend.UpsertClusterToken(refCopy)
			return nil
		}); perr != nil && sealErr == nil {
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to persist org: " + perr.Error()})
			return
		}
	}

	if sealErr != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "token saved but sealing failed: " + sealErr.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"cluster": clusterName, "status": "sealed"})
}

// validateVault confirms the vault is accessible to the stored SA token.
func (h *secretsHandler) validateVault(ctx context.Context, vaultID string) error {
	if h.saTokenStore == nil || h.saClientFactory == nil {
		return fmt.Errorf("1Password client not configured")
	}
	token, err := h.saTokenStore.LoadToken(ctx)
	if err != nil || token == "" {
		return fmt.Errorf("SA token not saved yet — paste it first")
	}
	client, err := h.saClientFactory(ctx, token)
	if err != nil {
		return fmt.Errorf("failed to create 1Password client: %w", err)
	}
	if _, err := client.GetVault(ctx, vaultID); err != nil {
		return fmt.Errorf("vault not accessible to the stored SA token: %w", err)
	}
	return nil
}

// clusterVaultIDs returns the vault UUIDs clusterName reads: the global vault
// (if registered) plus the env vault of each environment bound to the cluster
// (cluster overrides live inside the env vaults). Global first, envs in org
// order — the order is cosmetic since item names are scope-unique.
func clusterVaultIDs(org *rbac.Org, clusterName string) []string {
	var ids []string
	if ref := org.SecretBackend.FindVault(secrets.GlobalScope()); ref != nil && ref.VaultID != "" {
		ids = append(ids, ref.VaultID)
	}
	for _, e := range org.Environments {
		if e.EffectiveClusterRef() != clusterName {
			continue
		}
		if ref := org.SecretBackend.FindVault(secrets.EnvScope(e.Name)); ref != nil && ref.VaultID != "" {
			ids = append(ids, ref.VaultID)
		}
	}
	return ids
}

// sealCluster seals clusterName's single Connect token (from the stash) and
// publishes its unified ClusterSecretStore listing every registered vault the
// cluster reads. The cluster's ClusterTokenRef seal status is updated in org
// (the caller persists org). Returns an error when no token has been pasted
// yet, no vault is registered, or sealing/publishing fails.
func (h *secretsHandler) sealCluster(ctx context.Context, org *rbac.Org, clusterName string) (retErr error) {
	tokenRef := org.SecretBackend.FindClusterToken(clusterName)
	defer func() {
		if tokenRef == nil {
			return
		}
		if retErr != nil {
			tokenRef.Sealed = false
			tokenRef.LastError = retErr.Error()
			return
		}
		tokenRef.Sealed = true
		tokenRef.LastSealed = time.Now()
		tokenRef.LastError = ""
	}()

	if h.sealPublisher == nil {
		return fmt.Errorf("seal publisher not configured")
	}
	if h.clusterStore == nil {
		return fmt.Errorf("cluster store not configured")
	}
	cluster, err := h.clusterStore.GetCluster(ctx, clusterName)
	if err != nil {
		return fmt.Errorf("get cluster %q: %w", clusterName, err)
	}
	if cluster.APIServer == "" {
		return fmt.Errorf("cluster %q has no apiServer", clusterName)
	}

	vaultIDs := clusterVaultIDs(org, clusterName)
	if len(vaultIDs) == 0 {
		return fmt.Errorf("no vaults registered for cluster %q (set the global vault / register env vaults first)", clusterName)
	}

	var token []byte
	if h.kubeClient != nil {
		if t, err := secrets.LoadConnectToken(ctx, h.kubeClient, secrets.ClusterStashKey(clusterName)); err == nil {
			token = t
		}
	}
	if len(token) == 0 {
		return fmt.Errorf("no Connect token pasted for cluster %q yet", clusterName)
	}

	cert, err := h.fetchFreshCert(ctx, clusterName)
	if err != nil {
		return fmt.Errorf("fetch sealing cert for %q: %w", clusterName, err)
	}

	// Connect endpoint precedence: per-cluster override > org-level setting >
	// built-in default (resolved by the YAML builder when empty).
	var connectEndpoint string
	if tokenRef != nil {
		connectEndpoint = tokenRef.ConnectEndpoint
	}
	if connectEndpoint == "" && org.SecretBackend.OnePassword != nil {
		connectEndpoint = org.SecretBackend.OnePassword.Connect.Endpoint
	}
	return h.sealPublisher.PublishClusterSecretStore(ctx, gitops.ClusterSealParams{
		ClusterName:       clusterName,
		ArgoCDDestination: cluster.APIServer,
		ESONamespace:      cluster.EffectiveESONamespace(),
		Cert:              cert,
		Token:             token,
		VaultIDs:          vaultIDs,
		ConnectEndpoint:   connectEndpoint,
	})
}

// resealAllClusters republishes every registered cluster's unified store —
// called when the vault set changes (global vault set, env vault registered,
// env bindings changed). Best-effort: clusters without a pasted token are
// skipped with a log line; failures update that cluster's seal status. The
// caller is responsible for persisting org afterwards when it wants the
// updated statuses saved.
func (h *secretsHandler) resealAllClusters(ctx context.Context, org *rbac.Org, reason string) {
	if h.sealPublisher == nil || h.clusterStore == nil {
		return
	}
	clusters, err := h.clusterStore.ListClusters(ctx)
	if err != nil {
		h.logger.Warn("reseal clusters: list failed", "reason", reason, "error", err)
		return
	}
	for _, c := range clusters {
		if err := h.sealCluster(ctx, org, c.Name); err != nil {
			h.logger.Warn("reseal cluster skipped/failed", "reason", reason, "cluster", c.Name, "error", err)
		}
	}
	// Persist only the per-cluster seal statuses sealCluster updated, via a
	// conflict-retried read-modify-write — so this background save doesn't
	// clobber a concurrent config edit (env bindings, vault registration).
	var refs []secrets.ClusterTokenRef
	if org.SecretBackend.OnePassword != nil {
		refs = append(refs, org.SecretBackend.OnePassword.ClusterTokens...)
	}
	if len(refs) == 0 {
		return
	}
	if err := rbac.MutateOrg(ctx, h.orgStore, func(o *rbac.Org) error {
		for _, r := range refs {
			o.SecretBackend.UpsertClusterToken(r)
		}
		return nil
	}); err != nil {
		h.logger.Warn("reseal clusters: persisting seal status failed", "reason", reason, "error", err)
	}
}

// resealAllClustersAsync reloads the org in the background (so it sees the
// just-saved config, including 1Password-only fields) and republishes every
// cluster's unified store. No-op for non-1Password backends.
func (h *secretsHandler) resealAllClustersAsync(reason string) {
	if h.sealPublisher == nil || h.clusterStore == nil {
		return
	}
	go func() {
		ctx := context.Background()
		org, err := h.orgStore.GetOrg(ctx)
		if err != nil || org == nil {
			return
		}
		if org.SecretBackend.Effective() != secrets.Backend1Password {
			return
		}
		h.resealAllClusters(ctx, org, reason)
	}()
}

// orgEnvCluster returns the cluster bound to envName, or "".
func orgEnvCluster(org *rbac.Org, envName string) string {
	for _, e := range org.Environments {
		if e.Name == envName {
			return e.EffectiveClusterRef()
		}
	}
	return ""
}

// ── Generic secret CRUD across scopes/tiers ──────────────────────────────────

// scopeFromPath derives the secret Scope from the request path variables.
// Cluster routes are nested under env ({env} is always present alongside
// {cluster}) — cluster overrides are per-(env, cluster).
func scopeFromPath(r *http.Request) secrets.Scope {
	if c := r.PathValue("cluster"); c != "" {
		return secrets.ClusterScope(r.PathValue("env"), c)
	}
	// Stack-scope shared secrets: {project} + {stack} present WITHOUT {app}.
	if p, s := r.PathValue("project"), r.PathValue("stack"); p != "" && s != "" && r.PathValue("app") == "" {
		if e := r.PathValue("env"); e != "" {
			return secrets.StackEnvScope(p, s, e)
		}
		return secrets.StackScope(p, s)
	}
	// Project-scope shared secrets: {project} present WITHOUT {app}/{stack}.
	// (App-tier routes carry both {project} and {app} and fall through to the
	// global/env scopes below, where tierAndApp returns the app tier.)
	if p := r.PathValue("project"); p != "" && r.PathValue("app") == "" {
		if e := r.PathValue("env"); e != "" {
			return secrets.ProjectEnvScope(p, e)
		}
		return secrets.ProjectScope(p)
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
	var projectGlobal, projectEnv secrets.ScopeKeys
	if project != "" {
		projectGlobal = read(secrets.ProjectScope(project))
		projectEnv = read(secrets.ProjectEnvScope(project, envName))
	}
	// Stack layers — only when the app belongs to a stack.
	var stackGlobal, stackEnv secrets.ScopeKeys
	if h.appStore != nil {
		if app, err := h.appStore.GetApp(ctx, project, appName); err == nil && app != nil && app.Spec.Stack != "" {
			stackGlobal = read(secrets.StackScope(project, app.Spec.Stack))
			stackEnv = read(secrets.StackEnvScope(project, app.Spec.Stack, envName))
		}
	}
	var cluster secrets.ScopeKeys
	if clusterRef := h.resolveClusterRef(r, envName); clusterRef != "" {
		cluster = read(secrets.ClusterScope(envName, clusterRef))
	}

	resolved := secrets.ResolveScopes(global, projectGlobal, stackGlobal, env, projectEnv, stackEnv, cluster)
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

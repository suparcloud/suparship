package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"sync"
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
	// k8sUpperWriter is always the K8s implementation regardless of the
	// active backend. Used as the migration *source* when copying upper-level
	// secrets from suparship-system K8s Secrets into a 1Password vault
	// (the current upperWriter may already be the 1Password writer post-switch).
	k8sUpperWriter *secrets.UpperLevelSecretWriter

	// upperWriter holds the active upper-level writer. Guarded by upperWriterMu
	// so it can be hot-swapped when the operator picks a new platform vault
	// (otherwise the cached 1Password writer keeps PlatformVaultID="" from
	// startup and org/project writes fail with "platform vault not provisioned").
	// Always access via currentUpperWriter() / replaceUpperWriter().
	upperWriterMu sync.RWMutex
	upperWriter   secrets.UpperLevelWriter
}

// currentUpperWriter returns the active upper-level writer under a read-lock.
// Callers should not retain the returned value across goroutines without
// re-fetching.
func (h *secretsHandler) currentUpperWriter() secrets.UpperLevelWriter {
	h.upperWriterMu.RLock()
	defer h.upperWriterMu.RUnlock()
	return h.upperWriter
}

// replaceUpperWriter swaps the active writer atomically. Used by handlers that
// observe an org-config change requiring a writer rebuild (most importantly
// the platform-vault picker — the upper-level writer is built once at startup
// from the org's PlatformVaultID; without a swap, every subsequent org/project
// write fails until the server is restarted).
func (h *secretsHandler) replaceUpperWriter(w secrets.UpperLevelWriter) {
	h.upperWriterMu.Lock()
	defer h.upperWriterMu.Unlock()
	h.upperWriter = w
}

// build1PasswordUpperWriter constructs an SAUpperLevelWriter from the current
// org config + saved SA token. Returns an error when prerequisites are not
// met (no SA token, no client factory, backend not 1Password) — callers
// decide whether that's fatal or a no-op.
func (h *secretsHandler) build1PasswordUpperWriter(ctx context.Context, org *rbac.Org) (secrets.UpperLevelWriter, error) {
	if h.saTokenStore == nil || h.saClientFactory == nil {
		return nil, fmt.Errorf("1Password client factory not configured")
	}
	if org == nil || org.SecretBackend.Effective() != secrets.Backend1Password || org.SecretBackend.OnePassword == nil {
		return nil, fmt.Errorf("org backend is not 1Password")
	}
	token, err := h.saTokenStore.LoadToken(ctx)
	if err != nil || token == "" {
		return nil, fmt.Errorf("SA token not saved")
	}
	saClient, err := h.saClientFactory(ctx, token)
	if err != nil {
		return nil, fmt.Errorf("init SA client: %w", err)
	}
	orgName := org.Name
	if orgName == "" {
		orgName = "default"
	}
	return onepassword.NewSAUpperLevelWriter(onepassword.SAUpperLevelWriterConfig{
		Client:          saClient,
		PlatformVaultID: org.SecretBackend.OnePassword.PlatformVaultID,
		Bindings:        org.SecretBackend.OnePassword.Bindings,
		OrgName:         orgName,
		Naming:          org.ResourceNaming,
		EnvForCluster:   buildOrgEnvForClusterResolver(org.Environments),
	}), nil
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

// MigrateToOnePasswordRequest is the JSON body for the migration endpoint.
//
// All three lists are inventories the operator wants migrated. Empty lists
// skip that scope's migration entirely (the helper only iterates the inputs
// it's given). The org scope is always attempted because there's only one
// org-level item — no inventory to enumerate.
type MigrateToOnePasswordRequest struct {
	EnvTypes []string `json:"envTypes,omitempty"`
	Projects []string `json:"projects,omitempty"`
	Clusters []string `json:"clusters,omitempty"`
}

// MigrateToOnePasswordResponse reports per-scope counts of keys copied. Useful
// for the UI to surface "moved 3 staging keys, 1 prod key" feedback.
type MigrateToOnePasswordResponse struct {
	OrgKeys     int            `json:"orgKeys"`
	EnvTypeKeys map[string]int `json:"envTypeKeys"`
	ProjectKeys map[string]int `json:"projectKeys"`
	ClusterKeys map[string]int `json:"clusterKeys"`
}

// handleMigrateToOnePassword copies upper-level secrets (org / env-type /
// project / cluster) from the K8s suparship-system Secrets into the
// configured 1Password vaults. App and app-env secrets are NOT migrated by
// this endpoint — they live in env-bound K8s namespaces and follow a
// different lifecycle.
//
// Preconditions: org backend is 1Password, the SA token has been pasted, and
// the platform vault is provisioned (see handlePostSAToken). The migration
// is idempotent — re-running picks up new keys without clobbering values
// already entered directly into the destination vaults.
func (h *secretsHandler) handleMigrateToOnePassword(w http.ResponseWriter, r *http.Request) {
	if h.k8sUpperWriter == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{
			Error: "migration source unavailable — suparship is not running on a Kubernetes cluster",
		})
		return
	}
	if h.saTokenStore == nil || h.saClientFactory == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{
			Error: "1Password client not configured",
		})
		return
	}

	var req MigrateToOnePasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}

	ctx := r.Context()
	org, err := h.orgStore.GetOrg(ctx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to load org"})
		return
	}
	if org.SecretBackend.Effective() != secrets.Backend1Password || org.SecretBackend.OnePassword == nil {
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{
			Error: "org backend must be set to 1Password before running migration",
		})
		return
	}
	if org.SecretBackend.OnePassword.PlatformVaultID == "" {
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{
			Error: "platform vault not provisioned — re-paste the SA token in Settings first",
		})
		return
	}

	token, err := h.saTokenStore.LoadToken(ctx)
	if err != nil || token == "" {
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: "SA token not saved yet"})
		return
	}
	saClient, err := h.saClientFactory(ctx, token)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to create 1Password client: " + err.Error()})
		return
	}

	orgName := org.Name
	if orgName == "" {
		orgName = "default"
	}
	envForCluster := buildOrgEnvForClusterResolver(org.Environments)
	dst := onepassword.NewSAUpperLevelWriter(onepassword.SAUpperLevelWriterConfig{
		Client:          saClient,
		PlatformVaultID: org.SecretBackend.OnePassword.PlatformVaultID,
		Bindings:        org.SecretBackend.OnePassword.Bindings,
		OrgName:         orgName,
		Naming:          org.ResourceNaming,
		EnvForCluster:   envForCluster,
	})

	res, migErr := secrets.MigrateUpperLevelSecrets(ctx, h.k8sUpperWriter, dst, secrets.MigrateUpperLevelInput{
		EnvTypes: req.EnvTypes,
		Projects: req.Projects,
		Clusters: req.Clusters,
	})
	if migErr != nil {
		// Return what we managed to copy plus the error so the operator can
		// see partial progress and retry.
		h.logger.Error("upper-level migration failed mid-run",
			"copied", res, "err", migErr)
		writeJSON(w, http.StatusInternalServerError, errorResponse{
			Error: fmt.Sprintf("migration failed after partial progress (%d org / %d env-type / %d project / %d cluster keys copied): %v",
				res.OrgKeys, len(res.EnvTypeKeys), len(res.ProjectKeys), len(res.ClusterKeys), migErr),
		})
		return
	}

	h.logger.Info("upper-level migration to 1Password complete",
		"orgKeys", res.OrgKeys,
		"envTypes", len(res.EnvTypeKeys),
		"projects", len(res.ProjectKeys),
		"clusters", len(res.ClusterKeys),
	)
	writeJSON(w, http.StatusOK, MigrateToOnePasswordResponse{
		OrgKeys:     res.OrgKeys,
		EnvTypeKeys: res.EnvTypeKeys,
		ProjectKeys: res.ProjectKeys,
		ClusterKeys: res.ClusterKeys,
	})
}

// buildOrgEnvForClusterResolver returns a closure that maps a registered
// cluster name to the env-name that has it as ClusterRef. Mirrors the helper
// in cmd/suparship/server.go but inlined here so the secrets handler doesn't
// import that package.
func buildOrgEnvForClusterResolver(envs []rbac.OrgEnvironment) func(string) string {
	clusterToEnv := make(map[string]string, len(envs))
	for _, e := range envs {
		if e.ClusterRef != "" {
			clusterToEnv[e.ClusterRef] = e.Name
		}
	}
	return func(cluster string) string {
		return clusterToEnv[cluster]
	}
}

// SetPlatformVaultRequest is the JSON body for the platform-vault picker
// endpoint. The operator supplies a vault ID they created manually in the
// 1Password console (1Password Service Accounts cannot create vaults, so
// suparShip cannot auto-provision one).
type SetPlatformVaultRequest struct {
	// VaultID is the 1Password vault UUID the operator picked from the
	// dropdown populated by listVaults. Required.
	VaultID string `json:"vaultId"`
	// VaultName is informational — the operator-visible title carried from
	// the listVaults response. Persisted alongside the ID for UI display.
	VaultName string `json:"vaultName,omitempty"`
}

// handleSetPlatformVault persists the operator's choice of platform-shared
// vault. The vault must already exist in 1Password and be visible to the
// stored SA token; this handler validates that with a GetVault call before
// saving so a typo can't leave org config pointing at a non-existent vault.
//
// Note: org/project writes won't actually start landing in the chosen vault
// until the suparShip server is restarted — the upper-level writer is
// constructed once at startup using the persisted PlatformVaultID. Hot-reload
// is a follow-up.
func (h *secretsHandler) handleSetPlatformVault(w http.ResponseWriter, r *http.Request) {
	var req SetPlatformVaultRequest
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
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{
			Error: "platform vault can only be set when the 1Password backend is selected",
		})
		return
	}

	// Validate the vault exists and is accessible to the stored SA token.
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
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{
			Error: "vault not accessible to the stored SA token: " + err.Error(),
		})
		return
	}
	resolvedName := info.Title
	if req.VaultName != "" {
		resolvedName = req.VaultName
	}

	if org.SecretBackend.OnePassword == nil {
		org.SecretBackend.OnePassword = &secrets.OnePasswordConfig{}
	}
	org.SecretBackend.OnePassword.PlatformVaultID = info.ID
	org.SecretBackend.OnePassword.PlatformVaultName = resolvedName
	if err := h.orgStore.SaveOrg(ctx, org); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to persist org"})
		return
	}

	// Hot-swap the upper-level writer so subsequent org/project writes
	// pick up the new PlatformVaultID without a server restart. The
	// previous writer was constructed at startup with PlatformVaultID="";
	// every WriteOrgSecrets/WriteProjectSecrets call against it would
	// return "platform vault not provisioned".
	if newWriter, err := h.build1PasswordUpperWriter(ctx, org); err != nil {
		h.logger.Warn("upper-level writer rebuild failed — server restart required for org/project writes to use the new platform vault",
			"err", err)
	} else {
		h.replaceUpperWriter(newWriter)
		h.logger.Info("upper-level writer rebuilt against new platform vault",
			"vaultID", info.ID,
		)
	}

	h.logger.Info("platform vault set",
		"vaultID", info.ID,
		"vaultTitle", resolvedName,
	)
	writeJSON(w, http.StatusOK, map[string]string{
		"vaultId":   info.ID,
		"vaultName": resolvedName,
	})
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
	entries, err := h.currentUpperWriter().ReadOrgSecretKeys(r.Context())
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
	if err := h.currentUpperWriter().WriteOrgSecrets(r.Context(), toByteMap(req.Entries)); err != nil {
		h.logger.Error("failed to upsert org secrets", "err", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to save secrets"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *secretsHandler) handleDeleteOrgSecret(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if err := h.currentUpperWriter().DeleteOrgSecretKey(r.Context(), key); err != nil {
		h.logger.Error("failed to delete org secret key", "key", key, "err", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to delete secret"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ── Env-type-level secrets CRUD ─────────────────────────────────────────────

func (h *secretsHandler) handleListEnvTypeSecrets(w http.ResponseWriter, r *http.Request) {
	envType := r.PathValue("envtype")
	entries, err := h.currentUpperWriter().ReadEnvTypeSecretKeys(r.Context(), envType)
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
	if err := h.currentUpperWriter().WriteEnvTypeSecrets(r.Context(), envType, toByteMap(req.Entries)); err != nil {
		h.logger.Error("failed to upsert env-type secrets", "envtype", envType, "err", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to save secrets"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *secretsHandler) handleDeleteEnvTypeSecret(w http.ResponseWriter, r *http.Request) {
	envType := r.PathValue("envtype")
	key := r.PathValue("key")
	if err := h.currentUpperWriter().DeleteEnvTypeSecretKey(r.Context(), envType, key); err != nil {
		h.logger.Error("failed to delete env-type secret key", "envtype", envType, "key", key, "err", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to delete secret"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ── Project-level secrets CRUD ──────────────────────────────────────────────

func (h *secretsHandler) handleListProjectSecrets(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	entries, err := h.currentUpperWriter().ReadProjectSecretKeys(r.Context(), project)
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
	if err := h.currentUpperWriter().WriteProjectSecrets(r.Context(), project, toByteMap(req.Entries)); err != nil {
		h.logger.Error("failed to upsert project secrets", "project", project, "err", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to save secrets"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *secretsHandler) handleDeleteProjectSecret(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	key := r.PathValue("key")
	if err := h.currentUpperWriter().DeleteProjectSecretKey(r.Context(), project, key); err != nil {
		h.logger.Error("failed to delete project secret key", "project", project, "key", key, "err", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to delete secret"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ── Cluster-level secrets CRUD ──────────────────────────────────────────────

func (h *secretsHandler) handleListClusterSecrets(w http.ResponseWriter, r *http.Request) {
	cluster := r.PathValue("cluster")
	entries, err := h.currentUpperWriter().ReadClusterSecretKeys(r.Context(), cluster)
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
	if err := h.currentUpperWriter().WriteClusterSecrets(r.Context(), cluster, toByteMap(req.Entries)); err != nil {
		h.logger.Error("failed to upsert cluster secrets", "cluster", cluster, "err", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to save secrets"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *secretsHandler) handleDeleteClusterSecret(w http.ResponseWriter, r *http.Request) {
	cluster := r.PathValue("cluster")
	key := r.PathValue("key")
	if err := h.currentUpperWriter().DeleteClusterSecretKey(r.Context(), cluster, key); err != nil {
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

	orgKeys, err := h.currentUpperWriter().ReadOrgSecretKeys(ctx)
	if err != nil {
		h.logger.Error("failed to read org secret keys", "err", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to resolve secrets"})
		return
	}

	envType := h.resolveEnvType(r, project, appName, envName)
	envTypeKeys, err := h.currentUpperWriter().ReadEnvTypeSecretKeys(ctx, envType)
	if err != nil {
		h.logger.Error("failed to read env-type secret keys", "envtype", envType, "err", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to resolve secrets"})
		return
	}

	projectKeys, err := h.currentUpperWriter().ReadProjectSecretKeys(ctx, project)
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
		clusterKeys, err = h.currentUpperWriter().ReadClusterSecretKeys(ctx, clusterRef)
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

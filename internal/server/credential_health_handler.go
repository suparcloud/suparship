package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/suparcloud/suparship/internal/envconfig"
	"github.com/suparcloud/suparship/internal/gitops"
	"github.com/suparcloud/suparship/internal/rbac"
	"github.com/suparcloud/suparship/internal/registry"
	"github.com/suparcloud/suparship/internal/secrets"
)

// credentialHealthHandler serves GET /api/v1/credentials/health which reports
// the status and expiry of all configured credential references.
type credentialHealthHandler struct {
	auth                  *authHandler
	kubeClient            kubernetes.Interface
	orgProvider           rbac.OrgProvider
	gitopsConfigStore     *gitops.ConfigStore
	registryStore         *registry.Store
	logger                *slog.Logger
}

func (h *credentialHealthHandler) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/credentials/health", h.auth.requireAuth(h.handleHealth))
}

// CredentialStatus is the health of one credential source.
type CredentialStatus struct {
	// Name identifies the credential source (e.g. "gitops", "registry", "1password").
	Name string `json:"name"`
	// SecretRef is the Kubernetes Secret name being referenced.
	SecretRef string `json:"secretRef,omitempty"`
	// Status is "healthy", "warning", "expired", "missing", or "not_configured".
	Status string `json:"status"`
	// Message provides human-readable context.
	Message string `json:"message,omitempty"`
	// ExpiresAt is the ISO 8601 expiry timestamp (if set).
	ExpiresAt string `json:"expiresAt,omitempty"`
	// DaysUntilExpiry is nil when no expiry is set.
	DaysUntilExpiry *int `json:"daysUntilExpiry,omitempty"`
}

// CredentialHealthResponse is the response for GET /api/v1/credentials/health.
type CredentialHealthResponse struct {
	Credentials []CredentialStatus `json:"credentials"`
	OverallStatus string           `json:"overallStatus"`
}

const (
	credStatusHealthy       = "healthy"
	credStatusWarning       = "warning"
	credStatusExpired       = "expired"
	credStatusMissing       = "missing"
	credStatusNotConfigured = "not_configured"
	expiryWarningDays       = 30
)

func (h *credentialHealthHandler) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var creds []CredentialStatus

	creds = append(creds, h.checkGitOps(ctx))
	creds = append(creds, h.checkRegistry(ctx))
	creds = append(creds, h.checkOnePassword(ctx))

	overall := credStatusHealthy
	for _, c := range creds {
		if c.Status == credStatusExpired || c.Status == credStatusMissing {
			overall = credStatusExpired
			break
		}
		if c.Status == credStatusWarning && overall != credStatusExpired {
			overall = credStatusWarning
		}
	}

	writeJSON(w, http.StatusOK, CredentialHealthResponse{
		Credentials:   creds,
		OverallStatus: overall,
	})
}

func (h *credentialHealthHandler) checkGitOps(ctx context.Context) CredentialStatus {
	cs := CredentialStatus{Name: "gitops"}

	if h.gitopsConfigStore == nil {
		cs.Status = credStatusNotConfigured
		cs.Message = "GitOps not configured"
		return cs
	}

	cfg, err := h.gitopsConfigStore.Get(ctx)
	if err != nil {
		if errors.Is(err, gitops.ErrConfigNotFound) {
			cs.Status = credStatusNotConfigured
			cs.Message = "GitOps repository not configured"
			return cs
		}
		cs.Status = credStatusMissing
		cs.Message = "Failed to read GitOps config"
		return cs
	}

	if cfg.AuthSecretRef == "" {
		cs.Status = credStatusNotConfigured
		cs.Message = "No auth secret reference configured"
		return cs
	}

	cs.SecretRef = cfg.AuthSecretRef
	cs.ExpiresAt = cfg.CredentialExpiresAt

	if !h.secretExists(ctx, cfg.AuthSecretRef) {
		cs.Status = credStatusMissing
		cs.Message = "Secret " + cfg.AuthSecretRef + " not found in cluster"
		return cs
	}

	return h.applyExpiry(cs, cfg.CredentialExpiresAt)
}

func (h *credentialHealthHandler) checkRegistry(ctx context.Context) CredentialStatus {
	cs := CredentialStatus{Name: "registry"}

	if h.registryStore == nil {
		cs.Status = credStatusNotConfigured
		cs.Message = "Registry not configured"
		return cs
	}

	cfg, err := h.registryStore.Get(ctx)
	if err != nil {
		if errors.Is(err, registry.ErrConfigNotFound) {
			cs.Status = credStatusNotConfigured
			cs.Message = "Container registry not configured"
			return cs
		}
		cs.Status = credStatusMissing
		cs.Message = "Failed to read registry config"
		return cs
	}

	if !cfg.Enabled {
		cs.Status = credStatusNotConfigured
		cs.Message = "Container registry disabled"
		return cs
	}

	if cfg.AuthSecretRef == "" {
		cs.Status = credStatusNotConfigured
		cs.Message = "No auth secret reference configured"
		return cs
	}

	cs.SecretRef = cfg.AuthSecretRef
	cs.ExpiresAt = cfg.CredentialExpiresAt

	if !h.secretExists(ctx, cfg.AuthSecretRef) {
		cs.Status = credStatusMissing
		cs.Message = "Secret " + cfg.AuthSecretRef + " not found in cluster"
		return cs
	}

	return h.applyExpiry(cs, cfg.CredentialExpiresAt)
}

func (h *credentialHealthHandler) checkOnePassword(ctx context.Context) CredentialStatus {
	cs := CredentialStatus{Name: "1password"}

	if h.orgProvider == nil {
		cs.Status = credStatusNotConfigured
		cs.Message = "1Password not configured"
		return cs
	}

	org, err := h.orgProvider.GetOrg(ctx)
	if err != nil {
		cs.Status = credStatusMissing
		cs.Message = "Failed to read org config"
		return cs
	}

	if org.SecretBackend.Effective() != secrets.Backend1Password {
		cs.Status = credStatusNotConfigured
		cs.Message = "Secret backend is not 1Password"
		return cs
	}

	if org.SecretBackend.OnePassword == nil {
		cs.Status = credStatusMissing
		cs.Message = "1Password config is nil"
		return cs
	}

	ref := org.SecretBackend.OnePassword.ExistingSecret
	if ref == "" {
		cs.Status = credStatusNotConfigured
		cs.Message = "No token secret reference configured"
		return cs
	}

	cs.SecretRef = ref

	if !h.secretExists(ctx, ref) {
		cs.Status = credStatusMissing
		cs.Message = "Secret " + ref + " not found in cluster"
		return cs
	}

	cs.Status = credStatusHealthy
	cs.Message = "Token secret present"
	return cs
}

func (h *credentialHealthHandler) secretExists(ctx context.Context, name string) bool {
	if h.kubeClient == nil {
		return true
	}
	_, err := h.kubeClient.CoreV1().Secrets(envconfig.SystemNamespace).Get(ctx, name, metav1.GetOptions{})
	return err == nil
}

func (h *credentialHealthHandler) applyExpiry(cs CredentialStatus, expiresAt string) CredentialStatus {
	if expiresAt == "" {
		cs.Status = credStatusHealthy
		cs.Message = "Credential secret present"
		return cs
	}

	expiry, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		cs.Status = credStatusWarning
		cs.Message = "Invalid expiry timestamp: " + expiresAt
		return cs
	}

	days := int(time.Until(expiry).Hours() / 24)
	cs.DaysUntilExpiry = &days

	if days < 0 {
		cs.Status = credStatusExpired
		cs.Message = "Credential expired"
		return cs
	}

	if days <= expiryWarningDays {
		cs.Status = credStatusWarning
		cs.Message = "Credential expires soon"
		return cs
	}

	cs.Status = credStatusHealthy
	cs.Message = "Credential valid"
	return cs
}


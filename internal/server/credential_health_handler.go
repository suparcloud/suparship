package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/suparcloud/suparship/internal/envconfig"
	"github.com/suparcloud/suparship/internal/gitops"
	"github.com/suparcloud/suparship/internal/rbac"
	"github.com/suparcloud/suparship/internal/registry"
	"github.com/suparcloud/suparship/internal/secrets"
	"github.com/suparcloud/suparship/internal/tpl"
)

// credentialHealthHandler serves GET /api/v1/credentials/health which reports
// the status and expiry of all configured credential references.
type credentialHealthHandler struct {
	auth                  *authHandler
	kubeClient            kubernetes.Interface
	orgProvider           rbac.OrgProvider
	gitopsConfigStore     *gitops.ConfigStore
	registryStore         *registry.Store
	templateRegistryStore *tpl.RegistryStore
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
	creds = append(creds, h.checkVault(ctx))
	creds = append(creds, h.checkTemplates(ctx)...)

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

	ref := secrets.SATokenSecretName
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

// checkVault reports whether the HashiCorp Vault backend can actually be used.
//
// This exists because the failure it catches is otherwise INVISIBLE. When Vault
// is selected but its write token or address is missing, secret writes used to be
// redirected to the Kubernetes store, returning 200 and reading back fine — so an
// operator saw their secrets "saved" while Vault stayed empty. Writes now fail
// closed (dynamicVaultStore.resolve), and this check is how the cause shows up
// somewhere an operator looks, instead of only in a server log line.
//
// Both the address and the write token are required: either one missing means no
// write can succeed, so both are reported as missing rather than warning.
func (h *credentialHealthHandler) checkVault(ctx context.Context) CredentialStatus {
	cs := CredentialStatus{Name: "vault"}

	if h.orgProvider == nil {
		cs.Status = credStatusNotConfigured
		cs.Message = "Vault not configured"
		return cs
	}

	org, err := h.orgProvider.GetOrg(ctx)
	if err != nil {
		cs.Status = credStatusMissing
		cs.Message = "Failed to read org config"
		return cs
	}

	if org.SecretBackend.Effective() != secrets.BackendVault {
		cs.Status = credStatusNotConfigured
		cs.Message = "Secret backend is not Vault"
		return cs
	}

	if org.SecretBackend.Vault == nil || org.SecretBackend.Vault.Address == "" {
		cs.Status = credStatusMissing
		cs.Message = "Vault is the selected backend but no server address is configured — " +
			"secret writes are refused until it is set (Settings → Secrets Backend)"
		return cs
	}

	cs.SecretRef = secrets.VaultTokenSecretName
	if !h.secretExists(ctx, secrets.VaultTokenSecretName) {
		cs.Status = credStatusMissing
		cs.Message = "Write token secret " + secrets.VaultTokenSecretName + " not found — " +
			"secret writes are refused until it is pasted (Settings → Secrets Backend)"
		return cs
	}

	// Per-cluster ESO read tokens are a separate credential from the write token
	// above: without them a cluster's workloads cannot resolve secrets even though
	// suparship itself writes them fine. Report it as a warning, not missing —
	// the control plane is healthy, the data plane is incomplete.
	if pending := unsealedVaultClusters(org); len(pending) > 0 {
		cs.Status = credStatusWarning
		cs.Message = "Write token present, but " + strconv.Itoa(len(pending)) +
			" cluster(s) have no sealed read token yet (" + strings.Join(pending, ", ") +
			") — their workloads cannot resolve secrets"
		return cs
	}

	cs.Status = credStatusHealthy
	cs.Message = "Write token present; every cluster has a sealed read token"
	return cs
}

// unsealedVaultClusters returns the clusters bound to an environment that have no
// sealed Vault token yet, sorted. A cluster bound to nothing is skipped — it has
// no workloads resolving secrets, so a missing token is not yet a problem.
func unsealedVaultClusters(org *rbac.Org) []string {
	bound := map[string]bool{}
	for _, e := range org.Environments {
		for _, ref := range e.ClusterRefs {
			if ref != "" {
				bound[ref] = true
			}
		}
	}
	sealed := map[string]bool{}
	if org.SecretBackend.Vault != nil {
		for _, t := range org.SecretBackend.Vault.ClusterTokens {
			if t.Sealed {
				sealed[t.Cluster] = true
			}
		}
	}
	var pending []string
	for name := range bound {
		if !sealed[name] {
			pending = append(pending, name)
		}
	}
	sort.Strings(pending)
	return pending
}

// checkTemplates returns one CredentialStatus per external template
// repository, or a single not_configured entry when no external repos
// are registered. Per-source granularity makes the dashboard actionable
// — operators see *which* repo's PAT is missing, not just "templates: bad".
//
// No expiry is reported because ExternalTemplateRepo doesn't carry an
// expiresAt field today; status reflects whether the referenced Secret
// exists in the management cluster. Sources without an existingSecret
// (public repos, hand-managed off-cluster) report not_configured.
func (h *credentialHealthHandler) checkTemplates(ctx context.Context) []CredentialStatus {
	if h.templateRegistryStore == nil {
		return []CredentialStatus{{
			Name:    "templates",
			Status:  credStatusNotConfigured,
			Message: "Template registry not configured",
		}}
	}

	reg, err := h.templateRegistryStore.Get(ctx)
	if err != nil {
		if errors.Is(err, tpl.ErrRegistryNotFound) {
			return []CredentialStatus{{
				Name:    "templates",
				Status:  credStatusNotConfigured,
				Message: "Template registry not configured",
			}}
		}
		return []CredentialStatus{{
			Name:    "templates",
			Status:  credStatusMissing,
			Message: "Failed to read template registry",
		}}
	}

	if len(reg.External) == 0 {
		return []CredentialStatus{{
			Name:    "templates",
			Status:  credStatusNotConfigured,
			Message: "No external template repositories configured",
		}}
	}

	out := make([]CredentialStatus, 0, len(reg.External))
	for _, ext := range reg.External {
		cs := CredentialStatus{Name: "templates/" + ext.Name}
		if ext.ExistingSecret == "" {
			cs.Status = credStatusNotConfigured
			cs.Message = "Public repo or hand-managed credentials"
			out = append(out, cs)
			continue
		}
		cs.SecretRef = ext.ExistingSecret
		if !h.secretExists(ctx, ext.ExistingSecret) {
			cs.Status = credStatusMissing
			cs.Message = "Secret " + ext.ExistingSecret + " not found in cluster"
			out = append(out, cs)
			continue
		}
		cs.Status = credStatusHealthy
		cs.Message = "Credential secret present"
		out = append(out, cs)
	}
	return out
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


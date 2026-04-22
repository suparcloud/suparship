package onepassword

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/suparcloud/suparship/internal/secrets"
)

const (
	connectChartRepo    = "https://1password.github.io/connect-helm-charts"
	connectChartName    = "connect"
	connectChartVersion = "1.16.0"
	connectAppVersion   = "1.7.3"

	DefaultConnectNamespace = "onepassword-connect"
	connectArgoAppName      = "onepassword-connect"
)

// ConnectInstallerConfig holds the configuration for the managed Connect install.
type ConnectInstallerConfig struct {
	Namespace     string // defaults to DefaultConnectNamespace
	ArgoCDNS      string // ArgoCD namespace, defaults to "argocd"
	CredentialsOp string // 1Password credentials JSON (base64-encoded)
}

// ConnectInstaller manages the lifecycle of the 1Password Connect server
// deployment in the tooling cluster via an ArgoCD Application.
type ConnectInstaller struct {
	cfg    ConnectInstallerConfig
	logger *slog.Logger
}

// NewConnectInstaller creates a Connect installer.
func NewConnectInstaller(cfg ConnectInstallerConfig, logger *slog.Logger) *ConnectInstaller {
	if cfg.Namespace == "" {
		cfg.Namespace = DefaultConnectNamespace
	}
	if cfg.ArgoCDNS == "" {
		cfg.ArgoCDNS = "argocd"
	}
	return &ConnectInstaller{cfg: cfg, logger: logger}
}

// ArgoApplication represents the YAML for the Connect ArgoCD Application.
type ArgoApplication struct {
	Name      string
	Namespace string
	YAML      string
}

// BuildArgoApplication generates the ArgoCD Application manifest for 1Password Connect.
func (ci *ConnectInstaller) BuildArgoApplication() ArgoApplication {
	yaml := fmt.Sprintf(`apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: %s
  namespace: %s
  labels:
    app.kubernetes.io/managed-by: suparship
    suparship.io/component: onepassword-connect
spec:
  project: default
  source:
    repoURL: %s
    chart: %s
    targetRevision: %s
    helm:
      releaseName: onepassword-connect
      values: |
        connect:
          serviceType: ClusterIP
  destination:
    server: https://kubernetes.default.svc
    namespace: %s
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    syncOptions:
      - CreateNamespace=true
`,
		connectArgoAppName,
		ci.cfg.ArgoCDNS,
		connectChartRepo,
		connectChartName,
		connectChartVersion,
		ci.cfg.Namespace,
	)

	return ArgoApplication{
		Name:      connectArgoAppName,
		Namespace: ci.cfg.ArgoCDNS,
		YAML:      yaml,
	}
}

// ConnectEndpoint returns the in-cluster endpoint for the Connect server.
func (ci *ConnectInstaller) ConnectEndpoint() string {
	return fmt.Sprintf("http://onepassword-connect.%s.svc.cluster.local:8080", ci.cfg.Namespace)
}

// ReconcileStatus checks whether Connect is installed and healthy, and returns
// an updated ConnectStatus. This is designed to be called periodically or on
// demand to keep the org config up to date.
func (ci *ConnectInstaller) ReconcileStatus(ctx context.Context, checker ConnectHealthChecker) secrets.ConnectStatus {
	status := secrets.ConnectStatus{
		Endpoint:  ci.ConnectEndpoint(),
		LastProbe: time.Now(),
	}

	installed, err := checker.IsInstalled(ctx, ci.cfg.Namespace)
	if err != nil {
		ci.logger.Warn("failed to check connect installation", "error", err)
		return status
	}
	status.Installed = installed

	if installed {
		healthy, err := checker.IsHealthy(ctx, ci.ConnectEndpoint())
		if err != nil {
			ci.logger.Warn("connect health check failed", "error", err)
		}
		status.Healthy = healthy
	}

	return status
}

// ConnectHealthChecker abstracts the checks for Connect installation and health.
type ConnectHealthChecker interface {
	// IsInstalled checks if the Connect deployment exists in the given namespace.
	IsInstalled(ctx context.Context, namespace string) (bool, error)

	// IsHealthy probes the Connect server's health endpoint.
	IsHealthy(ctx context.Context, endpoint string) (bool, error)
}

// FakeConnectHealthChecker is a test implementation.
type FakeConnectHealthChecker struct {
	Installed bool
	Healthy   bool
	InstErr   error
	HealthErr error
}

func (f *FakeConnectHealthChecker) IsInstalled(_ context.Context, _ string) (bool, error) {
	return f.Installed, f.InstErr
}

func (f *FakeConnectHealthChecker) IsHealthy(_ context.Context, _ string) (bool, error) {
	return f.Healthy, f.HealthErr
}

// ParseConnectEndpointNamespace extracts the namespace from a Connect endpoint URL.
func ParseConnectEndpointNamespace(endpoint string) string {
	// Format: http://onepassword-connect.{ns}.svc.cluster.local:8080
	parts := strings.Split(endpoint, ".")
	if len(parts) >= 2 {
		return parts[1]
	}
	return DefaultConnectNamespace
}

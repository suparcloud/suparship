package onepassword

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
)

func TestConnectInstaller_BuildArgoApplication(t *testing.T) {
	ci := NewConnectInstaller(ConnectInstallerConfig{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	app := ci.BuildArgoApplication()

	if app.Name != "onepassword-connect" {
		t.Errorf("Name = %q, want onepassword-connect", app.Name)
	}
	if app.Namespace != "argocd" {
		t.Errorf("Namespace = %q, want argocd", app.Namespace)
	}

	checks := []string{
		"apiVersion: argoproj.io/v1alpha1",
		"kind: Application",
		"name: onepassword-connect",
		"namespace: argocd",
		"repoURL: " + connectChartRepo,
		"chart: connect",
		"targetRevision: " + connectChartVersion,
		"namespace: " + DefaultConnectNamespace,
		"app.kubernetes.io/managed-by: suparship",
		"suparship.io/component: onepassword-connect",
		"CreateNamespace=true",
		"selfHeal: true",
	}
	for _, c := range checks {
		if !strings.Contains(app.YAML, c) {
			t.Errorf("YAML missing %q", c)
		}
	}
}

func TestConnectInstaller_BuildArgoApplication_CustomNamespace(t *testing.T) {
	ci := NewConnectInstaller(ConnectInstallerConfig{
		Namespace: "custom-ns",
		ArgoCDNS:  "custom-argocd",
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	app := ci.BuildArgoApplication()

	if app.Namespace != "custom-argocd" {
		t.Errorf("Namespace = %q, want custom-argocd", app.Namespace)
	}
	if !strings.Contains(app.YAML, "namespace: custom-ns") {
		t.Error("YAML missing custom namespace in destination")
	}
}

func TestConnectInstaller_ConnectEndpoint(t *testing.T) {
	tests := []struct {
		namespace string
		want      string
	}{
		{"", "http://onepassword-connect.onepassword-connect.svc.cluster.local:8080"},
		{"custom-ns", "http://onepassword-connect.custom-ns.svc.cluster.local:8080"},
	}
	for _, tt := range tests {
		ci := NewConnectInstaller(ConnectInstallerConfig{Namespace: tt.namespace}, slog.New(slog.NewTextHandler(io.Discard, nil)))
		got := ci.ConnectEndpoint()
		if got != tt.want {
			t.Errorf("ConnectEndpoint() with ns=%q = %q, want %q", tt.namespace, got, tt.want)
		}
	}
}

func TestConnectInstaller_ReconcileStatus_NotInstalled(t *testing.T) {
	ci := NewConnectInstaller(ConnectInstallerConfig{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	checker := &FakeConnectHealthChecker{Installed: false}

	status := ci.ReconcileStatus(context.Background(), checker)

	if status.Installed {
		t.Error("expected Installed=false")
	}
	if status.Healthy {
		t.Error("expected Healthy=false when not installed")
	}
	if status.Endpoint == "" {
		t.Error("expected non-empty endpoint")
	}
	if status.LastProbe.IsZero() {
		t.Error("expected non-zero LastProbe")
	}
}

func TestConnectInstaller_ReconcileStatus_InstalledHealthy(t *testing.T) {
	ci := NewConnectInstaller(ConnectInstallerConfig{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	checker := &FakeConnectHealthChecker{Installed: true, Healthy: true}

	status := ci.ReconcileStatus(context.Background(), checker)

	if !status.Installed {
		t.Error("expected Installed=true")
	}
	if !status.Healthy {
		t.Error("expected Healthy=true")
	}
}

func TestConnectInstaller_ReconcileStatus_InstalledUnhealthy(t *testing.T) {
	ci := NewConnectInstaller(ConnectInstallerConfig{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	checker := &FakeConnectHealthChecker{Installed: true, Healthy: false}

	status := ci.ReconcileStatus(context.Background(), checker)

	if !status.Installed {
		t.Error("expected Installed=true")
	}
	if status.Healthy {
		t.Error("expected Healthy=false")
	}
}

func TestConnectInstaller_ReconcileStatus_CheckError(t *testing.T) {
	ci := NewConnectInstaller(ConnectInstallerConfig{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	checker := &FakeConnectHealthChecker{InstErr: fmt.Errorf("network error")}

	status := ci.ReconcileStatus(context.Background(), checker)

	if status.Installed {
		t.Error("expected Installed=false on check error")
	}
}

func TestParseConnectEndpointNamespace(t *testing.T) {
	tests := []struct {
		endpoint string
		want     string
	}{
		{"http://onepassword-connect.onepassword-connect.svc.cluster.local:8080", "onepassword-connect"},
		{"http://onepassword-connect.custom-ns.svc.cluster.local:8080", "custom-ns"},
		{"invalid", DefaultConnectNamespace},
	}
	for _, tt := range tests {
		got := ParseConnectEndpointNamespace(tt.endpoint)
		if got != tt.want {
			t.Errorf("ParseConnectEndpointNamespace(%q) = %q, want %q", tt.endpoint, got, tt.want)
		}
	}
}

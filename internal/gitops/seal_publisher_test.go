package gitops

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/suparcloud/suparship/internal/branding"
)

// freshTestCertPEM generates a self-signed cert and returns the PEM bytes.
func freshTestCertPEM(t *testing.T) []byte {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "ss"},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestBuildSecretStoreArgoApp(t *testing.T) {
	yaml := buildSecretStoreArgoApp(
		"staging-aks-02-scus", "https://git.example.com/org/gitops.git", "main",
		"https://10.0.0.1:6443", "external-secrets-system", branding.Config{}, "",
	)
	for _, want := range []string{
		"apiVersion: argoproj.io/v1alpha1",
		"kind: Application",
		"name: secrets-staging-aks-02-scus",
		"suparship.io/cluster: staging-aks-02-scus",
		"namespace: argocd",
		"project: suparship-system",
		"path: _secret-stores/staging-aks-02-scus",
		"server: https://10.0.0.1:6443",
		"namespace: external-secrets-system",
		"prune: true",
	} {
		if !strings.Contains(yaml, want) {
			t.Errorf("missing %q in:\n%s", want, yaml)
		}
	}
}

func TestPublishClusterSecretStore_ValidatesInputs(t *testing.T) {
	p, err := NewPublisher(PublisherConfig{RepoURL: "https://example.com/r.git"})
	if err != nil {
		t.Fatal(err)
	}
	certPEM := freshTestCertPEM(t)
	token := []byte("x")
	vaults := []string{"v-global", "v-env-staging"}

	cases := []struct {
		name   string
		params ClusterSealParams
	}{
		{"missing cluster", ClusterSealParams{ArgoCDDestination: "https://k8s:6443", Cert: certPEM, Token: token, VaultIDs: vaults}},
		{"missing destination", ClusterSealParams{ClusterName: "c1", Cert: certPEM, Token: token, VaultIDs: vaults}},
		{"missing cert", ClusterSealParams{ClusterName: "c1", ArgoCDDestination: "https://k8s:6443", Token: token, VaultIDs: vaults}},
		{"missing token", ClusterSealParams{ClusterName: "c1", ArgoCDDestination: "https://k8s:6443", Cert: certPEM, VaultIDs: vaults}},
		{"missing vaults", ClusterSealParams{ClusterName: "c1", ArgoCDDestination: "https://k8s:6443", Cert: certPEM, Token: token}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := p.PublishClusterSecretStore(t.Context(), tc.params); err == nil {
				t.Error("expected validation error")
			}
		})
	}
}

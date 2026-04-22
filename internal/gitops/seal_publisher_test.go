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
		"staging",
		"staging-aks-02-scus",
		"https://git.example.com/org/gitops.git",
		"main",
		"https://10.0.0.1:6443",
		"external-secrets-system",
	)
	for _, want := range []string{
		"apiVersion: argoproj.io/v1alpha1",
		"kind: Application",
		"name: secrets-staging-aks-02-scus",
		"suparship.io/env: staging",
		"suparship.io/cluster: staging-aks-02-scus",
		"namespace: argocd",
		"project: suparship-system",
		"path: gitops-output/_secret-stores/staging",
		"server: https://10.0.0.1:6443",
		"namespace: external-secrets-system",
		"prune: true",
		"selfHeal: true",
	} {
		if !strings.Contains(yaml, want) {
			t.Errorf("missing %q in:\n%s", want, yaml)
		}
	}
}

func TestPublishSealedReadToken_ValidatesInputs(t *testing.T) {
	p, err := NewPublisher(PublisherConfig{RepoURL: "https://example.com/r.git"})
	if err != nil {
		t.Fatal(err)
	}
	certPEM := freshTestCertPEM(t)

	const dest = "https://k8s:6443"
	const cluster = "my-cluster"

	cases := []struct {
		name   string
		params SealedReadTokenPublishParams
	}{
		{"missing env", SealedReadTokenPublishParams{VaultID: "v1", Cert: certPEM, Token: []byte("x"), ArgoCDDestination: dest, ClusterName: cluster}},
		{"missing vault", SealedReadTokenPublishParams{Env: "prod", Cert: certPEM, Token: []byte("x"), ArgoCDDestination: dest, ClusterName: cluster}},
		{"missing cert", SealedReadTokenPublishParams{Env: "prod", VaultID: "v1", Token: []byte("x"), ArgoCDDestination: dest, ClusterName: cluster}},
		{"empty token", SealedReadTokenPublishParams{Env: "prod", VaultID: "v1", Cert: certPEM, ArgoCDDestination: dest, ClusterName: cluster}},
		{"empty destination", SealedReadTokenPublishParams{Env: "prod", VaultID: "v1", Cert: certPEM, Token: []byte("x"), ClusterName: cluster}},
		{"missing cluster", SealedReadTokenPublishParams{Env: "prod", VaultID: "v1", Cert: certPEM, Token: []byte("x"), ArgoCDDestination: dest}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := p.PublishSealedReadToken(t.Context(), tc.params)
			if err == nil {
				t.Error("expected validation error")
			}
		})
	}
}

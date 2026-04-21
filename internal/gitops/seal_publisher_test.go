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

	"github.com/suparcloud/suparship/internal/seal"
)

func freshTestKey(t *testing.T) *rsa.PublicKey {
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
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	pub, err := seal.LoadCertFromPEM(pemBytes)
	if err != nil {
		t.Fatal(err)
	}
	return pub
}

func TestBuildSecretStoreArgoApp(t *testing.T) {
	yaml := buildSecretStoreArgoApp("prod", "https://git.example.com/org/gitops.git", "main", "https://10.0.0.1:6443")
	for _, want := range []string{
		"apiVersion: argoproj.io/v1alpha1",
		"kind: Application",
		"name: secrets-prod",
		"namespace: argocd",
		"path: gitops-output/_infra/secret-stores/prod",
		"server: https://10.0.0.1:6443",
		"namespace: external-secrets",
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
	pub := freshTestKey(t)

	cases := []struct {
		name   string
		params SealedReadTokenPublishParams
	}{
		{"missing env", SealedReadTokenPublishParams{VaultID: "v1", Cert: pub, Token: []byte("x")}},
		{"missing vault", SealedReadTokenPublishParams{Env: "prod", Cert: pub, Token: []byte("x")}},
		{"missing cert", SealedReadTokenPublishParams{Env: "prod", VaultID: "v1", Token: []byte("x")}},
		{"empty token", SealedReadTokenPublishParams{Env: "prod", VaultID: "v1", Cert: pub}},
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

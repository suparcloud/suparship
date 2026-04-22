package seal

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// genTestCertPEM generates a small RSA key + self-signed cert PEM for tests.
func genTestCertPEM(t *testing.T) ([]byte, *rsa.PrivateKey) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "sealed-secrets-test"},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return pemBytes, priv
}

func kubesealAvailable() bool {
	_, err := exec.LookPath("kubeseal")
	return err == nil
}

func TestLoadCertFromPEM_Cert(t *testing.T) {
	certPEM, _ := genTestCertPEM(t)
	pub, err := LoadCertFromPEM(certPEM)
	if err != nil {
		t.Fatalf("LoadCertFromPEM: %v", err)
	}
	if pub == nil {
		t.Fatal("expected non-nil public key")
	}
}

func TestLoadCertFromPEM_Invalid(t *testing.T) {
	if _, err := LoadCertFromPEM([]byte("not pem")); err == nil {
		t.Error("expected error for non-PEM input")
	}
}

func TestBuildSecretYAML_ContainsExpectedFields(t *testing.T) {
	in := SealedSecretInput{
		Name:      "my-secret",
		Namespace: "my-ns",
		Scope:     ScopeNamespaceWide,
		Type:      "Opaque",
		Data:      map[string][]byte{"token": []byte("abc123")},
		Labels: map[string]string{
			"app": "test",
		},
	}
	yaml := buildSecretYAML(in)
	for _, want := range []string{
		"kind: Secret",
		"name: my-secret",
		"namespace: my-ns",
		"app: test",
		"type: Opaque",
		"stringData:",
		"token: abc123",
	} {
		if !strings.Contains(yaml, want) {
			t.Errorf("Secret YAML missing %q:\n%s", want, yaml)
		}
	}
}

func TestBuildSealedSecret_Validation(t *testing.T) {
	certPEM, _ := genTestCertPEM(t)

	if _, err := BuildSealedSecret(certPEM, SealedSecretInput{Namespace: "ns"}); err == nil {
		t.Error("expected error when name is missing")
	}
	if _, err := BuildSealedSecret(certPEM, SealedSecretInput{Name: "n"}); err == nil {
		t.Error("expected error when namespace is missing")
	}
}

func TestBuildSealedSecret_WithKubeseal(t *testing.T) {
	if !kubesealAvailable() {
		t.Skip("kubeseal not on PATH")
	}

	certPEM, _ := genTestCertPEM(t)

	cases := []struct {
		name  string
		input SealedSecretInput
		want  []string
	}{
		{
			name: "namespace-wide",
			input: SealedSecretInput{
				Name:      "op-token-staging",
				Namespace: "external-secrets-system",
				Scope:     ScopeNamespaceWide,
				Data:      map[string][]byte{"token": []byte("rt-stg")},
				Labels: map[string]string{
					"app.kubernetes.io/managed-by": "suparship",
					"suparship.io/env":             "staging",
				},
			},
			want: []string{
				"kind: SealedSecret",
				"name: op-token-staging",
				"namespace: external-secrets-system",
				`sealedsecrets.bitnami.com/namespace-wide`,
				"encryptedData:",
				"token:",
			},
		},
		{
			name: "cluster-wide",
			input: SealedSecretInput{
				Name:      "op-token-cw",
				Namespace: "kube-system",
				Scope:     ScopeClusterWide,
				Data:      map[string][]byte{"token": []byte("rt-cw")},
			},
			want: []string{
				"kind: SealedSecret",
				`sealedsecrets.bitnami.com/cluster-wide`,
			},
		},
		{
			name: "strict",
			input: SealedSecretInput{
				Name:      "op-token-prod",
				Namespace: "1password",
				Scope:     ScopeStrict,
				Data:      map[string][]byte{"token": []byte("rt-prod")},
			},
			want: []string{
				"kind: SealedSecret",
				"name: op-token-prod",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			yaml, err := BuildSealedSecret(certPEM, tc.input)
			if err != nil {
				t.Fatalf("BuildSealedSecret: %v", err)
			}
			for _, w := range tc.want {
				if !strings.Contains(yaml, w) {
					t.Errorf("missing %q in:\n%s", w, yaml)
				}
			}
		})
	}
}

package seal

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/binary"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"
)

// genTestKey generates a small RSA key + self-signed cert PEM for tests.
func genTestKey(t *testing.T) (*rsa.PrivateKey, []byte) {
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
	return priv, pemBytes
}

// decryptValue is the inverse of EncryptValue, used to verify the format.
// Mirrors HybridDecrypt in bitnami-labs/sealed-secrets: OAEP label is the
// scope label; AES-GCM additional data is nil.
func decryptValue(priv *rsa.PrivateKey, ct, label []byte) ([]byte, error) {
	rsaLen := int(binary.BigEndian.Uint16(ct[0:2]))
	rsaCT := ct[2 : 2+rsaLen]
	aesCT := ct[2+rsaLen:]

	sessionKey, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, priv, rsaCT, label)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(sessionKey)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	return gcm.Open(nil, nonce, aesCT, nil)
}

func TestLoadCertFromPEM_Cert(t *testing.T) {
	_, pemBytes := genTestKey(t)
	pub, err := LoadCertFromPEM(pemBytes)
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

func TestEncryptValue_RoundTrip(t *testing.T) {
	priv, pemBytes := genTestKey(t)
	pub, err := LoadCertFromPEM(pemBytes)
	if err != nil {
		t.Fatal(err)
	}

	plaintext := []byte("super-secret-token")
	label := []byte("1password/op-token-prod")

	ct, err := EncryptValue(pub, plaintext, label)
	if err != nil {
		t.Fatalf("EncryptValue: %v", err)
	}
	got, err := decryptValue(priv, ct, label)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if string(got) != string(plaintext) {
		t.Errorf("roundtrip mismatch: got %q, want %q", got, plaintext)
	}
}

func TestBuildSealedSecret_Strict(t *testing.T) {
	priv, pemBytes := genTestKey(t)
	pub, err := LoadCertFromPEM(pemBytes)
	if err != nil {
		t.Fatal(err)
	}

	yaml, err := BuildSealedSecret(pub, SealedSecretInput{
		Name:      "op-token-prod",
		Namespace: "1password",
		Scope:     ScopeStrict,
		Data:      map[string][]byte{"token": []byte("rt-prod-secret")},
		Labels: map[string]string{
			"app.kubernetes.io/managed-by": "suparship",
		},
	})
	if err != nil {
		t.Fatalf("BuildSealedSecret: %v", err)
	}

	for _, want := range []string{
		"apiVersion: bitnami.com/v1alpha1",
		"kind: SealedSecret",
		"name: op-token-prod",
		"namespace: 1password",
		"type: Opaque",
		`app.kubernetes.io/managed-by: "suparship"`,
	} {
		if !strings.Contains(yaml, want) {
			t.Errorf("YAML missing %q.\n%s", want, yaml)
		}
	}
	// Strict scope: no namespace-wide / cluster-wide annotations.
	if strings.Contains(yaml, "namespace-wide") || strings.Contains(yaml, "cluster-wide") {
		t.Errorf("strict scope should have no scope annotation; got:\n%s", yaml)
	}

	// Extract the encrypted token, decrypt it, verify.
	ctB64 := extractEncrypted(t, yaml, "token")
	ct, err := base64.StdEncoding.DecodeString(ctB64)
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	got, err := decryptValue(priv, ct, []byte("1password/op-token-prod"))
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if string(got) != "rt-prod-secret" {
		t.Errorf("got %q, want %q", got, "rt-prod-secret")
	}
}

func TestBuildSealedSecret_NamespaceWide(t *testing.T) {
	priv, pemBytes := genTestKey(t)
	pub, _ := LoadCertFromPEM(pemBytes)

	yaml, err := BuildSealedSecret(pub, SealedSecretInput{
		Name:      "op-token-staging",
		Namespace: "1password",
		Scope:     ScopeNamespaceWide,
		Data:      map[string][]byte{"token": []byte("rt-stg")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(yaml, `sealedsecrets.bitnami.com/namespace-wide: "true"`) {
		t.Errorf("missing namespace-wide annotation:\n%s", yaml)
	}

	ct, _ := base64.StdEncoding.DecodeString(extractEncrypted(t, yaml, "token"))
	got, err := decryptValue(priv, ct, []byte("1password"))
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if string(got) != "rt-stg" {
		t.Errorf("got %q, want %q", got, "rt-stg")
	}
}

func TestBuildSealedSecret_ClusterWide(t *testing.T) {
	priv, pemBytes := genTestKey(t)
	pub, _ := LoadCertFromPEM(pemBytes)

	yaml, err := BuildSealedSecret(pub, SealedSecretInput{
		Name:      "op-token-cw",
		Namespace: "kube-system",
		Scope:     ScopeClusterWide,
		Data:      map[string][]byte{"token": []byte("rt-cw")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(yaml, `sealedsecrets.bitnami.com/cluster-wide: "true"`) {
		t.Errorf("missing cluster-wide annotation:\n%s", yaml)
	}
	ct, _ := base64.StdEncoding.DecodeString(extractEncrypted(t, yaml, "token"))
	got, err := decryptValue(priv, ct, nil)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if string(got) != "rt-cw" {
		t.Errorf("got %q, want %q", got, "rt-cw")
	}
}

func TestBuildSealedSecret_Validation(t *testing.T) {
	_, pemBytes := genTestKey(t)
	pub, _ := LoadCertFromPEM(pemBytes)

	if _, err := BuildSealedSecret(pub, SealedSecretInput{Namespace: "ns"}); err == nil {
		t.Error("expected error when name is missing")
	}
	if _, err := BuildSealedSecret(pub, SealedSecretInput{Name: "n"}); err == nil {
		t.Error("expected error when namespace is missing")
	}
}

// extractEncrypted finds the line `    <key>: <base64>` in the encryptedData
// block of a SealedSecret YAML and returns the base64.
func extractEncrypted(t *testing.T, yaml, key string) string {
	t.Helper()
	prefix := "    " + key + ": "
	for _, line := range strings.Split(yaml, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimPrefix(line, prefix)
		}
	}
	t.Fatalf("encrypted key %q not found in YAML:\n%s", key, yaml)
	return ""
}

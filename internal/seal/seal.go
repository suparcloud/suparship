// Package seal implements native, kubeseal-compatible encryption of
// Kubernetes Secret values into SealedSecret resources.
//
// The encryption scheme matches the one used by Bitnami's sealed-secrets
// controller (and the kubeseal CLI):
//
//   - A fresh 32-byte AES-256 session key is generated per value.
//   - The session key is wrapped with RSA-OAEP (SHA-256, no label) using
//     the controller's public RSA key.
//   - The plaintext is encrypted with AES-256-GCM using a zero nonce
//     (safe because the session key is single-use) and AAD = scope label.
//   - The wire format is:
//         uint16(BE) length of RSA ciphertext  || RSA ciphertext || AES ciphertext
//   - The result is base64-encoded into the SealedSecret encryptedData map.
//
// This package does not require the kubeseal CLI or a connection to the
// target cluster: callers supply the controller's public certificate
// (PEM-encoded X.509) once and reuse it.
package seal

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

// Scope defines the authentication context for a sealed value. It controls
// where the resulting SealedSecret can be successfully decrypted.
type Scope int

const (
	// ScopeStrict (default): name + namespace fixed.
	// Sealed value can be decrypted only at this exact name+namespace.
	ScopeStrict Scope = iota
	// ScopeNamespaceWide: namespace fixed, name free.
	// Sealed value can be decrypted by any SealedSecret in the namespace.
	ScopeNamespaceWide
	// ScopeClusterWide: no constraints.
	// Sealed value can be decrypted by any SealedSecret in the cluster.
	ScopeClusterWide
)

// scopeAnnotation returns the SealedSecret annotation that signals the
// scope to the controller (none for strict).
func (s Scope) annotation() (string, string, bool) {
	switch s {
	case ScopeNamespaceWide:
		return "sealedsecrets.bitnami.com/namespace-wide", "true", true
	case ScopeClusterWide:
		return "sealedsecrets.bitnami.com/cluster-wide", "true", true
	default:
		return "", "", false
	}
}

// LoadCertFromPEM parses the controller's PEM-encoded public certificate
// and returns the RSA public key. Returns an error if the PEM is malformed
// or does not contain an RSA public key.
func LoadCertFromPEM(pemData []byte) (*rsa.PublicKey, error) {
	block, _ := pem.Decode(pemData)
	if block == nil {
		return nil, errors.New("seal: no PEM block found in certificate data")
	}
	switch block.Type {
	case "CERTIFICATE":
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("seal: parsing certificate: %w", err)
		}
		pub, ok := cert.PublicKey.(*rsa.PublicKey)
		if !ok {
			return nil, fmt.Errorf("seal: certificate public key is %T, want RSA", cert.PublicKey)
		}
		return pub, nil
	case "PUBLIC KEY", "RSA PUBLIC KEY":
		pub, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			// Fallback for "RSA PUBLIC KEY" in PKCS#1 form.
			rsaPub, err2 := x509.ParsePKCS1PublicKey(block.Bytes)
			if err2 != nil {
				return nil, fmt.Errorf("seal: parsing public key: %w / %v", err, err2)
			}
			return rsaPub, nil
		}
		rsaPub, ok := pub.(*rsa.PublicKey)
		if !ok {
			return nil, fmt.Errorf("seal: public key is %T, want RSA", pub)
		}
		return rsaPub, nil
	default:
		return nil, fmt.Errorf("seal: unsupported PEM block type %q", block.Type)
	}
}

// EncryptValue encrypts plaintext using the hybrid scheme.
// label is the SealedSecret AAD: "" for cluster-wide, "<namespace>" for
// namespace-wide, "<namespace>/<name>" for strict.
func EncryptValue(pub *rsa.PublicKey, plaintext, label []byte) ([]byte, error) {
	// 1. Generate a single-use AES-256 session key.
	sessionKey := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, sessionKey); err != nil {
		return nil, fmt.Errorf("seal: generating session key: %w", err)
	}

	// 2. Wrap the session key with RSA-OAEP-SHA256 (no OAEP label).
	rsaCiphertext, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, pub, sessionKey, nil)
	if err != nil {
		return nil, fmt.Errorf("seal: RSA-OAEP encrypt: %w", err)
	}

	// 3. Encrypt plaintext with AES-256-GCM using a zero nonce + AAD = label.
	block, err := aes.NewCipher(sessionKey)
	if err != nil {
		return nil, fmt.Errorf("seal: aes.NewCipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("seal: cipher.NewGCM: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	aesCiphertext := gcm.Seal(nil, nonce, plaintext, label)

	// 4. Concatenate: uint16(BE) len(rsa) || rsa || aes.
	out := make([]byte, 2+len(rsaCiphertext)+len(aesCiphertext))
	binary.BigEndian.PutUint16(out[0:2], uint16(len(rsaCiphertext)))
	copy(out[2:2+len(rsaCiphertext)], rsaCiphertext)
	copy(out[2+len(rsaCiphertext):], aesCiphertext)
	return out, nil
}

// SealedSecretInput captures the inputs needed to build one SealedSecret.
type SealedSecretInput struct {
	Name      string
	Namespace string
	// Scope controls where the sealed value can be decrypted.
	Scope Scope
	// Data holds the secret key/value pairs to encrypt.
	Data map[string][]byte
	// Type is the K8s Secret type for the embedded template (e.g. "Opaque").
	// Defaults to "Opaque" when empty.
	Type string
	// Labels are added to the produced K8s Secret.
	Labels map[string]string
}

// label returns the AAD bytes for a given input.
func (in SealedSecretInput) label() []byte {
	switch in.Scope {
	case ScopeClusterWide:
		return nil
	case ScopeNamespaceWide:
		return []byte(in.Namespace)
	default:
		return []byte(in.Namespace + "/" + in.Name)
	}
}

// BuildSealedSecret returns a YAML manifest for a SealedSecret that the
// sealed-secrets controller in the target cluster can decrypt with its
// matching private key.
func BuildSealedSecret(pub *rsa.PublicKey, in SealedSecretInput) (string, error) {
	if in.Name == "" {
		return "", errors.New("seal: name is required")
	}
	if in.Namespace == "" {
		return "", errors.New("seal: namespace is required")
	}
	if in.Type == "" {
		in.Type = "Opaque"
	}

	label := in.label()
	encrypted := make(map[string]string, len(in.Data))
	for k, v := range in.Data {
		ct, err := EncryptValue(pub, v, label)
		if err != nil {
			return "", fmt.Errorf("seal: encrypting key %q: %w", k, err)
		}
		encrypted[k] = base64.StdEncoding.EncodeToString(ct)
	}

	// Build YAML with deterministic key ordering.
	var sb strings.Builder
	sb.WriteString("apiVersion: bitnami.com/v1alpha1\n")
	sb.WriteString("kind: SealedSecret\n")
	sb.WriteString("metadata:\n")
	sb.WriteString("  creationTimestamp: null\n")
	sb.WriteString(fmt.Sprintf("  name: %s\n", in.Name))
	sb.WriteString(fmt.Sprintf("  namespace: %s\n", in.Namespace))
	if ann, val, ok := in.Scope.annotation(); ok {
		sb.WriteString("  annotations:\n")
		sb.WriteString(fmt.Sprintf("    %s: %q\n", ann, val))
	}
	sb.WriteString("spec:\n")
	sb.WriteString("  encryptedData:\n")
	for _, k := range sortedStringKeys(encrypted) {
		sb.WriteString(fmt.Sprintf("    %s: %s\n", k, encrypted[k]))
	}
	sb.WriteString("  template:\n")
	sb.WriteString("    metadata:\n")
	sb.WriteString("      creationTimestamp: null\n")
	sb.WriteString(fmt.Sprintf("      name: %s\n", in.Name))
	sb.WriteString(fmt.Sprintf("      namespace: %s\n", in.Namespace))
	if len(in.Labels) > 0 {
		sb.WriteString("      labels:\n")
		for _, k := range sortedStringKeys(in.Labels) {
			sb.WriteString(fmt.Sprintf("        %s: %q\n", k, in.Labels[k]))
		}
	}
	sb.WriteString(fmt.Sprintf("    type: %s\n", in.Type))

	return sb.String(), nil
}

func sortedStringKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

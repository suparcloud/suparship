// Package seal wraps the kubeseal CLI to produce SealedSecret manifests.
//
// BuildSealedSecret writes a temporary cert file and pipes a plain K8s Secret
// to `kubeseal --cert <certfile> --scope <scope> --format yaml`, returning
// the SealedSecret YAML.  Callers obtain the cert PEM via FetchCert /
// FetchAndCache; no connection to the target cluster is needed at seal time.
//
// Requires `kubeseal` on PATH.  Install from
// https://github.com/bitnami-labs/sealed-secrets/releases
package seal

import (
	"bytes"
	"crypto/rsa"
	"encoding/base64"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
)

// Scope defines the authentication context for a sealed value. It controls
// where the resulting SealedSecret can be successfully decrypted.
type Scope int

const (
	// ScopeStrict (default): name + namespace fixed.
	ScopeStrict Scope = iota
	// ScopeNamespaceWide: namespace fixed, name free.
	ScopeNamespaceWide
	// ScopeClusterWide: no constraints.
	ScopeClusterWide
)

func (s Scope) flag() string {
	switch s {
	case ScopeNamespaceWide:
		return "namespace-wide"
	case ScopeClusterWide:
		return "cluster-wide"
	default:
		return "strict"
	}
}

// SealedSecretInput captures the inputs needed to build one SealedSecret.
type SealedSecretInput struct {
	Name      string
	Namespace string
	// Scope controls where the sealed value can be decrypted.
	Scope Scope
	// Data holds the secret key/value pairs to encrypt.
	Data map[string][]byte
	// Type is the K8s Secret type (e.g. "Opaque"). Defaults to "Opaque".
	Type string
	// Labels are copied into spec.template.metadata.labels.
	Labels map[string]string
}

// LoadCertFromPEM parses the controller's PEM-encoded public certificate
// and returns the RSA public key. Used by the cert cache for validation.
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

// BuildSealedSecret calls the kubeseal CLI to produce a SealedSecret YAML
// manifest from certPEM (the sealed-secrets controller's public certificate)
// and the provided input.
//
// kubeseal must be on PATH.  The cert is written to a temporary file that is
// removed before the function returns.
func BuildSealedSecret(certPEM []byte, in SealedSecretInput) (string, error) {
	if in.Name == "" {
		return "", errors.New("seal: name is required")
	}
	if in.Namespace == "" {
		return "", errors.New("seal: namespace is required")
	}
	if in.Type == "" {
		in.Type = "Opaque"
	}

	// Write cert to a temp file — kubeseal reads it via --cert.
	certFile, err := os.CreateTemp("", "suparship-seal-cert-*.pem")
	if err != nil {
		return "", fmt.Errorf("seal: creating cert temp file: %w", err)
	}
	defer os.Remove(certFile.Name())
	if _, err := certFile.Write(certPEM); err != nil {
		certFile.Close()
		return "", fmt.Errorf("seal: writing cert temp file: %w", err)
	}
	certFile.Close()

	// Build a plain K8s Secret YAML to pipe into kubeseal.
	// kubeseal carries metadata.labels into spec.template.metadata.labels.
	secretYAML := buildSecretYAML(in)

	// kubeseal --cert <file> --scope <scope> --format yaml  < secret.yaml
	path, err := exec.LookPath("kubeseal")
	if err != nil {
		return "", fmt.Errorf("seal: kubeseal not found on PATH: %w", err)
	}

	var stdout, stderr bytes.Buffer
	cmd := exec.Command(path, //nolint:gosec // controlled args, no user input
		"--cert", certFile.Name(),
		"--scope", in.Scope.flag(),
		"--format", "yaml",
	)
	cmd.Stdin = strings.NewReader(secretYAML)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("seal: kubeseal failed: %w\n%s", err, stderr.String())
	}
	return stdout.String(), nil
}

// buildSecretYAML renders a minimal K8s Secret YAML from the input.
// Data values are base64-encoded per the K8s Secret spec.
func buildSecretYAML(in SealedSecretInput) string {
	var sb strings.Builder
	sb.WriteString("apiVersion: v1\n")
	sb.WriteString("kind: Secret\n")
	sb.WriteString("metadata:\n")
	sb.WriteString(fmt.Sprintf("  name: %s\n", in.Name))
	sb.WriteString(fmt.Sprintf("  namespace: %s\n", in.Namespace))
	if len(in.Labels) > 0 {
		sb.WriteString("  labels:\n")
		for _, k := range sortedKeys(in.Labels) {
			sb.WriteString(fmt.Sprintf("    %s: %s\n", k, in.Labels[k]))
		}
	}
	sb.WriteString(fmt.Sprintf("type: %s\n", in.Type))
	if len(in.Data) > 0 {
		sb.WriteString("data:\n")
		for _, k := range sortedDataKeys(in.Data) {
			sb.WriteString(fmt.Sprintf("  %s: %s\n", k, base64.StdEncoding.EncodeToString(in.Data[k])))
		}
	}
	return sb.String()
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedDataKeys(m map[string][]byte) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

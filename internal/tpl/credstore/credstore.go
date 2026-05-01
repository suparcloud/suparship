// Package credstore writes UI-submitted external-template-repo credentials
// into the management cluster as SealedSecrets, never as plaintext Secrets.
//
// The flow:
//
//  1. UI submits plaintext credentials.
//  2. Caller (handler) tests them against the live repo while still in memory.
//  3. On success, Store.SealAndApply fetches the management cluster's
//     sealed-secrets controller cert, builds a SealedSecret CR from the
//     credentials, and applies it to suparship-system.
//  4. The sealed-secrets controller decrypts the CR and produces a regular
//     Secret with a deterministic name (suparship-tpl-credentials-<source>).
//  5. registrysync.Engine.readAuth reads that Secret as before; it already
//     accepts both data["token"] and data["username"]+data["password"].
//
// Why SealedSecrets here even though the Secret only ever lives in-cluster:
// keeps suparship's "no plaintext Secrets in operator-touched flows"
// invariant intact (matches the gitops sealed-token pattern), and makes
// Velero/etc. backups of suparship-system safe by default.
package credstore

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/yaml"

	"github.com/suparcloud/suparship/internal/seal"
)

// SystemNamespace is where suparship reads template auth Secrets from.
// Mirrors registrysync.systemNamespace; duplicated here to avoid an
// import cycle (registrysync imports tpl, which would otherwise import
// this package).
const SystemNamespace = "suparship-system"

// SecretNamePrefix is the prefix for managed template credential Secrets.
// The full name is "<prefix><sanitized-source-name>". A stable per-source
// suffix lets multiple template sources each have independent credentials.
const SecretNamePrefix = "suparship-tpl-credentials-"

// sealedSecretGVR addresses the bitnami-labs sealed-secrets CRD.
var sealedSecretGVR = schema.GroupVersionResource{
	Group:    "bitnami.com",
	Version:  "v1alpha1",
	Resource: "sealedsecrets",
}

// ErrNoCredentials is returned when SealAndApply receives no usable
// credentials (all token/username/password empty for the given provider).
var ErrNoCredentials = errors.New("credstore: no credentials provided")

// ErrSealedSecretsNotInstalled is returned when the management cluster
// has no sealed-secrets controller. The caller should surface this as a
// 412 Precondition Failed so the UI can prompt the operator to install
// it (suparship's prerequisites flow already covers this).
var ErrSealedSecretsNotInstalled = errors.New("credstore: sealed-secrets controller not found in management cluster")

// Store seals and applies template-repo credentials.
//
// Both clients must point at the management cluster — this is where
// suparship runs and where registrysync reads the resulting Secret from.
// The dynamic client is used to apply the SealedSecret CR; the typed
// client is used to fetch the controller cert via the API-server proxy.
type Store struct {
	Client    kubernetes.Interface
	DynClient dynamic.Interface
	Logger    *slog.Logger
}

// SealAndApply builds a SealedSecret encoding the given credentials and
// applies it to suparship-system. Returns the deterministic Secret name
// the sealed-secrets controller will decrypt the SealedSecret into; the
// caller writes this name to ExternalTemplateRepo.ExistingSecret.
//
// Provider drives the data-key shape:
//
//   - github / gitlab / gitea  → data["token"]
//   - bitbucket / generic / "" → data["username"], data["password"]
//
// The sync engine (registrysync.readAuth) auto-detects both shapes, so
// callers don't need to round-trip the provider through the Secret.
func (s *Store) SealAndApply(ctx context.Context, sourceName, provider, token, username, password string) (string, error) {
	if s == nil || s.Client == nil || s.DynClient == nil {
		return "", errors.New("credstore: Store not configured (missing clients)")
	}
	if sourceName == "" {
		return "", errors.New("credstore: sourceName is required")
	}

	data, err := buildSecretData(provider, token, username, password)
	if err != nil {
		return "", err
	}

	secretName := SecretNameFor(sourceName)
	logger := s.logger()

	// Fetch the controller cert directly each call — credential edits are
	// rare (operator-driven) so caching gives nothing here, and an outdated
	// cached cert would silently produce SealedSecrets the controller
	// can't decrypt. FetchCert hits the cert endpoint via the API-server
	// proxy, no extra reachability requirements.
	certPEM, err := seal.FetchCert(ctx, s.Client, seal.FetchOptions{})
	if err != nil {
		if errors.Is(err, seal.ErrControllerNotFound) {
			return "", fmt.Errorf("%w: %v", ErrSealedSecretsNotInstalled, err)
		}
		return "", fmt.Errorf("credstore: fetch controller cert: %w", err)
	}

	stringData := make(map[string][]byte, len(data))
	for k, v := range data {
		stringData[k] = []byte(v)
	}

	yamlOut, err := seal.BuildSealedSecret(certPEM, seal.SealedSecretInput{
		Name:      secretName,
		Namespace: SystemNamespace,
		Scope:     seal.ScopeStrict,
		Data:      stringData,
		Type:      "Opaque",
		Labels: map[string]string{
			"suparship.io/managed-by": "suparship",
			"suparship.io/type":       "tpl-credentials",
			"suparship.io/source":     sanitizeName(sourceName),
		},
	})
	if err != nil {
		return "", fmt.Errorf("credstore: build SealedSecret: %w", err)
	}

	obj, err := decodeSealedSecret(yamlOut)
	if err != nil {
		return "", fmt.Errorf("credstore: decode kubeseal output: %w", err)
	}

	if err := s.applySealedSecret(ctx, obj, secretName); err != nil {
		return "", err
	}

	logger.Info("credstore: applied SealedSecret for template source",
		"source", sourceName,
		"secret", secretName,
		"keys", sortedKeys(data),
	)
	return secretName, nil
}

// SecretNameFor returns the deterministic Secret name a given source's
// credentials will live under. Exported so the handler can echo it back
// to the UI without needing the Store.
func SecretNameFor(sourceName string) string {
	return SecretNamePrefix + sanitizeName(sourceName)
}

// buildSecretData shapes the credential map for the given provider.
// Returns ErrNoCredentials when all relevant fields are empty.
func buildSecretData(provider, token, username, password string) (map[string]string, error) {
	out := map[string]string{}
	switch provider {
	case "github", "gitlab", "gitea":
		if token == "" {
			return nil, fmt.Errorf("%w: %s requires a token", ErrNoCredentials, provider)
		}
		out["token"] = token
	case "bitbucket", "generic", "":
		if username == "" && password == "" {
			return nil, fmt.Errorf("%w: %s requires username and password", ErrNoCredentials, providerOrGeneric(provider))
		}
		if username != "" {
			out["username"] = username
		}
		if password != "" {
			out["password"] = password
		}
	default:
		return nil, fmt.Errorf("credstore: unsupported provider %q", provider)
	}
	return out, nil
}

func providerOrGeneric(p string) string {
	if p == "" {
		return "generic"
	}
	return p
}

// Delete removes the managed SealedSecret CR for sourceName. Idempotent
// (NotFound is treated as success) so callers can invoke it during a
// registry-update sweep without first checking existence. The underlying
// decrypted Secret is garbage-collected by the sealed-secrets controller
// via ownerReferences — we don't touch it directly.
func (s *Store) Delete(ctx context.Context, sourceName string) error {
	if s == nil || s.DynClient == nil {
		return errors.New("credstore: Store not configured (missing dynamic client)")
	}
	if sourceName == "" {
		return errors.New("credstore: sourceName is required")
	}
	secretName := SecretNameFor(sourceName)
	err := s.DynClient.Resource(sealedSecretGVR).Namespace(SystemNamespace).Delete(ctx, secretName, metav1.DeleteOptions{})
	if err == nil || apierrors.IsNotFound(err) {
		s.logger().Info("credstore: deleted SealedSecret for template source",
			"source", sourceName,
			"secret", secretName,
			"existed", err == nil,
		)
		return nil
	}
	return fmt.Errorf("credstore: delete SealedSecret %q: %w", secretName, err)
}

// applySealedSecret creates or updates the SealedSecret CR. We use the
// Get→Create-or-Update pattern (mirrors kube.EnsureRootArgoApp) over
// server-side apply because the upstream SealedSecret CRD doesn't yet
// declare openAPI v3 schemas needed for SSA's field tracking, and we
// don't share ownership of these objects with anyone else.
func (s *Store) applySealedSecret(ctx context.Context, obj *unstructured.Unstructured, secretName string) error {
	resource := s.DynClient.Resource(sealedSecretGVR).Namespace(SystemNamespace)

	existing, err := resource.Get(ctx, secretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, createErr := resource.Create(ctx, obj, metav1.CreateOptions{})
		if createErr == nil {
			return nil
		}
		if apierrors.IsNotFound(createErr) {
			return fmt.Errorf("%w: %v", ErrSealedSecretsNotInstalled, createErr)
		}
		return fmt.Errorf("credstore: create SealedSecret %q: %w", secretName, createErr)
	}
	if err != nil {
		return fmt.Errorf("credstore: get SealedSecret %q: %w", secretName, err)
	}

	// Carry resourceVersion forward so Update doesn't conflict.
	obj.SetResourceVersion(existing.GetResourceVersion())
	if _, updateErr := resource.Update(ctx, obj, metav1.UpdateOptions{}); updateErr != nil {
		return fmt.Errorf("credstore: update SealedSecret %q: %w", secretName, updateErr)
	}
	return nil
}

// decodeSealedSecret converts kubeseal's YAML output into an unstructured
// object suitable for the dynamic client.
func decodeSealedSecret(y string) (*unstructured.Unstructured, error) {
	var raw map[string]any
	if err := yaml.Unmarshal([]byte(y), &raw); err != nil {
		return nil, fmt.Errorf("yaml decode: %w", err)
	}
	if raw == nil {
		return nil, errors.New("kubeseal returned empty document")
	}
	return &unstructured.Unstructured{Object: raw}, nil
}

// sanitizeName converts a free-form source name into a DNS-1123-safe
// suffix. Replaces every non-[a-z0-9-] rune with "-", lowercases, trims
// leading/trailing "-", and caps at 200 chars to leave headroom under the
// 253-char Secret-name limit (prefix + suffix + Kubernetes accounting).
func sanitizeName(in string) string {
	in = strings.ToLower(in)
	var b strings.Builder
	b.Grow(len(in))
	prevDash := false
	for _, r := range in {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-'
		if !ok {
			r = '-'
		}
		if r == '-' && prevDash {
			continue
		}
		b.WriteRune(r)
		prevDash = r == '-'
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 200 {
		out = strings.TrimRight(out[:200], "-")
	}
	if out == "" {
		out = "unnamed"
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// Small map; insertion sort keeps it cheap and avoids the sort import.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

func (s *Store) logger() *slog.Logger {
	if s != nil && s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
}

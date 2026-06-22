package registry

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/suparcloud/suparship/internal/k8s"
)

const (
	// KargoCredSecretName is the Secret, created in each Kargo Project namespace,
	// that gives Kargo's Warehouse credentials to list/inspect image tags. It is
	// distinct from any workload imagePullSecret — Kargo discovers it by the
	// kargo.akuity.io/cred-type label and matches it to a subscription by repoURL.
	KargoCredSecretName = "suparship-registry"
	kargoCredTypeLabel  = "kargo.akuity.io/cred-type"
	kargoCredTypeImage  = "image"
)

// EnsureKargoCred provisions (or refreshes) the Kargo image-credential Secret in
// the given Kargo Project namespace from the org registry config, so Warehouses
// in that namespace can authenticate to the registry to discover image tags.
//
// It is a no-op when the registry is disabled or unconfigured. The namespace is
// created if missing — Kargo adopts a pre-existing namespace whose name matches
// the Project. Idempotent: safe to call on every publish / registry-config save.
func (s *Store) EnsureKargoCred(ctx context.Context, projectNamespace string) error {
	cfg, err := s.Get(ctx)
	if err != nil {
		if err == ErrConfigNotFound {
			return nil
		}
		return fmt.Errorf("read registry config: %w", err)
	}
	if !cfg.Enabled || cfg.URL == "" {
		return nil
	}
	username, password, err := s.credentials(ctx, cfg)
	if err != nil {
		return err
	}
	if password == "" {
		return fmt.Errorf("registry auth secret %q has no usable password", cfg.AuthSecretRef)
	}

	if err := k8s.EnsureKargoProjectNamespace(ctx, s.client, projectNamespace); err != nil {
		return fmt.Errorf("ensure kargo namespace %q: %w", projectNamespace, err)
	}

	// Kargo matches a credential to a subscription by repoURL — EXACTLY unless
	// repoURLIsRegex is set. Subscriptions use the full repo path
	// (host/repo), so a host-only repoURL would never match. Emit a regex that
	// matches the registry host and every repository under it, so one
	// project-level credential covers all of a chart's images.
	repoURLPattern := "^" + regexp.QuoteMeta(cfg.URL) + "(/.*)?$"

	return s.upsertKargoCredSecret(ctx, projectNamespace, map[string][]byte{
		"repoURL":        []byte(repoURLPattern),
		"repoURLIsRegex": []byte("true"),
		"username":       []byte(username),
		"password":       []byte(password),
	})
}

func (s *Store) upsertKargoCredSecret(ctx context.Context, ns string, data map[string][]byte) error {
	labels := map[string]string{
		kargoCredTypeLabel:             kargoCredTypeImage,
		"app.kubernetes.io/managed-by": "suparship",
	}
	existing, err := s.client.CoreV1().Secrets(ns).Get(ctx, KargoCredSecretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, createErr := s.client.CoreV1().Secrets(ns).Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: KargoCredSecretName, Namespace: ns, Labels: labels},
			Type:       corev1.SecretTypeOpaque,
			Data:       data,
		}, metav1.CreateOptions{})
		if createErr != nil {
			return fmt.Errorf("create kargo cred secret %s/%s: %w", ns, KargoCredSecretName, createErr)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("get kargo cred secret %s/%s: %w", ns, KargoCredSecretName, err)
	}
	if existing.Labels == nil {
		existing.Labels = map[string]string{}
	}
	for k, v := range labels {
		existing.Labels[k] = v
	}
	existing.Data = data
	existing.Type = corev1.SecretTypeOpaque
	if _, err := s.client.CoreV1().Secrets(ns).Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update kargo cred secret %s/%s: %w", ns, KargoCredSecretName, err)
	}
	return nil
}

// credentials resolves a username/password for the registry from the configured
// AuthSecretRef (in the suparship-system namespace). It supports a plain
// "password" key (username taken from the config, then the secret) and a
// kubernetes.io/dockerconfigjson secret.
func (s *Store) credentials(ctx context.Context, cfg *Config) (username, password string, err error) {
	if cfg.AuthSecretRef == "" {
		return cfg.Username, "", nil
	}
	sec, err := s.client.CoreV1().Secrets(namespace).Get(ctx, cfg.AuthSecretRef, metav1.GetOptions{})
	if err != nil {
		return "", "", fmt.Errorf("get registry auth secret %q: %w", cfg.AuthSecretRef, err)
	}
	if pw, ok := sec.Data["password"]; ok && len(pw) > 0 {
		u := cfg.Username
		if u == "" {
			u = string(sec.Data["username"])
		}
		return u, string(pw), nil
	}
	if dcj, ok := sec.Data[corev1.DockerConfigJsonKey]; ok && len(dcj) > 0 {
		return credentialsFromDockerConfig(dcj, cfg.URL, cfg.Username)
	}
	return "", "", fmt.Errorf("registry auth secret %q has neither a password nor a %s key", cfg.AuthSecretRef, corev1.DockerConfigJsonKey)
}

// credentialsFromDockerConfig extracts username/password for registryURL from a
// dockerconfigjson blob, falling back to the sole auth entry when the URL isn't
// an exact key, and decoding the base64 "auth" field when explicit fields are
// absent.
func credentialsFromDockerConfig(raw []byte, registryURL, fallbackUser string) (string, string, error) {
	var dc struct {
		Auths map[string]struct {
			Username string `json:"username"`
			Password string `json:"password"`
			Auth     string `json:"auth"`
		} `json:"auths"`
	}
	if err := json.Unmarshal(raw, &dc); err != nil {
		return "", "", fmt.Errorf("parse dockerconfigjson: %w", err)
	}
	entry, ok := dc.Auths[registryURL]
	if !ok && len(dc.Auths) == 1 {
		for _, e := range dc.Auths {
			entry, ok = e, true
		}
	}
	if !ok {
		return "", "", fmt.Errorf("dockerconfigjson has no auth entry for %q", registryURL)
	}
	u, p := entry.Username, entry.Password
	if (u == "" || p == "") && entry.Auth != "" {
		decoded, err := base64.StdEncoding.DecodeString(entry.Auth)
		if err != nil {
			return "", "", fmt.Errorf("decode auth for %q: %w", registryURL, err)
		}
		if i := strings.IndexByte(string(decoded), ':'); i >= 0 {
			u, p = string(decoded[:i]), string(decoded[i+1:])
		}
	}
	if u == "" {
		u = fallbackUser
	}
	return u, p, nil
}

package secrets

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// connectTokenStashKey is the data key inside the per-env stash Secret
// that holds the plaintext 1Password Connect token.
const connectTokenStashKey = "token"

// ConnectTokenStashName returns the suparship-system Secret name that
// holds the platform's local copy of the per-env 1Password Connect
// token. The stash is what the startup self-heal goroutine reads to
// re-seal + re-publish to the gitops repo when the gitops file has
// gone missing (e.g. after a `git rm` of the gitops-output tree).
//
// This is the platform's PRIVATE persistence of the token. The same
// token is also sealed into the gitops repo via PublishSealedReadToken,
// but that gitops copy is opaque to the platform without per-cluster
// kubeseal certs — and operators sometimes wipe it. The stash unblocks
// recovery.
//
// Naming intentionally distinct from ConnectTokenSecretName(env) — the
// latter is the WORKLOAD cluster's unsealed Secret that ESO reads.
func ConnectTokenStashName(env string) string {
	return "suparship-onepassword-connect-token-" + env
}

// StashConnectToken upserts the plaintext per-env Connect token into
// suparship-system. Idempotent; rotating tokens is a normal operation
// (operator re-pastes via the binding flow) and the stash should
// reflect the latest value.
//
// Failure to stash is treated as non-fatal by callers — the binding
// still works for the current process, only the startup self-heal is
// degraded. The error is returned so callers can log it.
func StashConnectToken(ctx context.Context, client kubernetes.Interface, env string, token []byte) error {
	if env == "" {
		return fmt.Errorf("env required")
	}
	if len(token) == 0 {
		return fmt.Errorf("token required")
	}
	name := ConnectTokenStashName(env)
	secrets := client.CoreV1().Secrets(SystemNamespace)

	desired := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: SystemNamespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "suparship",
				"suparship.io/type":            "onepassword-connect-token-stash",
				"suparship.io/env":             env,
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{connectTokenStashKey: token},
	}

	existing, err := secrets.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if _, createErr := secrets.Create(ctx, desired, metav1.CreateOptions{}); createErr != nil {
			return fmt.Errorf("create connect-token stash: %w", createErr)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("get connect-token stash: %w", err)
	}
	desired.ResourceVersion = existing.ResourceVersion
	if _, updateErr := secrets.Update(ctx, desired, metav1.UpdateOptions{}); updateErr != nil {
		return fmt.Errorf("update connect-token stash: %w", updateErr)
	}
	return nil
}

// LoadConnectToken returns the plaintext per-env Connect token from
// the suparship-system stash, or (nil, nil) when no stash exists. The
// not-found case is intentionally not an error — callers (the self-heal
// goroutine) treat it as "operator never paste-stashed for this env"
// and skip it with a friendly log message.
func LoadConnectToken(ctx context.Context, client kubernetes.Interface, env string) ([]byte, error) {
	sec, err := client.CoreV1().Secrets(SystemNamespace).Get(ctx, ConnectTokenStashName(env), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get connect-token stash %q: %w", env, err)
	}
	return sec.Data[connectTokenStashKey], nil
}

// DeleteConnectToken removes the per-env stash. Called when an
// operator removes a binding so we don't leak a token for an env the
// platform no longer manages. Not-found is non-error (the stash may
// already be gone, or the binding pre-dated the stash feature).
func DeleteConnectToken(ctx context.Context, client kubernetes.Interface, env string) error {
	err := client.CoreV1().Secrets(SystemNamespace).Delete(ctx, ConnectTokenStashName(env), metav1.DeleteOptions{})
	if err == nil || apierrors.IsNotFound(err) {
		return nil
	}
	return fmt.Errorf("delete connect-token stash %q: %w", env, err)
}

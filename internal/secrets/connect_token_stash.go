package secrets

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// connectTokenStashKey is the data key inside the per-cluster stash Secret
// that holds the plaintext 1Password Connect token.
const connectTokenStashKey = "token"

// ClusterStashKey returns the stash key for one cluster's Connect token.
// Each cluster has exactly one token, covering every vault it reads.
func ClusterStashKey(cluster string) string { return "cluster-" + cluster }

// ConnectTokenStashName returns the suparship-system Secret name that
// holds the platform's local copy of one cluster's 1Password Connect
// token. The stash is what re-seal flows read to re-publish the sealed
// token + unified store to the gitops repo — on vault-binding changes,
// or when the gitops file has gone missing (e.g. after a `git rm` of
// the gitops-output tree).
//
// This is the platform's PRIVATE persistence of the token. The same
// token is also sealed into the gitops repo, but that gitops copy is
// opaque to the platform without per-cluster kubeseal certs — and
// operators sometimes wipe it. The stash unblocks recovery.
//
// Naming intentionally distinct from ConnectTokenSecretName — the
// latter is the WORKLOAD cluster's unsealed Secret that ESO reads.
func ConnectTokenStashName(key string) string {
	return "suparship-onepassword-connect-token-" + key
}

// StashConnectToken upserts the plaintext Connect token into
// suparship-system under the given stash key (one per cluster).
// Idempotent; rotating tokens is a normal operation (operator re-pastes
// via the cluster token flow) and the stash should reflect the latest
// value.
//
// Failure to stash is treated as non-fatal by callers — sealing still
// works for the current request, only later re-seals are degraded. The
// error is returned so callers can log it.
func StashConnectToken(ctx context.Context, client kubernetes.Interface, key string, token []byte) error {
	if key == "" {
		return fmt.Errorf("stash key required")
	}
	if len(token) == 0 {
		return fmt.Errorf("token required")
	}
	name := ConnectTokenStashName(key)
	secrets := client.CoreV1().Secrets(SystemNamespace)

	desired := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: SystemNamespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "suparship",
				"suparship.io/type":            "onepassword-connect-token-stash",
				"suparship.io/key":             key,
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

// LoadConnectToken returns the plaintext Connect token from the
// suparship-system stash under the given key, or (nil, nil) when no
// stash exists. The not-found case is intentionally not an error —
// callers treat it as "operator never pasted a token for this cluster"
// and skip it with a friendly log message.
func LoadConnectToken(ctx context.Context, client kubernetes.Interface, key string) ([]byte, error) {
	sec, err := client.CoreV1().Secrets(SystemNamespace).Get(ctx, ConnectTokenStashName(key), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get connect-token stash %q: %w", key, err)
	}
	return sec.Data[connectTokenStashKey], nil
}

// DeleteConnectToken removes the stash under the given key. Called when
// an operator removes a cluster so we don't leak a token the platform
// no longer manages. Not-found is non-error (the stash may already be
// gone).
func DeleteConnectToken(ctx context.Context, client kubernetes.Interface, key string) error {
	err := client.CoreV1().Secrets(SystemNamespace).Delete(ctx, ConnectTokenStashName(key), metav1.DeleteOptions{})
	if err == nil || apierrors.IsNotFound(err) {
		return nil
	}
	return fmt.Errorf("delete connect-token stash %q: %w", key, err)
}

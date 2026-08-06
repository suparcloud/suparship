package secrets

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// clusterCredentialStashKey is the data key inside the per-cluster stash
// Secret that holds the plaintext credential.
const clusterCredentialStashKey = "token"

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

// VaultTokenStashName returns the suparship-system Secret name stashing one
// cluster's Vault token — the Vault backend's counterpart to
// ConnectTokenStashName, and deliberately a DIFFERENT name per backend: the
// stashes must never alias, or a backend switch would re-seal the previous
// backend's credential into the new backend's ClusterSecretStore.
func VaultTokenStashName(cluster string) string {
	return "suparship-vault-token-cluster-" + cluster
}

// ClusterStashSecretName returns the stash Secret name for the ACTIVE
// backend's per-cluster credential, or "" for backends that have none (k8s).
func (c BackendConfig) ClusterStashSecretName(cluster string) string {
	switch c.Effective() {
	case Backend1Password:
		return ConnectTokenStashName(ClusterStashKey(cluster))
	case BackendVault:
		return VaultTokenStashName(cluster)
	default:
		return ""
	}
}

// StashClusterCredential upserts a plaintext per-cluster credential into
// suparship-system under the given Secret name (one per cluster per backend).
// Idempotent; rotating is a normal operation (operator re-pastes via the
// cluster token flow) and the stash should reflect the latest value.
//
// Failure to stash is treated as non-fatal by callers — sealing still
// works for the current request, only later re-seals are degraded. The
// error is returned so callers can log it.
func StashClusterCredential(ctx context.Context, client kubernetes.Interface, name string, token []byte) error {
	if name == "" {
		return fmt.Errorf("stash secret name required")
	}
	if len(token) == 0 {
		return fmt.Errorf("token required")
	}
	secrets := client.CoreV1().Secrets(SystemNamespace)

	desired := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: SystemNamespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "suparship",
				"suparship.io/type":            "cluster-credential-stash",
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{clusterCredentialStashKey: token},
	}

	existing, err := secrets.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if _, createErr := secrets.Create(ctx, desired, metav1.CreateOptions{}); createErr != nil {
			return fmt.Errorf("create credential stash: %w", createErr)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("get credential stash: %w", err)
	}
	desired.ResourceVersion = existing.ResourceVersion
	if _, updateErr := secrets.Update(ctx, desired, metav1.UpdateOptions{}); updateErr != nil {
		return fmt.Errorf("update credential stash: %w", updateErr)
	}
	return nil
}

// LoadClusterCredential returns the plaintext credential from the
// suparship-system stash Secret, or (nil, nil) when no stash exists. The
// not-found case is intentionally not an error — callers treat it as
// "operator never pasted a token for this cluster" and skip it with a
// friendly log message.
func LoadClusterCredential(ctx context.Context, client kubernetes.Interface, name string) ([]byte, error) {
	if name == "" {
		return nil, nil
	}
	sec, err := client.CoreV1().Secrets(SystemNamespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get credential stash %q: %w", name, err)
	}
	return sec.Data[clusterCredentialStashKey], nil
}

// DeleteClusterCredential removes the stash Secret by name. Called when an
// operator removes a cluster so we don't leak a credential the platform no
// longer manages. Not-found is non-error (the stash may already be gone).
func DeleteClusterCredential(ctx context.Context, client kubernetes.Interface, name string) error {
	if name == "" {
		return nil
	}
	err := client.CoreV1().Secrets(SystemNamespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err == nil || apierrors.IsNotFound(err) {
		return nil
	}
	return fmt.Errorf("delete credential stash %q: %w", name, err)
}

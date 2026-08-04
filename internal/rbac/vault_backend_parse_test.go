package rbac

import (
	"testing"

	"github.com/suparcloud/suparship/internal/secrets"
)

// The Helm chart writes secretBackend into the org ConfigMap as raw YAML
// (charts/suparship/templates/configmap-org.yaml). ParseOrg decodes it
// non-strictly, so a key whose name does not match the struct tag is dropped
// WITHOUT error and the backend silently loads unconfigured — the same class of
// failure as the singular `clusterRef` bug.
//
// This test pins the chart's exact output shape against the real decoder.
func TestParseOrg_VaultBackendFromChartShape(t *testing.T) {
	// Copied verbatim from what `helm template --set secrets.backend=vault`
	// emits. Keep in sync with configmap-org.yaml.
	orgYAML := []byte(`
name: default
displayName: My Organization
secretBackend:
  type: "vault"
  vault:
    address: "https://vault.example.com:8200"
    mount: "platform-kv"
    namespace: "team-a"
    caCert: "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"
teams:
  - name: admins
    displayName: Administrators
    members: [admin]
`)

	org, err := ParseOrg(orgYAML)
	if err != nil {
		t.Fatalf("ParseOrg() error = %v", err)
	}

	if got := org.SecretBackend.Effective(); got != secrets.BackendVault {
		t.Errorf("Effective() = %q, want %q", got, secrets.BackendVault)
	}
	v := org.SecretBackend.Vault
	if v == nil {
		t.Fatal("SecretBackend.Vault is nil — the chart's `vault:` key did not decode")
	}
	if v.Address != "https://vault.example.com:8200" {
		t.Errorf("Address = %q", v.Address)
	}
	if v.EffectiveMount() != "platform-kv" {
		t.Errorf("EffectiveMount() = %q, want platform-kv", v.EffectiveMount())
	}
	if v.Namespace != "team-a" {
		t.Errorf("Namespace = %q", v.Namespace)
	}
	if v.CACert == "" {
		t.Error("CACert did not decode")
	}
}

// A chart that omits the optional keys must still produce a usable config,
// with the mount falling back to the documented default.
func TestParseOrg_VaultBackendMinimal(t *testing.T) {
	orgYAML := []byte(`
name: default
secretBackend:
  type: "vault"
  vault:
    address: "https://vault.example.com:8200"
    mount: "suparship"
teams:
  - name: admins
    members: [admin]
`)

	org, err := ParseOrg(orgYAML)
	if err != nil {
		t.Fatalf("ParseOrg() error = %v", err)
	}
	if org.SecretBackend.Vault == nil {
		t.Fatal("SecretBackend.Vault is nil")
	}
	if got := org.SecretBackend.Vault.EffectiveMount(); got != secrets.DefaultVaultMount {
		t.Errorf("EffectiveMount() = %q, want %q", got, secrets.DefaultVaultMount)
	}
}

// Round-tripping must not lose a non-active backend's settings: Marshal then
// ParseOrg keeps both blocks, which is what lets an operator switch away and
// back without re-entering configuration.
func TestParseOrg_RetainsInactiveBackendConfig(t *testing.T) {
	orgYAML := []byte(`
name: default
secretBackend:
  type: "k8s"
  vault:
    address: "https://vault.example.com:8200"
    mount: "suparship"
  onePassword:
    groupName: "Suparship"
teams:
  - name: admins
    members: [admin]
`)

	org, err := ParseOrg(orgYAML)
	if err != nil {
		t.Fatalf("ParseOrg() error = %v", err)
	}
	if org.SecretBackend.Effective() != secrets.BackendK8s {
		t.Fatalf("Effective() = %q, want k8s", org.SecretBackend.Effective())
	}
	if org.SecretBackend.Vault == nil || org.SecretBackend.Vault.Address == "" {
		t.Error("inactive vault config was dropped on parse")
	}
	if org.SecretBackend.OnePassword == nil {
		t.Error("inactive 1Password config was dropped on parse")
	}
}

package main

import (
	"strings"
	"testing"

	"github.com/suparcloud/suparship/internal/secrets"
)

// Every valid backend must be named in the --type help and in the
// unsupported-type error. Both are derived from ValidBackendTypes precisely so
// that adding a backend cannot leave a stale "use k8s or onepassword" behind —
// which is what happened when Vault landed.
func TestBackendSetCmd_HelpNamesEveryBackend(t *testing.T) {
	flag := secretsBackendSetCmd.Flags().Lookup("type")
	if flag == nil {
		t.Fatal("--type flag not registered")
	}
	for _, name := range secrets.BackendTypeNames() {
		if !strings.Contains(flag.Usage, name) {
			t.Errorf("--type help %q omits backend %q", flag.Usage, name)
		}
	}
	if len(secrets.BackendTypeNames()) < 3 {
		t.Fatalf("expected at least k8s/onepassword/vault, got %v", secrets.BackendTypeNames())
	}
}

func TestBackendTypeNames_SortedAndComplete(t *testing.T) {
	got := secrets.BackendTypeNames()
	if len(got) != len(secrets.ValidBackendTypes) {
		t.Fatalf("got %v, want one entry per valid backend type", got)
	}
	for i := 1; i < len(got); i++ {
		if got[i-1] > got[i] {
			t.Errorf("not sorted: %v", got)
			break
		}
	}
	for _, want := range []string{"k8s", "onepassword", "vault"} {
		if !strings.Contains(strings.Join(got, ","), want) {
			t.Errorf("missing %q in %v", want, got)
		}
	}
}

// The Example block should show the Vault switch — an operator scanning
// `--help` shouldn't conclude the CLI can't do it.
func TestBackendSetCmd_ExampleCoversVault(t *testing.T) {
	if !strings.Contains(secretsBackendSetCmd.Example, "--type=vault") {
		t.Errorf("example omits the vault switch:\n%s", secretsBackendSetCmd.Example)
	}
	// And the long help must point at where a Vault address is actually set,
	// since the CLI has no flag for it.
	if !strings.Contains(secretsBackendSetCmd.Long, "Secrets Backend") {
		t.Errorf("long help doesn't say where to set the Vault address:\n%s", secretsBackendSetCmd.Long)
	}
	// Switching backends must not be confused with moving values.
	if !strings.Contains(secretsBackendSetCmd.Long, "secrets migrate") {
		t.Errorf("long help should point at migrate for moving values:\n%s", secretsBackendSetCmd.Long)
	}
}

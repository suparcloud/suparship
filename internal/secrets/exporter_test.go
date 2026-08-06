package secrets

import (
	"context"
	"testing"

	"k8s.io/client-go/kubernetes/fake"
)

// Every concrete store must agree on the ItemExporter contract: full contents
// for an existing item, (nil, nil) for an absent one. The hcvault store has the
// same test in its own package (import cycle keeps it out of this one).
func TestItemExporter_Conformance(t *testing.T) {
	stores := map[string]VaultStore{
		"mem": NewMemVaultStore(),
		"k8s": NewK8sVaultStore(fake.NewSimpleClientset()),
	}
	ctx := context.Background()
	for name, store := range stores {
		t.Run(name, func(t *testing.T) {
			exporter, ok := store.(ItemExporter)
			if !ok {
				t.Fatalf("%s store does not implement ItemExporter", name)
			}
			scope := EnvScope("staging").WithProject("acme")

			if data, err := exporter.ExportItem(ctx, scope, TierApp, "ghost"); data != nil || err != nil {
				t.Errorf("export missing = %v, %v; want nil, nil", data, err)
			}

			if err := store.Upsert(ctx, scope, TierApp, "api", map[string][]byte{
				"A": []byte("1"), "B": []byte("2"),
			}); err != nil {
				t.Fatal(err)
			}
			data, err := exporter.ExportItem(ctx, scope, TierApp, "api")
			if err != nil {
				t.Fatalf("export: %v", err)
			}
			if len(data) != 2 || string(data["A"]) != "1" || string(data["B"]) != "2" {
				t.Errorf("export = %v", data)
			}

			// The export is a copy — mutating it must not corrupt the store.
			data["A"] = []byte("mutated")
			again, _ := exporter.ExportItem(ctx, scope, TierApp, "api")
			if string(again["A"]) != "1" {
				t.Error("ExportItem returned an aliased map — store data was mutated")
			}
		})
	}
}

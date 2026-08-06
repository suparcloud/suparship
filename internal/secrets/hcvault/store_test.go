package hcvault

import (
	"context"
	"errors"
	"testing"

	"github.com/suparcloud/suparship/internal/secrets"
)

func keyNames(entries []secrets.SecretEntry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Key
	}
	return out
}

func TestHCVaultStore_UpsertMergesAndPreserves(t *testing.T) {
	ctx := context.Background()
	fc := NewFakeClient()
	s := NewHCVaultStore(fc)
	scope := secrets.EnvScope("staging")

	if err := s.Upsert(ctx, scope, secrets.TierApp, "api", map[string][]byte{
		"DB_URL": []byte("postgres://old"), "KEEP": []byte("1"),
	}); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	// Second upsert with a disjoint + overlapping key set: KEEP must survive,
	// DB_URL must take the new value.
	if err := s.Upsert(ctx, scope, secrets.TierApp, "api", map[string][]byte{
		"DB_URL": []byte("postgres://new"), "TOKEN": []byte("t"),
	}); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	got, err := s.ExportItem(ctx, scope, secrets.TierApp, "api")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if string(got["DB_URL"]) != "postgres://new" || string(got["KEEP"]) != "1" || string(got["TOKEN"]) != "t" {
		t.Errorf("merged item = %v", got)
	}
	if len(got) != 3 {
		t.Errorf("key count = %d, want 3", len(got))
	}
}

// The same isolation contract secrets_test.go pins for mem and k8s (that test
// is package-internal, and this package cannot join it without a cycle).
func TestHCVaultStore_Isolation(t *testing.T) {
	ctx := context.Background()
	s := NewHCVaultStore(NewFakeClient())
	scope := secrets.EnvScope("staging")

	if err := s.Upsert(ctx, scope, secrets.TierShared, "", map[string][]byte{"S": []byte("1")}); err != nil {
		t.Fatalf("upsert shared: %v", err)
	}
	if err := s.Upsert(ctx, scope, secrets.TierApp, "api", map[string][]byte{"A": []byte("1")}); err != nil {
		t.Fatalf("upsert app: %v", err)
	}
	if keys, _ := s.ListKeys(ctx, scope, secrets.TierApp, "api"); len(keys) != 1 || keys[0].Key != "A" {
		t.Errorf("app api keys = %v", keyNames(keys))
	}
	if keys, _ := s.ListKeys(ctx, scope, secrets.TierApp, "web"); len(keys) != 0 {
		t.Errorf("app web should be empty, got %v", keyNames(keys))
	}
	if keys, _ := s.ListKeys(ctx, scope, secrets.TierShared, ""); len(keys) != 1 || keys[0].Key != "S" {
		t.Errorf("shared keys = %v", keyNames(keys))
	}
	// A different env's vault is a different path prefix entirely.
	if keys, _ := s.ListKeys(ctx, secrets.EnvScope("prod"), secrets.TierApp, "api"); len(keys) != 0 {
		t.Errorf("prod app api should be empty, got %v", keyNames(keys))
	}
}

// Paths must be {VaultName(scope)}/{ItemName(scope,tier,app)} — this is the
// contract the ESO dataFrom keys must match, so pin it explicitly.
func TestHCVaultStore_PathLayout(t *testing.T) {
	ctx := context.Background()
	fc := NewFakeClient()
	s := NewHCVaultStore(fc)

	scope := secrets.EnvScope("prod").WithProject("acme")
	if err := s.Upsert(ctx, scope, secrets.TierApp, "web", map[string][]byte{"K": []byte("v")}); err != nil {
		t.Fatal(err)
	}
	if err := s.Upsert(ctx, secrets.GlobalScope(), secrets.TierShared, "", map[string][]byte{"K": []byte("v")}); err != nil {
		t.Fatal(err)
	}

	want := []string{
		"suparship-secrets-env-prod/acme-web-env-prod",
		"suparship-secrets-global/shared-global",
	}
	got := fc.Paths()
	if len(got) != len(want) {
		t.Fatalf("paths = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("path[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestHCVaultStore_EnsureItem(t *testing.T) {
	ctx := context.Background()
	s := NewHCVaultStore(NewFakeClient())
	scope := secrets.GlobalScope()

	if err := s.EnsureItem(ctx, scope, secrets.TierApp, "api"); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	// Exists with data → second ensure must not clobber.
	if err := s.Upsert(ctx, scope, secrets.TierApp, "api", map[string][]byte{"K": []byte("v")}); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureItem(ctx, scope, secrets.TierApp, "api"); err != nil {
		t.Fatalf("re-ensure: %v", err)
	}
	if keys, _ := s.ListKeys(ctx, scope, secrets.TierApp, "api"); len(keys) != 1 {
		t.Errorf("ensure clobbered existing keys: %v", keyNames(keys))
	}
}

func TestHCVaultStore_DeleteKey(t *testing.T) {
	ctx := context.Background()
	s := NewHCVaultStore(NewFakeClient())
	scope := secrets.EnvScope("staging")

	if err := s.Upsert(ctx, scope, secrets.TierApp, "api", map[string][]byte{
		"A": []byte("1"), "B": []byte("2"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteKey(ctx, scope, secrets.TierApp, "api", "A"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if keys, _ := s.ListKeys(ctx, scope, secrets.TierApp, "api"); len(keys) != 1 || keys[0].Key != "B" {
		t.Errorf("after delete keys = %v", keyNames(keys))
	}
	// Missing key and missing item are both no-ops.
	if err := s.DeleteKey(ctx, scope, secrets.TierApp, "api", "NOPE"); err != nil {
		t.Errorf("delete missing key: %v", err)
	}
	if err := s.DeleteKey(ctx, scope, secrets.TierApp, "ghost", "A"); err != nil {
		t.Errorf("delete on missing item: %v", err)
	}
}

// The FakeClient enforces check-and-set for real, so this proves the store's
// read-modify-write submits the version it read: a write that raced (version
// moved between read and write) surfaces ErrStaleVersion instead of silently
// dropping the other writer's keys.
func TestHCVaultStore_ConcurrentWriteConflict(t *testing.T) {
	ctx := context.Background()
	fc := NewFakeClient()
	s := NewHCVaultStore(fc)
	scope := secrets.GlobalScope()
	path := "suparship-secrets-global/shared-global"

	if err := s.Upsert(ctx, scope, secrets.TierShared, "", map[string][]byte{"A": []byte("1")}); err != nil {
		t.Fatal(err)
	}
	// Simulate a racing writer by bumping the version behind the store's back.
	data, ver, _ := fc.ReadItem(ctx, path)
	if err := fc.WriteItem(ctx, path, data, ver); err != nil {
		t.Fatal(err)
	}
	// A write against the stale version must fail loudly.
	if err := fc.WriteItem(ctx, path, data, ver); !errors.Is(err, secrets.ErrStaleVersion) {
		t.Errorf("stale write error = %v, want ErrStaleVersion", err)
	}
	// And the store's own next Upsert re-reads, so it succeeds.
	if err := s.Upsert(ctx, scope, secrets.TierShared, "", map[string][]byte{"B": []byte("2")}); err != nil {
		t.Errorf("post-race upsert: %v", err)
	}
}

func TestHCVaultStore_CopyAndDeleteItem(t *testing.T) {
	ctx := context.Background()
	fc := NewFakeClient()
	s := NewHCVaultStore(fc)
	scope := secrets.EnvScope("staging")

	// Copy of a missing source is a no-op.
	if err := s.CopyItem(ctx, scope, "ghost", "dst"); err != nil {
		t.Fatalf("copy missing: %v", err)
	}
	if len(fc.Paths()) != 0 {
		t.Errorf("copy of missing source created something: %v", fc.Paths())
	}

	if err := s.Upsert(ctx, scope, secrets.TierApp, "api", map[string][]byte{"K": []byte("v")}); err != nil {
		t.Fatal(err)
	}
	src := secrets.ItemName(scope, secrets.TierApp, "api")
	if err := s.CopyItem(ctx, scope, src, "renamed-item"); err != nil {
		t.Fatalf("copy: %v", err)
	}
	// Destination has the data; source is untouched (copy, not move).
	dst, _, err := fc.ReadItem(ctx, secrets.VaultName(scope)+"/renamed-item")
	if err != nil || string(dst["K"]) != "v" {
		t.Errorf("destination after copy = %v, %v", dst, err)
	}
	data, err := s.ExportItem(ctx, scope, secrets.TierApp, "api")
	if err != nil || string(data["K"]) != "v" {
		t.Errorf("source after copy = %v, %v", data, err)
	}

	if err := s.DeleteItem(ctx, scope, src); err != nil {
		t.Fatalf("delete item: %v", err)
	}
	if keys, _ := s.ListKeys(ctx, scope, secrets.TierApp, "api"); len(keys) != 0 {
		t.Errorf("item survived DeleteItem: %v", keyNames(keys))
	}
}

func TestHCVaultStore_ExportItem(t *testing.T) {
	ctx := context.Background()
	s := NewHCVaultStore(NewFakeClient())
	scope := secrets.GlobalScope()

	// Absent → (nil, nil).
	if data, err := s.ExportItem(ctx, scope, secrets.TierApp, "ghost"); data != nil || err != nil {
		t.Errorf("export missing = %v, %v; want nil, nil", data, err)
	}
	if err := s.Upsert(ctx, scope, secrets.TierApp, "api", map[string][]byte{"K": []byte("v")}); err != nil {
		t.Fatal(err)
	}
	data, err := s.ExportItem(ctx, scope, secrets.TierApp, "api")
	if err != nil || string(data["K"]) != "v" {
		t.Errorf("export = %v, %v", data, err)
	}
}

func TestHCVaultStore_Probe(t *testing.T) {
	ctx := context.Background()
	fc := NewFakeClient()
	s := NewHCVaultStore(fc)

	if err := s.Probe(ctx, secrets.GlobalScope()); err != nil {
		t.Errorf("probe on empty store: %v", err)
	}
	fc.ProbeErr = errors.New("bad token")
	if err := s.Probe(ctx, secrets.GlobalScope()); err == nil {
		t.Error("probe should surface client probe failure")
	}
	fc.ProbeErr = nil
	fc.ReadErr = errors.New("mount missing")
	if err := s.Probe(ctx, secrets.GlobalScope()); err == nil {
		t.Error("probe should surface unreachable scope path")
	}
}

func TestHCVaultStore_ErrorPropagation(t *testing.T) {
	ctx := context.Background()
	fc := NewFakeClient()
	s := NewHCVaultStore(fc)
	scope := secrets.GlobalScope()

	fc.ReadErr = errors.New("boom")
	if err := s.Upsert(ctx, scope, secrets.TierApp, "api", map[string][]byte{"K": []byte("v")}); err == nil {
		t.Error("upsert should surface read error")
	}
	if _, err := s.ListKeys(ctx, scope, secrets.TierApp, "api"); err == nil {
		t.Error("list should surface read error")
	}
	fc.ReadErr = nil
	fc.WriteErr = errors.New("boom")
	if err := s.Upsert(ctx, scope, secrets.TierApp, "api", map[string][]byte{"K": []byte("v")}); err == nil {
		t.Error("upsert should surface write error")
	}
}

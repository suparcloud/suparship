package seal

import (
	"context"
	"errors"
	"testing"
)

func TestMemCertCache_PutGet(t *testing.T) {
	c := NewMemCertCache()
	ctx := context.Background()

	if _, err := c.Get(ctx, "prod"); !errors.Is(err, ErrCertNotCached) {
		t.Errorf("expected ErrCertNotCached, got %v", err)
	}
	if err := c.Put(ctx, "prod", []byte("pem-bytes")); err != nil {
		t.Fatal(err)
	}
	got, err := c.Get(ctx, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "pem-bytes" {
		t.Errorf("got %q, want %q", got, "pem-bytes")
	}
}

func TestMemCertCache_List(t *testing.T) {
	c := NewMemCertCache()
	ctx := context.Background()
	_ = c.Put(ctx, "a", []byte("p"))
	_ = c.Put(ctx, "b", []byte("p"))
	names, err := c.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 {
		t.Errorf("expected 2 entries, got %d", len(names))
	}
}

func TestLoadCachedCert(t *testing.T) {
	c := NewMemCertCache()
	ctx := context.Background()
	_, pemBytes := genTestKey(t)
	if err := c.Put(ctx, "prod", pemBytes); err != nil {
		t.Fatal(err)
	}
	pub, err := LoadCachedCert(ctx, c, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if pub == nil {
		t.Error("expected non-nil public key")
	}
}

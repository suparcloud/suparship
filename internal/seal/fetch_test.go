package seal

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// End-to-end FetchCert with a recording transport is exercised via the
// secrets handler integration tests. These unit tests cover input validation
// and error propagation that don't need a fake REST round-tripper.

func TestFetchCert_NilClient(t *testing.T) {
	_, err := FetchCert(context.Background(), nil, FetchOptions{})
	if err == nil || !strings.Contains(err.Error(), "target cluster client is required") {
		t.Fatalf("expected nil-client error, got %v", err)
	}
}

func TestFetchOptions_Defaults(t *testing.T) {
	o := FetchOptions{}
	if o.namespace() != DefaultControllerNamespace {
		t.Errorf("namespace default mismatch: %q", o.namespace())
	}
	if o.name() != DefaultControllerName {
		t.Errorf("name default mismatch: %q", o.name())
	}
	if o.portName() != defaultControllerPort {
		t.Errorf("port default mismatch: %q", o.portName())
	}

	o = FetchOptions{ControllerNamespace: "ns", ControllerName: "ctl", PortName: "https"}
	if o.namespace() != "ns" || o.name() != "ctl" || o.portName() != "https" {
		t.Errorf("explicit overrides not honoured: %+v", o)
	}
}

func TestFetchAndCache_PropagatesFetchError(t *testing.T) {
	cache := NewMemCertCache()
	_, err := FetchAndCache(context.Background(), cache, nil, "anycluster", FetchOptions{})
	if err == nil {
		t.Fatal("expected error from FetchAndCache with nil client")
	}
	got, gerr := cache.Get(context.Background(), "anycluster")
	if !errors.Is(gerr, ErrCertNotCached) || got != nil {
		t.Errorf("expected nothing cached on fetch failure, got %v / %v", got, gerr)
	}
}

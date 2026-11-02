package onepassword

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	sdk "github.com/1password/onepassword-sdk-go"
)

func TestRuntimeHealthClassification(t *testing.T) {
	for _, err := range []error{nil, context.Canceled, context.DeadlineExceeded, ErrTokenInvalid, errors.New("429 rate limited"), errors.New("503 unavailable"), errors.New("permission denied"), errors.New("out of bounds in application input")} {
		h := &runtimeHealth{}
		h.observe(err)
		if h.check() != nil {
			t.Fatalf("ordinary error affected liveness: %v", err)
		}
	}
	h := &runtimeHealth{}
	h.observe(fmt.Errorf("list items: %w", errors.New("wasm error: out of bounds memory access")))
	h.observe(nil)
	if !errors.Is(h.check(), errRuntimeUnhealthy) {
		t.Fatal("runtime trap must remain latched until restart")
	}
}

func TestRuntimeHealthConcurrent(t *testing.T) {
	h := &runtimeHealth{}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.observe(errors.New("wasm error: out of bounds memory access"))
			_ = h.check()
		}()
	}
	wg.Wait()
	if h.check() == nil {
		t.Fatal("missing runtime failure")
	}
}

type failingRuntimeItems struct {
	sdk.ItemsAPI
	listErr   error
	createErr error
	creates   int
}

func (f *failingRuntimeItems) List(context.Context, string, ...sdk.ItemListFilter) ([]sdk.ItemOverview, error) {
	return nil, f.listErr
}
func (f *failingRuntimeItems) Create(context.Context, sdk.ItemCreateParams) (sdk.Item, error) {
	f.creates++
	return sdk.Item{}, f.createErr
}

func TestSDKRuntimeTrapRecoverySignal(t *testing.T) {
	// These tests deliberately exercise the process-wide latch without parallelism.
	for _, operation := range []string{"list", "lookup", "create"} {
		t.Run(operation, func(t *testing.T) {
			sdkRuntimeHealth.failed.Store(false)
			t.Cleanup(func() { sdkRuntimeHealth.failed.Store(false) })
			trap := errors.New("wasm error: out of bounds memory access")
			items := &failingRuntimeItems{listErr: trap}
			c := &SDKClient{client: &sdk.Client{ItemsAPI: items}}
			var err error
			switch operation {
			case "list":
				_, err = c.ListItems(context.Background(), "vault")
			case "lookup":
				_, err = c.UpsertItem(context.Background(), "vault", "title", nil)
			case "create":
				items.listErr = nil
				items.createErr = trap
				_, err = c.UpsertItem(context.Background(), "vault", "title", nil)
				if items.creates != 1 {
					t.Fatalf("ambiguous write replayed: %d calls", items.creates)
				}
			}
			if !errors.Is(err, trap) {
				t.Fatalf("original error lost: %v", err)
			}
			if RuntimeHealthError() == nil {
				t.Fatal("SDK trap did not fail liveness")
			}
		})
	}
}

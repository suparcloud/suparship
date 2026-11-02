package onepassword

import (
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
)

// All SDK clients share a process-wide WASM core. SDK v0.4.0 has no public
// reset API; constructing another client reuses the damaged core. Latch this
// failure until the process restarts instead of replaying ambiguous writes.
type runtimeHealth struct{ failed atomic.Bool }

var sdkRuntimeHealth runtimeHealth
var errRuntimeUnhealthy = errors.New("onepassword: WASM runtime requires process restart")

func (h *runtimeHealth) observe(err error) {
	if err == nil {
		return
	}
	message := strings.ToLower(err.Error())
	if !strings.Contains(message, "wasm error: out of bounds memory access") {
		return
	}
	if h.failed.CompareAndSwap(false, true) {
		// Never log the SDK payload: it may contain secret values or credentials.
		slog.Error("onepassword: WASM runtime failed; requesting recovery through liveness probe", "recovery", "process_restart")
	}
}

func (h *runtimeHealth) check() error {
	if h.failed.Load() {
		return errRuntimeUnhealthy
	}
	return nil
}

// RuntimeHealthError reports only a known unrecoverable local runtime trap.
// Authentication, permissions, rate limits and remote outages do not affect
// liveness. This check never calls the provider or acquires its invocation lock.
func RuntimeHealthError() error { return sdkRuntimeHealth.check() }

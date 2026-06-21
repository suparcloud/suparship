// Package audit defines the platform-wide audit seam.
//
// The open-source core ships SlogAuditor, which emits one structured log line
// per audited operation. Enterprise builds can supply an Auditor that also
// streams events to a SIEM or an immutable, tamper-evident store (wired via
// server.Config.Auditor) — a common SOC2 requirement. Sensitive values are
// never recorded, only identifiers and outcome.
//
// This complements secrets.SecretAuditor (which carries secret-specific
// metadata); this package covers app / project / promotion / RBAC operations.
package audit

import (
	"context"
	"log/slog"
	"time"
)

// Result values for Event.Result.
const (
	ResultSuccess = "success"
	ResultError   = "error"
	ResultDenied  = "denied"
)

// Event captures a single audited operation.
type Event struct {
	Time     time.Time         // when the event occurred
	Actor    string            // authenticated username, when known
	Action   string            // dotted verb, e.g. "app.create", "project.delete", "app.promote"
	Project  string            // owning project, when applicable
	Resource string            // resource identifier, e.g. an app name
	Result   string            // ResultSuccess | ResultError | ResultDenied
	Detail   map[string]string // additional non-sensitive context (e.g. from/to env)
}

// Auditor records audit events. Implementations must be safe for concurrent
// use and must never block the request path.
type Auditor interface {
	Record(ctx context.Context, e Event)
}

// SlogAuditor is the core's default Auditor: one structured "audit" log line
// per event.
type SlogAuditor struct{ logger *slog.Logger }

// NewSlogAuditor returns an Auditor that writes to logger. A nil logger makes
// Record a no-op.
func NewSlogAuditor(logger *slog.Logger) *SlogAuditor { return &SlogAuditor{logger: logger} }

// Record emits one structured log line for e.
func (a *SlogAuditor) Record(_ context.Context, e Event) {
	if a == nil || a.logger == nil {
		return
	}
	attrs := []any{"actor", e.Actor, "action", e.Action, "result", e.Result}
	if e.Project != "" {
		attrs = append(attrs, "project", e.Project)
	}
	if e.Resource != "" {
		attrs = append(attrs, "resource", e.Resource)
	}
	for k, v := range e.Detail {
		attrs = append(attrs, "detail."+k, v)
	}
	a.logger.Info("audit", attrs...)
}

// Nop is an Auditor that discards every event.
type Nop struct{}

// Record discards e.
func (Nop) Record(context.Context, Event) {}

// Resolve returns a, or a Nop when a is nil, so callers can record
// unconditionally without nil checks.
func Resolve(a Auditor) Auditor {
	if a == nil {
		return Nop{}
	}
	return a
}

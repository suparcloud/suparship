package server

import (
	"context"
	"time"

	"github.com/suparcloud/suparship/internal/audit"
)

// recordAudit emits an audit event for a completed operation. The actor is
// taken from the request session when present. A nil auditor is a no-op, so
// handlers can call this unconditionally on their success paths.
func recordAudit(ctx context.Context, a audit.Auditor, action, project, resource, result string, detail map[string]string) {
	if a == nil {
		return
	}
	actor := ""
	if s := sessionFromContext(ctx); s != nil {
		actor = s.Username
	}
	a.Record(ctx, audit.Event{
		Time:     time.Now(),
		Actor:    actor,
		Action:   action,
		Project:  project,
		Resource: resource,
		Result:   result,
		Detail:   detail,
	})
}

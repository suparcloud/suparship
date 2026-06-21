package server

import (
	"context"
	"testing"

	"github.com/suparcloud/suparship/internal/audit"
	"github.com/suparcloud/suparship/internal/session"
)

// captureAuditor records events for assertions.
type captureAuditor struct{ events []audit.Event }

func (c *captureAuditor) Record(_ context.Context, e audit.Event) { c.events = append(c.events, e) }

func TestRecordAuditCapturesActorFromSession(t *testing.T) {
	cap := &captureAuditor{}
	ctx := context.WithValue(context.Background(), sessionCtxKey, &session.Session{Username: "alice"})

	recordAudit(ctx, cap, "app.create", "shop", "web", audit.ResultSuccess, map[string]string{"template": "web-service"})

	if len(cap.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(cap.events))
	}
	e := cap.events[0]
	if e.Actor != "alice" {
		t.Errorf("actor = %q, want alice", e.Actor)
	}
	if e.Action != "app.create" || e.Project != "shop" || e.Resource != "web" || e.Result != audit.ResultSuccess {
		t.Errorf("unexpected event: %+v", e)
	}
	if e.Detail["template"] != "web-service" {
		t.Errorf("detail.template = %q, want web-service", e.Detail["template"])
	}
}

func TestRecordAuditNilActorWhenNoSession(t *testing.T) {
	cap := &captureAuditor{}
	recordAudit(context.Background(), cap, "project.delete", "shop", "shop", audit.ResultSuccess, nil)

	if len(cap.events) != 1 || cap.events[0].Actor != "" {
		t.Fatalf("expected 1 event with empty actor, got %+v", cap.events)
	}
}

func TestRecordAuditNilAuditorIsNoop(t *testing.T) {
	// Must not panic when the auditor is nil.
	recordAudit(context.Background(), nil, "app.delete", "shop", "web", audit.ResultSuccess, nil)
}

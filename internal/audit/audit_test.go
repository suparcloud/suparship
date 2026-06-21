package audit

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestSlogAuditorRecord(t *testing.T) {
	var buf bytes.Buffer
	a := NewSlogAuditor(slog.New(slog.NewTextHandler(&buf, nil)))

	a.Record(context.Background(), Event{
		Actor:    "alice",
		Action:   "app.create",
		Project:  "shop",
		Resource: "web",
		Result:   ResultSuccess,
		Detail:   map[string]string{"template": "web-service"},
	})

	out := buf.String()
	for _, want := range []string{
		"msg=audit", "actor=alice", "action=app.create",
		"project=shop", "resource=web", "result=success", "detail.template=web-service",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log line missing %q\ngot: %s", want, out)
		}
	}
}

func TestSlogAuditorNilLoggerIsNoop(t *testing.T) {
	// Must not panic.
	NewSlogAuditor(nil).Record(context.Background(), Event{Action: "x"})
	var a *SlogAuditor
	a.Record(context.Background(), Event{Action: "x"})
}

func TestNopAndResolve(t *testing.T) {
	Nop{}.Record(context.Background(), Event{Action: "x"}) // no panic, no output
	if _, ok := Resolve(nil).(Nop); !ok {
		t.Error("Resolve(nil) should return Nop")
	}
	a := NewSlogAuditor(slog.Default())
	if got := Resolve(a); got != a {
		t.Error("Resolve(non-nil) should return the same auditor")
	}
}

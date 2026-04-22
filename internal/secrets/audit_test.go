package secrets

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestAuditor_Log_NoSecretValues(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	auditor := NewAuditor(logger)

	secretValue := "super-secret-password-12345"

	auditor.Log(AuditEvent{
		Timestamp: time.Now(),
		Actor:     "alice",
		Action:    AuditActionUpsert,
		Scope:     Scope{Level: LevelOrg, Org: "default"},
		Keys:      []string{"DB_PASSWORD", "API_KEY"},
		Result:    "ok",
		LatencyMs: 42,
	})

	output := buf.String()
	if strings.Contains(output, secretValue) {
		t.Errorf("audit log contains secret value %q", secretValue)
	}
	if !strings.Contains(output, "secrets.audit") {
		t.Error("expected log message to contain 'secrets.audit'")
	}
	if !strings.Contains(output, "alice") {
		t.Error("expected log to contain actor name")
	}
	if !strings.Contains(output, "DB_PASSWORD") {
		t.Error("expected log to contain key name")
	}
	if !strings.Contains(output, "upsert") {
		t.Error("expected log to contain action")
	}
}

func TestAuditor_Log_StructuredFields(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	auditor := NewAuditor(logger)

	auditor.Log(AuditEvent{
		Timestamp: time.Now(),
		Actor:     "bob",
		Action:    AuditActionDelete,
		Scope: Scope{
			Level:   LevelAppEnv,
			Org:     "default",
			Env:     "prod",
			Project: "acme",
			App:     "web",
		},
		Keys:      []string{"OLD_KEY"},
		Result:    "ok",
		LatencyMs: 15,
	})

	output := buf.String()
	for _, expected := range []string{
		"scope.level=app-environment",
		"scope.env=prod",
		"scope.project=acme",
		"scope.app=web",
		"action=delete",
	} {
		if !strings.Contains(output, expected) {
			t.Errorf("expected log to contain %q, got: %s", expected, output)
		}
	}
}

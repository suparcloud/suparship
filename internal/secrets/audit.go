package secrets

import (
	"log/slog"
	"time"
)

// AuditAction describes the type of secret mutation.
type AuditAction string

const (
	AuditActionUpsert AuditAction = "upsert"
	AuditActionDelete AuditAction = "delete"
)

// AuditEvent captures the metadata of a secret mutation. Values are never
// included — only key names and scope attribution.
type AuditEvent struct {
	Timestamp time.Time   `json:"ts"`
	Actor     string      `json:"actor"`
	Action    AuditAction `json:"action"`
	Scope     Scope       `json:"scope"`
	Tier      Tier        `json:"tier"`
	App       string      `json:"app,omitempty"`
	Keys      []string    `json:"keys"`
	Result    string      `json:"result"`
	LatencyMs int64       `json:"latencyMs"`
}

// SecretAuditor records secret-mutation audit events. The open-source core
// ships *Auditor (structured slog). Enterprise builds can supply an
// implementation that additionally streams events to a SIEM or an immutable
// store, wired in via server.Config.SecretsAuditor. A nil SecretAuditor
// disables auditing.
type SecretAuditor interface {
	Log(event AuditEvent)
}

// Auditor emits structured log lines for secret mutations. It is the core's
// default SecretAuditor.
// K8s Event emission can be added once a recorder is plumbed in.
type Auditor struct {
	logger *slog.Logger
}

// NewAuditor creates an Auditor that writes to the given logger.
func NewAuditor(logger *slog.Logger) *Auditor {
	return &Auditor{logger: logger}
}

// Log emits a structured log line for a secret mutation.
func (a *Auditor) Log(event AuditEvent) {
	a.logger.Info("secrets.audit",
		"actor", event.Actor,
		"action", string(event.Action),
		"scope.kind", string(event.Scope.Kind),
		"scope.env", event.Scope.Env,
		"scope.cluster", event.Scope.Cluster,
		"tier", string(event.Tier),
		"app", event.App,
		"keys", event.Keys,
		"result", event.Result,
		"latencyMs", event.LatencyMs,
	)
}

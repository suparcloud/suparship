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

// Auditor emits structured log lines for secret mutations.
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

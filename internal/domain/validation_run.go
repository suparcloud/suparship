package domain

import "time"

// ValidationRun is durable controller progress, independent of a server process.
// Cursor coalesces queued builds to the newest Warehouse discovery after a run.
type ValidationRun struct {
	Phase     string    `json:"phase"`
	Freight   string    `json:"freight,omitempty"`
	Cursor    time.Time `json:"cursor,omitempty"`
	StartedAt time.Time `json:"startedAt,omitempty"`
	FailedAt  time.Time `json:"failedAt,omitempty"`
}

type ValidationObservation struct {
	CurrentFreight   string
	CurrentCreatedAt time.Time
	Active           bool
	Verified         bool
	Failed           bool
}

func (cd CDConfig) EphemeralValidation() bool {
	return cd.ReleaseValidationEnvironment != "" && cd.ReleaseValidationMode == "ephemeral"
}

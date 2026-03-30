// Package domain defines the MVP view types and storage interfaces for
// suparShip.
//
// All types in this package are plain Go structs with JSON tags — no
// Kubernetes imports, no YAML tags, no external framework dependencies.
// This keeps the domain layer cheap to test and trivial to stub.
//
// Every interface here is expected to have two implementations:
//
//   - A fake implementation (internal/fake) that returns deterministic
//     fixture data. No cluster required. Used for local development
//     (SUPARSHIP_CLUSTER_MODE=fake) and unit tests.
//
//   - A real implementation backed by Kubernetes ConfigMaps, Deployments,
//     and eventually ArgoCD / Kargo. Used in core and full install profiles.
//
// Start here when adding a new feature: define the domain type, extend
// the relevant interface, then wire fake first and real after.
package domain

import "time"

// Org represents the single organization in a suparShip installation.
type Org struct {
	Name        string    `json:"name"`
	DisplayName string    `json:"displayName"`
	CreatedAt   time.Time `json:"createdAt"`
}

// Project is a logical grouping of services sharing environments and a
// common promotion pipeline (e.g. dev → staging → prod).
type Project struct {
	Name         string        `json:"name"`
	DisplayName  string        `json:"displayName,omitempty"`
	Description  string        `json:"description,omitempty"`
	Environments []Environment `json:"environments"`
}

// Environment is an ordered deployment target within a project.
type Environment struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName,omitempty"`
	Order       int    `json:"order"`
}

// Service is a deployable workload belonging to a project.
type Service struct {
	Name         string `json:"name"`
	ProjectName  string `json:"projectName"`
	TemplateName string `json:"templateName"`
	DisplayName  string `json:"displayName,omitempty"`
	Description  string `json:"description,omitempty"`
}

// ServiceStatus describes the live runtime state of a service in one
// environment. Status is one of the Status* constants below.
type ServiceStatus struct {
	Status       string   `json:"status"`
	Environment  string   `json:"environment"`
	Replicas     int32    `json:"replicas"`
	Available    int32    `json:"available"`
	Image        string   `json:"image,omitempty"`
	IngressURLs  []string `json:"ingressUrls"`
	Namespace    string   `json:"namespace"`
	LastDeployed string   `json:"lastDeployed,omitempty"`
}

// Status values for ServiceStatus.Status.
const (
	StatusHealthy     = "healthy"
	StatusDegraded    = "degraded"
	StatusProgressing = "progressing"
	StatusNotDeployed = "not_deployed"
	StatusUnknown     = "unknown"
)

// Template describes a golden path for deploying a service.
// The full template schema (inputs, presets, engine config) lives in
// internal/tpl; Template here is the lightweight summary used by list
// and status views.
type Template struct {
	Name        string `json:"name"`
	Version     string `json:"version,omitempty"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Category    string `json:"category,omitempty"`
}

// Preview is an ephemeral, branch-scoped deployment of a service.
// The namespace convention is {project}-preview-{name}.
type Preview struct {
	Name        string    `json:"name"`
	ProjectName string    `json:"projectName"`
	ServiceName string    `json:"serviceName"`
	Namespace   string    `json:"namespace"`
	Status      string    `json:"status"`
	URL         string    `json:"url,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
}

// LogLine is a single line of log output from a running container.
type LogLine struct {
	Timestamp string `json:"timestamp,omitempty"`
	Text      string `json:"text"`
	Pod       string `json:"pod,omitempty"`
	Container string `json:"container,omitempty"`
}

package domain

import (
	"context"

	"github.com/suparcloud/suparship/internal/envconfig"
)

// Stack is a logical grouping of tightly-coupled apps inside a project (e.g. a
// "voiceai" service = web + agent-server + capacity-manager). It is a
// config-cascade + orchestration layer, NOT a deployment unit: member apps keep
// their own identity, ArgoCD Application, Kargo pipeline, and ingress. A stack
// adds a shared override layer (between project and app), an optional shared
// namespace for intra-stack DNS, and batch lifecycle actions.
//
// Membership is recorded on the app (AppSpec.Stack), so apps move in/out of a
// stack without changing their identity. The Stack record below holds the
// stack's own metadata + the shared overrides.
type Stack struct {
	// Name uniquely identifies the stack within its project (DNS label).
	Name string `json:"name" yaml:"name"`
	// ProjectName is the owning project.
	ProjectName string `json:"projectName" yaml:"projectName"`
	// Spec is the stack's desired configuration.
	Spec StackSpec `json:"spec" yaml:"spec"`
}

// StackSpec is the shared configuration a stack cascades onto its member apps.
// It layers below the project and above the app in the override hierarchy.
type StackSpec struct {
	// DisplayName is a human-friendly label shown in the UI.
	DisplayName string `json:"displayName,omitempty" yaml:"displayName,omitempty"`
	// Description provides optional context about the stack's purpose.
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
	// EnvConfig holds env vars + secret refs shared by every app in the stack
	// (Stack level of the hierarchy — overrides project, overridden by app).
	EnvConfig envconfig.EnvConfig `json:"envConfig,omitempty" yaml:"envConfig,omitempty"`
	// EnvConfigByEnv holds stack-level per-environment env config keyed by env
	// name — a stack's variables in one specific environment, layered above
	// EnvConfig and below app config. Mirrors EnvRawValues / project EnvConfigByEnv.
	EnvConfigByEnv map[string]envconfig.EnvConfig `json:"envConfigByEnv,omitempty" yaml:"envConfigByEnv,omitempty"`
	// RawValues is a freeform Helm values overlay applied to every member app,
	// layered above the template platform values and below the app's own
	// RawValues. May reference {platform.*}/{vars.*} tokens. No secrets.
	RawValues map[string]any `json:"rawValues,omitempty" yaml:"rawValues,omitempty"`
	// EnvRawValues holds per-environment Helm overlays keyed by env name,
	// layered after RawValues.
	EnvRawValues map[string]map[string]any `json:"envRawValues,omitempty" yaml:"envRawValues,omitempty"`
	// SharedNamespace, when true, co-locates the stack's member apps in a single
	// {project}-{stack}-{env} namespace so they reach each other by in-cluster
	// DNS without cross-namespace plumbing. (Honored in Phase 2.)
	SharedNamespace bool `json:"sharedNamespace,omitempty" yaml:"sharedNamespace,omitempty"`
	// NamespacePattern overrides the stack namespace naming when SharedNamespace
	// is set. Tokens: {org}, {project}, {stack}, {env}. Empty uses the default.
	NamespacePattern string `json:"namespacePattern,omitempty" yaml:"namespacePattern,omitempty"`
	// AutoPromote, when set true, opts every member app into automatic promotion
	// to prod (see CDConfig.AutoPromote). nil = inherit each app's own setting;
	// the effective per-app value is `app.CD.AutoPromote || *stack.AutoPromote`.
	AutoPromote *bool `json:"autoPromote,omitempty" yaml:"autoPromote,omitempty"`
}

// StackStore persists stacks. Implementations live in internal/kube.
type StackStore interface {
	// SaveStack creates or updates a stack.
	SaveStack(ctx context.Context, stack *Stack) error
	// GetStack returns a stack by project + name, or an error when absent.
	GetStack(ctx context.Context, projectName, stackName string) (*Stack, error)
	// ListStacks returns all stacks in a project.
	ListStacks(ctx context.Context, projectName string) ([]*Stack, error)
	// DeleteStack removes a stack record (not its member apps).
	DeleteStack(ctx context.Context, projectName, stackName string) error
}

// Package envconfig defines the hierarchical environment variable and secret
// configuration model for suparship.
//
// # Hierarchy
//
// Configuration is resolved in this order, with each level overriding the
// previous for duplicate keys:
//
//  1. Org        — applies to all apps in all projects
//  2. Environment — applies to all apps in one env type (staging/prod/preview)
//  3. Project    — applies to all apps in one project
//  4. App        — applies to all environments of one app
//  5. AppEnv     — applies to one app in one specific environment (wins all)
//
// # Storage
//
// Env vars (Vars) are stored in Kubernetes ConfigMaps.
// Secrets (SecretRefs) reference an external provider via External Secrets
// Operator and are pulled into Kubernetes Secrets in the target namespace.
//
// Upper three levels (Org, Environment, Project) are written once to the
// suparship-system namespace and replicated to app namespaces automatically
// via Stakater Replicator. Lower two levels (App, AppEnv) are baked into
// the Helm values.yaml at GitOps publish time.
//
// # Precedence
//
// When the same key exists at multiple levels, the lowest (most specific)
// level wins. Within a namespace, ConfigMaps are loaded via envFrom in order;
// Kubernetes last-wins semantics apply for duplicate keys across envFrom
// entries. Direct env: entries from ComponentSpec.Config always override
// envFrom for the same key.
package envconfig

// KnownProviders is the set of recognised external secret backend names.
// Each maps to a well-known ClusterSecretStore deployed in the cluster.
var KnownProviders = map[string]bool{
	"k8s":    true, // Kubernetes Secrets in suparship-system (demo/default)
	"vault":  true, // HashiCorp Vault
	"aws-sm": true, // AWS Secrets Manager
}

// SecretRef is a backend-agnostic reference to a single key in an external
// secret provider. The Provider field determines which ClusterSecretStore
// is used for resolution; the resulting Kubernetes Secret key is EnvKey.
//
// No secret values are stored here — only references.
type SecretRef struct {
	// Provider is the external secrets backend.
	// One of: "k8s", "vault", "aws-sm".
	// Maps to ClusterSecretStore named "suparship-{provider}-store".
	Provider string `json:"provider" yaml:"provider"`

	// Name is the secret name within the provider.
	// For k8s: the Kubernetes Secret name in suparship-system.
	// For vault: the secret path.
	// For aws-sm: the Secrets Manager secret ID.
	Name string `json:"name" yaml:"name"`

	// Key is the field/property within the named secret.
	Key string `json:"key" yaml:"key"`

	// EnvKey is the environment variable name injected into the container.
	// Must match ^[A-Za-z_][A-Za-z0-9_]*$.
	EnvKey string `json:"envKey" yaml:"envKey"`
}

// EnvConfig holds the environment variable and secret configuration for one
// level of the hierarchy. Vars are stored as ConfigMap data; SecretRefs are
// stored as ExternalSecret data references.
type EnvConfig struct {
	// Vars are plaintext key/value pairs stored in a ConfigMap.
	// Keys must be valid environment variable names.
	// Secret values MUST NOT appear here.
	Vars map[string]string `json:"vars,omitempty" yaml:"vars,omitempty"`

	// SecretRefs are backend-agnostic references to external secret values.
	// Each entry produces one data item in an ExternalSecret CR.
	SecretRefs []SecretRef `json:"secretRefs,omitempty" yaml:"secretRefs,omitempty"`
}

// IsEmpty reports whether c has no vars and no secret refs.
func (c EnvConfig) IsEmpty() bool {
	return len(c.Vars) == 0 && len(c.SecretRefs) == 0
}

// EnvLayers holds the resolved EnvConfig for each level of the hierarchy.
// Only the App and AppEnv layers are baked into Helm values.yaml; the upper
// three layers are replicated from suparship-system via Stakater Replicator.
type EnvLayers struct {
	// Org is the org-wide env config.
	Org EnvConfig
	// Env is the org-level per-environment-type env config.
	Env EnvConfig
	// Project is the project-level env config.
	Project EnvConfig
	// App is the app-level env config.
	App EnvConfig
	// AppEnv is the app-environment-level env config (most specific).
	AppEnv EnvConfig
}

// ResolvedEnvVar describes a single env var key in the fully merged
// configuration, annotated with which hierarchy level it came from.
// Secret values are never included — only key names and source attribution.
type ResolvedEnvVar struct {
	// Source is the hierarchy level that provides the winning value.
	// One of: "org", "environment", "project", "app", "app-environment".
	Source string `json:"source"`

	// IsSecret is true when the value comes from a SecretRef rather than Vars.
	// The value itself is never returned via the API.
	IsSecret bool `json:"isSecret"`
}

// LevelName constants are the canonical string identifiers for each level.
const (
	LevelOrg        = "org"
	LevelEnvironment = "environment"
	LevelProject    = "project"
	LevelApp        = "app"
	LevelAppEnv     = "app-environment"
)

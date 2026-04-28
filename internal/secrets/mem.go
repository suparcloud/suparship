package secrets

import (
	"context"
	"sort"
	"sync"
)

const memSystemNS = "suparship-system"

// MemBackend is an in-memory Backend for development and testing.
// All data is lost when the process exits.
type MemBackend struct {
	mu   sync.Mutex
	data map[string]map[string]map[string][]byte // ns -> name -> key -> value
}

// NewMemBackend creates an empty in-memory secret backend.
func NewMemBackend() *MemBackend {
	return &MemBackend{data: make(map[string]map[string]map[string][]byte)}
}

func (m *MemBackend) Upsert(_ context.Context, ns, name string, data map[string][]byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.data[ns] == nil {
		m.data[ns] = make(map[string]map[string][]byte)
	}
	if m.data[ns][name] == nil {
		m.data[ns][name] = make(map[string][]byte)
	}
	for k, v := range data {
		m.data[ns][name][k] = v
	}
	return nil
}

func (m *MemBackend) ListKeys(_ context.Context, ns, name string) ([]SecretEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entries := m.data[ns][name]
	keys := make([]string, 0, len(entries))
	for k := range entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]SecretEntry, len(keys))
	for i, k := range keys {
		out[i] = SecretEntry{Key: k}
	}
	return out, nil
}

func (m *MemBackend) DeleteKey(_ context.Context, ns, name, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.data[ns] != nil && m.data[ns][name] != nil {
		delete(m.data[ns][name], key)
	}
	return nil
}

// ── MemUpperLevelWriter ─────────────────────────────────────────────────────

// MemUpperLevelWriter implements UpperLevelWriter backed by a MemBackend,
// storing upper-level secrets under a virtual "suparship-system" namespace.
type MemUpperLevelWriter struct {
	backend *MemBackend
}

// NewMemUpperLevelWriter creates a MemUpperLevelWriter backed by the given MemBackend.
func NewMemUpperLevelWriter(backend *MemBackend) *MemUpperLevelWriter {
	return &MemUpperLevelWriter{backend: backend}
}

func (m *MemUpperLevelWriter) WriteOrgSecrets(ctx context.Context, data map[string][]byte) error {
	return m.backend.Upsert(ctx, memSystemNS, OrgSecretName(), data)
}

func (m *MemUpperLevelWriter) ReadOrgSecretKeys(ctx context.Context) ([]SecretEntry, error) {
	return m.backend.ListKeys(ctx, memSystemNS, OrgSecretName())
}

func (m *MemUpperLevelWriter) DeleteOrgSecretKey(ctx context.Context, key string) error {
	return m.backend.DeleteKey(ctx, memSystemNS, OrgSecretName(), key)
}

func (m *MemUpperLevelWriter) WriteEnvTypeSecrets(ctx context.Context, envType string, data map[string][]byte) error {
	return m.backend.Upsert(ctx, memSystemNS, EnvTypeSecretName(envType), data)
}

func (m *MemUpperLevelWriter) ReadEnvTypeSecretKeys(ctx context.Context, envType string) ([]SecretEntry, error) {
	return m.backend.ListKeys(ctx, memSystemNS, EnvTypeSecretName(envType))
}

func (m *MemUpperLevelWriter) DeleteEnvTypeSecretKey(ctx context.Context, envType, key string) error {
	return m.backend.DeleteKey(ctx, memSystemNS, EnvTypeSecretName(envType), key)
}

func (m *MemUpperLevelWriter) WriteProjectSecrets(ctx context.Context, project string, data map[string][]byte) error {
	return m.backend.Upsert(ctx, memSystemNS, ProjectSecretName(project), data)
}

func (m *MemUpperLevelWriter) ReadProjectSecretKeys(ctx context.Context, project string) ([]SecretEntry, error) {
	return m.backend.ListKeys(ctx, memSystemNS, ProjectSecretName(project))
}

func (m *MemUpperLevelWriter) DeleteProjectSecretKey(ctx context.Context, project, key string) error {
	return m.backend.DeleteKey(ctx, memSystemNS, ProjectSecretName(project), key)
}

func (m *MemUpperLevelWriter) WriteClusterSecrets(ctx context.Context, cluster string, data map[string][]byte) error {
	return m.backend.Upsert(ctx, memSystemNS, ClusterSecretName(cluster), data)
}

func (m *MemUpperLevelWriter) ReadClusterSecretKeys(ctx context.Context, cluster string) ([]SecretEntry, error) {
	return m.backend.ListKeys(ctx, memSystemNS, ClusterSecretName(cluster))
}

func (m *MemUpperLevelWriter) DeleteClusterSecretKey(ctx context.Context, cluster, key string) error {
	return m.backend.DeleteKey(ctx, memSystemNS, ClusterSecretName(cluster), key)
}

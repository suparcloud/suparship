package server

import (
	"context"
	"fmt"
	"testing"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/k8s"
	"github.com/suparcloud/suparship/internal/rbac"
)

// minimalKubeconfig is just enough for clientcmd to build a REST config + client
// (no connection is made at build time, so the unreachable server is fine).
const minimalKubeconfig = `apiVersion: v1
kind: Config
clusters:
- name: c
  cluster:
    server: https://workload.invalid:6443
contexts:
- name: c
  context:
    cluster: c
    user: u
current-context: c
users:
- name: u
  user:
    token: x
`

// fakeKubeconfigGetter returns a kubeconfig only for clusters in have.
type fakeKubeconfigGetter struct{ have map[string]bool }

func (f fakeKubeconfigGetter) GetKubeconfig(_ context.Context, clusterName string) ([]byte, error) {
	if f.have[clusterName] {
		return []byte(minimalKubeconfig), nil
	}
	return nil, fmt.Errorf("cluster %q not registered", clusterName)
}

func orgWithEnvs(envs ...rbac.OrgEnvironment) *rbac.Org {
	return &rbac.Org{Name: "test", Environments: envs}
}

func TestWorkloadClusterClient(t *testing.T) {
	pool := k8s.NewClusterClientPool(fakeKubeconfigGetter{have: map[string]bool{"remote-1": true}})

	tests := []struct {
		name       string
		pool       *k8s.ClusterClientPool
		org        *rbac.Org
		envName    string
		wantClient bool
		wantErr    bool
	}{
		{
			name:    "no pool wired → local fallback",
			pool:    nil,
			org:     orgWithEnvs(rbac.OrgEnvironment{Name: "staging", ActiveClusterRef: "remote-1"}),
			envName: "staging",
		},
		{
			name:    "env bound to a registered remote cluster → remote client",
			pool:    pool,
			org:     orgWithEnvs(rbac.OrgEnvironment{Name: "staging", ClusterRefs: []string{"remote-1"}, ActiveClusterRef: "remote-1"}),
			envName: "staging", wantClient: true,
		},
		{
			name:    "ClusterRefs fallback when ActiveClusterRef empty → remote client",
			pool:    pool,
			org:     orgWithEnvs(rbac.OrgEnvironment{Name: "staging", ClusterRefs: []string{"remote-1"}}),
			envName: "staging", wantClient: true,
		},
		{
			name:    "env bound to an unregistered cluster → error, no local fallback",
			pool:    pool,
			org:     orgWithEnvs(rbac.OrgEnvironment{Name: "staging", ActiveClusterRef: "missing"}),
			envName: "staging", wantErr: true,
		},
		{
			name:    "env not bound to any cluster → local fallback",
			pool:    pool,
			org:     orgWithEnvs(rbac.OrgEnvironment{Name: "staging"}),
			envName: "staging",
		},
		{
			name:    "env not present in org → local fallback",
			pool:    pool,
			org:     orgWithEnvs(rbac.OrgEnvironment{Name: "prod", ActiveClusterRef: "remote-1"}),
			envName: "staging",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ah := &appHandler{orgProvider: &staticOrgProvider{org: tc.org}, clusterPool: tc.pool}
			client, err := ah.workloadClusterClient(context.Background(), tc.envName)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected an error for an unregistered cluster, got nil")
				}
				if client != nil {
					t.Error("client must be nil when the workload cluster is unreachable (no local fallback)")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantClient && client == nil {
				t.Error("expected a remote workload-cluster client, got nil")
			}
			if !tc.wantClient && client != nil {
				t.Error("expected nil (local fallback), got a remote client")
			}
		})
	}
}

func TestWorkloadClustersForEnv(t *testing.T) {
	pool := k8s.NewClusterClientPool(fakeKubeconfigGetter{have: map[string]bool{"c1": true, "c2": true}})
	ctx := context.Background()

	// active mode → only the active cluster.
	ah := &appHandler{clusterPool: pool, orgProvider: &staticOrgProvider{org: orgWithEnvs(
		rbac.OrgEnvironment{Name: "staging", ClusterRefs: []string{"c1", "c2"}, ActiveClusterRef: "c1", DeployMode: rbac.DeployModeActive},
	)}}
	clients, unreachable, routed := ah.workloadClustersForEnv(ctx, "staging")
	if !routed || len(clients) != 1 || clients[0].name != "c1" || len(unreachable) != 0 {
		t.Fatalf("active: got clients=%v unreachable=%v routed=%v", names(clients), unreachable, routed)
	}

	// all mode → every cluster.
	ah.orgProvider = &staticOrgProvider{org: orgWithEnvs(
		rbac.OrgEnvironment{Name: "staging", ClusterRefs: []string{"c1", "c2"}, DeployMode: rbac.DeployModeAll},
	)}
	clients, _, routed = ah.workloadClustersForEnv(ctx, "staging")
	if !routed || len(clients) != 2 {
		t.Fatalf("all: expected 2 clients, got %v (routed=%v)", names(clients), routed)
	}

	// all mode with an unregistered cluster → it lands in unreachable.
	ah.orgProvider = &staticOrgProvider{org: orgWithEnvs(
		rbac.OrgEnvironment{Name: "staging", ClusterRefs: []string{"c1", "missing"}, DeployMode: rbac.DeployModeAll},
	)}
	clients, unreachable, routed = ah.workloadClustersForEnv(ctx, "staging")
	if !routed || len(clients) != 1 || len(unreachable) != 1 || unreachable[0] != "missing" {
		t.Fatalf("unreachable: got clients=%v unreachable=%v", names(clients), unreachable)
	}

	// no pool → not routed (local fallback).
	ah.clusterPool = nil
	if _, _, r := ah.workloadClustersForEnv(ctx, "staging"); r {
		t.Error("no pool should not route")
	}
}

// A preview's status must be read from its BASE env's cluster — the preview's
// own name (e.g. "pr-712") is not a configured org env, so without base-env
// routing it falls back to the local cluster and always reads 0/0 "not deployed".
// Proof: with the base env "staging" bound to an unregistered cluster, enriching
// the preview surfaces that cluster as unreachable; routing on "pr-712" instead
// would not be routed at all (no such diagnostic).
func TestEnrichPreviewRoutesViaBaseEnv(t *testing.T) {
	pool := k8s.NewClusterClientPool(fakeKubeconfigGetter{have: map[string]bool{}})
	ah := &appHandler{
		clusterPool: pool,
		orgProvider: &staticOrgProvider{org: orgWithEnvs(
			rbac.OrgEnvironment{Name: "staging", ActiveClusterRef: "missing"},
		)},
	}
	env := &domain.AppEnvironment{
		AppName: "web", ProjectName: "voiceai", EnvName: "pr-712",
		EnvType: domain.AppEnvPreview, BaseEnv: "staging", Namespace: "voiceai-preview",
	}
	ah.enrichEnvWithLiveStatus(context.Background(), "web", env)

	found := false
	for _, d := range env.Status.Diagnostics {
		if d.Cluster == "missing" {
			found = true
		}
	}
	if !found {
		t.Fatalf("preview should route via base env 'staging' to cluster 'missing'; diagnostics=%+v", env.Status.Diagnostics)
	}
}

func names(cs []namedClient) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.name
	}
	return out
}

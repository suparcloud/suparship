package server

import (
	"context"
	"fmt"
	"testing"

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

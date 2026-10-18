package k8s

import (
	"context"
	"testing"

	"k8s.io/client-go/rest"
)

type poolKubeconfig struct{}

func (poolKubeconfig) GetKubeconfig(context.Context, string) ([]byte, error) {
	return []byte(`apiVersion: v1
kind: Config
clusters:
- name: test
  cluster:
    server: https://127.0.0.1:6443
contexts:
- name: test
  context:
    cluster: test
current-context: test
`), nil
}
func TestWorkloadPoolAppliesConfiguredBudgetToBothClients(t *testing.T) {
	calls := 0
	pool := NewClusterClientPool(poolKubeconfig{}, WithRateLimit(50, 100), func(cfg *rest.Config) {
		calls++
		if cfg.QPS != 50 || cfg.Burst != 100 {
			t.Fatalf("workload budget not applied: %v/%v", cfg.QPS, cfg.Burst)
		}
	})
	client, err := pool.Get(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	if got := client.CoreV1().RESTClient().GetRateLimiter().QPS(); got != 50 {
		t.Fatalf("effective QPS=%v", got)
	}
	if _, err = pool.GetDynamic(context.Background(), "test"); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Get(context.Background(), "test"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("expected one configuration per client, got %d", calls)
	}
}

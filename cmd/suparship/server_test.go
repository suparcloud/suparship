package main

import (
	"context"
	"errors"
	"testing"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/envconfig"
	"github.com/suparcloud/suparship/internal/gitops"
	"github.com/suparcloud/suparship/internal/rbac"
	"github.com/suparcloud/suparship/internal/secrets"
	"github.com/suparcloud/suparship/internal/tpl"
	"k8s.io/client-go/kubernetes/fake"
)

func overlayTemplate(name, version string) *tpl.Template {
	return &tpl.Template{
		APIVersion: tpl.CurrentAPIVersion,
		Kind:       tpl.TemplateKind,
		Metadata:   tpl.Metadata{Name: name, Version: version},
		Spec:       tpl.TemplateSpec{Title: name, Engine: tpl.Engine{Type: tpl.EngineHelm}},
	}
}

// TestResolveTemplate_ClusterFetchErrorFailsLoud locks in F2: a cluster-fetch
// failure must propagate as an error so the publish path aborts rather than
// silently shipping values.yaml without the PE platform/env overlays.
func TestResolveTemplate_ClusterFetchErrorFailsLoud(t *testing.T) {
	a := &gitOpsPublisherAdapter{
		builtin: []*tpl.Template{overlayTemplate("voiceai-agent", "1.0.0")},
		clusterLoader: func(context.Context) ([]*tpl.Template, error) {
			return nil, errors.New("apiserver unavailable")
		},
	}
	got, err := a.resolveTemplate(context.Background(), "voiceai-agent")
	if err == nil {
		t.Fatal("expected an error when the cluster fetch fails, got nil")
	}
	if got != nil {
		t.Fatalf("expected nil template on fetch error, got %+v", got)
	}
}

// TestResolveTemplate_ClusterOverridesBuiltin: on a clean fetch, the cluster
// copy wins over the built-in of the same name.
func TestResolveTemplate_ClusterOverridesBuiltin(t *testing.T) {
	a := &gitOpsPublisherAdapter{
		builtin: []*tpl.Template{overlayTemplate("voiceai-agent", "1.0.0")},
		clusterLoader: func(context.Context) ([]*tpl.Template, error) {
			return []*tpl.Template{overlayTemplate("voiceai-agent", "2.0.0")}, nil
		},
	}
	got, err := a.resolveTemplate(context.Background(), "voiceai-agent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil || got.Metadata.Version != "2.0.0" {
		t.Fatalf("cluster copy should win: got %+v", got)
	}
}

// TestResolveTemplate_NoLoaderUsesBuiltin: nil loader → built-in fallback,
// no error.
func TestResolveTemplate_NoLoaderUsesBuiltin(t *testing.T) {
	a := &gitOpsPublisherAdapter{builtin: []*tpl.Template{overlayTemplate("api", "1.0.0")}}
	got, err := a.resolveTemplate(context.Background(), "api")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil || got.Metadata.Version != "1.0.0" {
		t.Fatalf("expected built-in fallback, got %+v", got)
	}
	if missing, _ := a.resolveTemplate(context.Background(), "nope"); missing != nil {
		t.Fatalf("expected nil for unknown name, got %+v", missing)
	}
}

// TestSetPlatformOverlays_AppliesDefaultAndEnv: the resolved template's
// DefaultValues + EnvValues[env] land on the publish env.
func TestSetPlatformOverlays_AppliesDefaultAndEnv(t *testing.T) {
	tmpl := overlayTemplate("voiceai-agent", "1.0.0")
	tmpl.Spec.DefaultValues = map[string]any{"replicas": 1}
	tmpl.Spec.EnvValues = map[string]map[string]any{
		"prod": {"replicas": 5},
	}

	pub := gitops.AppPublishEnv{EnvName: "prod"}
	setPlatformOverlays(&pub, tmpl, nil, "prod")

	if v := pub.PlatformDefaultValues["replicas"]; v != 1 {
		t.Fatalf("default overlay not applied: %v", pub.PlatformDefaultValues)
	}
	if v := pub.PlatformEnvValues["replicas"]; v != 5 {
		t.Fatalf("prod env overlay not applied: %v", pub.PlatformEnvValues)
	}
}

// TestSetPlatformOverlays_OrgOverrideMergedOnTop: the org override layers on top
// of the template's own values (org wins) for both the default and env slices.
func TestSetPlatformOverlays_OrgOverrideMergedOnTop(t *testing.T) {
	tmpl := overlayTemplate("voiceai-agent", "1.0.0")
	tmpl.Spec.DefaultValues = map[string]any{"replicas": 1, "image": map[string]any{"tag": "base"}}
	tmpl.Spec.EnvValues = map[string]map[string]any{"prod": {"replicas": 3}}
	ov := &domain.TemplateOverride{
		DefaultValues: map[string]any{"replicas": 2}, // org > template default
		EnvValues:     map[string]map[string]any{"prod": {"replicas": 7}},
	}

	pub := gitops.AppPublishEnv{EnvName: "prod"}
	setPlatformOverlays(&pub, tmpl, ov, "prod")

	if pub.PlatformDefaultValues["replicas"] != 2 {
		t.Errorf("default replicas = %v, want 2 (org over template)", pub.PlatformDefaultValues["replicas"])
	}
	// template's non-overridden default key survives the merge.
	if img := pub.PlatformDefaultValues["image"].(map[string]any); img["tag"] != "base" {
		t.Errorf("template default key lost: %v", img)
	}
	if pub.PlatformEnvValues["replicas"] != 7 {
		t.Errorf("prod replicas = %v, want 7 (org env over template env)", pub.PlatformEnvValues["replicas"])
	}
}

// TestSetPlatformOverlays_ClusterValuesPassedThrough: the org per-cluster overlay
// is threaded onto AppPublishEnv (env-agnostic; publisher selects the target
// cluster's block per values.yaml).
func TestSetPlatformOverlays_ClusterValuesPassedThrough(t *testing.T) {
	tmpl := overlayTemplate("voiceai-agent", "1.0.0")
	ov := &domain.TemplateOverride{
		ClusterValues: map[string]map[string]any{
			"eks-uswest": {"ingress": map[string]any{"annotations": map[string]any{"aws": "nlb"}}},
		},
	}
	pub := gitops.AppPublishEnv{EnvName: "prod"}
	setPlatformOverlays(&pub, tmpl, ov, "prod")

	got := pub.PlatformClusterValues["eks-uswest"]["ingress"].(map[string]any)["annotations"].(map[string]any)
	if got["aws"] != "nlb" {
		t.Errorf("cluster block not passed through: %v", pub.PlatformClusterValues)
	}
}

// TestSetPlatformOverlays_NilTemplateNoOp: a nil template (app has no matching
// template) leaves the publish env untouched.
func TestSetPlatformOverlays_NilTemplateNoOp(t *testing.T) {
	pub := gitops.AppPublishEnv{EnvName: "prod"}
	setPlatformOverlays(&pub, nil, nil, "prod")
	if pub.PlatformDefaultValues != nil || pub.PlatformEnvValues != nil {
		t.Fatalf("nil template should be a no-op, got %+v / %+v", pub.PlatformDefaultValues, pub.PlatformEnvValues)
	}
}

// TestEnrichPubEnv_AlwaysEnsuresBaselineAppSecret: an app with zero secrets
// still gets the app-scope item ensured and GlobalApp presence forced, so the
// publisher emits a <app>-secrets ExternalSecret (→ a K8s Secret) the chart's
// envFrom can resolve.
func TestEnrichPubEnv_AlwaysEnsuresBaselineAppSecret(t *testing.T) {
	vault := secrets.NewMemVaultStore()
	a := &gitOpsPublisherAdapter{vault: vault}
	app := &domain.App{Name: "api", ProjectName: "proj"}

	var pub gitops.AppPublishEnv
	a.enrichPubEnvWithSecrets(context.Background(), &rbac.Org{}, app, "staging", &pub)

	if !pub.ScopeKeys.GlobalApp {
		t.Fatal("expected GlobalApp presence forced true for an app with no secrets")
	}
	es := gitops.BuildAppExternalSecret(gitops.WorkloadExternalSecretParams{
		App: "api", Namespace: "ns", Env: "staging", Presence: pub.ScopeKeys, UnifiedStore: true,
	})
	if es == nil {
		t.Fatal("expected a non-nil ExternalSecret for an app with no secrets")
	}
}

// TestEnrichPubEnv_NoVaultNoForce: with no vault configured there is no backend
// to extract from, so presence is left untouched (no dangling ExternalSecret ref).
func TestEnrichPubEnv_NoVaultNoForce(t *testing.T) {
	a := &gitOpsPublisherAdapter{} // vault nil
	app := &domain.App{Name: "api", ProjectName: "proj"}
	var pub gitops.AppPublishEnv
	a.enrichPubEnvWithSecrets(context.Background(), &rbac.Org{}, app, "staging", &pub)
	if pub.ScopeKeys.GlobalApp {
		t.Fatal("without a vault, GlobalApp must not be forced")
	}
}

// TestMergeAllEnvVars_ClusterEnvWinsAndLayersFallThrough locks the env-var
// precedence on the real publish path: cluster-env (per cluster, per env) is the
// most specific layer and wins, cluster-global overrides app-env, and keys set
// only at a lower layer fall through untouched.
func TestMergeAllEnvVars_ClusterEnvWinsAndLayersFallThrough(t *testing.T) {
	client := fake.NewSimpleClientset()
	w := envconfig.NewUpperLevelEnvWriter(client)
	ctx := context.Background()

	// LEVEL is written at every scope so we can see who wins; each scope also sets
	// a unique key so we can confirm it fell through.
	if err := w.WriteClusterEnvConfig(ctx, "eks-aws", envconfig.EnvConfig{
		Vars: map[string]string{"LEVEL": "cluster-global", "ONLY_CLUSTER_GLOBAL": "1"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteClusterEnvScopedConfig(ctx, "eks-aws", "staging", envconfig.EnvConfig{
		Vars: map[string]string{"LEVEL": "cluster-env", "ONLY_CLUSTER_ENV": "1"},
	}); err != nil {
		t.Fatal(err)
	}

	org := &rbac.Org{
		EnvConfig: envconfig.EnvConfig{Vars: map[string]string{"LEVEL": "org", "ONLY_ORG": "1"}},
		Environments: []rbac.OrgEnvironment{{
			Name:      "staging",
			EnvConfig: envconfig.EnvConfig{Vars: map[string]string{"LEVEL": "env", "ONLY_ENV": "1"}},
		}},
	}
	app := &domain.App{
		Name: "api", ProjectName: "demo",
		Spec: domain.AppSpec{
			EnvConfig: envconfig.EnvConfig{Vars: map[string]string{"LEVEL": "app", "ONLY_APP": "1"}},
			EnvironmentDefaults: map[string]domain.EnvironmentOverride{
				"staging": {EnvConfig: envconfig.EnvConfig{Vars: map[string]string{"LEVEL": "app-env", "ONLY_APP_ENV": "1"}}},
			},
		},
	}

	a := &gitOpsPublisherAdapter{envConfigReader: w}
	got := a.mergeAllEnvVars(ctx, app, "staging", "eks-aws", org)

	if got["LEVEL"] != "cluster-env" {
		t.Errorf("LEVEL = %q, want cluster-env (most specific wins)", got["LEVEL"])
	}
	for k := range map[string]string{
		"ONLY_ORG": "", "ONLY_ENV": "", "ONLY_APP": "", "ONLY_APP_ENV": "",
		"ONLY_CLUSTER_GLOBAL": "", "ONLY_CLUSTER_ENV": "",
	} {
		if got[k] != "1" {
			t.Errorf("%s = %q, want 1 (should fall through)", k, got[k])
		}
	}

	// A different env on the same cluster does NOT see staging's cluster-env vars.
	prod := a.mergeAllEnvVars(ctx, app, "prod", "eks-aws", org)
	if _, ok := prod["ONLY_CLUSTER_ENV"]; ok {
		t.Error("prod merge leaked staging's cluster-env var")
	}
	if prod["LEVEL"] != "cluster-global" {
		t.Errorf("prod LEVEL = %q, want cluster-global (no cluster-env for prod, wins over app)", prod["LEVEL"])
	}
}

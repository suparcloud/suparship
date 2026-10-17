package gitops

import (
	"github.com/suparcloud/suparship/internal/domain"
	"gopkg.in/yaml.v3"
	"os"
	"path/filepath"
	"testing"
)

func TestPreparedReleaseSurvivesRepublishAndActivates(t *testing.T) {
	root := t.TempDir()
	p, err := NewPublisher(PublisherConfig{RepoURL: "http://localhost/gitops.git", SyncAutomated: true})
	if err != nil {
		t.Fatal(err)
	}
	app := &domain.App{Name: "web", ProjectName: "demo"}
	env := AppPublishEnv{EnvName: "prod", Bound: true, PrepareOnly: true}
	pending, err := p.promotionPending(root, app, env)
	if err != nil || !pending {
		t.Fatalf("prepare: %v %v", pending, err)
	}
	env.PrepareOnly = false
	pending, err = p.promotionPending(root, app, env)
	if err != nil || !pending {
		t.Fatalf("republish activated pending target: %v %v", pending, err)
	}
	path := filepath.Join(root, releaseStatePath("", "demo", "web", "prod"))
	if err = os.WriteFile(path, []byte("pending: false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	pending, err = p.promotionPending(root, app, env)
	if err != nil || pending {
		t.Fatalf("activation not preserved: %v %v", pending, err)
	}
	if err = os.WriteFile(path, []byte("pending: [invalid]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = p.promotionPending(root, app, env); err == nil {
		t.Fatal("corrupt state must fail closed")
	}
}

func TestPreparedApplicationSetHasNoInheritedAutoSync(t *testing.T) {
	for _, build := range []func(AppSetEnv, string, AppSetOptions) *ApplicationSet{BuildArgoAppSet, BuildArgoExternalAppSet} {
		as := build(AppSetEnv{EnvName: "prod"}, "http://gitops", AppSetOptions{SyncAutomated: true})
		if as.Spec.Template.Spec.SyncPolicy.Automated != nil {
			t.Fatal("pending applications inherit automatic sync")
		}
		if len(as.Spec.Generators) != 2 {
			t.Fatal("missing disjoint generators")
		}
		raw, err := yaml.Marshal(as)
		if err != nil {
			t.Fatal(err)
		}
		var decoded map[string]any
		if err = yaml.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		gens := decoded["spec"].(map[string]any)["generators"].([]any)
		pending := gens[1].(map[string]any)
		if pending["selector"].(map[string]any)["matchLabels"].(map[string]any)["promotionPending"] != "true" {
			t.Fatal("invalid pending selector wire format")
		}
		active := gens[0].(map[string]any)["git"].(map[string]any)["template"].(map[string]any)
		if active["spec"].(map[string]any)["syncPolicy"].(map[string]any)["automated"] == nil {
			t.Fatal("active application lost automatic sync")
		}
	}
}

func TestReleaseActivationIsCommittedWithImages(t *testing.T) {
	app := &domain.App{Name: "web", ProjectName: "demo"}
	env := domain.AppEnvironment{EnvName: "prod"}
	opts := KargoBuildOptions{GitOpsRepoURL: "http://gitops", Images: []KargoImage{{Repository: "image", TagKey: "image.tag"}}}
	stage := BuildKargoStage(app, env, []string{"staging"}, opts)
	image, activation, commit, push, sync := -1, -1, -1, -1, -1
	for i, s := range stage.Spec.PromotionTemplate.Spec.Steps {
		switch s.Uses {
		case "yaml-update":
			if s.Config["path"] == "./src/envs/prod/demo/web/values.yaml" {
				image = i
			} else {
				activation = i
			}
		case "git-commit":
			commit = i
		case "git-push":
			push = i
		case "argocd-update":
			sync = i
		}
	}
	if !(image >= 0 && image < activation && activation < commit && commit < push && push < sync) {
		t.Fatalf("unsafe ordering: %d %d %d %d %d", image, activation, commit, push, sync)
	}

	last := stage.Spec.PromotionTemplate.Spec.Steps[len(stage.Spec.PromotionTemplate.Spec.Steps)-1]
	if last.Uses != "argocd-update" {
		t.Fatal("missing Kargo Stage health registration")
	}
	apps := stage.Spec.PromotionTemplate.Spec.Steps[sync].Config["apps"].([]map[string]any)
	if _, ok := apps[0]["sources"]; ok {
		t.Fatal("shared branch health must not pin an unrelated Git revision")
	}

}

func TestExternalReleaseFanoutUsesEveryClusterValues(t *testing.T) {
	app := &domain.App{Name: "web", ProjectName: "demo"}
	opts := KargoBuildOptions{GitOpsRepoURL: "http://gitops", ChartMode: AppMetadataChartTypeExternal, Clusters: []ClusterTarget{{Name: "east"}, {Name: "west"}}, Images: []KargoImage{{Repository: "image", TagKey: "image.tag"}}}
	stage := BuildKargoStage(app, domain.AppEnvironment{EnvName: "prod"}, []string{"staging"}, opts)
	paths := map[string]bool{}
	for _, step := range stage.Spec.PromotionTemplate.Spec.Steps {
		if step.Uses == "yaml-update" {
			path, _ := step.Config["path"].(string)
			paths[path] = true
		}
	}
	for _, cluster := range []string{"east", "west"} {
		if !paths["./src/envs-external/prod/_clusters/"+cluster+"/demo/web/values.yaml"] {
			t.Fatal("missing cluster values update", cluster)
		}
	}
	if paths["./src/envs/prod/demo/web/values.yaml"] {
		t.Fatal("updates nonexistent shared inline values")
	}
}

func TestStatefulApplicationAuthorizesKargoAndPreservesDataPolicy(t *testing.T) {
	app := &domain.App{Name: "web", ProjectName: "demo"}
	component := domain.ComponentSpec{Name: "db", Template: &domain.AppTemplateRef{Name: "postgres", Version: "0.1.0"}}
	opts := ComposedBuildOptions{AppName: "demo-web-prod", EnvName: "prod", KargoStage: "kargo-demo:web-prod", SyncAutomated: true}
	manifest := BuildComponentApplication(app, component, opts)
	if manifest.Metadata.Annotations["kargo.akuity.io/authorized-stage"] != "kargo-demo:web-prod" {
		t.Fatal("stateful application rejects promotion health checks")
	}
	if manifest.Spec.SyncPolicy.Automated.Prune {
		t.Fatal("stateful data protection changed")
	}
	disablePreparedSync(manifest)
	if manifest.Spec.SyncPolicy.Automated != nil {
		t.Fatal("prepared database auto-syncs")
	}
}

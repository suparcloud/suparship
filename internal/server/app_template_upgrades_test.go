package server

import (
	"context"
	"testing"

	"k8s.io/client-go/kubernetes/fake"

	"github.com/suparcloud/suparship/internal/domain"
)

// A composed app on two templates gets per-component hints keyed to each
// component's OWN template — the whole point of moving detection off the single
// app-level template.
func TestDecorateTemplateUpgrades_PerComponentTemplates(t *testing.T) {
	kc := fake.NewSimpleClientset(
		archiveCM("web-service", "1.0.0"), archiveCM("web-service", "2.0.0"),
		archiveCM("job", "3.0.0"),
	)
	ah := &appHandler{kubeClient: kc, templateVersionCache: newTemplateVersionCache(templateVersionsTTL)}

	app := upgradeTestApp("demo",
		comp("web", "web-service", "1.0.0"),
		comp("migrate", "job", "3.0.0"),
	)
	detail := appToDetailDTO(app, nil)
	ah.decorateTemplateUpgrades(context.Background(), &detail)

	byName := map[string]ComponentSummaryDTO{}
	for _, c := range detail.Components {
		byName[c.Name] = c
	}
	if got := byName["web"]; got.LatestVersion != "2.0.0" || !got.UpgradeAvailable {
		t.Errorf("web = {latest:%q upgrade:%v}, want {2.0.0 true}", got.LatestVersion, got.UpgradeAvailable)
	}
	// migrate is already on its template's newest archive — no upgrade, but the
	// latest is still reported so the picker can show the current selection.
	if got := byName["migrate"]; got.LatestVersion != "3.0.0" || got.UpgradeAvailable {
		t.Errorf("migrate = {latest:%q upgrade:%v}, want {3.0.0 false}", got.LatestVersion, got.UpgradeAvailable)
	}
	if detail.UpgradesAvailable != 1 {
		t.Errorf("UpgradesAvailable = %d, want 1", detail.UpgradesAvailable)
	}
	if detail.TemplateLatestVersion != "2.0.0" {
		t.Errorf("TemplateLatestVersion = %q, want 2.0.0 (the primary template)", detail.TemplateLatestVersion)
	}
	// Both templates' version lists ride along so the picker needs no extra calls.
	if len(detail.TemplateVersions["web-service"]) != 2 || len(detail.TemplateVersions["job"]) != 1 {
		t.Errorf("TemplateVersions = %+v, want web-service:2 job:1", detail.TemplateVersions)
	}
	// Newest-first ordering is what the UI's "latest" reads.
	if detail.TemplateVersions["web-service"][0].Version != "2.0.0" {
		t.Errorf("versions not newest-first: %+v", detail.TemplateVersions["web-service"])
	}
}

// A built-in template has no archive ConfigMaps. That must read as "not
// version-managed" — no latest, no upgrade badge — rather than as an upgrade to
// an empty version.
func TestDecorateTemplateUpgrades_UnarchivedTemplateIsNotVersionManaged(t *testing.T) {
	ah := &appHandler{
		kubeClient:           fake.NewSimpleClientset(),
		templateVersionCache: newTemplateVersionCache(templateVersionsTTL),
	}

	app := upgradeTestApp("demo", comp("web", "web-service", "1.0.0"))
	detail := appToDetailDTO(app, nil)
	ah.decorateTemplateUpgrades(context.Background(), &detail)

	if c := detail.Components[0]; c.LatestVersion != "" || c.UpgradeAvailable {
		t.Errorf("component = {latest:%q upgrade:%v}, want empty/false", c.LatestVersion, c.UpgradeAvailable)
	}
	if detail.UpgradesAvailable != 0 {
		t.Errorf("UpgradesAvailable = %d, want 0", detail.UpgradesAvailable)
	}
	if len(detail.TemplateVersions) != 0 {
		t.Errorf("TemplateVersions = %+v, want empty", detail.TemplateVersions)
	}
}

// An unpinned component already tracks whatever the registry serves, so it must
// not be reported as behind.
func TestDecorateTemplateUpgrades_UnpinnedComponentIsNotBehind(t *testing.T) {
	kc := fake.NewSimpleClientset(archiveCM("web-service", "2.0.0"))
	ah := &appHandler{kubeClient: kc, templateVersionCache: newTemplateVersionCache(templateVersionsTTL)}

	app := upgradeTestApp("demo", comp("web", "web-service", ""))
	detail := appToDetailDTO(app, nil)
	ah.decorateTemplateUpgrades(context.Background(), &detail)

	if detail.Components[0].UpgradeAvailable {
		t.Error("an unpinned component tracks latest; it must not show an upgrade")
	}
	if detail.Components[0].LatestVersion != "2.0.0" {
		t.Errorf("latest = %q, want 2.0.0 still reported", detail.Components[0].LatestVersion)
	}
}

// Without a kubeClient (fake mode, tests) decoration is a no-op rather than a
// failure — the hint is an affordance, not data the page depends on.
func TestDecorateTemplateUpgrades_NoKubeClientIsNoOp(t *testing.T) {
	ah := &appHandler{}
	app := upgradeTestApp("demo", comp("web", "web-service", "1.0.0"))
	detail := appToDetailDTO(app, nil)
	ah.decorateTemplateUpgrades(context.Background(), &detail)

	if detail.UpgradesAvailable != 0 || detail.TemplateLatestVersion != "" {
		t.Errorf("expected a no-op, got %+v", detail)
	}
}

// The component DTO must carry the pinned version, without which the UI's edit
// round-trip drops it and the server re-pins to latest.
func TestComponentDTOs_CarriesTemplateVersion(t *testing.T) {
	dtos := componentDTOs([]domain.ComponentSpec{
		comp("web", "web-service", "1.2.3"),
	}, nil)

	if len(dtos) != 1 {
		t.Fatalf("expected 1 dto, got %d", len(dtos))
	}
	if dtos[0].Template != "web-service" || dtos[0].TemplateVersion != "1.2.3" {
		t.Errorf("dto = {%q %q}, want {web-service 1.2.3}", dtos[0].Template, dtos[0].TemplateVersion)
	}
}

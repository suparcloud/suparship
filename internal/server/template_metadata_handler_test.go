package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"k8s.io/client-go/kubernetes/fake"

	"github.com/suparcloud/suparship/internal/kube"
	"github.com/suparcloud/suparship/internal/tpl"
)

func metadataTestTemplate() *tpl.Template {
	return &tpl.Template{
		APIVersion: tpl.CurrentAPIVersion,
		Kind:       tpl.TemplateKind,
		Metadata:   tpl.Metadata{Name: "voiceai-livekit-agent", Version: "0.1.0"},
		Spec: tpl.TemplateSpec{
			Title:    "VoiceAI LiveKit Agent",
			Category: "web", // auto-inferred — the test fixes it
			Engine:   tpl.Engine{Type: tpl.EngineHelm},
			Inputs: []tpl.Input{
				{Name: "caller_name", Title: "Caller Name", Type: tpl.InputTypeString},
			},
		},
	}
}

func patchReq(name string, body any) *http.Request {
	data, _ := json.Marshal(body)
	r := httptest.NewRequest("PATCH", "/api/v1/templates/"+name, bytes.NewReader(data))
	r.SetPathValue("name", name)
	return r
}

func TestUpdateTemplateMetadata_ImportedEditable(t *testing.T) {
	client := fake.NewSimpleClientset()
	// Pre-store as a cluster (imported) template.
	if err := kube.SaveTemplate(context.Background(), client, metadataTestTemplate(), nil); err != nil {
		t.Fatalf("seed: %v", err)
	}
	th := &templateHandler{
		kubeClient: client,
		clusterLoader: func(ctx context.Context) ([]*tpl.Template, error) {
			return kube.LoadTemplates(ctx, client)
		},
	}

	passthrough := false
	rec := httptest.NewRecorder()
	th.handleUpdateTemplateMetadata(rec, patchReq("voiceai-livekit-agent", templateMetadataPatch{
		Category:              strptr("voiceai"),
		InjectCanonicalValues: &passthrough,
	}))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var dto TemplateDetailDTO
	_ = json.NewDecoder(rec.Body).Decode(&dto)
	if dto.Category != "voiceai" {
		t.Errorf("category = %q, want voiceai", dto.Category)
	}
	if dto.InjectCanonicalValues == nil || *dto.InjectCanonicalValues {
		t.Errorf("expected passthrough (injectCanonicalValues false), got %v", dto.InjectCanonicalValues)
	}
	if len(dto.Inputs) != 0 {
		t.Errorf("passthrough should clear inputs, got %d", len(dto.Inputs))
	}
	if !dto.Editable || dto.Source == nil || dto.Source.Origin != "imported" {
		t.Errorf("expected editable imported template, got editable=%v source=%+v", dto.Editable, dto.Source)
	}

	// Persisted: reload and confirm category stuck.
	got, err := kube.LoadTemplates(context.Background(), client)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Spec.Category != "voiceai" {
		t.Errorf("persisted category not updated: %+v", got)
	}
}

func TestUpdateTemplateMetadata_SyncedSavesOverride(t *testing.T) {
	client := fake.NewSimpleClientset()
	if err := kube.SaveTemplate(context.Background(), client, metadataTestTemplate(), nil); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Mark it synced from an external repo.
	regStore := tpl.NewRegistryStore(client)
	reg := &tpl.TemplateRegistry{}
	reg.UpsertSource(tpl.TemplateSource{
		Name: "voiceai-livekit-agent", Origin: "external",
		ExternalRepo: "https://github.com/example/templates.git",
	})
	if err := regStore.Save(context.Background(), reg); err != nil {
		t.Fatal(err)
	}
	th := &templateHandler{
		kubeClient:    client,
		registryStore: regStore,
		clusterLoader: func(ctx context.Context) ([]*tpl.Template, error) {
			return kube.LoadTemplates(ctx, client)
		},
	}

	rec := httptest.NewRecorder()
	th.handleUpdateTemplateMetadata(rec, patchReq("voiceai-livekit-agent", templateMetadataPatch{Category: strptr("worker")}))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (override saved) for synced template, got %d: %s", rec.Code, rec.Body.String())
	}
	var dto TemplateDetailDTO
	_ = json.NewDecoder(rec.Body).Decode(&dto)
	if dto.Category != "worker" {
		t.Errorf("DTO category = %q, want worker (override applied)", dto.Category)
	}

	// The override is persisted sync-safe; the template body is untouched.
	ov, err := kube.LoadTemplateOverride(context.Background(), client, "voiceai-livekit-agent")
	if err != nil || ov == nil || ov.Metadata == nil || ov.Metadata.Category != "worker" {
		t.Fatalf("expected metadata override category=worker, got %+v (err=%v)", ov, err)
	}
	got, _ := kube.LoadTemplates(context.Background(), client)
	if len(got) != 1 || got[0].Spec.Category != "web" {
		t.Errorf("synced template body must not change, got category %q", got[0].Spec.Category)
	}
}

func TestUpdateTemplateMetadata_SyncedRejectsValuesMode(t *testing.T) {
	client := fake.NewSimpleClientset()
	_ = kube.SaveTemplate(context.Background(), client, metadataTestTemplate(), nil)
	regStore := tpl.NewRegistryStore(client)
	reg := &tpl.TemplateRegistry{}
	reg.UpsertSource(tpl.TemplateSource{Name: "voiceai-livekit-agent", Origin: "external", ExternalRepo: "https://x/y.git"})
	_ = regStore.Save(context.Background(), reg)
	th := &templateHandler{
		kubeClient: client, registryStore: regStore,
		clusterLoader: func(ctx context.Context) ([]*tpl.Template, error) { return kube.LoadTemplates(ctx, client) },
	}
	passthrough := false
	rec := httptest.NewRecorder()
	th.handleUpdateTemplateMetadata(rec, patchReq("voiceai-livekit-agent", templateMetadataPatch{InjectCanonicalValues: &passthrough}))
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 rejecting values-mode override on synced template, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestUpdateTemplateMetadata_BuiltinSavesOverride(t *testing.T) {
	client := fake.NewSimpleClientset() // nothing stored → disk built-in
	th := &templateHandler{
		kubeClient: client,
		builtin:    []*tpl.Template{metadataTestTemplate()},
	}
	rec := httptest.NewRecorder()
	th.handleUpdateTemplateMetadata(rec, patchReq("voiceai-livekit-agent", templateMetadataPatch{Category: strptr("worker")}))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (override saved) for built-in template, got %d: %s", rec.Code, rec.Body.String())
	}
	ov, err := kube.LoadTemplateOverride(context.Background(), client, "voiceai-livekit-agent")
	if err != nil || ov == nil || ov.Metadata == nil || ov.Metadata.Category != "worker" {
		t.Fatalf("expected built-in metadata override category=worker, got %+v (err=%v)", ov, err)
	}
}

func strptr(s string) *string { return &s }

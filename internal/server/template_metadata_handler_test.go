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

func TestUpdateTemplateMetadata_SyncedReturns409(t *testing.T) {
	client := fake.NewSimpleClientset()
	_ = kube.SaveTemplate(context.Background(), client, metadataTestTemplate(), nil)
	// Mark it synced from an external repo.
	regStore := tpl.NewRegistryStore(client)
	reg := &tpl.TemplateRegistry{}
	reg.UpsertSource(tpl.TemplateSource{
		Name: "voiceai-livekit-agent", Origin: "external",
		ExternalRepo: "https://github.com/biglysales/templates.git",
	})
	if err := regStore.Save(context.Background(), reg); err != nil {
		t.Fatal(err)
	}
	th := &templateHandler{kubeClient: client, registryStore: regStore}

	rec := httptest.NewRecorder()
	th.handleUpdateTemplateMetadata(rec, patchReq("voiceai-livekit-agent", templateMetadataPatch{Category: strptr("voiceai")}))
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 for synced template, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestUpdateTemplateMetadata_BuiltinReturns404(t *testing.T) {
	client := fake.NewSimpleClientset() // nothing stored → disk built-in
	th := &templateHandler{
		kubeClient: client,
		builtin:    []*tpl.Template{metadataTestTemplate()},
	}
	rec := httptest.NewRecorder()
	th.handleUpdateTemplateMetadata(rec, patchReq("voiceai-livekit-agent", templateMetadataPatch{Category: strptr("voiceai")}))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for built-in template, got %d: %s", rec.Code, rec.Body.String())
	}
}

func strptr(s string) *string { return &s }

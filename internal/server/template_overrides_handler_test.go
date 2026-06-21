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

// TestPutTemplateOverride_PreservesImageMapping is a regression test for the
// clobber bug where saving platform value overrides wiped a co-stored image
// mapping: both live in the same override ConfigMap, and the PUT handler used to
// blindly replace it. An image mapping wired for CD must survive a later
// platform-overrides save.
func TestPutTemplateOverride_PreservesImageMapping(t *testing.T) {
	client := fake.NewSimpleClientset()
	if err := kube.SaveTemplate(context.Background(), client, metadataTestTemplate(), nil); err != nil {
		t.Fatalf("seed: %v", err)
	}
	regStore := tpl.NewRegistryStore(client)
	reg := &tpl.TemplateRegistry{}
	reg.UpsertSource(tpl.TemplateSource{Name: "voiceai-livekit-agent", Origin: "external", ExternalRepo: "https://x/y.git"})
	if err := regStore.Save(context.Background(), reg); err != nil {
		t.Fatal(err)
	}
	th := &templateHandler{
		kubeClient: client, registryStore: regStore,
		clusterLoader: func(ctx context.Context) ([]*tpl.Template, error) { return kube.LoadTemplates(ctx, client) },
	}

	// 1. Wire an image mapping via the metadata editor (sync-safe override).
	images := []TemplateImageDTO{{
		Name: "agent", Repository: "acr.io/org/livekit", TagKey: "image.tag", SelectionStrategy: "SemVer",
	}}
	rec := httptest.NewRecorder()
	th.handleUpdateTemplateMetadata(rec, patchReq("voiceai-livekit-agent", templateMetadataPatch{Images: &images}))
	if rec.Code != http.StatusOK {
		t.Fatalf("seed image override: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// 2. Save platform value overrides through the PUT endpoint.
	putBody, _ := json.Marshal(TemplateOverrideDTO{
		DefaultValues: map[string]any{"resources": map[string]any{"limits": map[string]any{"cpu": "500m"}}},
	})
	putReq := httptest.NewRequest("PUT", "/api/v1/templates/voiceai-livekit-agent/overrides", bytes.NewReader(putBody))
	putReq.SetPathValue("name", "voiceai-livekit-agent")
	putRec := httptest.NewRecorder()
	th.handlePutTemplateOverride(putRec, putReq)
	if putRec.Code != http.StatusOK {
		t.Fatalf("put overrides: expected 200, got %d: %s", putRec.Code, putRec.Body.String())
	}

	// 3. The image mapping must survive, alongside the new values override.
	ov, err := kube.LoadTemplateOverride(context.Background(), client, "voiceai-livekit-agent")
	if err != nil || ov == nil {
		t.Fatalf("load override: %+v (err=%v)", ov, err)
	}
	if len(ov.Images) != 1 || ov.Images[0].Repository != "acr.io/org/livekit" {
		t.Errorf("image mapping clobbered by platform-overrides save: %+v", ov.Images)
	}
	if ov.DefaultValues == nil {
		t.Errorf("platform value override not persisted: %+v", ov)
	}

	// 4. A detail GET still reflects the image mapping.
	getReq := httptest.NewRequest("GET", "/api/v1/templates/voiceai-livekit-agent", nil)
	getReq.SetPathValue("name", "voiceai-livekit-agent")
	getRec := httptest.NewRecorder()
	th.handleDetail(getRec, getReq)
	var detail TemplateDetailDTO
	_ = json.NewDecoder(getRec.Body).Decode(&detail)
	if len(detail.Images) != 1 || detail.Images[0].TagKey != "image.tag" {
		t.Errorf("detail GET images = %+v, want mapping preserved after overrides save", detail.Images)
	}
}

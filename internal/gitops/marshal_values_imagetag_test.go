package gitops

import (
	"bytes"
	"testing"

	"github.com/suparcloud/suparship/internal/helmvalues"
)

// A stable deploy whose image tag is owned by external CD (no resolved tag yet)
// must NOT commit a literal "[[platform.imageTag]]" into values: charts stamp it
// into metadata.labels (app.kubernetes.io/version) + image refs, and "{...}" is
// an invalid label value that fails the whole apply. It must resolve to empty so
// the chart can default it to .Chart.AppVersion.
func TestMarshalPassthroughValues_NoLiteralImageTag(t *testing.T) {
	pv := helmvalues.PlatformValues{App: "lk-sh"} // ImageTag empty
	overlay := map[string]any{
		"image": map[string]any{"tag": "[[platform.imageTag]]"},
	}
	out, err := marshalPassthroughValues(pv, overlay, nil)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if bytes.Contains(out, []byte("[[platform.imageTag]]")) {
		t.Fatalf("committed values must not contain the literal token:\n%s", out)
	}
}

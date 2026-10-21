package server

import (
	"encoding/json"
	"testing"
)

func TestPreviewValuesOnly(t *testing.T) {
	for _, tc := range []struct {
		body string
		want bool
	}{
		{`{"envComponentValues":{"preview":{"web":{"replicas":2}}}}`, true},
		{`{"envRawValues":{"preview":{}}}`, true},
		{`{"envComponentEnvVars":{"preview":{"web":{}}}}`, true},
		{`{"envComponentValues":{"preview":{"web":{}},"staging":{"web":{}}}}`, false},
		{`{"envRawValues":{"preview":{}},"cd":{"managed":true}}`, false},
		{`{"envRawValues":{"release-validation":{}}}`, false},
		{`{}`, false},
	} {
		t.Run(tc.body, func(t *testing.T) {
			var req updateAppRequest
			if err := json.Unmarshal([]byte(tc.body), &req); err != nil {
				t.Fatal(err)
			}
			if got := previewValuesOnly(req); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

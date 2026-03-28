package runtime

import "testing"

func TestValidateLogsRequest_OK(t *testing.T) {
	tl := int64(100)
	req := &LogsRequest{
		Namespace: "api-dev",
		Pod:       "backend-abc",
		TailLines: &tl,
	}
	if err := ValidateLogsRequest(req); err != nil {
		t.Fatalf("expected valid, got: %v", err)
	}
}

func TestValidateLogsRequest_MissingNamespace(t *testing.T) {
	req := &LogsRequest{Pod: "backend-abc"}
	if err := ValidateLogsRequest(req); err == nil {
		t.Fatal("expected error for missing namespace")
	}
}

func TestValidateLogsRequest_NegativeTailLines(t *testing.T) {
	tl := int64(-1)
	req := &LogsRequest{
		Namespace: "api-dev",
		TailLines: &tl,
	}
	if err := ValidateLogsRequest(req); err == nil {
		t.Fatal("expected error for negative tailLines")
	}
}

func TestValidateLogsRequest_NilTailLines(t *testing.T) {
	req := &LogsRequest{Namespace: "api-dev"}
	if err := ValidateLogsRequest(req); err != nil {
		t.Fatalf("nil tailLines should be valid, got: %v", err)
	}
}

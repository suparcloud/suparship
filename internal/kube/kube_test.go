package kube_test

import (
	"testing"

	k8sfake "k8s.io/client-go/kubernetes/fake"

	"github.com/suparcloud/suparship/internal/kube"
	"github.com/suparcloud/suparship/internal/preview"
	"github.com/suparcloud/suparship/internal/project"
	"github.com/suparcloud/suparship/internal/runtime"
)

func TestNewServerDeps_FieldsAreNotNil(t *testing.T) {
	client := k8sfake.NewClientset()
	deps := kube.NewServerDeps(client, nil)

	if deps.ProjectStore == nil {
		t.Error("ProjectStore is nil")
	}
	if deps.PreviewStore == nil {
		t.Error("PreviewStore is nil")
	}
	if deps.RuntimeProvider == nil {
		t.Error("RuntimeProvider is nil")
	}
	if deps.LogsProvider == nil {
		t.Error("LogsProvider is nil")
	}
}

// TestNewServerDeps_IndependentInstances verifies that two successive calls
// return distinct objects (no shared state via package-level variables).
func TestNewServerDeps_IndependentInstances(t *testing.T) {
	client := k8sfake.NewClientset()
	a := kube.NewServerDeps(client, nil)
	b := kube.NewServerDeps(client, nil)

	if a == b {
		t.Error("NewServerDeps returned the same pointer on successive calls")
	}
	if a.ProjectStore == b.ProjectStore {
		t.Error("ProjectStore pointers are identical across instances")
	}
}

// TestInterfaceCompliance verifies that the concrete K8s types satisfy the
// server interface contracts expected by server.Config.
// These assertions are intentionally redundant with the compile-time var
// checks in kube.go to keep the intent explicit in the test suite.
func TestInterfaceCompliance(t *testing.T) {
	client := k8sfake.NewClientset()
	deps := kube.NewServerDeps(client, nil)

	// Assign to interface variables — compile error if contract is broken.
	var _ project.Store = deps.ProjectStore
	var _ preview.Store = deps.PreviewStore
	var _ runtime.Provider = deps.RuntimeProvider
	var _ runtime.LogsProvider = deps.LogsProvider
}

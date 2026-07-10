package server

import (
	"context"
	"net/http"
)

// StuckApp is one ArgoCD Application wedged in Terminating, surfaced so an
// operator can unstick it from suparship instead of hand-patching finalizers.
type StuckApp struct {
	Name              string   `json:"name"`
	Project           string   `json:"project,omitempty"`
	DeletionTimestamp string   `json:"deletionTimestamp,omitempty"`
	Finalizers        []string `json:"finalizers,omitempty"`
	Reason            string   `json:"reason"`
}

// StuckAppManager detects ArgoCD Applications stuck in Terminating and can
// unstick one by removing ArgoCD's cascade-delete finalizer. Implemented by an
// adapter over the ArgoCD reader; nil disables the endpoints.
type StuckAppManager interface {
	ListStuckApplications(ctx context.Context) ([]StuckApp, error)
	UnstickApplication(ctx context.Context, name string) error
}

// StuckAppsResponse is the JSON body for GET .../platform/stuck-apps.
type StuckAppsResponse struct {
	StuckApps []StuckApp `json:"stuckApps"`
}

// handleListStuckApps lists Applications wedged in Terminating. org_admin only.
func (rh *rbacHandler) handleListStuckApps(w http.ResponseWriter, r *http.Request) {
	apps, err := rh.stuckApps.ListStuckApplications(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to list stuck applications: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, StuckAppsResponse{StuckApps: apps})
}

// handleUnstickApp removes ArgoCD's cascade-delete finalizer from a stuck
// Application so its deletion completes. org_admin only. The underlying manager
// refuses to act on an app that isn't actually Terminating.
func (rh *rbacHandler) handleUnstickApp(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "application name is required"})
		return
	}
	if err := rh.stuckApps.UnstickApplication(r.Context(), name); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to unstick application: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"application": name, "status": "unstuck"})
}

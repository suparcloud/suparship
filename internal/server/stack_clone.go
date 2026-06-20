package server

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/suparcloud/suparship/internal/domain"
)

// Stack clone (Phase 4).
//
// Clone duplicates a whole collection with a few config diffs — the canonical
// case being voiceai → livekit-cloud vs voiceai → self-hosted. It creates a new
// stack record (the source spec ⊕ the request's overrides), then recreates each
// member app under the new stack via the same recreate-under-new-name path as
// rename — but as a COPY: the source stack and its apps stay intact. App names
// stay project-unique, so each member gets a derived name (default
// {newStack}-{oldApp}, overridable). App-tier secret VALUES cannot be migrated
// (write-only vault) and must be re-entered under the new app names.

// cloneStackRequest is the body for POST .../stacks/{stack}/clone. Override
// fields, when set, REPLACE the corresponding value copied from the source
// stack; when omitted the source value is carried over.
type cloneStackRequest struct {
	NewName          string                    `json:"newName"`
	AppNames         map[string]string         `json:"appNames,omitempty"` // source app name -> new app name
	DisplayName      *string                   `json:"displayName,omitempty"`
	Description      *string                   `json:"description,omitempty"`
	SharedNamespace  *bool                     `json:"sharedNamespace,omitempty"`
	NamespacePattern *string                   `json:"namespacePattern,omitempty"`
	RawValues        map[string]any            `json:"rawValues,omitempty"`
	EnvRawValues     map[string]map[string]any `json:"envRawValues,omitempty"`
	EnvConfig        *EnvConfigDTO             `json:"envConfig,omitempty"`
}

// cloneStackResponse returns the new stack plus the per-app copy result summary.
type cloneStackResponse struct {
	Stack   StackDTO        `json:"stack"`
	Results []stackOpResult `json:"results"`
}

func (rh *rbacHandler) handleCloneStack(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	src := r.PathValue("stack")
	if rh.appHandler == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "app store not configured"})
		return
	}
	var req cloneStackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}
	if err := domain.ValidateAppName(req.NewName); err != nil { // same DNS-label rule as create
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: "invalid stack name: " + err.Error()})
		return
	}
	if req.NewName == src {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "new stack name is the same as the source"})
		return
	}
	source, err := rh.stackStore.GetStack(r.Context(), project, src)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "stack not found: " + src})
		return
	}
	if _, err := rh.stackStore.GetStack(r.Context(), project, req.NewName); err == nil {
		writeJSON(w, http.StatusConflict, errorResponse{Error: "stack \"" + req.NewName + "\" already exists in project \"" + project + "\""})
		return
	}

	// Build the new spec: copy the source ⊕ the request's overrides. DisplayName
	// defaults to empty (UI falls back to the new name) unless explicitly given,
	// since a clone is a distinct stack.
	spec := source.Spec
	spec.DisplayName = ""
	if req.DisplayName != nil {
		spec.DisplayName = *req.DisplayName
	}
	if req.Description != nil {
		spec.Description = *req.Description
	}
	if req.SharedNamespace != nil {
		spec.SharedNamespace = *req.SharedNamespace
	}
	if req.NamespacePattern != nil {
		spec.NamespacePattern = *req.NamespacePattern
	}
	if req.RawValues != nil {
		spec.RawValues = req.RawValues
	}
	if req.EnvRawValues != nil {
		spec.EnvRawValues = req.EnvRawValues
	}
	if req.EnvConfig != nil {
		spec.EnvConfig = fromEnvConfigDTO(*req.EnvConfig)
	}

	newStack := &domain.Stack{Name: req.NewName, ProjectName: project, Spec: spec}
	// Save the new stack BEFORE copying apps: resolveEnvNamespaces reads the
	// stack's SharedNamespace to co-locate members, so it must exist first.
	if err := rh.stackStore.SaveStack(r.Context(), newStack); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to save cloned stack"})
		return
	}

	members := rh.stackMemberApps(r.Context(), project, src)
	results := make([]stackOpResult, 0, len(members))
	for _, a := range members {
		newAppName := req.AppNames[a.Name]
		if newAppName == "" {
			newAppName = req.NewName + "-" + a.Name
		}
		if err := domain.ValidateAppName(newAppName); err != nil {
			results = append(results, errResult(a.Name, fmt.Errorf("derived name %q invalid: %w", newAppName, err)))
			continue
		}
		if err := rh.appHandler.copyAppAs(r.Context(), a, newAppName, req.NewName); err != nil {
			results = append(results, errResult(a.Name, err))
			continue
		}
		results = append(results, okResult(a.Name, "cloned to "+newAppName))
	}

	writeJSON(w, http.StatusCreated, cloneStackResponse{
		Stack:   stackToDTO(newStack, rh.stackMemberNames(r.Context(), project, req.NewName)),
		Results: results,
	})
}

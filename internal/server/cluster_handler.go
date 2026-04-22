package server

import (
	"encoding/base64"
	"encoding/json"
	"net/http"

	"github.com/suparcloud/suparship/internal/domain"
)

// clusterHandler serves /api/v1/clusters endpoints.
//
// All write operations (POST, DELETE) require org_admin role (validated by
// the session middleware). Read operations (GET) require any authenticated user.
type clusterHandler struct {
	store domain.ClusterStore
	auth  *authHandler
}

// registerRoutes wires cluster endpoints into mux.
func (ch *clusterHandler) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/clusters", ch.requireAuth(ch.handleList))
	mux.HandleFunc("GET /api/v1/clusters/{name}", ch.requireAuth(ch.handleGet))
	mux.HandleFunc("POST /api/v1/clusters", ch.requireAuth(ch.handleCreate))
	mux.HandleFunc("DELETE /api/v1/clusters/{name}", ch.requireAuth(ch.handleDelete))
}

// requireAuth wraps a handler with session authentication.
func (ch *clusterHandler) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return ch.auth.requireAuth(next)
}

// ── GET /api/v1/clusters ──────────────────────────────────────────────────────

func (ch *clusterHandler) handleList(w http.ResponseWriter, r *http.Request) {
	clusters, err := ch.store.ListClusters(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to list clusters"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"clusters": clusters})
}

// ── GET /api/v1/clusters/{name} ───────────────────────────────────────────────

func (ch *clusterHandler) handleGet(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	cluster, err := ch.store.GetCluster(r.Context(), name)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "cluster not found"})
		return
	}
	writeJSON(w, http.StatusOK, cluster)
}

// ── POST /api/v1/clusters ─────────────────────────────────────────────────────

// createClusterRequest is the JSON body for POST /api/v1/clusters.
type createClusterRequest struct {
	// Name is the unique cluster identifier (DNS label).
	Name string `json:"name"`
	// DisplayName is a human-readable label shown in the UI.
	DisplayName string `json:"displayName,omitempty"`
	// APIServer is the Kubernetes API server URL (e.g. "https://10.0.0.1:6443").
	APIServer string `json:"apiServer"`
	// ESONamespace is the namespace where External Secrets Operator is installed
	// on this cluster. Defaults to "external-secrets" when empty.
	ESONamespace string `json:"esoNamespace,omitempty"`
	// Kubeconfig is the base64-encoded raw kubeconfig for this cluster.
	// It is stored encrypted in Kubernetes Secrets and never written to Git.
	Kubeconfig string `json:"kubeconfig"`
}

func (ch *clusterHandler) handleCreate(w http.ResponseWriter, r *http.Request) {
	var req createClusterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}

	if err := domain.ValidateClusterName(req.Name); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	if req.APIServer == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "apiServer is required"})
		return
	}
	if req.Kubeconfig == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "kubeconfig is required (base64-encoded)"})
		return
	}

	kubeconfigBytes, err := base64.StdEncoding.DecodeString(req.Kubeconfig)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "kubeconfig must be base64-encoded"})
		return
	}

	displayName := req.DisplayName
	if displayName == "" {
		displayName = req.Name
	}

	cluster := domain.Cluster{
		Name:         req.Name,
		DisplayName:  displayName,
		APIServer:    req.APIServer,
		ESONamespace: req.ESONamespace,
		Status:       "ready",
	}

	if err := ch.store.CreateCluster(r.Context(), cluster, kubeconfigBytes); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to register cluster: " + err.Error()})
		return
	}

	writeJSON(w, http.StatusCreated, cluster)
}

// ── DELETE /api/v1/clusters/{name} ───────────────────────────────────────────

func (ch *clusterHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := ch.store.DeleteCluster(r.Context(), name); err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "cluster not found or could not be removed"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

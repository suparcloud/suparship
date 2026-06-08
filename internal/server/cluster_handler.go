package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"k8s.io/client-go/kubernetes"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/rbac"
	"github.com/suparcloud/suparship/internal/seal"
	"github.com/suparcloud/suparship/internal/secrets"
)

// clusterHandler serves /api/v1/clusters endpoints.
//
// All write operations (POST, DELETE) require org_admin role (validated by
// the session middleware). Read operations (GET) require any authenticated user.
type clusterHandler struct {
	store     domain.ClusterStore
	auth      *authHandler
	certCache seal.CertCache
	pool      sealClientPool
	logger    *slog.Logger
	// storeReconciler republishes ESO ClusterSecretStores when a cluster is
	// created. Optional; nil disables the hook.
	storeReconciler SecretStoreReconciler
	// sealPublisher prunes the removed cluster's _secret-stores/{cluster}/ dir
	// + ArgoCD app on delete. Optional; nil disables the cleanup.
	sealPublisher SealedTokenPublisher
	// orgStore drops the removed cluster's Connect-token state on delete.
	// Optional; nil disables the cleanup.
	orgStore rbac.OrgStore
	// kubeClient removes the removed cluster's Connect-token stash on delete.
	// Optional; nil disables the cleanup.
	kubeClient kubernetes.Interface
}

// registerRoutes wires cluster endpoints into mux.
func (ch *clusterHandler) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/clusters", ch.requireAuth(ch.handleList))
	mux.HandleFunc("GET /api/v1/clusters/{name}", ch.requireAuth(ch.handleGet))
	mux.HandleFunc("POST /api/v1/clusters", ch.requireAuth(ch.handleCreate))
	mux.HandleFunc("DELETE /api/v1/clusters/{name}", ch.requireAuth(ch.handleDelete))
	mux.HandleFunc("POST /api/v1/clusters/{name}/sealing-cert/refresh", ch.requireAuth(ch.handleRefreshSealingCert))
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
	req.APIServer = strings.TrimSpace(req.APIServer)
	if err := domain.ValidateAPIServerURL(req.APIServer); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
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
	// Trim stray whitespace — a leading space in APIServer ends up in ArgoCD
	// destination.server and breaks cluster lookup.
	cluster.Normalize()

	if err := ch.store.CreateCluster(r.Context(), cluster, kubeconfigBytes); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to register cluster: " + err.Error()})
		return
	}

	// Best-effort: fetch and cache the sealed-secrets certificate from the
	// newly registered cluster so that token sealing works immediately without
	// a separate admin step. Failures are logged but do not fail the registration.
	go ch.tryFetchSealingCert(r.Context(), req.Name)

	// Best-effort: (re)publish ESO ClusterSecretStores so the new cluster's
	// store exists in gitops. Runs in the background to not delay the response.
	if ch.storeReconciler != nil {
		go func() {
			if err := ch.storeReconciler.ReconcileSecretStores(context.Background()); err != nil {
				ch.logger.Warn("cluster create: reconcile secret stores failed", "cluster", req.Name, "error", err)
			}
		}()
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

	// Best-effort: prune the removed cluster's ESO ClusterSecretStore.
	if ch.storeReconciler != nil {
		go func() {
			if err := ch.storeReconciler.ReconcileSecretStores(context.Background()); err != nil {
				ch.logger.Warn("cluster delete: reconcile secret stores failed", "cluster", name, "error", err)
			}
		}()
	}

	// Best-effort: drop the removed cluster's sealed token + unified store from
	// gitops, its Connect-token state from org config, and its token stash.
	go ch.cleanupClusterSecrets(context.Background(), name)

	w.WriteHeader(http.StatusNoContent)
}

// cleanupClusterSecrets removes everything the secrets subsystem holds for a
// deleted cluster: the _secret-stores/{cluster}/ gitops dir + ArgoCD app, the
// ClusterTokenRef in org config, and the Connect-token stash Secret. All
// best-effort — failures are logged only.
func (ch *clusterHandler) cleanupClusterSecrets(ctx context.Context, name string) {
	if ch.sealPublisher != nil {
		if err := ch.sealPublisher.DeleteClusterSecretStores(ctx, name); err != nil {
			ch.logger.Warn("cluster delete: gitops secret-store cleanup failed", "cluster", name, "error", err)
		}
	}
	if ch.orgStore != nil {
		if org, err := ch.orgStore.GetOrg(ctx); err == nil && org != nil && org.SecretBackend.FindClusterToken(name) != nil {
			org.SecretBackend.RemoveClusterToken(name)
			if err := ch.orgStore.SaveOrg(ctx, org); err != nil {
				ch.logger.Warn("cluster delete: token state cleanup failed", "cluster", name, "error", err)
			}
		}
	}
	if ch.kubeClient != nil {
		if err := secrets.DeleteConnectToken(ctx, ch.kubeClient, secrets.ClusterStashKey(name)); err != nil {
			ch.logger.Warn("cluster delete: token stash cleanup failed", "cluster", name, "error", err)
		}
	}
}

// ── POST /api/v1/clusters/{name}/sealing-cert/refresh ────────────────────────

// SealingCertRefreshResponse is the JSON body returned after a cert refresh.
type SealingCertRefreshResponse struct {
	Cluster string `json:"cluster"`
	Cached  bool   `json:"cached"`
	Message string `json:"message,omitempty"`
}

// handleRefreshSealingCert fetches the sealed-secrets controller public cert
// from the target cluster and stores it in the cert cache, replacing any
// previously cached (possibly wrong) cert. Use this to fix "could not decrypt"
// errors caused by a stale or incorrect cached certificate.
func (ch *clusterHandler) handleRefreshSealingCert(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "cluster name is required"})
		return
	}

	// Ensure cluster exists.
	if _, err := ch.store.GetCluster(r.Context(), name); err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "cluster not found"})
		return
	}

	if ch.certCache == nil || ch.pool == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{
			Error: "cert cache or cluster pool not configured on this server",
		})
		return
	}

	kubeClient, err := ch.pool.GetKubeClient(r.Context(), name)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{
			Error: "failed to build kube client for cluster: " + err.Error(),
		})
		return
	}

	if _, err := seal.FetchAndCache(r.Context(), ch.certCache, kubeClient, name, seal.FetchOptions{}); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{
			Error: "failed to fetch sealing cert from cluster: " + err.Error(),
		})
		return
	}

	if ch.logger != nil {
		ch.logger.Info("sealing cert refreshed", "cluster", name)
	}
	writeJSON(w, http.StatusOK, SealingCertRefreshResponse{
		Cluster: name,
		Cached:  true,
		Message: "sealing certificate fetched from cluster and cached successfully",
	})
}

// tryFetchSealingCert is a best-effort background fetch of the sealed-secrets
// controller cert for a newly registered cluster. Errors are only logged.
func (ch *clusterHandler) tryFetchSealingCert(ctx context.Context, clusterName string) {
	if ch.certCache == nil || ch.pool == nil {
		return
	}
	kubeClient, err := ch.pool.GetKubeClient(ctx, clusterName)
	if err != nil {
		if ch.logger != nil {
			ch.logger.Warn("could not build kube client for cert fetch", "cluster", clusterName, "err", err)
		}
		return
	}
	if _, err := seal.FetchAndCache(ctx, ch.certCache, kubeClient, clusterName, seal.FetchOptions{}); err != nil {
		if ch.logger != nil {
			ch.logger.Warn("sealing cert auto-fetch failed (non-fatal); use refresh endpoint to retry",
				"cluster", clusterName, "err", err)
		}
		return
	}
	if ch.logger != nil {
		ch.logger.Info("sealing cert auto-fetched after cluster registration", "cluster", clusterName)
	}
}

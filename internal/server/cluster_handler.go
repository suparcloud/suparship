package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

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

// registerRoutes wires cluster endpoints into mux. Clusters are infra/admin
// topology (API server URLs, kubeconfig-backed registration), so both reads and
// writes require org_admin — a developer/viewer has no business listing or
// mutating the workload-cluster registry.
func (ch *clusterHandler) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/clusters", ch.requireOrgAdmin(ch.handleList))
	mux.HandleFunc("GET /api/v1/clusters/argocd", ch.requireOrgAdmin(ch.handleListArgoCD))
	mux.HandleFunc("POST /api/v1/clusters/import", ch.requireOrgAdmin(ch.handleImport))
	mux.HandleFunc("GET /api/v1/clusters/{name}", ch.requireOrgAdmin(ch.handleGet))
	mux.HandleFunc("POST /api/v1/clusters", ch.requireOrgAdmin(ch.handleCreate))
	mux.HandleFunc("PUT /api/v1/clusters/{name}", ch.requireOrgAdmin(ch.handleUpdateMetadata))
	mux.HandleFunc("DELETE /api/v1/clusters/{name}", ch.requireOrgAdmin(ch.handleDelete))
	mux.HandleFunc("POST /api/v1/clusters/{name}/sealing-cert/refresh", ch.requireOrgAdmin(ch.handleRefreshSealingCert))
}

// requireOrgAdmin enforces session auth + the org_admin role. When no org store
// is wired (e.g. minimal test setups) it degrades to auth-only.
func (ch *clusterHandler) requireOrgAdmin(next http.HandlerFunc) http.HandlerFunc {
	return ch.auth.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		if ch.orgStore == nil {
			next(w, r)
			return
		}
		sess := sessionFromContext(r.Context())
		org, err := ch.orgStore.GetOrg(r.Context())
		if err != nil || org == nil {
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to load org config"})
			return
		}
		if !org.HasPermissionForIdentity(sess.Username, sess.Groups, "*", rbac.RoleOrgAdmin) {
			writeJSON(w, http.StatusForbidden, errorResponse{Error: "org_admin role required"})
			return
		}
		next(w, r)
	})
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
	// BaseDomain is the cluster's default ingress DNS zone (multi-cloud).
	BaseDomain string `json:"baseDomain,omitempty"`
	// RoutingProfiles overrides env/org routing for apps on this cluster, keyed
	// by ExposeMode (internal/external).
	RoutingProfiles domain.RoutingProfiles `json:"routingProfiles,omitempty"`
	// Kubeconfig is the base64-encoded raw kubeconfig for this cluster.
	// It is stored encrypted in Kubernetes Secrets and never written to Git.
	Kubeconfig string `json:"kubeconfig"`
}

// validateClusterRoutingProfiles checks each present profile resolves (valid
// mode name + non-empty ingressClassName), reusing domain.ResolveRoutingProfile.
func validateClusterRoutingProfiles(profiles domain.RoutingProfiles) error {
	for mode := range profiles {
		if _, err := domain.ResolveRoutingProfile(nil, nil, profiles, domain.ExposeMode(mode)); err != nil {
			return err
		}
	}
	return nil
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

	if err := validateClusterRoutingProfiles(req.RoutingProfiles); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: err.Error()})
		return
	}

	cluster := domain.Cluster{
		Name:            req.Name,
		DisplayName:     displayName,
		APIServer:       req.APIServer,
		ESONamespace:    req.ESONamespace,
		BaseDomain:      strings.TrimSpace(req.BaseDomain),
		RoutingProfiles: req.RoutingProfiles,
		Status:          "ready",
	}
	// Trim stray whitespace — a leading space in APIServer ends up in ArgoCD
	// destination.server and breaks cluster lookup.
	cluster.Normalize()

	if err := ch.store.CreateCluster(r.Context(), cluster, kubeconfigBytes); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to register cluster: " + err.Error()})
		return
	}

	ch.provisionClusterConnection(req.Name)

	writeJSON(w, http.StatusCreated, cluster)
}

// provisionClusterConnection runs the post-registration hooks that turn a stored
// cluster into a working secret-delivery target: fetch + cache the sealed-secrets
// cert from the cluster, and (re)publish the ESO ClusterSecretStores to gitops.
// Both run in the background (detached context) and are best-effort — failures
// are logged, not surfaced. Shared by register (handleCreate) and import
// (handleImport) so an imported cluster is wired identically to a registered one.
func (ch *clusterHandler) provisionClusterConnection(name string) {
	// r.Context() is cancelled when the handler returns, so use a detached
	// context with a timeout for the cert fetch.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		ch.tryFetchSealingCert(ctx, name)
	}()

	if ch.storeReconciler != nil {
		go func() {
			if err := ch.storeReconciler.ReconcileSecretStores(context.Background()); err != nil && ch.logger != nil {
				ch.logger.Warn("provision cluster connection: reconcile secret stores failed", "cluster", name, "error", err)
			}
		}()
	}
}

// ── GET /api/v1/clusters/argocd — list importable ArgoCD clusters ─────────────

// argoCDClusterLister is implemented by the real store; fake/dev stores that
// don't talk to ArgoCD won't satisfy it (import is a no-op in those modes).
type argoCDClusterLister interface {
	ListArgoCDClusters(ctx context.Context) ([]domain.ArgoCDClusterCandidate, error)
}

// argoCDClusterImporter reconstructs a kubeconfig from an ArgoCD cluster
// registration and stores it as a suparship cluster.
type argoCDClusterImporter interface {
	ImportArgoCDCluster(ctx context.Context, name string) (*domain.Cluster, error)
}

func (ch *clusterHandler) handleListArgoCD(w http.ResponseWriter, r *http.Request) {
	lister, ok := ch.store.(argoCDClusterLister)
	if !ok {
		writeJSON(w, http.StatusNotImplemented, errorResponse{Error: "importing clusters from ArgoCD is not supported in this mode"})
		return
	}
	candidates, err := lister.ListArgoCDClusters(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to list ArgoCD clusters: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"clusters": candidates})
}

// ── POST /api/v1/clusters/import — import clusters from ArgoCD ─────────────────

type importClustersRequest struct {
	// Names are the ArgoCD cluster names (data.name) to import.
	Names []string `json:"names"`
}

type importSkip struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

type importClustersResponse struct {
	Imported []domain.Cluster `json:"imported"`
	Skipped  []importSkip     `json:"skipped"`
}

// handleImport imports one or more clusters from ArgoCD. For each name it
// reconstructs a kubeconfig from the ArgoCD cluster registration, stores the
// cluster (linking — not re-creating — the ArgoCD Secret), and runs the same
// provisioning hooks as a fresh registration so the cluster can actually deliver
// secrets (sealing cert + ESO ClusterSecretStore). Clusters using exec/cloud-IAM
// auth, already registered, or otherwise un-reconstructable are skipped with a
// reason rather than failing the whole request.
func (ch *clusterHandler) handleImport(w http.ResponseWriter, r *http.Request) {
	importer, ok := ch.store.(argoCDClusterImporter)
	if !ok {
		writeJSON(w, http.StatusNotImplemented, errorResponse{Error: "importing clusters from ArgoCD is not supported in this mode"})
		return
	}

	var req importClustersRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}
	if len(req.Names) == 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "names is required"})
		return
	}

	resp := importClustersResponse{Imported: []domain.Cluster{}, Skipped: []importSkip{}}
	for _, name := range req.Names {
		cluster, err := importer.ImportArgoCDCluster(r.Context(), name)
		if err != nil {
			resp.Skipped = append(resp.Skipped, importSkip{Name: name, Reason: err.Error()})
			continue
		}
		ch.provisionClusterConnection(cluster.Name)
		resp.Imported = append(resp.Imported, *cluster)
	}

	writeJSON(w, http.StatusOK, resp)
}

// ── PUT /api/v1/clusters/{name} — edit metadata (routing) ─────────────────────

type updateClusterRequest struct {
	DisplayName     string                 `json:"displayName,omitempty"`
	BaseDomain      string                 `json:"baseDomain,omitempty"`
	ESONamespace    string                 `json:"esoNamespace,omitempty"`
	RoutingProfiles domain.RoutingProfiles `json:"routingProfiles,omitempty"`
}

// clusterMetadataUpdater is implemented by the real store; the dev/fake store
// doesn't support metadata edits (cluster routing isn't exercised in fake mode).
type clusterMetadataUpdater interface {
	UpdateClusterMetadata(ctx context.Context, name, displayName, baseDomain, esoNamespace string, routing domain.RoutingProfiles) (*domain.Cluster, error)
}

func (ch *clusterHandler) handleUpdateMetadata(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var req updateClusterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}
	if err := validateClusterRoutingProfiles(req.RoutingProfiles); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: err.Error()})
		return
	}
	updater, ok := ch.store.(clusterMetadataUpdater)
	if !ok {
		writeJSON(w, http.StatusNotImplemented, errorResponse{Error: "cluster metadata updates are not supported in this mode"})
		return
	}
	cluster, err := updater.UpdateClusterMetadata(r.Context(), name, req.DisplayName, strings.TrimSpace(req.BaseDomain), req.ESONamespace, req.RoutingProfiles)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to update cluster: " + err.Error()})
		return
	}
	// Routing changes affect published values for apps on this cluster; nudge a
	// secret-store reconcile (best-effort) so any dependent infra refreshes.
	if ch.storeReconciler != nil {
		go func() {
			if err := ch.storeReconciler.ReconcileSecretStores(context.Background()); err != nil {
				ch.logger.Warn("cluster update: reconcile secret stores failed", "cluster", name, "error", err)
			}
		}()
	}
	writeJSON(w, http.StatusOK, cluster)
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
		// Read-modify-write with conflict retry so a concurrent org write
		// (e.g. a reseal) can't resurrect the deleted cluster's token ref.
		err := rbac.MutateOrg(ctx, ch.orgStore, func(org *rbac.Org) error {
			org.SecretBackend.RemoveClusterToken(name)
			return nil
		})
		if err != nil {
			ch.logger.Warn("cluster delete: token state cleanup failed", "cluster", name, "error", err)
		}
	}
	if ch.kubeClient != nil {
		// Delete BOTH backends' stashes: whichever backend is active now, a
		// removed cluster must leak neither credential.
		for _, stash := range []string{
			secrets.ConnectTokenStashName(secrets.ClusterStashKey(name)),
			secrets.VaultTokenStashName(name),
		} {
			if err := secrets.DeleteClusterCredential(ctx, ch.kubeClient, stash); err != nil {
				ch.logger.Warn("cluster delete: token stash cleanup failed", "cluster", name, "stash", stash, "error", err)
			}
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

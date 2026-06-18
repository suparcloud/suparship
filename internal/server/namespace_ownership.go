package server

import (
	"context"
	"log/slog"

	"k8s.io/client-go/kubernetes"

	"github.com/suparcloud/suparship/internal/branding"
	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/k8s"
)

// Ownership-aware project namespaces.
//
// suparship stamps the namespaces it CREATES with branding ownership labels
// (managed-by + project, + cluster). A pre-existing namespace is adopted: it is
// used as-is and never stamped, so it can never be mistaken for owned and is
// never deleted at project teardown. The gitops _infra path can't place a
// namespace on a workload cluster (the root app syncs _infra only to the
// tooling cluster), so this is done with direct workload-cluster clients via
// the ClusterClientPool — the same path used for live status/logs.

// ownedNamespaceLabels are the labels suparship stamps on namespaces it creates.
// Mirrors gitops.BuildProjectNamespaceManifest's convention so a namespace
// created here looks identical to a gitops-managed one.
func ownedNamespaceLabels(brand branding.Config, project, cluster string) map[string]string {
	labels := branding.MergeLabels(
		brand.ManagedByLabels(),
		map[string]string{brand.LabelKey("project"): project},
	)
	if cluster != "" {
		labels[brand.LabelKey("cluster")] = cluster
	}
	return labels
}

// ownedNamespaceSelector matches namespaces suparship created for a project
// (managed-by + project labels). Cluster-agnostic so one selector finds the
// project's owned namespaces on any workload cluster.
func ownedNamespaceSelector(brand branding.Config, project string) string {
	return "app.kubernetes.io/managed-by=" + brand.EffectiveName() +
		"," + brand.LabelKey("project") + "=" + project
}

// envWorkloadClient resolves the workload-cluster client for an env, plus its
// cluster ref and the org branding. For a bound (remote) cluster it uses the
// pool; an unreachable bound cluster returns a nil client so the caller skips
// rather than acting on the wrong (tooling) cluster. An unbound env falls back
// to the local kubeClient (single-cluster: local IS the workload cluster).
func (ah *appHandler) envWorkloadClient(ctx context.Context, envName string) (kubernetes.Interface, string, branding.Config) {
	var brand branding.Config
	clusterRef := ""
	if ah.orgProvider != nil {
		if org, err := ah.orgProvider.GetOrg(ctx); err == nil && org != nil {
			brand = org.Branding
			for _, e := range org.Environments {
				if e.Name == envName {
					clusterRef = e.EffectiveClusterRef()
					break
				}
			}
		}
	}
	if clusterRef != "" && ah.clusterPool != nil {
		c, err := ah.clusterPool.Get(ctx, clusterRef)
		if err != nil {
			slog.Warn("namespace ownership: workload cluster unavailable; relying on ArgoCD CreateNamespace",
				"cluster", clusterRef, "env", envName, "err", err)
			return nil, clusterRef, brand
		}
		return c, clusterRef, brand
	}
	return ah.kubeClient, clusterRef, brand
}

// ensureAppNamespace creates the app-env namespace on its workload cluster with
// suparship ownership labels, or adopts a pre-existing one unchanged. Called
// before gitops publish so suparship — not ArgoCD's CreateNamespace=true — owns
// the namespace and can reclaim it at teardown. Best-effort: a failure never
// blocks the deploy (CreateNamespace=true remains the safety net).
func (ah *appHandler) ensureAppNamespace(ctx context.Context, app *domain.App, env *domain.AppEnvironment) {
	if env == nil || env.Namespace == "" {
		return
	}
	client, clusterRef, brand := ah.envWorkloadClient(ctx, env.EnvName)
	if client == nil {
		return // bound cluster unreachable — ArgoCD CreateNamespace=true covers it
	}
	if _, err := k8s.EnsureNamespaceOwned(ctx, client, env.Namespace, ownedNamespaceLabels(brand, app.ProjectName, clusterRef)); err != nil {
		slog.Warn("ensure app namespace", "namespace", env.Namespace, "app", app.Name, "err", err)
	}
}

// ensureAppNamespaces ensures every env's namespace (create-or-adopt).
func (ah *appHandler) ensureAppNamespaces(ctx context.Context, app *domain.App, envs []*domain.AppEnvironment) {
	for _, env := range envs {
		ah.ensureAppNamespace(ctx, app, env)
	}
}

// deleteOwnedProjectNamespaces removes every namespace suparship created for the
// project across all bound workload clusters (plus the local cluster), leaving
// adopted/external ones untouched — they carry no ownership labels. Best-effort.
// Must only run after the project's workloads have been pruned.
func (ah *appHandler) deleteOwnedProjectNamespaces(ctx context.Context, projectName string) {
	if ah.orgProvider == nil {
		return
	}
	org, err := ah.orgProvider.GetOrg(ctx)
	if err != nil || org == nil {
		return
	}
	selector := ownedNamespaceSelector(org.Branding, projectName)

	// Collect each distinct workload cluster the project's envs bind to; fall
	// back to the local cluster for unbound envs / single-cluster installs.
	clients := map[string]kubernetes.Interface{}
	for _, e := range org.Environments {
		if ref := e.EffectiveClusterRef(); ref != "" && ah.clusterPool != nil {
			if _, seen := clients[ref]; seen {
				continue
			}
			if c, err := ah.clusterPool.Get(ctx, ref); err == nil {
				clients[ref] = c
			} else {
				slog.Warn("project namespace cleanup: cluster unavailable", "cluster", ref, "project", projectName, "err", err)
			}
		} else if ah.kubeClient != nil {
			clients[""] = ah.kubeClient
		}
	}
	if len(clients) == 0 && ah.kubeClient != nil {
		clients[""] = ah.kubeClient
	}

	for ref, c := range clients {
		deleted, err := k8s.DeleteOwnedNamespaces(ctx, c, selector)
		if err != nil {
			slog.Warn("project namespace cleanup failed", "cluster", ref, "project", projectName, "err", err)
			continue
		}
		if len(deleted) > 0 {
			slog.Info("project namespace cleanup: deleted owned namespaces", "cluster", ref, "project", projectName, "namespaces", deleted)
		}
	}
}

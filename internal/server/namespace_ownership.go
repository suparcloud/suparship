package server

import (
	"context"
	"log/slog"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/suparcloud/suparship/internal/branding"
	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/k8s"
	"github.com/suparcloud/suparship/internal/secrets"
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
func ownedNamespaceLabels(brand branding.Config, project, stack, cluster string) map[string]string {
	labels := branding.MergeLabels(
		brand.ManagedByLabels(),
		map[string]string{brand.LabelKey("project"): project},
	)
	if cluster != "" {
		labels[brand.LabelKey("cluster")] = cluster
	}
	// Marks a shared stack namespace ({project}-{stack}-{env}) so it's reclaimed
	// on stack delete and never reclaimed as an app-exclusive namespace on
	// rename/move (siblings share it).
	if stack != "" {
		labels[brand.LabelKey("stack")] = stack
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
	if _, err := k8s.EnsureNamespaceOwned(ctx, client, env.Namespace, ownedNamespaceLabels(brand, app.ProjectName, ah.sharedStackName(ctx, app), clusterRef)); err != nil {
		slog.Warn("ensure app namespace", "namespace", env.Namespace, "app", app.Name, "err", err)
	}
}

// sharedStackName returns the app's stack name only when that stack co-locates
// its apps (SharedNamespace) — i.e. when the app's namespace genuinely belongs
// to the stack. Empty otherwise. Used to stamp the stack ownership label.
func (ah *appHandler) sharedStackName(ctx context.Context, app *domain.App) string {
	if app.Spec.Stack == "" || ah.stackStore == nil {
		return ""
	}
	st, err := ah.stackStore.GetStack(ctx, app.ProjectName, app.Spec.Stack)
	if err != nil || st == nil || !st.Spec.SharedNamespace {
		return ""
	}
	return app.Spec.Stack
}

// relocateApp republishes an app, accounting for a possibly-changed namespace
// (e.g. after joining/leaving a shared-namespace stack, or a stack toggling
// SharedNamespace). It recomputes + persists each stable env's namespace,
// ensures the new namespaces exist, republishes, and reclaims any previous
// app-exclusive namespace that changed. Best-effort; returns the publish error.
func (ah *appHandler) relocateApp(ctx context.Context, app *domain.App) error {
	envs, _ := ah.appStore.ListAppEnvironments(ctx, app.ProjectName, app.Name)
	oldNS := map[string]string{}
	var stable []*domain.AppEnvironment
	for _, e := range envs {
		oldNS[e.EnvName] = e.Namespace
		if e.EnvType != domain.AppEnvPreview {
			stable = append(stable, e)
		}
	}
	if len(stable) == 0 {
		stable = ah.stableEnvsFromOrg(ctx, app)
	}
	ah.resolveEnvNamespaces(ctx, app, stable)
	for _, e := range stable {
		if err := ah.appStore.SaveAppEnvironment(ctx, app.ProjectName, e); err != nil {
			slog.Warn("relocate: persist namespace failed", "app", app.Name, "env", e.EnvName, "err", err)
		}
	}
	ah.ensureAppNamespaces(ctx, app, stable)
	var pubErr error
	if ah.gitOpsPublisher != nil {
		pubErr = ah.gitOpsPublisher.PublishApp(ctx, app, stable)
	}
	for _, e := range stable {
		if old := oldNS[e.EnvName]; old != "" && old != e.Namespace {
			ah.reclaimAppExclusiveNamespace(ctx, app.ProjectName, e.EnvName, old)
		}
	}
	return pubErr
}

// ensureAppNamespaces ensures every env's namespace (create-or-adopt).
func (ah *appHandler) ensureAppNamespaces(ctx context.Context, app *domain.App, envs []*domain.AppEnvironment) {
	for _, env := range envs {
		ah.ensureAppNamespace(ctx, app, env)
	}
}

// deleteOldAppNamespaces reclaims the old app's namespaces after a rename — see
// reclaimAppExclusiveNamespace for the safety rules (it never deletes a shared
// stack namespace, which siblings rely on). Best-effort.
func (ah *appHandler) deleteOldAppNamespaces(ctx context.Context, projectName string, oldEnvs []*domain.AppEnvironment) {
	for _, env := range oldEnvs {
		ah.reclaimAppExclusiveNamespace(ctx, projectName, env.EnvName, env.Namespace)
	}
}

// reclaimAppExclusiveNamespace deletes a namespace on its workload cluster only
// when it is (a) owned by suparship (managed-by + project labels) and (b)
// app-exclusive — i.e. it does NOT carry the stack ownership label. A shared
// stack namespace ({project}-{stack}-{env}) is left intact because sibling apps
// in the stack share it. Used by rename and stack-move to drop the app's
// previous standalone namespace without breaking a stack. Best-effort, no-op on
// empty name / unreachable cluster.
func (ah *appHandler) reclaimAppExclusiveNamespace(ctx context.Context, projectName, envName, namespace string) {
	if namespace == "" {
		return
	}
	client, _, brand := ah.envWorkloadClient(ctx, envName)
	if client == nil {
		return
	}
	ns, err := client.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return
	}
	if err != nil {
		slog.Warn("namespace reclaim: read failed", "namespace", namespace, "err", err)
		return
	}
	owned := ns.Labels["app.kubernetes.io/managed-by"] == brand.EffectiveName() &&
		ns.Labels[brand.LabelKey("project")] == projectName
	if !owned {
		return // adopted/external — never delete
	}
	if ns.Labels[brand.LabelKey("stack")] != "" {
		return // shared stack namespace — siblings rely on it
	}
	if err := client.CoreV1().Namespaces().Delete(ctx, namespace, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		slog.Warn("namespace reclaim: delete failed", "namespace", namespace, "err", err)
		return
	}
	slog.Info("reclaimed app-exclusive namespace", "namespace", namespace)
}

// emptyAppVaultItems best-effort empties the old app's app-tier secret items
// across every scope (global, per-env, per bound cluster) after a rename, so the
// renamed app's secrets don't shadow stale values. The VaultStore has no
// item-delete, so 1Password leaves an empty item shell — harmless and
// unreferenced. No-op when no vault is wired.
func (ah *appHandler) emptyAppVaultItems(ctx context.Context, projectName, appName string, oldEnvs []*domain.AppEnvironment) {
	if ah.vault == nil {
		return
	}
	clusterRefByEnv := map[string]string{}
	if ah.orgProvider != nil {
		if org, err := ah.orgProvider.GetOrg(ctx); err == nil && org != nil {
			for _, e := range org.Environments {
				clusterRefByEnv[e.Name] = e.EffectiveClusterRef()
			}
		}
	}
	scopes := []secrets.Scope{secrets.GlobalScope()}
	for _, env := range oldEnvs {
		scopes = append(scopes, secrets.EnvScope(env.EnvName))
		if ref := clusterRefByEnv[env.EnvName]; ref != "" {
			scopes = append(scopes, secrets.ClusterScope(env.EnvName, ref))
		}
	}
	for _, scope := range scopes {
		entries, err := ah.vault.ListKeys(ctx, scope, secrets.TierApp, appName)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if err := ah.vault.DeleteKey(ctx, scope, secrets.TierApp, appName, e.Key); err != nil {
				slog.Warn("rename: failed to clear old app secret key", "app", appName, "key", e.Key, "err", err)
			}
		}
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

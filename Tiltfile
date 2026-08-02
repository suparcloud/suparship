# -*- mode: Python -*-
#
# suparShip local development — Kargo-inspired Tilt loop.
#
#   task up            # ctlptl apply hack/dev/cluster.yaml && tilt up
#   task up:ingress    # same, plus NGINX ingress + *.localhost routing
#   task down          # tilt down
#   task cluster:delete
#
# What this does: installs every cluster prerequisite (cert-manager, Argo
# Rollouts, Kargo, ArgoCD, Gitea, External Secrets, Replicator, Reloader) in
# dependency order, then builds suparShip from source and deploys it IN-CLUSTER
# via charts/suparship with a fast live_update inner loop. Everything is reached
# via localhost port-forwards (Tilt UI: http://localhost:10350).
#
# Quick UI-only work with no cluster? Use `task dev` (fake/in-memory) instead.

load('ext://helm_resource', 'helm_resource', 'helm_repo')
load('ext://restart_process', 'docker_build_with_restart')

# Prereq installs use `helm --wait` (cert-manager webhook, kargo, argocd, gitea)
# and take well over Tilt's default 30s apply timeout — raise it generously
# (must exceed the per-chart `--timeout` flags below).
update_settings(k8s_upsert_timeout_secs=900)

# ── Safety: only ever talk to the local dev cluster ────────────────────────
EXPECTED_CONTEXT = 'kind-suparship-dev'
if k8s_context() != EXPECTED_CONTEXT:
    fail(("Tilt is pointed at %r but expected %r.\n" +
          "Create/select the dev cluster first:  ctlptl apply -f hack/dev/cluster.yaml  (or run `task up`).")
         % (k8s_context(), EXPECTED_CONTEXT))

# ── Config: optional ingress + *.localhost routing, optional workload clusters ─
config.define_bool('ingress')
config.define_bool('multi')
cfg = config.parse()
INGRESS = cfg.get('ingress', False) or os.getenv('SUPARSHIP_INGRESS') == '1'
# MULTI adds two kind WORKLOAD clusters (kind-staging, kind-prod) and rebinds the
# seeded environments onto them, so the tooling/workload split is real. Off by
# default: it costs a couple of GiB, and nothing about UI or API work needs it.
MULTI = cfg.get('multi', False) or os.getenv('SUPARSHIP_MULTI') == '1'

# Host-reachable Gitea URL the init script clones from.
GITEA_HOST_URL = 'http://gitea.localhost:8880' if INGRESS else 'http://localhost:3000'

# ── Helm repos ─────────────────────────────────────────────────────────────
helm_repo('jetstack',   'https://charts.jetstack.io', labels=['prereq'])
helm_repo('argo',       'https://argoproj.github.io/argo-helm', labels=['prereq'])
helm_repo('eso',        'https://charts.external-secrets.io', labels=['prereq'])
helm_repo('mittwald',   'https://helm.mittwald.de', labels=['prereq'])
helm_repo('stakater',   'https://stakater.github.io/stakater-charts', labels=['prereq'])
helm_repo('gitea-charts', 'https://dl.gitea.com/charts', labels=['prereq'])

# ── Namespaces (argocd before argocd install; suparship-system before app) ──
# local_resource runs in the ambient shell, so --context is pinned explicitly:
# Tilt validates its own context above but cannot police a subprocess.
local_resource(
    'namespaces',
    cmd='for ns in suparship-system argocd; do ' +
        'kubectl --context %s create ns "$ns" --dry-run=client -o yaml ' % EXPECTED_CONTEXT +
        '| kubectl --context %s apply -f -; done' % EXPECTED_CONTEXT,
    labels=['cluster'],
)

# ── Optional NGINX ingress controller (v1.10.1, kind provider) ─────────────
if INGRESS:
    local_resource(
        'ingress-nginx',
        cmd='kubectl apply -f https://raw.githubusercontent.com/kubernetes/ingress-nginx/' +
            'controller-v1.10.1/deploy/static/provider/kind/deploy.yaml && ' +
            'kubectl rollout status -n ingress-nginx deploy/ingress-nginx-controller --timeout=180s',
        resource_deps=['namespaces'],
        labels=['cluster'],
    )

# ── Kargo dependency chain: cert-manager -> argo-rollouts -> kargo ─────────
helm_resource(
    'cert-manager', 'jetstack/cert-manager', namespace='cert-manager',
    flags=['--create-namespace', '--version=v1.17.1', '--set=crds.enabled=true', '--wait'],
    resource_deps=['jetstack'], labels=['prereq'],
)
helm_resource(
    'argo-rollouts', 'argo/argo-rollouts', namespace='argo-rollouts',
    flags=['--create-namespace', '--version=2.37.6', '--wait'],
    resource_deps=['argo'], labels=['prereq'],
)
# Kargo dev profile: a usable UI.
#
# The chart defaults to TLS-on + no admin account, which locally gives you a
# self-signed cert warning followed by a login screen you cannot get past.
# suparship itself is unaffected either way — it drives Kargo through the
# Kubernetes API (CRDs via the dynamic client), never Kargo's REST API — so
# these two settings only govern whether a human can use the UI.
#
#   api.tls.enabled=false      plain HTTP over the port-forward, no cert warning.
#                              cert-manager is still required and still used:
#                              Kargo's webhook servers keep their Certificates.
#   api.adminAccount.*         login admin / devpass. The hash is a FIXED dev
#                              bcrypt of "devpass" — hardcoded on purpose, since
#                              generating one per Tiltfile load would change the
#                              Helm value every reload and churn the release.
#                              htpasswd emits $2y$, which Go's bcrypt rejects, so
#                              this is stored rewritten to the byte-compatible
#                              $2a$ (same fix as hack/dev/admin-secret.sh).
#
# Dev-only credentials, never reachable off localhost. Override the password by
# regenerating the hash:
#   htpasswd -nbBC 10 "" <pw> | cut -d: -f2 | sed 's/^\$2y\$/\$2a\$/'
KARGO_ADMIN_PASSWORD_HASH = '$2a$10$6NDmBYvv6UZvUOERfebonupDuqNVUr8Y5Tj6pgYwODQcaXsYttaJq'
helm_resource(
    'kargo', 'oci://ghcr.io/akuity/kargo-charts/kargo', namespace='kargo',
    flags=['--create-namespace', '--version=1.9.5',
           '--set=api.tls.enabled=false',
           '--set=api.adminAccount.enabled=true',
           '--set=api.adminAccount.passwordHash=' + KARGO_ADMIN_PASSWORD_HASH,
           '--set=api.adminAccount.tokenSigningKey=suparship-dev-only-kargo-signing-key',
           '--wait', '--timeout=10m0s'],
    resource_deps=['cert-manager', 'argo-rollouts'],
    labels=['prereq'],
)
# NOTE: no port_forwards on the helm_resource above — deliberately.
#
# Kargo's chart ships an hourly garbage-collector CronJob whose pods carry the
# same release labels as its long-running Deployments. Tilt binds a resource to
# the NEWEST matching pod, so within an hour of `tilt up` the `kargo` resource
# latches onto a Completed GC pod. Tilt still reports the resource green (the
# job succeeded) while its port-forward now targets a dead pod: :8083 accepts
# connections and proxies nowhere, which looks exactly like "Kargo is down"
# even though every Deployment is 1/1.
#
# Forward the API Deployment explicitly instead of relying on pod discovery.
# Plain http because api.tls.enabled=false above; the container port is named
# `h2c`, which is accurate once TLS is off (with TLS on it still served https,
# which is a trap worth knowing if you ever re-enable it).
#
# Wrapped in a retry loop on purpose: `kubectl port-forward` binds to one pod
# and exits the moment that pod goes away — which happens on every `helm
# upgrade` of the kargo release, i.e. every time you touch its flags above.
# Without the loop the resource lands in `error` and 8083 simply stops
# listening until you notice and re-trigger it by hand.
local_resource(
    'kargo-api-forward',
    serve_cmd=('while true; do ' +
               'kubectl --context %s -n kargo port-forward deploy/kargo-api 8083:8080; ' % EXPECTED_CONTEXT +
               'echo "port-forward dropped (pod replaced?) — reconnecting in 2s"; sleep 2; done'),
    resource_deps=['kargo'],
    links=[link('http://localhost:8083', 'Kargo UI (admin / devpass)')],
    labels=['prereq'],
)

# ── External Secrets Operator + suparship reader RBAC ──────────────────────
helm_resource(
    'external-secrets', 'eso/external-secrets', namespace='external-secrets',
    flags=['--create-namespace', '--version=2.2.0', '--set=installCRDs=true', '--wait'],
    resource_deps=['eso'], labels=['prereq'],
)
local_resource(
    'eso-reader', cmd='hack/install-eso-rbac.sh',
    resource_deps=['external-secrets', 'namespaces'], labels=['prereq'],
)

# ── ConfigMap/Secret replication + reload ──────────────────────────────────
helm_resource(
    'kubernetes-replicator', 'mittwald/kubernetes-replicator', namespace='stakater-replicator',
    flags=['--create-namespace', '--version=2.12.3', '--wait'],
    resource_deps=['mittwald'], labels=['prereq'],
)
helm_resource(
    'reloader', 'stakater/reloader', namespace='stakater-reloader',
    flags=['--create-namespace', '--version=2.2.9', '--wait'],
    resource_deps=['stakater'], labels=['prereq'],
)

# ── ArgoCD (dev profile) ───────────────────────────────────────────────────
helm_resource(
    'argocd', 'argo/argo-cd', namespace='argocd',
    flags=['--version=7.7.0', '--values=config/argocd/values-dev.yaml', '--wait', '--timeout=10m0s'],
    resource_deps=['argo', 'namespaces'],
    port_forwards=['8081:8080'],  # server.insecure=true -> http://localhost:8081 (admin / see notes)
    labels=['prereq'],
)

# ── Gitea (gitops git server) + repo/ArgoCD wiring ─────────────────────────
gitea_deps = ['gitea-charts', 'argocd']
if INGRESS:
    gitea_deps = gitea_deps + ['ingress-nginx']
helm_resource(
    'gitea', 'gitea-charts/gitea', namespace='gitea',
    flags=['--create-namespace', '--version=10.6.0', '--values=config/gitea/values-dev.yaml', '--wait', '--timeout=10m0s'],
    resource_deps=gitea_deps,
    port_forwards=['3000:3000'],  # http://localhost:3000  (gitops / gitops-dev-only)
    labels=['prereq'],
)
local_resource(
    'init-gitops',
    cmd='GITEA_HOST_URL=%s hack/install-gitea-init.sh' % GITEA_HOST_URL,
    resource_deps=['gitea'], labels=['prereq'],
)

# ── Dev admin Secret (login: admin / devpass) ──────────────────────────────
local_resource(
    'suparship-admin-secret', cmd='hack/dev/admin-secret.sh',
    resource_deps=['namespaces'], labels=['app'],
)

# ── Build suparShip from source (CGO dev image + live_update) ──────────────
ui_sync = [sync('./ui/dist', '/app/ui')] if os.path.exists('ui/dist') else []
docker_build_with_restart(
    'suparship-dev',
    context='.',
    dockerfile='Dockerfile.dev',
    # restart_process wraps this as `sh -c "<entrypoint>"`, which swallows the
    # chart's container args as sh positional params — so the full command must
    # live here. The rest is covered by env (SUPARSHIP_CLUSTER_MODE=kubernetes
    # from the chart; SUPARSHIP_ADDR/SUPARSHIP_UI_DIR from Dockerfile.dev) and
    # flag defaults (admin-secret-* default to the chart's values).
    entrypoint='/usr/local/bin/suparship server --log-level=debug',
    only=['go.mod', 'go.sum', 'cmd', 'internal', 'ui/dist'],
    live_update=[
        fall_back_on(['go.mod', 'go.sum']),   # dep change -> full image rebuild
        sync('./cmd', '/src/cmd'),
        sync('./internal', '/src/internal'),
    ] + ui_sync + [                            # all sync steps must precede run steps
        run('go build -o /usr/local/bin/suparship ./cmd/suparship',
            trigger=['./cmd', './internal']),
    ],
)

# ── Deploy suparShip via its own Helm chart ────────────────────────────────
_chart_yaml = helm(
    './charts/suparship',
    name='suparship',
    namespace='suparship-system',
    values=['./hack/dev/values-dev.yaml'],
    set=['ingress.enabled=%s' % ('true' if INGRESS else 'false')],
)
# Strip Helm lifecycle hooks (e.g. the prereq-check Job). Tilt renders hooks as
# plain objects with no hook ordering, so they run ungated and race the prereqs
# that Tilt already sequences via resource_deps. Drop them for the dev deploy.
def _is_helm_hook(o):
    anns = (o.get('metadata') or {}).get('annotations') or {}
    return 'helm.sh/hook' in anns
_objs = [o for o in decode_yaml_stream(_chart_yaml) if o and not _is_helm_hook(o)]
k8s_yaml(encode_yaml_stream(_objs))
k8s_resource(
    'suparship',
    # Pull the ArgoCD AppProject (createSystemProject) into this resource so it
    # inherits resource_deps and is applied AFTER ArgoCD installs its CRDs
    # (otherwise Tilt applies it in an ungated group -> "no matches for kind
    # AppProject").
    objects=['suparship-system:appproject:argocd'],
    port_forwards=['8080:8080'],  # Service 80 -> pod 8080 ; Vite proxies /api here
    resource_deps=['suparship-admin-secret', 'argocd', 'external-secrets'],
    labels=['app'],
)

# ── Seed demo data (org / project / preview + demo AppProject) ─────────────
local_resource(
    'seed', cmd='hack/seed.sh',
    resource_deps=['namespaces', 'argocd'], labels=['app'],
)

# ── Optional: real workload clusters (`task up:multi`) ─────────────────────
# Makes the tooling/workload split genuine. Each workload cluster gets ESO +
# sealed-secrets and is registered with suparship, which writes its kubeconfig
# Secret AND its ArgoCD cluster Secret — the pieces a single-cluster loop can
# never exercise. Registration needs the API up, hence the dep on `suparship`;
# `seed` must land first so seed-multi rewrites the org rather than racing it.
if MULTI:
    for wl in [('staging', 'Staging'), ('prod', 'Production')]:
        local_resource(
            'workload-%s' % wl[0],
            cmd='hack/dev/workload-cluster.sh %s %s' % (wl[0], wl[1]),
            resource_deps=['suparship', 'seed'],
            labels=['cluster'],
        )
    local_resource(
        'seed-multi', cmd='hack/dev/seed-multi.sh',
        resource_deps=['workload-staging', 'workload-prod'],
        labels=['cluster'],
    )

# ── Frontend ───────────────────────────────────────────────────────────────
# Primary loop: host Vite HMR proxying /api -> port-forwarded pod :8080.
local_resource(
    'ui-dev',
    cmd='cd ui && npm install',
    serve_cmd='cd ui && npm run dev',
    resource_deps=['suparship'],
    links=[link('http://localhost:5173', 'suparShip UI (Vite HMR)')],
    labels=['ui'],
)
# Secondary loop (opt-in via Tilt UI): bundle UI into the pod for e2e/ingress.
local_resource(
    'ui-build',
    serve_cmd='cd ui && npm run build -- --watch',
    auto_init=False,
    labels=['ui'],
)

print("""
suparShip dev cluster is starting.  Tilt UI: http://localhost:10350
  UI (Vite HMR) : http://localhost:5173      login: admin / devpass
  API (pod)     : http://localhost:8080
  ArgoCD        : http://localhost:8081
  Gitea         : http://localhost:3000       gitops / gitops-dev-only
  Kargo API     : https://localhost:8083
%s""" % ("  Ingress ON: also at http://suparship.localhost:8880" if INGRESS else "  (run `task up:ingress` for *.localhost routing)"))

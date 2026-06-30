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

# ── Safety: only ever talk to the local dev cluster ────────────────────────
EXPECTED_CONTEXT = 'kind-suparship-dev'
if k8s_context() != EXPECTED_CONTEXT:
    fail("Tilt is pointed at %r but expected %r.\n"
         "Create/select the dev cluster first:  ctlptl apply -f hack/dev/cluster.yaml  (or run `task up`)."
         % (k8s_context(), EXPECTED_CONTEXT))

# ── Config: optional ingress + *.localhost routing ─────────────────────────
config.define_bool('ingress')
cfg = config.parse()
INGRESS = cfg.get('ingress', False) or os.getenv('SUPARSHIP_INGRESS') == '1'

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
local_resource(
    'namespaces',
    cmd='for ns in suparship-system argocd; do '
        'kubectl create ns "$ns" --dry-run=client -o yaml | kubectl apply -f -; done',
    labels=['cluster'],
)

# ── Optional NGINX ingress controller (v1.10.1, kind provider) ─────────────
if INGRESS:
    local_resource(
        'ingress-nginx',
        cmd='kubectl apply -f https://raw.githubusercontent.com/kubernetes/ingress-nginx/'
            'controller-v1.10.1/deploy/static/provider/kind/deploy.yaml && '
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
helm_resource(
    'kargo', 'oci://ghcr.io/akuity/kargo-charts/kargo', namespace='kargo',
    flags=['--create-namespace', '--version=1.9.5',
           '--set=api.adminAccount.enabled=false', '--wait'],
    resource_deps=['cert-manager', 'argo-rollouts'],
    port_forwards=['8083:8080'],  # Kargo API/UI (https, self-signed) -> https://localhost:8083
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
    flags=['--version=7.7.0', '--values=config/argocd/values-dev.yaml', '--wait'],
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
    flags=['--create-namespace', '--version=10.6.0', '--values=config/gitea/values-dev.yaml', '--wait'],
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
    entrypoint='/usr/local/bin/suparship',  # chart supplies `server --addr=... ` args
    only=['go.mod', 'go.sum', 'cmd', 'internal', 'ui/dist'],
    live_update=[
        fall_back_on(['go.mod', 'go.sum']),   # dep change -> full image rebuild
        sync('./cmd', '/src/cmd'),
        sync('./internal', '/src/internal'),
        run('go build -o /usr/local/bin/suparship ./cmd/suparship',
            trigger=['./cmd', './internal']),
    ] + ui_sync,
)

# ── Deploy suparShip via its own Helm chart ────────────────────────────────
k8s_yaml(helm(
    './charts/suparship',
    name='suparship',
    namespace='suparship-system',
    values=['./hack/dev/values-dev.yaml'],
    set=['ingress.enabled=%s' % ('true' if INGRESS else 'false')],
))
k8s_resource(
    'suparship',
    port_forwards=['8080:8080'],  # Service 80 -> pod 8080 ; Vite proxies /api here
    resource_deps=['suparship-admin-secret', 'argocd', 'external-secrets'],
    labels=['app'],
)

# ── Seed demo data (org / project / preview + demo AppProject) ─────────────
local_resource(
    'seed', cmd='hack/seed.sh',
    resource_deps=['namespaces', 'argocd'], labels=['app'],
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

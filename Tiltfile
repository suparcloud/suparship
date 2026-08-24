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

# ── Config: ingress + vault on by default, optional workload clusters ─────
# --ingress / --vault are accepted for muscle-memory back-compat but are now
# the defaults; --no-ingress / --no-vault opt out for the leanest loop.
config.define_bool('ingress')
config.define_bool('no-ingress')
config.define_bool('multi')
config.define_bool('vault')
config.define_bool('no-vault')
cfg = config.parse()
# INGRESS (NGINX + *.localhost routing) is ON BY DEFAULT: without it no app
# has a browsable URL, which made every first-run demo a port-forward hunt.
# The kind cluster maps host port 80, so http://<app>.<env>.localhost just
# works once `task dev:dns` has run (one-time, macOS; docs/local-dns.md for
# Linux). Opt out with --no-ingress / SUPARSHIP_INGRESS=0.
INGRESS = not (cfg.get('no-ingress', False) or os.getenv('SUPARSHIP_INGRESS') == '0')
# MULTI adds two kind WORKLOAD clusters (kind-staging, kind-prod) and rebinds the
# seeded environments onto them, so the tooling/workload split is real. Off by
# default: it costs a couple of GiB, and nothing about UI or API work needs it.
MULTI = cfg.get('multi', False) or os.getenv('SUPARSHIP_MULTI') == '1'
# VAULT adds a HashiCorp Vault and switches the org's secret backend to it.
# ON BY DEFAULT: Vault is the recommended production backend and the k8s
# backend is deprecated (demo-only; its ClusterSecretStores never reach the
# --multi workload clusters, so ExternalSecrets there would never resolve).
# Defaulting keeps the dev loop on the same secrets path as a real install.
# Opt out with `--no-vault` (or SUPARSHIP_VAULT=0) for the leanest loop.
# Composes with --multi.
#
# The Vault it brings up PERSISTS: standalone mode with file storage on a PVC, so
# secrets you enter survive a pod restart, a `tilt down`/`tilt up`, and an image
# rebuild. (Dev-mode Vault stored everything in memory, which meant the KV mount
# itself vanished with the pod.) State still dies with the cluster, since the PVC
# is hostPath-backed on the kind node — `kind delete cluster` is the reset, or
# delete the PVC + pod for a fresh init.
#
# The cost of persistence is that Vault is no longer auto-unsealed: it comes up
# uninitialised the first time and SEALED after every restart, so vault-bootstrap
# does init + unseal (see hack/dev/vault-bootstrap.sh).
VAULT = not (cfg.get('no-vault', False) or os.getenv('SUPARSHIP_VAULT') == '0')

# Host-reachable Gitea URL the init script clones from. Always the Tilt
# port-forward: the ingress URL would drag the one-time dnsmasq setup into
# the git-init path now that ingress defaults on.
GITEA_HOST_URL = 'http://localhost:3000'

# ── Helm repos ─────────────────────────────────────────────────────────────
helm_repo('jetstack',   'https://charts.jetstack.io', labels=['prereq'])
helm_repo('argo',       'https://argoproj.github.io/argo-helm', labels=['prereq'])
helm_repo('eso',        'https://charts.external-secrets.io', labels=['prereq'])
helm_repo('mittwald',   'https://helm.mittwald.de', labels=['prereq'])
helm_repo('stakater',   'https://stakater.github.io/stakater-charts', labels=['prereq'])
helm_repo('gitea-charts', 'https://dl.gitea.com/charts', labels=['prereq'])
helm_repo('sealed-secrets-repo', 'https://bitnami.github.io/sealed-secrets', labels=['prereq'])
if VAULT:
    helm_repo('hashicorp', 'https://helm.releases.hashicorp.com', labels=['prereq'])

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
#   api.adminAccount.*         login admin / admin123 (password-only). Hash is a FIXED
#                              bcrypt of "admin123" — hardcoded on purpose, since
#                              generating one per Tiltfile load would change the
#                              Helm value every reload and churn the release.
#                              htpasswd emits $2y$, which Go's bcrypt rejects, so
#                              this is stored rewritten to the byte-compatible
#                              $2a$ (same fix as hack/dev/admin-secret.sh).
#
# Dev-only credentials, never reachable off localhost. Override the password by
# regenerating the hash:
#   htpasswd -nbBC 10 "" <pw> | cut -d: -f2 | sed 's/^\$2y\$/\$2a\$/'
KARGO_ADMIN_PASSWORD_HASH = '$2a$10$neYoAax4aaQRd6HyEdDf9uH0nMFdBJe8IktiNI9Utya59cTxGU96W'
helm_resource(
    'kargo', 'oci://ghcr.io/akuity/kargo-charts/kargo', namespace='kargo',
    flags=['--create-namespace', '--version=1.9.5',
           '--set=api.tls.enabled=false',
           # Dev-only: the gitops repo lives in the in-cluster Gitea over plain
           # HTTP, and Kargo refuses to hand promotion steps credentials for
           # http:// endpoints by default ("refused to get credentials for
           # insecure HTTP endpoint") — git-push then fails with "could not
           # read Username". Never enable off-localhost.
           '--set=controller.allowCredentialsOverHTTP=true',
           '--set=api.adminAccount.enabled=true',
           '--set=api.adminAccount.passwordHash=' + KARGO_ADMIN_PASSWORD_HASH,
           '--set=api.adminAccount.tokenSigningKey=suparship-dev-only-kargo-signing-key',
           '--wait', '--timeout=10m0s'],
    # argocd must precede kargo: kargo-controller checks for the Argo CD CRDs
    # ONCE at startup and, if absent, permanently disables its ArgoCD
    # integration for the life of the pod — every promotion's argocd-update
    # step then fails with "Argo CD integration is disabled on this
    # controller". (Observed on fresh `task up`: controller started ~90s
    # before the CRDs landed.)
    resource_deps=['cert-manager', 'argo-rollouts', 'argocd'],
    labels=['prereq'],
)
# Kargo can't speak plain HTTP to the kind registry (it always dials HTTPS;
# insecureSkipTLSVerify only skips cert verification), so warehouse image
# discovery — and with it every promotion — fails without this shim: an
# in-cluster TLS-terminating proxy plus a hostAliases patch scoped to
# kargo-controller. See hack/dev/kargo-registry-shim.sh.
local_resource(
    'kargo-registry-shim',
    cmd='hack/dev/kargo-registry-shim.sh',
    resource_deps=['kargo'],
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
    links=[link('http://localhost:8083', 'Kargo UI (password: admin123)')],
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

# ── HashiCorp Vault secrets backend (default; skip with --no-vault) ────────
# Standalone Vault with file storage on a PVC, so its data survives pod restarts
# (see the VAULT comment above). The injector is disabled because suparship's
# delivery path is ESO, not agent injection.
#
# Two deliberate departures from the usual helm_resource shape, both forced by
# the fact that a persistent Vault starts sealed:
#
#   - NO --wait. The chart's readiness probe runs `vault status`, which exits
#     non-zero while Vault is uninitialised or sealed, so the pod never becomes
#     Ready and `helm install --wait` would sit there until it timed out. Tilt
#     instead treats the resource as done when the install returns, and
#     vault-bootstrap waits for the pod to be Running before init/unseal.
#   - readinessProbe disabled. The chart's probe IS `vault status`, whose exit
#     code is the seal status (0 unsealed, 2 sealed), so a sealed pod never goes
#     Ready. vault-bootstrap gates on `resource_deps=['vault', ...]`, and Tilt
#     resolves that on pod readiness — so leaving the probe on deadlocks the very
#     step that would unseal it. bootstrap reaches Vault by `kubectl exec`, not
#     through the Service, so it does not need the probe; and suparship/ESO only
#     dial Vault after bootstrap has unsealed it.
if VAULT:
    # Clusters created before Vault became persistent have a storage-less
    # StatefulSet, and the API forbids ADDING volumeClaimTemplates to an existing
    # one — so `helm upgrade` fails outright and the vault resource goes red.
    # Delete the incompatible StatefulSet first so helm recreates it with the
    # volume. No-op on a fresh cluster and on every later run.
    local_resource(
        'vault-storage-migrate', cmd='hack/dev/vault-storage-migrate.sh',
        labels=['prereq'],
    )
    helm_resource(
        'vault', 'hashicorp/vault', namespace='vault',
        flags=['--create-namespace', '--version=0.30.0',
               '--set=server.standalone.enabled=true',
               '--set=server.dataStorage.enabled=true',
               '--set=server.dataStorage.size=1Gi',
               '--set=server.readinessProbe.enabled=false',
               '--set=injector.enabled=false',
               '--timeout=5m0s'],
        resource_deps=['hashicorp', 'vault-storage-migrate'],
        port_forwards=['8200:8200'],  # http://localhost:8200
        links=[link('http://localhost:8200', 'Vault UI (token: see vault-bootstrap logs)')],
        labels=['prereq'],
    )
    # Init + unseal, then mount + token Secrets + org backend switch. Runs AFTER
    # seed (and seed-multi in multi mode): both rewrite the org ConfigMap
    # wholesale and would erase the backend selection, whereas this script goes
    # through PUT /org/secret-backend, which merges. Re-trigger this resource if
    # you ever re-run seed by hand.
    #
    # RE-TRIGGER IT AFTER A VAULT POD RESTART TOO. A persistent Vault comes back
    # sealed, and nothing in the cluster unseals it automatically — that would
    # mean handing the unseal key to a controller, which is not worth building
    # for a dev loop. The script is idempotent: it unseals, notices the mount and
    # Secrets already exist, and re-asserts the org backend.
    _vault_bootstrap_deps = ['vault', 'external-secrets', 'suparship', 'seed']
    if MULTI:
        _vault_bootstrap_deps += ['seed-multi']
    # SUPARSHIP_MULTI switches the org's Vault address to a NodePort on the
    # tooling node's docker-network IP, so workload clusters can reach it —
    # they cannot resolve the tooling cluster's Service DNS.
    local_resource(
        'vault-bootstrap', cmd='SUPARSHIP_MULTI=%s hack/dev/vault-bootstrap.sh' % ('1' if MULTI else '0'),
        resource_deps=_vault_bootstrap_deps,
        labels=['prereq'],
    )
    # Run the REAL per-cluster seal pipeline for the seeded clusters: paste the
    # dev Vault write token for each cluster so suparship seals a read token
    # with the cluster's sealed-secrets cert and publishes the unified store.
    # This is what turns credential health from "no sealed read token" to
    # green — the same flow an operator drives from Settings in production.
    local_resource(
        'seal-cluster-tokens', cmd='hack/dev/seal-cluster-tokens.sh',
        resource_deps=['vault-bootstrap', 'sealed-secrets'],
        labels=['prereq'],
    )

# ── Sealed-secrets: MANDATORY platform prerequisite ────────────────────────
# suparship's per-cluster secret delivery seals backend read tokens (Vault /
# 1Password Connect) with each workload cluster's sealed-secrets certificate;
# without the controller no cluster can ever get a sealed read token and the
# Platform page stays "Not ready". fullnameOverride matches the upstream
# default service name the seal cert fetcher expects
# (internal/seal: kube-system/sealed-secrets-controller).
helm_resource(
    'sealed-secrets', 'sealed-secrets-repo/sealed-secrets', namespace='kube-system',
    flags=['--version=2.16.2', '--set=fullnameOverride=sealed-secrets-controller', '--wait'],
    resource_deps=['sealed-secrets-repo'], labels=['prereq'],
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

# ── Template catalog (examples/charts via the registry) ────────────────────
# There are no built-in templates: the dev loop gets its catalog the same way
# a real install does — a gitcharts source. This pushes examples/charts into
# Gitea and registers/syncs the example-charts source.
local_resource(
    'seed-templates',
    cmd='hack/dev/seed-example-charts.sh',
    resource_deps=['suparship', 'init-gitops', 'seed'],
    labels=['app'],
)

# ── Shipnotes demo (manual ▶ in the Tilt UI, or `task demo:shipnotes`) ─────
# One click → the full working demo: mirror suparship-demo into Gitea, wire
# its CI, register the example-charts source (postgres), wait for the first
# images, create the composed app, set DATABASE_URL, print the tour.
local_resource(
    'demo-shipnotes',
    cmd='hack/dev/demo-shipnotes.sh',
    auto_init=False,
    trigger_mode=TRIGGER_MODE_MANUAL,
    resource_deps=['suparship', 'init-gitops', 'act-runner', 'seed', 'seed-templates'],
    labels=['demo'],
    links=[link('http://shipnotes-frontend.staging.localhost', 'Shipnotes (staging)')],
)

# ── Gitea Actions runner (host docker) ─────────────────────────────────────
# Powers the CI-driven golden path (`task demo:shipnotes`): workflows in
# mirrored repos build images straight into the kind registry. Runs on the
# HOST daemon — no privileged dind pods; see hack/dev/act-runner.sh for the
# full rationale and the reset procedure after a cluster recreate.
local_resource(
    'act-runner',
    serve_cmd='hack/dev/act-runner.sh',
    resource_deps=['gitea'],
    labels=['prereq'],
)

# ── Private (authenticated) registry ───────────────────────────────────────
# Stands in for a real private registry (ghcr/ECR/Harbor) so credential flows
# can be exercised locally — docker login, suparship registry settings with
# creds, Kargo warehouse auth. The ctlptl kind registry stays the
# unauthenticated workhorse for Tilt builds and kind-node pulls; see the
# header of hack/dev/private-registry.yaml for the full division of labor.
# Login: admin / admin123.  Host: localhost:5010; in-cluster:
# private-registry.registry.svc.cluster.local:5000 (HTTP + basic auth).
k8s_yaml('hack/dev/private-registry.yaml')
k8s_resource(
    'private-registry',
    objects=[
        'registry:namespace',
        'registry-htpasswd:secret',
        'registry-data:persistentvolumeclaim',
    ],
    port_forwards=['5010:5000'],
    links=[link('http://localhost:5010/v2/_catalog', 'Private registry catalog (admin / admin123)')],
    labels=['prereq'],
)

# ── Dev admin Secret (login: admin@local / admin123) ──────────────────────────────
local_resource(
    'suparship-admin-secret', cmd='hack/dev/admin-secret.sh',
    resource_deps=['namespaces'], labels=['app'],
)

# ── GitOps push credentials ────────────────────────────────────────────────
# Must exist BEFORE the suparship pod starts: gitops config is read at boot, and
# without a usable credential the publisher stays disabled for the process
# lifetime — app creates then silently write nothing.
local_resource(
    'gitops-credentials', cmd='hack/dev/gitops-credentials.sh',
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
    # There are no built-in templates: the server starts with zero and serves
    # cluster templates live; the `seed-templates` resource registers the
    # example-charts registry source so the catalog exists out of the box.
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

# Strip the ConfigMaps the chart intends to create ONCE and then leave alone.
#
# The chart guards them with `{{- if not (lookup "v1" "ConfigMap" ...) }}`, which
# works under `helm install/upgrade` — but Tilt renders with `helm template`,
# where `lookup` ALWAYS returns nil. So the guard is always true, Tilt applies
# them on every reconcile, and they clobber whatever the running system has.
#
# For the org ConfigMap that is destructive: hack/seed.sh and
# hack/dev/seed-multi.sh own it, and values-dev.yaml deliberately sets no
# `environments`, so each apply silently ERASED every environment binding —
# leaving "skipping kargo stage for unbound env" and nothing deployable.
#
# In dev, seed scripts own org + cluster state. The gitops/registry ConfigMaps
# are NOT stripped: values-dev.yaml is their real source of truth.
_SEED_OWNED = ['suparship-org-config']
def _is_seed_owned(o):
    if o.get('kind') != 'ConfigMap':
        return False
    name = (o.get('metadata') or {}).get('name') or ''
    return name in _SEED_OWNED or name.startswith('suparship-cluster-')

_objs = [o for o in decode_yaml_stream(_chart_yaml)
         if o and not _is_helm_hook(o) and not _is_seed_owned(o)]
k8s_yaml(encode_yaml_stream(_objs))
k8s_resource(
    'suparship',
    # Pull the ArgoCD AppProject (createSystemProject) into this resource so it
    # inherits resource_deps and is applied AFTER ArgoCD installs its CRDs
    # (otherwise Tilt applies it in an ungated group -> "no matches for kind
    # AppProject").
    objects=['suparship-system:appproject:argocd'],
    port_forwards=['8080:8080'],  # Service 80 -> pod 8080 ; Vite proxies /api here
    resource_deps=['suparship-admin-secret', 'gitops-credentials', 'init-gitops', 'argocd', 'external-secrets'],
    labels=['app'],
)

# ── Seed demo data (org / project / preview + demo AppProject) ─────────────
# SUPARSHIP_MULTI tells the seed to skip the placeholder cluster records, so no
# app is ever published against a cluster that is about to be deleted — see the
# note in hack/seed.sh.
local_resource(
    'seed', cmd='SUPARSHIP_MULTI=%s hack/seed.sh' % ('1' if MULTI else '0'),
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

# ── Endpoints summary ──────────────────────────────────────────────────────
# The table itself lives in hack/dev/endpoints.sh (single source of truth,
# also echoed by `task up*` in the terminal). This resource re-prints it once
# the user-facing services are ready and carries every endpoint as a clickable
# link, so the Tilt UI has ONE row that answers "where is everything?".
_endpoint_links = [
    link('http://localhost:5173', 'suparShip UI (admin@local / admin123)'),
    link('http://localhost:8080', 'suparShip API'),
    link('http://localhost:8081', 'ArgoCD'),
    link('http://localhost:3000', 'Gitea (gitops / gitops-dev-only)'),
    link('http://localhost:8083', 'Kargo UI (password: admin123)'),
    link('http://localhost:5010/v2/_catalog', 'Private registry (admin / admin123)'),
]
_endpoint_deps = ['suparship', 'ui-dev', 'gitea', 'argocd', 'kargo-api-forward', 'private-registry']
if VAULT:
    _endpoint_links.append(link('http://localhost:8200', 'Vault UI (token: see vault-bootstrap logs)'))
    _endpoint_deps.append('vault-bootstrap')
if INGRESS:
    _endpoint_links.append(link('http://suparship.localhost', 'suparShip via ingress'))
    _endpoint_deps.append('ingress-nginx')
local_resource(
    'endpoints',
    cmd='SUPARSHIP_VAULT=%s SUPARSHIP_INGRESS=%s hack/dev/endpoints.sh' % (
        '1' if VAULT else '0', '1' if INGRESS else '0'),
    resource_deps=_endpoint_deps,
    links=_endpoint_links,
    labels=['cluster'],
)

print("suparShip dev cluster is starting.  Tilt UI: http://localhost:10350\n" +
      "Endpoints: see the `endpoints` resource once green (links + table).\n" +
      str(local('SUPARSHIP_VAULT=%s SUPARSHIP_INGRESS=%s hack/dev/endpoints.sh' % (
          '1' if VAULT else '0', '1' if INGRESS else '0'), quiet=True)))

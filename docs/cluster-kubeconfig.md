# Create a token-based kubeconfig for registering a cluster

suparShip registers a workload cluster from a **kubeconfig** (Settings → Clusters
→ Register cluster). The most portable, long-lived credential is a
**ServiceAccount token** — it works the same on every distribution and, unlike
exec / cloud-IAM kubeconfigs (EKS `aws-iam-authenticator`, GKE `gcloud`), it can
be stored and replayed by suparShip's hub and by ArgoCD.

> This is also the recommended path for EKS/GKE clusters that the
> [Import from ArgoCD](install.md#6-register-at-least-one-workload-cluster) flow
> lists as **not importable** because they use exec / cloud-IAM auth: create a
> token kubeconfig with the steps below and register the cluster with it.

Run all `kubectl` commands **against the workload cluster** you want to register
(i.e. with your kubeconfig pointed at that cluster's context).

## 1. Create a ServiceAccount and grant it access

suparShip's stored credential is used both by ArgoCD (to apply your app
manifests) and by suparShip's hub (to read status/logs and fetch the
sealed-secrets cert). ArgoCD applies arbitrary manifests, so `cluster-admin` is
the simplest correct choice. You can scope it down later, but the role must cover
everything your apps deploy plus cluster-wide read.

```bash
kubectl create serviceaccount suparship -n kube-system

kubectl create clusterrolebinding suparship \
  --clusterrole=cluster-admin \
  --serviceaccount=kube-system:suparship
```

## 2. Mint a long-lived token Secret for the ServiceAccount

Kubernetes 1.24+ no longer auto-creates a token Secret for a ServiceAccount.
Create one explicitly — its token does not expire (unlike `kubectl create token`,
which is time-bound):

```bash
kubectl apply -f - <<'EOF'
apiVersion: v1
kind: Secret
metadata:
  name: suparship-token
  namespace: kube-system
  annotations:
    kubernetes.io/service-account.name: suparship
type: kubernetes.io/service-account-token
EOF
```

> Prefer short-lived tokens? Use
> `kubectl create token suparship -n kube-system --duration=8760h` instead of the
> Secret, and skip the `.data.token` lookup below — but you'll have to re-register
> the cluster when it expires.

## 3. Assemble the kubeconfig

Pull the API server URL, the cluster CA, and the token, then write a kubeconfig:

```bash
SERVER=$(kubectl config view --minify -o jsonpath='{.clusters[0].cluster.server}')
CA=$(kubectl get secret suparship-token -n kube-system -o jsonpath='{.data.ca\.crt}')
TOKEN=$(kubectl get secret suparship-token -n kube-system -o jsonpath='{.data.token}' | base64 -d)

cat > suparship-kubeconfig.yaml <<EOF
apiVersion: v1
kind: Config
clusters:
- name: target
  cluster:
    server: ${SERVER}
    certificate-authority-data: ${CA}
contexts:
- name: target
  context:
    cluster: target
    user: suparship
current-context: target
users:
- name: suparship
  user:
    token: ${TOKEN}
EOF
```

`certificate-authority-data` is taken straight from the Secret (already base64).
The `token` is decoded to its plaintext form, which is what a kubeconfig expects.

> If the API server is only reachable over a private endpoint (e.g. an AKS/EKS
> privatelink address), make sure suparShip's hub can route to that `server`
> value. Use the address that is reachable from the hub.

## 4. Verify and register

Confirm the kubeconfig works before uploading it:

```bash
kubectl --kubeconfig suparship-kubeconfig.yaml get nodes
```

Then in suparShip: **Settings → Clusters → Register cluster**

- **API server URL** — paste `$SERVER` (no trailing space; it's validated).
- **Kubeconfig** — upload `suparship-kubeconfig.yaml`.

suparShip stores the kubeconfig in a Kubernetes Secret (never in Git), registers
the cluster with ArgoCD, and fetches the sealed-secrets cert in the background.
The cluster should show **ready**.

## Rotating or revoking

- **Rotate** — delete and recreate the token Secret (step 2), rebuild the
  kubeconfig (step 3), and re-register the cluster (or use Refresh cert if only
  the CA changed).
- **Revoke** — delete the ServiceAccount and its ClusterRoleBinding; the token is
  immediately invalid. Remove the cluster from suparShip too.

#!/usr/bin/env bash
# hack/dev/admin-secret.sh — create the dev admin Secret the suparship chart
# expects (it does not create it). Login becomes:  admin / devpass
#
# `suparship admin bootstrap` generates a RANDOM password, so it can't give a
# fixed dev login — we derive the bcrypt hash deterministically here.
#
# bcrypt note: htpasswd -B emits a "$2y$" prefix which Go's golang.org/x/crypto
# /bcrypt (internal/auth/password.go) REJECTS. "$2y$" and "$2a$" are
# byte-compatible, so we rewrite the prefix.
#
# Override the password with SUPARSHIP_DEV_PASSWORD. Used by: Tiltfile.
set -euo pipefail

# Pin kubectl to the dev cluster — see hack/dev/lib.sh.
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
require_dev_context

NS="${SUPARSHIP_SYSTEM_NAMESPACE:-suparship-system}"
USER="${SUPARSHIP_DEV_USER:-admin}"
PW="${SUPARSHIP_DEV_PASSWORD:-devpass}"
SECRET_NAME="suparship-admin-auth"

if ! command -v htpasswd >/dev/null 2>&1; then
  echo "ERROR: 'htpasswd' not found (install apache2-utils / httpd-tools, or 'brew install httpd')." >&2
  exit 1
fi

# bcrypt cost 10; rewrite $2y$ -> $2a$ for Go bcrypt compatibility.
HASH="$(htpasswd -nbBC 10 "$USER" "$PW" | cut -d: -f2 | sed 's/^\$2y\$/\$2a\$/')"

kubectl get namespace "$NS" >/dev/null 2>&1 || kubectl create namespace "$NS"

kubectl create secret generic "$SECRET_NAME" -n "$NS" \
  --from-literal=username="$USER" \
  --from-literal=password-hash="$HASH" \
  --dry-run=client -o yaml | kubectl apply -f -

echo "  ✓ ${SECRET_NAME} ready in ${NS}  (login: ${USER} / ${PW})"

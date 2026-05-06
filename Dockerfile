# syntax=docker/dockerfile:1.7

# ── Stage 1: build the React/Vite frontend ─────────────────────────────
FROM --platform=$BUILDPLATFORM node:22-alpine AS ui-builder
WORKDIR /ui
COPY ui/package.json ui/package-lock.json ./
RUN npm ci
COPY ui/ ./
RUN npm run build

# ── Stage 2: build the Go binary ───────────────────────────────────────
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS go-builder
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown
ARG DATE=unknown
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build \
      -trimpath \
      -ldflags="-s -w \
        -X github.com/suparcloud/suparship/internal/version.Version=${VERSION} \
        -X github.com/suparcloud/suparship/internal/version.Commit=${COMMIT} \
        -X github.com/suparcloud/suparship/internal/version.Date=${DATE}" \
      -o /out/suparship ./cmd/suparship

# ── Stage 3: runtime image ─────────────────────────────────────────────
# Includes git, helm, and kubeseal because the binary shells out to all
# three (registrysync, gitops publisher, sealed-secret writer).
FROM alpine:3.21
ARG TARGETARCH
ARG HELM_VERSION=3.16.4
ARG KUBESEAL_VERSION=0.27.3

RUN set -eux; \
    apk add --no-cache ca-certificates git tini wget; \
    case "$TARGETARCH" in \
      amd64) ARCH=amd64 ;; \
      arm64) ARCH=arm64 ;; \
      *) echo "unsupported arch: $TARGETARCH" >&2; exit 1 ;; \
    esac; \
    wget -qO- "https://get.helm.sh/helm-v${HELM_VERSION}-linux-${ARCH}.tar.gz" \
      | tar -xz -C /tmp; \
    mv "/tmp/linux-${ARCH}/helm" /usr/local/bin/helm; \
    rm -rf "/tmp/linux-${ARCH}"; \
    wget -qO /tmp/kubeseal.tar.gz \
      "https://github.com/bitnami-labs/sealed-secrets/releases/download/v${KUBESEAL_VERSION}/kubeseal-${KUBESEAL_VERSION}-linux-${ARCH}.tar.gz"; \
    tar -xzf /tmp/kubeseal.tar.gz -C /tmp kubeseal; \
    mv /tmp/kubeseal /usr/local/bin/kubeseal; \
    rm /tmp/kubeseal.tar.gz; \
    addgroup -g 65532 suparship; \
    adduser -D -u 65532 -G suparship suparship; \
    mkdir -p /app; \
    chown -R suparship:suparship /app

WORKDIR /app
COPY --from=go-builder /out/suparship /usr/local/bin/suparship
COPY --from=ui-builder /ui/dist /app/ui/dist

USER suparship
ENV SUPARSHIP_UI_DIR=/app/ui/dist \
    SUPARSHIP_ADDR=:8080
EXPOSE 8080

ENTRYPOINT ["/sbin/tini", "--", "/usr/local/bin/suparship"]
CMD ["server"]

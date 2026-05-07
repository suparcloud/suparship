# syntax=docker/dockerfile:1.7

# tonistiigi/xx provides xx-go / xx-apk wrappers that select the right
# cross-toolchain (CC, AR, GOOS/GOARCH) for $TARGETPLATFORM, so we can
# run the build on $BUILDPLATFORM (host arch) and emit a binary for the
# target arch — no QEMU required.
FROM --platform=$BUILDPLATFORM tonistiigi/xx:1.5.0 AS xx

# ── Stage 1: build the React/Vite frontend ─────────────────────────────
FROM --platform=$BUILDPLATFORM node:22-alpine AS ui-builder
WORKDIR /ui
COPY ui/package.json ui/package-lock.json ./
RUN npm ci
COPY ui/ ./
RUN npm run build

# ── Stage 2: build the Go binary ───────────────────────────────────────
# CGO is required by the 1Password SDK. xx-go cross-compiles against the
# target platform's musl headers fetched by `xx-apk add`, so a single
# invocation on the build host produces both linux/amd64 and linux/arm64
# binaries without QEMU emulation.
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS go-builder
COPY --from=xx / /
ARG TARGETPLATFORM
ARG VERSION=dev
ARG COMMIT=unknown
ARG DATE=unknown
RUN apk add --no-cache clang lld
RUN xx-apk add --no-cache musl-dev gcc
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 xx-go build \
      -trimpath \
      -ldflags="-s -w \
        -X github.com/suparcloud/suparship/internal/version.Version=${VERSION} \
        -X github.com/suparcloud/suparship/internal/version.Commit=${COMMIT} \
        -X github.com/suparcloud/suparship/internal/version.Date=${DATE}" \
      -o /out/suparship ./cmd/suparship && \
    xx-verify /out/suparship

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
COPY --from=ui-builder /ui/dist /app/ui

USER suparship
ENV SUPARSHIP_UI_DIR=/app/ui \
    SUPARSHIP_ADDR=:8080
EXPOSE 8080

ENTRYPOINT ["/sbin/tini", "--", "/usr/local/bin/suparship"]
CMD ["server"]

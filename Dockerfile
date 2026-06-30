# syntax=docker/dockerfile:1

# Stage 1: Go builder
FROM golang:1.26 AS go-builder
WORKDIR /app
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download
COPY . .
ARG BUILD_SHA=dev
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -ldflags "-X github.com/assembledhq/143/internal/version.BuildSHA=${BUILD_SHA}" -o /bin/server ./cmd/server && \
    CGO_ENABLED=0 go build -ldflags "-X github.com/assembledhq/143/internal/version.BuildSHA=${BUILD_SHA}" -o /bin/session-executor ./cmd/server && \
    CGO_ENABLED=0 go build -o /bin/migrate ./cmd/migrate && \
    CGO_ENABLED=0 go build -o /bin/demo-seed ./cmd/demo-seed && \
    CGO_ENABLED=0 go build -o /bin/deploy-guardrail ./cmd/deploy-guardrail && \
    CGO_ENABLED=0 go build -o /bin/worker-deployctl ./cmd/worker-deployctl

# Cross-compile the 143-tools CLI for laptop installs (darwin/linux ×
# amd64/arm64) and generate the checksums.txt the installer verifies. Served
# by the Go server from /opt/143/cli via /install.sh and /download/143-tools/*.
# Keep the matrix in sync with `make build-cli` and cli_distribution.go.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    mkdir -p /opt/143/cli && \
    for platform in darwin/amd64 darwin/arm64 linux/amd64 linux/arm64; do \
      GOOS=${platform%/*} GOARCH=${platform#*/} CGO_ENABLED=0 \
      go build -trimpath -ldflags "-X github.com/assembledhq/143/internal/version.BuildSHA=${BUILD_SHA}" \
        -o /opt/143/cli/143-tools-${platform%/*}-${platform#*/} ./cmd/tools; \
    done && \
    cd /opt/143/cli && sha256sum 143-tools-* > checksums.txt

# Stage 2: Runtime
FROM debian:bookworm-slim
WORKDIR /app

# Install runtime deps + sops/age for encrypted secrets decryption at boot.
# libheif-examples provides heif-convert for server-side iPhone HEIC upload
# normalization before attachments are handed to coding agents.
RUN apt-get update && apt-get install -y ca-certificates wget libheif-examples && rm -rf /var/lib/apt/lists/* \
    && ARCH=$(dpkg --print-architecture) \
    && wget -qO /usr/local/bin/sops "https://github.com/getsops/sops/releases/download/v3.9.4/sops-v3.9.4.linux.${ARCH}" \
    && chmod +x /usr/local/bin/sops \
    && wget -qO /tmp/age.tar.gz "https://dl.filippo.io/age/v1.2.0?for=linux/${ARCH}" \
    && tar -xzf /tmp/age.tar.gz -C /tmp \
    && mv /tmp/age/age /usr/local/bin/age \
    && mv /tmp/age/age-keygen /usr/local/bin/age-keygen \
    && rm -rf /tmp/age /tmp/age.tar.gz

COPY --from=go-builder /bin/server /bin/server
COPY --from=go-builder /bin/session-executor /bin/session-executor
COPY --from=go-builder /bin/migrate /bin/migrate
COPY --from=go-builder /bin/demo-seed /bin/demo-seed
COPY --from=go-builder /bin/deploy-guardrail /bin/deploy-guardrail
COPY --from=go-builder /bin/worker-deployctl /bin/worker-deployctl
COPY --from=go-builder /app/migrations /migrations
COPY --from=go-builder /app/.143/seed /demo-seed
COPY --from=go-builder /opt/143/cli /opt/143/cli

# Copy entrypoint. The encrypted production env bundle is NOT baked into
# the image (this image is public on GHCR) — deploy bind-mounts it from
# /opt/143/.env.production.enc on the host to /app/.env.production.enc.
COPY docker-entrypoint.sh /docker-entrypoint.sh

EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget -qO- http://localhost:8080/healthz || exit 1

# UID 1000 is pinned so the host-side bind-mount source for the sandbox
# auth socket directory (/var/run/143/sandbox-auth, owned 1000:1000 by
# systemd-tmpfiles — see deploy/scripts/provision.sh) is writable by
# this user, and so the per-session unix socket created here at mode 0600
# is readable by the sandbox container's `sandbox` user (also UID 1000).
RUN useradd --uid 1000 --user-group --shell /usr/sbin/nologin appuser \
    && mkdir -p /app/.data \
    && chown -R appuser:appuser /app
USER appuser

ENTRYPOINT ["/docker-entrypoint.sh"]
CMD ["/bin/server"]

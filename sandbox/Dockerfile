# syntax=docker/dockerfile:1
# Sandbox image for running coding agents (Claude Code, Codex CLI, Gemini CLI).
# Tagged as 143-sandbox:latest by default.
#
# Build:  docker build -t 143-sandbox:latest -f sandbox/Dockerfile .
# See also: docs/design/24-design-resolutions.md (Resolution 15)

# Stage 1: Build 143-tools Go binary.
FROM golang:1.26 AS go-builder
WORKDIR /app
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -o /bin/143-tools ./cmd/tools

# Stage 2: Runtime
FROM ubuntu:24.04

# Retry apt downloads to survive transient mirror flakiness.
RUN echo 'Acquire::Retries "3";' > /etc/apt/apt.conf.d/80-retries

# System dependencies for agent CLIs and typical repo operations.
# postgresql-client is needed when a preview's command chain seeds a Postgres
# infrastructure container via `psql -f`; keeping it in the base image avoids
# per-preview apt installs.
RUN apt-get update && apt-get install -y --no-install-recommends \
    git \
    curl \
    ca-certificates \
    jq \
    sudo \
    postgresql-client \
    && rm -rf /var/lib/apt/lists/*

# Install Node.js 24 LTS via NodeSource.
RUN curl -fsSL https://deb.nodesource.com/setup_24.x | bash - \
    && apt-get install -y --no-install-recommends nodejs \
    && rm -rf /var/lib/apt/lists/*

# Install Go toolchain matching the builder stage so previews that run the
# agent's own Go source (e.g. `go run ./cmd/server`) compile inside the
# sandbox at session-start time.
#
# IMPORTANT: keep GO_VERSION below in sync with the `FROM golang:X.Y` pin in
# the go-builder stage at the top of this Dockerfile. If they drift, a
# session will compile against a different toolchain than the builder used,
# which can mask `//go:build` and module-resolution issues.
ARG TARGETARCH
RUN set -eux; \
    GO_VERSION="1.26.0"; \
    case "${TARGETARCH:-amd64}" in \
      amd64) GO_ARCH="amd64" ;; \
      arm64) GO_ARCH="arm64" ;; \
      *) echo "unsupported TARGETARCH=${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${GO_ARCH}.tar.gz" -o /tmp/go.tar.gz; \
    tar -C /usr/local -xzf /tmp/go.tar.gz; \
    rm /tmp/go.tar.gz
ENV PATH="/usr/local/go/bin:${PATH}"

# Install agent CLIs at pinned versions.
# The npm cache mount avoids re-downloading unchanged packages on rebuilds.
COPY sandbox/versions.json /tmp/versions.json
RUN --mount=type=cache,target=/root/.npm \
    CLAUDE_CODE_VERSION=$(jq -r '.claude_code' /tmp/versions.json) && \
    CODEX_CLI_VERSION=$(jq -r '.codex_cli' /tmp/versions.json) && \
    GEMINI_CLI_VERSION=$(jq -r '.gemini_cli' /tmp/versions.json) && \
    npm install -g \
      "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}" \
      "@openai/codex@${CODEX_CLI_VERSION}" \
      "@google/gemini-cli@${GEMINI_CLI_VERSION}" && \
    rm /tmp/versions.json

# Copy 143-tools binary from Go builder.
COPY --from=go-builder /bin/143-tools /usr/local/bin/143-tools

# Non-root user for sandbox execution with passwordless sudo.
RUN useradd -m -s /bin/bash sandbox \
    && echo 'sandbox ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/sandbox \
    && chmod 0440 /etc/sudoers.d/sandbox
USER sandbox
WORKDIR /workspace

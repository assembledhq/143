# syntax=docker/dockerfile:1
# Sandbox image for running coding agents (Claude Code, Codex CLI, Gemini CLI, Amp, Pi).
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
#
# sudo is intentionally omitted: under gVisor (and no-new-privileges /
# userns-remap / nosuid mounts) the kernel strips setuid bits, so `sudo`
# fails with "effective uid is not 0 ... nosuid". Any provisioning that
# needs root runs via docker exec User="root" from the provider instead.
# Consequence: agents cannot `apt-get install` at runtime — every
# dependency must be baked into this image up front, which is why the
# package set below is on the generous side.
#
# Package rationale:
#   - git/curl/ca-certificates/jq: baseline repo + HTTP + JSON tools.
#   - postgresql-client: previews seed Postgres via `psql -f`; baked in
#     here so per-preview startup doesn't apt-install it.
#   - python3/python3-pip/python3-venv: Python is the #1 missing runtime
#     when agents work on non-Node repos (scripts, pytest, linters, etc.).
#   - build-essential: gcc/g++/make/libc-dev — needed whenever a Go/Rust/
#     Node module has C bindings (ring, openssl-sys, node-gyp native
#     addons, cgo with C deps). Without it, `go build` / `cargo build` /
#     `npm install` fails opaquely on compile.
RUN apt-get update && apt-get install -y --no-install-recommends \
    git \
    curl \
    ca-certificates \
    jq \
    postgresql-client \
    python3 \
    python3-pip \
    python3-venv \
    build-essential \
    && rm -rf /var/lib/apt/lists/*

# Install Node.js 24 LTS via NodeSource.
RUN curl -fsSL https://deb.nodesource.com/setup_24.x | bash - \
    && apt-get install -y --no-install-recommends nodejs \
    && rm -rf /var/lib/apt/lists/*

# Install GitHub CLI (gh) at the version pinned in versions.json. We use the
# upstream tarball rather than the apt repo to avoid an extra signing-key
# rotation chore and to keep the version pin in exactly one place. gh reads
# GH_TOKEN automatically, so the agent can invoke `gh pr ...` directly with
# the token returned by the per-session credential helper.
COPY sandbox/versions.json /tmp/versions.json
RUN GH_VERSION=$(jq -r '.gh_cli' /tmp/versions.json) && \
    ARCH=$(dpkg --print-architecture) && \
    case "$ARCH" in \
        amd64) GH_ARCH=amd64 ;; \
        arm64) GH_ARCH=arm64 ;; \
        *) echo "unsupported arch: $ARCH" >&2; exit 1 ;; \
    esac && \
    curl -fsSL "https://github.com/cli/cli/releases/download/v${GH_VERSION}/gh_${GH_VERSION}_linux_${GH_ARCH}.tar.gz" -o /tmp/gh.tgz && \
    tar -xzf /tmp/gh.tgz -C /tmp && \
    install -m 0755 "/tmp/gh_${GH_VERSION}_linux_${GH_ARCH}/bin/gh" /usr/local/bin/gh && \
    rm -rf /tmp/gh.tgz "/tmp/gh_${GH_VERSION}_linux_${GH_ARCH}" /tmp/versions.json && \
    gh --version

# Copy the Go toolchain from the go-builder stage so previews that run the
# agent's own Go source (e.g. `go run ./cmd/server`) compile inside the
# sandbox at session-start time. Reusing the builder's /usr/local/go
# guarantees the runtime and builder toolchains cannot drift — the version
# is pinned in exactly one place (the `FROM golang:X.Y` line above).
COPY --from=go-builder /usr/local/go /usr/local/go
ENV PATH="/usr/local/go/bin:${PATH}"

# Install agent CLIs at pinned versions.
# The npm cache mount avoids re-downloading unchanged packages on rebuilds.
COPY sandbox/versions.json /tmp/versions.json
RUN --mount=type=cache,target=/root/.npm \
    CLAUDE_CODE_VERSION=$(jq -r '.claude_code' /tmp/versions.json) && \
    CODEX_CLI_VERSION=$(jq -r '.codex_cli' /tmp/versions.json) && \
    GEMINI_CLI_VERSION=$(jq -r '.gemini_cli' /tmp/versions.json) && \
    AMP_CLI_VERSION=$(jq -r '.amp_cli' /tmp/versions.json) && \
    PI_CLI_VERSION=$(jq -r '.pi_cli' /tmp/versions.json) && \
    npm install -g \
      "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}" \
      "@openai/codex@${CODEX_CLI_VERSION}" \
      "@google/gemini-cli@${GEMINI_CLI_VERSION}" \
      "@sourcegraph/amp@${AMP_CLI_VERSION}" \
      "@mariozechner/pi-coding-agent@${PI_CLI_VERSION}" && \
    rm /tmp/versions.json

# Copy 143-tools binary from Go builder.
COPY --from=go-builder /bin/143-tools /usr/local/bin/143-tools

# Non-root user for sandbox execution. No sudo is provisioned — see the
# comment on the apt-get block above.
#
# UID 1000 is pinned to match the worker server image's `appuser` (which
# creates the per-session credential socket at mode 0600). The bind-mounted
# socket file's owner UID is preserved across the mount, so both processes
# must agree on the same numeric UID for the in-sandbox client to open it.
#
# ubuntu:24.04 ships a default `ubuntu` user pre-claiming UID 1000, which
# collides with our useradd. Remove it first; `|| true` keeps the build
# working if a future base image drops the user.
RUN userdel -r ubuntu 2>/dev/null || true \
    && useradd --uid 1000 --create-home --user-group --shell /bin/bash sandbox
USER sandbox

# Per-repo .143/config.json `environment.commands` install opt-in tooling
# (linters, formatters, language toolchains) at session start. Without sudo,
# /usr/local/bin is off-limits at runtime, so installs land in the sandbox
# user's ~/.local/bin — keep it on PATH so subsequent bootstrap/validation
# commands can call those tools.
ENV PATH="/home/sandbox/.local/bin:${PATH}"

WORKDIR /workspace

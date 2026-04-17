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
RUN apt-get update && apt-get install -y --no-install-recommends \
    git \
    curl \
    ca-certificates \
    jq \
    sudo \
    && rm -rf /var/lib/apt/lists/*

# Install Node.js 24 LTS via NodeSource.
RUN curl -fsSL https://deb.nodesource.com/setup_24.x | bash - \
    && apt-get install -y --no-install-recommends nodejs \
    && rm -rf /var/lib/apt/lists/*

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

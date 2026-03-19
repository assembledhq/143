# syntax=docker/dockerfile:1
# Sandbox image for running coding agents (Claude Code, Codex CLI, Gemini CLI).
# Tagged as 143-sandbox:latest by default.
#
# Build:  docker build -t 143-sandbox:latest sandbox/
# See also: docs/design/24-design-resolutions.md (Resolution 15)

FROM ubuntu:26.04

# System dependencies for agent CLIs and typical repo operations.
RUN apt-get update && apt-get install -y --no-install-recommends \
    git \
    curl \
    ca-certificates \
    jq \
    && rm -rf /var/lib/apt/lists/*

# Install Node.js 24 LTS via NodeSource.
RUN curl -fsSL https://deb.nodesource.com/setup_24.x | bash - \
    && apt-get install -y --no-install-recommends nodejs \
    && rm -rf /var/lib/apt/lists/*

# Install agent CLIs at pinned versions.
# The npm cache mount avoids re-downloading unchanged packages on rebuilds.
COPY versions.json /tmp/versions.json
RUN --mount=type=cache,target=/root/.npm \
    CLAUDE_CODE_VERSION=$(jq -r '.claude_code' /tmp/versions.json) && \
    CODEX_CLI_VERSION=$(jq -r '.codex_cli' /tmp/versions.json) && \
    GEMINI_CLI_VERSION=$(jq -r '.gemini_cli' /tmp/versions.json) && \
    npm install -g \
      "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}" \
      "@openai/codex@${CODEX_CLI_VERSION}" \
      "@google/gemini-cli@${GEMINI_CLI_VERSION}" && \
    rm /tmp/versions.json

# Non-root user for sandbox execution.
RUN useradd -m -s /bin/bash sandbox
USER sandbox
WORKDIR /workspace

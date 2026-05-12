#!/usr/bin/env bash
set -euo pipefail

# install-agents.sh — installs pinned versions of agent CLIs into the sandbox image.
# Reads versions from versions.json co-located in the same directory.

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
VERSIONS_FILE="${SCRIPT_DIR}/versions.json"

if [ ! -f "$VERSIONS_FILE" ]; then
  echo "ERROR: versions.json not found at $VERSIONS_FILE" >&2
  exit 1
fi

CLAUDE_CODE_VERSION=$(jq -r '.claude_code' "$VERSIONS_FILE")
CODEX_CLI_VERSION=$(jq -r '.codex_cli' "$VERSIONS_FILE")
GEMINI_CLI_VERSION=$(jq -r '.gemini_cli' "$VERSIONS_FILE")
AMP_CLI_VERSION=$(jq -r '.amp_cli' "$VERSIONS_FILE")
PI_CLI_VERSION=$(jq -r '.pi_cli' "$VERSIONS_FILE")

echo "Installing Claude Code v${CLAUDE_CODE_VERSION}..."
npm install -g "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}"

echo "Installing Codex CLI v${CODEX_CLI_VERSION}..."
npm install -g "@openai/codex@${CODEX_CLI_VERSION}"

echo "Installing Gemini CLI v${GEMINI_CLI_VERSION}..."
npm install -g "@google/gemini-cli@${GEMINI_CLI_VERSION}"

echo "Installing Amp CLI v${AMP_CLI_VERSION}..."
npm install -g "@sourcegraph/amp@${AMP_CLI_VERSION}"

echo "Installing Pi CLI v${PI_CLI_VERSION}..."
npm install -g "@earendil-works/pi-coding-agent@${PI_CLI_VERSION}"

echo "Verifying installations..."
claude --version
codex --version
gemini --version
amp --version
pi --version

echo "All agent CLIs installed successfully."

#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT_DIR="${OUT_DIR:-$ROOT/internal/services/agent/adapters/testdata/opencode}"
MODEL="${OPENCODE_NATIVE_MODEL:-opencode/gpt-5.2}"

mkdir -p "$OUT_DIR"

if [[ -z "${OPENCODE_API_KEY:-}" ]]; then
  echo "OPENCODE_API_KEY is required to probe OpenCode native auth" >&2
  exit 64
fi

if ! command -v opencode >/dev/null 2>&1; then
  echo "opencode is not installed; install with: npm install -g opencode-ai" >&2
  exit 127
fi

OPENCODE_DISABLE_AUTOUPDATE=true \
OPENCODE_DISABLE_DEFAULT_PLUGINS=true \
OPENCODE_DISABLE_MODELS_FETCH=true \
OPENCODE_PERMISSION='{"permission":"allow"}' \
OPENCODE_CONFIG_CONTENT='{"$schema":"https://opencode.ai/config.json","permission":"allow","model":"'"$MODEL"'","provider":{"opencode":{"options":{"apiKey":"{env:OPENCODE_API_KEY}"}}}}' \
  opencode run \
    --format json \
    --dangerously-skip-permissions \
    --agent build \
    --model "$MODEL" \
    --dir "$ROOT" \
    "Answer with exactly: native opencode auth ok" > "$OUT_DIR/native_auth.real.jsonl"

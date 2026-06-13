#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT_DIR="${OUT_DIR:-$ROOT/internal/services/agent/adapters/testdata/opencode}"
MODEL="${OPENCODE_MODEL:-openai/gpt-5.4-mini}"

mkdir -p "$OUT_DIR"

if ! command -v opencode >/dev/null 2>&1; then
  echo "opencode is not installed; install with: npm install -g opencode-ai" >&2
  exit 127
fi

run_fixture() {
  local name="$1"
  local prompt="$2"
  OPENCODE_DISABLE_AUTOUPDATE=true \
  OPENCODE_DISABLE_DEFAULT_PLUGINS=true \
  OPENCODE_DISABLE_MODELS_FETCH=true \
  OPENCODE_PERMISSION='{"permission":"allow"}' \
    opencode run \
      --format json \
      --dangerously-skip-permissions \
      --agent build \
      --model "$MODEL" \
      --dir "$ROOT" \
      "$prompt" > "$OUT_DIR/$name.jsonl"
}

run_fixture "simple_answer.real" "Answer with exactly: fixture ok"
run_fixture "file_edit.real" "Create a temporary file named .opencode-fixture.tmp containing fixture ok, then report what changed."
run_fixture "shell_command.real" "Run pwd and report the current directory."

set +e
run_fixture "failing_command.real" "Run false, then explain the failure."
status=$?
set -e
if [[ "$status" -ne 0 ]]; then
  echo "captured failing_command.real with exit code $status" >&2
fi

rm -f "$ROOT/.opencode-fixture.tmp"

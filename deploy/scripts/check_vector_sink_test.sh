#!/usr/bin/env bash
# Included by the deploy shell-test CI gate.
set -euo pipefail
python3 -B "$(dirname "$0")/check_vector_sink_test.py"

#!/bin/sh
# Install locked dependency sources for review; this does not build or test the app.
set -eu
SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
exec python3 "$SCRIPT_DIR/review-dependencies.py" "$@"

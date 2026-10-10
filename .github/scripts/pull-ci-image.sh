#!/usr/bin/env bash
# Pull equivalent images from trusted registries, then tag for the CI consumer.
set -euo pipefail

if [ "$#" -lt 2 ]; then
  echo "Usage: $0 TARGET_IMAGE SOURCE_IMAGE [SOURCE_IMAGE ...]" >&2
  exit 2
fi

target_image=$1
shift

# A registry can rate-limit shared runner addresses or stall during a download.
# Bound every attempt and retry the sources once after a short backoff.
for round in 1 2; do
  for candidate in "$@"; do
    printf 'Pulling %s (round %s/2)\n' "$candidate" "$round"
    if timeout --kill-after=5s 90s docker pull "$candidate"; then
      docker tag "$candidate" "$target_image"
      exit 0
    fi
    printf 'Pull failed for %s; trying the next source.\n' "$candidate" >&2
  done
  if [ "$round" -eq 1 ]; then
    sleep 5
  fi
done

printf 'Unable to pull %s from any configured source after two rounds.\n' "$target_image" >&2
exit 1

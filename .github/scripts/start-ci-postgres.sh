#!/usr/bin/env bash
# Start the disposable CI database only after registry fallback succeeds.
set -euo pipefail

: "${GITHUB_OUTPUT:?GITHUB_OUTPUT is required to retain the container ID}"

bash "$(dirname "$0")/pull-ci-image.sh" postgres:17 \
  public.ecr.aws/docker/library/postgres:17 \
  mirror.gcr.io/library/postgres:17 \
  postgres:17

container_id=$(docker run --detach --publish 5432:5432 \
  --env POSTGRES_USER=onefortythree \
  --env POSTGRES_PASSWORD=dev \
  --env POSTGRES_DB=onefortythree \
  --health-cmd pg_isready --health-interval 10s \
  --health-timeout 5s --health-retries 5 \
  postgres:17 -c max_locks_per_transaction=256)
printf 'container_id=%s\n' "$container_id" >> "$GITHUB_OUTPUT"

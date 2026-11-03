#!/usr/bin/env bash
# Post-test snapshots, not peak usage. Only the disposable CI container is queried.
# Never capture raw inspect/env, SQL text, identifiers, table data, or server logs:
# PostgreSQL error logs can include fixture contents in STATEMENT/DETAIL records.
set -euo pipefail

: "${POSTGRES_CONTAINER:?POSTGRES_CONTAINER is required}"
case "${CI_POSTGRES_DIAGNOSTICS_SCOPE:-database}" in
  database) diagnostics="${RUNNER_TEMP:?}/ci-db-diagnostics" ;;
  command) diagnostics="${RUNNER_TEMP:?}/ci-command-diagnostics" ;;
  demoseed) diagnostics="${RUNNER_TEMP:?}/ci-demoseed-diagnostics" ;;
  *) printf 'Unsupported CI PostgreSQL diagnostic scope\n' >&2; exit 1 ;;
esac
mkdir -p "$diagnostics"

capture() {
  local name="$1"
  shift
  local status
  # Do not publish raw errors or partial output: Docker errors can contain host
  # paths, while PostgreSQL errors can include SQL and connection details.
  if timeout 10s "$@" >"$diagnostics/$name" 2>/dev/null; then
    return
  else
    status=$?
    printf '{"unavailable":true,"exit_status":%s}\n' "$status" >"$diagnostics/$name"
    printf '::warning::CI PostgreSQL diagnostic %s unavailable (exit %s)\n' "$name" "$status"
  fi
}

capture container-state.json docker inspect --format \
  '{"status":{{json .State.Status}},"running":{{.State.Running}},"oom_killed":{{.State.OOMKilled}},"exit_code":{{.State.ExitCode}},"restart_count":{{.RestartCount}},"memory_limit_bytes":{{.HostConfig.Memory}},"nano_cpus":{{.HostConfig.NanoCpus}}}' \
  "$POSTGRES_CONTAINER"
capture container-resources.txt docker stats --no-stream --format \
  'cpu={{.CPUPerc}} memory={{.MemUsage}} memory_percent={{.MemPerc}} pids={{.PIDs}}' \
  "$POSTGRES_CONTAINER"

# All queries are aggregate-only and read-only, bounded both at the connection
# and command level. No database/user/client/application names or query text.
sql="SELECT json_build_object(
  'observed_at', clock_timestamp(),
  'server_version', current_setting('server_version'),
  'max_connections', current_setting('max_connections'),
  'max_locks_per_transaction', current_setting('max_locks_per_transaction'),
  'activity', (SELECT coalesce(json_agg(a), '[]'::json) FROM (
    SELECT state, wait_event_type, wait_event, count(*) AS connections
    FROM pg_stat_activity GROUP BY state, wait_event_type, wait_event
  ) a),
  'locks', (SELECT coalesce(json_agg(l), '[]'::json) FROM (
    SELECT locktype, mode, granted, count(*) AS locks
    FROM pg_locks GROUP BY locktype, mode, granted
  ) l),
  'database_totals', (SELECT json_build_object(
    'connections', sum(numbackends), 'commits', sum(xact_commit),
    'rollbacks', sum(xact_rollback), 'deadlocks', sum(deadlocks),
    'temp_files', sum(temp_files), 'temp_bytes', sum(temp_bytes)
  ) FROM pg_stat_database)
);"
capture postgres-snapshot.json docker exec \
  --env 'PGOPTIONS=-c statement_timeout=5000 -c lock_timeout=1000 -c default_transaction_read_only=on' \
  "$POSTGRES_CONTAINER" psql --no-psqlrc --no-password --tuples-only --no-align \
  --set=ON_ERROR_STOP=1 --username=onefortythree --dbname=onefortythree --command "$sql"

# PostgreSQL resource tuning

Use this procedure to evaluate the dedicated database configuration in
`deploy/postgres/postgresql.conf`. Treat the proposed budgets as workload-specific
starting points. Measure your own workload before adopting them.

## Budgets and tradeoffs

| Setting | Proposed value | Meaning |
| --- | --- | --- |
| `work_mem` | `8MB` | Budget for each sort/hash operation before spilling. Hash operations also use `hash_mem_multiplier`; several operations and parallel processes can run at once. |
| `maintenance_work_mem` | `128MB` | Memory budget for manual maintenance, including index creation. Smaller budgets can increase sorting and maintenance time. |
| `autovacuum_work_mem` | `128MB` | Independent budget for each autovacuum worker. With four workers, the combined allowance can reach 512 MB. |
| `max_parallel_workers_per_gather` | `2` | Limits helpers for one parallel query. The leader may also work; queries can take longer with fewer helpers. |
| `temp_file_limit` | `2GB` | Limit on temporary files held by each database process at one time, including maintenance sorts. Exceeding it cancels the transaction. It excludes explicit temporary tables and does not cap aggregate disk usage. |
| `log_temp_files` | `16MB` | Reports sufficiently large temporary files when they are deleted. It does not measure current occupancy. |

These settings do not change `shared_buffers`, `max_connections`, or the worker
connection policy. They do not guarantee memory savings. See the PostgreSQL 18
[resource settings](https://www.postgresql.org/docs/18/runtime-config-resource.html)
and [temporary-file logging](https://www.postgresql.org/docs/18/runtime-config-logging.html#GUC-LOG-TEMP-FILES).

## Approve the baseline before changing defaults

Keep proposed defaults out of the deployed configuration until the database
operator approves all of the following:

- Inventory every filesystem used by the data directory, WAL, tablespaces,
  `temp_tablespaces`, container logs, and backups. Budget free bytes for concurrent
  temporary files, ordinary growth, WAL retention, maintenance, and recovery.
  A 2 GB per-process limit is not a 2 GB database-wide limit.
- Record memory available, container memory limits, Linux `CommitLimit` and
  `Committed_AS`, swap activity, connection counts, query latency, failures,
  temporary-file growth, and I/O under comparable peak workloads. Commit counters
  describe virtual-memory accounting, not resident database memory or a promise
  that the next allocation will succeed.
- Size the largest index builds, `REINDEX`, `CLUSTER`, and other maintenance sorts
  on a representative restored dataset. Test pending migrations as well as normal
  application queries. Choose an explicit maintenance budget if 2 GB is too small.
- Rehearse authentication, malformed-config rejection, reload, effective-setting
  readback, rollback, and a failed large-index build on a disposable instance using
  the exact PostgreSQL 18 image, Compose mount layout, and `pg_hba.conf` you deploy.
  A PostgreSQL 17 rehearsal or Bash syntax check is useful but does not satisfy
  this exact-version/container gate.
- Agree on workload-specific stop thresholds and an observation period covering
  representative traffic and maintenance. Verify usable retained backups and
  appoint an operator who can restore the previous configuration.
- Hold all database deployment/provisioning paths, scheduled or manual, for the
  entire reload and rollback observation window. Pause the deployment workflow
  that can include the database, wait for in-flight runs to finish, and coordinate
  a maintenance hold with every operator. A GitHub concurrency group does not
  serialize a command run from a laptop.

A later database deploy/provision uses the tracked configuration. In particular,
`make deploy-db` and a fleet deployment that includes `db` copy the repository
configuration and recreate PostgreSQL. Merging defaults therefore approves their
use on the next such operation. Merge approved defaults only as part of the
coordinated database maintenance decision; do not use a database deployment as
this procedure's tuning step. Routine app/worker deployments alone do not reload
PostgreSQL.

Keep live baselines and approval records in your private operations repository.
CI configuration tests check tracked text, not disk capacity, representative
performance, or PostgreSQL 18 parsing.

## Authenticated read-only baseline

The following blocks run in **one Bash session on your database host**. They
assume the repository's dedicated Compose service in `/opt/143`; adapt and
rehearse paths for your own installation first. Use a shell without tracing
(`set -x`), and do not print or copy the container's environment. The helper sets
`PGPASSWORD` only inside the database container from its existing
`POSTGRES_PASSWORD`, then runs `psql` in that same shell. Local socket access
requires authentication under the repository's `pg_hba.conf`.

```bash
set -euo pipefail
cd /opt/143
DB_CONTAINER="$(docker compose -f docker-compose.db.yml ps -q postgres)"
test -n "$DB_CONTAINER"
test "$(docker inspect -f '{{.State.Running}}' "$DB_CONTAINER")" = true
CONFIG_HOST=/opt/143/deploy/postgres/postgresql.conf
CONFIG_CONTAINER=/etc/postgresql/conf.d/custom.conf

db_psql() {
  docker exec "$DB_CONTAINER" sh -eu -c '
    : "${POSTGRES_PASSWORD:?database container password is required}"
    export PGPASSWORD="$POSTGRES_PASSWORD"
    export PGCONNECT_TIMEOUT=5
    exec psql -X -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" "$@"
  ' sh "$@" < /dev/null
}

test "$(db_psql -Atc 'SHOW server_version_num')" -ge 180000
test "$(db_psql -Atc 'SHOW server_version_num')" -lt 190000
test "$(db_psql -Atc 'SHOW config_file')" = "$CONFIG_CONTAINER"
test "$(db_psql -Atc 'SELECT count(*) FROM pg_file_settings WHERE error IS NOT NULL')" = 0
test "$(db_psql -Atc 'SELECT count(*) FROM pg_settings WHERE pending_restart')" = 0

db_psql -c "SELECT name, setting, unit, context, source, sourcefile, pending_restart
FROM pg_settings WHERE name IN
('work_mem','maintenance_work_mem','autovacuum_work_mem',
 'max_parallel_workers_per_gather','temp_file_limit','log_temp_files',
 'hash_mem_multiplier','temp_tablespaces') ORDER BY name"
db_psql -c 'SHOW data_directory'
db_psql -c 'SELECT spcname, pg_tablespace_location(oid) FROM pg_tablespace'
db_psql -c 'SELECT datname, numbackends, temp_files, temp_bytes, deadlocks, stats_reset FROM pg_stat_database'
db_psql -c 'SELECT backend_type, state, count(*) FROM pg_stat_activity GROUP BY backend_type, state ORDER BY backend_type, state'
df -B1 /opt/143
awk '/^(MemAvailable|CommitLimit|Committed_AS|SwapTotal|SwapFree):/ { print }' /proc/meminfo
docker stats --no-stream "$DB_CONTAINER"
```

Repeat the SQL counters and filesystem samples over the agreed control window;
compare deltas only when `stats_reset` is unchanged. `df /opt/143` is a starting
point: use `docker inspect` mounts and the tablespace/data-directory results to
sample each actual backing filesystem, including WAL and external logs/backups.
Use your application's latency/error telemetry alongside the database samples.
The readonly application role may not expose other sessions' details; these
owner-role diagnostics are local to the database container.

## Stage and validate an approved candidate

After the baseline and exact PostgreSQL 18 rehearsal are approved, copy the
reviewed candidate to `/opt/143/deploy/postgres/postgresql.conf.candidate` without
replacing the live file. Set `PG_TUNING_APPROVED=yes` in this Bash session **only
when the private approval record, deployment hold, baseline, maintenance sizing,
and rollback rehearsal are complete**. The script below refuses to proceed
without that gate. It also rejects extra settings, includes, duplicate active
assignments, and any active change outside the six proposed settings.

```bash
set -euo pipefail
test "${PG_TUNING_APPROVED:-}" = yes
CANDIDATE_HOST=/opt/143/deploy/postgres/postgresql.conf.candidate
test -f "$CANDIDATE_HOST"
# This flat configuration contains no credentials; the container postgres user
# must be able to read the staged copy.
chmod 0644 "$CANDIDATE_HOST"
python3 - "$CONFIG_HOST" "$CANDIDATE_HOST" <<'PY'
import pathlib, re, sys
expected = {
    'work_mem': '8MB', 'maintenance_work_mem': '128MB',
    'autovacuum_work_mem': '128MB', 'max_parallel_workers_per_gather': '2',
    'temp_file_limit': '2GB', 'log_temp_files': '16MB',
}
def assignments(path):
    result = {}
    for line in pathlib.Path(path).read_text().splitlines():
        text = line.strip()
        if not text or text.startswith('#'):
            continue
        match = re.fullmatch(r"([a-z_]+)\s*=\s*([^#]+?)(?:\s*#.*)?", text)
        if not match or match[1] in result or match[1].startswith('include'):
            raise SystemExit('unsupported or duplicate active config assignment')
        result[match[1]] = match[2].strip()
    return result
prior, candidate = map(assignments, sys.argv[1:])
if any(candidate.get(key) != value for key, value in expected.items()):
    raise SystemExit('candidate does not match the reviewed tuning values')
if {k: v for k, v in prior.items() if k not in expected} != {
    k: v for k, v in candidate.items() if k not in expected
}:
    raise SystemExit('candidate changes settings outside the tuning scope')
PY

BACKUP_HOST="$(mktemp "${CONFIG_HOST}.before-tuning.XXXXXX")"
cp -p "$CONFIG_HOST" "$BACKUP_HOST"
CONFIG_INODE="$(stat -c '%d:%i' "$CONFIG_HOST")"
SETTINGS_SQL="SELECT name, setting, COALESCE(unit,''), source,
COALESCE(sourcefile,''), pending_restart FROM pg_settings WHERE name IN
('work_mem','maintenance_work_mem','autovacuum_work_mem',
 'max_parallel_workers_per_gather','temp_file_limit','log_temp_files') ORDER BY name"
PRIOR_SETTINGS="$(db_psql -At -F '|' -c "$SETTINGS_SQL")"
printf '%s\n' "$PRIOR_SETTINGS" > "${BACKUP_HOST}.settings"
printf '%s\n' "$CONFIG_INODE" > "${BACKUP_HOST}.inode"
CANDIDATE_CONTAINER="/tmp/143-postgresql.conf.candidate.$(date -u +%Y%m%dT%H%M%SZ)"
docker cp "$CANDIDATE_HOST" "$DB_CONTAINER:$CANDIDATE_CONTAINER"
# Parsing the complete file rejects invalid names/values. This alone does not
# establish effective settings or exclude restart-only changes.
docker exec -u postgres "$DB_CONTAINER" sh -eu -c '
  exec postgres -D "$PGDATA" -c "config_file=$1" -C work_mem
' sh "$CANDIDATE_CONTAINER" < /dev/null
```

Retain the backup path and its `.settings`/`.inode` checkpoint files in the
private maintenance record. They contain configuration metadata, not credentials.
The parser is intentionally limited to this repository's flat config format;
it fails closed for installations using includes or more complex quoted values.
Do not weaken it to force an unexpected candidate through.

## Reload and read back

All six proposed settings support reload. Do not restart PostgreSQL or change
restart-only settings such as `shared_buffers` or `max_connections` here. Write
in place: replacing the host file with `mv` can leave the running container's
single-file bind mount attached to the old inode.

The PostgreSQL [configuration-file view](https://www.postgresql.org/docs/18/view-pg-file-settings.html)
reads the file currently on disk. Zero errors there does not establish that the
running server has adopted the file. Reload is asynchronous; new connections
must verify the effective values and their source after the signal.

```bash
set -Eeuo pipefail
test "${PG_TUNING_APPROVED:-}" = yes
restore_prior_tuning() {
  test -f "$BACKUP_HOST" || return 1
  test "$(stat -c '%d:%i' "$CONFIG_HOST")" = "$CONFIG_INODE" || return 1
  cat "$BACKUP_HOST" > "$CONFIG_HOST" || return 1
  test "$(stat -c '%d:%i' "$CONFIG_HOST")" = "$CONFIG_INODE" || return 1
  test "$(db_psql -Atc 'SELECT count(*) FROM pg_file_settings WHERE error IS NOT NULL')" = 0 || return 1
  test "$(db_psql -Atc 'SELECT pg_reload_conf()')" = t || return 1
  local restored=no observed
  for attempt in $(seq 1 30); do
    observed="$(db_psql -At -F '|' -c "$SETTINGS_SQL")" || return 1
    if test "$observed" = "$PRIOR_SETTINGS"; then
      restored=yes
      break
    fi
    sleep 1
  done
  test "$restored" = yes || return 1
  test "$(db_psql -Atc 'SELECT count(*) FROM pg_file_settings WHERE error IS NOT NULL')" = 0 || return 1
  test "$(db_psql -Atc 'SELECT count(*) FROM pg_settings WHERE pending_restart')" = 0 || return 1
  db_psql -c "$SETTINGS_SQL" || return 1
}
restore_on_error() {
  local status=$?
  trap - ERR
  if restore_prior_tuning; then
    printf '%s\n' 'Tuning failed; previous settings restored. Keep the deployment hold.' >&2
  else
    printf '%s\n' 'Rollback could not be confirmed. Keep the deployment hold and escalate to the database operator.' >&2
  fi
  exit "$status"
}
trap restore_on_error ERR
test "$(stat -c '%d:%i' "$CONFIG_HOST")" = "$CONFIG_INODE"
cat "$CANDIDATE_HOST" > "$CONFIG_HOST"
test "$(stat -c '%d:%i' "$CONFIG_HOST")" = "$CONFIG_INODE"
# Any error aborts before reload. Use the rollback block if this test fails.
test "$(db_psql -Atc 'SELECT count(*) FROM pg_file_settings WHERE error IS NOT NULL')" = 0
test "$(db_psql -Atc 'SELECT pg_reload_conf()')" = t

EFFECTIVE_SQL="WITH expected(name,setting) AS (VALUES
('work_mem','8192'),('maintenance_work_mem','131072'),
('autovacuum_work_mem','131072'),('max_parallel_workers_per_gather','2'),
('temp_file_limit','2097152'),('log_temp_files','16384'))
SELECT count(*) FROM expected e JOIN pg_settings s USING (name)
WHERE s.setting=e.setting AND s.source='configuration file'
AND s.sourcefile='/etc/postgresql/conf.d/custom.conf' AND NOT s.pending_restart"
APPLIED=no
for attempt in $(seq 1 30); do
  if test "$(db_psql -Atc "$EFFECTIVE_SQL")" = 6; then
    APPLIED=yes
    break
  fi
  sleep 1
done
test "$APPLIED" = yes
test "$(db_psql -Atc 'SELECT count(*) FROM pg_file_settings WHERE error IS NOT NULL')" = 0
test "$(db_psql -Atc 'SELECT count(*) FROM pg_settings WHERE pending_restart')" = 0
db_psql -c "$SETTINGS_SQL"
trap - ERR
```

A validation/readback error runs the authenticated restoration function before
exiting. If restoration cannot be confirmed, keep the deployment hold and have
the database operator recover using the retained checkpoint. Do not leave an
invalid candidate on disk for a later restart. The trap does not handle a killed
shell, lost host, or hardware failure; retain the checkpoints outside the session.

Each `db_psql` call opens a new connection, bypasses `.psqlrc`, and fails on SQL
errors. These are owner-role checks. Also verify the six values from a fresh
connection using the application's actual database, role, and connection options;
role/database settings or `PGOPTIONS` may override file defaults. Exercise normal
reads/writes and representative review/job work through the application. A successful
reload receipt is not sufficient to accept application behavior.

During the approved observation window, compare query/maintenance latency,
errors, job completion, memory commitment, swap/OOM events, I/O, disk free bytes,
and `temp_files`/`temp_bytes` deltas against the control window. Inspect temporary
file deletion logs, but do not treat them as live disk occupancy. Roll back at the
agreed thresholds; record workload changes that prevent a valid comparison.

## Rollback that survives the next deployment

This block also works in a new maintenance Bash session: recover `BACKUP_HOST`
from the private record first. It re-establishes authentication and loads the
retained settings/inode checkpoints without applying the candidate. Restoration
must preserve the live bind-mount inode, signal reload, and confirm the previous
settings on new connections. Do this even if the candidate never passed validation;
the invalid on-disk file must not survive for a future database restart.

```bash
set -euo pipefail
cd /opt/143
# Recover this exact path from your private maintenance record.
: "${BACKUP_HOST:?set the retained before-tuning backup path}"
test -f "$BACKUP_HOST"
test -f "${BACKUP_HOST}.settings"
test -f "${BACKUP_HOST}.inode"
DB_CONTAINER="$(docker compose -f docker-compose.db.yml ps -q postgres)"
test -n "$DB_CONTAINER"
test "$(docker inspect -f '{{.State.Running}}' "$DB_CONTAINER")" = true
CONFIG_HOST=/opt/143/deploy/postgres/postgresql.conf
CONFIG_CONTAINER=/etc/postgresql/conf.d/custom.conf

db_psql() {
  docker exec "$DB_CONTAINER" sh -eu -c '
    : "${POSTGRES_PASSWORD:?database container password is required}"
    export PGPASSWORD="$POSTGRES_PASSWORD"
    export PGCONNECT_TIMEOUT=5
    exec psql -X -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" "$@"
  ' sh "$@" < /dev/null
}

# Check the running server's config path; do not require its on-disk file to
# parse before restoring a backup, because that file may be the failed candidate.
test "$(db_psql -Atc 'SHOW config_file')" = "$CONFIG_CONTAINER"
SETTINGS_SQL="SELECT name, setting, COALESCE(unit,''), source,
COALESCE(sourcefile,''), pending_restart FROM pg_settings WHERE name IN
('work_mem','maintenance_work_mem','autovacuum_work_mem',
 'max_parallel_workers_per_gather','temp_file_limit','log_temp_files') ORDER BY name"
PRIOR_SETTINGS="$(cat "${BACKUP_HOST}.settings")"
CONFIG_INODE="$(cat "${BACKUP_HOST}.inode")"

restore_prior_tuning() {
  test -f "$BACKUP_HOST" || return 1
  test "$(stat -c '%d:%i' "$CONFIG_HOST")" = "$CONFIG_INODE" || return 1
  cat "$BACKUP_HOST" > "$CONFIG_HOST" || return 1
  test "$(stat -c '%d:%i' "$CONFIG_HOST")" = "$CONFIG_INODE" || return 1
  test "$(db_psql -Atc 'SELECT count(*) FROM pg_file_settings WHERE error IS NOT NULL')" = 0 || return 1
  test "$(db_psql -Atc 'SELECT pg_reload_conf()')" = t || return 1
  local restored=no observed
  for attempt in $(seq 1 30); do
    observed="$(db_psql -At -F '|' -c "$SETTINGS_SQL")" || return 1
    if test "$observed" = "$PRIOR_SETTINGS"; then
      restored=yes
      break
    fi
    sleep 1
  done
  test "$restored" = yes || return 1
  test "$(db_psql -Atc 'SELECT count(*) FROM pg_file_settings WHERE error IS NOT NULL')" = 0 || return 1
  test "$(db_psql -Atc 'SELECT count(*) FROM pg_settings WHERE pending_restart')" = 0 || return 1
  db_psql -c "$SETTINGS_SQL" || return 1
}
restore_prior_tuning
```

Verify a fresh application-role connection and normal application operations
again. **Before releasing the deployment hold**, restore the previous defaults
in the tracked `deploy/postgres/postgresql.conf` and its assertions in
`deploy/deploy_config_test.go`, publish that reviewed rollback, and confirm every
checkout/artifact that can deploy the database uses it. A host-only rollback is
temporary: the next database deploy would otherwise reapply the tuned defaults.
Retain the backup and evidence until both live and tracked restoration are verified.

## Large index and maintenance failures

Lower memory can produce larger maintenance spills. Existing migrations with
smaller `maintenance_work_mem` overrides still inherit `temp_file_limit` unless
they override it themselves. A failed index sort can abort the migration and
prevent an application rollout. Do not edit already-applied historical migrations
to work around a new cluster default.

For a maintenance operation that legitimately needs more than the baseline cap,
size it first, approve free-space and memory headroom, and use a bounded
session-specific `temp_file_limit` for that operation. Use `SET LOCAL` within an
ordinary transactional maintenance operation; `CREATE INDEX CONCURRENTLY` must
run outside a transaction, so use a dedicated session with `SET`, then close it.
A global unlimited override is not a substitute for sizing.

A failed concurrent build can leave an invalid index. Inspect `pg_index.indisvalid`
and `indisready`, verify its definition and dependency ownership, and stop retries
that rely only on `IF NOT EXISTS`: that clause does not certify a valid index.
Follow an approved repair using `DROP INDEX CONCURRENTLY` and a correctly sized
rebuild, or the applicable `REINDEX INDEX CONCURRENTLY` procedure, outside a
transaction block. Do not drop a constraint-owned index blindly. Confirm validity
and the expected definition before resuming migrations. See PostgreSQL's
[concurrent index caveats](https://www.postgresql.org/docs/18/sql-createindex.html#SQL-CREATEINDEX-CONCURRENTLY)
and [REINDEX](https://www.postgresql.org/docs/18/sql-reindex.html).

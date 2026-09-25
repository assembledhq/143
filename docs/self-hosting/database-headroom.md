# Database headroom and safe rollout

The September 25, 2026 incident reached PostgreSQL allocation failures on an 8 GB host despite positive `MemAvailable`. Linux used strict overcommit accounting (`vm.overcommit_memory=2`, ratio 80), and `Committed_AS` approached `CommitLimit`. Old worker generations and blocked publication transactions overlapped. Their individual contributions were not measured; this does not establish an M3 memory leak.

The last incident snapshot also showed the root filesystem at 96% utilization. Memory tuning that increases temporary files must be preceded by disk capacity work. The numbers below describe that incident, not current production health.

## Prepared defaults

| Setting | Previous | Prepared | Effect |
| --- | --- | --- | --- |
| Worker/executor pool idle lifetime | pgx default 30 minutes | 5 minutes | Retires unused pool connections after bursts; acquired connections remain owned. |
| `work_mem` | 16 MB | 8 MB | Reduces the budget for each sort/hash operation; may increase temporary I/O. |
| `maintenance_work_mem` | 512 MB | 128 MB | Bounds ordinary maintenance operations. Migrations retain their own smaller override. |
| `autovacuum_work_mem` | inherited 512 MB | explicit 128 MB | Four workers have a combined configured allowance of 512 MB rather than 2 GB. This is an allowance, not measured resident savings. |
| `max_parallel_workers_per_gather` | 4 | 2 | Limits query parallelism and the multiplication of query memory. |
| `temp_file_limit` | unlimited | 2 GB per process | Cancels a transaction whose backend exceeds the temporary-file budget; concurrent backends can exceed this in aggregate. |
| `log_temp_files` | disabled | 16 MB | Logs completed temporary files at or above the threshold when they are removed. |

These settings do not bound total PostgreSQL memory. Operations, sessions and workers can allocate concurrently. See PostgreSQL's [resource configuration](https://www.postgresql.org/docs/18/runtime-config-resource.html). Pool idle expiry also does not release connections held by open transactions. Publication timeouts and durable worker draining address those separately.

The connection ceilings remain API 20, worker/executor 4 per process, and PostgreSQL 300. Reducing those requires concurrency and pool-wait measurements because review publication and lease renewal share the pool. Override the worker idle lifetime with `WORKER_DATABASE_MAX_CONN_IDLE_TIME`; new executor containers inherit the worker environment. Existing processes retain their startup configuration.

## Rollout order

1. Deploy the drain-intent and terminal-review recovery fixes first. Verify old generations are draining and no longer claim jobs, while their owned work can finish. Avoid overlapping fleet retries.
2. Roll one worker generation with the five-minute pool idle lifetime. Observe for at least 15 minutes with completed reviews, checking connection counts, pool acquisition waits, lease renewal, queue delay and publication errors. Continue across workers if those remain healthy. The API already uses five minutes.
3. Resolve disk headroom before reducing query memory. Inventory `df -h`, `df -i`, `docker system df -v`, all containers including stopped containers, volume mounts, and backup retention. The incident snapshot had about 67 GB of unattached volumes and 27 GB of backups, but neither category is automatically disposable. Identify owners, confirm snapshots/restores and retention, then approve a named removal list. Do not run blanket volume pruning. Aim for at least 20 GB free on the 150 GB filesystem and verify inode headroom.
4. Record a fresh database baseline: connection counts by state/application, blocking graph, idle transaction age, `Committed_AS`/`CommitLimit`, `MemAvailable`, PostgreSQL cgroup usage, `pg_stat_database.temp_bytes`/`temp_files` deltas, current temporary-file sizes, autovacuum progress and query/review latency. Keep timestamps and sample counts.
5. Stage the reviewed PostgreSQL config on the database host and use the reload procedure below. **Do not use a general database redeploy for this step**: it may recreate the container. Preserve the previous config for rollback, validate the staged file, and inspect `pg_file_settings` for errors before reload. Read back `pg_settings` afterward, including `source`, `sourcefile` and `pending_restart`; verify the effective settings from a new application connection. Role/database or session overrides can take precedence. All changed PostgreSQL settings here support reload; startup-only limits remain unchanged.
6. Observe a comparable workload window after reload. Sample `df` alongside `pg_stat_database` deltas and, with monitoring privileges, `SELECT COALESCE(sum(size),0) FROM pg_ls_tmpdir()` for current spills in the default tablespace. Inspect other temporary tablespaces if configured. `log_temp_files` reports files when removed, not live disk use. The 2 GB cap applies per backend process, including parallel workers, and excludes explicit temporary tables; it cannot guarantee a database-wide reserve. If aggregate temp-file growth threatens the disk reserve or queries start failing on the temporary-file limit, autovacuum falls behind, or query latency regresses materially, restore the previous config and reload. Do not count idle snapshots as proof of improvement. Compare connection peaks and commitment headroom during one controlled worker rollover as well as ordinary reviews.

## Reload without recreating the database container

Use an approved database maintenance shell; the read-only diagnostic role cannot reload configuration. Copy the reviewed file to `/opt/143/deploy/postgres/postgresql.conf.candidate` on the verified database host. The file contains server settings, not credentials. The commands below run **on that host**. Confirm there is exactly one matching database container and inspect the diff; proceed only for the six reviewed reloadable settings. Do not use `make deploy-db`.

```bash
set -euo pipefail
DB_CONTAINER="$(docker ps --filter label=com.docker.compose.service=postgres --format '{{.ID}}')"
test -n "$DB_CONTAINER"
test "$(printf '%s\n' "$DB_CONTAINER" | wc -l)" -eq 1
DB_CONFIG=/opt/143/deploy/postgres/postgresql.conf
DB_CANDIDATE=/opt/143/deploy/postgres/postgresql.conf.candidate
DB_BACKUP="${DB_CONFIG}.before-headroom-$(date -u +%Y%m%dT%H%M%SZ)"
diff -u "$DB_CONFIG" "$DB_CANDIDATE" || test "$?" -eq 1
# Stop here if the diff includes unrelated or startup-only changes.
cp -p "$DB_CONFIG" "$DB_BACKUP"
chmod 644 "$DB_CANDIDATE"
docker cp "$DB_CANDIDATE" "$DB_CONTAINER:/tmp/143-postgresql.conf.candidate"
DB_DATA_DIR="$(docker exec "$DB_CONTAINER" psql -X -U onefortythree -d onefortythree -Atc 'SHOW data_directory' < /dev/null)"
docker exec --user postgres "$DB_CONTAINER" postgres -D "$DB_DATA_DIR" -c config_file=/tmp/143-postgresql.conf.candidate -C work_mem < /dev/null
# Preserve the inode: this file is individually bind-mounted read-only.
# Renaming a replacement over it would leave the container on the old inode.
cat "$DB_CANDIDATE" > "$DB_CONFIG"
docker exec "$DB_CONTAINER" psql -X -v ON_ERROR_STOP=1 -U onefortythree -d onefortythree -c "SELECT sourcefile,sourceline,name,error FROM pg_file_settings WHERE error IS NOT NULL" < /dev/null
```

If copying/writing/validation fails or that query returns any rows, restore the backup **in place** using the rollback command below and investigate before reloading. After confirming zero errors, reload:

```bash
docker exec "$DB_CONTAINER" psql -X -v ON_ERROR_STOP=1 -U onefortythree -d onefortythree -c 'SELECT pg_reload_conf()' < /dev/null
docker exec "$DB_CONTAINER" psql -X -v ON_ERROR_STOP=1 -U onefortythree -d onefortythree -c "SELECT name,setting,unit,source,sourcefile,pending_restart FROM pg_settings WHERE name IN ('work_mem','maintenance_work_mem','autovacuum_work_mem','max_parallel_workers_per_gather','temp_file_limit','log_temp_files') ORDER BY name" < /dev/null
```

Reload is asynchronous: read back until all six settings match and `pending_restart=false`; check PostgreSQL logs and `pg_file_settings` again for errors. Expected settings in these units: `work_mem=8192 kB`, maintenance/autovacuum `131072 kB`, per-gather workers `2`, temp limit `2097152 kB`, temp logging `16384 kB`. Also verify a newly opened application connection because role/database/session overrides may differ. Keep the recorded backup path with the rollout notes. Roll back by writing to the same inode and reloading, then verify the previous settings:

```bash
cat "$DB_BACKUP" > "$DB_CONFIG"
docker exec "$DB_CONTAINER" psql -X -v ON_ERROR_STOP=1 -U onefortythree -d onefortythree -c 'SELECT pg_reload_conf()' < /dev/null
```

Temporary-file caps cancel queries; they are not a substitute for disk capacity. See PostgreSQL's [disk resource settings](https://www.postgresql.org/docs/18/runtime-config-resource.html#RUNTIME-CONFIG-RESOURCE-DISK) and [temporary-file logging](https://www.postgresql.org/docs/18/runtime-config-logging.html#GUC-LOG-TEMP-FILES).

## Resize plan

Prepare a move from 8 GB to at least 16 GB RAM. Confirm the provider's exact machine type, cost, resize downtime, disk behavior, and rollback constraints before execution. A larger host improves the strict commitment budget; do not simultaneously raise `shared_buffers`, connection ceilings, or worker concurrency. Keep the existing PostgreSQL container memory limit at 8 GB initially so the host gains reserve.

Treat a stop/start resize as a database outage. Stop new review admission through supported operational controls, let active executors and database-dependent work finish, and verify zero active leases before stopping PostgreSQL. Include other product workflows, not just code reviews. Take and verify a restorable backup off the affected root disk, save configuration, record the exact target and a maintenance window, and have a recovery plan before the resize. Confirm that storage growth is reversible or explicitly accept that it is not.

After startup, verify clean schema state, PostgreSQL recovery completion, application connectivity, worker heartbeats, leases and drain state. Run a canary review through actual GitHub delivery before restoring normal admission. Do not promise that a database restart preserves running reviews.

## Monitoring follow-up

Add host alerts for commitment above 85% (warning) / 95% (critical), filesystem use above 85% / 90%, and sustained low `MemAvailable`. Pair them with database connection utilization, blocked-session count, idle transaction age and allocation errors. Start with five-minute sustained windows for capacity signals; page immediately on allocation failures. Thresholds need tuning against workload history. These alerts and the resize are operational follow-ups, not installed by this configuration change.

Success means reviews complete and publish, worker rollover remains healthy, and measured memory/disk reserves survive a comparable load window. This change alone does not establish a production speedup or resolve the host capacity problem.

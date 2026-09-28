# Database backup monitoring proposal

**Status: proposed integration.** The runtime supervisor and local health
collector exist. Scheduled collection, central delivery and alert routing
described here are not installed by the backup helpers. This proposal describes
software behavior; it does not report the state of any hosted deployment.

## Proposed monitoring integration

Run `pg_backup_health.py` once per minute on the backup host. It reads local receipts,
watchdog records and filesystem capacity without taking the backup lock. It
publishes one small JSON event directly to a configured private VictoriaLogs
HTTP ingestion endpoint, with a five-second timeout. It does not need a Docker
socket, new container, database password, S3 credential, or new public listener.
Read only the literal `VICTORIALOGS_HOST` value from the installed host configuration;
validate it as a private IPv4 address. Bound the event size and disable HTTP
redirects and ambient proxies. Emit local errors if delivery fails. The central
missing-heartbeat rule must detect both a dead collector and failed delivery.

The installer would copy the new helpers and render the minute health entry
independently of the two existing holds. It would save health output in a private
log with bounded rotation. It must preserve both existing hold values and never
start a backup or restore as an installation side effect. The backup host
needs its private logging destination supplied explicitly at installation.
Keep destinations and credentials in deployment configuration.

Extend the repository-owned vmalert rules and the deployment's Alertmanager
routes for missing/invalid telemetry, failed attempts (including admission and
lock failures), stale watchdogs, capacity pressure, six-hour recovery-point
freshness, and overdue independent full restores. Freshness is based on dump
start, conservatively before snapshot acquisition. Allow one 45-minute dump
budget before paging for a six-hour-target miss. A held schedule still ages.

Keep unattended execution disabled until a real owned-stop exercise, a complete
guarded backup, independent collector delivery and a controlled alert test have
passed. Installation does not establish delivery or recovery guarantees.
Restore drills require isolated capacity; keep the production restore hold.

## Privilege boundary

Receipt-based collection needs filesystem access to the private backup state.
It does not need the Docker socket or database and object-storage credentials.
Keep transport separate from backup execution so a collector or network failure
cannot resume a held job. Installing collection must preserve the existing
backup and restore schedule settings. No database schema or application API
change is proposed.

## Validation and rollback

Test destination validation, proxy/redirect refusal, bounded transport failure,
missing-heartbeat queries and health calculations. Installer tests must prove
both holds survive and health continues independently. A production rollout must
read back helper hashes and cron, observe a fresh central heartbeat, demonstrate
the test alert arriving, and recheck capacity before any writer exercise.

Rollback restores saved helpers/cron and retains both holds. Do not delete any
archives, pending markers, receipts or uncertain owned containers as rollback.

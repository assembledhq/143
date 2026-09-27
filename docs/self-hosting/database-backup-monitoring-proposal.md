# Database backup monitoring rollout proposal

This is a source-code proposal. Nothing in this document authorizes installation,
schedule resumption, a test notification, or a database restart.

The runtime supervisor and independent health collector are being implemented in
`codex/database-backup-runtime-protection`. Both production schedules remain held.

## Proposed monitoring integration

Use the host's Python 3 health collector once per minute. It reads local receipts,
watchdog records and filesystem capacity without taking the backup lock. It
publishes one small JSON event directly to the existing private VictoriaLogs
HTTP ingestion endpoint, with a five-second timeout. It does not need a Docker
socket, new container, database password, S3 credential, or new public listener.
Read only the literal `VICTORIALOGS_HOST` value from the installed host configuration;
validate it as a private IPv4 address. Bound the event size and disable HTTP
redirects and ambient proxies. Emit local errors if delivery fails. The central
missing-heartbeat rule must detect both a dead collector and failed delivery.

The installer would copy the new helpers and render the minute health entry
independently of the two existing holds. It would save health output in a private
log with bounded rotation. It must preserve both existing hold values and never
start a backup or restore as an installation side effect. The database host
needs its existing private logging destination supplied explicitly at installation;
do not silently change the encrypted production configuration.

Extend the existing repository-owned vmalert rules and approved Alertmanager
routes for missing/invalid telemetry, failed attempts (including admission and
lock failures), stale watchdogs, capacity pressure, six-hour recovery-point
freshness, and overdue independent full restores. Freshness is based on dump
start, conservatively before snapshot acquisition. Allow one 45-minute dump
budget before paging for a six-hour-target miss. A held schedule still ages.

Keep unattended execution disabled until a real owned-stop exercise, a complete
guarded backup, independent collector delivery and a controlled alert test have
passed and their evidence has been reviewed. Installing code alone cannot open
that gate. Keep the restore hold: a full isolated restore is separate work.

## Approval boundary

Automatic approval review rejected the first source patch because it would have
added Vector with a Docker socket to the database host and changed persistent
scheduling. This revised proposal removes the new Docker privilege boundary.
Approval requested now covers preparing and testing these source changes for
Fable review. Installation and the controlled alert test remain subsequent,
concrete operational decisions.

## Validation and rollback

Test destination validation, proxy/redirect refusal, bounded transport failure,
missing-heartbeat queries and health calculations. Installer tests must prove
both holds survive and health continues independently. A production rollout must
read back helper hashes and cron, observe a fresh central heartbeat, demonstrate
an approved alert arriving, and recheck capacity before any writer exercise.

Rollback restores saved helpers/cron and retains both holds. Do not delete any
archives, pending markers, receipts or uncertain owned containers as rollback.

# Database backup monitoring

Backup monitoring is a separate, opt-in installation. It does not enable backup
or restore schedules, remove the attended-run requirement, or prove that a full
restore works. The backup installer alone does not install this integration.

## Collection and transport

`pg_backup_monitor.py` reads the current backup cron settings and local health
evidence once a minute, without taking the backup lock or changing receipts.
It sends one scalar JSON event to the private VictoriaLogs JSON ingestion API.
The payload contains a configured monitor identity, hold states, capacity,
snapshot-based recovery age and failure indicators. It excludes archive names,
raw errors, attempt details and credentials.

The collector does not access Docker, the database, S3, or the host secret bundle.
Its private JSON configuration contains only `logging_host` and `monitor_id`.
Only literal RFC1918 IPv4 destinations on port 9428 are accepted. There is no DNS,
proxy use, redirect following, retry, or response-body download. Collection has a
10-second deadline and HTTP delivery has a five-second total deadline. Cron also
enforces a 25-second process limit, with a five-second kill grace. These budgets
finish before the next minute invocation. The private delivery log rotates daily,
with seven compressed rotations. A collection error produces a failure event;
an absent collector or failed delivery is detected centrally.

VictoriaLogs assigns ingestion time to events; host clock skew cannot make an
absent sender look fresh. `observed_at` records the host clock for diagnosis.
Current host settings are read as literal data on every run, never sourced as
shell code. Missing or ambiguous schedule fields fail visibly. Holds do not
silence capacity, recovery or restore alerts.

## Installation

Keep actual destinations, identities, rollback copies and execution evidence in
your private operations repository. Before changing a host, save the current
helper files and any existing monitoring config, health cron and logrotate file.
Record the existing backup cron hash and both hold values.

Copy these files from one reviewed source revision into the root-owned scripts
directory on the backup host:

- `pg_backup_monitor.py`
- `install-pg-backup-monitoring.py`
- `pg_backup_health.py` and `pg_backup_state.py` from the same revision

With Python 3, GNU `timeout`, cron and logrotate installed, run as root:

```sh
python3 /opt/143/deploy/scripts/install-pg-backup-monitoring.py \
  --logging-host '<private-logging-ip>' --monitor-id primary-db
```

This creates `/opt/143/backup-monitoring.json` (0600),
`/etc/cron.d/143-pg-backup-health`, `/var/log/pg-backup-health.log` (0600), and
`/etc/logrotate.d/143-pg-backup-health`. It never writes
`/etc/cron.d/143-pg-backup`, runs a backup/restore, or sends a test notification.
Cron begins independent health collection on its next minute tick. Read back
hashes and verify the backup cron and holds are unchanged.

On the logging deployment, persist `BACKUP_MONITOR_ID=primary-db` in its private
configuration and deploy the logging compose/rules update. The ID must match the
collector, begin with a lowercase letter, and contain at most 63 lowercase
letters, digits, underscores or hyphens. `disabled` is reserved and is the default:
no backup alerts fire until this explicit activation. The supplied rules expect
one backup host per logging deployment; multiple hosts need separate expected-ID
rules so one host cannot hide another's outage.

Existing Alertmanager warning and critical routes handle these alerts. Both
`GRAFANA_ALERTS_WARNING_WEBHOOK_URL` and `GRAFANA_ALERTS_CRITICAL_WEBHOOK_URL` must
be real destinations: the default disabled relay destinations discard alerts.
Persist settings in the deployment source; a host-only `.env` edit can be lost
on the next deployment.

## Alerts and acceptance

The rules in `deploy/vmalert/rules/database-backup.yml` cover:

| Condition | Severity | Evaluation delay after the condition |
| --- | --- | --- |
| No expected heartbeat in five minutes, including a collector that never started | Critical | 1 minute |
| Invalid health evidence or failed collection | Critical | 1 minute |
| Latest backup/resume attempt failed, including admission or lock refusal | Warning | 1 minute |
| Stale/failed owned watchdog | Critical | 1 minute |
| Recovery point older than the configured target plus a 45-minute dump budget, or no qualified copy | Critical | 1 minute |
| Capacity below the next admission budget | Warning | 5 minutes |
| Free disk approaching the reserved floor | Critical | 1 minute |
| Independent full restore overdue | Warning | 15 minutes |
| Swap above 256 MiB | Warning | 5 minutes |
| Backup schedule held | Warning | 15 minutes |

Evaluation runs once per minute and delivery adds Alertmanager grouping delay.
State alerts use the latest report, so a healthy report resolves prior failures.
Capacity includes space before normal verified retention; three retained copies
can therefore produce a warning even when an attended retention step would make
room. Recovery age always uses dump start, not import/upload completion.
`BACKUP_RECOVERY_TARGET_HOURS` in the backup cron file controls this objective
(default 6; integer 1–24). The collector emits `recovery_target_seconds` alongside
actual snapshot age. Local health marks the objective missed at that age; central
alerts retain the additional 45-minute dump budget. Missing or invalid receipts
still alert, and malformed target settings report telemetry failure. Match this
setting to the approved schedule without changing snapshot timestamps.

Before relying on this integration, confirm current central events for the exact
expected ID. In an approved window, demonstrate collector/delivery failure,
restore reporting afterward, and confirm that both warning and critical alerts
arrive at the intended destination and resolve. Use a separate test identity for
synthetic state events; do not make a production backup look healthy with test
data. The collector's HTTP success alone is not proof of queryability or alert
delivery. Keep existing operator checks until this validation is complete.

Local regression checks:

```sh
bash deploy/scripts/pg_backup_test.sh
python3 deploy/scripts/pg_backup_alert_integration_test.py \
  --binary /path/to/victoria-logs-prod --vmalert-binary /path/to/vmalert-prod
```

The second command requires the same official pinned binaries as the logging
compose file. It uses only localhost and synthetic data, tests the real stats API
for missing/disabled/failed/recovered states, and exercises firing/resolved
notifications into a local test receiver with the real HTTP sender. Only test
evaluation intervals are accelerated. It removes its temporary store and does
not establish production alert delivery. Without `--vmalert-binary`, it checks
only LogsQL behavior; this integration test is separate from the unit suite.

## Rollback

Remove the independent health cron or restore its saved version, then restore
saved monitoring helpers/configuration and logrotate settings. Set the logging
deployment's expected ID back to `disabled` only as an explicit monitoring
rollback; otherwise the missing-telemetry alert should fire. Retain delivery logs
and backup evidence. Leave the backup/restore cron, archives, receipts, pending
markers and uncertain owned containers unchanged.

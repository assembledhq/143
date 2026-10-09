# Database backups and recovery

This guide describes the backup scripts shipped with 143. Validate them on your
own deployment before relying on them for recovery. Keep deployment inventories,
incident timelines, verification receipts and access details in your private
operations records; examples here use repository defaults and placeholders.

## Schedule controls

Use `BACKUP_ENABLED=false` to hold scheduled full backups and
`RESTORE_TEST_ENABLED=false` to defer the weekly restore drill. Both default to
`true` on first installation. These are operational holds: a backup hold creates
a recovery-point gap, and a deferred restore has not verified recovery.

Store the canonical values in your private deployment configuration.
`make provision-db-backups` reads both values from that configuration; the full
`provision.sh db ...` path exports and forwards them too. An explicit environment
value overrides the private value. This provisioning command also copies backup
scripts and may update offsite configuration; it is not a cron-only command.

The installer writes literal boolean fields and comments out the held cron
entries. When an override is omitted, it preserves the installed boolean instead
of silently enabling the job. To resume, explicitly set the relevant value to
`true` only after fresh capacity and recovery checks. Invalid or duplicate
installed fields fail before the cron file is replaced. A deliberate explicit
boolean can repair an invalid installed value.

An operator with root access who only needs to update cron can run the
already-installed `install-pg-backups.sh` with the chosen booleans, without
running the full provisioning path. Save the current cron, ensure no dump/restore
is active, and verify the resulting entries and script hashes. The installer
atomically replaces `/etc/cron.d/143-pg-backup`; no Docker or PostgreSQL restart
is required. Old installer versions do not recognize these settings, so do not
allow an old provisioning checkout to overwrite a hold.

The backup and restore entry points also reject an explicit `false` environment
value before touching archives or Docker, exiting with status 75. Direct manual
invocations do not automatically inherit cron's environment: pass the setting
explicitly and inspect the installed cron before running anything. An absent
value permits a direct manual invocation for backward compatibility; scheduled
mode requires an explicit `BACKUP_ENABLED=true`.

Record the operator, reason, held schedule, latest verified recovery point, next
reassessment deadline, and rollback contents. Confirm alerting or an explicit
operator follow-up; commented cron entries will not themselves produce alerts.
The schedule switches alone do not prove backup health. The policy described
below adds retention/admission/serialization and a dump supervisor. Production
validation of that supervisor, alert delivery, and independent offsite restoration
remain separate operational gates.

Validation uses mocked transports and temporary files:

```sh
bash deploy/scripts/install_pg_backups_test.sh
bash deploy/scripts/provision_db_backups_test.sh
bash deploy/scripts/pg_backup_test.sh
bash deploy/scripts/restore_test_test.sh
```

## Backup admission and retention

`pg-backup.sh` and `restore-test.sh` call the Python 3 standard-library policy in
`deploy/scripts/pg-backup-policy.py`. Install all helpers from the same revision.
`restore-test-body.sh` is an internal reader retaining the owned-container cleanup;
do not invoke it directly. A common bounded file lock covers backups, receipt
imports, pruning, pin updates, and the entire local restore reader/cleanup.
Lock timeout is a nonzero, observable result, not a successful backup.

Each normal backup retains **two completed local archives in total**: the newest
qualified copy and the last fully restored known-good copy. If no distinct
known-good copy is pinned, it keeps the two newest qualified copies. A pin uses
one of these two slots. An old archive is not deleted merely because of its age.
The old `BACKUP_RETENTION_DAYS` setting is no longer used.
After a successful archive deletion, its receipt moves to `.backup-state/retired/`
for audit instead of remaining in the active receipt directory.

Pruning requires private per-file receipts in `BACKUP_DIR/.backup-state`, a
matching local inode/size/mtime/ctime identity, and a fresh exact S3 key, size,
ETag, and last-modified match for every local archive. The policy refuses all
pruning/admission if any archive is unknown, changed, or has invalid evidence,
or if listing fails. Unknown files consume capacity; they do not count as
qualified recovery copies. Existing archives are never automatically re-uploaded
to create evidence. No remote deletion is implemented.

A new backup is individually uploaded using the pinned AWS CLI 2.35.11 image and
explicit `--checksum-algorithm CRC64NVME`. Only successful uploads followed by a
matching listing receive upload receipts. A directory-wide sync cannot qualify
skipped files. [AWS documents upload checksum validation](https://docs.aws.amazon.com/cli/latest/topic/s3-faq.html);
the pinned image still needs an attended canary before pruning is enabled.
Upload-integrity evidence is not independent download or full-restore proof.

Before creating a dump, the policy uses the largest retained archive, increases
its estimate for database growth since receipt measurement, adds a 25% margin,
and requires that estimate **plus 20 GiB free reserve**. Empty bootstraps estimate
from the full database size. This is preflight admission, not a hard dump-size
limit. `BACKUP_RESERVE_BYTES` can increase the reserve but cannot lower it below
20 GiB. `BACKUP_LOCK_TIMEOUT_SECONDS` defaults to 60 seconds, maximum 3600.

Manual backup, prune, resume-upload and local restore commands require
both `BACKUP_ATTENDED=true` and a nonempty
`BACKUP_OBSERVER` identifying the operator. They report JSON events and nonzero
failures to that operator's terminal. Do not set these variables in cron to
simulate an observer. The observer must watch disk and memory and have an
ownership-checked stop procedure. Scheduled backups use a separately approved
trial or ongoing profile as described below; installing helpers does not validate alert delivery.
Keep schedules held while validating a deployment and assign an operator to
arrange attended backups at the configured target cadence (six hours by default). If that cannot be met,
record the recovery-point gap and next reassessment time. Installing helpers
does not establish backup coverage.

## Scheduled backups

The backup installer renders `pg-backup.sh --scheduled` for cron. It preserves
the existing backup and restore holds. Scheduled execution requires both
`BACKUP_ENABLED=true` and a private approval profile at
`$BACKUP_DIR/.backup-state/scheduled-backup.json`. Code installation and cron
refresh do not create, modify or renew this file.

`BACKUP_CRON` sets the five-field schedule in the **host's cron timezone**
(default `0 */6 * * *`). `BACKUP_RECOVERY_TARGET_HOURS` sets the snapshot-age
objective, an integer from 1 to 24 (default 6). Choose both together: changing
cron does not automatically change the recovery objective. Persist them in
private deployment configuration; provisioning forwards them and the installer
preserves installed values when an override is omitted. Neither setting renews
the approval window or changes resource guards. For a bounded window, convert
local start times to the host timezone and check daylight-saving transitions.
A missed start extends the recovery gap until another backup succeeds.

An operator must separately approve and atomically install a profile owned by
the account running the backup (root for the installed cron), with mode 0600
inside the existing private state directory. For a time-limited trial, the profile has this shape; replace the placeholders
with the approved owner, evidence reference and timezone-aware ISO-8601 timestamps:

```json
{
  "schema": 1,
  "owner": "<responsible operator>",
  "evidence": "<private rollout acceptance record>",
  "starts_at": "<approved start with timezone>",
  "expires_at": "<approved expiry with timezone>",
  "host_memory_full_percent": 1,
  "db_memory_full_percent": 1
}
```

The start is inclusive, expiry exclusive, and the window cannot exceed 24 hours.
Missing, unsafe, malformed, future or expired profiles refuse new work. The
policy validates the window after acquiring the existing writer lock, before
retention or backup work. Once an operation is admitted, expiry does not cancel
its verification, upload, retention or cleanup. A subsequent invocation refuses
the expired window. Each admitted scheduled attempt records the approved profile;
failed starts record durable failures. Health reports `backup_held=true` for an
unavailable window even when the cron enable flag remains true, without hiding
stale recovery points or overdue restores. Expiry does not rewrite cron.

### Ongoing operation

After accepting a trial, an operator can explicitly approve an ongoing schedule.
This uses a separate schema, so deleting a trial expiry or changing its schema
number alone cannot remove the trial limit:

```json
{
  "schema": 2,
  "mode": "ongoing",
  "owner": "<responsible operator>",
  "evidence": "<private ongoing-operation acceptance record>",
  "starts_at": "<approved start with timezone>",
  "timezone": "America/New_York",
  "hours": [8, 20],
  "host_memory_full_percent": 1,
  "db_memory_full_percent": 1
}
```

Set `BACKUP_CRON="0 * * * *"` for an hourly tick at minute zero; the validated
profile selects the eligible local hours using the host's IANA timezone data.
The start timestamp must have arrived, and `BACKUP_ENABLED=true` is still
required. The hours must be sorted, unique integers from 0 through 23. Ongoing
profiles have no expiry. Stop new starts with `BACKUP_ENABLED=false` or by
removing the approval profile. Both controls stop future starts without
interrupting admitted work or cleanup. Install the shared helper and policy before the ongoing profile, and
install the hourly cron last. Installers never promote trials automatically.

Off-hour ticks exit without a new attempt, database access, storage work or
clearing an earlier failed attempt. The profile and eligible hour are checked
again under the common writer lock before any backup or retention work. Invalid
profiles retain durable failed-attempt reporting. Health remains active between
eligible hours while still reporting stale recovery points and overdue restores.

Choose local hours outside daylight-saving transitions to avoid skipped or
repeated clock hours. For example, 8 AM/8 PM remains at those Eastern clock times
in summer and winter, with an 11- or 13-hour overnight interval on transition
weekends. The configured recovery objective and its 45-minute alert allowance
remain unchanged; a 13-hour interval can therefore briefly exceed a 12-hour
objective plus its allowance.

Scheduled mode uses normal two-copy receipt-qualified retention. It cannot
bootstrap, act as a canary, run a stop exercise, opt into higher swap limits,
restore, or retry an ambiguous upload. Standalone prune, upload recovery and
restore remain attended commands. Common locking, strict admission, runtime
resource guards, owned cleanup, checksum qualification and preservation of
uncertain archives are unchanged. Expiry limits the start window, not the number
of invocations; select the cron cadence and window together for the intended trial.

Each profile's host and database memory-full PSI limit must be exactly 1% or 5%.
Higher values accept more stalls and possible application latency; use evidence
from an attended run when approving them. Admission remains at 1% for both,
and scheduled swap limits remain 256 MiB total and 4 MiB/s. CLI pressure and
swap overrides cannot be combined with `--scheduled`. Effective limits are
logged after loading the profile and retained with runtime observations.

Before enabling a window, verify runtime protection and independent monitoring,
save rollback copies, and identify the operator who will inspect each outcome.
If external notifications or a full isolated restore are deliberately deferred,
record those gaps and their reassessment in the private acceptance record.
A profile is operational authorization, not proof that delivery or restoration
passed. A backup can stop safely without notifying a human. Observe completed
cycles, receipt integrity, retained copies and capacity before approving another
window; there is no automatic renewal or indefinite scheduling mode.

## Runtime protection and rollout

The dump runs in a dedicated, named client container using the running database's
immutable image ID and network namespace. It connects through the database's
existing Docker-network address. The database's HBA rules must permit this
connection; loopback TCP access is not assumed.
There is no production data-volume mount or database restart. The client mounts
only its private partial archive and has a 2 GiB memory limit, no swap allowance,
one CPU, 32-process limit, read-only root filesystem, dropped capabilities and
a hard file-size limit. Image-declared data volumes are masked by small tmpfs
mounts instead of creating anonymous restore volumes.
The higher client limit leaves room for large COPY rows and libpq buffering;
the attended canary must measure actual usage. Heartbeats and terminal results
record `memory.current` and the kernel's `memory.peak`, with the observation time,
plus anonymous memory, file cache and swap separately. A peak at the cgroup cap
can reflect reclaimable file cache and does not by itself imply an OOM.
This is the peak through the last live sample, not a claim to have measured a
spike between that sample and process exit. Host commitment and memory guards
remain in force independently of the client limit.

A detached watchdog inherits the common backup lock. It samples disk, available
memory, commitment headroom, swap activity, database cgroup headroom and host/DB
pressure while the dump is active. It stops on missing telemetry, a changed
database generation, a disappeared caller, a cancellation request, a 45-minute
dump timeout or unsafe resources. The caller watches the watchdog in turn.
Killing the calling SSH session does not release the lock while its watchdog
is still supervising or cleaning up. A host failure or simultaneous death of
both processes is not claimed safe: hard limits bound the writer, and a pending
marker prevents a second backup until an operator reconciles ownership.

After the dump stops, a second detached watchdog protects structural verification,
SHA-256, upload and the confirming S3 listing with caller/heartbeat checks.
Active verification/hash/upload use the dump's resource thresholds. Structural
verification uses a separate PostgreSQL
client container with no network or live data volume. Upload and listing use
the pinned AWS image on the bridge network. Each reader has an exact recorded
container ID and run label, a 1 GiB memory limit with no container swap, one CPU,
64 PIDs, a read-only root filesystem, 64 MiB writable `/tmp` and no capabilities. Only the small S3
listing retains bounded Docker logs; TOC output and upload diagnostics are not
stored. The limits require an attended production canary before acceptance.

Uploads use a private, credential-free AWS config mounted read-only into the
reader: the classic transfer client, two concurrent requests, 16 MiB multipart
chunks and a 100 MiB/s bandwidth cap. Selecting classic explicitly matters:
[the CRT client ignores concurrency and bandwidth settings](https://docs.aws.amazon.com/cli/latest/topic/s3-config.html#preferred-transfer-client).
These settings pace reads; they do not guarantee that the host's pressure limits
will pass. The 1 GiB reader limit and all host/database stop thresholds remain.
Post-dump telemetry includes reader memory/IO pressure and memory events, sampled
before checking host thresholds. `postdump/samples.json` retains the latest 240
samples, including the violating sample, separately from cleanup heartbeats.

Hashing reads 8 MiB chunks in the supervised process, uses Linux
`POSIX_FADV_DONTNEED` before reading and behind each chunk to release clean file
cache, and checks cancellation and resources between reads. Cache advice is not
a kernel-enforced memory cap; resource monitoring still applies. Verification
and hashing honor `BACKUP_VERIFY_TIMEOUT_SECONDS`; upload has a two-hour limit,
and the listing has a two-minute limit. Reader polling follows the five-second
sampling interval. Capacity is resampled after Docker creation and before start;
time spent in bounded setup/cleanup commands is not treated as a missed active
reader sample. The external watchdog detects a hung
hash/read even when the hashing process cannot emit its next heartbeat.

Post-dump failure cleanup stops only the owned reader and preserves the completed
dump, pending marker and earlier archives. It never cancels a database backend
or discards a completed dump. Uploader exit status is recorded before container
removal. A zero exit and matching listing are both required for a receipt;
an uncertain upload is never automatically retried or qualified from size alone.
Completion samples remain in the receipt even if they show residual swap or
pressure. They do not invalidate an already proven successful reader exit.
The small confirming S3 listing still checks free disk, available memory,
commitment and database cgroup headroom, but can run despite residual swap/PSI.
Caller identity, cancellation, deadlines, database generation and telemetry
availability remain enforced. A completed backup with recorded pressure is
usable recovery evidence, not a successful resource-behavior canary.
If the DB generation changes after dumping, the archive is preserved for
reconciliation: this conservative guard cannot follow its old cgroup identity
across a restart, even though the completed dump itself is independent of it.

Admission requires the estimated dump plus the configured reserve plus **5 GiB**,
at least 3 GiB available memory and commitment headroom, and 1.5 GiB database
cgroup headroom. Runtime stopping begins at reserve plus **4 GiB**, 1.5 GiB
available memory, 1 GiB commitment headroom, 0.5 GiB database cgroup headroom,
256 MiB swap use, or 4 MiB/s swap-out. Pressure thresholds are explicit in
`pg_backup_runtime.py`. The default reserve remains 20 GiB and can only increase.

An explicitly attended `backup --canary --host-memory-full-percent 5` permits
host memory **full PSI avg10 up to 5%** during the dump, verification and upload.
The default is 1%; admission still requires 1% or less. This invocation-only
option requires `--canary`, `BACKUP_ATTENDED=true` and a named `BACKUP_OBSERVER`.
It does not alter cron or persist a new default. Database pressure, available
memory, commitment, swap, disk and timeout limits remain unchanged. Observations
record the effective host pressure limit before enforcement, including any
triggering sample; a null limit means that observation did not enforce PSI
(for example, the small metadata listing described above).

The higher limit accepts more memory stalls and can increase application latency;
5% is an operator-selected ceiling, not a demonstrated safe latency budget or a
guarantee that a backup will finish. Use this CLI option only for an attended
canary after reviewing current workload and capacity, keeping the existing stop
controls. A scheduled window selects its limits through its approved profile.

`--db-memory-full-percent 5` independently permits database memory full PSI
avg10 up to 5% under the same attended-canary requirements. Its default and
admission limit remain 1%; database memory **some** PSI remains capped at 10%.
This accepts more stalls affecting database work. The effective database limit
is recorded alongside the host limit, and actual memory headroom, swap, disk
and time limits remain unchanged. Both options must be supplied explicitly to
relax both stall thresholds for a single invocation.

`--allow-swap-bursts` permits up to **512 MiB total host swap and 8 MiB/s
swap-out** during an attended canary's dump, verification and upload. It requires
`--canary`, `BACKUP_ATTENDED=true` and a named `BACKUP_OBSERVER`. Admission and
subsequent invocations retain the default 256 MiB and 4 MiB/s limits. Each
resource observation records the effective byte and byte-per-second ceilings;
metadata-only checks record null ceilings because they enforce capacity only.
The option accepts additional paging and possible application latency, while
preserving memory headroom, disk, PSI, IO, timeout and cleanup controls. It does
not change cron or persist a new policy.

Samples normally run five seconds apart. A sample interval exceeding 30 seconds
fails closed; the caller detects a stuck monitoring watchdog after 45 seconds.
Cancellation or an explicit cleanup heartbeat starts a separate 120-second
cleanup budget; each bounded stop stage emits progress. The parent preserves a
terminal result if completion races with its deadline. The common lock stays
held through cleanup, so a competing invocation can report a lock timeout.
Budget at least 150 seconds for a watchdog stalled at cleanup entry followed
by caller takeover and client stopping: the 4 GiB margin tolerates roughly
27 MiB/s of unrelated growth over that interval. This is a capacity assumption to validate, not a
guarantee against unbounded concurrent writes or an unresponsive Docker daemon.
Use separate storage or a larger validated reserve if that margin is inadequate.

Only the exact labelled client ID and captured PostgreSQL backend identity
(PID, backend-start time, application, database and role) may be stopped. Cleanup
must prove that both client and backend are gone before unlinking the exact
partial inode and its matching dump marker. Ambiguous Docker creation, changed
identities, failed queries, changed database generations, or failed cleanup keep
the marker and partial for investigation. An ambiguous create may retain its
partial even after its labelled client has been safely removed. Completed archives are not failure
cleanup targets. A proven failed dump clears its marker but records failure.

Per-run ownership, watchdog heartbeat and result files are private under
`.backup-state/143-backup-<uuid>/`. Separate attempt records include admission and
lock failures. Mutating policy operations retire older finished attempts under
the common lock, retaining the newest 200, every unfinished attempt, and the
latest result for each operation in the hot directory. Retired attempts remain
under `.backup-state/retired/attempts/`; no audit evidence is deleted. Operator
helpers that validate exact state-directory contents must allow these records
and `health.json` and `recovery.json` rather than reusing a historical directory
snapshot. The recovery pointer is retained as evidence after success; health
ignores it when its pending marker is absent, and a later recovery replaces it.
Post-dump ownership, heartbeat, result and per-reader exit evidence live in that
run's `postdump/` subdirectory. Health checks inspect this watchdog during
verification and upload instead of treating successful dump cleanup as overall
completion.
New receipts preserve dump start, completion, structural check,
local SHA-256, upload and integrity-verification timestamps; a full restore starts
as `not_verified`. An imported independent receipt preserves an existing timeline.
The dump-start timestamp is a conservative lower bound before snapshot acquisition,
not a claim to the exact PostgreSQL snapshot time.
They also retain resource observations at verification/hash/upload boundaries,
including database cgroup memory and swap. These are sampled observations, not
continuous maxima. Health reports current host swap and a `swap_high` flag above
256 MiB, along with the newest receipt's resource evidence.

The independent `pg_backup_health.py` computes freshness from that lower bound,
or the original timestamp in a recognized legacy filename. Receipt-import and
upload times never reset recovery-point age. It reads atomic evidence without
taking the common lock and emits JSON for failure, stale telemetry, reserve
pressure, missed configured recovery targets and overdue full restore evidence.
It has no database, Docker or S3 access. Scheduled collection and central alerts
have a separate opt-in [monitoring installation](database-backup-monitoring.md).
The backup installer alone does not enable them. Keep operator checks and both
schedule holds until the deployment's acceptance gates pass.

Before approving a scheduled window, save installation rollback copies,
run a controlled owned-stop exercise and a complete attended backup, and verify
independent heartbeat/alert delivery through your monitoring destination, or
explicitly record deferred delivery as a rollout risk. This
explicitly induced failure runs only as an attended canary and skips pruning:

```sh
BACKUP_ENABLED=true BACKUP_ATTENDED=true BACKUP_OBSERVER='named operator' \
  /opt/143/deploy/scripts/pg-backup.sh --canary --exercise-stop-after 30
```

Expect failure, a terminal result with `cleanup_verified=true`, no remaining
owned client/backend, identical prior archives, and no owned partial/marker.
Inspect these independently; a nonzero command exit alone is not proof. Do not
run this command or clear uncertain markers merely because code/tests passed.
Full recovery verification requires an isolated restore host. The restore helper
refuses a Docker daemon containing the configured production database container.


## Data-only offsite configuration

Provisioning writes `/opt/143/backup-storage.json` atomically with root ownership
and mode 0600 from the existing `BACKUP_S3_BUCKET`, `BACKUP_S3_REGION`,
`BACKUP_AWS_ACCESS_KEY_ID`, and `BACKUP_AWS_SECRET_ACCESS_KEY` settings. It validates
all four settings before copying scripts. The backup reads JSON as data and passes
credentials to the AWS CLI container through environment variables, never command
arguments. The old executable `backup-sync.env` is left untouched for rollback
but is never sourced or evaluated by this policy. Missing JSON configuration
fails closed. No additional IAM permission is required beyond listing and upload.
After the rollback window, remove the old executable configuration as a separate
maintenance action once it is no longer needed.

Keep both schedule holds while installing or upgrading the helpers. Verify helper hashes,
Python 3 availability, JSON configuration permissions, and the continued presence
of the previous owned-container restore cleanup. The provisioning wrapper also
updates scripts and storage configuration; review all of those changes before running it.
Do not use an old checkout's provisioning command because it can replace held cron.
This installer omits the obsolete retention-days field, so even a held cron file
will have a new hash. Record the new bytes/hash at installation; do not reuse
one-time incident helpers whose preconditions pin the previous cron hash.

## Qualify existing copies and run a canary

First inventory every completed local archive. Independently stream each exact
offsite key/version through SHA-256 using your restore identity on separate
capacity. Record unchanged size, version ID, ETag, last-modified, and full-file hash.
Then import its receipt on the database host:

```sh
python3 /opt/143/deploy/scripts/pg-backup-policy.py import-receipt \
  --file onefortythree-YYYYMMDD-HHMMSS.dump \
  --sha256 FULL_FILE_SHA256 --version-id S3_VERSION_ID \
  --etag '"EXACT_ETAG"' --last-modified EXACT_LISTING_TIMESTAMP \
  --evidence 'Private change record identifying the independent verification'
```

This command performs its own local full-file hash and structural check and
fresh metadata comparisons. It never downloads or uploads a legacy object.
The version ID is an **operator attestation**, not an API-verified value: the
writer's ListObjectsV2 permission does not expose version IDs. The receipt's
`database_bytes` is measured from the current database at import, not at the
legacy dump's creation time. Structural verification defaults to a two-hour
timeout for large archives; `BACKUP_VERIFY_TIMEOUT_SECONDS` may set 60–86400
seconds. A timeout retains the pending marker for reader reconciliation.
Import receipts for **every** local completed copy; normal admission also
requires at least two qualified copies. `plan` checks the inventory and current
admission without pruning or creating a dump:

```sh
python3 /opt/143/deploy/scripts/pg-backup-policy.py plan
```

For an attended canary, leave cron held and override only
this invocation. The first canary skips both pre/post pruning and preserves all
existing copies:

```sh
BACKUP_ENABLED=true BACKUP_ATTENDED=true BACKUP_OBSERVER='named operator' \
  /opt/143/deploy/scripts/pg-backup.sh --canary
```

For a genuinely empty new installation only, `--bootstrap --canary` permits the
initial copies to be created without a two-copy floor. It never exempts unknown
legacy files or bypasses capacity admission. This is not a migration shortcut.

Independently verify the new canary's exact offsite object/hash using the restore
identity. Validate the pinned CLI's checksum behavior in the canary record; do
not infer it from an opaque multipart ETag. Record the verification:

```sh
python3 /opt/143/deploy/scripts/pg-backup-policy.py approve-checksums \
  --file EXACT_NEW_CANARY_FILENAME --sha256 VERIFIED_FULL_FILE_SHA256 \
  --evidence 'Private record: pinned CLI checksum canary and independent object verification'
```

Only then may an attended `prune` command or normal attended backup reduce excess
qualified files to the selected pair. After a **full isolated
restore**, `pin-restored --file ... --sha256 ... --evidence ...` records the known-good
selection. The local restore helper's basic table checks do not automatically
create a full-restore pin. Moving a pin requires evidence of its replacement.

## Restore admission and cleanup

The restore entry point requires a local Docker socket and refuses a daemon
containing the configured production database container, including a stopped one.
It lists all container names and compares exact values, without a name-filter
regex; listing failures refuse admission. Docker documents the
[all-container listing and name formatting](https://docs.docker.com/reference/cli/docker/container/ls/).
Choose a separately verified isolated host; changing the container name to bypass
this guard is not isolation. Before hashing or allocating a container, it checks
the Docker data filesystem for **twice the larger of receipt database size and
archive size, plus the 20 GiB reserve**. This is a conservative admission estimate,
not a runtime resource limit. Isolated host provisioning, memory limits, complete
application checks, and independent offsite recovery must be validated separately.
Keep the production restore schedule held; run drills on isolated capacity.

TERM, HUP, and INT sent to the wrapper are forwarded once as TERM to the reader's
separate process group. The wrapper keeps the shared lock and gives cleanup 120
seconds to remove only the recorded disposable container and its anonymous
volumes. Repeated signals do not interrupt cleanup. After the grace period, it
kills the remaining local process group and keeps the pending marker: killing a
Docker client alone cannot prove daemon-side resource removal.

The reader publishes a private cleanup receipt only after temporary-state cleanup
and either successful owned container removal or proof that creation was never
attempted. A failed or cancelled drill with
that proof records its nonzero status in `last-restore.json` and releases the
pending marker, so backups can continue. Missing ownership evidence or failed
removal preserves the marker and available cidfile for inspection. Basic restore
success still does not create a known-good pin.
`last-restore.json` is replaced with a pending result before the reader starts and
with an unsuccessful result when the reader raises or times out. A previous
successful drill must not mask the current incomplete one.

## Interruption and rollback

Before dump/restore work starts, the policy saves `pending.json` with the archive,
phase and (for a dump) unique `PGAPPNAME`. Failed or interrupted backup work, or
restore work without proven cleanup, preserves this marker and any
partial/completed output. The next operation refuses to
proceed. There is deliberately no age-only partial sweep: a dead Docker client
does not prove its database backend or upload container is gone.

Do not simply remove the marker or re-upload an uncertain archive. Inspect the
exact tagged database backend, Docker activity, archive identity, and remote
object under your recovery procedure. Establish that no owned
writer/reader remains before removing only a proven abandoned partial; preserve
completed uncertain archives until independently verified. Inspect a failed
restore's cidfile/logged container identity before cleanup. Reconcile the receipt
and clear the marker only after those checks. For a post-dump interruption,
inspect `postdump/ownership.json`, `upload.json` when present, the exact owned
container and a fresh object listing. A missing exit record, nonzero exit or
ambiguous command completion requires independent integrity verification before
receipt import; matching object size alone is insufficient. Automatic uncertain
upload recovery remains deliberately unavailable.

An attended recovery can use `resume-upload` when
the archive is complete, unchanged, and its previous runtime has a failed result
with verified cleanup. This is a manual action; it does not resume cron:

```sh
BACKUP_ENABLED=true BACKUP_ATTENDED=true BACKUP_OBSERVER='<attending operator>' \
  python3 /opt/143/deploy/scripts/pg-backup-policy.py resume-upload
```

The command holds the common lock, preserves the old pending marker and runtime
evidence in a new attempt, confirms the absence of old labeled containers and
the original dump backend, and refuses an already completed S3 object. It repeats
structural and SHA-256 checks under resource supervision, then uploads to a new
unique key under `postgres/resumed/<attempt-uuid>/<original-filename>`. Verify
that the writer IAM policy and retention lifecycle cover this prefix before
operational use. Each attempt uses a different key so a delayed old completion
cannot overwrite its object. Incomplete multipart uploads remain a separate
reconciliation task; this command never deletes remote data.

A receipt requires a successful checksum upload and matching listing, and keeps
the original dump start as its recovery point. Inventory, pruning verification
and independent receipt import use its recorded remote key. A resumed upload
does not make an old snapshot fresh, prove a full restore, prune local files, or
run another dump. Failure preserves the archive and pending marker. A completed
remote object with missing upload acknowledgement still requires independent
verification, not another upload.

For that case, independently download and hash the exact pending object/version
using the restore identity on separate capacity. Record its unchanged metadata
and compare the full hash with the preserved local archive. Reconcile the
pending upload using its exact key:

```sh
BACKUP_ENABLED=false RESTORE_TEST_ENABLED=false \
  BACKUP_ATTENDED=true BACKUP_OBSERVER='<attending operator>' \
  python3 /opt/143/deploy/scripts/pg-backup-policy.py import-receipt \
  --file EXACT_PENDING_FILENAME --key EXACT_PENDING_S3_KEY \
  --sha256 VERIFIED_FULL_FILE_SHA256 --version-id VERIFIED_S3_VERSION_ID \
  --etag '"EXACT_ETAG"' --last-modified EXACT_LISTING_TIMESTAMP \
  --evidence 'Private record identifying the independent object/version verification'
```

The common lock covers ownership and cleanup validation, fresh metadata checks,
and supervised local structure/hash verification. The command records the receipt
durably before consuming the matching pending marker. An interruption between
those writes permits the same verified import again. Wrong identity, key, checksum
or metadata, a live old reader/backend, or unproven cleanup leaves the marker in
place. It never uploads, deletes an object, prunes, or runs another dump. As for
other independent imports, the version ID is an operator attestation because the
writer can list objects but cannot read their versions. Unlike legacy imports
without a pending upload, this path preserves the original admission database
size and original dump timeline.

During recovery preparation, `.backup-state/recovery.json` links the unchanged
pending marker to the new supervised attempt. Health follows that watchdog while
continuing to calculate recovery age from qualified receipts. Successful import
does not erase the previous failed backup attempt; a fresh successful backup is
still needed to establish normal operation.

Early post-dump markers lack `database_bytes`. For those only, recover the exact
original admission measurement from the private run log and pass
`--original-database-bytes <bytes> --size-evidence '<log path and admission entry>'`.
Do not substitute today's database size. New markers store the measurement and
reject overrides. The evidence reference is retained in the new receipt.

Keep cron held when rolling code back. Reinstalling the historical age-pruning
script is unsafe on a disk that cannot hold its retention window. Preserve
receipts and the known-good pair, and add capacity if admission cannot be met.
Once a receipt points under `postgres/resumed/`, older policy versions that assume
`postgres/<filename>` cannot validate that copy. Keep a compatible receipt
reader available for recovery, or use a validated restore procedure
that follows the recorded key. Do not run an older retention policy, rewrite the
receipt key, or re-upload the object to make an older version accept it.
Routine operation needs runtime monitoring/alert delivery, independent restoration,
an attended upload/prune canary, and observation across multiple backup cycles.
A time-limited trial may carry explicitly accepted gaps as described above;
it does not complete those checks. Assign an owner to track free bytes,
selected/pinned archives, the next-dump estimate, and the seven-day capacity forecast.

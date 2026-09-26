# Holding database backups and restore tests

Use `BACKUP_ENABLED=false` to hold scheduled full backups and
`RESTORE_TEST_ENABLED=false` to defer the weekly restore drill. Both default to
`true` on first installation. These are operational holds: a backup hold creates
a recovery-point gap, and a deferred restore has not verified recovery.

The canonical values belong in the private production configuration. Obtain
approval for that configuration change and the database-host installation.
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

An authorized root operator who only needs to update cron can run the **reviewed,
already-installed** `install-pg-backups.sh` with the chosen booleans, without
running the full provisioning path. Save the current cron, ensure no dump/restore
is active, and verify the resulting entries and script hashes. The installer
atomically replaces `/etc/cron.d/143-pg-backup`; no Docker or PostgreSQL restart
is required. Old installer versions do not recognize these settings, so do not
allow an old provisioning checkout to overwrite a hold.

The backup and restore entry points also reject an explicit `false` environment
value before touching archives or Docker, exiting with status 75. Direct manual
invocations do not automatically inherit cron's environment: pass the setting
explicitly and inspect the installed cron before running anything. An absent
value permits a direct invocation for backward compatibility.

Record the operator, reason, held schedule, latest verified recovery point, next
reassessment deadline, and rollback contents. Confirm alerting or an explicit
operator follow-up; commented cron entries will not themselves produce alerts.
The schedule switches alone do not prove backup health. The policy described
below adds retention/admission/serialization, but runtime capacity monitoring and
independent offsite restoration remain separate operational gates.

Validation uses mocked transports and temporary files:

```sh
bash deploy/scripts/install_pg_backups_test.sh
bash deploy/scripts/provision_db_backups_test.sh
bash deploy/scripts/pg_backup_test.sh
bash deploy/scripts/restore_test_test.sh
```

## Attended backup policy

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

Until runtime cancellation/monitoring is implemented, backup, prune, and local
restore commands require both `BACKUP_ATTENDED=true` and a nonempty
`BACKUP_OBSERVER` identifying the operator. They report JSON events and nonzero
failures to that operator's terminal. Do not set these variables in cron to
simulate an observer, and do not resume unattended schedules. The observer must
watch disk and memory and have a separately approved stop procedure. M1a does not
claim automated alert delivery or four successful scheduled runs.
**Installing M1a does not resume scheduled backups.** Until M1b is installed and
validated, an assigned human must arrange each approved, attended backup at the
six-hour target cadence, or explicitly record the growing recovery-point gap and
the next decision time. A healthy installer result is not a successful backup.

## Data-only offsite configuration

Provisioning writes `/opt/143/backup-storage.json` atomically with root ownership
and mode 0600 from the existing `BACKUP_S3_BUCKET`, `BACKUP_S3_REGION`,
`BACKUP_AWS_ACCESS_KEY_ID`, and `BACKUP_AWS_SECRET_ACCESS_KEY` settings. It validates
all four settings before copying scripts. The backup reads JSON as data and passes
credentials to the AWS CLI container through environment variables, never command
arguments. The old executable `backup-sync.env` is left untouched for rollback
but is never sourced or evaluated by this policy. Missing JSON configuration
fails closed. No additional IAM permission is required beyond listing and upload.
After the rollback window, remove the old executable configuration only under a
separate, explicit host-change approval.

Keep both schedule holds while installing this revision. Verify helper hashes,
Python 3 availability, JSON configuration permissions, and the continued presence
of the previous owned-container restore cleanup. The provisioning wrapper also
updates scripts and storage configuration; approval must cover that whole scope.
Do not use an old checkout's provisioning command because it can replace held cron.
This installer omits the obsolete retention-days field, so even a held cron file
will have a new hash. Record the new bytes/hash at installation; do not reuse
one-time incident helpers whose preconditions pin the previous cron hash.

## Qualify existing copies and run a canary

First inventory every completed local archive. Independently stream each exact
offsite key/version through SHA-256 using an approved restore identity on separate
capacity. Record unchanged size, version ID, ETag, last-modified, and full-file hash.
Then, under explicit operator approval, import its receipt on the database host:

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

For an explicitly approved, attended canary, leave cron held and override only
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
not infer it from an opaque multipart ETag. Under explicit approval record it:

```sh
python3 /opt/143/deploy/scripts/pg-backup-policy.py approve-checksums \
  --file EXACT_NEW_CANARY_FILENAME --sha256 VERIFIED_FULL_FILE_SHA256 \
  --evidence 'Private record: pinned CLI checksum canary and independent object verification'
```

Only then may an attended `prune` command or normal attended backup reduce excess
qualified files to the selected pair. After a separately approved **full isolated
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
application checks, and independent offsite recovery remain M2 work. Keep the
production restore schedule held.

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
object under a separately approved recovery procedure. Establish that no owned
writer/reader remains before removing only a proven abandoned partial; preserve
completed uncertain archives until independently verified. Inspect a failed
restore's cidfile/logged container identity before cleanup. Reconcile the receipt
and clear the marker only after those checks. Automatic backup cancellation and
recovery are deferred to the runtime-protection milestone.

Keep cron held when rolling code back. Reinstalling the historical age-pruning
script is unsafe on a disk that cannot hold its retention window. Preserve
receipts and the known-good pair, and add capacity if admission cannot be met.
Before routine schedules resume, complete runtime monitoring/alert delivery,
independent restoration, the attended upload/prune canary, and the planned 24-hour
observation. Assign a daily owner to track free bytes, selected/pinned archives,
the next-dump estimate, and the seven-day capacity forecast.

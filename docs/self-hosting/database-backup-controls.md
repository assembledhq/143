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
These switches do not implement backup retention, free-space admission, runtime
monitoring, or independent offsite restore verification.

Validation uses mocked transports and temporary files:

```sh
bash deploy/scripts/install_pg_backups_test.sh
bash deploy/scripts/provision_db_backups_test.sh
bash deploy/scripts/pg_backup_test.sh
bash deploy/scripts/restore_test_test.sh
```

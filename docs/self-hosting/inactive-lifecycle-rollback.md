# Binary-only rollback of the inactive lifecycle release

This procedure is limited to the unactivated lifecycle 0a release at schema 392
or schema 393, which adds the package-registry credentials table.
It restores a separately reviewed older application revision while retaining the
installed schema. It is not an activation procedure or a general schema downgrade.

Before rollout, retain the immutable server, frontend and sandbox image revisions
and validate the intended rollback revision against the upgraded database schema.
Keep the deployment scripts from the newer release: the older release's scripts
and migrator do not understand the installed migration version.

## Preconditions

- Obtain authorization for the exact rollback target. Prevent competing manual
  or automatic deployments for the entire operation; coordinate with any queued
  main-branch deployment. Keep activation and caller participation disabled.
- Use the newer release's deployment checkout and a matching, locally available
  server image that implements `migrate verify-inactive-rollback`.
- Record the current clean migration version and verify that the target image's
  configuration and runtime contracts remain compatible. Pin the exact current
  schema, either 392 or 393; the inspector refuses a mismatch or any other version.
  Use the default `public.schema_migrations` ledger. Custom ledger tables are not
  supported by this inspector.
- For schema 393, use a guard image that explicitly supports 393. Earlier guard
  images accept only 392. Preserve the package-registry table, its encrypted rows,
  and the encryption key. The old application may not support registry credential
  management or authenticated package setup; validate the intended workloads and
  accept that temporary feature loss before choosing that target.
- Preserve the usual worker blue/green port, capacity and retirement settings.
  Do not use force, maintenance or detached deployment modes.

## Execution

Use full immutable Git SHAs for both values below. The guard image is the newer
release containing the read-only inspector; the positional target is the older
application revision already validated for compatibility. The example retains
schema 393; use 392 only when the installed ledger is exactly 392.

```bash
BINARY_ROLLBACK_GUARD_TAG='<newer-release-full-sha>' \
BINARY_ROLLBACK_SCHEMA_VERSION=393 \
  ./deploy/scripts/deploy-fleet.sh '<ssh-key-path>' '<rollback-target-full-sha>' app,worker
```

The inspector runs from the newer image before application replacement and before
each worker replacement. It requires an exact clean ledger, disabled caller
participation, readiness, and absence of lifecycle/caller history that older code
cannot interpret. Its database connection and transaction are read-only, and it
neither creates a ledger nor runs migrations, repair commands or down migrations.
Missing images, unsupported state and inspection errors stop the operation.

Successful inspection permits the existing API/frontend health-check rollout and
serial worker canary/retirement procedure. The old application's migrator is never
invoked. The inspector is a bounded snapshot, not a fleet-wide maintenance lock:
activation and other deployments must remain excluded until rollback completes.

## Verification and failure handling

Verify the expected image on every serving generation, API/frontend health,
ordinary sends, live delivery, review completion and retirement of old workers.
Read back the exact pinned schema with a clean ledger and confirm activation
remains disabled. At schema 393, compare credential rows before and after recovery
and verify they can still be decrypted with the preserved key when a compatible
release resumes. Never run migration 393 down: it drops the credentials table.
Retain diagnostics in private operations storage.

Before using a new target/schema pair, rehearse with an isolated database: apply
the candidate schema, seed synthetic business data and encrypted credentials,
run the guard with both app and worker connection settings, replace the candidate
with the exact fallback revision without invoking its migrator, and verify sends,
review completion, schema, and retained data. Test dirty ledgers, enabled
participation, retained protocol history, and wrong schema pins as refusal cases.
Record whether replacement and retirement were exercised in actual containers or
only simulated; component tests do not establish fleet rollout timing or capacity.

If any guard, health check or retirement fails, stop and inspect the preserved
state. Do not bypass the inspector, retry a dirty ledger blindly, enable lifecycle
participation, or delete retained history to force a pass. A schema downgrade is
a separate authorized maintenance procedure with its own compatibility gates.

Ordinary deployments without either rollback variable still run both the existing
known-dirty repair check and `migrate up` before replacing the API. There is no
unconditional skip-migrations option.

## Source revert with retained migrations

A source revert of the lifecycle runtime is different from deploying an older
binary. Keep migrations through 392 unchanged, along with any unrelated later
migrations. The reverted runtime includes schema 393 for package-registry
credentials, schema 394 for the workspace-preparation phase, and schema 395 for
applied size-limit overrides. It retains the normal migration runner.

For this source revert, `migrate up` checks clean schemas 392 through 395 for disabled
participation and absence of incompatible lifecycle/caller history before making
changes. It verifies the same inactive-state conditions at clean schema 395 after
migration, including on fresh installations and no-change runs. Unsupported later
schemas fail closed. These checks do not disable activation or delete retained
history. A refusal requires investigation; do not bypass it or run down migrations.

Keep activation excluded throughout deployment. Review and validate this source
revision with the retained schema before separately authorizing deployment. The
binary-only rollback command above requires an explicit pin to 392 or 393 and a
guard image that supports that version. It does not infer compatibility with
future schemas or enable lifecycle participation.

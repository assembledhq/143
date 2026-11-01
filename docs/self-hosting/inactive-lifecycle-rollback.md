# Binary-only rollback of the inactive lifecycle release

This procedure is limited to the unactivated lifecycle 0a release at schema 392.
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
  configuration and runtime contracts remain compatible. This mode accepts only
  schema 392, not arbitrary later schema versions.
- Preserve the usual worker blue/green port, capacity and retirement settings.
  Do not use force, maintenance or detached deployment modes.

## Execution

Use full immutable Git SHAs for both values below. The guard image is the newer
release containing the read-only inspector; the positional target is the older
application revision already validated for compatibility.

```bash
BINARY_ROLLBACK_GUARD_TAG='<newer-release-full-sha>' \
BINARY_ROLLBACK_SCHEMA_VERSION=392 \
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
Read back schema 392 with a clean ledger and confirm activation remains disabled.
Retain diagnostics in private operations storage.

If any guard, health check or retirement fails, stop and inspect the preserved
state. Do not bypass the inspector, retry a dirty ledger blindly, enable lifecycle
participation, or delete retained history to force a pass. A schema downgrade is
a separate authorized maintenance procedure with its own compatibility gates.

Ordinary deployments without either rollback variable still run both the existing
known-dirty repair check and `migrate up` before replacing the API. There is no
unconditional skip-migrations option.

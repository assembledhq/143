# Versioned Coding Credentials and Runtime State

> **Status:** Implemented | **Last reviewed:** 2026-06-30

Coding-agent credentials are split into stable credential config and mutable runtime state, and both use the repo's insert-only active-row versioning pattern.

## Tables

`coding_credentials` keeps `id` as the stable logical credential id exposed through APIs and resolver results. Each config version has its own `version_id` primary key and `active` flag. Config changes deactivate the current active config row and insert a new active row with the same `id`.

Config-versioned fields include provider, label, encrypted config, priority, created metadata, and the temporary legacy `status` copy retained for rollback/debug visibility.

`coding_credential_runtime_state` stores runtime health separately from credential config. Each runtime state version has its own `version_id`, points at the logical `credential_id`, carries `org_id` and nullable `user_id`, and has its own `active` flag.

Runtime-versioned fields include status, `last_verified_at`, rate-limit metadata, and runtime-state creation time.

## Mutation Rules

Config-only mutations insert new `coding_credentials` versions without touching runtime state. These include label changes and priority moves. (Every production config-replacement path re-verifies, so the store currently exposes no unverified config-edit method.)

Verified credential replacement inserts both a new config version and a new runtime state version with `status = 'active'`, a fresh `last_verified_at`, and cleared rate-limit metadata. These paths include OAuth completion, token refresh, harvested Claude/Codex subscription credentials, API-key replacement, and re-authentication over an invalid credential.

Legacy dual-write mirror upserts carry the existing runtime version's rate-limit metadata forward: legacy tables know nothing about rate limits, so a legacy write (status update, stack reorder) must not clear a credential's durable rate-limit marker.

Runtime mutations insert new `coding_credential_runtime_state` versions only. These include disable, invalidation/auth rejection, status changes, rate-limit marking/clearing, and verification timestamp changes.

Credential creation inserts one active config version and one active runtime state version in the same transaction.

OAuth completion promotes a pending credential by inserting the final config version and a runtime version with `status = 'active'` and a fresh verification timestamp.

## Query Rules

Public credential ids remain stable logical ids. Reads, lists, and resolver queries select active config rows joined to active runtime rows. Runnable resolver queries additionally require runtime `status = 'active'`; settings lists hide runtime `status = 'disabled'`.

Inactive config and runtime rows remain in the database for audit and future history/restore APIs. This PR intentionally does not expose a credential-history API or restore method.

Partial unique indexes enforce one active config version per logical `id`, one active `(org_id, user_id, provider, label)` row per scope, and one active runtime state row per `credential_id`.

## Rollout Notes

The cutover migration backfills a config `version_id`, marks existing config rows active, creates and backfills `coding_credential_runtime_state` from the old runtime columns on `coding_credentials`, and swaps indexes to active-row partial indexes. A trigger rejects orphaned runtime rows and keeps the temporary legacy runtime columns synced for rollback/debug visibility. New code reads runtime state; a later cleanup PR can drop the old columns and trigger together.

**Rolling-deploy window.** `deploy.sh` runs migrations before rolling api containers, and worker hosts deploy after the app, so pre-versioning code runs against the migrated schema for a window. New→old compatibility is handled by the legacy-column sync trigger. Old→new is one-directional:

- Old code that *creates* a credential (create, pending-auth insert, OAuth promote) writes a config row with no runtime-state row, invisible to every versioned read and mutation. `ReconcileCodingCredentialRuntimeState` heals these at boot (both `cmd/server` binaries), copying the legacy runtime columns into a fresh active runtime version.
- Old workers' runtime writes (rate-limit marks, auth-reject status) land only in the legacy columns and are not seen by versioned readers until the workers are rolled. Deploy app and workers back-to-back to keep this window short.
- Old replicas have no `active = true` filter, so once new code writes a second version they see duplicate rows per credential, and new-code deactivations stay visible to them. Same mitigation: keep the window short.

The Postgres-backed behavior test (`TestCodingCredentialsVersioningMigrationPostgresBehavior`, driven by `TEST_DATABASE_URL` in CI) exercises the up migration's invariants, the deploy-window reconciliation, and the down migration round-trip.

**Cleanup (PR 5, 2026-06-10).** The `credentials_cleanup` migration dropped the temporary legacy runtime columns and `team_default_origin_user_id` from `coding_credentials`, reduced the runtime-state trigger to a pure orphan guard (no more legacy-column sync), and removed the boot-time `ReconcileCodingCredentialRuntimeState` sweep — with the whole fleet on versioned code, no writer can create a config row without its runtime row. The versioned pair is now the only credential storage for coding agents; see [future/65-unified-coding-credentials.md](../future/65-unified-coding-credentials.md) PR 5 notes.

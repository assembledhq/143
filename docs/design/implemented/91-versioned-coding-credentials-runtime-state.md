# Versioned Coding Credentials and Runtime State

> Status: Implemented 2026-06-06

Coding-agent credentials are split into stable credential config and mutable runtime state, and both use the repo's insert-only active-row versioning pattern.

## Tables

`coding_credentials` keeps `id` as the stable logical credential id exposed through APIs and resolver results. Each config version has its own `version_id` primary key and `active` flag. Config changes deactivate the current active config row and insert a new active row with the same `id`.

Config-versioned fields include provider, label, encrypted config, priority, created metadata, and the temporary legacy `status` copy retained for rollback/debug visibility.

`coding_credential_runtime_state` stores runtime health separately from credential config. Each runtime state version has its own `version_id`, points at the logical `credential_id`, carries `org_id` and nullable `user_id`, and has its own `active` flag.

Runtime-versioned fields include status, `last_verified_at`, rate-limit metadata, and runtime-state creation time.

## Mutation Rules

Config-only mutations insert new `coding_credentials` versions without touching runtime state. These include label changes, provider config edits that have not just been verified, and priority moves.

Verified credential replacement inserts both a new config version and a new runtime state version with `status = 'active'`, a fresh `last_verified_at`, and cleared rate-limit metadata. These paths include OAuth completion, token refresh, harvested Claude/Codex subscription credentials, API-key replacement, and re-authentication over an invalid credential.

Runtime mutations insert new `coding_credential_runtime_state` versions only. These include disable, invalidation/auth rejection, status changes, rate-limit marking/clearing, and verification timestamp changes.

Credential creation inserts one active config version and one active runtime state version in the same transaction.

OAuth completion promotes a pending credential by inserting the final config version and a runtime version with `status = 'active'` and a fresh verification timestamp.

## Query Rules

Public credential ids remain stable logical ids. Reads, lists, and resolver queries select active config rows joined to active runtime rows. Runnable resolver queries additionally require runtime `status = 'active'`; settings lists hide runtime `status = 'disabled'`.

Inactive config and runtime rows remain in the database for audit and future history/restore APIs. This PR intentionally does not expose a credential-history API or restore method.

Partial unique indexes enforce one active config version per logical `id`, one active `(org_id, user_id, provider, label)` row per scope, and one active runtime state row per `credential_id`.

## Rollout Notes

The cutover migration backfills a config `version_id`, marks existing config rows active, creates and backfills `coding_credential_runtime_state` from the old runtime columns on `coding_credentials`, and swaps indexes to active-row partial indexes. A trigger rejects orphaned runtime rows and keeps the temporary legacy runtime columns synced for rollback/debug visibility. New code reads runtime state; a later cleanup PR can drop the old columns and trigger together.

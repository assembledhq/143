# Design: Foreign Key Policy and Hot Table Audit

> **Status:** Not Started | **Last reviewed:** 2026-06-09

## Summary

143 should keep database-backed foreign keys as the default for control-plane and moderate-write product data, but stop treating DB-enforced foreign keys as mandatory for every org-scoped table. Very high-write tables can create Postgres MultiXact pressure because foreign-key checks take shared locks on referenced parent rows. Under normal vacuum health this is usually manageable; under replication-slot horizon pinning, large dead-tuple backlogs, or full-load replication pressure, those shared-lock records can turn into `MultiXactOffsetSLRU` thrash and broad database latency.

The desired policy is:

- `org_id` remains non-negotiable on tenant data.
- Every query remains explicitly scoped by `org_id`.
- DB-backed `REFERENCES organizations(id)` and other parent FKs remain the default.
- High-write append-only, event, log, cache, and runtime tables require FK review before adding parent-row FKs.
- If a hot table omits a DB FK, the write path must prove parent existence and tenant ownership in code, preferably in the same request/transaction that authorizes the operation.

This is not a proposal to remove all FKs. It is a proposal to use them where their integrity value exceeds their operational cost, and to make exceptions explicit.

## Background

Before this review, the repo had strong multi-tenancy guidance that treated DB-backed org FKs as universal:

- Root `AGENTS.md` says every table has an `org_id` FK to `organizations`.
- `internal/AGENTS.md` repeats that new migrations should include `org_id uuid NOT NULL REFERENCES organizations(id)`.
- `cmd/lint-schema` enforces the presence of an `org_id uuid` column, but its comments and failure message currently describe the stricter FK requirement.
- Store linting and tenancy tests enforce that store methods pass and query by `org_id`.

The codebase also already has clues that FK behavior needs nuance:

- `migrations/000041_standardize_on_delete.up.sql` documents a policy split between `CASCADE` for owned content and `RESTRICT` for cross-domain references, and says org deletion should be deliberate rather than accidental cascade.
- `migrations/000048_soft_delete.up.sql` prevents hard deletes on high-value parents such as sessions, projects, and issues because cascades can be catastrophic.
- `migrations/000038_partitioning_prep.up.sql` and `000039_partitioning_session_messages.up.sql` partition high-growth append-only session tables, but still keep FKs to `sessions`, `organizations`, and `session_threads`.
- Later migrations sometimes reintroduced `org_id ... ON DELETE CASCADE`, which drifts from the `000041` org-deletion policy.

The external incident reviewed for this design was a Postgres production outage where high-concurrency writes to wide/hot tables with a parent FK, combined with DMS/logical replication horizon pinning and vacuum pressure, led to `MultiXactOffsetSLRU` waits, stalled sessions, connection pool exhaustion, and application errors. The important lesson is not that FKs are always bad; it is that FKs on very hot child tables pointing at low-cardinality parent rows can become a major contributor when vacuum cannot advance.

Relevant references:

- PostgreSQL explicit locking docs: `FOR KEY SHARE` protects referenced keys from deletion or key-changing updates.
- PostgreSQL monitoring docs: SLRU wait events include MultiXact offset/member access.
- AWS guidance on Postgres MultiXacts: foreign-key checks can create MultiXacts when many transactions share-lock the same row.
- PlanetScale/Vitess guidance: operating without FK constraints is common in high-scale/sharded systems, but the 143 decision should be Postgres-specific rather than copied from Vitess.

## Review Findings

### 1. The strongest invariant is tenant scoping, not necessarily the org FK

The P0 safety property is that every tenant row is attributable to an org and every query filters by org. A DB FK to `organizations(id)` is helpful integrity enforcement, but it is not the same thing as tenancy safety. For hot tables, retaining `org_id NOT NULL` plus strict store linting, query tests, and write-path ownership checks can preserve tenant isolation without locking the `organizations` parent row on every insert.

### 2. The riskiest current pattern is high fan-in to low-cardinality parents

Rows that all reference the same org, session, repository, or thread can create shared-lock fan-in on those parent rows. This is most relevant for tables that are:

- high-write or bursty under agent/runtime activity,
- append-only or event-like,
- replicated or backfilled from the primary,
- wide or high-bloat,
- partitioned because of growth,
- not user-facing source-of-truth records whose integrity must block writes.

Current tables worth auditing first:

- `session_logs`
- `session_messages`
- `thread_inbox_entries`
- `thread_runtimes`
- `session_sandbox_holders`
- `container_usage_events`
- `usage_hourly`
- `usage_hourly_execution`
- `preview_dependency_cache`
- `preview_dependency_cache_locations`
- future external-ingestion event mirrors or agent-state tables

This list is a review queue, not a drop list.

### 2a. Static table audit, 2026-06-09

Production DB stats were not available during this pass because `make db-query` could not auto-detect an `SSH_KEY` in the review environment. The findings below are therefore based on migrations, store code, write-path shape, and existing design docs. Before executing any FK-removal migration, repeat this audit with production `pg_stat_user_tables`, `pg_stat_slru`, `pg_stat_activity`, and `pg_constraint` data.

| Table | Current FK shape | Static write-path assessment | Recommendation |
|-------|------------------|------------------------------|----------------|
| `session_logs` | FKs to `sessions(id) ON DELETE CASCADE`, `organizations(id)`, and `session_threads(id) ON DELETE SET NULL`; partitioned by `timestamp`. | This is the clearest hot append-only table. `SessionLogStore.Create` inserts directly with caller-supplied `org_id`, `session_id`, and optional `thread_id`. Logs are operational/audit-ish data with retention cleanup and can tolerate explicit orphan cleanup better than most source-of-truth tables. | **Best first FK-removal candidate**, but only after adding non-locking write-path validation. Preferred migration: keep `org_id`, `session_id`, and `thread_id` columns and indexes; drop DB FKs from `session_logs`; change `Create` to insert through a `SELECT`/`WHERE EXISTS` against `sessions`/`session_threads` for org ownership without taking FK key-share locks. |
| `session_messages` | FKs to `sessions(id) ON DELETE CASCADE`, `organizations(id)`, `users(id)`, and `session_threads(id)`; partitioned by `created_at`; review-loop pointer FKs to message IDs are already intentionally avoided elsewhere because of partitioned PK shape. | High-growth transcript table and a possible FK-pressure candidate, but it is product source-of-truth, not disposable log data. Message inserts are generally downstream of session/thread authorization, but the store itself does not validate parent ownership. | **Do not drop immediately without prod stats.** If production stats show heavy FK/MultiXact pressure, consider first removing low-value attribution FKs (`org_id`, `user_id`) while keeping session/thread integrity, or add write-path validation before dropping session/thread FKs. |
| `thread_inbox_entries` | FKs to `organizations(id)`, `sessions(id) ON DELETE CASCADE`, `session_threads(id) ON DELETE CASCADE`, and `thread_runtimes(id) ON DELETE SET NULL`. | Append/update-heavy operational inbox. However `AppendForMessage` already locks the parent `session_threads` row `FOR UPDATE` to allocate per-thread sequence numbers, so FK removal alone would not eliminate the main parent-row contention point for appends. Runtime delivery updates mostly touch inbox rows directly. | **Do not prioritize FK removal first.** If this table becomes hot, first revisit per-thread sequencing/locking. FK removal may still make sense later, but it is secondary to the explicit thread lock already in the insert path. |
| `thread_runtimes` | FKs to `organizations(id)`, `sessions(id) ON DELETE CASCADE`, and `session_threads(id) ON DELETE CASCADE`. | Lifecycle/lease table with active-row uniqueness and heartbeats. Inserts are bounded by active thread runtime count; repeated heartbeats update the runtime row and do not re-run FK checks. | **Keep FKs for now.** Integrity value is high and FK insert volume should be much lower than logs/messages. |
| `session_sandbox_holders` | FKs to `organizations(id)` and `sessions(id) ON DELETE CASCADE`. | Lifecycle/lease holder table with upserts and heartbeats. Similar to `thread_runtimes`; row count should be bounded by active holders, and updates dominate after insert. | **Keep FKs for now.** Consider dropping only if production shows holder churn is a real bottleneck. |
| `container_usage_events` | FKs to `organizations(id)` and `sessions(id) ON DELETE CASCADE`. | One row per sandbox/container lifecycle plus stop updates. This is billing/usage source data, not disposable telemetry. Insert rate is tied to container starts, not log/message volume. | **Keep FKs for now.** If scale increases sharply, consider replacing `session_id` FK with write-path validation, but preserve stronger integrity until measured pressure exists. |
| `usage_hourly` | FK to `organizations(id)` plus nullable `users(id)`. | Rollup table upserted periodically. Cardinality and write rate should be low relative to raw events. | **Keep FKs.** Low expected operational benefit from removal. |
| `usage_hourly_execution` | FK to `organizations(id)`; primary key includes org/hour/execution dimensions. | Rollup table upserted periodically. | **Keep FK.** Low expected operational benefit from removal. |
| `preview_dependency_cache` | FKs to `organizations(id) ON DELETE CASCADE` and `repositories(id) ON DELETE CASCADE`. | Cache metadata backed by object storage. Rows are useful but rebuildable. Upserts happen on preview dependency cache saves/restores, not every request. `ON DELETE CASCADE` from org/repo conflicts with the broad org-deletion policy, but cache rows are not high-value source-of-truth. | **Do not prioritize for FK pressure; do fix cascade policy drift.** Change org/repo delete behavior to explicit cleanup or no action in a future maintenance migration. Dropping FKs entirely is acceptable later if cache churn becomes high. |
| `preview_dependency_cache_locations` | FKs to `organizations(id) ON DELETE CASCADE` and `repositories(id) ON DELETE CASCADE`; no FK to `preview_dependency_cache`, intentionally behaves like worker-local location hints. | Ephemeral location hints. Existing design says stale rows are acceptable because workers verify local file existence and fall back to object storage. Cleanup methods already delete by TTL/worker/cache key. | **Good FK-removal candidate if touching this area.** Keep `org_id`/`repo_id` columns and indexes, but drop org/repo DB FKs; this table is explicitly tolerant of stale/orphan hints and should not participate in org/repo parent locking. |

Static conclusion: the first concrete table changes should be narrow. Start with `session_logs` only if production stats confirm it is large/high-write enough to justify the migration. Independently, clean up `preview_dependency_cache_locations` and org/repo cascade drift when making preview-cache schema changes. Do not remove FKs from `session_messages`, `thread_inbox_entries`, lifecycle lease tables, or usage rollups without production evidence.

### 3. Cascades deserve separate treatment from FKs

`ON DELETE CASCADE` is more operationally dangerous than a simple FK because one parent delete can trigger large child work. The soft-delete trigger already prevents normal cascades for some critical parents, but newer migrations still include org cascades. Org-level cascades should be treated as suspect unless the table is tiny, purely owned, and explicitly safe to delete during an org deletion workflow.

### 4. Replication and vacuum health are part of FK policy

The incident pattern requires a combination of FK shared locks, MultiXact churn, and blocked cleanup. If 143 adds logical replication, DMS-like export, large backfills, or long-running snapshot jobs against the primary, FK risk rises sharply. The FK policy should therefore be reviewed together with:

- logical replication slot lag,
- `xmin` / catalog horizon pinning,
- autovacuum lag,
- dead tuple volume,
- long-running transactions,
- primary-vs-replica export strategy,
- per-table write rates.

## Proposed Policy

### Default

Use DB-backed FKs for normal product tables. They are valuable for correctness, developer feedback, and keeping migrations honest.

Default new tenant table shape:

```sql
CREATE TABLE example_entities (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id),
    ...
);
```

### Hot Table Exception

A table may omit one or more parent FKs when all of the following are true:

- It is expected to be high-write, append-only, cache-like, telemetry-like, event-like, or runtime-state-like.
- Parent existence and org ownership are already validated by the caller before insert/update.
- Orphan cleanup is acceptable or handled by an explicit cleanup job.
- The migration includes a comment explaining the omitted FK and the owning service/path responsible for validation.
- The design doc or migration names the operational reason, such as avoiding parent-row FK lock fan-in on a hot table.

Example:

```sql
CREATE TABLE session_log_events (
    id bigserial NOT NULL,
    org_id uuid NOT NULL,
    session_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    ...
    -- FK intentionally omitted for session_id/org_id on this hot append-only table.
    -- The session write path loads the session by (org_id, session_id) before insert.
    -- Orphan rows are acceptable until retention cleanup.
    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);
```

### Cascades

- Default org FKs should use `RESTRICT`/no action, not `ON DELETE CASCADE`.
- Use `ON DELETE CASCADE` only for small or moderate child tables whose rows have no value without the parent.
- Avoid cascades from high-value parents with large child fanout; prefer soft-delete plus explicit cleanup.
- Use `ON DELETE SET NULL` for attribution-style references where preserving the child row matters.

## Required Technical Contracts

### Database Schema

No immediate schema migration is proposed by this design.

Future schema changes should classify each new FK as one of:

- `control_plane_fk`: normal DB-enforced FK.
- `hot_table_omitted_fk`: intentionally omitted FK with write-path validation.
- `owned_cascade_fk`: cascade for small/moderate owned content.
- `attribution_set_null_fk`: attribution pointer that should not block parent deletion.
- `restricted_cross_domain_fk`: child has independent value and parent deletion should be deliberate.

Future migrations for hot tables should include comments explaining omitted FKs. A follow-up linter can enforce a marker such as:

```sql
-- lint:allow-hot-table-no-fk reason="append-only runtime events; session ownership checked before insert"
```

Completed linter/guidance alignment from this review:

- Update `cmd/lint-schema` wording to require `org_id uuid NOT NULL`, not necessarily `REFERENCES organizations(id)`.
- Add detection for nullable `org_id` columns.

Recommended remaining linter changes:

- Add optional detection/reporting for `org_id ... ON DELETE CASCADE`.
- Add an explicit hot-table omitted-FK marker if the team wants CI enforcement.

### API Contract

No API contract changes.

Write APIs that insert into hot tables without DB FKs must validate parent ownership before insert. For example, a session-event insert must first load or otherwise prove the session belongs to the current `orgID`.

## Operational Checks

Before dropping an existing FK or approving an omitted FK, gather:

- table row count and estimated writes/sec,
- dead tuple count and autovacuum recency,
- FK parent table and parent-row fan-in,
- whether the table participates in logical replication/export/backfill,
- whether the table is partitioned,
- whether child rows are source-of-truth, cache, telemetry, or operational state,
- cleanup and retention behavior for orphan rows.

Useful Postgres checks:

```sql
SELECT relname, n_live_tup, n_dead_tup, last_autovacuum, last_vacuum
FROM pg_stat_user_tables
ORDER BY n_dead_tup DESC
LIMIT 30;
```

```sql
SELECT name, blks_zeroed, blks_hit, blks_read, blks_written, flushes, truncates
FROM pg_stat_slru
WHERE name ILIKE '%multixact%';
```

```sql
SELECT pid, wait_event_type, wait_event, state, query
FROM pg_stat_activity
WHERE wait_event ILIKE '%MultiXact%' OR wait_event ILIKE '%SLRU%';
```

```sql
SELECT conrelid::regclass AS child_table,
       confrelid::regclass AS parent_table,
       conname,
       confdeltype
FROM pg_constraint
WHERE contype = 'f'
ORDER BY child_table::text, parent_table::text;
```

## Recommended Next Steps

1. Run a production read-only FK audit that joins constraint metadata with table size/dead-tuple/write-rate information. The 2026-06-09 static pass could not access prod because `SSH_KEY` was unavailable.
2. If prod confirms `session_logs` is among the largest/highest-write tables, design a migration to drop its parent FKs and add store-level non-locking parent/org validation before insert.
3. Treat `preview_dependency_cache_locations` as the safest FK-removal candidate the next time preview dependency cache schema is touched.
4. Fix policy drift where `org_id` FKs use `ON DELETE CASCADE` contrary to the `000041` org-deletion policy, starting with cache/location tables and newer preview/runtime migrations.
5. Do not prioritize FK removals for `thread_runtimes`, `session_sandbox_holders`, `usage_hourly`, or `usage_hourly_execution` unless production data shows unexpected write pressure.
6. Revisit `thread_inbox_entries` only after evaluating its explicit `session_threads FOR UPDATE` sequencing lock; dropping FKs alone is unlikely to solve its main append contention shape.
7. Add observability for `MultiXactOffsetSLRU`, long-running transactions, replication slot lag, autovacuum lag, and pgbouncer waiting connections before making FK removals.
8. Decide whether to add CI enforcement for `org_id ... ON DELETE CASCADE` warnings and explicit hot-table omitted-FK markers.

## Non-Goals

- Do not remove all foreign keys.
- Do not weaken `org_id` query scoping.
- Do not rely on application-only checks for auth, tenancy, or control-plane ownership unless the write path is explicit and tested.
- Do not use this policy to justify missing indexes on FK-like columns; hot-table parent IDs still need indexes for query and cleanup paths.

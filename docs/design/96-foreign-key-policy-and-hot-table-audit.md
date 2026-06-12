# Design: Foreign Key Policy and Hot Table Audit

> **Status:** Partially Implemented | **Last reviewed:** 2026-06-12

## Summary

143 should keep database-backed foreign keys as the default for control-plane and moderate-write product data, but stop treating DB-enforced foreign keys as mandatory for every org-scoped table. Very high-write tables can create Postgres MultiXact pressure because foreign-key checks take shared locks on referenced parent rows. Under normal vacuum health this is usually manageable; under replication-slot horizon pinning, large dead-tuple backlogs, or full-load replication pressure, those shared-lock records can turn into `MultiXactOffsetSLRU` thrash and broad database latency.

The desired policy is:

- `org_id` remains non-negotiable on tenant data.
- Every query remains explicitly scoped by `org_id`.
- DB-backed `REFERENCES organizations(id)` and other parent FKs remain the default.
- High-write append-only, event, log, cache, and runtime tables require FK review before adding parent-row FKs.
- If a hot table omits a DB FK, the write path must prove parent existence and tenant ownership in code, preferably in the same request/transaction that authorizes the operation.

This is not a proposal to remove all FKs. It is a proposal to use them where their integrity value exceeds their operational cost, and to make exceptions explicit.

## Implementation Status

Implemented:

- Root/internal agent guidance now distinguishes mandatory `org_id` tenancy from default DB-backed org FKs.
- `cmd/lint-schema` requires `org_id uuid NOT NULL` rather than requiring every tenant table to have a DB FK to `organizations`.
- `session_logs` no longer has DB FKs to `sessions`, `organizations`, or `session_threads` as of migration `000164_hot_table_fk_removal`.
- `SessionLogStore.Create` now validates the parent session and optional thread with a normal read before inserting a log row.
- `preview_dependency_cache_locations` no longer has DB FKs to `organizations` or `repositories` as of migration `000164_hot_table_fk_removal`.

Still open:

- Production FK/SLRU/dead-tuple audit once read-only prod access is available.
- Broader `org_id ... ON DELETE CASCADE` policy drift cleanup.
- Optional linting for `org_id ... ON DELETE CASCADE` and explicit hot-table omitted-FK markers.

## Background

The external incident reviewed for this design was a Postgres production outage where high-concurrency writes to wide/hot tables with a parent FK, combined with DMS/logical replication horizon pinning and vacuum pressure, led to `MultiXactOffsetSLRU` waits, stalled sessions, connection pool exhaustion, and application errors. The important lesson is not that FKs are always bad; it is that FKs on very hot child tables pointing at low-cardinality parent rows can become a major contributor when vacuum cannot advance.

Before this review, the repo treated DB-backed org FKs as universal. `migrations/000041_standardize_on_delete.up.sql` already documents a policy split between `CASCADE` for owned content and `RESTRICT` for cross-domain references, and says org deletion should be deliberate rather than accidental cascade — so nuance was already present. Later migrations sometimes reintroduced `org_id ... ON DELETE CASCADE`, drifting from that policy.

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

### 2a. Static table audit, 2026-06-09

Production DB stats were not available during this pass. The findings below are based on migrations, store code, and write-path shape. Before executing any further FK-removal migration, repeat this audit with production `pg_stat_user_tables`, `pg_stat_slru`, `pg_stat_activity`, and `pg_constraint` data.

| Table | Recommendation |
|-------|----------------|
| `session_logs` | **Implemented.** FKs dropped in migration `000164`. `SessionLogStore.Create` validates session/thread ownership before insert. Monitor for orphan rows and failed log inserts after deploy. |
| `preview_dependency_cache_locations` | **Implemented.** FKs dropped in migration `000164`. Stale/orphan rows handled by TTL/worker cleanup and runtime verification. |
| `session_messages` | **Do not drop without prod stats.** High-growth transcript table; product source-of-truth. If MultiXact pressure is confirmed, consider removing attribution FKs (`org_id`, `user_id`) before touching session/thread integrity FKs. |
| `thread_inbox_entries` | **Do not prioritize FK removal.** `AppendForMessage` already takes a `session_threads FOR UPDATE` lock for sequencing; FK removal alone would not address the main contention point. |
| `thread_runtimes` / `session_sandbox_holders` | **Keep FKs.** Lifecycle/lease tables with bounded row counts; integrity value exceeds write pressure. |
| `container_usage_events` | **Keep FKs for now.** Billing source data; preserve integrity until measured pressure exists. |
| `usage_hourly` / `usage_hourly_execution` | **Keep FKs.** Low-frequency rollup upserts; negligible operational benefit from removal. |
| `preview_dependency_cache` | **Keep FKs for now; fix cascade drift separately.** `ON DELETE CASCADE` from org/repo conflicts with the `000041` org-deletion policy. |

### 3. Cascades deserve separate treatment from FKs

`ON DELETE CASCADE` is more operationally dangerous than a simple FK because one parent delete can trigger large child work. Org-level cascades should be treated as suspect unless the table is small, purely owned, and explicitly safe to delete during an org deletion workflow. Prefer soft-delete plus explicit cleanup for high-value parents.

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

Use `-- lint:allow-hot-table-no-fk reason="..."` in the CREATE TABLE statement so `cmd/lint-schema` accepts the omission. Example:

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
    -- lint:allow-hot-table-no-fk reason="append-only runtime log; session ownership checked before insert"
    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);
```

### Cascades

- Default org FKs should use `RESTRICT`/no action, not `ON DELETE CASCADE`.
- Use `ON DELETE CASCADE` only for small or moderate child tables whose rows have no value without the parent.
- Avoid cascades from high-value parents with large child fanout; prefer soft-delete plus explicit cleanup.
- Use `ON DELETE SET NULL` for attribution-style references where preserving the child row matters.

## Required Technical Contracts

Write APIs that insert into hot tables without DB FKs must validate parent ownership before insert. For example, a session-event insert must first load or otherwise prove the session belongs to the current `orgID`.

## Non-Goals

- Do not remove all foreign keys.
- Do not weaken `org_id` query scoping.
- Do not rely on application-only checks for auth, tenancy, or control-plane ownership unless the write path is explicit and tested.
- Do not use this policy to justify missing indexes on FK-like columns; hot-table parent IDs still need indexes for query and cleanup paths.

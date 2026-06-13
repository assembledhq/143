# Design: Previews Index, Auto-Preview Policy, and Warm Resume

> **Status:** Mostly Implemented; queue-at-cap remains | **Last reviewed:** 2026-06-11

---

# Part 1: Product Spec

## Summary

Previews became a first-class branch artifact in [83-branch-and-pr-previews](../implemented/83-branch-and-pr-previews.md), but the product still treats them as leaf pages: `/previews/new`, `/previews/[id]`, and the durable PR route exist, while a parent `/previews` index does not. Once a user navigates away from a preview, there is no way to find it again except back through the session or PR that spawned it. There is also no way to express "I want a preview for every PR" — every preview is a manual act.

This design adds three things:

1. **A Previews index** at `/previews`, promoted to a top-level navigation item, showing what is running, what is warm and ready to resume quickly, and what recently ran or failed.
2. **Per-repository auto-preview policy** with three modes — `off`, `warm`, and `on` — so a repo can automatically build a preview for every open PR, either keeping it running or hibernating it for fast resume.
3. **Warm resume** as a named product concept: a stopped preview whose startup snapshot is still cached on a healthy worker resumes in seconds instead of minutes, and the product surfaces that distinction.

## Why Now

- Previews are the artifact our **nontechnical contributors** actually consume. They do not read diffs or session transcripts; they look at a running app and react to it. Today that segment has no home surface — they need an engineer to hand them a link.
- The orphan-route IA is a real bug: three preview routes exist with no parent list. Engineers lose previews they started an hour ago.
- The startup-acceleration machinery already exists ([93-session-preview-dependency-cache](../implemented/93-session-preview-dependency-cache.md), worker startup snapshots in `preview_startup_cache`). "Warm and ready" is mostly true at the infrastructure level; the product just never says it or lets users rely on it.
- We are deciding whether previews become a pillar of the product. Nav placement is the cheapest honest test of that thesis: the index page is needed under any option, and promoting or demoting a nav item is a one-line change.

## Users

| Segment | Priority | What they need from previews |
|---|---|---|
| Engineers / builders | P0 | Find the preview for a branch/PR fast; know what is consuming worker memory; restart stale previews; configure per-repo policy once and forget it. |
| Nontechnical contributors | P0 | One legible place that answers "show me the running version of this change." No knowledge of sessions, branches, or sandboxes required. Click a row, see the app. |
| PR reviewers | P1 | A working preview link on every PR they're asked to review, without asking the author to spin one up. Warm resume matters most here: review happens hours after the build, so the link must come back fast. Auto-preview `warm` mode is built for this segment. |
| Execs / leadership | P1 | Occasional, zero-setup ability to poke at an in-flight change — open the index or a PR link, click, play with the app. They will never create a preview, never configure anything, and will judge the product by whether that one click works. The index's Resume button and the durable PR link are their entire surface. |

## Product Principles

1. **The user should answer "can I see this change running?" in one click from anywhere.** The index is that anywhere.
2. **Previews stay temporary.** Auto-preview does not create always-on staging (explicit non-goal of design 83). Even `on` mode is bounded by idle TTLs; what we guarantee is *fast resume*, not perpetual uptime. Warm resume is how we square "feels always available" with "costs almost nothing while idle."
3. **Policy is set per repository, by admins, and then disappears.** Casual users never see policy; they just notice previews are already there when they open a PR.

## Goals

- Add a `/previews` index page listing running, resumable, and recent previews for the org, and promote `Previews` to top-level navigation.
- Let admins set a per-repository auto-preview mode (`off` / `warm` / `on`) that reacts to PR open and push events.
- Define and surface "ready to resume" (warm) as a derived state with an honest resume-time estimate.
- Keep auto-created previews from starving interactive usage: separate org-level pool, lower-priority builds.
- Reorganize `/settings/previews` so policy is the first tab and credentials (secrets, API tokens) follow.

## Non-Goals

- Always-on staging environments or production deploys (unchanged from design 83).
- Cross-worker replication of full startup snapshots. Warm resume is best-effort; a snapshot evicted or on a drained worker degrades to a normal cold start, never an error.
- Auto-preview for branches without PRs, or for forks. PRs from forks are excluded in v1 (secret exposure risk).
- Public unauthenticated preview sharing.
- A live capacity dashboard. The index shows coarse pool usage, not per-container telemetry ([09-observability](../backlog/09-observability.md) territory).

## Navigation Decision

Options considered:

1. **Top-level `Previews` nav item** — chosen. The nav has only three items (Sessions, Automations, Autopilot); previews are org-shared infrastructure with their own lifecycle, already modeled independently of sessions; and "Previews" is the most legible word in the product for nontechnical users.
2. Tab inside Sessions — rejected: previews are not sessions, outlive them, and auto-PR previews have no session at all.
3. "Environments" umbrella — rejected: speculative naming for a product we have not built, and more engineer-coded than "Previews."
4. List under Settings — rejected: settings is where you configure a capability, not operate it; the target segment never opens settings.

Fallback: if the index sees no traffic after a quarter, demote the nav item and keep the page reachable from the command palette and repository pages. Nothing else in this design changes.

## Feature 1: Previews Index

Route: `/previews`. Org-scoped, visible to all roles (viewers can open previews; create/stop affordances follow existing RBAC).

The page is grouped by operational state, not a flat table. Grouping is the product statement: *running things cost resources, warm things are one click away, history is for diagnosis.*

### Desktop Wireframe

```text
┌ Sidebar ─────────┐
│ Sessions         │
│ Automations      │
│ Autopilot        │
│ Previews    ● 2  │   <- new top-level item, badge = active count
│ ─────────        │
│ Settings ▾       │
└──────────────────┘

Previews                                                  [New preview]
See running previews, resume warm ones, and review recent activity.

[ Search branch, repo, or PR…          ]   Repository [ All        v ]

Running (2)                                      Pool: 3 of 6 previews
┌──────────────────────────────────────────────────────────────────────┐
│ ●  feature/checkout-v2    acme/shop      PR #482   ready             │
│    started 25m ago · shuts off in 1h 35m                             │
│                                  [Open preview] [Stop] [⏱ Lifetime]  │
├──────────────────────────────────────────────────────────────────────┤
│ ◐  fix/login-redirect     acme/shop      Session   starting (build)  │
│    started 40s ago · install/build                                   │
│                                            [View status] [Stop]      │
└──────────────────────────────────────────────────────────────────────┘

Ready to resume (3)                            warm — resumes in ~30s
┌──────────────────────────────────────────────────────────────────────┐
│ ◌  feature/pricing-table  acme/shop      PR #479   hibernated 2h ago │
│    auto-preview (warm) · commit 3f2a91c                              │
│                                              [Resume] [Start latest] │
├──────────────────────────────────────────────────────────────────────┤
│ ◌  docs/api-reference     acme/docs      Manual    stopped 5h ago    │
│                                              [Resume] [Start latest] │
└──────────────────────────────────────────────────────────────────────┘

Recent (last 7 days)                                        [Show all]
┌──────────────────────────────────────────────────────────────────────┐
│ ✕  feature/sso            acme/shop      PR #475   failed: install   │
│    2 days ago                                  [View logs] [Retry]   │
├──────────────────────────────────────────────────────────────────────┤
│ ○  chore/deps             acme/shop      API       expired 3d ago    │
│                                                       [Start latest] │
└──────────────────────────────────────────────────────────────────────┘
```

### Mobile Wireframe

```text
Previews                       [New]

[ Search…                          ]

Running (2)
[ ● feature/checkout-v2            ]
  acme/shop · PR #482 · ready
  shuts off in 1h 35m
  [Open] [Stop]

Ready to resume (3)
[ ◌ feature/pricing-table          ]
  acme/shop · PR #479
  hibernated 2h ago · resumes ~30s
  [Resume]

Recent
[ ✕ feature/sso                    ]
  failed: install · 2 days ago
  [Retry]
```

### Row Semantics

- One row per **preview target** (branch + commit + config), showing its latest runtime attempt — not one row per attempt. History of attempts lives on the existing `/previews/[id]` detail page.
- **Running** = instance status in `starting`, `ready`, `partially_ready`, `unhealthy`. Unhealthy renders with a warning treatment, not buried.
- **Ready to resume** = terminal instance (`stopped`/`expired`) whose target has a valid startup snapshot on a healthy worker (see Warm Resume). Shows the *reason* it stopped when known: "hibernated by policy" vs "stopped by you" vs "expired."
- **Recent** = everything else terminal in the last 7 days, with `failed` pinned above `expired`/`stopped`.
- Source badge reuses design 83 vocabulary: `Session`, `PR #n`, `Manual`, `API`, `Automation`. PR badges link to GitHub; session badges link to session detail.
- Clicking a row opens `/previews/[id]`. The primary button is the one-click action (`Open preview` / `Resume` / `Retry`).
- Pool line shows active previews against the org cap so engineers understand why a start might queue. Coarse on purpose.

### Empty State

First-run matters because the page ships before most orgs have preview volume:

```text
            No previews yet

  Previews let anyone see a branch or PR
  running in the browser — no setup needed.

        [Create your first preview]

  Tip: turn on auto-preview for a repository in
  Settings -> Preview to get one for every PR.
```

## Feature 2: Auto-Preview Policy

Per-repository setting, admin-only, three modes:

| Mode | On PR open / push | Idle behavior | Who it's for |
|---|---|---|---|
| `off` (default) | Nothing | — | Repos that don't want previews or use external deploy previews |
| `warm` | Build the preview, save the startup snapshot, then stop it ("hibernate") | Already stopped; resumes in ~30s from the PR link or index | Most repos: every PR link works fast, near-zero idle cost |
| `on` | Build and leave running | Normal idle TTL applies; when reclaimed it degrades to warm (resumable), never to cold | Repos with active nontechnical reviewers who click PR links all day |

Key behaviors:

- Triggered by GitHub PR `opened`, `reopened`, and `synchronize` (new head SHA) webhooks for PRs targeting the repository's default branch. Draft PRs are skipped in v1. Fork PRs are always skipped (secrets).
- A push to an auto-previewed PR creates a new target for the new head SHA. The previous head's runtime is stopped/hibernated; the durable PR link already resolves to the newest target (design 83 behavior).
- Auto-created previews draw from a **separate org-level pool** (`preview_auto_pool_max_active`), not the per-user quota, so a busy repo cannot exhaust a human's interactive previews — and vice versa. Warm builds queue at lower priority than user-initiated starts.
- When the PR closes or merges, any active auto-preview is stopped and its target is excluded from the "Ready to resume" section (it still appears in Recent).
- `on` mode is not staging: idle TTL and the 2h lifetime cap from [82-preview-lifetime-controls](../implemented/82-preview-lifetime-controls.md) still apply. The promise is "the PR link is always one fast click from a running app," not "a container is always burning."

### Settings Wireframe

`/settings/previews` gains a first tab. Existing secrets and API tokens (design 88) become tabs two and three; their content is unchanged.

```text
Preview
Configure auto-previews, runtime secrets, and API access.

[ Auto-preview ]  [ Secrets ]  [ API tokens ]

Auto-preview builds a preview for every open pull request.
Warm mode hibernates it after building so it resumes in seconds.

┌──────────────────────────────────────────────────────────────────────┐
│ Repository           Mode                       Open PRs   Updated   │
├──────────────────────────────────────────────────────────────────────┤
│ acme/shop            ( ) Off (•) Warm ( ) On      12       Jun 8     │
│ acme/docs            (•) Off ( ) Warm ( ) On       3       —         │
│ acme/mobile          ( ) Off (•) Warm ( ) On       7       Jun 2     │
└──────────────────────────────────────────────────────────────────────┘

Auto-preview pool
Concurrent auto-previews allowed to run at once: [ 4 ]  (1–20)
Warm and hibernated previews don't count against this pool.
Capacity and lifetime limits live in Runtime settings ->
```

Mode changes autosave per row with the same autosave indicator pattern as the Runtime settings page. The pool limit lives here (it is preview policy) while resource tiers and TTLs stay in `/settings/runtime` with the existing cross-link.

## Feature 3: Warm Resume

Today `stopped` and `expired` are terminal and undifferentiated; restarting always looks like a cold start to the user even when the worker still holds the startup snapshot and the restart would take seconds. This feature names the distinction:

- A target is **resumable** when its last successful runtime recorded a startup snapshot key and a matching `preview_startup_cache` row exists on a healthy, non-draining worker.
- `Resume` on a resumable target is the existing restart action, but the scheduler pins it to the snapshot's worker and the UI sets the expectation (`~30s`). If the snapshot is gone by the time the job runs, the start silently degrades to cold — same outcome, longer wait, never an error.
- `warm` policy is just "build, snapshot, stop" — it manufactures resumability on purpose. Manual previews get the same benefit for free whenever their snapshot survives.
- Resumability is **derived state**, recomputed at read time. We do not add a `warm` status to the `PreviewStatus` enum; a hibernated preview is a `stopped` preview with a live snapshot. This keeps the lifecycle state machine untouched.

## Success Metrics

- Index adoption: weekly unique visitors to `/previews`, split by role (admin/member vs viewer proxy for nontechnical).
- Time-to-open: p50 from PR-link click to preview ready, for cold vs warm paths. Target: warm p50 under 45s.
- Policy adoption: % of connected repos with mode ≠ `off` after 30 days.
- Resume honesty: % of `Resume` clicks that actually hit the snapshot (vs degraded to cold). If this drops below ~70%, the "~30s" promise is a lie and eviction/pinning needs work.
- Cost guardrail: auto-pool saturation rate; container-minutes attributable to `on` mode.

## Rollout

1. Ship the index page reachable from the command palette and `/previews/new` breadcrumb (no nav change). Validate data shape and polling cost.
2. Promote `Previews` to top-level nav.
3. Ship warm resume (derived resumability + pinned restart) — independent of policy.
4. Ship auto-preview policy, `warm` mode first, `on` mode behind the same setting once pool accounting is proven.

---

# Part 2: Engineering Spec

## Overview of Changes

| Area | Change |
|---|---|
| Schema | New `repository_preview_policies` table; `preview_targets.last_snapshot_key`; `preview_instances.stopped_reason` |
| Org settings | New JSONB fields on `OrgSettings` (no migration) |
| API | Richer `GET /api/v1/previews` (grouped, filtered, counted); policy CRUD; resume hint on restart |
| Workers | `start_branch_preview` honors `stop_after_ready`; records snapshot key; restart prefers snapshot worker |
| Webhooks | PR event handler evaluates policy and enqueues auto-preview jobs |
| Frontend | Nav item, `/previews` index page, settings Auto-preview tab |

## Database Schema

Migration `000176_preview_policies.up.sql` (renumber against `origin/main` immediately before push; the CI migrator is the only duplicate check).

```sql
-- Per-repository auto-preview policy. One row per repo; absence = 'off'.
CREATE TABLE repository_preview_policies (
    id                 UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id             UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    repository_id      UUID        NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    auto_mode          TEXT        NOT NULL DEFAULT 'off',
    updated_by_user_id UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT repository_preview_policies_auto_mode_check
        CHECK (auto_mode IN ('off', 'warm', 'on'))
);

CREATE UNIQUE INDEX idx_repository_preview_policies_repo
    ON repository_preview_policies (org_id, repository_id);

-- Snapshot key recorded by the worker after a successful launch, used to
-- derive resumability by joining against preview_startup_cache.
ALTER TABLE preview_targets
    ADD COLUMN last_snapshot_key TEXT NOT NULL DEFAULT '';

-- Why the latest runtime attempt stopped; drives index copy
-- ("hibernated by policy" vs "stopped by you" vs "expired").
ALTER TABLE preview_instances
    ADD COLUMN stopped_reason TEXT NOT NULL DEFAULT '',
    ADD CONSTRAINT preview_instances_stopped_reason_check
        CHECK (stopped_reason IN ('', 'user', 'expired', 'warm_policy',
                                  'pr_closed', 'drain', 'error'));

-- Resumable-section query: terminal instances per target, newest first.
CREATE INDEX idx_preview_instances_org_terminal
    ON preview_instances (org_id, created_at DESC)
    WHERE status IN ('stopped', 'expired', 'failed', 'unavailable');
```

Notes:

- `auto_mode` and `stopped_reason` get typed string enums in `internal/models` (`PreviewAutoMode`, `PreviewStoppedReason`) with `Validate()` methods, plus migration-pin tests asserting the Go enum set matches the SQL CHECK list — the established pattern for CHECK-constraint columns.
- No new table for resumability: it is derived at read time from `preview_targets.last_snapshot_key` joined to `preview_startup_cache (org_id, repo_id, snapshot_key)` and worker health. `preview_startup_cache` already tracks `worker_node_id` and `last_used_at` (migration 000057).
- Down migration drops the table, columns, constraint, and index.

### Org Settings (JSONB, no migration)

`organizations.settings` via `internal/models/org_settings.go`:

```go
// PreviewAutoPoolMaxActive caps concurrently *running* auto-created
// previews org-wide. Warm/hibernated previews do not count.
PreviewAutoPoolMaxActive int `json:"preview_auto_pool_max_active,omitempty"`
```

Defaults/bounds following the existing pattern (`DefaultPreviewAutoPoolMaxActive = 4`, min 1, max 20), normalized in the same place `PreviewMaxPreviewsPerUser` is.

## API Contract

All routes org-scoped via existing auth middleware. Preview API tokens (scoped `previews:*`) work on the read/list/start routes exactly as today.

### 1. List Previews (extend existing `GET /api/v1/previews`)

The current endpoint returns recent target/runtime rows. Extend with grouping, scope filter, search, and counts. Existing query params (`repository_id`, `branch`, `status`) keep working for API-token callers.

```
GET /api/v1/previews?scope=running|resumable|recent&repository_id=&q=&limit=&cursor=
```

Response (one row per target, latest instance embedded):

```json
{
  "data": [
    {
      "target_id": "uuid",
      "preview_id": "uuid",
      "repository": { "id": "uuid", "full_name": "acme/shop" },
      "branch": "feature/checkout-v2",
      "commit_sha": "3f2a91c...",
      "preview_config_name": "",
      "source": { "type": "pull_request", "id": "482", "url": "https://github.com/..." },
      "status": "ready",
      "current_phase": "",
      "error": "",
      "created_at": "2026-06-10T17:05:00Z",
      "expires_at": "2026-06-10T19:05:00Z",
      "stopped_at": null,
      "stopped_reason": "",
      "resumable": false,
      "resume_estimate_seconds": null,
      "stable_url": "https://app.143.dev/previews/abc"
    }
  ],
  "meta": {
    "counts": { "running": 2, "resumable": 3, "recent": 11 },
    "pool": { "auto_active": 1, "auto_max": 4,
              "user_active": 2, "user_max": 4 },
    "next_cursor": null
  }
}
```

Rules:

- `scope=running`: latest instance `IsActive()`.
- `scope=resumable`: latest instance in (`stopped`, `expired`), target's PR (if PR-sourced) still open per `pr_preview_state`, `last_snapshot_key != ''`, and a `preview_startup_cache` row exists for `(org_id, repo_id, last_snapshot_key)` on a healthy non-draining worker. `resume_estimate_seconds` is a coarse constant (30) when the snapshot worker is healthy; `null` otherwise.
- `scope=recent`: terminal instances from the last 7 days not in resumable; `failed` ordered first.
- `q` matches branch, repository full name, and PR number (`#482` or `482`).
- Worker-health join must not block the list: resolve snapshot/worker liveness from the existing worker registry in memory/Redis; on lookup failure return `resumable: false` rather than erroring (mirrors the non-blocking rule from design 93's scheduler).
- `meta.counts` is computed with the same filters minus `scope`; powers the section headers and the nav badge.
- RBAC: any org member or viewer can list. Action affordances (stop/restart) remain enforced server-side on their own routes.

### 2. Resume (reuse existing restart)

`POST /api/v1/previews/{preview_id}/restart` gains scheduler behavior, not a new contract: when the target has a live snapshot row, worker selection pins to that `worker_node_id` first (capacity permitting), then falls back to normal selection. Response unchanged. No new endpoint — `Resume` in the UI is `restart`; `Start latest` is the existing `start-latest`.

### 3. Preview Policies

```
GET  /api/v1/previews/policies
```

Returns one row per connected repository (left join — repos without a policy row report `"auto_mode": "off"`), plus open-PR counts for the settings table:

```json
{
  "data": [
    { "repository": { "id": "uuid", "full_name": "acme/shop" },
      "auto_mode": "warm", "open_pr_count": 12,
      "updated_at": "2026-06-08T12:00:00Z" }
  ]
}
```

```
PUT  /api/v1/repositories/{id}/preview-policy
```

Request: `{ "auto_mode": "warm" }`. Upserts the policy row. Admin-only (RBAC enforced server-side; frontend role checks are affordance hiding only). Validation through `PreviewAutoMode.Validate()`. Emits an audit event with repository, previous mode, new mode, actor.

Setting `auto_mode` to `off` does not stop running previews; it only stops reacting to future PR events.

### 4. Org Setting

`preview_auto_pool_max_active` rides the existing org settings PATCH surface used by the Runtime settings page; the Auto-preview tab edits this one field via the same endpoint. No new route.

## Webhook and Job Flow

### PR Event Handling

In the existing GitHub webhook path (where `pr_preview_state` is maintained):

1. On `pull_request` `opened` / `reopened` / `synchronize` for a non-draft, non-fork PR targeting the default branch, look up `repository_preview_policies`. Mode `off` (or no row): done.
2. Resolve head SHA → find-or-create the `preview_target` (existing idempotent create path, `source_type = 'pull_request'`). If an active instance already exists for the target, done.
3. Enqueue `start_branch_preview` with two new job fields:
   - `initiator: "auto_policy"` — routes quota accounting to the auto pool and the job to a lower-priority queue.
   - `stop_after_ready: bool` — true for `warm` mode.
4. On `synchronize`, after enqueueing the new head's build, stop the previous head's active runtime with `stopped_reason = 'warm_policy'` (its snapshot remains, so it stays resumable until the new head's build lands).
5. On `closed`, stop any active runtime for the PR's targets with `stopped_reason = 'pr_closed'`.

Idempotency: webhook redelivery is absorbed by the existing target find-or-create plus the active-instance check; no new idempotency table needed.

### Worker Changes

`start_branch_preview`:

- After a successful launch and startup-snapshot save (existing `createBranchPreviewStartupCache` path), write the snapshot key to `preview_targets.last_snapshot_key`.
- When `stop_after_ready` is set: wait for readiness, save the snapshot, then stop the runtime cleanly with `stopped_reason = 'warm_policy'`. Failures before readiness record `failed` as today (a warm build failure must be visible in Recent, not silent).
- Existing stop paths set `stopped_reason`: user-initiated stop → `'user'`, TTL sweeper → `'expired'`, worker drain ([88-preview-runtime-ownership-drain](../future/88-preview-runtime-ownership-drain.md)) → `'drain'`.

Quota accounting:

- `initiator = auto_policy` instances count against `preview_auto_pool_max_active` (running only — `stop_after_ready` builds occupy a slot just during the build). User/API-initiated previews keep their existing counters from design 83. Worker saturation remains shared, as today.
- When the auto pool is full, jobs wait in queue rather than failing; the settings page copy explains the pool.

## Frontend

### Navigation

`authenticated-layout.tsx`: add `Previews` → `/previews` after Autopilot (icon: `MonitorPlay` or `Eye`). Badge shows `meta.counts.running` when > 0; the badge reuses the list query so it costs nothing extra while the page is open, and polls lazily (60s via `pollMs()`) otherwise.

### Index Page

`frontend/src/app/(dashboard)/previews/page.tsx`:

- One `useQuery` per scope section, key `["previews", scope, repositoryId, q]`, all hitting `GET /api/v1/previews`. Poll the `running` section at `pollMs(5000)`; `resumable`/`recent` at `pollMs(30000)`. All poll delays go through `pollMs()` so jsdom tests shrink them.
- Desktop: shared `Table` components per section, following the credential-table column pattern (entity first, metadata middle, actions right-aligned). Mobile: stacked rows, same as the settings pages.
- Actions call existing API client methods (`stop`, `restart`, `startLatest`); mutations invalidate all `["previews", ...]` keys.
- Empty state uses `EmptyState` with the create CTA and a settings deep link for admins only.
- `PageContainer`/`PageHeader`, browser title `Previews` (per [85-browser-page-titles](../implemented/85-browser-page-titles.md)).

### Settings

`/settings/previews` becomes three tabs: **Auto-preview** (new, first), **Secrets**, **API tokens** (both unchanged from design 88). The Auto-preview tab:

- `GET /api/v1/previews/policies` → table with a three-option segmented control per row; each change `PUT`s immediately with the Runtime-settings autosave indicator.
- Pool size input edits `preview_auto_pool_max_active` through the org settings surface.
- Tab visible to all admins; non-admins keep seeing Secrets/Tokens per existing gating.

## Security and RBAC

- Policy mutations: admin-only, audited.
- Auto-created previews default to authenticated org-only access (unchanged bootstrap flow). No change to the preview-origin token model.
- Fork PRs never auto-preview: head checkout would run third-party code with repo preview secrets mounted. Revisit only with a secrets-free preview profile.
- The list endpoint exposes no secret material; `stable_url`/source URLs only.

## Observability

New metrics (org/repo dimensions where applicable):

- `preview_auto_builds_total{mode, result}` — policy-triggered builds and outcomes.
- `preview_resume_total{path}` — `snapshot_hit` vs `cold_degraded`; this is the resume-honesty metric.
- `preview_auto_pool_saturation` — time at cap.
- `preview_index_list_duration` — list endpoint latency (the resumable join is the risk; budget p99 under 250ms).
- Reuse existing startup-phase duration metrics for warm builds via the `initiator` dimension.

## Test Plan

Backend (`go test ./...`, lint via `make lint`):

1. Enum pin tests: `PreviewAutoMode` and `PreviewStoppedReason` Go sets match migration CHECK lists.
2. Policy upsert: RBAC (member 403), validation, audit event, off-row absence semantics.
3. List endpoint: scope filtering, resumable derivation (snapshot present / absent / worker unhealthy / PR closed), counts meta, `q` matching, API-token access on read scope.
4. Webhook flow: opened→enqueue, synchronize→new target + old runtime hibernated, closed→stopped with `pr_closed`, draft/fork skipped, redelivery idempotent, pool-full queues rather than fails.
5. Worker: `stop_after_ready` saves snapshot then stops with `warm_policy`; snapshot key persisted to target; restart pins to snapshot worker and degrades cleanly.

Frontend (from `frontend/`: `npm run typecheck && npm run lint && npm run build`, tests in `page-*.test.tsx` chunks):

1. Nav renders `Previews` with badge from counts.
2. Index sections render per scope; empty state; resume button calls restart; poll intervals via `pollMs()`.
3. Settings tab: mode change PUTs and autosaves; non-admin sees no Auto-preview mutations.
4. Mobile stacked layouts for index and settings tab.

## Implementation Plan

The work splits into five workstreams. A and B can start immediately and in parallel (B develops against MSW mocks of the extended list contract). C is the shared schema/enum foundation for D and E; it is small and should land first among the backend behavior changes. D and E are independent of each other once C is merged. One engineer per workstream is the intended parallelism; within a workstream, tasks are ordered.

Dependency graph:

```text
A (list API) ──────────────┐
                            ├──> B wires real API (B3)
B (index UI, mocked) ───────┘
C (schema + enums) ──> D (warm resume) ──> B resumable section live
                  └──> E (policy + webhooks + settings)
```

### Workstream A: List endpoint extension (backend)

No schema dependency except the terminal-instances index (safe to include here even though the rest of migration 000176 lands in C — coordinate the migration file with C's owner, or land the index as its own numbered migration; renumber against `origin/main` before push).

- [x] A1. Add the partial index on terminal `preview_instances` (`idx_preview_instances_org_terminal`).
- [x] A2. Extend the list query layer to return one row per `preview_target` with its latest instance embedded, supporting `scope=running|resumable|recent`, `repository_id`, `q` (branch / repo full name / PR number with optional `#`), `limit`, `cursor`.
- [x] A3. Add `meta.counts` (per-scope counts with the same filters minus `scope`) and `meta.pool` (reuse existing user-quota counters; `auto_*` fields return the org-settings cap and live active count).
- [x] A4. Preserve backward compatibility for existing callers of `GET /api/v1/previews` (`repository_id`, `branch`, `status` params; preview-API-token auth with `previews:read`). Runtime compatibility plus dedicated handler tests for old and new param shapes are implemented.
- [ ] A5. Add `preview_index_list_duration` metric; budget p99 < 250ms; add a query test against a seeded org with ~500 targets to catch N+1s. Metric is implemented; seeded performance test remains.
- [x] A6. Publish the response contract (this doc's JSON shape) to the frontend by updating `frontend/src/lib/api.ts` types — unblocks B3.

### Workstream B: Previews index page + nav (frontend)

Starts immediately against MSW mocks of the A contract.

- [x] B1. Build `/previews` page at `frontend/src/app/(dashboard)/previews/page.tsx`: three sections (Running / Ready to resume / Recent), `PageContainer`/`PageHeader`, browser title, search input, repository filter, desktop `Table` rows + stacked mobile rows, empty state with create CTA (settings deep-link shown to admins only). One query per scope, keys `["previews", scope, repositoryId, q]`, polling through `pollMs()` (5s running, 30s others).
- [x] B2. Wire row actions to existing client methods: `Open preview` (stable URL), `Stop`, `Resume` → `restart`, `Start latest` → `start-latest`, `Retry`, `View logs` (detail page deep link). Mutations invalidate all `["previews", ...]` keys. Hide mutation affordances from viewers (server still enforces).
- [x] B3. Replace MSW-only types with the real client from A6; verify against a local backend.
- [x] B4. Add `Previews` to `authenticated-layout.tsx` after Autopilot with running-count badge fed by the list query's `meta.counts` (lazy 60s poll via `pollMs()` when the page is closed). Add a command-palette entry (`Go to Previews`).
- [x] B5. Add a breadcrumb/back link from `/previews/new` and `/previews/[id]` to the index.
- [x] B6. Tests in `page-*.test.tsx` chunks: section rendering per scope, empty state, action wiring, viewer affordance hiding, badge count, mobile stacked layout. `npm run typecheck && npm run lint && npm run build` pass.

### Workstream C: Schema + enum foundation (backend, small — land first among C/D/E)

- [x] C1. Migration `000176_preview_policies` (up + down; renumber vs `origin/main` immediately before push): `repository_preview_policies` table, `preview_targets.last_snapshot_key`, `preview_instances.stopped_reason` + CHECK, minus whatever A1 already landed.
- [x] C2. Typed enums in `internal/models`: `PreviewAutoMode` (`off`/`warm`/`on`) and `PreviewStoppedReason` (`''`/`user`/`expired`/`warm_policy`/`pr_closed`/`drain`/`error`) with `Validate()`.
- [x] C3. Migration-pin tests asserting each Go enum set matches its SQL CHECK list.
- [x] C4. `OrgSettings` JSONB field `preview_auto_pool_max_active` with default 4, bounds 1–20, normalization alongside `PreviewMaxPreviewsPerUser`, plus settings parse/normalize tests.
- [x] C5. Model structs + store methods: policy upsert/get/list-with-repos (left join, absent row = `off`), target snapshot-key update, instance stopped-reason setter. Unit tests cover the new warm-resume and stopped-reason store paths.

### Workstream D: Warm resume (backend + small frontend) — depends on C

- [x] D1. Worker: after successful launch + startup-snapshot save in `start_branch_preview`, persist the snapshot key to `preview_targets.last_snapshot_key`.
- [x] D2. Set `stopped_reason` on every existing stop path: user stop endpoint → `user`, TTL sweeper → `expired`, drain → `drain`, startup failure cleanup → `error`.
- [x] D3. Implement real resumable derivation in the list endpoint (replaces A2's hardcoded `false`): terminal status in (`stopped`,`expired`) + PR still open when PR-sourced (via `pr_preview_state`) + `last_snapshot_key != ''` + `preview_startup_cache` row on a healthy non-draining worker. Worker-health lookup must be non-blocking; on failure return `resumable: false`. `resume_estimate_seconds = 30` on healthy hit, else `null`.
- [x] D4. Restart scheduling: when the target has a live snapshot row, pin worker selection to its `worker_node_id` before normal selection; degrade silently to cold start when the snapshot is gone. No API contract change.
- [x] D5. Metrics: `preview_resume_total{path=snapshot_hit|cold_degraded}`.
- [x] D6. Tests: resumable derivation truth table coverage for snapshot availability, worker health/drain behavior, PR-closed exclusion, pinned-restart selection/degradation, and stopped-reason coverage.
- [x] D7. Frontend (with B's owner): light the "Ready to resume" section copy — stopped-reason strings ("hibernated by policy" / "stopped by you" / "expired") and the `~30s` estimate from `resume_estimate_seconds`.

### Workstream E: Auto-preview policy (backend + settings UI) — depends on C; independent of D

- [x] E1. `GET /api/v1/previews/policies`: one row per connected repo with `auto_mode`, `open_pr_count` (from `pr_preview_state`), `updated_at`.
- [x] E2. `PUT /api/v1/repositories/{id}/preview-policy`: upsert with `PreviewAutoMode` validation, admin-only RBAC route grouping, audit event (repo, previous mode, new mode, actor).
- [x] E3. Job plumbing: add `initiator` (`user`/`api`/`auto_policy`) and `stop_after_ready` fields to `start_branch_preview`; auto-preview jobs run at lower priority.
- [x] E4. Webhook handler: on PR `opened`/`reopened`/`synchronize` (non-draft, non-fork, default-branch target), evaluate policy → find-or-create target (`source_type='pull_request'`) → skip if an active instance exists → enqueue with `initiator='auto_policy'`, `stop_after_ready=(mode=='warm')`. On `synchronize`, stop the previous head's runtime with `warm_policy`. On `closed`, stop active runtimes with `pr_closed`. Redelivery idempotence via the existing target find-or-create + active-instance check.
- [x] E5. Worker: honor `stop_after_ready` — wait for readiness, save snapshot, stop cleanly with `stopped_reason='warm_policy'`; pre-readiness failures record `failed` as today.
- [ ] E6. Auto pool accounting: running auto-preview instances count against `preview_auto_pool_max_active`; `meta.pool.auto_*` in the list endpoint is live; metrics `preview_auto_builds_total{mode,result}` and `preview_auto_pool_saturation` are live. Remaining gap: true queue-at-cap behavior. The current implementation suppresses auto starts at cap and records `pool_full`; implementing queueing requires a deferred auto-preview start job that does not reserve a `preview_instance` until capacity is available.
- [x] E7. Settings UI: add tab structure to `/settings/previews` (**Auto-preview** first; existing Secrets and API tokens content moves into tabs unchanged). Auto-preview tab: policy table with per-row three-option segmented control, autosave-per-row with the Runtime-settings indicator, pool-size input writing `preview_auto_pool_max_active` through the existing org-settings PATCH. Page tests including mobile stacked layout are implemented.
- [x] E8. Enable `on` mode last (it is the same pipeline with `stop_after_ready=false`): verify idle-TTL reclaim degrades an `on` preview to resumable (D's derivation picks it up), then remove any temporary gating.

### Ship order

1. A + B together → index live with Running/Recent sections (resumable section renders empty), nav promoted.
2. C → D → resumable section lights up; Resume is fast.
3. C → E (parallel with D) → `warm` policy + settings tab; E8 (`on` mode) ships once D and E6 are both verified in production.

Each numbered ship is independently valuable; nothing blocks the index on policy work.

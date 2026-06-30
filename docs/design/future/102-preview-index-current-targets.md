# Design: Current-Oriented Preview Index

> **Status:** Not Started | **Last reviewed:** 2026-06-30

The `/previews` index should show the thing users are trying to use every day: the current runnable preview for a branch or pull request. Runtime attempts, pinned commits, and older preview targets remain available for debugging and audit, but they should not be the default table row.

This builds on the implemented branch-preview primitive in [implemented/83-branch-and-pr-previews.md](../implemented/83-branch-and-pr-previews.md). The existing storage model is useful: `preview_targets` identify repo/branch/commit/config targets, and `preview_instances` identify runtime attempts. The product index needs a coarser grouping layer above that model so ten starts on the same branch do not look like ten separate previews.

## Problem

The current preview list is target-oriented at the database level. `ListBranchPreviewIndex` returns one row per `preview_targets` row, with the newest runtime attempt embedded. Because `preview_targets` are unique by `(org_id, repository_id, branch, commit_sha, preview_config_name)`, repeated starts on the same branch can appear as multiple top-level rows when the branch head changes or when manual/API callers do not supply stable source metadata.

That is technically accurate, but it creates the wrong day-to-day mental model:

- A non-technical builder expects "the preview for this branch" or "the preview for this PR."
- An engineer expects the default page to answer "what can I open, resume, or restart now?"
- Older commits and failed attempts matter, but mostly as history behind the current branch/PR surface.

Session detail already feels closer to the desired product shape because it presents one current preview surface for the session. The preview index should do the same for branch and PR previews.

## Product Principle

The Preview page should answer one question first:

`Can I open this branch or PR right now?`

A preview row is therefore a **current preview surface**, not an immutable runtime attempt.

## Goals

1. Show one default row per PR or branch/config grouping.
2. Make the latest usable state obvious: ready, starting, warm/resumable, failed, stopped, out of date, or not started.
3. Keep older commits and runtime attempts available in a detail-page `History` section.
4. Preserve durable URLs and immutable IDs for diagnostics, API callers, audit logs, and support.
5. Avoid destructive deduplication of historical data.
6. Preserve advanced workflows where a user intentionally pins a commit or runs a separate config.

## Non-Goals

1. Deleting or merging existing `preview_targets` rows.
2. Removing immutable runtime attempt visibility.
3. Supporting public unauthenticated previews by default.
4. Turning previews into always-on staging environments.
5. Replacing provider-native deploy previews such as Vercel or Netlify.

## User-Facing Model

Use three terms consistently:

| Term | User meaning | Implementation backing |
|---|---|---|
| Preview | The current preview surface for a PR or branch/config. | Computed group over preview targets and session runtime-only previews. |
| Attempt | A start/restart/resume event for a preview. | `preview_instances`, plus startup phase/log records. |
| Pinned preview | A deliberately selected commit/config that should remain separate from branch latest. | Existing `preview_targets` with explicit pin metadata. |

Do not expose "target" in UI copy unless the user is looking at raw API/debug metadata.

## Group Identity

The preview index should group rows by a stable **current key**:

```text
org_id
repository_id
preview_config_name
source family
source identity or branch
```

Grouping rules, in priority order (first match wins):

1. Pull request previews group by `(repository_id, preview_config_name, pull_request_number)`.
2. API/manual/automation previews with a non-empty `source_id` that is not a PR group by `(repository_id, preview_config_name, source_type, source_id)`.
3. Branch previews group by `(repository_id, preview_config_name, branch)`.
4. Session runtime-only previews group by `session_id` until attached to a branch target (see session-only handling below).
5. Explicit pinned-commit previews are separate only when the caller sets the pinned flag.

The precedence chain is PR > source > branch. An automation-triggered preview that references a PR `source_id` is classified as a PR group (rule 1), not a source group (rule 2). `UpsertPreviewGroupForTarget` must apply the rules in this order.

For a PR, the PR grouping wins over branch grouping because a PR is the review object users recognize. The response should still include the branch.

**Session-only previews:** A preview that has no `preview_target_id` (started directly from a session without a branch push) uses `group_kind = 'session'` and stores the session ID in `source_id`. The `branch` column stores an empty string for session groups; the `preview_groups_branch_not_blank` constraint is scoped to exclude `group_kind = 'session'` (see DDL). Session groups are upgraded to a branch or PR group automatically when the session's preview is attached to a `preview_target`.

## Freshness Semantics

`PreviewCurrentFreshness` is derived by comparing `preview_groups.latest_commit_sha` against the commit SHA of the currently running preview instance:

| Value | Meaning |
|---|---|
| `current` | Running SHA matches `latest_commit_sha`. |
| `outdated` | Running SHA differs from `latest_commit_sha`, or the running preview's config hash differs from the currently resolved config. Config staleness (`.143/config.json` changes) triggers `outdated` the same as a commit delta. |
| `pinned` | Group has `pinned = true`; freshness comparisons are suppressed. |
| `unknown` | `latest_commit_sha` could not be resolved — branch was deleted, repo access was revoked, or the GitHub token expired. Groups in `unknown` freshness are surfaced inline as attention rows with an explanatory message. |

`latest_commit_sha` is updated by:

1. **On preview start/restart**: the start path resolves the branch or PR head via GitHub API and writes it to `preview_groups.latest_commit_sha` before creating the target.
2. **On GitHub push webhook**: the push handler resolves the affected branch/PR and calls `UpdatePreviewGroupLatestSHA` for any matching group. This is the primary path for surfacing staleness while a preview is already running.
3. **On `GetPreviewCurrentSummary`**: the detail read path may refresh `latest_commit_sha` from GitHub if the stored value is older than a configurable threshold (default 5 minutes), to handle low-traffic groups that receive no push events.

Groups with unresolvable branches are written with `freshness = 'unknown'` and are not silently left as `current`.

## Product Surface

### `/previews` Index

Default sections remain useful, but they should operate on grouped current previews:

- `Running`
- `Ready to resume`
- `Recent`

Attention is an inline priority state, not a separate top-level section. Failed, unavailable, blocked, out-of-date, and freshness-unknown previews should be promoted within the section that matches their runtime state: running stale previews remain in `Running`, resumable previews remain in `Ready to resume`, and stopped or failed previews appear at the top of `Recent`. This keeps the page simpler while still preventing actionable problems from being buried under ordinary stopped previews.

Table columns:

| Column | Content |
|---|---|
| Preview | Branch or `PR #123`, repo, optional config name. Pinned rows show a `Pinned` badge. |
| State | Badge plus short explanation: `Ready`, `Starting`, `Out of date`, `Failed`, `Warm`, `Stopped`. |
| Source | PR, Session, API, Automation, Manual, with link when available. |
| Last attempt | Short SHA, start time, stopped/failed time when relevant. |
| Actions | Contextual primary action (see Row Behavior) plus `History`. |

Default row examples:

```text
PR #42 - feature/checkout
assembledhq/143 - web
Ready - a1b2c3d - expires in 41m
Open | Restart | History
```

```text
feature/checkout
assembledhq/143
Out of date - running a1b2c3d, branch is d4e5f6a
Open stale | Start | History
```

```text
feature/checkout
assembledhq/143
Starting - d4e5f6a - install phase
Cancel | History
```

```text
feature/checkout
assembledhq/143
Failed - install failed
Retry | History
```

```text
feature/checkout [Pinned - a1b2c3d]
assembledhq/143
Stopped - 2h ago
Start pinned | History
```

### Row Behavior

The primary action adapts to the current group state so non-technical builders never see conflicting choices:

| Group state | Primary action label | Secondary |
|---|---|---|
| Ready, current | `Open` | `Restart`, `Stop` |
| Ready, outdated | `Start` (latest head) | `Open stale`, `Stop` |
| Starting / recycling | `Cancel` | — |
| Warm / resumable | `Resume` | `Restart` |
| Failed (latest head) | `Retry` | `Start` |
| Failed (older head) | `Start` | — |
| Stopped / expired | `Start` | — |
| Pinned, any state | `Start pinned` or `Open` | `Unpin`, `History` |

Action semantics:

- `Open` opens the current ready runtime.
- `Open stale` is secondary-only and appears when a ready runtime is behind branch/PR head.
- `Start` resolves the latest branch/PR head, creates or reuses the matching target, and starts a runtime. When nothing is running this is always the label; use `Restart` only when a runtime must be stopped first.
- `Restart` stops the active runtime and starts from the latest branch/PR head. Only shown when a runtime is currently active.
- `Resume` reattaches to a warm paused runtime.
- `Retry` repeats the same target only when the failed target is already for the latest head.
- `Cancel` stops an in-progress start. Only shown during `starting`/`recycling` states.
- `History` opens the detail page history drawer or a dedicated section.

### Pinned Previews

Pinned rows appear in the index alongside normal rows. They are visually differentiated by a `Pinned` badge next to the branch/PR name and a secondary `Unpin` action. Pinned rows do not show freshness warnings — `outdated` is suppressed and the `Start` action label reads `Start pinned` to reinforce that the user is working with a fixed commit.

Pinned rows appear in whichever scope section matches their current runtime state (`Running`, `Ready to resume`, or `Recent`). They are not given a dedicated section in v1. Engineers who want to find all pinned rows can filter by `pinned=true` in the query string.

### Detail Page

`/previews/{id}` should remain valid for both group IDs and target IDs:

- If `id` is a group ID, show the current surface and history.
- If `id` is a target ID, resolve to the containing group and focus the matching target/attempt in history.
- If `id` is a runtime instance ID for a session-only preview, keep the existing fallback behavior.

Detail page layout:

1. Header: repo, PR/branch, config, current state, latest SHA.
2. Primary action: contextual label per Row Behavior table above.
3. Current attempt: phases, services, infrastructure, logs.
4. Freshness: running SHA vs branch/PR head SHA, with reason when `unknown`.
5. History: grouped list of targets and runtime attempts.
6. Metadata: created by, source link, API request/source IDs, quota impact.

### History Section

History should be compact by default:

```text
Latest branch head
d4e5f6a - target a3f8…c12d - failed - 8m ago
  attempt b9d1…7e3a - install failed - logs

Previous
a1b2c3d - target 7c2b…91fa - ready - started 44m ago
  attempt f4e8…30bc - stopped by user
  attempt 2d71…a95c - expired
```

Engineers can expand individual attempts to see logs, phase steps, services, infra, request IDs, and worker ownership. Non-technical users can ignore it.

## API Design

Keep existing target/runtime APIs for compatibility. Add current-oriented endpoints and response types.

### List Current Previews

```http
GET /api/v1/previews/current?scope=running|resumable|attention|recent&repository_id=&pinned=&q=&cursor=&limit=
```

Response:

```json
{
  "data": [
    {
      "preview_group_id": "uuid",
      "group_kind": "pull_request",
      "repository_id": "uuid",
      "repository_full_name": "assembledhq/143",
      "branch": "feature/checkout",
      "pull_request_number": 42,
      "preview_config_name": "web",
      "source_type": "pull_request",
      "source_id": "assembledhq/143#42",
      "source_url": "https://github.com/assembledhq/143/pull/42",
      "status": "ready",
      "freshness": "current",
      "latest_commit_sha": "def5678...",
      "running_commit_sha": "def5678...",
      "current_target_id": "uuid",
      "current_preview_id": "uuid",
      "preview_url": "https://...",
      "stable_url": "https://app.143.dev/previews/current/uuid",
      "pinned": false,
      "created_at": "2026-06-15T12:00:00Z",
      "last_activity_at": "2026-06-15T12:04:00Z",
      "expires_at": "2026-06-15T12:44:00Z",
      "stopped_at": null,
      "stopped_reason": "",
      "error": "",
      "current_phase": "ready",
      "attempt_count": 10,
      "target_count": 3,
      "resumable": false,
      "resume_estimate_seconds": null,
      "launch": {
        "action": "open",
        "primary_label": "Open",
        "secondary_label": "Restart",
        "message": ""
      }
    }
  ],
  "meta": {
    "next_cursor": "",
    "counts": {
      "running": 4,
      "resumable": 2,
      "attention": 1,
      "recent": 8
    },
    "pool": {
      "user_active": 1,
      "user_max": 4,
      "auto_active": 2,
      "auto_max": 6
    }
  }
}
```

`scope` semantics:

| Scope | Meaning |
|---|---|
| `running` | Current group has active runtime status such as `starting`, `ready`, or `recycling`. |
| `resumable` | Current group is warm and can resume quickly. |
| `attention` | Current group is failed, unavailable, blocked, capacity-blocked, config-invalid, out of date, or freshness-unknown. |
| `recent` | Current group has terminal activity in the last 7 days and is not already in another displayed section. |

The `recent` 7-day window is a server constant (`previewRecentWindowDays = 7`) rather than a client parameter in v1.

### Get Current Preview

```http
GET /api/v1/previews/current/{preview_group_id}
```

Returns `PreviewCurrentResponse` plus current attempt details, phase steps, services, infrastructure, and the first page of history.

### Get Preview History

```http
GET /api/v1/previews/current/{preview_group_id}/history?cursor=&limit=25
```

Response:

```json
{
  "data": [
    {
      "target_id": "uuid",
      "commit_sha": "def5678...",
      "preview_config_name": "web",
      "source_type": "pull_request",
      "source_id": "assembledhq/143#42",
      "created_at": "2026-06-15T12:00:00Z",
      "is_latest_head": true,
      "attempts": [
        {
          "preview_id": "uuid",
          "status": "failed",
          "created_at": "2026-06-15T12:04:00Z",
          "stopped_at": "2026-06-15T12:06:00Z",
          "stopped_reason": "error",
          "current_phase": "preview.install",
          "error": "install failed",
          "preview_url": null,
          "logs_url": "/api/v1/previews/uuid/logs"
        }
      ]
    }
  ],
  "meta": { "next_cursor": "" }
}
```

### Start Latest Current Preview

```http
POST /api/v1/previews/current/{preview_group_id}/start-latest
```

Behavior:

1. Resolve the current branch or PR head.
2. Reuse an existing target if one already exists for that head/config.
3. If a ready runtime already exists for the latest head, return it without starting a duplicate unless `restart=true`.
4. Start a new runtime attempt when needed.
5. Return the updated `PreviewCurrentResponse`.

Request:

```json
{
  "restart": false,
  "ttl_seconds": 3600
}
```

### Restart Current Preview

```http
POST /api/v1/previews/current/{preview_group_id}/restart
```

Equivalent to `start-latest` with `restart=true`, but the dedicated route keeps UI intent and audit events clear.

### Stop Current Preview

```http
POST /api/v1/previews/current/{preview_group_id}/stop
```

Stops the active runtime for the current group. It does not delete targets or attempts.

### Compatibility

Existing endpoints remain:

- `GET /api/v1/previews`
- `GET /api/v1/previews/{preview_id}`
- `POST /api/v1/previews/{preview_id}/restart`
- `POST /api/v1/previews/{preview_id}/start-latest`
- `POST /api/v1/previews/{preview_id}/stop`

Frontend should migrate the `/previews` index to `/api/v1/previews/current`. Existing detail links, PR links, API clients, and CLI callers continue to work.

## Data Model

Prefer a materialized table for stable group IDs and cheap list queries.

### `preview_groups`

```sql
CREATE TABLE preview_groups (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id),
    repository_id uuid NOT NULL REFERENCES repositories(id),
    group_kind text NOT NULL,
    branch text NOT NULL DEFAULT '',
    preview_config_name text NOT NULL DEFAULT '',
    pull_request_number integer,
    source_type text NOT NULL DEFAULT '',
    source_id text NOT NULL DEFAULT '',
    source_url text NOT NULL DEFAULT '',
    current_target_id uuid REFERENCES preview_targets(id) ON DELETE SET NULL,
    latest_commit_sha text NOT NULL DEFAULT '',
    current_status text NOT NULL DEFAULT 'none',
    pinned boolean NOT NULL DEFAULT false,
    created_by_user_id uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    last_activity_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT preview_groups_kind_check
        CHECK (group_kind IN ('pull_request', 'branch', 'source', 'session', 'pinned')),
    -- branch is required for all group kinds except session, which has no branch yet
    CONSTRAINT preview_groups_branch_not_blank
        CHECK (group_kind = 'session' OR length(trim(branch)) > 0)
);

CREATE UNIQUE INDEX idx_preview_groups_identity
    ON preview_groups (
        org_id,
        repository_id,
        group_kind,
        branch,
        preview_config_name,
        COALESCE(pull_request_number, 0),
        source_type,
        source_id,
        pinned
    );

-- primary list query: org + status scope, ordered by activity
CREATE INDEX idx_preview_groups_org_status_activity
    ON preview_groups (org_id, current_status, last_activity_at DESC, id DESC);

-- fallback for unscoped activity-ordered queries
CREATE INDEX idx_preview_groups_org_activity
    ON preview_groups (org_id, last_activity_at DESC, id DESC);
```

`current_status` is a denormalized copy of the latest `preview_instances.status` for `current_target_id`. It is written by `UpsertPreviewGroupStatus` whenever a preview instance transitions state, and by `UpsertPreviewGroupForTarget` when a new current target is set. This avoids a three-table JOIN on every list page load; the source of truth remains `preview_instances`.

Every `preview_groups` row has `org_id` and is org-scoped. It is not insert-only; this is a current index object, not audit history.

### `preview_targets`

Add a nullable group pointer:

```sql
ALTER TABLE preview_targets
    ADD COLUMN preview_group_id uuid REFERENCES preview_groups(id) ON DELETE SET NULL;

CREATE INDEX idx_preview_targets_group_created
    ON preview_targets (org_id, preview_group_id, created_at DESC, id DESC);
```

### Backfill Plan

Backfill group IDs from existing targets as a background job, not an inline migration, to avoid long-running row locks on `preview_targets`:

1. A migration adds the `preview_group_id` column (nullable, no backfill in the migration itself).
2. A separate one-time background job (`BackfillPreviewGroups`) processes `preview_targets` in batches of 500, ordered by `created_at ASC`, using `UpsertPreviewGroupForTarget` for each row. The job is idempotent: rows already pointing at a group are skipped.
3. Backfill classification order: PR targets (parseable `source_id` matching `owner/repo#N`) → non-PR source targets (non-empty `source_id`) → remaining targets (branch groups).
4. Session runtime-only previews remain represented through a fallback query until they are attached to targets; they do not need backfill rows.
5. The backfill job records progress in a `background_jobs` or equivalent table so operators can observe and resume it after a deploy.
6. New preview create/start paths upsert groups synchronously; the backfill only covers historical rows.

### Derived Status

`preview_groups.current_status` is updated by:

- `UpsertPreviewGroupForTarget`: sets `current_status` from the newest instance for the new current target.
- `UpsertPreviewGroupStatus(ctx, orgID, groupID, status)`: called by the preview instance state machine whenever `preview_instances.status` changes for the group's `current_target_id`. This is the hot path.

List queries filter on `current_status` directly using `idx_preview_groups_org_status_activity` — no join to `preview_instances` is needed for scoped list results. The full instance row is joined only when building `PreviewCurrentSummary` for the response payload (single-row get or small result set).

Scope-to-status mapping for list queries:

| Scope | `current_status` values |
|---|---|
| `running` | `starting`, `ready`, `recycling` |
| `resumable` | `warm` |
| `attention` | `failed`, `unavailable`, `blocked`, `capacity_blocked`, `config_invalid`, `outdated` (freshness-derived) |
| `recent` | any terminal status, `last_activity_at` within 7 days |

## Go Models

Add typed strings in `internal/models`. `PreviewSourceType` is the existing type from the branch-preview model; import it rather than redefining it.

```go
type PreviewGroupKind string

const (
    PreviewGroupKindPullRequest PreviewGroupKind = "pull_request"
    PreviewGroupKindBranch      PreviewGroupKind = "branch"
    PreviewGroupKindSource      PreviewGroupKind = "source"
    PreviewGroupKindSession     PreviewGroupKind = "session"
    PreviewGroupKindPinned      PreviewGroupKind = "pinned"
)

func (k PreviewGroupKind) Validate() error { ... }

type PreviewCurrentFreshness string

const (
    PreviewCurrentFreshnessCurrent  PreviewCurrentFreshness = "current"
    PreviewCurrentFreshnessOutdated PreviewCurrentFreshness = "outdated"
    PreviewCurrentFreshnessUnknown  PreviewCurrentFreshness = "unknown"
    PreviewCurrentFreshnessPinned   PreviewCurrentFreshness = "pinned"
)

func (f PreviewCurrentFreshness) Validate() error { ... }

type PreviewLaunchAction string

const (
    PreviewLaunchActionOpen        PreviewLaunchAction = "open"
    PreviewLaunchActionWait        PreviewLaunchAction = "wait"
    PreviewLaunchActionStart       PreviewLaunchAction = "start"
    PreviewLaunchActionStartLatest PreviewLaunchAction = "start_latest"
    PreviewLaunchActionRestart     PreviewLaunchAction = "restart"
    PreviewLaunchActionResume      PreviewLaunchAction = "resume"
    PreviewLaunchActionRetry       PreviewLaunchAction = "retry"
    PreviewLaunchActionCancel      PreviewLaunchAction = "cancel"
    PreviewLaunchActionBlocked     PreviewLaunchAction = "blocked"
    PreviewLaunchActionClosed      PreviewLaunchAction = "closed"
    PreviewLaunchActionNone        PreviewLaunchAction = "none"
)

func (a PreviewLaunchAction) Validate() error { ... }
```

Store/query structs:

```go
type PreviewGroup struct {
    ID                uuid.UUID         `db:"id"                  json:"id"`
    OrgID             uuid.UUID         `db:"org_id"              json:"org_id"`
    RepositoryID      uuid.UUID         `db:"repository_id"       json:"repository_id"`
    GroupKind         PreviewGroupKind  `db:"group_kind"          json:"group_kind"`
    Branch            string            `db:"branch"              json:"branch"`
    PreviewConfigName string            `db:"preview_config_name" json:"preview_config_name"`
    PullRequestNumber *int              `db:"pull_request_number" json:"pull_request_number,omitempty"`
    SourceType        PreviewSourceType `db:"source_type"         json:"source_type"`
    SourceID          string            `db:"source_id"           json:"source_id,omitempty"`
    SourceURL         string            `db:"source_url"          json:"source_url,omitempty"`
    CurrentTargetID   *uuid.UUID        `db:"current_target_id"   json:"current_target_id,omitempty"`
    LatestCommitSHA   string            `db:"latest_commit_sha"   json:"latest_commit_sha,omitempty"`
    CurrentStatus     string            `db:"current_status"      json:"current_status"`
    Pinned            bool              `db:"pinned"              json:"pinned"`
    CreatedByUserID   *uuid.UUID        `db:"created_by_user_id"  json:"created_by_user_id,omitempty"`
    CreatedAt         time.Time         `db:"created_at"          json:"created_at"`
    LastActivityAt    time.Time         `db:"last_activity_at"    json:"last_activity_at"`
}

// PreviewCurrentSummary is built from a JOIN query. Fields that come from
// preview_instances or are computed server-side do not carry db: tags
// because they require explicit column aliases in the query (e.g.
// "preview_instances.id AS current_preview_id"). The query must alias all
// joined columns to avoid sqlx scanning ambiguity with embedded PreviewGroup.
type PreviewCurrentSummary struct {
    PreviewGroup
    RepositoryFullName    string                      `json:"repository_full_name"`
    Status                PreviewStatus               `json:"status"`
    Freshness             PreviewCurrentFreshness     `json:"freshness"`
    RunningCommitSHA      string                      `json:"running_commit_sha,omitempty"`
    CurrentPreviewID      *uuid.UUID                  `json:"current_preview_id,omitempty"`
    PreviewURL            *string                     `json:"preview_url,omitempty"`
    StableURL             string                      `json:"stable_url"`
    ExpiresAt             *time.Time                  `json:"expires_at,omitempty"`
    StoppedAt             *time.Time                  `json:"stopped_at,omitempty"`
    StoppedReason         string                      `json:"stopped_reason,omitempty"`
    Error                 string                      `json:"error,omitempty"`
    CurrentPhase          string                      `json:"current_phase,omitempty"`
    AttemptCount          int                         `json:"attempt_count"`
    TargetCount           int                         `json:"target_count"`
    Resumable             bool                        `json:"resumable"`
    ResumeEstimateSeconds *int                        `json:"resume_estimate_seconds,omitempty"`
    Launch                PreviewLaunchRecommendation `json:"launch"`
}

type PreviewLaunchRecommendation struct {
    Action         PreviewLaunchAction `json:"action"`
    PrimaryLabel   string              `json:"primary_label"`
    SecondaryLabel string              `json:"secondary_label,omitempty"`
    Message        string              `json:"message,omitempty"`
}
```

`StableURL` is computed by the handler from the group ID and the configured app origin; it is not stored in the database. API response structs should live near the handler boundary if they differ from model structs, but enum-like fields must use typed strings from `internal/models`.

## Store API

Add `internal/db/preview_groups.go` or extend `PreviewStore` with grouped methods. Every method takes `orgID` immediately after `ctx`.

```go
type PreviewCurrentIndexFilters struct {
    RepositoryID *uuid.UUID
    Scope        string
    Pinned       *bool
    Query        string
    CursorTime   *time.Time
    CursorID     *uuid.UUID
    Limit        int
}

// UpsertPreviewGroupForTarget classifies the target using the precedence
// chain (PR > source > branch), upserts the preview_groups row, and sets
// current_target_id + current_status + latest_commit_sha if the new target
// is more recent than the current one (compare last_activity_at).
//
// Conflict resolution: uses INSERT ... ON CONFLICT (identity index) DO UPDATE.
// Fields updated on conflict: current_target_id, latest_commit_sha,
// current_status, last_activity_at. Fields never overwritten on conflict:
// group_kind, created_by_user_id, created_at. The update is conditional:
// EXCLUDED.last_activity_at > preview_groups.last_activity_at to prevent
// a stale concurrent upsert from rolling back the current pointer.
func (s *PreviewStore) UpsertPreviewGroupForTarget(
    ctx context.Context,
    orgID uuid.UUID,
    target models.PreviewTarget,
    latestCommitSHA string,
) (*models.PreviewGroup, error)

// UpsertPreviewGroupStatus updates current_status on the group row when a
// preview_instances status transition occurs. Called from the preview
// instance state machine; must be fast and non-blocking.
func (s *PreviewStore) UpsertPreviewGroupStatus(
    ctx context.Context,
    orgID uuid.UUID,
    groupID uuid.UUID,
    status string,
) error

// AttachTargetToPreviewGroup sets preview_targets.preview_group_id for
// targets that already exist (e.g. during backfill or when a session-only
// preview is promoted to a branch target). Use UpsertPreviewGroupForTarget
// for new targets created through the normal start path.
func (s *PreviewStore) AttachTargetToPreviewGroup(
    ctx context.Context,
    orgID uuid.UUID,
    targetID uuid.UUID,
    groupID uuid.UUID,
) error

// UpdatePreviewGroupLatestSHA is called by the GitHub push webhook handler
// to record a new branch/PR head without changing the current target.
func (s *PreviewStore) UpdatePreviewGroupLatestSHA(
    ctx context.Context,
    orgID uuid.UUID,
    groupID uuid.UUID,
    latestCommitSHA string,
) error

func (s *PreviewStore) GetPreviewCurrentSummary(
    ctx context.Context,
    orgID uuid.UUID,
    groupID uuid.UUID,
) (models.PreviewCurrentSummary, error)

func (s *PreviewStore) ListPreviewCurrentIndex(
    ctx context.Context,
    orgID uuid.UUID,
    filters PreviewCurrentIndexFilters,
) ([]models.PreviewCurrentSummary, error)

func (s *PreviewStore) CountPreviewCurrentIndexScopes(
    ctx context.Context,
    orgID uuid.UUID,
    filters PreviewCurrentIndexFilters,
) (map[string]int, error)

func (s *PreviewStore) ListPreviewGroupHistory(
    ctx context.Context,
    orgID uuid.UUID,
    groupID uuid.UUID,
    cursorTime *time.Time,
    cursorID *uuid.UUID,
    limit int,
) ([]models.PreviewTargetHistory, error)
```

`UpsertPreviewGroupForTarget` owns the grouping rules and must parse PR source IDs through the shared `ParsePRSourceID(sourceID string) (owner, repo string, number int, ok bool)` helper rather than ad hoc regex in multiple callsites.

## Handler API

Add methods to `BranchPreviewHandler`:

```go
func (h *BranchPreviewHandler) ListCurrent(w http.ResponseWriter, r *http.Request)
func (h *BranchPreviewHandler) GetCurrent(w http.ResponseWriter, r *http.Request)
func (h *BranchPreviewHandler) CurrentHistory(w http.ResponseWriter, r *http.Request)
func (h *BranchPreviewHandler) StartLatestCurrent(w http.ResponseWriter, r *http.Request)
func (h *BranchPreviewHandler) RestartCurrent(w http.ResponseWriter, r *http.Request)
func (h *BranchPreviewHandler) StopCurrent(w http.ResponseWriter, r *http.Request)
```

Router additions:

```go
r.Get("/api/v1/previews/current", branchPreviewHandler.ListCurrent)
r.Get("/api/v1/previews/current/{preview_group_id}", branchPreviewHandler.GetCurrent)
r.Get("/api/v1/previews/current/{preview_group_id}/history", branchPreviewHandler.CurrentHistory)
r.Post("/api/v1/previews/current/{preview_group_id}/start-latest", branchPreviewHandler.StartLatestCurrent)
r.Post("/api/v1/previews/current/{preview_group_id}/restart", branchPreviewHandler.RestartCurrent)
r.Post("/api/v1/previews/current/{preview_group_id}/stop", branchPreviewHandler.StopCurrent)
```

Register these routes before the existing `/api/v1/previews/{preview_id}` route so `current` is never parsed as a preview ID.

Permissions mirror existing preview routes:

- `viewer`: read only.
- `builder`, `member`, `admin`: start/restart/stop if repository access allows it.
- Preview/API tokens need `previews:read`, `previews:create`, or `previews:stop`.

**PR visibility:** A caller who has repository access but limited PR visibility (e.g. a private fork) will see the group's `source_url` and `pull_request_number` in the response. Handlers must confirm the caller has at least `viewer` role on the owning org before returning any group data; PR-number exposure follows the same rules as existing branch-preview endpoints and is not further restricted in v1.

## Frontend Types

Add to `frontend/src/lib/types.ts` alongside existing `PreviewStatus` and `PreviewSourceType`:

```ts
export type PreviewGroupKind =
  | "pull_request"
  | "branch"
  | "source"
  | "session"
  | "pinned";

export type PreviewCurrentFreshness = "current" | "outdated" | "unknown" | "pinned";

export type PreviewLaunchAction =
  | "open"
  | "wait"
  | "start"
  | "start_latest"
  | "restart"
  | "resume"
  | "retry"
  | "cancel"
  | "blocked"
  | "closed"
  | "none";

export interface PreviewLaunchRecommendation {
  action: PreviewLaunchAction;
  primary_label: string;
  secondary_label?: string;
  message?: string;
}

export interface PreviewCurrentResponse {
  preview_group_id: string;
  group_kind: PreviewGroupKind;
  repository_id: string;
  repository_full_name?: string;
  branch: string;
  pull_request_number?: number;
  preview_config_name?: string;
  source_type: PreviewSourceType;
  source_id?: string;
  source_url?: string;
  status: PreviewStatus;
  freshness: PreviewCurrentFreshness;
  latest_commit_sha?: string;
  running_commit_sha?: string;
  current_target_id?: string;
  current_preview_id?: string;
  preview_url?: string;
  stable_url: string;
  pinned: boolean;
  created_at: string;
  last_activity_at: string;
  expires_at?: string;
  stopped_at?: string;
  stopped_reason?: string;
  error?: string;
  current_phase?: string;
  attempt_count: number;
  target_count: number;
  resumable: boolean;
  resume_estimate_seconds?: number;
  launch: PreviewLaunchRecommendation;
}
```

API client (extend the existing client in `frontend/src/lib/api.ts`):

```ts
api.previews.current = {
  list(params: PreviewCurrentListParams): Promise<PreviewCurrentListResponse>,
  get(groupId: string): Promise<PreviewCurrentResponse>,
  history(groupId: string, params: CursorParams): Promise<PreviewHistoryResponse>,
  startLatest(groupId: string, body: StartLatestBody): Promise<PreviewCurrentResponse>,
  restart(groupId: string, body: StartLatestBody): Promise<PreviewCurrentResponse>,
  stop(groupId: string): Promise<void>,
}
```

## Migration Plan

1. Add typed enums and validation tests.
2. Add `preview_groups` table and `preview_targets.preview_group_id` column (nullable, no inline backfill).
3. Deploy a one-time background backfill job (`BackfillPreviewGroups`) that processes existing `preview_targets` in batches of 500, is idempotent, and records progress. The job runs after the migration lands, not inside it. Monitor completion via the `background_jobs` table before proceeding to step 4.
4. Update preview create/start paths to call `UpsertPreviewGroupForTarget`; update the preview instance state machine to call `UpsertPreviewGroupStatus` on every status transition.
5. Wire the GitHub push webhook handler to call `UpdatePreviewGroupLatestSHA` for branches/PRs with matching groups.
6. Add grouped store methods and tenancy tests for org filters.
7. Add current-oriented API routes.
8. Add frontend types/API client and update React Query hooks for the `/previews` index.
9. Move `/previews` index to `/api/v1/previews/current`.
10. Add detail-page history section.
11. Keep legacy `/api/v1/previews` API and existing links stable.

## Testing Requirements

Backend:

- `PreviewGroupKind`, `PreviewCurrentFreshness`, and `PreviewLaunchAction` validation tests.
- `UpsertPreviewGroupForTarget` precedence tests: PR source ID beats plain branch; automation with PR source_id classifies as PR group.
- `UpsertPreviewGroupForTarget` concurrent-upsert test: two goroutines racing on the same identity produce exactly one group row.
- `UpsertPreviewGroupStatus` test: status transitions propagate to `current_status` and `last_activity_at`.
- Store tests proving two targets for the same branch but different commits collapse into one current summary.
- Store tests proving explicit pinned previews remain separate.
- Store tests proving PR grouping beats branch grouping.
- Store tests proving all grouped queries filter by `org_id`.
- Store test proving `ListPreviewCurrentIndex` scope filter uses `current_status` without joining `preview_instances`.
- Store test for `unknown` freshness when `latest_commit_sha` is blank.
- Store test for session-only group (`group_kind = 'session'`, blank branch) upsert and promotion to branch group.
- Handler tests for list/get/history/start/restart/stop current routes.
- Migration/backfill integration test: existing target rows get correct group classification after backfill.

Frontend:

- Preview index renders one row for multiple target history entries.
- Out-of-date row shows `Open stale` as secondary and `Start` as primary.
- Failed latest row shows `Retry` only when the failed target is latest head; otherwise shows `Start`.
- Starting row shows only `Cancel`.
- Pinned row shows `Pinned` badge and `Unpin` action.
- History opens and renders targets/attempts with abbreviated UUIDs.
- Viewer role can read but not mutate.

## Decisions

1. **Group IDs are opaque UUIDs in v1.** Stable human-readable paths (`/previews/current/pr/assembledhq-143/42`) are useful but require URL-encoding edge cases and add router complexity. Use opaque UUIDs now and add canonical slug paths in a follow-up once the group model is stable.

2. **Attention is an inline priority state.** A separate `Needs attention` section added visual weight even when empty and made the page feel like backend taxonomy instead of a user workflow. The UI still queries the `attention` scope, but it merges those rows into `Running`, `Ready to resume`, or `Recent`, sorts attention rows first, and gives them state-appropriate actions such as `Start latest`. This preserves the recovery path without adding another section.

3. **API-created previews default to branch grouping.** A preview created via API with no `source_id` and no PR reference groups by branch. Callers who want source-scoped groups must supply a stable `source_id`. This is the least-surprising default and matches how manual starts behave.

4. **Pinned previews have no dedicated top-level filter section in v1.** They appear inline with their current runtime state section and are visually distinguished by a badge. Engineers can filter with `?pinned=true`. A dedicated section can be added in v1 if usage data shows engineers need it.

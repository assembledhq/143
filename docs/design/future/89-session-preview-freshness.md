# Design: Session Preview Freshness

> **Status:** Future
> **Last reviewed:** 2026-05-28

Session previews can keep running after a coding-agent turn changes the workspace. The Preview tab should make that visible: when the running preview was started from an older workspace state, it shows that the preview is out of date and offers a single action to update and restart it with the latest session changes.

## Product Contract

The user should be able to trust the Preview tab answer to:

`Am I looking at the latest code from this session?`

When a preview is current, the tab stays quiet and keeps `Open Preview` as the primary action. When a preview is stale, the command header adds a compact warning and exposes `Update preview` as the primary lifecycle action until the preview is restarted from the latest workspace revision.

Suggested copy:

- Current: no extra freshness badge; ready-state metadata remains quiet.
- Out of date marker: `New changes available`
- Out of date action: `Refresh preview`
- Out of date helper text: `Restart the preview to see the latest session changes.`
- Updating: `Updating preview...`
- Unknown: `Preview freshness could not be verified.`

The stale state should be visible inside the Preview tab even before the user opens the secondary preview actions menu. The marker should look like a small inline status callout or amber-tinted badge row near the Preview command header, not a blocking modal and not a global toast. The user should understand two things at a glance: the preview still exists, and there is newer session code available.

`Refresh preview` should reuse the existing start/retry/restart mental model. The user should not have to choose between "restart", "retry", and "update"; for stale previews the product intent is "make this preview current again".

Recommended command-header layout for a ready but stale preview:

- Left: `Preview` title, quiet running metadata, and a compact `New changes available` marker.
- Right: keep `Open Preview` available because the current preview may still be useful, and add `Refresh preview` as the adjacent update action.
- Secondary menu: keep `Restart preview`, `Stop preview`, and lifetime controls under `Preview actions`.

On narrow mobile widths, stack the marker under the title and put `Refresh preview` before `Open Preview` only if there is not enough horizontal room for both. The button text should remain action-oriented; avoid vague labels such as `Update` without the preview object.

## Existing Flow

Session preview startup currently enters through:

- `POST /api/v1/sessions/{id}/preview`
- `POST /api/v1/sessions/{id}/preview/ensure`
- `POST /api/v1/sessions/{id}/preview/restart`

The API handler either starts locally or, in worker-routed production, reserves a `preview_instances` row and enqueues a durable `start_preview` job:

- `internal/api/handlers/preview.go`
  - `startPreviewFromRequest`
  - `enqueueStartPreviewJob`
  - `startPreviewLocal`
  - `GetPreview`
  - `ensurePreview`
- `internal/services/preview/manager.go`
  - `StartPreviewInput`
  - `reservePreview`
  - `LaunchPreview`
  - `RecyclePreviewWithConfig`
- `internal/services/preview/start_runner.go`
  - `StartReservedPreview`
- `internal/db/preview_store.go`
  - `CreatePreviewInstance`
  - `GetActivePreviewForSession`
  - `GetLatestTerminalPreviewForSession`

The frontend Preview tab reads status through `api.sessions.preview.get(sessionId)`, backed by `GET /api/v1/sessions/{id}/preview`, and restarts through `api.sessions.preview.ensure(sessionId)`.

Standalone branch/PR previews already expose a related but different concept through `new_commits_available`: the pinned preview target commit is older than the latest branch/PR head. Session previews need a workspace-scoped freshness signal because the latest session changes may be unpushed and only exist in the live sandbox or persisted session snapshot.

## Decision

Add a first-class session workspace revision and stamp session preview runtime attempts with the revision they were launched from.

The backend is authoritative for freshness:

- A session has a monotonically increasing `workspace_revision`.
- A session preview records `source_workspace_revision` when the preview is reserved.
- `GET /api/v1/sessions/{id}/preview` compares the preview revision with the current session revision and returns a `freshness` object.
- `Update preview` calls the existing ensure/restart flow. On success, the preview's `source_workspace_revision` is advanced to the revision used for the new launch.

The frontend never infers freshness from timestamps, preview age, or diff text.

## Schema Changes

Add revision state to `sessions`:

```sql
ALTER TABLE sessions
    ADD COLUMN workspace_revision bigint NOT NULL DEFAULT 0,
    ADD COLUMN workspace_revision_updated_at timestamptz NOT NULL DEFAULT now();
```

Add the revision that a session preview was started from:

```sql
ALTER TABLE preview_instances
    ADD COLUMN source_workspace_revision bigint,
    ADD COLUMN source_workspace_revision_updated_at timestamptz;
```

Indexing:

```sql
CREATE INDEX idx_preview_instances_session_workspace_revision
    ON preview_instances (org_id, session_id, source_workspace_revision)
    WHERE session_id IS NOT NULL;
```

Notes:

- `source_workspace_revision` is nullable for existing previews and for branch previews where the durable identity is already `(repository, branch, commit_sha, preview_config_name)`.
- `source_workspace_revision_updated_at` copies `sessions.workspace_revision_updated_at` when the preview is reserved or recycled. It is not used for correctness; it supports readable UI copy, diagnostics, and audit/log context.
- This is not insert-only versioning. Sessions and preview instances are operational lifecycle rows, so normal updates are appropriate.

## Model Changes

Add fields to `models.Session`:

```go
WorkspaceRevision          int64      `db:"workspace_revision" json:"workspace_revision"`
WorkspaceRevisionUpdatedAt time.Time  `db:"workspace_revision_updated_at" json:"workspace_revision_updated_at"`
```

Add fields to `models.PreviewInstance`:

```go
SourceWorkspaceRevision          *int64     `db:"source_workspace_revision" json:"source_workspace_revision,omitempty"`
SourceWorkspaceRevisionUpdatedAt *time.Time `db:"source_workspace_revision_updated_at" json:"source_workspace_revision_updated_at,omitempty"`
```

Add typed freshness state in `internal/models`:

```go
type PreviewFreshnessState string

const (
    PreviewFreshnessCurrent   PreviewFreshnessState = "current"
    PreviewFreshnessOutOfDate PreviewFreshnessState = "out_of_date"
    PreviewFreshnessUpdating  PreviewFreshnessState = "updating"
    PreviewFreshnessUnknown   PreviewFreshnessState = "unknown"
)
```

`PreviewFreshnessState` should get a `Validate() error` method and table-driven enum tests, following the existing typed-string pattern.

Extend `PreviewStatusResponse`:

```go
type PreviewStatusResponse struct {
    Instance       *PreviewInstance        `json:"instance"`
    Services       []PreviewService        `json:"services"`
    Infrastructure []PreviewInfrastructure `json:"infrastructure,omitempty"`
    PreviewOrigin  string                  `json:"preview_origin,omitempty"`
    Freshness      *PreviewFreshness       `json:"freshness,omitempty"`
}

type PreviewFreshness struct {
    State                              PreviewFreshnessState `json:"state"`
    CurrentWorkspaceRevision           int64                 `json:"current_workspace_revision"`
    CurrentWorkspaceRevisionUpdatedAt  time.Time             `json:"current_workspace_revision_updated_at"`
    PreviewWorkspaceRevision           *int64                `json:"preview_workspace_revision,omitempty"`
    PreviewWorkspaceRevisionUpdatedAt  *time.Time            `json:"preview_workspace_revision_updated_at,omitempty"`
    Reason                             string                `json:"reason,omitempty"`
}
```

Reasons should be stable strings, for example:

- `session_changed_after_preview_start`
- `preview_starting`
- `preview_revision_missing`
- `not_session_preview`

## Revision Semantics

`workspace_revision` represents the latest durable session workspace state that a preview can intentionally run.

Increment it when the backend records a new effective workspace version:

- When a turn completes and `SessionStore.writeDiffSnapshot` inserts a new `session_diff_snapshots` row.
- When a checkpoint or snapshot promotion makes a newer workspace available for hydrate/restart.
- When a continuation updates the session snapshot after user-guided or agent-guided changes.

Do not increment it for:

- Pure status transitions.
- Preview lifecycle changes.
- PR/branch push bookkeeping that does not change workspace files.
- `MarkLatestDiffSnapshotPushed`, because that normalizes push metadata for an already captured workspace.

The first implementation can increment in the same transaction as `session_diff_snapshots` insertion. That catches the user-visible case: an agent produced a new changed workspace and the session detail already has a new diff snapshot. Follow-up work can extend revision bumps to earlier checkpoint moments if the product wants the preview tab to mark stale before a turn has fully completed.

Recommended store method:

```go
func (s *SessionStore) BumpWorkspaceRevision(ctx context.Context, orgID, sessionID uuid.UUID, reason string) (int64, time.Time, error)
```

For diff capture, prefer a single SQL update instead of a second read:

```sql
UPDATE sessions
SET latest_diff_snapshot_id = @snapshot_id,
    diff_collected_at = @captured_at,
    workspace_revision = workspace_revision + 1,
    workspace_revision_updated_at = @captured_at
WHERE id = @session_id AND org_id = @org_id
RETURNING workspace_revision, workspace_revision_updated_at;
```

## Preview Reservation And Restart

Extend `preview.StartPreviewInput`:

```go
WorkspaceRevision          int64
WorkspaceRevisionUpdatedAt time.Time
```

Slotting:

1. `PreviewHandler.startPreviewFromRequest` already loads the session before selecting a worker. Copy `session.WorkspaceRevision` and `session.WorkspaceRevisionUpdatedAt` into `StartPreviewInput`.
2. `PreviewHandler.enqueueStartPreviewJob` reserves the preview in the API transaction. The reservation should store the copied revision on `preview_instances`.
3. Include the same revision in `StartPreviewJobPayload`. The worker should verify that the reserved row still carries the expected source revision, but it should not re-stamp the row after hydrate. The preview is intentionally tied to the revision chosen when the user clicked start/update.
4. `PreviewHandler.startPreviewLocal` uses the same input fields before calling `ReservePreview`.
5. `Manager.reservePreview` copies `input.WorkspaceRevision` and `input.WorkspaceRevisionUpdatedAt` onto the `PreviewInstance` before `CreatePreviewInstance`.
6. `PreviewStore.CreatePreviewInstance` inserts and returns the new columns.

Restart/recycle needs an extra update because it currently restarts the same preview instance in place:

- Before `RecyclePreviewWithConfig`, the handler should load the current session and pass the current workspace revision to the manager.
- Add `RecyclePreviewWithConfigAndRevision(ctx, orgID, previewID, cfg, revision, revisionUpdatedAt)` or extend recycle input with optional revision fields.
- During recycle, validate config before stopping the current preview as it does today.
- After the preview is moved to `starting` and before provider launch, update:

```sql
UPDATE preview_instances
SET source_workspace_revision = @workspace_revision,
    source_workspace_revision_updated_at = @workspace_revision_updated_at,
    updated_at = now()
WHERE id = @preview_id AND org_id = @org_id;
```

If launch fails after the stamp is updated, the failed preview row still records the revision it attempted to run. The UI can show startup diagnostics and, if the session changes again, still compute freshness against the new current revision.

## API Shape

### Get Session Preview

`GET /api/v1/sessions/{id}/preview`

Current response:

```json
{
  "data": {
    "instance": {
      "id": "preview-id",
      "status": "ready"
    },
    "services": [],
    "preview_origin": "https://preview.example"
  }
}
```

New response:

```json
{
  "data": {
    "instance": {
      "id": "preview-id",
      "status": "ready",
      "source_workspace_revision": 4,
      "source_workspace_revision_updated_at": "2026-05-28T16:11:00Z"
    },
    "services": [],
    "preview_origin": "https://preview.example",
    "freshness": {
      "state": "out_of_date",
      "current_workspace_revision": 5,
      "current_workspace_revision_updated_at": "2026-05-28T16:18:00Z",
      "preview_workspace_revision": 4,
      "preview_workspace_revision_updated_at": "2026-05-28T16:11:00Z",
      "reason": "session_changed_after_preview_start"
    }
  }
}
```

Freshness rules:

- `current`: active or terminal session preview has `source_workspace_revision == sessions.workspace_revision`.
- `out_of_date`: preview source revision is lower than session revision and preview is openable/restartable.
- `updating`: preview status is `starting` and its source revision equals the current session revision.
- `unknown`: preview source revision is null, current session revision cannot be loaded, or legacy data prevents a trustworthy comparison.

For legacy previews with null source revision, return `unknown` rather than `out_of_date`.

### Ensure/Update Session Preview

Use the existing endpoint:

`POST /api/v1/sessions/{id}/preview/ensure`

Optional request body extension:

```json
{
  "expected_workspace_revision": 5
}
```

The first implementation can omit this field because the backend will always load the current session revision before reserving or recycling. Adding it later can improve race diagnostics:

- If `expected_workspace_revision` is older than the current session revision, still update to the latest revision.
- If a session is actively running and the platform does not want to restart mid-turn, return the existing `SANDBOX_BUSY`/conflict style response with copy telling the user to wait for the current turn to finish.

Response remains compatible:

```json
{
  "data": {
    "action": "restarted",
    "instance": {
      "id": "preview-id",
      "status": "starting",
      "source_workspace_revision": 5
    }
  }
}
```

`action` may remain `started`, `already_starting`, or `restarted`. The frontend can label the button `Update preview` based on `freshness.state`, without requiring a new backend action enum.

## Frontend Changes

Types:

- Extend `frontend/src/lib/preview-types.ts` with `PreviewFreshness`.
- Add `freshness?: PreviewFreshness` to `PreviewStatusResponse`.
- Add `source_workspace_revision?: number` and `source_workspace_revision_updated_at?: string` to `PreviewInstance`.

Preview tab behavior in `frontend/src/components/preview/preview-panel.tsx`:

- Read `data.freshness?.state`.
- If `out_of_date`, render an inline marker near the command header with `New changes available` and helper copy such as `Restart the preview to see the latest session changes.`
- Show a visible `Refresh preview` button in the command header when stale, wired to the existing `startMutation` / `api.sessions.preview.ensure(sessionId)`.
- Keep `Open Preview` available for ready stale previews, but do not let it visually suppress the freshness marker.
- Keep the existing secondary `Restart preview` menu item. `Refresh preview` is the promoted stale-state action; `Restart preview` remains the generic lifecycle command.
- While the mutation is pending or status is `starting` with current revision, render `Updating preview...`.
- For `unknown`, avoid a loud warning; show quiet metadata only if useful for support.

The preview status query already polls/refetches around active states. The session detail SSE/polling path should invalidate `["preview-status", sessionId]` when session detail receives a new `latest_diff_snapshot_id`, `diff_collected_at`, or future explicit `workspace_revision` change. This keeps the Preview tab aware soon after the agent completes a change even if the tab was already open.

## Tests

Backend tests:

- `internal/db/session_store_diff_test.go`: diff snapshot insertion increments `workspace_revision` and updates `workspace_revision_updated_at`.
- `internal/db/preview_store_test.go`: `CreatePreviewInstance` persists and scans `source_workspace_revision`.
- `internal/services/preview/manager_test.go`: `ReservePreview` stamps the input workspace revision.
- `internal/api/handlers/preview_test.go`: `GetPreview` returns `freshness.state = out_of_date` when session revision is newer than preview revision.
- `internal/api/handlers/preview_test.go`: `ensure`/restart stamps the current session revision before recycling.
- Enum tests for `PreviewFreshnessState`.

Frontend tests:

- `frontend/src/components/preview/preview-panel.test.tsx`: stale preview shows the warning and `Update preview`.
- Existing restart tests should keep passing with `Restart preview` still in secondary actions for non-stale ready previews.
- Session detail tests should cover invalidating preview status when the session revision changes.

Verification after implementation:

- Backend: `go vet ./...`, `go build ./...`, `go test ./...`.
- Frontend: from `frontend/`, `npm run typecheck`, `npm run lint`, `npm run build`.

## Rollout

1. Add nullable/defaulted columns and model fields.
2. Stamp new preview reservations with the current session revision.
3. Return `freshness` from `GET /api/v1/sessions/{id}/preview`.
4. Update the Preview tab to surface stale state and use the existing ensure endpoint.
5. Add revision bumps to diff snapshot capture.
6. Optionally expand revision bumps to earlier checkpoint/snapshot moments once the product wants stale detection before turn completion.

Legacy previews remain usable. They return `unknown` freshness until restarted, at which point the preview row receives a source revision and future comparisons become authoritative.

## Open Questions

- Should a running turn mark the preview as "potentially changing" before a durable workspace revision exists? The recommended v1 answer is no: show stale only after the backend has a durable revision it can restart from.
- Should `Update preview` be available while a session turn is running? The current preview/sandbox acquisition path can return `SANDBOX_BUSY`. The UI should preserve that behavior and explain that the user can update after the current turn finishes.
- Should branch previews ever reuse this freshness shape? They already compare pinned target commit with branch/PR head through `new_commits_available`. A later unification could map branch freshness into the same response shape on branch-preview APIs, but session preview freshness should not wait for that.

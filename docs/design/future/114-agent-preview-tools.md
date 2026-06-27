# Design: Agent Preview Tools

> **Status:** Not Started | **Last reviewed:** 2026-06-27

## Part 1: Product Spec

### Summary

`143-tools` should let coding agents create, inspect, interact with, and update
143 previews through the same platform-controlled paths users already rely on in
the web app. Agents should be able to run the full visual iteration loop:

1. Start or reuse a session preview.
2. Wait until it is usable.
3. Open pages, click, type, scroll, and wait for app state.
4. Capture screenshots, console output, DOM details, and responsive snapshots.
5. Edit code in the session workspace.
6. Update the preview through the fastest safe lifecycle path.
7. Repeat until the product behavior and visuals are correct.

The tool surface should be session-preview-first. Branch and PR previews remain
important publishing and review artifacts, but agent iteration should not require
pushing a branch before every visual check.

### Problem

143 already has strong preview primitives:

- Session previews and branch/PR previews.
- Worker-owned preview routing.
- Headless browser inspection through `PreviewInspector`.
- Public session preview endpoints for screenshots, interaction, console reads,
  multi-viewport capture, visual diff, and assertions.
- `143-tools preview create/status/list/stop` for human CLI preview lifecycle.
- Remote agent tools for branch preview lifecycle.

The gap is that the agent-facing `143-tools` surface does not expose a complete,
cohesive preview workflow. Agents can create/check/stop branch previews, but they
cannot reliably use `143-tools` as their one control plane for browser
inspection, interaction, screenshots, and fast preview updates.

This causes three product problems:

- Agents cannot independently verify the UI they changed without relying on
  separate browser tooling or user screenshots.
- Agents are encouraged toward branch preview workflows that require pushing
  code, even when the relevant code only exists in the live session workspace.
- Updating a preview is treated as a restart operation instead of a smart
  "make the running preview reflect my latest edit" operation.

### Users

| Segment | Priority | Need |
|---|---:|---|
| Coding agents in 143 sandboxes | P0 | A compact CLI workflow for preview lifecycle, browser interaction, screenshots, console checks, and fast update after edits. |
| Engineers supervising sessions | P0 | Evidence that the agent checked the live app before claiming UI work is done. |
| Builders and nontechnical reviewers | P1 | Higher-quality visual changes from agents, fewer "looks fine in code but broken in browser" handoffs. |
| Platform operators | P1 | Preview automation that remains audited, tenant-scoped, rate-limited, and routed through existing preview workers. |

### Product Principles

1. **Agents use platform paths.** Preview tools must go through `143-tools` and
   `/api/v1`, not direct unaudited access to preview worker internals.
2. **Session previews are the visual editing surface.** Agents should be able to
   inspect unpushed workspace edits before publish.
3. **Update means fastest safe refresh.** The agent should ask to update a
   preview; the platform should choose browser reload, soft service restart, full
   recycle, or cold relaunch.
4. **Screenshots are first-class evidence.** Tool responses should make
   screenshot artifacts easy to reference from a session transcript without
   forcing the model to carry large base64 blobs when a stored artifact is
   available.
5. **The browser is shared infrastructure, not a secret channel.** Browser
   automation must be scoped to the user's org/session/preview permissions and
   audited like other CLI tool calls.

### Goals

- Add a first-class agent-facing `143-tools preview ...` namespace for preview
  lifecycle and browser inspection.
- Support session-scoped preview creation, status, update, restart, stop,
  screenshot, console, inspect, interact, multi-viewport, visual diff, and
  assertions.
- Preserve branch preview lifecycle commands for published branches and PRs.
- Add a smart preview update operation that selects the cheapest safe lifecycle
  path.
- Return structured JSON suitable for LLM callers.
- Store large screenshots as artifacts where possible and return references plus
  metadata, while allowing small inline responses for compatibility.
- Audit preview tool invocations and enforce RBAC consistently with preview UI
  actions.

### Non-Goals

- Public unauthenticated preview access.
- Arbitrary remote browser control outside 143 previews.
- Direct DOM patching as a product feature. Agents should edit source files and
  update the preview; DOM mutation can be a diagnostic interaction but not the
  persisted fix.
- Replacing repository CI/CD or provider-native deploy previews.
- Always-on preview environments.
- Adding another browser automation stack when `PreviewInspector` already
  provides the worker-local abstraction.

### Agent Workflow

The expected happy path for a UI change inside a session:

```bash
143-tools preview create --session-id <session-id> --wait
143-tools preview screenshot --session-id <session-id> --path /
143-tools preview interact --session-id <session-id> --steps '[{"action":"click","selector":"[data-testid=save]","screenshot":true}]'
# agent edits files
143-tools preview update --session-id <session-id> --wait
143-tools preview multi_viewport --session-id <session-id> --path /
143-tools preview console --session-id <session-id> --level error
```

For a published branch preview:

```bash
143-tools preview create --repo assembledhq/143 --branch feature/foo --wait
143-tools preview status --preview-id <preview-id>
```

When both `session-id` and `preview-id` are accepted, `session-id` should be the
recommended form for agent editing because it follows the current active session
preview even if a restart creates or resumes underlying runtime state.

### Tool Naming

Use the existing hierarchical CLI model:

```text
143-tools preview <action> [flags]
```

Recommended actions:

| Action | Purpose |
|---|---|
| `create` | Create or reuse a session/branch preview. |
| `status` | Read current preview status, URL/origin, phase, freshness, and update mode. |
| `list` | List previews available to the org or current session. |
| `stop` | Stop a running preview. |
| `restart` | Force a full restart/recycle. |
| `update` | Smart update after code changes; chooses fastest safe path. |
| `screenshot` | Capture viewport or full-page screenshot. |
| `console` | Read browser console messages. |
| `inspect` | Inspect a DOM element by coordinates or selector. |
| `interact` | Execute browser interactions. |
| `multi_viewport` | Capture standard or caller-provided responsive screenshots. |
| `visual_diff` | Compare two stored screenshot/snapshot artifacts. |
| `assert` | Run visual/browser assertions. |

The CLI should still support the human shorthand:

```bash
143-tools preview create --wait
```

when it can infer repository and branch from the local Git checkout.

### Update Modes

`preview update` is the main product concept. It should return one of these
actions:

| Mode | Meaning | Example trigger |
|---|---|---|
| `browser_reload` | Running service already sees the latest files; reload the browser and re-check readiness. | React/Next/Vite HMR-friendly source edit in live session sandbox. |
| `soft_service_restart` | Restart app service processes inside the same preview sandbox without reinstalling dependencies or rebuilding preview infrastructure. | Backend route code changed and HMR is unavailable. |
| `full_recycle` | Stop/restart preview services through existing recycle path, reusing the preview sandbox and dependency caches where safe. | Service command, ports, env, or app startup assumptions changed. |
| `cold_relaunch` | Existing sandbox/runtime cannot be reused; start from the latest session snapshot. | Dead sandbox, expired snapshot state, or worker/runtime mismatch. |
| `noop_current` | Preview is already current and no browser reload was requested. | Status check with no workspace changes. |

The agent should not have to decide among these modes. It may pass
`--force-mode` only for diagnostics or explicit user requests.

### UX And Transcript Evidence

Tool outputs should be concise JSON. Screenshot tools should return:

- Page URL and title.
- Viewport.
- Capture time.
- Console errors observed during capture.
- Stored artifact reference when available.
- Optional inline base64 only when requested.

Agents should be able to cite screenshot references in their final turn or use
them as image attachments for follow-up reasoning without bloating transcript
context.

## Part 2: Engineering Spec

### Existing Building Blocks

This work should extend existing preview systems instead of adding a parallel
runtime:

- Human CLI lifecycle: `internal/cli/preview.go`
- Agent/remote preview lifecycle executor: `internal/cli/preview_tools.go`
- Hierarchical CLI mapper: `internal/services/mcp/cli.go`
- Remote CLI tool source: `internal/cli/remote.go`
- Session preview HTTP handlers: `internal/api/handlers/preview.go`
- Internal app-to-worker preview RPC: `internal/api/handlers/internal_preview.go`
- Worker client: `internal/services/preview/worker_client.go`
- Browser abstraction: `internal/services/preview/inspector.go`
- ChromeDP implementation: `internal/services/preview/chromedp_inspector.go`
- Preview manager/recycle paths: `internal/services/preview/manager.go`
- Preview freshness and restart classifier:
  `internal/services/preview/restart_classifier.go`

### Architecture

The command flow should be:

```text
Agent
  |
  v
143-tools preview <action>
  |
  v
CLI client with bearer token / sandbox API token
  |
  v
/api/v1/sessions/{id}/preview... or /api/v1/previews...
  |
  v
API PreviewHandler
  |
  +--> local worker inspector, when preview is local
  |
  +--> WorkerPreviewClient signed internal RPC, when preview is remote
          |
          v
        InternalPreviewHandler
          |
          v
        PreviewInspector / PreviewManager
```

The CLI must not construct worker URLs or preview internal auth tokens. The API
is responsible for org/session authorization, worker resolution, signed internal
RPC, audit emission, response shaping, and error normalization.

### CLI Tool Surface

Add preview tools to the remote tool source in `internal/cli/preview_tools.go`
and expose them through `mcp.RunCLI` as the `preview` namespace.

#### Tool Names

The underlying tool names can remain flat for compatibility with the existing
MCP registry pattern:

```text
preview_create
preview_status
preview_list
preview_stop
preview_restart
preview_update
preview_screenshot
preview_console
preview_inspect
preview_interact
preview_multi_viewport
preview_visual_diff
preview_assert
```

`cliPathForTool` should map the `preview_` prefix to:

```text
143-tools preview <suffix>
```

#### Common Target Flags

Most actions should accept one of:

```json
{
  "session_id": "uuid",
  "preview_id": "uuid"
}
```

Branch preview creation additionally accepts:

```json
{
  "repository": "assembledhq/143",
  "branch": "feature/foo"
}
```

Resolution order:

1. `preview_id`, when supplied, targets that preview instance/current surface.
2. `session_id`, when supplied, targets the active preview for that session.
3. For `create`, `repository` + `branch` creates a branch preview.
4. For human local CLI only, infer `repository` and `branch` from Git when no
   explicit target is provided.

Agent-facing help should recommend `session_id` for visual iteration.

### API Contract

Most browser inspection endpoints already exist for session previews. This
design adds a stable CLI-friendly contract and fills lifecycle gaps.

#### Create Or Reuse Session Preview

```http
POST /api/v1/sessions/{session_id}/preview/ensure
```

Existing route. The CLI should use it for session-preview creation.

Response shape:

```json
{
  "data": {
    "action": "resumed|started|already_starting|restarted",
    "instance": {
      "id": "uuid",
      "status": "starting|ready|partially_ready|failed|stopped",
      "preview_url": "https://...",
      "current_phase": "install"
    }
  }
}
```

#### Get Session Preview Status

```http
GET /api/v1/sessions/{session_id}/preview
```

Existing route. CLI response should include the relevant subset:

```json
{
  "preview_id": "uuid",
  "session_id": "uuid",
  "status": "ready",
  "preview_url": "https://...",
  "current_phase": "",
  "freshness": {
    "state": "current|out_of_date|restart_required|updating|unknown",
    "current_workspace_revision": 12,
    "preview_workspace_revision": 11,
    "restart_required": true,
    "restart_reasons": [
      {"kind": "preview_config_changed", "path": ".143/config.json"}
    ]
  },
  "recommended_update_mode": "browser_reload|soft_service_restart|full_recycle|cold_relaunch|noop_current"
}
```

`recommended_update_mode` may be added by this work.

#### Smart Update

Add:

```http
POST /api/v1/sessions/{session_id}/preview/update
```

Auth/RBAC:

- Authenticated user or sandbox API token scoped to the session org.
- Same write permission as restarting a preview.
- Viewer should not be able to update/restart previews.

Request:

```json
{
  "path": "/",
  "wait": false,
  "force_mode": "",
  "reload_browser": true,
  "config": {}
}
```

Fields:

| Field | Type | Required | Description |
|---|---|---:|---|
| `path` | string | no | Page path to reload/check after update. Defaults to `/`. |
| `wait` | boolean | no | If true, block until ready or failed within bounded timeout. CLI may also poll client-side. |
| `force_mode` | string | no | Diagnostic override. One of update modes. Empty means auto. |
| `reload_browser` | boolean | no | Defaults true. Reload after update/restart when a browser context exists. |
| `config` | object | no | Optional preview config override, same semantics as restart. |

Response:

```json
{
  "data": {
    "preview_id": "uuid",
    "session_id": "uuid",
    "mode": "soft_service_restart",
    "status": "starting",
    "action": "updated|restarting|started|already_current",
    "freshness": {
      "state": "updating",
      "current_workspace_revision": 13,
      "preview_workspace_revision": 12
    },
    "preview_url": "https://...",
    "message": "service restart started"
  }
}
```

Errors:

| Code | HTTP | Meaning |
|---|---:|---|
| `NO_ACTIVE_PREVIEW` | 404 | No active preview exists for the session. CLI may suggest `preview create`. |
| `PREVIEW_NOT_READY` | 409 | Update mode requires a running preview but preview is not usable. |
| `PREVIEW_UPDATE_CONFLICT` | 409 | Another start/recycle/update is already in progress. |
| `PREVIEW_UPDATE_MODE_UNSUPPORTED` | 422 | Requested `force_mode` is not available for this preview/config. |
| `PREVIEW_RESTART_FAILED` | 500 | Existing recycle/restart path failed. |
| `PREVIEW_WORKER_REQUEST_FAILED` | 502 | Worker-routed request failed. |

#### Force Restart

Use existing:

```http
POST /api/v1/sessions/{session_id}/preview/restart
```

The CLI `preview restart` should call this endpoint. It should be distinct from
`preview update`: restart means the caller intentionally wants a full restart.

#### Screenshot

Use existing:

```http
POST /api/v1/sessions/{session_id}/preview/screenshot
```

Request:

```json
{
  "path": "/",
  "viewport_w": 1280,
  "viewport_h": 720,
  "full_page": false,
  "delay_ms": 1000,
  "inline_base64": false
}
```

Response:

```json
{
  "data": {
    "page_title": "Dashboard",
    "url": "https://...",
    "captured_at": "2026-06-27T00:00:00Z",
    "viewport": {"width": 1280, "height": 720},
    "artifact": {
      "kind": "image/png",
      "ref": "session-preview-screenshot:...",
      "download_url": "/api/v1/uploads/files/..."
    },
    "png_base64": "",
    "console_errors": []
  }
}
```

The current endpoint returns `png_base64`; this design recommends adding
artifact storage and making inline base64 optional for CLI friendliness.

#### Console

Use existing:

```http
GET /api/v1/sessions/{session_id}/preview/console?level=error
```

If the current route does not accept `level`, add optional filtering in the API
or CLI response layer.

Response:

```json
{
  "data": [
    {
      "level": "error",
      "text": "ReferenceError: x is not defined",
      "source": "app.js",
      "line": 42,
      "timestamp": "2026-06-27T00:00:00Z"
    }
  ]
}
```

#### Inspect Element

Use existing:

```http
POST /api/v1/sessions/{session_id}/preview/inspect
```

Extend request to support selector in addition to coordinates:

```json
{
  "x": 420,
  "y": 180,
  "selector": "[data-testid=submit]"
}
```

When `selector` is present, the worker should inspect the first matching element
without relying on coordinates. This likely requires adding
`InspectElementBySelector` to `PreviewInspector` or implementing selector
lookup through `ExecuteInteraction`/DOM capture.

#### Interact

Use existing:

```http
POST /api/v1/sessions/{session_id}/preview/interact
```

Request:

```json
{
  "steps": [
    {
      "action": "navigate|click|type|wait|scroll|select",
      "selector": "[data-testid=email]",
      "value": "user@example.com",
      "wait_for": "networkidle",
      "timeout_ms": 10000,
      "screenshot": true
    }
  ]
}
```

Response:

```json
{
  "data": {
    "steps": [
      {
        "action": "click",
        "success": true,
        "screenshot": {
          "artifact": {"ref": "session-preview-screenshot:..."}
        }
      }
    ],
    "console_errors": []
  }
}
```

#### Multi-Viewport

Use existing:

```http
POST /api/v1/sessions/{session_id}/preview/multi-viewport
```

Request:

```json
{
  "path": "/",
  "viewports": [
    {"name": "mobile", "width": 375, "height": 812},
    {"name": "tablet", "width": 768, "height": 1024},
    {"name": "desktop", "width": 1280, "height": 720}
  ],
  "delay_ms": 1000,
  "inline_base64": false
}
```

#### Visual Diff

Use existing:

```http
POST /api/v1/sessions/{session_id}/preview/visual-diff
```

Request:

```json
{
  "before_snapshot_id": "artifact-or-snapshot-id",
  "after_snapshot_id": "artifact-or-snapshot-id"
}
```

#### Assertions

Use existing:

```http
POST /api/v1/sessions/{session_id}/preview/assert
```

Request:

```json
{
  "assertions": [
    {
      "type": "element_exists|element_text|element_style|element_count|no_console_errors|page_title|viewport_screenshot_match",
      "selector": "[data-testid=submit]",
      "contains": "Save",
      "description": "save button is visible"
    }
  ]
}
```

### Data Model

#### Required Schema Changes

No new table is required for the minimal tool surface because preview lifecycle,
status, runtime, logs, snapshots, freshness, and audit tables already exist.

Recommended additions:

1. Store screenshot artifacts instead of only base64 JSON payloads.
2. Track preview update attempts separately from generic preview logs if product
   analytics need mode-level reporting.

#### Option A: Use Existing Upload/Artifact Storage

Preferred for v1 if the existing upload/file artifact model can store generated
PNG files with org/session ownership.

No preview-specific migration required. Screenshot responses return the upload
reference.

#### Option B: Add Preview Tool Artifacts

If generated preview artifacts need preview-native retention and indexing, add:

```sql
CREATE TABLE preview_tool_artifacts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id),
    preview_instance_id uuid NOT NULL REFERENCES preview_instances(id) ON DELETE CASCADE,
    session_id uuid REFERENCES sessions(id) ON DELETE SET NULL,
    tool_name text NOT NULL,
    artifact_kind text NOT NULL,
    storage_key text NOT NULL,
    content_type text NOT NULL,
    byte_size bigint NOT NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_by_user_id uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_preview_tool_artifacts_preview_created
    ON preview_tool_artifacts (org_id, preview_instance_id, created_at DESC);

CREATE INDEX idx_preview_tool_artifacts_session_created
    ON preview_tool_artifacts (org_id, session_id, created_at DESC)
    WHERE session_id IS NOT NULL;
```

Tenancy:

- `org_id` is required and must filter every query.
- `preview_instance_id` points to the preview being inspected.
- `session_id` is nullable for branch previews.

Model:

```go
type PreviewToolArtifactKind string

const (
    PreviewToolArtifactScreenshot PreviewToolArtifactKind = "screenshot"
    PreviewToolArtifactScreencast PreviewToolArtifactKind = "screencast"
)

func (k PreviewToolArtifactKind) Validate() error
```

Use the typed-string enum pattern and table-driven validation tests.

#### Preview Update Attempt Logs

For v1, write update activity into existing preview logs with stable metadata:

```json
{
  "tool": "preview_update",
  "mode": "soft_service_restart",
  "workspace_revision": 13,
  "previous_preview_revision": 12,
  "duration_ms": 842,
  "result": "started"
}
```

If analytics need a dedicated table later:

```sql
CREATE TABLE preview_update_attempts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id),
    preview_instance_id uuid NOT NULL REFERENCES preview_instances(id) ON DELETE CASCADE,
    session_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    requested_by_user_id uuid REFERENCES users(id) ON DELETE SET NULL,
    requested_mode text NOT NULL,
    selected_mode text NOT NULL,
    status text NOT NULL,
    workspace_revision bigint NOT NULL,
    error text NOT NULL DEFAULT '',
    started_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz
);

CREATE INDEX idx_preview_update_attempts_session_started
    ON preview_update_attempts (org_id, session_id, started_at DESC);
```

This table is not required for the first implementation.

### Models And Types

Add typed strings in `internal/models`:

```go
type PreviewUpdateMode string

const (
    PreviewUpdateModeBrowserReload      PreviewUpdateMode = "browser_reload"
    PreviewUpdateModeSoftServiceRestart PreviewUpdateMode = "soft_service_restart"
    PreviewUpdateModeFullRecycle        PreviewUpdateMode = "full_recycle"
    PreviewUpdateModeColdRelaunch       PreviewUpdateMode = "cold_relaunch"
    PreviewUpdateModeNoopCurrent        PreviewUpdateMode = "noop_current"
)

func (m PreviewUpdateMode) Validate() error
```

```go
type PreviewUpdateAction string

const (
    PreviewUpdateActionUpdated        PreviewUpdateAction = "updated"
    PreviewUpdateActionRestarting     PreviewUpdateAction = "restarting"
    PreviewUpdateActionStarted        PreviewUpdateAction = "started"
    PreviewUpdateActionAlreadyCurrent PreviewUpdateAction = "already_current"
)

func (a PreviewUpdateAction) Validate() error
```

Request/response models:

```go
type PreviewUpdateRequest struct {
    Path          string            `json:"path,omitempty"`
    Wait          bool              `json:"wait,omitempty"`
    ForceMode     PreviewUpdateMode `json:"force_mode,omitempty"`
    ReloadBrowser bool              `json:"reload_browser"`
    Config        *PreviewConfig    `json:"config,omitempty"`
}

type PreviewUpdateResponse struct {
    PreviewID   uuid.UUID              `json:"preview_id"`
    SessionID   uuid.UUID              `json:"session_id"`
    Mode        PreviewUpdateMode      `json:"mode"`
    Action      PreviewUpdateAction    `json:"action"`
    Status      PreviewStatus          `json:"status"`
    PreviewURL  string                 `json:"preview_url,omitempty"`
    Freshness   *PreviewFreshness      `json:"freshness,omitempty"`
    Message     string                 `json:"message,omitempty"`
}
```

Extend `PreviewFreshness` or preview status response with:

```go
RecommendedUpdateMode PreviewUpdateMode `json:"recommended_update_mode,omitempty"`
```

### Update Mode Selection

Extend the existing restart classifier so it returns an update mode, not only
restart reasons.

Inputs:

- Current session `workspace_revision`.
- Preview `source_workspace_revision`.
- Preview `runtime_workspace_revision`.
- Changed file paths from the latest diff snapshot.
- Preview config digest/config path changes.
- Lockfile/package file changes.
- Whether the preview sandbox is alive.
- Whether the preview service advertises HMR/live reload support.
- Whether preview is ready/partially ready/starting/failed.

Initial conservative rules:

| Condition | Mode |
|---|---|
| No active preview | `cold_relaunch` from create path, or `NO_ACTIVE_PREVIEW` for update. |
| Preview is starting/recycling | Conflict unless caller only wants status. |
| Sandbox is dead or unknown-dead | `cold_relaunch`. |
| `.143/config.json`, preview config digest, service command, port, resource, secret, or env file changed | `full_recycle`. |
| Lockfile or package manifest changed | `full_recycle`. |
| Only source/static/template/style files changed and service supports HMR | `browser_reload`. |
| Source files changed and HMR support is unknown/false | `soft_service_restart`. |
| No revision delta | `noop_current` or `browser_reload` if `reload_browser=true`. |

The first implementation may map `soft_service_restart` to `full_recycle` until
provider support exists, but the API should return a clear mode so the product
contract can evolve without changing the CLI.

### Provider Work

Add optional soft restart support to the preview provider interface:

```go
type PreviewSoftRestarter interface {
    SoftRestartPreview(ctx context.Context, handle string, opts SoftRestartOptions, observer ServiceObserver) (*PreviewHandle, error)
}

type SoftRestartOptions struct {
    OrgID        uuid.UUID
    RepositoryID uuid.UUID
    SessionID    uuid.UUID
    PreviewID    uuid.UUID
    ConfigDigest string
}
```

For Docker provider:

- Stop the configured app service process group.
- Preserve infrastructure containers, dependency caches, installed artifacts,
  preview secret files, preview origin, and browser context where possible.
- Re-run only service start commands and readiness checks.
- Update runtime handle/port only if changed.
- Keep full recycle fallback on unsupported configs.

Do not run `preview.install` in the soft restart path.

### Worker RPC

Add worker-routed internal actions only if needed beyond existing endpoints.

Likely additions:

```http
POST /internal/previews/{previewID}/update
POST /internal/previews/{previewID}/soft-restart
POST /internal/previews/{previewID}/reload-browser
```

Each internal route must use signed preview worker auth with action-specific
tokens, following existing internal preview authorization.

The public API should remain the only caller that decides whether to invoke
these worker routes.

### RBAC And Security

- `preview status`, `screenshot`, `console`, `inspect`, `interact`,
  `multi_viewport`, `visual_diff`, and `assert` require preview read access.
- `preview create`, `update`, `restart`, and `stop` require preview write
  access.
- Sandbox API tokens injected into sessions may act only on their own session's
  previews unless explicitly granted broader org preview capability.
- Every store lookup must filter by `org_id`.
- Internal worker calls must use signed preview tokens with action-specific
  claims.
- Browser automation must only target 143 preview origins generated from active
  preview state. Do not accept arbitrary URLs.
- Cap interaction steps, screenshot dimensions, screencast duration, and result
  bytes using existing preview inspector limits.
- Tool arguments should not be retained in audit logs; audit the tool name,
  preview/session IDs, user/org IDs, selected update mode, and result.

### Audit Events

Use existing `cli.tool_invoked` for generic tool usage and add preview-specific
details where needed:

```json
{
  "tool": "preview_update",
  "session_id": "uuid",
  "preview_id": "uuid",
  "selected_mode": "soft_service_restart",
  "status": "updated"
}
```

Consider adding typed audit actions later:

- `preview.tool_invoked`
- `preview.updated`
- `preview.screenshot_captured`

Generic `cli.tool_invoked` is acceptable for v1 if details remain non-sensitive.

### Generated Agent Skills Text

Update the sandbox-injected integration tools guidance so agents learn the
preview workflow without listing every flag.

Recommended snippet:

```markdown
## Preview Tools

Use `143-tools preview` to inspect and verify web UI work:

- `143-tools preview create --session-id <id> --wait`
- `143-tools preview status --session-id <id>`
- `143-tools preview screenshot --session-id <id> --path /`
- `143-tools preview interact --session-id <id> --steps '[...]'`
- `143-tools preview update --session-id <id> --wait`
- `143-tools preview console --session-id <id> --level error`

Prefer session previews while editing because they can reflect unpushed
workspace changes. Use branch previews after pushing a branch.
```

### Testing Plan

Backend:

- CLI command mapping tests for `preview <action>`.
- CLI executor tests for target resolution and JSON shaping.
- Handler tests for `POST /sessions/{id}/preview/update`.
- RBAC tests for read vs write preview actions.
- Worker routing tests for remote screenshot/interact/update.
- Store tests if artifact/update-attempt tables are added.
- Enum validation tests for `PreviewUpdateMode` and `PreviewUpdateAction`.
- Restart/update classifier table tests covering config, lockfile, source-only,
  dead sandbox, and no-op cases.

Frontend:

- No frontend change is required for the CLI surface.
- If preview update mode is surfaced in the UI later, add focused tests around
  status/freshness rendering and action labels.

Integration:

- Session preview create -> screenshot -> interact -> edit fixture file ->
  update -> screenshot.
- Worker-routed preview screenshot and interact in non-local worker tests.
- Failed preview update returns actionable error JSON.

### Rollout

1. Add read-only browser inspection commands first:
   `status`, `screenshot`, `console`, `inspect`, `interact`, `multi_viewport`,
   `assert`.
2. Add session-scoped lifecycle commands:
   `create`, `restart`, `stop`.
3. Add `preview update` initially mapping to existing full recycle when needed.
4. Add update-mode classifier response.
5. Add soft service restart provider support.
6. Add screenshot artifact storage and transcript references.
7. Update public docs only after the CLI workflow is stable.

### Open Questions

- Should screenshot artifacts use the existing upload system or a
  preview-specific artifact table?
- Should sandbox-injected API tokens be restricted to the current session preview
  by default, or can they inspect any org preview visible to the session user?
- What is the minimum reliable signal for HMR support per preview service?
- Should `preview update --wait` block server-side or should the CLI always poll
  status client-side?
- Should `inspect` support CSS selectors in v1, or only coordinates until the
  inspector interface is extended?

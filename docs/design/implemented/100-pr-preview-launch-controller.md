# Design: PR Preview Launch Controller

> **Status:** Implemented
> **Last reviewed:** 2026-06-16

## Summary

143 already has durable branch/PR preview targets, stable PR preview routes, preview bootstrap, warm resume, and repository auto-preview policy. The missing product layer is a PR-specific launch controller: one stable Preview action that always resolves the user's intent for a pull request and chooses the fastest safe path to a live preview.

This design turns PR preview opening into an explicit state machine. A reviewer clicks Preview for a PR; the product decides whether to open a ready runtime, resume a warm runtime, start the latest PR head, retry a failure, show stale-code guidance, or explain a permission/capacity block. Runtime instance URLs stay disposable. The PR preview URL stays durable.

## Product Goal

Every reviewable PR should have one Preview affordance that answers:

`Can I see the latest version of this PR running?`

The answer may be immediate, resumable, starting, failed, stale, or blocked, but the user should never land on a dead short-lived runtime URL with no recovery path.

## Current System

- 143-generated PRs append a `Preview:` footer after PR creation.
- `/previews/github/{owner}/{repo}/pull/{number}` is the stable app route for a GitHub PR.
- `GET /api/v1/previews/github/{owner}/{repo}/pull/{number}` resolves the PR head through GitHub, finds or creates a preview target, starts a runtime when allowed, and reports stale active previews with `new_commits_available`.
- `OpenPreviewButton` bootstraps access before opening the preview origin. It opens `about:blank` for manual button clicks, loads `<preview-origin>/bootstrap` in a hidden iframe, mints a short-lived token from the app API, posts it to the preview origin, waits for completion, and navigates the popup. Stable PR launch sessions use the same bootstrap flow and then replace the current PR-launch tab with the preview origin.
- Preview-origin recovery pages for stopped, expired, or disconnected previews hand off through the current tab to the stable app launch route. The app route owns start/restart, progress, diagnostics, bootstrap, and final navigation back to the preview origin.
- Preview-origin traffic remains isolated from the app origin and is gateway-routed to the worker that owns the runtime.

This is close, but product behavior is still page/button oriented instead of intent oriented. The response does not expose a single launch decision, and the frontend does not have a reusable auto-launch state machine.

## Non-Goals

- Public unauthenticated preview sharing.
- Bypassing preview bootstrap or serving preview content from the main app origin.
- Replacing external deploy previews from Vercel, Netlify, Render, Fly, or repository CI/CD.
- Guaranteeing that every preview is always running. The guarantee is a durable control path, not permanent runtime uptime.
- Supporting fork PR auto-preview in v1.

## User-Facing Behavior

### PR Footer Link

Each 143-generated PR should contain exactly one durable preview link:

```text
Preview: https://app.143.dev/previews/github/{owner}/{repo}/pull/{number}?launch=1
```

Use the app stable route as the default PR footer link. It is the most reliable control-plane entry because it can authenticate the app user, resolve current GitHub head state, show diagnostics, and choose whether to open, resume, start, retry, or block.

A preview-origin target URL may remain supported for advanced/direct flows, but it must degrade into the same stable launch controller when the user lacks preview-domain access or no active runtime exists.

When a user lands directly on a stopped, expired, or disconnected preview-origin URL, the gateway should render a lightweight recovery page whose primary action navigates the current tab to the stable app route with `launch=1`. It should not open a secondary app popup; the stable route is responsible for showing startup state and returning the tab to the preview when ready.

### PR Page Button

In 143 PR surfaces, the primary PR action should be `Preview`.

Click behavior:

| Condition | Behavior |
|---|---|
| Latest PR head has a ready active preview | Bootstrap and open the live preview immediately. |
| Latest PR head has an active preview still starting | Show stable page with startup progress and auto-open when ready if the click initiated launch. |
| Latest PR head has a warm/resumable preview | Start/resume the preview, show `Resuming preview...`, then auto-open when ready. |
| No target/runtime exists and caller may create | Create target for latest PR head, start runtime, show progress, then auto-open. |
| Active preview exists for an older SHA | Do not auto-open stale code. Show `New commits available` with `Open stale preview` secondary and `Restart` primary. |
| Latest target failed | Show failure summary, phase/log diagnostics, and `Retry`. |
| Preview is blocked by role/token/capacity/config | Show a specific recovery state; do not fall through to generic failure. |
| PR is closed or merged | Show terminal state and do not start new runtimes by default. |

### Auto-Open Rules

Auto-open is allowed only when all are true:

- The action was initiated by a user gesture on the stable app page or a 143 PR surface.
- The returned launch decision says the preview represents the latest PR head.
- The runtime is `ready` or transitions to `ready` during the page's launch session.
- The launch session came from a stable app route with `launch=1` or from an explicit in-page start/retry action; the PR-launch tab bootstraps access and navigates itself to the preview origin. Manual in-app `Open preview` buttons may still use a popup opened synchronously from the click so engineers can keep 143 open beside the preview.

Auto-open is not allowed for stale previews, failed previews, permission-blocked previews, or closed PRs.

## Launch State Model

Add a typed launch decision to branch preview responses. Keep existing fields for compatibility.

### Go Types

Add enum-like typed strings in `internal/models`.

```go
type PreviewLaunchAction string

const (
	PreviewLaunchActionOpen        PreviewLaunchAction = "open"
	PreviewLaunchActionWait        PreviewLaunchAction = "wait"
	PreviewLaunchActionResume      PreviewLaunchAction = "resume"
	PreviewLaunchActionStart       PreviewLaunchAction = "start"
	PreviewLaunchActionStartLatest PreviewLaunchAction = "start_latest"
	PreviewLaunchActionRetry       PreviewLaunchAction = "retry"
	PreviewLaunchActionBlocked     PreviewLaunchAction = "blocked"
	PreviewLaunchActionClosed      PreviewLaunchAction = "closed"
)

func (a PreviewLaunchAction) Validate() error
```

```go
type PreviewLaunchReason string

const (
	PreviewLaunchReasonReady              PreviewLaunchReason = "ready"
	PreviewLaunchReasonStarting           PreviewLaunchReason = "starting"
	PreviewLaunchReasonResumable          PreviewLaunchReason = "resumable"
	PreviewLaunchReasonNoRuntime          PreviewLaunchReason = "no_runtime"
	PreviewLaunchReasonStale              PreviewLaunchReason = "stale"
	PreviewLaunchReasonFailed             PreviewLaunchReason = "failed"
	PreviewLaunchReasonRoleForbidden      PreviewLaunchReason = "role_forbidden"
	PreviewLaunchReasonTokenForbidden     PreviewLaunchReason = "token_forbidden"
	PreviewLaunchReasonCapacity           PreviewLaunchReason = "capacity"
	PreviewLaunchReasonConfigRequired     PreviewLaunchReason = "config_required"
	PreviewLaunchReasonConfigInvalid      PreviewLaunchReason = "config_invalid"
	PreviewLaunchReasonRepositoryMissing  PreviewLaunchReason = "repository_missing"
	PreviewLaunchReasonGitHubUnavailable  PreviewLaunchReason = "github_unavailable"
	PreviewLaunchReasonPullRequestClosed  PreviewLaunchReason = "pull_request_closed"
	PreviewLaunchReasonPreviewUnavailable PreviewLaunchReason = "preview_unavailable"
)

func (r PreviewLaunchReason) Validate() error
```

Add table-driven validation tests in `internal/models/preview_enums_test.go`.

### API Shape

Extend `branchPreviewResponse`:

```go
type branchPreviewLaunch struct {
	Action              models.PreviewLaunchAction `json:"action"`
	Reason              models.PreviewLaunchReason `json:"reason"`
	AutoOpen            bool                       `json:"auto_open"`
	RepresentsLatest    bool                       `json:"represents_latest"`
	RequiresUserGesture bool                       `json:"requires_user_gesture,omitempty"`
	Message             string                     `json:"message,omitempty"`
	PrimaryLabel        string                     `json:"primary_label,omitempty"`
	SecondaryLabel      string                     `json:"secondary_label,omitempty"`
	StalePreviewURL     *string                    `json:"stale_preview_url,omitempty"`
}

type branchPreviewResponse struct {
	// existing fields...
	Launch branchPreviewLaunch `json:"launch"`
}
```

Frontend type:

```ts
export type PreviewLaunchAction =
  | "open"
  | "wait"
  | "resume"
  | "start"
  | "start_latest"
  | "retry"
  | "blocked"
  | "closed";

export type PreviewLaunchReason =
  | "ready"
  | "starting"
  | "resumable"
  | "no_runtime"
  | "stale"
  | "failed"
  | "role_forbidden"
  | "token_forbidden"
  | "capacity"
  | "config_required"
  | "config_invalid"
  | "repository_missing"
  | "github_unavailable"
  | "pull_request_closed"
  | "preview_unavailable";

export interface PreviewLaunchDecision {
  action: PreviewLaunchAction;
  reason: PreviewLaunchReason;
  auto_open: boolean;
  represents_latest: boolean;
  requires_user_gesture?: boolean;
  message?: string;
  primary_label?: string;
  secondary_label?: string;
  stale_preview_url?: string;
}

export interface BranchPreviewResponse {
  // existing fields...
  launch: PreviewLaunchDecision;
}
```

Example ready response:

```json
{
  "data": {
    "target_id": "target-1",
    "preview_id": "preview-1",
    "repository_full_name": "acme/web",
    "branch": "feature/checkout",
    "commit_sha": "abc123",
    "latest_commit_sha": "abc123",
    "status": "ready",
    "stable_url": "https://app.143.dev/previews/github/acme/web/pull/42",
    "preview_url": "https://target-1.preview.143.dev",
    "launch": {
      "action": "open",
      "reason": "ready",
      "auto_open": true,
      "represents_latest": true,
      "primary_label": "Open preview"
    }
  }
}
```

Example stale response:

```json
{
  "data": {
    "target_id": "target-old",
    "preview_id": "preview-old",
    "commit_sha": "abc123",
    "latest_commit_sha": "def456",
    "new_commits_available": true,
    "status": "ready",
    "stable_url": "https://app.143.dev/previews/github/acme/web/pull/42",
    "preview_url": "https://target-old.preview.143.dev",
    "launch": {
      "action": "start_latest",
      "reason": "stale",
      "auto_open": false,
      "represents_latest": false,
      "primary_label": "Restart",
      "secondary_label": "Open stale preview",
      "message": "This preview is for abc123; the pull request is now at def456."
    }
  }
}
```

## API Contract

### Resolve PR Preview Launch

`GET /api/v1/previews/github/{owner}/{repo}/pull/{number}`

Responsibilities:

1. Authenticate app session or preview API token.
2. Load repository by `org_id` and full name.
3. Check `previews:read` for API-token callers.
4. Resolve PR head from GitHub using installation token.
5. Detect closed/merged PR state if available from the GitHub response.
6. Find latest preview target for the PR head branch/config.
7. Find active runtime for the target, if any.
8. Detect stale runtime if target SHA differs from current PR head SHA.
9. Derive warm resumability from startup snapshot availability.
10. Optionally create/start when the current behavior already allows it.
11. Return `launch` with the explicit action/reason.

The endpoint may keep today's create/start-on-read behavior for compatibility, but the launch decision must make the side effect transparent. Longer term, split "resolve" and "start" by adding a query flag:

```text
GET /api/v1/previews/github/{owner}/{repo}/pull/{number}?intent=open
```

Supported intent values:

| Intent | Behavior |
|---|---|
| `status` | Read-only resolution. Never creates or starts. |
| `open` | User intends to open; may create/start/resume if permitted. |
| `diagnose` | Prefer status/log detail; never auto-open. |

Default `intent=open` preserves current PR-link behavior.

### Restart Latest Source

`POST /api/v1/previews/{target_id}/start-latest`

Current endpoint remains, but PR callers must get a response with `launch`.

Rules:

- Resolve the latest branch/PR head server-side.
- Verify the resolved SHA exists before creating a target.
- Reuse an existing target for the same SHA/config if present.
- Upsert the PR preview link to the latest target.
- Start or resume the runtime.
- Return `launch.action = "wait"` while starting, then subsequent polls return `open`.

### Restart/Retry

`POST /api/v1/previews/{preview_id}/restart`

Rules:

- Restart the same target by default.
- If body contains `{ "start_latest": true }`, use the `StartLatest` semantics.
- Failed latest target returns `launch.action = "retry"`.
- Starting retry returns `launch.action = "wait"`.

### Bootstrap

`POST /api/v1/previews/{preview_id}/bootstrap`

No API shape change is required.

Rules to keep:

- Preview must be active.
- Target repository access must be checked for preview API-token callers.
- Token remains one-time/short-lived and never appears in URL, browser history, or logs.

## Backend Implementation Plan

### 1. Add Launch Enums

Files:

- `internal/models/preview_enums.go`
- `internal/models/preview_enums_test.go`

Add `PreviewLaunchAction` and `PreviewLaunchReason` with `Validate()` methods and tests. These are API response enums, so use typed strings rather than raw strings.

### 2. Extend Branch Preview Response

Files:

- `internal/api/handlers/branch_previews.go`
- `frontend/src/lib/types.ts`

Add `launch` to `branchPreviewResponse` and `BranchPreviewResponse`.

Keep existing fields:

- `status`
- `new_commits_available`
- `latest_commit_sha`
- `resumable`
- `resume_estimate_seconds`
- `preview_url`
- `error`
- `current_phase`
- `phase_steps`

The launch object is an interpretation layer over those fields.

### 3. Centralize Launch Derivation

Add a helper in `internal/api/handlers/branch_previews.go`:

```go
func derivePRPreviewLaunch(resp branchPreviewResponse, opts prPreviewLaunchOptions) branchPreviewLaunch
```

Suggested options:

```go
type prPreviewLaunchOptions struct {
	CanCreate        bool
	CanRead          bool
	PRClosed         bool
	LatestCommitSHA  string
	ClickedOpen      bool
	BlockingReason   models.PreviewLaunchReason
	BlockingMessage  string
}
```

Rules:

- `PRClosed` -> `closed/pull_request_closed`, `auto_open=false`.
- no read permission -> `blocked/token_forbidden` or `blocked/role_forbidden`.
- stale target/runtime -> `start_latest/stale`, `auto_open=false`.
- status `ready` or `partially_ready`, latest represented, has preview URL -> `open/ready`, `auto_open=true`.
- status `starting`, latest represented -> `wait/starting`, `auto_open=true` only if the request came from a user open intent.
- status `stopped` or `expired` and `resumable=true` -> `resume/resumable`.
- status `failed` -> `retry/failed`.
- no runtime and can create -> `start/no_runtime`.
- no runtime and cannot create -> `blocked/role_forbidden`.
- config selection/invalid config -> `blocked/config_required` or `blocked/config_invalid`.
- capacity errors -> `blocked/capacity`.

### 4. Map Errors Into Launch Decisions

Today many failure paths call `writeError`. For PR preview launch, user-recoverable preview errors should be returned as a 200 with `launch.action = "blocked"` when the repository and PR can be resolved.

Use normal non-200 errors for:

- unauthenticated request
- malformed PR number
- repository not found
- unexpected database failure

Use blocked launch responses for:

- viewer cannot create a new preview
- API token lacks `previews:create`
- preview capacity is full
- preview config requires selection
- preview config is invalid
- GitHub temporarily cannot resolve config/content after the PR itself was resolved

This keeps the stable PR page renderable and recoverable.

### 5. Support Read-Only Status Intent

Add optional query parsing:

```go
intent := r.URL.Query().Get("intent")
```

Accepted:

- empty / `open`
- `status`
- `diagnose`

For `status` and `diagnose`, the endpoint must not create targets or start runtimes. It should return the best known target/runtime and a launch action describing what would happen if the user clicked.

This lets list rows, hover cards, and PR health surfaces check status without accidentally consuming preview capacity.

### 6. Preserve PR Link Idempotency

Files:

- `internal/services/github/pr.go`

Change PR footer generation to prefer the app stable route:

```go
func (s *PRService) prPreviewURL(...) string {
	return s.stablePRPreviewURL(owner, repoName, number)
}
```

Still create/upsert the preview target and link as a best-effort side effect when all inputs are available, but do not return the preview-origin target URL as the PR body URL by default.

Ensure body update is idempotent:

- If a `Preview:` footer exists for the same PR route, do not append another.
- If an old preview-origin target footer exists, replace it with the app stable route when updating a 143-generated PR body.

### 7. Auto-Preview Policy Hook

Files:

- `internal/services/github/pr.go`
- `internal/api/handlers/branch_previews.go`
- worker preview start runner

Use existing `PreviewAutoMode`:

- `off`: no automatic runtime work.
- `warm`: on PR open/reopen/synchronize, build preview, save startup snapshot, stop with `warm_policy`.
- `on`: on PR open/reopen/synchronize, build and leave running until idle/TTL.

Implementation requirements:

- Skip draft PRs unless policy later adds `include_drafts`.
- Skip fork PRs in v1.
- On synchronize, stop prior head runtime with `warm_policy` or `pr_closed` as appropriate.
- Defer/queue when auto pool is full instead of failing the user-facing PR link.
- Record `pr_preview_state` so stable PR launch can show last known warm/failed/closed state even without an active runtime.

### 8. Preview-Origin Recovery Overlay

Files:

- `internal/api/gateway/preview_gateway.go`

Direct preview-origin links should remain supported, but the gateway must not become the main control plane.

For document requests to a target host when access/runtime is missing:

- If preview-domain access is missing, show a minimal overlay that links to the app stable route.
- If no active runtime exists, link to the app stable route to start/resume.
- If worker routing fails for an active runtime, show restart/retry guidance and link to the stable page.

Do not expose app session cookies to preview origin. Do not call app APIs from preview-origin JavaScript using app cookies.

## Frontend Implementation Plan

### 1. Add Launch Types

File:

- `frontend/src/lib/types.ts`

Add `PreviewLaunchAction`, `PreviewLaunchReason`, and `PreviewLaunchDecision`.

### 2. Extract Bootstrap Into a Hook

Current component:

- `frontend/src/components/preview/open-preview-button.tsx`

Add:

```ts
export function usePreviewLauncher(): {
  launchPreview: (input: { previewId: string; previewUrl: string; popup?: Window | null; target?: "new_tab" | "current_tab" }) => Promise<void>;
  isOpening: boolean;
  error: Error | null;
}
```

The hook should own:

- hidden bootstrap iframe source
- popup lifecycle
- message listener
- timeout
- `api.previews.bootstrap`
- origin validation

`OpenPreviewButton` becomes a thin wrapper around the hook.

### 3. PR Preview Page Launch Controller

File:

- `frontend/src/app/(dashboard)/previews/github/[owner]/[repo]/pull/[number]/page.tsx`

Behavior:

- On first render from a direct navigation, call `getPullRequest(..., { intent: "open" })`.
- If user arrived from clicking a 143 `Preview` button, open the stable PR route in a new tab with `launch=1`; that stable page owns bootstrap and replaces its own tab with the preview origin when ready.
- If user arrived from a preview-origin recovery page, the current tab is already on the stable route with `launch=1`; keep showing launch progress in that tab and replace it with the preview origin when ready.
- If `launch.action === "open"`, call `launchPreview`.
- If `launch.action === "wait"` and `launch.auto_open`, poll until `open` or terminal.
- If `resume/start/start_latest/retry`, show the primary action and optionally execute it immediately only when the original click explicitly requested open.
- If `blocked`, render tailored recovery copy from `launch.reason`.
- If `closed`, render closed PR state.

Polling:

- Continue current 3s poll for `starting`.
- Also poll for launch actions `wait`, `resume`, and `start` until `open`, `blocked`, `retry`, or `closed`.
- Stop polling when hidden unless a popup is pending; resume on visibility.

### 4. PR Surface Button

Wherever PR health/header actions are rendered, add a `Preview` button that routes through the stable app launch page.

Preferred implementation:

- Use a Next link for normal navigation.
- For same-app PR surfaces where popup behavior matters, add a command that opens the stable route in a new tab from the click. The stable page then owns bootstrap and launch state.

Avoid duplicating launch logic in PR health rows. They should link to the stable controller.

### 5. UI States

The stable PR preview page should render these states:

- `Opening preview...`
- `Starting preview...`
- `Resuming preview... resumes in about Ns`
- `New commits available`
- `Preview failed`
- `Preview unavailable`
- `Preview blocked`
- `Pull request closed`

Use existing startup progress, services, infrastructure, and logs. Keep diagnostics expandable by default unless failed.

## Tests

### Backend Tests

Add table-driven tests with `t.Parallel()` and `require`.

Files:

- `internal/models/preview_enums_test.go`
- `internal/api/handlers/branch_previews_test.go`
- `internal/services/github/pr_handlers_test.go`

Required cases:

- launch enum validation accepts all constants and rejects invalid strings.
- ready latest PR preview returns `launch.open`, `auto_open=true`.
- starting latest PR preview returns `launch.wait`.
- resumable latest target returns `launch.resume` with estimate.
- stale active preview returns `launch.start_latest`, `auto_open=false`.
- failed latest preview returns `launch.retry`.
- viewer with existing preview can open.
- viewer with no target gets blocked launch or 403, depending on whether PR/repo could be resolved.
- API token with read but not create can inspect existing preview but cannot start.
- `intent=status` does not create target or enqueue start.
- `intent=open` preserves existing create/start behavior.
- PR footer generation appends exactly one stable app URL.
- old preview-origin footer is replaced with stable app route when PR body is updated.

### Frontend Tests

Files:

- `frontend/src/components/preview/open-preview-button.test.tsx` or hook test file
- `frontend/src/app/(dashboard)/previews/github/[owner]/[repo]/pull/[number]/page.test.tsx`

Required cases:

- ready launch bootstraps and opens preview origin.
- starting launch shows progress and polls until ready, then opens.
- stale launch does not auto-open and shows `Restart`.
- resumable launch shows resume estimate and calls `start-latest` or restart path.
- failed launch shows retry and logs/phase summary.
- blocked role/token/capacity launch renders specific copy.
- bootstrap or navigation errors are visible, and the normal `Open preview` button remains available for manual retry.
- bootstrap token is sent only to the expected preview origin.

### Verification

Frontend changes must pass:

```bash
cd frontend
npm run typecheck
npm run lint
npm run build
```

Go changes must pass:

```bash
go vet ./...
go build ./...
go test ./...
```

## Rollout Plan

1. Add backend launch enums and response fields behind compatible additive JSON.
2. Add frontend types and keep current page behavior using existing fields.
3. Centralize launch derivation and populate `launch`.
4. Convert PR preview page to consume `launch`.
5. Extract preview bootstrap hook and reuse it from the page and button.
6. Change PR body footer to prefer app stable route.
7. Add `intent=status` read-only mode for passive PR surfaces.
8. Polish gateway document-overlay fallback for direct preview-origin links.
9. Enable auto-preview `warm` policy for selected dogfood repositories.
10. Measure click-to-ready by launch path: active, warm, cold, failed, blocked.

## Metrics

Record:

- `preview.pr_launch.decisions{preview.launch_action,preview.launch_reason,repository.full_name,org.id,preview.intent,preview.auto_open}`
- `preview_pr_click_to_open_seconds{path=active|warm|cold}`
- `preview_pr_launch_blocked_total{reason}`
- `preview_pr_stale_open_attempt_total`
- manual preview-button popup-blocked errors are surfaced through the shared launcher hook.
- existing startup phase duration metrics by initiator

Success criteria:

- Active preview p95 click-to-open under 3s after page JS is loaded.
- Warm preview p50 click-to-ready under 45s.
- Stale preview auto-open rate is zero.
- PR footer duplicate rate is zero.
- Preview-origin direct-link recovery produces a stable-page click instead of a dead gateway error.

## Security Notes

- Preview content remains untrusted.
- The preview app must never run on the app origin.
- Bootstrap tokens remain short-lived, one-time, and never placed in URLs.
- Preview-domain cookies are scoped to preview origins only.
- The app stable route is responsible for org/repo/role checks before minting preview access.
- Gateway worker-routing errors are translated into preview-specific unavailable/retry states without exposing internal worker URLs or tokens.

## Open Questions

- Should `intent=open` continue to create/start on `GET`, or should a follow-up design split this into `POST /launch` for stricter HTTP semantics?
- Should PR footer links for non-143 PRs be written automatically when auto-preview policy is enabled, or only shown in 143 UI?
- Should draft PRs have a separate `warm_drafts` policy later?
- How much startup log detail should be visible to viewers if logs may include repo-defined command output?

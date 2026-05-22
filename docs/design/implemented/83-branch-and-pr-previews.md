# Design: Standalone Branch Previews

> **Status:** Implemented
> **Last reviewed:** 2026-05-22

## Implementation Summary

The implemented branch preview primitive now has:

- Durable `preview_targets` and `preview_links` records for stable branch/commit/config targets.
- Target-owned `preview_instances` with nullable `session_id` and optional `preview_target_id`.
- `POST /api/v1/previews` to create or reuse a target, reserve a runtime, honor bounded `ttl_seconds`, persist `Idempotency-Key`, support `restart=true`, validate supplied SHAs against GitHub, validate committed `.143/config.json` preview config selection before target creation, support source-metadata idempotency, and enqueue `start_branch_preview`.
- `GET /api/v1/previews` and `GET /api/v1/previews/{preview_id}` for recent target/runtime lists and stable target/runtime status, including repo/branch/source links, current phase, failure text, services, infrastructure, and recent startup logs.
- `POST /api/v1/previews/{preview_id}/stop`, `/restart`, `/start-latest`, and `/bootstrap` for runtime control, latest branch-head starts, and preview access bootstrap.
- Durable PR preview resolution at `/previews/github/{owner}/{repo}/pull/{number}` backed by `GET /api/v1/previews/github/{owner}/{repo}/pull/{number}`. The route resolves the PR head, opens a matching active runtime, flags stale active previews with `new_commits_available`, starts the latest head when allowed, and keeps retry/diagnostics on the same stable URL.
- Generic stored preview-link resolution through `GET /api/v1/previews/links/{link_type}/{slug...}` and the matching `/previews/links/{link_type}/{slug...}` frontend route.
- A `start_branch_preview` worker job that creates a cold sandbox, clones the GitHub repo, checks out the pinned commit, loads `.143/config.json`, and launches through the existing preview manager/provider path.
- Config digest persistence on the target after the worker resolves the checked-out `.143/config.json`, with support for both single preview configs and named multi-config maps through `preview_config_name`. The frontend create flow also resolves committed config options through `GET /api/v1/previews/configs` and presents a selector when named configs exist.
- Branch preview creation entry points in the command palette, repository page, session detail header, PR header action, `/previews/new`, `/previews/[id]`, `/previews/links/{link_type}/{slug...}`, and the durable PR route.
- Generated GitHub PR bodies get a stable app-owned preview link appended after the PR number is known.
- Session preview reuse when a session-sourced branch preview matches an already-active session runtime at the same commit; the existing runtime is attached to the branch target instead of starting a cold clone. Session detail labels the action as `Open preview` or `Preview current sandbox` based on the running/unpushed session state.
- Scoped preview API tokens stored as hashes in `preview_api_tokens`, with `previews:create`, `previews:read`, and `previews:stop` scopes plus optional repository allowlists. Preview API tokens can authenticate only `/api/v1/previews*` routes.
- An admin Preview API settings page for creating, listing, and revoking scoped preview API tokens.
- Separate standalone branch-preview quota counters for user/org accounting so standalone branch previews do not consume the session-preview quota surface, while worker saturation remains shared.
- Startup progress and diagnostics in preview detail pages, including phase steps, stable-link expired state, created/source/request metadata, services, infrastructure, and recent logs.
- Metrics for branch preview creates, idempotency hits, stable-link opens with expiry state, checkout duration, install/build/start/readiness phase duration buckets, startup failures, runtime minutes on worker stop/cleanup paths, and startup concurrency deltas with org/repo dimensions.
- A small `cmd/preview` CLI for external callers: `143 preview create --repo owner/name --branch BRANCH`, with `--repository-id` retained for direct UUID callers.

Session previews continue to use their existing session-backed startup path while sharing the same runtime table, gateway, bootstrap token, lifecycle, worker routing machinery, and branch-preview target attachment when the user opens a branch-owned preview from session detail.

143 previews should become a first-class branch artifact. A preview is no longer primarily something hidden inside session detail; it is a cloud runtime for a specific repository branch and commit, with optional links back to the session, PR, automation, API caller, or human who requested it.

This is the long-term path. Session previews, PR preview links, and externally created previews should all use the same branch-preview primitive.

## Product Principle

The user should be able to answer one question quickly:

`Can I see this branch running?`

That should work whether the branch was created by a 143 session, by a human locally, by another coding agent, or by an external CI/system calling the 143 API.

## Goals

- Let authenticated users create a preview for any accessible repository branch without creating an agent session.
- Make preview creation obvious from the places where users already have code context: session detail, PR records, repository pages, command palette, and API.
- Give every 143-generated PR a durable preview link that resolves through the branch-preview system.
- Reuse a live 143 session sandbox when it exactly matches the requested branch/commit and is still safe to reuse.
- Support cold previews by checking out the branch from GitHub when no reusable session sandbox exists.
- Expose a public API that teams can call from scripts, CI, GitHub Actions, other coding-agent systems, or local tooling.
- Preserve the current security model: isolated preview origin, org-scoped access, short-lived tokens, worker-owned runtime, and bounded lifetime.

## Non-Goals

- Replacing repository-native deploy previews from Vercel, Netlify, Render, Fly, or CI/CD.
- Providing always-on staging environments.
- Supporting production secrets by default.
- Making unauthenticated public previews the default.
- Requiring users to understand sessions in order to create or view a preview.

## Core Model

A preview target is `(org, repository, branch, commit_sha, preview_config_name)`.

Everything else is metadata:

- A 143 session may point at that target.
- A GitHub PR may point at that target.
- An API caller may create that target.
- A stable preview link may resolve to that target.
- Multiple runtime attempts may exist over time for that target as previews start, expire, fail, and restart.

This keeps the product model simple: the thing being previewed is branch code.

## UX Entry Points

### 1. Current 143 Session

Session detail should make preview available where the user is already working:

- Add a primary or near-primary `Preview` action in the session header when the session has a repository branch.
- Keep the existing Preview tab for the live browser, logs, inspector, lifetime controls, and diagnostics.
- The header action starts or opens a branch preview for the session's current working branch and latest known commit/snapshot.
- If a preview is already active, the action reads `Open preview`.
- If the session is running and the branch is not yet pushed, the action can offer `Preview current sandbox` and clearly mark it as session-backed.
- If the branch has been published to GitHub, the preview target should pin the branch head SHA used to start the preview.

The important UX change is that users should not have to discover preview inside a side panel. The session remains a convenient source of repo/branch context, but the preview itself is branch-owned.

### 2. Non-143 Branches

Add a `Create preview` flow that works without a session.

Primary entry points:

- Global command palette: `Create preview`
- Repository page: `Preview branch`
- Pull request page or PR record: `Preview`
- Empty/current preview page: `New preview`
- Optional future CLI: `143 preview create --repo owner/name --branch my-branch`

The create form should be short and context-aware:

- Repository: searchable, defaults from current page.
- Branch: searchable remote branch picker with recent branches first.
- Commit: defaults to branch head; advanced users can pin a SHA.
- Preview config: resolved from the branch's committed `.143/config.json`; auto-select if there is only one config/default, otherwise show a compact selector for the repo-local config name.
- Lifetime: default policy, with advanced duration tucked behind `Options`.
- Action: `Start preview`.

The form should optimize for the common path: choose branch, start. Repository and config should often be prefilled.

### 3. Durable PR Links

Every 143-generated PR should include one small footer link:

`Preview: https://app.143.dev/.../previews/github/{owner}/{repo}/pull/{number}`

That URL should open a 143-owned preview page. It should not point directly at `<preview-id>.preview.143.dev`, because runtime previews are intentionally short-lived.

When opened:

- If a matching active preview exists, open it.
- If the active preview is for an older commit, show `New commits available` with `Start latest`.
- If no preview exists, start one from the PR head branch when the viewer has access.
- If startup fails, keep diagnostics at the same URL and offer retry.
- If the preview expired, show `Preview expired` with `Start again`.

This same durable route can be used for externally created PRs when a user manually starts a preview from the PR page or an API caller creates one for a PR branch.

### 4. Preview Landing Page

The stable preview page is the control plane for a branch preview. It should not look like a generic session page.

Recommended layout:

- Header: repo, branch, short SHA, source badge (`Session`, `PR`, `API`, `Manual`), status.
- Primary action area: `Open preview`, `Start preview`, `Restart`, or `Retry`.
- Startup progress: checkout, install/build, start services, readiness.
- Diagnostics: compact failure summary with expandable logs.
- Metadata: created by, created from, started at, expires at, config name.
- Links: session, PR, GitHub branch, API request ID when present.

The live preview can open inline or in a new tab, but the stable page remains the place to restart, inspect failures, and understand what code is being rendered.

## API

Expose a branch-preview API under `/api/v1/previews`.

### Create Preview

`POST /api/v1/previews`

Request:

```json
{
  "repository_id": "uuid",
  "branch": "feature/new-dashboard",
  "commit_sha": "optional-full-sha",
  "preview_config_name": null,
  "source": {
    "type": "api",
    "external_id": "github-actions-run-123",
    "url": "https://github.com/acme/app/actions/runs/123"
  },
  "ttl_seconds": 1800
}
```

Response:

```json
{
  "data": {
    "target_id": "uuid",
    "preview_id": "uuid",
    "status": "starting",
    "stable_url": "https://app.143.dev/orgs/acme/previews/abc",
    "preview_url": null,
    "expires_at": "2026-05-22T18:30:00Z"
  }
}
```

Rules:

- `repository_id` must belong to the request org.
- The caller must have permission to read the repository.
- If `commit_sha` is omitted, resolve the current branch head from GitHub and persist it.
- `preview_config_name` is optional and is a selector into the branch's committed `.143/config.json`, not a database ID. When omitted, the system should auto-select the only available config, use the repository default config, or run config detection from the checked-out workspace. If multiple configs exist and none is marked default, the API should return a validation error listing the available config names.
- If an active preview already exists for the same target, return it unless `restart=true` is requested.
- API-created previews should default to authenticated org-only access.
- Idempotency should be supported through `Idempotency-Key` and/or source metadata.

### Get Preview

`GET /api/v1/previews/{preview_id}`

Returns runtime status, stable URL, preview origin when ready, current phase, failure diagnostics, and expiry.

### List Previews

`GET /api/v1/previews?repository_id=&branch=&status=`

Returns recent branch previews for repository pages, command palette history, and API callers.

### Stop Preview

`POST /api/v1/previews/{preview_id}/stop`

Stops the active runtime while preserving the target and stable diagnostic/history page.

### Restart Preview

`POST /api/v1/previews/{preview_id}/restart`

Starts a new runtime attempt for the same target. If the caller wants the newest branch head, they should create a new target or call a `start_latest` action that resolves and pins the current SHA.

### Bootstrap Preview Access

Keep bootstrap/token exchange semantics isolated to the preview origin. The stable app URL should mint short-lived preview-domain access only after app/org authorization succeeds.

## Data Model Shape

Add branch targets instead of treating `preview_instances.session_id` as the owner:

- `preview_targets`
  - `id`
  - `org_id`
  - `repository_id`
  - `branch`
  - `commit_sha`
  - `preview_config_name`
  - `resolved_config_digest`
  - `source_type`: `session`, `pull_request`, `api`, `manual`, `automation`
  - `source_id`
  - `source_url`
  - `created_by_user_id`
  - `created_at`

- `preview_instances`
  - linked to `preview_target_id`
  - concrete runtime attempt
  - worker ownership, status, port, handle, phase, diagnostics, TTL, expiry
  - may keep nullable `session_id` during migration for compatibility

- `preview_links`
  - stable user-facing link
  - resolves to a target and latest allowed runtime attempt
  - supports PR footer links and API handoff URLs

- `preview_api_tokens` or existing org credentials extension
  - scoped tokens for creating previews by API
  - permissions should be repo-scoped where possible

All tables must be org-scoped.

## Runtime Flow

1. User or API creates a preview target for repo, branch, SHA, and config.
2. API validates org, repository access, branch/SHA existence, and preview config.
3. System checks for a reusable active session sandbox matching the same repo/branch/SHA.
4. If reusable, start preview from the session-owned sandbox and attach the instance to the branch target.
5. If not reusable, enqueue `start_branch_preview`.
6. Worker clones or hydrates the repository, checks out the pinned SHA, detects or applies preview config, starts services, and runs readiness checks.
7. Runtime writes phases and diagnostics to `preview_instances`.
8. Stable page observes status and bootstraps preview-domain access once ready.

Session-backed preview is an optimization, not a separate product mode.

## Permissions

Suggested defaults:

- Admin/member/builder can create previews for connected repositories they can access.
- Viewer can open existing previews but cannot create new ones unless explicitly allowed.
- API tokens can be scoped to `previews:create`, `previews:read`, `previews:stop`, and repository allowlists.
- Public preview sharing is off by default and should be a separate future policy.

Branch previews must not bypass repository ownership rules. If the org no longer owns or has access to the repo, new starts should fail closed even if old stable links exist.

## GitHub Integration

For 143-generated PRs:

- Append one durable preview link in the existing 143 footer.
- The link resolves by PR number and current head branch.
- If the PR head SHA changes, the page should detect that the latest target is stale and offer `Start latest`.

For non-143 PRs:

- A user can create a preview from the PR page if the GitHub App can read the head branch.
- API callers can create previews using repository/branch/SHA and then post the stable URL wherever they want.

Later polish:

- Add a `143 Preview` GitHub check run linked to the stable preview page.
- Keep the check informational, not a CI gate, unless an org explicitly configures preview assertions as required.

## Observability and Quotas

Track:

- create source: session, PR, manual, API, automation
- checkout time
- install/build/start/readiness phase durations
- startup failure class
- preview minutes consumed
- branch preview concurrency by org and repo
- API idempotency hits
- stable link opens before/after runtime expiry

Quotas should count standalone branch previews separately from session-held previews so API/manual usage cannot starve active agent sessions. Worker selection should still use the existing preview-capable worker model.

## Migration Plan

1. Add `preview_targets` and link new runtime attempts to targets while preserving session preview endpoints.
2. Build `POST /api/v1/previews` for branch/SHA targets.
3. Add the stable preview landing page.
4. Update session detail so its `Preview` action creates or opens a branch target for the current session branch.
5. Add repository/command-palette `Create preview`.
6. Put durable branch-preview links into 143-generated PR descriptions.
7. Add API token scopes and docs for external systems.
8. Move old session-only preview routes onto branch target internals.

The end state is one preview system: branch targets with short-lived runtime instances.

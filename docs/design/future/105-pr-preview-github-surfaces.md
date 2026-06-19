# Design: PR Preview GitHub Surfaces

> **Status:** Future
> **Last reviewed:** 2026-06-18

## Summary

Add Preview links to GitHub pull requests in connected repositories, including PRs not created by 143.

The preview runtime model, auto-preview policy, PR launch controller, and current preview index already exist. This design only adds GitHub surfacing for the stable PR launch URL:

```text
{frontend_url}/previews/github/{owner}/{repo}/pull/{number}?launch=1
```

In production, `{frontend_url}` defaults to `https://143.dev`. The `launch=1` query is required because the stable PR page uses it to bootstrap preview access and navigate to the preview origin when safe.

## Goal

Every reviewable PR in a connected repository should have an obvious Preview link in GitHub. Reviewers should not need to know whether the PR came from 143, a human, another coding agent, or CI automation.

Opening the link should use the implemented PR preview launch controller, which decides whether to open, wait, resume, start latest, retry, or show a blocked/closed state.

## Existing Pieces

- [implemented/100-pr-preview-launch-controller.md](../implemented/100-pr-preview-launch-controller.md): stable PR route and launch decisions.
- [implemented/99-previews-index-and-auto-preview-policy.md](../implemented/99-previews-index-and-auto-preview-policy.md): repository `auto_mode` and warm/on behavior.
- [future/102-preview-index-current-targets.md](102-preview-index-current-targets.md): `preview_groups`, current summaries, and current-preview actions.
- `PRService.stablePRPreviewURL`: already emits the stable launch URL.
- 143-created PRs: already append/replace a `Preview:` footer.
- `repository_preview_policies`: currently stores `auto_mode`.
- `pr_preview_state`: already stores `github_comment_id` and preview lifecycle state.

## Product Decisions

- GitHub surfacing is opt-in per repository.
- GitHub surfacing is only allowed when previews are configured and known to work for the repository.
- Surfacing is independent from auto-preview runtime policy. Repos can post links while `auto_mode=off`.
- V1 publishes two GitHub surfaces:
  - a link-only sticky PR comment,
  - a commit status on the PR head SHA.
- The sticky comment must not mirror live preview state, commit freshness, screenshots, startup phases, or failure text.
- The commit status is a link carrier, not a runtime-health gate.
- Externally created PR descriptions are not edited in v1.
- Fork and draft PRs may receive links, but existing safety rules still block unsafe auto-starts.

## Eligibility

Do not publish Preview links to GitHub until the repository is preview-ready.

A repository is preview-ready when:

- the repository has a valid committed preview configuration for the relevant branch/config,
- preview startup has succeeded at least once for that repository/config,
- the GitHub App has the permissions required for the selected surfaces.

If previews are not configured, config detection fails, startup has never succeeded, or previews are currently disabled for the repository, 143 should not create PR comments or commit statuses. The settings UI must keep `pr_preview_surfaces_enabled` disabled and show the missing prerequisite instead of allowing a no-op enable.

If previews later become broken after surfacing was enabled, stop publishing new GitHub surfaces until the repository returns to a preview-ready state. Existing comments/statuses can remain, because the stable route can explain the current failure, but new PRs should not get Preview links while the feature is known to be nonfunctional.

## GitHub UX

### Sticky Comment

143 creates or updates one marker comment:

```markdown
<!-- 143-preview-comment -->
### Preview

[Open preview](https://143.dev/previews/github/acme/web/pull/42?launch=1)

This link opens, resumes, starts, or diagnoses the latest preview for this PR.
```

Only rewrite the comment when it is missing, deleted, points to the wrong stable URL, or product copy changes.

### Commit Status

Publish a status on the current PR head SHA:

| Field | Value |
| --- | --- |
| `context` | `preview/143` |
| `state` | `success` |
| `target_url` | stable launch URL |
| `description` | `Open 143 preview` |

This should stay green because it means "the 143 preview entry point exists." If runtime health is needed later, add a separate status context.

### 143-Created PR Footer

Keep the existing footer for 143-created PRs:

```markdown
Preview: https://143.dev/previews/github/acme/web/pull/42?launch=1
```

Do not add this footer to externally created PRs in v1.

## Repository Policy

Extend `repository_preview_policies`:

| Column | Type | Default |
| --- | --- | --- |
| `pr_preview_surfaces_enabled` | boolean | `false` |
| `github_pr_comment_enabled` | boolean | `true` |
| `github_commit_status_enabled` | boolean | `true` |

Existing `auto_mode` remains the runtime policy:

- `off`: links only.
- `warm`: links plus build/hibernate on eligible PR events.
- `on`: links plus running previews subject to capacity and TTL.

Settings UI should show `PR preview links` separately from `Auto-preview`.

## Event Behavior

GitHub webhook handling should enqueue surface sync independently from auto-preview starts.

| Event | Surfacing | Runtime |
| --- | --- | --- |
| `opened`, `reopened`, `ready_for_review` | Enqueue sync when enabled. | Existing auto-preview path may start/warm if eligible. |
| `synchronize` | Enqueue sync for the new head SHA. | Existing path starts/warm latest and updates `preview_groups.latest_commit_sha`. |
| `converted_to_draft` | Leave existing surfaces. | Do not start new preview work while draft-skipping applies. |
| `closed` | No comment update required. | Existing teardown stops active auto-preview and marks state terminal. |

Surfacing should not require `auto_mode != off`, default-branch targeting, non-draft state, or non-fork state. It must still require repository preview readiness.

## Backfill

Do not backfill existing open PRs in v1.

When an admin enables `pr_preview_surfaces_enabled`, 143 starts publishing GitHub surfaces for new PR webhook events from that point forward. Existing open PRs remain untouched unless they later emit a qualifying event such as `synchronize`, `reopened`, or `ready_for_review`.

This avoids a sudden burst of GitHub writes and avoids surprising teams by commenting on old PRs.

## Capacity Impact

PR preview surfacing by itself does not start preview runtimes. Sticky comments and commit statuses are GitHub API writes only, so enabling `pr_preview_surfaces_enabled` has negligible worker capacity impact.

Worker capacity changes only when repository `auto_mode` is `warm` or `on`:

- `auto_mode=off`: no additional preview instances are created. Capacity impact is limited to short-lived surface sync jobs.
- `auto_mode=warm`: each eligible PR open/reopen/synchronize can create a preview instance long enough to build, snapshot, and hibernate. This increases peak startup concurrency but should not materially increase steady-state running preview count.
- `auto_mode=on`: eligible PRs can leave previews running until TTL/idle cleanup, increasing steady-state preview instances roughly in proportion to the number of active open PRs and update frequency.

Capacity planning should treat GitHub surfacing and auto-preview separately. A repository can safely enable PR preview links while leaving `auto_mode=off`; that creates review entry points without requiring more preview workers. Repositories using `warm` or `on` should continue to rely on the existing auto-preview pool, lower-priority queueing, TTLs, and saturation metrics before increasing worker count.

## Engineering Shape

### Schema

Add the repository policy fields above.

Add surfacing diagnostics to `pr_preview_state`:

```sql
last_surface_sync_sha text
last_surface_sync_at timestamptz
last_surface_sync_error text
```

Do not store stable URLs; derive them from configured frontend URL and PR identity. Do not duplicate PR head tracking already covered by webhook payloads and `preview_groups`.

### Jobs

Add:

- `sync_pr_preview_surfaces`

`sync_pr_preview_surfaces` payload should include `org_id`, `repository_id`, owner, repo, PR number, head SHA, fork flag, and draft flag.

Dedupe key:

```text
pr_preview_surface:{org_id}:{repository_id}:{pr_number}:{head_sha}
```

Sync job responsibilities:

1. Load policy.
2. Return if surfacing is disabled.
3. Return if the repository is not preview-ready.
4. Compute stable launch URL.
5. Upsert/read `pr_preview_state`.
6. Create/update marker comment if enabled.
7. Publish commit status if enabled.
8. Record sync success/error.

Comment sync must recover from deleted comments by searching for the marker before creating a new comment, and must guard against duplicate comments from concurrent jobs.

### GitHub Calls

Needed GitHub App operations:

- create issue comment,
- update issue comment,
- list issue comments,
- create commit status.

Use GitHub App installation tokens, not sandbox `gh`.

## Settings UI

In `/settings/previews`:

- Add `PR preview links` per repository.
- Add child toggles for `PR comment` and `Commit status`.
- Keep `Auto-preview` mode separate.
- Disable `PR preview links` until preview configuration is valid and a test preview has succeeded.
- Show a tooltip or inline message on the disabled toggle that names the missing prerequisite, for example `Add .143/config.json first` or `Run a successful test preview before enabling GitHub PR links`.
- Provide a `Test preview` action next to the disabled toggle when config exists but no successful preview has been recorded. The action should start a normal manual preview for the repository/config and mark the repository preview-ready only after that preview reaches `ready` or `partially_ready`.
- Show missing GitHub permissions and recent sync errors.

Required permissions:

| Surface | Permission |
| --- | --- |
| PR comment | Issues write or pull request comment write |
| Commit status | Commit statuses write |

## Failure Handling

- Comment failure must not block status publishing.
- Status failure must not block comment publishing.
- Final sync errors are stored on `pr_preview_state`.
- The stable route must work even if GitHub publishing fails.
- Rate limits should use normal job retry/backoff.
- Duplicate webhook deliveries must not create duplicate comments.

## Tests

Backend:

- Policy defaults and persistence.
- Surface sync enqueue behavior, including `auto_mode=off`.
- Preview-readiness gating: no comment/status when previews are unconfigured, have never succeeded, disabled, or known broken.
- Fork/draft/non-default-base PRs still get surfaces when enabled.
- Comment create/update/marker recovery/duplicate prevention.
- Commit status publishing.
- Partial failure recording.

Frontend:

- Settings toggles.
- Disabled-state message for missing config, missing successful preview, and missing GitHub permissions.
- `Test preview` action visibility when config exists but preview readiness is not yet proven.
- Missing-permission and sync-error states.

## Rollout

1. Add schema, models, stores, and tests.
2. Add sync jobs behind a flag.
3. Add settings API/UI.
4. Enable one internal repository.
5. Verify new PRs and new PR pushes get one comment and one commit status.
6. Monitor GitHub API errors, sync errors, duplicate comments, stable PR route opens, and auto-preview pool saturation for repos using `warm` or `on`.
7. Enable selected customer repositories.

## Open Questions

- Should link-carrier status keep `preview/143`, or use a distinct context such as `preview/143-link`?
- Should teams be able to suppress fork PR comments?
- Should externally created PR body footers stay permanently out of scope, or become a v2 opt-in setting?

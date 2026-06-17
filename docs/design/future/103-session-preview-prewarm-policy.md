# Design: Session Preview Prewarm Policy

> **Status:** Proposed
> **Last reviewed:** 2026-06-17

## Summary

143 already has three preview acceleration mechanisms:

- Live session sandbox reuse, which is the fastest path when the coding session container is still running.
- Branch/PR warm resume, which builds a committed branch preview, saves a startup snapshot, and can stop the runtime while keeping restart fast.
- Session preview dependency-cache prewarming, which runs `preview.install.command` in a low-priority ephemeral sandbox after successful snapshot-producing turns and stores package-manager and install-artifact caches.

The missing behavior is a policy for starting preview work as soon as a coding session begins, before the user clicks `Preview`. This design recommends a conservative product and technical path: use cheap cache warming broadly, use a small platform-LLM classifier to decide when stronger warming is justified, and keep live speculative previews out of the default path.

This design should integrate with the current-oriented preview index in [102-preview-index-current-targets.md](102-preview-index-current-targets.md). Any user-visible warm result should appear as the current preview surface for the session or eventual branch/PR group, not as a parallel prewarm-only concept.

## Goals

- Make likely preview opens feel ready or near-ready from the first click.
- Avoid turning every coding session into an extra long-running preview container.
- Keep foreground agent work and user-initiated preview starts ahead of speculative work.
- Respect existing preview isolation, org settings, worker capacity, and preview lifetime controls.
- Give admins a simple repo-level policy rather than per-session tuning.

## Non-Goals

- Always-on staging environments.
- Public unauthenticated previews.
- Running speculative preview services for every session by default.
- Replacing the existing cache prewarm, warm resume, or auto-preview PR policy.

## Recommended Product Shape

Add a repository-level setting on the existing Preview settings page:

| Mode | Behavior | Default use |
|---|---|---|
| `off` | Never start speculative session preview work. | Repos without preview config, expensive monorepos, self-hosters that want explicit control. |
| `cache` | Start only low-priority cache prewarm for eligible sessions. | Safe default for repos with preview config and meaningful dependency caches. |
| `smart` | Use a small classifier and capacity gates to choose `none`, `cache`, or delayed `warm`. | Hosted rollout after dogfood telemetry proves the classifier is conservative. |

Do not add a default `live` mode. A live speculative preview consumes the same scarce sandbox resources as an intentional preview and should remain behind an internal allowlist or a future explicit advanced setting.

The settings UI should sit beside repository auto-preview policy:

- Auto-preview continues to mean PR-triggered preview behavior.
- Session preview prewarm means speculative warming for coding sessions before a user clicks Preview.

This separation matters because PR auto-preview works from committed branch state, while session prewarm may use unpushed, moving workspace state.

## Decision Flow

When a coding session is created:

1. Load the repository policy.
2. If policy is `off`, do nothing.
3. Check cheap eligibility:
   - Repository has preview config or enough metadata to resolve one after checkout.
   - Preview install config has lockfiles and effective cache paths, or the repo has prior successful previews.
   - Org and worker speculative capacity are not saturated.
4. If policy is `cache`, enqueue cache prewarm when eligible.
5. If policy is `smart`, fire the classifier **asynchronously** (do not block session creation on the LLM call) and execute the selected action when the result arrives:
   - `none`: record the decision only.
   - `cache`: enqueue cache prewarm.
   - `warm_candidate`: wait until the first successful snapshot-producing turn, re-check freshness and capacity, then run a stop-after-ready warm build.

After every successful snapshot-producing `run_agent` or `continue_session` turn:

1. Re-evaluate only sessions with policy `smart`.
2. Skip if the user has already started a preview, the session is finished, the repo has no valid preview config, or a newer speculative job is already pending.
3. If the classifier previously returned `warm_candidate`, enqueue the warm build against the newest snapshot.
4. If the prior decision was `cache`, keep the existing cache-prewarm behavior and do not escalate unless classifier inputs changed meaningfully, such as the agent modifying frontend files.

This two-phase flow avoids the worst timing problem: at session creation, the system often does not yet know what files the agent will change or whether the initial prompt will actually produce previewable work.

## Classifier Contract

Use the platform LLM, not the coding-agent model. The call must be **async and non-blocking** relative to session creation — session creation should not wait for the classifier result. Budget 5 seconds maximum for the async call before recording a `classifier_timeout` and falling back to `none`. The classifier is independently configurable through the existing platform LLM settings.

### Inputs by phase

**Session-creation inputs** (always available):

- Repository identity and language/framework hints.
- Preview config presence and recent preview success/failure history.
- Session source: `manual`, `linear`, `sentry`, `automation`, `pr_repair`, `slack`, `api`.
- User prompt or issue summary — **truncated to 500 characters, with URLs, code blocks, and special tokens stripped** to prevent prompt injection from user-controlled content.
- Linked issue labels and issue type when available — **label values only, not label descriptions or comments**.
- Historical preview-open rate for the repo/source, when enough data exists (minimum 20 past sessions before this signal is weighted).
- Current capacity signal: available speculative slots, worker saturation, recent cache-prewarm skip rate.

**Post-turn additional inputs** (available for re-evaluation after the first snapshot turn):

- File paths written or modified in the turn, classified by type (frontend, backend, config, test, docs).
- Whether the turn produced a snapshot (required for warm-build eligibility).

Do not include full session transcripts, full issue bodies, logs, diffs, uploaded file content, or secret names in either phase.

### Output

```json
{
  "decision": "none|cache|warm_candidate",
  "confidence": 0.0,
  "reason": "ui_change|frontend_files|product_review|backend_only|docs_only|no_preview_config|capacity_tight|low_history",
  "explanation": "short operator-readable sentence"
}
```

### Decision guidance

- `none`: backend-only migrations, CLI work, docs-only edits, tests-only tasks, dependency maintenance, no preview config, repeated preview startup failures, or tight capacity.
- `cache`: UI-adjacent work with moderate confidence, repos with useful dependency caches, broad frontend repos where install dominates startup.
- `warm_candidate`: high-confidence user-visible UI/product work, PR review flows where reviewers often click preview, or sessions from repos with high historical preview-open rate.

The first implementation should default unknown or ambiguous classifier outputs to `cache` only when the repo is cheap enough and capacity is available; otherwise default to `none`. A classifier timeout or hard failure must fall back to `none`, not `cache` — fail closed on compute spend.

## Cache Prewarm Behavior

Cache prewarm should reuse the implemented `preview_cache_prewarm` job and runner (`internal/services/preview/start_runner.go`). The existing `PreviewCachePrewarmSourceSession` enum value already exists for the job payload source field.

Session creation should enqueue the job only when:

- Policy is `cache`, or policy is `smart` and classifier decision is `cache`.
- The repo has preview install lockfiles and effective cache paths, or those can be resolved from the workspace.
- The speculative pool has room.
- A recent equivalent prewarm run is not already `running` or `succeeded` — dedupe by `session_preview_cache_prewarm:<session_id>:<workspace_revision>:<config_digest>`.

**Same-repo concurrent sessions**: if two sessions start for the same repo within the per-repo cooldown window, only the first should enqueue cache prewarm. The per-repo cooldown applies across sessions; it is not session-scoped. See the Capacity Model for cooldown parameters.

The job remains disposable:

- Low priority below `run_agent`, `continue_session`, `start_preview` (user), and `pr_auto_preview`.
- Bounded by `PREVIEW_CACHE_PREWARM_TIMEOUT`.
- Capacity exhaustion records `skipped_capacity` and completes without retrying or dead-lettering.
- The sandbox is destroyed after cache save.
- No user-visible preview row is created.

This is the broad path because it reduces install/download latency without occupying a long-lived preview runtime.

## Warm Build Behavior

Warm builds are stronger than cache prewarm and should happen only after a successful snapshot-producing turn.

Add a session-scoped warm build path that behaves like branch auto-preview `warm` mode but uses the session snapshot:

1. Reserve a preview operation with initiator `session_prewarm` and record the session workspace revision it targets.
2. Hydrate or live-clone the newest session workspace.
3. Start a preview through the normal preview manager.
4. Wait for readiness.
5. Save startup snapshot and build/dependency caches.
6. Stop the runtime with stopped reason `session_prewarm_policy` (a new distinct value in `PreviewStoppedReason` — see Resolved Decisions).
7. Update the current preview surface to `warm` only if the session workspace revision still matches the latest durable session revision.
8. Mark the warmed preview as stale if a newer session snapshot appears before the user opens it.

The UI surfaces state only when user-actionable:

- `Warming preview` while the warm build is running.
- `Resume preview` with supporting text `Warmed and ready` only if the warmed snapshot matches the latest session snapshot and the worker-local startup snapshot is still available.
- If the user clicks Preview after the session has moved on, degrade to the normal fresh start path and do not present the stale warm runtime as ready.

Warm builds should use the same scheduler locality rules as existing preview cache and warm resume: prefer the live session worker, then workers with local relevant cache/snapshot state, then normal healthy workers.

### Current Preview Integration

User-visible warm builds should write through the current-preview grouping model:

- Session-only prewarm uses a `preview_groups` row with `group_kind = 'session'`, `source_id = session_id`, `branch = ''`, and `current_status = 'warm'` after the runtime is stopped. The `'warm'` value is already in the `current_status` check constraint as of migration `000196_preview_current_groups.up.sql`.
- Before using `branch = ''`, audit all existing queries on `preview_groups` for filters or grouping on the `branch` column. Any query that filters `branch != ''` or uses `branch` as a group key will silently exclude session-only warm rows.
- If the session later publishes or attaches to a branch/PR target, the warm surface migrates to the branch or PR group. Concretely: the session-scoped `preview_groups` row's `source_id` and `group_kind` are updated to reflect the branch/PR target, and the `session_preview_prewarm_runs.preview_group_id` FK remains pointing to the same row. Do not create a second `preview_groups` row; update the existing one through the target-attachment path used by normal session previews.
- The `preview_groups.current_status = 'warm'` value is the product signal for `Resume preview`; do not add `warm` to `PreviewStatus` unless preview instances themselves gain a non-running warm lifecycle state.
- **The grouped preview list must not hide warm state behind the latest stopped instance.** If the current index query uses `COALESCE(pi.status, pg.current_status)`, add explicit precedence when the instance was stopped by session prewarm:

  ```sql
  CASE
    WHEN pg.current_status = 'warm'
         AND pi.stopped_reason = 'session_prewarm_policy'
    THEN 'warm'
    ELSE COALESCE(pi.status, pg.current_status)
  END AS effective_status
  ```

  This query change must ship before `warm_candidate` decisions are enabled (see Rollout).
- Resume must verify the latest session workspace revision before using the warm snapshot. A mismatch clears or ignores the warm status and starts from the current workspace.

This keeps the Preview index, session Preview panel, and stable preview routes using one product vocabulary: a current preview surface may be running, warming, warm/resumable, stale, failed, or recent.

### Session Deletion and Cleanup

When a session is deleted, the `session_preview_prewarm_runs` row cascades automatically via the `ON DELETE CASCADE` FK. However, any associated `preview_instances` and `preview_groups` rows have their own lifecycle and will not cascade. Cleanup must:

- Mark any `preview_groups` row with `group_kind = 'session'` and `source_id = <deleted_session_id>` as `current_status = 'expired'`.
- Cancel any `running` or `queued` speculative jobs linked to the deleted session (via `session_preview_prewarm_runs.job_id`).
- Release the associated speculative capacity slot.

This cleanup should run in the same transaction or immediately-after hook as session deletion.

### Idempotency And Supersession

Speculative work must be exactly-once from the user's point of view and disposable from the worker's point of view.

- Cache prewarm dedupe key: `session_preview_cache_prewarm:<session_id>:<workspace_revision>:<config_digest>`.
- Warm build dedupe key: `session_preview_warm:<session_id>:<workspace_revision>:<config_digest>`.
- A newer workspace revision supersedes older pending/running warm jobs. Older jobs may finish cache saves, but they must not mark the current surface warm.
- User-initiated `Start Preview` supersedes speculative work immediately. Pending speculative jobs should no-op; running warm jobs should stop after cleanup and should not steal or overwrite the user's preview row.
- Classification decisions should be recorded once per session revision and config digest so repeated continuations do not create repeated LLM calls without new evidence.
- **Capacity reservation failure**: if a speculative capacity slot is reserved (the `session_preview_prewarm_runs` row is written with status `queued`) but the job fails to enqueue, the slot will remain reserved indefinitely unless cleaned up. A background sweep should mark rows stuck in `queued` for more than `PREVIEW_CACHE_PREWARM_TIMEOUT` as `failed` and release the slot. Use the same `started_at` / `completed_at` pattern as `preview_cache_prewarm_runs`.

### Failure Handling

Cache prewarm failures should remain quiet from the user experience, but they must still be logged and recorded for operators. Warm build failures are more expensive and closer to the user-visible preview path, so they need bounded visibility:

- Cache prewarm failures should log at warn level with `org_id`, `repository_id`, `session_id`, `workspace_revision`, `config_digest`, `prewarm_run_id`, `decision`, `reason`, and the classified error.
- Warm build failure state (`state: "failed"` in the session Preview status API) should only be returned when both: (a) the failure targets the current workspace revision, and (b) the user has opened the Preview panel. Before those conditions are met, record the failure in the decision/outcome table and logs only — do not surface it in the API or create session transcript messages.
- If the Preview panel is open or the user later clicks Preview, show the normal preview startup diagnostics as the latest failed warm attempt only when it targets the current workspace revision.
- Do not retry user-actionable failures such as missing config, install failure, image pull failure, or readiness timeout.
- Capacity and worker-drain failures should skip or defer with cooldown, not dead-letter as product failures.

## Capacity Model

Add an org-level speculative preview pool separate from:

- User/API active preview limits.
- PR auto-preview pool.
- Foreground coding-session concurrency.

Suggested setting:

```go
PreviewSessionPrewarmMaxActive int `json:"preview_session_prewarm_max_active,omitempty"`
```

Default/bounds:

- Default hosted dogfood: 1 or 2 per org.
- Default general/self-hosted: 0 until enabled.
- Default maximum once enabled: 10 per org.
- Bounds: 0-25.

When `PreviewSessionPrewarmMaxActive = 0`, the settings UI must disable the session prewarm mode selector and show an explanation: "Set speculative preview slots above 0 to enable session prewarm." Showing a mode selector that has no effect when the pool is 0 will confuse admins.

### Counting

- Count active speculative jobs from the `session_preview_prewarm_runs` table status field, not from sandbox existence. Status `running` means the slot is occupied; any terminal status (`succeeded`, `failed`, `skipped_*`) means it is free. Counting from sandbox existence is brittle under retries, worker restarts, and orphaned containers.
- Cache-prewarm jobs count while status is `running`.
- Warm builds count from status `queued` through `running` until any terminal state. Hibernated warm results (status `succeeded`, runtime stopped) do not count.

### Scheduling Rules

Job priority order, highest to lowest:

```
run_agent > continue_session > start_preview (user-initiated) > pr_auto_preview > session_warm_build > session_cache_prewarm
```

PR auto-preview is not user-initiated in the same sense as clicking Preview, but it represents committed work and should not be starved by speculative builds.

Additional rules:

- **Worker headroom**: never schedule speculative work on a worker with fewer than 2 free sandbox slots. This is a concrete floor, not "the last slot." Workers with exactly 1 free slot are reserved for user-initiated work.
- **Pool saturation**: if the org's speculative pool is at or above `PreviewSessionPrewarmMaxActive`, skip and record `skipped_capacity`.
- **Per-repo cooldown**: after any `skipped_capacity` event or consecutive warm build failures for a repo, apply a 5-minute cooldown before scheduling new speculative work for that repo. Reset the cooldown on a successful prewarm run. This prevents a busy automation or flaky repo from monopolizing the speculative pool.
- **Short-circuit on spin**: speculative work that is skipped or deferred should not retry. Record the skip reason and wait for the next natural trigger (next session creation, next snapshot turn).

Implementation should reserve speculative capacity durably before enqueueing a warm build, then release it in all terminal paths. A lightweight `session_preview_prewarm_runs` row acts as both the idempotency record and active-slot accounting source.

## Data Model

### `config_digest` Definition

`config_digest` is used as a component of all dedupe keys and the unique index. It must be deterministic and cheap to compute. Compose it as:

```
SHA256(preview_config_path + ":" + lockfile_paths_sorted + ":" + install_command)
```

where each field comes from the resolved preview config for the repo. If no preview config exists, use `""`. Reuse the same digest computation as `PreviewCachePrewarmScopeKey()` in `internal/services/preview/start_runner.go` so cache-prewarm runs and session-prewarm runs share a consistent config identity.

### Repository Policy

Add a repository policy row or extend `repository_preview_policies` with a separate session-prewarm mode:

```sql
ALTER TABLE repository_preview_policies
  ADD COLUMN session_prewarm_mode TEXT NOT NULL DEFAULT 'off',
  ADD CONSTRAINT repository_preview_policies_session_prewarm_mode_check
    CHECK (session_prewarm_mode IN ('off', 'cache', 'smart'));
```

If keeping PR auto-preview and session-prewarm policy in one table makes the row too overloaded, create `repository_session_preview_policies` instead. The single-table path is simpler because settings already list repositories and preview policy there.

Add typed string enums in `internal/models`:

- `PreviewSessionPrewarmMode`: `off`, `cache`, `smart`.
- `PreviewSpeculativeDecision`: `none`, `cache`, `warm_candidate`.

### Decision and Outcome Table

Track decisions and outcomes in a durable audit/debug table so classifier quality can be measured without scraping logs:

```sql
CREATE TABLE session_preview_prewarm_runs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    repository_id UUID NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    session_id UUID NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    -- workspace_revision = -1 means the decision was made at session creation
    -- before any snapshot was taken. Use -1 rather than 0 as the sentinel
    -- because revision 0 may be a valid initial workspace state.
    workspace_revision BIGINT NOT NULL DEFAULT -1,
    config_digest TEXT NOT NULL DEFAULT '',
    mode TEXT NOT NULL,
    decision TEXT NOT NULL,
    confidence DOUBLE PRECISION NOT NULL DEFAULT 0,
    reason TEXT NOT NULL DEFAULT '',
    explanation TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL,
    job_id UUID REFERENCES jobs(id) ON DELETE SET NULL,
    preview_id UUID REFERENCES preview_instances(id) ON DELETE SET NULL,
    preview_group_id UUID REFERENCES preview_groups(id) ON DELETE SET NULL,
    capacity_snapshot JSONB NOT NULL DEFAULT '{}'::jsonb,
    error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ
);
```

**Unique index note**: the index includes `decision` as a column, which intentionally allows two rows for the same session/revision/config — one for `cache` and one for `warm_candidate` — when the classifier is called twice (once at session creation, once post-turn). This is by design: the `cache` row tracks the cache-prewarm outcome and the `warm_candidate` row tracks the warm-build outcome. Queries for "the latest decision for this session" should use `ORDER BY created_at DESC LIMIT 1` or filter by `decision` explicitly.

```sql
CREATE UNIQUE INDEX idx_session_preview_prewarm_runs_scope
    ON session_preview_prewarm_runs (org_id, session_id, workspace_revision, config_digest, decision);
```

This table should not store full prompts or issue bodies. Store reason codes, short explanations, IDs, revision/config keys, and capacity snapshots only.

Run statuses should include at least `decided`, `queued`, `running`, `skipped_capacity`, `skipped_superseded`, `skipped_user_started`, `skipped_cooldown`, `classifier_timeout`, `succeeded`, and `failed`.

### API Surface

The settings API extends the existing preview-policy routes:

- `GET /api/v1/previews/policies` returns `session_prewarm_mode` with each repository policy row.
- `PUT /api/v1/repositories/{id}/preview-policy` accepts `session_prewarm_mode` alongside `auto_mode`. The existing `UpdatePolicy()` handler (`internal/api/handlers/branch_previews.go`) must support partial updates — sending only `session_prewarm_mode` must not overwrite `auto_mode` with a zero value. Verify and fix partial-update semantics in the handler before extending the endpoint.
- Org settings PATCH accepts `preview_session_prewarm_max_active`.

The session Preview status response includes optional prewarm state only for user-actionable states. The `failed` state is returned only when the failure targets the current workspace revision **and** the user has opened the Preview panel (tracked server-side via panel visibility or first-click event); suppress it otherwise.

```json
{
  "prewarm": {
    "state": "warming|warm|stale|failed",
    "workspace_revision": 12,
    "resume_estimate_seconds": 30
  }
}
```

Cache-only prewarm does not appear in this response.

**Push notification**: prewarm state transitions (`warming → warm`, `warming → failed`) should emit through the existing session event stream or websocket so clients do not need to poll. If no session event stream exists, clients must poll the session status endpoint; document the expected polling interval (recommend 10–15 seconds while `state = "warming"`).

## Product UX

### Settings Page

- Add a `Session prewarm` column or row under Preview settings for each repository.
- Modes labeled: `Off`, `Cache only`, `Smart`.
  - Use `Cache only` rather than `Cache` to make the scope of the mode immediately clear to admins who scan the UI without reading helper copy.
- Pool setting: `Speculative preview slots`.
- Show helper copy: `Cache only warms dependencies before the user clicks Preview. Smart mode may also prepare a full preview when a session looks likely to need one. Speculative work yields to active sessions and user-started previews.`
- **Disabled state**: when `Speculative preview slots = 0`, show the mode selector as disabled with inline text: `Set speculative preview slots above 0 to enable session prewarm.` This prevents admins from setting a mode that silently does nothing.
- **Discoverability**: repos already using `auto_mode = 'warm'` for PR auto-preview are natural candidates for `cache` mode session prewarm. Consider surfacing a one-time banner or an indicator in the settings list for these repos: `This repo uses warm PR previews — cache session prewarm is a natural next step.`

### Session Detail

- No new primary action.
- If cache prewarm is running, show no prominent state; cache prewarm is an implementation detail.
- If a warm build is running, show low-emphasis status in the Preview panel: `Warming preview`.
- If a warm build is current and resumable, the Preview button reads `Resume preview` with supporting text `Warmed and ready`. Do not use `Open warm preview` (implies a separate artifact) or `Ready to resume` (passive and unclear what resumes).
- If a warm build becomes stale, hide it and let the next Preview click start the current workspace normally.

### Operators

- Preview health dashboard should eventually show speculative pool usage and skip reasons.
- Logs should include `org_id`, `repository_id`, `session_id`, `decision`, `reason`, and `prewarm_run_id` or preview id.

## Security And Secrets

- **Classifier input bounding**: truncate text fields at 500 characters before sending to the platform LLM. Strip URLs, code blocks (triple-backtick regions), and special tokens from the user prompt and issue summary. Use only label names for linked issue labels, not descriptions or comments. Do not send full session transcripts, full issue bodies, logs, secrets, diffs, or uploaded file content.
- Cache-only prewarm should avoid mounting preview secret bundles by default. If a repo's install command requires secrets, require an explicit repo config opt-in before speculative cache prewarm can receive those bundles.
- Warm builds follow normal preview secret delivery because they execute the same runtime path as an intentional preview. This is why they require stronger classifier confidence and pool limits.
- **Untrusted session content**: sessions created from untrusted external branch content (e.g., a fork PR from an unverified contributor) must be treated as secrets-ineligible for both cache prewarm and warm builds unless the repository policy explicitly allows it. The same fork-PR trust model that governs PR auto-preview should be applied to session prewarm for any session whose workspace derives from a fork branch.
- Audit events are needed for policy changes, not for every classifier decision. Per-run decision records are operational telemetry rather than audit log entries.

## Guardrails

- Never start speculative work when the worker has fewer than 2 free sandbox slots; skip instead of retrying aggressively.
- Use lower job priority than `start_preview`, `run_agent`, `continue_session`, and `pr_auto_preview`.
- Add per-org caps for speculative active jobs and 5-minute per-repo cooldowns after capacity skips or consecutive failures.
- Cancel or mark stale speculative preview work when a newer session snapshot supersedes it.
- Do not mount preview secrets for low-confidence cache-only prewarm unless the install command requires them and the repo has explicitly allowed that behavior.
- Prefer session live-sandbox reuse when already owned by the worker; otherwise hydrate from durable snapshots.
- Surface speculative work as `Warming preview` or `Resume preview` only when the preview state is actually user-actionable.
- Never let speculative warm state overwrite a newer user-created preview, newer workspace revision, or current branch/PR group state.
- Treat classifier unavailable or timed-out as `none` for `smart` mode during rollout, not as `cache`; fail closed on spending compute.
- Clean up session-scoped `preview_groups` and cancel in-flight speculative jobs when the parent session is deleted.

## Metrics

- `session_preview_prewarm_decisions_total{mode, decision, source, reason}`
- `session_preview_prewarm_cost_seconds{decision, phase}` — label by `session_start` vs `post_turn` and by `cache` vs `warm_build`
- `session_preview_prewarm_skipped_total{reason}` — includes `capacity`, `cooldown`, `superseded`, `user_started`
- `session_preview_open_after_prewarm_total{decision}`
- `session_preview_click_to_ready_seconds{path}` — `path` distinguishes `live_reuse`, `warm_resume`, `prewarm_cache`, `prewarm_warm`, `cold_start`
- `session_preview_speculative_waste_total{reason}` — stale warm builds that were never opened
- `session_preview_live_minutes_total{initiator=speculative}`
- `session_preview_classifier_latency_seconds` — p50/p95 to verify the async call budget

The launch gate should be p50 click-to-ready improvement without a meaningful rise in worker saturation, OOM kills, or foreground queue delay.

## Rollout

1. Ship schema, settings UI, decision table, and `off`/`cache` mode. Default all repos to `off`. Verify partial-update semantics of the preview-policy PUT endpoint before launch.
2. Enable `cache` mode for selected dogfood repos. Verify cache hit rate, skipped-capacity rate, and click-to-ready deltas.
3. Add classifier in shadow mode for `smart`: record decisions but execute only cache-mode behavior. **Also set up the correlation query or dashboard** that joins `session_preview_prewarm_runs.decision = 'warm_candidate'` against session analytics to measure how often those sessions actually led to a Preview open. This is the signal needed to evaluate classifier precision before step 4.
4. Turn on `smart` for dogfood repos with conservative thresholds. Permit only `none` and `cache` decisions initially.
5. **Ship the `effective_status` COALESCE fix** for the preview index query (see Current Preview Integration) and the `session_prewarm_policy` stopped reason. These are prerequisites for user-visible warm state to appear correctly.
6. Enable `warm_candidate` after first-turn snapshots once stale-result handling and UI copy (`Resume preview` / `Warmed and ready`) are in place.
7. Add grouped current-preview integration for session warm results, if not already complete from step 5.
8. Consider live speculative previews only after warm builds show high preview-open conversion and low waste. This should be a separate design or an explicit advanced setting, not part of the default rollout.

## Resolved Decisions

- **`PreviewStoppedReason` for warm builds**: add `session_prewarm_policy` as a distinct value in `PreviewStoppedReason`. Do not reuse `warm_policy` with `initiator` as a differentiator — consumers would have to join on two fields instead of one, and the index query precedence logic depends on a single discriminant.
- **Resume copy**: use `Resume preview` as the button label with `Warmed and ready` as supporting text. `Open warm preview` implies a separate artifact; `Ready to resume` is passive and unclear.
- **`workspace_revision` sentinel**: use `-1` for decisions made at session creation before any snapshot exists. Revision `0` may be a valid initial state; `-1` is unambiguous.

## Open Questions

- Should session warm builds create a `preview_target` immediately, or should session-only `preview_groups` support warm state without a target until branch publication? Creating a target improves history consistency but may require a synthetic commit/revision identity for unpushed work.
- Should smart mode require a minimum historical preview-open rate (e.g., at least 20 past sessions) before permitting `warm_candidate` for new repositories, or can high-confidence prompt/file signals alone justify it?
- **Shadow-mode evaluation**: what is the minimum number of shadow decisions and what precision threshold (e.g., >70% of `warm_candidate` decisions correlating with actual preview opens) should gate the transition from step 4 to step 6?
- **Concurrent same-repo sessions**: if two sessions for the same repo start within the per-repo cooldown window, only the first enqueues cache prewarm. Should the second session be eligible for its own prewarm if the first session's prewarm completes (and its result is reusable), or should the cooldown suppress it regardless?

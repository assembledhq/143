# Design: Session Preview Prewarm Policy

> **Status:** Implemented
> **Last reviewed:** 2026-06-19

## Summary

143 already has three preview acceleration mechanisms:

- Live session sandbox reuse, which is the fastest path when the coding session container is still running.
- Branch/PR warm resume, which builds a committed branch preview, saves a startup snapshot, and can stop the runtime while keeping restart fast.
- Session preview dependency-cache prewarming, which runs `preview.install.command` in a low-priority ephemeral sandbox after successful snapshot-producing turns and stores package-manager and install-artifact caches.

The missing piece is a policy for starting preview work at session creation, before the user clicks `Preview`. This design uses cheap cache warming broadly, a small platform-LLM classifier to decide when stronger warming is justified, and keeps live speculative previews out of the default path.

Any user-visible warm result should appear as the current preview surface for the session or eventual branch/PR group, not as a parallel prewarm-only concept. See [102-preview-index-current-targets.md](102-preview-index-current-targets.md).

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

The settings UI should sit beside repository auto-preview policy. Auto-preview means PR-triggered preview behavior; session preview prewarm means speculative warming for coding sessions before a user clicks Preview. PR auto-preview works from committed branch state; session prewarm may use unpushed, moving workspace state.

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

## Classifier Contract

Use the platform LLM, not the coding-agent model. The call must be **async and non-blocking** relative to session creation. Budget 5 seconds maximum before recording a `classifier_timeout` and falling back to `none`. The classifier is independently configurable through the existing platform LLM settings.

### Inputs by phase

**Session-creation inputs** (always available):

- Repository identity and language/framework hints.
- Preview config presence and recent preview success/failure history.
- Session source: `manual`, `linear`, `sentry`, `automation`, `pr_repair`, `slack`, `api`.
- User prompt or issue summary — **truncated to 500 characters, URLs, code blocks, and special tokens stripped** to prevent prompt injection.
- Linked issue labels and issue type — **label values only, not descriptions or comments**.
- Historical preview-open rate for the repo/source (minimum 20 past sessions before this signal is weighted).
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

Default unknown or ambiguous classifier outputs to `cache` only when the repo is cheap enough and capacity is available; otherwise default to `none`. A classifier timeout or hard failure falls back to `none` — fail closed on compute spend.

## Cache Prewarm Behavior

Cache prewarm reuses the implemented `preview_cache_prewarm` job and runner (`internal/services/preview/start_runner.go`).

Session creation should enqueue the job only when:

- Policy is `cache`, or policy is `smart` and classifier decision is `cache`.
- The repo has preview install lockfiles and effective cache paths, or those can be resolved from the workspace.
- The speculative pool has room.
- A recent equivalent prewarm run is not already `running` or `succeeded` — dedupe by `session_preview_cache_prewarm:<session_id>:<workspace_revision>:<config_digest>`.

**Same-repo concurrent sessions**: if two sessions start for the same repo within the per-repo cooldown window, only the first should enqueue cache prewarm. The per-repo cooldown applies across sessions, not per-session. See Capacity Model.

The job remains disposable:

- Low priority below `run_agent`, `continue_session`, `start_preview` (user), and `pr_auto_preview`.
- Bounded by `PREVIEW_CACHE_PREWARM_TIMEOUT`.
- Capacity exhaustion records `skipped_capacity` and completes without retrying or dead-lettering.
- The sandbox is destroyed after cache save.
- No user-visible preview row is created.

## Warm Build Behavior

Warm builds happen only after a successful snapshot-producing turn.

Add a session-scoped warm build path that behaves like branch auto-preview `warm` mode but uses the session snapshot:

1. Reserve a preview operation with initiator `session_prewarm` and record the session workspace revision it targets.
2. Hydrate or live-clone the newest session workspace.
3. Start a preview through the normal preview manager.
4. Wait for readiness.
5. Save startup snapshot and build/dependency caches.
6. Stop the runtime with stopped reason `session_prewarm_policy` — a new distinct value in `PreviewStoppedReason`, separate from `warm_policy`, so the preview index query can use it as a single discriminant.
7. Update the current preview surface to `warm` only if the session workspace revision still matches the latest durable session revision.
8. Retire the warmed preview run if a newer session snapshot appears before the user opens it.

UI state, shown only when user-actionable:

- `Warming preview` while the warm build is running.
- `Resume preview` with supporting text `Warmed and ready` — only when the warmed snapshot matches the latest session snapshot and the worker-local startup snapshot is still available.
- When the warm build is stale, hide it and let the next Preview click start the current workspace normally.

Warm builds use the same scheduler locality rules as existing preview cache and warm resume: prefer the live session worker, then workers with local relevant cache/snapshot state, then normal healthy workers.

### Current Preview Integration

User-visible warm builds write through the current-preview grouping model:

- Session-only prewarm uses a `preview_groups` row with `group_kind = 'session'`, `source_id = session_id`, `branch = ''`, and `current_status = 'warm'` after the runtime is stopped. (`'warm'` is already in the `current_status` check constraint as of migration `000196_preview_current_groups.up.sql`.)
- Before using `branch = ''`, audit all existing queries on `preview_groups` for filters or grouping on the `branch` column — any filter on `branch != ''` will silently exclude session-only warm rows.
- When the session later publishes or attaches to a branch/PR target, update the session-scoped `preview_groups` row's `source_id` and `group_kind` to reflect the target. Do not create a second row; migrate the existing one through the normal target-attachment path. The `session_preview_prewarm_runs.preview_group_id` FK continues pointing to the same row.
- The `preview_groups.current_status = 'warm'` value is the product signal for `Resume preview`. Do not add `warm` to `PreviewStatus` unless preview instances themselves gain a non-running warm lifecycle state.
- **The grouped preview list must not hide warm state behind the latest stopped instance.** Where the current index query uses `COALESCE(pi.status, pg.current_status)`, add explicit precedence:

  ```sql
  CASE
    WHEN pg.current_status = 'warm'
         AND pi.stopped_reason = 'session_prewarm_policy'
    THEN 'warm'
    ELSE COALESCE(pi.status, pg.current_status)
  END AS effective_status
  ```

  This query change is a prerequisite for enabling `warm_candidate` — see Rollout step 5.
- Resume must verify the latest session workspace revision before using the warm snapshot. A mismatch clears warm status and starts from the current workspace.

### Session Deletion and Cleanup

The `session_preview_prewarm_runs` row cascades on session deletion. Associated `preview_instances` and `preview_groups` rows do not. On session deletion:

- Set `current_status = 'expired'` on any `preview_groups` row with `group_kind = 'session'` and `source_id = <deleted_session_id>`.
- Cancel any `running` or `queued` speculative jobs via `session_preview_prewarm_runs.job_id`.
- Release the associated speculative capacity slot.

### Idempotency And Supersession

- Cache prewarm dedupe key: `session_preview_cache_prewarm:<session_id>:<workspace_revision>:<config_digest>`.
- Warm build dedupe key: `session_preview_warm:<session_id>:<workspace_revision>:<config_digest>`.
- A newer workspace revision supersedes older pending/running warm jobs. Older jobs may finish cache saves but must not mark the current surface warm.
- User-initiated `Start Preview` supersedes speculative work immediately. Pending speculative jobs no-op; running warm jobs stop after cleanup and must not overwrite the user's preview row.
- Classification decisions are recorded once per session revision and config digest to prevent repeated LLM calls without new evidence.
- **Capacity reservation failure**: if a slot is reserved (row written with status `queued`) but the job fails to enqueue, a background sweep must mark rows stuck in `queued` beyond `PREVIEW_CACHE_PREWARM_TIMEOUT` as `failed` and release the slot. Use the same `started_at` / `completed_at` pattern as `preview_cache_prewarm_runs`.

### Failure Handling

- Cache prewarm failures log at warn level with `org_id`, `repository_id`, `session_id`, `workspace_revision`, `config_digest`, `prewarm_run_id`, `decision`, `reason`, and the classified error. No user-visible signal.
- Warm build `state: "failed"` is returned in the session Preview status API only when: (a) the failure targets the current workspace revision, and (b) the user has opened the Preview panel. Otherwise record in the decision table and logs only.
- When the Preview panel is open or the user clicks Preview, show preview startup diagnostics for the latest failed warm attempt only when it targets the current workspace revision.
- Do not retry user-actionable failures: missing config, install failure, image pull failure, readiness timeout.
- Capacity and worker-drain failures skip or defer with cooldown, not dead-letter.

## Capacity Model

Add an org-level speculative preview pool separate from user/API active preview limits, PR auto-preview pool, and foreground coding-session concurrency.

```go
PreviewSessionPrewarmMaxActive int `json:"preview_session_prewarm_max_active,omitempty"`
```

Defaults and bounds:

- Hosted dogfood: 1 or 2 per org.
- General/self-hosted: 0 until enabled.
- Maximum once enabled: 10 per org.
- Bounds: 0–25.

When `PreviewSessionPrewarmMaxActive = 0`, disable the session prewarm mode selector in the settings UI with the text: `Set speculative preview slots above 0 to enable session prewarm.`

### Counting

Count active speculative jobs from `session_preview_prewarm_runs.status`, not from sandbox existence. Status `running` means the slot is occupied; any terminal status means it is free. Cache-prewarm jobs count while `running`. Warm builds count from `queued` through `running` until any terminal state. Hibernated warm results (runtime stopped, status `succeeded`) do not count.

### Scheduling Rules

Job priority order, highest to lowest:

```
run_agent > continue_session > start_preview (user-initiated) > pr_auto_preview > session_warm_build > session_cache_prewarm
```

- **Worker headroom**: never schedule speculative work on a worker with fewer than 2 free sandbox slots.
- **Pool saturation**: if the org's speculative pool is at or above `PreviewSessionPrewarmMaxActive`, skip and record `skipped_capacity`.
- **Per-repo cooldown**: after any `skipped_capacity` event or consecutive warm build failures, apply a 5-minute cooldown before scheduling new speculative work for that repo. Reset on a successful prewarm run.
- Skipped or deferred speculative work does not retry. Wait for the next natural trigger.

Reserve speculative capacity durably before enqueueing a warm build; release it in all terminal paths.

## Data Model

### `config_digest`

Compose as `SHA256(preview_config_path + ":" + lockfile_paths_sorted + ":" + install_command)` from the resolved preview config. Use `""` when no preview config exists. Reuse the same computation as `PreviewCachePrewarmScopeKey()` in `internal/services/preview/start_runner.go`.

### Repository Policy

```sql
ALTER TABLE repository_preview_policies
  ADD COLUMN session_prewarm_mode TEXT NOT NULL DEFAULT 'off',
  ADD COLUMN session_prewarm_untrusted_fork BOOLEAN NOT NULL DEFAULT false,
  ADD CONSTRAINT repository_preview_policies_session_prewarm_mode_check
    CHECK (session_prewarm_mode IN ('off', 'cache', 'smart'));
```

If the single table becomes too overloaded, create `repository_session_preview_policies` instead. The single-table path is simpler because settings already list repositories and preview policy there.

Add typed string enums in `internal/models`:

- `PreviewSessionPrewarmMode`: `off`, `cache`, `smart`.
- `PreviewSpeculativeDecision`: `none`, `cache`, `warm_candidate`.

### Decision and Outcome Table

```sql
CREATE TABLE session_preview_prewarm_runs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    repository_id UUID NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    session_id UUID NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    -- -1 = decision made at session creation before any snapshot; 0 is a valid revision
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
    completed_at TIMESTAMPTZ,
    panel_opened_at TIMESTAMPTZ
);
```

The unique index includes `decision` to intentionally allow two rows per scope — one for `cache` and one for `warm_candidate` — when the classifier runs twice (session creation and post-turn). Queries for the latest decision use `ORDER BY created_at DESC LIMIT 1` or filter by `decision` explicitly.

```sql
CREATE UNIQUE INDEX idx_session_preview_prewarm_runs_scope
    ON session_preview_prewarm_runs (org_id, session_id, workspace_revision, config_digest, decision);
```

Store reason codes, short explanations, IDs, revision/config keys, and capacity snapshots only. No full prompts or issue bodies.

Run statuses: `decided`, `queued`, `running`, `skipped_capacity`, `skipped_superseded`, `skipped_user_started`, `skipped_cooldown`, `skipped_untrusted_fork`, `skipped_no_lockfiles`, `skipped_no_paths`, `classifier_timeout`, `succeeded`, `failed`.

### API Surface

- `GET /api/v1/previews/policies` returns `session_prewarm_mode` and `session_prewarm_untrusted_fork` with each repository policy row.
- `PUT /api/v1/repositories/{id}/preview-policy` accepts `session_prewarm_mode` and `session_prewarm_untrusted_fork` alongside `auto_mode`. Partial updates preserve omitted fields — sending only `session_prewarm_mode` must not zero out `auto_mode`.
- Org settings PATCH accepts `preview_session_prewarm_max_active`.

The session Preview status response includes prewarm state only for user-actionable states. `failed` is returned only when the failure targets the current workspace revision **and** the user has opened the Preview panel.

```json
{
  "prewarm": {
    "state": "warming|warm|failed",
    "workspace_revision": 12,
    "resume_estimate_seconds": 30,
    "preview_id": "018f...",
    "error": "preview install failed before services started"
  }
}
```

Cache-only prewarm does not appear in this response.

State transitions (`warming → warm`, `warming → failed`) should emit through the existing session event stream or websocket. The current implementation uses Preview panel polling every ~12 seconds while `state = "warming"`.

## Product UX

### Settings Page

- Add a `Session prewarm` row under Preview settings for each repository.
- Mode labels: `Off`, `Cache only`, `Smart`. (`Cache only` is clearer than `Cache` for admins scanning without helper copy.)
- Fork prewarm remains off and is not exposed in the normal settings UI. Sessions derived from untrusted fork content should not be speculatively warmed unless a reviewed operator-only path explicitly allows it.
- Pool setting: `Speculative preview slots`.
- Helper copy: `Cache only installs dependencies ahead of time without starting the app. Smart starts with cache warming and may prepare a full preview when a session looks likely to need one. Speculative work yields to active sessions and user-started previews.`
- When `Speculative preview slots = 0`, disable the mode selector with inline text: `Set speculative preview slots above 0 to enable session prewarm.`
- Repos already using `auto_mode = 'warm'` for PR auto-preview are natural candidates for `cache` mode. Consider surfacing an indicator in the settings list for these repos.

### Session Detail

- No new primary action.
- If cache prewarm is running, show no prominent state.
- If a warm build is running, show low-emphasis status in the Preview panel: `Warming preview`.
- If a warm build is resumable, the Preview button reads `Resume preview` with supporting text `Warmed and ready`.
- When stale, hide warm state and let the next Preview click start normally.

### Operators

- Preview health dashboard should show speculative pool usage and skip reasons.
- Logs must include `org_id`, `repository_id`, `session_id`, `decision`, `reason`, and `prewarm_run_id`.

## Security And Secrets

- **Classifier input bounding**: truncate text fields at 500 characters; strip URLs, code blocks, and special tokens from user prompt and issue summary; send label names only for issue labels.
- Cache-only prewarm avoids mounting preview secret bundles by default. Repos whose install command requires secrets must explicitly opt in before speculative cache prewarm receives those bundles.
- Warm builds follow normal preview secret delivery because they run the same path as an intentional preview.
- Sessions derived from untrusted fork branch content are secrets-ineligible for both cache prewarm and warm builds unless the repository policy explicitly allows it. Apply the same fork-PR trust model as PR auto-preview.
- Audit events are needed for policy changes, not for individual classifier decisions.

## Guardrails

- Never start speculative work when the worker has fewer than 2 free sandbox slots.
- Use lower job priority than `start_preview`, `run_agent`, `continue_session`, and `pr_auto_preview`.
- Apply per-org caps and 5-minute per-repo cooldowns after capacity skips or consecutive failures.
- Cancel or hide stale speculative work when a newer session snapshot supersedes it.
- Do not prewarm sessions from untrusted fork content unless the repository policy explicitly allows it.
- Do not mount preview secrets for cache-only prewarm unless required and explicitly allowed.
- Prefer live-sandbox reuse when already owned by the worker; otherwise hydrate from durable snapshots.
- Surface warm state only when user-actionable. Never let speculative state overwrite a newer user-created preview, workspace revision, or branch/PR group state.
- Treat classifier timeout or unavailability as `none`; fail closed on compute spend.
- Clean up session-scoped `preview_groups` and cancel in-flight jobs when the parent session is deleted.

## Metrics

- `session_preview_prewarm_decisions_total{mode, decision, source, reason}`
- `session_preview_prewarm_cost_seconds{decision, phase}` — phase: `session_start` or `post_turn`; decision: `cache` or `warm_build`
- `session_preview_prewarm_skipped_total{reason}` — `capacity`, `cooldown`, `superseded`, `user_started`, `untrusted_fork`, `no_lockfiles`, `no_paths`
- `session_preview_open_after_prewarm_total{decision}`
- `session_preview_click_to_ready_seconds{path}` — `live_reuse`, `warm_resume`, `prewarm_cache`, `cold_start`
- `session_preview_speculative_waste_total{reason}`
- `session_preview_live_minutes_total{initiator=speculative}`
- `session_preview_classifier_latency_seconds` — p50/p95 to verify the 5-second async budget

Launch gate: p50 click-to-ready improvement without meaningful rise in worker saturation, OOM kills, or foreground queue delay.

## Rollout

1. Ship schema, settings UI, decision table, and `off`/`cache` mode. Default all repos to `off`. Verify partial-update semantics of the preview-policy PUT endpoint before launch.
2. Enable `cache` mode for selected dogfood repos. Verify cache hit rate, skipped-capacity rate, and click-to-ready deltas.
3. Add classifier in shadow mode for `smart`: record decisions but execute only cache-mode behavior. Build the correlation query joining `decision = 'warm_candidate'` against session analytics to measure preview-open conversion — this gates step 4.
4. Turn on `smart` for dogfood repos with conservative thresholds. Permit only `none` and `cache` decisions initially.
5. Ship the `effective_status` COALESCE fix (see Current Preview Integration) and add `session_prewarm_policy` to `PreviewStoppedReason`. These are prerequisites for user-visible warm state.
6. Enable `warm_candidate` once stale-result handling and UI copy (`Resume preview` / `Warmed and ready`) are in place.
7. Consider live speculative previews only after warm builds show high preview-open conversion and low waste. This should be a separate design or explicit advanced setting.

## Open Questions

- Should session warm builds create a `preview_target` immediately, or should session-only `preview_groups` support warm state without a target until branch publication? Creating a target improves history consistency but requires a synthetic commit/revision identity for unpushed work.
- Should smart mode require a minimum historical preview-open rate (e.g., 20 past sessions) before permitting `warm_candidate` for new repositories, or can high-confidence prompt/file signals alone justify it?
- What minimum shadow-decision volume and `warm_candidate` precision threshold (e.g., >70% correlation with actual preview opens) should gate the move from step 4 to step 6?
- If two sessions for the same repo start within the per-repo cooldown window, should the second be eligible for its own prewarm once the first completes and its result is reusable, or does the cooldown suppress it regardless?

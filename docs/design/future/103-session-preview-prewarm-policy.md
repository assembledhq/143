# Design: Session Preview Prewarm Policy

> **Status:** Proposed
> **Last reviewed:** 2026-06-16

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
5. If policy is `smart`, run the classifier and execute the selected action:
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

Use the platform LLM, not the coding-agent model. The call should be cheap, latency-tolerant, and independently configurable through the existing platform LLM settings.

Input fields:

- Repository identity and language/framework hints.
- Preview config presence and recent preview success/failure history.
- Session source: manual, Linear, Sentry, automation, PR repair, Slack, API.
- User prompt or issue summary.
- Linked issue labels and issue type when available.
- Early file hints when available, such as planned or changed paths after the first turn.
- Historical preview-open rate for the repo/source, when enough data exists.
- Current capacity signal: available speculative slots, worker saturation, recent cache-prewarm skip rate.

Output:

```json
{
  "decision": "none|cache|warm_candidate",
  "confidence": 0.0,
  "reason": "ui_change|frontend_files|product_review|backend_only|docs_only|no_preview_config|capacity_tight|low_history",
  "explanation": "short operator-readable sentence"
}
```

Decision guidance:

- `none`: backend-only migrations, CLI work, docs-only edits, tests-only tasks, dependency maintenance, no preview config, repeated preview startup failures, or tight capacity.
- `cache`: UI-adjacent work with moderate confidence, repos with useful dependency caches, broad frontend repos where install dominates startup.
- `warm_candidate`: high-confidence user-visible UI/product work, PR review flows where reviewers often click preview, or sessions from repos with high historical preview-open rate.

The first implementation should default unknown or ambiguous classifier outputs to `cache` only when the repo is cheap enough and capacity is available; otherwise default to `none`.

## Cache Prewarm Behavior

Cache prewarm should reuse the implemented `preview_cache_prewarm` job and runner.

Session creation should enqueue the job only when:

- Policy is `cache`, or policy is `smart` and classifier decision is `cache`.
- The repo has preview install lockfiles and effective cache paths, or those can be resolved from the workspace.
- The speculative pool has room.
- A recent equivalent prewarm run is not already `running` or `succeeded`.

The job remains disposable:

- Low priority below user-initiated `start_preview`, `run_agent`, and `continue_session`.
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
6. Stop the runtime with a distinct stopped reason, preferably a new `session_prewarm_policy` value so PR auto-preview warm stops remain distinguishable.
7. Update the current preview surface to `warm` only if the session workspace revision still matches the latest durable session revision.
8. Mark the warmed preview as stale if a newer session snapshot appears before the user opens it.

The UI may surface this only when it is true:

- `Warming preview` while the warm build is running.
- `Ready to resume` only if the warmed snapshot matches the latest session snapshot and the worker-local startup snapshot is still available.
- If the user clicks Preview after the session has moved on, degrade to the normal fresh start path and do not present the stale warm runtime as ready.

Warm builds should use the same scheduler locality rules as existing preview cache and warm resume: prefer the live session worker, then workers with local relevant cache/snapshot state, then normal healthy workers.

### Current Preview Integration

User-visible warm builds should write through the current-preview grouping model:

- Session-only prewarm uses a `preview_groups` row with `group_kind = 'session'`, `source_id = session_id`, `branch = ''`, and `current_status = 'warm'` after the runtime is stopped.
- If the session later publishes or attaches to a branch/PR target, the warm surface should migrate to the branch or PR group through the same target-attachment path used by normal session previews.
- The `preview_groups.current_status = 'warm'` value is the product signal for `Ready to resume`; do not add `warm` to `PreviewStatus` unless preview instances themselves gain a non-running warm lifecycle state.
- The grouped preview list must not accidentally hide warm state behind the latest stopped `preview_instances.status`. If the current index query uses `COALESCE(latest.status, pg.current_status)`, it needs explicit precedence for `pg.current_status = 'warm'` when the latest instance stopped because of session prewarm.
- Resume must verify the latest session workspace revision before using the warm snapshot. A mismatch clears or ignores the warm status and starts from the current workspace.

This keeps the Preview index, session Preview panel, and stable preview routes using one product vocabulary: a current preview surface may be running, warming, warm/resumable, stale, failed, or recent.

### Idempotency And Supersession

Speculative work must be exactly-once from the user's point of view and disposable from the worker's point of view.

- Cache prewarm dedupe key: `session_preview_cache_prewarm:<session_id>:<workspace_revision>:<config_digest>`.
- Warm build dedupe key: `session_preview_warm:<session_id>:<workspace_revision>:<config_digest>`.
- A newer workspace revision supersedes older pending/running warm jobs. Older jobs may finish cache saves, but they must not mark the current surface warm.
- User-initiated `Start Preview` supersedes speculative work immediately. Pending speculative jobs should no-op; running warm jobs should stop after cleanup and should not steal or overwrite the user's preview row.
- Classification decisions should be recorded once per session revision and config digest so repeated continuations do not create repeated LLM calls without new evidence.

### Failure Handling

Cache prewarm failures should remain quiet from the user experience, but they must still be logged and recorded for operators. Warm build failures are more expensive and closer to the user-visible preview path, so they need bounded visibility:

- Cache prewarm failures should log at warn level with `org_id`, `repository_id`, `session_id`, `workspace_revision`, `config_digest`, `prewarm_run_id`, `decision`, `reason`, and the classified error.
- If the user has not opened the Preview panel, record warm build failure in the decision/outcome table and logs, but do not create noisy session transcript messages.
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

Counting:

- Cache-prewarm jobs count while their ephemeral sandbox exists.
- Warm builds count from sandbox creation until stopped and cleaned up.
- Hibernated warm results do not count after the runtime is stopped.

Scheduling rules:

- Never reserve the last available worker sandbox slot for speculative work.
- User-initiated preview starts can ignore speculative pool saturation and should outrank speculative jobs.
- If pool or worker capacity is full, speculative work is skipped or deferred with a short cooldown; it should not spin in retry loops.
- Per-repo cooldown prevents a busy automation or repeated failed session starts from monopolizing the speculative pool.

Implementation should reserve speculative capacity durably before enqueueing a warm build, then release it in all terminal paths. A lightweight `session_preview_prewarm_runs` row can act as both the idempotency record and active-slot accounting source. Counting active jobs only from the jobs table is brittle because retries, worker restarts, and cancelled superseded revisions need product-aware cleanup.

## Data Model

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
- Optional `PreviewSpeculativeDecision`: `none`, `cache`, `warm_candidate`.

Track decisions and outcomes in a durable audit/debug table so classifier quality can be measured without scraping logs:

```sql
CREATE TABLE session_preview_prewarm_runs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    repository_id UUID NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    session_id UUID NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    workspace_revision BIGINT NOT NULL DEFAULT 0,
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
    completed_at TIMESTAMPTZ
);

CREATE UNIQUE INDEX idx_session_preview_prewarm_runs_scope
    ON session_preview_prewarm_runs (org_id, session_id, workspace_revision, config_digest, decision);
```

This table should not store full prompts or issue bodies. Store reason codes, short explanations, IDs, revision/config keys, and capacity snapshots only.

Run statuses should include at least `decided`, `queued`, `running`, `skipped_capacity`, `skipped_superseded`, `skipped_user_started`, `succeeded`, and `failed`.

### API Surface

The settings API can extend the existing preview-policy routes:

- `GET /api/v1/previews/policies` returns `session_prewarm_mode` with each repository policy row.
- `PUT /api/v1/repositories/{id}/preview-policy` accepts `session_prewarm_mode` alongside `auto_mode`, preserving partial update semantics if the current endpoint supports them.
- Org settings PATCH accepts `preview_session_prewarm_max_active`.

The session Preview status response should include optional prewarm state only for user-actionable states:

```json
{
  "prewarm": {
    "state": "warming|warm|stale|failed",
    "workspace_revision": 12,
    "resume_estimate_seconds": 30
  }
}
```

Cache-only prewarm should not appear in this response.

## Product UX

Settings page:

- Add a `Session prewarm` column or row under Preview settings for each repository.
- Modes: `Off`, `Cache`, `Smart`.
- Pool setting: `Speculative session prewarm slots`.
- Show compact helper copy: `Cache mode warms dependencies. Smart mode may prepare a preview when a session looks likely to need one. Speculative work yields to active sessions and user-started previews.`

Session detail:

- No new primary action.
- If cache prewarm is running, show no prominent state; cache prewarm is an implementation detail.
- If a warm build is running, show low-emphasis status in the Preview panel: `Warming preview`.
- If a warm build is current and resumable, the Preview action can read `Open warm preview` or keep `Preview` with supporting text `Ready to resume`.
- If a warm build becomes stale, hide it and let the next Preview click start the current workspace normally.

Operators:

- Preview health dashboard should eventually show speculative pool usage and skip reasons.
- Logs should include `org_id`, `repository_id`, `session_id`, `decision`, `reason`, and `prewarm_run_id` or preview id.

## Security And Secrets

- Classifier inputs must be summarized and bounded. Do not send full session transcripts, full issue bodies, logs, secrets, diffs, or uploaded files to the platform LLM for this decision.
- Cache-only prewarm should avoid mounting preview secret bundles by default. If a repo's install command requires secrets, require an explicit repo config opt-in before speculative cache prewarm can receive those bundles.
- Warm builds follow normal preview secret delivery because they execute the same runtime path as an intentional preview. This is why they require stronger classifier confidence and pool limits.
- Fork PR restrictions from auto-preview do not directly apply to session prewarm, but sessions created from untrusted external branch content should be treated as secrets-ineligible unless the repository policy explicitly allows it.
- Audit events are needed for policy changes, not for every classifier decision. Per-run decision records are operational telemetry rather than audit log entries.

## Guardrails

- Never start speculative work when the worker has no free sandbox capacity; skip instead of retrying aggressively.
- Use lower job priority than `start_preview`, `run_agent`, and `continue_session`.
- Add per-org caps for speculative active jobs and per-repo cooldowns to avoid one busy repo dominating prewarm capacity.
- Cancel or mark stale speculative preview work when a newer session snapshot supersedes it.
- Do not mount preview secrets for low-confidence cache-only prewarm unless the install command requires them and the repo has explicitly allowed that behavior.
- Prefer session live-sandbox reuse when already owned by the worker; otherwise hydrate from durable snapshots.
- Surface speculative work as `warming preview` or `ready to resume` only when the preview state is actually user-actionable.
- Never let speculative warm state overwrite a newer user-created preview, newer workspace revision, or current branch/PR group state.
- Treat classifier unavailable as `none` for `smart` mode during rollout, not as `cache`; fail closed on spending compute.

## Metrics

- `session_preview_prewarm_decisions_total{mode, decision, source, reason}`
- `session_preview_prewarm_cost_seconds{decision}`
- `session_preview_prewarm_skipped_total{reason}`
- `session_preview_open_after_prewarm_total{decision}`
- `session_preview_click_to_ready_seconds{path}`
- `session_preview_speculative_waste_total{reason}`
- `session_preview_live_minutes_total{initiator=speculative}`

The launch gate should be p50 click-to-ready improvement without a meaningful rise in worker saturation, OOM kills, or foreground queue delay.

## Rollout

1. Ship schema, settings UI, decision table, and `off`/`cache` mode. Default all repos to `off`.
2. Enable `cache` mode for selected dogfood repos. Verify cache hit rate, skipped-capacity rate, and click-to-ready deltas.
3. Add classifier in shadow mode for `smart`: record decisions but execute only cache-mode behavior.
4. Turn on `smart` for dogfood repos with conservative thresholds. Permit only `none` and `cache` decisions initially.
5. Enable `warm_candidate` after first-turn snapshots once stale-result handling and UI copy are in place.
6. Add grouped current-preview integration for session warm results, including query precedence for `current_status = 'warm'`.
7. Consider live speculative previews only after warm builds show high preview-open conversion and low waste. This should be a separate design or an explicit advanced setting, not part of the default rollout.

## Open Questions

- Should session warm builds create a `preview_target` immediately, or should session-only `preview_groups` support warm state without a target until branch publication? Creating a target improves history consistency but may require a synthetic commit/revision identity for unpushed work.
- Should `session_prewarm_policy` be added to `PreviewStoppedReason`, or is reusing `warm_policy` acceptable with `initiator = session_prewarm` as the differentiator?
- Should smart mode require a minimum historical preview-open rate before permitting `warm_candidate`, or can high-confidence prompt/file signals be enough for new repositories?

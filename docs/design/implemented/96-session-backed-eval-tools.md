# Design: Session-Backed Eval Tools

> **Status:** Implemented with product hardening | **Last reviewed:** 2026-06-19

## Implementation Status

Implemented on 2026-06-09:

- Added `sessions.origin` support for `eval_bootstrap` and `eval_run`.
- Added the normalized `eval_bootstrap_candidates` table and bootstrap
  session/thread linkage columns without adding a separate launch-context table.
- Added typed candidate status models and DB methods for creating/listing
  bootstrap candidates and resolving a bootstrap run by session/thread.
- Added the internal sandbox API endpoint `POST /api/v1/internal/eval/candidates`.
  It validates the existing sandbox token, requires a session-scoped and
  thread-scoped token, requires the session origin to be `eval_bootstrap`, and
  writes candidates only for the attached bootstrap run.
- Added `143-tools eval add`, registered only when the orchestrator injects
  `EVAL_BOOTSTRAP_TOOLS_ENABLED=true` for an `eval_bootstrap` session.
- Changed new eval bootstrap launches to create a real session and enqueue
  `run_agent`.
- Changed eval task and batch execution launches to create `eval_run` sessions
  and enqueue `run_agent`, linking `eval_runs.session_id` and `thread_id`.
- Removed the legacy direct worker implementations for `run_eval` and
  `run_eval_bootstrap`; eval state is now bootstrapped from session terminal
  state.
- Added session-terminal finalization that marks linked bootstrap runs and eval
  runs failed when the session fails, stores completed session artifacts on eval
  runs, moves successful sessions into `grading`, enqueues `run_eval_grader`,
  updates batches, and publishes eval SSE wake events.
- Added a dedicated post-session eval grader job boundary that reads the linked
  eval run/task rows and writes criterion results, final score, and pass/fail
  back to `eval_runs`.
- Kept existing bootstrap candidate reads and accepts backward-compatible by
  exposing normalized candidate payloads through the existing `candidates` field
  when present.
- Added a richer Settings -> Evals bootstrap candidate review surface that uses
  stable candidate IDs and candidate status, shows task prompt, evidence,
  commits, scoring criteria, warnings, and accepts selected candidates by ID.
- Added lightweight candidate validation warning codes for missing deterministic
  checks, missing test commands, docs-only changes, weak prompts, large diffs,
  and flaky command patterns.
- Added explicit eval bootstrap token provenance, including `eval:add`,
  `session_origin`, and `eval_bootstrap_run_id` claims when the bootstrap run can
  be resolved from the linked session/thread.
- Added durable eval run input manifests and repository setup behavior that
  checks out the pinned base commit and overlays bounded config files from
  `config_ref` before the agent starts.
- Added eval run and batch UI drilldowns that link scored/running cells back to
  the underlying session attempt and expose config/base/diff/grader signals.
- Added MVP schema for eval datasets, dataset task membership, and insert-only
  eval release gates.
- Added real post-session grader execution for `code_check` criteria by
  hydrating the completed session snapshot and running the configured command,
  plus LLM judge execution through the existing worker LLM client.
- Added structured candidate validation warnings in candidate payloads while
  preserving legacy warning-code chips for compatibility.
- Added eval dataset and release-gate stores, API routes, frontend API clients,
  Settings -> Evals inventory panels, and batch-level release-gate summaries.
- Added a legacy backfill migration from `eval_bootstrap_runs.candidates` into
  normalized `eval_bootstrap_candidates` rows.
- Added explicit eval-run preview actions that link to the session-backed
  preview surface, and richer batch metrics for pass rate plus deterministic
  vs. LLM judge failures.
- Added repository-aware candidate acceptance validation that verifies commits
  through the repository GitHub installation, proves the proposed solution diff
  matches the base-to-solution range in a sandbox, and dry-runs deterministic
  `code_check` commands before task creation.
- Added persisted batch release-gate decision artifacts so completed batches
  record pass/fail/no-data decisions per active release gate and return them
  from batch detail APIs.
- Added a first-class compare API that builds a normal eval batch from one
  baseline config and one or more candidate configs, keeping comparison runs on
  the same session-backed batch infrastructure.
- Removed the legacy `eval_bootstrap_runs.candidates` column after the
  normalized candidate backfill path.
- Added a candidate detail side sheet in Settings -> Evals for full prompt,
  evidence, grader config, validation warning, and solution-diff review.

Still intentionally left for a later product iteration:

- A baseline registry with named production releases and historical promotion
  metadata. The current compare API is first-class but still uses batch configs
  directly.
- Slice-level release-gate regression decisions across repository area, issue
  type, complexity, required tools, and risk tags. The current server-side gate
  decision artifact evaluates aggregate completed-run metrics and optional
  dataset scoping.
- Reviewer override workflow for accepting intentionally judgment-heavy
  candidates that fail deterministic-check requirements. The current hardening
  requires a deterministic `code_check` for acceptance.

## Problem

Eval bootstrap and eval execution currently run as parallel worker flows instead
of normal sessions. `run_eval_bootstrap` creates a lightweight session row for
logs, then creates a sandbox directly and shells out to Claude Code. `run_eval`
does the same for task execution. These paths bypass the session orchestrator,
durable session executors, coding credential selection, session threads,
internal tool injection, preview ownership, snapshots, and normal transcript
surfaces.

That makes evals harder to debug and easier to break:

- Eval bootstrap depends on raw Claude stdout being a JSON array.
- Agent auth, model selection, rate-limit fallback, and tool instructions can
  drift from normal sessions.
- The linked "session" is not a real session runtime.
- Preview and eval output are disconnected from the workspace lifecycle users
  already understand.

We should make eval bootstrap and eval task execution ordinary session-backed
agent work, with one narrow `143-tools` command that lets only eval-launched
sessions write eval candidates/tasks.

## Goals

1. Add an agent-facing `143-tools eval add` command for creating eval candidates
   from inside a sandbox.
2. Expose that command only to sessions that were explicitly launched from the
   Settings -> Evals surface.
3. Reuse existing session infrastructure for eval bootstrap, eval execution,
   logging, snapshots, cancellation, credential selection, and preview handoff.
4. Remove the direct eval sandbox/Claude execution paths after the session-backed
   path is live.
5. Keep eval writes tenant-scoped and repository-scoped, with clear provenance
   back to the session, thread, and eval bootstrap/run row.
6. Make eval creation a quality workflow: candidates must be inspectable,
   validated, and deliberately promoted before they can affect release gates.

## Non-Goals

- Do not make eval authoring available to ordinary coding sessions.
- Do not let agents create arbitrary eval datasets across repositories.
- Do not replace the existing eval task/run/batch UI in this design.
- Do not make eval grading depend on parsing agent stdout.
- Do not update `overall.md` until this is implemented.

## Product Principles

The best eval experience should help teams create a small number of durable,
high-signal evals rather than a large pile of noisy examples.

1. **Creation is not acceptance.** Agent-discovered evals are candidates until a
   human accepts them or an explicit repository policy auto-accepts low-risk
   candidates. The agent can propose; the product decides what enters a dataset.
2. **Every eval needs evidence.** A candidate should explain why the task matters,
   what failure it catches, which files/tests prove the behavior, and why it is
   not a brittle snapshot of incidental implementation detail.
3. **Deterministic checks first.** The UI should steer authors toward tests,
   build commands, lint checks, or custom scripts before LLM judges. LLM judges
   are useful for judgment, but they should not be the only pass/fail mechanism
   for critical regressions.
4. **Prevent leakage.** The known solution diff is available to the bootstrap
   authoring agent and graders, but never to the agent being evaluated.
5. **Protect against overfitting.** Evals should be grouped into datasets with
   roles such as `golden`, `shadow`, and `adversarial`, and release gates should
   report slice-level regressions rather than only a single average.
6. **Debuggability beats cleverness.** Failed evals should link to the underlying
   session, transcript, diff, preview, grader logs, and exact pinned inputs.

## Architectural Revisions

The session-backed direction is right, but there are four important changes from
the simplest version of the design.

### Candidates, Not Direct Tasks

`143-tools eval add` should create `eval_bootstrap_candidates`, not final
`eval_tasks`. Direct task creation from an agent would optimize for speed at the
expense of benchmark quality. Candidate review gives the product room to show
quality signals, warn about weak tests, and let humans decide whether the eval
belongs in the golden set, shadow set, or backlog.

### Reuse Session Origin

Use the existing `sessions.origin` provenance field instead of adding a
dedicated launch-context table. Add two origin values:

- `eval_bootstrap`
- `eval_run`

Tool availability can be inferred from `sessions.origin` plus the linked
`eval_bootstrap_runs` / `eval_runs` row. This keeps the schema small and avoids
creating a generic abstraction before we have multiple consumers.

### Authoring Sessions And Execution Sessions Are Different

Eval bootstrap sessions are authoring sessions. They inspect history and create
candidates.

Eval run sessions are execution sessions. They are pinned to a historical base
commit, run an agent blind to the known solution, and produce an output diff.

The product should use the same session runtime for both, but the prompts,
allowed tools, workspace setup, and success criteria are different enough that
they should have separate session origins and UI copy.

### Grading Is A Post-Session Pipeline

The coding-agent session should produce a workspace and diff. Grading should be
separate jobs that consume the pinned session snapshot. This keeps agent runtime
failure, deterministic check failure, LLM judge failure, and scoring failure
separate in the UI and in release gates.

### V1 Simplifications

Keep the first implementation intentionally small:

- No `session_launch_contexts` table; use `sessions.origin`.
- No `eval_run_attempts` table; model repeated attempts as multiple `eval_runs`
  in the same batch.
- No `143-tools eval complete`; bootstrap completion follows the linked session
  terminal state.
- No direct task creation from the tool; the tool only creates candidates.
- Only one new table is required for v1: `eval_bootstrap_candidates`. If even
  that proves too much for the first migration, the fallback is appending to
  `eval_bootstrap_runs.candidates`, but that loses clean review state,
  per-candidate IDs, and concurrent append behavior.

## User Experience

On Settings -> Evals, users can start either:

- **Bootstrap from PR history**: launches a normal session whose goal is to
  inspect the selected repository history and propose eval tasks.
- **Run eval task/batch**: launches one session per eval execution using the
  selected task base commit, model, and config overlay.

The eval page shows the linked session just like any other session: transcript,
logs, files, diff, snapshot state, and preview. The bootstrap detail sheet can
keep its compact progress view, but its source of truth is the session and the
eval bootstrap row.

Inside eval-launched sessions, the agent instructions include:

```bash
143-tools eval add --bootstrap-run-id <uuid> --input /tmp/candidate.json
```

The command validates the candidate and persists it through the internal API.
The agent can call it multiple times; the UI updates as candidates arrive. The
agent no longer needs to emit one perfect JSON array at the end.

Ordinary sessions do not see this tool in the generated tools/skills document,
and attempts to call the internal endpoint with a non-eval session token return
`403`.

### Settings -> Evals Layout

The page should feel like a quality-control workspace, not a generic settings
list. The implemented page now has dense inventory areas for tasks, candidates,
batches, datasets, and release gates. The target shape for the next iteration is
still:

1. **Tasks**: accepted eval tasks with repository, source PR, complexity, tags,
   last run status, and last passing baseline.
2. **Candidates**: proposed evals from bootstrap sessions, grouped by bootstrap
   run and repository.
3. **Batches**: recent comparison runs with pass rate, regression summary, and
   linked failure drilldown.
4. **Baselines**: the current production config and recent candidate configs
   that have been evaluated. This remains future work.

The primary action remains `Bootstrap from PR history`. Secondary actions are
`Create eval task` and `Run batch`. `Compare config` remains future work. Avoid
a landing-page style explanation; the first screen should immediately show
inventory and quality state.

### Bootstrap Progress

Starting bootstrap opens a side sheet tied to the real session:

- Header: repository, bootstrap run status, linked session button.
- Progress: latest session state, candidate count, elapsed time.
- Transcript/log preview: short, append-only log window with a link to the full
  session.
- Candidate stream: new candidates appear as rows as the agent calls
  `143-tools eval add`.

The sheet should not pretend bootstrap is one opaque background job. Users
should be able to see whether the agent is reading history, inspecting a PR,
adding candidates, or stuck.

### Candidate Review UI

Each candidate is currently reviewed in a dense card. A future refinement can
move the same information into a right-side sheet or detail page with:

- **Summary**: PR title/number, repo, complexity, fitness score, and source
  bootstrap session.
- **Task prompt**: the issue description exactly as the evaluated agent will see
  it.
- **Evidence**: changed files, test commands, why this catches a regression, and
  bootstrap reasoning.
- **Diff**: solution diff with file list and line-count summary. The UI should
  clearly label that this diff is hidden from evaluated agents.
- **Graders**: scoring criteria split into deterministic checks and LLM judges.
  Deterministic checks should be visually preferred and shown first.
- **Validation warnings**: weak or missing signals, such as no deterministic
  test, docs-only change, very large diff, missing solution commit, or flaky
  command pattern.
- **Actions**: `Accept`, `Needs revision`, `Reject`, and `Open session`.

`Accept` should create an eval task and mark the candidate accepted. `Needs
revision` should keep the candidate visible with the reviewer note. `Reject`
should hide it from the default candidate list but keep it available in a
filtered rejected view for auditability.

### Candidate List States

The candidate list should support fast triage:

- `Proposed`: new candidate needing review.
- `Needs revision`: candidate has potential but lacks evidence or a good rubric.
- `Accepted`: converted to an eval task.
- `Rejected`: not suitable for evals.

Rows should show compact warning chips, not long prose. Examples: `No test
command`, `Large diff`, `Docs-only`, `Weak prompt`, `Missing solution diff`.

### Eval Run Detail UI

Each eval run should link to its session attempt and show:

- pinned task inputs: base commit, config ref, model, prompt/template version;
- final agent diff;
- grader results grouped by deterministic checks and LLM judges;
- terminal reason: agent failed, session canceled, grader failed, or scored
  below threshold;
- preview action when a workspace snapshot exists.

The most important debugging link is the session itself. The eval page should
summarize, not duplicate, the full transcript/debug surface.

## Session Model

Add explicit eval provenance to sessions and threads instead of creating
"logging-only" session rows.

### Session Origin

Extend the existing `sessions.origin` check constraint with:

- `eval_bootstrap`
- `eval_run`

The important invariant is that the session has durable, queryable proof that it
was created by the eval settings workflow. Tool scope should be derived from
that origin and the linked eval row, not stored as a separate mutable list.

### Eval Linkage

`eval_bootstrap_runs.session_id` already exists and should point to the real
bootstrap session. The linked session must have `origin = 'eval_bootstrap'` so
tool availability and UI provenance do not depend on ad hoc title/status checks.

Add eval run linkage:

- `eval_runs.session_id uuid NULL REFERENCES sessions(id) ON DELETE SET NULL`
- `eval_runs.thread_id uuid NULL REFERENCES session_threads(id) ON DELETE SET NULL`

For bootstrap sessions, optionally add:

- `eval_bootstrap_runs.thread_id uuid NULL REFERENCES session_threads(id) ON DELETE SET NULL`

The linked session/thread owns the transcript and workspace. The eval row owns
eval-specific state: candidate list, run score, grader output, and failure
messages.

## Tool Contract

### Command

```bash
143-tools eval add --bootstrap-run-id <uuid> --input <path>
```

The initial implementation should support bootstrap candidates only. Eval run
results should come from the session completion and grading pipeline rather than
from an agent-authored tool call.

`--input` is a JSON file so large diffs and rubrics do not have to fit safely in
CLI flags.

### Candidate Input

```json
{
  "pr_number": 123,
  "pr_title": "Fix auth token refresh race condition",
  "base_commit_sha": "abc123",
  "solution_commit_sha": "def456",
  "solution_diff": "diff --git ...",
  "issue_description": "The auth token refresh has a race condition...",
  "scoring_criteria": [
    {
      "name": "tests_pass",
      "notes": "All tests should pass.",
      "grader_type": "code_check",
      "grader_config": {"command": "make test", "timeout_seconds": 300},
      "weight": 1.0,
      "required": true
    }
  ],
  "complexity": "moderate",
  "fitness_score": 0.85,
  "fitness_reasoning": "Clear bug fix with tests.",
  "evidence": {
    "changed_files": ["internal/auth/token.go", "internal/auth/token_test.go"],
    "test_commands": ["go test ./internal/auth"],
    "why_it_catches_regression": "Fails before the PR because concurrent refreshes race."
  }
}
```

### Output

```json
{
  "candidate_id": "uuid",
  "bootstrap_run_id": "uuid",
  "status": "proposed"
}
```

## Tool Availability Gate

The eval tool must be gated in three places.

1. **Skills document generation**

   `BuildIntegrationSkills` should include the `eval add` tool only when the
   current session has `origin = 'eval_bootstrap'`.

2. **Sandbox token claims**

   The internal sandbox token should include:

   ```json
   {
     "session_id": "...",
     "thread_id": "...",
     "repo_id": "...",
     "org_id": "...",
     "allowed_tool_scopes": ["eval:add"],
     "session_origin": "eval_bootstrap",
     "eval_bootstrap_run_id": "..."
   }
   ```

   Normal sessions must omit `eval:add`.

3. **Internal API authorization**

   The API must validate all of:

   - token is a sandbox/session token, not a browser cookie
   - token org matches `eval_bootstrap_runs.org_id`
   - token repo matches `eval_bootstrap_runs.repo_id`
   - token session matches `eval_bootstrap_runs.session_id`
   - session origin is `eval_bootstrap`
   - token scope contains `eval:add`

This prevents a regular coding session from fabricating eval tasks even if it
discovers the endpoint.

## Database Schema

### `sessions`

Extend the existing origin constraint:

```sql
ALTER TABLE sessions
  DROP CONSTRAINT IF EXISTS chk_sessions_origin;

ALTER TABLE sessions
  ADD CONSTRAINT chk_sessions_origin
    CHECK (origin IN (
      'issue_trigger', 'manual', 'project', 'automation', 'revision',
      'slack', 'external_api', 'eval_bootstrap', 'eval_run'
    ));
```

No new table is needed for launch context in v1.

### `eval_bootstrap_runs`

Add:

```sql
ALTER TABLE eval_bootstrap_runs
  ADD COLUMN thread_id uuid NULL REFERENCES session_threads(id) ON DELETE SET NULL;

CREATE INDEX idx_eval_bootstrap_runs_session
  ON eval_bootstrap_runs (org_id, session_id)
  WHERE session_id IS NOT NULL;
```

Keep `candidates jsonb` temporarily for API compatibility during migration.

### `eval_bootstrap_candidates`

Replace append-to-JSON behavior with normalized candidate rows:

```sql
CREATE TABLE eval_bootstrap_candidates (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  bootstrap_run_id uuid NOT NULL REFERENCES eval_bootstrap_runs(id) ON DELETE CASCADE,
  session_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  thread_id uuid NULL REFERENCES session_threads(id) ON DELETE SET NULL,
  repo_id uuid NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
  candidate_index integer NOT NULL,
  pr_number integer NOT NULL,
  pr_title text NOT NULL,
  base_commit_sha text NOT NULL,
  solution_commit_sha text NOT NULL,
  solution_diff text NOT NULL,
  issue_description text NOT NULL,
  scoring_criteria jsonb NOT NULL,
  complexity text NOT NULL CHECK (complexity IN ('trivial', 'simple', 'moderate', 'complex')),
  fitness_score double precision NOT NULL DEFAULT 0,
  fitness_reasoning text NOT NULL DEFAULT '',
  evidence jsonb NOT NULL DEFAULT '{}',
  status text NOT NULL DEFAULT 'proposed'
    CHECK (status IN ('proposed', 'accepted', 'rejected', 'needs_revision')),
  rejection_reason text NULL,
  accepted_task_id uuid NULL REFERENCES eval_tasks(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  reviewed_by uuid NULL REFERENCES users(id) ON DELETE SET NULL,
  reviewed_at timestamptz NULL,
  UNIQUE (org_id, bootstrap_run_id, candidate_index)
);

CREATE INDEX idx_eval_bootstrap_candidates_run
  ON eval_bootstrap_candidates (org_id, bootstrap_run_id, created_at);
```

This table is org-scoped and should be covered by tenancy lints.

### `eval_runs`

Add:

```sql
ALTER TABLE eval_runs
  ADD COLUMN session_id uuid NULL REFERENCES sessions(id) ON DELETE SET NULL,
  ADD COLUMN thread_id uuid NULL REFERENCES session_threads(id) ON DELETE SET NULL;

CREATE INDEX idx_eval_runs_session
  ON eval_runs (org_id, session_id)
  WHERE session_id IS NOT NULL;
```

No `eval_run_attempts` table in v1. If pass@k is needed, create multiple
`eval_runs` rows with the same task/config and a shared `batch_id`; aggregate
those rows in the API. Add a dedicated attempts table only after the product
needs attempt-level state that cannot be represented by existing runs.

## API Contract

### Start Bootstrap

Existing:

```http
POST /api/v1/evals/bootstrap
```

Behavior changes:

1. Create `eval_bootstrap_runs` as `pending`.
2. Create a real session with `origin = 'eval_bootstrap'`.
3. Create a primary thread.
4. Persist `session_id` and `thread_id` on the bootstrap row.
5. Enqueue normal `run_agent`, not `run_eval_bootstrap`.

Response remains:

```json
{
  "data": {
    "id": "...",
    "status": "pending",
    "session_id": "...",
    "thread_id": "..."
  }
}
```

### Add Candidate

New internal route:

```http
POST /api/v1/internal/evals/bootstrap/{bootstrap_run_id}/candidates
```

Auth:

- sandbox/session token only
- requires `eval:add`
- must satisfy the availability gate above

Request:

```json
{
  "...": "EvalBootstrapCandidate shape"
}
```

Response:

```json
{
  "data": {
    "candidate_id": "uuid",
    "candidate_index": 3,
    "bootstrap_run_id": "uuid",
    "status": "proposed"
  }
}
```

Errors:

- `400 INVALID_BODY`
- `400 INVALID_CANDIDATE`
- `401 UNAUTHORIZED`
- `403 FORBIDDEN`
- `404 BOOTSTRAP_NOT_FOUND`
- `409 BOOTSTRAP_NOT_RUNNING`

On success, publish an eval bootstrap SSE update so the settings page refetches.

### Get Candidates

Existing:

```http
GET /api/v1/evals/bootstrap/candidates?bootstrap_run_id=<uuid>
```

Behavior changes:

- Read from `eval_bootstrap_candidates`.
- Keep returning `EvalBootstrapRun` with `candidates` populated for frontend
  compatibility until the UI migrates to a candidate list response.

### Accept Candidates

Existing:

```http
POST /api/v1/evals/bootstrap/accept
```

Behavior changes:

- Accept candidate IDs rather than array indexes in the new API shape.
- Keep index acceptance temporarily for compatibility.
- Set `eval_bootstrap_candidates.accepted_task_id` when a task is created.

### Review Candidate

New browser route:

```http
PATCH /api/v1/evals/bootstrap/candidates/{candidate_id}
```

Auth:

- browser cookie
- `admin` or `member`; `viewer` and `builder` cannot promote evals

Request:

```json
{
  "status": "needs_revision",
  "rejection_reason": "Needs a deterministic test command before acceptance."
}
```

Response:

```json
{
  "data": {
    "candidate_id": "...",
    "status": "needs_revision",
    "eval_task_id": null
  }
}
```

Errors:

- `400 INVALID_STATUS`
- `403 FORBIDDEN`
- `404 NOT_FOUND`
- `409 ALREADY_ACCEPTED`

## Worker Flow

### Bootstrap

Remove `run_eval_bootstrap` as a sandbox-executing job. Replace it with normal
session dispatch:

1. API creates eval bootstrap row.
2. API creates session/thread with eval provenance.
3. API enqueues `run_agent` using existing session dedupe.
4. Orchestrator injects normal credentials, tools, logs, and runtime control.
5. Agent calls `143-tools eval add` for each candidate.
6. The session completion hook closes the bootstrap row.

The bootstrap prompt should instruct the agent to use the tool incrementally and
to keep candidate files under a temp path, not to return a raw JSON array.

### Eval Run

Eval runs should also be session-backed:

1. Create `eval_runs` row.
2. Create a session with `origin = 'eval_run'`.
3. Hydrate repository at `base_commit_sha`.
4. Apply config overlay before the agent starts.
5. Run normal agent turn.
6. On session completion, collect diff from the session snapshot.
7. Enqueue deterministic/LLM graders as normal jobs.
8. Aggregate repeated `eval_runs` into pass@1/pass@k, pass rate, score, and
   failure codes when a batch requests repeated attempts.

The eval runner may need a small new session start mode to clone and checkout a
specific commit before the agent turn. That should live in the orchestrator or a
shared session setup helper, not in eval worker code.

### Completion Semantics

Bootstrap and eval rows should follow the linked session terminal state:

- Session `completed`: bootstrap run becomes `completed`; eval run moves to
  grading if it has a snapshot/diff.
- Session `failed`: bootstrap/eval run becomes `failed` with the session failure
  message and a link to the failed session.
- Session `canceled`: bootstrap/eval run becomes `failed` with a
  `canceled_by_user` failure code.
- Session completed with zero candidates: bootstrap still completes, but the UI
  should show an empty state that explains no suitable PRs were found.
- Session completed with candidates but validation warnings: bootstrap completes;
  warnings live on individual candidates and do not block review.

This avoids a separate agent-authored completion tool while still keeping eval
state deterministic.

### Candidate Validation

Current implementation:

- blocks missing required fields, malformed SHA shapes, invalid complexity, and
  malformed scoring criteria before creating or accepting a candidate;
- verifies the repository exists for the org and uses its GitHub installation to
  prove that both candidate commits exist;
- creates a sandbox checkout to prove `solution_diff` exactly matches the
  normalized `base_commit_sha..solution_commit_sha` diff;
- checks out the proposed solution commit, applies `.143/config.json`
  dependency/bootstrap setup in the validation sandbox, and dry-runs every
  deterministic `code_check` command before acceptance;
- requires at least one deterministic `code_check` criterion before acceptance;
- emits structured `validation_warnings` in the candidate payload while keeping
  legacy warning-code chips;
- warns for no deterministic check, missing test command, docs-only candidates,
  weak prompts, large diffs, missing solution diffs, and flaky command patterns;
- shows those warnings in the review UI.

Future hardening can add an explicit reviewer override path for candidates that
intentionally rely on human/LLM judgment instead of deterministic checks, plus
additional weak-candidate detectors for dependency-only changes and missing
test coverage.

Validation output is visible in the review UI, but not every warning blocks
acceptance.

Validation should produce structured warnings:

```json
{
  "code": "missing_deterministic_check",
  "severity": "warning",
  "message": "No code_check criterion was proposed.",
  "suggestion": "Add a focused test/build command before accepting.",
  "blocking": false
}
```

Only a small set should be blocking:

- invalid or unreachable commits;
- malformed scoring criteria;
- solution diff missing or not matching the commit range;
- repository/org mismatch.

Everything else should be warning-level so reviewers can accept intentionally
hard or judgment-heavy evals with eyes open.

## Preview Integration

Because eval runs become real sessions, Preview can use the existing session
preview lifecycle:

- Bootstrap sessions usually do not need preview, but the surface can still show
  files/logs/transcript.
- Eval run sessions can start Preview from the completed workspace using the
  same `start_preview` jobs and session snapshot hydration as regular sessions.
- Preview freshness should key off the session workspace revision, not eval row
  timestamps.

No separate eval preview runtime should exist.

## Release-Gate Experience

The implemented product now persists eval datasets and insert-only release
gates, shows them on Settings -> Evals, and summarizes batch pass-rate gate
status. The full future product should answer: "Will this prompt/model/tooling
change degrade our coding agents?"

Settings -> Evals should therefore organize around:

- **Datasets**: golden, shadow, adversarial, and custom tags.
- **Baselines**: last production config, current candidate config, and optional
  model/config overlays.
- **Slices**: repository area, issue type, complexity, required tools, and risk.
- **Metrics**: pass@1, pass@k, required-criterion failure rate, deterministic
  failure rate, LLM-judge disagreement, average runtime, and cost.
- **Failure drilldown**: each failed cell links to the session attempt, diff,
  grader logs, and preview when available.

Future server-side release-gate evaluation should fail on meaningful
regressions, not just a low aggregate score. A candidate config should be
blocked when any critical slice regresses past threshold even if the global
average improves.

### Comparison UI

The implemented batch page has a comparison matrix, session links, pass rate,
gate status, and deterministic-vs-LLM failure counts. A fuller comparison UI
should add:

- rows: eval tasks;
- columns: baseline config and candidate configs;
- cells: score/pass/fail with compact failure code;
- row expansion: attempts, session links, diff links, grader details;
- summary header: pass rate, required-check failures, average runtime, and cost.

The top of the page should call out regressions in plain terms:

- `Blocked: auth tasks regressed from 8/8 to 6/8`
- `Warning: average runtime increased 42%`
- `Improved: frontend tasks pass rate +9%`

Users should not have to inspect every cell to understand whether a prompt/model
change is safe.

### Failure Drilldown

Current failed cells link back to the session attempt and expose grader result
details through the task/run surfaces. Future drilldown should make every
failed cell answer four questions inline:

1. What did the agent do?
2. Which criterion failed?
3. Is the failure from the agent, the test environment, or the grader?
4. What exact input/config produced it?

The drilldown should link to the session transcript, final diff, preview,
grader logs, and pinned manifest. This makes the eval suite actionable rather
than just a scoreboard.

## Cleanup Plan

After the new flow is shipped:

1. Stop registering `run_eval_bootstrap`. Done.
2. Delete `executeBootstrapScan`, `bootstrapAgentCommand`, and
   `bootstrapLogWriter`. Done.
3. Replace direct `runCodingAgent` with session-backed eval run dispatch. Done.
4. Move grading into post-session jobs that read session diff/snapshot output.
   Done.
5. Backfill `eval_bootstrap_candidates` from existing
   `eval_bootstrap_runs.candidates`. Done.
6. Keep `eval_bootstrap_runs.candidates` read-only for one deploy, then remove
   it after the frontend has migrated. Pending.
7. Update docs:
   - move this design to `implemented/` when complete. Done.
   - update `docs/design/overall.md`. Done.
   - update `docs/design/implemented/01-database-schema.md`. Done.
   - update public agent-tools docs for `143-tools eval`. Done.

## Rollout

1. Add schema and internal API while old bootstrap still works.
2. Add `143-tools eval add` and endpoint tests.
3. Add eval-session provenance and sandbox token scope.
4. Switch Settings -> Evals bootstrap to create a session-backed run.
5. Migrate the UI to normalized candidates.
6. Switch eval task execution to session-backed runs.
7. Remove old direct sandbox code.

## Open Questions

- Should `143-tools eval add` create final `eval_tasks` directly, or only
  candidates that the user must accept? Default: candidates only.
- Should eval run sessions be visible in the global Sessions list by default?
  Default: show them with an `Eval` origin marker, but allow filtering.
- Should failed eval sessions automatically create failure-derived eval cases?
  That belongs in a later flywheel design.

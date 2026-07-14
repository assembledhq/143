# Design: Automation Goal Improvement

> **Status:** Implemented | **Last reviewed:** 2026-07-14

## Problem

Automation quality depends heavily on the saved goal prompt. The current goal-first UX makes that prompt visible and editable, but users still have to know how to write durable agent instructions: scope, evidence requirements, output format, no-op behavior, verification, PR expectations, and trigger-specific context.

The product should help users improve automation prompts directly from creation and edit surfaces. The improvement mechanism should be fast for routine cleanup, but should also support a deeper coding-agent-backed pass when repository context, prior run failures, CI, or PR history matter.

## Goals

- Add an `Improve goal` control to automation creation and edit/detail surfaces.
- Make the default action a fast LLM prompt adjustment that returns in the normal UI flow.
- Offer a deep improvement option from the same dropdown that uses a coding agent to inspect repository and run context before proposing a better goal.
- Preserve user control: generated improvements are proposals, not invisible writes.
- Preserve run reproducibility: future automation runs use the saved goal, while historical `automation_runs.goal_snapshot` rows remain unchanged.
- Make proposals auditable and explainable enough that a team can understand why the automation prompt changed.

## Non-Goals

- Do not automatically mutate automation goals after every run.
- Do not replace the existing automation template catalog.
- Do not change coding-agent adapters so automation runs receive wrapped prompts. Automation execution should continue to pass the raw `goal_snapshot` as the user task.
- Do not let deep prompt improvement publish branches, open PRs, or comment externally.

## Product Direction

Use a split button in both the automation composer and saved automation edit surface:

```text
[ Improve goal ] [v]
```

Primary click runs the default fast improvement. The dropdown exposes:

- `Fast improve`
- `Deep improve with agent`

The button should live near the goal editor, not in advanced settings, because prompt quality is part of the primary authoring workflow.

### Fast Improve

Fast improve is a synchronous backend LLM call. It rewrites the current goal into a more reliable automation prompt while preserving intent.

For an unsaved draft, the input is:

- current draft name,
- current draft goal,
- selected repository, if any,
- scope,
- trigger configuration,
- model/agent settings,
- selected template ID, if known.

For a saved automation, the backend additionally loads a bounded evidence snapshot:

- recent terminal runs, biased toward failed and `completed_noop`,
- run status, summary, failure category, retry advice, and linked session title/status,
- trigger context and config snapshot,
- recent PR outcome if already joined into the run list response.

Fast improve should return:

- proposed goal,
- rationale,
- concise change list,
- confidence,
- warnings, such as "little run history available" or "repository not selected."

The UI opens a review panel with a diff and explicit `Apply` / `Discard` actions. On creation, `Apply` updates the local draft textarea. On a saved automation, `Apply` updates the automation through the normal goal update path.

### Deep Improve With Agent

Deep improve is asynchronous. It creates a durable improvement proposal and launches a normal 143 coding session, similar to session-backed eval work. The session is the visible execution record for the analysis: users should be able to open it, watch the agent run, inspect its transcript, and see whether it is pending, running, completed, or failed. The agent can inspect the repository, prior automation runs, session transcripts/summaries, PR outcomes, CI history, and relevant repo docs, then produce a structured proposal.

Deep improve is intended for cases where prompt quality depends on codebase facts:

- flaky-test automation that needs the right test command and evidence threshold,
- PR-feedback automation that needs repository review conventions,
- database-index automation that must understand migration/index patterns,
- release or regression automations that need repo-specific verification gates.

Deep improve should be available on saved automations. It can also be available during creation once a repository is selected, but draft deep improvement should be treated as an ephemeral proposal tied to the draft inputs, not to an automation row that does not exist yet.

The deep agent should not apply the goal itself. It writes only a proposal result back to 143 through a restricted `143-tools` CLI command injected into the analysis session. The proposal stores the session ID so the UI can keep the improvement result and the underlying agent run connected.

## Interaction Details

### Creation

In `/automations/new`, the composer shows `Improve goal` next to the goal editor or in the composer footer near templates.

Fast improve:

1. User writes or inserts a draft goal.
2. User clicks `Improve goal`.
3. Button enters loading state.
4. Backend returns a proposal.
5. UI shows a diff review panel.
6. `Apply` replaces the draft goal and leaves normal create flow unchanged.

Deep improve:

1. User chooses `Deep improve with agent`.
2. If no repository is selected, show a repository-required validation state.
3. Backend creates a draft improvement proposal row with `automation_id = NULL` and starts an analysis session.
4. UI shows progress, links to the running session, and polls the proposal.
5. When complete, the same diff review panel appears.
6. `Apply` updates the draft goal only. If the user later creates the automation, the normal create request carries the improved goal.

Draft deep proposals may expire because they are not canonical automation state.

### Saved Automation Edit

On `/automations/:id`, the goal edit surface uses the same split button.

Fast improve:

1. User clicks `Improve goal`.
2. Backend loads the automation and bounded run evidence.
3. Backend returns and stores a completed proposal.
4. UI shows diff, rationale, confidence, and warnings.
5. `Apply` updates `automations.goal` if the automation has not changed since proposal generation.

Deep improve:

1. User chooses `Deep improve with agent`.
2. Backend creates an `automation_goal_improvements` row and starts a visible agent analysis session.
3. UI shows progress, including the linked session's running state and transcript.
4. Completion shows a diff and explanation.
5. `Apply` performs a stale-write check and updates the saved automation goal.

If another user edits the goal while a proposal is running, the proposal should complete but apply should fail with a stale-goal response. The UI can offer `Regenerate from current goal`.

## Backend Design

### Proposal Table

Add a durable proposal table so fast and deep paths share the same lifecycle:

```sql
CREATE TABLE automation_goal_improvements (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id              UUID NOT NULL REFERENCES organizations(id),
    automation_id       UUID REFERENCES automations(id),
    repository_id       UUID REFERENCES repositories(id),

    mode                TEXT NOT NULL, -- fast, deep
    status              TEXT NOT NULL, -- pending, running, completed, failed, canceled

    input_name          TEXT,
    input_goal          TEXT NOT NULL,
    input_config        JSONB NOT NULL DEFAULT '{}'::jsonb,
    base_goal_hash      TEXT NOT NULL,
    evidence_snapshot   JSONB NOT NULL DEFAULT '{}'::jsonb,

    proposed_goal       TEXT,
    proposal            JSONB NOT NULL DEFAULT '{}'::jsonb,
    confidence          TEXT,
    warnings            JSONB NOT NULL DEFAULT '[]'::jsonb,
    error_message       TEXT,

    analysis_session_id UUID REFERENCES sessions(id),
    created_by          UUID REFERENCES users(id),
    applied_by          UUID REFERENCES users(id),
    applied_at          TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_automation_goal_improvements_org_automation
    ON automation_goal_improvements (org_id, automation_id, created_at DESC);
```

`automation_id` is nullable for creation-time draft proposals. Every query must still filter by `org_id`.

The `proposal` JSON should carry structured fields:

```json
{
  "rationale": "The current prompt does not define no-op behavior or verification.",
  "changes": [
    "Added explicit investigation window.",
    "Added output requirements.",
    "Added no-op behavior."
  ],
  "evidence": [
    "Last 3 runs completed_noop with no reason recorded."
  ],
  "risks": [
    "No repository was selected, so verification commands are generic."
  ]
}
```

### API

Use separate draft and saved routes while keeping the response model identical.

```http
POST /api/v1/automations/goal-improvements
POST /api/v1/automations/:id/goal-improvements
GET  /api/v1/automations/goal-improvements/:improvement_id
POST /api/v1/automations/:id/goal-improvements/:improvement_id/apply
```

Draft request:

```json
{
  "mode": "fast",
  "name": "Nightly flaky test cleanup",
  "goal": "...",
  "repository_id": "...",
  "scope": "...",
  "config": {
    "schedule_type": "interval",
    "triggers": ["github.checks.completed"],
    "base_branch": "main",
    "agent_type": "codex",
    "model": "..."
  }
}
```

Saved request:

```json
{
  "mode": "deep",
  "include_recent_runs": 10
}
```

Apply request:

```json
{
  "expected_base_goal_hash": "sha256:...",
  "proposed_goal": "..."
}
```

Apply should:

1. Load proposal by `org_id`, `automation_id`, and `improvement_id`.
2. Verify proposal status is `completed`.
3. Verify `expected_base_goal_hash` matches the current automation goal hash.
4. Validate goal length and non-empty constraints.
5. Update `automations.goal` using the existing update path.
6. Mark the proposal `applied_at` and `applied_by`.
7. Emit audit details that include the improvement ID and mode.

### Prompt Templates

Add LLM prompts under `internal/prompts/templates/`:

- `automation_goal_fast_improvement.template`
- `automation_goal_proposal_judge.template`

Expose render functions from `internal/prompts/prompts.go`. Do not inline prompt text in service code.

The fast prompt should require JSON output, intent preservation, and a hard maximum goal length below the database limit. It should optimize for:

- clear objective,
- trigger context,
- investigation strategy,
- evidence thresholds,
- no-op conditions,
- output requirements,
- verification,
- PR/publish expectations,
- safety and tenant/repository boundaries.

The judge prompt should compare original and proposed goals and flag:

- changed intent,
- broadened scope,
- missing required constraints,
- excessive length,
- unsupported assumptions,
- likely prompt-injection leakage from run evidence.

Fast improve can use the judge in the same request path when latency allows. Deep improve should always run the judge before marking a proposal completed.

## Deep Agent Execution

Deep improvement must use the normal session execution infrastructure because coding-agent work should remain observable and durable. It should create a `sessions` row and dispatch a normal `run_agent` job, not run as an opaque worker-only background task. This should mirror the session-backed eval pattern: the improvement proposal owns the product workflow, while the linked session owns live agent execution, transcript, runtime status, cancellation, and debugging.

The implementation can add a session origin such as `automation_goal_improvement` and must link the resulting session to `automation_goal_improvements.analysis_session_id`. That session should be visible from the improvement progress UI and directly openable like other agent sessions, with a clear label that it is analyzing an automation goal rather than producing a code change.

The deep session should be analysis-only:

- use a throwaway workspace,
- deny publishing and external comment capabilities,
- avoid PR creation paths,
- discard any filesystem diff,
- write the proposal through a restricted `143-tools automation-goal-improvement complete ...` command rather than a structured final-message parser.

The `143-tools` command should be the only supported write-back path from the deep agent. It should use the session-scoped internal token already injected into the sandbox, validate that the session matches `automation_goal_improvements.analysis_session_id`, and accept structured JSON containing the proposed goal, rationale, change list, evidence summary, risks, confidence, and warnings. This keeps the write-back path consistent with the rest of the platform's sandbox-agent tooling and avoids scraping agent prose from the transcript.

Recommended capability snapshot:

- `repo_context: read`,
- `session_history: read`,
- `pr_history: read`,
- `ci_history: read` when available,
- `review_feedback: read` when relevant,
- no `publishing`,
- no write or publish access by default.

The deep agent prompt should tell the agent to:

1. Restate the current automation intent.
2. Inspect repository conventions and likely verification commands.
3. Inspect recent automation failures/no-op runs if this is a saved automation.
4. Identify missing instructions or ambiguity.
5. Produce a replacement goal that is specific enough to run repeatedly.
6. Return structured JSON only.

The result should be judged before it is surfaced. If the judge rejects the proposal, store the failure reason and expose it in the UI rather than applying a fallback automatically.

## Evidence Handling

Run history and integration content can contain untrusted text from GitHub comments, PR bodies, CI logs, issue trackers, or previous agent output. Improvement prompts must fence this content as data.

Rules:

- Treat event bodies, comments, CI logs, and prior agent output as untrusted evidence.
- Do not let untrusted evidence instruct the improvement LLM or deep agent.
- Summarize or truncate large logs before including them in fast improve.
- Keep raw evidence out of audit logs.
- Store enough `evidence_snapshot` metadata to explain the proposal without storing secrets or oversized transcripts.

## Accuracy Strategy

Fast improve should optimize prompt structure and known automation best practices. It is accurate when the problem is underspecified wording.

History-aware fast improve should improve accuracy by using observed failures, no-ops, and summaries. It is accurate when the automation has already produced enough signal.

Deep improve should be used when the prompt depends on repository reality. Accuracy comes from letting a coding agent inspect:

- actual repo layout,
- scripts and test commands,
- migration and PR conventions,
- prior run sessions,
- CI failure patterns,
- review feedback patterns.

The product should label confidence conservatively:

- `high`: proposal is grounded in repository or repeated run evidence.
- `medium`: proposal uses current config and limited run history.
- `low`: proposal is mostly style/structure with little context.

## Permissions, Quotas, And Abuse Controls

- Users need the same permission required to create or update automations.
- Deep improve consumes agent capacity and should be rate-limited separately from fast improve.
- Allow at most one running deep improvement per automation unless explicitly canceled.
- Fast improve should be debounced client-side and rate-limited server-side.
- API-token access should be opt-in later. The initial implementation can restrict goal improvement to authenticated dashboard users.
- Proposal apply should require update permission even if proposal generation was allowed earlier.

## Audit And Versioning

The saved automation goal remains the source of truth. The proposal table is supporting history.

Audit events should capture:

- improvement requested,
- improvement completed or failed,
- improvement canceled,
- improvement applied.

The existing automation update audit diff should still record the actual `goal` before and after. Include `automation_goal_improvement_id`, `mode`, and `base_goal_hash` in audit details.

`automation_runs.goal_snapshot` remains unchanged for historical runs and continues to explain exactly which goal executed.

## Frontend Implementation Notes

- Use existing shadcn components: `Button`, `DropdownMenu`, `Dialog` or `Sheet`, `Textarea`, and existing form controls.
- Add the split button to `AutomationComposer` so creation and edit surfaces share the UI.
- Keep the current character counter and validation after applying a proposal.
- For fast improve, use a mutation that returns a completed proposal.
- For deep improve, use a mutation followed by polling with TanStack Query.
- Show proposal state with clear actions: `Apply`, `Discard`, `Regenerate`.
- On saved automations, show stale-goal apply failures inline and offer regenerate.
- Avoid adding explanatory product copy inside the goal editor itself. The review panel can show rationale because it is part of reviewing generated output.

## Rollout Plan

1. Add proposal model, migrations, handlers, prompt templates, and fast improve for draft and saved automations.
2. Add history-aware evidence snapshots for saved fast improve.
3. Add frontend split button, diff review panel, and apply flow.
4. Add async deep improve for saved automations using an analysis-only session.
5. Add creation-time deep improve for selected repositories.
6. Later: suggest running goal improvement after repeated failures or repeated no-op runs, but still require user approval before applying.

### Implementation Progress

- **Implemented:** durable `automation_goal_improvements` proposals, fast-improve prompt templates, draft/saved fast-improve API routes, proposal get/list/apply/cancel routes, stale-goal apply protection, saved-run evidence snapshots with PR/CI summary metadata, stale draft proposal expiry, and audit events for requested/completed/failed/canceled/applied proposals.
- **Implemented:** automation creation and saved-edit UI split button, proposal review dialog with a compact diff, draft-local apply, saved apply, stale-goal regenerate, session link, cancellation, and recent proposal history for saved automations.
- **Implemented:** saved and creation-time deep improvement sessions with `automation_goal_improvement` session origin, proposal-linked `run_agent` dispatch, read-only capability snapshot, scoped sandbox completion token, restricted `143-tools automation-goal-improvement complete` write-back, internal completion authorization, direct internal API blocking for PR/issue/project side effects, judge-before-complete validation, failed/canceled proposal recording, linked-session cancel requests, one-running-deep-proposal enforcement for saved automations, dedicated route rate limits, client-side debounce, and UI polling for running/completed/failed/canceled deep proposals.
- **Deferred:** passive repeated-failure/no-op recommendations. The rollout plan already treats this as a later suggestion mechanism that must still require human approval.

## Tests And Verification

Backend:

- Prompt render tests for fast and judge templates.
- Handler tests for draft fast improve, saved fast improve, deep enqueue, get proposal, apply, stale apply, permission failures, and org isolation.
- Store tests verifying every query filters by `org_id`.
- Migration tests for schema constraints and indexes.
- Deep job tests verifying it creates a proposal-linked analysis session and denies publishing capabilities.

Frontend:

- Composer test for split button default fast action.
- Dropdown test for deep action and repository-required validation.
- Draft apply test that updates the textarea without creating an automation.
- Saved apply test that calls the apply endpoint and refreshes automation detail.
- Deep polling test for pending, completed, and failed proposal states.
- Stale-goal failure test.

Verification follows normal repo requirements:

- Go changes: `go vet ./...`, `go build ./...`, `go test ./...`.
- Frontend changes: from `frontend/`, `npm run typecheck`, `npm run lint`, `npm run build`.

## Decisions

- Creation-time deep improve ships for drafts with a selected repository. Draft proposals use `automation_id = NULL` and apply only to local draft goal state.
- Evidence classes are inferred from the workflow. Saved fast improve stores bounded run/session evidence plus PR/CI summary metadata when present; deep improve receives a read-only capability snapshot for repository, session history, PR history, CI history, and review feedback.
- Saved proposal history is retained as durable audit-supporting product history. Draft-only active proposals expire after a short TTL so abandoned creation flows do not leave indefinitely running proposal state.
- Saved deep improve uses the automation's configured agent/model settings when present. Draft deep improve uses the default Codex agent path because no saved automation policy exists yet.
- Passive recommendations after repeated failures or repeated no-op runs remain a later follow-up; they must still create user-reviewed proposals and must not auto-apply goals.

## Engineering Implementation Spec

This should be implemented in small vertical phases. Each phase should keep generated text as a proposal until an explicit apply action updates `automations.goal`.

### Phase 1: Fast Improve Backend

- Add `automation_goal_improvements` migration, models, and a store under `internal/db/` with `orgID` on every exported method.
- Add prompt templates and render functions for fast improvement and proposal judging.
- Add a small service that builds draft/saved improvement inputs, calls the configured LLM client, validates JSON output, runs the judge when enabled, and stores a completed proposal.
- Add handlers for draft create, saved create, proposal get, and proposal apply. Saved apply must check `base_goal_hash` against the current automation goal before patching.
- Add audit details for proposal creation/completion/failure/cancellation/apply, while preserving the existing automation goal diff.

### Phase 2: Fast Improve UI

- Add a reusable split-button control to `AutomationComposer` with default `Fast improve` and dropdown `Deep improve with agent`.
- Add a proposal review sheet/dialog that shows diff, rationale, change list, warnings, confidence, and `Apply` / `Discard`.
- Wire creation flow so apply only updates local draft state.
- Wire saved automation flow so apply calls the proposal apply endpoint and refreshes automation detail.
- Handle stale-goal apply errors with a clear regenerate action.

### Phase 3: Deep Improve For Saved Automations

- Add a deep-improvement job path that creates a visible `sessions` row linked through `automation_goal_improvements.analysis_session_id`, then dispatches a normal `run_agent` job.
- Add a session origin such as `automation_goal_improvement` if needed, and label the session in the UI as automation-goal analysis.
- Inject an analysis-only capability snapshot: read repo/session/PR/CI/review context, no publishing, no external comments, no write/publish capabilities.
- Add a restricted `143-tools automation-goal-improvement complete ...` command. This is the only deep-agent write-back path; it must validate org, session, and proposal linkage before storing structured proposal JSON.
- Run the proposal judge before marking the deep proposal completed. If rejected, mark failed with a user-visible reason.

### Phase 4: Creation-Time Deep Improve

- Allow deep improve from `/automations/new` only after a repository is selected.
- Create a draft proposal with `automation_id = NULL`, link it to the visible analysis session, and expire old draft proposals.
- Applying a draft deep proposal should update only the local goal textarea. The eventual automation create request remains the source of truth.

### Phase 5: Polish And Follow-Ups

- Add rate limits: fast improve per user/org, deep improve per automation, and one running deep proposal per automation.
- Add passive recommendations after repeated failed or no-op runs, but keep apply user-approved.
- Add proposal history to the saved automation detail page only if users need rollback or comparison beyond audit logs.

Implementation should update or add tests in the same phase as each behavior: Go tests for stores/services/handlers/prompts/jobs/`143-tools`, and React tests for split button, proposal review, draft apply, saved apply, deep polling, and stale-goal handling.

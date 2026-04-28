# Design: Agent-Native Review Button

> **Status:** Implemented | **Last reviewed:** 2026-04-25
>
> **Related docs:** [../11-review-feedback-loop.md](../11-review-feedback-loop.md), [../36-code-review-display.md](../36-code-review-display.md), [61-pr-state-sync-and-repair-actions.md](61-pr-state-sync-and-repair-actions.md), [../future/53-session-composer-mentions.md](../future/53-session-composer-mentions.md)

## Summary

Add a **Review** button to the session UI that kicks off a follow-up turn in the *current* session, asking the configured coding agent to review its own changes using **that agent's native review surface** — Claude Code's `/review` and `/security-review` skills, Codex's review subcommand, Gemini's review prompt, Amp's review pipeline, etc.

This should look similar to the existing **Fix tests** and **Resolve conflicts** affordances in the UI, but it should be modeled as a **session-native review flow**, not as a PR repair action. The backend should reuse low-level continuation/session-enqueue mechanics where possible while keeping review semantics separate from PR-scoped repair semantics. No new table, no separate review-run concept, no parallel pipeline.

## Motivation

Today the only way to ask an agent to review its own diff is to type "please review this code" into the composer. That works, but:

- it ignores the **vendor-curated review prompts** that ship inside Claude Code (`/review`, `/security-review`), Codex, Amp, etc. — those are tuned by their authors and improve without 143 shipping anything
- it puts the burden of remembering the right framing on the user
- it is not a single-click action like the rest of our session affordances

The Fix tests / Resolve conflicts buttons already prove the right interaction shape for "one-click follow-up turn": short prompt + structured context + run on the existing session. Review should follow that UX pattern without inheriting PR repair types or PR repair validation rules.

## Current State

Already in place:

| Piece | Where |
|------|------|
| `AgentAdapter` interface (`PreparePrompt` + `Execute`) | `internal/services/agent/adapter.go:21` |
| Per-agent adapters | `internal/services/agent/adapters/{claude_code,codex,gemini_cli,amp,pi}.go` |
| `RevisionContext` for follow-up turns | `agent.RevisionContext` (`adapter.go:64`) |
| Session continuation/job enqueue path | existing session resume + message enqueue flow |
| PR repair flow that is a useful UX analogy, but not the model to reuse directly | `pr_health_service.go:332` (`StartPullRequestRepair`) |
| Review-comment storage + threading | `internal/api/handlers/session_review_comments.go`, `frontend/src/components/code-review/*` |
| Session SSE stream | already streams assistant messages |

Missing:

- a session-scoped endpoint to start a review turn
- a session-scoped typed review mode and review context payload
- a small session review service/helper that owns review-specific validation
- per-adapter handling that routes a review turn to the agent's native review surface
- a `Review` button in the session UI

## Design

### Backend: one session-native review type, one endpoint

Reviews should be modeled as a **session action**, because they can run before a PR exists and because the eligibility rules are about session state, not PR health. The natural API is:

```
POST /api/v1/sessions/:id/review
{ "mode": "default" | "security" }   // optional; defaults to "default"
```

Add a typed session review mode in `internal/models`, separate from `PullRequestRepairActionType`:

```go
type SessionReviewMode string

const (
    SessionReviewModeDefault  SessionReviewMode = "default"
    SessionReviewModeSecurity SessionReviewMode = "security"
)
```

Extend continuation metadata with a dedicated review payload instead of overloading `RepairAction`:

```go
type SessionReviewContext struct {
    Mode           models.SessionReviewMode `json:"mode"`
    PreviousDiff   string                   `json:"previous_diff,omitempty"`
    RequestSummary string                   `json:"request_summary,omitempty"`
}

type RevisionContext struct {
    FormattedFeedback string                 `json:"formatted_feedback"`
    PreviousDiff      string                 `json:"previous_diff"`
    CommentSummary    string                 `json:"comment_summary"`
    ReviewContext     *SessionReviewContext  `json:"review_context,omitempty"`
    RepairAction      models.PullRequestRepairActionType `json:"repair_action,omitempty"`
    RepairContext     *PullRequestRepairContext          `json:"repair_context,omitempty"`
}
```

This keeps `RevisionContext` as the generic "why is this a follow-up turn?" envelope, while preserving a clear separation between:

- session review context
- PR repair context
- human review feedback context

Add a small session-scoped service, for example `StartSessionReview`, that owns review-specific validation and enqueue behavior. The handler:

1. Loads the session and the agent's adapter.
2. Validates that the session:
   - is not currently running
   - has a resumable snapshot or continuation path
   - has a non-empty current diff
3. Builds a `RevisionContext` with:
   - `ReviewContext.Mode`
   - `ReviewContext.PreviousDiff` populated from the session's latest authoritative diff
   - `ReviewContext.RequestSummary` such as `"User asked for an agent review."`
4. Calls the session continuation/job-enqueue path with:
   - `shortPrompt`: `"Please review your changes."` (or `"Please run a security review on your changes."`)
   - `revisionContext`: the JSON payload above

This service can internally reuse the same lower-level mechanics PR repair uses today:

- claim/resume an idle session when possible
- append a user turn
- persist revision context
- enqueue `continue_session`

But the review flow should not depend on PR repair enums, PR health snapshots, or PR repair dedupe tables.

### Adapters: a tiny capability check

The adapter layer is where each agent's native review surface gets invoked. We add **one optional method** to `AgentAdapter`:

```go
// ReviewModes returns the review modes this adapter supports natively.
// Empty / nil means the adapter has no native review surface; the UI
// hides the Review button for sessions running this agent.
type ReviewCapableAdapter interface {
    ReviewModes() []string  // e.g. []string{"default", "security"}
}
```

We do **not** add a separate `PrepareReview` / `ExecuteReview`. Reviews flow through the existing `PreparePrompt` + `Execute` path, gated on `RevisionContext.ReviewContext`. Adapters that recognize a review context build the right CLI invocation for their agent. Adapters that do not support native review simply do not expose `ReviewModes()`.

This keeps the contract minimal:

- New agent with native review support → implement `ReviewModes()` and branch in `PreparePrompt` / `Execute` when `input.RevisionContext != nil && input.RevisionContext.ReviewContext != nil`.
- New agent without native review → do nothing. The Review button doesn't show up; user can still type "review this".

### Per-agent integration

Each adapter is the single owner of how its agent does review. Sketches of the four current ones:

#### Claude Code (`adapters/claude_code.go`)

- `ReviewModes()` returns `["default", "security"]`.
- When `ReviewContext.Mode == "default"`: invoke Claude Code's native review affordance for the current diff.
- When `ReviewContext.Mode == "security"`: invoke Claude Code's native security-review affordance.
- The skill output streams back through the current `claudeStreamEvent` parser unchanged — review findings land in the assistant message just like any other Claude Code output.

#### Codex (`adapters/codex.go`)

- `ReviewModes()` returns `["default"]` (extend if/when Codex adds more).
- On a review turn, the adapter invokes Codex's native `/review` entry point for the current diff. Output streams through the existing Codex parser.

#### Gemini CLI (`adapters/gemini_cli.go`)

- Gemini does not ship a dedicated review subcommand today. Either:
  - omit `ReviewModes()` so the button hides on Gemini sessions, **or**
  - return `["default"]` and emit a hand-rolled review prompt from the adapter.
- Recommendation: omit for v1. We do not invent fake "native" review for agents that don't have one — that's the explicit quality bar. When Gemini ships a review surface, the adapter swaps over; nothing else changes.

#### Amp (`adapters/amp.go`)

- `ReviewModes()` returns `["default"]`.
- Adapter invokes Amp's native review entry point and streams output through the existing Amp parser.

#### Pi (`adapters/pi.go`)

- No native review surface; do not implement `ReviewModes()`. Button hidden.

### Frontend

- New `<ReviewButton sessionId mode={...} />` rendered in the session detail header in `frontend/src/app/(dashboard)/sessions/[id]/session-detail-content.tsx`, near the existing run/cancel controls. This is the same neighborhood where `<PRHealthBanner>` (`frontend/src/components/pr-health-banner.tsx`) renders Fix tests / Resolve conflicts on PR-attached sessions.
- The button is shown only when the server reports that the concrete session supports review and the session currently has a resumable diff. Expose this via the existing session response or a small `GET /api/v1/sessions/:id/review-capabilities` endpoint.
- Click → `POST /api/v1/sessions/:id/review` → backend enqueues the continuation turn → existing SSE stream renders the assistant message in place. No new sidebar, no new view.
- A small dropdown appears only when there are 2+ modes: `Review ▾ → { Code review, Security review }`. Single-mode adapters render a plain button.
- Pending state mirrors the Fix tests / Resolve conflicts spinner pattern (`pr-health-banner.tsx:91-111`): button disabled, spinner + "Reviewing…" while a turn is in flight.

### Output

Review output is just an assistant message in the session transcript. No new persistence:

- It shows up in the chat scroll like any other agent turn.
- If the agent emits structured findings (e.g. Claude Code's review skill outputs file-anchored comments), the existing review-comment plumbing in `session_review_comments.go` and `<ReviewDiffView>` already renders them inline. Adapters that produce structured output write findings through the same path human reviewers already use.
- If the agent only emits prose, the user sees prose. That's fine — the same as the existing Fix tests / Resolve conflicts output.

## What we are explicitly *not* building

- **No `review_runs` table.** A review is one turn on the existing session.
- **No PR repair reuse at the type/service boundary.** We can reuse low-level continuation helpers, but review remains a session-native flow.
- **No new SSE channel.** The session's existing stream carries the review output.
- **No fallback prompt-based review for agents without native support.** The button hides; users can still ask in prose.
- **No auto-trigger on session completion.** v1 is user-initiated only.
- **No cross-PR / cross-repo batch review.** v1 is one session, one turn.

## Pluggability

Adding review support for a new agent is two changes:

1. Implement `ReviewModes()` on the adapter.
2. Branch on `RevisionContext.ReviewContext` inside the adapter's existing `PreparePrompt` / `Execute` to call the agent's native review entry point.

No frontend changes. No migrations. No new endpoints.

## Risks

- **Adapter drift.** Vendor CLIs change their review surfaces. Mitigation: the same one we use for normal runs — golden-file tests against representative output, `agent.LogEntry` traces preserved for debugging.
- **Cost.** Each click is a full agent run on the session sandbox. Mitigation: the existing per-org session-duration and concurrency limits already cap this. If review usage blows up, add a per-session rate limit (1 review every N minutes) before considering anything heavier.
- **Relationship to doc 11's flywheel.** Doc 11 captures *human* review feedback on 143-generated PRs and turns it into rules. Findings produced by the Review button are *agent* feedback and should not feed that extractor. Tag review-button-derived comments with `source = "agent_review"` so the extractor filters them out — this is a one-line check in the existing pipeline.

## Phasing

1. **Phase 1** — Backend: add `SessionReviewMode`, session-scoped endpoint/service, and `RevisionContext.ReviewContext`. Update Claude Code and Codex adapters to invoke their native review affordances when review context is present. ✅ shipped
2. **Phase 2** — Frontend: add the button + spinner state in the session detail header, gated on capability check. ✅ shipped
3. **Phase 3** — Per-agent rollout: Codex, Amp, then any future agents. Each is independent and lands in its own PR. 🚧 in progress — Codex now routes review turns through native `/review`; Amp and future agents remain gated on upstream-native review support.

## What shipped

- `models.SessionReviewMode` (`default`, `security`) and `SessionReviewCapabilities` API shape (`internal/models/session_review.go`).
- `agent.RevisionContext.ReviewContext` + `SessionReviewContext`, plus `agent.ReviewCapableAdapter`, `agent.AdapterReviewModes`, `agent.ReviewModeProvider`, and `AgentPrompt.RevisionContext` so adapters can branch in `Execute` without re-running `PreparePrompt` (`internal/services/agent/adapter.go`).
- `sessionreview.Service` owning validation + claim/enqueue, decoupled from PR repair (`internal/services/sessionreview/service.go`).
- `POST /api/v1/sessions/{id}/review` and `GET /api/v1/sessions/{id}/review-capabilities` (`internal/api/handlers/session_review.go`).
- `ClaudeCodeAdapter.ReviewModes()` + `claudeCodeReviewSlashCommand` so review turns route to `/review` and `/security-review`.
- `CodexAdapter.ReviewModes()` + `codexReviewSlashCommand` so review turns route to Codex's native `/review`.
- `<ReviewButton>` in the session detail header, single-mode button or 2+ mode dropdown, spinner mirroring PR health repair affordances (`frontend/src/components/review-button.tsx`, `session-detail-content.tsx`).
- Audit action `session.review.requested` recorded on every successful start.

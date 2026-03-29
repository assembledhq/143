# 39 - PM Agent Logging & Issue Creation CLI

> **Status:** Proposed | **Last reviewed:** 2026-03-29

## Problem

The PM agent has two gaps compared to the session coding agent:

1. **No session-level logging.** The coding agent streams every turn, tool
   call, and reasoning step into `session_logs`, giving operators full
   visibility. The PM agent discards all of this — its `logCh` channel is
   drained into a no-op goroutine (`service.go:303-307`). The only persisted
   artifacts are the final `pm_decision_log` entries and the `analysis` text
   field on `pm_plans`. When something goes wrong (bad prioritization, missed
   issue, unexpected skip) there is no way to trace *why* the PM agent made
   that decision.

2. **No ability to create issues.** The PM agent can only reference issues
   that already exist in the database via Sentry/Linear ingestion. When the
   PM agent identifies new work — from Slack threads, codebase exploration,
   or pattern analysis — it has no mechanism to create a first-class issue.
   It can only embed the idea in its plan's `analysis` text or recommend a
   `new_project`, both of which are easy to lose.

## Goals

- PM agent runs produce full session logs, identical in shape to coding agent
  session logs, viewable in the same UI surfaces.
- PM agent can create issues via a CLI tool (`143-tools issue_create`),
  following the same pattern used for Sentry/Linear/Notion tools.
- Issues created by the PM agent are first-class `issues` rows, triageable
  and delegatable in subsequent PM cycles.

## Non-Goals

- Changing how the PM agent's plan output format works (tasks, clusters,
  skips, etc. remain the same).
- Adding issue creation to the coding agent — only the PM agent needs this.
- Building a full issue management CLI (update, close, list) — only creation
  is in scope for now.

---

## Current State

### PM Agent Wake-Up Flow

```
Scheduler (10-min tick)
  → Acquires distributed lock
  → Checks: last plan > PMScheduleHours ago?
  → Enqueues pm_analyze job (trigger=cron)

Worker claims pm_analyze job
  → pmService.Analyze(ctx, orgID, trigger, repoID)

PM Service: Gather Context
  → Open issues (Sentry, Linear, etc.)
  → In-flight sessions
  → Recent outcomes + recent PRs
  → Past PM decisions (institutional memory)
  → Product context docs + Slack thread summaries

PM Service: Setup Sandbox
  → Docker container (1 CPU, 2GB RAM, 10-min timeout)
  → Clone repo with GitHub token
  → Write .pm-context.json, .pm-documents/, AGENTS.md

PM Agent Executes (Claude Code adapter)
  → Reads AGENTS.md, .pm-context.json
  → Explores codebase (git log, file reads, code tracing)
  → Outputs <pm-plan> JSON

PM Service: Parse & Persist
  → Extract plan from XML tags
  → Create PMPlan record (status=executing)
  → Write pm_decision_log entries

PM Service: Execute Plan
  → For each task (within capacity):
    → Create Session record (status=pending)
    → Mark issues as triaged
    → Enqueue run_agent job
  → Update plan status to completed
```

### PM Agent Logging (Current)

```go
// service.go:303-307 — logs are discarded
logCh := make(chan agent.LogEntry, 100)
go func() {
    for range logCh { }
}()
```

The coding agent, by contrast, uses `orchestrator.streamLogs()` which
persists every entry to `session_logs` with session ID, level, message,
metadata, and turn number.

### Issue Creation (Current)

Issues enter the system exclusively through:
- Sentry webhook ingestion
- Linear sync ingestion
- Manual creation via API

The PM agent has no path to create issues. The `143-tools` CLI provides
Sentry, Linear, Notion, and GitHub tools but no issue creation command.

---

## Design

### Part 1: PM Agent Session Logging

#### Approach

Create a lightweight Session record for each PM analysis run, then stream
the PM agent's `logCh` into `session_logs` using the same persistence
pattern as the coding agent's `orchestrator.streamLogs()`.

#### Changes

**`internal/services/pm/service.go`**

1. Before executing the PM agent, create a Session record to serve as the
   log anchor:

   ```go
   pmSession := &models.Session{
       OrgID:     orgID,
       AgentType: "pm-agent",
       Status:    "running",
       Title:     ptrStr("PM Analysis"),
       PMPlanID:  nil, // set after plan is created
   }
   s.sessions.Create(ctx, pmSession)
   ```

2. Replace the no-op drain goroutine with a real log writer:

   ```go
   logCh := make(chan agent.LogEntry, 100)
   go func() {
       for entry := range logCh {
           metadata, _ := json.Marshal(entry.Metadata)
           log := &models.SessionLog{
               SessionID: pmSession.ID,
               Level:     entry.Level,
               Message:   entry.Message,
               Metadata:  metadata,
           }
           if err := s.sessionLogs.Create(ctx, log); err != nil {
               s.logger.Error().Err(err).Msg("failed to persist PM log entry")
           }
       }
   }()
   ```

3. After the plan is created and persisted, link the PM session to the plan:
   - Set `pmSession.PMPlanID = &planModel.ID`
   - Update session status to `completed` (or `failed` on error)

**`internal/services/pm/service.go` (Service struct)**

Add a `sessionLogs` dependency:

```go
type sessionLogStore interface {
    Create(ctx context.Context, log *models.SessionLog) error
}
```

Wire it in `NewService` or via a setter like the other optional stores.

**`internal/models/session.go`**

Ensure the Session model supports a nil `IssueID` (it should already —
the PM session has no primary issue). Verify the `AgentType` field
accepts `"pm-agent"` as a value.

#### Database

No schema changes. Reuses `session_logs` and `sessions` tables. The PM
session is distinguished by `agent_type = 'pm-agent'`.

#### Frontend: Reuse Existing Session Detail Components

**Principle:** PM agent sessions must use the same UI components as coding
agent sessions. There should be one viewing experience, not two. The PM
session is a session — it just happens to have `agent_type = "pm-agent"`.

**Existing component chain (reuse as-is):**

| Component | File | Role |
|-----------|------|------|
| `ChatTimeline` | `frontend/src/components/chat-timeline.tsx` | Renders the unified timeline of tool calls, assistant output, errors, and messages. Accepts `TimelineEntry[]` — fully generic, no agent-type assumptions. |
| `buildTimeline()` | `frontend/src/lib/timeline.ts` | Merges `SessionMessage[]` + `SessionLog[]` into `TimelineEntry[]` with tool-use/tool-result pairing and dedup. Entirely data-driven, works for any session. |
| `ToolGroupEntry` | `frontend/src/components/chat-timeline.tsx` | Collapsible tool call display (tool name badge, expandable result). Will show PM agent's `143-tools` calls naturally. |
| `ErrorEntry` | `frontend/src/components/chat-timeline.tsx` | Error log display. Works for PM errors identically. |
| Session detail page | `frontend/src/app/(dashboard)/sessions/[id]/session-detail-content.tsx` | Full session detail view with tabs (overview, changes, validation), status badges, duration, log streaming. |
| Log streaming (SSE) | `GET /api/v1/sessions/{id}/logs/stream` | Real-time log updates via EventSource. PM sessions stream identically since they use the same `session_logs` table. |
| `api.sessions.getLogs()` | `frontend/src/lib/api.ts` | Fetches logs by session ID. No agent-type filter — works for PM sessions out of the box. |

**What needs minor adjustment:**

1. **Sessions list page** (`sessions-page-content.tsx`): Currently shows
   all sessions in one table. PM agent sessions will appear here with
   `agent_type = "pm-agent"`. The existing `agent_type` column already
   renders this field (`row.original.agent_type.replace(/_/g, " ")`), so
   PM sessions will show as "pm agent". No code change needed — this works
   by default.

2. **Session detail: overview tab** (`OverviewTab` in
   `session-detail-content.tsx`): Shows "PM context" card when
   `session.pm_plan_id` is set. For PM sessions this may need a variant —
   the PM session *is* the PM plan, not a child of it. Two options:
   - (a) Skip the "PM context" card when `agent_type === "pm-agent"` and
     show a "PM Analysis" card with the plan's `analysis` text instead.
   - (b) Do nothing — the PM session won't have `pm_reasoning`/
     `pm_approach` set, so the card simply won't render. The plan analysis
     is visible in the timeline logs.
   **Recommendation:** Option (b) for launch. The timeline itself is the
   primary value. A dedicated PM summary card can be added later.

3. **Session detail: changes/validation tabs**: PM sessions produce no
   diffs or validation checks. These tabs will show empty states naturally
   (the existing "No validation data" and empty diff views handle this).
   No changes needed.

4. **Session detail: input bar**: The input bar at the bottom allows
   sending messages to active sessions. For PM sessions this is not
   meaningful (PM agent doesn't accept interactive input). Hide the input
   bar when `agent_type === "pm-agent"` — a one-line conditional.

5. **SSE streaming**: The session detail page already connects to
   `/api/v1/sessions/{id}/logs/stream` for active sessions. Since PM
   sessions write to the same `session_logs` table and use the same
   `ListByRunIDSince` query, SSE streaming works without changes. Users
   can watch the PM agent think in real time.

**What does NOT need a new component:**

- No `PMSessionDetail` component — use `SessionDetailContent` directly.
- No `PMLogViewer` — `ChatTimeline` + `buildTimeline` handle all log types.
- No `PMSessionList` — PM sessions appear in the existing sessions table.
- No separate API endpoints — all existing session/log endpoints work by
  session ID, agnostic of agent type.

**Filtering PM sessions (optional, post-launch):**

The sessions list could add a filter chip for `agent_type` to let users
isolate PM sessions. The backend `SessionFilters` struct would need an
`AgentType` field (not yet present), and the API handler would accept an
`agent_type` query parameter. This is a nice-to-have, not a blocker.

---

### Part 2: Issue Creation CLI Tool

#### Approach

The `143-tools` CLI runs inside the sandbox with no direct database access.
It communicates with external services via API tokens passed as environment
variables. To create issues in the 143 database, we need:

1. An internal API endpoint for issue creation
2. A scoped auth token passed to the PM sandbox
3. A new `issue_create` CLI command in `143-tools`

#### Option Analysis

| Option | Pros | Cons |
|--------|------|------|
| **(a) API endpoint + sandbox token** | Follows existing CLI pattern; issues are created in real-time during PM execution; PM agent gets feedback (created issue ID) | Requires new API endpoint + auth mechanism for sandbox-to-server calls |
| **(b) File-based output parsing** | No new API surface; host parses files after execution | PM agent gets no feedback; fragile file convention; can't reference created issues in plan |
| **(c) Extend `<pm-plan>` output** | Simplest change; no new infra | Same as (b) — no real-time feedback; bloats plan format |

**Recommendation: Option (a)** — API endpoint with sandbox token.

#### Changes

**New API endpoint: `POST /api/v1/internal/issues`**

File: `internal/api/handlers/issues.go`

```go
type CreateIssueRequest struct {
    Title       string   `json:"title"`
    Description string   `json:"description"`
    Source      string   `json:"source"`      // "pm-agent"
    Severity    string   `json:"severity"`    // info|warning|error|critical
    Tags        []string `json:"tags"`
}

type CreateIssueResponse struct {
    ID    string `json:"id"`
    Title string `json:"title"`
}
```

- Authenticated via a short-lived internal token scoped to the org
- Creates an `Issue` record via `IssueStore.Upsert`
- Sets `source = "pm-agent"` so these issues are identifiable
- Returns the issue UUID so the PM agent can reference it in the plan

**Sandbox auth token:**

File: `internal/services/pm/service.go`

Before creating the sandbox, generate a short-lived (10-min TTL) internal
API token scoped to the org. Pass it as `INTERNAL_API_TOKEN` env var.
Also pass `INTERNAL_API_URL` pointing to the server's internal endpoint.

**New CLI command: `143-tools issue_create`**

File: `internal/services/mcp/tools.go` (tool registration)

Register a new tool in the integration registry:

```
Name:        issue_create
Description: Create a new issue for the engineering team to work on
Flags:
  --title       (required) Issue title
  --description (required) Detailed description of the issue
  --severity    (optional) info|warning|error|critical (default: info)
  --tags        (optional) Comma-separated tags
```

File: `internal/services/integration/issue_creator.go` (new)

Implements the tool handler — makes an HTTP POST to the internal API
endpoint using the `INTERNAL_API_TOKEN` and `INTERNAL_API_URL` env vars.

File: `internal/services/mcp/registry_builder.go`

Register the issue creator when `INTERNAL_API_TOKEN` is present:

```go
if token := os.Getenv("INTERNAL_API_TOKEN"); token != "" {
    apiURL := os.Getenv("INTERNAL_API_URL")
    creator := integration.NewIssueCreator(token, apiURL)
    reg.RegisterIssueCreator(creator)
}
```

**Integration registry update:**

File: `internal/services/integration/registry.go`

Add `IssueCreator` interface and `RegisterIssueCreator` / `IssueCreator()`
methods, following the same pattern as ErrorTracker, TaskManager, etc.

```go
type IssueCreator interface {
    Name() string
    CreateIssue(ctx context.Context, params CreateIssueParams) (CreateIssueResult, error)
}
```

#### PM Sandbox Environment

File: `internal/services/pm/service.go`

Add to sandbox env vars (alongside existing SENTRY_AUTH_TOKEN, etc.):

```
INTERNAL_API_TOKEN=<short-lived-scoped-token>
INTERNAL_API_URL=http://server:8080/api/v1/internal
```

---

### Part 3: PM System Prompt Update

File: `internal/prompts/templates/pm_system_prompt.template`

Add a new section after the Slack Channel Monitoring section:

```
## Creating New Issues

When your analysis reveals work that should be tracked but no existing issue
covers it — for example, a pattern you noticed in the codebase, a concern
raised in a Slack thread, or a gap you identified during exploration — you
can create a new issue using the 143-tools CLI:

    143-tools issue_create --title "..." --description "..." --severity warning

The tool returns the created issue's UUID. You can then reference this UUID
in your plan's tasks or clusters, just like any other issue.

Use this when:
- A Slack thread surfaces a real bug or feature gap with no existing issue
- Your codebase exploration reveals a problem not tracked anywhere
- You want to break a cluster into trackable individual items

Do NOT use this to duplicate issues that already exist in .pm-context.json.
```

---

## File Change Summary

### Backend

| File | Change |
|------|--------|
| `internal/services/pm/service.go` | Add session log persistence; add sessionLogs dependency; generate sandbox auth token; pass new env vars |
| `internal/models/session.go` | Verify nil IssueID support and pm-agent agent type |
| `internal/api/handlers/issues.go` | New internal issue creation endpoint |
| `internal/api/router.go` | Register new endpoint |
| `internal/services/integration/registry.go` | Add IssueCreator interface + registration |
| `internal/services/integration/issue_creator.go` | New file — HTTP client for internal API |
| `internal/services/mcp/tools.go` | Register `issue_create` tool |
| `internal/services/mcp/registry_builder.go` | Wire IssueCreator from env vars |
| `internal/prompts/templates/pm_system_prompt.template` | Add issue creation instructions |
| `cmd/server/main.go` | Wire sessionLogs into PM service |

### Frontend (minimal — reuse existing components)

| File | Change |
|------|--------|
| `frontend/src/app/(dashboard)/sessions/[id]/session-detail-content.tsx` | Hide input bar when `agent_type === "pm-agent"` (one conditional) |
| No new components | `ChatTimeline`, `buildTimeline`, `ToolGroupEntry`, `ErrorEntry`, SSE streaming, session list, session detail — all reused as-is |

## Migration

No database migrations required. All changes use existing tables
(`sessions`, `session_logs`, `issues`).

The new internal API endpoint requires a token generation mechanism. This
can reuse the existing `github.GetInstallationToken` pattern or use a
simpler HMAC-signed JWT with org scope and short TTL.

## Rollout

1. **Phase 1:** PM agent session logging (no external dependencies, low risk)
2. **Phase 2:** Internal API endpoint + issue creation CLI tool
3. **Phase 3:** System prompt update + end-to-end testing

Each phase can be shipped independently. Phase 1 provides immediate
observability value. Phase 2+3 enable the new issue creation capability.

## Open Questions

1. **Should PM session logs share the `session_logs` table or get their
   own table?** Sharing is simpler and lets the existing UI work
   immediately, but PM logs may have different retention needs. Recommend
   sharing for now, splitting later if needed.

2. **Should the PM agent be able to create issues during execution, or
   only recommend them in the plan output?** This design proposes real-time
   creation (option a). If we prefer a more conservative approach, we
   could start with plan-output-only (option c) and upgrade later.

3. **Token scoping for the internal API.** The sandbox auth token should
   be org-scoped and short-lived. Need to decide: reuse existing JWT
   infrastructure or create a purpose-built token type?

4. **Rate limiting on issue creation.** Should there be a cap on how many
   issues the PM agent can create per cycle? Recommend starting with a
   sensible limit (e.g., 10 per run) to prevent runaway creation.

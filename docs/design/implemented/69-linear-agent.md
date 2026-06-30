# 69 - Linear Agent (inbound assignment / @-mention triggering)

> **Status:** Implemented | **Last reviewed:** 2026-06-30

> **Depends on:** [./62-linear-session-linking.md](./62-linear-session-linking.md), [./04-ingestion.md](./04-ingestion.md)

## Summary

Assign a Linear issue (or @-mention `@143` in a comment) to the 143 agent
user, and 143 spins up a coding session in the cloud, posts progress back
into the Linear AgentSession as live activities, and ends with a PR
linked to the issue. Mirrors what Cursor's `@Cursor` and OpenAI's
`@Codex` ship for Linear.

This reverses the one-way contract from design 62 ("Linear changes never
affect 143 sessions") *only* for the explicit, opt-in trigger event.
Assignment to the agent user is the user's signal that they want a
session to start; the rest of design 62 stands.

## Architecture

```
Linear (assign @143 / @-mention / follow-up comment)
  │  POST /api/v1/webhooks/linear?integration_id=<uuid>
  │  Linear-Event: AgentSessionEvent
  ▼
HandleLinear  (existing — verify HMAC, record webhook_deliveries)
  │  branch on Linear-Event header
  ▼
LinearAgentDispatcher
  │  parse minimal envelope
  │  upsert linear_agent_sessions row (idempotent)
  │  emit a fast bootstrap "thought" activity (10s SLA)
  │  enqueue linear_agent_event job
  │  return 200  (well under 5s SLA)
  ▼
worker job linear_agent_event
  │  case "created":
  │    - fetch issue from Linear (live, not cached webhook payload)
  │    - resolve repo via team→repo mapping (label override → exact →
  │      team default → Linear default → shared org default →
  │      fail-with-message)
  │    - upsert issues row
  │    - create models.Session (Origin = SessionOriginIssueTrigger,
  │      AgentType = OrgSettings.DefaultAgentType)
  │    - link primary issue, persist AgentSessionID in provider_state
  │    - attach session_id to linear_agent_sessions row
  │    - enqueue run_agent on the agent queue
  │  case "prompted":
  │    - lookup linked 143 session
  │    - fetch the comment body via FetchComment
  │    - claim idle/resumable session, append turn message, enqueue
  │      continue_session; if the session is still running, append under
  │      a session row lock so the in-flight turn drains the message
  ▼
agent.Orchestrator (existing) → enqueueLinearMilestone
  │  HandleMilestone — durable attachment + rolling comment (existing)
  │  HandleStateTransition — workflow state moves (existing)
  │  HandleAgentMilestone — fans out AgentActivity emits (new)
  ▼
Linear AgentSession streams thoughts / actions / response (PR link)
  + agentSessionUpdate sets externalUrls=[143 session]
```

## OAuth — single upgraded flow

The existing org-scoped Linear OAuth was upgraded in place (rather than
a parallel install) to request `actor=app` plus `app:assignable` and
`app:mentionable` alongside the legacy `read,write` scopes. The
resulting token is the `@143` agent user identity and is used for *all*
Linear writes — agent activities, attachments, the rolling comment, and
state transitions.

Implications:

- Workspace-admin install only (Linear requirement for `actor=app`).
- Existing connected orgs need a one-time admin re-authorize. Surfaced
  via the install-status endpoint
  (`GET /api/v1/integrations/linear/agent`); the frontend shows a
  banner when `agent_scopes_granted=false`.
- Once re-authorized, the existing `Service.integrationFor(orgID)`
  resolver returns the right token for `services/linear/writes.go`.
  The `🤖 143 automated update` comment prefix retires automatically
  because every write now authors as the `@143` user.

## Data model

### `linear_agent_sessions`
Idempotency anchor + Linear↔143 session bridge. Re-deliveries of the
same `AgentSessionEvent: created` collide on
`UNIQUE (org_id, linear_agent_session_id)` and the row's `session_id`
(if any) is preserved so the worker can recover the prior 143 session.
State machine mirrors Linear's: `pending → in_progress →
awaiting_input → complete | error`.

### `linear_agent_activity_log`
At-most-once log of AgentActivity emits.
`UNIQUE (agent_session_row_id, idem_key)` makes the writer's
two-phase Reserve→Emit→Complete pattern naturally idempotent under
concurrent fan-outs and replays.

### `linear_team_repo_mappings`
The `(team, project) → repo` lookup the dispatcher consults so an
inbound AgentSession knows which repo to clone.
`UNIQUE (org_id, team_id, COALESCE(project_id, ''))` lets a team have
both project-specific mappings and a team-default row. Resolver
priority: label override → exact match → team default → Linear default
(`org_settings.linear_agent.default_repo_id`) → shared org default
(`org_settings.default_work_repository_id`).

### `LinearProviderState.AgentSessionID`
JSONB-side field added to the existing `session_issue_link_provider_state`
row so `HandleAgentMilestone` can discover whether a 143 session was
triggered through the agent path. Empty for sessions created the
manual way; the milestone fan-out silently no-ops when empty.

### `linear_user_links`
Linear user identity bridge used for PR authorship and audit attribution.
The inbound AgentSession creator is the preferred human actor for
Linear-started sessions; the issue creator is only a compatibility fallback.
On session creation, 143 first looks for an existing
`(org_id, linear_workspace_id, linear_user_id)` link. If none exists and
Linear exposes an email for the AgentSession creator, 143 matches that email
against org members using primary email, GitHub noreply email, and secondary
emails, then stores an `email_match` link for future sessions. Admin/self-link
sources are reserved for private or mismatched-email users and must not be
overwritten by automatic email matching.

The bridge keeps onboarding low for the common case where Linear and 143
emails align, while avoiding repeated heuristic matching once a stable Linear
user id has been observed. PR creation still follows the GitHub authorship
policy: a mapped user only creates the PR as themselves when they have valid
GitHub App user auth and repo access; otherwise `user_preferred` falls back to
the 143 GitHub App and `user_required` blocks.

### `OrgSettings.LinearAgent`
`{ enabled, default_repo_id?, app_user_handle, allow_revision_per_prompt,
per_team_enabled }`. Per-org opt-in; `enabled` defaults to false. The
process-wide `LINEAR_AGENT_ENABLED` env var is the kill switch above
the per-org toggle.

`default_repo_id` is the Linear-specific override. When it is unset, the
resolver falls back to `OrgSettings.DefaultWorkRepositoryID`, the shared
default used by other inbound integration starts such as Slack mentions.

The per-team `enabled` gate is applied when the `created` event starts a
new session. `prompted` follow-ups on an existing AgentSession do not
re-apply `EnabledFor(team)`; disabling a team should prevent new inbound
sessions without yanking or stranding in-flight work. Terminal-session
follow-ups are still governed by `allow_revision_per_prompt`.

## Activity stream

Body strings here are authoritative copy that the implementation must
match. Drift between this table and the runtime strings has been a
source of confusion (the implementation lives in
`internal/services/linear/agent_state.go`); the strings below were
synced to match the implementation as of 2026-05-15. When changing copy,
update both sides in the same PR.

| 143 moment | AgentActivity | Idem key |
|---|---|---|
| Dispatcher (after `created`) | `thought` ephemeral: "Reading {KEY} and resolving the right repo…" | `bootstrap:opened` |
| `MilestoneLinked` | (suppressed — bootstrap already covered it) | — |
| `MilestoneStarted` | `action`: "Starting coding session" + state pin `active` | `milestone:started` |
| `MilestonePROpened` | `response`: "Opened PR #N." | `milestone:pr_opened` |
| `MilestonePRMerged` | `action`: "PR #N merged." + state pin `complete` | `milestone:pr_merged` |
| `MilestoneEndedNoPR` | `response`: "Done — no code changes were needed." + state pin `complete` | `milestone:ended_no_pr` |
| `MilestoneFailed` | `error`: "Session failed. See the 143 deep link for details." + state pin `error` | `milestone:failed` |
| Repo unmapped | `response`: "I don't have a repository configured…" + state pin `complete` | `bootstrap:unmapped_repo` |
| Prompted-before-created timeout | `error`: "We didn't receive a session-start event from Linear in time…" + state pin `error` | `prompted:awaiting_created_timeout` |

## Failure modes

- **5s ack SLA**: Dispatcher does idempotent INSERT + worker enqueue
  + best-effort bootstrap emit, all comfortably under 1s. Ack first
  is the key invariant.
- **10s first-activity SLA**: bootstrap thought emits from the
  dispatcher (synchronous in the HTTP request), not the worker, so a
  backed-up worker queue can't blow this SLA.
- **Linear API down during created**: the worker job fails and
  retries; the dispatcher's row already exists so retries just
  resume from "fetch the issue".
- **Re-delivery of created**: row UNIQUE collides, returns the
  existing `session_id`, worker handler short-circuits.
- **Prompted before created**: lookup misses, dispatcher still enqueues
  a retryable worker job. The worker re-checks the bridge row and backs
  off briefly until `created` attaches the 143 session.
- **Prompted while a turn is finishing**: the running-session append path
  locks the session row before inserting the user message. If the turn is
  still running, the finisher's post-turn drain sees the message; if the
  turn already went idle/terminal, the prompted job retries through the
  normal idle/resume branch so no message is left without a continuation.
- **Repo unmapped**: `response` activity + AgentSession state pinned
  to `complete`. Treated as a benign user-actionable state, not a
  system error.
- **Created worker dead-letter before session start**: emit an `error`
  activity and pin the AgentSession/bridge state to `error` so Linear
  does not keep showing only the bootstrap "Reading…" thought after the
  retry budget is exhausted.
- **Activity emit failure post-Reserve**: row stays present without
  `linear_activity_id`; replays short-circuit on UNIQUE collision.
  Trade-off: prefer "missing activity" to "duplicate notifications".
  The `EmitOrDiscard` variant exists for elicitation activities
  where missing emit is more visible than dup.

## Phased rollout

| Phase | Scope | Status |
|---|---|---|
| 1. Plumbing | Migrations, stores, OAuth scope upgrade, agent client GraphQL | Shipped |
| 2. Created path | Dispatcher, worker handler, repo resolver, milestone fan-out, settings API | Shipped |
| 3. Prompted path | Turn-append on follow-up comments | Shipped |
| 4. Polish | Comment fetch, debug surface, enable toggle, metrics | Shipped |
| 5. GA | Docs, marketing, marketplace listing | In progress |

## Open questions / risks

- **Token rotation**: actor=app tokens are long-lived but
  workspace-admin revocable. No refresh token. The install-status
  endpoint flips to "needs re-authorize" when `HasAgentScopes()`
  returns false; a 24h health probe (TODO) will detect rotation.
- **AppUserNotification fallback**: phase 4 logs but doesn't
  process. A future fallback would synthesize a `created` event by
  calling `agentSessionCreateOnIssue` against Linear, then re-enter
  the standard path.
- **Cancellation**: design 62 forbids "Linear changes affect 143
  sessions" — closing the AgentSession on Linear is currently
  ignored. Revisit if dogfood shows demand.
- **Worker queue partition**: a backed-up `run_agent` queue must not
  starve the agent dispatcher's job throughput; consider a separate
  `linear_agent` queue partition for phase-5+ scale.

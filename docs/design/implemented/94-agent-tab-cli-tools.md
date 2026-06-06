# Design: Agent Tab CLI Tools

> **Status:** Implemented | **Last reviewed:** 2026-06-05

## Summary

143 should let coding agents manage sibling tabs in their current session through
`143-tools`. An agent should be able to discover the tabs that share its
sandbox, read another tab's recent transcript output, create a new blank tab,
and send work to that tab.

The feature builds on the implemented session-thread model:

- product UI calls them **tabs**
- backend/API objects call them **threads**
- one session owns the sandbox, branch, preview, PR path, and final artifact
- each tab/thread owns a transcript, runtime lane, status, model, delivery
  state, file attribution, and cost

The new contract is not a new orchestration system. It is a sandbox-safe CLI
surface over the existing thread APIs, gated by an organization coding-agent
setting that defaults on.

Related docs:

- [implemented/68-sandbox-agent-tabs-and-threads.md](../implemented/68-sandbox-agent-tabs-and-threads.md)
- [implemented/88-shared-sandbox-thread-runtimes.md](../implemented/88-shared-sandbox-thread-runtimes.md)
- [implemented/57-coding-agent-settings-rethink.md](../implemented/57-coding-agent-settings-rethink.md)
- [implemented/90-agent-log-provider-tools.md](../implemented/90-agent-log-provider-tools.md)

## Problem

Human users can already work with multiple tabs in one session, but sandbox
agents cannot use that product surface directly. When an agent wants another
agent lane to review a diff, run tests independently, inspect a failing area, or
continue a parallel investigation, it has no first-party way to do that from
inside the sandbox.

This creates two bad patterns:

- agents ask the human to create a tab manually, even when the task is routine
- agents improvise with shell background processes or external CLIs, which
  bypasses 143's transcript, logs, cost, permissions, and tab recovery model

`143-tools` is already the approved sandbox-agent integration surface for PR
creation and connected-service lookup. Agent tab management should live there
for the same reason: it preserves product workflows while giving agents a small,
auditable interface.

## Goals

- Let a sandbox agent list tabs in its current session.
- Let a sandbox agent read enough sibling-tab output to coordinate work.
- Let a sandbox agent create a new tab in the same session/sandbox.
- Let a sandbox agent send an initial or follow-up instruction to a tab.
- Add an organization coding-agent setting, default on, that controls whether
  sandbox agents can use these tab tools.
- Keep all access org-scoped and session-scoped; no cross-session browsing in
  v1.

## Non-Goals

- Building a planner that automatically decomposes tasks across tabs.
- Letting agents create independent sessions, branches, PRs, or sandboxes.
- Letting agents read arbitrary historical sessions from the org.
- Adding direct peer-to-peer process communication between agent runtimes.
- Replacing the human tab UI or existing session-thread APIs.
- Granting agents access to raw secrets, hidden credentials, or production data
  beyond tools they already have.

## Product Surface Area

### Coding Agent Settings

Add a setting on the organization coding-agent settings page:

```text
Agent tab tools                                      On
Allow coding agents to view, create, and message tabs in their current session.
```

Placement:

- Page: Settings -> Coding Agents (`/settings/agent`)
- Section: execution controls, near max concurrent runs and session duration
- Control: shadcn `Switch`
- Default: on for new organizations and for organizations with missing setting
- Visibility: admins can edit; members/builders/viewers can see read-only state

The setting copy should be direct. It should not imply agents can see all tabs
across the org; v1 is current-session only.

When off:

- `143-tools session_tabs_*` commands are not exposed in the generated sandbox
  tool documentation for agents.
- Direct calls to hidden tab-tool commands still return `403 TAB_TOOLS_DISABLED`
  so stale agents or cached tool lists fail closed.
- The session UI still allows humans to manage tabs.
- Existing agent runs continue; only sandbox-agent tab tooling is disabled.

### Session UI

No new primary session UI is required in v1. Tabs created by an agent should
appear in the existing tab strip with the same pending/running/unread states as
human-created tabs.

Small additions:

- Tabs created through CLI should show provenance only in a tooltip or details
  hover state, such as `Created by Codex via 143-tools`. Do not add visible
  text to the tab title or tab strip; that space is already tight.
- Audit log details should identify the source thread, created thread, agent
  type/model, and tool name.
- Transcript messages sent through CLI should appear as normal user messages in
  the target tab, with metadata recording `source = agent_tool`.

### Agent Prompt Surface

When the setting is on, the generated sandbox tools documentation should include
the new commands. The prompt should tell agents:

- use a new tab for parallel review/testing/investigation in the same branch
- use a new session only when work needs an independent branch or PR
- summarize why a tab was created in the message sent to it
- inspect sibling output before assuming the other tab has finished

## Database Schema

Use the existing `organizations.settings` JSONB document for the setting:

```json
{
  "coding_agent_tab_tools_enabled": true
}
```

Contract:

- Type: boolean
- Default when absent: `true`
- Tenancy: org-scoped through `organizations.id`
- Validation: reject non-boolean values in settings patch validation
- Audit: settings diff should include `coding_agent_tab_tools_enabled`

No new table is required for v1. Existing tables already store the durable tab
and transcript state:

- `session_threads`
- `session_messages`
- `session_logs`
- `thread_inbox_entries`
- `thread_runtimes`
- `session_thread_file_events`

Recommended future hardening, not required for v1:

- Add `session_threads.created_by_source text NOT NULL DEFAULT 'user'` with
  enum-like values `user`, `agent_tool`, `system`.
- Add `session_threads.created_by_thread_id uuid NULL REFERENCES session_threads(id)`.
- Add `session_threads.created_by_user_id uuid NULL REFERENCES users(id) ON DELETE SET NULL`
  when the creator maps to a real platform user. This follows the codebase's
  provenance pattern for user-created resources (`created_by_user_id`,
  `triggered_by_user_id`, `added_by_user_id`) while staying nullable for
  system or agent-tool creation where the durable actor is the source thread
  rather than a human. If the sandbox token carries the user who initiated the
  source session, store that user here; if not, leave it null and rely on
  `created_by_source`, `created_by_thread_id`, and audit actor details.
- Add `session_messages.source text` if transcript provenance needs to be
  queryable without inspecting audit details.

If those columns are added, they must include `org_id`-safe writes and reads
through existing session/thread ownership.

## API Contract

The frontend-facing thread APIs mostly exist. The new work is a narrower,
sandbox-token-safe internal API layer for `143-tools` so sandbox tokens do not
need broad browser-session privileges.

All routes are under `/api/v1/internal/session-tabs`. They require the
same short-lived sandbox API token used by `143-tools create_pr` and must resolve
`org_id`, `session_id`, `repository_id`, and current `thread_id` from token
claims, not request body trust.

RBAC:

- Human org roles do not call these routes directly.
- The sandbox token is valid only for its session/repository.
- The org setting `coding_agent_tab_tools_enabled` must be true.
- All reads and writes must filter by `org_id` and `session_id`.

### List Tabs

```http
GET /api/v1/internal/session-tabs
```

Query params:

- `include_archived`: optional boolean, default false

Response:

```json
{
  "data": [
    {
      "id": "uuid",
      "label": "Codex",
      "agent_type": "codex",
      "model_override": "gpt-5.3-codex",
      "status": "running",
      "current_turn": 4,
      "last_activity_at": "2026-06-05T12:00:00Z",
      "pending_message_count": 0,
      "inbox_delivery": {
        "state": "idle",
        "pending_count": 0,
        "recoverable_count": 0
      },
      "cost_cents": 123,
      "result_summary": "Updated the API client and tests.",
      "created_at": "2026-06-05T11:30:00Z"
    }
  ],
  "meta": {}
}
```

Errors:

- `403 TAB_TOOLS_DISABLED`
- `401 INVALID_SANDBOX_TOKEN`
- `500 LIST_TABS_FAILED`

### Get Tab

```http
GET /api/v1/internal/session-tabs/{thread_id}
```

Response:

```json
{
  "data": {
    "thread": { "...": "same shape as list item" },
    "recent_files": [
      {
        "path": "internal/api/handlers/session_threads.go",
        "operation": "modify",
        "last_seen_at": "2026-06-05T12:01:00Z"
      }
    ]
  }
}
```

Errors:

- `404 TAB_NOT_FOUND` when the thread does not belong to the token session
- `403 TAB_TOOLS_DISABLED`

### Create Tab

```http
POST /api/v1/internal/session-tabs
```

Request:

```json
{
  "label": "Review",
  "agent_type": "codex",
  "model": "gpt-5.3-codex",
  "instructions": "Review the current diff for missed tests."
}
```

Fields:

- `label`: optional string, trimmed, backend generates a label when omitted
- `agent_type`: optional typed string; defaults to the source thread's agent
  type or org default when no source thread is available
- `model`: optional string; validated with the same rules as UI thread creation
- `instructions`: optional string stored on the thread, not sent as a message

Response:

```json
{
  "data": {
    "id": "uuid",
    "session_id": "uuid",
    "label": "Review",
    "agent_type": "codex",
    "model_override": "gpt-5.3-codex",
    "status": "idle",
    "created_at": "2026-06-05T12:00:00Z"
  }
}
```

Behavior:

- Creates a blank idle tab in the same session/sandbox.
- Does not start agent execution.
- Enforces the same max tab/running-thread limits as the UI service.
- Records audit event `session.thread.created_by_agent_tool`.

Errors:

- `400 INVALID_AGENT_TYPE`
- `400 INVALID_MODEL`
- `403 TAB_TOOLS_DISABLED`
- `409 TAB_LIMIT_REACHED`
- `500 CREATE_TAB_FAILED`

### Send Message To Tab

```http
POST /api/v1/internal/session-tabs/{thread_id}/messages
```

Request:

```json
{
  "message": "Please run the backend session-thread tests and report failures.",
  "client_message_id": "agent-tool-uuid"
}
```

Response:

```json
{
  "data": {
    "message": {
      "id": "uuid",
      "thread_id": "uuid",
      "role": "user",
      "content": "Please run the backend session-thread tests and report failures.",
      "created_at": "2026-06-05T12:01:00Z"
    },
    "thread": {
      "id": "uuid",
      "status": "pending",
      "pending_message_count": 1
    },
    "delivery_state": "pending"
  }
}
```

Behavior:

- Uses the existing thread `SendMessage` service path.
- Supports idempotency through `client_message_id`.
- Rejects empty messages.
- Does not allow file/image attachment URLs in v1.
- Records audit event `session.thread.messaged_by_agent_tool`.

Errors:

- `400 EMPTY_MESSAGE`
- `404 TAB_NOT_FOUND`
- `409 THREAD_INBOX_BACKPRESSURE`
- `403 TAB_TOOLS_DISABLED`

### Read Tab Messages

```http
GET /api/v1/internal/session-tabs/{thread_id}/messages
```

Query params:

- `position`: optional, `latest` only in v1
- `before`: optional message cursor/turn value, same semantics as public thread
  message window API
- `limit`: optional integer, default 20, max 100
- `include_tool_events`: optional boolean, default false. When false, omit raw
  tool-call/tool-result internals and provider-specific coding-agent event
  noise; return the same human-readable transcript messages the UI shows.

Sort order:

- Return newest messages first by default, ordered by `created_at DESC, id DESC`.
- `meta.next_cursor` points to the next older page.
- This is the best fit for agent coordination because agents usually need the
  sibling tab's latest conclusion or current blocker before older context.

Response:

```json
{
  "data": [
    {
      "id": "uuid",
      "thread_id": "uuid",
      "role": "assistant",
      "turn_number": 3,
      "content": "Tests failed in internal/services/thread/service_test.go.",
      "created_at": "2026-06-05T12:02:00Z"
    }
  ],
  "meta": {
    "next_cursor": "2"
  }
}
```

Redaction:

- Return raw transcript message content as shown in the 143 UI.
- By default, omit tool calls, tool results, hidden prompt internals, credential
  material, raw provider auth metadata, and provider-specific coding-agent event
  details. `include_tool_events=true` may expose sanitized tool-event summaries
  later, but it should remain off by default.
- Apply the same message window limits as the UI to avoid large transcript dumps.

## CLI Interface Proposal

Use explicit session-tab command names in the shared tool registry. They should
be available to sandbox agents through `143-tools` alongside integrations.

### `session_tabs_list`

```bash
143-tools session_tabs_list [--include-archived]
```

Output:

```json
[
  {
    "id": "uuid",
    "label": "Codex",
    "agent_type": "codex",
    "status": "running",
    "current_turn": 4,
    "pending_message_count": 0,
    "last_activity_at": "2026-06-05T12:00:00Z"
  }
]
```

### `session_tabs_get`

```bash
143-tools session_tabs_get --tab-id <uuid>
```

Returns one tab plus recent touched files and delivery state.

### `session_tabs_create`

```bash
143-tools session_tabs_create \
  --label "Review" \
  --agent codex \
  --model gpt-5.3-codex \
  --instructions "Review the current diff for missed tests."
```

Flags:

- `--label`: optional
- `--agent`: optional, one of supported coding-agent enum values
- `--model`: optional
- `--instructions`: optional

Output is the created tab JSON. Creating a tab does not start a run.

### `session_tabs_send`

```bash
143-tools session_tabs_send \
  --tab-id <uuid> \
  --message "Run the backend thread tests and summarize failures."
```

Flags:

- `--tab-id`: required
- `--message`: required unless `--message-file` is supplied
- `--message-file`: optional path inside sandbox, useful for longer prompts
- `--client-message-id`: optional idempotency key; generated by CLI if omitted

Output includes the accepted message ID, target tab status, and delivery state.

### `session_tabs_messages`

```bash
143-tools session_tabs_messages --tab-id <uuid> --limit 30
```

Flags:

- `--tab-id`: required
- `--limit`: optional, default 20, max 100
- `--before`: optional cursor
- `--include-tool-events`: optional, default false

Output preserves the API envelope so cursor pagination is explicit. Messages
are newest-first, and `meta.next_cursor` points to the next older page.

### CLI UX Requirements

- All commands output JSON.
- Errors should be JSON with stable `code` and `message`, matching API error
  codes where possible.
- The CLI should use the injected sandbox API token and should not require the
  agent to pass org/session IDs.
- Help text must state "current session only".
- Commands must avoid printing token claims or secrets in verbose/debug output.

## Security And Policy

### Scope

The sandbox token must bind every command to:

- one `org_id`
- one `session_id`
- one `repository_id`
- optionally one `source_thread_id`

The backend must ignore any client-supplied org/session fields. This keeps the
tool from becoming a session browser.

### Rate Limits

Use the normal thread/session limits rather than introducing a separate
agent-tool quota:

- create uses the existing per-session tab/thread limit
- send uses existing thread inbox backpressure and running-thread admission
- list/get/messages use normal internal API rate limits and the same message
  window caps as the UI

### Audit

Emit audit events with structured details:

- `session.thread.created_by_agent_tool`
- `session.thread.messaged_by_agent_tool`

Details should include:

- `session_id`
- `source_thread_id`
- `target_thread_id`
- `agent_type`
- `model`
- `tool_name`
- `message_length`

Do not include full message bodies, logs, diffs, prompts, or token claims in
audit details.

## Implementation Plan

1. Add `coding_agent_tab_tools_enabled` to org settings parsing, validation,
   defaults, API typing, settings autosave constants if needed, and frontend
   `OrgSettings`.
2. Add the Settings -> Coding Agents switch with tests proving default-on
   rendering and PATCH behavior.
3. Add sandbox-internal API handlers that wrap existing thread service methods
   and enforce sandbox token scope plus the org setting.
4. Register `session_tabs_*` tools in the same `ToolRegistry` used by CLI and
   MCP, but expose them in generated sandbox skills only for coding-agent
   sessions.
5. Add CLI tests for argument parsing, JSON output, error output, and token
   omission.
6. Add backend handler/service tests for org scoping, disabled setting,
   current-session-only access, idempotent send, and create-without-run.
7. Update sandbox prompt/tool documentation so agents understand when to use a
   sibling tab.

## Open Questions

- Should the source tab be included in `session_tabs_list` by default? The
  simplest answer is yes, with an `is_current` field added to each row.
- Should agent-created tabs inherit the source tab's model/reasoning by default
  or the current org/user default? The backend should prefer source tab values
  when available because the source agent is intentionally spawning a peer lane.

# Design: Agent Human Input Requests

> **Status:** Implemented | **Last reviewed:** 2026-05-13

## Problem

Some coding agents pause mid-run and ask the human to choose, approve, or
provide more context. Claude Code can ask explicit user questions and can
route tool approval decisions through a structured callback surface. Other
coding agents expose similar concepts as approval prompts, interrupt events,
proposed commands, or plain clarifying questions.

143 has pieces of this today, but not a complete product path:

- `sessions.status` and `session_threads.status` already include
  `awaiting_input`.
- `session_questions` can persist one text question with simple string
  options.
- `GET /api/v1/sessions/{id}/questions` and
  `POST /api/v1/sessions/{id}/questions/{qid}/answer` exist.
- `Orchestrator.streamLogs` creates a `SessionQuestion` only when an adapter
  emits a log entry with `level = "question"`.
- Sending a follow-up to an `awaiting_input` session marks the latest pending
  question as answered.

The missing parts are the important ones:

- Current Claude, Codex, Gemini, Amp, and Pi adapters do not normalize native
  "ask the user" or approval events into `question` log entries.
- The session detail UI does not query or render pending questions.
- `session_questions` cannot represent tool approvals, grouped choices,
  multi-select choices, rich action previews, request urgency, thread scope,
  or provider-native response payloads.
- Codex currently runs with approval bypass flags, so its native approval flow
  is intentionally disabled inside 143's sandbox path.
- There is no cross-agent contract that says whether answering a prompt should
  resume by direct stdin/live handle, by a provider SDK callback, or by a
  follow-up job.

## Product Goal

When any coding agent needs human input, 143 should surface a clean, durable
request in the session UI:

- an attention state in the session list, tab strip, and open session header
- a dialog or prominent inline card in the transcript when the user is viewing
  the affected session
- clear choice rows for suggested actions, including short descriptions and
  previews when the agent provides them
- a free-form answer field when the request is open-ended
- a reliable resume path after the user answers, even if the worker restarted
  or the browser was closed

This should be provider-neutral. "Claude asked AskUserQuestion", "Codex needs
tool approval", and "Gemini proposed actions" should all become the same
platform concept: a pending session human-input request.

## External Reference Points

Claude Code has two relevant integration surfaces. The direct CLI supports
non-interactive runs with `--print`, structured streaming with
`--output-format stream-json`, permission routing with
`--permission-prompt-tool`, and custom settings through `--settings`. Claude
Code hooks can also return
`permissionDecision: "defer"` for subprocess/custom UI integrations, causing
Claude to pause with a deferred tool payload instead of blocking on native UI.
The Agent SDK's `canUseTool` and `AskUserQuestion` APIs remain useful semantic
references and a fallback if the direct CLI cannot expose enough state.

Conductor's public AskUserQuestion update is a good product reference for the
choice UI: show a short question, present discrete options as readable action
rows, and let the user respond without parsing raw agent JSON.

## Core Abstraction

Introduce `session_human_input_requests` as the durable request model.

Do not stretch `session_questions` into this role. Keep it as compatibility
data until the new surface ships, then either backfill it into the new table
or make it a read-only compatibility view.

Suggested shape:

```sql
CREATE TABLE session_human_input_requests (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id),
    session_id uuid NOT NULL REFERENCES sessions(id),
    thread_id uuid REFERENCES session_threads(id),
    turn_number int NOT NULL DEFAULT 0,
    agent_type text NOT NULL,
    provider_request_id text,
    request_kind text NOT NULL,
    status text NOT NULL DEFAULT 'pending',
    title text NOT NULL,
    body text NOT NULL,
    context text,
    blocks_phase text,
    choices jsonb NOT NULL DEFAULT '[]'::jsonb,
    response_schema jsonb,
    provider_payload jsonb,
    answer_text text,
    answer_payload jsonb,
    answered_by uuid REFERENCES users(id),
    answered_at timestamptz,
    expires_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
```

`request_kind` should be a typed string in `internal/models`, with validation:

- `free_text` for a clarifying question
- `single_choice` for one required selection
- `multi_choice` for multiple selections
- `tool_approval` for allow/deny/edit decisions on a tool or command
- `action_choice` for a provider-suggested list of next actions

`status` should also be typed:

- `pending`
- `answered`
- `cancelled`
- `expired`
- `superseded`

`choices` should contain provider-neutral action rows:

```json
[
  {
    "id": "use-existing-table",
    "label": "Reuse the existing settings table",
    "description": "Fastest path, but preserves current versioning limits.",
    "preview": "Changes migration 000123 and SettingsStore only.",
    "kind": "recommended",
    "destructive": false
  }
]
```

For tool approvals, the same array can represent:

- `approve`
- `deny`
- `approve_once`
- `always_allow_for_session`
- `edit_command`

The raw provider payload is stored for audit/debugging, but the UI should
render only the normalized fields unless an operator expands an advanced
detail block.

## Runtime Contract

Add a provider-neutral runtime event:

```go
type HumanInputRequest struct {
    ProviderRequestID string
    Kind              models.HumanInputRequestKind
    Title             string
    Body              string
    Context           *string
    BlocksPhase       *string
    Choices           []models.HumanInputChoice
    ResponseSchema    json.RawMessage
    ProviderPayload   json.RawMessage
}
```

Adapters should not emit ad hoc `LogEntry{Level: "question"}` directly.
They should emit a structured runtime event or call an orchestrator callback
such as:

```go
RequestHumanInput(
    ctx context.Context,
    req HumanInputRequest,
) (HumanInputAnswer, error)
```

The orchestrator owns persistence and status transitions:

1. Persist the request with `status = pending`.
2. Persist a transcript/timeline entry so the request is visible inline.
3. Set the session and affected thread to `awaiting_input`.
4. Notify connected clients over SSE.
5. Wait for an answer when the provider runtime supports a live callback, or
   return a typed pause error when the provider can only resume later.
6. On answer, mark the request `answered`, audit the response, and resume the
   agent through the best available path.

This contract should sit next to the live command handle and durable inbox
designs:

- If a live runtime handle is still owned by a worker, deliver the answer
  directly to the waiting provider callback or stdin protocol.
- If the live handle is gone, enqueue a `continue_session` job that resumes
  from the saved checkpoint and includes the answered request in the prompt or
  native resume payload.
- If the provider cannot resume a specific paused request, answer submission
  should fail clearly instead of pretending the question was delivered.

## Agent Adapter Mapping

### Claude Code

Preferred path: keep running the `claude` command directly.

143 should continue using `claude --print --output-format stream-json
--verbose` as the execution transport, with `--permission-mode auto` only when
the selected Claude credential tier, CLI version, and model family support it.
Otherwise the adapter falls back to Claude's broadly supported `acceptEdits`
mode. The transport also adds a sandbox-local `--settings` file that installs a
small hook script. The hook should defer Claude tool calls that need 143 UI
involvement:

- `AskUserQuestion` becomes `free_text`, `single_choice`, or `multi_choice`
  depending on the tool input.
- Routine tool execution, including Bash and file edits, is left to Claude
  `auto` mode when supported so common review commands do not become 143
  approval prompts.
- Native permission prompts can use `--permission-prompt-tool` later if an MCP
  permission tool is a better fit than auto mode for reviewable approvals.

When the hook returns `permissionDecision: "defer"`, Claude exits with a
deferred tool payload. The adapter should parse that payload from the
`stream-json` result, persist a normalized human-input request, and mark the
session `awaiting_input`. After the user answers, resume the same Claude
session with `--resume <session_id>` and the same settings; the hook can then
return `allow` with `updatedInput`, or an allow/deny payload for approval
requests.

Use the Agent SDK only if this CLI path cannot provide stable deferred-tool
metadata or deterministic resume behavior. The current parser only handles
system, assistant, user/tool_result, error, and result events, so no Claude
question will be created today.

### Codex

143 currently starts Codex with `--dangerously-bypass-approvals-and-sandbox`,
which suppresses approval prompts. Keep that default for isolated unattended
runs if desired, but define a non-bypass mode for sessions where the operator
wants reviewable tool approvals.

When Codex emits structured approval or interruption events, map them to
`tool_approval` or `action_choice`. If Codex only exposes a text prompt in
the stream, adapters should initially map it to `free_text` with
`provider_payload` attached, then tighten the parser once the event contract
is pinned.

### Gemini, Amp, and Pi

These adapters already share stream-JSON parsing helpers for assistant
output, tool use, tool results, thinking, errors, and usage. Extend the shared
parser with provider-specific human-input event aliases rather than adding
separate UI logic per agent.

## API Contract

Add request endpoints:

- `GET /api/v1/sessions/{id}/human-input-requests?status=pending`
- `POST /api/v1/sessions/{id}/human-input-requests/{request_id}/answer`
- `POST /api/v1/sessions/{id}/human-input-requests/{request_id}/cancel`

Answer body:

```json
{
  "answer_text": "Use the existing settings table.",
  "selected_choice_ids": ["use-existing-table"],
  "answer_payload": {
    "edited_command": null
  }
}
```

The answer endpoint should be transactional:

1. Lock the request row by `org_id`, `session_id`, and `id`.
2. Reject non-pending requests.
3. Validate selected choices against request kind and response schema.
4. Mark answered with `answered_by`, `answered_at`, and answer payload.
5. Enqueue/deliver resume work.
6. Emit audit and SSE events.

SSE should include:

- `session_human_input.created`
- `session_human_input.updated`
- existing `session_status` updates for list/sidebar compatibility

## Frontend UX

Session detail should load pending requests for the selected session/thread
and render two surfaces:

1. **Inline timeline card** at the moment the agent paused.
2. **Dialog** when the request is pending and the viewer is in the affected
   session.

The dialog should use shadcn components only:

- `Dialog`
- `Button`
- `RadioGroup` for single choice
- `Checkbox` rows for multi-choice
- `Textarea` for free-form context or command edits
- `Alert` for destructive or security-sensitive approvals
- `Badge` for provider, thread, and request kind

Choice rows should feel like action options, not raw radio labels:

- left icon for action type or risk
- primary label
- one-line description
- optional muted preview block
- recommended/default marker when provided
- clear destructive styling only for real destructive actions

The shared composer should remain available for ordinary follow-up messages,
but when a pending request exists it should show context-aware placeholder
text such as `Answer Claude's question...` and submitting through the composer
should answer the latest pending request only if the request accepts free
text.

For request types with fixed options, prefer the structured dialog/card
controls over forcing the user to type an answer manually.

## Notifications and Attention

Pending requests should be treated as "needs you":

- session list row uses the existing amber `awaiting_input` status treatment
- tab strip shows the affected thread as needing attention
- document title/browser notification can use the existing notification helper
- open session auto-opens the dialog once per request per viewer, with a
  stable dismissed-in-this-tab state so the dialog does not reopen after every
  refetch

## Implemented Migration

1. **Compatibility UI for existing questions.**
   Legacy `question` logs are promoted into durable
   `session_human_input_requests` rows while still creating compatibility
   `session_questions` rows for older integrations.

2. **Generalized request model.**
   Create typed models, migration, store, handlers, tests, tenancy coverage,
   audit events, and SSE events for `session_human_input_requests`.

3. **Orchestrator contract.**
   Replace direct `level = "question"` detection with a structured
   human-input callback. Keep `question` log support as a compatibility
   adapter that creates `free_text` requests.

4. **Claude CLI integration.**
   Generate Claude settings and a sandbox hook script, then add parser tests
   using real `stream-json` fixtures for `AskUserQuestion` deferral, single
   choice, multi-choice, resume-with-answer, and tool approval. Revisit the
   Agent SDK only if the direct CLI cannot provide stable deferred-tool
   metadata or deterministic resume behavior.

5. **Codex and shared stream parsers.**
   Add parser tests using real captured Codex/Gemini/Amp/Pi events. Avoid
   removing Codex's bypass mode until there is a deliberate product setting
   for reviewable approvals.

6. **Action-choice UI.**
   Add option-row rendering, previews, keyboard handling, mobile dialog
   layout, and tests for answering, cancelling, and stale/superseded requests.

## Implementation Notes

Implemented on 2026-05-12. Full once-over fixes completed on 2026-05-13.

- `session_human_input_requests` is the durable request model for
  provider-neutral free-text, choice, action-choice, and tool-approval
  prompts.
- Agent adapters emit structured `HumanInputRequest` events on `LogEntry`
  instead of relying on ad hoc `question` logs. Legacy `question` logs are
  normalized into durable `free_text` requests and still create compatibility
  `session_questions`.
- Claude Code continues to run through the direct `claude` command. 143 selects
  `auto` permission mode only for compatible auth, CLI-version, and model-family
  combinations, otherwise uses `acceptEdits`; either way it writes a
  sandbox-local Claude settings file plus hook script, defers `AskUserQuestion`,
  parses `deferred_tool_use`, checkpoints the session, and resumes the same
  Claude session with the user's
  normalized answer.
- Codex and the shared stream parser now recognize generic human-input,
  approval, and action-choice events, while Codex's existing bypass mode
  remains unchanged for unattended isolated runs.
- The session API exposes list, answer, and cancel endpoints backed by a
  shared human-input service. Answer submission is transactional: it locks the
  request, validates the answer and optional `response_schema`, stores the
  answer payload, closes the compatibility `session_questions` row when one
  exists, creates the user transcript message, and enqueues
  `continue_session` with the answered request id. Thread-scoped answers claim
  the affected thread before enqueueing resume work so thread status and turn
  numbering stay consistent.
- Cancelling a pending request is transactional and sends a provider-visible
  denial payload through the same `continue_session` resume path. Compatible
  legacy `session_questions` rows are marked skipped so older integrations do
  not keep showing stale pending prompts.
- Thread composer replies to an `awaiting_input` tab also atomically answer
  the latest pending thread-scoped `free_text` request, attach the request id
  to the `continue_session` job, and emit the same answered audit event as the
  dialog path.
- SSE now emits both the existing transcript/status events and explicit
  `session_human_input.created` / `session_human_input.updated` events. Answer
  and cancel operations add update log entries so connected session views can
  invalidate pending-request and timeline queries immediately.
- Server-owned session timelines include durable human-input request entries
  for all statuses, so answered and cancelled decisions remain inspectable
  after the pending dialog disappears.
- Session detail queries pending requests, renders inline action cards, opens
  a dialog for pending prompts, and supports free-text, single-choice,
  multi-choice, action-choice, approval-style request rows, and structured
  decision payloads.
- The open session auto-opens one pending dialog per request per tab, tracks
  dismissal locally, and avoids reopening the dialog after every refetch.

## Implemented Decisions

- Human input is a durable pause, not a transient browser prompt. Runs move
  to `awaiting_input` and remain resumable even if the worker or browser goes
  away.
- The primary Claude integration remains the direct `claude` CLI. The Agent
  SDK is not required for this implementation.
- Claude uses hook-based deferral for explicit user questions, while routine
  tool permissions are handled by Claude auto mode when the selected
  auth, CLI-version, and model-family combination supports it.
  `--permission-prompt-tool` remains an optional future transport for
  permission prompts, not a dependency of the current path.
- Resume uses a durable `continue_session` job with the answered request id.
  Live callback delivery can be added later without changing the persistence
  or API contract.
- Tool-approval rows remain supported by the provider-neutral API, but Claude
  Code's default sandbox path no longer converts routine Bash/edit permissions
  into 143 approval cards. Org-level allow/deny policy can layer on top later.
- Selected choices are stored in answer metadata and reflected through a user
  transcript message when the answer resumes the run.
- Pending requests do not expire automatically. The schema includes
  `expires_at`, but current product behavior is explicit answer or cancel.

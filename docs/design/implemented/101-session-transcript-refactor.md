# Design: Session Transcript Window API

> **Status:** Implemented | **Last reviewed:** 2026-06-16

## Summary

Long coding-agent sessions need bounded, durable transcript loading without
changing the existing session detail experience. This refactor moves thread
history loading from client-assembled message/log windowing to a backend-defined
**transcript window** API, while preserving the current `ChatTimeline` rendering
and interaction model.

The backend returns one coherent, turn-grouped slice of a thread containing
messages, compact tool/log entries, and human-input events. The frontend fetches
those windows through TanStack Query, flattens the returned entries back into
the existing timeline arrays, and continues to render the same transcript UI.

## Implementation Status

Implemented:

- `GET /api/v1/sessions/{session_id}/threads/{thread_id}/transcript`.
- Typed transcript window models, cursor helpers, and validation tests in
  `internal/models`.
- `SessionTranscriptStore.ListThreadWindow`, backed by org-scoped queries over
  messages, logs, and human-input requests.
- Thread service and handler wiring with session/thread/org validation.
- Frontend API client types and tests for transcript-window query parameters.
- Session detail integration through one thread transcript infinite query,
  adapted by `flattenTranscriptWindows` into the existing `buildTimeline` and
  `ChatTimeline` path.
- Bounded message/log content in list responses, with truncation metadata.

Intentional product decision:

- No new transcript visual design shipped with this refactor.
- No virtualized turn renderer shipped with this refactor.
- Existing message/log endpoints remain for compatibility and direct inspection.

## Goals

- Let the backend own transcript ordering, grouping, cursoring, and org-scoped
  validation for thread transcript reads.
- Reduce session detail request fanout by replacing separate thread message/log
  assembly with one transcript-window source.
- Keep transcript payloads bounded by turn count and content size.
- Preserve the existing session transcript UI, scroll behavior, message
  rendering, and timeline components.
- Keep legacy sessions and internal tools inspectable through existing APIs.

## Non-Goals

- Replacing `ChatTimeline` with a new transcript renderer.
- Adding transcript virtualization.
- Changing the visual treatment of messages, logs, tool rows, human-input
  requests, loading rows, or jump controls.
- Adding transcript search, checkpoints, or generated summaries.
- Removing existing message and log endpoints.

## Current Shape

Thread views now use the transcript-window API as their durable source for
loaded history. The frontend still keeps the established transcript UI:

- `useInfiniteQuery` loads transcript windows for the active thread.
- `flattenTranscriptWindows` converts turn-grouped transcript entries into the
  message, log, and human-input arrays expected by the existing timeline code.
- `buildTimeline`, `sortTimelineEntries`, and `ChatTimeline` remain the rendered
  surface.
- Live SSE log/message buffers are still merged on top of persisted windows for
  active sessions.
- Older and newer history use transcript cursors, with the existing scroll
  compensation path preserving the visible area when older content is prepended.

The older thread message and log APIs remain available for compatibility,
internal tools, and direct log inspection.

## Transcript Window API

```http
GET /api/v1/sessions/{session_id}/threads/{thread_id}/transcript
```

Supported query parameters:

| Parameter | Meaning |
| --- | --- |
| `position` | `latest` or `around`. Default `latest`. `older` and `newer` are inferred from cursors. |
| `before` | Opaque cursor for the page older than the loaded window. |
| `after` | Opaque cursor for the page newer than the loaded window. |
| `anchor_entry_id` | Stable transcript entry id for `around`. |
| `anchor_message_id` | Compatibility anchor for restored message positions. |
| `anchor_turn_number` | Optional turn anchor when no specific entry exists. |
| `limit_turns` | Target number of turns. Default 20, max 80. |
| `include` | Optional comma list: `messages,tools,human_inputs,system`. Default all renderable entries. |

Cursor rules:

- `before` and `after` cannot be combined.
- `position=around` requires one anchor.
- Cursors are opaque base64 JSON with version, org id, thread id, turn number,
  and entry id.
- Sort direction is not stored in the cursor; it is inferred from whether the
  cursor is passed as `before` or `after`.
- Cursors are scoped to the same org and thread on decode.
- The API returns chronological entries inside the window regardless of which
  direction the cursor came from.
- When an `around` anchor is provided but not found, the server falls back to
  `latest` and sets `anchor_found: false`.

Response shape:

```json
{
  "data": [
    {
      "turn_number": 42,
      "started_at": "2026-06-14T12:00:00Z",
      "ended_at": "2026-06-14T12:03:12Z",
      "entries": [
        {
          "id": "msg_123",
          "kind": "message",
          "message_id": 123,
          "role": "user",
          "content": "Run the focused frontend tests.",
          "created_at": "2026-06-14T12:00:00Z"
        },
        {
          "id": "tuse_991",
          "kind": "tool_use",
          "log_id": 991,
          "tool_name": "exec_command",
          "summary": "npm run test -- session-detail",
          "created_at": "2026-06-14T12:00:18Z"
        }
      ]
    }
  ],
  "meta": {
    "position": "latest",
    "has_older": true,
    "next_older_cursor": "opaque",
    "has_newer": false,
    "anchor_found": false,
    "latest_assistant_entry_id": "msg_124",
    "latest_assistant_message_id": 124,
    "live_edge_entry_id": "msg_124",
    "live_edge_message_id": 124,
    "thread_status": "idle"
  }
}
```

## Entry IDs

Entry IDs are stable, opaque strings constructed on the server as
`{kind_prefix}_{source_id}`.

| Kind | Prefix | Example |
| --- | --- | --- |
| `message` | `msg` | `msg_123` |
| `tool_use` | `tuse` | `tuse_991` |
| `tool_result` | `tres` | `tres_991` |
| `log` | `log` | `log_991` |
| `human_input` | `hiq` | `hiq_f3a...` |
| `milestone` | `ms` | `ms_42` |
| `checkpoint` | `cp` | `cp_7` |

`tool_use` and `tool_result` entries that share the same `log_id` get distinct
prefixes so they never collide. Clients compare entry IDs for equality and use
them as cursor anchors; clients must not parse them for business logic.

## Backend Model

The implemented response models live in `internal/models/session_transcript.go`:

- `TranscriptWindowPosition`
- `TranscriptEntryKind`
- `TranscriptCursor`
- `SessionTranscriptEntry`
- `SessionTranscriptTurn`
- `SessionTranscriptWindowMeta`
- `SessionTranscriptWindowResponse`

The wire model includes nested `message`, `log`, and `human_input` objects so
the existing frontend timeline can keep rendering through the current component
path without a UI migration.

## Service Layer

The thread service exposes transcript loading through:

```go
GetTranscriptWindow(
    ctx context.Context,
    orgID uuid.UUID,
    sessionID uuid.UUID,
    threadID uuid.UUID,
    opts db.SessionTranscriptWindowOptions,
) (thread.TranscriptWindowResult, error)
```

Responsibilities:

- validate the thread belongs to the session and org,
- normalize options and limits,
- delegate raw event window loading to the transcript store,
- return the window with thread status for response metadata.

Handlers remain thin: parse query parameters, decode cursors, call the service,
group raw rows into response turns, and write JSON.

## Store Layer

`SessionTranscriptStore.ListThreadWindow` returns raw transcript rows under org
scope:

```go
func (s *SessionTranscriptStore) ListThreadWindow(
    ctx context.Context,
    orgID uuid.UUID,
    threadID uuid.UUID,
    opts SessionTranscriptWindowOptions,
) (SessionTranscriptWindow, error)
```

The store reads from existing tables:

- `session_messages`
- `session_logs`
- `session_human_input_requests`

Query rules:

1. Select candidate turns for the requested position.
2. Fetch all renderable entries for those turns.
3. Order by `turn_number`, `created_at`, explicit source precedence, and source
   id.
4. Fetch enough boundary information to compute `has_older`, `has_newer`, and
   next cursors.
5. Filter every branch by `org_id` and `thread_id`.

The refactor added transcript-specific indexes for message, log, and
human-input turn lookups.

## Frontend Integration

The frontend intentionally keeps the existing session transcript UI.

Implemented pieces:

- `api.sessions.getThreadTranscriptWindow(sessionId, threadId, params)`.
- `SessionTranscriptWindowResponse` and related TypeScript types.
- `flattenTranscriptWindows`, which adapts transcript turns back into the
  arrays consumed by `buildTimeline`.
- A single active-thread transcript infinite query in
  `session-detail-content.tsx`.
- Cursor-backed older/newer loading while preserving the existing scroll
  compensation behavior.

The old client-side assembly code path for session-level timelines remains in
place for views that are not thread-scoped.

## Oversized Content Policy

Transcript windows are bounded by both turn count and content size.

Server-side policy:

- cap raw message and log content in list responses,
- include byte, character, and truncation metadata,
- keep full log detail available through existing log/detail endpoints,
- summarize tool-use/tool-result rows for compact list rendering.

The transcript API must not return unbounded command output just because that
output falls inside a loaded turn.

## Tests

Backend coverage includes:

- enum validation tests for transcript position and entry kind,
- cursor encode/decode and scope validation tests,
- store tests for latest, older, newer, around, missing-anchor fallback, and
  mixed message/log/human-input ordering,
- service tests for thread/session validation,
- handler tests for query validation and response shape,
- tenancy coverage for org-scoped transcript queries.

Frontend coverage includes:

- API client tests for query parameter construction,
- `flattenTranscriptWindows` tests,
- session detail tests that exercise transcript-window loading through the
  existing timeline path.

Verification for frontend changes remains:

```bash
cd frontend
npm run typecheck
npm run lint
npm run build
```

Verification for Go changes remains:

```bash
go vet ./...
go build ./...
go test ./...
```

## Follow-Up Work

- Transcript search over messages and log summaries.
- Synthetic primary-thread transcript support for older session shapes if a
  future migration needs it.
- Generated checkpoints or summaries for very old turns.

## Decision

Thread transcript loading is now a backend-defined, turn-grouped,
cursor-paginated API. The product keeps the existing session transcript UI and
uses the new API as a more coherent data source underneath it.

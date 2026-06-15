# Design: Session Transcript Refactor

> **Status:** Implemented | **Last reviewed:** 2026-06-15

## Summary

Long coding-agent sessions should feel like durable workspaces that can be
resumed, inspected, and searched without forcing the user to wait for or scroll
through the whole transcript.

The session detail transcript should move from client-assembled message/log
windowing to a backend-defined **transcript window** API plus virtualized,
anchor-stable rendering. The backend returns one coherent renderable slice of a
thread, grouped by turn and containing messages, compact tool-call summaries
(name + one-line summary only — not raw input/output), and human-input events.
The frontend renders only the visible range, preserves the user's visual anchor
while older or newer content loads, and treats old history as
searchable/jumpable context rather than an endlessly growing DOM.

This replaces the earlier message-window/log-window design. That design bounded
the initial payload, but it still required the browser to assemble the timeline
from separate messages, logs, live logs, and human-input requests, and loaded
pages could continue accumulating in React and the DOM.

## Product Goals

### What should feel different

Opening a long session should be calm:

1. Session chrome appears immediately from cached/detail metadata.
2. The transcript pane holds a stable, bottom-weighted skeleton while the first
   transcript window loads.
3. The first real content appears at a useful continuation point without a
   visible jump.
4. Older history loads above the viewport only when the user asks for it by
   scrolling or using navigation.
5. Loading older content keeps the same message under the user's eyes.
6. Active sessions follow the live edge only while the user is already near the
   bottom.
7. Search and jump targets become the primary way to navigate long history.

The user should not experience:

- the page growing upward after first paint,
- the transcript jumping as additional windows arrive,
- seconds of blank space while the UI stitches several independent requests,
- a full re-render of thousands of historical rows,
- old tool logs overwhelming the latest useful answer.

### Default open behavior

The transcript should resolve intent in this order:

1. Restore the user's saved message/turn anchor when available.
2. For active/running threads, open at the live edge.
3. For idle/completed threads, open at the start of the latest assistant turn.
4. If no assistant turn exists, fall back to the live edge.

Saved positions are message/turn anchors plus an offset within that rendered
entry, not raw `scrollTop`. Raw scroll offsets are only a short-lived fallback
for legacy local storage entries.

### Look and feel

The transcript pane remains the central surface. It should not become a noisy
log viewer by default.

- User and assistant messages keep the current conversation rhythm.
- Tool activity renders as compact, scannable rows inside the turn they belong
  to, collapsed by default when the output is long.
- Human-input requests render inline at the point they were created, and pending
  requests may also surface in the existing active guidance UI.
- Old loaded boundaries use small inline loading rows: `Loading older...` and
  `Loading newer...`. When both are visible simultaneously — i.e., an `around`
  anchor was restored in the middle of history — the user can scroll in either
  direction to page. As one direction reaches its terminal boundary (`has_older`
  or `has_newer` becomes false), that loading row disappears and is replaced by
  the first or last entry in the thread. No jump or reflow should occur at that
  transition.
- A floating `Jump to latest` affordance appears only when the user is away from
  the live edge.
- A secondary `Latest response` affordance may appear in keyboard/command
  navigation and can scroll to the latest assistant turn boundary.
- Very old turns may later collapse into generated checkpoints, but raw
  transcript expansion remains available.

The first skeleton should reserve final transcript geometry:

- same transcript gutters,
- same max message width,
- same composer clearance,
- bottom-aligned placeholder cluster for latest/live opens,
- centered placeholder cluster only for explicit `around` anchors when the
  saved anchor is being restored and the anchor turn is not expected to fall
  within the bottom quarter of the visible range. For short transcripts or
  anchors near the end of history, fall back to bottom-alignment — centering
  content that fits on screen without scrolling looks broken.

### Navigation model

Long transcripts should be navigated by intent, not only by chronology.

Minimum navigation:

- jump to latest,
- jump to latest assistant response,
- restore last read position,
- load older on upward scroll,
- load newer on downward scroll from an anchored window.

Follow-up navigation:

- transcript search,
- jump to user prompts,
- jump to errors,
- jump to tool failures,
- jump to files changed,
- jump to PR/branch/preview milestones.

## Current State

Current thread views use:

- `GET /api/v1/sessions/{id}/threads/{tid}/messages` with latest/around/older/newer
  message windows.
- `GET /api/v1/sessions/{id}/threads/{tid}/logs` with latest-turn or explicit
  turn filters.
- `GET /api/v1/sessions/{id}/human-input-requests` for human-input state.
- client-side merging through `buildTimeline`, `mergeVisibleThreadLogs`, and
  related helpers in `session-detail-content.tsx`.
- manual prepend scroll compensation using the previous and new
  `scrollHeight`.

That design is a good stepping stone, but the frontend is still responsible for
ordering, dedupe, log visibility, live-log handoff, human-input placement, and
scroll anchoring. It also keeps all loaded pages mounted unless the user leaves
the route.

## Proposed Architecture

### Transcript window

Introduce a new thread-scoped transcript endpoint:

```http
GET /api/v1/sessions/{session_id}/threads/{thread_id}/transcript
```

Supported query parameters:

| Parameter | Meaning |
| --- | --- |
| `position` | `latest`, `older`, `newer`, or `around`. Default `latest`. |
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
- Cursors are opaque base64 JSON with version, thread id, turn number, and
  entry id. Sort direction is never stored in the cursor; it is inferred from
  whether the cursor appears in `before` or `after`. Embedding direction in the
  cursor creates a class of bugs where a cursor from one direction is silently
  misapplied in the other.
- Cursors must be scoped to the same org/session/thread on decode.
- The API returns chronological entries inside the window regardless of which
  direction the cursor came from.
- `position=around` requires exactly one of `anchor_entry_id`,
  `anchor_message_id`, or `anchor_turn_number`. When multiple are provided,
  precedence is `anchor_entry_id` > `anchor_message_id` > `anchor_turn_number`.
  When none are provided with `position=around`, the server returns HTTP 400.
  When an anchor is provided but not found, the server falls back to `latest`
  and sets `anchor_found: false` in the response meta.

Example:

```http
GET /api/v1/sessions/sess/threads/thread/transcript?position=latest&limit_turns=20
```

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
          "id": "log_991",
          "kind": "tool_use",
          "log_id": 991,
          "tool_name": "exec_command",
          "summary": "npm run test -- session-detail",
          "created_at": "2026-06-14T12:00:18Z"
        },
        {
          "id": "msg_124",
          "kind": "message",
          "message_id": 124,
          "role": "assistant",
          "content": "The focused tests pass.",
          "created_at": "2026-06-14T12:03:12Z"
        }
      ]
    }
  ],
  "meta": {
    "position": "latest",
    "has_older": true,
    "next_older_cursor": "eyJ2IjoxLCJ0dXJuIjo0MiwiZW50cnlfaWQiOiJtc2dfMTI0In0=",
    "has_newer": false,
    "next_newer_cursor": "",
    "anchor_entry_id": "",
    "anchor_found": false,
    "latest_assistant_entry_id": "msg_124",
    "latest_assistant_message_id": 124,
    "live_edge_entry_id": "msg_124",
    "live_edge_message_id": 124,
    "thread_status": "idle"
  }
}
```

#### Entry ID format

Entry IDs are stable, opaque strings constructed on the server as
`{kind_prefix}_{source_id}`. The prefix table:

| Kind | Prefix | Example |
| --- | --- | --- |
| `message` | `msg` | `msg_123` |
| `tool_use` | `tuse` | `tuse_991` |
| `tool_result` | `tres` | `tres_991` |
| `log` | `log` | `log_991` |
| `human_input` | `hiq` | `hiq_f3a...` |
| `milestone` | `ms` | `ms_42` |
| `checkpoint` | `cp` | `cp_7` |

`tool_use` and `tool_result` that share the same `log_id` get distinct prefixes
so they never collide. The ID is stable across pages: the same log row always
produces the same `tuse_991` or `tres_991` entry id. Clients must not parse or
construct entry IDs — they are only compared for equality and used as cursor
anchors.

#### Meta field definitions

`latest_assistant_entry_id` / `latest_assistant_message_id` — the most recent
assistant *message* entry in the returned window, regardless of thread status.
For idle sessions this is also the last content the user saw. For running
sessions it may be one or more turns behind the live edge.

`live_edge_entry_id` / `live_edge_message_id` — the most recently persisted
entry in the thread at the time the window was computed. For idle sessions this
equals `latest_assistant_*`. For running sessions the live edge advances as new
messages and logs are persisted, and may be a non-message entry (e.g., a
`tool_use` log). The frontend uses the live edge to decide whether to auto-scroll
and whether to show `Jump to latest`.

### Go models

Add typed response models in `internal/models`.

```go
type TranscriptWindowPosition string

const (
	TranscriptWindowPositionLatest TranscriptWindowPosition = "latest"
	TranscriptWindowPositionOlder  TranscriptWindowPosition = "older"
	TranscriptWindowPositionNewer  TranscriptWindowPosition = "newer"
	TranscriptWindowPositionAround TranscriptWindowPosition = "around"
)

func (p TranscriptWindowPosition) Validate() error { /* table-driven tests */ }

type TranscriptEntryKind string

const (
	TranscriptEntryKindMessage     TranscriptEntryKind = "message"
	TranscriptEntryKindToolUse     TranscriptEntryKind = "tool_use"
	TranscriptEntryKindToolResult  TranscriptEntryKind = "tool_result"
	TranscriptEntryKindLog         TranscriptEntryKind = "log"
	TranscriptEntryKindHumanInput  TranscriptEntryKind = "human_input"
	TranscriptEntryKindMilestone   TranscriptEntryKind = "milestone"
	TranscriptEntryKindCheckpoint  TranscriptEntryKind = "checkpoint"
)

func (k TranscriptEntryKind) Validate() error { /* table-driven tests */ }

type ThreadStatus string

const (
	ThreadStatusIdle    ThreadStatus = "idle"
	ThreadStatusRunning ThreadStatus = "running"
	ThreadStatusFailed  ThreadStatus = "failed"
)

// ThreadStatus drives default-open scroll behavior: running → live edge,
// idle/failed → latest assistant turn.

type SessionTranscriptWindowResponse struct {
	Data []SessionTranscriptTurn `json:"data"`
	Meta SessionTranscriptWindowMeta `json:"meta"`
}

type SessionTranscriptTurn struct {
	TurnNumber int `json:"turn_number"`
	StartedAt  time.Time `json:"started_at"`
	EndedAt    *time.Time `json:"ended_at,omitempty"`
	Entries    []SessionTranscriptEntry `json:"entries"`
}

type SessionTranscriptEntry struct {
	ID        string `json:"id"`
	Kind      TranscriptEntryKind `json:"kind"`
	CreatedAt time.Time `json:"created_at"`

	MessageID *int64 `json:"message_id,omitempty"`
	LogID     *int64 `json:"log_id,omitempty"`
	RequestID *uuid.UUID `json:"request_id,omitempty"`

	Role      MessageRole `json:"role,omitempty"`
	Level     SessionLogLevel `json:"level,omitempty"`
	Content        string `json:"content,omitempty"`
	ContentTruncated bool `json:"content_truncated,omitempty"`
	ContentBytes   int  `json:"content_bytes,omitempty"`
	ContentChars   int  `json:"content_chars,omitempty"`
	Summary   string `json:"summary,omitempty"`
	ToolName  string `json:"tool_name,omitempty"`
	Collapsed bool `json:"collapsed,omitempty"`

	Message    *SessionMessage `json:"message,omitempty"`
	Log        *SessionLog `json:"log,omitempty"`
	HumanInput *HumanInputRequest `json:"human_input,omitempty"`
}

type SessionTranscriptWindowMeta struct {
	Position TranscriptWindowPosition `json:"position"`

	HasOlder        bool   `json:"has_older"`
	NextOlderCursor string `json:"next_older_cursor,omitempty"`
	HasNewer        bool   `json:"has_newer"`
	NextNewerCursor string `json:"next_newer_cursor,omitempty"`

	AnchorEntryID   string `json:"anchor_entry_id,omitempty"`
	AnchorFound     bool   `json:"anchor_found"`
	LatestAssistantEntryID string `json:"latest_assistant_entry_id,omitempty"`
	LatestAssistantMessageID int64 `json:"latest_assistant_message_id,omitempty"`
	LiveEdgeEntryID string `json:"live_edge_entry_id,omitempty"`
	LiveEdgeMessageID int64 `json:"live_edge_message_id,omitempty"`

	ThreadStatus ThreadStatus `json:"thread_status"`
}
```

The wire model may include nested `message`, `log`, and `human_input` objects
initially to reduce migration risk. Once the frontend is fully migrated, the
entry fields can become the primary stable contract and nested raw objects can
be phased down.

### Service layer

Add a transcript service under `internal/services/thread` or a dedicated
`internal/services/transcript` package if the logic grows beyond thread
ownership.

```go
type TranscriptService interface {
	GetThreadWindow(
		ctx context.Context,
		orgID uuid.UUID,
		sessionID uuid.UUID,
		threadID uuid.UUID,
		opts db.SessionTranscriptWindowOptions,
	) (models.SessionTranscriptWindowResponse, error)
}
```

Responsibilities:

- validate the thread belongs to the session and org,
- normalize options and limits,
- decode/validate cursors,
- request the raw event window from the store,
- group events by turn,
- classify logs into renderable entry kinds,
- collapse or summarize oversized log payloads,
- compute anchors and cursors,
- keep legacy transcript sessions inspectable.

Handlers should remain thin: parse request, call service, write JSON.

### Store layer

Add a store method that returns raw transcript rows under org scope:

```go
// TranscriptCursor is the decoded form of the opaque base64 cursor string.
// It carries only position information; direction is never stored in the cursor.
type TranscriptCursor struct {
	Version    int       `json:"v"`
	OrgID      uuid.UUID `json:"org_id"`
	ThreadID   uuid.UUID `json:"thread_id"`
	TurnNumber int       `json:"turn"`
	EntryID    string    `json:"entry_id"`
}

// Encode returns the wire-safe opaque string form.
func (c TranscriptCursor) Encode() (string, error) { /* base64(json(c)) */ }

// DecodeCursor decodes and validates scope. Returns error if version is
// unknown or if org/thread scope does not match the calling context.
func DecodeCursor(raw string, orgID, threadID uuid.UUID) (TranscriptCursor, error) { /* ... */ }

type SessionTranscriptWindowOptions struct {
	Position          models.TranscriptWindowPosition
	Before           *TranscriptCursor
	After            *TranscriptCursor
	AnchorEntryID    string
	AnchorMessageID  int64
	AnchorTurnNumber *int
	LimitTurns       int
	Include          TranscriptInclude
}

type SessionTranscriptRawRow struct {
	EntryID    string
	EntryKind  models.TranscriptEntryKind
	TurnNumber int
	CreatedAt  time.Time
	SortID     int64

	Message *models.SessionMessage
	Log     *models.SessionLog
	HumanInput *models.HumanInputRequest
}

// SessionTranscriptWindow is the raw store result before service-layer grouping.
type SessionTranscriptWindow struct {
	Rows     []SessionTranscriptRawRow
	HasOlder bool
	HasNewer bool
	// OlderCursor and NewerCursor are pre-encoded opaque strings ready to
	// return in the API response. Empty when the corresponding Has* is false.
	OlderCursor string
	NewerCursor string
}

func (s *SessionTranscriptStore) ListThreadWindow(
	ctx context.Context,
	orgID uuid.UUID,
	threadID uuid.UUID,
	opts SessionTranscriptWindowOptions,
) (SessionTranscriptWindow, error)
```

Implementation can begin as a SQL `UNION ALL` over existing tables:

- `session_messages`,
- `session_logs`,
- `session_human_input_requests`,
- legacy `session_questions` only where compatibility requires it,
- optional synthetic milestone rows from session/thread state.

The query must filter every branch by `org_id` and `thread_id`. It should page
by turn first, then entry sort inside those turns. This keeps windows
meaningful: loading 20 turns should not accidentally mean "20 individual log
rows from one command."

Recommended query shape:

1. Select candidate turns for the requested position.
2. Fetch all renderable entries for those turns.
3. Order by `turn_number`, `created_at`, explicit source precedence, source id.
   Source precedence ascending: `session_messages` → 1, `session_logs` → 2,
   `session_human_input_requests` → 3. Add a constant `source_rank` column to
   each UNION branch so same-millisecond entries within a turn have a
   deterministic, stable order.
4. Fetch one extra older/newer turn to compute `has_older`/`has_newer`.

Indexes to verify or add:

```sql
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_session_messages_thread_turn_id
  ON session_messages (org_id, thread_id, turn_number, id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_session_logs_thread_turn_id
  ON session_logs (org_id, thread_id, turn_number, id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_session_human_input_requests_thread_turn_created
  ON session_human_input_requests (org_id, session_id, thread_id, turn_number, created_at, id);
```

`session_human_input_requests` currently scopes by both `session_id` and
`thread_id`, which is why its index includes `session_id` while the message and
log indexes do not. The extra column is intentional — the table's existing
queries filter by session — and must be preserved in the UNION ALL branch to
maintain index coverage.

If the refactor needs exact inline turn placement, add nullable `turn_number int`
to the table and backfill best-effort from the creation-time-adjacent transcript
turn. If exact placement is deferred, the service layer can approximate placement
by mapping each request to the nearest known turn by `created_at`. This
approximation requires fetching the thread's turn boundary timestamps as a
separate query before grouping; it must be documented as an explicit second query
in the service, not treated as a free side effect of the UNION ALL. Legacy
`session_questions` should remain readable until the old question flow is fully
retired.

### Frontend API client

Add:

```ts
export interface SessionTranscriptWindowResponse {
  data: SessionTranscriptTurn[];
  meta: SessionTranscriptWindowMeta;
}

export interface SessionTranscriptTurn {
  turn_number: number;
  started_at: string;
  ended_at?: string;
  entries: SessionTranscriptEntry[];
}

export type SessionTranscriptEntryKind =
  | "message"
  | "tool_use"
  | "tool_result"
  | "log"
  | "human_input"
  | "milestone"
  | "checkpoint";

export interface SessionTranscriptEntry {
  id: string;
  kind: SessionTranscriptEntryKind;
  created_at: string;
  message_id?: number;
  log_id?: number;
  request_id?: string;
  role?: MessageRole;
  level?: SessionLogLevel;
  content?: string;
  content_truncated?: boolean;
  content_bytes?: number;
  content_chars?: number;
  summary?: string;
  tool_name?: string;
  collapsed?: boolean;
  message?: SessionMessage;
  log?: SessionLog;
  human_input?: HumanInputRequest;
}
```

Add client method:

```ts
api.sessions.getThreadTranscriptWindow(sessionId, threadId, params)
```

The existing message/log APIs remain during migration and for internal tools,
but the session detail transcript should move to the transcript-window API.

### Frontend rendering

Replace the current thread transcript assembly in `ChatPanel` with:

- one `useInfiniteQuery` for transcript windows,
- optional live-overlay query/buffer for SSE events not yet persisted,
- a virtualized transcript list,
- anchor-based position persistence.

Use a variable-height virtualizer such as TanStack Virtual. The virtualizer
should measure final rendered row heights and preserve anchors on prepend.

The unit of virtualization should be a **turn block** first, with entries inside
the turn. This makes paging, keyboard movement, and collapse state easier than
virtualizing every tiny log row independently. If a single turn becomes huge,
that turn can internally virtualize or collapse long tool outputs.

Frontend state should track:

```ts
type TranscriptAnchor = {
  entryId: string;
  turnNumber: number;
  // No pixel offset — entry boundaries are stable; intra-entry px offsets break
  // on viewport resize, zoom, and content reflow. Anchoring to entry start is
  // sufficient and durable.
};

type LoadedTranscriptRange = {
  oldestCursor?: string;
  newestCursor?: string;
  hasOlder: boolean;
  hasNewer: boolean;
};
```

Persisted reading position:

```ts
type StoredSessionTranscriptPosition = {
  version: 3;
  sessionId: string;
  threadId: string;
  viewerScope: SessionScrollViewerScope;
  anchor: TranscriptAnchor;
  savedAt: string;
};
```

On read, if a stored position has `version` < 3, discard it and open at the
default position (do not attempt to reinterpret legacy raw scroll offsets as
entry anchors). This simplifies migration and avoids subtly wrong restores.
Stored version 3 positions should be cleared when the user explicitly navigates
away from a thread (not just on tab close).

The frontend should not save skeleton positions or raw scroll offsets after the
new transcript system is enabled.

### Accessibility

Virtualizing the transcript removes rows from the DOM, which breaks screen reader
navigation if not handled explicitly.

- Each turn block should render with `role="group"` and an `aria-label` such as
  `"Turn 42"` so assistive technologies can describe the structure.
- New live-edge content appended to the overlay must announce via
  `aria-live="polite"` so screen readers surface activity without interrupting.
  Use `aria-live="off"` when the user is reading older history to prevent
  interruptions from background activity.
- Keyboard navigation must allow moving turn-by-turn (e.g. `j`/`k` or arrow
  keys) without requiring visible scroll. The virtualizer must scroll the focused
  turn into view and ensure it is in the DOM before focus is placed on it.
- Collapsed tool outputs must expose the uncollapsed content to keyboard/screen
  reader users via a toggle, not only on hover.
- Mobile and reduced-motion users: the `Jump to latest` scroll animation should
  respect `prefers-reduced-motion` and jump instantly when set.

### Live updates

SSE remains a wake-up path, not the only source of truth.

For active threads:

- append live SSE log/message events to a small live overlay while the user is
  near the live edge. "Near the live edge" means the last rendered turn block
  is the live edge turn, or the scroll position is within 200px of the bottom
  of the transcript container. This threshold should be a named constant, not
  an inline magic number.
- invalidate/refetch the latest transcript window on durable status/done events,
- reconcile on stream open/reopen,
- clear live overlay entries whose IDs appear in the newly fetched transcript
  window. SSE events must carry the same stable entry ID scheme (see Entry ID
  format) as persisted transcript entries so that reconciliation is an O(n) set
  difference, not a heuristic string match. If an SSE event does not carry a
  stable ID, it must be replaced by the persisted entry on the next refetch
  rather than kept in the overlay indefinitely.

When the user is reading older content:

- do not force-scroll,
- show `Jump to latest`,
- avoid repeatedly invalidating historical transcript windows.

### Oversized content policy

Transcript windows must remain bounded by both count and byte size.

Server-side policy:

- cap raw log entry content in list responses,
- include `message_bytes`, `message_chars`, and `message_truncated` metadata for
  log-derived entries,
- provide an existing detail endpoint for full log content when expanded,
- collapse tool results over a display threshold by default.

The transcript API should never return unbounded command output just because it
falls inside a loaded turn.

### Error states

The transcript pane must handle failure gracefully at every loading boundary.

| Situation | User-visible behavior |
| --- | --- |
| Initial window load fails | Show an inline error with a retry button in place of the skeleton. Do not leave a blank pane. |
| Older/newer page load fails | Show a retry affordance at the failed boundary (`Could not load older messages. Retry`). Do not remove already-loaded content. |
| Anchor restore fails (404 or expired cursor) | Fall back to the default open position silently. Clear the stale stored anchor. |
| Network reconnect during live view | Re-open SSE and refetch the latest window; reconcile against the live overlay. Show a transient "Reconnecting…" indicator only if the gap exceeds 3 seconds. |
| Empty thread (zero turns) | Show a prompt-focused empty state ("Send a message to start"), not a blank scroll area. |

## Migration Plan

1. Add backend models, validation tests, cursor helpers, and store tests.
2. Add `SessionTranscriptStore.ListThreadWindow` using existing tables.
3. Add transcript service and handler tests for org/thread/session scoping.
4. Add `GET /sessions/{id}/threads/{tid}/transcript`.
5. Add frontend API types and client tests.
6. Build a new transcript renderer behind a feature flag (e.g.
   `transcript_window_v1`). The flag should support: off (old renderer),
   on for all, and percentage rollout. Define the kill-switch behavior
   explicitly: toggling off reverts to the old client-assembly renderer using
   existing message/log/human-input endpoints — no data loss. Stored version 3
   positions written during a rollout are silently discarded by the old renderer
   (which ignores unknown versions), so rollback is safe.
7. Move thread-scoped session detail to the transcript-window query.
8. Add virtualization and anchor persistence.
9. Run visual/manual checks on long transcripts, mobile, active SSE, and anchored
   restore.
10. Remove session-detail dependence on separate thread message/log window
    assembly once parity is proven. Parity definition: the transcript-window
    renderer produces equivalent visual output for a representative sample
    covering short (< 5 turns), medium (20–50 turns), and long (> 100 turns)
    sessions, including one active session observed under live SSE, verified
    manually and signed off in the visual/manual test report. The old
    client-assembly path is removed only after that bar is met and the report
    is attached to the migration PR.

The existing message and log endpoints should remain for internal tools,
backward compatibility, and direct log inspection until all consumers are
audited.

## Test Plan

Backend:

- table-driven enum validation tests for transcript position and entry kind,
- cursor encode/decode tests,
- store tests for latest, older, newer, around, missing anchor fallback, and
  mixed message/log/human-input ordering,
- tenancy tests proving every query filters by `org_id`,
- service tests proving thread/session validation and oversized content
  collapsing,
- handler tests for query validation and response shape.

Frontend:

- API client tests for query param construction,
- transcript renderer tests for messages, tool rows, human inputs, collapsed
  long outputs, and empty windows,
- scroll anchoring tests for prepending older windows,
- restore tests for saved entry anchors and missing-anchor fallback,
- active-session tests for near-bottom auto-follow vs reading older content,
- performance regression test that a large loaded history does not mount every
  entry.

Verification for frontend changes must include:

```bash
cd frontend
npm run typecheck
npm run lint
npm run build
```

Verification for Go changes must include:

```bash
go vet ./...
go build ./...
go test ./...
```

## Open Questions

- Should checkpoint summaries be part of the first refactor or a follow-up?
  Recommendation: follow-up after raw transcript windows are stable.
- Should the API page by turn count or estimated rendered height?
  Recommendation: page by turn count first; add server byte caps and client
  virtualization for height variance.
- Should legacy session-level timelines without threads use the same API?
  Recommendation: migrate thread views first, then provide a synthetic primary
  thread transcript for legacy sessions if needed.
- Should transcript search be backed by Postgres full-text search or an
  external index?
  Recommendation: start with Postgres scoped search over messages/log summaries,
  then revisit if usage demands richer ranking.

## Decision

The long-term session transcript should be a backend-defined, turn-grouped,
cursor-paginated transcript window rendered through an anchor-stable virtualized
frontend. This is the right abstraction for coding-agent sessions because the
product unit is not an infinite chat log; it is a resumable work session with
messages, tools, logs, questions, milestones, and code-review outcomes that
need to stay inspectable without making the user pay the cost of the entire
history on every open.

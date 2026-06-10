# Design: Session Open Position for Existing Sessions

> **Status:** Implemented | **Last reviewed:** 2026-06-10

## Summary

When a user opens an existing session, the product should open them at the **most relevant continuation point**, not blindly at the very top or very bottom.

Recommended default:

1. If the user has a saved reading position for that session, restore it.
2. Otherwise, if the session is currently active/streaming, open at the live edge (bottom).
3. Otherwise, open at the **start of the latest assistant turn**, not the absolute bottom of the transcript.

For multi-thread sessions, resolve the **last active thread first**, then apply
the same per-thread scroll/open-position rules inside that thread rather than
defaulting back to the primary tab.

This keeps the behavior simple, quiet, and predictable while still respecting two very different intents: "continue work" and "re-read history."

## Problem

Opening every session at the top optimizes for chronology, but it is usually the wrong default for a tool people return to in order to continue work. In long coding sessions, the top is often the least useful place:

- It forces users to manually traverse the entire session before they can act.
- It hides the latest result, question, or failure state.
- It creates friction precisely when a user is resuming a thread.

Opening every session at the absolute bottom is also imperfect:

- Users can land on the tail end of a long assistant response with no context.
- It can feel jumpy or disorienting if the final message is still streaming.
- It makes careful review of the latest turn harder than it should be.

The product needs a continuation rule that feels obvious without adding visible controls or preference complexity.

## What Comparable Tools Suggest

### Claude Code

Claude Code's official docs emphasize **resuming the most recent conversation** with `claude --continue`, and `claude --resume` opens a picker for prior conversations. The product framing is continuation-first, not chronology-first. When resumed, the docs say the full message history and tool state are restored.  
Source: [Anthropic Claude Code common workflows](https://docs.anthropic.com/en/docs/claude-code/common-workflows)

### Codex

Codex Desktop users explicitly reference an existing **"jump to bottom"** affordance and request a complementary "jump to start of latest assistant response" action for long threads. That is strong evidence that the important navigation problem in agent sessions is around the latest work, especially the boundary of the latest response, not getting users back to the beginning.  
Source: [openai/codex issue #17536](https://github.com/openai/codex/issues/17536)

### Conductor

Conductor frames the unit of continuity as the **workspace**. Its docs say archived workspaces can later be restored "including your chat history." The emphasis is not on rereading from the top; it is on returning to the working state of an existing workspace. That suggests the right mental model is "resume work where I left off," not "restart the transcript from the beginning every time."  
Sources: [Conductor workflow](https://docs.conductor.build/workflow), [Conductor workspaces and branches](https://docs.conductor.build/tips/workspaces-and-branches)

## Recommendation

### Primary rule

Use a single hierarchy of intent:

1. **Restore last position** when available.
2. Else **follow the live edge** for active sessions.
3. Else **anchor to the start of the latest assistant turn** for inactive sessions.

This is the least obtrusive version that still behaves intelligently.

### Why not always go to the bottom?

Because the bottom is often the middle or end of a long answer. Users reopening a completed session usually need the latest meaningful block, not the last pixel.

The better unit is the **latest turn boundary**:

- It preserves context.
- It minimizes surprise.
- It gives users one short upward scroll to see what they said, and one downward scroll to continue.

### Why restore last position first?

Because if a user has already been reading a session, their own prior position is the strongest signal of intent. It is more respectful than any generic default.

This should be lightweight and forgiving:

- Persist per user, per session, with scroll positions scoped per thread when a
  session has multiple agent tabs.
- For thread-scoped transcripts, persist a structured message anchor plus
  offset (`version: 2`, message id, offset px, raw scroll fallback) so long
  sessions can reopen in one bounded window request.
- Persist the last active thread per user and session so reopening a multi-tab
  session restores the same conversation lane before reading scroll state.
- Update on debounced scroll or on unmount.
- Best effort only. If it is missing, fall back cleanly.

## UX Rules

### Active sessions

For `running` or live-streaming sessions:

- Open at the bottom.
- Keep auto-follow only while the user is near the bottom.
- If the user scrolls away, stop auto-follow and show a subtle "Jump to latest" affordance.

This matches established chat/log behavior and supports live monitoring.

### Idle/completed sessions

For non-active sessions:

- Restore the user's last position if known.
- In multi-thread sessions, reopen the last active thread first when that tab
  still exists.
- Otherwise scroll to the **start of the latest assistant turn**.
- If the latest turn is missing or malformed, fall back to the bottom.

This makes the latest completed work legible instead of dropping users into the tail of a response.

### Brand-new arrivals from creation/resume actions

If the user has just:

- created a session,
- sent a follow-up message,
- clicked an explicit "Resume session" action,

then bias to the live edge even if there is no saved position yet. In these flows, intent is unambiguously "continue."

## Interaction Details

Keep the interface quiet:

- No settings page toggle for "top vs bottom."
- No modal asking the user where to start.
- No extra chrome unless the user scrolls away from the live edge.

Useful minimal affordances:

- Floating "Jump to latest" button when not near bottom.
- Optional secondary action: "Start of latest response."

If only one affordance ships, prefer `Jump to latest` first.

## Notes for 143

The current session detail implementation already auto-scrolls after sending a message and while new entries arrive **only if** the user is near the bottom. But on initial mount it does not choose an initial anchor; `isNearBottomRef` starts false, so existing sessions naturally open at the top. That is why the current behavior feels wrong for resumed sessions.

The intended product behavior should therefore be:

- add an **initial anchor decision** on mount,
- separate that from the existing "auto-follow while near bottom" behavior,
- treat "restore last position" and "latest turn anchor" as mount-time concerns, not streaming concerns.

## Decision

Recommended product decision:

- **Do not open existing sessions at the top by default.**
- **Restore last position when known.**
- **Otherwise open active sessions at the bottom and inactive sessions at the start of the latest assistant turn.**

This is the simplest behavior that still feels attentive.

## Implementation Plan: Bottom-First Loading With Stable First Paint

Bottom-first loading is the preferred implementation strategy for session
detail. It should not be interpreted as "always show the absolute bottom." It
means the first transcript payload should be the newest useful window of the
active thread, and the UI should resolve its initial visual anchor inside that
window before the user sees partial transcript paint.

The product goal is a quiet reopen:

- the route changes and session chrome appear immediately,
- the transcript area has stable structure while the first message window loads,
- real recent messages replace placeholders without changing the user's
  perceived position,
- older history loads above the viewport only when needed,
- auto-follow behavior starts only after the initial anchor decision has
  completed.

Session-list navigation may seed React Query with list-row session metadata so
the destination route can mount without waiting for `/sessions/{id}`. That
seeded payload is explicitly marked provisional. While provisional data is the
only cached detail, the detail route renders the normal skeleton and suppresses
session-specific child requests such as timeline, diff, review-loop, PR, and
file-event fetches. The authoritative detail response replaces the provisional
entry and then starts the transcript/detail queries.

### Target Behavior

1. Resolve the active thread first.
   - Prefer the user's last active thread for the session.
   - Fall back to the primary visible thread.
   - Do not render one thread and then switch to another after messages load.

2. Fetch the relevant bounded message window first.
   - Request the newest messages for the active thread in descending or
     cursor-friendly order, then render chronologically in the viewport.
   - If a structured saved position exists, request
     `/threads/{tid}/messages?position=around&anchor_message_id=...` and render
     the returned older/anchor/newer range chronologically.
   - Include enough messages to cover at least one desktop viewport and one
     mobile viewport, with margin for the latest assistant turn.
   - The payload should include cursors for older messages and enough metadata
     to locate the latest assistant turn boundary.

3. Choose the initial anchor before showing real transcript content.
   - If a structured saved position exists and the anchor was found, scroll to
     that message plus the saved offset.
   - If the anchor is missing, fall back to the latest window and ignore the
     stale raw scroll value for that initial restore.
   - Else if the thread is active, anchor to the live edge.
   - Else anchor to the start of the latest assistant turn when present.
   - Else fall back to the bottom of the loaded window.

4. Use a minimal transcript skeleton only before the first real window appears.
   - Show a small set of bottom-aligned message placeholders in the transcript
     area, not a full fake conversation.
   - Match final message spacing, max widths, and composer clearance so the
     first real render does not feel like a page replacement.
   - Do not persist skeleton scroll offsets as saved reading positions.

5. Load older messages above the viewport.
   - When the user scrolls near the top of the loaded window, fetch the next
     older page.
   - Prepend older messages while preserving the user's visible content and
     scroll offset.
   - Show a compact top loading row or short skeleton while the older page is
     pending.

6. Load newer messages below anchored windows.
   - Anchor-centered windows can represent the middle of a long transcript, so
     the response includes `has_newer` and `next_newer_cursor`.
   - When the user scrolls near the bottom of an anchored range, fetch
     `after=<newest_loaded_id>` and append the newer messages.
   - `Jump to latest` skips page-by-page newer loading and requests the latest
     window directly.

7. Keep live auto-follow separate from initial anchoring.
   - During initial load, suppress repeated scroll-to-bottom effects.
   - After the anchor resolves, active sessions can follow the live edge only
     while the user remains near bottom.
   - If the user scrolls away, stop following and expose `Jump to latest`.

### API Shape

The cleanest backend contract is a thread-message-window endpoint rather than
forcing the frontend to fetch the entire session transcript:

```text
GET /api/v1/sessions/{session_id}/threads/{thread_id}/messages?position=latest&limit=...
GET /api/v1/sessions/{session_id}/threads/{thread_id}/messages?before=<cursor>&limit=...
```

The initial payload should include:

- `data`: chronological messages for the loaded window,
- `meta.next_older_cursor`: cursor for fetching older history,
- `meta.has_older`: whether more history exists,
- `meta.latest_assistant_message_id`: latest assistant turn anchor candidate,
- `meta.live_edge_message_id`: newest message in the thread,
- `meta.thread_status`: enough state to decide active/live behavior.

Saved reading position can remain client-owned initially if that is already the
current pattern, but the stored value should be structured:

```json
{
  "thread_id": "...",
  "anchor_message_id": "...",
  "anchor_offset_px": 128,
  "saved_at": "..."
}
```

A raw numeric scroll value is not enough for bottom-first pagination because
the scroll container's total height changes as older pages are prepended.

### Frontend Architecture

The session transcript should have an explicit load state machine:

- `resolving_thread`: session shell is visible, thread choice is not final,
- `loading_initial_window`: show minimal bottom-aligned transcript skeleton,
- `anchoring_initial_window`: messages are available, but scroll restoration is
  being applied before the content is exposed,
- `ready`: normal interaction, older pagination, and live updates are enabled,
- `loading_older`: same as `ready`, with top loading affordance.

Implementation details:

- Keep message identity stable by `message.id`; never key by array index.
- Use a single scroll coordinator for initial anchor, older-message prepend,
  and live-edge follow. Avoid independent effects competing to set
  `scrollTop`.
- Measure the first visible message before prepending older pages and restore
  its visual offset after render.
- Gate expensive message decoration, syntax highlighting, and attachment
  previews behind stable message containers so rich rendering cannot cause a
  large reopen shift.
- Virtualization is optional for the first version, but the design should not
  block it. If virtualization is introduced, use message IDs and measured
  height cache as the durable positioning model.

### Skeleton Design

The skeleton should be intentionally modest:

- 3-6 message rows depending on viewport height,
- bottom-aligned so the composer region feels stable,
- alternating user and assistant widths,
- semantic surface tokens from the design system,
- no fake timestamps, controls, labels, or message text,
- no full-page spinner for the transcript.

Once the initial message window is available, the skeleton disappears entirely.
For older pages, use only a top inline loading row or 1-2 short placeholders.
Do not mix fake message rows into already-loaded real conversation content.

### Quality Bar

The implementation is complete only when these behaviors hold:

- Opening a long inactive session does not flash top-of-thread content before
  moving to the latest turn.
- Opening an active session lands at the live edge and follows new messages
  only while the user is near bottom.
- Reopening a multi-thread session restores the last active thread before any
  transcript content is shown.
- Loading older messages does not move the message the user was reading.
- Sending a follow-up still shows the optimistic user message immediately.
- A legacy saved numeric scroll position of `0` does not force old sessions to
  reopen at the top.
- Mobile and desktop both leave the composer area stable during first load.

### Rollout Phases

1. **Frontend scroll coordinator and minimal skeleton**
   - Add the explicit initial-load state machine.
   - Suppress competing initial auto-scroll effects.
   - Add the bottom-aligned initial transcript skeleton.
   - Keep the existing full transcript API if necessary.

2. **Bottom-first message window API**
   - Add cursor-based thread message loading.
   - Return latest-window metadata for anchor selection.
   - Keep org scoping on every store query.

3. **Older-message prepend**
   - Add top pagination and scroll offset preservation.
   - Add tests for prepend stability and near-top loading.

4. **Saved position hardening**
   - Store structured message-anchor positions.
   - Migrate or ignore legacy raw numeric positions where they are not
     meaningful.

5. **Rich-content stability**
   - Reserve dimensions for slow-rendering message subcontent where practical.
   - Add regression coverage for code blocks, attachments, tool output, and
     long assistant messages.

### Test Plan

Frontend tests should cover:

- initial skeleton appears before the first window resolves,
- inactive sessions anchor to the latest assistant turn,
- active sessions anchor to the live edge,
- saved structured position wins over default anchors,
- legacy top-only raw positions are ignored,
- older-page prepend preserves the visible anchor message,
- user scroll-away disables live auto-follow,
- optimistic follow-up messages still render immediately.

Backend tests should cover:

- latest-window query is scoped by `org_id`,
- older-page query is scoped by `org_id`,
- cursor ordering is stable and deterministic,
- returned metadata identifies the latest assistant turn correctly,
- empty-thread and deleted/archived-thread fallbacks are explicit.

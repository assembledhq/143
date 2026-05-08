# Design: Session Open Position for Existing Sessions

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

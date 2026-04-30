# 66 - Mobile Session Diff Review

> **Status:** Partially Implemented | **Last reviewed:** 2026-04-30
>
> **Implementation notes:** The core mobile handoff is now shipped. On mobile, the conversation-level `Files changed` entry point opens review directly instead of foregrounding session details, the reader uses a dedicated single-file mobile presentation with unified diff only, the toolbar shows the current file position and explicit file-list reopen affordance, and the mobile `Changes` sheet can reopen as the file index while the diff reader remains the primary surface. Further refinement may still be warranted around comment-specific mobile affordances and richer per-file utility actions.

## Problem

On mobile, the session detail right rail becomes a bottom sheet. The current review entry flow still assumes a desktop layout:

- Tapping `Files changed` in the conversation does not clearly hand off into a readable diff surface.
- The file list remains in the mobile bottom sheet.
- Tapping a file in `Changes` keeps too much emphasis on session-detail chrome instead of the diff itself.

In practice this makes the modified files discoverable but not comfortably reviewable. The user can see session details and the file list, but the actual diff-reading experience is not obvious or optimized for a phone viewport.

## Goals

- Make file diffs clearly reachable from `Changes` on mobile.
- Optimize for reading and scanning, not desktop feature parity.
- Preserve the existing desktop review surface.
- Keep inline comments possible, but treat readable navigation as the first priority.

## Non-goals

- Rebuilding the desktop review architecture.
- Full split diff parity on phones.
- Heavy repo-explorer workflows as the primary mobile path.

## Current Constraint

`SessionDetailContent` currently uses:

- desktop: inline right detail panel plus center review pane
- mobile: bottom-sheet detail panel plus center review pane

That means mobile has a two-surface interaction model. The user selects a file in one surface and is expected to understand that the diff is rendered somewhere else behind the sheet.

## Decision

Use a dedicated full-screen mobile diff route/state. This is the only direction we are designing for.

Use a dedicated mobile review screen/state. Tapping a file in `Changes` opens a full-screen diff reader for that file, with a back button returning to the file list.

### What it looks like

- Tapping `Files changed` in the conversation immediately opens the mobile diff reader instead of opening or emphasizing session details first.
- `Changes` sheet stays a file-list entry point.
- Tapping a file closes the sheet and opens a full-screen diff reader.
- Header shows file name, file count position, and back.
- Primary mode is unified diff.
- Swipe or previous/next buttons move between files.
- Inline comments remain available behind a secondary action.

## Why this is the direction

- Clearest mental model on mobile.
- Best use of narrow viewport width.
- Removes the hidden-behind-sheet problem entirely.
- Lets the conversation-level `Files changed` action behave like a direct navigation action: tap it, read the diff.
- Keeps desktop review unchanged while making mobile intentionally different where it needs to be.

## Recommended UX details

- `Files changed` in the conversation is a direct entry point into review on mobile.
- Mobile `Changes` remains the launcher and file index.
- Tapping a file opens a full-screen unified diff reader for that file.
- Default to unified view only on mobile.
- Provide sticky top bar with:
  - back
  - truncated file path
  - previous/next file controls
  - overflow actions for comment and open repo file
- Keep comments collapsed by default behind a `Comments` action so reading stays primary.
- Preserve current file, scroll position, and pass filter when backing out to the file list.

## Rollout Plan

### Phase 1

- Make file taps on mobile open a dedicated full-screen reader.
- Support read-only diff review plus previous/next file navigation.
- Keep unified diff only.

### Phase 2

- Add inline comment entry and comment anchors.
- Add lightweight file search/jump inside the full-screen reader.

### Phase 3

- Consider repo browsing and richer comment management only if mobile usage proves it is needed.

## Success Criteria

- On mobile, tapping `Files changed` in the conversation immediately shows diff content.
- On mobile, a first-time user can tap `Changes`, tap a file, and immediately see readable diff content.
- Users no longer need to infer that the diff is rendered behind the detail sheet.
- Mobile review completion rate improves without materially hurting desktop review speed.

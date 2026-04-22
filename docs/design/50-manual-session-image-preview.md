# Design: Manual Session Image Hover Preview

> **Status:** Proposal | **Last reviewed:** 2026-04-21

## Problem

Manual session creation currently accepts uploaded images and image URLs, but the pre-submit attachment strip renders them as small thumbnails only. This makes screenshots and photos hard to inspect before the user starts the session.

The immediate UX need is simple: when a user hovers an attached image while composing a manual session, they should see a larger preview without losing their place in the composer.

## Current State

Three frontend surfaces currently render pending manual-session attachments independently:

1. `/sessions/new` manual session page
2. `CreateSessionDialog` modal
3. Session detail follow-up composer for interactive manual sessions

The session timeline already has separate image-preview behavior for persisted attachments: clicking an image opens a lightbox. That logic is local to `chat-timeline.tsx` and is not reusable by the composer surfaces.

## Goals

- Make attached images easier to inspect before submit.
- Keep the interaction lightweight and fast for pointer users.
- Reuse one attachment-preview implementation across all manual-session composer surfaces.
- Preserve existing remove-attachment behavior.
- Avoid any backend or API changes.

## Non-Goals

- Reworking upload APIs, storage, or attachment metadata
- Adding image transforms, zoom controls, or rotation
- Replacing the existing timeline lightbox behavior for persisted messages in the same change

## Constraints

- The app uses shadcn/Radix primitives; the implementation should stay inside that system.
- Manual-session creation already has duplicated attachment-strip code. A hover preview should reduce duplication, not add another variant.
- Keyboard and touch users still need a usable path. Hover cannot be the only way to inspect an image.

## Proposed UX

For image attachments in composer surfaces:

- Keep the existing small thumbnail in the attachment strip.
- On hover, show a larger floating preview near the thumbnail.
- Keep the remove affordance visible and easy to click without accidentally opening the preview.
- On click, open the full lightbox preview used elsewhere for accessibility parity and touch support.
- Non-image attachments remain unchanged.

This gives users a fast hover peek on desktop and a deliberate click-to-open path on all devices.

## Proposed Component Shape

Extract the pending attachment-strip UI into a shared component under `frontend/src/components/`, for example:

- `PendingAttachmentStrip`
- `ImageAttachmentPreview`
- shared `ImageLightbox`

Suggested responsibilities:

- `PendingAttachmentStrip`
  - accepts `attachments`, `isUploading`, and `onRemove`
  - splits image vs non-image attachments
  - renders the upload spinner tile
- `ImageAttachmentPreview`
  - renders thumbnail
  - shows hover preview for pointer devices
  - opens lightbox on click
- `ImageLightbox`
  - reused from timeline instead of duplicated

## Recommended Implementation

### Phase 1: Shared component extraction

- Move duplicated pending-attachment rendering out of:
  - `frontend/src/app/(dashboard)/sessions/new/manual-session-create-page-content.tsx`
  - `frontend/src/components/create-session-dialog.tsx`
  - `frontend/src/app/(dashboard)/sessions/[id]/session-detail-content.tsx`
- Reuse one shared remove-button pattern and one file badge pattern.

### Phase 2: Hover preview behavior

- Use a Radix-based floating primitive for the desktop hover preview.
- Show a preview sized for inspection, roughly in the 240-320px range, bounded by viewport.
- Add a short open delay so the preview does not flicker during pointer movement.
- Keep the preview non-modal so the user stays in the composer flow.

### Phase 3: Click-to-open full preview

- Reuse the existing lightbox pattern from `chat-timeline.tsx` so the same larger modal preview works from composer thumbnails too.
- This covers touch devices and keyboard users who cannot rely on hover.

## Why This Shape

- No backend work is required because attachments are already represented as URLs and image detection already exists via `isImageURL()`.
- The real work is frontend consolidation. Today the codebase has three pending-attachment implementations and one separate persisted-attachment implementation.
- If hover preview is added only to `/sessions/new`, the product will immediately feel inconsistent because the dialog and follow-up composer behave differently.

## Accessibility Requirements

- Clicking a thumbnail should open the larger preview.
- Keyboard focus on a thumbnail should allow opening the lightbox via Enter/Space.
- Hover preview should be treated as an enhancement, not the only inspection path.
- Remove buttons must remain independently focusable and labeled.

## Testing Requirements

Add component tests that cover:

- image thumbnail renders in the shared strip
- hover/focus shows larger preview
- click opens lightbox
- remove button still removes the attachment
- non-image attachments still render as files, not previews
- the shared component works in:
  - manual session create page
  - create-session dialog
  - session detail follow-up composer

## Verification

After the frontend change:

1. `cd frontend && npm run typecheck`
2. `cd frontend && npm run lint`
3. `cd frontend && npm run build`

## Open Questions

- Whether the hover preview should appear on focus as well, or only hover plus click. Focus support is better for accessibility.
- Whether the timeline attachment viewer should also be migrated onto the same shared `ImageLightbox` in the same PR or a follow-up cleanup PR.

## Recommendation

Treat this as a small frontend UX improvement with moderate cleanup value. The safest implementation is:

1. extract a shared pending-attachment strip
2. add hover preview plus click-to-lightbox for image thumbnails
3. roll it out to all three manual-session composer surfaces in one PR

That keeps the change entirely frontend-only, improves usability immediately, and reduces duplicated attachment UI going forward.

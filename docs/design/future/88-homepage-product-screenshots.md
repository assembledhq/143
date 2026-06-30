# Homepage Product Screenshots

> **Status:** Partially Implemented | **Last reviewed:** 2026-05-26

The homepage should replace mock-only landing visuals with real product screenshots taken from a repeatable seeded product state. The goal is to make 143 feel like a real shared coding-agent workspace while keeping the current positioning around team context, cloud execution, previews, review loops, and integrations.

## Image Set

Use five primary captures:

- **Workspace/setup:** onboarding or autopilot setup showing coding-agent choice plus team integrations.
- **Session detail:** transcript, agent tabs, PR state, and preview/status surfaces in one frame.
- **Review loop:** changed files, review comments, repair/test status, or PR health.
- **Preview:** preview tab/status with the main preview action visible.
- **Settings/integrations:** connected GitHub/Linear/Sentry-style setup to show team-owned configuration.

Prefer screenshots from the dogfood preview seed in `.143/seed/`, or a dedicated screenshot seed that is safe to publish. The capture state should use realistic repository names, task titles, statuses, and timestamps, but no real customer data, secrets, private repo names, emails outside the demo set, tokens, URLs with credentials, or production incident identifiers.

## Capture Workflow

Capture from the product itself rather than recreating UI in a design tool:

1. Boot the dogfood/demo app with `DEMO_MODE=true`.
2. Enter the demo directly as the seeded viewer.
3. Capture desktop screenshots at 1440x960 or 1600x1000.
4. Capture one narrow/mobile state only if it clarifies that the product works away from desktop.
5. Store final web assets under `frontend/public/product/` as `.webp` plus a source `.png` when useful.

Each screenshot should have a stable route, viewport, theme, and seed row documented so it can be regenerated after UI changes.

## Initial Implementation

The first homepage pass uses demo-mode captures saved under `frontend/public/product/`:

- `product-integrations.webp`
- `product-session-overview.webp`
- `product-review-diff.webp`
- `product-session-preview.webp`
- `product-sessions-list.webp`

The review screenshot depends on the seeded session `00000000-0000-4000-a000-000000000300`, which now includes a safe demo diff in `.143/seed/42_session_conversation.sql` so the Changes tab renders a realistic review surface after reseeding.

## Postprocessing

Use light postprocessing only:

- Crop to the product surface; avoid oversized browser chrome.
- Redact or replace sensitive strings before export.
- Use consistent 16:10 or 16:9 crops so screenshots can swap into shared landing frames.
- Export 2x assets, then compress to WebP with high quality.
- Keep screenshots sharp. Do not blur the UI, add fake depth-of-field, or paint over the interface.
- Add a minimal frame/shadow in CSS, not inside the bitmap, so dark/light themes can reuse the same asset.

## Layout Direction

Landing screenshot sections should use wide page shells and give the visual column more width than copy on desktop. Copy stays readable in a narrower column, while product imagery can occupy most of the row and reveal actual interface detail. This matches the current direction of AI coding homepages that show concrete app surfaces and task states instead of abstract generated illustrations.

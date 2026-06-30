# Design: Audit Log Feed Pagination

> **Status:** Not Started | **Last reviewed:** 2026-06-30

## 1. Problem

The current audit log experience uses cursor pagination in the API, but the UI presents it as a page-replacement flow with `Newer` and `Older` buttons. That creates three product problems:

- audit logs feel like a document with pages instead of an activity feed
- moving between pages replaces the whole list, so the user loses visual context
- the interaction does not scale well to investigations where operators need to scan, compare, and keep place

This is primarily a UX problem, not a storage/query problem. The backend already uses the right primitive for scale: newest-first cursor pagination on `(created_at, id)` backed by `idx_audit_logs_org_created`.

## 2. Goals

- Make audit logs feel like a stable event stream.
- Preserve reading position while loading older history.
- Keep queries index-friendly and safe at large row counts.
- Support both broad browsing and targeted investigation.
- Reuse the same pagination model for full-page and scoped audit surfaces.

## 3. Non-goals

- Full-text search across audit payloads.
- Real-time websocket/SSE streaming in the first iteration.
- Arbitrary numbered-page navigation.

## 4. Pagination patterns considered

### Option A: Numbered pages with offset/limit

Example UI:

- `1 2 3 4 5`
- `Previous` / `Next`

Pros:

- familiar pattern
- easy to explain
- allows rough positional jumping

Cons:

- poor database behavior at scale because large offsets get slower
- unstable under concurrent writes; page 2 can change while the user is browsing
- a bad fit for audit investigations because users think in time and events, not page numbers

Decision:

- Do not use for audit logs.

### Option B: Cursor pagination with whole-page replacement

Example UI:

- `Newer`
- `Older`

Pros:

- operationally efficient
- stable under append-heavy workloads
- simple to implement

Cons:

- loses visual continuity on every navigation
- feels slow even when backend latency is good because the user must re-orient after every click
- makes comparison across page boundaries awkward

Decision:

- This is better than offset pagination technically, but not good enough as the primary audit log UX.

### Option C: Cursor pagination with append-only "Load older"

Example UI:

- page opens with newest 25 events
- `Load older` appends the next 25 below the current list
- current viewport stays anchored

Pros:

- keeps cursor-based scalability properties
- preserves context and reading position
- matches how operators naturally inspect timelines
- works well for both global audit pages and scoped audit modules

Cons:

- list can become large in the DOM if unbounded
- "go back to top/newest" needs an explicit affordance

Decision:

- Recommended default.

### Option D: Infinite scroll auto-loading

Example UI:

- older events load automatically when nearing the bottom

Pros:

- very fluid
- removes one click from the experience

Cons:

- easier to overshoot
- harder to preserve a deliberate investigation checkpoint
- weaker footer discoverability and less explicit control in admin/compliance surfaces

Decision:

- Not the default. Can be acceptable later if paired with virtualization, but explicit `Load older` is the better first product decision.

### Option E: Time-window navigation

Example UI:

- `Last hour`, `Last 24 hours`, `Last 7 days`
- date picker or `Jump to date`

Pros:

- matches how audit investigations often begin
- sharply reduces scanned row counts for large orgs
- helps users answer "what happened around X?"

Cons:

- not sufficient by itself; users still need intra-window pagination
- date picking is a refinement, not a replacement for feed browsing

Decision:

- Strong secondary control. Pair with Option C.

## 5. Recommended product direction

Adopt a **feed model**:

- default to newest-first
- use **cursor pagination**
- append older results inline via **`Load older`**
- never replace the whole list when loading older history

Then add two supporting controls:

- **time range filters** for broad narrowing (`Last 1h`, `24h`, `7d`, custom)
- a **new activity banner** when fresh events arrive above the current viewport (`12 new events`)

This gives the best balance across UX, speed, and scalability.

## 6. Why this is the right choice

### UX

Audit logs are an investigation surface. Users need continuity more than page navigation. Replacing the whole page forces mental reloading. Appending older entries lets the user maintain context, skim patterns, and open details without losing place.

### Performance

Cursor pagination on `(created_at, id)` is the correct backend strategy for append-heavy logs. It avoids expensive offsets, remains stable while new rows are inserted, and aligns with the existing index and API contract.

### Scalability

The main scaling pressure moves to the client, not the database. That is manageable:

- keep page size modest (`25` or `50`)
- append incrementally
- add list virtualization only when the rendered row count becomes large enough to matter

This is a better trade than pushing complexity into the database with offset pages or asking users to mentally stitch separate pages together.

## 7. Proposed UX

### 7.1 Full audit log page

- Show the newest 25 entries on load.
- Replace `Older` / `Newer` paging with:
  - `Load older`
  - `Back to newest`
- Keep filters sticky in the URL.
- Show a compact result context line above the feed, for example:
  - `Showing latest activity`
  - `Showing 24 hours of activity`
  - `Filtered to session actions by Alice`

### 7.2 New events while browsing

If the user is at or near the top:

- allow a background refresh and prepend safely

If the user has scrolled away from the top:

- do not shift the list unexpectedly
- show a sticky banner like `12 new events`
- clicking it prepends the new rows and returns focus to the newest region

### 7.3 Scoped audit timelines

Session or project detail views should use the same feed model but with smaller page sizes and without the heavier filter bar. The interaction should stay consistent across surfaces.

## 8. Implementation notes

Backend:

- no pagination model change required for the first iteration
- reuse existing `cursor` and `next_cursor`
- continue ordering by `created_at DESC, id DESC`

Frontend:

- move from single-page `useQuery` state to an appended-page model
- prefer `useInfiniteQuery` or equivalent manual page accumulation
- preserve row identity and scroll position during append/prepend
- add a clear "return to newest" affordance once the user is not at the top

Future enhancements:

- virtualization for very long loaded feeds
- jump-to-date that resolves to an anchor cursor
- optional auto-refresh / live mode for active investigation sessions

## 9. Final recommendation

Use **cursor-based feed pagination with explicit `Load older` append behavior**, plus **time-range filters** and a **new-events banner**. Avoid numbered pages and avoid full-page replacement.

This is the strongest choice because it keeps the backend architecture correct for scale while making the UI feel calm, continuous, and investigation-friendly.

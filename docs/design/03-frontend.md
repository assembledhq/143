# Design: Frontend Architecture

> **Status:** Implemented | **Last reviewed:** 2026-07-11

This document is the current frontend architecture map for 143.dev. It should help engineers place new UI work, choose the right state pattern, and keep the product surface consistent.

## Product Principles

Every authenticated page should answer one question: **what should I do next?**

- Session pages help users start work, inspect work, continue work, and publish work.
- Queue pages help users decide what can run, what is blocked, and what needs review.
- Settings pages help admins configure durable defaults, credentials, policies, and integrations.
- Public pages explain the product and docs without requiring app context.

Avoid standalone pages that only report information. If a page has no next action, it usually belongs inside a detail page, settings surface, dashboard panel, or public docs page.

## Stack

- Next.js 16+ with App Router.
- React 19+.
- TypeScript in strict mode.
- Tailwind v4 and shadcn/ui for app UI.
- Radix primitives through shadcn components.
- TanStack Query for server state.
- `nuqs` for URL-backed filters and search state.
- Fumadocs for public docs under `/docs`.

## Visual System

- Use shadcn/ui components for interactive and structural UI.
- Use the warm mineral/charcoal semantic tokens. Choose `bg-background`, `bg-surface-raised`, or `bg-surface-recessed` by elevation; use `border-border-strong` only when a boundary must be emphasized.
- Instrument Sans (`font-display`) is for brand, page titles, and narrative hierarchy. Geist remains the readable and operational face.
- Body copy is 14px. Use `type-dense` for the explicit 13px dense-UI role and `text-xs` only for metadata.
- Keep app surfaces dense and work-focused: compact controls, hairline boundaries, restrained selected states, and stable columns.
- Use saturated color only for primary actions and meaningful state.
- Primary controls and user messages use solid fills. Do not reintroduce blue-purple gradients or ambient glows.
- Static `Card` instances have no hover affordance. Use `variant="interactive"` or `InteractiveCard` only when the entire surface has an action.
- Use `StatusLabel` for operational state, `ResourceRow` for identity/metadata/state/action rows, `SectionGroup` for explanatory sections, and `ContextHeader` for durable workspace context.
- Use a single selected-state signal: a soft accent surface plus, where needed, one leading indicator. Do not stack border, ring, and shadow signals.
- Prefer persistent layout over surprise: tabs, columns, sidebars, and empty states should stay in place across loading and zero-data states.
- Keep typing local. Search boxes, composers, and settings forms should not rerender large adjacent panels on every keystroke.

## Navigation

The authenticated app is organized around team-visible agent work, not raw backend tables.

| Route | User question | Notes |
| --- | --- | --- |
| `/sessions` | What agent work is running or ready to review? | Primary execution surface for prompts, transcripts, diffs, previews, follow-ups, branches, and PRs. |
| `/automations` | What recurring or event-triggered goals exist? | Team-owned automation setup, run history, pause/resume, and failure recovery. |
| `/autopilot` | What work can run automatically? | Queue, eligibility gates, active caps, and manual-review states. |
| `/projects` | What larger work is being planned? | Multi-step work that may feed sessions over time. |
| `/previews` | What live app previews exist? | Session, branch, and PR preview index and health. |
| `/code-reviews` | What PR review work is active? | Reviewer-bot findings, evidence, risk decisions, and GitHub review output. |
| `/settings` | How is the org configured? | Integrations, agents, runtime, API keys, team, audit log, usage, evals, Autopilot, and previews. |

Secondary surfaces such as repository details, onboarding, team management, and integration setup should be reachable from the workflow or setting that creates the need.

Public landing pages and `/docs` use the landing shell, not the authenticated dashboard shell.

## Project Shape

Keep the architecture map compact. Feature-specific component inventories belong near the feature or in the relevant design doc.

```text
frontend/
├── source.config.ts                  # Fumadocs source config
├── src/
│   ├── app/
│   │   ├── (auth)/                   # login and auth routes
│   │   ├── (dashboard)/              # authenticated app shell
│   │   ├── (landing)/                # public website and docs shell
│   │   └── api/                      # route handlers such as raw docs
│   ├── components/
│   │   ├── ui/                       # shadcn/ui components
│   │   ├── docs/                     # public docs MDX/shell components
│   │   └── ...                       # feature components by product area
│   ├── hooks/                        # React Query and UI hooks
│   ├── lib/
│   │   ├── api/                      # typed API clients and query helpers
│   │   ├── docs/                     # public docs source/raw/llms helpers
│   │   └── utils.ts
│   └── test/                         # MSW, setup, and shared test helpers
├── public/
│   └── product/                      # product screenshots used by docs/marketing
└── components.json                   # shadcn/ui config
```

## State Patterns

### Server State

All API calls should go through TanStack Query hooks or shared API helpers. Do not call `fetch` directly from components.

Use stable query keys that include tenant-scoped filters:

```tsx
const { data, isLoading, error } = useQuery({
  queryKey: ["sessions", filters],
  queryFn: () => api.sessions.list(filters),
});
```

Mutations should invalidate the narrowest useful query set and surface errors through the page state or toast system.

### URL State

Use `nuqs` for filter/search state that should be bookmarkable or shareable:

- list filters
- tabs with meaningful URLs
- query text
- sort/order controls
- selected preview/session/project where deep linking matters

Keep ephemeral UI state local: open menus, focused rows, draft text before submit, and temporary popover state.

### Live State

Use SSE or polling only for surfaces that need it:

- session transcripts and run state
- preview startup state
- PR health/readiness state
- queues that operators actively watch

Durable backend state remains authoritative. Optimistic UI is fine, but live updates should not imply a state transition that has not been committed.

## Shared Components

Use shared components before adding one-off page chrome:

- `PageHeader` for page title and description.
- `EmptyState` for zero-data states.
- shadcn `Button`, `Input`, `Select`, `Tabs`, `Dialog`, `DropdownMenu`, `Tooltip`, `Table`, and `Card` where applicable.
- `src/components/docs/docs-mdx-components.tsx` for public docs MDX components.

Command-style popover pickers that support search and single- or multi-select should share the checked-row primitive in `components/ui/command.tsx` instead of reimplementing checkmark colors or spacing per screen.

## Public Docs

Public docs are part of the frontend app:

- Route shell: `frontend/src/app/(landing)/docs`.
- Source: `docs/public`.
- MDX components: `frontend/src/components/docs/docs-mdx-components.tsx`.
- Docs helpers: `frontend/src/lib/docs`.
- Product screenshots: `frontend/public/product`.

The public docs contract is recorded in [implemented/85-public-docs-fumadocs/README.md](implemented/85-public-docs-fumadocs/README.md).

## Testing

Use Vitest and Testing Library for components, hooks, API clients, and docs helpers. Use MSW for API mocking. Use Playwright or browser checks when visual behavior, routing, or responsive layout matters.

Test at the level of behavior:

- component tests assert visible content and user actions
- API-client tests assert route, params, response parsing, and error handling
- page tests assert loading, empty, error, and success states
- docs tests assert metadata, raw Markdown, MDX mappings, and generated `/llms.txt`

## Verification

Choose verification by blast radius:

- Touched one component or hook: run the focused test and lint the touched file.
- Touched shared frontend helpers, routes, docs source, or build config: run `npm run typecheck`, `npm run lint`, and `npm run build` from `frontend/`.
- Touched public docs MDX or docs components: run docs-focused tests and a production build, then spot-check representative docs routes in the browser.

## Related Docs

- [implemented/117-visual-system-and-product-polish.md](implemented/117-visual-system-and-product-polish.md)
- [implemented/45-global-command-palette.md](implemented/45-global-command-palette.md)
- [implemented/55-toast-notifications.md](implemented/55-toast-notifications.md)
- [implemented/73-session-keyboard-navigation.md](implemented/73-session-keyboard-navigation.md)
- [implemented/85-browser-page-titles.md](implemented/85-browser-page-titles.md)
- [implemented/85-public-docs-fumadocs/README.md](implemented/85-public-docs-fumadocs/README.md)

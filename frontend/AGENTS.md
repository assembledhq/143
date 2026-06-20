# Frontend AGENTS Guide

This file applies to the entire `frontend/` tree. Follow these patterns strictly to maintain a consistent, Stripe/Shopify-quality dashboard UI.

## Technology Stack

- **Framework:** Next.js (App Router) with React 19 and TypeScript
- **Styling:** Tailwind CSS v4 with oklch color tokens
- **UI Library:** shadcn/ui (new-york style)
- **Data Fetching:** TanStack React Query
- **URL State:** nuqs (for filter/tab state in URL)
- **Icons:** lucide-react
- **Fonts:** Geist Sans (primary), Geist Mono (monospace)

## Design System

### Surface Hierarchy

The shell is the **only** tinted structural surface; every other plane is canvas-colored and separates with borders, not background color. Dark mode inverts the values, never the roles. **Feature panes must not invent their own background color** — pick the surface that matches the pane's role:

| Surface | Token | Role |
|---------|-------|------|
| Shell | `bg-sidebar` + `border-sidebar-border` | Global navigation only: app sidebar, rails, top bars. The single tinted plane. |
| Panel | `bg-panel` | Secondary navigation: session list, file trees, any pane that lists/filters objects. Currently equal to canvas, but always use the token so panes stay declaratively distinct. |
| Canvas | `bg-background` | Primary content — where the user reads and works (softly tinted near-white in light mode) |
| Card | `bg-card` | Grouped content on the canvas — pure white, lifted by border + `shadow-sm`, never by a different gray |

Rules that follow from the ramp:

- Rows on a panel are transparent at rest; the **selected object** is a card-colored chip (`bg-card shadow-sm border-primary/25 ring-primary/10` + a `bg-primary` left bar). Hover uses a muted wash (`hover:bg-muted/50`), since panel and card share a color.
- Active items in the shell are card-colored chips (`bg-card text-foreground shadow-sm ring-1 ring-sidebar-border/60`); inactive shell text is `text-sidebar-foreground/70`, hover restores `text-sidebar-foreground`.
- `bg-muted` / `bg-muted/30` / `bg-muted/50` are for elements *within* a surface (badges, table headers, row hover inside cards) — never as a pane background.
- Never use `bg-muted/30` or `bg-background` to build a sidebar/list pane; that's what `bg-panel` is for.

### Colors: Always Use Theme Tokens

**NEVER use hardcoded Tailwind colors** like `text-gray-*`, `bg-white`, `border-gray-*` in dashboard pages. These break dark mode and create visual inconsistency. Always use semantic theme tokens:

| Purpose | Token | Example |
|---------|-------|---------|
| Primary text | `text-foreground` | Page titles, row text |
| Secondary text | `text-muted-foreground` | Descriptions, metadata |
| Borders | `border-border` | Card borders, dividers |
| Dividers | `divide-border` | List row dividers |
| Card background | `bg-card` | Card, list containers |
| Muted background | `bg-muted` | Badges, disabled inputs |
| Muted hover | `hover:bg-muted/50` | Row hover states |
| Subtle section bg | `bg-muted/30` | Table headers, section headers |
| Primary accent | `text-primary`, `bg-primary` | Active states, links |
| Destructive | `text-destructive` | Errors, delete actions |

### State Colors: Semantic Tokens Only

Status meaning always goes through state tokens — **never raw Tailwind palette classes** (`bg-emerald-500`, `text-amber-700 dark:text-amber-400`, …). The tokens are theme-aware, so they need no `dark:` variants:

| State | Token family | Meaning |
|-------|--------------|---------|
| Success | `success` | Completed, passed, connected, saved |
| Warning | `warning` (amber) | Agent awaiting input, expiring, soft warnings |
| Attention | `attention` (orange) | Needs human guidance, stronger warnings |
| Info | `info` (blue) | Informational, processing |
| Error | `destructive` | Failed, errors, destructive actions |
| Running/active | `primary` | Working sessions, active selections |
| PR/merged | `prMergedAccent` from `@/lib/pr-status-styles` | PR-related accents (violet) |

Usage recipes: dot `bg-success`; text `text-success`; tinted badge `bg-success/10 text-success`; tinted banner `border-success/30 bg-success/10`; solid fill `bg-success text-success-foreground`. Same shapes for `warning`, `attention`, `info`, `destructive`.

**Exceptions** (the only blessed raw-palette uses):

- Diff add/remove coloring in the code-review viewer keeps its conventional green/red palette classes — that is diff semantics, not status.
- Plan-mode amber (plan bubbles in `chat-timeline.tsx`, the Plan Mode chip/composer accents in session detail) is a *mode* accent, not a status, and keeps its amber palette classes with `dark:` variants.
- Violet PR accents are not raw palette either — they must go through `prMergedAccent` in `@/lib/pr-status-styles`, never inline violet classes.

### Typography Scale

The base font size is `text-[13px]` (set globally on `body`). The project uses a strict 7-level type scale. **Do NOT introduce arbitrary pixel sizes** — only `text-[13px]` is permitted as an arbitrary value (it is the body default).

| Token | Tailwind | Pixels | Role |
|-------|----------|--------|------|
| Micro | `text-xs` | 12px | Badges, timestamps, table headers, notification counters, helper text |
| Body | _(inherited)_ | 13px | Default — descriptions, card content, inputs, nav items. Never set explicitly unless overriding a parent. |
| Label | `text-sm` | 14px | Card titles, form labels, emphasized text |
| Subhead | `text-lg` | 18px | Dialog/sheet titles, section headings |
| Title | `text-2xl` | 24px | Page headings (via `PageHeader`) |
| Display | `text-3xl` | 30px | Landing section headings only |
| Hero | `text-[2.75rem]` → `text-6xl` | 44–60px | Landing hero only |

**Allowed font weights (dashboard):**

| Weight | Tailwind | Use for |
|--------|----------|---------|
| Regular | `font-normal` (400) | Body text, descriptions |
| Medium | `font-medium` (500) | Buttons, links, nav items, form labels, badges |
| Semibold | `font-semibold` (600) | All headings — page titles, card titles, dialog titles |

**Banned classes (enforced by ESLint `custom/no-banned-typography`):**

- `text-[9px]`, `text-[10px]`, `text-[11px]`, `text-[12px]`, `text-[14px]`, `text-[15px]`, `text-[16px]` — use `text-xs` or `text-sm` instead
- `font-bold` — use `font-semibold`
- `tracking-widest` — use `tracking-wider`
- `text-xl` — use `text-lg`

#### Common typography recipes

| Context | Classes |
|---------|---------|
| Page title | `text-2xl font-semibold tracking-tight text-foreground` (via `PageHeader`) |
| Page description | `text-sm text-muted-foreground` (via `PageHeader`) |
| Section label | `text-xs font-medium uppercase tracking-wider text-muted-foreground` |
| Card title | `text-sm font-semibold` (via `CardTitle`) |
| Dialog/sheet title | `text-lg font-semibold` |
| Body text | _(inherited 13px — no class needed)_ |
| Badge text | `text-xs font-medium` |
| Metric number | `text-2xl font-semibold tabular-nums` |
| Table header | `text-xs font-medium tracking-wider text-muted-foreground` |
| Small metadata | `text-xs text-muted-foreground` |
| Helper/hint text | `text-xs text-muted-foreground` |
| Labels | `text-[13px]` (via `<Label>` component) |
| Code/mono text | `font-mono text-xs` |

#### Quantitative columns

Numbers that users compare or scan (counts, costs, durations, dates in tables/metric rows) always get `tabular-nums`, and quantitative table columns are **right-aligned — header cell included**. Don't right-align the data cells while leaving the header left-aligned.

### Spacing System

| Context | Pattern |
|---------|---------|
| Page-level section spacing | `space-y-6` |
| Within sections (header → content) | `space-y-3` |
| Within cards | `space-y-4` to `space-y-6` |
| Form field groups (label → input → hint) | `space-y-2` |
| Filter tabs gap | `gap-1` |
| Button groups | `gap-3` |
| Sidebar/panel header padding | `px-4 pt-3 pb-3` — minimum `pb-3` (12px) bottom padding to prevent scrollable content from overlapping with the last header element (e.g., filter tabs, buttons) |
| Gap between fixed header and scrollable list | Always ensure at least 12px (`pb-3`) of bottom padding on the fixed header container so interactive elements (tabs, buttons) are fully visible and not clipped by the scroll area |

### Border Radius

Use the design system's radius tokens: `rounded-lg` (8px) for buttons/inputs, `rounded-xl` (12px) for cards, `rounded-full` for pills/dots.

### Depth & Shadows

Cards use `shadow-sm` by default. Buttons (default/outline/destructive variants) use `shadow-sm`. Inputs use `shadow-sm`. Interactive elements should have smooth transitions (`transition-all duration-150`).

## Page Layout Patterns

### Page Container

Use `PageContainer` (`src/components/page-container.tsx`) for ALL dashboard pages:

```tsx
<PageContainer size="default">
  <div className="space-y-6">
    <PageHeader title="..." description="..." />
    {/* page content */}
  </div>
</PageContainer>
```

Available sizes:

- `narrow` (max-w-3xl): Only for focused single-column forms/modals
- `default` (max-w-5xl): **Standard for most dashboard pages** — use this
- `wide` (max-w-7xl): For data-heavy pages (tables, logs) that benefit from extra horizontal space
- `full` (max-w-none): Only for true full-bleed layouts

Use `size="default"` for most pages. Use `size="wide"` for pages dominated by data tables or tabular content. The outer `<div className="space-y-6">` is mandatory for consistent vertical rhythm.

### Page Header

Use `PageHeader` (`src/components/page-header.tsx`) for ALL page titles:

```tsx
<PageHeader
  title="Page title"
  description="Brief description of the page."
  action={<Button size="sm">Action</Button>}
/>
```

**NEVER** create ad-hoc page headers with `<h1>` tags. Always use the component.

### Settings Pages

Settings pages must use `PageContainer size="default"` with `PageHeader`, matching all other dashboard pages. Do **not** use `SettingsPageFrame` — it wraps content in `size="narrow"` which is inconsistent with the rest of the settings UI.

```tsx
<PageContainer size="default">
  <div className="space-y-6">
    <PageHeader title="Settings" description="..." />
    {/* sections */}
  </div>
</PageContainer>
```

### Section Pattern

For sub-sections within a page (e.g., "Setup", "Execution", "Product context"):

```tsx
<section className="space-y-3">
  <h2 className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">Section title</h2>
  <Card>
    <CardContent>
      {/* section content */}
    </CardContent>
  </Card>
</section>
```

## List Page Patterns

### Filter Tabs

Use `Button` components with variant toggling for filter tabs. **NEVER** use custom underline-style tab buttons.

```tsx
<div className="flex items-center gap-1">
  {tabs.map((tab) => (
    <Button
      key={tab.value}
      variant={currentFilter === tab.value ? "default" : "ghost"}
      size="sm"
      className="text-xs"
      onClick={() => setFilter(tab.value === "all" ? null : tab.value)}
    >
      {tab.label}
      {tab.value === "active" && activeCount > 0 && (
        <span className="ml-1.5 rounded-full bg-primary text-primary-foreground text-xs px-1.5 py-0.5 font-normal">
          {activeCount}
        </span>
      )}
    </Button>
  ))}
</div>
```

### List Rows

Wrap list content in a Card with `p-0` CardContent:

```tsx
<Card>
  <CardContent className="p-0">
    {/* Optional header row */}
    <div className="flex items-center justify-between px-4 py-3 border-b border-border bg-muted/30">
      <span className="text-xs font-medium text-muted-foreground uppercase tracking-wider">
        {count} items
      </span>
    </div>
    {/* Rows */}
    {items.map((item) => (
      <Link
        key={item.id}
        href={`/items/${item.id}`}
        className="flex items-center justify-between py-3 px-4 border-b border-border last:border-b-0 hover:bg-muted/50 transition-colors cursor-pointer"
      >
        {/* Row content */}
      </Link>
    ))}
  </CardContent>
</Card>
```

Key rules:
- Row padding: `py-3 px-4`
- Row borders: `border-b border-border last:border-b-0`
- Hover: `hover:bg-muted/50 transition-colors`
- Section headers (grouped lists): `px-4 py-3 bg-muted/30`
- Section header text: `text-xs font-medium text-muted-foreground uppercase tracking-wider`

### Status Dots

Active/running items use animated ping dots (prefer the `StatusDot` component):
```tsx
<span className="relative flex h-2 w-2">
  <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-primary/60 opacity-75" />
  <span className="relative inline-flex rounded-full h-2 w-2 bg-primary" />
</span>
```

Static status dots use state tokens: `<span className="inline-flex rounded-full h-2 w-2 bg-success" />` (or `bg-warning`, `bg-attention`, `bg-info`, `bg-destructive`)

### Status Badges

Use `<span>` with inline status colors for row status indicators:
```tsx
<span className={`inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium ${status.color}`}>
  {status.label}
</span>
```

### Empty State

Use `EmptyState` (`src/components/empty-state.tsx`) for all empty list/data states:
```tsx
<EmptyState
  icon={CalendarClock}
  title="No items yet"
  description="Items will appear here when..."
  action={{ label: "Create item", href: "/items/new" }}
/>
```

## Form Patterns

### Form Field Layout

```tsx
<div className="space-y-2">
  <Label htmlFor="field-id">Field Label</Label>
  <Input id="field-id" ... />
  <p className="text-xs text-muted-foreground">Helper text explaining the field.</p>
</div>
```

### Field Grid

For side-by-side fields, always use responsive grid:
```tsx
<div className="grid gap-4 md:grid-cols-2">
  {/* field 1 */}
  {/* field 2 */}
</div>
```

**NEVER** use `grid grid-cols-2` without the `md:` breakpoint — fields must stack on mobile.

### Radio Card Groups

For radio groups displayed as selectable cards:
```tsx
<RadioGroup className="grid grid-cols-3 gap-3">
  {options.map((option) => (
    <label
      key={option.value}
      className={`relative flex cursor-pointer flex-col rounded-lg border p-3 shadow-sm transition-all duration-150 ${
        selected === option.value
          ? "border-primary bg-primary/5 ring-1 ring-primary/20"
          : "border-input hover:bg-muted/40 hover:border-border"
      }`}
    >
      <div className="flex items-center gap-2">
        <RadioGroupItem value={option.value} />
        <span className="text-[13px] font-medium">{option.label}</span>
      </div>
      <span className="mt-1 pl-6 text-xs text-muted-foreground">{option.description}</span>
    </label>
  ))}
</RadioGroup>
```

### Save Button Footer

Primary action buttons (Save, Submit) must always be **right-aligned** using `justify-end`. This applies to all settings pages, form footers, and card action rows.

```tsx
<div className="flex items-center justify-end gap-3">
  {saveStatus === "success" && (
    <span className="text-[13px] text-success">Settings saved.</span>
  )}
  {saveStatus === "error" && (
    <span className="text-[13px] text-destructive">Failed to save settings.</span>
  )}
  <Button onClick={handleSave} disabled={mutation.isPending}>
    {mutation.isPending ? "Saving..." : "Save settings"}
  </Button>
</div>
```

### Submit Buttons That Navigate

When a submit/create mutation navigates to a different page on success, do **not** rely only on `mutation.isPending` for the button loading state. React Query clears `isPending` once the mutation settles, but `router.push()`/`router.replace()` can take another render before the current page unmounts. Keep a local navigation-pending flag so the button stays disabled and visibly loading until the user is off the screen.

```tsx
const [isNavigatingAfterSubmit, setIsNavigatingAfterSubmit] = useState(false);

const mutation = useMutation({
  mutationFn: submitForm,
  onMutate: () => {
    setIsNavigatingAfterSubmit(false);
  },
  onSuccess: (response) => {
    setIsNavigatingAfterSubmit(true);
    router.push(`/items/${response.data.id}`);
  },
  onError: () => {
    setIsNavigatingAfterSubmit(false);
  },
});

const isSubmitting = mutation.isPending || isNavigatingAfterSubmit;

<Button onClick={() => mutation.mutate()} disabled={!canSubmit || isSubmitting}>
  {isSubmitting ? "Creating..." : "Create item"}
</Button>
```

If the successful action closes a dialog before navigation, the submit button is already off screen; this extra flag is most important for full-page forms and any still-visible CTA that initiates cross-page navigation.

## Modal & Dialog Patterns

### Modal Action Layout

- Keep modal actions on a single horizontal row in the footer
- Place `Cancel` on the left and the primary action on the right
- For retry flows: `Cancel` (outline) left, `Try Again` (primary) right
- Footer pattern: `flex items-center justify-end gap-2`

### Destructive Confirmations

Always use an `AlertDialog` confirmation for destructive actions (delete, remove, revoke):

```tsx
<AlertDialogFooter>
  <AlertDialogCancel>Cancel</AlertDialogCancel>
  <AlertDialogAction className="bg-destructive text-destructive-foreground hover:bg-destructive/90">
    Delete
  </AlertDialogAction>
</AlertDialogFooter>
```

## Alert & Banner Patterns

### Info/Notice Banners

```tsx
<div className="rounded-md border border-border bg-muted/50 px-4 py-3">
  <p className="text-xs text-muted-foreground">Notice text here.</p>
</div>
```

### Status Banners (in-progress, success, error)

```tsx
{/* In-progress */}
<Card className="border-info/30 bg-info/10">
  <CardContent className="flex items-center gap-3 py-3">
    <RefreshCw className="h-4 w-4 animate-spin text-info" />
    <p className="text-[13px] text-info">Processing...</p>
  </CardContent>
</Card>

{/* Success */}
<div className="flex items-center gap-3 rounded-lg border border-success/30 bg-success/10 px-4 py-3">
  <Check className="h-3.5 w-3.5 text-success" />
  <p className="text-[13px] text-success">Success message.</p>
</div>

{/* Error */}
<div className="rounded-md bg-destructive/10 px-3 py-2 text-[13px] text-destructive">
  Error message.
</div>
```

State tokens (`success`, `warning`, `attention`, `info`, `destructive`) adapt to dark mode automatically — banners built from them must **not** carry `dark:` overrides.

## Component Reference

| Component | Location | Purpose |
|-----------|----------|---------|
| `PageContainer` | `src/components/page-container.tsx` | Page width constraint |
| `PageHeader` | `src/components/page-header.tsx` | Standard page title + description + action |
| `EmptyState` | `src/components/empty-state.tsx` | Empty list/data placeholder |
| `AuthenticatedLayout` | `src/components/authenticated-layout.tsx` | Sidebar + main content shell |
| `StatusDot` | `src/components/status-dot.tsx` | Animated/static status dots |
| `Kbd` | `src/components/ui/kbd.tsx` | Keyboard shortcut hints |

## Keyboard Shortcut Hints

Use the `Kbd` primitive (`src/components/ui/kbd.tsx`) for every keyboard shortcut hint — never hand-roll `<kbd>` styling. Rules:

- Shortcut hints are visual affordances only: `Kbd` renders `aria-hidden`, keeping shortcuts out of accessible names. Don't add your own `aria-label` containing the shortcut.
- Pick the variant for the surface it sits on: `default` (cards, panels, inputs), `inverted` (tooltips, which use `bg-foreground`), `primary` (solid primary/gradient buttons).
- Existing wiring to match: ⌘K on the Search tooltip, `/` in session search, `N` on New session.

## Button Guidelines

| Context | Variant | Size |
|---------|---------|------|
| Primary page action | `default` | `sm` |
| Secondary action / Back | `outline` | `sm` |
| Filter tabs | `default`/`ghost` toggle | `sm` |
| Inline destructive | `ghost` + `text-destructive` | `sm` |
| Save/Submit | `default` | default (h-8) |
| Modal cancel | via `AlertDialogCancel` or `outline` | default |

## Text Casing

**Always use sentence case** for all UI text: headings, section titles, button labels, tab labels, badge text, tooltips, and descriptions. Only capitalize the first word and proper nouns.

| Correct (sentence case) | Wrong (Title Case) |
|---|---|
| `Save settings` | `Save Settings` |
| `Project configuration` | `Project Configuration` |
| `Provider keys` | `Provider Keys` |
| `Pending invitations` | `Pending Invitations` |
| `New project` | `New Project` |

**Exceptions** — always capitalize:
- Proper nouns and product names: GitHub, Linear, Sentry, Claude, Codex, Gemini
- Acronyms: PM, LLM, PR, API
- The word after an acronym stays lowercase: "PM agent", "LLM model", "PR status"

## Prefer Non-Mutating Code

**Default to immutability.** When transforming data, return a new object/array rather than mutating the input in place. This is required for React rendering correctness (referential equality drives re-renders) and for TanStack Query cache integrity (mutating cached data breaks query invalidation).

- Use spread to copy: `{ ...obj, foo: v }`, `[...arr, x]`.
- Use array methods that return new arrays: `map`, `filter`, `concat`, `slice`, `toSorted`, `toReversed`. Avoid in-place mutators: `push`, `pop`, `splice`, `sort`, `reverse`, direct index assignment.
- Never mutate props, `useState` values, or data returned from `useQuery`. Inside `setState`/`queryClient.setQueryData`, return a new value instead of mutating the previous one.
- Prefer `const` and readonly types. If a value really needs to change, replace it rather than mutate it.

**Mutation is the exception, not the default.** Only reach for mutating code when there is a real, measured performance reason — e.g., a hot loop building a large array where each spread would be O(n²). When you do mutate, keep the mutation strictly local to the function and add a short comment explaining why immutability was rejected.

When in doubt, write the immutable version first. It's almost always fast enough and it sidesteps a whole class of stale-render and cache-corruption bugs.

## Settings Mutations: Patch, Don't Replace

Settings-style endpoints (user settings, org settings, per-resource preference documents) use **JSON merge-patch semantics** (RFC 7386): omitted fields keep their stored value, `null` clears a field, and nested objects merge per key. `PATCH /api/v1/auth/me/settings` works this way.

- **Send only the fields the user changed.** A toggle sends `{ diff_viewer_full_screen: true }` — nothing else.
- **Clear a field with an explicit `null`**, not by omitting it: `{ coding_agent_model_default: null }`.
- **Never rebuild the full settings document from the React Query cache** (`{ ...user.settings, changed_field: value }`) and send it as the mutation body. The local cache can be stale, so the write clobbers concurrent edits made in another tab or surface (cross-tab last-write-wins). This was the old contract for `/auth/me/settings` and it caused exactly that bug.
- **When adding a new settings-style endpoint, give it merge-patch semantics on the backend** rather than full-document replace, so callers are never forced into the cache-merge pattern. See `UserStore.MergeSettings` + `models.ApplyUserSettingsMergePatch` for the server-side reference implementation, and the backend rule in `internal/AGENTS.md`.
- If multiple rapid edits to the same patch field are coalesced client-side (in-flight + queued refs), **merge queued patches per key** instead of replacing the queue, so edits to different keys all land.

## Active organization (multi-tenancy)

A user can belong to many orgs and have **a different org open in each browser tab**. Getting this right is a correctness requirement, not a nicety — mixing two orgs' data on one screen is a data-leak-shaped bug. There is exactly one client-side source of truth for "which org is this tab acting as":

> **`src/lib/active-org.ts` — the per-tab `active_org_id` in `sessionStorage`. Read it with `getActiveOrgId()`; change it only with `setActiveOrgId()`.**

Everything else is downstream of that value. Do not introduce a second store (React context, a Zustand slice, a second storage key, a prop drilled from the server) for "current org" — funnel through `active-org.ts`.

How the value flows and why:

- **Every org-scoped request carries it as the `X-Active-Org-ID` header** (`src/lib/api.ts`). EventSource streams can't send headers, so they pass it as the `?org_id=` query param (`src/lib/sse.ts`). If you add a new request path, it must carry the active org one of these two ways.
- **Never rely on the server's `last_org_id` fallback for correctness.** The backend falls back to the session's `last_org_id` only when no header/param is present — and that hint is **shared across all of the user's tabs**, so any sibling tab can change it. A request that omits the header resolves against whatever org another tab last touched. That is the original cause of the "two workspaces blended into one screen" bug.
- **A fresh tab adopts, but never mutates, the shared hint.** `OrgSwitcher` pins the server-resolved active org into this tab's `sessionStorage` on first load (local `setActiveOrgId`, **not** `api.auth.setActiveOrg`) so the tab immediately starts sending an explicit header. Adopting is read-only; only an explicit user switch (`activateOrgAndNavigate`) writes the server-side hint and drags future cold loads along.
- **The React Query cache is scoped to the active org structurally** (`src/components/providers.tsx`): the `QueryClient` is recreated whenever `active_org_id` changes. **Do not encode the org id into individual `queryKey`s** — the boundary lives in the provider, in one place, so no current or future query (or invalidation prefix) has to remember to include it. Org-scoped query keys (`["sessions", …]`, `["integrations"]`, `["repositories"]`, etc.) stay org-free on purpose; the surrounding client is what makes them org-correct.

When you touch anything org-aware, the test is: *could a request resolve to the wrong org because the header was missing, or could org A's cached data survive into an org-B client?* If either is possible, route the org through `active-org.ts` and let the provider own cache isolation.

## Error Reporting (Sentry)

Errors are reported to Sentry via `@sentry/nextjs`. Three layers handle this automatically:

1. **`sentry.client.config.ts`** — catches unhandled browser errors and promise rejections
2. **`src/app/global-error.tsx`** — Next.js root error boundary (catches rendering errors outside the app layout)
3. **`src/components/error-boundary.tsx`** — React error boundary for component-level crashes

For **caught errors** (try/catch, error callbacks), use the helpers in `src/lib/errors.ts`:

```tsx
import { captureError, captureMessage } from "@/lib/errors";

// In a catch block — error is still handled, but Sentry gets visibility
try {
  await riskyOperation();
} catch (err) {
  captureError(err, { feature: "session-polling" });
  // show fallback UI
}

// For unexpected-but-not-crashing states
if (!expectedData) {
  captureMessage("Missing expected data", { endpoint: "/api/sessions" });
}
```

Use the `tags` parameter to add searchable context (feature name, endpoint, component). Do **not** call `Sentry.*` directly — always use the helpers so error reporting stays centralized.

## Frontend Performance Guardrails

- Treat text-entry surfaces as hot paths. Composers, search inputs, filters, and settings fields should update only the smallest subtree necessary for each keystroke.
- When an input sits next to heavier chrome (repo pickers, tables, timelines, charts, drawers, syntax highlighters), isolate that chrome behind a memoized child boundary so unrelated controls do not rerender while typing.
- Add a focused render-isolation test for interactive surfaces with known heavy siblings. The test should type into the input and assert that an unrelated expensive child did not rerender.
- Prefer `*.performance.test.tsx` for these checks when the file is primarily about render churn, polling isolation, or typing latency rather than feature correctness.
- If a component adds polling, subscriptions, `ResizeObserver`, `scroll` listeners, or large list filtering near an input path, call that out in the PR and verify cleanup behavior in tests.

## Anti-Patterns to Avoid

1. **Hardcoded colors** — Never `text-gray-*`, `bg-white`, `border-gray-*` in dashboard. Use tokens.
2. **Custom tab implementations** — Never build underline tabs manually. Use `Button` variant toggling.
3. **Ad-hoc page headers** — Never use `<h1>` directly. Use `PageHeader` component.
4. **Non-responsive grids** — Never `grid grid-cols-2` without `md:` breakpoint.
5. **Missing PageContainer** — Every dashboard page must be wrapped in `PageContainer`.
6. **Inconsistent container sizes** — Use `size="default"` for most pages and `size="wide"` for data-table-heavy pages. Never use `size="narrow"` for regular dashboard pages.
7. **Inconsistent row padding** — Always `py-3.5 px-4` for list rows.
8. **Raw palette status colors** — Never `bg-emerald-500`, `text-amber-700 dark:text-amber-400`, `bg-blue-50` for status meaning. Use the state tokens (`success`/`warning`/`attention`/`info`/`destructive`), which adapt to dark mode automatically.
9. **Flat cards** — Cards should always have `shadow-sm` (provided by the Card component). Don't override with `shadow-none`.
10. **Missing transitions** — Interactive elements (radio cards, buttons, rows) need `transition-all duration-150`.
11. **Insufficient header-to-scroll-area spacing** — Fixed header sections above scrollable content must have at least `pb-3` (12px) bottom padding. Using `pb-2` or less causes the scroll area to overlap with the last header element (e.g., filter tabs, buttons), clipping their bottom border or active indicator.
12. **Full-document settings writes** — Never spread the cached settings object into a mutation body to "preserve" unchanged fields. Settings endpoints are merge patches; send only the changed fields (see "Settings Mutations: Patch, Don't Replace").

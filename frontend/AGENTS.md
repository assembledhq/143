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

**Exception:** Status-specific colors (blue for active, green for success, red for error, orange for warning) may use Tailwind color classes like `bg-blue-500` since these are semantic status colors, not arbitrary grays.

### Typography Scale

The base font size is `text-[13px]` (set globally). Use these specific sizes:

| Element | Classes |
|---------|---------|
| Page title | `text-xl font-semibold tracking-tight text-foreground` (via `PageHeader`) |
| Page description | `text-sm text-muted-foreground` (via `PageHeader`) |
| Section label | `text-xs font-semibold uppercase tracking-wider text-muted-foreground` |
| Card title | `text-sm font-semibold` (via `CardTitle`) |
| Body text | `text-[13px]` (default) |
| Small metadata | `text-[11px]` (badges, timestamps, counts) |
| Helper/hint text | `text-xs text-muted-foreground` |
| Labels | `text-[13px]` (via `<Label>` component) |

### Spacing System

| Context | Pattern |
|---------|---------|
| Page-level section spacing | `space-y-6` |
| Within sections (header → content) | `space-y-3` |
| Within cards | `space-y-4` to `space-y-6` |
| Form field groups (label → input → hint) | `space-y-2` |
| Filter tabs gap | `gap-1` |
| Button groups | `gap-3` |

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

**All pages MUST use the same `size="default"` (max-w-5xl)** to ensure consistent margins across the app. This is critical — using different sizes creates jarring layout shifts when navigating between pages.

- `narrow` (max-w-3xl): Only for focused single-column forms/modals
- `default` (max-w-5xl): **Standard for all dashboard pages** — use this
- `wide` (max-w-5xl): Alias for default (kept for backwards compat)
- `full` (max-w-none): Only for true full-bleed layouts

**NEVER use `size="wide"` or `size="narrow"` for regular dashboard pages.** The outer `<div className="space-y-6">` is mandatory for consistent vertical rhythm.

### Page Header

Use `PageHeader` (`src/components/page-header.tsx`) for ALL page titles:

```tsx
<PageHeader
  title="Page Title"
  description="Brief description of the page."
  action={<Button size="sm">Action</Button>}
/>
```

**NEVER** create ad-hoc page headers with `<h1>` tags. Always use the component.

### Settings Pages

Use `SettingsPageFrame` for settings-style pages that combine PageContainer + PageHeader:

```tsx
<SettingsPageFrame title="Settings" description="...">
  {/* sections */}
</SettingsPageFrame>
```

### Section Pattern

For sub-sections within a page (e.g., "Setup", "Execution", "Product Context"):

```tsx
<section className="space-y-3">
  <h2 className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">Section Title</h2>
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
        <span className="ml-1.5 rounded-full bg-blue-500 text-white text-[10px] px-1.5 py-0.5 font-normal">
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

Active/running items use animated ping dots:
```tsx
<span className="relative flex h-2 w-2">
  <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-blue-400 opacity-75" />
  <span className="relative inline-flex rounded-full h-2 w-2 bg-blue-500" />
</span>
```

Static status dots: `<span className="inline-flex rounded-full h-2 w-2 bg-{color}-500" />`

### Status Badges

Use `<span>` with inline status colors for row status indicators:
```tsx
<span className={`inline-flex items-center rounded-full px-2 py-0.5 text-[11px] font-medium ${status.color}`}>
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
  action={{ label: "Create Item", href: "/items/new" }}
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

For pages with save actions:
```tsx
<div className="flex items-center gap-3">
  <Button onClick={handleSave} disabled={mutation.isPending}>
    {mutation.isPending ? "Saving..." : "Save Settings"}
  </Button>
  {saveStatus === "success" && (
    <span className="text-[13px] text-green-600">Settings saved.</span>
  )}
  {saveStatus === "error" && (
    <span className="text-[13px] text-destructive">Failed to save settings.</span>
  )}
</div>
```

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
<Card className="border-blue-200 bg-blue-50 dark:border-blue-800 dark:bg-blue-950/30">
  <CardContent className="flex items-center gap-3 py-3">
    <RefreshCw className="h-4 w-4 animate-spin text-blue-600 dark:text-blue-400" />
    <p className="text-[13px] text-blue-800 dark:text-blue-300">Processing...</p>
  </CardContent>
</Card>

{/* Success */}
<div className="flex items-center gap-3 rounded-lg border border-green-200 bg-green-50 px-4 py-3 dark:border-green-800 dark:bg-green-950/30">
  <Check className="h-3.5 w-3.5 text-green-700 dark:text-green-400" />
  <p className="text-[13px] text-green-800 dark:text-green-300">Success message.</p>
</div>

{/* Error */}
<div className="rounded-md bg-destructive/10 px-3 py-2 text-[13px] text-destructive">
  Error message.
</div>
```

Always include `dark:` variants for colored banners.

## Component Reference

| Component | Location | Purpose |
|-----------|----------|---------|
| `PageContainer` | `src/components/page-container.tsx` | Page width constraint |
| `PageHeader` | `src/components/page-header.tsx` | Standard page title + description + action |
| `SettingsPageFrame` | `src/components/settings-page-frame.tsx` | Settings page wrapper (PageContainer + PageHeader) |
| `EmptyState` | `src/components/empty-state.tsx` | Empty list/data placeholder |
| `AuthenticatedLayout` | `src/components/authenticated-layout.tsx` | Sidebar + main content shell |

## Button Guidelines

| Context | Variant | Size |
|---------|---------|------|
| Primary page action | `default` | `sm` |
| Secondary action / Back | `outline` | `sm` |
| Filter tabs | `default`/`ghost` toggle | `sm` |
| Inline destructive | `ghost` + `text-destructive` | `sm` |
| Save/Submit | `default` | default (h-8) |
| Modal cancel | via `AlertDialogCancel` or `outline` | default |

## Anti-Patterns to Avoid

1. **Hardcoded colors** — Never `text-gray-*`, `bg-white`, `border-gray-*` in dashboard. Use tokens.
2. **Custom tab implementations** — Never build underline tabs manually. Use `Button` variant toggling.
3. **Ad-hoc page headers** — Never use `<h1>` directly. Use `PageHeader` component.
4. **Non-responsive grids** — Never `grid grid-cols-2` without `md:` breakpoint.
5. **Missing PageContainer** — Every dashboard page must be wrapped in `PageContainer`.
6. **Inconsistent container sizes** — All dashboard pages MUST use `size="default"`. Never use `size="wide"` or `size="narrow"` for regular pages — this creates different margins between pages.
7. **Inconsistent row padding** — Always `py-3.5 px-4` for list rows.
8. **Missing dark mode** — All colored banners/alerts need `dark:` variant classes.
9. **Flat cards** — Cards should always have `shadow-sm` (provided by the Card component). Don't override with `shadow-none`.
10. **Missing transitions** — Interactive elements (radio cards, buttons, rows) need `transition-all duration-150`.

# Design: Runtime Settings UI Refresh

> **Status:** Not Started | **Last reviewed:** 2026-06-11

The Runtime settings page already centralizes sandbox networking, capacity, session lifecycle, cleanup, and resource bounds. The next UI pass should preserve that information architecture while making the page easier to scan and safer to edit.

The recommended direction combines a top-level policy summary with progressive disclosure for advanced resource caps. This keeps the page useful for admins who only need to confirm current runtime policy, while keeping exact CPU, memory, and disk limits available for teams that tune preview infrastructure.

## Goals

1. Make the current runtime policy readable at a glance.
2. Keep common controls visible: networking, concurrency, preview count, session length, cleanup defaults, sandbox tiers, and repository resource request behavior.
3. Move exact CPU, memory, and disk caps into an advanced resource section so technical limits do not dominate the page.
4. Standardize setting rows so labels, descriptions, units, ranges, controls, and autosave state align across the page.
5. Keep the existing backend settings payloads and autosave behavior.
6. Preserve mobile usability with stacked rows and full-width controls where needed.

## Non-Goals

1. Introducing preset policies such as "Cost controlled" or "High throughput".
2. Adding worker fleet diagnostics or runtime health internals.
3. Changing runtime scheduling semantics.
4. Changing settings persistence from autosave to explicit page-level save.
5. Adding new backend fields for this UI pass.

## Product Model

The page should read as a runtime policy editor, not as a loose collection of infrastructure knobs.

Primary admin questions:

- Is static egress enabled and what IP should I allowlist?
- How many expensive runtime objects can the organization create?
- How long do agent runs and preview sandboxes stay alive?
- What sandbox sizes are used by default?
- Can repositories request larger preview resources, and what are the upper bounds?

The page should answer the first four questions without expanding anything. The final question has a simple visible toggle plus advanced details.

## Layout

### Page Structure

```text
Runtime
Configure sandbox networking, capacity, and lifecycle defaults.

[Policy summary]

Sandbox network
[card]

Capacity
[card]

Sessions and cleanup
[card]

Sandbox defaults
[card]

Advanced resource limits
[collapsible card]
```

Use the existing `PageContainer`, `PageHeader`, `Card`, `CardContent`, `Button`, `Input`, `Select`, `Switch`, `Tooltip`, and `AutosaveIndicator` primitives. Use semantic design tokens only.

### Policy Summary

The summary is read-only and derived from the same settings rendered below. It should update immediately from local autosave state where practical, falling back to server settings while queries load.

Desktop:

```text
┌────────────────────┬────────────────────┬────────────────────┬────────────────────┐
│ Agent runs          │ Active previews     │ Session max         │ Preview idle TTL    │
│ 5 concurrent        │ 4 per user          │ 25 minutes          │ 4 hours             │
│ Org-wide cap        │ Per-user cap        │ Agent turn limit    │ Auto-stop preview   │
└────────────────────┴────────────────────┴────────────────────┴────────────────────┘
```

Mobile:

```text
┌──────────────────────────────────────┐
│ Agent runs                 5         │
│ Org-wide concurrent cap              │
├──────────────────────────────────────┤
│ Active previews            4/user    │
│ Per-user active preview cap          │
├──────────────────────────────────────┤
│ Session max                25 min    │
│ Agent turn runtime limit             │
├──────────────────────────────────────┤
│ Preview idle TTL           4 hr      │
│ Auto-stop inactive previews          │
└──────────────────────────────────────┘
```

The summary should not be visually heavier than editable sections. It is a compact status band, not a dashboard.

### Standard Setting Row

Most controls should use one shared row pattern.

Desktop:

```text
┌──────────────────────────────────────────────────────────────────────┐
│ Label [?]                                      [ control       unit ] │
│ Short description explaining operational effect.       Range 1-25     │
└──────────────────────────────────────────────────────────────────────┘
```

Mobile:

```text
┌──────────────────────────────────────┐
│ Label [?]                            │
│ Short description.                   │
│ [ control                         ]  │
│ Range 1-25                           │
└──────────────────────────────────────┘
```

Rules:

- Labels stay short and concrete.
- Descriptions sit inline under labels instead of hiding all meaning in tooltips.
- Tooltips are for caveats, not the only explanation.
- Numeric controls use fixed widths on desktop so rows align.
- Units render as an attached suffix or a fixed-width adjacent label.
- Min/max or range text is visible for bounded numeric values.
- Autosave indicators remain at section level.

## Wireframes

### Desktop

```text
Runtime
Configure sandbox networking, capacity, and lifecycle defaults.

┌──────────────────┬──────────────────┬──────────────────┬──────────────────┐
│ Agent runs       │ Active previews  │ Session max      │ Preview idle TTL │
│ 5 concurrent     │ 4 per user       │ 25 minutes       │ 4 hours          │
│ Org-wide cap     │ Per-user cap     │ Agent turn limit │ Auto-stop        │
└──────────────────┴──────────────────┴──────────────────┴──────────────────┘

Sandbox network                                               Saved
┌──────────────────────────────────────────────────────────────────────┐
│ Static egress IP [?]                              [ toggle on ]      │
│ Routes new and resumed sandboxes through one stable public IP.       │
│                                                                      │
│ Public IP                                  203.0.113.10      [copy]  │
└──────────────────────────────────────────────────────────────────────┘

Capacity                                                      Saved
┌──────────────────────────────────────────────────────────────────────┐
│ Concurrent agent runs [?]                         [ - ][ 5 ][ + ]    │
│ Maximum coding-agent turns that can run at the same time. Range 1-25 │
│                                                                      │
│ Active previews per user [?]                      [ - ][ 4 ][ + ]    │
│ Maximum preview environments one user can keep running. Range 1-20   │
└──────────────────────────────────────────────────────────────────────┘

Sessions and cleanup                                          Saved
┌──────────────────────────────────────────────────────────────────────┐
│ Maximum session length [?]                       [ 25        min ]   │
│ Stops an agent turn when it exceeds this org limit. Range 2-120 min  │
│                                                                      │
│ Keep completed sessions for [?]                  [ 60        min ]   │
│ Keeps completed sandboxes available before cleanup. Range 0-1440 min │
│                                                                      │
│ Idle preview timeout [?]                         [ 240       min ]   │
│ Stops previews after this long without activity. Range 15-1440 min   │
│                                                                      │
│ Keep sandbox while preview is active [?]          [ toggle on ]      │
│ Preserves the sandbox while a preview is still running.              │
│                                                                      │
│ Agent tab tools [?]                              [ toggle on ]       │
│ Allows sibling agent tabs to coordinate through 143 tools.           │
└──────────────────────────────────────────────────────────────────────┘

Sandbox defaults                                              Saved
┌──────────────────────────────────────────────────────────────────────┐
│ Agent sandbox size [?]                           [ Standard      v ] │
│ Default sandbox tier for new coding-agent sessions.                  │
│                                                                      │
│ Preview sandbox size [?]                         [ Standard      v ] │
│ Default tier when repository config does not request one.            │
│                                                                      │
│ Allow repository resource requests [?]            [ toggle on ]      │
│ Repos may request preview CPU, memory, and disk up to org limits.    │
└──────────────────────────────────────────────────────────────────────┘

Advanced resource limits                                      Saved
┌──────────────────────────────────────────────────────────────────────┐
│ Largest preview size [?]                         [ Large         v ] │
│ Largest sandbox tier repository preview config can request.          │
│                                                                      │
│ Preview CPU limit [?]                            [ 2       cores ]   │
│ Largest CPU request a preview config can make. Range 0.25-2 cores    │
│                                                                      │
│ Preview memory limit [?]                         [ 8192      MiB ]   │
│ Largest memory request a preview config can make. Range 512-8192 MiB │
│                                                                      │
│ Preview disk limit [?]                           [ 10240     MiB ]   │
│ Largest temp disk request a preview config can make. Range 1024-10240│
└──────────────────────────────────────────────────────────────────────┘
```

### Collapsed Advanced Section

The default state may be collapsed if user testing shows the page still feels too technical. If collapsed, the section header should include the most important current bound.

```text
Advanced resource limits                         Large max · 2 cores · 8 GiB  [Expand]
┌──────────────────────────────────────────────────────────────────────┐
│ Exact CPU, memory, and disk caps for repository-requested previews.  │
└──────────────────────────────────────────────────────────────────────┘
```

Expanded:

```text
Advanced resource limits                                                [Collapse]
┌──────────────────────────────────────────────────────────────────────┐
│ Largest preview size                          [ Large             v ]│
│ Preview CPU limit                             [ 2          cores   ]│
│ Preview memory limit                          [ 8192       MiB     ]│
│ Preview disk limit                            [ 10240      MiB     ]│
└──────────────────────────────────────────────────────────────────────┘
```

### Mobile

```text
Runtime
Configure sandbox networking, capacity, and lifecycle defaults.

┌──────────────────────────────────────┐
│ Agent runs                    5      │
│ Org-wide concurrent cap              │
├──────────────────────────────────────┤
│ Active previews               4/user │
│ Per-user active preview cap          │
├──────────────────────────────────────┤
│ Session max                   25 min │
│ Agent turn runtime limit             │
├──────────────────────────────────────┤
│ Preview idle TTL              4 hr   │
│ Auto-stop inactive previews          │
└──────────────────────────────────────┘

Sandbox network                         Saved
┌──────────────────────────────────────┐
│ Static egress IP [?]       [toggle]  │
│ Routes sandboxes through one stable  │
│ public IP.                           │
│                                      │
│ Public IP                            │
│ 203.0.113.10                  [copy] │
└──────────────────────────────────────┘

Capacity                                Saved
┌──────────────────────────────────────┐
│ Concurrent agent runs [?]             │
│ Maximum concurrent agent turns.       │
│ [ - ] [ 5                 ] [ + ]     │
│ Range 1-25                            │
│                                      │
│ Active previews per user [?]          │
│ Maximum running previews per user.    │
│ [ - ] [ 4                 ] [ + ]     │
│ Range 1-20                            │
└──────────────────────────────────────┘

Sessions and cleanup                    Saved
┌──────────────────────────────────────┐
│ Maximum session length [?]            │
│ [ 25                         min ]   │
│ Range 2-120 minutes                   │
│                                      │
│ Keep completed sessions for [?]       │
│ [ 60                         min ]   │
│ Range 0-1440 minutes                  │
│                                      │
│ Idle preview timeout [?]              │
│ [ 240                        min ]   │
│ Range 15-1440 minutes                 │
│                                      │
│ Keep sandbox while preview is active  │
│ [toggle]                              │
│                                      │
│ Agent tab tools                       │
│ [toggle]                              │
└──────────────────────────────────────┘

Sandbox defaults                        Saved
┌──────────────────────────────────────┐
│ Agent sandbox size [?]                │
│ [ Standard                       v ]  │
│                                      │
│ Preview sandbox size [?]              │
│ [ Standard                       v ]  │
│                                      │
│ Allow repository resource requests    │
│ [toggle]                              │
└──────────────────────────────────────┘

Advanced resource limits                [Expand]
┌──────────────────────────────────────┐
│ Large max · 2 cores · 8 GiB           │
│ Exact caps for requested previews.    │
└──────────────────────────────────────┘
```

## Interaction Details

### Numeric Controls

Use one consistent numeric-control treatment.

Preferred control:

- Fixed-width `Input type="number"` with attached unit suffix.
- Optional small decrement/increment icon buttons for short integer ranges such as concurrent runs and previews per user.
- Commit and clamp on blur, matching the existing autosave behavior.
- Keep debounced autosave only where current hooks already do it safely.

Avoid sliders for these fields in the first pass. Sliders make sense for subjective settings, but these are exact operational limits where typing the number is faster and less ambiguous.

### Selects

Tier selects should remain selects:

- `Small`
- `Standard`
- `Large`

Do not introduce custom tier cards until the product has tier descriptions or pricing/capacity metadata to justify the extra footprint.

### Switch Rows

Switch rows should follow the same row pattern as numeric rows. The switch stays right-aligned on desktop and sits below the description on mobile if needed.

### Autosave and Errors

Keep section-level `AutosaveIndicator` in each editable section header. If a save fails, the affected row should eventually be able to show a compact row-level error, but the initial UI refresh can continue using the existing autosave error surface.

### Loading State

The summary band should show skeleton values or muted placeholders while settings are loading:

```text
Agent runs        --
Active previews   --
Session max       --
Preview idle TTL  --
```

Editable controls should continue using current query defaults only where they match server defaults. Avoid showing misleading configured-looking values if the settings query is still pending.

### Empty or Unavailable Network State

When static egress is enabled but unavailable:

```text
Static egress is not currently available for new sandbox starts.
```

Do not expose worker capability reasons or gateway internals in customer-facing UI.

When no public IP is configured:

```text
Public IP    Not configured
```

The copy button should be disabled or hidden when there is no value.

## Component Guidance

Add small local components in the Runtime page or a shared settings component module if another settings page adopts the pattern:

- `RuntimeSummary`
- `SettingsSection`
- `SettingRow`
- `NumericSetting`
- `SwitchSetting`
- `SelectSetting`
- `AdvancedSection`

`SettingRow` should own the desktop/mobile alignment contract so future runtime settings do not regress into uneven per-field layouts.

Suggested row props:

```ts
type SettingRowProps = {
  id: string;
  label: string;
  description: string;
  tooltip?: string;
  helper?: string;
  children: React.ReactNode;
};
```

`NumericSetting` can wrap the existing `useAutosaveNumericField` hook and standardize input width, unit rendering, min/max helper copy, and optional stepper buttons.

## API Contract

No API changes are required.

The page continues reading:

- `GET /api/v1/settings`
- `GET /api/v1/settings/network`

The page continues writing through:

- `PATCH /api/v1/settings`

Settings fields remain:

- `settings.sandbox_network.static_egress_enabled`
- `settings.max_concurrent_runs`
- `settings.preview_max_previews_per_user`
- `settings.max_session_duration_seconds`
- `settings.coding_agent_tab_tools_enabled`
- `settings.sandbox_lifecycle.completed_session_retention_minutes`
- `settings.sandbox_lifecycle.idle_preview_ttl_minutes`
- `settings.sandbox_lifecycle.preview_holds_sandbox`
- `settings.sandbox_resources.agent_default_tier`
- `settings.sandbox_resources.preview_default_tier`
- `settings.sandbox_resources.allow_repo_resource_requests`
- `settings.sandbox_resources.preview_max_tier`
- `settings.sandbox_resources.preview_max_cpu_millis`
- `settings.sandbox_resources.preview_max_memory_mib`
- `settings.sandbox_resources.preview_max_ephemeral_disk_mib`

RBAC remains admin-only through the existing settings layout guard and backend authorization.

## Database Schema

No schema changes are required.

The UI uses existing organization settings JSON fields. Multi-tenancy behavior is unchanged because all settings reads and writes remain scoped to the authenticated organization.

## Test Plan

Frontend tests should cover:

1. The policy summary renders derived values from settings.
2. Numeric rows render visible ranges and unit labels.
3. Stepper buttons, if implemented, clamp to min/max and autosave the clamped value.
4. Advanced resource limits can be expanded and collapsed without losing current input values.
5. Static egress hides or disables copy when no public IP is configured.
6. Mobile-friendly structure remains stacked rather than table-like.
7. Existing autosave behavior still patches the same settings payloads.

Run from `frontend/` after implementation:

```bash
npm run typecheck
npm run lint
npm run build
```

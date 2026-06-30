# 104 - Settings Simplification

> **Status:** Implemented | **Last reviewed:** 2026-06-30

> **Applies to:** Next.js settings routes under
> `frontend/src/app/(dashboard)/settings`

## Purpose

The settings area is becoming hard to scan because it mixes personal setup,
organization administration, external service connections, runtime policy,
security controls, observability, and eval workflows in one expandable sidebar.
Many pages are individually reasonable, but the information architecture makes
users learn implementation boundaries before they can find the control they
need.

This design defines a settings simplification plan that can be implemented in
small route and navigation changes without requiring backend schema changes.

## Product Goals

1. Make the settings sidebar match user intent rather than backend subsystem
   names.
2. Keep the existing **Coding agents** label. It is understandable and should
   remain the primary product label for agent configuration.
3. Separate settings from active workflows. Configuration belongs in Settings;
   work creation, benchmarking, and review workflows should either live near
   their owning feature or stay in a lower-prominence Settings area until they
   are first-class product surfaces.
4. Make advanced admin controls available without forcing every admin to scan
   them during basic setup.
5. Preserve the current role-based access model: admins can manage org-wide
   settings, members can see or use allowed collaborative surfaces, builders
   keep narrower setup access, and viewers stay read-only or redirected.

## Current Issues

### Sidebar Grouping Is Too Broad

The current groups are Personal, Platform, and Organization. The Platform group
contains integrations, Coding agents, app-level LLM configuration, Autopilot,
Runtime, Preview, API keys, and Evals. Those items are not one mental category
for users.

### Evals Are Not A First-Class Settings Item

The Evals page includes creation, bootstrap scans, candidate review, run
launching, and comparison details. Those are active workflows, but Evals is not
yet a first-class top-level product surface. Keeping Evals in Settings is fine
for now, but it should be lower-prominence than core setup and admin controls.

### Model Configuration Has Two Namespaces

Users have to distinguish between:

- **Coding agents**: credentials and fallback behavior for coding-agent
  sessions.
- **LLM**: app-level models and provider keys for generated titles, PR
  descriptions, validation, prioritization, and project generation.

The distinction is valid, but the label "LLM" is terse and easy to confuse with
coding-agent model defaults.

### Runtime And Preview Settings Overlap

Runtime controls include sandbox networking, capacity, cleanup, tab tools, and
resource sizes. Preview settings include auto-preview policy and preview
secrets. Users are likely to think of both as runtime behavior, while preview
secrets also have a security/credential feel.

### Personal And Organization Agent Setup Is Split

My settings shows personal Coding agent auths, org fallback rows, CLI sessions,
and personal defaults. Coding agents shows the organization-level auth stack.
The split is technically correct, but users are really trying to answer one
question: "what credentials and defaults will my coding sessions use?"

## Proposed Navigation

Replace the existing sidebar groups with intent-based groups:

| Group | Items | Notes |
| --- | --- | --- |
| Personal | Account | Keep personal profile, appearance, personal Coding agent auths, CLI sessions, and personal defaults together unless a later implementation creates a dedicated personal agents route. |
| Connections | Integrations | External services such as GitHub, Sentry, Linear, Slack, Notion, CircleCI, and log providers. |
| Agents | Coding agents, App LLM, Autopilot | Keep "Coding agents" as-is. Rename "LLM" to "App LLM" to distinguish app support models from coding-agent credentials. |
| Runtime | Sandboxes, Previews | Rename "Runtime" to "Sandboxes" in the sidebar while the page title can remain "Runtime" if needed during migration. Keep Preview near runtime behavior. |
| Security & Admin | Organization, Team, API keys | Put org identity, membership, and machine access in one admin-oriented group. |
| Operations | Usage, Audit log, Evals | Keep read-only operating views and non-first-class eval tooling separate from first-run setup. |

### Route Mapping

The first implementation can preserve most existing routes and change labels
only where necessary:

| Current route | Proposed label | Route change |
| --- | --- | --- |
| `/settings/account` | Account | Keep |
| `/settings/integrations` | Integrations | Keep |
| `/settings/agent` | Coding agents | Keep |
| `/settings/llm` | App LLM | Keep initially; optional future alias `/settings/app-llm` |
| `/settings/autopilot` | Autopilot | Keep |
| `/settings/runtime` | Sandboxes | Keep initially; optional future alias `/settings/sandboxes` |
| `/settings/previews` | Previews | Keep |
| `/settings` | Organization | Keep route, rename label/page title |
| `/settings/team` | Team | Keep |
| `/settings/api-keys` | API keys | Keep |
| `/settings/usage` | Usage | Keep |
| `/settings/audit-log` | Audit log | Keep |
| `/settings/evals` | Evals | Keep in Settings under Operations |

## Page-Level Changes

### Account

Keep Account as the personal entry point. The page should answer:

- which personal Coding agent auths run before org fallback
- what org fallback exists, read-only for non-admins
- what CLI/session auth is available
- what personal default model and reasoning settings are used

No label change is needed. If the page continues to grow, split the visible
sections with tabs:

- `Auths`
- `Defaults`
- `CLI`

### Coding Agents

Keep the page label and title as **Coding agents**. The page should remain the
organization-wide place to manage shared coding-agent credentials and fallback
priority.

Recommended simplifications:

- Keep the existing Runtime cross-link.
- Add a reciprocal link to Account for personal auths.
- Use one short explainer near the top: personal auths run first, then the org
  Coding agents fallback.
- Keep stack details in the sheet, not expanded inline.

### App LLM

Rename the sidebar label and page title from **LLM** to **App LLM**. The page
description should say these models power app-generated content and background
analysis, not coding-agent execution.

Recommended copy:

> Configure models for app-generated titles, PR descriptions, validation,
> prioritization, and project generation. Coding-agent credentials are managed
> separately on Coding agents.

Add a secondary link to Coding agents.

### Autopilot

Keep the route and label. The page currently has a clear structure:

- PM configuration
- Execution
- Repository overrides

Future simplification should only happen if Autopilot grows. At that point,
hide repository override details behind a "View repository overrides" control
or move repo-specific overrides to repository settings.

### Sandboxes / Runtime

Use **Sandboxes** as the sidebar label because it describes what admins are
controlling. The page can keep the title "Runtime" for one release if a softer
migration is preferred.

Default visible sections:

- Sandbox network
- Capacity
- Sandbox defaults

Move or collapse by default:

- Sessions and cleanup
- Agent tab tools
- Advanced resource limits

Advanced controls should remain searchable and accessible, but not compete with
first-run capacity and networking setup.

### Previews

Keep Previews near Sandboxes. The current tabs should be renamed for clarity:

- `Auto-start policy`
- `Secret bundles`

If preview secrets continue to grow, move Secret bundles to Security & Admin or
create a Security page that links back to Previews.

### Integrations

Keep Integrations under Connections. The index page should focus on connection
state and next action:

- connected providers
- providers needing attention
- connect buttons for unavailable providers
- concise summaries for connected providers

Provider-specific configuration should live in detail sheets or provider detail
routes. This keeps the integration index from becoming a long admin console.

### Organization

Rename "General settings" to **Organization**. Keep organization name and
organization-wide identity controls here.

Move PR behavior settings out of Organization if more shipping controls are
added. A future **Pull requests** or **Shipping** page would be a better home
for PR authorship, draft defaults, auto-archive behavior, and builder PR review
requirements.

### Usage And Audit Log

Keep both in Operations. They are read-only operational views, not setup
steps. They should stay out of the main setup-oriented groups.

## Evals Placement

Keep Evals in Settings because it is not yet a first-class top-level product
surface. The simplification goal is to make it lower-prominence than core
setup, not to promote it.

### Proposed Route Treatment

| Current route | Proposed treatment |
| --- | --- |
| `/settings/evals` | Keep |
| `/settings/evals/new` | Keep |
| `/settings/evals/[id]` | Keep |
| `/settings/evals/batch/[id]` | Keep |

### Placement Requirements

- Preserve role guards: admins and members can access, viewers and builders are
  blocked unless the product policy changes.
- Keep Evals visually separate from first-run setup controls.
- Place Evals under Operations or another lower-prominence advanced group.
- Do not add top-level app navigation for Evals until it becomes a first-class
  product surface.

## Implementation Plan

### Phase 1: Navigation And Labels

1. Update `SidebarSettingsSection` groups and labels.
2. Rename the `/settings/llm` visible label and page title to "App LLM".
3. Rename the `/settings` visible label and page title to "Organization".
4. Rename the `/settings/runtime` sidebar label to "Sandboxes".
5. Update settings layout role guard tests for any new visible labels.

No route changes are required in this phase.

### Phase 2: Runtime Progressive Disclosure

1. Keep Sandbox network, Capacity, and Sandbox defaults visible.
2. Collapse Sessions and cleanup by default.
3. Keep Advanced resource limits collapsed by default.
4. Consider moving Agent tab tools into an Advanced section if it remains a
   rarely changed setting.

### Phase 3: Preview Wording

1. Rename Preview tabs to "Auto-start policy" and "Secret bundles".
2. Review copy so preview secret bundles are clearly scoped to preview
   runtimes, not general coding-agent credentials.

### Phase 4: Evals De-Emphasis

1. Keep `/settings/evals/**` routes unchanged.
2. Move Evals into the Operations or lower-prominence advanced sidebar group.
3. Update settings sidebar tests for the new grouping.
4. Avoid adding top-level navigation until Evals is promoted as a first-class
   product area.

### Phase 5: Optional Route Aliases

After labels settle, decide whether route aliases are worth adding:

- `/settings/app-llm` -> `/settings/llm`
- `/settings/sandboxes` -> `/settings/runtime`

If aliases are added, prefer redirects or route groups that avoid duplicating
page implementation.

## Acceptance Criteria

- A new admin can find integrations, coding-agent credentials, app LLM keys,
  sandbox capacity, preview policy, team members, API keys, usage, and audit log
  without scanning an overloaded Platform group.
- The product continues to use **Coding agents** as the label for coding-agent
  credential and fallback configuration.
- "App LLM" is visually and textually distinguished from Coding agents.
- Runtime advanced controls are still available but do not dominate first-run
  setup.
- Evals stay in Settings as a lower-prominence, non-first-class feature.
- Existing permissions and guarded redirects continue to behave the same.

## Non-Goals

- No backend schema changes.
- No change to credential resolution order.
- No change to org/member/builder/viewer permissions.
- No redesign of individual provider setup flows beyond labels and placement.
- No removal of existing settings functionality.

## Open Questions

- Should "Previews" stay under Runtime permanently, or should preview secret
  bundles eventually move into Security & Admin?
- Should PR behavior remain on Organization until a broader Shipping settings
  page exists?
- Should `/settings/llm` and `/settings/runtime` receive public route aliases,
  or are label-only changes enough?

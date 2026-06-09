# Runtime / Sandbox Settings

> **Status:** Partially implemented first pass
> **Last reviewed:** 2026-06-09

143 currently exposes shared sandbox runtime controls across multiple settings pages. Static egress lives under Organization -> General, preview capacity lives on General, and coding-agent execution limits live under Platform -> Coding agents. That split makes each individual setting understandable in isolation, but it hides the product model: these settings all affect how 143 creates, schedules, networks, and retains sandbox runtimes for an organization.

Create a Platform-level **Runtime** or **Sandbox** settings page as the authoritative home for shared sandbox policy across coding-agent sessions and previews.

## Goals

1. Give admins one place to configure org-wide sandbox runtime behavior.
2. Move static egress out of General without incorrectly making it a Coding agents-only or Preview-only setting.
3. Keep Coding agents focused on auth stacks, provider defaults, and agent-specific behavior.
4. Keep Preview focused on preview secrets and preview API tokens.
5. Reuse existing settings-page layout and shadcn/ui primitives so the page feels native to the current product.
6. Avoid surfacing backend worker fleet diagnostics as customer-facing copy.

## Non-Goals

1. A full worker fleet admin console.
2. Per-worker health, deployment generation, hostnames, WireGuard internals, or static-egress capability details.
3. A general-purpose infrastructure settings page.
4. Duplicating the same setting on Coding agents and Preview.
5. Changing the underlying org-settings storage model in the first implementation pass.

## Information Architecture

Add a new item under Settings -> Platform:

```text
PLATFORM
  Integrations
  Coding agents
  LLM
  Autopilot
  Runtime
  Preview
  Evals
```

Use **Runtime** as the sidebar label unless product copy converges on **Sandbox** elsewhere. Runtime is slightly broader and can own scheduler and lifecycle controls; Sandbox is more concrete for networking and resources. The page title may be `Runtime` with a description such as `Configure sandbox networking, capacity, and lifecycle defaults.`

The page should be admin-only, matching the current access level for General, LLM, Autopilot, Preview, Usage, and Audit log.

## Page Sections

### 1. Sandbox Network

Owns org-wide network policy for new and hydrated sandboxes.

Controls:
- `Use static egress IP for sessions and previews` switch.
- Copyable public IP row.
- Generic availability message when static egress cannot be used.
- Optional future links to setup docs or operator diagnostics.

Existing setting/API:
- `settings.sandbox_network.static_egress_enabled`
- `GET /api/v1/settings/network`

UI:
- Use `section` with a compact `h2`, matching General settings.
- Use `Card` and `CardContent`.
- Use `Label` and `Switch` for the toggle.
- Use `CopyButton` for the public IP.
- Use semantic muted text, not warning-heavy styling, unless enabling the setting failed.
- Use `AutosaveIndicator` in the section header.

Customer-facing copy rule:
- Do not render `static_egress_unavailable_reason` or any wording such as `not all active session workers are static-egress-capable for the configured public IP`.
- Customer copy may say `Static egress is not currently available for new sandbox starts.`
- Detailed worker-readiness causes belong in logs, operator tooling, or an admin-only internal diagnostic endpoint.

Wireframe:

```text
Sandbox network                                      Saved

[Card]
  Use static egress IP for sessions and previews       [switch]
  Uses a stable public IP for new and hydrated sandboxes.
  Static egress is not currently available for new sandbox starts.

  Public IP   203.0.113.10                             [copy icon]
```

### 2. Capacity Limits

Owns the org-level controls that determine how many runtimes users can consume.

Controls:
- `Concurrent coding-agent runs`
- `Active previews per user`

Existing settings/API:
- `settings.max_concurrent_runs`
- `settings.preview_max_previews_per_user`
- `PATCH /api/v1/settings`

UI:
- Use one `Card`.
- Use a two-column responsive layout on desktop and stacked rows on mobile.
- Use `Label`, `Input type="number"`, and short help text.
- Use `useAutosaveNumericField` for clamping and commit-on-blur behavior, matching existing General and Coding agents patterns.
- Use constants from `settings-constants.ts` where available.

Wireframe:

```text
Capacity limits                                      Saved

[Card]
  Concurrent coding-agent runs
  [ 5 ]
  Limits how many agent turns can run for the org at once.

  Active previews per user
  [ 4 ]
  Limits how many previews one user can keep running at once.
```

### 3. Session Runtime

Owns defaults that shape individual coding-agent sandbox runs.

Controls:
- `Maximum session duration`
- `Sandbox tab tools` toggle, if product wants this to live with runtime controls instead of Coding agents.

Existing settings/API:
- `settings.max_session_duration_seconds`
- `settings.coding_agent_tab_tools_enabled`
- `PATCH /api/v1/settings`

UI:
- Use `Card` and `CardContent`.
- For duration, use `Input type="number"` in minutes and write seconds to the API, matching the current Coding agents implementation.
- For tab tools, use `Switch`, `Label`, and concise helper copy.
- If tab tools stays on Coding agents, this section should contain only the duration control in the first pass.

Wireframe:

```text
Session runtime                                     Saved

[Card]
  Maximum session duration
  [ 25 ] minutes
  Stops long-running turns after the configured org limit.

  Sandbox tab tools                                  [switch]
  Allows agent tabs in the same session to coordinate through the 143 tools CLI.
```

### 4. Lifecycle Defaults

Owns cleanup and retention policies for runtime artifacts. This can start as a future-only section if the backend settings do not exist yet.

Potential controls:
- Default idle preview cleanup window.
- Default sandbox retention after completed runs.
- Whether previews may hold a sandbox after a session turn completes.
- Default restart/recycle behavior for stale previews.

Proposed settings shape:

```json
{
  "sandbox_lifecycle": {
    "completed_session_retention_minutes": 60,
    "idle_preview_ttl_minutes": 240,
    "preview_holds_sandbox": true
  }
}
```

API/schema impact:
- Add `SandboxLifecycleSettings` to `internal/models/org_settings.go`.
- Add validation tests for min/max ranges and defaults.
- Add frontend `OrgSettings.sandbox_lifecycle` type.
- Reuse `PATCH /api/v1/settings`.
- No DB migration is required if the setting remains in the existing organization settings JSON.

UI:
- Use `Card`, `Input`, `Switch`, and `AutosaveIndicator`.
- Keep controls disabled or hidden until backend policy exists.
- Do not expose worker cleanup implementation details.

### 5. Resource Defaults

Owns org-level upper bounds and defaults for sandbox resource sizing. This should not replace repo-declared `.143/config.json` resource requests; it should bound or default them.

Potential controls:
- Default CPU/memory/disk tier for agent sessions.
- Default CPU/memory/disk tier for previews.
- Maximum allowed preview resource request.
- Whether repo-declared preview resources are honored.

Proposed settings shape:

```json
{
  "sandbox_resources": {
    "agent_default_tier": "standard",
    "preview_default_tier": "standard",
    "allow_repo_resource_requests": true,
    "preview_max_tier": "large"
  }
}
```

API/schema impact:
- Add typed string enums in `internal/models` for resource tiers, with `Validate() error` and table-driven tests.
- Add `SandboxResourceSettings` to `OrgSettings`.
- Add frontend types and option constants.
- Reuse `PATCH /api/v1/settings`.
- If the backend needs exact CPU/memory/disk values in responses, expose them as read-only resolved metadata from a runtime status endpoint rather than duplicating constants in the frontend.

UI:
- Use `Select` for tier choices.
- Use `Switch` for `allow_repo_resource_requests`.
- Use `Badge` for read-only resolved CPU/memory/disk summaries if shown.
- Avoid raw tables unless showing a tier comparison; if a comparison is needed, use shared `Table` components.

### 6. Runtime Diagnostics

Provides a product-safe read-only summary that helps admins understand whether runtime features are available without exposing fleet internals.

Possible first-pass rows:
- Static egress: `Available` / `Unavailable`
- Preview startup: `Ready` / `Degraded` if a backend aggregate exists
- Runtime capacity: `Normal` / `Limited` if a backend aggregate exists

API/schema impact:
- Existing `GET /api/v1/settings/network` can continue to power the static egress row.
- If diagnostics grow beyond network status, add `GET /api/v1/settings/runtime/status` returning a sanitized org-scoped status payload:

```json
{
  "data": {
    "static_egress": {
      "available": true,
      "enabled": false,
      "public_ip": "203.0.113.10"
    },
    "capacity": {
      "state": "normal",
      "active_agent_runs": 2,
      "max_concurrent_agent_runs": 5,
      "active_previews": 3
    }
  }
}
```

Rules:
- Do not include worker hostnames, node IDs, static-egress-capable counts, WireGuard state, or deployment generation.
- Use typed string status fields in `internal/models`, with validation tests.
- Every query behind the endpoint must be org-scoped.

UI:
- Use a compact `Card`.
- Use `Badge` for state labels.
- Use `Table` only if there are enough rows to scan; otherwise use stacked rows with `border-border` separators.
- Use `EmptyState` only if diagnostics are unavailable because the feature is not configured.

## First Implementation Pass

Move these existing controls into `/settings/runtime`:

1. Network access card from General settings. **Implemented.**
2. Preview capacity card from General settings. **Implemented.**
3. `max_concurrent_runs` and `max_session_duration_seconds` controls from Coding agents. **Implemented.**
4. `coding_agent_tab_tools_enabled` control from Coding agents. **Implemented.**
5. Sanitized static-egress diagnostics using the existing network status API. **Implemented.**

Keep these controls where they are:

- Coding-agent auth stacks: Coding agents.
- Default agent type and provider-specific auth setup: Coding agents.
- Preview secrets and preview API tokens: Preview.
- PR authorship, draft PR default, auto-archive, and builder PR review gate: General / Pull requests.
- LLM model defaults: LLM or Coding agents, depending on the existing page contract.

Optional first-pass behavior:
- Leave a short link row on Coding agents: `Runtime limits moved to Runtime settings`.
- Leave a short link row on Preview: `Preview capacity is managed in Runtime settings`.
- Do not duplicate editable controls across pages.

## Implemented First Pass

The first pass added `/settings/runtime` as an admin-only Platform settings page and moved shared sandbox runtime controls into it. Runtime now owns:

- Sandbox network static egress toggle.
- Copyable static egress public IP.
- Product-safe static egress availability copy that does not expose worker capability diagnostics.
- Concurrent coding-agent run limit.
- Active previews per user.
- Maximum session duration.
- Sandbox tab tools.
- Sanitized runtime diagnostics for static egress availability and public IP.

The first pass also:

- Added Runtime to the settings sidebar.
- Added `/settings/runtime` to settings role guards.
- Added a browser-title rule for `Runtime settings`.
- Removed duplicated runtime controls from General and Coding agents.
- Added focused frontend coverage for the new page, old-page removals, sidebar, role guards, and page title.

This design remains under `future/` because lifecycle defaults, resource defaults, and the broader runtime status endpoint are still future work.

## Component Guidance

Use the same settings primitives already used by General, Coding agents, and Preview:

- `PageContainer` with `size="default"`.
- `PageHeader` with title `Runtime`.
- Section headers outside cards using `h2 className="text-xs font-medium text-foreground"`.
- `AutosaveIndicator` in section headers for editable sections.
- `Card`, `CardContent`, and only use `CardHeader`/`CardTitle` when a card contains multiple independently titled subsections.
- `Label`, `Input`, `Switch`, `Select`, `Badge`, `Button`, `CopyButton`.
- Shared `Table` components for diagnostic tables only.
- `EmptyState` for missing configuration states.
- `AlertDialog` only for destructive lifecycle controls if future cleanup actions are added.

Layout rules:
- Use `space-y-6` or `space-y-8` at the page level, matching existing settings pages.
- Keep card content compact.
- Use stacked mobile rows; do not rely on desktop-only table layouts.
- Keep all copy operational and short.
- Avoid nested cards.

## API and Schema Summary

No DB migration is needed for the first pass if settings continue to live in `organizations.settings`.

First-pass reusable fields:

| UI control | Existing setting/API |
|---|---|
| Static egress toggle | `settings.sandbox_network.static_egress_enabled` |
| Static egress public IP/status | `GET /api/v1/settings/network` |
| Concurrent coding-agent runs | `settings.max_concurrent_runs` |
| Maximum session duration | `settings.max_session_duration_seconds` |
| Active previews per user | `settings.preview_max_previews_per_user` |
| Sandbox tab tools | `settings.coding_agent_tab_tools_enabled` |

First-pass frontend work:
- Add `/settings/runtime/page.tsx`.
- Add `/settings/runtime/page.test.tsx`.
- Add Runtime to `SidebarSettingsSection` under Platform.
- Add `/settings/runtime` to settings role guards.
- Move tests for the migrated controls from General/Coding agents or add coverage that the old pages no longer render those controls.
- Update `queryKeys` only if a new runtime status endpoint is added.

Potential future backend work:
- Add `sandbox_lifecycle` settings.
- Add `sandbox_resources` settings.
- Add typed resource-tier and runtime-status enums.
- Add `GET /api/v1/settings/runtime/status` for product-safe diagnostics.
- Add org-scoped store/service methods for any aggregated runtime counts.

Remaining work before this can move to `implemented/`:

- Implement `sandbox_lifecycle` policy and UI controls.
- Implement `sandbox_resources` policy and UI controls.
- Add a broader sanitized runtime status endpoint if the product wants capacity or preview-startup diagnostics beyond static egress.
- Decide whether repo-declared resource requests can be constrained by org policy and expose the resulting resolved tier metadata safely.

## Placement Tradeoffs

### Organization -> General

Pros:
- Clearly admin-owned and organization-wide.
- Keeps broad defaults in one place.
- Requires no new navigation surface.

Cons:
- Too generic for runtime routing and capacity.
- Easy to miss when debugging preview or agent networking.
- General settings becomes a catch-all for unrelated controls.

### Platform -> Coding agents

Pros:
- Agent sessions are common static-egress consumers.
- Some execution limits already live nearby.
- Makes allowlisted dependencies feel connected to agent execution.

Cons:
- Misleading for previews, which use the same sandbox network.
- Users may assume controls apply only to coding-agent turns.
- The page should stay centered on auth stacks and agent-specific setup.

### Platform -> Preview

Pros:
- Preview users often notice network issues first.
- Preview settings is already admin-only.
- Static egress is useful for allowlisted preview dependencies.

Cons:
- Misleading for non-preview agent work.
- Preview settings is currently scoped to preview secrets and API tokens.
- It mixes runtime policy with preview-specific credentials.

### Platform -> Runtime / Sandbox

Pros:
- Matches the shared product model.
- Gives capacity, lifecycle, resources, and network controls one home.
- Lets Coding agents and Preview link to the authoritative setting.

Cons:
- Adds a navigation item.
- Requires moving existing controls and tests.

## Recommendation

Create Settings -> Platform -> **Runtime** and move shared sandbox controls there. Start with existing settings so the first implementation does not require schema migration. Add lifecycle, resource defaults, and sanitized runtime diagnostics only when the backend policy exists.

The guiding rule: if a setting changes how a sandbox is created, scheduled, networked, retained, or cleaned up across more than one product surface, it belongs on Runtime. If it configures credentials, model choice, PR behavior, preview secrets, or team access, it belongs on the existing specialized page.

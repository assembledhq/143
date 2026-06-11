# Runtime / Sandbox Settings

> **Status:** Implemented
> **Last reviewed:** 2026-06-11

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
5. Requiring a database migration for settings that fit in the existing org settings JSON.

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
- `Static egress IP` switch.
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
- Put explanatory copy in a question-mark tooltip next to the label.
- Use `CopyButton` for the public IP.
- Use semantic muted text, not warning-heavy styling, unless enabling the setting failed.
- Use `AutosaveIndicator` in the section header.

Customer-facing copy rule:
- Do not render `static_egress_unavailable_reason` or any worker capability detail.
- Customer copy may say `Static egress is not currently available for new sandbox starts.`
- Detailed worker-readiness causes belong in logs, operator tooling, or an admin-only internal diagnostic endpoint.

Wireframe:

```text
Sandbox network                                      Saved

[Card]
  Static egress IP [?]                                 [switch]
  Static egress is not currently available for new sandbox starts.

  Public IP   203.0.113.10                             [copy icon]
```

### 2. Usage Limits

Owns the org-level controls that determine how many runtimes users can consume.

Controls:
- `Concurrent agent runs`
- `Active previews per user`

Existing settings/API:
- `settings.max_concurrent_runs`
- `settings.preview_max_previews_per_user`
- `PATCH /api/v1/settings`

UI:
- Use one `Card`.
- Use a two-column responsive layout on desktop and stacked rows on mobile.
- Use `Label`, `Input type="number"`, question-mark tooltips for explanations, and visible helper text only for max values.
- Use `useAutosaveNumericField` for clamping and commit-on-blur behavior, matching existing General and Coding agents patterns.
- Use constants from `settings-constants.ts` where available.

Wireframe:

```text
Usage limits                                        Saved

[Card]
  Concurrent agent runs [?]
  [ 5 ]
  Max 25

  Active previews per user [?]
  [ 4 ]
  Max 20
```

### 3. Sessions

Owns defaults that shape individual coding-agent sandbox runs.

Controls:
- `Maximum session length`
- `Agent tab tools` toggle, if product wants this to live with runtime controls instead of Coding agents.

Existing settings/API:
- `settings.max_session_duration_seconds`
- `settings.coding_agent_tab_tools_enabled`
- `PATCH /api/v1/settings`

UI:
- Use `Card` and `CardContent`.
- For duration, use `Input type="number"` in minutes and write seconds to the API, matching the current Coding agents implementation.
- For tab tools, use `Switch`, `Label`, and a question-mark tooltip.
- If tab tools stays on Coding agents, this section should contain only the duration control in the first pass.

Wireframe:

```text
Sessions                                            Saved

[Card]
  Maximum session length [?]
  [ 25 ] minutes
  Max 120 minutes

  Agent tab tools [?]                                [switch]
```

### 4. Cleanup Defaults

Owns cleanup and retention policies for runtime artifacts.

Controls:
- `Idle preview timeout`.
- `Keep completed sessions for`.
- `Keep sandbox while preview is active`.

Settings shape:

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
- `SandboxLifecycleSettings` lives in `internal/models/org_settings.go`.
- Validation tests cover min/max ranges and defaults.
- Frontend `OrgSettings.sandbox_lifecycle` mirrors the backend payload.
- Reuse `PATCH /api/v1/settings`.
- No DB migration is required if the setting remains in the existing organization settings JSON.

UI:
- Use `Card`, `Input`, `Switch`, and `AutosaveIndicator`.
- Put explanatory copy in question-mark tooltips and keep only max-value helper text below numeric inputs.
- Do not expose worker cleanup implementation details.

### 5. Resource Defaults

Owns org-level upper bounds and defaults for sandbox resource sizing. This should not replace repo-declared `.143/config.json` resource requests; it should bound or default them.

Controls:
- Default CPU/memory/disk tier for agent sessions.
- Default CPU/memory/disk tier for previews.
- Maximum allowed preview resource request.
- Whether repo-declared preview resources are honored.

Settings shape:

```json
{
  "sandbox_resources": {
    "agent_default_tier": "standard",
    "preview_default_tier": "standard",
    "allow_repo_resource_requests": true,
    "preview_max_tier": "large",
    "preview_max_cpu_millis": 2000,
    "preview_max_memory_mib": 8192,
    "preview_max_ephemeral_disk_mib": 10240
  }
}
```

API/schema impact:
- Typed string enums in `internal/models` validate resource tiers with `Validate() error` and table-driven tests.
- `SandboxResourceSettings` lives in `OrgSettings`.
- Frontend types and option constants are wired into the Runtime page.
- Reuse `PATCH /api/v1/settings`.
- If the backend needs exact CPU/memory/disk values in responses, expose them as read-only resolved metadata from a runtime status endpoint rather than duplicating constants in the frontend.

UI:
- Use `Select` for tier choices.
- Use `Switch` for `allow_repo_resource_requests`.
- Show preview CPU limits in CPU cores in the UI, while saving the existing `preview_max_cpu_millis` field.
- Avoid raw tables unless showing a tier comparison; if a comparison is needed, use shared `Table` components.

## Implementation

`/settings/runtime` is the authoritative Platform settings page for shared sandbox runtime policy. Runtime owns:

- Sandbox network static egress toggle.
- Copyable static egress public IP.
- Product-safe static egress availability copy that does not expose worker capability diagnostics.
- Concurrent agent run limit.
- Active previews per user.
- Maximum session length.
- Agent tab tools.
- Cleanup defaults for completed-session retention, idle preview timeout, and keeping the sandbox while a preview is active.
- Resource defaults for agent tier, preview tier, preview max tier, and repo resource request policy.
- Preview CPU limits shown in CPU cores while stored as millicores in `settings.sandbox_resources.preview_max_cpu_millis`.

The Runtime settings page intentionally does not include a runtime diagnostics section. Current capacity and fleet state belong in operational tooling or purpose-built status surfaces, not in the editable settings page.

Keep these controls where they are:

- Coding-agent auth stacks: Coding agents.
- Default agent type and provider-specific auth setup: Coding agents.
- Preview secrets and preview API tokens: Preview.
- PR authorship, draft PR default, auto-archive, and builder PR review gate: General / Pull requests.
- LLM model defaults: LLM or Coding agents, depending on the existing page contract.

The implementation also:

- Added Runtime to the settings sidebar.
- Added `/settings/runtime` to settings role guards.
- Added a browser-title rule for `Runtime settings`.
- Removed duplicated runtime controls from General and Coding agents.
- Added compact links from Coding agents and Preview to Runtime.
- Added focused backend and frontend coverage for the new page, old-page removals, sidebar, role guards, page title, lifecycle/resource validation, tooltip copy, and CPU unit conversion.

## Component Guidance

Use the same settings primitives already used by General, Coding agents, and Preview:

- `PageContainer` with `size="default"`.
- `PageHeader` with title `Runtime`.
- Section headers outside cards using `h2 className="text-xs font-medium text-foreground"`.
- `AutosaveIndicator` in section headers for editable sections.
- `Card`, `CardContent`, and only use `CardHeader`/`CardTitle` when a card contains multiple independently titled subsections.
- `Label`, `Input`, `Switch`, `Select`, `Button`, `Tooltip`, and `CopyButton`.
- `AlertDialog` only for destructive lifecycle controls if future cleanup actions are added.

Layout rules:
- Use `space-y-6` or `space-y-8` at the page level, matching existing settings pages.
- Keep card content compact.
- Use stacked mobile rows; do not rely on desktop-only table layouts.
- In Resource defaults, keep the repository resource request policy as its own row below the tier and numeric resource limit grid.
- Keep all copy operational and short.
- Put field explanations in question-mark tooltips; keep visible helper text only for max values.
- Avoid nested cards.

## API and Schema Summary

No DB migration is needed because settings continue to live in `organizations.settings`.

Reusable fields:

| UI control | Existing setting/API |
|---|---|
| Static egress IP | `settings.sandbox_network.static_egress_enabled` |
| Static egress public IP/status | `GET /api/v1/settings/network` |
| Concurrent agent runs | `settings.max_concurrent_runs` |
| Maximum session length | `settings.max_session_duration_seconds` |
| Active previews per user | `settings.preview_max_previews_per_user` |
| Agent tab tools | `settings.coding_agent_tab_tools_enabled` |
| Keep completed sessions for | `settings.sandbox_lifecycle.completed_session_retention_minutes` |
| Idle preview timeout | `settings.sandbox_lifecycle.idle_preview_ttl_minutes` |
| Keep sandbox while preview is active | `settings.sandbox_lifecycle.preview_holds_sandbox` |
| Agent sandbox size | `settings.sandbox_resources.agent_default_tier` |
| Preview sandbox size | `settings.sandbox_resources.preview_default_tier` |
| Allow repository resource requests | `settings.sandbox_resources.allow_repo_resource_requests` |
| Largest preview size | `settings.sandbox_resources.preview_max_tier` |
| Preview CPU limit | `settings.sandbox_resources.preview_max_cpu_millis` |
| Preview memory limit | `settings.sandbox_resources.preview_max_memory_mib` |
| Preview disk limit | `settings.sandbox_resources.preview_max_ephemeral_disk_mib` |

Resource tiers are typed backend enum strings: `small`, `standard`, and `large`. The UI displays CPU as cores for readability, then converts to millicores when patching the existing API field.

Remaining future enhancement:
- If repo-declared resource requests need exact CPU/memory/disk summaries in the UI, expose read-only resolved tier metadata safely from a runtime endpoint rather than duplicating infrastructure constants in the frontend.

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

Settings -> Platform -> **Runtime** is the shared sandbox policy home. Coding agents and Preview link to it, but do not duplicate the editable controls.

The guiding rule: if a setting changes how a sandbox is created, scheduled, networked, retained, or cleaned up across more than one product surface, it belongs on Runtime. If it configures credentials, model choice, PR behavior, preview secrets, or team access, it belongs on the existing specialized page.

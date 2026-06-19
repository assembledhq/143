# Design: Coding Agent Settings Rethink

> **Status:** Implemented | **Last reviewed:** 2026-04-22

## Implementation Notes

Implemented in this pass:

- Added an organization-scoped coding-auth stack API with persisted row priority, create/update/disable operations, and reorder support.
- Reworked the organization settings screen around a prioritized auth table with explicit default state, move actions, and a right-side detail sheet.
- Reworked the personal settings screen around a default-auth card, backup list, and clearer effective-resolution summary.
- Standardized the empty auth-stack states on the shared inline empty-state component. Personal auths, account-level org fallback, and org-level coding auths now render as explicit setup states with a clear title, explanatory sentence, and local action where the viewer can act.
- Updated the system overview to reference this design from `implemented/`.

Notable boundary:

- The new settings UX and ordering metadata are fully implemented. Automatic cross-agent execution fallback remains bounded by the existing runtime model, which still selects an agent type at session start.

## Problem

The current coding agent settings UI exposes too much detail too early and hides the most important system behavior:

- The operator cannot see all configured coding agent auths in one place.
- The fallback order is implicit, even though it directly affects reliability.
- Configuration is trapped inside per-agent detail views, which makes comparison slow.
- Adding a new auth feels like entering a settings maze instead of extending a resilient pool.

The result is a control surface that is technically flexible but cognitively noisy. It asks the user to think in terms of forms and tabs when they actually need to think in terms of availability, priority, and trust.

This document supersedes the settings-page UI direction in [implemented/34-personal-team-coding-agents.md](../implemented/34-personal-team-coding-agents.md) while preserving the underlying product capability: multiple personal and organization-scoped coding agent auths, including both subscription and API key auth for Codex and Claude Code.

## Design Principles

- Show the system, not the plumbing. The first screen should explain which auths exist and what will happen if one fails.
- Prefer one strong default view. Most users should not need to click into a row to understand the current setup.
- Make adding capacity feel lightweight. Adding another auth should feel like extending a queue, not configuring a separate product.
- Preserve depth behind progressive disclosure. Details like verification history, scopes, or model overrides should live in drawers or sheets, not the default surface.
- Make fallback order tangible. Priority should be visible, editable, and clearly tied to runtime behavior.
- Keep personal and organization setup visually related, but not artificially identical.

## Goals

- Present all authorized coding agent auths in a single prioritized table.
- Make the default auth obvious.
- Support multiple auths per provider and auth type.
- Support Codex and Claude Code with both subscription and API key auth.
- Let users add a new auth from the primary screen.
- Give admins enough clarity to debug rate-limit and fallback behavior without opening row details.
- Provide a personal setup experience that feels consistent with org settings but better suited to smaller auth counts.

## Non-Goals

- Redesigning onboarding copy for the first-run Autopilot card.
- Changing credential resolution rules outside the settings surface.
- Designing provider-specific OAuth/device-auth flows in detail.

## Primary User Mental Model

The right mental model is not "I configure providers one by one."

The right mental model is:

1. These are the auths my org can use.
2. This is the order we try them in.
3. This one is the default.
4. If one is unhealthy or rate-limited, the next one takes over.
5. I can add capacity quickly.

The UI should therefore behave more like a dispatch queue than a settings form.

## Organization Settings Design

### Recommended information architecture

Use a single page with three layers:

1. A compact page header with one-line explanation and a primary `Add auth` action.
2. A prioritized table that shows every configured organization auth in runtime order.
3. A right-side details sheet for editing one selected row without losing the overall view.

This keeps the center of gravity on the system as a whole while preserving room for deeper configuration.

## Recommended Table

Each row represents one authorized coding agent auth, not one provider.

### Columns

- `Priority` — drag handle + numeric order
- `Agent` — `Codex` or `Claude Code`
- `Auth type` — `Subscription` or `API key`
- `Label` — human-friendly identifier like `Codex team seat A` or `Claude prod key 2`
- `Scope` — `Organization`
- `Status` — `Healthy`, `Rate limited`, `Needs reauth`, `Invalid`
- `Default` — badge on exactly one row
- `Last used` — relative timestamp
- `Usage notes` — masked identity or email, key suffix, or subscription owner
- `Actions` — overflow menu

### Required behaviors

- Rows are shown in effective fallback order from top to bottom.
- The first runnable row in priority order is the effective default.
- Reordering is done by dragging rows using a dedicated drag handle in the `Priority` column.
- Reordering a runnable row to the top immediately makes it the default auth.
- Reordering updates fallback order immediately after confirmation.
- The default row shows a visible badge, not just a boolean column.
- Status is glanceable with color + text, never color alone.
- `Add auth` opens a flow with provider first, then auth type, then credential steps.
- Multiple Codex subscription auths, multiple Codex API key auths, multiple Claude subscription auths, and multiple Claude API key auths are all supported.
- Each row also exposes non-drag actions for accessibility: `Move to top`, `Move up`, and `Move down`.

## Organization Wireframe

```text
+----------------------------------------------------------------------------------+
| Coding Agents                                             [Add auth]             |
| Control which auths the org can use, and in what fallback order.                |
+----------------------------------------------------------------------------------+
| Priority | Agent       | Auth type    | Label                | Status   |        |
|-------------------------------------------------------------------------------   |
| 1  ≡     | Codex       | Subscription | Team seat A          | Healthy  | DEFAULT|
|          |             |              | user@company.com     |          |        |
| 2  ≡     | Claude Code | API key      | Claude prod key      | Healthy  |        |
|          |             |              | ...9K3F              |          |        |
| 3  ≡     | Codex       | API key      | Codex backup key     | Rate lim |        |
|          |             |              | ...7A1D              |          |        |
| 4  ≡     | Claude Code | Subscription | Design team seat     | Reauth   |        |
|          |             |              | designer@company.com |          |        |
+----------------------------------------------------------------------------------+

Selected row opens side sheet:

+--------------------------------------------------+
| Codex team seat A                         DEFAULT |
| Subscription auth                                  |
|                                                    |
| Status            Healthy                          |
| Priority          1                                |
| Fallback role     First choice                     |
| Last verified     2 hours ago                      |
| Last used         8 minutes ago                    |
| Notes             user@company.com                 |
|                                                    |
| [Move up] [Move down] [Set as default]             |
| [Reverify] [Rename] [Disable]                      |
+--------------------------------------------------+
```

## Add Auth Flow

The add flow should be short, provider-led, and explicit about multiplicity.

### Step sequence

1. Choose provider: `Codex` or `Claude Code`
2. Choose auth type: `Subscription` or `API key`
3. Complete provider-specific auth
4. Name the auth
5. Choose insertion point:
   `Make default`, `Add as next fallback`, or `Place manually`
6. Confirm

### UX notes

- The flow should say plainly that multiple auths per provider are allowed.
- The default insertion should be `Add as next fallback`, because it is the safest behavior.
- If the new auth is healthier than the current default, the UI can suggest making it default, but not force it.

## Organization Rationale

This is the chosen direction.

### Why it works

- It solves the visibility problem directly.
- It turns fallback order into the primary organizing principle.
- It scales to many auths without feeling like a wall of forms.
- It keeps editing nearby without breaking context.

### Tradeoffs

- Slightly denser than a card-based layout.
- Requires careful row design to avoid looking like an ops console.

It is the cleanest expression of the real job to be done: inspect the fleet, understand fallback behavior, and change it quickly. It feels disciplined rather than busy. It does not sentimentalize configuration. It makes the system legible.

The visual tone should be calm, spare, and sharply hierarchical:

- generous spacing
- restrained color
- one strong accent on the default badge and primary action
- minimal iconography
- no nested tabs
- no per-provider cards on the default view

The page should feel like the product knows what matters and refuses to distract.

## Personal Coding Agent Setup

Personal setup should rhyme with organization setup, but it should acknowledge that most users will only have one or two auths.

The best direction is to preserve the same row language and status semantics while reducing the amount of visible control chrome.

## Personal Design

Show one prominent default card at top, then a compact table of backup auths underneath.

### Shape

- Top card: current personal default, status, and quick actions
- Below: `Backups` table in fallback order

### Why it works

- Better for the likely common case of one primary auth.
- Feels more personal and less administrative.

### Tradeoffs

- Less symmetrical with org settings.
- Gets awkward once the user has several auths.

Use this pattern for personal settings, with one important consistency rule: the backup list should use the exact same row structure, badges, and statuses as the organization table.

That gives the product one visual language but two different emphases:

- Organization settings optimize for fleet management.
- Personal settings optimize for confidence and quick setup.

## Personal Wireframe

```text
+---------------------------------------------------------------------------+
| My Coding Agents                                            [Add auth]    |
| Your auths are tried before organization fallbacks.                       |
+---------------------------------------------------------------------------+
| Default auth                                                               |
| Codex subscription                                         Healthy         |
| john@company.com                                           PERSONAL DEFAULT|
| [Change default] [Reverify] [Disable]                                      |
+---------------------------------------------------------------------------+
| Backups in fallback order                                                  |
|--------------------------------------------------------------------------- |
| 2  | Claude Code | API key      | ...8LM2            | Healthy            |
| 3  | Codex       | API key      | ...1DF9            | Healthy            |
+---------------------------------------------------------------------------+
| Effective resolution: Personal #1 -> Personal #2 -> Org default -> Org #2 |
+---------------------------------------------------------------------------+
```

## Key Interaction Details

- Reordering should use drag-and-drop plus explicit keyboard-accessible move actions.
- The drag affordance should be a small handle, not the whole row, so row click can still open detail.
- During drag, the row should lift slightly, preserve cell widths, and show a clear insertion target line.
- On touch devices, the drag handle should require a short press delay so vertical scrolling still feels natural.
- Disabling an auth should never silently collapse fallback order; the user should see the resulting next default before confirming.
- When a default auth becomes invalid, the page should show the promoted fallback immediately.
- Status changes caused by real runtime failures should appear in this table without requiring a page refresh.
- A row click should open detail, not navigate away.

## Drag And Drop Implementation

### Existing library check

The frontend does **not** currently include a dedicated drag-and-drop or sortable library in [frontend/package.json](../../../frontend/package.json).

Relevant existing UI infrastructure:

- `@tanstack/react-table` is already installed and used for table rendering.
- The shared table primitives in [frontend/src/components/ui/table.tsx](../../../frontend/src/components/ui/table.tsx) already give us semantic table markup.
- The current settings pages do not yet use a sortable table pattern.

### Recommendation

Add `@dnd-kit/core`, `@dnd-kit/sortable`, `@dnd-kit/modifiers`, and `@dnd-kit/utilities`.

This is the best fit because:

- it works well with modern React and semantic table markup
- TanStack Table's own React drag-and-drop guidance recommends `@dnd-kit/core`
- TanStack's official row drag-and-drop example uses `@dnd-kit`
- it supports pointer, touch, and keyboard sensors, which matters for a settings table

Avoid `react-dnd` here. TanStack explicitly recommends against it for React 18+ and notes compatibility problems in modern React/Strict Mode.

Avoid native HTML5 drag-and-drop for the main implementation. It is lighter, but it is worse on touch devices and usually takes more custom work to reach acceptable UX quality.

### Best implementation shape

Use a semantic table with a dedicated drag-handle column.

Implementation outline:

1. Keep the visual table built from the existing `Table` primitives and `@tanstack/react-table`.
2. Wrap the table body in a `DndContext` and `SortableContext`.
3. Use `useSortable` on each row and on the drag handle cell.
4. Restrict movement to the vertical axis.
5. Use `verticalListSortingStrategy`.
6. On drag end, reorder a local list optimistically, then persist the new priorities through an explicit mutation.
7. Treat the first runnable row by priority as the effective default auth. Do not maintain a separate conflicting default field.

### Interaction details

- Dragging a runnable row to position `1` should immediately preview the `DEFAULT` badge on that row.
- The drag handle should use a grip icon and `cursor-grab` / `cursor-grabbing`.
- The active row should keep its measured width so cells do not collapse while dragging.
- Use keyboard support via `KeyboardSensor` and sortable keyboard coordinates.
- Preserve explicit action-menu commands for `Move to top`, `Move up`, and `Move down` so reordering is not mouse-only.
- After reorder, announce the new position in an aria-live region.

### Data model implications

This UI should be backed by explicit ordered auth records rather than inferred provider summaries.

At minimum, each auth row needs:

- `id`
- `scope`
- `agent`
- `auth_type`
- `label`
- `priority`
- `status`
- `last_used_at`
- `last_verified_at`
- `masked_identity`

### Backend implications

This is not just a frontend refactor.

Current backend reality:

- org-scoped credentials already support multiple labeled rows in `org_credentials`
- some org-scoped flows already use round-robin selection across multiple credentials
- user-scoped credentials and resolved-credential APIs are still mostly provider-summary based

To match this design, the system should move from a provider-summary API to an auth-row API with explicit order.

Recommended backend changes:

- add a stable `priority` field for auth rows
- define effective default as the first runnable row by priority
- add reorder endpoints or a bulk reorder mutation
- return full auth rows for organization settings and personal settings
- update credential resolution to use ordered fallback rather than round-robin for this settings surface

### Migration recommendation

Do not try to layer drag-and-drop on top of the current `provider -> single summary row` API.

Instead:

1. introduce a new auth-row API
2. build the new sortable org table against that API
3. build the personal default-card-plus-backups view on the same row model
4. retire the old provider-card settings UI

## Concrete Implementation Plan

### Phase 1: Data model and migration

The first step is to unify both settings surfaces around a shared auth-row model.

#### Organization auth rows

Use `org_credentials` as the source of truth for organization auths.

Required schema changes:

- add `priority integer NOT NULL`
- backfill `priority` for existing rows
- add an index on `(org_id, status, priority)`

Notes:

- `org_credentials.label` already exists and already supports multiple auths per provider.
- `auth_type` should be derived from the decrypted config type, not stored redundantly.
- `agent` should also be derived from provider + config type:
  - `OpenAIConfig` -> `Codex` + `API key`
  - `OpenAIChatGPTConfig` -> `Codex` + `Subscription`
  - `AnthropicConfig` -> `Claude Code` + `API key`
  - `AnthropicSubscription` -> `Claude Code` + `Subscription`

#### Personal auth rows

`user_credentials` needs to move from one-row-per-provider to multi-row-per-provider.

Required schema changes:

- add `label text NOT NULL DEFAULT ''`
- add `priority integer NOT NULL`
- add `last_used_at timestamptz`
- replace `UNIQUE (org_id, user_id, provider)` with `UNIQUE (org_id, user_id, provider, label)`

Migration notes:

- keep `is_team_default` temporarily for backward compatibility, but stop using it for the new UI
- the long-term design should remove `is_team_default` entirely once the old team-default flows are retired

#### Backfill strategy

There is no true existing global priority model today, so the initial ordering should be deterministic and low-surprise.

Recommended backfill:

- put auths matching the current org `default_agent_type` first
- then order remaining rows by `created_at`
- assign contiguous priorities `1..N`

This preserves the current mental model as closely as possible while introducing explicit ordering.

### Phase 2: Backend models and stores

Add a new API-safe summary type that represents one settings row.

Suggested model:

```go
type CodingAgentAuthRow struct {
    ID              uuid.UUID  `json:"id"`
    Scope           string     `json:"scope"` // "organization" | "personal"
    Agent           string     `json:"agent"` // "codex" | "claude_code"
    AuthType        string     `json:"auth_type"` // "subscription" | "api_key"
    Label           string     `json:"label"`
    Priority        int        `json:"priority"`
    Status          string     `json:"status"`
    StatusReason    string     `json:"status_reason,omitempty"`
    IsRunnable      bool       `json:"is_runnable"`
    MaskedIdentity  string     `json:"masked_identity,omitempty"`
    LastVerifiedAt  *time.Time `json:"last_verified_at,omitempty"`
    LastUsedAt      *time.Time `json:"last_used_at,omitempty"`
    CreatedAt       time.Time  `json:"created_at"`
}
```

Store work:

- extend [internal/db/org_credentials.go](../../../internal/db/org_credentials.go) with ordered list + reorder methods
- extend [internal/db/user_credentials.go](../../../internal/db/user_credentials.go) with label-aware multi-row methods
- keep all new store methods org-scoped and tenancy-safe

Suggested store methods:

- `ListCodingAgentAuthRowsByOrg(ctx, orgID uuid.UUID) ([]models.CodingAgentAuthRow, error)`
- `ListCodingAgentAuthRowsByUser(ctx, orgID, userID uuid.UUID) ([]models.CodingAgentAuthRow, error)`
- `ReorderOrgCodingAgentAuths(ctx, orgID uuid.UUID, ids []uuid.UUID) error`
- `ReorderUserCodingAgentAuths(ctx, orgID, userID uuid.UUID, ids []uuid.UUID) error`
- `RenameOrgCodingAgentAuth(ctx, orgID, id uuid.UUID, label string) error`
- `RenameUserCodingAgentAuth(ctx, orgID, userID, id uuid.UUID, label string) error`

Reordering should run inside a transaction and rewrite contiguous priorities in one pass.

### Phase 3: New settings APIs

Do not try to retrofit the current provider-summary endpoints for this screen.

Introduce a dedicated auth-row API:

- `GET /api/v1/settings/coding-agent-auths/org`
- `PATCH /api/v1/settings/coding-agent-auths/org/reorder`
- `PATCH /api/v1/settings/coding-agent-auths/org/{id}`
- `DELETE /api/v1/settings/coding-agent-auths/org/{id}`
- `GET /api/v1/settings/coding-agent-auths/personal`
- `PATCH /api/v1/settings/coding-agent-auths/personal/reorder`
- `PATCH /api/v1/settings/coding-agent-auths/personal/{id}`
- `DELETE /api/v1/settings/coding-agent-auths/personal/{id}`

Payload shape for reorder:

```json
{
  "ordered_ids": ["uuid-1", "uuid-2", "uuid-3"]
}
```

#### Creation flows

For v1, keep the existing provider-specific subscription auth flows behind the scenes:

- Codex subscriptions continue to use the existing `codex-auth` initiation/completion flow
- Claude Code subscriptions continue to use the existing `claude-code-auth` initiation/completion flow

But the page should call them through one unified `Add auth` flow.

For API keys, add new row-scoped create endpoints so multiple API-key auths are actually possible:

- `POST /api/v1/settings/coding-agent-auths/org/api-key`
- `POST /api/v1/settings/coding-agent-auths/personal/api-key`

These endpoints should accept:

- `agent`
- `label`
- `api_key`
- optional provider-specific metadata like `base_url`
- optional insertion mode: `top`, `after_default`, or explicit `priority`

### Phase 4: Runtime resolution changes

The settings page only makes sense if runtime behavior matches what the user sees.

Update [internal/services/agent/env.go](../../../internal/services/agent/env.go) so coding-agent auth resolution becomes ordered-stack based:

1. list personal auth rows for the relevant agent, ordered by `priority`
2. choose the first runnable personal row
3. if none exist, list organization auth rows for the relevant agent, ordered by `priority`
4. choose the first runnable org row
5. if none exist, fall back to `none`

Important:

- disabled and invalid rows remain visible in settings but are skipped at runtime
- `pending_auth` rows are visible but not runnable
- the effective `DEFAULT` badge in the UI should match the first runnable row, not merely the row with `priority = 1`

This replaces the older mental model of `personal -> team_default -> org provider summary`.

### Phase 5: Frontend shared components

Build the new UI from shared components rather than embedding everything directly in page files.

Suggested new frontend folder:

- [frontend/src/components/coding-agents/](../../../frontend/src/components/)

Suggested components:

- `coding-agent-auth-table.tsx`
- `sortable-auth-row.tsx`
- `auth-status-badge.tsx`
- `auth-default-badge.tsx`
- `auth-details-sheet.tsx`
- `add-auth-dialog.tsx`
- `api-key-auth-form.tsx`
- `subscription-auth-launcher.tsx`
- `personal-default-card.tsx`

Suggested supporting client work:

- add API methods in [frontend/src/lib/api.ts](../../../frontend/src/lib/api.ts)
- add types in [frontend/src/lib/types.ts](../../../frontend/src/lib/types.ts)
- add query keys in `frontend/src/lib/query-keys.ts`

### Phase 6: Organization settings page

Replace the current provider-section organization page in [frontend/src/app/(dashboard)/settings/agent/page.tsx](../../../frontend/src/app/(dashboard)/settings/agent/page.tsx) with:

- page header + `Add auth`
- one sortable table
- one details sheet

Implementation notes:

- keep the existing table styling language by continuing to use [frontend/src/components/ui/table.tsx](../../../frontend/src/components/ui/table.tsx)
- use `@tanstack/react-table` for column definition and row rendering
- add `@dnd-kit` only for interaction, not for visual layout
- reorder persists immediately on drop with optimistic cache update and rollback on error
- rename can autosave on blur inside the details sheet
- disable, disconnect, and reverify remain explicit actions

### Phase 7: Personal settings page

Replace the coding-agent credentials section in [frontend/src/app/(dashboard)/settings/account/page.tsx](../../../frontend/src/app/(dashboard)/settings/account/page.tsx) with:

- a top `Default auth` card
- a compact `Backups` table using the same row component language
- an `Effective resolution` line that shows `personal -> organization`

Implementation notes:

- the card should simply render the first runnable personal auth
- the backup table should render the remaining personal auth rows
- `Change default` should focus or highlight the backup table and instruct the user to drag a row to the top
- the visual language should stay consistent with the org page, but the page should feel lighter and more personal

### Phase 8: Add-auth flow

Build one unified add flow in the frontend, even if the backend remains provider-specific under the hood for v1.

Suggested dialog steps:

1. choose `Codex` or `Claude Code`
2. choose `Subscription` or `API key`
3. complete the provider-specific auth step
4. enter a label
5. choose insertion:
   `Make default`, `Add as next fallback`, or `Place manually later`

Implementation detail:

- for subscriptions, the dialog should call the existing Codex / Claude auth flows
- for API keys, the dialog should call the new row-scoped create endpoint
- on success, refetch the ordered auth list and open the new row in the details sheet

### Phase 9: Deprecation and cleanup

Once the new UI is live:

- remove team-default UI and endpoint usage from the settings pages
- stop rendering the old per-provider cards
- deep-link all Autopilot and setup entrypoints into the new `Add auth` dialog
- leave existing provider-specific auth endpoints in place temporarily for backward compatibility, but make the new auth-row API the primary settings surface

### Testing and verification

#### Backend

- migration tests for backfill ordering
- store tests for list and reorder behavior
- handler tests for org list, personal list, reorder, rename, and delete
- resolver tests for `personal ordered stack -> org ordered stack`
- tenancy tests that every new store query still filters by `org_id`

#### Frontend

- component tests for the org table, details sheet, and personal default card
- drag-and-drop tests for reorder behavior and optimistic rollback
- accessibility tests for keyboard reordering and aria-live announcements
- integration tests for the unified `Add auth` flow

#### Required frontend verification

From `frontend/`:

1. `npm run typecheck`
2. `npm run lint`
3. `npm run build`

## Data Needed By UI

- Auth ID
- Agent provider
- Auth type
- Display label
- Priority
- Is default
- Effective status
- Status reason
- Masked identity summary
- Last verified at
- Last used at
- Scope
- Can edit
- Can reorder

## Open Questions

- Should org settings allow more than one default concept, such as `default for interactive sessions` and `default for unattended runs`, or is one global default enough for now?
- Should the table show recent rate-limit counts inline, or is plain status enough for v1?
- Do we want optional labels grouped by team or function, such as `backend`, `overflow`, or `night queue`, or should that wait until we have real operational demand?

## Rollout Plan

1. Ship the new auth-row API and organization table while keeping the existing provider-specific creation flows behind the scenes.
2. Ship the personal default-card-plus-backups view on top of the same ordered auth-row model.
3. Switch runtime credential resolution to ordered stacks so the UI and execution behavior match.
4. Remove older tab-first navigation and per-provider cards from settings, then deep-link setup entrypoints into the new `Add auth` flow.

## Success Criteria

- An admin can explain the org fallback chain without clicking into a row.
- Adding a second or third auth feels linear and obvious.
- Users can tell which auth is default within one second of opening the page.
- Users can compare Codex and Claude Code capacity in one screen.
- Support/debugging questions about "why did this run use that auth?" decrease because the system state is visible.

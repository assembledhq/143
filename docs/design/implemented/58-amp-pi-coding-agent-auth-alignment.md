# Design: Amp And Pi Coding Agent Auth Alignment

> **Status:** Implemented | **Last reviewed:** 2026-04-22

## Problem

Amp and Pi are installed in the sandbox and selectable as coding agents, but they are not treated as first-class coding agents by the auth system.

Today:

- Codex, Claude Code, and OpenCode use the coding-auth stack backed by `org_credentials`.
- Codex and Claude Code also have provider-specific subscription flows that still land in `org_credentials`.
- Amp does **not** use the coding-auth stack. Its API key lives in `settings.agent_config.amp.AMP_API_KEY`.
- Pi does **not** have its own auth at all. It inherits `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, and `GEMINI_API_KEY` from other configured agents, then narrows them based on `PI_MODEL`.

That split violates the product mental model introduced by the coding-auth stack redesign: all coding-agent auth should live in the same place, follow the same resolution rules, and be inspectable in one system view.

It also creates real architecture problems:

- Amp and Pi bypass the encrypted credential-store primitives used by the other coding agents.
- `agent_config` is carrying secrets for Amp, even though that path was intended for agent defaults and tightly-scoped env overrides.
- Pi reuses sibling provider keys, so its auth boundary is implicit and coupled to unrelated agent setup.
- Personal/team/org credential resolution is inconsistent across agent types.
- The UI has to special-case Amp and Pi instead of treating them as rows in the same auth fleet.

## Current Implementation Snapshot

### Backend

- `internal/services/agent/env.go` resolves Codex, Claude Code, and OpenCode via `resolveProviderConfig(...)` from the credential stores.
- The same file explicitly documents that Amp and Pi have **no first-class provider credential store** and therefore use `agent_config` overrides instead.
- `CheckAuth(...)` only validates `AMP_API_KEY` directly for Amp and validates Pi by checking for inherited sibling-provider keys.
- `NarrowScopedCredentials(...)` exists only because Pi currently receives multiple foreign provider keys and needs post-processing to remove some of them.

### Storage

- `internal/db/org_credentials.go` only includes `anthropic`, `openai`, `openai_chatgpt`, and `gemini` in `ListCodingAuths(...)`.
- `CreateCodingAuth(...)` only supports Codex, Claude Code, and OpenCode.
- There is no `ProviderAmp` or `ProviderPi`.

### Frontend

- The organization agent settings page shows Amp and Pi in the add-auth modal, but:
  - Amp writes to `settings.agent_config.amp`.
  - Pi only writes `settings.agent_config.pi.PI_MODEL`.
  - neither appears in the fallback stack returned from `/api/v1/settings/coding-auths`.
- `frontend/src/lib/coding-auth-metadata.ts` explicitly marks Amp and Pi as not participating in stack order.
- `frontend/src/app/(dashboard)/settings/account/page.tsx` only supports personal API-key auth for Codex, Claude Code, and OpenCode.
- `frontend/src/lib/agents.ts` still documents Pi as a meta-agent that inherits other providers' keys and uses `inheritsProviderKeys` to keep it out of the normal credential flows.
- Agent/model selection surfaces already know about `amp` and `pi` as agent types, but their credential UX is inconsistent with the rest of the product because those agents do not participate in the same auth primitives.

### Runtime behavior

- Amp is effectively a special env-var-backed integration, not a first-class coding auth.
- Pi is effectively a meta-agent wrapper around other agents' auth, not an independently configured coding agent.

## Goal

Amp and Pi should use the same coding-agent primitives as Codex, Claude Code, and OpenCode:

- auth stored in the same credential system
- visible in the same coding-auth stack
- visible in both organization and personal settings
- resolved through the same user/team/org lookup path
- isolated from app-level LLM keys and unrelated coding-agent keys
- exposed through the same API and UI concepts: auth row, label, status, default, fallback order, disable, rename
- available anywhere the frontend lets a user choose a coding agent or that agent's model/mode

The system should treat "Amp auth" and "Pi auth" as deliberate, inspectable runtime capabilities, not incidental env-variable side channels.

## Non-Goals

- Reworking the sandbox adapter protocol itself.
- Expanding app-level LLM settings to include more providers.
- Designing every possible upstream Pi provider integration in this pass.
- Preserving backward compatibility for `agent_config.amp.AMP_API_KEY` or Pi key inheritance forever.

## Design Principles

- One primitive for coding-agent auth. Auth for every coding agent should terminate in `org_credentials` / user credentials, not in ad hoc settings blobs.
- Credentials should be agent-scoped and explicit. Running Pi must not silently depend on Claude/Codex/Gemini setup.
- Settings vs auth must stay separate. `agent_config` may hold defaults like model/mode selection, but not durable secret material for coding agents.
- Resolution order should stay uniform. User personal → team default → org auth should apply to Amp and Pi the same way it applies to the other agents.
- The fallback stack should be authoritative. If an auth is runnable, it should be representable as a row in the same stack UI and API.

## Recommended Target Model

### 1. Add first-class credential providers for Amp and Pi

Introduce dedicated provider names and config types:

- `ProviderAmp`
- `ProviderPi`

Expected shape:

- `AmpConfig`
  - auth material required by Amp
  - optional mode/model defaults that belong to the credential row only if they are truly auth-coupled
- `PiConfig`
  - Pi-owned auth material
  - optional provider/model routing defaults
  - enough structure to avoid depending on sibling-agent credentials

The exact `PiConfig` fields should follow upstream Pi capabilities, but the key product rule is fixed: Pi auth is stored as Pi auth, not inferred from other agents.

### 2. Move Amp and Pi secrets out of `agent_config`

After the migration:

- `agent_config.amp` may still hold non-secret defaults like `AMP_MODE`, if we keep that as an org-level runtime default.
- `agent_config.pi` may still hold non-secret defaults like `PI_MODEL`, if we keep that as an org-level runtime default.
- secret values such as `AMP_API_KEY` must move into encrypted credential storage.
- Pi must stop inheriting OpenAI/Anthropic/Gemini keys from other agents.

### 3. Make Amp and Pi visible through `/api/v1/settings/coding-auths`

The coding-auth API should return Amp and Pi rows just like the existing agents:

- `agent`
- `auth_type`
- `label`
- `status`
- `priority`
- `is_default`
- `usage_note`

That makes fallback ordering, rename/disable, and default selection consistent.

### 3a. Make Amp and Pi visible in personal auth management

The personal credential path should stop treating Amp/Pi as out-of-band.

After the migration:

- `My settings` should allow adding, listing, and disabling personal Amp/Pi auths using the same core patterns as the other coding agents.
- personal Amp/Pi auths should participate in the same resolution order as other personal coding-agent auths.
- the frontend should not rely on `inheritsProviderKeys` or similar sentinels to hide Pi from the personal auth flow.

### 4. Resolve Amp and Pi through the same `AgentEnv` path

`AgentEnv.Resolve(...)` should stop treating Amp and Pi as a separate category.

Target behavior:

- `Resolve(...)` pulls Amp/Pi credentials from the same credential stores as the other agents.
- `CheckAuth(...)` validates the resolved Amp/Pi credential row, not ambient sibling-provider env.
- `NarrowScopedCredentials(...)` should disappear for Pi once Pi no longer inherits foreign keys.

### 5. Align Pi with upstream auth primitives

This needs one explicit design choice because upstream Pi supports its own auth file and OAuth/API-key resolution model.

Recommended direction:

- treat Pi like Codex/Claude Code: if upstream has a native auth-file-based path, inject Pi-owned auth material into Pi's expected on-disk location in the sandbox
- fall back to env vars only when that is the upstream-native mechanism for the configured Pi auth type

The important constraint is not the file format itself. The important constraint is that the source of truth is a Pi credential row, not borrowed keys from other agents.

## Migration Plan

### Phase 1: Data model and backend primitives

1. Add `ProviderAmp` and `ProviderPi` to `internal/models/credentials.go`.
2. Add typed config structs for both.
3. Extend provider parsing, validation, masking, and summary helpers.
4. Extend `CodingAgentProviders` and `ListCodingAuths(...)` so Amp/Pi rows participate in the coding-auth stack.
5. Extend `CreateCodingAuthInput` / `providerConfigForCodingAuthInput(...)` to support Amp and Pi.
6. Add store tests proving Amp/Pi rows round-trip through `org_credentials` and appear in `ListCodingAuths(...)`.

### Phase 2: Agent runtime alignment

1. Refactor `AgentEnv.Resolve(...)` so Amp and Pi use `resolveProviderConfig(...)` rather than `agent_config` secrets.
2. Remove Pi inheritance of `ANTHROPIC_API_KEY` / `OPENAI_API_KEY` / `GEMINI_API_KEY`.
3. Replace Pi's current auth preflight with Pi-specific credential validation based on the Pi credential row.
4. Delete `narrowPiProviderKeys(...)` once no foreign keys are exported.
5. If Pi needs auth-file injection, add an `InjectPiAuth(...)` helper parallel to `InjectCodexAuth(...)` / Claude credential injection.
6. Add orchestrator and env tests proving:
   - Amp works with only Amp auth configured
   - Pi works with only Pi auth configured
   - Pi no longer depends on Codex/Claude/Gemini auth presence

### Phase 3: API and UI parity

1. Update `/api/v1/settings/coding-auths` create/list/update behavior to fully support Amp and Pi.
2. Remove the frontend special cases that save Amp/Pi through `settings.agent_config`.
3. Make Amp and Pi reorderable in the same fallback stack UI.
4. Show Amp/Pi rows in the detail sheet with the same rename/disable/default controls.
5. Keep model/mode defaults in the UI, but store them via the same coding-auth flow or a clearly separate non-secret settings channel.
6. Update `My settings` so personal Amp/Pi auths can be added, listed, and removed in the same page as other personal coding-agent auths.
7. Update shared frontend metadata and agent registries so Amp/Pi are treated as normal coding agents instead of meta/special-case entries.
8. Update all agent-selection surfaces to include Amp/Pi consistently:
   - default agent selectors
   - session/project creation flows
   - setup and onboarding selectors
   - any model/mode picker that is keyed off the selected coding agent
9. Ensure model selection UX stays coherent:
   - Amp modes should be presented as Amp's selectable runtime choices anywhere the product lets a user choose an Amp model/mode.
   - Pi models should be selectable anywhere the product lets a user choose Pi as the coding agent.
   - these selectors should read/write through the same normalized agent metadata used by the rest of the frontend.

### Phase 4: Backward compatibility and cleanup

1. Add a one-time migration path from:
   - `agent_config.amp.AMP_API_KEY`
   - legacy Pi default-only setup
2. Backfill encrypted credential rows where possible.
3. Stop reading secret auth from `agent_config`.
4. Remove obsolete comments and tests that describe Amp/Pi as special cases.

## Open Design Choice: What Counts As "Pi Auth"?

Pi is the only tricky part.

There are two plausible implementations:

### Option A: Pi owns one upstream-native auth bundle

Store Pi credentials in the form Pi expects natively, then inject them into Pi's auth location in the sandbox.

Pros:

- strongest parity with "Pi is its own coding agent"
- closest to upstream Pi behavior
- no dependence on sibling-agent secrets

Cons:

- requires a deeper mapping from 143 credential rows to Pi's upstream auth file / provider model

### Option B: Pi rows store provider credentials, but under Pi ownership

A Pi credential row could contain the provider credential Pi needs for its selected model, without reusing the org's Codex/Claude/Gemini rows.

Pros:

- simpler migration from the current model-driven Pi UX
- still fixes the core problem: no cross-agent key reuse

Cons:

- weaker conceptual parity with Pi's own upstream auth system
- more custom 143-side translation logic

Recommendation: start with the strongest upstream-native Pi path we can support cleanly. If the first implementation must be narrower, prefer a Pi-owned credential row over continued key inheritance.

## Risks

- Pi upstream supports many providers, so an over-broad first pass could sprawl.
- Migrating off inherited keys may break orgs that currently rely on Pi "just working" because Claude/Codex/Gemini were already configured.
- UI churn is easy to underestimate because the current agent settings page has explicit Amp/Pi branches.

## Mitigations

- Ship in phases with backend read compatibility before removing old settings writes.
- Keep the first Pi scope narrow: support one well-defined Pi auth path, then expand.
- Add migration-time UI messaging when legacy Amp/Pi setup is detected.
- Preserve non-secret model/mode defaults separately from auth migration so behavior stays legible.

## Implementation Notes

This design is now implemented.

Delivered behavior:

- `ProviderAmp` and `ProviderPi` are first-class coding-agent credential providers with dedicated encrypted credential rows.
- Amp and Pi participate in `/api/v1/settings/coding-auths`, the org fallback stack, personal credentials, and resolved-credential previews.
- Legacy org-level Amp/Pi secrets in `agent_config` are now detectable and migratable from the coding-agent settings page, and migrated secrets are scrubbed back out of `agent_config`.
- `AgentEnv.Resolve(...)` now resolves Amp/Pi through the same user → team default → org credential path as the other coding agents.
- `agent_config` no longer carries Amp secrets or Pi borrowed-provider auth. It now only carries non-secret defaults:
  - `agent_config.amp.AMP_MODE`
  - `agent_config.pi.PI_MODEL`
  - `agent_config.pi.PI_MODEL_CUSTOM`
- Pi no longer inherits `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, or `GEMINI_API_KEY`. The sandbox receives `PI_API_KEY`, and the Pi adapter passes it through Pi's native `--api-key` flag.
- Frontend parity is complete across:
  - org coding-agent settings
  - `My settings` personal auth management
  - setup/onboarding readiness checks
  - session/model selection flows

Intentional first shipped scope:

- Pi is implemented as a dedicated Pi API-key credential rather than a translated upstream auth-file bundle.
- Amp/Pi model and mode defaults remain in org settings as non-secret runtime defaults instead of being embedded into credential rows.

## Success Criteria

- Amp auth is stored in encrypted credential storage, not `agent_config`.
- Pi no longer inherits `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, or `GEMINI_API_KEY` from other coding agents.
- Amp and Pi both appear in `/api/v1/settings/coding-auths`.
- Amp and Pi both appear in `My settings` as personal coding-agent auth options.
- Amp and Pi can be reordered, renamed, disabled, and selected as defaults in the same fallback stack as the other coding agents.
- Amp and Pi are available anywhere the frontend exposes coding-agent selection or model/mode selection.
- `AgentEnv` no longer documents Amp/Pi as exceptions to the first-class credential model.
- Legacy-detection comments/tests that still treated Amp/Pi as secret-in-`agent_config` exceptions are removed or updated.

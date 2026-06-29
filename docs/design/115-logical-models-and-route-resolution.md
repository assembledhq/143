# Design: Logical Models and Background Route Resolution

> **Status:** Implemented | **Last reviewed:** 2026-06-28

## Implementation Status

Implemented. The OpenCode model registry is the single source of truth
(`internal/models/opencode_models.go`): logical models, ordered routes, and the
audited US-only OpenRouter provider allowlists. Launch-time resolution walks a
selection's routes in priority order and picks the first runnable credential,
failing over across transports (`internal/services/agent/env.go`,
`resolveOpenCodeProviderConfig` → `resolveAcrossOpenCodeRoutes`). A pinned
`provider/model` id resolves a single explicit route; logical ids auto-route.
The frontend picker offers one entry per model with friendly labels
(`frontend/src/lib/model-constants.ts`, `OPENCODE_LOGICAL_MODELS`). The org
policy is `OrgSettings.OpenCodeRouting.RequireOpenRouter` with a Settings toggle.

**Decision taken — fallback policy default.** The draft recommended native as
opt-in (OpenRouter-only by default). The shipped default is the inverse:
**OpenRouter is always tried first, and native fallback is allowed by default**;
US/data-sensitive orgs opt into OpenRouter-only via `RequireOpenRouter`. This
avoids silently breaking orgs whose only OpenCode key is native (they relied on
the native default), while still defaulting to the recommended OpenRouter route.
A pinned native model id always bypasses the gate.

**Deferred (follow-ups, not blocking):**
- Per-model *disabled* state in the picker when an org has no runnable transport
  for that model. Agent-level availability already hides OpenCode entirely when
  no OpenCode key exists; per-model gating is a refinement.
- A resolved-route badge on the session detail ("GLM 5.2 · OpenRouter"). The
  resolved physical model is already recorded on the runtime credential binding
  and in `OPENCODE_MODEL`; surfacing it in the session UI is pending.

## Summary

Today an OpenCode model ID conflates two independent things: *which model* the user
wants (GLM 5.2) and *how to reach it* (the OpenRouter transport vs. the OpenCode-native
transport). Because of that coupling, the picker shows two entries for the same model
(`opencode/glm-5.2` and `openrouter/z-ai/glm-5.2`), and selecting the "wrong" one with a
valid key produces a misleading error:

> agent auth failed (opencode): missing OpenCode credential for OpenCode native. Add an
> OpenCode via OpenCode native auth or choose a model backed by an existing OpenCode key.

This proposal separates the two axes. The user picks a **logical model** (`glm-5.2`); at
session launch the system resolves it to a concrete **route** by walking a priority-ordered
list of transports and choosing the first one backed by a runnable credential. This
collapses the double list, removes the footgun, and adds automatic failover across
transports for free.

---

# Part 1 — Product Spec

## Problem

- **Duplicate entries.** Open-source models appear twice in the picker — once per transport.
  Users cannot tell `opencode/glm-5.2` from `openrouter/z-ai/glm-5.2` and have no reason to
  care about the difference.
- **Valid keys produce "missing credential" errors.** A user who added an OpenRouter key and
  picked the `opencode/*` variant is told they have no credential, even though they can run
  the model — just over a different transport.
- **No failover.** If the chosen transport's key is rate-limited or down, the session hard-
  fails instead of trying the other transport that would run the same model.

## Goals

- One picker entry per model. The user selects a model, not a transport.
- Background routing chooses the transport automatically from available credentials, in a
  configurable priority order (default: **OpenRouter first, then OpenCode-native**).
- Automatic failover: if the preferred transport's credential is missing or rate-limited,
  fall through to the next eligible transport for the same model.
- The resolved transport is **visible** after the fact, never a black box.
- No loss of capability: power users can still pin an explicit transport.

## Non-goals

- Changing which models are offered, their pricing, or the audited US-provider lists.
- Cross-*model* fallback (e.g. GLM → Kimi). This is cross-*transport* fallback for one model.
- Reworking first-party agents (Codex, Claude Code, Amp, Pi). Scope is the OpenCode adapter.

## Why "OpenRouter first"

The OpenRouter transport pins inference to an **audited US-only provider allowlist** with
`data_collection: deny` and `allow_fallbacks: false`. The OpenCode-native transport offers no
equivalent provider-location control — it routes direct to the upstream vendor's gateway.
OpenRouter is therefore the recommended default for US/data-sensitive orgs, and native is the
convenience fallback.

> **Decision taken — fallback policy.** Because native lacks the US-provider guarantee,
> "OpenRouter first, then native" means a *missing or rate-limited OpenRouter key silently
> downgrades the compliance posture.* The options considered:
>
> 1. **Native as silent fallback** — most convenient, weakest guarantee.
> 2. **OpenRouter-only by default, native opt-in** — strongest guarantee, but breaks orgs
>    whose only OpenCode key is native (they relied on the native default).
> 3. **Native allowed by default, OpenRouter-only opt-in** *(shipped)* — OpenRouter is always
>    tried first; native fallback is permitted unless the org sets
>    `OpenCodeRouting.RequireOpenRouter`. Non-breaking, defaults to the recommended route, and
>    gives US/data-sensitive orgs one switch to enforce OpenRouter-only.
>
> Shipped (3). A pinned native model id always bypasses the gate (explicit user choice).

## User experience

- **Picker.** Shows logical models (`GLM 5.2`, `Kimi K2.6`, `DeepSeek V4 Pro`, …) once. A
  small badge shows the route that *would* run given current keys (e.g. `via OpenRouter`).
- **No runnable route.** A logical model with zero runnable transports is shown disabled with
  an inline "Add an OpenRouter or OpenCode key to enable" affordance, rather than erroring at
  launch.
- **Session detail.** The actually-resolved route is recorded and shown: `GLM 5.2 · OpenRouter`.
  On failover, both the attempt and the fallback are visible.
- **Advanced / pinning.** An explicit transport can still be pinned (power users, debugging,
  reproducing a vendor-specific result). A pinned transport disables auto-routing for that
  selection.

## Acceptance criteria

1. Picking `GLM 5.2` with only an OpenRouter key runs over OpenRouter with the audited US
   allowlist; no "missing credential" error.
2. Picking `GLM 5.2` with only an OpenCode-native key runs over native (if org policy permits).
3. With both keys, OpenRouter is chosen by default; if its credential is rate-limited, the
   session fails over to native (when policy permits) instead of erroring.
4. The resolved route is displayed on the session and recorded for usage attribution.
5. Existing sessions/configs that stored a physical model ID continue to run, pinned to that
   transport.

---

# Part 2 — Engineering Spec

## Current state

- Model IDs are physical and transport-encoded: `opencode/glm-5.2`,
  `openrouter/z-ai/glm-5.2` (`internal/models/agent_model_constants.go`).
- The selected ID is stored as `OPENCODE_MODEL` (or `OPENCODE_MODEL_CUSTOM`) in the agent
  defaults / session config.
- At launch, `resolveOpenCodeProviderConfig` (`internal/services/agent/env.go:1073`) derives a
  single backing provider from the prefix via `openCodeBackingProviderForModel`
  (`env.go:1134`), finds one matching credential row, and errors with
  `missingOpenCodeBackingBlock` (`env.go:1163`) if none match.
- The runtime config JSON (`openCodeRuntimeConfigContent`, `env.go:1226`) and the US-provider
  allowlist (`auditedUSOpenRouterModelProviders`, `env.go:1257`) are derived from that single
  resolved transport. The allowlist block is only emitted for the OpenRouter transport.
- The model list is hand-synced between Go (`agent_model_constants.go`) and TS
  (`frontend/src/lib/model-constants.ts`), plus the US-provider map in Go.

## Target architecture

### 1. Logical model registry (single source of truth)

Introduce a backend-owned registry. A logical model maps to an **ordered** list of routes;
each route is a (transport, physical model ID, optional US-provider allowlist) tuple. Only
genuinely interchangeable transports may be merged under one logical model (same weights,
context window, quantization).

```go
type Transport string // "openrouter", "opencode-native", "anthropic", "openai", "gemini"

type ModelRoute struct {
    Transport       Transport
    PhysicalModelID string   // passed to `opencode run --model`
    USProviderList  []string // OpenRouter `only`/`order`; empty ⇒ no allowlist
}

type LogicalModel struct {
    ID          string       // "glm-5.2" (stored in config)
    DisplayName string       // "GLM 5.2"
    Routes      []ModelRoute // priority order is the *default* ordering; org policy reorders
}
```

The registry replaces both `AvailableOpenCodeModels` and `auditedUSOpenRouterModelProviders`
as the source of truth. The US allowlist moves onto the route it belongs to, so route and
allowlist can never drift apart. Single-route models (e.g. `gpt-5.4`, `claude-sonnet-4-6`)
are just logical models with one route — the abstraction is uniform.

The frontend consumes the registry via a read API (or build-time codegen from the Go table),
eliminating the hand-synced TS constants and the `openCodeModelsForBackingProvider` filtering
in `coding-auth-metadata.ts`.

### 2. Route resolution at launch

Replace "derive one backing provider, match one credential" with "walk routes in policy order,
pick first runnable":

```go
func (e *AgentEnv) resolveOpenCodeRoute(
    ctx context.Context, orgID uuid.UUID, userID *uuid.UUID, selection ModelSelection,
) (ModelRoute, models.ProviderConfig, *credentialBlock) {

    // Pinned physical ID ⇒ single-route list, auto-routing disabled.
    routes := selection.Routes(orgPolicy(ctx, orgID)) // policy reorders / filters transports

    var sawAny, sawRateLimited bool
    for _, route := range routes {
        rows := e.runnableCredentialsForTransport(ctx, orgID, userID, route.Transport)
        for _, cred := range rows {
            if !credentialRunnableForModelAwarePick(cred) {
                sawRateLimited = sawRateLimited || isRateLimited(cred)
                continue
            }
            sawAny = true
            cfg := providerConfigForRoute(route, cred)
            e.recordCredentialPick(orgID, userID, cred.Provider, cred)
            return route, cfg, nil
        }
    }
    return ModelRoute{}, nil, noRunnableRouteBlock(selection, sawAny, sawRateLimited)
}
```

Key properties:

- **Failover is inherent.** A rate-limited OpenRouter credential is skipped; the loop
  continues to the native route (if policy includes it). This subsumes the existing
  rate-limit handling and generalizes it across transports.
- **Policy is the priority.** `orgPolicy` returns the transport ordering and the allowed
  transport set. Default: `[openrouter, opencode-native, anthropic, openai, gemini]` with
  native gated by the opt-in flag (per the Part 1 decision).
- **The chosen route drives everything downstream** — the physical `--model` string, the
  runtime-config provider block, and the US allowlist all come from `route`, not from stored
  config. `openCodeRuntimeConfigContent` takes the resolved `ModelRoute` and emits the
  allowlist block iff `route.USProviderList` is non-empty (works for any transport, not just a
  hardcoded OpenRouter check).

### 3. Selection model and storage

`ModelSelection` is either a **logical id** (auto-route) or a **pinned physical id** (single
route, auto-routing off). This reuses the existing two-field convention:

- `OPENCODE_MODEL = "glm-5.2"` (logical) ⇒ auto-route.
- `OPENCODE_MODEL_CUSTOM = "opencode/glm-5.2"` (physical) ⇒ pinned route / escape hatch for
  uncurated slugs, exactly as today.

The adapter default (`opencode.go:77`, `openCodeStreamingConfig`) changes from the physical
`OpenCodeModelGLM52` constant to the logical default; resolution maps it to a physical ID
before constructing the CLI command.

### 4. Migration

- **Reads.** A `physical → logical` lookup maps any stored physical ID to its logical model
  for display. Unknown/uncurated physical IDs are treated as pinned and pass through verbatim.
- **No forced rewrite.** Existing stored physical IDs keep working as pins — satisfying
  acceptance criterion 5 — so the migration is non-destructive. Optionally, a one-time
  backfill can rewrite curated physical IDs to their logical id to normalize new sessions; not
  required for correctness.
- **Per the repo convention, renumber this doc against `origin/main` before pushing** (current
  max is 114; this is 115) and keep `95-opencode-agent-adapter.md` in sync — the US-provider
  audit notes there must point at the new per-route allowlist location.

### 5. Observability & cost attribution

- Token usage cost parsing (`token_usage_cost.go:249`, `splitProviderModel`) keys off the
  physical provider/model. Record the **resolved** `ModelRoute.PhysicalModelID` on the run so
  attribution is unchanged.
- Persist and surface the resolved transport on the session (`GLM 5.2 · OpenRouter`) and log
  failover transitions (preferred transport skipped → reason → fallback chosen) for support.
- Update `effectiveOpenCodeModel` / `updateRuntimeCredentialBindingModel` (`env.go:1066`,
  `env.go:1188`) to record the logical selection plus the resolved physical route.

### 6. Error messaging

`missingOpenCodeBackingBlock` (`env.go:1163`) is replaced by `noRunnableRouteBlock`, framed
around the logical model and the transports actually tried, e.g.:

> No runnable route for **GLM 5.2**. Tried OpenRouter (no key) and OpenCode-native (disabled by
> org policy). Add an OpenRouter key, or enable native routing in Settings.

Rate-limited-only cases keep the existing rate-limit block, now reported per logical model.

## Testing

- **Resolver unit tests** (`env_test.go`): only-OpenRouter, only-native, both (prefers
  OpenRouter), OpenRouter rate-limited ⇒ native fallback, policy excludes native ⇒ error not
  fallback, pinned physical ID bypasses auto-routing, no runnable route ⇒ correct block.
- **Runtime config tests**: US allowlist emitted for OpenRouter route, omitted for native;
  allowlist sourced from the route, not a transport literal.
- **Registry invariants**: every logical model has ≥1 route; merged routes are flagged
  interchangeable; every OpenRouter route with a slash has an audited US-provider list (port
  the existing audit assertion).
- **Migration tests**: stored physical IDs resolve to logical display and run pinned;
  uncurated custom slugs pass through.
- **Integration** (`make test-integration`): launch a session per scenario; assert the CLI
  `--model` and the generated runtime config JSON match the resolved route.
- Frontend: picker renders one entry per logical model; disabled state when no runnable route;
  resolved-route badge reflects available keys.

## Rollout

1. Land the registry + resolver behind the logical-model path while continuing to accept
   physical IDs (pinned). No user-visible change yet.
2. Switch the picker to logical models; keep an "advanced: pin transport" control.
3. Add the org fallback policy (default OpenRouter-only, native opt-in) and the resolved-route
   UI.
4. Optional backfill to normalize curated physical IDs to logical.

## Open questions

- **Policy granularity** — org-level only, or also per-session / per-user override? Default to
  org-level; revisit if users ask for per-session control.
- **Per-model priority** — is the transport order ever model-specific (e.g. prefer native for
  one model), or always the org-wide order? Start org-wide; the registry can carry a per-model
  override later without a schema change.
- **Failover visibility threshold** — surface every failover, or only when it crosses a
  compliance boundary (OpenRouter → native)? Leaning: always record, prominently flag only the
  compliance-relevant ones.

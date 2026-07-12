# Design: Agent-Native Preview Verification

> **Status:** In progress — Phase 1 complete; Phase 2 core implemented | **Last reviewed:** 2026-07-12

## Implementation Status

Phase 1 and the Phase 2 core now provide the durable session-owned browser
foundation and native `ensure`/`observe`/`act` path: persisted browser identity and storage state,
compatible-origin restoration, bounded accessibility/DOM and console-cursor
observations, serialized structured actions, configured path enforcement,
worker routing, explicit session-token preview capabilities, artifact-backed
screenshots, implicit session targeting, an audited human/agent control lease,
and a preview panel that watches and controls the same worker-owned browser
context used by the agent. Active-action fencing prevents human takeover and
agent input from racing. MCP observations carry compact semantic context plus
native PNG image content; CLI-only runtimes retain a private workspace-file
fallback, and durable artifact references remain authoritative evidence.

Adapter-by-adapter native-image compatibility still needs rollout validation;
CLI-only runtimes can use the implemented workspace fallback. Verification
evidence and automatic diff-aware verification remain future phases. Repository
`browser` and `verification` policy parsing, defaults, and validation are already
implemented as groundwork for Phase 3.

## Summary

143 should make preview verification a native part of a coding-agent session,
not a sequence of loosely related browser commands the model must remember to
assemble. After a user-facing edit, the agent should be able to update the live
application, observe it through pixels and semantic browser state, exercise a
focused workflow, fix failures, and leave concise evidence for the supervising
engineer.

This plan builds on the implemented lifecycle and inspection surface in
[114-agent-preview-tools.md](../implemented/114-agent-preview-tools.md). It does
not replace those APIs. It connects them into one reliable product loop:

```text
edit -> ensure/update preview -> observe -> act -> diagnose -> fix -> verify
                                      |                         |
                                      +---- shared evidence <---+
```

The initial product should remain web-preview-specific. Full desktop control,
arbitrary public-web automation, broad visual-regression management, and edited
video recordings are later extensions.

## Product Goal

A coding agent working in a 143 session can verify a UI change without human
operation while the user can watch, take control when necessary, and review
what was tested. The agent and user operate the same browser context, including
the same URL, cookies, local storage, and authenticated application state.

The default agent workflow should require no preview ID, worker address, port,
credential exchange, or artifact plumbing:

```bash
143-tools preview ensure --wait
143-tools preview observe
143-tools preview act --steps '[...]'
143-tools preview update --wait
```

`143_SESSION_ID` is the implicit target. Explicit session and preview IDs remain
available for diagnostics and privileged workflows.

## Product Principles

1. **The session owns the browser.** A session preview has one durable browser
   context that survives app reloads and safe service restarts.
2. **Observation combines pixels and semantics.** The model receives a native
   image together with URL, viewport, accessibility/DOM state, and relevant
   console errors.
3. **Verification is automatic.** User-facing edits trigger a bounded,
   diff-aware verification loop by default; correctness does not depend on the
   model remembering optional guidance.
4. **Agent and human share state.** The supervising user can see and explicitly
   take control of the same browser when login, MFA, CAPTCHA, or judgment needs
   human input.
5. **Claims carry evidence.** Completion records what was exercised and what the
   browser showed without flooding the transcript with raw logs or base64.

## P0 Feature Set

### 1. Session-Owned Preview And Browser Context — Implemented

Add `preview ensure` as the idempotent agent entry point. It resolves the
current session, starts configured services when necessary, waits for the
primary readiness probe, and creates or resumes the session's browser context.
`preview create` remains supported as an alias for compatibility.

The browser context persists across:

- Browser reloads.
- HMR updates.
- Soft service restarts.
- Full preview recycles when the runtime can safely restore browser storage.

The context tracks the current page, cookies, local storage, viewport, console
cursor, and last successful observation. A cold relaunch may replace the app
runtime without silently replacing browser identity. When state cannot be
restored, the tool response must say so.

The API and CLI continue to target platform preview origins. Agents never
receive worker-local browser endpoints or preview credentials. A platform-owned
Playwright/CDP bridge may be added for complex scripted flows, but it must bind
to the same authorized browser context and remain unreachable outside the
session tool path.

### 2. Unified Observe And Act — Core Implemented

Add a high-signal `preview observe` operation. One response includes:

- Screenshot as a native model image attachment.
- Stored artifact reference and short-lived signed URL.
- Current URL, title, viewport, and capture time.
- Bounded accessibility tree or requested DOM excerpt.
- New console errors since the previous observation.
- Readiness and browser-context status.

The CLI may also write the image to a workspace path for runtimes that cannot
consume image tool results. Large image base64 is opt-in and excluded from the
normal transcript.

`preview act` executes structured steps against the same browser and returns an
observation after any meaningful state change. Supported P0 actions are:

- Navigate within the preview origin.
- Click, fill/type, select, check/uncheck, and press a key.
- Hover and scroll.
- Wait for selector, URL, text, readiness, or network idle.
- Change viewport.

Semantic selectors and accessibility roles are preferred. Coordinates are a
fallback for visual-only surfaces. A failed step returns its index, URL,
selector match count, browser error, screenshot, and relevant console messages.

Existing `screenshot`, `inspect`, `interact`, and `console` commands remain
available as lower-level and compatibility operations.

The MCP transport now returns the bounded observation as text followed by a
validated native `image/png` content block. It removes inline base64 from the
text block, retains durable artifact metadata, rejects missing, malformed,
unsupported, or oversized image payloads, and preserves `--output` as a private
workspace-file fallback. Confirming native image consumption in each supported
coding-agent adapter remains rollout work.

### 3. Automatic Verification Loop

The orchestrator should trigger preview verification after an agent changes
user-facing code and before it reports completion. It should:

1. Derive a small test plan from the user request, changed files, and routes.
2. Ensure or update the session preview.
3. Exercise only the affected workflows and configured smoke paths.
4. Observe visual state, semantic state, readiness, and console errors.
5. Let the agent fix failures and rerun within bounded attempts and time.
6. Report success, a concrete failure, or required human intervention.

Automatic verification is on by default when preview configuration exists. It
is skipped when the diff has no likely user-facing effect, the repository
explicitly disables it, or preview startup fails with an actionable setup
error. Skips are recorded; they are not presented as successful verification.

This loop belongs in orchestration policy rather than prompt text alone. Agent
guidance still explains the tools, but the runtime is responsible for deciding
that verification is due and making image observations available to the model.

### 4. Shared Human/Agent Browser — Implemented

The existing preview UI should render the session browser's live page rather
than opening an unrelated browser context. It exposes three explicit states:

- `agent_control`: the user can watch while agent actions are accepted.
- `human_control`: agent browser actions pause while the user interacts.
- `waiting_for_handoff`: the agent requested help and supplied a reason.

Taking control is an audited lease, not simultaneous input. Returning control
preserves cookies, local storage, URL, and page state. This supports login, MFA,
CAPTCHA, seed-data preparation, and ambiguous visual decisions without creating
a second environment.

Both the sandbox token and user session can access the browser and its artifacts
through their respective authenticated platform paths. Neither needs direct
network access to the worker. Same-session sandbox tokens cannot inspect or
control another session's preview.

### 5. Verification Evidence

Each automatic or requested verification produces a compact run record:

- Preview ID, session ID, workspace revision, and config digest.
- Test-plan steps, routes, and viewports.
- Action outcomes and failed-step diagnostics.
- Final screenshot references.
- Console-error summary.
- Result: `passed`, `failed`, `skipped`, or `human_intervention_required`.

The session transcript receives a concise summary and references; artifact bytes
remain in object storage or a workspace-visible file. The preview panel and run
detail can render the same evidence. Screenshot sequences and an action
transcript are P0. GIF or video recording is a later presentation layer over
the same event stream.

## Repository Configuration

Extend the existing `preview` section in `.143/config.json`. Service launch,
ports, environment, infrastructure, and readiness continue to use the current
`preview.install`, `preview.services`, and `preview.primary` fields. Do not add a
second launch file.

Add optional `browser` and `verification` policy:

```json
{
  "preview": {
    "primary": "frontend",
    "services": {
      "frontend": {
        "command": ["npm", "run", "dev"],
        "port": 3000,
        "ready": {"http_path": "/", "timeout_seconds": 120},
        "hmr": true
      }
    },
    "browser": {
      "persist_session": true,
      "default_viewport": {"width": 1440, "height": 900},
      "allowed_paths": ["/**"]
    },
    "verification": {
      "auto": true,
      "max_attempts": 3,
      "timeout_seconds": 300,
      "viewports": [
        {"name": "desktop", "width": 1440, "height": 900},
        {"name": "mobile", "width": 390, "height": 844}
      ],
      "smoke_paths": ["/"],
      "fail_on_console_error": true
    }
  }
}
```

Configuration semantics:

| Field | Default | Contract |
|---|---|---|
| `browser.persist_session` | `true` | Preserve browser storage across safe updates and control handoffs. |
| `browser.default_viewport` | platform desktop viewport | Initial browser size and default observation size. |
| `browser.allowed_paths` | `/**` | Preview-origin paths the agent may navigate; never expands to arbitrary origins. |
| `verification.auto` | `true` | Run verification after likely user-facing edits. |
| `verification.max_attempts` | `3` | Maximum agent fix-and-reverify cycles for one turn. |
| `verification.timeout_seconds` | `300` | Total verification budget, excluding initial preview build limit. |
| `verification.viewports` | default viewport only | Named viewport set for affected-route checks. |
| `verification.smoke_paths` | primary readiness path | Repository-owned routes included in generated smoke plans. |
| `verification.fail_on_console_error` | `true` | Treat newly observed error-level console entries as verification failures. |

These fields guide verification; they do not encode a large test DSL. Complex,
deterministic workflows should remain normal Playwright tests checked into the
repository. A later `verification.commands` field may reference those tests if
the native browser bridge can execute them against the shared context safely.

Config changes participate in the existing preview config digest and force the
existing full-recycle classification. Browser storage may be restored after the
new config is validated, subject to `persist_session` and origin compatibility.

## Architecture

```text
Coding agent runtime
  | ensure / observe / act / update
  v
143 API + session authorization
  |                         |
  |                         +--> verification run + artifacts
  v
Worker preview controller
  | app services from .143/config.json
  | persistent browser context
  | screenshot + DOM/accessibility + console
  v
Isolated preview origin
  ^
  |
143 preview UI <--> audited human/agent control lease
```

New responsibilities should remain separated:

- The preview manager owns app runtime lifecycle and config application.
- A browser-session service owns persistent browser context and control leases.
- `PreviewInspector` owns bounded observation and actions.
- The preview tool transport turns screenshot bytes into native MCP image
  content while preserving artifact references and the CLI workspace fallback;
  coding-agent adapters consume the richest form they support.
- A verification coordinator owns trigger policy, bounded retries, plans, and
  evidence records.

## Security And Isolation

- Sandbox tokens receive preview capabilities scoped to their own session.
- Read, interact, and manage permissions remain distinct for users and service
  tokens.
- Browser navigation is restricted to the active 143 preview origin and
  configured paths. Redirects outside that origin fail closed.
- Preview pages are untrusted input. Browser observations must be treated as
  data, not instructions, and the agent must not gain integration credentials
  through page content.
- Network access continues to follow preview network policy; browser automation
  cannot bypass it.
- Console, DOM, screenshots, and evidence apply existing secret redaction and
  size limits.
- Human takeover revokes the active agent action lease until explicitly
  returned or timed out.
- Artifact URLs are short-lived and authorized independently from main-app
  browser cookies so sandbox agents can retrieve their own evidence.

## Delivery Plan

### Phase 0: Repair The Existing Agent Path

- **Complete:** Separate public and internal API base URLs and normalize legacy environment
  values.
- **Complete:** Default preview tools to `143_SESSION_ID` and inject it in every coding-agent
  launch path.
- **Complete:** Add explicit preview capability mappings and same-session authorization tests.
- **Complete:** Verify sandbox-token access to preview endpoints and artifact downloads.

### Phase 1: Persistent Session Browser

- **Complete:** Add `preview ensure` and session-default targeting.
- **Complete:** Introduce browser-context identity and persistence across update modes.
- **Complete:** Route the preview UI and agent inspector to the same context.
- **Complete:** Add control-lease APIs and cross-session isolation tests.

### Phase 2: Native Observe/Act

- **Complete:** Add combined observation responses and actionable step failures.
- **Complete:** Store screenshots and provide workspace paths plus signed URLs.
- **Complete:** Return screenshots as native MCP image content while preserving
  the workspace-file fallback for CLI-only runtimes.
- **Complete:** Add semantic actions against the platform-owned shared browser
  context. A general-purpose Playwright/CDP attachment remains optional and is
  deferred until a workflow requires it.
- **Pending rollout validation:** Confirm which supported coding-agent adapters
  consume MCP image blocks directly and document/use the workspace fallback for
  adapters that do not.

### Phase 3: Automatic Verification And Evidence

- **Complete:** Parse and validate `.143/config.json` browser/verification policy.
- Add diff-aware verification triggers, bounded retries, and smoke plans.
- Persist verification summaries and artifact references.
- Render evidence and handoff state in session/preview UI.

### Phase 4: Quality Improvements

- Add reusable checked-in scripted workflows against the shared context.
- Add responsive and baseline visual comparisons where repositories opt in.
- Add GIF/video rendering from recorded interaction events.
- Evaluate full desktop control separately from web preview verification.

## Acceptance Criteria

The P0 product is complete when a fresh coding-agent session can:

1. Run `143-tools preview ensure --wait` without specifying an ID.
2. Receive the rendered page as native image input plus semantic/error context.
3. Complete a multi-step authenticated workflow across safe app updates without
   losing browser state.
4. Detect a visual, DOM, readiness, or console failure, edit code, and reverify.
5. Ask for human control and resume from the exact browser state afterward.
6. Finish with an auditable report and screenshots accessible to both the agent
   and supervising user.
7. Fail closed when the sandbox token targets another session or navigation
   leaves the preview origin.

Criteria 1, 2, 3, 5, and 7 are implemented at the platform/tool-contract
level. Criterion 4 has the required observe/act/update and diagnostic
primitives, but automatic failure-driven re-verification remains Phase 3.
Criterion 6 remains pending durable verification-run evidence. Criterion 2
also retains adapter rollout validation before native image consumption can be
claimed uniformly across every supported coding-agent runtime.

## Deferred Features

- General computer/desktop use outside web previews.
- Browser automation against arbitrary external websites.
- Simultaneous human and agent browser input.
- A repository-specific assertion language that duplicates Playwright.
- Cross-browser matrices and broad visual-baseline administration.
- Edited video as a requirement for successful verification.

## Open Questions

- Which coding-agent adapters consume MCP image content directly in production,
  and which should be configured or guided to use the implemented workspace-file
  fallback?
- Should a full preview recycle restore cookies only, or cookies plus local and
  session storage?
- How should the verifier identify user-facing diffs in repositories with
  custom frameworks or generated frontend bundles?
- Should browser contexts expire with the sandbox, the session, or a shorter
  independent idle TTL?
- Which evidence belongs in the canonical session transcript versus a dedicated
  verification-run detail surface?

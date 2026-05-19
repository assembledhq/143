# Design: Cursor CLI Coding Agent

> **Status:** Future
>
> **Linear:** VIR-99
>
> **Last reviewed:** 2026-05-19
>
> **Related docs:** [overall.md](../overall.md), [implemented/57-coding-agent-settings-rethink.md](../implemented/57-coding-agent-settings-rethink.md), [implemented/78-coding-agent-rate-limit-fallback.md](../implemented/78-coding-agent-rate-limit-fallback.md), [future/70-live-agent-command-handles.md](70-live-agent-command-handles.md), [future/75-thread-runtime-handles-and-durable-inbox.md](75-thread-runtime-handles-and-durable-inbox.md), [future/78-review-agent-loops.md](78-review-agent-loops.md)

## Summary

143 should add Cursor CLI as a first-class coding agent by following the same
adapter, credential, runtime-handle, cancellation, review-loop, and settings
surfaces used by Codex, Claude Code, Gemini CLI, Amp, and Pi.

The first implementation should use Cursor's headless `cursor-agent -p` mode
for regular 143 runs because it gives scriptable execution, structured output,
and file modification support. Interactive Cursor CLI mode should remain a
supported follow-up path once 143's live command handles and durable inbox can
deliver stdin and cancellation semantics consistently across agents.

The launch credential must preserve Cursor subscription economics. The first
engineering milestone is therefore a billing proof that decides whether the
launch credential is a Cursor User API Key or browser-login subscription auth.

Cursor should not become a special automation path. It should be another value
in the coding-agent registry with provider-specific details hidden behind the
same platform contracts.

## External Cursor CLI Capabilities

Cursor's current CLI docs and changelog describe these relevant surfaces:

- `cursor-agent` runs the Cursor agent from a terminal.
- `-p` / `--print` runs non-interactively for scripts and automation.
- `--force` lets headless runs make file changes instead of only proposing
  edits.
- `--output-format` supports at least `text`, `json`, and `stream-json`.
- API-key auth can be provided with `CURSOR_API_KEY` or `--api-key`; browser
  login is also available for local interactive use.
- Cursor reads project-level rules such as `AGENTS.md`, `CLAUDE.md`, and
  `.cursor/rules`.
- Cursor supports history and resume with explicit thread IDs.
- CLI plan mode is available through `/plan` or `--mode=plan`; ask mode is
  available through `/ask` or `--mode=ask`.
- Review is exposed in the CLI UX through change-review flows. The issue asks
  us to support `/review`; before implementation, run a local spike against
  the installed Cursor CLI version and capture whether `/review` is accepted in
  headless mode, only in interactive mode, or replaced by another documented
  review command.

Sources:

- [Cursor CLI overview](https://docs.cursor.com/en/cli/overview)
- [Cursor CLI usage](https://docs.cursor.com/en/cli/using)
- [Cursor headless mode](https://docs.cursor.com/en/cli/headless)
- [Cursor CLI authentication](https://docs.cursor.com/en/cli/reference/authentication)
- [Cursor CLI parameters](https://docs.cursor.com/en/cli/reference/parameters)
- [Cursor CLI agent modes changelog, 2026-01-16](https://cursor.com/changelog/cli-jan-16-2026)

## Goals

- Add `cursor_cli` as a selectable coding agent everywhere users can choose an
  agent.
- Store Cursor auth in the unified coding-agent credential stack, with personal
  before organization fallback, rate-limit health, and status reporting.
- Preserve Cursor subscription-level pricing. Do not ship a launch path that
  silently moves users onto unrelated BYO provider-key economics.
- Run Cursor inside the existing sandbox, branch guardrail, GitHub auth socket,
  preview, snapshot, and PR pipeline.
- Support regular implementation runs, continuation turns, cancellation,
  plan-only runs, and review-loop runs through existing 143 abstractions.
- Preserve Cursor-native behavior where it matters: rules files, model
  selection, resume IDs, plan/ask/review commands, stream events, and usage
  reporting.
- Make gaps explicit when Cursor does not expose a durable or structured
  equivalent to an existing 143 capability.

## Non-Goals

- Replacing Codex as the default agent.
- Using Cursor Cloud Agents as the initial execution backend.
- Building a separate Cursor-specific session UI.
- Persisting Cursor's full local auth store as an opaque user home directory
  unless API-key auth proves insufficient.
- Shipping without tests. This is an implementation plan only; code changes
  must still follow test-first development.

## Recommended Runtime Choice

### Start with headless mode for production runs

Use:

```bash
cursor-agent -p --force --output-format stream-json < .143-prompt.md
```

Rationale:

- It fits the current adapter model: one command, streamed output, exit code,
  diff collection after completion.
- It supports automation and file edits without per-edit prompts. Cursor's
  docs describe `--force` as forcing allowed commands unless explicitly denied,
  so this relies on the sandbox as the permission boundary and needs explicit
  deny/egress validation before production rollout.
- `stream-json` can feed the same transcript, token-usage, and human-input
  parsing patterns used by Codex and Claude Code.
- It avoids depending on terminal keybindings for the first production
  integration.

The adapter should prefer stdin or a prompt file over shell-interpolating the
prompt. This matches the current Codex and Claude implementations and keeps
large prompts, attachments, and embedded quotes predictable.

### Keep interactive mode as a later capability

Cursor's interactive CLI has product value: native plan mode, ask mode, review
navigation, MCP menus, and conversational handoff. 143 should not block the
first integration on fully emulating that TUI.

Interactive Cursor should be enabled after the live-command-handle contract is
the standard path for all agent runs. At that point the Cursor adapter can
declare:

```go
agent.AgentRuntimeProfile{
    Cancellation:      agent.DefaultCancellationSpec,
    RequiresTTY:      true,  // verify during spike
    RequiresOpenStdin: true, // required for live follow-up and Ctrl+C
    PreferSplitOutput: false,
}
```

If Cursor headless cancellation works cleanly through process-group SIGINT,
the initial headless profile can stay no-TTY with split output.

### Sandbox policy for `--force`

Cursor's `--force` is acceptable only if the sandbox remains the hard boundary:

- filesystem writes stay inside the checked-out workspace and existing sandbox
  home/config directories
- branch guardrails prevent pushing outside the session branch
- metadata/private-network egress remains blocked by worker host policy
- secrets are injected through the scoped credential path, not ambient host env
- destructive commands are limited to the sandbox filesystem
- Cursor-specific MCP/tool config is either disabled or generated from 143's
  approved integration-tool surface

The Phase 0 spike should include a malicious-prompt safety fixture that asks
Cursor to read host metadata, private network addresses, mounted auth sockets,
and unrelated home-directory files. The expected result is not that Cursor
refuses; the expected result is that the sandbox blocks access or exposes only
the scoped runtime material.

## Agent Registry And Types

Add a new model enum:

```go
const AgentTypeCursorCLI AgentType = "cursor_cli"
```

Update:

- `AgentType.Validate()`
- enum DB sync tests and migrations for any DB enum/check constraints
- session composer agent metadata
- onboarding/default-agent selector
- settings agent/auth metadata
- usage and billing agent filters
- adapter registry `adapters.DefaultMap(...)`
- worker tests that enumerate valid agent types

Do not name it just `cursor`; the product already uses "cursor" heavily for
pagination. `cursor_cli` is unambiguous in logs, DB rows, and metrics.

## Credential Model

Add Cursor provider rows to the coding-agent credential registry:

```go
const ProviderCursorAPIKey ProviderName = "cursor_api_key"
const ProviderCursorSubscription ProviderName = "cursor_subscription"

type CursorAPIKeyConfig struct {
    APIKey string `json:"api_key"`
}

type CursorSubscriptionConfig struct {
    // Exact shape depends on the subscription-auth spike. This should hold
    // either a portable encrypted credential bundle or a reference to a
    // host-managed credential/proxy record, never raw desktop state.
    CredentialBundle []byte `json:"credential_bundle,omitempty"`
    CredentialRef    string `json:"credential_ref,omitempty"`
}
```

Runtime env:

```bash
CURSOR_API_KEY=<decrypted key>
```

Cursor CLI does not require an API key in every environment. Its documented
browser login authenticates the local CLI with a Cursor account. Its documented
automation path uses User API Keys generated in the Cursor dashboard under
Integrations.

Do not conflate three different credential classes:

1. **Cursor User API Key.** Passed as `CURSOR_API_KEY` or `--api-key` to
   `cursor-agent`. This is the documented headless/CI path and is scoped to a
   Cursor account. It may be enough for subscription economics, but we should
   prove that by running a billing-dashboard test against a paid Cursor account.
2. **Cursor browser-login subscription auth.** Created by `cursor-agent login`.
   This is the clearest semantic match for "use my Cursor subscription", but
   Cursor does not document a stable server/device-code flow or portable
   credential format.
3. **BYO model-provider keys.** OpenAI/Anthropic/Google keys configured inside
   Cursor settings. These are not the same as Cursor CLI User API Keys and
   should not be the 143 integration path if the product goal is Cursor
   subscription-level pricing.

The first milestone should be a **billing proof**, not a full adapter:

- Run a paid Cursor account with included usage.
- Execute `cursor-agent -p --force --output-format stream-json` with a Cursor
  User API Key.
- Verify whether usage appears against the Cursor plan/included API-agent usage
  bucket.
- Repeat with browser login.
- Record the observed `cursor-agent status` output, stream metadata, and
  billing-dashboard deltas as fixtures or runbook screenshots.

If Cursor User API Keys consume included subscription usage, v1 should use
`cursor_api_key` because it is deterministic and automation-safe. If User API
Keys bypass included usage or have worse pricing/limits, promote
`cursor_subscription` to the launch credential and gate the adapter on
subscription-auth rehydration.

### Subscription auth path

This should be feasible in principle, but it needs a Cursor-specific spike
instead of assuming it works exactly like Codex or Claude Code. Treat it as a
launch blocker if the billing proof shows User API Keys do not meet the
subscription-pricing goal.

Cursor's official docs say browser login stores credentials locally, but they
do not document a stable credential file path, JSON schema, refresh contract, or
device-code flow. Docker's sandbox integration for Cursor shows one workable
pattern: when no API key is set, Cursor signs in interactively and the sandbox
proxy intercepts the token exchange with `api2.cursor.sh/auth/poll`; credentials
are managed by the host and are not stored inside the sandbox.

143 has two viable designs:

1. **Portable credential capture.** Run `cursor-agent login` in a controlled
   auth helper with an isolated `$HOME`, let the user complete the browser
   flow, discover the Cursor credential files that changed, encrypt only those
   files into `coding_credentials`, and rehydrate them into each sandbox before
   `cursor-agent` runs. This matches the Codex/Claude shape if Cursor's stored
   auth is portable and refreshable.
2. **Host-managed Cursor auth proxy.** Keep Cursor subscription credentials on
   the worker host or in a small credential service, and let sandboxes reach
   Cursor through a per-session auth proxy. This is closer to Docker's sandbox
   design and closer to 143's existing host credential socket model, but it is
   more infrastructure than plain file injection.

Prefer portable credential capture if the spike proves the credential bundle is
small, stable, and refreshable. Prefer the host-managed proxy if Cursor stores
tokens in platform keychains, uses non-portable state, or couples refresh to a
local auth service.

#### Experiment log: 2026-05-30

Tested Cursor Agent CLI `2026.05.28-a70ca7c` on Linux/gVisor.

Observed CLI behavior:

- `cursor-agent login --help` documents `NO_OPEN_BROWSER` for headless login.
- With `NO_OPEN_BROWSER=1`, `cursor-agent login` prints a browser URL in the
  form `https://cursor.com/loginDeepControl?...&redirectTarget=cli` and polls
  `https://api2.cursor.sh/auth/poll`.
- The login poll returns an `accessToken` and `refreshToken`; the CLI stores
  them via its credential manager.
- On Linux, the credential manager is file-backed at
  `$XDG_CONFIG_HOME/cursor/auth.json`, falling back to
  `$HOME/.config/cursor/auth.json`.
- The file schema is:

  ```json
  {
    "accessToken": "...",
    "refreshToken": "...",
    "apiKey": "..."
  }
  ```

  Browser-login auth writes `accessToken` and `refreshToken`; API-key exchange
  writes those plus `apiKey`.
- File permissions are `0600`; the containing directory is created with
  restricted permissions by the CLI.
- `cursor-agent status --format json` reports authenticated when this file has
  both access and refresh tokens.
- Copying the auth file into a second isolated `$XDG_CONFIG_HOME/cursor/`
  allowed `cursor-agent status --format json` to report authenticated there as
  well. This proves Linux file-level credential rehydration is mechanically
  possible.
- `cursor-agent logout` removes `auth.json` and leaves non-secret CLI config.
- On macOS the same credential manager uses keychain services instead of the
  file-backed auth store, so portable capture should run in a Linux helper
  environment rather than asking users to upload desktop state.

Important caveat: the test did not complete a real Cursor account browser
login, so it does not yet prove token refresh, billing behavior, model access,
or long-lived reuse. Those still require a paid Cursor account and billing
dashboard verification.

Credential behavior should match existing coding auths:

- personal auths resolve before organization auths
- organization fallbacks preserve priority order
- auth failures mark the credential invalid or needing reauth
- rate-limit failures populate the temporary rate-limit metadata used by
  `coding_credentials`
- settings UI shows Cursor rows with label, status, priority, last used, and
  masked usage note

## Adapter Design

Create `internal/services/agent/adapters/cursor_cli.go`.

The adapter should mirror Codex more than Claude:

- `Name() models.AgentType` returns `models.AgentTypeCursorCLI`
- `PreparePrompt(...)` uses the shared system/user prompt builders
- `ResumeMode()` should be `ResumeBySessionID` only after the stream parser
  reliably captures Cursor's explicit thread/session ID
- `RuntimeProfile()` starts as no-TTY, open-stdin false, split-output true
- `Execute(...)` writes prompt files under `$HOME`, invokes `cursor-agent`, parses
  stream JSON, collects diff, finalizes token usage, and returns a normal
  `agent.AgentResult`

Initial command shape:

```bash
cursor-agent -p --force --output-format stream-json < "$HOME/.143-prompt.md"
```

Continuation command shape, after resume spike:

```bash
cursor-agent --resume "$CURSOR_THREAD_ID" -p --force --output-format stream-json < "$HOME/.143-followup-prompt.md"
```

If Cursor's documented resume command differs for headless mode, implement that
exact syntax and pin it with parser/command-construction tests.

## Stream Parsing

Add `parseCursorStreamLine(...)` with captured fixtures from real Cursor runs.
Minimum parsed fields:

- assistant/user-visible text for session logs
- tool start/end events if present
- file write events if present
- result/success/error events
- session/thread ID
- token usage and provider-native cost, if Cursor reports it
- structured auth/rate-limit failure signals

Do not assume Cursor's JSON schema is identical to Codex or Claude. Add a small
Cursor-specific event model and map it into the shared `AgentResult`,
`LogEntry`, `TokenUsage`, and human-input event types.

If Cursor does not emit token usage, record usage with provenance `unknown` and
let billing surfaces show execution counts without synthetic token cost.

## Plan Mode

143 has two different needs that are easy to conflate:

1. **Plan-before-code inside a normal run.** The agent can plan in its own
   reasoning before making edits. This does not need a new platform state.
2. **Plan-only mode visible to the user.** Cursor should produce a plan and
   stop before edits, so the user can approve or revise the plan.

For VIR-99, implement plan-only support as an agent capability:

```go
type AgentCapabilities struct {
    SupportsPlanMode           bool
    SupportsAskMode            bool
    SupportsNativeReview       bool
    SupportsPromptBasedReview  bool
    SupportsSubscriptionPricing bool
}
```

Cursor's plan command should run:

```bash
cursor-agent -p --mode=plan --output-format stream-json < "$HOME/.143-prompt.md"
```

Only use `--mode=plan` after the spike confirms it is supported by the
installed Cursor CLI version in headless mode. Do not include `--force` in
plan-only mode. Plan output should be saved as a session message and, if
productized, a durable plan artifact. The next approved implementation turn
should run a normal edit-capable Cursor command with the approved plan included
in the prompt.

This maps cleanly onto existing human-input requests: when a session is created
in plan mode, finish the plan turn as `awaiting_input` with choices such as
`approve plan`, `revise`, and `cancel`.

## Review Command

Cursor should participate in the review-loop design, but split review into two
capabilities:

- **Prompt-based review:** headless Cursor reviews the current diff because the
  prompt asks it to. Cursor's headless docs already show this style of
  automated review. This is enough for a basic pre-PR review loop.
- **Native review command:** Cursor runs a first-class `/review` or equivalent
  CLI review command. This is what VIR-99 asks us to investigate, but it needs
  a spike before code lands.

Spike questions:

- Does `cursor-agent -p "/review"` work in headless mode?
- Does review require an interactive TTY command such as `Ctrl+R`?
- Does Cursor expose a separate review command or flag in the current CLI?
- Does the review command emit structured findings, free-form text, or only a
  TUI diff view?
- Can review run without modifying files, then a second fix pass apply changes?

If headless `/review` works, use it for native automation review loops:

```bash
cursor-agent -p --output-format stream-json "/review the current branch diff"
```

If native review is interactive-only, Cursor can still report
`SupportsPromptBasedReview=true` for the automation review loop, but should
report `SupportsNativeReview=false` until live TTY review is wired through
`future/70-live-agent-command-handles.md`.

The review loop should not normalize Cursor findings into platform severities.
Store Cursor's summary and logs like the existing native-review design.

## Cancellation

Initial behavior:

- Register Cursor runs through `runInteractiveCommand(...)`.
- Use `DefaultCancellationSpec` for a graceful stop.
- Let the provider deliver SIGINT / Ctrl+C through the interactive handle.
- Escalate to `Kill` after the existing grace window.
- Mark session/thread status `cancelled` only through the existing cancellation
  service, not from adapter-local shell errors.

Validation:

- Cancel during normal generation.
- Cancel while Cursor is running a terminal command.
- Cancel during a hung headless process.
- Confirm no orphaned `cursor-agent` child remains in the sandbox.
- Confirm a cancelled run still leaves a collectable workspace diff and logs.

If Cursor requires TTY control for reliable cancellation, set
`RequiresTTY=true` and `RequiresOpenStdin=true` in its runtime profile.

## Human Input And Approvals

V1 should use `--force` for implementation turns inside the gVisor sandbox so
file edits do not require approval.

For terminal commands, web fetches, MCP auth, and any Cursor-native approval
events:

- parse structured approval events if Cursor emits them
- map them to `session_human_input_requests`
- checkpoint the thread before pausing
- resume the same Cursor thread ID after the user answers

If Cursor only exposes approval prompts as interactive text, keep approvals
disabled/bypassed for v1 and document the limitation in `AgentCapabilities`.

## Installation And Sandbox Image

Add Cursor CLI to the sandbox image using Cursor's official installer or pinned
download once verified:

```bash
curl https://cursor.com/install -fsS | bash
```

Production requirements:

- pin the installed version or checksum in the Dockerfile/build script
- add a worker startup preflight: `cursor-agent --version`
- optionally run `cursor-agent status` only when a credential is present
- include Cursor in sandbox health diagnostics
- add a feature flag such as `ENABLE_CURSOR_CLI_AGENT`

The first rollout should be opt-in until cancellation, resume, and review
behavior are verified against the production sandbox runtime.

## UI And API Changes

Backend:

- Add `cursor_cli` to agent validation and request/response typed strings.
- Add Cursor to coding credential provider metadata.
- Add Cursor to effective credential resolution and auth checks.
- Add Cursor capability metadata to the agent registry.
- Include Cursor in usage rollups by agent/model/reasoning.

Frontend:

- Add Cursor to session composer agent selection.
- Add Cursor to onboarding/default-agent selector.
- Add Cursor to organization and personal coding-auth settings.
- Add Cursor auth creation with API-key input and masked key suffix display.
- Show plan/review availability from server capability metadata rather than
  hardcoding it in the client.

The UI should not expose Cursor Cloud Agent handoff in the first version. That
is a separate execution backend with different security and state ownership.

## Observability

Log and persist:

- `agent_type=cursor_cli`
- Cursor CLI version
- command mode: `implement`, `plan`, `ask`, `review`
- output format
- auth provider/credential ID, never raw key material
- resume thread ID presence, not the full local auth store
- cancellation method and outcome
- parsed token usage or `usage_provenance=unknown`
- structured failure subtype: auth, rate limit, context, command failure,
  parse failure, cancellation, timeout

Rate-limit and auth errors should feed the same credential health paths as the
other coding agents.

## Rollout Plan

### Phase 0: Spike

Run Cursor CLI inside the same sandbox image class we use for agents. Do not
start implementation until this phase answers pricing, auth portability, stream
shape, and runtime-control questions.

Required artifacts:

- billing proof for Cursor User API Key vs browser-login subscription auth
- normal headless edit run
- `--mode=plan`
- resume by explicit thread ID
- cancellation during generation and during a terminal command
- review command behavior
- prompt-based review behavior
- sandbox policy fixture for `--force`
- auth failure
- rate limit or quota failure
- token usage output

Commit stream fixtures under adapter tests. Store billing and auth-portability
findings in this design doc or a linked spike note before opening the adapter
PR.

### Phase 1: Credential And Metadata

Add model/provider enums, config parsing, masking, validation, settings API
support, and frontend metadata. Choose `cursor_api_key`,
`cursor_subscription`, or both based on Phase 0. Tests should cover provider
parsing, coding credential listing, auth creation, resolution order, compatible
provider-set selection, rate-limit fallback, and invalid config.

### Phase 2: Headless Adapter

Implement `CursorCLIAdapter` with prompt construction, command building,
stream parsing, diff collection, token usage, failure classification, and
resume-ID capture. Add table-driven tests for first turn, continuation, bad
stream lines, auth errors, and shell escaping.

### Phase 3: Runtime Controls

Wire Cursor through the existing live command handle path. Add tests for
cancel registration, graceful interrupt, forced kill fallback, and cleanup.

### Phase 4: Plan And Review

Add capability-aware plan mode. Add review-loop support only if the spike proves
a reliable headless review command. Otherwise leave review disabled with an
explicit capability reason.

### Phase 5: Product Rollout

Gate by feature flag, enable for internal orgs, run real sessions, compare
quality and failure rates against Codex/Claude Code, then expose as an
available agent in onboarding and settings.

## Test Plan

Backend:

- `internal/models` enum and provider validation tests
- credential config parse/mask/summary tests
- credential store/list/resolution tests with `org_id` filtering
- adapter command construction tests for normal, plan, review, and resume
- stream parser fixture tests
- auth/rate-limit failure classification tests
- cancellation tests through `runInteractiveCommand`
- orchestrator tests that select Cursor and propagate credential env
- review-loop capability tests

Frontend:

- agent selector renders Cursor and persists `cursor_cli`
- settings add-auth flow creates the chosen Cursor launch auth type
- personal/org auth lists show Cursor status and fallback order
- plan/review controls respect server capabilities

Verification commands after implementation:

```bash
go vet ./...
go build ./...
go test ./...
cd frontend && npm run typecheck && npm run lint && npm run build
```

## Open Questions

- What exact JSON schema does current `cursor-agent --output-format stream-json`
  emit for edits, tools, errors, session IDs, and usage?
- Is `/review` supported in headless mode, or only the interactive change-review
  UI?
- Does `--mode=plan` in headless mode always stop before edits, or can tools
  still write without `--force`?
- Does Cursor support deterministic `--resume <thread_id>` in headless mode
  when multiple Cursor sessions exist in the same sandbox home directory?
- Can Cursor browser-login auth be represented server-side safely, or should
  143 only support API keys?
- Which Cursor models should 143 expose, and does model selection require paid
  plan state that the CLI reports at runtime?

## Decision

Implement Cursor CLI as `cursor_cli` using headless `cursor-agent -p --force
--output-format stream-json` for the first execution path. Do not decide the
launch credential until Phase 0 proves pricing behavior:

- if Cursor User API Keys consume included Cursor subscription/API-agent usage,
  launch with `cursor_api_key`
- if they do not, make `cursor_subscription` the launch credential and solve
  browser-login credential capture or host-managed auth before product rollout

Treat plan and review as declared capabilities backed by real Cursor command
behavior, not hardcoded assumptions. Use the existing credential stack, live
command handle path, cancellation registry, review-loop design, and session UI
so Cursor becomes a normal coding agent rather than a parallel runtime.

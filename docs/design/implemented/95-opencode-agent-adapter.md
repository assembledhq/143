# Design: OpenCode First-Class Agent Adapter

> **Status:** Implemented | **Last reviewed:** 2026-06-12

## Implementation Status

Implemented as a first-class `opencode` coding agent with explicit OpenCode-scoped API-key credentials, sandbox installation via `opencode-ai`, non-interactive `opencode run --format json` execution, explicit `--session` resume, curated cost-first model metadata, and frontend settings/session picker surfaces.

OpenCode credentials are stored under `ProviderOpenCode` with an optional `backing_provider`. Runtime env injection maps that explicit OpenCode row to the provider-specific env var OpenCode expects; Codex, Claude Code, OpenCode, and OpenRouter rows are not reused implicitly. Direct backing providers are validated against the selected model prefix (`openai/*`, `anthropic/*`, `google/*`, or `opencode/*`); OpenRouter remains the flexible multi-provider escape hatch.

## Context

OpenCode is a high-adoption open-source terminal coding agent and is now the fifth first-class 143 coding agent alongside Codex, Claude Code, Amp, and Pi. Public signals as of June 2026 put it ahead of other open-source CLI candidates: roughly 170k+ GitHub stars and more than 1M weekly npm downloads for `opencode-ai`.

OpenCode fits 143 better than Antigravity for this role because it is explicitly API-key/provider driven, supports many model providers, and documents a non-interactive CLI execution path. It should be added as its own `opencode` agent type rather than hidden under Codex/Claude/Gemini provider settings.

## OpenCode Capabilities Relevant To 143

Documented surfaces:

- Install:
  - `npm install -g opencode-ai`
  - install script: `curl -fsSL https://opencode.ai/install | bash`
  - Homebrew and binary releases are also available.
- Non-interactive CLI:
  - `opencode run [message..]`
  - `--format json` emits raw JSON events.
  - `--continue`, `--session <id>`, and `--fork` support session continuation.
  - `--model provider/model`, `--agent <name>`, `--file`, `--title`, `--dir`, `--variant`, `--thinking`, and `--dangerously-skip-permissions` are available on `run`.
- Auth:
  - `opencode auth login` writes credentials to `~/.local/share/opencode/auth.json`.
  - OpenCode also loads keys from environment variables and `.env` files.
  - Provider base URLs and custom provider options are configurable in `opencode.json`.
- Config:
  - `opencode.json` or `opencode.jsonc` supports `$schema`, `model`, `provider`, `agent`, `permission`, `command`, `share`, and server settings.
  - `OPENCODE_CONFIG`, `OPENCODE_CONFIG_DIR`, and `OPENCODE_CONFIG_CONTENT` can override config locations/content.
  - `OPENCODE_PERMISSION` can inline permission configuration.
- Permissions:
  - Permission outcomes are `allow`, `ask`, or `deny`.
  - Permissions can be global (`"permission": "allow"`) or per tool (`bash`, `edit`, `read`, etc.).
  - `--dangerously-skip-permissions` auto-approves permissions that are not explicitly denied.
- Sessions and server:
  - `opencode serve` starts a headless HTTP server with OpenAPI docs.
  - Server APIs cover session creation, messages, async prompts, permission responses, diffs, events, commands, files, tools, agents, auth, and config.
  - The CLI `run` path can attach to a running server, but v1 should avoid a long-lived server in 143 sandboxes unless cold-start costs demand it.
- Models:
  - OpenCode uses AI SDK and Models.dev to support 75+ providers and local models.
  - Recommended models include GPT, Claude, Minimax, and Gemini families, but model availability is provider/config dependent.

## Integration Strategy

Add OpenCode as a first-class coding agent with a conservative CLI-run adapter first. Do not start with the server API.

The server API is powerful, but it introduces a second process lifecycle, port management, auth for the local server, SSE parsing, and cleanup semantics. The current adapter pattern already supports one process per agent turn through `runInteractiveCommand`, and `opencode run --format json` maps cleanly onto that shape.

The long-term design can switch to `opencode serve` if we need lower per-turn cold-start latency, richer permission handling, or structured diff/session APIs that are more stable than CLI JSON events.

## Interfaces To Implement

### Sandbox Image

Files:

- `sandbox/versions.json`
- `sandbox/Dockerfile`
- `sandbox/install-agents.sh`

Changes:

- Add `opencode_cli` version, initially pinned to the npm package version, for example `1.17.3`.
- Install `opencode-ai@${OPENCODE_CLI_VERSION}` alongside the other npm-based agent CLIs.
- Verify with `opencode --version`.
- Disable auto-update at runtime with `OPENCODE_DISABLE_AUTOUPDATE=true`.

### Backend Models

Files:

- `internal/models/org_settings.go`
- `internal/models/agent_model_constants.go`
- `internal/models/agent_model_constants_test.go`
- `internal/models/agent_slash_commands.go`
- enum/check-constraint migrations that contain agent types

Changes:

- Add `AgentTypeOpenCode AgentType = "opencode"`.
- Include OpenCode in `AgentType.Validate()`.
- Add OpenCode model constants as provider/model strings because OpenCode's model format is native `provider/model`.
- Treat OpenCode as a cost-tiered agent, not just another flagship-model surface. The picker should make cheaper models obvious because OpenCode's biggest value for 143 is easy access to lower-cost provider/model combinations.
- Suggested curated defaults, with exact provider/model IDs verified against OpenCode Zen's model catalog before implementation:
  - **Default inexpensive models**
    - `openai/gpt-5.4-mini`
    - `openai/gpt-5.3-codex-spark`
    - `anthropic/claude-haiku-4-5`
    - `opencode/gemini-3.5-flash`
    - `google/gemini-3-flash`
    - `opencode/minimax-m2.7`
    - `opencode/minimax-m2.5`
    - `opencode/qwen3.7-plus`
    - `opencode/qwen3.6-plus`
    - `opencode/deepseek-v4-flash`
    - `opencode/deepseek-v4-pro`
    - `opencode/glm-5.2`
    - `opencode/glm-5.1`
    - `opencode/kimi-k2.5`
  - **Balanced models**
    - `openai/gpt-5.4`
    - `anthropic/claude-sonnet-4-6`
    - `opencode/gemini-3.1-pro`
    - `opencode/qwen3.7-max`
    - `opencode/kimi-k2.6`
  - **Premium models**
    - `opencode/gpt-5.2`
    - `opencode/gpt-5.5`
    - `opencode/gpt-5.5-pro`
    - `anthropic/claude-opus-4-8`
    - `anthropic/claude-opus-4-7`
    - `opencode/claude-fable-5`
- Avoid baking a large static catalog into v1. Start with the curated cost-tiered list above plus a custom `provider/model` override, then refresh the curated list as part of normal model maintenance.
- Model maintenance note (2026-06-24): OpenCode Zen lists `gpt-5.2-codex`, `gpt-5.1-codex`, `gpt-5.1-codex-max`, `gpt-5.1-codex-mini`, `gpt-5-codex`, `claude-sonnet-4`, `glm-5`, `minimax-m2.1`, `glm-4.7`, `glm-4.6`, `gemini-3-pro`, `kimi-k2-thinking`, `kimi-k2`, `claude-3-5-haiku`, and `qwen3-coder-480b` as deprecated. Keep those out of the curated picker unless there is a deliberate compatibility reason to re-add them.
- Add `OPENCODE_MODEL` as the model env var returned by `ModelEnvVarForAgentType`.
- Add OpenCode to `AgentTypeForModel` and PM model validation.
- Add a slash-command catalog for OpenCode's common commands only after probing exact names. For v1, rely on pass-through user-entered slash tokens.
- Add project command discovery for `.opencode/commands/*.md`.

### Credential Model

Files:

- `internal/models/provider.go` or equivalent provider constants
- `internal/db/org_credentials.go`
- `internal/api/handlers/coding_credentials.go`
- `internal/services/agent/env.go`
- frontend coding auth metadata

Decision:

OpenCode should use explicit OpenCode-scoped credential rows, even when the underlying provider is Anthropic, OpenAI, Gemini, or OpenRouter. Do not silently reuse the user's existing Claude Code, Codex, OpenCode, or platform provider credentials for OpenCode runs.

Why:

- Users should be able to tell at a glance which keys can be spent by OpenCode. Reusing a Claude Code or Codex key implicitly makes cost, rate limits, and security boundaries harder to reason about.
- OpenCode may send different prompts, tool traces, provider options, and model IDs than the native provider CLIs. Treating those credentials as a separate auth scope is clearer and auditable.
- OpenCode's main strength is provider flexibility, but that flexibility should be represented as explicit OpenCode auth rows such as `OpenCode via Anthropic` or `OpenCode via OpenRouter`, not as hidden borrowing from sibling-agent stacks.

Credential resolution options:

1. **OpenCode-native auth row**
   - Agent: `opencode`
   - Provider: `opencode`
   - Auth type: `api_key`
   - Inject as `OPENCODE_API_KEY` through generated `OPENCODE_CONFIG_CONTENT` with the `opencode` provider id and `opencode/*` model ids. `scripts/probe-opencode-native-auth.sh` is the repeatable credentialed probe for validating this path against OpenCode Zen/Go.

2. **Provider-backed OpenCode auth**
   - Store separate OpenCode-scoped credential rows whose backing provider is `anthropic`, `openai`, `gemini`, or `openrouter`.
   - Examples: `opencode_anthropic`, `opencode_openai`, `opencode_openrouter`, or a normalized `ProviderOpenCode` row with `backing_provider` in config.
   - Generate an OpenCode config that references only the selected OpenCode-scoped credential through environment variables.
   - Avoid writing raw secrets into committed workspace files; generate config under `$HOME` or pass `OPENCODE_CONFIG_CONTENT`.

Initial recommendation:

- v1 should support OpenCode-native API key and one or two explicit OpenCode-scoped provider-backed rows, probably Anthropic and OpenAI first.
- Defer local models, Bedrock, Vertex, and arbitrary custom providers until the config generator is proven.

### Agent Env Resolution

File:

- `internal/services/agent/env.go`

Changes:

- Add a case for `models.AgentTypeOpenCode`.
- Resolve the selected OpenCode auth stack row. This row must belong to `agent = opencode`; sibling-agent credentials are not candidates.
- For known OpenCode model prefixes, select the runnable OpenCode credential whose `backing_provider` matches the effective model before priority fallback:
  - `openai/*` -> OpenCode via OpenAI
  - `anthropic/*` -> OpenCode via Anthropic
  - `google/*` / `gemini/*` -> OpenCode via Gemini
  - `openrouter/*` -> OpenCode via OpenRouter
  - `opencode/*` -> OpenCode native
- Inject:
  - `OPENCODE_DISABLE_AUTOUPDATE=true`
  - `OPENCODE_DISABLE_DEFAULT_PLUGINS=true` unless we intentionally allow upstream plugins.
  - `OPENCODE_DISABLE_MODELS_FETCH=true` if we want reproducible startup and a curated model list.
  - `OPENCODE_CONFIG_CONTENT=<json>` or write a generated config file and set `OPENCODE_CONFIG=<path>`.
  - Provider API keys only for the selected provider/model.
- Clear OpenCode env in `clearAgentCredentialEnv`.
- Map OpenCode credential shedding to the selected OpenCode credential row, not to a shared provider row used by Codex or Claude Code.

Runtime credential binding records `(agent_type, credential_id, effective_model, backing_provider)` for OpenCode picks so shedding, fallback, and usage attribution target the explicit OpenCode credential row rather than a sibling provider row.

### Adapter

Files:

- `internal/services/agent/adapters/opencode.go`
- `internal/services/agent/adapters/opencode_test.go`
- `internal/services/agent/adapters/registry.go`

Adapter methods:

- `Name() models.AgentType`
- `ResumeMode() agent.SessionResumeMode`
- `PreparePrompt(...)`
- `RuntimeProfile() agent.AgentRuntimeProfile`
- `Execute(...)`

Command shape:

```bash
opencode run \
  --format json \
  --dangerously-skip-permissions \
  --agent build \
  --model '<provider/model>' \
  --dir '<workspace>' \
  - < '<prompt-file>'
```

If `opencode run` does not accept stdin for message content, use:

```bash
opencode run --format json --dangerously-skip-permissions --agent build --model '<provider/model>' --dir '<workspace>' "$(cat '<prompt-file>')"
```

Continuation:

```bash
opencode run --format json --dangerously-skip-permissions --session '<session-id>' --dir '<workspace>' "$(cat '<followup-file>')"
```

If `--session` does not compose reliably with `run`, fall back to embedding prior conversation context in the prompt, as the Gemini adapter does when no session ID is captured.

Runtime profile:

- Use `DefaultCancellationSpec`.
- Prefer split stdout/stderr parsing.
- Start with no TTY requirement.

Diff:

- Use existing `collectDiff`.
- Do not rely on OpenCode server `/session/:id/diff` in v1.

### Stream Parsing

OpenCode `run --format json` emits raw JSON events. The adapter should parse line-delimited JSON and map it to 143 logs:

- session start/session metadata -> capture `AgentSessionID`.
- assistant text -> `LogEntry{Level: "output"}` and `AgentResult.Summary` fallback.
- tool call/part start -> `LogEntry{Level: "tool_use"}`.
- tool result/part finish -> `LogEntry{Level: "output", Metadata: {"type":"tool_result"}}`.
- error -> `LogEntry{Level: "error"}` and `AgentResult.Error` if terminal.
- usage/cost/stats -> `AgentResult.TokenUsage` where available.
- permission prompt events -> if emitted despite permissions, either fail fast in v1 or normalize to `HumanInputRequest` in v2.

Implementation note:

OpenCode's docs say JSON format exists but do not fully specify the event schema. The adapter therefore parses a conservative set of event aliases and is covered by JSONL fixtures under `internal/services/agent/adapters/testdata/opencode/`, including simple answer, file edit, shell command, failing command, auth failure, rate limit, permission request, continuation, usage, and session-id shapes. `scripts/probe-opencode-fixtures.sh` provides the repeatable credentialed probe path for refreshing or expanding those fixtures from real `opencode run --format json` executions.

### Human Input

Existing integrations:

- Claude Code uses hooks to defer `AskUserQuestion`.
- Codex parses human input JSON events.

OpenCode docs expose permission response APIs in server mode, but the CLI `run --format json` docs do not specify a headless prompt/answer loop.

v1:

- Run with `--dangerously-skip-permissions` and generated `"permission": "allow"` to avoid permission prompts.
- Treat unexpected permission requests as adapter errors with actionable copy.

v2:

- If we adopt `opencode serve`, map permission events to 143 human-input requests and answer them via `POST /session/:id/permissions/:permissionID`.

### Usage Accounting

Files:

- `internal/services/agent/token_usage_cost.go`
- `internal/services/agent/token_usage_cost_test.go`
- usage rollup labels

Changes:

- Add OpenCode label/cost handling.
- If OpenCode JSON events include usage/cost, mark `TokenUsage.Reported=true`.
- If no usage is reported, persist explicit unavailable native-cost metadata rather than inventing token counts. If usage tokens are reported but cost is missing, derive provider/model cost where 143 has known rate data.
- If OpenCode uses multiple providers, preserve provider/model in usage rows so execution analytics remain meaningful.

### Frontend

Files:

- `frontend/src/lib/types.ts`
- `frontend/src/lib/agents.ts`
- `frontend/src/lib/coding-auth-metadata.ts`
- `frontend/src/lib/model-constants.ts`
- `frontend/src/components/agent-badge.tsx`
- settings/account/org agent pages
- usage filters
- setup checklist
- tests for all of the above

Changes:

- Add `opencode` to agent union types.
- Add `OpenCode` metadata, badge, model list, and API-key setup.
- Use explicit provider-backed auth wording in add-auth flows: "OpenCode native" and "OpenCode via Anthropic/OpenAI/Gemini/OpenRouter".
- Add a curated cost-first model picker plus `OPENCODE_MODEL_CUSTOM` for arbitrary provider/model overrides.
- Add OpenCode to usage filters and session detail labels.

### Session, Automation, PM, And Evals

Files:

- session creation/validation handlers
- automation validation
- PM model routing
- eval model allowlists
- migrations/check constraints

Changes:

- Allow `agent_type = 'opencode'`.
- Map selected OpenCode models to `AgentTypeOpenCode`.
- Ensure automation auth validation understands multi-provider OpenCode auth.
- Add OpenCode to eval allowlists with a small cost-first model set after the headless adapter and parser fixture coverage are in place.

### Tests

Required test areas:

- model constants validation for OpenCode provider/model strings.
- `AgentTypeOpenCode.Validate`.
- adapter command construction: fresh, continuation, model, config, permissions.
- stream parser fixtures for assistant output, tool use, tool result, session ID, errors, usage.
- env resolution for OpenCode-native and provider-backed credentials.
- credential shedding for multi-provider model selection.
- sandbox install version tests if present.
- frontend tests for agent metadata, model groups, auth setup, setup checklist, usage filter, and session label.
- tenancy lints unaffected, but any new store methods must include org ID.

## Rollout Plan

1. **Probe**
   - Build OpenCode into a local sandbox image.
   - Run `scripts/probe-opencode-fixtures.sh` with an OpenCode-compatible provider key available in the environment.
   - Refresh or add JSONL fixtures for a simple answer, file edit, shell command, failing command, rate limit/auth failure, and continuation.

2. **Minimal backend adapter**
   - Implement `AgentTypeOpenCode`, model constants, sandbox install, env resolution for one OpenCode-native or Anthropic-backed credential, and `OpenCodeAdapter`.
   - Keep frontend hidden behind a feature flag or internal setting.

3. **Full product surface**
   - Add settings/auth UI, session picker, usage filters, setup checklist, slash command discovery, and automation validation.

4. **Credential expansion**
   - Support OpenAI, Anthropic, OpenRouter, and OpenCode-native keys.
   - Add custom model entry with validation for `provider/model`.

5. **Server API evaluation**
   - Evaluate `opencode serve` only if CLI-run JSON lacks stable events, if permission/human-input handling matters, or if startup latency is too high.

## Areas Of Ambiguity

OpenCode has several areas that differ from the existing Codex/Claude/Gemini adapters:

- **Multi-provider auth**: existing agent credential selection assumes one agent maps to one provider. OpenCode can route to many providers depending on model.
- **Event schema stability**: docs expose `--format json`, but not a complete line-event contract equivalent to Claude's `stream-json` docs or Codex's observed events.
- **Continuation semantics**: docs list `--session`, but we need to verify `opencode run --session <id> <message>` deterministically resumes the right turn in an ephemeral sandbox.
- **Permission prompts**: `--dangerously-skip-permissions` may avoid prompts, but server APIs imply permission requests exist. We need to know whether CLI run can emit permission requests in a machine-answerable way.
- **BYO provider config shape**: OpenCode supports many providers through Models.dev/AI SDK. We should not expose arbitrary providers until we can validate provider-specific env/config keys.
- **OpenCode-native subscription/key**: OpenCode Zen/Go is API-key based and uses `opencode/*` model ids. The native path is implemented with generated config and `OPENCODE_API_KEY`; refresh with `scripts/probe-opencode-native-auth.sh` when OpenCode changes provider schema.
- **Server lifecycle**: OpenCode's headless server is powerful, but adopting it would require port allocation, basic auth, SSE clients, cleanup, and possibly a different cancellation/checkpoint model.
- **Cost accounting**: OpenCode may report usage differently per backing provider. Token usage needs provider/model provenance, not just agent type.
- **Plugin/default behavior**: OpenCode loads config, plugins, Claude Code skills, and models unless disabled. 143 should start with `--pure`/disable env vars where possible to keep sandbox behavior reproducible.
- **Local model support**: attractive for self-hosters, but incompatible with 143's managed cloud worker unless the model endpoint is reachable and tenant-scoped.

## References

- OpenCode intro/install: https://opencode.ai/docs/
- OpenCode CLI: https://opencode.ai/docs/cli/
- OpenCode config: https://opencode.ai/docs/config/
- OpenCode providers: https://opencode.ai/docs/providers/
- OpenCode permissions: https://opencode.ai/docs/permissions/
- OpenCode agents: https://opencode.ai/docs/agents/
- OpenCode server API: https://opencode.ai/docs/server/
- OpenCode models: https://opencode.ai/docs/models/

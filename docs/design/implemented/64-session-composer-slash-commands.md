# Design: Session Composer Slash Commands

> **Status:** Implemented | **Last reviewed:** 2026-04-25
>
> **Depends on:** [../overall.md](../overall.md), [../03-frontend.md](../03-frontend.md), [53-session-composer-mentions.md](53-session-composer-mentions.md)

## Problem

The session composers — both `/sessions/new` and the follow-up editor on `/sessions/[id]` — let the user type free-form prompts and `@` references, but they do not surface the slash commands that the underlying coding agent (Claude Code, Codex, OpenCode, Amp, Pi) already understands.

Today a user who knows Claude Code's `/review` or Gemini's `/compress` has to remember the command, type it perfectly, and hope the agent recognizes it. The UI does not advertise what is available, does not validate against the selected agent, and does not adapt when the user switches agents on the same form.

This is the same product gap that `@` mentions filled for repository context — we already taught the composer to "see" structured tokens and route them through an adapter; we now need the same treatment for slash commands so the user gets the discoverability and ergonomics they get in Claude Code, Codex, Conductor, etc., without us reimplementing each agent's command list inside our prompt.

## Core Decision

Slash commands should follow the same pattern that worked for `@` mentions:

1. The user types a visible `/...` token at the start of a message and a picker opens.
2. The picker is **scoped to the currently selected coding agent** — Claude Code's commands when Claude Code is selected, Gemini's when Gemini is selected, and so on.
3. Selecting a command inserts the canonical token into the textarea and records a structured `SessionInputCommand` alongside the message.
4. The adapter for the selected agent decides how to deliver the command to the underlying CLI.

We do **not** invent a unified, vendor-neutral command vocabulary. Each agent's commands are specific to that agent; trying to hide that creates surprises ("why does `/review` mean different things on Claude vs Codex?"). Instead we treat each agent's command list as a first-class catalog and let the user see what their selected agent actually supports.

We **reuse the existing mention picker UI** rather than ship a parallel component. The trigger character, the data source, the result shape, and the keyboard contract are all generalizations of what `session-composer-mentions.ts` already does.

## External Reference Points

### Claude Code

- Built-in commands come from Claude Code's published commands table, using the full-form command names in the picker (`/review`, `/security-review`, `/diff`, `/plan`, `/tasks`, `/resume`, `/status`, `/mcp`, etc.).
- **Custom project commands** discovered from `.claude/commands/*.md` in the repo.
- **Custom user commands** discovered from `~/.claude/commands/*.md`.
- **Plugin commands** namespaced as `/plugin:command`.
- Commands are recognized when they appear at the start of a turn; arguments follow the command name.
- Source: [Claude Code commands](https://code.claude.com/docs/en/commands).

### Codex CLI / Codex app

- Built-in commands currently surfaced in 143 include `/init`, `/diff`, `/review`, `/edit`, `/model`, `/clear`, and `/compact`.
- Codex's app-server protocol distinguishes structured `skill` ($-prefix) and `mention` items from free text — slash commands sit in the `skill` family.
- Source: [Codex app-server README](https://github.com/openai/codex/blob/main/codex-rs/app-server/README.md).

### OpenCode

- Documented `/`-prefixed commands such as `/help`, `/clear`, `/compress`, `/tools`, `/quit`, `/chat`, `/memory`.
- OpenCode processes slash commands locally before sending the residual prompt to the model.
- Source: [OpenCode commands](https://github.com/google-gemini/opencode/blob/main/docs/reference/commands.md).

### Amp

- Slash commands cover sub-agents and modes — e.g. `/agent`, `/mode`, plus MCP prompt invocations.
- Amp also supports MCP prompts as commands surfaced via the same picker.
- Source: [Amp manual](https://ampcode.com/manual).

### Pi

- Pi's documented surface is leaner; slash commands are limited and not a primary interaction mode.
- Source: [pi-agent npm docs](https://www.npmjs.com/package/@mariozechner/pi-agent).

### Conductor

- Conductor's composer also supports slash commands tied to its harness (e.g. `/loop`, `/schedule`).
- Conductor's lesson is the one we already absorbed for `@`: keep the visible token but resolve to a real artifact (file on disk, structured intent) so multiple agents can consume it.

## Implementation Principles

1. **Visible token, structured meaning.**
   The prompt keeps `/review`, but the request payload also carries a canonical `commands[]` entry so the adapter can act deterministically.

2. **Per-agent catalog.**
   The picker only shows commands that the currently selected agent actually supports. Switching agent mid-compose re-filters the list.

3. **Reuse the mention picker UI.**
   We extend the existing mention popover and parser to support a second trigger character. We do not ship a second floating-popover component.

4. **Adapters own delivery.**
   Whether a command is passed through as `/foo` text, intercepted by our orchestrator (e.g. `/clear`), or translated into a Codex `skill` item is an adapter-layer concern — not a composer concern.

5. **Static catalog first, discovery later.**
   v1 ships a hand-maintained catalog of well-known commands per agent. Project-level discovery (e.g. reading `.claude/commands/*.md` from the repo) is deferred.

6. **No backwards-compat shims.**
   We extend the existing `references[]` payload model with a sibling `commands[]` field rather than overloading mention references to also carry commands.

## Product Scope For v1

### In scope

- `/` trigger picker in **both** composer surfaces:
  - new-session composer (`/sessions/new`)
  - follow-up composer on the session detail page (`/sessions/[id]`)
- Per-agent filtering of the command list keyed off the user's currently selected agent type.
- Static, code-shipped catalog of well-known commands for each supported agent.
- Persistence of structured command selections on the message payload.
- Adapter pass-through: commands are emitted as `/foo` text in the prompt by default; adapters may override.
- Reuse of the existing mention popover component and trigger-parser utilities.

### Out of scope for v1

- Project-local custom command discovery (`.claude/commands/*.md`, `.codex/commands/*.md`, etc.) — addressed in Phase 3 via repo-tree fetch.
- User-global commands (`~/.claude/commands/`), MCP prompts, and plugin commands — explicitly **not in this design at all**, and not slated for a future phase here. Revisit only if real user demand surfaces.
- Argument-aware autocomplete (showing a second-stage picker for `/model <model>` choices).
- Client-side command interception for commands that mutate session state (`/clear`, `/compact`, `/model` — these still pass through to the agent in v1).
- Slash commands appearing mid-message — v1 only triggers when `/` is the first non-whitespace character on a line, matching how the supported agents recognize them.
- Plugin namespaces (`/plugin:command`).

## UX Model

### Trigger behavior

- Typing `/` at the **start of the message, or at the start of a new line**, opens the picker anchored to the caret.
- Typing additional characters filters results by name and description prefix/substring.
- Arrow keys navigate; `Enter` inserts the selected command's canonical token; `Escape` closes.
- Backspacing across an inserted command token clears the corresponding entry from `commands[]`.
- The picker does **not** trigger on `/` typed inside a path expression like `dir/foo` — leverage the same caret-context check used for `@` mentions.

### Result group

A single section labeled with the agent's display name, e.g. **"Claude Code commands"** or **"Codex commands"**, so the user always sees which agent they're targeting. If the user has not selected an agent yet (the new-session form supports an empty default in some flows), show the picker scoped to the org's default agent type and label it accordingly.

### Empty / unsupported state

- If the selected agent has no shipped command catalog (e.g. Pi until we add one), the picker shows an inline "no commands available for `<Agent>`" message rather than silently failing — slash characters are then treated as plain text.
- Switching agent mid-compose with a previously inserted command that is **not** valid for the new agent: highlight the command chip in a warning state and prompt the user to either remove it or switch agents back. We do not silently strip it.

### Display model

Mirror the mention pattern:

- The textarea contains plain `/foo` text.
- A companion chip row below the textarea shows resolved command chips (`/review`, `/clear`) with remove affordances. Reuse the chip row already implemented for `@` references.

## Internal Data Model

The composer should additionally maintain `commands[]` next to `references[]`:

```ts
type SessionInputCommand = {
  kind: "command";
  agentType: AgentType;          // claude_code | codex | opencode | amp | pi
  name: string;                  // canonical command without leading slash, e.g. "review"
  token: string;                 // the literal text inserted, e.g. "/review"
  display: string;               // human label for chips, usually same as token
  description?: string;          // shown in the picker
  arguments?: string;            // raw trailing argument text the user typed after the command
};
```

`agentType` is recorded so the backend can validate the command is consistent with the request's `agent_type`.

`arguments` is captured as opaque text in v1; we do not parse it. The user can keep typing after `/review` and we just store everything up to the next newline (or end of message) as the argument string.

## API Model

Extend the manual session create payload **and** the in-session message send payload:

```json
{
  "message": "/review focus on the auth handler",
  "references": [],
  "commands": [
    {
      "kind": "command",
      "agent_type": "claude_code",
      "name": "review",
      "token": "/review",
      "display": "/review",
      "arguments": "focus on the auth handler"
    }
  ]
}
```

The same `commands[]` shape is added to:

- `POST /api/v1/sessions/manual` (new session)
- `POST /api/v1/sessions/{id}/messages` (follow-up)

The API persists `commands[]` alongside the user message, exactly the way `references[]` is persisted today.

### Catalog discovery endpoint

Add a single read-only endpoint:

```
GET /api/v1/session-composer/slash-commands?agent_type=claude_code&q=rev
```

Returns up to ~30 commands ordered by relevance. Filtering happens server-side using the same fuzzy ranking used for file mentions (prefix > basename-style > contains). This keeps the catalog source-of-truth in Go (next to `internal/models/agent_model_constants.go`) so backend and frontend cannot drift.

The endpoint does **not** require a `repository_id` in v1; the catalog is global per agent. When project-level discovery ships (Phase 3), `repository_id` and `branch` become optional inputs.

## Adapter Contract

Extend `agent.AgentInput` (the canonical agent input layer described in [53-session-composer-mentions.md](53-session-composer-mentions.md)) with:

```go
type InputCommand struct {
    AgentType string
    Name      string
    Token     string
    Display   string
    Arguments string
}

// AgentInput
Commands []InputCommand
```

Each adapter decides what to do:

- **Claude adapter:** emit the `/foo` token verbatim at the start of the prompt; arguments follow the token. Claude Code parses these natively at turn boundaries.
- **Codex adapter:** emit `/foo` in the visible prompt and, when we move to the richer Codex protocol surface, also send a structured `skill` item (`{ "type": "skill", "name": "foo" }`) — same pattern we use for `@` mentions vs structured mention items.
- **OpenCode adapter:** emit `/foo` in the prompt; OpenCode handles them locally before model invocation.
- **Amp adapter:** emit `/foo` in the prompt; for MCP-prompt-style commands, the adapter may later upgrade to Amp's structured JSON input blocks.
- **Pi adapter:** v1 has no Pi catalog, so this is a no-op. If we add one later, treat it as text-only.

The composer and API layers do **not** know any of this. They preserve canonical commands; adapters serialize.

### Validating per-agent constraint

The backend validates that every entry in `commands[]` has `agent_type` equal to the request's `agent_type`. A mismatch is a 400 — this catches stale commands left in the composer after the user switched agents.

## Catalog Source of Truth

Define the catalog in Go alongside the existing `agent_model_constants.go`:

```go
// internal/models/agent_slash_commands.go
type SlashCommand struct {
    Name        string
    Description string
    AcceptsArgs bool
}

var ClaudeCodeSlashCommands = []SlashCommand{
    {Name: "init",       Description: "Generate a CLAUDE.md from the repo"},
    {Name: "review",     Description: "Review pending changes",                 AcceptsArgs: true},
    {Name: "clear",      Description: "Clear conversation context"},
    {Name: "compact",    Description: "Compact the conversation"},
    {Name: "model",      Description: "Change the active model",                AcceptsArgs: true},
    // ...
}

var CodexSlashCommands = []SlashCommand{ /* ... */ }
var OpenCodeSlashCommands = []SlashCommand{ /* ... */ }
var AmpSlashCommands = []SlashCommand{ /* ... */ }
// Pi: empty in v1
```

Each catalog entry is sourced from the agent's public docs (linked above) and updated as those agents evolve. The catalog is intentionally hand-maintained for the **built-in** commands of each agent — it is short, changes infrequently, and giving the user a curated list is better than scraping each agent's CLI.

What this catalog does **not** cover, and the next section addresses, is commands that the **user** has authored — `.claude/commands/foo.md` checked into the repo, files in `~/.claude/commands/` on the user's setup, MCP prompts attached to the agent, plugin-provided commands, and so on. We need a discovery story for those because they're the most distinctive, and most valuable, commands a power user has.

## Dynamic Discovery of User-Defined Commands

A static Go catalog cannot represent commands the user wrote themselves. The most common case across the agents we support is project-scoped command files committed to the repo:

- **Claude Code:** `.claude/commands/*.md`
- **Codex:** `.codex/commands/*.md`
- **OpenCode:** `.gemini/commands/*.toml`
- **Amp / Pi:** no widely-adopted project-scope convention to read today.

Repo-scoped discovery covers this case cleanly using infrastructure that already exists for `@` mentions. We deliberately scope discovery to the repo and **do not** reach into the user's global config (`~/.claude/commands/`), MCP prompts, or plugin commands in this design — those would require runtime introspection inside the sandbox, which adds complexity and runtime cost, and the population mechanism for user-global commands inside our sandboxes isn't even well-defined today.

### Approach: repo-tree discovery

For each supported agent, define a known subtree where project-scoped commands live:

```go
// internal/models/agent_slash_commands.go (extension)
type ProjectCommandSpec struct {
    Dir      string
    FileGlob string
}

var ProjectCommandPaths = map[AgentType]ProjectCommandSpec{
    AgentTypeClaudeCode: {Dir: ".claude/commands", FileGlob: "*.md"},
    AgentTypeCodex:      {Dir: ".codex/commands",  FileGlob: "*.md"},
    AgentTypeOpenCode:   {Dir: ".opencode/commands", FileGlob: "*.md"},
    // Amp / Pi: omitted until upstream conventions stabilize
}
```

The catalog endpoint, when called with `repository_id` + `branch`, asks the existing repo-tree service for the tree at that ref and filters entries under the agent's command directory. The endpoint returns the union of built-in and project commands as separately tagged groups:

```json
GET /api/v1/session-composer/slash-commands?agent_type=claude_code&q=rev&repository_id=...&branch=...
→ {
    "groups": [
      { "source": "builtin", "label": "Claude Code commands", "items": [ ... ] },
      { "source": "project", "label": "Project commands",     "items": [ ... ] }
    ]
  }
```

Command names are derived from filenames (strip the extension and the `.claude/commands/` prefix). For richer metadata in the picker (description, accepts-args), the **descriptions are fetched lazily**: the catalog endpoint returns names only, and a separate endpoint fetches a single command file's frontmatter on demand when the user inserts it. This keeps the picker fast on cold cache and avoids paying per-file cost for commands the user never selects.

The result shape is locked in now so the frontend doesn't change if a future redesign adds more groups:

```json
{ "groups": [ { "source": "builtin" | "project", "label": "...", "items": [...] } ] }
```

### What this design explicitly does not do

- **No in-sandbox enumeration.** We do not run `docker exec` against the session's container to read `~/.claude/commands/` or call any agent CLI's `--list-commands` flag. That would push discovery cost onto the runtime path and depends on undocumented CLI behavior.
- **No scraping of agent CLI `--help` output.** Output is unstable and version-dependent.
- **No live HTTP calls to upstream agent vendors** to enumerate commands.
- **No user-global / MCP / plugin discovery in v1.** If real users ask for it, revisit then; the API shape leaves room for additional groups.

## Performance

The single biggest constraint behind this design is that **discovery must not slow down container creation or the agent execution path**. It does not, by construction:

### Container / sandbox start: zero added cost

Discovery never touches the sandbox. The catalog endpoint runs entirely in the API server process and talks only to GitHub. Specifically:

- `internal/services/agent/providers/docker.go` (`Create`, `Exec`, snapshot/restore) is **not modified**. No new `docker exec` call, no extra mount, no extra bootstrap step.
- `internal/services/agent/orchestrator.go`'s `RunAgent` path is **not modified**. The agent input does not gain any field that requires runtime fetching.
- The agent CLI inside the sandbox handles `/foo` commands itself, exactly as it would today if a user typed it manually. Our adapter pass-through layer just preserves the text.

In other words: **the runtime impact on session start, session resume, and per-turn agent execution is exactly zero.** Discovery happens in the API server when the user opens the picker, on a request path that is independent of the orchestrator.

### Picker open: bounded by GitHub Trees API + 30s cache

When the user types `/` and the frontend hits the catalog endpoint:

- **Built-in items** come from the in-memory Go catalog. Microsecond cost.
- **Project items** come from `ListRepositoryTree`, which is the same call already used by `@` mentions and shares the same 30-second in-memory cache (`internal/api/handlers/session_composer.go`, ~line 18).

Two cases:

1. **Warm cache** (the user has already opened the `@` picker recently for this repo+branch, or has used the session composer at all in the last 30 s): the project-command list is served from the cached tree with a single in-memory walk. Total endpoint latency on the order of single-digit milliseconds.
2. **Cold cache**: a single GitHub Trees API call. p50 ~100–300 ms for typical repos, up to ~1 s for very large monorepos. The same call would happen on first `@` use anyway, so for any user who composes with `@` references the slash picker effectively always hits a warm cache.

Filtering by query string (`?q=rev`) happens in-process on the cached tree slice. No additional network calls per keystroke. The frontend should debounce keystrokes the same way it does for file mentions.

### Description fetch: lazy, paid only on insertion

Reading each command file's frontmatter for descriptions requires the GitHub Contents API (one call per file). To keep the picker open snappy:

- The catalog endpoint returns **names only**.
- A second endpoint (e.g. `GET /api/v1/session-composer/slash-commands/:agent_type/:repo/:branch/:name`) fetches a single file's frontmatter and is called only when the user **inserts** that command (or hovers it long enough to trigger a description popover, if we add that later).
- Fetched contents go into a longer-lived cache keyed by `repo + branch + path + sha`, so repeated insertions of the same command reuse a single fetch.

Worst-case cost for a repo defining 50 commands: 50 Contents API calls, but spread across user actions. We never fan out a batch fetch on picker open.

### Memory and storage

- The repo tree cache already exists; project-command listing is just a different filter over the same cached structure.
- No new database tables for discovery itself. Resolved commands persist on the user message via the existing `commands[]` payload (next to `references[]`).

### Where the budget could be exceeded

Three failure modes worth calling out in code review:

1. **Eager content fetching.** If the catalog endpoint ever fetches every command's frontmatter on open, picker latency degrades linearly with command count. Lazy-fetch is non-negotiable.
2. **Per-keystroke API calls.** If the frontend issues a new endpoint call for every character typed in the picker query, we'll spam the cache key. Reuse the file-mention `useDeferredValue` pattern.
3. **Tree cache busting.** If we accidentally add the query string or agent type to the tree cache key, we'll recompute the tree per request. The cache key must remain `repo_id + branch`.

## Backend Plan

### 1. Add the catalog endpoint

`GET /api/v1/session-composer/slash-commands` in `internal/api/handlers/session_composer.go` next to `ListFileMentions`. Implementation reads from the static Go catalog, applies fuzzy ranking, returns the top N.

### 2. Extend message create payloads

Add `commands []SessionInputCommand` to:

- the manual session create handler (`internal/api/handlers/sessions.go`, manual-create path)
- the follow-up message handler (`internal/api/handlers/sessions.go`, `SendMessage`)

Persist them using the same storage pattern as `references[]` — either on the session message row or a sibling table, matching whatever 53 lands on. **Do not** invent a separate persistence path; commands are message metadata.

### 3. Validate per-agent consistency

In the request DTO, reject any command whose `agent_type` differs from the request's selected agent. Return a 400 with a per-command error so the UI can highlight the offending chip.

### 4. Thread commands into orchestrator input

Populate `agent.AgentInput.Commands` from the persisted command list when the session is started or resumed. Existing prompt-build code stays unchanged for adapters that pass commands through as text.

### 5. Per-adapter serialization

Each adapter in `internal/services/agent/` chooses its serialization. Default behavior: prepend `token + " " + arguments` to the user message text in the order the user inserted them.

## Frontend Plan

### 1. Generalize the mention parser

Refactor `frontend/src/lib/session-composer-mentions.ts` so the active-trigger detection accepts a configurable trigger set. The current `findActiveMention(text, caret)` becomes:

```ts
findActiveTrigger(text, caret, triggers: { char: "@" | "/"; startOfLineOnly?: boolean }[])
```

The slash variant sets `startOfLineOnly: true` so we don't fire on `dir/foo` paths.

`insertMentionAtCaret`, `syncReferencesWithMessage`, and `removeMentionReference` get a sibling pair (`insertCommandAtCaret`, `syncCommandsWithMessage`, `removeCommandReference`) — or, preferably, are generalized to operate on a discriminated `Insertable = Reference | Command` union so we don't fork the implementation.

### 2. Generalize the popover component

The mention popover rendered via `createPortal()` in `manual-session-create-page-content.tsx` should be lifted into a dedicated component, e.g. `SessionComposerTriggerPicker`, parameterized by:

- `triggerLabel` ("Files and directories" vs "Claude Code commands")
- `results` (mention or command shape, normalized to `{ id, primary, secondary }`)
- `onSelect`, `onClose`, anchor rect, query string

This same component is then mounted twice with different data sources.

### 3. Add the slash-command query hook

```ts
useSessionComposerSlashCommands(agentType, query)
  // -> GET /api/v1/session-composer/slash-commands?agent_type=...&q=...
```

Mirrors `useSessionComposerFileMentions`. Re-runs when `agentType` changes so switching agent mid-compose updates the picker.

### 4. Wire into both composer surfaces

- `frontend/src/app/(dashboard)/sessions/new/manual-session-create-page-content.tsx` — already hosts the mention picker; add the slash trigger and second picker mount. Submit `commands[]` alongside `references[]` in the create-session mutation.
- `frontend/src/app/(dashboard)/sessions/[id]/session-detail-content.tsx` — does **not** currently host the mention picker. Add the trigger picker (both `@` and `/` in one go) and submit both `references[]` and `commands[]` to `sendMessage`. This pulls the mention work for the follow-up surface forward in the same change since the lift-to-component refactor makes it cheap.

### 5. Sync on agent switch

When the user changes the selected agent, walk `commands[]` and mark any entry whose `agent_type` doesn't match the new selection as invalid. Render those chips in a warning style and block submission until they're resolved.

### 6. Keep submit text faithful

The textarea text is the source of truth for what the agent will see. `commands[]` is metadata that travels alongside it; we never reconstruct the prompt from the structured list. This matches how `references[]` is handled today.

## Sequenced Rollout

### Phase 1: Static catalog, both surfaces

- Catalog endpoint + Go-side static catalogs for Claude Code, Codex, OpenCode, Amp.
- Generalized trigger parser + lifted popover component.
- Slash trigger wired into `/sessions/new` and `/sessions/[id]` composers.
- `commands[]` persisted on user messages.
- Adapters pass commands through as text.

### Phase 2: Agent-switch UX polish + adapter upgrades

- Warning state for commands that don't match the newly selected agent.
- Codex adapter emits structured `skill` items in addition to text where supported.
- Argument-aware autocomplete for `/model <choice>` and similar.

### Phase 3: Project-local custom commands (repo-tree discovery)

- Catalog endpoint accepts `repository_id` + `branch` and lists files under the agent's known project-command directory via the existing `ListRepositoryTree` service. Filtering happens in-process against the cached tree.
- Surface them as a second group (`source: "project"`) in the picker.
- Lazy-fetch each command file's contents via the GitHub Contents API only when the user inserts it, to populate description/argument-hint metadata without paying for content fetches up front.
- Persist the resolved command definition (path + contents hash at insertion time) with the message so a replay can rehydrate it even if the source file changes later.
- Zero impact on container creation or the orchestrator runtime path — discovery is server-side only.

## Testing Plan

### Frontend

- Unit tests for `findActiveTrigger` covering `@`, `/`, and the start-of-line-only constraint for `/`.
- Component tests for picker keyboard navigation across both trigger types.
- Component tests for inserting a command, then switching agent, then seeing the warning chip.
- Component tests for submitting a message with a mix of `@` references and `/` commands.
- Regression tests ensuring `dir/foo` typing does **not** open the slash picker.

Required verification: `npm run typecheck`, `npm run lint`, `npm run build`.

### Backend

- Handler tests for the catalog endpoint per agent type, including filtering by `q`.
- Handler tests rejecting `commands[]` whose `agent_type` doesn't match the request.
- Orchestrator tests proving commands are threaded into `agent.AgentInput.Commands`.
- Per-adapter tests proving default text serialization preserves command order and arguments.

Required verification: `make lint`, `go vet ./...`, `go build ./...`, `go test ./...`.

## Risks

### 1. Catalog drift and missing user-defined commands

The hand-maintained catalog will lag upstream agent releases. Mitigation: treat it like model lists — update when we update model lists, cite the upstream doc in a comment next to each command. Phase 3 (repo-tree discovery) covers the most common user-authored case.

Commands the user defines outside the repo (`~/.claude/commands/`, MCP prompts, plugin commands) are intentionally not represented in this design. The cost of supporting them is an in-sandbox runtime probe with non-trivial impact on container start and per-turn latency, which we do not want to take on without clear demand. If users surface this gap, a follow-up design can revisit.

### 2. False positives on `/`

Users type `/` constantly inside paths, regex, URLs. Anchoring the trigger to start-of-line plus a non-whitespace check after the slash is necessary. Without it the picker becomes a constant nuisance.

### 3. Cross-agent leakage

If we forget the per-agent constraint, a user can switch from Claude Code to Codex with `/review` already inserted and the request will succeed but produce confusing output (Codex's `/review` is not Claude Code's). The 400-on-mismatch validation is the primary guardrail.

### 4. Conflating slash commands with side-effecting client actions

`/clear` and `/model` in the agent CLIs mutate session state. v1 passes them through verbatim and lets the agent handle them in-session. We are explicitly **not** intercepting them client-side in v1 — but we should expect to revisit this as users discover that, e.g., `/model gpt-5.4` typed mid-session does not change the model dropdown in our UI.

### 5. Picker proliferation

If `/`, `@`, and future triggers each ship their own popover code path, we end up with three subtly different UIs. The Phase 1 lift-to-component refactor is non-negotiable for that reason.

## Open Questions

1. Should `/clear`-style commands be intercepted by the orchestrator (resetting our session state) or passed through (letting the agent handle its own context)? Phase 1 picks pass-through; revisit after we see real usage.
2. Should the catalog endpoint be per-org so admins can curate the visible command set, or strictly global? Default to global; revisit if customers ask.
3. When an agent is switched mid-compose and an inserted command becomes invalid, should we auto-translate where a one-to-one mapping exists (e.g. `/clear` → `/clear` across all agents)? v1 says no — auto-translation hides cross-agent semantic drift.
4. Phase 3 description-fetch caching: should fetched command frontmatter live in the same 30 s tree cache, or in a longer-TTL cache keyed by `repo + branch + path + sha` since contents only change when the file does? The latter is probably right but worth confirming during implementation.

## Recommendation

Build slash commands as the second instance of the same pattern that worked for `@` mentions:

**The composer resolves user intent into canonical commands keyed by agent, and the adapter is responsible for translating those commands into the downstream agent's native input format.**

Reuse the picker UI, reuse the trigger-parser utilities, reuse the `references[]`-style persistence and validation pipeline. The interesting work is the per-agent catalog and the agent-switch UX — everything else is a generalization of code we already shipped.

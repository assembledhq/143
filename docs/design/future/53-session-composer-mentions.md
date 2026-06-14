# Design: Session Composer Mentions

> **Status:** Implemented in v1 for file and directory references
>
> **Last reviewed:** 2026-04-21
>
> **Depends on:** [../overall.md](../overall.md), [../03-frontend.md](../03-frontend.md), [45-global-command-palette.md](45-global-command-palette.md)
>
> **Implementation record:** [../implemented/53-session-composer-mentions.md](../implemented/53-session-composer-mentions.md)

## Problem

The new-session composer at `/sessions/new` supports free-form text, uploads, image URLs, repository selection, branch selection, and model selection. It does **not** support inline `@` targeting for repository files, directories, or tool/plugin surfaces.

That leaves two product gaps:

- users cannot quickly point the agent at concrete repo context while typing
- the app has no consistent way to preserve what the user selected in the UI and pass it to the chosen coding agent in that agent's native format

The second gap matters more than the first. `@` autocomplete is easy to fake in the UI. Correctly preserving those references through the backend and into Claude, Codex, Gemini, Amp, and Pi without overfitting to one vendor is the real design problem.

## Core Decision

Do **not** treat `@foo` as text-only.

Do **not** invent a single universal attachment format and push it directly to every coding agent.

Instead:

1. let the user type visible `@...` tokens in the composer
2. resolve each selection into a canonical internal reference
3. persist that structured reference with the message
4. let each agent adapter translate those references into the native style that agent expects

This is the compatibility layer across Claude-style file/resource references, Codex-style structured mentions, Gemini's file injection, Amp's JSON input blocks, and Pi's text-plus-filesystem-tools model.

## External Reference Points

### Claude Code

Claude Code's public docs are the clearest for file-backed context.

- `@file` includes the full file content in the conversation.
- `@directory` includes a directory listing, not full file contents.
- `@` also covers MCP resources, which appear alongside files in autocomplete.
- MCP resources are automatically fetched and included as attachments when referenced.
- Claude Code also adds `CLAUDE.md` files from parent directories when files are referenced.

This means Claude's native mental model is: **reference files/resources in prompt text, then resolve them into attached context before the model works**.

Sources:

- [Claude Code tutorials](https://code.claude.com/docs/en/tutorials)
- [Claude Code MCP docs](https://code.claude.com/docs/en/mcp)

### Codex app

Codex's public app-server protocol is strongest on structured references.

- Skills are invoked with `$<skill-name>` in text plus a structured `skill` item.
- Apps are invoked with `$<app-slug>` in text plus a structured `mention` item using `app://...`.
- Plugins are invoked with a UI mention token such as `@sample` plus a structured `mention` item using `plugin://...`.
- The docs explicitly recommend sending the structured item so the backend uses the exact target instead of guessing from text.

This means Codex's native mental model is: **keep the visible token in text, but pair it with canonical structured metadata**.

Sources:

- [Introducing the Codex app](https://openai.com/index/introducing-the-codex-app/)
- [Codex app-server README](https://github.com/openai/codex/blob/main/codex-rs/app-server/README.md)

### OpenCode

OpenCode documents `@` commands directly.

- `@file` reads the file content.
- `@directory` reads files in that directory and subdirectories.
- Gemini says the CLI uses `read_many_files` internally and inserts the fetched content into the query before sending it to the model.
- Git-aware filtering applies by default.

This means Gemini's native mental model is: **file references in prompt text that are expanded into prompt content by the CLI**.

Source:

- [OpenCode `@` commands](https://github.com/google-gemini/opencode/blob/main/docs/reference/commands.md)

### Amp

Amp supports both `@`-style resource browsing and explicit structured input.

- Amp's CLI supports structured JSON input via `--stream-json-input`.
- Input content blocks can be plain text or base64-encoded images.
- Amp also supports MCP prompts and resources via `@`.
- Amp documents fuzzy file search configuration for `@` references.

This means Amp's native mental model is: **use structured input when you need rich payloads like images; use `@` references for files/resources**.

Sources:

- [Amp manual appendix](https://ampcode.com/manual/appendix)
- [Amp MCP prompts and resources](https://ampcode.com/news/mcp-prompts-and-resources)
- [Amp manual](https://ampcode.com/manual)

### Pi

Pi's public docs are leaner.

- Pi JSON mode accepts stdin messages like `{"type":"message","content":"..."}`.
- Pi exposes built-in filesystem tools such as `read_file`, `list_directory`, `glob`, and `ripgrep`.
- I did **not** find verified public docs for a rich structured attachment/image input format comparable to Amp.

This means Pi's safest public compatibility model is: **text messages plus filesystem tools**, not structured attachment blocks.

Source:

- [pi-agent npm docs](https://www.npmjs.com/package/@mariozechner/pi-agent)

### Conductor

Conductor's public material is useful for one architectural choice.

- Their `.context` directory exists specifically so context can be imported into new chats with `@`.
- They explicitly considered storing this in a database or injecting it into prompts, and chose files on disk instead.

That is the strongest product lesson from Conductor: when context is durable and reusable, make it **real files** so multiple agents can consume it naturally.

Sources:

- [The `.context` directory](https://www.conductor.build/blog/context)
- [0.44.0 changelog](https://www.conductor.build/changelog/0.44.0-new-sidebar-rebuilt-composer-codex-checkpoints)

## Implementation Principles

1. **Visible token, structured meaning**
   The prompt should keep `@foo/bar.ts` or `@github`, but the backend must preserve canonical meaning separately.

2. **Internal normalization, adapter-specific serialization**
   The composer and API should not care whether the downstream agent expects `@path`, a JSON content block, or a `mention` item.

3. **File-backed context first**
   Files and directories should be first-class references. This matches Claude, Gemini, and Conductor, and remains usable for Pi and Codex.

4. **Do not overclaim attachment support**
   Only Amp is clearly documented, in the sources we verified, as accepting structured image blocks in programmatic CLI input. For the others, use file-backed or text-backed fallbacks unless we verify richer native interfaces later.

5. **Adapters own compatibility**
   The adapter layer should decide how to turn a canonical reference into agent-specific prompt/input shape.

## Product Scope For 143

### In scope for v1

- `@` mention picker in the new-session composer
- repository file references
- repository directory references
- future-proof result typing for app/plugin/tool references
- persistence of structured references on the initial user message
- adapter-layer translation of those references per agent

### Out of scope for v1

- generic binary attachment serialization to every agent
- line-range references
- symbol references
- cross-repo search
- remote MCP resource browsing from the composer
- slash-command support in the same release

## UX Model

### Trigger behavior

- Typing `@` opens a floating picker anchored to the caret.
- Typing additional characters filters results.
- Arrow keys move through results.
- `Enter` inserts the highlighted mention.
- `Escape` closes the picker.
- Backspacing over a mention chip removes it cleanly.

### Result groups

Order groups like this:

1. `Files`
2. `Directories`
3. `Apps / integrations`
4. `Plugins`

If app/plugin support is not implemented on the backend yet, ship only `Files` and `Directories`, but keep the result model typed.

### Display model

Use plain text tokens in the textarea:

- `@internal/api/handlers/sessions.go`
- `@docs/design/overall.md`
- `@github`

Because `Textarea` cannot style inline tokens reliably, render a companion row below the composer with removable chips for the resolved references.

## Internal Data Model

The composer should maintain:

1. `messageText`
   The user-visible message.

2. `references[]`
   Canonical resolved references selected through the picker.

Suggested canonical shape:

```ts
type SessionInputReference =
  | {
      kind: "file";
      token: string;
      path: string;
      display: string;
    }
  | {
      kind: "directory";
      token: string;
      path: string;
      display: string;
    }
  | {
      kind: "app";
      token: string;
      id: string;
      display: string;
    }
  | {
      kind: "plugin";
      token: string;
      id: string;
      display: string;
    };
```

This should be the product-facing and API-facing format. It should **not** encode Claude-specific `@server:...` syntax, Codex-specific `app://...` paths, or Amp JSON block shapes directly.

## API Model

Extend manual session creation to submit both text and references:

```json
{
  "message": "Investigate @internal/api/handlers/sessions.go and use @github if you need PR context",
  "references": [
    {
      "kind": "file",
      "path": "internal/api/handlers/sessions.go",
      "display": "internal/api/handlers/sessions.go"
    },
    {
      "kind": "app",
      "id": "github",
      "display": "GitHub"
    }
  ]
}
```

The API should persist:

- raw message text
- structured references
- existing uploaded attachments/images

The timeline can continue rendering the text, while later UI improvements can also render reference chips or links from the structured metadata.

## Adapter Contract

This design only works cleanly if the adapter boundary owns serialization.

Today `agent.AgentPrompt` mostly contains:

- `SystemPrompt`
- `UserPrompt`
- `Files`

That is not enough for correct cross-agent handling of references and attachments.

We should extend the agent input/prompt contract so adapters can receive canonical references and serialize them natively.

### Recommended Go shapes

At the `agent.AgentInput` layer:

```go
type InputReference struct {
	Kind    string // file | directory | app | plugin
	Token   string
	Path    string // for file/directory
	ID      string // for app/plugin
	Display string
}
```

And optionally, for richer future payloads:

```go
type InputAttachment struct {
	Kind      string // image | file
	Name      string
	URL        string
	MediaType string
}
```

Then extend `AgentInput` with:

```go
References  []InputReference
Attachments []InputAttachment
```

Do **not** overload `Files []string` to mean all references. `Files` is too narrow and only covers a subset of what the user may select.

### Adapter responsibility

Each adapter should accept canonical references and translate them into the native style of its target CLI:

- Claude adapter:
  - file references become `@path` in the prompt text or equivalent prompt injection
  - directory references become `@path`
  - app/plugin references are ignored unless we later have a verified native mapping

- Codex adapter:
  - app/plugin references become structured mention items when we integrate with a richer Codex protocol surface
  - until then, preserve readable text in the prompt and treat references as backend-side context hints

- OpenCode adapter:
  - file/directory references become `@path` tokens because OpenCode expands them through `read_many_files`

- Amp adapter:
  - file/resource references can be represented through prompt text
  - images should eventually use Amp's structured JSON content blocks when we move the adapter to its JSON input mode

- Pi adapter:
  - preserve readable paths in text and rely on Pi's built-in file tools for access
  - do not assume native structured attachments until verified

This keeps the composer and API stable while allowing each adapter to evolve independently.

## Recommended Serialization Strategy

### For files and directories

Store them canonically in `references[]`, then let adapters choose one of:

1. emit `@path` into the prompt for agents that natively expand file references
2. pre-read and inject content/listings into prompt text for agents that do not
3. attach as structured reference metadata where the target protocol supports it

### For apps and plugins

Store canonical ids in `references[]`, then let adapters choose one of:

1. structured mention item where supported
2. textual hint plus tool availability in the system prompt
3. no-op for unsupported agents

### For uploaded images/files

Keep existing upload behavior separate from `@` references.

- uploads remain explicit attachments
- references remain repo-local or tool-local context selections

Do not try to force uploads through the same serialization path as file mentions.

## Backend Plan

### 1. Add mention discovery endpoints

Add lightweight APIs under `/api/v1/`:

- `GET /api/v1/session-composer/files?q=...&repository_id=...`
- `GET /api/v1/session-composer/tools?q=...`

`files` should be scoped to the selected repo and prefer:

- exact prefix matches
- basename matches
- shorter relative paths

### 2. Extend manual session create payload

Add `references` to the manual session create request.

Persist structured references with the initial user message and/or a dedicated session-input payload record.

### 3. Thread references into orchestrator input

When the session is started, thread the stored references into `agent.AgentInput`.

### 4. Move serialization into adapters

Do not serialize `references[]` in handlers.

Handlers and services should preserve canonical meaning only. The adapter should decide how the target CLI should see those references.

### 5. Preserve replay/resume fidelity

Stored references should be available when resuming or replaying the first turn so the agent can be rehydrated with the same contextual targets.

## Frontend Plan

### 1. Add mention-aware composer behavior to the existing textarea

Keep the current `Textarea`. Add:

- active mention query parsing
- suggestion popover
- insertion/removal helpers
- resolved reference chips below the textarea

Do not switch to a rich text editor in v1.

### 2. Add mention search hooks

Use TanStack Query hooks:

- `useSessionComposerFileMentions`
- `useSessionComposerToolMentions`

Use `useDeferredValue` to keep the picker responsive.

### 3. Keep text and references synchronized

If a token is removed from the message, drop the matching canonical reference.

### 4. Keep uploads separate

Uploads and image URLs continue to work as they do today. `@` mentions should complement that workflow, not replace it.

## Sequenced Rollout

### Phase 1: Canonical file references

- file and directory mention picker
- `references[]` in create-session payload
- persistence on initial user message
- adapter support for file/directory references

### Phase 2: Timeline and resume fidelity

- render references in the session timeline
- preserve references when reconstructing a session

### Phase 3: App/plugin references

- tool/app/plugin suggestions in the picker
- canonical id persistence
- adapter-specific mappings where supported

### Phase 4: Rich attachment serialization

- Amp JSON input support for image blocks
- richer Codex protocol serialization if/when we integrate beyond plain CLI prompt text

## Testing Plan

### Frontend

- unit tests for active mention parsing
- component tests for keyboard navigation in the picker
- component tests for token insertion at arbitrary caret positions
- component tests for removing a chip and syncing message text
- component tests proving repo changes invalidate file suggestions

Required verification after implementation:

- `npm run typecheck`
- `npm run lint`
- `npm run build`

### Backend

- handler tests for mention discovery endpoints
- handler tests for create-session validation with `references`
- orchestrator tests proving references are threaded into `AgentInput`
- adapter tests proving each adapter serializes canonical references correctly
- tests proving repo/org authorization is enforced

Required verification after implementation:

- `go vet ./...`
- `go build ./...`
- `go test ./...`

## Risks

### 1. Treating `@` as text only

This throws away UI intent and forces every downstream agent path to reparsed user text.

### 2. Baking vendor syntax into product APIs

If the API stores `app://...` or Amp JSON content blocks directly, the product becomes adapter-specific and harder to evolve.

### 3. Overstating attachment support

Only use native structured attachment modes we have verified. For everything else, prefer file-backed or text-backed fallback behavior.

### 4. Expanding directories too aggressively

Claude and Gemini differ here. Our canonical reference should stay neutral and let the adapter decide whether to emit a directory reference or a listing/injected summary.

## Open Questions

1. Should references be stored directly on `session_messages`, or on a sibling table keyed by message id?
2. Should adapters be allowed to pre-read file contents in the sandbox and rewrite prompts, or should they prefer native `@path` expansion whenever available?
3. Should app/plugin references silently degrade to plain text on unsupported agents, or should the UI warn the user before submission?

## Recommendation

Build the feature around one rule:

**The composer resolves user intent into canonical references, and the adapter is responsible for translating those references into the downstream agent's native input format.**

That avoids overengineering the product layer while still matching how the major coding agents actually work:

- Claude and Gemini prefer file-backed `@` references
- Codex prefers visible trigger text paired with structured mentions
- Amp supports structured content blocks for richer payloads
- Pi is safest with text plus filesystem tools

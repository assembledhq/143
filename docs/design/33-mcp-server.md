# MCP Server Implementation Plan

> **Status:** Implemented | **Last reviewed:** 2026-03-19

## Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│ Orchestrator (RunAgent)                                         │
│                                                                 │
│  1. resolveAgentEnv()     — LLM API keys                       │
│  2. Create sandbox                                              │
│  3. injectCodexAuth()     — existing                            │
│  4. injectMCPServer()     — NEW: copy binary + write config     │
│  5. Clone repo                                                  │
│  6. Execute agent                                               │
└─────────────────────────────────────────────────────────────────┘
                          │
          ┌───────────────┼───────────────┐
          ▼               ▼               ▼
   Claude Code         Codex         Gemini CLI
   (mcp.json)    (config.toml)    (prompt inject)
          │               │               │
          └───────┬───────┘               │
                  ▼                       │
         143-mcp (STDIO)                  │
         JSON-RPC server          static context
         in sandbox               fallback
```

## Steps

### Step 1: MCP Protocol Layer (`internal/services/mcp/`)

**server.go** — Core JSON-RPC STDIO server implementing MCP spec:
- `Server` struct holding integration registry + config
- `Serve(ctx, stdin, stdout)` — main loop reading JSON-RPC requests
- Handles: `initialize`, `tools/list`, `tools/call`
- Clean shutdown on context cancellation

**tools.go** — Tool definitions and dispatch:
- Converts registry contents into MCP tool schemas
- Routes `tools/call` to the correct integration method
- Handles parameter validation and error formatting
- Tools: `list_errors`, `get_error`, `get_error_trend`, `find_related_errors`,
  `list_tasks`, `get_task`, `create_task`, `update_task`, `find_related_tasks`,
  `search_documents`, `get_document`, `search_messages`, `get_thread`

**protocol.go** — MCP/JSON-RPC wire types:
- Request, Response, Error types
- Tool, ToolInput, ToolResult types
- Initialize/Capabilities handshake types

### Step 2: MCP Binary (`cmd/mcp/main.go`)

Thin entry point that:
- Reads integration credentials from env vars (SENTRY_TOKEN, LINEAR_TOKEN, etc.)
- Builds integration registry from available credentials
- Starts the MCP STDIO server on stdin/stdout
- Logs to stderr (STDIO MCP requires clean stdout)

### Step 3: Orchestrator Integration (`orchestrator.go`)

**`injectMCPServer()`** method:
- Fetches Sentry + Linear credentials for the org
- Builds env var map for the MCP binary (SENTRY_TOKEN, SENTRY_ORG, LINEAR_TOKEN)
- Writes per-CLI config files:
  - Claude Code: `$HOME/.claude/mcp.json`
  - Codex: `$HOME/.codex/config.toml` (append MCP section)
  - Gemini: skip (no MCP support — use prompt-based fallback)
- Non-fatal: log warning on failure, don't block agent run

**resolveAgentEnv() extension:**
- Add MCP credential env vars alongside LLM keys so the MCP binary can read them

### Step 4: Tests

- `internal/services/mcp/server_test.go` — protocol handshake, tool listing
- `internal/services/mcp/tools_test.go` — tool dispatch with mock registry
- Orchestrator injection test

## Security Considerations

- MCP binary runs inside gVisor sandbox — same isolation as agent CLI
- Credentials passed via env vars (not written to files)
- MCP binary only has access to integration tokens, not LLM keys
- STDIO transport — no network ports exposed
- Tool inputs validated before passing to integration layer

## Key Design Decisions

1. **STDIO over HTTP**: No network policy changes needed. Each agent gets isolated MCP process.
2. **Binary in container image**: MCP binary pre-installed in `143-sandbox:latest` Docker image — no runtime copying needed.
3. **Env var credentials**: Follow existing pattern (resolveAgentEnv). MCP binary reads from env, not files.
4. **Gemini fallback**: Since Gemini CLI MCP is undocumented, inject integration data as static context in the prompt instead of MCP.
5. **Non-fatal injection**: If MCP setup fails, agent still runs — just without integration tools.

# mcp package — Integration Tools (CLI + MCP)

## Prefer CLI over MCP for agent-facing tools

When exposing integration tools (Sentry, Linear, Notion, Slack) to agents running in sandboxes, **always use the `143-tools` CLI** rather than the MCP server. The MCP server exists for IDE integrations and external MCP clients, not for sandbox agents.

### Why CLI wins for agents

1. **Lower token overhead.** The CLI skills doc (`GenerateSkillsDoc()`) is 200-800 tokens. An MCP server's tool definitions, protocol handshake, and JSON-RPC framing consume significantly more context. Every token spent on tool infrastructure is a token not available for reasoning.

2. **LLMs already know how to use CLIs.** Shell commands are heavily represented in training data. Agents reliably produce `143-tools linear_list_tasks --team ENG --limit 10`. MCP tool calls require the agent to understand JSON-RPC framing, which adds a failure mode.

3. **Simpler debugging.** A CLI call is a single line in the session log. An MCP interaction is a multi-message protocol exchange that's harder to trace.

4. **No subprocess lifecycle.** The MCP server runs as a long-lived subprocess that must be started, health-checked, and cleaned up. The CLI is a single invocation — run it, read stdout, done.

### How CLI injection works

The orchestrator (`internal/services/agent/orchestrator.go`) handles two things:

1. **Prompt injection** — `buildIntegrationSkills()` generates a markdown skills doc via `GenerateSkillsDoc(tr)` and injects it into `AgentInput.IntegrationSkills`. Each adapter writes this into the agent's system prompt.

2. **Credential injection** — `resolveAgentEnv()` passes credentials as environment variables (`SENTRY_AUTH_TOKEN`, `LINEAR_ACCESS_TOKEN`, etc.) to the sandbox. The `143-tools` binary reads these at runtime via `BuildRegistryFromEnv()`.

The `143-tools` binary is pre-installed in the sandbox Docker image. No per-session setup needed.

### When to use the MCP server

- IDE integrations that speak MCP protocol
- External clients that connect via JSON-RPC over stdio
- Interactive development tools (not sandbox agents)

### Adding new integration tools

When adding a new tool (e.g., a new Notion query):

1. Add the integration interface method in `internal/services/integration/types.go`
2. Implement it on the provider (e.g., `notion.go`)
3. Add the tool definition and dispatch in `tools.go` — this automatically makes it available in **both** the CLI and MCP server since they share `ToolRegistry`
4. The CLI skills doc auto-generates from the tool registry for sandbox prompt injection
5. Update the public API reference at `docs/public/reference/agent-tools.mdx` so the documented command names, flags, required fields, defaults, and coding-agent use cases match the tool registry
6. Update `registry_builder.go` if new environment variables are needed

# 143-tools CLI

This directory owns the `143-tools` binary entrypoint. The command surface is defined in `internal/services/mcp/tools.go` and exposed through the CLI runner in `internal/services/mcp/cli.go`.

When changing the CLI itself, update the directory-scoped implementation notes and the public API reference at `docs/public/reference/agent-tools.mdx` in the same change. Keep the reference aligned with command names, flags, required fields, defaults, and common coding-agent use cases.

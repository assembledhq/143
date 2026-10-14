# 143-tools CLI

This directory owns the `143-tools` binary entrypoint. The command surface is defined in `internal/services/mcp/tools.go` and exposed through the CLI runner in `internal/services/mcp/cli.go`.

When changing the CLI itself, update the directory-scoped implementation notes and the public API reference at `docs/public/reference/agent-tools.mdx` in the same change. Keep the reference aligned with command names, flags, required fields, defaults, and common coding-agent use cases.

Sandbox helpers dispatch through `internal/services/sandboxauth`. The installed
`gh` wrapper calls `143-tools gh`, which uses the CLI's Unix transport to observe
actual GitHub API requests below its cache. The credential socket binds those
observations to the session and resolved installation. Fresh tokens stay in the
child environment; HTTP bodies and credentials never enter request telemetry.

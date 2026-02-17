# 143 — autonomous bug fixing

**from issue to PR, with zero human intervention**

[Quickstart](#quickstart) · [Architecture](docs/design/overall.md) · [How it works](#how-it-works) · [Contributing](#contributing)

---

143 ingests production issues from Sentry, Linear, and support tickets, runs AI coding agents in sandboxed containers to generate fixes, validates them, and ships PRs — with zero human intervention required.

Every PR review makes the next run smarter. Learned conventions are extracted and fed back into future agent executions, creating a flywheel that compounds over time.

## Quickstart

```bash
git clone https://github.com/assembledhq/143.git && cd 143 && ./setup.sh
```

Requires Go, Node.js, and PostgreSQL. The setup script will install anything missing via Homebrew (macOS) or apt (Linux).

```bash
# start the api server
go run cmd/server/main.go

# start the frontend
cd frontend && npm run dev
```

API runs on `localhost:8080`, frontend on `localhost:3000`.

## How it works

```
issues in → prioritize → estimate complexity → run agents → validate → ship PR → measure impact
                                                                                       ↓
                                                                              learn from outcomes
```

1. **Ingest** — pull issues from Sentry, Linear, support tickets via webhooks
2. **Prioritize** — score by customer count, severity, revenue risk
3. **Estimate** — LLM pre-analysis to classify complexity before burning compute
4. **Execute** — run coding agents (Claude Code, Codex, etc.) in isolated Docker containers
5. **Validate** — LLM-based direction/correctness/quality checks + CI + regression tests
6. **Ship** — open a GitHub PR with full context for human review
7. **Observe** — measure post-deploy impact on error rates and support volume
8. **Learn** — extract review feedback into conventions, feed production outcomes back into future runs

## Stack

| Layer | Tech |
|-------|------|
| Backend | Go, chi, pgx, zerolog |
| Frontend | Next.js, React, shadcn/ui, TanStack Query |
| Database | PostgreSQL |
| Infra | Docker (sandboxes), Datadog (monitoring) |

## Project structure

```
docs/design/    # numbered design docs (architecture, schema, each subsystem)
setup.sh        # one-command local setup
AGENTS.md       # coding conventions and patterns
```

## Why "143"

In 1943, Lockheed's Skunk Works team designed and built the XP-80 Shooting Star — America's first operational jet fighter — in just 143 days. A small, autonomous team with full ownership, no bureaucracy, and a bias toward shipping.

Most bugs don't need a sprint planning meeting. They need someone (or something) to isolate the issue, write the fix, validate it, and open the PR. 143 is that something — a small, autonomous system that takes ownership of the boring-but-important work so your team can focus on what actually moves the product forward.

## Contributing

We welcome contributions. Read through the [design docs](docs/design/overall.md) to understand the architecture, then pick an issue and open a PR.

## License

[MIT](LICENSE)

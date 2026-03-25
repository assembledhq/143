# 143 — AI agents that fix and improve production systems

**from issues to validated PRs, on autopilot**

[Getting Started](#getting-started) · [Development Setup](docs/contributing/development-setup.md) · [Architecture](docs/design/overall.md) · [143.dev](https://www.143.dev)

---

## Why 143

Most issues in your backlog don't need a sprint planning meeting. They need someone (or something) to analyze the landscape, prioritize what matters, write the fix, validate it, and open the PR.

143 gives you two things:

### Bring your own coding agent

Use whatever coding agent you trust — Claude Code, Codex, Cursor, or your own custom agent. 143 orchestrates the work: it ingests issues, plans the approach, spins up sandboxed containers, and hands off execution to the coding agent you configure. You stay in control of how code gets written while 143 handles everything around it.

### An autopilot PM that learns your product

143 includes an AI product manager agent that understands your product roadmap and engineering philosophy. It analyzes your full issue landscape across Sentry errors, Linear tickets, and support requests — clusters related problems, identifies root causes, and builds a prioritized plan. Every PR review teaches it more about your codebase conventions and preferences, creating a flywheel that compounds over time.

The result: bugs get triaged, planned, fixed, validated, and shipped as PRs — without context-switching your team away from the work that matters.

> **Don't want to self-host?** [143.dev](https://www.143.dev) is the hosted version — connect your repos and start shipping fixes in minutes.

## How it works

```
issues in → PM agent plans → coding agents execute → validate → ship PRs → measure impact
                                                                                  ↓
                                                                         learn from outcomes
```

1. **Ingest** — pull issues from Sentry, Linear, support tickets via webhooks
2. **Plan** — AI product manager analyzes all issues, clusters related problems, and builds a prioritized work plan
3. **Execute** — coding agents run in isolated Docker containers with the PM's approach hints
4. **Validate** — direction/correctness/quality checks + CI + regression tests
5. **Ship** — open GitHub PRs with full context for human review
6. **Observe** — measure post-deploy impact on error rates and support volume
7. **Learn** — extract review feedback into conventions, feed back into future runs

## Key Features

**Sandboxed execution with gVisor** — every agent run happens in an isolated container with gVisor syscall-level enforcement, read-only root filesystem, dropped capabilities, and network restricted to LLM APIs and package registries. Graceful fallback to standard Docker in local dev.

**6-stage validation pipeline** — direction check, correctness check, quality check, security scanning (gitleaks + semgrep), regression tests, and CI integration. Fail-fast ordering minimizes wasted tokens.

**Smart routing & complexity estimation** — before spinning up an agent, 143 estimates issue complexity (trivial → very complex) and routes accordingly. An admin-configurable aggressiveness slider controls which tiers the system attempts autonomously.

**Interactive sessions** — create free-form sessions without waiting for a PM cycle. Each agent turn auto-snapshots sandbox state so you can review, send follow-ups, or resume locally via CLI.

**Multi-agent sessions** — run multiple agents in parallel on the same codebase: compare Claude vs Codex output, or split backend and frontend work across independent threads.

**Post-deploy impact measurement** — after a fix ships, 143 measures the actual impact on Sentry error rates, support ticket volume, and custom metrics. Closes the loop from issue to outcome.

**Distributed, no primary node** — symmetric architecture where every node runs the same binary. Leader election via Postgres advisory locks, job distribution via `SELECT ... FOR UPDATE SKIP LOCKED`. No external coordination service needed.

**Dashboard credential management** — all API keys (LLM providers, GitHub, Sentry, Linear) are configured through the UI with per-org isolation and AES-256-GCM encryption at rest. No env vars for secrets in production.

**Prompt overrides & tuning** — customize agent system prompts per repo, issue type, or execution phase. Insert-only versioned config preserves full change history with point-in-time rollback.

## Getting Started

Requires Go 1.24+, Node.js 18+, and PostgreSQL 17. The setup script installs anything missing via Homebrew (macOS) or apt (Linux).

```bash
git clone https://github.com/assembledhq/143.git && cd 143 && ./setup.sh
make dev
```

See the [development setup guide](docs/contributing/development-setup.md) for detailed instructions, make commands, environment configuration, and secrets management.

## Contributing

Read through the [design docs](docs/design/overall.md) to understand the architecture, then pick an issue and open a PR. See [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

## Why "143"

In 1943, Lockheed's Skunk Works team designed and built the XP-80 Shooting Star — America's first operational jet fighter — in just 143 days. A small, autonomous team with full ownership, no bureaucracy, and a bias toward shipping.

## License

[MIT](LICENSE)

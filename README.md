# 143 — AI coding agents, built for teams

**Transparent automations. Cloud agents. Eval-driven loops.**

[Getting Started](#getting-started) · [Development Setup](docs/contributing/development-setup.md) · [Architecture](docs/design/overall.md) · [143.dev](https://www.143.dev)

---

## Why 143

Most coding agent tools are built for solo developers. But most professional engineering teams need a high level of visibility. When someone sets up an automation to improve test coverage or scan for security vulnerabilities, the whole team should be able to access and have visibility by default. When a coding agent opens a PR, everyone should be able to see the prompt that drove it.

143 is built from the ground up for teams (engineers and non-engineers alike). It was born from the experience of working on a small team where nobody had visibility into what others were doing, knowledge stayed siloed, and non-technical teammates had a hard time contributing code even when they knew exactly what needed to change.

### Built for teams

By default, every automation, prompt, and agent run is visible to your entire team. When someone configures an automation to fix flaky tests or audit API endpoints, the rest of the team can see exactly what's been set up, what's running, and what it produced. This means non-engineers can contribute code too — they write prompts, the agent writes code, and the whole team can review both the prompt and the output in the open.

### Cloud agents you already use

The big labs are constantly one-upping each other with newer and better models and agent harnesses. 143 has no vendor lock-in: use Claude Code, Codex, or whatever comes next. When a better model drops, you can swap it in and keep going.

143 runs your agents in the cloud so your whole team can use them without local setup. You can spin up the same workflow across branches, get preview environments automatically, and let anyone on the team kick off runs.

### Loops: eval-driven improvement

Based on Karpathy's autoresearch concept, you can define an eval and have your coding agents hill-climb toward better results. Want to improve API latency? Define a latency benchmark, and 143 will run your coding agent in a loop — each iteration measuring against the eval, learning what worked, and pushing further.

This works for anything measurable: test coverage, bundle size, response times, error rates. Define the target, and let the agents grind toward it.

## How it works

```
┌─────────────────────────────────────────────────────────┐
│  Your Team (engineers + non-engineers)                  │
│  Configure automations, projects, loops via 143 UI      │
└──────────────────────┬──────────────────────────────────┘
                       │
                       ▼
┌─────────────────────────────────────────────────────────┐
│  143 Orchestrator                                       │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐     │
│  │ Automations │  │  Sessions   │  │   Loops     │     │
│  │ (recurring) │  │ (one-shot)  │  │ (eval-driven│     │
│  └──────┬──────┘  └──────┬──────┘  │  iteration) │     │
│         │                │         └──────┬──────┘     │
│         └────────────────┼────────────────┘             │
│                          ▼                              │
│  ┌──────────────────────────────────────────────┐       │
│  │  Cloud Sandboxes (gVisor-isolated Docker)    │       │
│  │  ┌────────────┐ ┌───────┐ ┌────────────────┐│       │
│  │  │ Claude Code│ │ Codex │ │ Any future agent││       │
│  │  └────────────┘ └───────┘ └────────────────┘│       │
│  └──────────────────────┬───────────────────────┘       │
│                         ▼                               │
│  Validate (CI + security scan + quality checks)         │
└──────────────────────┬──────────────────────────────────┘
                       │
          ┌────────────┼────────────┐
          ▼            ▼            ▼
    GitHub PRs   Preview Envs   Eval Results
    (for review) (for testing)  (feed back into loops)
```

1. **Configure** — set up automations, projects, or loops — shared with the team by default
2. **Execute** — coding agents run in isolated Docker containers in the cloud
3. **Validate** — CI, security scanning (gitleaks, semgrep), and quality checks
4. **Ship** — open GitHub PRs with full context and preview environments
5. **Loop** — for eval-driven tasks, measure results and iterate automatically

## Using 143

If you're using 143 against your repos — on the hosted version or not — the [user guides in `docs/guides/`](docs/guides/) cover per-repo configuration (currently: preview environments).

If you're running your own 143 instance, see [`docs/self-hosting/`](docs/self-hosting/) for GitHub App setup and the production deployment checklist.

## Built for production

Every agent runs in a gVisor-isolated container with a read-only filesystem and network access limited to LLM APIs and package registries. PRs go through security scanning (gitleaks, semgrep), correctness checks, and your CI before a human ever sees them. Your code never leaves infrastructure you control.

The architecture is symmetric — there's no primary node. A Postgres-backed job queue handles scheduling and leader election, so scaling out means running more copies of the same binary behind a load balancer.

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

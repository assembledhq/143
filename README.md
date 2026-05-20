# 143

Open-source autopilot for coding agents.

143 gives engineering teams one shared place to run coding agents in the cloud, connect them to the tools that hold product context, and turn the result into reviewable GitHub PRs.

[143.dev](https://www.143.dev) · [Getting started](#getting-started) · [Development setup](docs/contributing/development-setup.md) · [Architecture](docs/design/overall.md) · [Self-hosting](docs/self-hosting/README.md)

## What is this?

Most coding tools are built around one developer at a time. That works fine for local autocomplete, but it gets awkward once agents start doing real work: every engineer has their own setup, their own automations, their own prompt history, and their own pile of context.

143 is built for the team version of that workflow.

You connect your repos, pick the coding agents your team wants to use, and wire in the tools that already describe what needs to be built or fixed. From there, engineers can start one-off sessions, set up recurring automations, let Autopilot work through Linear or Sentry issues, and review the branches and PRs that come out the other side.

The important part is that the work is visible by default. Runs, prompts, outputs, previews, audit logs, and usage are shared at the organization level, so the team can see what the agents are doing instead of guessing from a surprise PR.

## Why it is interesting

143 is trying to make coding agents feel less like personal sidecars and more like shared engineering infrastructure.

- **Team-owned agent work.** Automations, sessions, Autopilot runs, and history live in one workspace instead of on individual laptops.
- **Context from the tools you already use.** Linear, Sentry, Slack, Notion, and GitHub can all feed the agent useful context. Setup happens once for the organization.
- **Cloud execution.** Agents run in isolated cloud sandboxes, so teammates can kick off work from a browser, Slack, or mobile without keeping a local machine awake.
- **PRs and previews, not mystery patches.** Agent output becomes a branch or pull request with a live preview when the repo supports it.
- **Bring the agent you prefer.** 143 is designed around coding-agent adapters. Today that means tools like Claude Code, Codex, Gemini CLI, Amp, and Pi; the point is not to bet the product on one model vendor.
- **Open source by default.** You can use the hosted service or run it yourself. The repo is MIT licensed.

## What you can use it for

Teams are using 143 for work that benefits from repeatability and shared visibility:

- recurring automations like fixing flaky tests, adding missing coverage, or running maintenance work;
- Autopilot runs for Linear and Sentry issues;
- manual cloud sessions when you want an agent to work on a branch while you are away from your desk;
- multi-agent session tabs when you want to compare approaches or continue work with a different agent;
- review/fix loops where an agent reviews its own change, applies fixes, and only then hands the PR to a human;
- builder workflows where non-engineers can request scoped changes with extra review gates before a human reviewer sees the PR;
- usage tracking, token and runtime analytics, and audit logs for settings changes.

You do not need to use all of that at once. A common first setup is simply: connect GitHub, choose a coding agent, connect Linear or Sentry, and start with one issue-to-PR flow.

## How it works

At a high level:

1. Connect GitHub repositories and the tools that carry product or production context.
2. Start a session manually, schedule an automation, or let Autopilot pick up an issue.
3. 143 creates an isolated sandbox, checks out the repo, and runs the selected coding agent.
4. The agent produces a diff, can run repo-defined checks, and can start a preview if the repo has preview config.
5. 143 publishes a branch or opens a GitHub PR for normal human review and CI.

The backend is Go, Postgres, and a Postgres-backed job queue. The frontend is Next.js. Worker nodes run agent sandboxes with Docker/gVisor. The detailed system design lives in [docs/design/overall.md](docs/design/overall.md).

## Getting Started

For local development you need:

- Go 1.24+
- Node.js 18+
- PostgreSQL 17

The setup script installs missing dependencies on macOS with Homebrew or on Linux with apt, creates the local database, installs dependencies, copies `.env.example` to `.env`, and runs migrations.

```bash
git clone https://github.com/assembledhq/143.git
cd 143
./setup.sh
make dev
```

`make dev` starts Postgres, the Go API server, and the Next.js frontend through Docker Compose.

For webhook or GitHub OAuth work, use the ngrok flow instead:

```bash
make dev-ngrok NGROK_DOMAIN=yourname.ngrok.dev
```

See the [development setup guide](docs/contributing/development-setup.md) for the full local setup, environment variables, and useful Make targets.

## Self-hosting

143 is free to run yourself. Self-hosting means bringing your own infrastructure, GitHub App, domain, worker capacity, and LLM/coding-agent credentials.

Start with [docs/self-hosting/](docs/self-hosting/README.md). The hosted version at [143.dev](https://www.143.dev) is the managed path; hosted billing is based on container runtime minutes, and 143 does not mark up LLM usage.

## Contributing

If you want to work on the product, read the [development setup guide](docs/contributing/development-setup.md) and the [design overview](docs/design/overall.md). The design docs are intentionally part of the repo because a lot of the product decisions are architectural, not just UI copy.

Issues and PRs are welcome. See [CONTRIBUTING.md](CONTRIBUTING.md) for the project rules.

## Why "143"?

In 1943, the Lockheed Skunk Works team built the XP-80 Shooting Star in 143 days. The name is a nod to small teams with enough ownership to move quickly.

## License

[MIT](LICENSE)

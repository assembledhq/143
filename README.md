# 143

The open-source platform where your whole team builds software together.

143 is an open-source platform for running coding agents in the cloud. Connect your repos, pick the agents you want — Codex, Claude Code, OpenCode, and others — wire in the tools that hold your product context, and let the team kick off work from a browser, Slack, or an issue tracker. What comes back is a reviewable branch or GitHub PR, with the diff, transcript, checks, and a live preview all in one place.

[143.dev](https://www.143.dev) · [Docs](https://www.143.dev/docs) · [Architecture](docs/design/overall.md) · [Self-hosting](https://www.143.dev/docs/self-hosting)

<p align="center">
  <img src="frontend/public/product/product-review-diff.png" alt="Reviewing an agent's change in 143: a code diff with the changed-files list and review controls" width="900">
</p>

## What is this?

Most coding tools are built for one developer at a time. That's fine for local autocomplete, but it falls apart once agents start doing real work — every engineer ends up with their own setup, their own automations, and their own pile of context that no one else can see.

143 runs that work as shared infrastructure instead. Sessions, automations, Autopilot runs, prompts, and history live in one workspace, so the team can watch what the agents are doing rather than find out from a surprise PR. Setup happens once for the org, not once per laptop.

The agent stays close to the tools you already trust: GitHub remains the source of truth for branches, review, CI, and merge rules. 143 owns the workflow around it — context, credentials, sandboxed execution, previews, follow-ups, and audit logs.

## What it does

- **Team-owned agent work:** automations, sessions, Autopilot runs, and history live in one shared workspace.
- **Context from your existing tools:** GitHub, Linear, Sentry, Slack, Notion, PagerDuty, and more can feed agents the context they need to do the work.
- **Cloud execution:** agents run in isolated sandboxes (Docker/gVisor), so anyone can start work from a browser, Slack, or their phone without keeping a laptop awake.
- **PRs and previews:** output becomes a branch or PR, with a live preview when the repo supports it.
- **Bring the agent you prefer:** 143 is built around coding-agent adapters (Codex, Claude Code, OpenCode, Amp, Pi), so it isn't tied to one model vendor.
- **Review loops before humans step in:** agents can repair failing tests, respond to review feedback, and iterate inside guardrails first.

A common first setup is the smallest one: connect GitHub, choose an agent, connect Linear or Sentry, and run a single issue-to-PR flow. Add automations, previews, and Autopilot when you need them.

## How it works

1. Connect GitHub repos and the tools that carry product or production context.
2. Start a session manually, schedule an automation, or let Autopilot pick up an eligible issue.
3. 143 spins up an isolated sandbox, checks out the repo, and runs your chosen agent.
4. The agent produces a diff, runs any repo-defined checks, and can launch a preview.
5. 143 opens a branch or PR for normal human review and CI.

The backend is Go and Postgres with a Postgres-backed job queue; the frontend is Next.js; workers run agent sandboxes with Docker/gVisor. The full system design lives in [docs/design/overall.md](docs/design/overall.md).

## Getting started

You'll need Go 1.24+, Node.js 24+, and PostgreSQL 17.

```bash
git clone https://github.com/assembledhq/143.git
cd 143
./setup.sh
make dev
```

`setup.sh` installs missing dependencies (Homebrew on macOS, apt/NodeSource on Linux), creates the local database, copies `.env.example` to `.env`, and runs migrations. `make dev` brings up Postgres, the Go API, and the Next.js frontend through Docker Compose.

For webhook or GitHub OAuth work, use the ngrok flow:

```bash
make dev-ngrok NGROK_DOMAIN=yourname.ngrok.dev
```

See the [development setup guide](docs/contributing/development-setup.md) for environment variables and the full list of Make targets.

## Self-hosting

143 is free to run yourself — you bring your own infrastructure, GitHub App, domain, worker capacity, and LLM/coding-agent credentials. Start with [docs/self-hosting/](docs/self-hosting/README.md).

The hosted service at [143.dev](https://www.143.dev) is the managed path. Billing there is based on container runtime minutes, and 143 doesn't mark up LLM usage.

This repo contains everything you need to self-host: the application code, Dockerfiles, migrations, local dev setup, the single-node deployment path, public docs, and operational scripts. Anything that references Assembled's private production infrastructure (encrypted env bundles, deploy keys, fleet hosts, `assembledhq/143-infra`) is specific to the hosted service and isn't needed to run your own instance.

## Contributing

Issues and PRs are welcome. Start with the [development setup guide](docs/contributing/development-setup.md) and the [design overview](docs/design/overall.md) — a lot of the product decisions are architectural, and the design docs are in the repo on purpose. See [CONTRIBUTING.md](CONTRIBUTING.md) for the rules, and report security issues through [SECURITY.md](SECURITY.md) rather than public issues.

## Why "143"?

In 1943, Lockheed's Skunk Works built the XP-80 Shooting Star — America's first operational jet fighter — in 143 days. The name is a nod to small teams with enough ownership to move fast.

## License

[MIT](LICENSE)

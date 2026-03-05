# 143 — AI agents that fix and improve production systems

**from issues to validated PRs, on autopilot**

[Local Development](docs/local-development.md) · [Architecture](docs/design/overall.md) · [How it works](#how-it-works)

---

143 ingests production issues from Sentry, Linear, and support tickets. An AI product manager agent analyzes the full issue landscape, clusters related problems, and builds a prioritized plan. Coding agents execute the plan in sandboxed containers — fixing bugs, shipping improvements, and opening validated PRs.

Every PR review makes the next run smarter. Learned conventions are extracted and fed back into future agent executions, creating a flywheel that compounds over time.

## Local Development

### Setup

Requires Go 1.24+, Node.js 18+, and PostgreSQL 17. The setup script installs anything missing via Homebrew (macOS) or apt (Linux).

```bash
git clone https://github.com/assembledhq/143.git && cd 143 && ./setup.sh
```

This creates the database, copies `.env.example` to `.env`, installs dependencies, and runs migrations.

### Running

**Option A — with Docker Compose**:

```bash
make dev              # starts Postgres, API server, and frontend
```

**Option B — without Docker** (two terminals):

```bash
make server-dev       # Go API on localhost:8080
make frontend-dev     # Next.js on localhost:3000
```

The frontend proxies `/api/*` to the Go server automatically.

### Common commands

```bash
make test             # run all tests
make test-race        # run tests with race detector
make test-coverage    # generate coverage.html
make migrate-up       # apply pending migrations
make migrate-down     # roll back last migration
make lint             # run golangci-lint
```

### Environment

All config lives in `.env` (created by setup). The defaults work out of the box for local dev.

To enable GitHub OAuth login and repo onboarding, set these in `.env`:

- `GITHUB_OAUTH_CLIENT_ID` / `GITHUB_OAUTH_CLIENT_SECRET` — create an OAuth app
- `GITHUB_APP_ID` / `GITHUB_APP_PRIVATE_KEY` / `GITHUB_WEBHOOK_SECRET` — create a GitHub App

See the [local development guide](docs/local-development.md) for step-by-step setup including webhook tunneling, and `.env.example` for the full list of variables.

### Secrets & API Keys

For local dev, just copy `.env.example` into `.env` and edit it directly — it's gitignored. For syncing secrets across machines or environments, we use [SOPS](https://github.com/getsops/sops) + [age](https://github.com/FiloSottile/age) to encrypt secrets into `.env.enc` (safe to commit). See the [secrets management guide](docs/secrets/README.md) for full setup.

```bash
make secrets-setup                   # one-time: generate age keypair
make secrets-encrypt                 # encrypt .env → .env.enc
make secrets-decrypt                 # decrypt .env.enc → .env
make secrets-encrypt ENV=staging     # per-environment support
make secrets-rotate                  # re-encrypt after adding a team member's key
```

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

## Project structure

```
cmd/
  server/         # API server entrypoint
  migrate/        # database migration runner
internal/
  api/
    handlers/     # HTTP handlers (auth, repos, webhooks, health, settings)
    middleware/   # auth, CORS, logging, org context
    router.go     # chi router + route registration
  config/         # env-based configuration
  db/             # data access layer (pgx, named args, store-per-domain)
  models/         # domain types + API response envelopes
  services/
    github/       # GitHub App JWT + installation token management
  worker/         # background job processor
  cluster/        # node heartbeat + scheduler leader lock
  logging/        # zerolog setup
migrations/       # SQL migration files
frontend/         # Next.js app (App Router, shadcn/ui, TanStack Query)
docs/design/      # numbered design docs
```

## Why "143"

In 1943, Lockheed's Skunk Works team designed and built the XP-80 Shooting Star — America's first operational jet fighter — in just 143 days. A small, autonomous team with full ownership, no bureaucracy, and a bias toward shipping.

Most issues in your backlog don't need a sprint planning meeting. They need someone (or something) to analyze the landscape, prioritize what matters, write the fix, validate it, and open the PR. 143 is that something.

## Contributing

Read through the [design docs](docs/design/overall.md) to understand the architecture, then pick an issue and open a PR.

## License

[MIT](LICENSE)

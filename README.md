# 143 — autonomous bug fixing

**from issue to PR, with zero human intervention**

[Local Development](#local-development) · [Architecture](docs/design/overall.md) · [How it works](#how-it-works)

---

143 ingests production issues from Sentry, Linear, and support tickets, runs AI coding agents in sandboxed containers to generate fixes, validates them, and ships PRs — with zero human intervention required.

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

- `GITHUB_OAUTH_CLIENT_ID` / `GITHUB_OAUTH_CLIENT_SECRET` — [create an OAuth app](https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/creating-an-oauth-app)
- `GITHUB_APP_ID` / `GITHUB_APP_PRIVATE_KEY` / `GITHUB_WEBHOOK_SECRET` — [create a GitHub App](https://docs.github.com/en/apps/creating-github-apps)

See `.env.example` for the full list of variables.

### Secrets & API Keys

For local dev, just copy `.env.example` into `.env` and edit it directly — it's gitignored. For shared/deployed environments, we use [SOPS](https://github.com/getsecrets/sops) + [age](https://github.com/FiloSottile/age) to encrypt secrets into `.env.enc` (safe to commit). See the [secrets management guide](docs/secrets/README.md) for setup instructions.

```bash
make secrets-setup     # one-time: generate age keypair
make secrets-encrypt   # encrypt .env → .env.enc
make secrets-decrypt   # decrypt .env.enc → .env
```

## How it works

```
issues in → prioritize → estimate complexity → run agents → validate → ship PR → measure impact
                                                                                       ↓
                                                                              learn from outcomes
```

1. **Ingest** — pull issues from Sentry, Linear, support tickets via webhooks
2. **Prioritize** — score by customer count, severity, revenue risk
3. **Estimate** — LLM pre-analysis to classify complexity before burning compute
4. **Execute** — run coding agents in isolated Docker containers
5. **Validate** — direction/correctness/quality checks + CI + regression tests
6. **Ship** — open a GitHub PR with full context for human review
7. **Observe** — measure post-deploy impact on error rates and support volume
8. **Learn** — extract review feedback into conventions, feed back into future runs

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

Most bugs don't need a sprint planning meeting. They need someone (or something) to isolate the issue, write the fix, validate it, and open the PR. 143 is that something.

## Contributing

Read through the [design docs](docs/design/overall.md) to understand the architecture, then pick an issue and open a PR.

## License

[MIT](LICENSE)

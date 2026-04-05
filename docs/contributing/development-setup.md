# Development Setup

This guide covers everything you need to build and run 143 locally.

For the full GitHub App configuration walkthrough, see the [local development guide](../local-development.md).

## Prerequisites

- Go 1.24+
- Node.js 18+
- PostgreSQL 17

The setup script installs anything missing via Homebrew (macOS) or apt (Linux).

## Quick Start

```bash
git clone https://github.com/assembledhq/143.git && cd 143 && ./setup.sh
```

This creates the database, copies `.env.example` to `.env`, installs dependencies, and runs migrations.

## Running

**Option A — with ngrok tunnel** (recommended for GitHub OAuth / webhook testing):

```bash
make dev-ngrok NGROK_DOMAIN=yourname.ngrok.dev
```

**Option B — with Docker Compose**:

```bash
make dev              # starts Postgres, API server, and frontend
```

**Option C — without Docker** (two terminals):

```bash
make server-dev       # Go API on localhost:8080
make frontend-dev     # Next.js on localhost:3000
```

The frontend proxies `/api/*` to the Go server automatically.

## Make Commands

### Development

| Command | Description |
|---------|-------------|
| `make setup` | Run setup.sh (install deps, create DB, run migrations) |
| `make dev` | Start everything via Docker Compose |
| `make dev-ngrok NGROK_DOMAIN=...` | Start with ngrok tunnel for webhook testing |
| `make server-dev` | Go API server only (localhost:8080) |
| `make frontend-dev` | Next.js frontend only (localhost:3000) |

### Build & Test

| Command | Description |
|---------|-------------|
| `make build` | Compile server and migrate binaries to `bin/` |
| `make test` | Run all tests |
| `make test-race` | Run tests with Go race detector |
| `make test-coverage` | Generate `coverage.html` |
| `make lint` | Run golangci-lint |
| `make frontend-lint` | Run frontend linter |
| `make frontend-typecheck` | Run TypeScript type checking |
| `make frontend-check` | Typecheck + lint + build (all frontend checks) |

### Database

| Command | Description |
|---------|-------------|
| `make migrate-up` | Apply pending migrations |
| `make migrate-down` | Roll back last migration |

### Secrets Management

143 uses [SOPS](https://github.com/getsops/sops) + [age](https://github.com/FiloSottile/age) to encrypt secrets into `.env.enc` (safe to commit). Production secrets are stored in `.env.production.enc`.

| Command | Description |
|---------|-------------|
| `make secrets-setup` | One-time: generate age keypair |
| `make secrets-encrypt` | Encrypt `.env` → `.env.enc` |
| `make secrets-decrypt` | Decrypt `.env.enc` → `.env` |
| `make secrets-encrypt ENV=production` | Encrypt production secrets |
| `make secrets-decrypt ENV=production` | Decrypt production secrets |
| `make secrets-edit` | Edit encrypted `.env.enc` in-place |
| `make secrets-rotate` | Re-encrypt after adding a team member's key |

After running `make secrets-setup`, add this to your shell profile (`~/.bash_profile` or `~/.zshrc`):

```bash
export SOPS_AGE_KEY_FILE="$HOME/.config/sops/age/keys.txt"
```

See the [secrets management guide](../secrets/README.md) for the full walkthrough including production secrets.

## Environment

All config lives in `.env` (created by setup). The defaults work out of the box for local dev.

To enable GitHub OAuth login and repo onboarding, set these in `.env`:

- `GITHUB_OAUTH_CLIENT_ID` / `GITHUB_OAUTH_CLIENT_SECRET` — create an OAuth app
- `GITHUB_APP_ID` / `GITHUB_APP_PRIVATE_KEY` / `GITHUB_WEBHOOK_SECRET` — create a GitHub App

See the [local development guide](../local-development.md) for step-by-step setup including webhook tunneling, and `.env.example` for the full list of variables.

## Project Structure

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

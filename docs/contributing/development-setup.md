# Development Setup

This guide covers everything you need to build and run 143 locally.

For the full GitHub App configuration walkthrough, see the [local development guide](../local-development.md).

## Prerequisites

- Go 1.24+
- Node.js 24+
- PostgreSQL 17

The setup script installs anything missing via Homebrew (macOS) or apt/NodeSource (Linux).

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

143 uses [SOPS](https://github.com/getsops/sops) + [age](https://github.com/FiloSottile/age) to encrypt secrets into `.env*.enc` bundles. The bundles and `.sops.yaml` live in a **private** sibling repo (`SECRETS_DIR`, default `../143-infra`) — never in this public repo. See the [secrets management guide](../secrets/README.md) for the layout and bootstrap steps.

| Command | Description |
|---------|-------------|
| `make secrets-setup` | One-time: generate age keypair |
| `make secrets-encrypt` | Encrypt `.env` → `$SECRETS_DIR/.env.enc` |
| `make secrets-decrypt` | Decrypt `$SECRETS_DIR/.env.enc` → `.env` |
| `make secrets-encrypt ENV=production` | Encrypt production secrets |
| `make secrets-decrypt ENV=production` | Decrypt production secrets |
| `make secrets-edit` | Edit encrypted `.env.enc` in-place |
| `make secrets-rotate` | Re-encrypt after adding a team member's key |

### Deployment

| Command | Description |
|---------|-------------|
| `make deploy` | Deploy all fleet nodes (alias for `deploy-fleet`) |
| `make deploy-app` | Deploy app node(s) |
| `make deploy-worker` | Deploy worker node(s) |
| `make deploy-db` | Deploy database node(s) |
| `make deploy-logging` | Deploy logging node(s) |
| `make sync-keys` | Dry-run: show what keys would change on all servers |
| `make sync-keys APPLY=true` | Push SSH public keys from `deploy/authorized_keys/` to all servers |
| `make logs` | Open Grafana via SSH tunnel on localhost:9999 |

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

## Deployment

To deploy to production servers, you need an SSH key that the servers trust.

### SSH Key Setup

1. Generate a dedicated deploy key (if you don't have one):

```bash
ssh-keygen -t ed25519 -f ~/.ssh/143-deploy -C "your-email@example.com"
```

2. Add your public key to the repo:

```bash
cp ~/.ssh/143-deploy.pub deploy/authorized_keys/yourname.pub
```

3. Open a PR with your key and get it reviewed:

```bash
git add deploy/authorized_keys/yourname.pub
git commit -m "Add yourname deploy key"
# push and open a PR as usual
```

4. Once the PR is merged, someone with existing server access runs:

```bash
make sync-keys            # dry run — review the diff
make sync-keys APPLY=true    # push changes to all servers
```

This replaces `authorized_keys` on every fleet server with exactly the keys in the repo. You'll have deploy access after the apply step completes.

### Deploying

The Makefile auto-detects your SSH key from `~/.ssh/143-deploy`. If your key is at a different path, pass `SSH_KEY=<path>` explicitly.

```bash
# Deploy everything (auto-detects SSH key)
make deploy

# Deploy a single role
make deploy-app
make deploy-worker

# Override SSH key if yours is named differently
make deploy SSH_KEY=~/.ssh/my-other-key
```

### Provisioning New Servers

```bash
make provision-app    HOST=<ip>
make provision-worker HOST=<ip>
make provision-db     HOST=<ip>
```

### Viewing Logs

```bash
make logs    # opens Grafana via SSH tunnel on localhost:9999
```

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

# Local Development Guide

This guide gets you from a fresh clone to a fully working 143 instance with GitHub login and webhook support.

## Quick Start

```bash
git clone https://github.com/assembledhq/143.git && cd 143 && ./setup.sh
```

This installs prerequisites (Go, Node.js, PostgreSQL), creates the database, copies `.env.example` to `.env`, installs dependencies, and runs migrations.

Then start the app:

```bash
# Option A: Docker Compose (starts Postgres, API, frontend)
make dev

# Option B: without Docker (two terminals)
make server-dev       # Go API on localhost:8080
make frontend-dev     # Next.js on localhost:3000
```

The frontend proxies `/api/*` to the Go server automatically.

At this point you have a running app, but GitHub login and webhooks won't work until you configure the GitHub apps below.

## What Works Without GitHub Configured

The server starts and the frontend loads even without GitHub credentials. At startup you'll see:

```
feature status  configured=false  feature="GitHub OAuth"  enables=login
feature status  configured=false  feature="GitHub App"    enables="webhooks, PRs"
```

Without GitHub configured, you can still:
- View the frontend UI
- Hit API endpoints that don't require auth
- Run tests (`make test`)
- Work on non-GitHub features

To enable login and repo integration, continue below.

## Setting Up GitHub Apps for Local Dev

You need two GitHub apps: an **OAuth App** (for login) and a **GitHub App** (for repo access/webhooks). Each developer creates their own.

### Step 1: Start a Webhook Tunnel

GitHub needs to reach your local machine for OAuth callbacks and webhooks. Use ngrok or cloudflared:

```bash
# ngrok (https://ngrok.com)
ngrok http 8080
# Note the URL, e.g. https://abc123.ngrok.io

# OR cloudflared (https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/)
cloudflared tunnel --url http://localhost:8080
```

Your tunnel URL is your `{BASE_URL}` for the steps below. You'll need to update it whenever the tunnel restarts (ngrok free tier gives you a new URL each time).

### Step 2: Create a GitHub OAuth App

This handles "Sign in with GitHub".

1. Go to **[GitHub > Settings > Developer settings > OAuth Apps > New OAuth App](https://github.com/settings/applications/new)**
2. Fill in:

| Field | Value |
|-------|-------|
| Application name | `143-dev-yourname` |
| Homepage URL | `{BASE_URL}` (e.g. `https://abc123.ngrok.io`) |
| Authorization callback URL | `{BASE_URL}/api/v1/auth/github/callback` |

3. Click **Register application**
4. Copy the **Client ID**
5. Click **Generate a new client secret** and copy it immediately

### Step 3: Create a GitHub App

This handles repo access, PR creation, and webhooks. See [self-hosting/github-app-setup.md](self-hosting/github-app-setup.md) for the full walkthrough with permissions and events. The short version:

1. Go to **[GitHub > Settings > Developer settings > GitHub Apps > New GitHub App](https://github.com/settings/apps/new)**
2. Fill in:

| Field | Value |
|-------|-------|
| GitHub App name | `143-dev-yourname` (must be globally unique) |
| Homepage URL | `{BASE_URL}` |
| Setup URL | `{BASE_URL}/settings/integrations/github/setup` |
| Webhook URL | `{BASE_URL}/api/v1/webhooks/github` |
| Webhook secret | Generate with `openssl rand -hex 32` |

3. Set permissions and events per [self-hosting/github-app-setup.md](self-hosting/github-app-setup.md)
4. Under "Where can this app be installed?" select **Only on this account**
5. Click **Create GitHub App**
6. Note the **App ID**
7. Scroll to **Private keys** → **Generate a private key** (downloads a `.pem` file)
8. Go to **Install App** in the sidebar → install on your account, selecting your test repos

### Step 4: Configure `.env.local`

Create `.env.local` with your credentials (it's gitignored and overrides `.env`):

```env
BASE_URL=https://abc123.ngrok.io
FRONTEND_URL=http://localhost:3000

# GitHub OAuth (from step 2)
GITHUB_OAUTH_CLIENT_ID=Iv1.abc123
GITHUB_OAUTH_CLIENT_SECRET=your_secret

# GitHub App (from step 3)
GITHUB_APP_ID=123456
GITHUB_APP_PRIVATE_KEY="-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBA...\n-----END RSA PRIVATE KEY-----"
GITHUB_WEBHOOK_SECRET=your_webhook_secret
```

**Private key formatting**: Replace each newline in the `.pem` file with `\n` and wrap the whole thing in quotes.

Restart the server. You should see:

```
feature status  configured=true  feature="GitHub OAuth"  enables=login
feature status  configured=true  feature="GitHub App"    enables="webhooks, PRs"
```

### When Your Tunnel URL Changes

If you're on ngrok free tier, you get a new URL each restart. When that happens:

1. Update `BASE_URL` in `.env.local`
2. Update the **Authorization callback URL** in your OAuth App settings on GitHub
3. Update the **Webhook URL** in your GitHub App settings on GitHub
4. Restart the server

Tip: ngrok paid plans give you a stable subdomain. Cloudflared tunnels with named tunnels also provide stable URLs.

## Common Commands

```bash
make dev              # start everything (Docker Compose)
make server-dev       # Go API only (no Docker)
make frontend-dev     # Next.js frontend only

make test             # run all Go tests
make test-race        # run tests with race detector
make test-coverage    # generate coverage.html

make migrate-up       # apply pending migrations
make migrate-down     # roll back last migration

make lint             # run golangci-lint
make frontend-check   # typecheck + lint + build frontend

make build            # build Go binaries to bin/
```

## Environment Variables

All config is in `.env` (shared defaults) and `.env.local` (personal overrides). Both are gitignored.

`.env.local` takes precedence over `.env`. Real environment variables take precedence over both.

See [.env.example](../.env.example) for the full list with comments. The server logs which features are configured at startup so you can immediately see what's working.

### Encrypted secrets (SOPS + age)

If you're setting up secrets for the first time or on a new machine:

```bash
make secrets-setup        # generates your age keypair (one-time)

# Add this to your ~/.bash_profile (or ~/.zshrc):
export SOPS_AGE_KEY_FILE="$HOME/.config/sops/age/keys.txt"
source ~/.bash_profile

# If your team keeps encrypted secrets in a private sibling repo
# (default ../143-infra), clone it next to this one and decrypt:
git clone git@github.com:<your-org>/143-infra.git ../143-infra
make secrets-decrypt
```

Encrypted bundles never live in this (public) repo — see the [secrets management guide](secrets/README.md) for the layout, the full walkthrough, production secrets, and adding team members.

## Testing Webhooks

Once your GitHub App is installed and the tunnel is running:

1. Make a change in a test repo that triggers an event (open a PR, push a commit, etc.)
2. Check delivery status: **GitHub App settings > Advanced > Recent Deliveries**
3. You can **Redeliver** any event from that page for debugging
4. Server logs show webhook processing in real time

## Troubleshooting

| Problem | Fix |
|---------|-----|
| `configured=false` for GitHub OAuth | Check that both `GITHUB_OAUTH_CLIENT_ID` and `GITHUB_OAUTH_CLIENT_SECRET` are set in `.env.local` |
| `configured=false` for GitHub App | Check that `GITHUB_APP_ID` is a number and `GITHUB_APP_PRIVATE_KEY` is set |
| OAuth login redirects to wrong URL | Update `BASE_URL` in `.env.local` to match your current tunnel URL |
| Webhook returns 401 | `GITHUB_WEBHOOK_SECRET` doesn't match what's in your GitHub App settings |
| "Resource not accessible by integration" | App is missing a permission — check [self-hosting/github-app-setup.md](self-hosting/github-app-setup.md) |
| Private key parse error | Make sure the full PEM is included with `-----BEGIN/END RSA PRIVATE KEY-----` |
| Port 3000 already in use | `FRONTEND_PORT=3001 make dev` or kill the process on 3000 |
| Database connection refused | Check PostgreSQL is running: `brew services list` (macOS) or `systemctl status postgresql` (Linux) |

## Project Layout

```
cmd/
  server/         # API server entrypoint
  migrate/        # database migration runner
internal/
  api/
    handlers/     # HTTP handlers
    middleware/   # auth, CORS, logging
    router.go     # route registration
  config/         # env-based configuration
  db/             # data access layer
  models/         # domain types
  services/
    github/       # GitHub App JWT + token management
  worker/         # background job processor
migrations/       # SQL migration files (applied in order)
frontend/         # Next.js app
docs/design/      # numbered design docs
```

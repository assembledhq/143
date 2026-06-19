# Production Deployment Checklist (Current Repo)

This checklist is intentionally minimal and aligned to what is implemented in this repository today.

## 1. Decide Deployment Shape

- [ ] Deploy **backend** (`cmd/server`) and **frontend** (`frontend/`) as two services
- [ ] Put them behind one domain (recommended):
  - frontend serves `/`
  - backend serves `/api/*`, `/healthz`, `/readyz`, `/metrics`
- [ ] Enable TLS at the edge (load balancer/reverse proxy)

Notes:
- The Go server does not currently bundle/serve the Next.js build directly.
- `MODE=all` is the default single-node mode.

## 2. Provision Core Infra

- [ ] PostgreSQL 15+ (managed preferred)
- [ ] App runtime for backend container
- [ ] App runtime for frontend (`next build` + `next start`, or managed Next.js host)
- [ ] Automated database backups
- [ ] Backup restore test completed

## 3. Configure Required Environment Variables

### Backend: always required

- [ ] `DATABASE_URL` (production TLS settings from your provider)
- [ ] `BASE_URL` (public backend URL, used in OAuth callback construction)
- [ ] `FRONTEND_URL` (public app URL, used for post-login redirect)
- [ ] `CORS_ALLOWED_ORIGINS` (comma-separated allowed origins, usually frontend URL)
- [ ] `SESSION_SECRET` (set a strong value now; currently reserved for session/security hardening)
- [ ] `PORT` (usually `8080`)
- [ ] `MODE` (`all` for single-node)

### Backend: strongly recommended for production

- [ ] `ENCRYPTION_MASTER_KEY` (required if you want encrypted credentials at rest)
- [ ] Worker capacity knobs (for `MODE=worker` or mixed `MODE=all` nodes):
  - `WORKER_PROCESS_COUNT` (default `1`) — how many in-process worker loops run on this node
  - For fleet deploys, put worker sizing env vars in `.env.production.enc` like other deploy env vars (the bundle lives in your private secrets checkout — see [docs/secrets/README.md](../secrets/README.md)).
  - For mixed worker sizes, set `WORKER_BUCKET_MAP=hcloud-cpx21:10.0.0.4,hcloud-cpx31:10.0.0.5,hcloud-ccx23:10.0.0.6` (supports CPX shared + CCX dedicated families), or set `WORKER_PROCESS_COUNT` directly per worker.
  - The codebase still has internal sandbox CPU/memory/disk defaults, but those are not part of the documented self-hosting env surface in this checklist.
  - See [worker-capacity-tuning.md](worker-capacity-tuning.md) for sizing guidance by server size.

### GitHub auth/integration (required for login + GitHub features)

- [ ] `GITHUB_OAUTH_CLIENT_ID`
- [ ] `GITHUB_OAUTH_CLIENT_SECRET`
- [ ] `GITHUB_APP_ID`
- [ ] `GITHUB_APP_PRIVATE_KEY`
- [ ] `GITHUB_WEBHOOK_SECRET`

### LLM (required for LLM-dependent checks/features)

- [ ] `LLM_MODEL` (model used for agent sessions)
- [ ] At least one provider key:
  - `ANTHROPIC_API_KEY`, or
  - `OPENAI_API_KEY`, or
  - `OPENROUTER_API_KEY`
- [ ] `PLATFORM_LLM_MODEL` (small model used for background features — defaults to `gpt-5.4-nano`; see [platform-llm.md](platform-llm.md))

Optional LLM routing vars:
- `OPENAI_API_TYPE`, `ANTHROPIC_BASE_URL`, `OPENAI_BASE_URL`, `OPENROUTER_BASE_URL`, `OPENROUTER_APP_NAME`, `OPENROUTER_SITE_URL`

## 4. Create GitHub Apps

- [ ] Create OAuth App:
  - Homepage: `https://<your-domain>`
  - Callback: `https://<your-domain>/api/v1/auth/github/callback`
- [ ] Create GitHub App:
  - Setup URL: `https://<your-domain>/settings/integrations/github/setup`
  - Webhook URL: `https://<your-domain>/api/v1/webhooks/github`
  - Permissions/events: see [github-app-setup.md](github-app-setup.md)
- [ ] Install GitHub App on a test org/repo

## 5. Build and Deploy

- [ ] Build backend image:
  - `docker build -t 143-server .`
- [ ] Run database migrations before startup:
  - local/CI: `go run cmd/migrate/main.go up`
  - containerized: run `/bin/migrate up` in an image/container with `DATABASE_URL` set
- [ ] Deploy backend container with env vars from section 3
- [ ] Build and deploy frontend with `API_PROXY_TARGET` pointing to backend base URL (if using rewrites in `frontend/next.config.ts`)

## 6. Smoke Test (Must Pass)

- [ ] `GET /healthz` returns `200`
- [ ] `GET /readyz` returns `200`
- [ ] `GET /metrics` returns Prometheus metrics
- [ ] GitHub OAuth login works end-to-end
- [ ] GitHub webhook delivery succeeds (GitHub App -> Recent Deliveries)
- [ ] Basic authenticated API flow works (`/api/v1/issues`, `/api/v1/runs`)

## 7. Staging (Recommended)

- [ ] Separate staging domain (example: `staging.<domain>`)
- [ ] Separate staging database
- [ ] Separate OAuth App + GitHub App
- [ ] Same build artifacts as prod, different env vars/secrets

## 8. Current Repo Reality Checks

These are common points of confusion:

- `readyz` currently verifies database connectivity only.
- Documented worker sizing knobs are loaded at process startup from env. Changing them requires a container/process restart.
- There is no `Dockerfile.sandbox` in this repo today.
- Observability env vars like `MEZMO_*` and `DD_*` are documented in design docs, but are not currently loaded by `internal/config/config.go`.

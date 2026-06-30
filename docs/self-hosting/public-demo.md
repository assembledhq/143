# Public Demo Deployment

This guide runs the read-only public demo on one separate Linux host. It is not the production fleet and does not use production secrets.

The demo stack includes:

- Caddy
- frontend
- API in `MODE=api`
- local Postgres
- local Redis
- migration job
- demo seed job

It intentionally does not run workers, session executors, Chrome, sandbox networking, a Docker socket mount, GitHub App credentials, or LLM keys.

## Host Model

Use a small dedicated VM for `demo.143.dev`. Start with 2 vCPU / 4 GB RAM so Postgres, Next.js, and the Go API have headroom.

Point DNS at the host:

```text
demo.143.dev A <demo-host-ip>
```

Caddy uses normal HTTP-01 TLS for this domain. No wildcard preview DNS and no Cloudflare DNS token are needed.

## Provision

Provisioning is intentionally separate from production and does not read `.env.production.enc`.

```bash
make provision-demo HOST=<demo-host-ip>
```

This installs Docker, creates `/opt/143-demo`, copies the demo compose/Caddy config, and generates `/opt/143-demo/.env.demo` with local random secrets:

- `DB_PASSWORD`
- `SESSION_SECRET`
- `CSRF_SIGNING_KEY`

The generated env file is chmodded `600` and stays on the demo host.

## Deploy

Deploy the same GHCR server/frontend images used by production:

```bash
make deploy-demo HOST=<demo-host-ip> TAG=<image-sha>
```

The deploy target:

1. Syncs `docker-compose.demo.yml` and `deploy/Caddyfile.demo`.
2. Updates `IMAGE_TAG` in `/opt/143-demo/.env.demo`.
3. Pulls images.
4. Starts local Postgres and Redis.
5. Runs migrations.
6. Applies `.143/seed` through `/bin/demo-seed`.
7. Starts API, frontend, and Caddy.
8. Runs `public-demo-smoke`.

## Reset And Logs

Re-apply seed and prune volatile state:

```bash
make demo-reset HOST=<demo-host-ip>
```

Tail demo logs:

```bash
make demo-logs HOST=<demo-host-ip>
```

Run smoke checks against the configured host URL:

```bash
make public-demo-smoke HOST=<demo-host-ip>
```

Run smoke checks against an explicit URL:

```bash
make public-demo-smoke DEMO_URL=https://demo.143.dev
```

## Runtime Behavior

The demo uses:

```env
DEMO_MODE=true
DEMO_READ_ONLY=true
DEMO_ENTRY_EMAIL=preview-viewer@143.dev
```

Visitors click **Enter demo**. The app calls `POST /api/v1/auth/demo`, signs in the seeded viewer, and redirects to `/demo`.

`DEMO_READ_ONLY=true` blocks state-changing API routes except demo entry and logout. Seeded rows are fixed; the only expected runtime churn is auth sessions, which the host prunes nightly.

For an emergency full reset:

```bash
ssh deploy@<demo-host-ip>
cd /opt/143-demo
docker compose --env-file .env.demo -f docker-compose.demo.yml down -v
docker compose --env-file .env.demo -f docker-compose.demo.yml up -d
```

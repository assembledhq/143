# Single-Node Deployment

This is the simplest production-shaped self-hosting path: one Linux box runs Caddy, frontend, API, worker loops, Postgres, Redis, Chrome, sandbox DNS, and Docker sandboxes.

Use it for a small team, an evaluation instance, or a cost-conscious self-hosted install. It is not high availability: if the box dies, 143 is down until the host and backups are restored.

## What Runs On The Box

- `caddy`: public TLS reverse proxy for the app and wildcard preview subdomains.
- `frontend`: Next.js production server.
- `api`: Go API and worker loops in `MODE=all`.
- `migrate`: one-shot database migration job.
- `postgres`: local Postgres source of truth.
- `redis`: local cache/pub-sub for live updates and worker wakeups.
- `chrome`: headless Chrome for preview inspection.
- `sandbox-dns`: DNS sidecar for gVisor sandboxes on the `143-sandbox` bridge.
- `gvisor-check`: startup preflight for the `runsc` runtime.

Durable app data is stored under `SINGLE_NODE_DATA_DIR` (`/var/lib/143` by default) and bind-mounted into both the API/worker container and durable session-executor containers. `docker-compose.single-node.yml` derives the default session-executor bind from `SINGLE_NODE_DATA_DIR`; if you override `SESSION_EXECUTOR_EXTRA_BINDS` directly, keep the data root included.

## Requirements

- Linux host with Docker and Docker Compose v2.
- Public DNS for `DOMAIN` and `*.preview.DOMAIN` pointing at the box.
- Access to the runtime images in `IMAGE_REGISTRY` (`ghcr.io/assembledhq` by default). If those packages are private for your deployment, run `docker login ghcr.io` on the host or mirror the images and set `IMAGE_REGISTRY`.
- Cloudflare DNS API token for the bundled Caddy wildcard certificate flow. If you do not use Cloudflare, replace `deploy/Caddyfile`/`Dockerfile.caddy` or put your own proxy in front.
- gVisor `runsc` installed and registered with Docker for production isolation.
- Backups for the Postgres Docker volume and `SINGLE_NODE_DATA_DIR`.

## Setup

1. Prepare the env file:

   ```bash
   cp .env.single-node.example .env.single-node
   $EDITOR .env.single-node
   ```

   Set `IMAGE_TAG` to the release you want to run. Leave `IMAGE_REGISTRY=ghcr.io/assembledhq` unless you mirror the images or are testing local builds.

2. Prepare host networks, sandbox resolver files, firewall rules, and writable data directories:

   ```bash
   sudo deploy/scripts/prepare-single-node.sh
   ```

   The script reads `SINGLE_NODE_DATA_DIR` from `.env.single-node`. Copy the printed `DOCKER_GID` value into `.env.single-node`.

3. Start the stack:

   ```bash
   make single-node-up
   ```

4. Check service health:

   ```bash
   docker compose --env-file .env.single-node -f docker-compose.single-node.yml ps
   docker compose --env-file .env.single-node -f docker-compose.single-node.yml logs api
   ```

5. Configure GitHub:

   - OAuth callback: `https://<domain>/api/v1/auth/github/callback`
   - GitHub App setup URL: `https://<domain>/settings/integrations/github/setup`
   - Webhook URL: `https://<domain>/api/v1/webhooks/github`
   - Preview DNS: `*.preview.<domain>` points at the same box.

## Capacity Defaults

The template starts conservatively:

- `WORKER_PROCESS_COUNT=1`
- `WORKER_MAX_ACTIVE_SANDBOXES=1`
- `SANDBOX_MEMORY_LIMIT_MB=3072`
- `SANDBOX_DISK_LIMIT_GB=10`

Raise worker counts only after watching CPU, memory, Docker disk usage, and run duration. For anything beyond a few concurrent sandboxes, use separate worker hosts instead of turning one box into an overloaded mixed control-plane/runtime node.

## Known Deficiencies And Tradeoffs

- The bundled Caddyfile assumes Cloudflare for wildcard preview certificates.
- Single-node local storage is durable only as long as the host and backups are healthy. Multi-node deployments should use S3-compatible snapshot/upload storage.
- `SANDBOX_REQUIRE_DISK_QUOTA` defaults to `false` in the single-node compose for broader Docker storage-driver compatibility. Set it to `true` when the host storage driver supports Docker `StorageOpt` size limits.
- Postgres, Redis, API, workers, and sandboxes compete for the same CPU, memory, disk, and network. Keep worker concurrency low.
- Blue/green worker rollouts and multi-node recovery are fleet features; a single-node restart drains or interrupts local capacity.
- Static egress is optional and still requires the WireGuard/static-egress host setup used by worker nodes.

## Backups

At minimum, back up:

- the `pgdata` Docker volume,
- `SINGLE_NODE_DATA_DIR` (`/var/lib/143` by default),
- `.env.single-node`,
- Caddy data if you want to preserve issued certificates (`caddy_data` volume).

Test restoring onto a fresh box before allowing real repositories or automations to depend on the instance.

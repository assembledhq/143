# Design Doc 36: Self-Hosted Docker Infrastructure

> **Status:** Proposed | **Last reviewed:** 2026-04-04

## Context

143.dev currently runs on **Render** (Go API + Next.js frontend + Render Managed
Postgres). Render does not support Docker-in-Docker or privileged containers, so
agent sandboxes cannot run there. We need infrastructure where we control the
Docker daemon.

This document describes how to migrate off Render to a self-hosted VPS and then
scale incrementally. It is organized into four phases, each with a clear list of
what changes in our code, what infrastructure to provision, and what is
provider-specific.

### Design Principles

1. **Cloud-agnostic** — everything runs on any Linux VPS with Docker. No
   proprietary cloud services required. Provider-specific details are called out
   in `[PROVIDER-SPECIFIC]` blocks so you can swap them.
2. **Docker Compose everywhere** — the same orchestration tool from dev laptop to
   production cluster. No Kubernetes, no Nomad, no proprietary container
   services.
3. **Postgres is the only state** — the server, frontend, and sandboxes are
   stateless. If anything other than Postgres dies, restart it.
4. **Incremental complexity** — each phase is independently valuable. Don't skip
   ahead. Move to the next phase only when you hit the limits of the current one.
5. **Standard tooling** — Caddy (TLS), gVisor (sandbox isolation), cloud-init
   (node provisioning), S3-compatible storage (backups). All open source, all
   work on every provider.

### What Exists Today (on Render)

| Component | Render Service | Notes |
|---|---|---|
| Go API | Render Docker service | Runs API + worker + scheduler in `mode=all` |
| Next.js frontend | Render Node service | Served separately |
| PostgreSQL | Render Managed DB | Automated backups, managed TLS |
| TLS | Render auto-TLS | Automatic cert management |
| DNS | External (Cloudflare) | Points to Render |
| CI/CD | `git push` → Render auto-builds | Zero-config deploys |
| Agent sandboxes | **Cannot run** | No Docker socket access |

### What Exists in Our Codebase Today

| Artifact | Path | Notes |
|---|---|---|
| Server Dockerfile | `Dockerfile` | Multi-stage Go build, runs as non-root, includes `sops`/`age` |
| Dev docker-compose | `docker-compose.yml` | Postgres + server (with `air` live reload) + frontend |
| CI pipeline | `.github/workflows/ci.yml` | Lint, test, build, security scan, Docker image build |
| Docker sandbox provider | `internal/services/agent/providers/docker.go` | Uses Docker API, supports gVisor (`runsc`) runtime |
| Environment config | `.env.example` | All env vars documented |
| Sandbox Dockerfile | **Does not exist** | Needs to be created |
| Production compose | **Does not exist** | Needs to be created |
| Deploy workflow | **Does not exist** | Needs to be created |
| Caddy config | **Does not exist** | Needs to be created |
| Backup scripts | **Does not exist** | Needs to be created |

---

## Phase 1: Single-Node Production (Migrate Off Render)

**Goal:** Everything on one VPS. Agent sandboxes work. Zero application code
changes.

**When to do this:** Now. This is the prerequisite for running agent sandboxes.

### What This Looks Like

```
┌──────────────────────────────────────────────────────────────────────┐
│  VPS (8 vCPU, 16GB RAM, 160GB SSD)                                   │
│                                                                      │
│  ┌────────────────────────────────────────────────────────────────┐  │
│  │  Docker Compose                                                │  │
│  │                                                                │  │
│  │  ┌──────────┐  ┌─────────┐  ┌──────────┐  ┌──────────────┐   │  │
│  │  │ Caddy    │  │ Go API  │  │ Next.js  │  │  Postgres 17 │   │  │
│  │  │ :443     │─▶│ :8080   │  │ :3000    │  │  :5432       │   │  │
│  │  │ (TLS)    │  │         │  │          │  │              │   │  │
│  │  └──────────┘  └────┬────┘  └──────────┘  └──────────────┘   │  │
│  │                     │                                          │  │
│  │           ┌─────────▼──────────┐                               │  │
│  │           │  Docker Daemon      │                               │  │
│  │           │  ┌───────┐┌───────┐│                               │  │
│  │           │  │ Sbox 1││ Sbox 2││ ...                           │  │
│  │           │  └───────┘└───────┘│                               │  │
│  │           └────────────────────┘                               │  │
│  └────────────────────────────────────────────────────────────────┘  │
└──────────────────────────────────────────────────────────────────────┘
```

### Infrastructure to Provision

**VPS requirements (provider-agnostic):**

| Resource | Minimum | Recommended | Why |
|----------|---------|-------------|-----|
| vCPUs | 4 | 8 | Sandboxes get 2 CPUs each; need headroom for API + Postgres |
| RAM | 8 GB | 16 GB | Sandboxes get 4 GB each; Postgres wants ~2 GB `shared_buffers` |
| Disk | 80 GB SSD | 160 GB SSD | Postgres data + Docker images + sandbox workspace volumes |
| OS | Ubuntu 24.04 | Ubuntu 24.04 | gVisor packages target Debian/Ubuntu |

> **`[PROVIDER-SPECIFIC]` VPS sizing:**
>
> | Provider | Instance Type | Monthly Cost | Notes |
> |----------|--------------|-------------|-------|
> | Hetzner | CX42 (8 vCPU / 16 GB) | ~€14 (~$16) | Best price/performance for EU |
> | AWS | t3.xlarge (4 vCPU / 16 GB) | ~$120 | Use spot instances for workers to reduce cost |
> | GCP | e2-standard-4 (4 vCPU / 16 GB) | ~$100 | Sustained use discount applies automatically |
> | DigitalOcean | s-8vcpu-16gb | ~$96 | Simple, good for small teams |

**On the VPS, install:**

```bash
# Docker
curl -fsSL https://get.docker.com | sh

# gVisor (sandbox isolation)
curl -fsSL https://gvisor.dev/archive.key | gpg --dearmor -o /usr/share/keyrings/gvisor-archive-keyring.gpg
echo "deb [arch=amd64 signed-by=/usr/share/keyrings/gvisor-archive-keyring.gpg] https://storage.googleapis.com/gvisor/releases release main" \
  > /etc/apt/sources.list.d/gvisor.list
apt-get update && apt-get install -y runsc
runsc install
systemctl restart docker
```

These commands work on any Ubuntu VPS regardless of provider.

### New Files to Create in the Repo

#### 1. `docker-compose.prod.yml`

This is the single-node production stack. It replaces the dev `docker-compose.yml`
(which uses `air` live reload and mounts source code).

```yaml
services:
  caddy:
    image: caddy:2-alpine
    ports:
      - "80:80"
      - "443:443"
    volumes:
      - ./deploy/Caddyfile:/etc/caddy/Caddyfile:ro
      - caddy_data:/data
    restart: unless-stopped

  api:
    image: ghcr.io/assembledhq/143-server:latest
    environment:
      DATABASE_URL: postgres://onefortythree:${DB_PASSWORD}@postgres:5432/onefortythree?sslmode=disable
      PORT: "8080"
      MODE: all
      NODE_ID: ${HOSTNAME:-node-1}
      BASE_URL: ${BASE_URL:-https://143.dev}
      FRONTEND_URL: ${FRONTEND_URL:-https://143.dev}
    env_file:
      - .env
    depends_on:
      postgres:
        condition: service_healthy
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
    restart: unless-stopped
    deploy:
      resources:
        limits:
          memory: 2G
          cpus: "4.0"

  frontend:
    image: ghcr.io/assembledhq/143-frontend:latest
    environment:
      API_PROXY_TARGET: http://api:8080
      NODE_ENV: production
    restart: unless-stopped
    deploy:
      resources:
        limits:
          memory: 1G
          cpus: "2.0"

  postgres:
    image: postgres:17
    environment:
      POSTGRES_USER: onefortythree
      POSTGRES_PASSWORD: ${DB_PASSWORD}
      POSTGRES_DB: onefortythree
      POSTGRES_INITDB_ARGS: "--data-checksums"
    volumes:
      - pgdata:/var/lib/postgresql/data
      - ./deploy/postgres/postgresql.conf:/etc/postgresql/conf.d/custom.conf:ro
    command: postgres -c config_file=/etc/postgresql/conf.d/custom.conf
    ports:
      - "127.0.0.1:5432:5432"   # localhost only — no external access
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U onefortythree"]
      interval: 10s
      timeout: 5s
      retries: 5
    restart: unless-stopped
    deploy:
      resources:
        limits:
          memory: 2G
          cpus: "2.0"

volumes:
  pgdata:
  caddy_data:
```

#### 2. `deploy/Caddyfile`

```
{$BASE_URL:143.dev} {
    handle /api/* {
        reverse_proxy api:8080
    }
    handle {
        reverse_proxy frontend:3000
    }
}
```

Caddy automatically provisions TLS via Let's Encrypt. No cert management needed.

#### 3. `deploy/postgres/postgresql.conf`

Production-tuned Postgres configuration. See the [PostgreSQL Configuration](#production-postgres-configuration) section below.

#### 4. `Dockerfile.sandbox` (if sandbox image is separate from server)

This does not exist yet. If sandboxes use a separate image (tools, languages,
etc.), this Dockerfile needs to be created. If sandboxes just use a stock image
with the workspace mounted, no Dockerfile is needed — configure via
`SANDBOX_IMAGE` env var.

### Code Changes Required

**None for Phase 1.** The existing `Dockerfile`, `docker.go` sandbox provider,
and server binary all work as-is on any VPS with Docker. The only changes are
new config files checked into the repo (`docker-compose.prod.yml`, `Caddyfile`,
`postgresql.conf`).

### CI/CD: GitHub Actions Deploy Workflow

Replace Render's auto-deploy with SSH-based deployment.

#### `deploy/scripts/deploy.sh`

```bash
#!/usr/bin/env bash
set -euo pipefail

# Deploy to a single node via SSH.
# Usage: ./deploy.sh <host> <ssh-key-path> [image-tag]
#
# This script is provider-agnostic — it just needs SSH access to the target.

HOST="$1"
SSH_KEY="$2"
TAG="${3:-latest}"

ssh -i "$SSH_KEY" -o StrictHostKeyChecking=no deploy@"$HOST" << REMOTE
  cd /opt/143
  docker compose -f docker-compose.prod.yml pull
  docker compose -f docker-compose.prod.yml up -d --remove-orphans
  docker compose -f docker-compose.prod.yml exec -T api /bin/migrate up
  echo "Deploy complete."
REMOTE
```

#### `.github/workflows/deploy.yml`

```yaml
name: Build & Deploy
on:
  push:
    branches: [main]

env:
  REGISTRY: ghcr.io
  SERVER_IMAGE: ghcr.io/assembledhq/143-server
  FRONTEND_IMAGE: ghcr.io/assembledhq/143-frontend

jobs:
  build:
    runs-on: ubuntu-latest
    permissions:
      contents: read
      packages: write
    steps:
      - uses: actions/checkout@v4

      - name: Log in to GHCR
        uses: docker/login-action@v3
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}

      - name: Build & push server image
        uses: docker/build-push-action@v6
        with:
          context: .
          push: true
          tags: |
            ${{ env.SERVER_IMAGE }}:latest
            ${{ env.SERVER_IMAGE }}:${{ github.sha }}

      # Add frontend build once Dockerfile.frontend exists

  deploy:
    needs: build
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: Deploy via SSH
        run: |
          chmod +x deploy/scripts/deploy.sh
          ./deploy/scripts/deploy.sh "${{ secrets.DEPLOY_HOST }}" "${{ secrets.DEPLOY_SSH_KEY }}"
```

Note: `DEPLOY_HOST` and `DEPLOY_SSH_KEY` are generic — they work regardless of
whether the VPS is on Hetzner, AWS, GCP, or anywhere else with SSH access.

### Database Migration from Render

```bash
# 1. Export from Render (use Render's external connection string)
pg_dump -h <render-db-host> -U <render-db-user> -Fc <render-db-name> > render.dump

# 2. Copy dump to VPS
scp render.dump deploy@<vps-ip>:/tmp/render.dump

# 3. Start Postgres on the VPS
docker compose -f docker-compose.prod.yml up -d postgres

# 4. Restore
docker exec -i 143-postgres-1 \
  pg_restore -U onefortythree -d onefortythree --clean --if-exists < /tmp/render.dump

# 5. Verify
docker exec 143-postgres-1 \
  psql -U onefortythree -c "SELECT count(*) FROM organizations;"
```

### Migration Checklist

- [ ] Provision VPS (any provider — see sizing table above)
- [ ] Install Docker + gVisor
- [ ] Create `deploy/` directory with config files (Caddyfile, postgresql.conf)
- [ ] Create `docker-compose.prod.yml`
- [ ] Create GitHub Actions deploy workflow
- [ ] Push images to GHCR
- [ ] `pg_dump` Render DB → `pg_restore` on VPS
- [ ] Copy `.env` to VPS (or use SOPS-encrypted secrets)
- [ ] Update DNS (Cloudflare A record → VPS IP)
- [ ] Verify health checks (`/healthz`, `/readyz`)
- [ ] Decommission Render services

### Impact Assessment

| Area | Impact | Notes |
|---|---|---|
| Application code | **None** | Zero changes to Go or frontend code |
| Dockerfile | **None** | Existing multi-stage build works as-is |
| Database | **Low** | `pg_dump`/`pg_restore`. ~30 min downtime for the cutover. |
| DNS | **Low** | Update A records. Cloudflare can proxy during transition. |
| TLS | **None** | Caddy handles Let's Encrypt automatically |
| CI/CD | **Medium** | New GitHub Actions workflow replaces Render auto-deploy |
| Secrets | **None** | SOPS + age works identically anywhere |
| Agent sandboxes | **Huge win** | Docker socket access — sandboxes work natively |

---

## Phase 2: Production Hardening (Backups, Monitoring, CI/CD)

**Goal:** Make the single-node deployment production-ready. Automated backups,
health monitoring, and a tested restore procedure.

**When to do this:** Immediately after Phase 1, before accepting real users.

### Code Changes Required

**None.** Phase 2 is entirely new deploy scripts and config files checked into
the repo. No Go or frontend code changes.

### New Files to Create

#### 1. `deploy/scripts/pg-backup.sh`

Automated `pg_dump` backups with verification and retention.

```bash
#!/usr/bin/env bash
set -euo pipefail

BACKUP_DIR="${BACKUP_DIR:-/backups/postgres}"
RETENTION_DAYS="${BACKUP_RETENTION_DAYS:-30}"
CONTAINER_NAME="${POSTGRES_CONTAINER:-143-postgres-1}"
DB_USER="${POSTGRES_USER:-onefortythree}"
DB_NAME="${POSTGRES_DB:-onefortythree}"

mkdir -p "$BACKUP_DIR"

TIMESTAMP=$(date +%Y%m%d-%H%M%S)
BACKUP_FILE="$BACKUP_DIR/$DB_NAME-$TIMESTAMP.dump"

# Custom format: compressed, supports selective restore
docker exec "$CONTAINER_NAME" \
  pg_dump -U "$DB_USER" -Fc "$DB_NAME" > "$BACKUP_FILE"

# Verify the backup is valid
pg_restore --list "$BACKUP_FILE" > /dev/null 2>&1 || {
  echo "ERROR: Backup verification failed for $BACKUP_FILE" >&2
  rm -f "$BACKUP_FILE"
  exit 1
}

BACKUP_SIZE=$(du -h "$BACKUP_FILE" | cut -f1)
echo "Backup complete: $BACKUP_FILE ($BACKUP_SIZE)"

# Clean up old backups
find "$BACKUP_DIR" -name "*.dump" -mtime +$RETENTION_DAYS -delete
echo "Cleaned backups older than $RETENTION_DAYS days"
```

**Cron schedule** (add to host crontab):

```cron
# Every 6 hours: dump the database
0 */6 * * * /opt/143/deploy/scripts/pg-backup.sh >> /var/log/pg-backup.log 2>&1

# Daily: sync backups offsite to S3-compatible storage
30 2 * * * rclone sync /backups/postgres s3:143-backups/postgres/ --log-file=/var/log/pg-backup-sync.log
```

> **`[PROVIDER-SPECIFIC]` Offsite backup target:**
>
> | Provider | S3-Compatible Storage | Notes |
> |----------|----------------------|-------|
> | Hetzner | Hetzner Object Storage | Cheapest if VPS is also Hetzner |
> | AWS | S3 | Native; use `aws s3 sync` instead of `rclone` if preferred |
> | GCP | Cloud Storage | Use `gsutil rsync` or `rclone` with GCS backend |
> | Any | MinIO (self-hosted) | If you want to avoid cloud storage entirely |
>
> `rclone` works with all of the above — configure the remote once, the backup
> script doesn't change.

**RPO:** 6 hours worst case. **RTO:** 15-30 minutes (spin up new VPS, restore from dump).

#### 2. WAL Archiving (Optional — for Near-Zero Data Loss)

Add WAL-G when 6 hours of potential data loss is unacceptable (typically once you
have paying customers).

**Changes to `deploy/postgres/postgresql.conf`** (append):

```ini
# WAL archiving (enable when Layer 3 backups are needed)
wal_level = replica
archive_mode = on
archive_command = 'wal-g wal-push %p'
archive_timeout = 60   # force archive every 60s even if segment isn't full
```

**WAL-G environment** (add to Postgres container or sidecar):

```bash
# These use the S3 API — works with any S3-compatible provider
export WALG_S3_PREFIX=s3://143-backups/wal-g
export AWS_ACCESS_KEY_ID=your-key
export AWS_SECRET_ACCESS_KEY=your-secret
export AWS_ENDPOINT=https://your-s3-endpoint.com   # omit for real AWS S3
export AWS_REGION=us-east-1
```

**Point-in-time restore:**

```bash
# Fetch latest base backup
wal-g backup-fetch /var/lib/postgresql/data LATEST

# Set recovery target
cat > /var/lib/postgresql/data/recovery.signal <<EOF
EOF
cat >> /var/lib/postgresql/data/postgresql.conf <<EOF
restore_command = 'wal-g wal-fetch %f %p'
recovery_target_time = '2025-07-15 14:47:00 UTC'
recovery_target_action = 'promote'
EOF

# Start Postgres — it replays WAL up to the target time
pg_ctl start -D /var/lib/postgresql/data
```

**RPO:** ~60 seconds. **RTO:** 15-30 minutes.

### Backup Layers Summary

| Layer | What | Protects Against | Does NOT Protect Against |
|-------|------|-----------------|------------------------|
| 1. Docker volume (`pgdata`) | Data persists across container restarts | Container crashes, restarts, upgrades, `docker compose down` | Disk failure, `DROP TABLE`, VPS deletion |
| 2. Scheduled `pg_dump` | Offsite logical backups every 6 hours | Disk failure, VPS deletion, accidental data deletion | Last 6 hours of data |
| 3. WAL-G archiving | Continuous WAL streaming to object storage | Everything — restore to any second | Nothing (this is the comprehensive layer) |

### Restore Procedures

**From pg_dump** (Layer 2):

```bash
docker compose -f docker-compose.prod.yml up -d postgres
docker exec -i 143-postgres-1 \
  pg_restore -U onefortythree -d onefortythree --clean --if-exists \
  < /backups/postgres/onefortythree-YYYYMMDD-HHMMSS.dump
```

**From WAL-G** (Layer 3): See point-in-time restore procedure above.

**Test your restore procedure.** Run a restore drill before going to production,
and monthly afterward. An untested backup is not a backup.

### Postgres Health Monitoring

These checks are provider-agnostic — they query Postgres directly.

| Check | Query / Method | Alert Threshold |
|-------|---------------|-----------------|
| Connection count | `SELECT count(*) FROM pg_stat_activity` | > 80% of `max_connections` |
| Disk usage | `SELECT pg_database_size('onefortythree')` | > 80% of available disk |
| Long-running queries | `pg_stat_activity WHERE state = 'active' AND now() - query_start > interval '5 min'` | Any |
| Dead tuples | `pg_stat_user_tables ORDER BY n_dead_tup DESC` | > 100K dead tuples |
| Backup freshness | Check latest `.dump` file mtime | > 12 hours old |
| WAL archiving status | `pg_stat_archiver` — check `last_failed_wal` | Any failed WAL |

### Phase 2 Checklist

- [ ] Set up `pg-backup.sh` cron (Layer 2 — **required before accepting users**)
- [ ] Configure offsite backup sync (`rclone` to S3-compatible storage)
- [ ] Run a restore drill — verify the backup actually works
- [ ] Set up monitoring (Datadog, Prometheus, or even just cron + email alerts)
- [ ] (Optional) Enable WAL-G archiving (Layer 3) for near-zero RPO

### Environment Variables (Backup & Recovery)

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `BACKUP_DIR` | No | `/backups/postgres` | Directory for pg_dump files |
| `BACKUP_RETENTION_DAYS` | No | `30` | Days to retain local backups |
| `WALG_S3_PREFIX` | No (Layer 3) | - | S3 path for WAL-G archives |
| `AWS_ACCESS_KEY_ID` | No (Layer 3) | - | S3 credentials for WAL-G |
| `AWS_SECRET_ACCESS_KEY` | No (Layer 3) | - | S3 credentials for WAL-G |
| `AWS_ENDPOINT` | No (Layer 3) | - | S3-compatible endpoint (omit for real AWS) |

---

## Phase 3: Multi-Node Scaling

**Goal:** Separate concerns across multiple VPSes for performance and
reliability. Add dedicated worker nodes for agent sandboxes.

**When to do this:** When agent runs are queuing up, or when you need the API to
stay responsive while heavy sandbox runs consume CPU/memory.

For background on node modes (`all`, `api`, `worker`), scheduler leader election,
and job queue distribution, see [10-infrastructure.md](10-infrastructure.md).

### What Changes in Our Code

Phase 3 is the first phase that requires application code changes.

#### 1. Container Registry Images (CI/CD change)

Multi-node means you can't `docker build` on each node — you need pre-built
images in a registry. The CI workflow from Phase 1 already pushes to GHCR.
Each node pulls from `ghcr.io/assembledhq/143-server:latest`.

#### 2. Read/Write Splitting (Go code change — optional, for Phase 3c)

If you add a Postgres read replica, the app needs to route read-heavy queries
(dashboard, audit log, experiment reads) to the replica:

```go
// internal/database/pool.go
type DBPool struct {
    Primary *pgxpool.Pool  // DATABASE_URL — all writes
    Replica *pgxpool.Pool  // DATABASE_REPLICA_URL — read-heavy queries (nil if no replica)
}

func (db *DBPool) ReadPool() *pgxpool.Pool {
    if db.Replica != nil {
        return db.Replica
    }
    return db.Primary  // falls back to primary if no replica configured
}
```

This is a small change. Job queue operations, writes, and anything requiring
strong consistency always use `Primary`. Dashboard queries, experiment reads, and
audit log queries use `ReadPool()`.

### Scaling Steps

Phase 3 is broken into independent steps. Do them in order, but each step is
self-contained.

#### Step 3a: Separate Postgres to Its Own VPS

The single most impactful scaling move. Isolates the database from sandbox
CPU/memory spikes.

```
┌──────────────────────┐     ┌──────────────────────┐
│  VPS-1 (DB)          │     │  VPS-2 (App)         │
│  4 CPU / 8 GB        │     │  8 CPU / 16 GB       │
│                      │     │                      │
│  ┌──────────┐        │     │  ┌──────────┐        │
│  │ Postgres │◄───────┼─────┼──│  Server  │        │
│  │          │        │     │  │ mode=all │        │
│  └──────────┘        │     │  └──────────┘        │
│                      │     │  ┌──────────┐        │
│  pg_dump cron        │     │  │  Caddy   │        │
│  WAL-G archiving     │     │  └──────────┘        │
└──────────────────────┘     └──────────────────────┘
        private network (10.x.x.x)
```

**Infrastructure changes:**
1. Provision a second VPS for the database
2. Put both VPSes on a private network (see provider-specific note below)
3. Postgres listens on the private IP; firewall blocks port 5432 on public

**Config changes:**
- `DATABASE_URL` on VPS-2 points to VPS-1's private IP
- Remove Postgres from the app compose file

> **`[PROVIDER-SPECIFIC]` Private networking:**
>
> | Provider | Feature | Notes |
> |----------|---------|-------|
> | Hetzner | Cloud Networks (vSwitch) | Free, ~2 Gbps, no encryption needed |
> | AWS | VPC + private subnets | Default VPC works; use security groups for access control |
> | GCP | VPC network | Automatic; instances in the same VPC see each other |
> | DigitalOcean | VPC | Free, auto-assigned private IPs |
>
> The concept is identical everywhere: instances on the same private network
> communicate over private IPs without traversing the public internet.

**New file: `docker-compose.db.yml`** (runs on the DB VPS):

```yaml
services:
  postgres:
    image: postgres:17
    environment:
      POSTGRES_DB: onefortythree
      POSTGRES_USER: onefortythree
      POSTGRES_PASSWORD: ${DB_PASSWORD}
      POSTGRES_INITDB_ARGS: "--data-checksums"
    volumes:
      - pgdata:/var/lib/postgresql/data
      - ./deploy/postgres/postgresql.conf:/etc/postgresql/conf.d/custom.conf:ro
    command: postgres -c config_file=/etc/postgresql/conf.d/custom.conf
    ports:
      - "0.0.0.0:5432:5432"   # accessible from private network
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U onefortythree"]
      interval: 10s
      timeout: 5s
      retries: 5
    restart: unless-stopped
    deploy:
      resources:
        limits:
          memory: 4G
          cpus: "3.0"

volumes:
  pgdata:
```

**Data migration from single-node:**

```bash
# On the old VPS:
docker exec 143-postgres-1 pg_dump -U onefortythree -Fc onefortythree > /tmp/143.dump
scp /tmp/143.dump db-vps:/tmp/143.dump

# On the new DB VPS:
docker compose -f docker-compose.db.yml up -d
docker exec -i 143-postgres-1 \
  pg_restore -U onefortythree -d onefortythree --clean --if-exists < /tmp/143.dump

# On the app VPS — update DATABASE_URL to point at DB VPS private IP:
DATABASE_URL=postgres://onefortythree:${DB_PASSWORD}@10.0.0.2:5432/onefortythree?sslmode=disable
docker compose -f docker-compose.prod.yml up -d
```

**Move to Step 3b when:** you need more concurrent agent runs than one VPS can
handle (typically >3-5 concurrent sandboxes).

#### Step 3b: Add Dedicated Worker Nodes

Workers run agent sandboxes — the most resource-intensive part. Each worker runs
`MAX_CONCURRENT_RUNS` sandboxes in parallel.

```
┌───────────────┐     ┌───────────────┐
│  VPS-1 (DB)   │     │  VPS-2 (App)  │
│               │     │  mode=all     │
│  Postgres  ◄──┼─────┤  Caddy        │
│               │  ┌──┤               │
└───────────────┘  │  └───────────────┘
                   │
        ┌──────────┼──────────┐
        │          │          │
   ┌────▼────┐ ┌──▼──────┐ ┌─▼───────┐
   │ VPS-3   │ │ VPS-4   │ │ VPS-5   │
   │ worker  │ │ worker  │ │ worker  │
   │ 5 runs  │ │ 5 runs  │ │ 5 runs  │
   └─────────┘ └─────────┘ └─────────┘
```

**New file: `docker-compose.worker.yml`** (runs on each worker VPS):

```yaml
services:
  worker:
    image: ghcr.io/assembledhq/143-server:latest
    environment:
      DATABASE_URL: postgres://onefortythree:${DB_PASSWORD}@${DB_HOST}:5432/onefortythree?sslmode=disable
      MODE: worker
      NODE_ID: ${HOSTNAME}
      MAX_CONCURRENT_RUNS: ${MAX_CONCURRENT_RUNS:-5}
      SANDBOX_IMAGE: ghcr.io/assembledhq/143-sandbox:latest
      SANDBOX_RUNTIME: runsc
    env_file:
      - .env
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
    restart: unless-stopped
    deploy:
      resources:
        limits:
          memory: 2G
          cpus: "1.0"
```

**Adding a new worker (5-minute operation on any provider):**

```bash
# 1. Provision VPS, install Docker + gVisor (same as Phase 1)
# 2. Copy docker-compose.worker.yml and .env to /opt/143
# 3. Start it:
docker compose -f docker-compose.worker.yml up -d

# The worker registers itself in the nodes table,
# starts polling for jobs, and picks up work immediately.
```

**Worker VPS sizing:**

| VPS Size | `MAX_CONCURRENT_RUNS` | Good for |
|----------|----------------------|----------|
| 4 CPU / 8 GB | 1-2 | Small / test |
| 8 CPU / 16 GB | 3 | Medium |
| 16 CPU / 32 GB | 5-7 | Production sweet spot |
| 32 CPU / 64 GB | 10-15 | Heavy workloads |

Each sandbox gets `SANDBOX_CPU_LIMIT` (default 2) cores and
`SANDBOX_MEMORY_LIMIT` (default 4 GB). Size the VPS to fit the desired
concurrency plus headroom for the worker process and OS.

**Move to Step 3c when:** you need API redundancy (uptime SLA), or a single API
node can't keep up with webhook volume.

#### Step 3c: Multiple API Nodes + Load Balancer

API nodes are stateless — sessions live in Postgres. Add as many as needed behind
a load balancer.

```
              ┌──────────────────┐
              │   Load Balancer  │
              └──┬──────────┬────┘
                 │          │
           ┌─────▼──┐  ┌───▼────┐
           │ VPS-2  │  │ VPS-6  │
           │ all    │  │ api    │
           └────┬───┘  └───┬────┘
                │          │
┌───────────────▼──────────▼──────────────┐
│              VPS-1 (DB)                  │
│              Postgres                    │
└──────────────────────────────────────────┘
```

Keep at least one node as `mode=all` so the scheduler runs. All `api` and `all`
nodes serve the same traffic.

**Load balancer options (all provider-agnostic except managed LBs):**

| Option | Provider-Agnostic? | Notes |
|--------|-------------------|-------|
| Caddy as reverse proxy on a VPS | Yes | Cheapest; add `reverse_proxy` upstream block |
| nginx as reverse proxy on a VPS | Yes | More config, same result |
| HAProxy on a VPS | Yes | Best for high-throughput |
| Managed LB (cloud provider) | No | Simplest ops-wise |

> **`[PROVIDER-SPECIFIC]` Managed load balancers:**
>
> | Provider | Service | Cost |
> |----------|---------|------|
> | Hetzner | Hetzner Load Balancer | ~€6/mo |
> | AWS | ALB | ~$16/mo + per-request |
> | GCP | Cloud Load Balancing | ~$18/mo + per-request |

**Caddy config for multi-node** (provider-agnostic):

```
app.143.dev {
    reverse_proxy vps-2:8080 vps-6:8080 {
        health_uri /healthz
        health_interval 10s
        lb_policy round_robin
    }
}
```

#### Connection Pooling (PgBouncer) — When You Need It

When total connections across all nodes approach 80% of `max_connections` (default
100). Each node's pgx pool defaults to ~10 connections, so with 8+ nodes you're
getting close.

Add to `docker-compose.db.yml`:

```yaml
  pgbouncer:
    image: edoburu/pgbouncer:1.23.1
    environment:
      DATABASE_URL: postgres://onefortythree:${DB_PASSWORD}@postgres:5432/onefortythree
      POOL_MODE: transaction   # MUST be transaction — session mode breaks SKIP LOCKED
      MAX_CLIENT_CONN: 500
      DEFAULT_POOL_SIZE: 30
      RESERVE_POOL_SIZE: 5
    ports:
      - "0.0.0.0:6432:6432"
    depends_on:
      - postgres
    restart: unless-stopped
```

All app/worker nodes change `DATABASE_URL` to point at PgBouncer (port 6432)
instead of Postgres directly.

### Fleet Deployment

Once you have multiple nodes, the Phase 1 single-node deploy script doesn't
scale. Here's a fleet deployment script that works with any provider.

#### `deploy/scripts/deploy-fleet.sh`

```bash
#!/usr/bin/env bash
set -euo pipefail

# Deploy to all nodes listed in a hosts file.
# Usage: ./deploy-fleet.sh [image-tag]
#
# Reads node IPs from /opt/143/fleet-hosts.txt (one IP per line).
# Provider-agnostic — just needs SSH access.

TAG="${1:-latest}"
HOSTS_FILE="${FLEET_HOSTS:-/opt/143/fleet-hosts.txt}"
SERVER_IMAGE="ghcr.io/assembledhq/143-server:$TAG"

echo "Deploying $TAG to fleet..."

while IFS= read -r IP; do
  [[ -z "$IP" || "$IP" == \#* ]] && continue
  echo "--- Deploying to $IP ---"

  ssh -o StrictHostKeyChecking=no deploy@"$IP" << REMOTE
    docker pull $SERVER_IMAGE
    cd /opt/143
    docker compose -f docker-compose.*.yml up -d --remove-orphans

    # Wait for health check
    for i in \$(seq 1 30); do
      if curl -sf http://localhost:8080/healthz > /dev/null 2>&1; then
        echo "Health check passed."
        break
      fi
      sleep 2
    done
REMOTE

  echo "$IP deployed."
done < "$HOSTS_FILE"

echo "Fleet deployment complete."
```

This reads from a plain text hosts file. No cloud API dependency. You can also
generate the hosts file from your provider's CLI or API if you prefer.

### When to Scale What

| Signal | Action |
|--------|--------|
| Agent runs queuing for > 5 min | Add worker nodes (Step 3b) |
| Postgres CPU > 70% sustained | Separate Postgres to its own VPS (Step 3a) |
| API p95 latency > 500ms under load | Add API nodes (Step 3c) |
| Disk I/O wait > 20% on shared VPS | Separate Postgres (Step 3a) |
| Need 99.9%+ uptime | Multiple API nodes + Postgres HA (Step 3c + Phase 4) |

---

## Phase 4: Fleet Automation and High Availability (Future)

**Goal:** Automated node provisioning, auto-scaling workers based on queue depth,
and Postgres high availability.

**When to do this:** When you're managing 5+ nodes and
provisioning/decommissioning frequently enough that it's a chore.

### Node Provisioning with cloud-init

cloud-init is a standard supported by **every major cloud provider** (AWS, GCP,
Azure, Hetzner, DigitalOcean, Oracle Cloud, Vultr). You write a user-data script
once; it runs on first boot regardless of provider.

#### `deploy/cloud-init/worker.yml`

```yaml
#cloud-config

packages:
  - docker.io
  - docker-compose-plugin

runcmd:
  # Install gVisor
  - curl -fsSL https://gvisor.dev/archive.key | gpg --dearmor -o /usr/share/keyrings/gvisor-archive-keyring.gpg
  - echo "deb [arch=amd64 signed-by=/usr/share/keyrings/gvisor-archive-keyring.gpg] https://storage.googleapis.com/gvisor/releases release main" > /etc/apt/sources.list.d/gvisor.list
  - apt-get update && apt-get install -y runsc
  - runsc install
  - systemctl restart docker

  # Pull images
  - docker login ghcr.io -u deploy -p ${REGISTRY_TOKEN}
  - docker pull ghcr.io/assembledhq/143-server:latest
  - docker pull ghcr.io/assembledhq/143-sandbox:latest

  # Write compose file and start
  - mkdir -p /opt/143
  - cp /opt/143/docker-compose.worker.yml /opt/143/docker-compose.yml
  - cd /opt/143 && docker compose up -d

write_files:
  - path: /opt/143/.env
    content: |
      DATABASE_URL=${DATABASE_URL}
      MEZMO_INGESTION_KEY=${MEZMO_INGESTION_KEY}
      DD_API_KEY=${DD_API_KEY}
    permissions: '0600'
```

> **`[PROVIDER-SPECIFIC]` How to pass user-data at instance creation:**
>
> | Provider | Method |
> |----------|--------|
> | Hetzner | `hcloud server create --user-data "$(cat worker.yml)"` |
> | AWS | `aws ec2 run-instances --user-data file://worker.yml` |
> | GCP | `gcloud compute instances create --metadata-from-file user-data=worker.yml` |
> | DigitalOcean | `doctl compute droplet create --user-data "$(cat worker.yml)"` |

### Auto-Scaling Workers (Go Code Change)

The auto-scaler runs as part of the scheduler (on whichever node holds the
advisory lock). It checks queue depth and adjusts the fleet.

**Important:** The auto-scaler uses a `CloudProvider` interface so it's not
locked to any vendor:

```go
// internal/autoscaler/provider.go

// CloudProvider abstracts VM lifecycle operations.
// Implement this interface for each cloud provider you want to support.
type CloudProvider interface {
    // CreateInstance provisions a new VM with the given cloud-init user-data.
    // Returns the instance ID and private IP.
    CreateInstance(ctx context.Context, opts CreateOpts) (instanceID string, privateIP string, err error)

    // DeleteInstance terminates a VM by ID.
    DeleteInstance(ctx context.Context, instanceID string) error

    // ListInstances returns all VMs matching the given labels/tags.
    ListInstances(ctx context.Context, labels map[string]string) ([]Instance, error)
}

type CreateOpts struct {
    Name       string            // e.g., "143-worker-1712345678"
    Size       string            // provider-specific instance type (e.g., "cx42", "t3.xlarge")
    Region     string            // provider-specific region/location
    Image      string            // OS image (e.g., "ubuntu-24.04")
    UserData   string            // cloud-init script
    Labels     map[string]string // for fleet management
    NetworkID  string            // private network to attach to
    SSHKeyName string            // for SSH access
}

type Instance struct {
    ID        string
    Name      string
    PrivateIP string
    Labels    map[string]string
}
```

```go
// internal/autoscaler/autoscaler.go

type AutoScaler struct {
    db       *pgxpool.Pool
    cloud    CloudProvider   // NOT a specific vendor client
    config   AutoScaleConfig
    logger   zerolog.Logger
}

type AutoScaleConfig struct {
    Enabled          bool          `env:"AUTOSCALE_ENABLED" envDefault:"false"`
    MinWorkers       int           `env:"AUTOSCALE_MIN_WORKERS" envDefault:"1"`
    MaxWorkers       int           `env:"AUTOSCALE_MAX_WORKERS" envDefault:"10"`
    InstanceSize     string        `env:"AUTOSCALE_INSTANCE_SIZE"`     // provider-specific
    Region           string        `env:"AUTOSCALE_REGION"`            // provider-specific
    ScaleUpThreshold int           `env:"AUTOSCALE_SCALE_UP_THRESHOLD" envDefault:"5"`
    ScaleDownAfter   time.Duration `env:"AUTOSCALE_SCALE_DOWN_AFTER" envDefault:"15m"`
    CooldownPeriod   time.Duration `env:"AUTOSCALE_COOLDOWN" envDefault:"5m"`
    RunsPerWorker    int           `env:"AUTOSCALE_RUNS_PER_WORKER" envDefault:"5"`
    NetworkID        string        `env:"AUTOSCALE_NETWORK_ID"`
}

func (a *AutoScaler) Tick(ctx context.Context) {
    var pendingJobs int
    a.db.QueryRow(ctx,
        "SELECT count(*) FROM jobs WHERE status = 'pending' AND job_type = 'agent_run'",
    ).Scan(&pendingJobs)

    var activeWorkers int
    a.db.QueryRow(ctx,
        "SELECT count(*) FROM nodes WHERE mode = 'worker' AND status = 'active'",
    ).Scan(&activeWorkers)

    // Scale up: more pending jobs than capacity
    if pendingJobs > a.config.ScaleUpThreshold && activeWorkers < a.config.MaxWorkers {
        needed := (pendingJobs / a.config.RunsPerWorker) + 1 - activeWorkers
        needed = min(needed, a.config.MaxWorkers-activeWorkers)
        for i := 0; i < needed; i++ {
            a.provisionWorker(ctx)
        }
        return
    }

    // Scale down: idle workers
    if activeWorkers > a.config.MinWorkers {
        a.drainIdleWorkers(ctx)
    }
}
```

**Provider implementations** would live in:
- `internal/autoscaler/hetzner.go` — wraps `hcloud-go`
- `internal/autoscaler/aws.go` — wraps AWS EC2 SDK
- `internal/autoscaler/gcp.go` — wraps GCP Compute SDK

Selected by env var: `AUTOSCALE_PROVIDER=hetzner|aws|gcp`

### Auto-Scale Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `AUTOSCALE_ENABLED` | `false` | Enable auto-scaling |
| `AUTOSCALE_PROVIDER` | - | Cloud provider: `hetzner`, `aws`, `gcp` |
| `AUTOSCALE_MIN_WORKERS` | `1` | Minimum worker count (never scale below) |
| `AUTOSCALE_MAX_WORKERS` | `10` | Maximum worker count (hard cap) |
| `AUTOSCALE_INSTANCE_SIZE` | - | Provider-specific instance type |
| `AUTOSCALE_REGION` | - | Provider-specific region/location |
| `AUTOSCALE_SCALE_UP_THRESHOLD` | `5` | Pending jobs that trigger scale-up |
| `AUTOSCALE_SCALE_DOWN_AFTER` | `15m` | Idle time before drain |
| `AUTOSCALE_COOLDOWN` | `5m` | Min time between scale events |
| `AUTOSCALE_RUNS_PER_WORKER` | `5` | Target concurrent runs per worker |

### Postgres High Availability

Two options:

**Option A: Streaming Replication (self-managed)**

- Primary handles all writes; replica handles read-heavy queries
- Failover: manual or via [Patroni](https://github.com/patroni/patroni)
- Requires the read/write splitting code change from Step 3c

**Option B: Managed Postgres**

When operational overhead outweighs cost savings, move to managed Postgres. The
migration is a single `pg_dump`/`pg_restore` with no application code changes —
the app only sees `DATABASE_URL`.

| Provider | HA Setup | Cost (4GB RAM) | Notes |
|----------|----------|----------------|-------|
| Supabase | Auto-failover | ~$25/mo | Easy setup |
| Neon | Serverless | Pay-per-query | Good for variable workloads |
| AWS RDS | Multi-AZ | ~$70/mo | Battle-tested |
| GCP Cloud SQL | HA with failover | ~$80/mo | Native GCP integration |
| Crunchy Bridge | Managed HA | ~$50/mo | Postgres-focused |

---

## Production Postgres Configuration

For a single-VPS deployment (4-16GB RAM). This file is used across all phases.

```ini
# deploy/postgres/postgresql.conf

# Connection limits
max_connections = 100
shared_buffers = 256MB          # 25% of RAM; scale with VPS size (see table)
effective_cache_size = 768MB    # 75% of RAM; tells planner about OS cache
work_mem = 4MB                  # per-sort/hash — keep conservative
maintenance_work_mem = 64MB     # for VACUUM, CREATE INDEX

# Write performance
wal_buffers = 16MB
checkpoint_completion_target = 0.9
random_page_cost = 1.1          # for SSD storage (all modern VPS providers use SSDs)

# Autovacuum
autovacuum = on
autovacuum_max_workers = 3
autovacuum_naptime = 60

# Logging
log_min_duration_statement = 1000  # log queries > 1 second
log_checkpoints = on
log_connections = on
log_disconnections = on
log_lock_waits = on

# Data integrity
fsync = on                      # NEVER turn this off in production
full_page_writes = on
```

**Scale with VPS RAM:**

| VPS RAM | `shared_buffers` | `effective_cache_size` |
|---------|------------------|----------------------|
| 2 GB | 512 MB | 1.5 GB |
| 4 GB | 1 GB | 3 GB |
| 8 GB | 2 GB | 6 GB |
| 16 GB | 4 GB | 12 GB |

### Data Integrity Safeguards

Built into the schema and application:

1. **Data checksums** — enabled at `initdb` time (`--data-checksums`). Detects silent disk corruption.
2. **Audit log immutability** — trigger prevents `UPDATE`/`DELETE` on `audit_log` (see migration `000001`).
3. **Foreign key constraints** — `ON DELETE CASCADE` or `ON DELETE RESTRICT` everywhere.
4. **`timestamptz` everywhere** — all timestamps are timezone-aware (UTC).
5. **UUID primary keys** — no auto-increment collisions across nodes.
6. **Transaction isolation** — job queue uses `FOR UPDATE SKIP LOCKED` under `READ COMMITTED`.

### Postgres Scaling Path

| Scale | DB Size | Setup | Action |
|-------|---------|-------|--------|
| Launch | < 1 GB | Single VPS, Postgres in Docker | Layer 1 + Layer 2 backups |
| Growing | 1-50 GB | Single VPS | Add Layer 3 (WAL-G), tune `shared_buffers` |
| Busy | 50-500 GB | Dedicated DB VPS | Separate DB (Step 3a), add read replica |
| Large | 500 GB+ | Managed Postgres | PgBouncer, table partitioning for `agent_run_logs` and `audit_log` |

---

## Capacity Planning

| Scale | Nodes | Monthly Cost (approx) | Concurrent Agents | Phase |
|-------|-------|----------------------|-------------------|-------|
| **Solo** | 1 (8 CPU / 16 GB) | $16-120 | 3-5 | 1 |
| **Small team** | 2 (DB + App) | $30-200 | 3-5 | 3a |
| **Growing** | 4 (DB + App + 2 Workers) | $60-400 | 10-15 | 3b |
| **Busy** | 7 (DB + 2 API + 4 Workers) | $120-800 | 20-30 | 3c |
| **Auto-scaled** | 2-20 (dynamic) | $30-2000 | 5-100 (elastic) | 4 |

Cost ranges reflect the difference between budget providers (Hetzner/DigitalOcean)
and premium providers (AWS/GCP). The wide range is intentional — choose based on
your existing cloud relationships, compliance requirements, and team familiarity.

**The dominant cost is LLM API, not infrastructure.** A single agent run costs
$0.50-5.00 in Claude API tokens. The VPS to run it costs ~$0.02-0.15/hr. Don't
under-provision to save $10/month.

| Category | % of Total Cost | Example (100 repos) |
|----------|----------------|---------------------|
| LLM API (Claude/GPT) | 80-90% | $2,000-10,000/mo |
| Infrastructure | 5-10% | $100-800/mo |
| Observability | 3-5% | $50-100/mo |
| Backups (S3 storage) | < 1% | $5-10/mo |

---

## Cloud Provider Portability Summary

Everything in this design uses standard, portable technology:

| Component | Technology | Provider-Agnostic? | Notes |
|-----------|-----------|-------------------|-------|
| Container orchestration | Docker Compose | Yes | Works on any Linux host |
| Container registry | GHCR | Yes | Could also use Docker Hub, ECR, GCR, etc. |
| TLS termination | Caddy | Yes | Auto Let's Encrypt on any public IP |
| Sandbox isolation | gVisor (runsc) | Yes | Works on any Linux kernel 4.4+ |
| Database | Postgres 17 in Docker | Yes | Or any managed Postgres service |
| Backup storage | S3-compatible via rclone | Yes | AWS S3, GCS, Hetzner Object Storage, MinIO |
| WAL archiving | WAL-G | Yes | Supports S3, GCS, Azure Blob, local filesystem |
| Node provisioning | cloud-init | Yes | Supported by every major cloud provider |
| CI/CD | GitHub Actions + SSH | Yes | Just needs SSH access to the target VPS |
| Private networking | Provider VPC/vNetwork | **Provider-specific** | Concept is universal; API/config differs |
| Auto-scaling | `CloudProvider` interface | **Provider-specific impl** | Interface is ours; implementations wrap vendor SDKs |
| Managed load balancer | Provider LB | **Provider-specific** | Or use Caddy/nginx/HAProxy on a VPS instead |

**What you'd need to change to switch providers:**
1. Provision new VPSes on the new provider (same specs)
2. Set up a private network (different API, same concept)
3. Update `DEPLOY_HOST` in GitHub Actions secrets
4. Update `rclone` config if backup storage endpoint changes
5. (Phase 4 only) Implement the `CloudProvider` interface for the new provider

Application code, Docker Compose files, Caddy config, Postgres config, backup
scripts, and CI/CD workflows all stay identical.

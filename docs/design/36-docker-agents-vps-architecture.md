# Design Doc 36: Docker Agent Nodes — Render → Hetzner Migration

## Context

143.dev currently runs on **Render** (Go API + Next.js frontend + Render Managed
Postgres). Render does not support Docker-in-Docker or privileged containers, so
agent sandboxes cannot run on Render. To run Docker-based agent sandboxes, we
need to migrate to infrastructure where we control the Docker daemon.

**Hetzner VPS** is the target — dramatically cheaper than Render, full Docker
access, and scales naturally from a single node to a multi-node cluster.

This document covers two phases:
1. **Phase 1: Migrate from Render to Hetzner** — Get everything running on a single Hetzner VPS
2. **Phase 2: Scale on Hetzner** — Add worker nodes, auto-scaling, HA Postgres as needed

---

## Phase 1: Migrate from Render to Hetzner (Single VPS)

### What's on Render Today

| Component | Render Service | Notes |
|---|---|---|
| Go API | Render Docker service | Runs the API + worker + scheduler |
| Next.js frontend | Render Node service | Served separately |
| PostgreSQL | Render Managed DB | Automated backups, managed TLS |
| TLS | Render auto-TLS | Automatic certificate management |
| DNS | External (Cloudflare) | Points to Render |
| CI/CD | `git push` → Render auto-builds | Zero-config deploys |
| Agent sandboxes | **Cannot run** | No Docker socket access |

### Target: Single Hetzner VPS

Everything on one machine. The existing Docker provider works as-is — no remote
provider or cross-cloud networking needed.

```
┌──────────────────────────────────────────────────────────────────────┐
│  HETZNER VPS (CX42 — 8 vCPU, 16GB RAM, 160GB SSD, €14/mo)          │
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

### docker-compose.yml (Hetzner Production)

```yaml
services:
  caddy:
    image: caddy:2-alpine
    ports:
      - "80:80"
      - "443:443"
    volumes:
      - ./Caddyfile:/etc/caddy/Caddyfile
      - caddy_data:/data
    restart: unless-stopped

  api:
    build:
      context: .
      dockerfile: Dockerfile
    environment:
      DATABASE_URL: postgres://onefortythree:${DB_PASSWORD}@postgres:5432/onefortythree?sslmode=disable
      PORT: "8080"
      MODE: all
      BASE_URL: https://143.dev
      FRONTEND_URL: https://143.dev
      # ... all other env vars from .env
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
    depends_on:
      postgres:
        condition: service_healthy
    restart: unless-stopped
    deploy:
      resources:
        limits:
          memory: 2G
          cpus: "4.0"

  frontend:
    build:
      context: ./frontend
      dockerfile: Dockerfile
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
      - ./backups:/backups
    command: postgres -c config_file=/etc/postgresql/conf.d/custom.conf
    ports:
      - "127.0.0.1:5432:5432"
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

### Caddyfile

```
143.dev {
    handle /api/* {
        reverse_proxy api:8080
    }
    handle {
        reverse_proxy frontend:3000
    }
}
```

### CI/CD: GitHub Actions

Replace Render's auto-deploy:

```yaml
# .github/workflows/deploy.yml
name: Deploy to Hetzner
on:
  push:
    branches: [main]
jobs:
  deploy:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: Build and push images
        run: |
          docker build -t ghcr.io/assembledhq/143-api:${{ github.sha }} .
          docker push ghcr.io/assembledhq/143-api:${{ github.sha }}
      - name: Deploy via SSH
        uses: appleboy/ssh-action@v1
        with:
          host: ${{ secrets.HETZNER_IP }}
          username: deploy
          key: ${{ secrets.HETZNER_SSH_KEY }}
          script: |
            cd /opt/143
            docker compose pull
            docker compose up -d --remove-orphans
            docker compose exec api /bin/migrate up
```

### Database Migration from Render

```bash
# 1. Export from Render
# Use Render's external connection string or pg_dump from within the service
pg_dump -h <render-db-host> -U <render-db-user> -Fc <render-db-name> > render.dump

# 2. Copy dump to Hetzner VPS
scp render.dump deploy@<hetzner-ip>:/tmp/render.dump

# 3. Start Postgres on Hetzner
docker compose up -d postgres

# 4. Restore
docker exec -i 143-postgres-1 pg_restore -U onefortythree -d onefortythree --clean --if-exists < /tmp/render.dump

# 5. Verify
docker exec 143-postgres-1 psql -U onefortythree -c "SELECT count(*) FROM organizations;"
```

### Impact Assessment

| Area | Impact | Notes |
|---|---|---|
| Code changes | **None** | No application code changes needed |
| Dockerfile | **None** | Same multi-stage Dockerfile works on Hetzner |
| Database migration | **Low** | `pg_dump` from Render → `pg_restore` on Hetzner. ~30 min downtime. |
| DNS cutover | **Low** | Update A records. Use Cloudflare for zero-downtime. |
| TLS | **Low** | Caddy handles Let's Encrypt automatically |
| CI/CD | **Medium** | Replace Render auto-deploy with GitHub Actions SSH deploy |
| Secrets management | **None** | SOPS + age works identically |
| Monitoring | **Medium** | Replace Render's dashboard with Prometheus/Grafana or keep Datadog |
| Backups | **Medium** | Set up pg_dump cron + Hetzner volume snapshots |
| Agent sandboxes | **Huge win** | Docker socket access works natively — no remote provider needed |

### Cost Comparison

| Setup | Cost | Docker Support |
|---|---|---|
| Render (Starter API + Web + DB) | ~$21/mo minimum | No |
| Hetzner CX42 (8 vCPU, 16GB, 160GB SSD) | ~€14/mo (~$16/mo) | Full Docker access |

### Migration Checklist

- [ ] Provision Hetzner CX42
- [ ] Install Docker + Docker Compose
- [ ] Install gVisor for sandbox isolation
- [ ] Copy docker-compose.yml + Caddyfile to `/opt/143`
- [ ] Set up GitHub Actions deploy workflow
- [ ] `pg_dump` Render DB → `pg_restore` on Hetzner
- [ ] Copy environment variables / SOPS-encrypted secrets
- [ ] Update DNS to point to Hetzner IP
- [ ] Set up pg_dump cron for backups (see [10-infrastructure.md](10-infrastructure.md) for backup scripts)
- [ ] Verify health checks and monitoring
- [ ] Decommission Render services

---

## Phase 2: Scale on Hetzner

Phase 1 gives you a single VPS running everything. Phase 2 is the step-by-step
path to a multi-node cluster. Each stage builds on the previous one — don't skip
ahead. Move to the next stage only when you hit the limits of the current one.

For background on node modes (`all`, `api`, `worker`), scheduler leader election,
job queue distribution, and other architectural patterns, see
[10-infrastructure.md](10-infrastructure.md).

### Stage 1: Single VPS (where Phase 1 leaves you)

Everything runs on one machine via docker compose. A CX42 (8 vCPU, 16GB) handles
the API, frontend, Postgres, and 3-5 concurrent agent sandboxes comfortably.

```
┌─────────────────────────────────────────┐
│  VPS-1 (8 CPU / 16 GB)                 │
│                                         │
│  ┌──────────┐  ┌──────────┐            │
│  │ Postgres │  │  Server  │            │
│  │          │  │ mode=all │            │
│  └──────────┘  └──────────┘            │
│                ┌──────────┐            │
│                │  Caddy   │            │
│                └──────────┘            │
└─────────────────────────────────────────┘
```

**Move to Stage 2 when:** agent runs are queuing up (check `jobs.queue.depth` metric), or you need the API to stay responsive while heavy agent runs consume CPU/memory.

### Stage 2: Split Postgres to Its Own VPS

The single most impactful scaling move:

1. **Resource isolation** — sandbox spikes won't starve Postgres
2. **Independent backups** — backup scripts don't compete with the app for I/O
3. **Independent upgrades** — restart Docker without touching the database
4. **Foundation for everything after** — every subsequent stage assumes Postgres is separate

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
```

**How to do it:**

```bash
# === On the NEW database VPS (VPS-1) ===

# 1. Set up Postgres
mkdir -p /opt/143 && cd /opt/143
```

```yaml
# /opt/143/docker-compose.db.yml
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
      - "0.0.0.0:5432:5432"
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

```bash
# 2. Migrate data from the Phase 1 VPS
# On the old VPS:
docker exec 143-postgres-1 pg_dump -U onefortythree -Fc onefortythree > /tmp/143.dump

# Copy to the new VPS:
scp /tmp/143.dump db-vps:/tmp/143.dump

# On the new VPS — start Postgres and restore:
docker compose -f docker-compose.db.yml up -d
docker exec -i 143-postgres-1 pg_restore -U onefortythree -d onefortythree --clean --if-exists < /tmp/143.dump

# 3. Secure the connection
# Put both VPSs on the same Hetzner private network (free, ~2Gbps, no encryption needed).
# See: https://docs.hetzner.com/cloud/networks/getting-started/creating-a-network/
# Postgres listens on the private IP. Firewall blocks port 5432 on the public interface.

# === On the APP VPS (VPS-2) ===

# 4. Update DATABASE_URL to point at the DB VPS private IP
DATABASE_URL=postgres://onefortythree:${DB_PASSWORD}@10.0.0.2:5432/onefortythree?sslmode=disable

# 5. Remove Postgres from the app compose file and restart
docker compose up -d
```

**Move to Stage 3 when:** you need more concurrent agent runs than one VPS can handle (typically > 3-5 concurrent sandboxes).

### Stage 3: Add Dedicated Worker Nodes

Worker nodes run agent sandboxes — the most resource-intensive part. Each worker
runs `MAX_CONCURRENT_RUNS` sandboxes in parallel. Adding a worker is a 5-minute
operation.

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

**On each new worker VPS:**

```bash
# 1. Install Docker + gVisor
curl -fsSL https://get.docker.com | sh
# Install gVisor (see 10-infrastructure.md for gVisor setup)

# 2. Pull the images
docker pull ghcr.io/assembledhq/143-server:latest
docker pull ghcr.io/assembledhq/143-sandbox:latest

# 3. Create the compose file
```

```yaml
# /opt/143/docker-compose.worker.yml
services:
  worker:
    image: ghcr.io/assembledhq/143-server:latest
    environment:
      DATABASE_URL: postgres://onefortythree:${DB_PASSWORD}@10.0.0.2:5432/onefortythree?sslmode=disable
      MODE: worker
      NODE_ID: ${HOSTNAME}
      MAX_CONCURRENT_RUNS: 5
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

```bash
# 4. Start it
docker compose -f docker-compose.worker.yml up -d

# That's it. The worker registers itself in the nodes table,
# starts polling for jobs, and picks up work immediately.
```

**Sizing worker VPSs:** Each sandbox gets `SANDBOX_CPU_LIMIT` (default 2) cores and `SANDBOX_MEMORY_LIMIT` (default 4GB) memory. For 5 concurrent runs, you want at least 12 CPU / 24 GB RAM on the worker VPS (headroom for the worker process and OS).

| Worker VPS size | `MAX_CONCURRENT_RUNS` | Good for |
|----------------|----------------------|----------|
| 4 CPU / 8 GB | 1-2 | Small/test |
| 8 CPU / 16 GB | 3 | Medium |
| 16 CPU / 32 GB | 5-7 | Production sweet spot |
| 32 CPU / 64 GB | 10-15 | Heavy workloads |

**Move to Stage 4 when:** you need API redundancy (uptime SLA), or a single API node can't keep up with webhook volume.

### Stage 4: Multiple API Nodes Behind a Load Balancer

API nodes are stateless — sessions live in Postgres. Add as many as you need
behind Caddy, nginx, or a Hetzner Load Balancer (€6/mo).

```
                 ┌──────────────────┐
                 │   Caddy / LB     │
                 │   (VPS-6 or      │
                 │    cloud LB)     │
                 └──┬──────────┬────┘
                    │          │
              ┌─────▼──┐  ┌───▼────┐
              │ VPS-2  │  │ VPS-7  │
              │ all    │  │ api    │
              └────┬───┘  └───┬────┘
                   │          │
┌──────────────────▼──────────▼───────────────┐
│                 VPS-1 (DB)                   │
│                 Postgres                     │
└──────────────────────────────────────────────┘
```

Keep at least one node as `mode=all` so the scheduler runs. All `api` and `all` nodes serve the same traffic.

```
# Caddyfile for multi-node
app.143.dev {
    reverse_proxy vps-2:8080 vps-7:8080 {
        health_uri /healthz
        health_interval 10s
        lb_policy round_robin
    }
}
```

### Stage 5: Managed Postgres (Optional)

When operational overhead of self-hosted Postgres outweighs cost savings:

```bash
# 1. Take a final backup
docker exec 143-postgres-1 pg_dump -U onefortythree -Fc onefortythree > final.dump

# 2. Restore to managed service
pg_restore -h managed-db.provider.com -U onefortythree -d onefortythree final.dump

# 3. Update DATABASE_URL on all nodes and restart
```

No application code changes. The app only sees `DATABASE_URL`.

| Provider | HA Setup | Cost (4GB RAM) | Notes |
|----------|----------|----------------|-------|
| Supabase | Auto-failover | ~$25/mo | Managed Postgres, easy setup |
| Neon | Serverless, auto-scale | Pay-per-query | Good for variable workloads |
| Ubicloud | Open-source managed | ~$40/mo | Runs on Hetzner hardware |
| AWS RDS | Multi-AZ | ~$70/mo | Battle-tested |

### Stage 6: Automated Fleet Provisioning

Manual SSH + docker compose works for 3-5 nodes. Beyond that, automate with
cloud-init and the Hetzner API.

**cloud-init (Hetzner native, no dependencies):**

Every Hetzner VPS accepts a cloud-init user-data script at creation time. This
runs once on first boot and fully provisions the node.

```yaml
# deploy/cloud-init/worker.yml
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

  # Pull images from your registry
  - docker login ghcr.io -u deploy -p ${REGISTRY_TOKEN}
  - docker pull ghcr.io/assembledhq/143-server:latest
  - docker pull ghcr.io/assembledhq/143-sandbox:latest

  # Write the compose file
  - mkdir -p /opt/143
  - |
    cat > /opt/143/docker-compose.yml << 'COMPOSE'
    services:
      worker:
        image: ghcr.io/assembledhq/143-server:latest
        environment:
          DATABASE_URL: ${DATABASE_URL}
          MODE: worker
          NODE_ID: ${HOSTNAME}
          MAX_CONCURRENT_RUNS: ${MAX_CONCURRENT_RUNS}
          SANDBOX_IMAGE: ghcr.io/assembledhq/143-sandbox:latest
          SANDBOX_RUNTIME: runsc
          MEZMO_INGESTION_KEY: ${MEZMO_INGESTION_KEY}
          DD_API_KEY: ${DD_API_KEY}
        volumes:
          - /var/run/docker.sock:/var/run/docker.sock
        restart: unless-stopped
    COMPOSE

  # Start
  - cd /opt/143 && docker compose up -d

write_files:
  - path: /opt/143/.env
    content: |
      DATABASE_URL=${DATABASE_URL}
      MEZMO_INGESTION_KEY=${MEZMO_INGESTION_KEY}
      DD_API_KEY=${DD_API_KEY}
    permissions: '0600'
```

**Provisioning script** (`deploy/scripts/provision-worker.sh`):

```bash
#!/usr/bin/env bash
set -euo pipefail

# Provision a new worker node via Hetzner Cloud API
# Usage: ./provision-worker.sh [server-type] [location]

SERVER_TYPE="${1:-cx42}"
LOCATION="${2:-fsn1}"
WORKER_NAME="143-worker-$(date +%s)"

source /opt/143/.env.provisioning

USERDATA=$(envsubst < deploy/cloud-init/worker.yml)

RESPONSE=$(hcloud server create \
  --name "$WORKER_NAME" \
  --type "$SERVER_TYPE" \
  --image ubuntu-24.04 \
  --location "$LOCATION" \
  --network 143-private \
  --ssh-key deploy-key \
  --user-data "$USERDATA" \
  --label env=production \
  --label role=worker \
  --output json)

SERVER_ID=$(echo "$RESPONSE" | jq -r '.server.id')
SERVER_IP=$(echo "$RESPONSE" | jq -r '.server.public_net.ipv4.ip')
PRIVATE_IP=$(echo "$RESPONSE" | jq -r '.server.private_net[0].ip')

echo "Created $WORKER_NAME (ID: $SERVER_ID)"
echo "  Public IP:  $SERVER_IP"
echo "  Private IP: $PRIVATE_IP"
echo "Node will register itself in ~90 seconds."
```

**Decommission script** (`deploy/scripts/decommission-worker.sh`):

```bash
#!/usr/bin/env bash
set -euo pipefail

SERVER="$1"

# 1. Drain the node
SERVER_IP=$(hcloud server ip "$SERVER" --output noheader)
ssh deploy@"$SERVER_IP" "docker compose -f /opt/143/docker-compose.yml exec worker kill -SIGTERM 1"

echo "Draining $SERVER... waiting for in-progress jobs to complete."

# 2. Wait for the node to show as 'dead'
for i in $(seq 1 60); do
  STATUS=$(psql "$DATABASE_URL" -t -c "SELECT status FROM nodes WHERE host LIKE '%${SERVER}%'" | xargs)
  if [ "$STATUS" = "dead" ]; then
    echo "Node drained and marked dead."
    break
  fi
  sleep 5
done

# 3. Delete the VPS
hcloud server delete "$SERVER"
echo "Server $SERVER deleted."
```

Hetzner servers boot in ~20s, cloud-init completes in ~60s, worker accepts jobs within 90s.

**Move to Stage 7 when:** you're provisioning/decommissioning frequently enough that it's a chore.

### Stage 7: Auto-Scaling Workers

When queue depth exceeds capacity, automatically spin up more workers. The Go
server does this — no external orchestrator needed.

```go
// internal/autoscaler/autoscaler.go
//
// Runs as part of the scheduler (on whichever node holds the advisory lock).
// Checks queue depth every 60 seconds and adjusts the fleet.

type AutoScaler struct {
    db          *pgxpool.Pool
    hetzner     *hcloud.Client
    config      AutoScaleConfig
    logger      zerolog.Logger
}

type AutoScaleConfig struct {
    Enabled          bool          `env:"AUTOSCALE_ENABLED" envDefault:"false"`
    MinWorkers       int           `env:"AUTOSCALE_MIN_WORKERS" envDefault:"1"`
    MaxWorkers       int           `env:"AUTOSCALE_MAX_WORKERS" envDefault:"10"`
    ServerType       string        `env:"AUTOSCALE_SERVER_TYPE" envDefault:"cx42"`
    Location         string        `env:"AUTOSCALE_LOCATION" envDefault:"fsn1"`
    ScaleUpThreshold int           `env:"AUTOSCALE_SCALE_UP_THRESHOLD" envDefault:"5"`
    ScaleDownAfter   time.Duration `env:"AUTOSCALE_SCALE_DOWN_AFTER" envDefault:"15m"`
    CooldownPeriod   time.Duration `env:"AUTOSCALE_COOLDOWN" envDefault:"5m"`
    NetworkID        string        `env:"AUTOSCALE_NETWORK_ID"`
    RunsPerWorker    int           `env:"AUTOSCALE_RUNS_PER_WORKER" envDefault:"5"`
}

func (a *AutoScaler) Tick(ctx context.Context) {
    // 1. Count pending sandbox jobs
    var pendingJobs int
    a.db.QueryRow(ctx,
        "SELECT count(*) FROM jobs WHERE status = 'pending' AND job_type = 'agent_run'",
    ).Scan(&pendingJobs)

    // 2. Count active workers
    var activeWorkers int
    a.db.QueryRow(ctx,
        "SELECT count(*) FROM nodes WHERE mode = 'worker' AND status = 'active'",
    ).Scan(&activeWorkers)

    totalCapacity := activeWorkers * a.config.RunsPerWorker

    // 3. Scale up: more pending jobs than capacity
    if pendingJobs > a.config.ScaleUpThreshold && activeWorkers < a.config.MaxWorkers {
        needed := (pendingJobs / a.config.RunsPerWorker) + 1 - activeWorkers
        needed = min(needed, a.config.MaxWorkers-activeWorkers)
        for i := 0; i < needed; i++ {
            a.provisionWorker(ctx)
        }
        return
    }

    // 4. Scale down: idle workers
    if activeWorkers > a.config.MinWorkers {
        a.drainIdleWorkers(ctx)
    }
}
```

**Scale-up:** When pending `agent_run` jobs exceed threshold and worker count is below max.

**Scale-down:** Workers idle for > `AUTOSCALE_SCALE_DOWN_AFTER` get drained and destroyed. Never below `AUTOSCALE_MIN_WORKERS`.

**Cost control:** At €14/mo per CX42 (billed hourly at ~€0.02/hr), a worker that runs for 2 hours costs €0.04. Auto-scaling is effectively free compared to LLM API costs.

| Variable | Default | Description |
|----------|---------|-------------|
| `AUTOSCALE_ENABLED` | `false` | Enable auto-scaling |
| `AUTOSCALE_MIN_WORKERS` | `1` | Minimum worker count |
| `AUTOSCALE_MAX_WORKERS` | `10` | Maximum worker count (hard cap) |
| `AUTOSCALE_SERVER_TYPE` | `cx42` | Hetzner server type |
| `AUTOSCALE_LOCATION` | `fsn1` | Hetzner datacenter |
| `AUTOSCALE_SCALE_UP_THRESHOLD` | `5` | Pending jobs that trigger scale-up |
| `AUTOSCALE_SCALE_DOWN_AFTER` | `15m` | Idle time before drain |
| `AUTOSCALE_COOLDOWN` | `5m` | Min time between scale events |
| `AUTOSCALE_RUNS_PER_WORKER` | `5` | Concurrent runs per worker |
| `HCLOUD_TOKEN` | - | Hetzner Cloud API token |

### Stage 8: Postgres High Availability

At this scale, Postgres is the only single point of failure. If the DB VPS dies, everything stops. Two options:

**Option A: Streaming Replication (self-managed)**

```
┌──────────────────┐     ┌──────────────────┐
│  VPS-DB-1        │     │  VPS-DB-2        │
│  (Primary)       │────▶│  (Replica)       │
│                  │ WAL │                  │
│  Postgres        │     │  Postgres        │
│  PgBouncer       │     │  (read-only)     │
└──────────────────┘     └──────────────────┘
         ▲                        ▲
         │ writes                 │ reads
         │                        │
    ┌────┴────────────────────────┴────┐
    │          App / Worker nodes       │
    └──────────────────────────────────┘
```

- Primary handles all writes (job queue, agent run logs, webhooks)
- Replica handles read-heavy queries (dashboard, experiment evaluation, audit log queries)
- If Primary dies, promote Replica to Primary (manual or via [Patroni](https://github.com/patroni/patroni) for automatic failover)
- App uses two `DATABASE_URL`s: one for writes, one for reads

**Splitting reads and writes in the app:**

```go
type DBPool struct {
    Primary *pgxpool.Pool  // DATABASE_URL — all writes
    Replica *pgxpool.Pool  // DATABASE_REPLICA_URL — read-heavy queries
}

func (db *DBPool) ReadPool() *pgxpool.Pool {
    if db.Replica != nil {
        return db.Replica
    }
    return db.Primary
}
```

Dashboard queries, experiment reads, and audit log queries use `ReadPool()`. Job queue operations, writes, and anything requiring strong consistency use `Primary` directly.

**Option B: Managed Postgres with HA (simplest)**

Hetzner doesn't offer managed Postgres, but several providers do:

| Provider | HA Setup | Cost (4GB RAM) | Notes |
|----------|----------|----------------|-------|
| Supabase | Auto-failover | ~$25/mo | Managed Postgres, easy setup |
| Neon | Serverless, auto-scale | Pay-per-query | Good for variable workloads |
| Ubicloud | Open-source managed | ~$40/mo | Runs on Hetzner hardware |
| AWS RDS | Multi-AZ | ~$70/mo | Battle-tested |
| Crunchy Bridge | Managed HA | ~$50/mo | Postgres-focused, excellent support |

For this project, **self-managed streaming replication** (Option A) is the right fit until operational burden outweighs cost savings. The app code change (read/write splitting) is worth doing regardless — it's a one-time investment that works with any Postgres setup.

### When to Split What

| Signal | Action |
|--------|--------|
| Agent runs queuing for > 5 min | Add worker nodes (Stage 3) |
| Postgres CPU > 70% sustained | Move Postgres to its own VPS (Stage 2) |
| API p95 latency > 500ms under load | Add API nodes (Stage 4) |
| Disk I/O wait > 20% on shared VPS | Separate Postgres (Stage 2) |
| Spending > 2 hrs/month on Postgres ops | Consider managed Postgres (Stage 5) |
| Need 99.9%+ uptime SLA | Multiple API nodes + Postgres HA (Stage 4+8) |

### Connection Pooling (PgBouncer)

When you have many app/worker nodes, each opens a pool of connections to Postgres. At scale (10+ nodes), this can exhaust `max_connections`.

**When to add PgBouncer:** When total connections across all nodes approach 80% of `max_connections` (default 100). Each node's pgx pool defaults to ~10 connections, so with 8+ nodes you're getting close.

```yaml
# Add to docker-compose.db.yml on the Postgres VPS
services:
  pgbouncer:
    image: edoburu/pgbouncer:1.23.1
    environment:
      DATABASE_URL: postgres://onefortythree:${DB_PASSWORD}@postgres:5432/onefortythree
      POOL_MODE: transaction    # required for SKIP LOCKED and advisory locks
      MAX_CLIENT_CONN: 500
      DEFAULT_POOL_SIZE: 30
      RESERVE_POOL_SIZE: 5
    ports:
      - "0.0.0.0:6432:6432"
    depends_on:
      - postgres
    restart: unless-stopped
```

All app/worker nodes change their `DATABASE_URL` to point at PgBouncer (port 6432) instead of Postgres directly. PgBouncer multiplexes hundreds of client connections into 30 actual Postgres connections.

**Important:** Use `POOL_MODE=transaction` (not `session`). Session mode breaks `FOR UPDATE SKIP LOCKED` across transaction boundaries.

---

### Container Registry & CI/CD for Multi-Node

Once you have more than one node, you need a container registry and a fleet
deployment pipeline.

**Registry: GitHub Container Registry (ghcr.io)**

```yaml
# .github/workflows/build-and-push.yml
name: Build & Push Images
on:
  push:
    branches: [main]

env:
  REGISTRY: ghcr.io
  SERVER_IMAGE: ghcr.io/assembledhq/143-server
  SANDBOX_IMAGE: ghcr.io/assembledhq/143-sandbox

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

      - name: Build & push sandbox image
        uses: docker/build-push-action@v6
        with:
          context: .
          file: Dockerfile.sandbox
          push: true
          tags: |
            ${{ env.SANDBOX_IMAGE }}:latest
            ${{ env.SANDBOX_IMAGE }}:${{ github.sha }}
```

**Fleet deployment** (`deploy/scripts/deploy-fleet.sh`):

```bash
#!/usr/bin/env bash
set -euo pipefail

TAG="${1:-latest}"
SERVER_IMAGE="ghcr.io/assembledhq/143-server:$TAG"
SANDBOX_IMAGE="ghcr.io/assembledhq/143-sandbox:$TAG"

echo "Deploying $TAG to fleet..."

NODES=$(hcloud server list --selector env=production -o columns=name,ipv4 -o noheader)

while IFS=$'\t' read -r NAME IP; do
  echo "--- Deploying to $NAME ($IP) ---"

  ssh -o StrictHostKeyChecking=no deploy@"$IP" << REMOTE
    docker pull $SERVER_IMAGE
    docker pull $SANDBOX_IMAGE
    docker tag $SERVER_IMAGE ghcr.io/assembledhq/143-server:latest
    docker tag $SANDBOX_IMAGE ghcr.io/assembledhq/143-sandbox:latest
    cd /opt/143
    docker compose up -d --remove-orphans

    for i in \$(seq 1 30); do
      if docker compose exec -T worker wget -q -O /dev/null http://localhost:8080/healthz 2>/dev/null || \
         docker compose exec -T api wget -q -O /dev/null http://localhost:8080/healthz 2>/dev/null; then
        echo "Health check passed."
        break
      fi
      sleep 2
    done
REMOTE

  echo "$NAME deployed."
done <<< "$NODES"

echo "Fleet deployment complete."
```

**GitHub Actions deployment workflow:**

```yaml
# .github/workflows/deploy.yml
name: Deploy to Fleet
on:
  workflow_run:
    workflows: ["Build & Push Images"]
    types: [completed]
    branches: [main]

jobs:
  deploy:
    if: ${{ github.event.workflow_run.conclusion == 'success' }}
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: Install hcloud CLI
        run: |
          curl -sL https://github.com/hetznercloud/cli/releases/latest/download/hcloud-linux-amd64.tar.gz | tar xz
          sudo mv hcloud /usr/local/bin/

      - name: Deploy fleet
        env:
          HCLOUD_TOKEN: ${{ secrets.HCLOUD_TOKEN }}
        run: |
          chmod +x deploy/scripts/deploy-fleet.sh
          ./deploy/scripts/deploy-fleet.sh ${{ github.event.workflow_run.head_sha }}

      - name: Run migrations (on one API node)
        run: |
          API_IP=$(hcloud server list --selector role=api -o columns=ipv4 -o noheader | head -1)
          ssh deploy@"$API_IP" "cd /opt/143 && docker compose exec -T api ./server migrate up"
```

**Deployment strategy:**
- **Rolling deploy** — one node at a time, health check before moving on
- **Migrations run once** — on a single API node after all nodes are updated
- **Rollback** — re-deploy the previous git SHA: `./deploy-fleet.sh <previous-sha>`. Images are tagged by SHA so every version is available.

---

### Full Architecture at Scale

```
                    ┌─────────────────────────────────────┐
                    │       GitHub (source of truth)       │
                    │                                     │
                    │  push to main → build images →      │
                    │  push to GHCR → deploy to fleet     │
                    └──────────────┬──────────────────────┘
                                   │
                    ┌──────────────▼──────────────────────┐
                    │       Hetzner Load Balancer          │
                    │       (€6/mo, health-checked)        │
                    └──┬───────────┬───────────┬──────────┘
                       │           │           │
              ┌────────▼──┐ ┌─────▼─────┐ ┌───▼───────┐
              │  API-1    │ │  API-2    │ │  API-3    │
              │  mode=all │ │  mode=api │ │  mode=api │
              └─────┬─────┘ └─────┬─────┘ └─────┬────┘
                    │             │             │
       ┌────────────▼─────────────▼─────────────▼──────────┐
       │                   PgBouncer                        │
       │              (on DB VPS, port 6432)                │
       └────────────────────────┬───────────────────────────┘
                                │
                ┌───────────────▼───────────────┐
                │     Postgres Primary          │───── WAL ─────▶ Replica
                │     (dedicated VPS, 8GB+)     │───── WAL ─────▶ S3 (WAL-G)
                └───────────────────────────────┘
                                ▲
                                │
       ┌────────────────────────┼────────────────────────┐
       │                        │                        │
  ┌────▼────┐  ┌────────┐  ┌───▼────┐  ┌────────┐  ┌───▼────┐
  │Worker-1 │  │Worker-2│  │Worker-3│  │Worker-4│  │Worker-N│
  │ 5 runs  │  │ 5 runs │  │ 5 runs │  │ 5 runs │  │ auto   │
  │ (fixed) │  │ (fixed)│  │ (fixed)│  │ (fixed)│  │ scaled │
  └─────────┘  └────────┘  └────────┘  └────────┘  └────────┘

  ┌──────────────────────────────────────────────────────────┐
  │  Observability                                           │
  │  Datadog: metrics, APM traces, dashboards, alerts        │
  │  Mezmo: structured logs, log-based alerts                │
  │  S3: WAL archives, pg_dump backups, audit logs           │
  └──────────────────────────────────────────────────────────┘
```

### Capacity Planning

| Scale | VPSes | Monthly Cost (Hetzner) | Concurrent Agents | Repos | Stage |
|-------|-------|----------------------|-------------------|-------|-------|
| **Solo** | 1x CX42 (8CPU/16GB) | ~€14 | 3-5 | 1-10 | 1 |
| **Small team** | 2 VPSes (DB + App) | ~€22 | 3-5 | 5-15 | 2 |
| **Growing** | 4 VPSes (DB + App + 2 Workers) | ~€50 | 10-15 | 15-40 | 3 |
| **Busy** | 7 VPSes (DB + 2 API + 4 Workers) | ~€110 | 20-30 | 40-100 | 3-4 |
| **Large** | 12 VPSes (DB HA + 2 API + LB + 8 Workers) | ~€200 | 40-60 | 100-300 | 4+ |
| **Auto-scaled** | 2-20 VPSes (dynamic) | ~€30-400 | 5-100 (elastic) | 100+ | 7 |

**The dominant cost is LLM API, not infrastructure.** A single agent run costs $0.50-5.00 in Claude API tokens. The VPS to run it costs ~$0.02/hr. Don't under-provision to save $10/month.

| Category | % of Total Cost | Example (100 repos) |
|----------|----------------|---------------------|
| LLM API (Claude/GPT) | 80-90% | $2,000-10,000/mo |
| Infrastructure (Hetzner) | 5-10% | $100-200/mo |
| Observability (Datadog/Mezmo) | 3-5% | $50-100/mo |
| Backups (S3 storage) | < 1% | $5-10/mo |

---

## Appendix: Hybrid Architecture (Render + Hetzner)

If for any reason full migration is not feasible, a hybrid approach keeps the web
stack on Render and runs only agent nodes on Hetzner. This requires a
`RemoteDockerProvider` and cross-cloud networking (WireGuard or mTLS).

This is **not recommended** for initial deployment — it adds complexity with no
benefit over full migration. However, if you migrate to Hetzner (Phase 1) and
later want to move the web layer back to a PaaS, this architecture shows how to
keep agent nodes on Hetzner while running the API elsewhere.

### Cross-Cloud Connectivity

#### WireGuard Tunnel (Recommended)

WireGuard creates a point-to-point encrypted tunnel at the kernel level.

- Each peer gets a private IP on a shared subnet (e.g., `10.143.0.0/24`)
- Only one UDP port (51820) needs to be open on Hetzner's firewall
- ~3ms overhead, essentially line-speed
- Handles NAT traversal automatically

#### mTLS Over Public Internet (Simpler)

Agent API on Hetzner listens on port 443 with mutual TLS. Simpler but requires
firewall updates if the PaaS egress IPs change.

#### Tailscale (Zero-Config WireGuard)

Tailscale wraps WireGuard with identity-based access. Zero firewall config, ACLs
in a central dashboard. Adds a SaaS dependency.

### Remote Sandbox Provider

```go
// internal/services/agent/providers/remote.go
type RemoteDockerProvider struct {
    nodes      []NodeConfig
    httpClient *http.Client
    selector   NodeSelector      // round-robin, least-loaded, etc.
    logger     zerolog.Logger
}

type NodeConfig struct {
    ID       string // "hetzner-fsn1-01"
    Endpoint string // "https://10.143.0.2:9090"
    Capacity int    // max concurrent sandboxes
}
```

### Agent API (Runs on Hetzner Node)

A thin HTTP server (~500 lines) that wraps the Docker client:

```
POST   /v1/sandboxes              → Create
DELETE /v1/sandboxes/:id          → Destroy
POST   /v1/sandboxes/:id/exec    → Exec
POST   /v1/sandboxes/:id/stream  → ExecStream (SSE/WebSocket)
POST   /v1/sandboxes/:id/clone   → CloneRepo
GET    /v1/sandboxes/:id/files   → ReadFile
PUT    /v1/sandboxes/:id/files   → WriteFile
POST   /v1/sandboxes/:id/snapshot → Snapshot
POST   /v1/sandboxes/:id/restore  → Restore
GET    /v1/health                 → Health + capacity
```

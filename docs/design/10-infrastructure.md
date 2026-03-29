# Design: Infrastructure & Deployment

This document describes how 143.dev is packaged, deployed, and scaled.

## Design Principles

1. **One command to run** — `./setup.sh` gets you running without Docker; `docker compose up` for containerized setup
2. **Single container for small teams** — everything in one process for simplicity
3. **Symmetric nodes** — every node runs the same binary. No special "primary" node. Add API or worker capacity by starting more containers pointed at the same database
4. **No vendor lock-in** — standard Postgres, standard Docker, no proprietary cloud services required
5. **Observable by default** — structured logging via Mezmo and monitoring via Datadog built in from day one

## Quick Start (No Docker)

The fastest way to get 143.dev running locally — one command, no Docker required:

```bash
git clone https://github.com/assembledhq/143.git && cd 143 && ./setup.sh
```

`setup.sh` handles everything automatically:

1. **Checks prerequisites** — Go, Node.js, PostgreSQL
2. **Installs missing tools** — via Homebrew (macOS) or apt (Linux)
3. **Creates the database** — sets up the `onefortythree` Postgres database and user
4. **Generates `.env`** — creates a development config with sensible defaults
5. **Installs dependencies** — `go mod download` + `npm install`
6. **Runs migrations** — applies the latest schema

After setup completes, start the services:

```bash
# Option A: start both with make
make dev

# Option B: start individually
go run cmd/server/main.go          # API on :8080
cd frontend && npm run dev         # UI  on :3000
```

### Prerequisites

The setup script installs these if missing, or you can install them manually:

| Tool | Minimum Version | Install |
|------|----------------|---------|
| Go | 1.23+ | `brew install go` / `apt install golang` |
| Node.js | 20+ | `brew install node` / `apt install nodejs npm` |
| PostgreSQL | 15+ | `brew install postgresql@17` / `apt install postgresql` |

### Why No Docker for Local Dev?

Docker Compose remains the recommended approach for production and CI. The non-Docker path exists because:

- **Faster iteration** — native hot-reload with `air` and `next dev` is faster than container rebuilds
- **Lower barrier** — contributors don't need Docker installed to send their first PR
- **Simpler debugging** — native tools, native debuggers, no container networking issues

## Local Development (Docker)

### Docker Compose

```yaml
# docker-compose.yml
version: "3.8"

services:
  postgres:
    image: postgres:18
    environment:
      POSTGRES_DB: onefortythree
      POSTGRES_USER: onefortythree
      POSTGRES_PASSWORD: dev
    ports:
      - "5432:5432"
    volumes:
      - pgdata:/var/lib/postgresql/data

  server:
    build:
      context: .
      dockerfile: Dockerfile
    ports:
      - "8080:8080"
    environment:
      DATABASE_URL: postgres://onefortythree:dev@postgres:5432/onefortythree?sslmode=disable
      PORT: 8080
      LOG_LEVEL: debug
      SANDBOX_IMAGE: 143-sandbox:latest
    depends_on:
      - postgres
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock  # for sandbox containers (dev only — use docker-proxy in production)

  frontend:
    build:
      context: ./frontend
      dockerfile: Dockerfile.dev
    ports:
      - "3000:3000"
    environment:
      NEXT_PUBLIC_API_URL: http://localhost:8080
    volumes:
      - ./frontend:/app
      - /app/node_modules

volumes:
  pgdata:
```

### Development Workflow

```bash
# Start everything
docker compose up

# Go backend hot-reload (using air)
# Install: go install github.com/air-verse/air@latest
cd cmd/server && air

# Frontend hot-reload (Next.js dev server)
cd frontend && npm run dev

# Run migrations
go run cmd/migrate/main.go up

# Run tests
go test ./...
cd frontend && npm test
```

### Makefile

```makefile
.PHONY: dev setup test migrate

setup:
	docker compose up -d postgres
	go run cmd/migrate/main.go up
	cd frontend && npm install

dev:
	docker compose up

test:
	go test ./...
	cd frontend && npm test

migrate:
	go run cmd/migrate/main.go up

migrate-down:
	go run cmd/migrate/main.go down 1

build:
	docker build -t 143-server .
	docker build -t 143-sandbox -f Dockerfile.sandbox .
```

## Production Deployment

### Self-Hosted (Single Machine)

The simplest production setup — everything in one `docker compose`:

```bash
# Clone the repo
git clone https://github.com/assembledhq/143.git
cd 143

# Configure
cp .env.example .env
# Edit .env with your API keys, secrets, etc.

# Launch
docker compose -f docker-compose.prod.yml up -d
```

`docker-compose.prod.yml` differs from dev:

- Postgres uses a persistent named volume
- Server runs in production mode (no hot reload)
- Frontend is pre-built and served by the Go server
- TLS termination via a reverse proxy (Caddy or Traefik)
- Docker socket access via [docker-socket-proxy](https://github.com/Tecnativa/docker-socket-proxy) instead of direct socket mount (see [20-security-architecture.md](20-security-architecture.md))
- Server container runs with `security_opt: [no-new-privileges:true]`, `read_only: true`
- Database connection uses `sslmode=verify-full` with TLS

### Dockerfiles

**Server Dockerfile**:

```dockerfile
# Build stage
FROM golang:1.26-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o server ./cmd/server

# Frontend build stage
FROM node:24-alpine AS frontend
WORKDIR /app
COPY frontend/package*.json ./
RUN npm ci
COPY frontend/ .
RUN npm run build

# Runtime stage
FROM alpine:3.23
RUN apk add --no-cache ca-certificates docker-cli
WORKDIR /app
COPY --from=builder /app/server .
COPY --from=builder /app/migrations ./migrations
COPY --from=frontend /app/out ./static
EXPOSE 8080
CMD ["./server"]
```

**Sandbox Dockerfile**:

```dockerfile
FROM ubuntu:24.04@sha256:PINNED_DIGEST  # Pin by digest — tags are mutable

# Install common dev tools
RUN apt-get update && apt-get install -y \
    git curl wget \
    build-essential \
    nodejs npm \
    python3 python3-pip \
    golang-go \
    && rm -rf /var/lib/apt/lists/*

# Install agent CLIs
RUN npm install -g @anthropic-ai/claude-code

# Non-root user for sandbox execution
RUN useradd -m -s /bin/bash sandbox
USER sandbox
WORKDIR /workspace
```

This image is used by the Docker sandbox provider. It runs under **gVisor** (`runsc` runtime) by default for syscall-level isolation. The same image works with both `runsc` (gVisor) and `runc` (standard Docker) — no image changes needed when switching runtimes.

**gVisor Setup (Production)**:

gVisor must be installed on Linux hosts that run worker nodes. On systems where gVisor is unavailable (macOS, non-KVM hosts), the provider falls back to `runc` with a logged warning.

```bash
# Install gVisor on the worker node
curl -fsSL https://gvisor.dev/archive.key | sudo gpg --dearmor -o /usr/share/keyrings/gvisor-archive-keyring.gpg
echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/gvisor-archive-keyring.gpg] https://storage.googleapis.com/gvisor/releases release main" | sudo tee /etc/apt/sources.list.d/gvisor.list > /dev/null
sudo apt-get update && sudo apt-get install -y runsc

# Register runsc as a Docker runtime
sudo runsc install
sudo systemctl restart docker

# Verify
docker run --runtime=runsc hello-world
```

### Horizontal Scaling (Multi-Machine)

Every 143.dev node runs the same binary. There is no special "primary" or "leader" — any node can serve API traffic, process jobs, or both. Postgres is the only coordination layer. To add capacity, start more containers on new machines pointed at the same database.

#### Architecture

```
              ┌─────────────────────────────────────┐
              │           Load Balancer              │
              │           (Caddy / Nginx)            │
              └───┬──────────┬──────────┬───────────┘
                  │          │          │
         ┌────────▼───┐ ┌───▼────────┐ ┌▼───────────┐
         │  Node A    │ │  Node B    │ │  Node C    │
         │ --mode=all │ │ --mode=api │ │ --mode=api │
         │            │ │            │ │            │
         │ API+Worker │ │ API only   │ │ API only   │
         │ +Scheduler │ │            │ │            │
         └─────┬──────┘ └─────┬──────┘ └─────┬──────┘
               │              │              │
    ┌──────────▼──────────────▼──────────────▼────────┐
    │                   PostgreSQL                     │
    │         (shared state — the only                 │
    │          coordination point)                     │
    └──────┬──────────────────────────┬───────────────┘
           │                          │
  ┌────────▼─────────┐    ┌──────────▼─────────┐
  │  Node D          │    │  Node E            │
  │  --mode=worker   │    │  --mode=worker     │
  │                  │    │                    │
  │  Job processing  │    │  Job processing    │
  │  Agent sandboxes │    │  Agent sandboxes   │
  └──────────────────┘    └────────────────────┘
```

All nodes are peers. Any node running `--mode=all` does everything. You can split roles for isolation and scaling — API nodes for HTTP throughput, worker nodes for agent compute — but no node is special.

#### Node Modes

The Go server supports a `--mode` flag that determines which components run:

| Mode | Components | When to Use |
|------|-----------|-------------|
| `all` | API + workers + scheduler candidate + UI | Default. Single-machine or small setup. Every `all` node is identical. |
| `api` | API + UI only | Horizontal API capacity behind a load balancer. Stateless — add as many as needed. |
| `worker` | Job processing + sandbox execution only | Horizontal compute capacity for agent runs. Add machines when agent runs queue up. |

```go
switch config.Mode {
case "all":
    startAPIServer()
    startScheduler()     // competes for advisory lock — only one wins
    startWorkerLoop()
    serveStaticUI()
case "api":
    startAPIServer()
    serveStaticUI()
case "worker":
    startWorkerLoop()    // no HTTP, just processes jobs
}
```

#### Scheduler Leader Election (No Primary Needed)

The scheduler (which enqueues periodic jobs like `ingest_sync` and `evaluate_experiment`) uses a **Postgres advisory lock** for leader election. Any node that runs the scheduler component (`--mode=all`) attempts to acquire the lock. Only one succeeds — the rest wait.

```go
func (s *Scheduler) Run(ctx context.Context) {
    for {
        // Try to acquire advisory lock (non-blocking)
        acquired, _ := s.db.TryAdvisoryLock(ctx, schedulerLockID)
        if acquired {
            s.runScheduleLoop(ctx) // enqueue periodic jobs
            // Lock is held until this node releases it or disconnects
        }
        // If not acquired, another node has it — sleep and retry
        time.Sleep(10 * time.Second)
    }
}
```

- If the lock holder dies, Postgres automatically releases the advisory lock (connection closes).
- Another node acquires it within 10 seconds.
- No manual intervention needed — zero-downtime failover.
- You can run 1 or 100 `--mode=all` nodes and the scheduler just works.

#### Node Registration & Health

Every node registers itself in a `nodes` table on startup and sends periodic heartbeats. This is for dashboard visibility and dead node cleanup — not for coordination.

```sql
CREATE TABLE nodes (
    id            text PRIMARY KEY,           -- hostname or UUID
    mode          text NOT NULL,              -- all, api, worker
    host          text NOT NULL,              -- reachable address
    started_at    timestamptz NOT NULL,
    last_heartbeat_at timestamptz NOT NULL,
    status        text NOT NULL DEFAULT 'active',  -- active, draining, dead
    metadata      jsonb                       -- version, CPU count, memory, active sandbox count
);
```

- Heartbeat interval: every 30 seconds (update `last_heartbeat_at` + `metadata`)
- A node is considered `dead` if no heartbeat for 2 minutes
- Any node with worker capability periodically scans for jobs locked by dead nodes and re-queues them to `pending`
- The dashboard shows a cluster health panel (nodes, their roles, status, resource usage)

#### Adding Nodes

Every node just needs the `DATABASE_URL`. Point it at the shared Postgres and choose a mode.

**Add a worker node** (more agent run capacity):

```bash
docker run -d \
  -e DATABASE_URL=postgres://onefortythree:pass@db-host:5432/onefortythree \
  -e MODE=worker \
  -e NODE_ID=worker-2 \
  -v /var/run/docker.sock:/var/run/docker.sock \
  143-server:latest
```

Or with Docker Compose on the new machine:

```yaml
# docker-compose.worker.yml
version: "3.8"

services:
  worker:
    image: 143-server:latest
    environment:
      DATABASE_URL: postgres://onefortythree:pass@db-host:5432/onefortythree
      MODE: worker
      NODE_ID: ${HOSTNAME}
      SANDBOX_IMAGE: 143-sandbox:latest
      MEZMO_INGESTION_KEY: ${MEZMO_INGESTION_KEY}
      DD_API_KEY: ${DD_API_KEY}
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
    restart: unless-stopped
```

**Add an API node** (more HTTP throughput):

```yaml
# docker-compose.api.yml
version: "3.8"

services:
  api:
    image: 143-server:latest
    ports:
      - "8080:8080"
    environment:
      DATABASE_URL: postgres://onefortythree:pass@db-host:5432/onefortythree
      MODE: api
      NODE_ID: ${HOSTNAME}
      MEZMO_INGESTION_KEY: ${MEZMO_INGESTION_KEY}
      DD_API_KEY: ${DD_API_KEY}
    restart: unless-stopped
```

**Add another "all" node** (full redundancy):

```bash
docker run -d \
  -e DATABASE_URL=postgres://onefortythree:pass@db-host:5432/onefortythree \
  -e MODE=all \
  -e NODE_ID=node-2 \
  -e PORT=8080 \
  -p 8080:8080 \
  -v /var/run/docker.sock:/var/run/docker.sock \
  143-server:latest
```

Both `all` nodes serve API traffic and process jobs. Only one runs the scheduler (advisory lock). If node-1 dies, node-2 takes over scheduling within seconds. No reconfiguration needed.

API nodes are stateless — they share sessions via the Postgres session table, so any node can serve any request.

#### Job Queue Distribution

The Postgres-backed job queue naturally distributes across all worker-capable nodes:

```sql
-- Each worker polls for jobs with SELECT ... FOR UPDATE SKIP LOCKED
-- This ensures no two workers pick up the same job
SELECT id, job_type, payload
FROM jobs
WHERE status = 'pending'
  AND run_after <= now()
ORDER BY created_at
LIMIT 1
FOR UPDATE SKIP LOCKED;
```

- Workers poll every 1 second (configurable via `WORKER_POLL_INTERVAL`)
- Each worker processes up to `MAX_CONCURRENT_RUNS` sandbox jobs concurrently
- Non-sandbox jobs (ingest, prioritize, open_pr) are lightweight and processed immediately
- Job affinity is not required — any worker can process any job type
- If a worker dies mid-job, the dead node cleanup process re-queues its locked jobs

#### Scaling Guidance

| Scale | Setup | Notes |
|-------|-------|-------|
| Small (1-5 repos) | 1 `all` node | Default. Everything in one container. |
| Medium (5-20 repos) | 2 `all` nodes + 1-3 `worker` nodes | Two `all` nodes for API redundancy + scheduler failover. Workers for compute. |
| Large (20+ repos) | N `api` behind LB + M `worker` nodes | Dedicated roles. Move Postgres to managed service. At least 1 `all` node (or separate scheduler process) for cron. |

#### Scaling Playbook

This is the step-by-step path from a single VPS to a multi-node cluster. Each stage builds on the previous one — don't skip ahead. You should only move to the next stage when you're hitting the limits of the current one.

##### Stage 1: Single VPS (where you start)

Everything runs on one machine via docker compose. This handles more than you'd expect — a single 4-core / 8GB VPS can run the API, 3 concurrent agent sandboxes, and Postgres comfortably.

```
┌─────────────────────────────────────────┐
│  VPS-1 (4 CPU / 8 GB)                  │
│                                         │
│  ┌──────────┐  ┌──────────┐            │
│  │ Postgres │  │  Server  │            │
│  │          │  │ mode=all │            │
│  └──────────┘  └──────────┘            │
│                ┌──────────┐            │
│                │ Caddy/LB │            │
│                └──────────┘            │
└─────────────────────────────────────────┘
```

```yaml
# docker-compose.prod.yml — this is your entire production stack
services:
  postgres:
    image: postgres:18
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
      - "127.0.0.1:5432:5432"   # only bind to localhost — no external access
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U onefortythree"]
      interval: 10s
      timeout: 5s
      retries: 5
    restart: unless-stopped
    deploy:
      resources:
        limits:
          memory: 1G
          cpus: "1.0"

  server:
    image: 143-server:latest
    environment:
      DATABASE_URL: postgres://onefortythree:${DB_PASSWORD}@postgres:5432/onefortythree?sslmode=disable
      MODE: all
      PORT: "8080"
      NODE_ID: ${HOSTNAME:-node-1}
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
          memory: 1G
          cpus: "2.0"

  caddy:
    image: caddy:2
    ports:
      - "80:80"
      - "443:443"
    volumes:
      - ./deploy/Caddyfile:/etc/caddy/Caddyfile:ro
      - caddy_data:/data
    restart: unless-stopped

volumes:
  pgdata:
  caddy_data:
```

**Move to Stage 2 when:** agent runs are queuing up (check `jobs.queue.depth` metric), or you need the API to stay responsive while heavy agent runs are consuming CPU/memory on the same box.

##### Stage 2: Split Postgres to Its Own VPS

**Yes — Postgres should get its own dedicated node.** This is the single most impactful scaling move because:

1. **Resource isolation** — agent sandboxes are CPU/memory hungry. A sandbox spike won't starve Postgres of resources and cause query timeouts or connection drops.
2. **Independent backups** — backup scripts (pg_dump, WAL archiving) run without competing with the application for I/O.
3. **Independent upgrades** — you can upgrade the app server or restart Docker without touching the database.
4. **Foundation for everything after** — every subsequent stage assumes Postgres is on its own machine. Do this early.

```
┌──────────────────────┐     ┌──────────────────────┐
│  VPS-1 (DB)          │     │  VPS-2 (App)         │
│  4 CPU / 8 GB        │     │  4 CPU / 8 GB        │
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
    image: postgres:18
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
      - "0.0.0.0:5432:5432"    # accessible from other VPSs
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U onefortythree"]
      interval: 10s
      timeout: 5s
      retries: 5
    restart: unless-stopped
    deploy:
      resources:
        limits:
          memory: 4G            # give Postgres most of the RAM
          cpus: "3.0"

volumes:
  pgdata:
```

```bash
# 2. Migrate data from the old VPS
# On the OLD VPS:
docker exec 143-postgres-1 pg_dump -U onefortythree -Fc onefortythree > /tmp/143.dump

# Copy to the new VPS:
scp /tmp/143.dump db-vps:/tmp/143.dump

# On the NEW VPS — start Postgres and restore:
docker compose -f docker-compose.db.yml up -d
docker exec -i 143-postgres-1 pg_restore -U onefortythree -d onefortythree --clean --if-exists < /tmp/143.dump

# 3. Secure the connection
# Option A: Hetzner private network (recommended — free, no encryption overhead)
#   Both VPSs on the same Hetzner private network. Postgres listens on the private IP.
#   DATABASE_URL uses the private IP (e.g., 10.0.0.2)
#
# Option B: Public IP + firewall
#   Firewall Postgres port to only accept connections from the app VPS IP.
#   Use sslmode=verify-full in DATABASE_URL.

# === On the APP VPS (VPS-2) ===

# 4. Update DATABASE_URL to point at the DB VPS
# In .env or .env.production:
DATABASE_URL=postgres://onefortythree:${DB_PASSWORD}@10.0.0.2:5432/onefortythree?sslmode=disable
# (use private IP if on Hetzner private network, sslmode=disable is fine over private network)

# 5. Remove Postgres from the app compose file and restart
docker compose up -d
```

**Networking:** If you're on Hetzner, put both VPSs in the same [private network](https://docs.hetzner.com/cloud/networks/getting-started/creating-a-network/) (free, ~2Gbps, no encryption needed). Postgres listens on the private IP. The firewall blocks port 5432 on the public interface.

**Move to Stage 3 when:** you need more concurrent agent runs than one VPS can handle (typically > 3-5 concurrent sandboxes depending on VPS size).

##### Stage 3: Add Dedicated Worker Nodes

Worker nodes run agent sandboxes — the most resource-intensive part of the system. Each worker VPS can run `MAX_CONCURRENT_RUNS` sandboxes in parallel. Adding a worker is a 5-minute operation.

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
# Install gVisor (see gVisor Setup section above)

# 2. Pull the images
docker pull 143-server:latest
docker pull 143-sandbox:latest

# 3. Create the compose file
```

```yaml
# /opt/143/docker-compose.worker.yml
services:
  worker:
    image: 143-server:latest
    environment:
      DATABASE_URL: postgres://onefortythree:${DB_PASSWORD}@10.0.0.2:5432/onefortythree?sslmode=disable
      MODE: worker
      NODE_ID: ${HOSTNAME}
      MAX_CONCURRENT_RUNS: 5
      SANDBOX_IMAGE: 143-sandbox:latest
      SANDBOX_RUNTIME: runsc
    env_file:
      - .env
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
    restart: unless-stopped
    deploy:
      resources:
        limits:
          memory: 2G    # for the server process itself
          cpus: "1.0"   # sandboxes get their own resource limits
```

```bash
# 4. Start it
docker compose -f docker-compose.worker.yml up -d

# That's it. The worker registers itself in the nodes table,
# starts polling for jobs, and picks up work immediately.
```

**Sizing worker VPSs:** Each sandbox gets `SANDBOX_CPU_LIMIT` (default 2) cores and `SANDBOX_MEMORY_LIMIT` (default 4GB) memory. For 5 concurrent runs, you want at least 12 CPU / 24 GB RAM on the worker VPS (some headroom for the worker process and OS).

| Worker VPS size | `MAX_CONCURRENT_RUNS` | Good for |
|----------------|----------------------|----------|
| 4 CPU / 8 GB | 1-2 | Small/test |
| 8 CPU / 16 GB | 3 | Medium |
| 16 CPU / 32 GB | 5-7 | Production sweet spot |
| 32 CPU / 64 GB | 10-15 | Heavy workloads |

**Move to Stage 4 when:** you need API redundancy (uptime SLA), or a single API node can't keep up with webhook volume.

##### Stage 4: Multiple API Nodes Behind a Load Balancer

API nodes are stateless — sessions live in Postgres. Add as many as you need behind Caddy, nginx, or a cloud load balancer.

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

At this point your Caddy VPS (or a Hetzner Load Balancer — €6/mo) distributes traffic across API nodes. Keep at least one node as `mode=all` so the scheduler runs. All `api` and `all` nodes serve the same traffic.

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

##### Stage 5: Managed Postgres (Optional)

At some point the operational overhead of self-hosted Postgres (backups, monitoring, upgrades, failover) may outweigh the cost savings. Migration is straightforward:

```bash
# 1. Take a final backup
docker exec 143-postgres-1 pg_dump -U onefortythree -Fc onefortythree > final.dump

# 2. Restore to managed service
pg_restore -h managed-db.provider.com -U onefortythree -d onefortythree final.dump

# 3. Update DATABASE_URL on all nodes
DATABASE_URL=postgres://onefortythree:pass@managed-db.provider.com:5432/onefortythree?sslmode=verify-full

# 4. Restart all nodes
# On each VPS: docker compose up -d

# 5. Decommission the DB VPS
```

No application code changes. The app only sees `DATABASE_URL`.

##### Stage 6: Automated Fleet Provisioning

Manual SSH + docker compose works for 3-5 nodes. Beyond that, you need provisioning automation — a script that spins up a new worker in minutes without you logging in.

**cloud-init (Hetzner native, no dependencies):**

Every Hetzner VPS accepts a cloud-init user-data script at creation time. This runs once on first boot and fully provisions the node.

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
# Example: ./provision-worker.sh cx42 fsn1

SERVER_TYPE="${1:-cx42}"
LOCATION="${2:-fsn1}"
WORKER_NAME="143-worker-$(date +%s)"

# Load secrets
source /opt/143/.env.provisioning

# Render cloud-init template with secrets
USERDATA=$(envsubst < deploy/cloud-init/worker.yml)

# Create the server
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
echo "  Type:       $SERVER_TYPE"
echo "  Location:   $LOCATION"
echo ""
echo "Node will register itself in ~90 seconds (cloud-init + image pull)."
echo "Monitor: SELECT * FROM nodes WHERE id = '$WORKER_NAME';"
```

**Decommission script** (`deploy/scripts/decommission-worker.sh`):

```bash
#!/usr/bin/env bash
set -euo pipefail

# Gracefully decommission a worker node
# Usage: ./decommission-worker.sh <server-name-or-id>

SERVER="$1"

# 1. Drain the node (let it finish current work)
SERVER_IP=$(hcloud server ip "$SERVER" --output noheader)
ssh deploy@"$SERVER_IP" "docker compose -f /opt/143/docker-compose.yml exec worker kill -SIGTERM 1"

echo "Draining $SERVER... waiting for in-progress jobs to complete."

# 2. Wait for the node to show as 'dead' in the nodes table (up to SHUTDOWN_TIMEOUT)
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

This takes node provisioning from "SSH in and set stuff up" to a single command. Hetzner servers boot in ~20 seconds, cloud-init completes in ~60 seconds, and the worker is accepting jobs within 90 seconds.

**Move to Stage 7 when:** you're provisioning/decommissioning workers frequently enough that doing it manually is a chore (more than a few times a week), or you want capacity to automatically respond to demand.

##### Stage 7: Auto-Scaling Workers

When queue depth exceeds capacity, automatically spin up more workers. When demand drops, drain and destroy them. The Go server does this — no external orchestrator needed.

**How it works:**

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
    ScaleUpThreshold int           `env:"AUTOSCALE_SCALE_UP_THRESHOLD" envDefault:"5"`   // pending jobs
    ScaleDownAfter   time.Duration `env:"AUTOSCALE_SCALE_DOWN_AFTER" envDefault:"15m"`   // idle time before removal
    CooldownPeriod   time.Duration `env:"AUTOSCALE_COOLDOWN" envDefault:"5m"`            // min time between scale events
    NetworkID        string        `env:"AUTOSCALE_NETWORK_ID"`                          // Hetzner private network ID
    RunsPerWorker    int           `env:"AUTOSCALE_RUNS_PER_WORKER" envDefault:"5"`      // MAX_CONCURRENT_RUNS per worker
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

    // 3. Scale up: more pending jobs than capacity can absorb
    if pendingJobs > a.config.ScaleUpThreshold && activeWorkers < a.config.MaxWorkers {
        needed := (pendingJobs / a.config.RunsPerWorker) + 1 - activeWorkers
        needed = min(needed, a.config.MaxWorkers-activeWorkers)
        for i := 0; i < needed; i++ {
            a.provisionWorker(ctx)
        }
        return
    }

    // 4. Scale down: workers with no active runs for > ScaleDownAfter
    if activeWorkers > a.config.MinWorkers {
        a.drainIdleWorkers(ctx)
    }
}
```

**Scale-up policy:** When pending `agent_run` jobs exceed `AUTOSCALE_SCALE_UP_THRESHOLD` (default 5) and current worker count is below `AUTOSCALE_MAX_WORKERS`, provision enough workers to absorb the backlog. Each worker handles `AUTOSCALE_RUNS_PER_WORKER` concurrent runs.

**Scale-down policy:** When a worker has had zero active sandbox runs for longer than `AUTOSCALE_SCALE_DOWN_AFTER` (default 15 minutes), drain it (SIGTERM → wait for completion → delete VPS). Never scale below `AUTOSCALE_MIN_WORKERS`.

**Cooldown:** At least `AUTOSCALE_COOLDOWN` (default 5 minutes) between scale events to avoid thrashing.

**Cost control:** `AUTOSCALE_MAX_WORKERS` is your hard cap. At €14/mo per CX42 (billed hourly at ~€0.02/hr), a worker that runs for 2 hours to handle a spike costs €0.04. Auto-scaling is effectively free compared to the LLM API costs of the agent runs themselves.

**Environment variables:**

| Variable | Default | Description |
|----------|---------|-------------|
| `AUTOSCALE_ENABLED` | `false` | Enable auto-scaling |
| `AUTOSCALE_MIN_WORKERS` | `1` | Minimum worker count (never scale below) |
| `AUTOSCALE_MAX_WORKERS` | `10` | Maximum worker count (hard cap) |
| `AUTOSCALE_SERVER_TYPE` | `cx42` | Hetzner server type for new workers |
| `AUTOSCALE_LOCATION` | `fsn1` | Hetzner datacenter location |
| `AUTOSCALE_SCALE_UP_THRESHOLD` | `5` | Pending jobs that trigger scale-up |
| `AUTOSCALE_SCALE_DOWN_AFTER` | `15m` | Idle time before a worker is drained |
| `AUTOSCALE_COOLDOWN` | `5m` | Minimum time between scale events |
| `AUTOSCALE_NETWORK_ID` | - | Hetzner private network ID |
| `AUTOSCALE_RUNS_PER_WORKER` | `5` | `MAX_CONCURRENT_RUNS` for provisioned workers |
| `HCLOUD_TOKEN` | - | Hetzner Cloud API token |

**Move to Stage 8 when:** you need high-availability Postgres (zero-downtime failover), or your database is large enough that single-node Postgres is a SPOF you can't tolerate.

##### Stage 8: Postgres High Availability

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
         │ writes                 │ reads (dashboard, experiments)
         │                        │
    ┌────┴────────────────────────┴────┐
    │          App / Worker nodes       │
    └──────────────────────────────────┘
```

- Primary handles all writes (job queue, agent run logs, webhooks)
- Replica handles read-heavy queries (dashboard, experiment evaluation, audit log queries)
- If Primary dies, promote Replica to Primary (manual or via Patroni for automatic failover)
- App uses two `DATABASE_URL`s: one for writes, one for reads

**Splitting reads and writes in the app:**

```go
type DBPool struct {
    Primary *pgxpool.Pool  // DATABASE_URL — all writes
    Replica *pgxpool.Pool  // DATABASE_REPLICA_URL — read-heavy queries (optional, falls back to Primary)
}

// Use Replica for read-heavy, latency-tolerant queries
func (db *DBPool) ReadPool() *pgxpool.Pool {
    if db.Replica != nil {
        return db.Replica
    }
    return db.Primary
}
```

Dashboard queries, experiment metric reads, and audit log queries use `ReadPool()`. Job queue operations, writes, and anything requiring strong consistency use `Primary` directly.

**Option B: Managed Postgres with HA (simplest)**

Hetzner doesn't offer managed Postgres, but several providers do:

| Provider | HA Setup | Cost (4GB RAM) | Notes |
|----------|----------|----------------|-------|
| Supabase | Auto-failover | ~$25/mo | Managed Postgres, easy setup |
| Neon | Serverless, auto-scale | Pay-per-query | Good for variable workloads |
| Ubicloud | Open-source managed | ~$40/mo | Runs on Hetzner hardware |
| AWS RDS | Multi-AZ | ~$70/mo | More expensive but battle-tested |
| Crunchy Bridge | Managed HA | ~$50/mo | Postgres-focused, excellent support |

For this project, **self-managed streaming replication** (Option A) is the right fit until operational burden outweighs cost savings. The app code change (read/write splitting) is worth doing regardless — it's a one-time investment that works with any Postgres setup.

#### Capacity Planning Reference

**Concrete numbers at different scales:**

| Scale | VPSes | Monthly Cost (Hetzner) | Concurrent Agents | Repos Supported | Setup |
|-------|-------|----------------------|-------------------|-----------------|-------|
| **Solo** | 1x CX32 (4CPU/8GB) | ~€8 | 2-3 | 1-5 | Stage 1 |
| **Small team** | 2 VPSes (DB + App) | ~€20 | 3-5 | 5-15 | Stage 2 |
| **Growing** | 4 VPSes (DB + App + 2 Workers) | ~€50 | 10-15 | 15-40 | Stage 3 |
| **Busy** | 7 VPSes (DB + 2 API + 4 Workers) | ~€110 | 20-30 | 40-100 | Stage 3-4 |
| **Large** | 12 VPSes (DB HA + 2 API + LB + 8 Workers) | ~€200 | 40-60 | 100-300 | Stage 4+ |
| **Auto-scaled** | 2-20 VPSes (dynamic) | ~€30-400 | 5-100 (elastic) | 100+ | Stage 7 |

**The dominant cost is LLM API, not infrastructure.** A single agent run costs $0.50-5.00 in Claude API tokens. The VPS to run it costs ~$0.02/hr. Infrastructure is rounding error compared to LLM spend — so don't under-provision to save $10/month.

**Where the money actually goes at scale:**

| Category | % of Total Cost | Example (100 repos) |
|----------|----------------|---------------------|
| LLM API (Claude/GPT) | 80-90% | $2,000-10,000/mo |
| Infrastructure (Hetzner) | 5-10% | $100-200/mo |
| Observability (Datadog/Mezmo) | 3-5% | $50-100/mo |
| Backups (S3 storage) | < 1% | $5-10/mo |

#### Container Registry & CI/CD for Multi-Node

When you have more than one node, you need a container registry (so workers can pull the latest images) and a deployment pipeline that updates the fleet.

**Registry: GitHub Container Registry (ghcr.io) — free for public repos, included with GitHub Pro**

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

# Deploy to all nodes in the fleet.
# Usage: ./deploy-fleet.sh [image-tag]
# Example: ./deploy-fleet.sh abc123f

TAG="${1:-latest}"
SERVER_IMAGE="ghcr.io/assembledhq/143-server:$TAG"
SANDBOX_IMAGE="ghcr.io/assembledhq/143-sandbox:$TAG"

echo "Deploying $TAG to fleet..."

# Get all active nodes from Hetzner
NODES=$(hcloud server list --selector env=production -o columns=name,ipv4 -o noheader)

# Deploy to each node (rolling — one at a time)
while IFS=$'\t' read -r NAME IP; do
  echo "--- Deploying to $NAME ($IP) ---"

  ssh -o StrictHostKeyChecking=no deploy@"$IP" << REMOTE
    # Pull new images
    docker pull $SERVER_IMAGE
    docker pull $SANDBOX_IMAGE

    # Tag as latest locally so compose file picks them up
    docker tag $SERVER_IMAGE ghcr.io/assembledhq/143-server:latest
    docker tag $SANDBOX_IMAGE ghcr.io/assembledhq/143-sandbox:latest

    # Rolling restart
    cd /opt/143
    docker compose up -d --remove-orphans

    # Wait for health check
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
- **Rolling deploy** — update one node at a time. Each node drains, pulls the new image, restarts, and passes health checks before moving to the next.
- **Migrations run once** — on a single API node after all nodes are updated. The server binary includes the migrate command.
- **Rollback** — re-deploy the previous git SHA: `./deploy-fleet.sh <previous-sha>`. Images are tagged by SHA so every version is available.

#### Full Architecture at Scale

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
              │           │ │           │ │           │
              └─────┬─────┘ └─────┬─────┘ └─────┬────┘
                    │             │             │
       ┌────────────▼─────────────▼─────────────▼──────────┐
       │                   PgBouncer                        │
       │              (on DB VPS, port 6432)                │
       └────────────────────────┬───────────────────────────┘
                                │
                ┌───────────────▼───────────────┐
                │     Postgres Primary          │───── WAL ─────▶ Replica (reads)
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

#### When to Split What

| Signal | Action |
|--------|--------|
| Agent runs queuing for > 5 min | Add worker nodes (Stage 3) |
| Postgres CPU > 70% sustained | Move Postgres to its own VPS (Stage 2) |
| API p95 latency > 500ms under load | Add API nodes (Stage 4) |
| Disk I/O wait > 20% on shared VPS | Separate Postgres (Stage 2) |
| Spending > 2 hrs/month on Postgres ops | Consider managed Postgres (Stage 5) |
| Need 99.9%+ uptime SLA | Multiple API nodes + Postgres HA (Stage 4+5) |

#### Connection Pooling

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

#### Draining & Graceful Shutdown

When removing a node:

```bash
# Signal the node to drain (finish current work, stop accepting new jobs)
kill -SIGTERM <pid>
```

On `SIGTERM`:
1. Node sets its status to `draining` in the `nodes` table
2. Releases the scheduler advisory lock (if held) — another node takes over immediately
3. Stops polling for new jobs
4. Waits for in-progress jobs to complete (up to `SHUTDOWN_TIMEOUT`, default 5 min)
5. Cleans up any running sandbox containers
6. Sets status to `dead` and exits

#### SSE Routing for Multi-Node

Agent run log streaming (SSE) requires routing the client to a node with access to the logs. Two approaches:

1. **Postgres-based (default)**: All nodes write logs to `agent_run_logs` table. Any API node can tail the table and stream via SSE. Simple, works everywhere, slight latency (~1s).
2. **Direct routing (optional)**: The `agent_runs` table stores `worker_node_id`. The API node proxies the SSE connection to the worker node running the sandbox for real-time streaming. Lower latency but requires worker nodes to be reachable from API nodes.

## Configuration

All configuration via environment variables:

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| **Core** | | | |
| `DATABASE_URL` | Yes | - | PostgreSQL connection string |
| `PORT` | No | `8080` | HTTP server port |
| `MODE` | No | `all` | Server mode: `all`, `api`, `worker` |
| `NODE_ID` | No | hostname | Unique node identifier for cluster registration |
| `LOG_LEVEL` | No | `info` | Logging level |
| `SESSION_SECRET` | Yes | - | Secret for session encryption |
| **GitHub** | | | |
| `GITHUB_APP_ID` | Yes | - | GitHub App ID |
| `GITHUB_APP_PRIVATE_KEY` | Yes | - | GitHub App private key (PEM) |
| `GITHUB_WEBHOOK_SECRET` | Yes | - | GitHub webhook signature secret |
| **Integrations** | | | |
| `SENTRY_WEBHOOK_SECRET` | No | - | Sentry webhook signature secret |
| `LINEAR_WEBHOOK_SECRET` | No | - | Linear webhook signature secret |
| **Sandbox** | | | |
| `SANDBOX_PROVIDER` | No | `docker` | Sandbox provider: `docker` (default, uses gVisor) or `e2b` |
| `SANDBOX_IMAGE` | No | `143-sandbox:latest` | Docker image for agent sandboxes (docker provider only) |
| `SANDBOX_RUNTIME` | No | `runsc` | Container runtime: `runsc` (gVisor, default) or `runc` (standard Docker) |
| `SANDBOX_TIMEOUT` | No | `300` | Sandbox timeout in seconds |
| `SANDBOX_CPU_LIMIT` | No | `2` | CPU cores per sandbox |
| `SANDBOX_MEMORY_LIMIT` | No | `4096` | Memory MB per sandbox |
| `SANDBOX_REQUIRE_GVISOR` | No | `true` | If true, server refuses to start without gVisor in production |
| `SANDBOX_IMAGE_DIGEST` | No | - | Expected digest for sandbox image verification |
| `MAX_CONCURRENT_RUNS` | No | `3` | Max concurrent agent runs per org |
| `E2B_API_KEY` | No | - | E2B API key (required if `SANDBOX_PROVIDER=e2b`) |
| `E2B_TEMPLATE_ID` | No | - | E2B sandbox template ID (required if `SANDBOX_PROVIDER=e2b`) |
| **Security** | | | |
| `ENCRYPTION_MASTER_KEY` | Yes (prod) | - | Master key for envelope encryption of integration credentials. Min 32 chars. |
| `EVAL_ENCRYPTION_KEY` | Yes (if private evals enabled) | - | Key used for application-layer encryption of private eval payload fields |
| `EVAL_PRIVATE_DATA_REDACTION` | No | `true` | Redact private eval payload-derived content from logs/traces |
| `SESSION_IDLE_TIMEOUT` | No | `1800` | Session idle timeout in seconds (default: 30 min) |
| **LLM** | | | |
| `LLM_API_KEY` | Yes | - | API key for validation LLM calls |
| `LLM_MODEL` | No | `claude-sonnet-4-5-20250929` | Model for validation checks |
| **Worker** | | | |
| `WORKER_POLL_INTERVAL` | No | `1s` | How often workers poll for new jobs |
| `SHUTDOWN_TIMEOUT` | No | `300` | Seconds to wait for in-progress jobs on shutdown |
| **Logging (Mezmo)** | | | |
| `MEZMO_INGESTION_KEY` | No | - | Mezmo ingestion key. If set, logs are shipped to Mezmo. |
| `MEZMO_HOSTNAME` | No | `NODE_ID` | Hostname tag sent to Mezmo |
| `MEZMO_APP_NAME` | No | `143-dev` | App name tag in Mezmo |
| `MEZMO_ENV` | No | `production` | Environment tag (`production`, `staging`, `development`) |
| **Monitoring (Datadog)** | | | |
| `DD_API_KEY` | No | - | Datadog API key. If set, metrics are shipped to Datadog. |
| `DD_APP_KEY` | No | - | Datadog app key (for querying metrics in experiments) |
| `DD_SITE` | No | `datadoghq.com` | Datadog site (`datadoghq.com`, `datadoghq.eu`, etc.) |
| `DD_ENV` | No | `production` | Environment tag for Datadog |
| `DD_SERVICE` | No | `143-dev` | Service name for Datadog APM |
| `DD_AGENT_HOST` | No | - | Datadog agent host (if using DD agent instead of direct API) |

## Health Checks

The server exposes:

- `GET /healthz` — basic liveness check (returns 200)
- `GET /readyz` — readiness check (verifies DB connection, sandbox provider connectivity, gVisor availability, secret validation)

## Logging: Mezmo

All application logs are structured JSON via zerolog. Mezmo is the primary log aggregation platform.

### Log Pipeline

```
Application (zerolog)
    │
    ├──▶ stdout (always — for local dev, Docker log drivers)
    │
    └──▶ Mezmo (if MEZMO_INGESTION_KEY is set)
         via HTTPS ingestion API
```

### Integration

Use a custom zerolog writer that ships logs to Mezmo's ingestion API in batches:

```go
// internal/logging/mezmo.go

type MezmoWriter struct {
    ingestionKey string
    hostname     string
    appName      string
    env          string
    buffer       []LogLine
    mu           sync.Mutex
    flushTicker  *time.Ticker
}

// Write implements io.Writer for zerolog
func (m *MezmoWriter) Write(p []byte) (n int, err error) {
    // Parse JSON log line, buffer it
    // Flush every 250ms or when buffer hits 100 lines
}

func (m *MezmoWriter) flush() {
    // POST https://logs.mezmo.com/logs/ingest
    // Headers: apikey: <ingestion_key>, Content-Type: application/json
    // Body: { "lines": [...] }
}
```

### Log Structure

Every log line includes:

```json
{
  "timestamp": "2025-01-15T10:30:00Z",
  "level": "info",
  "message": "agent run completed",
  "node_id": "worker-2",
  "mode": "worker",
  "org_id": "abc-123",
  "request_id": "req-456",
  "component": "agent.orchestrator",
  "agent_run_id": "run-789",
  "duration_ms": 45000,
  "env": "production"
}
```

### Mezmo Features Used

- **Log views**: Filtered views per node role (all, api, worker), per component (ingestion, agent, validation)
- **Alerts**: Trigger on error rate spikes, agent run failures, webhook processing errors
- **Archiving**: Long-term log storage to S3 for compliance
- **Log-based metrics**: Extract metrics from log patterns (e.g., agent run duration distribution)

### Local Development

In local dev (`LOG_LEVEL=debug`), logs go only to stdout with pretty-printed console output. Set `MEZMO_INGESTION_KEY` in dev if you want to test the Mezmo pipeline.

## Monitoring: Datadog

Datadog is the primary monitoring and APM platform. It provides metrics, traces, dashboards, and alerting.

### Integration Approach

Use the Datadog Go client library (`DataDog/datadog-go` for StatsD metrics, `DataDog/dd-trace-go` for APM) to emit metrics directly. Two modes:

1. **Agent mode** (recommended for production): Run the Datadog Agent as a sidecar container. The app sends metrics to `DD_AGENT_HOST` via UDP (StatsD) and traces via the agent's trace endpoint.
2. **Agentless mode** (simpler): Ship metrics directly to Datadog's API via `DD_API_KEY`. Higher latency, simpler setup. Good for small deployments.

### Docker Compose with Datadog Agent

```yaml
# Add to docker-compose.prod.yml
services:
  datadog-agent:
    image: gcr.io/datadoghq/agent:7
    environment:
      DD_API_KEY: ${DD_API_KEY}
      DD_SITE: ${DD_SITE:-datadoghq.com}
      DD_APM_ENABLED: "true"
      DD_LOGS_ENABLED: "true"           # optional: also ship logs via DD agent
      DD_DOGSTATSD_NON_LOCAL_TRAFFIC: "true"
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
      - /proc/:/host/proc/:ro
      - /sys/fs/cgroup/:/host/sys/fs/cgroup:ro
    ports:
      - "8125:8125/udp"   # StatsD
      - "8126:8126"       # APM traces
```

### Metrics Emitted

The application emits the following custom metrics:

#### HTTP Metrics (via middleware)

| Metric | Type | Tags |
|--------|------|------|
| `http.request.duration` | histogram | `method`, `route`, `status_code`, `node_id` |
| `http.request.count` | counter | `method`, `route`, `status_code`, `node_id` |
| `http.request.active` | gauge | `node_id` |

#### Job Queue Metrics

| Metric | Type | Tags |
|--------|------|------|
| `jobs.queue.depth` | gauge | `job_type`, `status` |
| `jobs.processing.duration` | histogram | `job_type`, `node_id` |
| `jobs.completed` | counter | `job_type`, `status` (completed/failed), `node_id` |
| `jobs.retries` | counter | `job_type`, `node_id` |

#### Agent Run Metrics

| Metric | Type | Tags |
|--------|------|------|
| `agent_run.duration` | histogram | `agent_type`, `status`, `org_id` |
| `agent_run.token_usage` | histogram | `agent_type`, `token_type` (input/output) |
| `agent_run.cost_usd` | counter | `agent_type`, `org_id` |
| `agent_run.active` | gauge | `node_id` |
| `sandbox.count` | gauge | `node_id`, `status` (running/creating/destroying) |
| `sandbox.cpu_usage` | gauge | `node_id`, `container_id` |
| `sandbox.memory_usage` | gauge | `node_id`, `container_id` |

#### Validation Metrics

| Metric | Type | Tags |
|--------|------|------|
| `validation.duration` | histogram | `check_name` |
| `validation.result` | counter | `check_name`, `result` (pass/fail) |

#### Cluster Metrics

| Metric | Type | Tags |
|--------|------|------|
| `cluster.nodes.active` | gauge | `mode` |
| `cluster.nodes.dead` | gauge | |

### APM Tracing

`dd-trace-go` provides distributed tracing across:

- HTTP requests (auto-instrumented via chi middleware)
- Database queries (auto-instrumented via pgx integration)
- Job processing (manual spans)
- Agent sandbox execution (manual spans)
- External API calls to Sentry, Linear, GitHub (manual spans)

Traces link API requests to the background jobs they enqueue, providing end-to-end visibility from webhook receipt to PR creation.

### Pre-Built Dashboards

Ship a Datadog dashboard JSON export (`deploy/datadog-dashboard.json`) that teams can import:

- **Overview**: request rate, error rate, p95 latency, active agent runs, queue depth
- **Agent Runs**: run duration distribution, success/failure rate, token usage, cost
- **Cluster Health**: node count by role, heartbeat staleness, sandbox utilization per node
- **Pipeline**: end-to-end funnel (issues ingested -> prioritized -> agent run -> validated -> PR opened -> deployed -> impact measured)

### Alerts

Recommended Datadog monitors (shipped as Terraform or JSON):

| Alert | Condition |
|-------|-----------|
| High error rate | `http.request.count{status_code:5xx}` > 5% of total for 5 min |
| Job queue backing up | `jobs.queue.depth{status:pending}` > 50 for 10 min |
| Agent run failures | `agent_run.completed{status:failed}` > 3 in 15 min |
| Node dead | `cluster.nodes.dead` > 0 for 3 min |
| Sandbox resource exhaustion | `sandbox.memory_usage` > 90% for 5 min |

### Datadog as a Metrics Source for Experiments

Datadog also serves as a source for Step 6 (observability/impact measurement). See [09-observability.md](09-observability.md) for details on how 143.dev queries Datadog metrics to evaluate experiment outcomes.

## Observability (Fallback)

For teams that don't use Mezmo or Datadog:

- **Logs**: Structured JSON to stdout works with any log aggregator (ELK, Loki, CloudWatch, etc.)
- **Metrics**: Prometheus-compatible `/metrics` endpoint is always available as a fallback
  - HTTP request duration, status codes
  - Job queue depth, processing time
  - Agent run duration, success rate
  - Active sandbox count

## PostgreSQL Operations & Data Protection

Postgres is the **only stateful component** in the entire system. The server, frontend, and sandboxes are all stateless — if any of them die, you just restart them. Losing Postgres means losing everything. This section describes how to make that effectively impossible.

### Data Protection Layers

There are three independent layers of protection. Each layer covers failures the previous one doesn't.

#### Layer 1: Docker Volume Persistence

The `pgdata` named volume ensures data survives container restarts, upgrades, and `docker compose down` (without `-v`).

```yaml
# Already configured in docker-compose
volumes:
  pgdata:/var/lib/postgresql/data
```

**Protects against:** container crashes, restarts, image upgrades, `docker compose down`.

**Does NOT protect against:** disk failure, accidental `DROP TABLE`, VPS deletion, data corruption.

#### Layer 2: Automated pg_dump Backups

Scheduled logical backups via `pg_dump`, uploaded offsite. This is the **minimum viable backup strategy** and must be configured before accepting customers.

**Backup script** (`deploy/scripts/pg-backup.sh`):

```bash
#!/usr/bin/env bash
set -euo pipefail

# Configuration
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

**Cron schedule** (add to host crontab or a dedicated backup container):

```cron
# Every 6 hours: dump the database
0 */6 * * * /opt/143/deploy/scripts/pg-backup.sh >> /var/log/pg-backup.log 2>&1

# Daily: sync backups offsite to S3-compatible storage (Hetzner Object Storage, AWS S3, etc.)
30 2 * * * rclone sync /backups/postgres s3:143-backups/postgres/ --log-file=/var/log/pg-backup-sync.log
```

**Protects against:** disk failure, VPS deletion, accidental data deletion (up to 6 hours of data loss).

**Does NOT protect against:** data written in the last 6 hours before a failure.

**RPO (Recovery Point Objective):** 6 hours worst case. Reduce by increasing dump frequency.

**RTO (Recovery Time Objective):** 15-30 minutes (spin up new VPS, restore from dump).

#### Layer 3: WAL Archiving & Point-in-Time Recovery (PITR)

For zero data loss tolerance. Postgres continuously streams its write-ahead log (WAL) to object storage. You can restore to any point in time, down to the second.

**When to add this:** When 6 hours of potential data loss is unacceptable — typically when you have paying customers generating high-value data (agent runs, validated PRs, production learnings).

**WAL-G setup** (recommended tool for WAL archiving):

```yaml
# docker-compose.prod.yml — postgres service with WAL archiving
services:
  postgres:
    image: postgres:18
    environment:
      POSTGRES_DB: onefortythree
      POSTGRES_USER: onefortythree
      POSTGRES_PASSWORD: ${DB_PASSWORD}
      # WAL archiving config
      POSTGRES_INITDB_ARGS: "--wal-segsize=16"
    volumes:
      - pgdata:/var/lib/postgresql/data
      - ./deploy/postgres/postgresql.conf:/etc/postgresql/conf.d/custom.conf:ro
    command: postgres -c config_file=/etc/postgresql/conf.d/custom.conf
```

**Postgres config for WAL archiving** (`deploy/postgres/postgresql.conf`):

```ini
# Include defaults
listen_addresses = '*'
max_connections = 100

# WAL archiving
wal_level = replica
archive_mode = on
archive_command = 'wal-g wal-push %p'
archive_timeout = 60    # force archive every 60s even if WAL segment isn't full

# Checksums (detect silent corruption)
# Note: must be enabled at initdb time with --data-checksums
```

**WAL-G environment** (configure in the postgres container or a sidecar):

```bash
# S3-compatible storage (Hetzner Object Storage, MinIO, AWS S3)
export WALG_S3_PREFIX=s3://143-backups/wal-g
export AWS_ACCESS_KEY_ID=your-key
export AWS_SECRET_ACCESS_KEY=your-secret
export AWS_ENDPOINT=https://fsn1.your-objectstorage.com  # Hetzner example
export AWS_REGION=fsn1

# Full backup: run weekly via cron
wal-g backup-push /var/lib/postgresql/data

# WAL segments: archived automatically every 60 seconds by archive_command
```

**Point-in-time restore:**

```bash
# Restore to a specific timestamp
export WALG_S3_PREFIX=s3://143-backups/wal-g
wal-g backup-fetch /var/lib/postgresql/data LATEST

# Create recovery.signal and set target time
cat > /var/lib/postgresql/data/recovery.signal <<EOF
EOF

cat >> /var/lib/postgresql/data/postgresql.conf <<EOF
restore_command = 'wal-g wal-fetch %f %p'
recovery_target_time = '2025-07-15 14:47:00 UTC'
recovery_target_action = 'promote'
EOF

# Start postgres — it replays WAL up to the target time
pg_ctl start -D /var/lib/postgresql/data
```

**Protects against:** everything — disk failure, data corruption, accidental deletion, VPS destruction.

**RPO:** seconds (WAL segments archive every 60s).

**RTO:** 15-30 minutes (fetch base backup + replay WAL).

### Restore Procedures

**Restore from pg_dump** (Layer 2):

```bash
# 1. Create a fresh postgres instance
docker compose up -d postgres

# 2. Restore from the most recent dump
docker exec -i 143-postgres-1 \
  pg_restore -U onefortythree -d onefortythree --clean --if-exists \
  < /backups/postgres/onefortythree-20250715-020000.dump

# 3. Verify
docker exec 143-postgres-1 \
  psql -U onefortythree -c "SELECT count(*) FROM organizations;"
```

**Restore from WAL-G** (Layer 3): See the point-in-time restore procedure above.

**Test your restore procedure.** Run a restore drill at least once before going to production, and monthly afterward. An untested backup is not a backup.

### Postgres Health Monitoring

Add these checks to your Datadog or Prometheus monitoring:

| Check | Query / Method | Alert Threshold |
|-------|---------------|-----------------|
| **Connection count** | `SELECT count(*) FROM pg_stat_activity` | > 80% of `max_connections` |
| **Disk usage** | `SELECT pg_database_size('onefortythree')` | > 80% of available disk |
| **Replication lag** (if using replicas) | `SELECT extract(epoch FROM replay_lag) FROM pg_stat_replication` | > 30 seconds |
| **Long-running queries** | `SELECT * FROM pg_stat_activity WHERE state = 'active' AND now() - query_start > interval '5 minutes'` | Any |
| **Dead tuples** (needs VACUUM) | `SELECT relname, n_dead_tup FROM pg_stat_user_tables ORDER BY n_dead_tup DESC` | > 100K dead tuples |
| **Backup freshness** | Check latest `.dump` file mtime | > 12 hours old |
| **WAL archiving status** | `SELECT last_archived_wal, last_failed_wal FROM pg_stat_archiver` | Any failed WAL |

**Recommended Datadog monitors** (add to existing alert set):

| Alert | Condition |
|-------|-----------|
| Postgres down | `pg_isready` fails for 30 seconds |
| Disk usage critical | Postgres data directory > 85% of disk |
| Backup stale | No new backup file in 12 hours |
| Connection pool exhaustion | Active connections > 80 |
| Long-running transaction | Any transaction open > 10 minutes |

### Production Postgres Configuration

For a single-VPS deployment (4-16GB RAM), these settings improve on the defaults without requiring tuning expertise:

```ini
# deploy/postgres/postgresql.conf

# Connection limits
max_connections = 100           # plenty for pgx pool + direct connections
shared_buffers = 256MB          # 25% of RAM on a 1GB instance, scale up with RAM
effective_cache_size = 768MB    # 75% of RAM — tells planner how much OS cache to expect
work_mem = 4MB                  # per-sort/hash operation — keep conservative
maintenance_work_mem = 64MB     # for VACUUM, CREATE INDEX

# Write performance
wal_buffers = 16MB
checkpoint_completion_target = 0.9
random_page_cost = 1.1          # for SSD storage (Hetzner uses SSDs)

# Autovacuum (keep defaults, but ensure it runs aggressively enough)
autovacuum = on
autovacuum_max_workers = 3
autovacuum_naptime = 60         # check every 60s instead of default 1min (same, but explicit)

# Logging (useful for debugging slow queries)
log_min_duration_statement = 1000  # log queries taking > 1 second
log_checkpoints = on
log_connections = on
log_disconnections = on
log_lock_waits = on

# Data integrity
fsync = on                      # NEVER turn this off in production
full_page_writes = on           # protects against partial page writes on crash
```

Scale `shared_buffers` and `effective_cache_size` with your VPS RAM:

| VPS RAM | `shared_buffers` | `effective_cache_size` |
|---------|------------------|----------------------|
| 2 GB | 512 MB | 1.5 GB |
| 4 GB | 1 GB | 3 GB |
| 8 GB | 2 GB | 6 GB |
| 16 GB | 4 GB | 12 GB |

### Data Integrity Safeguards

These are already built into the schema and application, but worth calling out:

1. **Data checksums** — Enable at `initdb` time (`--data-checksums`). Detects silent disk corruption. Add `POSTGRES_INITDB_ARGS: "--data-checksums"` to docker-compose.
2. **Audit log immutability** — The `audit_log` table has a trigger that prevents `UPDATE` and `DELETE` operations (see migration `000001`).
3. **Foreign key constraints** — All cross-table references use `ON DELETE CASCADE` or `ON DELETE RESTRICT` to prevent orphaned records.
4. **`timestamptz` everywhere** — All timestamps are timezone-aware (UTC), preventing timezone-related data corruption.
5. **UUID primary keys** — No auto-increment collisions across nodes in a multi-node deployment.
6. **Transaction isolation** — The job queue uses `FOR UPDATE SKIP LOCKED` which operates correctly under Postgres's default `READ COMMITTED` isolation level.

### Postgres Scaling Path

| Scale | Database size | Setup | Action needed |
|-------|--------------|-------|---------------|
| **Launch** | < 1 GB | Single VPS, Postgres in Docker | Layer 1 + Layer 2 backups |
| **Growing** | 1-50 GB | Single VPS, Postgres in Docker | Add Layer 3 (WAL archiving), tune `shared_buffers` |
| **Busy** | 50-500 GB | Dedicated Postgres VPS (Hetzner CX22-CX42) | Separate DB from app server, add read replica for dashboard queries |
| **Large** | 500 GB+ | Managed Postgres (RDS, Cloud SQL, Ubicloud) or Citus | Connection pooling (PgBouncer), table partitioning for `agent_run_logs` and `audit_log` |

**Migration from self-hosted to managed is a single pg_dump/pg_restore.** No application code changes — the app only sees `DATABASE_URL`.

### Environment Variables (Backup & Recovery)

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `BACKUP_DIR` | No | `/backups/postgres` | Directory for pg_dump backup files |
| `BACKUP_RETENTION_DAYS` | No | `30` | Days to retain local backup files |
| `WALG_S3_PREFIX` | No (Layer 3) | - | S3 path for WAL-G archives |
| `AWS_ACCESS_KEY_ID` | No (Layer 3) | - | S3 credentials for WAL-G |
| `AWS_SECRET_ACCESS_KEY` | No (Layer 3) | - | S3 credentials for WAL-G |
| `AWS_ENDPOINT` | No (Layer 3) | - | S3-compatible endpoint (Hetzner, MinIO) |

## Security Considerations

See [20-security-architecture.md](20-security-architecture.md) for the comprehensive security architecture. Key points:

- All inter-service communication over TLS in production. Database connections use `sslmode=verify-full`.
- **Sandbox isolation via gVisor**: Agent sandboxes run under gVisor (`runsc`) by default. gVisor is **required in production** — the server refuses to start without it unless `SANDBOX_REQUIRE_GVISOR=false` is explicitly set. In development, fallback to `runc` is allowed with a warning.
- **Container hardening**: Sandboxes run as non-root with `--cap-drop=ALL`, `--security-opt=no-new-privileges`, `--read-only` root filesystem, `--pids-limit=256`. Only `/workspace` and `/tmp` are writable.
- **Network isolation**: Sandbox network is restricted to LLM APIs and package registries only — no access to the host network, internal services, or metadata endpoints (`169.254.0.0/16` blocked).
- **Docker socket protection**: In production, use [docker-socket-proxy](https://github.com/Tecnativa/docker-socket-proxy) to restrict Docker API access. The server container never mounts `/var/run/docker.sock` directly — it connects via `DOCKER_HOST=tcp://docker-proxy:2375`.
- **Pluggable sandbox providers**: For teams needing even stronger isolation, the sandbox layer can be swapped to E2B (Firecracker microVMs with separate kernels per sandbox) or other providers without changing the orchestrator.
- **Envelope encryption**: Integration credentials are encrypted at rest using `ENCRYPTION_MASTER_KEY` (dedicated key, not `SESSION_SECRET`) with per-record data encryption keys (AES-256-GCM).
- **Startup security checks**: The server validates that secrets are set, gVisor is available, and default credentials are not in use. Failures are fatal in production.
- Webhook endpoints validate HMAC signatures before processing.
- **Prompt injection defense**: All issue content is sanitized before prompt construction, and prompts use explicit delimiters and instructions to treat external data as data.
- **Validation pipeline security scanning**: Agent diffs are scanned for secrets (gitleaks), vulnerabilities (semgrep), and exfiltration patterns before PRs are opened.

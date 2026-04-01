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

For the step-by-step scaling playbook (from single VPS to multi-node cluster with auto-scaling), see [36-docker-agents-vps-architecture.md](36-docker-agents-vps-architecture.md) — Phase 2.

#### Production docker-compose.yml

```yaml
# docker-compose.prod.yml — single-node production stack
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

#### Connection Pooling

When you have many app/worker nodes, each opens a pool of connections to Postgres. At scale (10+ nodes), this can exhaust `max_connections`. See [36-docker-agents-vps-architecture.md](36-docker-agents-vps-architecture.md) for PgBouncer setup.

**When to add PgBouncer:** When total connections across all nodes approach 80% of `max_connections` (default 100). Each node's pgx pool defaults to ~10 connections, so with 8+ nodes you're getting close.

**Important:** Use `POOL_MODE=transaction` (not `session`). Session mode breaks `FOR UPDATE SKIP LOCKED` across transaction boundaries.

#### Draining & Graceful Shutdown

When removing a node:

```bash
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

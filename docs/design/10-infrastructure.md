# Design: Infrastructure & Deployment

> **Status:** Partially Implemented | **Last reviewed:** 2026-05-05

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

For the standard local workflow, `make dev` builds `143-sandbox:latest` via
`docker compose build sandbox` before starting the stack, so Docker-backed
agent runs use the same sandbox image tag as the runtime default.

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
- Docker socket access via [docker-socket-proxy](https://github.com/Tecnativa/docker-socket-proxy) instead of direct socket mount (see [20-security-architecture.md](implemented/20-security-architecture.md))
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

**Sandbox Dockerfile**: See `sandbox/Dockerfile` for the full definition. The image installs all five agent CLIs (Claude Code, Codex, OpenCode, Amp, and Pi) at pinned versions from `sandbox/versions.json`; Pi now tracks the upstream `@earendil-works/pi-coding-agent` package scope there. Build with `docker build -t 143-sandbox:latest sandbox/`. CI also builds this image, and local Docker development builds it through the `sandbox` compose target.

This image is used by the Docker sandbox provider. It runs under **gVisor** (`runsc` runtime) by default for syscall-level isolation. The same image works with both `runsc` (gVisor) and `runc` (standard Docker) — no image changes needed when switching runtimes.

### Deploy-Time Host Hardening

Routine fleet deploys roll only the user-facing runtime roles, `app` and `worker`. Stateful/supporting roles (`db`, `redis`, `logging`) are deployed explicitly with `make deploy-db`, `make deploy-redis`, `make deploy-logging`, or `make deploy-fleet ROLES=all` during maintenance. `make deploy-fleet TAG=<image-tag> ROLES=<roles>` is the operator-facing shape for deploying a specific image tag to a selected role set. Fleet deploys use bounded concurrency with `DEPLOY_JOBS=4` by default while serializing multiple role deploys that target the same host; set `DEPLOY_JOBS=1` for a fully serialized rollout, or raise it when the fleet and database connection budget can tolerate more simultaneous blue/green overlap. For non-disruptive worker deploys, manual operators should run `make deploy-worker-preflight` and then `make deploy-fleet ROLES=app,worker`; Make defaults the worker blue/green range to `8080-8087`, and app nodes must be able to reach every configured worker port in that range. This keeps frequent application deploys from recreating Postgres, Redis, Grafana, or other non-runtime services.

Routine worker deploys also treat host support services as verify-only. If `sandbox-dns` is already healthy and owns its pinned sandbox resolver addresses, reconciliation leaves it running even when the local `143-sandbox-dns:local` image tag has drifted. Support-service changes such as `sandbox-dns`, Chrome, gVisor checks, bridge behavior, or Docker daemon changes are activated only in `DEPLOY_MODE=maintenance`, after operators review active runtime impact.

Fleet deploys attempt to keep host hardening in place as they roll services:

- Docker `json-file` log rotation is installed through `deploy/scripts/install-log-rotation.sh`, which merges the desired `log-driver` and `log-opts` into `/etc/docker/daemon.json` and restarts Docker only when the file changes.
- App-role deploys skip Docker-daemon-mutating hardening checks by default because a Docker restart recycles Caddy and briefly unbinds ports `80`/`443`, which Cloudflare surfaces as origin downtime. Operators can opt in for explicit maintenance with `ALLOW_DEPLOY_DOCKER_DAEMON_RESTART=1`.
- Routine app-role deploys also leave the running Caddy container untouched unless `Dockerfile.caddy`, the Caddyfile, or Caddy-specific env changes. Caddyfile-only changes use in-place `caddy reload`; image/env changes still reconcile the container because the running process cannot absorb them safely.
- The deploy user receives a narrow `NOPASSWD` sudoers grant during provisioning so routine deploys can run the log-rotation helper and worker firewall helper without broad root access. Worker cloud-init installs the same grant on first boot, because those hosts can start successfully from user-data before any operator runs SSH provisioning.
- Existing hosts that predate a sudoers entry are repaired through `deploy/scripts/repair-deploy-sudoers.sh` when root SSH is available.
- If a routine deploy cannot repair sudoers from CI, Docker log-rotation update failure is warning-only. The service rollout continues, and operators can run `make repair-deploy-sudoers ROLE=<role> HOST=<host>` later from a machine with root SSH access.

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
| `SANDBOX_HEALTH_CHECK_IMAGE` | No | `busybox:1.36.1` | Small image used for worker startup runtime probes; lazy-pulled if missing and overrideable for private mirrors |
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

The production frontend image runs Next.js standalone on port 3000 and sets
`HOSTNAME=0.0.0.0`. Docker injects `HOSTNAME` as the container ID by default;
overriding it keeps the Next server bound to all interfaces so both Docker
health checks on `127.0.0.1:3000/healthz` and other compose services can reach
the process. Because the frontend build runs from a monorepo workspace, Next's
standalone output places the app entrypoint under `frontend/server.js`; the
runtime image keeps the traced repo-level files under `/app` and starts from
`/app/frontend` so the server, `.next/static`, and `public` assets line up.

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

Datadog also serves as a source for Step 6 (observability/impact measurement). See [09-observability.md](backlog/09-observability.md) for details on how 143.dev queries Datadog metrics to evaluate experiment outcomes.

## Observability (Fallback)

For teams that don't use Mezmo or Datadog:

- **Logs**: Structured JSON to stdout works with any log aggregator (ELK, Loki, CloudWatch, etc.)
- **Metrics**: Prometheus-compatible `/metrics` endpoint is always available as a fallback
  - HTTP request duration, status codes
  - Job queue depth, processing time
  - Agent run duration, success rate
  - Active sandbox count

## Backup & Recovery

- **Postgres**: Standard `pg_dump` for backups. In production, use managed Postgres with automated backups.
- **No other stateful systems** — the server and sandboxes are stateless.
- **Configuration**: All config is in environment variables. Store `.env` securely (e.g., in a secrets manager).

## Security Considerations

See [20-security-architecture.md](implemented/20-security-architecture.md) for the comprehensive security architecture. Key points:

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

# Design Doc 36: Docker Agent Nodes on Hetzner VPS

## Context

143.dev currently runs on Render (Go API + Next.js frontend + Postgres). The Go
server talks to a local Docker daemon (`/var/run/docker.sock`) to spin up agent
sandbox containers. Render does not support Docker-in-Docker or privileged
containers, so agent sandboxes cannot run on Render.

This document covers two architectures:
1. **Hybrid (Render + Hetzner)** — Keep the web stack on Render, run agent nodes on Hetzner
2. **Full migration to Hetzner** — Move everything off Render

---

## Option A: Hybrid Architecture (Render + Hetzner)

```
┌─────────────────────────────────────────────────────────────────┐
│  RENDER (Oregon)                                                │
│                                                                 │
│  ┌─────────────┐   ┌──────────────┐   ┌──────────────────────┐ │
│  │ Next.js Web │──▶│   Go API     │──▶│  Postgres 17         │ │
│  │ (frontend)  │   │ (api+worker) │   │  (Render Managed DB) │ │
│  └─────────────┘   └──────┬───────┘   └──────────────────────┘ │
│                           │                                     │
└───────────────────────────┼─────────────────────────────────────┘
                            │ HTTPS (mTLS) / WireGuard
                            ▼
┌─────────────────────────────────────────────────────────────────┐
│  HETZNER (Falkenstein / Nuremberg / Ashburn)                    │
│                                                                 │
│  ┌──────────────────────────────────────────────────────────┐   │
│  │  Agent Node 1 (CX42 — 8 vCPU, 16GB, €14/mo)            │   │
│  │  ┌────────────┐  ┌────────────┐  ┌────────────┐         │   │
│  │  │ Agent API  │  │ Sandbox 1  │  │ Sandbox 2  │  ...    │   │
│  │  │ (port 9090)│  │ (Docker)   │  │ (Docker)   │         │   │
│  │  └────────────┘  └────────────┘  └────────────┘         │   │
│  └──────────────────────────────────────────────────────────┘   │
│                                                                 │
│  ┌──────────────────────────────────────────────────────────┐   │
│  │  Agent Node 2 (identical)                                │   │
│  └──────────────────────────────────────────────────────────┘   │
│                                                                 │
│  ┌──────────────────────────────────────────────────────────┐   │
│  │  Agent Node N (scale horizontally)                       │   │
│  └──────────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────┘
```

### How Render Talks to Hetzner (Crossing VPC Boundaries)

Render and Hetzner are completely separate networks. Three options, in order of
recommendation:

#### 1. WireGuard Tunnel (Recommended)

WireGuard creates a point-to-point encrypted tunnel at the kernel level. It is
the simplest and most reliable approach for cross-cloud connectivity.

**Setup:**
- Install WireGuard on each Hetzner node and on the Render service
- Each peer gets a private IP on a shared subnet (e.g., `10.143.0.0/24`)
- Render API server → `10.143.0.2:9090` (Agent Node 1)
- Render API server → `10.143.0.3:9090` (Agent Node 2)
- Only one UDP port (51820) needs to be open on each Hetzner node's firewall

**Why WireGuard:**
- ~3ms overhead, essentially line-speed
- Handles NAT traversal automatically
- Render's outbound traffic is unrestricted, so it can initiate connections
- No IP allowlisting needed since traffic routes over the tunnel
- Survives Render's ephemeral IP changes (WireGuard authenticates by key, not IP)

**Render limitation:** Render containers can't listen on custom UDP ports, but
they CAN initiate outbound WireGuard connections. The tunnel is initiated from
Render → Hetzner, so Hetzner only needs to accept incoming WireGuard peers. If
Render's container restarts and gets a new IP, WireGuard will re-establish the
tunnel automatically because Hetzner's endpoint has a stable IP.

**Alternative if WireGuard on Render is blocked:** Run a lightweight relay (e.g.,
`wstunnel` or `chisel`) that tunnels over HTTPS/WebSocket from Render to
Hetzner. This works on any PaaS since it's just outbound HTTPS.

#### 2. mTLS Over Public Internet (Simpler, Still Secure)

Skip the VPN. The Agent API on Hetzner listens on a public port (443) with
mutual TLS:
- Agent API presents a server cert; Go API validates it
- Go API presents a client cert; Agent API validates it
- Hetzner firewall only allows connections from Render's egress IPs

**Drawback:** Render's egress IPs can change. You'd need to periodically update
Hetzner's firewall rules, or use a broader CIDR block.

#### 3. Tailscale (Zero-Config WireGuard)

Tailscale wraps WireGuard with identity-based access. Install Tailscale on each
Hetzner node + the Render container. Nodes find each other automatically via
Tailscale's coordination server.

- Pros: Zero firewall config, ACLs in a central dashboard, MagicDNS
- Cons: Adds a SaaS dependency, free tier limited to 100 devices

### The Agent Node (What Runs on Hetzner)

Each Hetzner VPS runs a small **Agent API** — a thin HTTP server that the Go
worker calls instead of the local Docker socket.

```
agent-node/
├── docker-compose.yml          # Agent API + Docker runtime
├── Dockerfile.agent-api        # Thin Go/shell wrapper
├── agent-images/
│   └── Dockerfile.143-agent    # The sandbox image (same as today)
├── wireguard/
│   └── wg0.conf                # WireGuard config
└── scripts/
    ├── setup.sh                # One-time node bootstrap
    └── health-check.sh         # Liveness probe
```

**Agent API responsibilities:**
- Accept gRPC or REST calls from the Go worker
- Map them to local Docker API calls (create, exec, snapshot, destroy)
- Stream logs back over the connection
- Report health + capacity (running container count, CPU/mem usage)

**This is essentially a remote `SandboxProvider`.** The existing
`SandboxProvider` interface already abstracts sandbox lifecycle perfectly. You
implement a new `RemoteDockerProvider` that makes HTTP calls to the Agent API
instead of calling the local Docker client.

### Implementation Plan (Hybrid)

#### Phase 1: Remote Sandbox Provider

Create `internal/services/agent/providers/remote.go`:

```go
// RemoteDockerProvider implements agent.SandboxProvider by forwarding
// calls to an Agent API running on a remote Hetzner node.
type RemoteDockerProvider struct {
    nodes      []NodeConfig      // list of agent node endpoints
    httpClient *http.Client      // configured with mTLS or WireGuard
    selector   NodeSelector      // round-robin, least-loaded, etc.
    logger     zerolog.Logger
}

type NodeConfig struct {
    ID       string // "hetzner-fsn1-01"
    Endpoint string // "https://10.143.0.2:9090" (WireGuard) or public URL
    Capacity int    // max concurrent sandboxes
}
```

All existing `SandboxProvider` interface methods (Create, CloneRepo, Exec,
ExecStream, ReadFile, WriteFile, Destroy, Snapshot, Restore, ConnectionInfo)
get forwarded as HTTP/gRPC calls to the remote node.

#### Phase 2: Agent API Service (Runs on Hetzner)

A small Go service (~500 lines) that wraps the Docker client:

```
POST   /v1/sandboxes              → Create
DELETE /v1/sandboxes/:id          → Destroy
POST   /v1/sandboxes/:id/exec    → Exec (returns exit code + output)
POST   /v1/sandboxes/:id/stream  → ExecStream (SSE/WebSocket)
POST   /v1/sandboxes/:id/clone   → CloneRepo
GET    /v1/sandboxes/:id/files   → ReadFile
PUT    /v1/sandboxes/:id/files   → WriteFile
POST   /v1/sandboxes/:id/snapshot → Snapshot (streams tar)
POST   /v1/sandboxes/:id/restore  → Restore (accepts tar)
GET    /v1/health                 → Health + capacity
```

Authentication: mTLS client certs or a shared bearer token over the WireGuard
tunnel. The tunnel already encrypts traffic, so a simple bearer token is fine
for authz.

#### Phase 3: Node Management + Scaling

- **Static initially**: Configure node endpoints in env vars or a config file
- **Later**: Add a node registry table in Postgres. Nodes register on boot and
  heartbeat every 30s. The worker's `NodeSelector` picks the least-loaded node.
- **Auto-scaling**: Use Hetzner's API to spin up/down nodes based on queue depth.
  Hetzner cloud servers boot in ~10s.

### docker-compose.yml for a Hetzner Agent Node

```yaml
services:
  agent-api:
    build:
      context: .
      dockerfile: Dockerfile.agent-api
    ports:
      - "9090:9090"
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
    environment:
      AUTH_TOKEN: ${AGENT_AUTH_TOKEN}
      NODE_ID: ${NODE_ID:-node-1}
      MAX_SANDBOXES: ${MAX_SANDBOXES:-5}
      LOG_LEVEL: info
    restart: unless-stopped
    deploy:
      resources:
        limits:
          memory: 256M
          cpus: "0.5"

  # WireGuard sidecar (if not using host networking)
  wireguard:
    image: linuxserver/wireguard
    cap_add:
      - NET_ADMIN
      - SYS_MODULE
    environment:
      - PUID=1000
      - PGID=1000
    volumes:
      - ./wireguard/wg0.conf:/config/wg0.conf
    sysctls:
      - net.ipv4.conf.all.src_valid_mark=1
    restart: unless-stopped
```

### Changes to Existing Code

1. **`internal/config/config.go`** — Add:
   ```
   SANDBOX_MODE=local|remote          (default: local)
   AGENT_NODES=10.143.0.2:9090,10.143.0.3:9090
   AGENT_AUTH_TOKEN=<shared secret>
   ```

2. **`cmd/server/main.go`** — Provider selection:
   ```go
   var sandboxProvider agent.SandboxProvider
   if cfg.SandboxMode == "remote" {
       sandboxProvider = providers.NewRemoteDockerProvider(cfg.AgentNodes, ...)
   } else {
       sandboxProvider = providers.NewDockerProvider(dockerClient, ...)
   }
   ```

3. **Worker mode split** — The Go server runs in `MODE=all|api|worker`. For
   hybrid mode, you may want to keep `MODE=all` on Render (the worker claims
   jobs and dispatches to remote nodes), or run a dedicated worker process.

### Network Security

```
Hetzner Firewall Rules:
  - Allow UDP 51820 (WireGuard) from 0.0.0.0/0
    (WireGuard authenticates by public key, not source IP)
  - Allow TCP 22 from your admin IPs only
  - Deny everything else

Agent sandbox network policy:
  - Sandbox containers use the `143-sandbox` Docker network
  - Egress restricted to LLM API endpoints only (same as today)
  - No access to the host network or the WireGuard tunnel
```

---

## Option B: Full Migration to Hetzner

Move everything off Render onto Hetzner.

```
┌──────────────────────────────────────────────────────────────────────┐
│  HETZNER (Single VPS or Multi-Node)                                  │
│                                                                      │
│  ┌────────────────────────────────────────────────────────────────┐  │
│  │  Docker Compose / Docker Swarm                                 │  │
│  │                                                                │  │
│  │  ┌──────────┐  ┌─────────┐  ┌──────────┐  ┌──────────────┐   │  │
│  │  │ Caddy/   │  │ Go API  │  │ Next.js  │  │  Postgres 17 │   │  │
│  │  │ Traefik  │─▶│ :8080   │  │ :3000    │  │  :5432       │   │  │
│  │  │ :443     │  │         │  │          │  │              │   │  │
│  │  └──────────┘  └────┬────┘  └──────────┘  └──────────────┘   │  │
│  │                     │                                          │  │
│  │           ┌─────────▼──────────┐                               │  │
│  │           │  Docker Daemon      │                               │  │
│  │           │  ┌───────┐┌───────┐│                               │  │
│  │           │  │ Sbox 1││ Sbox 2││ ...                           │  │
│  │           │  └───────┘└───────┘│                               │  │
│  │           └────────────────────┘                               │  │
│  └────────────────────────────────────────────────────────────────┘  │
│                                                                      │
│  For scale-out:                                                      │
│  ┌────────────────────────────┐  ┌────────────────────────────┐     │
│  │  Worker Node 2             │  │  Worker Node N             │     │
│  │  (agent sandboxes only)    │  │  (agent sandboxes only)    │     │
│  └────────────────────────────┘  └────────────────────────────┘     │
└──────────────────────────────────────────────────────────────────────┘
```

### What It Takes

| Component | Render Today | Hetzner Equivalent |
|---|---|---|
| Go API | Render Docker service | Docker Compose service, same Dockerfile |
| Next.js | Render Node service | Docker Compose service |
| Postgres | Render managed DB | Self-managed Postgres in Docker (or Hetzner Managed DB ~€10/mo) |
| TLS/Certs | Render auto-TLS | Caddy (auto Let's Encrypt) or Traefik |
| DNS | External (Cloudflare, etc.) | Same — just point A records to Hetzner IP |
| CI/CD deploys | `git push` → Render auto-builds | GitHub Actions → SSH deploy or Docker Registry pull |
| Scaling | Render auto-scale (limited) | Manual or scripted (Hetzner API) |
| Agent sandboxes | Can't run (no Docker socket) | Full Docker access, works natively |
| Backups | Render automated backups | Hetzner snapshots + pg_dump cron |
| Monitoring | Render metrics dashboard | Prometheus + Grafana (or keep Datadog) |

### docker-compose.yml (Full Hetzner)

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
    volumes:
      - pgdata:/var/lib/postgresql/data
      - ./backups:/backups
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

### CI/CD for Hetzner

Replace Render's auto-deploy with a GitHub Actions workflow:

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

### Impact Assessment: Full Migration

**Effort: Medium** (1-2 weeks for a careful migration)

| Area | Impact | Notes |
|---|---|---|
| Code changes | **Minimal** | No application code changes needed. The Go API, frontend, and Docker provider all work as-is. |
| Dockerfile | **None** | Same multi-stage Dockerfile works on Hetzner. |
| Database migration | **Low** | `pg_dump` from Render → `pg_restore` on Hetzner. ~30 min downtime. |
| DNS cutover | **Low** | Update A records. Can use Cloudflare for zero-downtime. |
| TLS | **Low** | Caddy handles Let's Encrypt automatically. |
| CI/CD | **Medium** | Replace Render auto-deploy with GitHub Actions SSH deploy. |
| Secrets management | **None** | SOPS + age works identically. |
| Monitoring | **Medium** | Replace Render's dashboard with Prometheus/Grafana or keep shipping to Datadog. |
| Backups | **Medium** | Set up pg_dump cron + Hetzner volume snapshots. |
| Agent sandboxes | **Huge win** | Docker socket access works natively. No remote provider needed. |

**Cost comparison:**
- Render: Starter plan API (~$7/mo) + Starter web (~$7/mo) + Basic DB (~$7/mo) = ~$21/mo minimum, but no Docker support
- Hetzner CX42: 8 vCPU, 16GB RAM, 160GB SSD = €14.49/mo (~$16/mo) — runs EVERYTHING including agent sandboxes

### Scaling the Full Hetzner Setup

**Single node (start here):**
- CX42 (8 vCPU, 16GB) handles API + frontend + Postgres + 3-5 concurrent agent sandboxes
- Each sandbox uses ~2 CPU + 4GB RAM, so a CX42 can run ~3 concurrently

**Multi-node (when you outgrow one box):**
- **Node 1**: API + frontend + Postgres (CX22, 4 vCPU, 8GB, €6/mo)
- **Node 2-N**: Agent worker nodes (CX42, 8 vCPU, 16GB, €14/mo each)
- Communication: Private network (Hetzner vSwitch, free) or WireGuard
- Use the same Remote Docker Provider from Option A

**Even bigger (Docker Swarm / k3s):**
- k3s is a lightweight Kubernetes that runs well on Hetzner
- Hetzner Cloud Controller Manager auto-provisions load balancers + volumes
- But this adds significant operational complexity — only needed at serious scale

---

## Recommendation

**Start with Option B (full Hetzner) on a single node.** Here's why:

1. **Simplest path to Docker sandboxes.** No cross-cloud networking, no remote
   provider, no tunnel setup. The existing Docker provider works as-is.

2. **Dramatically cheaper.** One CX42 at €14/mo replaces three Render services.

3. **No code changes required.** Your Dockerfile, docker-compose, and all
   application code work without modification. The only new things are Caddy for
   TLS and a GitHub Actions deploy workflow.

4. **Scales naturally.** When you outgrow one box, split API and worker nodes.
   At that point, implement the Remote Docker Provider (Phase 1 from Option A)
   to dispatch sandboxes to dedicated worker nodes on the same Hetzner private
   network — no VPN needed since they're in the same DC.

5. **Reversible.** If you want to go back to Render for the web layer later,
   you only need to implement the Remote Docker Provider to reach back to
   Hetzner for sandboxes (= Option A).

**Migration checklist (Option B):**
- [ ] Provision Hetzner CX42
- [ ] Install Docker + Docker Compose
- [ ] Copy docker-compose.yml + Caddyfile
- [ ] Set up GitHub Actions deploy workflow
- [ ] `pg_dump` Render DB → `pg_restore` on Hetzner
- [ ] Copy environment variables / SOPS-encrypted secrets
- [ ] Update DNS to point to Hetzner IP
- [ ] Set up pg_dump cron for backups
- [ ] Verify health checks and monitoring
- [ ] Decommission Render services

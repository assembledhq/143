# Design Doc 36: Self-Hosted Docker Infrastructure

> **Status:** Partially Implemented | **Last reviewed:** 2026-04-21
>
> **Implementation notes:** Multi-node Docker Compose deployment artifacts exist (`docker-compose.app.yml`, `docker-compose.worker.yml`, `docker-compose.db.yml`, `docker-compose.logging.yml`), along with `deploy/cloud-init/*` templates and `deploy/scripts/{provision,deploy}.sh`. Later-phase items in this design, including dedicated Redis rollout and autoscaling, remain future work.

## Context

143.dev needs infrastructure where we control the Docker daemon so agent
sandboxes can run. This document describes how to set up a self-hosted VPS
deployment from scratch and scale incrementally. It is organized into four
phases, each with a clear list of what changes in our code, what infrastructure
to provision, and what is provider-specific.

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

### What Exists in Our Codebase Today

| Artifact | Path | Notes |
|---|---|---|
| Server Dockerfile | `Dockerfile` | Multi-stage Go build, runs as non-root, includes `sops`/`age` |
| Dev docker-compose | `docker-compose.yml` | Postgres + server (with `air` live reload) + frontend |
| CI pipeline | `.github/workflows/ci.yml` | Lint, test, build, security scan, Docker image build |
| Docker sandbox provider | `internal/services/agent/providers/docker.go` | Uses Docker API, supports gVisor (`runsc`) runtime |
| Environment config | `.env.example` | All env vars documented |
| Agent sandbox Dockerfile | **Does not exist** | `Dockerfile.agent` — needs to be created (see Step 6) |
| Production compose | **Does not exist** | Needs to be created |
| Deploy workflow | **Does not exist** | Needs to be created |
| Caddy config | **Does not exist** | Needs to be created |
| Backup scripts | **Does not exist** | Needs to be created |

---

## Phase 1: Single-Node Production

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
│  │  │ Caddy    │  │ Go API  │  │ Next.js  │  │  Postgres 18 │   │  │
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

### Practical Setup Guide

This is the step-by-step walkthrough. Provider-specific steps use Hetzner as the
example, with equivalents noted for other providers.

#### Step 1: VPS Requirements

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

#### Step 2: Provision the VPS (Hetzner Example)

1. **Create a Hetzner Cloud account** at console.hetzner.cloud. Add a payment
   method. Billing is hourly — no upfront commitment.

2. **Create a project** (e.g., "143-prod"). Hetzner organizes resources under
   projects.

3. **Generate and upload an SSH key.** Create a dedicated deploy key (don't reuse
   personal keys):
   ```bash
   ssh-keygen -t ed25519 -f ~/.ssh/143-deploy -C "143-deploy"
   ```
   Upload the public key in Hetzner console → Security → SSH Keys.

4. **Create a firewall** (Hetzner console → Firewalls → Create):

   | Rule | Direction | Protocol | Port | Source | Why |
   |------|-----------|----------|------|--------|-----|
   | SSH | Inbound | TCP | 22 | Your IP (or 0.0.0.0/0) | Remote access |
   | HTTP | Inbound | TCP | 80 | 0.0.0.0/0 | Caddy ACME cert challenges + HTTPS redirect |
   | HTTPS | Inbound | TCP | 443 | 0.0.0.0/0 | Production traffic |

   Postgres (5432) is **not exposed** — it only listens on localhost. It's only
   exposed to the private network in Phase 3 when you separate the DB.

   > **`[PROVIDER-SPECIFIC]` Firewall equivalents:**
   > - **AWS:** Security Group attached to the EC2 instance
   > - **GCP:** VPC Firewall Rules
   > - **DigitalOcean:** Cloud Firewalls
   >
   > Same rules, different UI. The concept is identical on every provider.

5. **Create the VPS** (Hetzner console → Servers → Create):
   - Location: Falkenstein (fsn1) is cheapest; Nuremberg (nbg1) or Helsinki (hel1) also fine
   - Image: Ubuntu 24.04
   - Type: CX42 (8 vCPU / 16 GB / 160 GB SSD)
   - SSH key: select the one you uploaded
   - Firewall: select the one you created
   - Name: `143-prod-1`

   > **`[PROVIDER-SPECIFIC]` Equivalents:**
   > - **AWS:** `aws ec2 run-instances --instance-type t3.xlarge --image-id ami-xxxxx --key-name 143-deploy --security-group-ids sg-xxxxx`
   > - **GCP:** `gcloud compute instances create 143-prod-1 --machine-type=e2-standard-4 --image-family=ubuntu-2404-lts`
   > - **DigitalOcean:** `doctl compute droplet create 143-prod-1 --size s-8vcpu-16gb --image ubuntu-24-04-x64`

#### Step 3: Bootstrap the VPS (Automated via cloud-init)

Every major cloud provider supports **cloud-init** — a user-data script that runs
automatically on first boot. This eliminates manual SSH setup entirely. You
provision the VPS with a cloud-init YAML, and it boots ready to go with Docker,
gVisor, GHCR access, and the app directory configured.

**Create `deploy/cloud-init/app.yml`** (for the single-node Phase 1 setup):

```yaml
#cloud-config

users:
  - name: deploy
    groups: docker
    shell: /bin/bash
    sudo: ALL=(ALL) NOPASSWD:ALL
    ssh_authorized_keys:
      - ${SSH_PUBLIC_KEY}   # replaced at provision time via envsubst

packages:
  - docker.io
  - docker-compose-plugin
  - jq

runcmd:
  # Install gVisor
  - curl -fsSL https://gvisor.dev/archive.key | gpg --dearmor -o /usr/share/keyrings/gvisor-archive-keyring.gpg
  - echo "deb [arch=amd64 signed-by=/usr/share/keyrings/gvisor-archive-keyring.gpg] https://storage.googleapis.com/gvisor/releases release main" > /etc/apt/sources.list.d/gvisor.list
  - apt-get update && apt-get install -y runsc
  - runsc install
  - systemctl restart docker

  # Set up GHCR access
  - su - deploy -c 'echo "${GHCR_TOKEN}" | docker login ghcr.io -u deploy --password-stdin'

  # Pull images
  - su - deploy -c 'docker pull ghcr.io/assembledhq/143-server:latest'
  - su - deploy -c 'docker pull ghcr.io/assembledhq/143-agent:latest'

  # Apply kernel tuning
  - sysctl -p /etc/sysctl.d/99-postgres.conf

  # Start the stack
  - su - deploy -c 'cd /opt/143 && docker compose -f docker-compose.prod.yml up -d'
  - su - deploy -c 'cd /opt/143 && docker compose -f docker-compose.prod.yml exec -T api /bin/migrate up'

write_files:
  - path: /opt/143/.env
    owner: deploy:deploy
    permissions: '0600'
    content: |
      SOPS_AGE_KEY=${SOPS_AGE_KEY}
      DB_PASSWORD=${DB_PASSWORD}

  - path: /opt/143/.env.production.enc
    owner: deploy:deploy
    permissions: '0600'
    encoding: b64
    content: ${ENV_PRODUCTION_ENC_B64}   # base64-encoded .env.production.enc

  # Kernel tuning for Postgres
  - path: /etc/sysctl.d/99-postgres.conf
    content: |
      vm.overcommit_memory = 2
      vm.overcommit_ratio = 80
      vm.swappiness = 1
```

The compose files, Caddyfile, and Postgres config are baked into the server
Docker image or SCP'd separately (see Step 9). For a fully self-contained setup,
add them as `write_files` entries.

**Provision with cloud-init** (the VPS boots fully configured, no SSH needed):

```bash
# Substitute secrets into the cloud-init template
export SSH_PUBLIC_KEY="$(cat ~/.ssh/143-deploy.pub)"
export GHCR_TOKEN="ghp_xxxx"
export SOPS_AGE_KEY="AGE-SECRET-KEY-xxxx"
export DB_PASSWORD="your-db-password"
export ENV_PRODUCTION_ENC_B64="$(base64 < .env.production.enc)"

envsubst < deploy/cloud-init/app.yml > /tmp/user-data.yml
```

> **`[PROVIDER-SPECIFIC]` Pass user-data at instance creation:**
>
> | Provider | Command |
> |----------|---------|
> | Hetzner | `hcloud server create --name 143-prod-1 --type cx42 --image ubuntu-24.04 --ssh-key 143-deploy --user-data "$(cat /tmp/user-data.yml)"` |
> | AWS | `aws ec2 run-instances --instance-type t3.xlarge --image-id ami-xxxxx --key-name 143-deploy --user-data file:///tmp/user-data.yml` |
> | GCP | `gcloud compute instances create 143-prod-1 --machine-type=e2-standard-4 --metadata-from-file user-data=/tmp/user-data.yml` |
> | DigitalOcean | `doctl compute droplet create 143-prod-1 --size s-8vcpu-16gb --image ubuntu-24-04-x64 --user-data "$(cat /tmp/user-data.yml)"` |

The VPS boots, cloud-init runs (~90 seconds), and the stack is up. Verify with:

```bash
ssh -i ~/.ssh/143-deploy deploy@<vps-ip> "docker compose -f /opt/143/docker-compose.prod.yml ps"
```

**Create role-specific cloud-init files** for multi-node (Phase 3):

| File | Role | What it starts |
|------|------|---------------|
| `deploy/cloud-init/app.yml` | API + frontend + Postgres (Phase 1 single-node) | `docker-compose.prod.yml` |
| `deploy/cloud-init/db.yml` | Dedicated Postgres (Phase 3a) | `docker-compose.db.yml` |
| `deploy/cloud-init/worker.yml` | Agent sandbox worker (Phase 3b) | `docker-compose.worker.yml` |
| `deploy/cloud-init/api.yml` | API-only node behind LB (Phase 3c) | `docker-compose.api.yml` |
| `deploy/cloud-init/redis.yml` | Redis cache + pub/sub (Phase 3d) | `docker-compose.redis.yml` |

Each is a variation of the same template — only the compose file and env vars
differ. The Docker + gVisor + kernel tuning section is identical across all roles.

**Alternative: `deploy/scripts/bootstrap.sh`** for machines where cloud-init isn't
available (e.g., bare metal, or re-provisioning an existing VPS):

```bash
#!/usr/bin/env bash
# deploy/scripts/bootstrap.sh — idempotent machine setup
# Usage: ssh root@<vps-ip> 'bash -s' < deploy/scripts/bootstrap.sh
set -euo pipefail

# Create deploy user (idempotent)
id deploy &>/dev/null || adduser --disabled-password --gecos "" deploy
usermod -aG docker deploy 2>/dev/null || true
mkdir -p /home/deploy/.ssh /opt/143
[ -f /root/.ssh/authorized_keys ] && cp /root/.ssh/authorized_keys /home/deploy/.ssh/
chown -R deploy:deploy /home/deploy/.ssh /opt/143
chmod 700 /home/deploy/.ssh

# Docker (idempotent)
command -v docker &>/dev/null || (curl -fsSL https://get.docker.com | sh)

# gVisor (idempotent)
command -v runsc &>/dev/null || {
  curl -fsSL https://gvisor.dev/archive.key | gpg --dearmor -o /usr/share/keyrings/gvisor-archive-keyring.gpg
  echo "deb [arch=amd64 signed-by=/usr/share/keyrings/gvisor-archive-keyring.gpg] https://storage.googleapis.com/gvisor/releases release main" \
    > /etc/apt/sources.list.d/gvisor.list
  apt-get update && apt-get install -y runsc
  runsc install
  systemctl restart docker
}

# Kernel tuning (idempotent)
cat > /etc/sysctl.d/99-postgres.conf <<SYSCTL
vm.overcommit_memory = 2
vm.overcommit_ratio = 80
vm.swappiness = 1
SYSCTL
sysctl -p /etc/sysctl.d/99-postgres.conf

echo "Bootstrap complete. Machine is ready for deploy."
```

Run it once: `ssh root@<vps-ip> 'bash -s' < deploy/scripts/bootstrap.sh`

#### Step 4: Container Registry (GHCR)

We use **GitHub Container Registry (ghcr.io)** for Docker images:

- **Free for public repos.** For private repos, included in your GitHub plan (free
  tier: 500 MB storage + 1 GB egress/month; Team/Enterprise includes much more).
- **Zero additional credentials in CI.** GitHub Actions uses the built-in `GITHUB_TOKEN`.
- **Standard Docker registry API.** Swap to ECR, GCR, or Docker Hub by changing
  image URLs in compose files — no code changes.

GHCR access on the VPS is configured by cloud-init (Step 3). If setting up
manually, create a GitHub PAT with `read:packages` scope:

```bash
echo "<your-ghcr-read-token>" | docker login ghcr.io -u <github-username> --password-stdin
```

#### Step 5: Secrets

The existing SOPS + age workflow works on any VPS. The `docker-entrypoint.sh`
already decrypts `.env.production.enc` at boot using `SOPS_AGE_KEY`.

Cloud-init (Step 3) writes the age key and encrypted env file to the VPS
automatically. If setting up manually, you need two files on the VPS:

```bash
# /opt/143/.env — the ONLY plaintext secret on disk
SOPS_AGE_KEY=AGE-SECRET-KEY-1XXXXXX...
DB_PASSWORD=<your-db-password>

# /opt/143/.env.production.enc — copied from the git repo
# Contains all other secrets, decrypted at container start by docker-entrypoint.sh
```

If you don't have a deploy key yet:
```bash
# On your local machine:
age-keygen -o /tmp/deploy-key.txt
# Copy the public key (age1...) and add it to .sops.yaml
# Then: make secrets-rotate && git add .sops.yaml .env.production.enc && git push
# The private key (AGE-SECRET-KEY-...) goes into the cloud-init template or VPS .env
rm /tmp/deploy-key.txt   # don't leave this lying around
```

#### Step 6: The Sandbox Image

The code defaults to `Image: "143-agent:latest"` (in
`internal/services/agent/adapter.go`), but **no Dockerfile for this image exists
yet**. This image must be created and pushed to GHCR before sandboxes can run.

The sandbox container runs with a read-only root filesystem (`--read-only`), only
`/workspace` and `/tmp` are writable, and everything executes as a non-root
`sandbox` user. The agent CLI tools are invoked via `sh -c` wrappers through
`provider.ExecStream()`.

**Create `Dockerfile.agent`** in the repo root:

```dockerfile
FROM ubuntu:24.04

# Core utilities required by the sandbox provider.
# All agent commands run via `sh -c`, file I/O uses cat/printf,
# and snapshots use tar. These are non-negotiable.
RUN apt-get update && apt-get install -y --no-install-recommends \
    git \
    curl \
    wget \
    ca-certificates \
    tar \
    gzip \
    jq \
    && rm -rf /var/lib/apt/lists/*

# Language runtimes — needed both by agents working on repos and by CLI installs.
RUN apt-get update && apt-get install -y --no-install-recommends \
    nodejs npm \
    python3 python3-pip python3-venv \
    golang-go \
    make \
    && rm -rf /var/lib/apt/lists/*

# Agent CLI tools — the orchestrator invokes these by name.
# Claude Code CLI
RUN curl -fsSL https://cli.anthropic.com/install.sh | sh

# Codex CLI (OpenAI)
RUN npm install -g @openai/codex

# Gemini CLI (Google)
RUN npm install -g @google/gemini-cli

# Create the sandbox user. The Docker provider runs all commands as this user.
# Home is /workspace so agent tools find their config files
# (e.g., ~/.claude/, ~/.codex/auth.json) in the workspace.
RUN useradd -m -d /workspace -s /bin/bash sandbox

# Create directories that agent CLIs expect to write to.
# These are inside /workspace so they survive the read-only rootfs.
RUN mkdir -p /workspace/.claude /workspace/.codex /workspace/.gemini \
    && chown -R sandbox:sandbox /workspace

USER sandbox
WORKDIR /workspace

# The Docker provider starts the container with `sleep infinity`
# and then exec's agent commands into it.
CMD ["sleep", "infinity"]
```

**Build and push** as part of the CI workflow (see the deploy workflow below —
it builds and pushes `ghcr.io/assembledhq/143-agent` alongside the server image).

On the VPS, the image is pulled automatically by `docker compose pull`. For the
initial bootstrap, pull it manually:

```bash
docker pull ghcr.io/assembledhq/143-agent:latest
```

#### Step 7: GitHub Webhooks

Your GitHub App sends webhooks to `https://143.dev/api/github/webhooks` (or
similar). Configure the GitHub App's webhook URL to point at your domain.

Verify after setup:
- Firewall allows port 443 from the internet (step 2)
- `GITHUB_WEBHOOK_SECRET` in the VPS env matches the GitHub App config
- Push a test commit or open a PR and check the server logs for webhook receipt

#### Step 8: GitHub Actions Secrets

Add these secrets in GitHub → Settings → Secrets and Variables → Actions:

| Secret | Value | Notes |
|--------|-------|-------|
| `DEPLOY_HOST` | VPS IP address (e.g., `65.108.xxx.xxx`) | The public IP of your VPS |
| `DEPLOY_SSH_KEY` | Contents of `~/.ssh/143-deploy` (the **private** key) | Used by the deploy workflow to SSH in |

These are provider-agnostic — they work the same whether the VPS is on Hetzner,
AWS, GCP, or anywhere else with SSH access.

#### Step 9: Copy Config Files and Start

If you used cloud-init (Step 3), the VPS is already running. Skip to
verification below.

If you bootstrapped manually (via `bootstrap.sh`), copy the config files and
start the stack:

```bash
# Copy config files to the VPS
scp -i ~/.ssh/143-deploy docker-compose.prod.yml deploy@<vps-ip>:/opt/143/
scp -i ~/.ssh/143-deploy -r deploy/ deploy@<vps-ip>:/opt/143/deploy/
scp -i ~/.ssh/143-deploy .env.production.enc deploy@<vps-ip>:/opt/143/

# SSH in, pull images, and start
ssh -i ~/.ssh/143-deploy deploy@<vps-ip>
cd /opt/143
docker compose -f docker-compose.prod.yml pull
docker compose -f docker-compose.prod.yml up -d
docker compose -f docker-compose.prod.yml exec -T api /bin/migrate up
```

**Verify** (regardless of how you bootstrapped):

```bash
ssh -i ~/.ssh/143-deploy deploy@<vps-ip>
docker compose -f /opt/143/docker-compose.prod.yml ps
# All services should be "Up" and healthy

curl http://localhost:8080/healthz
# Should return 200
```

After the first deploy, the GitHub Actions workflow handles image pulls and
restarts automatically. Manual setup is only for the initial bootstrap.

At this point the VPS is running but serving on a raw IP. Caddy won't issue TLS
certs until DNS points at it — see [DNS and TLS Setup](#dns-and-tls-setup) below.
First, create the config files that `docker-compose.prod.yml` references.

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
      REDIS_URL: ${REDIS_URL:-}   # optional — leave empty to disable Redis
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
    image: postgres:18.0            # pin minor version to avoid surprise upgrades
    shm_size: 2g                    # must match or exceed shared_buffers; default 64MB will crash under load
    environment:
      POSTGRES_USER: onefortythree
      POSTGRES_PASSWORD: ${DB_PASSWORD}
      POSTGRES_DB: onefortythree
      POSTGRES_INITDB_ARGS: "--data-checksums"
    volumes:
      - pgdata:/var/lib/postgresql/data
      - ./deploy/postgres/postgresql.conf:/etc/postgresql/conf.d/custom.conf:ro
      - ./deploy/postgres/pg_hba.conf:/etc/postgresql/conf.d/pg_hba.conf:ro
    command: postgres -c config_file=/etc/postgresql/conf.d/custom.conf -c hba_file=/etc/postgresql/conf.d/pg_hba.conf
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
          memory: 4G              # give Postgres room for shared_buffers + connections + OS cache
          cpus: "2.0"

volumes:
  pgdata:
  caddy_data:
```

#### 2. `deploy/Caddyfile`

```
# Redirect www → apex (pick one canonical domain).
# Replace 143.dev with your actual domain.
www.143.dev {
    redir https://143.dev{uri} permanent
}

143.dev {
    handle /api/* {
        reverse_proxy api:8080
    }
    handle {
        reverse_proxy frontend:3000
    }
}
```

Note: Caddy site addresses are literal hostnames, not environment variables.
Update the domain directly in this file for your deployment.

Caddy automatically provisions TLS certificates via Let's Encrypt for every
domain listed in the Caddyfile. No cert management, no certbot cron, no renewal
scripts. It just works.

**You do NOT need a separate load balancer for Phase 1.** Caddy serves as the
TLS-terminating reverse proxy on the same VPS. It listens on ports 80/443 and
routes requests to the API and frontend containers. A dedicated load balancer
only becomes relevant in Phase 3c (multiple API nodes).

#### 3. `deploy/postgres/postgresql.conf`

Production-tuned Postgres configuration. See the [PostgreSQL Configuration](#production-postgres-configuration) section below.

#### 4. `Dockerfile.agent`

The agent sandbox image. Contains the agent CLI tools (Claude Code, Codex,
Gemini), git, core utilities, and common language runtimes. See
[Step 6](#step-6-the-sandbox-image) for the full Dockerfile and rationale.

Built and pushed to GHCR as `ghcr.io/assembledhq/143-agent:latest` by the CI
workflow.

### Code Changes Required

**No application code changes for Phase 1.** The existing `Dockerfile`, `docker.go`
sandbox provider, and server binary all work as-is on any VPS with Docker. The
changes are new config/infra files checked into the repo (`docker-compose.prod.yml`,
`Dockerfile.agent`, `Caddyfile`,
`postgresql.conf`).

### CI/CD: GitHub Actions Deploy Workflow

SSH-based deployment triggered on push to main.

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
  AGENT_IMAGE: ghcr.io/assembledhq/143-agent
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

      - name: Build & push agent sandbox image
        uses: docker/build-push-action@v6
        with:
          context: .
          file: Dockerfile.agent
          push: true
          tags: |
            ${{ env.AGENT_IMAGE }}:latest
            ${{ env.AGENT_IMAGE }}:${{ github.sha }}

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

### Database Initialization

Postgres starts with an empty database. The migration tool creates all tables:

```bash
# Start Postgres
docker compose -f docker-compose.prod.yml up -d postgres

# Run migrations (creates all tables)
docker compose -f docker-compose.prod.yml exec -T api /bin/migrate up

# Verify
docker exec 143-postgres-1 \
  psql -U onefortythree -c "\dt"
# Should list all application tables
```

### DNS and TLS Setup

Point your domain at the VPS. DNS is assumed to be managed in Cloudflare (or any
DNS provider).

1. **Create DNS records in Cloudflare:**

   | Record | Type | Value |
   |--------|------|-------|
   | `143.dev` | A | `<vps-ip>` |
   | `www.143.dev` | CNAME | `143.dev` |

   If using Cloudflare's orange-cloud proxy (recommended):
   - Cloudflare terminates TLS and forwards to your VPS over HTTPS
   - This gives you Cloudflare's DDoS protection and CDN caching for free
   - Caddy still issues its own Let's Encrypt cert (Cloudflare connects to your
     origin over HTTPS with Caddy's cert)

   If using DNS-only (grey cloud):
   - Traffic goes directly to your VPS
   - Caddy handles TLS end-to-end
   - Simpler, but no Cloudflare CDN/DDoS protection

2. **Verify Caddy can issue certs.** Caddy needs to respond on port 80 for the
   ACME HTTP-01 challenge. Make sure the VPS firewall allows inbound 80 and 443.
   Caddy will automatically request certs the first time it receives traffic for
   the domain.

3. **Verify TLS.** Open `https://143.dev` in a browser. Caddy should have
   auto-provisioned the Let's Encrypt cert. Check:
   ```bash
   dig +short 143.dev
   # Should return your VPS IP

   curl -I https://143.dev/healthz
   # Should return 200 with a valid TLS cert
   ```

### Setup Checklist

**Infrastructure:**
- [ ] Provision VPS (any provider — see sizing table above)
- [ ] Install Docker + gVisor (Step 3)
- [ ] Set up GHCR pull access on VPS (`docker login ghcr.io`) (Step 4)
- [ ] Copy SOPS age key to VPS `.env` (Step 5)

**Repo changes:**
- [ ] Create `Dockerfile.agent` — agent sandbox image with CLI tools (Step 6)
- [ ] Create `deploy/` directory: Caddyfile, postgresql.conf, deploy scripts
- [ ] Create `docker-compose.prod.yml`
- [ ] Create `.github/workflows/deploy.yml` — builds and pushes server + agent images to GHCR
- [ ] Verify images are pushed to GHCR: `ghcr.io/assembledhq/143-server` and `ghcr.io/assembledhq/143-agent`

**Go live:**
- [ ] Copy config files to VPS and start all services (Steps 9-10)
- [ ] Run migrations (`/bin/migrate up`)
- [ ] Point DNS at VPS IP
- [ ] Verify `https://143.dev` loads correctly (TLS, API, frontend)
- [ ] Verify `https://www.143.dev` redirects to `https://143.dev`
- [ ] Configure GitHub App webhook URL to point at `https://143.dev`
- [ ] Monitor logs for errors

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

#### 2. WAL Archiving (Required — Near-Zero Data Loss)

WAL-G provides continuous WAL streaming to object storage. This is required for
production — 6 hours of potential data loss from pg_dump alone is not acceptable
for paying customers.

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

- [ ] Set up `pg-backup.sh` cron (Layer 2)
- [ ] Configure offsite backup sync (`rclone` to S3-compatible storage)
- [ ] Enable WAL-G archiving (Layer 3) — **required before accepting users**
- [ ] Run a restore drill — verify both pg_dump and WAL-G restores work
- [ ] Set up monitoring (Datadog, Prometheus, or even just cron + email alerts)

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

#### 2. Read/Write Splitting (Go code change — required for Phase 3c)

When you add a Postgres read replica (which you should when separating the DB in
Step 3a), the app needs to route read-heavy queries (dashboard, audit log,
experiment reads) to the replica:

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
    image: postgres:18.0
    shm_size: 4g
    environment:
      POSTGRES_DB: onefortythree
      POSTGRES_USER: onefortythree
      POSTGRES_PASSWORD: ${DB_PASSWORD}
      POSTGRES_INITDB_ARGS: "--data-checksums"
    volumes:
      - pgdata:/var/lib/postgresql/data
      - ./deploy/postgres/postgresql.conf:/etc/postgresql/conf.d/custom.conf:ro
      - ./deploy/postgres/pg_hba.conf:/etc/postgresql/conf.d/pg_hba.conf:ro
      - ./deploy/postgres/certs:/var/lib/postgresql/certs:ro
    command: postgres -c config_file=/etc/postgresql/conf.d/custom.conf -c hba_file=/etc/postgresql/conf.d/pg_hba.conf
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
          memory: 8G
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
      SANDBOX_IMAGE: ghcr.io/assembledhq/143-agent:latest
      SANDBOX_RUNTIME: runsc
      REDIS_URL: ${REDIS_URL:-}
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

For workers outside the primary Hetzner private network, enroll the host in
Tailscale and publish the Tailscale address as the worker's internal preview
endpoint. Keep the role-specific auth keys and host selection in
`.env.production.enc`:

```dotenv
TS_AUTH_KEY_APP=tskey-auth-...
TS_AUTH_KEY_DB=tskey-auth-...
TS_AUTH_KEY_WORKER=tskey-auth-...
TS_AUTH_KEY_REDIS=tskey-auth-...
TS_TAG_APP=tag:prod-app
TS_TAG_DB=tag:prod-db
TS_TAG_WORKER=tag:prod-worker
TS_TAG_REDIS=tag:prod-redis
TS_WORKER_HOSTS=worker-usw-1:<worker-public-management-ip>
```

Provisioning then derives the correct Tailscale behavior from the role and
host map:

```bash
make provision-worker \
  HOST=<worker-public-management-ip>
```

The worker writes `WORKER_PRIVATE_IP=<tailscale ip -4>` and
`PREVIEW_INTERNAL_BASE_URL=http://<tailscale-ip>:8080` into `/opt/143/.env.local`.
App nodes use that URL for signed internal preview RPC, so every app node that
can route previews to tailnet-backed workers must also be enrolled in Tailscale:

```bash
make provision-app \
  HOST=<app-public-management-ip>
```

The database node must also bind Postgres to an explicit primary private address
with `DB_BIND_IP` rather than `0.0.0.0`. Keep the default in-region `DB_HOST`
pointed at that private address so Ashburn app/worker nodes retain a direct DB
path if the tailnet control plane or tunnels are unavailable. To add
cross-region workers, enroll the database node or an Ashburn subnet router with
`TS_AUTH_KEY_DB`; provisioning advertises `DB_BIND_IP/32` automatically. Approve
that route in Tailscale and keep the out-of-region workers on the same
`DB_HOST=<db private ip>`. Provisioning always passes `--accept-routes=true` for
workers in `TS_WORKER_HOSTS` so Linux installs the advertised route. If the
overlay is down, those out-of-region workers stop reaching Postgres, but
same-datacenter nodes keep connecting over the private network because Docker
and Postgres do not depend on the Tailscale address being present.

Redis is the same pattern as the database node: enroll the Redis node or an
Ashburn subnet router with `TS_AUTH_KEY_REDIS`; provisioning advertises
`REDIS_PRIVATE_IP/32` automatically. Approve that route in Tailscale so
out-of-region workers can keep using `REDIS_PRIVATE_IP` for Redis without
exposing Redis on a public interface.

For already-provisioned app, db, or Redis nodes, enroll Tailscale without
touching containers or volumes:

```bash
make tailscale-enroll ROLE=app HOST=<app-public-management-ip>
make tailscale-enroll ROLE=db HOST=<db-public-management-ip>
make tailscale-enroll ROLE=redis HOST=<redis-public-management-ip>
```

**Worker VPS sizing:**

| VPS Size | `MAX_CONCURRENT_RUNS` | Good for |
|----------|----------------------|----------|
| 4 CPU / 8 GB | 1 | Small / test |
| 8 CPU / 16 GB | 2-3 | Medium |
| 16 CPU / 32 GB | 5-6 | Production sweet spot |
| 32 CPU / 64 GB | 10-12 | Heavy workloads |

Each sandbox gets `SANDBOX_CPU_LIMIT` (default 2) cores and
`SANDBOX_MEMORY_LIMIT` (default 4 GB). Rule of thumb: reserve 2 CPU + 2 GB for
the worker process and OS, then divide the remainder. For example, 16 CPU / 32 GB
→ 14 CPU / 30 GB available → 6 sandboxes (12 CPU / 24 GB) with comfortable
headroom.

**Move to Step 3c when:** you need API redundancy (uptime SLA), or a single API
node can't keep up with webhook volume.

#### Step 3c: Multiple API Nodes + Managed Load Balancer

API nodes are stateless — sessions live in Postgres. Add as many as needed behind
a managed load balancer. Keep at least one node as `mode=all` so the scheduler
runs. All `api` and `all` nodes serve the same HTTP traffic.

```
              ┌──────────────────┐
              │  Managed LB      │
              │  :443 (TLS)      │
              └──┬──────────┬────┘
                 │          │
           ┌─────▼──┐  ┌───▼────┐
           │ VPS-2  │  │ VPS-6  │
           │ all    │  │ api    │
           │ :8080  │  │ :8080  │
           └────┬───┘  └───┬────┘
                │          │
  ┌─────────────▼──────────▼────────────┐
  │          VPS-1 (DB)                  │
  │          Postgres                    │
  └──────────────────────────────────────┘
```

Use a managed LB from your cloud provider. It's HA by default (no SPOF to
manage), handles TLS termination, and supports health-checked routing out of the
box. This is the end-state architecture — no intermediate self-hosted LB step.

> **`[PROVIDER-SPECIFIC]` Managed load balancers:**
>
> | Provider | Service | Cost | Health Checks | TLS Termination |
> |----------|---------|------|---------------|-----------------|
> | Hetzner | Hetzner Load Balancer | ~€6/mo | Yes (HTTP/TCP) | Yes (upload cert or Let's Encrypt) |
> | AWS | ALB (Application LB) | ~$16/mo + per-request | Yes (HTTP path) | Yes (ACM certs, free) |
> | GCP | Cloud Load Balancing | ~$18/mo + per-request | Yes (HTTP path) | Yes (managed certs) |
> | DigitalOcean | DO Load Balancer | ~$12/mo | Yes (HTTP/TCP) | Yes (Let's Encrypt) |

**Setup:**

1. Create a managed LB from your provider's console or API
2. Configure the health check to hit `/healthz` on port 8080
3. Add your API VPSes as backend targets (use private IPs)
4. Enable TLS termination — the LB handles HTTPS, your API nodes serve plain
   HTTP on port 8080
5. Point your domain's DNS at the LB's IP (instead of a single VPS IP)

**With TLS termination on the LB**, you can simplify the API node setup: remove
Caddy from the API VPSes entirely. The LB handles HTTPS and forwards plain HTTP
to port 8080. Each API node just runs the server container — no reverse proxy
needed.

Note: Caddy remains on the single-node Phase 1 setup (where it handles TLS +
reverse proxy on the same machine). When you move to Phase 3c, the managed LB
replaces Caddy's role.

**Adding/removing API nodes:** Register or deregister backend targets with the
LB via the provider's console or API. Health checks automatically stop routing
to unhealthy nodes.

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

#### Step 3d: Add a Dedicated Redis Node

Redis provides shared caching, pub/sub for real-time log streaming, distributed
rate limiting, and job queue notifications. See
[52-redis.md](../implemented/52-redis.md) for the full design. Redis is **optional** —
all code paths fall back to current behavior (Postgres polling, per-node rate
limiting) when Redis is unavailable.

```
┌───────────────┐     ┌───────────────┐     ┌───────────────┐
│  VPS-1 (DB)   │     │  VPS-2 (App)  │     │  VPS-6 (Redis)│
│               │     │  mode=all     │     │               │
│  Postgres  ◄──┼─────┤  Caddy        ├────►│  Redis 8.6    │
│               │  ┌──┤               │     │  :6379        │
└───────────────┘  │  └───────────────┘     └───────▲───────┘
                   │                                │
        ┌──────────┼──────────┐                     │
        │          │          │                     │
   ┌────▼────┐ ┌──▼──────┐ ┌─▼───────┐             │
   │ VPS-3   │ │ VPS-4   │ │ VPS-5   │─────────────┘
   │ worker  │ │ worker  │ │ worker  │  (all nodes connect
   │ 5 runs  │ │ 5 runs  │ │ 5 runs  │   to shared Redis)
   └─────────┘ └─────────┘ └─────────┘
```

**New file: `docker-compose.redis.yml`** (runs on the Redis VPS):

```yaml
services:
  redis:
    image: redis:8.6-alpine
    ports:
      - "0.0.0.0:6379:6379"   # accessible from private network
    volumes:
      - redisdata:/data
    healthcheck:
      test: ["CMD", "redis-cli", "ping"]
      interval: 5s
      timeout: 3s
      retries: 5
    restart: unless-stopped
    deploy:
      resources:
        limits:
          memory: 512M
          cpus: "1.0"
    command: >-
      redis-server
        --save 60 1
        --loglevel warning
        --maxmemory 400mb
        --maxmemory-policy allkeys-lru
        --bind 0.0.0.0
        --protected-mode no
        --requirepass ${REDIS_PASSWORD}

volumes:
  redisdata:
```

**Cloud-init template: `deploy/cloud-init/redis.yml`**

```yaml
#cloud-config

users:
  - name: deploy
    groups: docker
    shell: /bin/bash
    sudo: ALL=(ALL) NOPASSWD:ALL
    ssh_authorized_keys:
      - ${SSH_PUBLIC_KEY}

packages:
  - docker.io
  - docker-compose-plugin

runcmd:
  # No gVisor needed — Redis doesn't run sandboxes
  - su - deploy -c 'cd /opt/143 && docker compose -f docker-compose.redis.yml up -d'

write_files:
  - path: /opt/143/.env
    owner: deploy:deploy
    permissions: '0600'
    content: |
      REDIS_PASSWORD=${REDIS_PASSWORD}

  - path: /opt/143/docker-compose.redis.yml
    owner: deploy:deploy
    permissions: '0644'
    encoding: b64
    content: ${DOCKER_COMPOSE_REDIS_B64}

  # Kernel tuning for Redis
  - path: /etc/sysctl.d/99-redis.conf
    content: |
      vm.overcommit_memory = 1
      net.core.somaxconn = 512
```

**Redis VPS sizing:** Redis is extremely lightweight for 143's workload. A
`CX22` (2 vCPU / 4 GB, ~€4/month on Hetzner) is more than sufficient. Redis
uses ~256MB for the expected workload (pub/sub messages are transient, rate-limit
keys are small integers, cached tokens are <1KB each).

> **`[PROVIDER-SPECIFIC]` Redis VPS sizing:**
>
> | Provider | Instance Type | Monthly Cost | Notes |
> |----------|--------------|-------------|-------|
> | Hetzner | CX22 (2 vCPU / 4 GB) | ~€4 (~$5) | More than enough for cache + pub/sub |
> | AWS | t3.micro (2 vCPU / 1 GB) | ~$8 | Or use ElastiCache `cache.t4g.micro` (~$12/mo) for managed |
> | GCP | e2-micro (shared 2 vCPU / 1 GB) | ~$7 | Or use Memorystore for managed |
> | DigitalOcean | s-1vcpu-1gb | ~$6 | Simple, adequate for small-medium scale |

**Connecting app/worker nodes to Redis:**

All app and worker nodes need `REDIS_URL` in their environment, pointing to the
Redis VPS's private IP:

```bash
# Add to .env on each app/worker VPS:
REDIS_URL=redis://:${REDIS_PASSWORD}@10.0.0.X:6379/0
```

Update `docker-compose.prod.yml` (app node) to pass `REDIS_URL`:

```yaml
  api:
    environment:
      # ... existing vars ...
      REDIS_URL: ${REDIS_URL}   # optional — omit or leave empty to disable Redis
```

Update `docker-compose.worker.yml` (worker nodes) similarly:

```yaml
  worker:
    environment:
      # ... existing vars ...
      REDIS_URL: ${REDIS_URL}
```

**Security:** Redis listens on 0.0.0.0 but requires a password
(`--requirepass`). The Hetzner firewall should block port 6379 from the public
internet — only allow it from the private network CIDR (e.g., `10.0.0.0/16`).

**Move to managed Redis when:** you need HA (automatic failover), or you're
managing 10+ nodes and want one fewer thing to operate. See
[52-redis.md Section 6](../implemented/52-redis.md#6-production-migrating-to-hosted-redis)
for the migration path — it's a config change (update `REDIS_URL`), not a code
change.

### Fleet Deployment

Once you have multiple nodes, the Phase 1 single-node deploy script doesn't
scale. Here's a fleet deployment script that works with any provider.

#### `deploy/scripts/deploy-fleet.sh`

```bash
#!/usr/bin/env bash
set -euo pipefail

# Deploy to all nodes in the fleet.
# Usage: ./deploy-fleet.sh [image-tag]
#
# Reads node IPs from FLEET_HOSTS env var.
# Provider-agnostic — just needs SSH access.

TAG="${1:-latest}"
SERVER_IMAGE="ghcr.io/assembledhq/143-server:$TAG"
AGENT_IMAGE="ghcr.io/assembledhq/143-agent:$TAG"

echo "Deploying $TAG to fleet..."

while IFS= read -r IP; do
  [[ -z "$IP" || "$IP" == \#* ]] && continue
  echo "--- Deploying to $IP ---"

  ssh -o StrictHostKeyChecking=no deploy@"$IP" << REMOTE
    docker pull $SERVER_IMAGE
    docker pull $AGENT_IMAGE
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
| SSE polling > 500 QPS or rate limits need cross-node enforcement | Add Redis node (Step 3d) |
| Need 99.9%+ uptime | Multiple API nodes + Postgres HA (Step 3c + Phase 4) |

---

## Phase 4: Fleet Automation and High Availability (Future)

**Goal:** Automated node provisioning, auto-scaling workers based on queue depth,
and Postgres high availability.

**When to do this:** When you're managing 5+ nodes and
provisioning/decommissioning frequently enough that it's a chore.

### Automated Node Provisioning

Phase 1 introduced cloud-init scripts for bootstrapping VPSes (see
[Step 3](#step-3-bootstrap-the-vps-automated-via-cloud-init)). In Phase 4, the
auto-scaler uses these same cloud-init templates programmatically — the
`CloudProvider.CreateInstance()` method passes the `worker.yml` cloud-init as
user-data when provisioning new VPSes.

The role-specific cloud-init files (`deploy/cloud-init/worker.yml`,
`deploy/cloud-init/db.yml`, etc.) are already defined in Phase 1. The auto-scaler
just calls the cloud provider API with the appropriate template.

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

Self-hosted streaming replication with automatic failover via
[Patroni](https://github.com/patroni/patroni). This builds on the read replica
setup from the [Streaming Replication](#streaming-replication-phase-3) section.

- Primary handles all writes (job queue, agent runs, webhooks)
- Replica handles read-heavy queries (dashboard, audit log, experiments)
- Patroni manages automatic failover (< 30 seconds) using etcd for consensus
- Requires the read/write splitting code change from Step 3c

**When to add Patroni:** When you have paying customers and can't tolerate manual
failover (5-30 minutes). Until then, the manual failover runbook in the
Streaming Replication section is sufficient.

**Patroni adds:** 3-node etcd cluster (can run on existing VPSes) + Patroni
sidecar container on each Postgres VPS. This is operational complexity but gives
you sub-minute automatic failover without depending on a third-party managed
database service.

---

## Known Scaling Constraints

These are application-level issues that don't need to be solved now, but will
surface as you scale through the phases above. They are documented here so you
can plan ahead and don't get surprised.

### Things That Break in Multi-Node (Must Fix Before Phase 3)

#### 1. Snapshot Storage is Local Disk

**Current:** `FileSnapshotStore` writes session snapshots (tar.gz of the sandbox
workspace) to the local filesystem of whichever node ran the agent.

**Problem:** In a multi-node deployment, if worker node A creates a snapshot for
a session, and the user later resumes that session via API node B, the snapshot
file doesn't exist on node B. Multi-turn sessions break silently.

**Fix (required for Phase 3):** Switch `FileSnapshotStore` to an S3-compatible
backend (`S3SnapshotStore`). This is a straightforward change — the store
interface is already abstracted. Use the same S3-compatible storage configured
for backups and WAL-G.

#### 2. Rate Limiting is Per-Node In-Memory

**Current:** `middleware.RateLimit()` uses an in-memory rate limiter on each API
node independently.

**Problem:** With 3 API nodes behind a load balancer, the effective rate limit is
3x what you configured. An org hitting all 3 nodes gets 3x the allowed requests.
More importantly, there's no way to enforce org-level concurrency limits on agent
runs across the fleet.

**Fix (required for Phase 3c):**
- **Redis sliding-window counter (recommended):** With a Redis node deployed
  (Step 3d), use the distributed rate limiter described in
  [52-redis.md Section 2.2](implemented/52-redis.md#22-distributed-rate-limiting).
  A simple `INCR`/`EXPIRE` counter on Redis provides globally-consistent rate
  limiting across all nodes with sub-millisecond overhead. Falls back to per-node
  in-memory limiting if Redis is unavailable.
- **Postgres-backed** (simpler, no Redis): rate limit check is a single
  `SELECT count(*) FROM requests WHERE org_id = $1 AND created_at > now() - interval '1 minute'`.
  Adds ~1ms per request. Fine up to ~1000 req/sec.
- **Load balancer sticky sessions**: route all requests from an org to the same
  API node. Per-node rate limiting then works correctly. Simplest if your LB
  supports it, but limits horizontal scaling benefits.
- For org-level agent run concurrency, the existing `maxConcurrent` check already
  queries Postgres, so it works correctly across nodes today. The concern is only
  for HTTP request rate limiting.

### Things That Degrade at Scale (Plan Ahead)

#### 3. SSE Log Streaming Polls the Database

**Current:** The `StreamLogs` SSE endpoint polls `session_logs` every 1 second
per connected client, querying for new rows since `lastSeenID`.

**Impact:** With 50 concurrent viewers streaming logs, that's 50 QPS against the
`session_logs` table just for streaming — on top of the writes from active agent
runs. This is fine at low scale but becomes a significant fraction of DB load as
you grow.

**When it matters:** When you regularly have 50+ concurrent streaming sessions
across all API nodes. Monitor `session_logs` query frequency in `pg_stat_statements`.

**Fix options:**
- **Redis Streams (recommended):** With a Redis node deployed (Step 3d), use
  Redis Streams for log delivery as described in
  [52-redis.md Section 2.1](implemented/52-redis.md#21-redis-streams-for-real-time-events-highest-value).
  A fan-out goroutine per node per active session calls `XREAD BLOCK` and
  broadcasts to local SSE clients via Go channels, reducing per-client DB
  queries to zero. Falls back to 1s Postgres polling if Redis is unavailable.
- **Postgres LISTEN/NOTIFY**: The orchestrator issues `NOTIFY session_log, '<run_id>'`
  on each log write. SSE handlers `LISTEN` on the channel and only query when
  notified. Eliminates polling entirely. Medium code change. No new dependency
  but doesn't address caching or rate limiting.
- **In-memory broadcast with cross-node pub/sub**: Each node maintains an
  in-memory channel per active session. Log writes fan out locally. Cross-node
  delivery via Postgres NOTIFY or Redis pub/sub. More complex but eliminates
  per-client DB queries entirely.

#### 4. Job Queue Contention Above ~20 Workers

**Current:** Workers dequeue jobs via `FOR UPDATE SKIP LOCKED` on the `jobs`
table. This is an excellent pattern for moderate scale — simple, reliable, no
external dependencies.

**Impact:** Each worker polls every 5 seconds. With 20 workers, that's 4 dequeue
attempts/second, all competing for the same index. Postgres handles this fine.
But at 50+ workers (250+ concurrent sandbox capacity), lock contention starts to
degrade dequeue throughput.

**When it matters:** When you see `jobs.dequeue.duration` p99 exceeding 100ms, or
workers spending more time waiting for locks than executing jobs.

**Future fix options (not needed now):**
- **LISTEN/NOTIFY for job pickup**: Instead of polling every 5s, workers `LISTEN`
  on a `jobs_pending` channel. The enqueue path issues `NOTIFY`. Workers only hit
  the DB when there's actually work. Eliminates thundering herd. Low-medium code
  change.
- **Queue sharding by org_id**: Partition the dequeue query so workers claim jobs
  for a subset of orgs. Reduces contention linearly. Medium code change.
- **Dedicated job queue (Temporal, RabbitMQ)**: Replace the Postgres-backed queue
  entirely. High code change, but unlocks 10-100x throughput. Only worth it if
  you're processing >1000 jobs/sec.

#### 5. Unbounded Table Growth

Several tables grow without bound and will eventually degrade query performance
and consume disk:

| Table | Growth Pattern | Current Mitigation | Scaling Risk |
|-------|---------------|-------------------|--------------|
| `session_logs` | ~1000 rows/min per active agent | None (append-only) | Queries slow after ~10M rows |
| `webhook_deliveries` | 1 row per webhook received | None | Table bloat, no reads after processing |
| `audit_log` | 1 row per auditable action | Immutable (no UPDATE/DELETE trigger) | Slow queries for compliance reports |
| `jobs` | 1 row per job (completed jobs stay) | None | Dequeue index includes dead rows |

**When it matters:** When any of these tables exceeds ~10M rows. Monitor with
`SELECT relname, n_live_tup FROM pg_stat_user_tables ORDER BY n_live_tup DESC`.

**Future fix options:**
- **Table partitioning** (Postgres native): Partition `session_logs` and
  `audit_log` by month. Old partitions can be detached and archived. Queries
  against recent data stay fast. Medium migration complexity.
- **Job archival**: Move completed/dead-lettered jobs to a `jobs_archive` table
  periodically. Keeps the hot `jobs` table small. Low code change.
- **Webhook TTL**: Delete `webhook_deliveries` older than 30 days via cron.
  They're only useful for debugging recent issues. Trivial.

#### 6. Scheduler Is a Single-Node Bottleneck

**Current:** The scheduler uses `pg_try_advisory_lock(143143143)` so only one
node runs scheduled tasks (PM analysis, cleanup, etc.). This is correct — you
don't want duplicate cron jobs — but it means:

- If the lock-holding node dies, it takes up to 30 seconds (session timeout) for
  another node to take over.
- All scheduled work runs on one node. If a scheduled task is CPU/memory
  intensive, it competes with that node's API or worker duties.

**When it matters:** Rarely. The scheduler is lightweight and leader failover at
30 seconds is fine for periodic tasks. Only becomes a concern if you add
time-sensitive scheduled work (e.g., SLA-based alerting).

**Future fix:** Run the scheduler on a dedicated `mode=scheduler` node, or reduce
the advisory lock timeout. Low priority.

---

## Enterprise Scale Considerations

The architecture described in Phases 1-4 works well from launch through moderate
scale (tens of customers, dozens of concurrent agent runs). This section addresses
what changes when targeting **thousands of large enterprise customers** with
strict SLA, compliance, and security requirements.

These are not things to build now — but they should inform design decisions today
so the architecture doesn't paint us into a corner.

### Zero-Downtime Deployments

**The problem:** The current deploy scripts do `docker compose up -d --remove-orphans`,
which restarts containers. During the restart (even if it's only seconds),
in-flight requests fail and SSE streams disconnect.

- **Phase 1 (single node):** Downtime during deploys is acceptable. You'll have
  a maintenance window anyway.
- **Phase 3+ (multi-node):** Unacceptable. Enterprise SLAs require deploys with
  no visible impact to users.

**The fix: drain → deploy → health check → re-add.** The fleet deploy script
already iterates over nodes. The change is to remove each node from the load
balancer before restarting, and only re-add it after the health check passes.

**Zero-downtime deploy flow for Phase 3+:**

```
For each node in fleet:
  1. Deregister node from managed LB (stop sending traffic)
  2. Wait for in-flight requests to complete (graceful drain, ~30s)
  3. Pull new images
  4. docker compose up -d --remove-orphans
  5. Wait for /healthz to return 200
  6. Re-register node with managed LB
  7. Move to next node
```

Use the provider API to deregister/register target nodes:

> **`[PROVIDER-SPECIFIC]` LB target management:**
>
> | Provider | Deregister | Register |
> |----------|-----------|----------|
> | Hetzner | `hcloud load-balancer remove-target <lb> --server <node>` | `hcloud load-balancer add-target <lb> --server <node>` |
> | AWS | `aws elbv2 deregister-targets --target-group-arn <arn> --targets Id=<instance>` | `aws elbv2 register-targets ...` |
> | GCP | `gcloud compute backend-services remove-backend ...` | `gcloud compute backend-services add-backend ...` |

**For worker nodes:** Workers already support graceful shutdown — `SIGTERM`
triggers drain (finish current jobs, stop accepting new ones, set status to
`dead`). The deploy script should send `SIGTERM`, wait for drain, then restart.

**Canary deploys:** Deploy to a single node first (`--canary` flag), monitor for
5-10 minutes, then roll to the rest. A bad image only affects 1/N of traffic
instead of everything.

```bash
# deploy-fleet.sh --canary
# 1. Deploy to first node only
# 2. Monitor error rate for 5 minutes
# 3. If error rate > threshold, rollback that one node
# 4. If clean, deploy to remaining nodes
```

### Multi-Tenancy and Noisy Neighbor Isolation

**The problem:** All orgs share one Postgres, one job queue, one set of workers,
and one Docker network. At enterprise scale:

- One org running 50 agent jobs can starve every other org
- No per-org resource quotas beyond the crude `maxConcurrent=3` limit
- No billing-tier differentiation (paying customers get the same queue priority as free)

**Direction:**

1. **Weighted job queue priority.** Add a `priority` column to the `jobs` table
   (or use the existing one) and assign higher priority to paying/enterprise
   orgs. The dequeue query already sorts by `priority DESC` — this is a config
   change, not an architecture change.

2. **Per-org concurrency limits from the database.** Move `maxConcurrent` from a
   hardcoded default to a per-org setting in the `organizations` table. Different
   tiers get different limits:

   | Tier | Max Concurrent Runs | Queue Priority |
   |------|-------------------|----------------|
   | Free | 1 | 0 (lowest) |
   | Team | 5 | 50 |
   | Enterprise | 20+ | 100 (highest) |

3. **Dedicated worker pools (future).** Large enterprise customers could get
   dedicated worker nodes tagged with their org ID. The job dequeue query filters
   by node labels so their jobs only run on their dedicated workers. This provides
   hard resource isolation without a separate deployment.

### Sandbox Network Isolation

**The problem:** All sandboxes on a worker node share the `143-sandbox` Docker
bridge network. One sandbox can potentially reach another sandbox's network
interfaces on the same node.

**The fix:** Create an isolated Docker network per sandbox instead of sharing one.

```go
// In DockerProvider.Create():
// 1. Create a unique network for this sandbox
networkName := fmt.Sprintf("143-sandbox-%s", sandboxID)
docker.NetworkCreate(ctx, networkName, types.NetworkCreate{
    Driver: "bridge",
    Labels: map[string]string{"143.sandbox_id": sandboxID},
})

// 2. Connect the sandbox to its own network
// 3. On cleanup, remove the network
```

This is a small code change to `internal/services/agent/providers/docker.go`.
Each sandbox gets its own network namespace — no cross-sandbox communication is
possible. The only external connectivity is through the network's gateway (which
the existing network policy restricts to LLM APIs and package registries).

**Cost:** One additional Docker network per active sandbox. Docker supports
thousands of networks per host. Negligible overhead.

### Agent Image Optimization

**The problem:** The `Dockerfile.agent` installs Ubuntu + Node + Python + Go +
Make + agent CLIs. This will produce a ~1-2 GB image. Consequences:

- Auto-scale cold start: image pull adds 1-3 minutes on top of VM provisioning
- Disk fills up faster on workers (each image version persists)
- CI builds take longer

**Options (in order of preference):**

1. **Minimal base + on-demand runtimes.** Ship a slim image (~200-300 MB) with
   only git, shell utilities, and the agent CLIs. When an agent needs Node or
   Python, it installs them from a pre-populated Docker volume cache on the
   worker node. Pro: fast pulls, flexible. Con: first-run penalty per language.

2. **Language-specific images.** Build `143-agent-node`, `143-agent-python`,
   `143-agent-go`. The orchestrator selects the image based on the repo's primary
   language (detectable from the repo's `languages` field in GitHub API). Pro:
   each image is smaller and specialized. Con: more images to build and manage.

3. **Single fat image (current plan).** Simplest to start with. Acceptable for
   Phase 1-2. Revisit when image size becomes a bottleneck for auto-scaling cold
   starts.

**Recommendation:** Start with the single fat image (option 3). It's the simplest
path and image pull time is irrelevant when you have a fixed fleet (Phase 1-3).
Switch to option 1 or 2 when auto-scaling (Phase 4) makes cold start time matter.

### Multi-Region

**The problem:** Everything is single-region. If that region goes down, the
entire platform is down. Enterprise customers with data residency requirements
(GDPR, etc.) need their data to stay in specific geographies.

**Why this is tractable later:** The architecture already separates state
(Postgres) from compute (stateless API/workers). This means:

- **Regional worker pools:** Workers in EU connect to the same Postgres (or a
  regional replica) and process jobs for EU orgs. Workers in US process US jobs.
  The job queue already has `org_id` — add a `region` column to orgs and filter
  the dequeue query by region.

- **Read replicas in each region:** Postgres logical replication to regional
  replicas for read-heavy queries (dashboard, audit log). Writes go to the
  primary. This uses the read/write splitting from Phase 3c.

- **Full multi-primary (future):** Postgres Citus or CockroachDB for global
  writes. This is a major migration but the app only sees `DATABASE_URL`, so the
  application code doesn't change.

**What to do now:** Nothing — but when choosing a cloud provider, prefer one with
multiple regions. And keep the architecture stateless except for Postgres.

### Observability and SLAs

**The problem:** The doc mentions Datadog and Mezmo in passing but doesn't define
what to measure or what SLAs to offer. You can't have SLAs without metrics.

**SLIs to instrument (in priority order):**

| SLI | What to Measure | Target |
|-----|----------------|--------|
| API availability | % of `/healthz` checks returning 200 | 99.9% |
| Agent run success rate | % of runs completing without error | > 95% |
| Agent run latency | p50 / p95 / p99 time from job enqueue to completion | p95 < 10 min |
| Webhook processing time | Time from webhook receipt to job enqueue | p99 < 5s |
| SSE stream reliability | % of log streams that don't disconnect unexpectedly | > 99% |
| Deploy success rate | % of deploys that complete without rollback | > 99% |

**Distributed tracing.** When a request flows from webhook → API → job queue →
worker → sandbox → result, you need a trace ID that links all of these. Use
OpenTelemetry — it's vendor-neutral (works with Datadog, Jaeger, Grafana Tempo)
and the Go SDK is mature. Add a `trace_id` to the `jobs` table so you can
correlate a job back to the webhook that triggered it.

**Alerting.** Define on-call escalation for:
- Postgres down for > 30 seconds
- API error rate > 5% for > 2 minutes
- Job queue depth > 100 for > 10 minutes (workers not keeping up)
- Backup freshness > 12 hours
- Disk usage > 85%

### Compliance and Audit

**Enterprise customers will ask about:**

| Concern | Current State | Path Forward |
|---------|--------------|-------------|
| SOC2 Type II | Not started | Audit log table exists; need access controls, change management |
| Data encryption at rest | Postgres data checksums only | Enable Postgres TDE or use encrypted volumes (LUKS, cloud-managed) |
| Data encryption in transit | Caddy TLS for external; Postgres SSL for DB connections | Already configured — see `pg_hba.conf` and SSL settings above |
| Secret rotation | SOPS + age, manual rotation | Add rotation scripts, integrate with cloud secret managers |
| Access control audit trail | `audit_log` table exists | Ensure all admin actions are logged, add `actor_id` to sensitive queries |
| Data residency | Single region | Multi-region (see above) |
| Right to deletion (GDPR) | No tooling | Add org data export + hard delete scripts |

The audit log table already has an immutability trigger (no UPDATE/DELETE). This
is a strong foundation — the main gap is instrumenting all access paths, not the
storage layer.

### PgBouncer and LISTEN/NOTIFY Conflict

**The problem:** The "Known Scaling Constraints" section proposes Postgres
LISTEN/NOTIFY as the future fix for both SSE polling (constraint #3) and job
queue contention (constraint #4). But PgBouncer in `transaction` mode (required
for SKIP LOCKED) **does not support LISTEN/NOTIFY** — notifications are tied to
sessions, and transaction mode releases the backend connection after each
transaction.

**The fix:** Maintain a small number of **direct Postgres connections** (bypassing
PgBouncer) specifically for LISTEN/NOTIFY channels. The pattern:

```go
// One persistent connection per API/worker node for pub/sub.
// This connection does NOT go through PgBouncer.
directConn, _ := pgx.Connect(ctx, directPostgresURL)  // port 5432, not 6432
directConn.Exec(ctx, "LISTEN job_ready")
directConn.Exec(ctx, "LISTEN session_log")

// All transactional queries still go through PgBouncer.
pool, _ := pgxpool.New(ctx, pgbouncerURL)  // port 6432
```

This uses 1 Postgres connection per node for pub/sub (N connections total) while
all regular queries still go through PgBouncer. At 20 nodes, that's 20 direct
connections — well within `max_connections=100`.

### Frontend Containerization

**The problem:** `docker-compose.prod.yml` references
`ghcr.io/assembledhq/143-frontend:latest` but no `Dockerfile.frontend` exists.
The CI workflow has a `# TODO` comment. This must be resolved before Phase 1 can
work.

**The fix:** Create `Dockerfile.frontend`:

```dockerfile
FROM node:22-alpine AS builder
WORKDIR /app
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci
COPY frontend/ .
RUN npm run build

FROM node:22-alpine
WORKDIR /app
COPY --from=builder /app/.next/standalone ./
COPY --from=builder /app/.next/static ./.next/static
COPY --from=builder /app/public ./public

ENV NODE_ENV=production
ENV PORT=3000
EXPOSE 3000

CMD ["node", "server.js"]
```

This uses Next.js standalone output mode (minimal Node.js server, no dev
dependencies). The resulting image is ~150 MB. Add the build step to the CI
workflow alongside the server and agent images.

---

## Self-Hosted PostgreSQL Operations

This section covers everything needed to run Postgres in Docker on a VPS at
production quality. Postgres is the only stateful component — getting this right
is critical.

### Production Configuration (`deploy/postgres/postgresql.conf`)

Tuned for a write-heavy SaaS workload (job queues, append-only logs, agent runs)
on VPS with NVMe SSDs. Scale the memory settings with VPS RAM.

```ini
# deploy/postgres/postgresql.conf

# ── Connections ──────────────────────────────────────────────────────
max_connections = 100
listen_addresses = '*'

# ── Memory ───────────────────────────────────────────────────────────
# Scale these with VPS RAM (see table below).
shared_buffers = 4GB                # 25% of RAM
effective_cache_size = 12GB         # 75% of RAM — tells planner about OS cache
work_mem = 16MB                     # per-sort/hash; conservative for OLTP
maintenance_work_mem = 1GB          # speeds up VACUUM and CREATE INDEX
huge_pages = try                    # use if kernel supports it (see host setup below)

# ── SSD-Specific Planner Settings ────────────────────────────────────
random_page_cost = 1.1              # default 4.0; critical for SSD (sequential ≈ random)
seq_page_cost = 1.0
effective_io_concurrency = 200      # NVMe can handle 200+ concurrent reads
maintenance_io_concurrency = 200    # for VACUUM I/O on SSDs

# ── WAL (Write-Ahead Log) ───────────────────────────────────────────
wal_level = replica                 # required for replication + WAL-G
wal_buffers = 64MB                  # default 16MB; increase for write-heavy
wal_compression = on                # reduces I/O at slight CPU cost (good tradeoff)
min_wal_size = 2GB                  # preallocate WAL to reduce I/O spikes
max_wal_size = 8GB                  # allow longer checkpoint intervals
max_wal_senders = 5                 # for replication + WAL-G
max_replication_slots = 5           # prevent WAL removal before replica catches up
wal_keep_size = 2GB                 # safety net for replication
archive_mode = on                   # required for WAL-G
archive_command = 'wal-g wal-push %p'
archive_timeout = 60                # force archive every 60s

# VM-specific optimizations (faster WAL handling on virtualized storage)
wal_recycle = off
wal_init_zero = off

# ── Checkpoints ──────────────────────────────────────────────────────
checkpoint_timeout = 15min          # default 5min; reduces checkpoint frequency
checkpoint_completion_target = 0.9  # spread checkpoint I/O evenly

# ── Parallelism ──────────────────────────────────────────────────────
max_worker_processes = 8
max_parallel_workers = 6
max_parallel_workers_per_gather = 4
max_parallel_maintenance_workers = 4

# ── Autovacuum (tuned for write-heavy workloads) ─────────────────────
autovacuum = on
autovacuum_max_workers = 4          # default 3; increase for many tables
autovacuum_naptime = 15s            # default 60s; check more frequently
autovacuum_vacuum_cost_limit = 1000 # default 200; let vacuum work harder on SSDs
autovacuum_vacuum_cost_delay = 2ms  # fine for SSDs
autovacuum_vacuum_insert_threshold = 1000
autovacuum_vacuum_insert_scale_factor = 0.1  # default 0.2; vacuum sooner on insert-only tables

# ── SSL/TLS ──────────────────────────────────────────────────────────
ssl = on
ssl_cert_file = '/var/lib/postgresql/certs/server.crt'
ssl_key_file = '/var/lib/postgresql/certs/server.key'
ssl_ca_file = '/var/lib/postgresql/certs/ca.crt'
ssl_min_protocol_version = 'TLSv1.2'

# ── Logging ──────────────────────────────────────────────────────────
log_min_duration_statement = 500    # log queries > 500ms
log_checkpoints = on
log_connections = on
log_disconnections = on
log_lock_waits = on
log_replication_commands = on

# ── Data integrity ───────────────────────────────────────────────────
fsync = on                          # NEVER turn this off in production
full_page_writes = on               # protects against partial page writes on crash
# Data checksums enabled at initdb time (--data-checksums)
```

**Scale with VPS RAM:**

| VPS RAM | `shared_buffers` | `effective_cache_size` | `shm_size` (docker-compose) |
|---------|------------------|----------------------|-----------------------------|
| 4 GB | 1 GB | 3 GB | 2g |
| 8 GB | 2 GB | 6 GB | 3g |
| 16 GB | 4 GB | 12 GB | 4g (or `5g` for headroom) |
| 32 GB | 8 GB | 24 GB | 10g |

### Connection Security (`deploy/postgres/pg_hba.conf`)

Use TLS even on private networks (defense in depth + compliance).

```
# TYPE    DATABASE        USER            ADDRESS           METHOD

# Local connections (inside container) — no SSL needed
local     all             all                               scram-sha-256

# Same-host Docker network — require SSL
hostssl   all             onefortythree   172.18.0.0/16     scram-sha-256

# Cross-VPS private network (Phase 3+) — require SSL
hostssl   all             onefortythree   10.0.0.0/24       scram-sha-256

# Replication connections — require SSL
hostssl   replication     replicator      10.0.0.0/24       scram-sha-256

# Deny everything else
host      all             all             0.0.0.0/0         reject
```

**Generate TLS certificates** (self-signed is fine for inter-VPS communication):

```bash
# Generate CA
openssl req -new -x509 -days 3650 -nodes -out ca.crt -keyout ca.key -subj "/CN=143-pg-ca"

# Generate server cert
openssl req -new -nodes -out server.csr -keyout server.key -subj "/CN=postgres"
openssl x509 -req -in server.csr -CA ca.crt -CAkey ca.key -CAcreateserial -out server.crt -days 365

# Fix permissions (Postgres requires strict ownership)
chmod 600 server.key
chown 999:999 server.key server.crt   # UID 999 = postgres user in Docker image

# Place in deploy/postgres/certs/
mkdir -p deploy/postgres/certs
mv ca.crt ca.key server.crt server.key deploy/postgres/certs/
```

The app connection string becomes:
```
postgres://onefortythree:xxx@postgres:5432/onefortythree?sslmode=verify-ca&sslrootcert=/path/to/ca.crt
```

### Per-Table Vacuum Overrides

High-write tables need more aggressive vacuum settings than the globals above.
Apply these after migrations create the tables:

```sql
-- Job queue: constant INSERT/UPDATE/DELETE cycling
ALTER TABLE jobs SET (
  autovacuum_vacuum_scale_factor = 0.01,       -- vacuum at 1% dead rows (not 20%)
  autovacuum_vacuum_threshold = 100,
  autovacuum_analyze_scale_factor = 0.02,
  autovacuum_vacuum_cost_limit = 2000
);

-- Session logs: append-only, grows fast
ALTER TABLE session_logs SET (
  autovacuum_vacuum_insert_scale_factor = 0.05,
  autovacuum_freeze_max_age = 200000000
);

-- Audit log: append-only, immutable
ALTER TABLE audit_log SET (
  autovacuum_vacuum_insert_scale_factor = 0.05,
  autovacuum_freeze_max_age = 200000000
);
```

### Host Kernel Tuning

Apply on the VPS host (add to `/etc/sysctl.conf`):

```bash
# Prevent OOM killer from targeting Postgres
vm.overcommit_memory = 2
vm.overcommit_ratio = 80

# Prefer dropping cache over swapping
vm.swappiness = 1

# Huge pages (optional but recommended for shared_buffers > 2GB)
# Calculate: shared_buffers / 2MB + 10% overhead
# For 4GB shared_buffers: 4096/2 * 1.1 = 2250
vm.nr_hugepages = 2250
```

Then set `huge_pages = on` (not `try`) in postgresql.conf and restart Postgres.

### Streaming Replication (Phase 3+)

When you separate Postgres to its own VPS (Step 3a), set up a read replica.

**On the primary — create replication user and slot:**

```sql
CREATE ROLE replicator WITH REPLICATION LOGIN PASSWORD 'secure_replication_password';
SELECT pg_create_physical_replication_slot('replica1_slot');
```

**On the replica VPS — take base backup and start:**

```bash
# Clear data directory and take base backup from primary
docker run --rm \
  -v pgdata:/var/lib/postgresql/data \
  postgres:18.0 \
  pg_basebackup \
    -h 10.0.0.1 \
    -U replicator \
    -D /var/lib/postgresql/data \
    -Fp -Xs -P -R \
    -S replica1_slot

# -R auto-creates standby.signal and writes connection info
```

**Replica `postgresql.conf` additions** (append to the standard config):

```ini
hot_standby = on
hot_standby_feedback = on
max_standby_streaming_delay = 30s
primary_conninfo = 'host=10.0.0.1 port=5432 user=replicator password=xxx sslmode=verify-ca'
primary_slot_name = 'replica1_slot'
```

**Failover runbook** (manual — add Patroni for automatic failover when you need
sub-minute recovery):

```bash
# 1. Promote replica to primary
docker exec 143-postgres-replica-1 pg_ctl promote -D /var/lib/postgresql/data

# 2. Update DATABASE_URL on all app/worker nodes to point to new primary

# 3. Rebuild old primary as a replica (pg_basebackup from new primary)
```

### PgBouncer Configuration

When running PgBouncer (Phase 3c), use this config:

```ini
# pgbouncer.ini
[databases]
onefortythree = host=postgres port=5432 dbname=onefortythree

[pgbouncer]
pool_mode = transaction
max_client_conn = 500
default_pool_size = 30
reserve_pool_size = 5
max_prepared_statements = 100   # enables prepared statements in transaction mode (PgBouncer 1.21+)
server_reset_query = DISCARD ALL
server_check_query = SELECT 1
server_check_delay = 30
```

**What breaks in transaction mode** (and workarounds):

| Feature | Status | Workaround |
|---------|--------|------------|
| Prepared statements (protocol-level) | Works (PgBouncer 1.21+) | Set `max_prepared_statements = 100` |
| `LISTEN/NOTIFY` | Broken | Use direct Postgres connection (see Enterprise section) |
| Session-level advisory locks | Broken | Use `pg_advisory_xact_lock()` (transaction-scoped) |
| Temporary tables | Broken | Use `ON COMMIT DROP` within same transaction |
| `SET` commands | Lost after transaction | Use `SET LOCAL` within transaction |

### Backup Verification

Beyond the `pg_restore --list` check in the backup script, run automated restore
tests weekly:

```bash
#!/usr/bin/env bash
# deploy/scripts/restore-test.sh
set -euo pipefail

BACKUP=$(ls -t /backups/postgres/*.dump | head -1)
TEST_CONTAINER="143-restore-test-$(date +%s)"

# Start a temporary Postgres for the test
docker run -d --name "$TEST_CONTAINER" \
  -e POSTGRES_USER=onefortythree \
  -e POSTGRES_PASSWORD=test \
  -e POSTGRES_DB=onefortythree \
  postgres:18.0

sleep 5  # wait for startup

# Restore
docker exec -i "$TEST_CONTAINER" \
  pg_restore -U onefortythree -d onefortythree --clean --if-exists < "$BACKUP"

# Verify critical tables have data
for TABLE in organizations users projects sessions jobs; do
  COUNT=$(docker exec "$TEST_CONTAINER" \
    psql -U onefortythree -tAc "SELECT count(*) FROM $TABLE" 2>/dev/null)
  if [ -z "$COUNT" ] || [ "$COUNT" -eq 0 ]; then
    echo "FAIL: $TABLE is empty after restore"
    docker rm -f "$TEST_CONTAINER"
    exit 1
  fi
  echo "OK: $TABLE has $COUNT rows"
done

echo "Restore test PASSED"
docker rm -f "$TEST_CONTAINER"
```

**Schedule:** Weekly via cron. Alert if it fails.

```cron
0 4 * * 0 /opt/143/deploy/scripts/restore-test.sh >> /var/log/restore-test.log 2>&1
```

### Upgrade Strategy (Postgres 18 → 19+)

Use `pg_upgrade --link` for near-zero-downtime upgrades. The `--link` flag
creates hard links instead of copying data files — the upgrade itself takes
seconds regardless of database size.

```bash
# 1. Stop the application (keep Postgres running)
docker compose stop api frontend

# 2. Stop Postgres
docker compose stop postgres

# 3. Run pg_upgrade with both versions
docker run --rm \
  -v pgdata:/var/lib/postgresql \
  pgautoupgrade/pgautoupgrade:18-to-19 \
  pg_upgrade \
    --old-datadir /var/lib/postgresql/18/data \
    --new-datadir /var/lib/postgresql/19/data \
    --old-bindir /usr/lib/postgresql/18/bin \
    --new-bindir /usr/lib/postgresql/19/bin \
    --link

# 4. Update docker-compose to postgres:19.x, then start everything
docker compose up -d

# 5. CRITICAL: rebuild planner statistics (pg_upgrade doesn't transfer them)
docker exec 143-postgres-1 vacuumdb --all --analyze-in-stages -U onefortythree
```

Total downtime: the time to stop containers + run pg_upgrade (seconds) + restart.
Typically under 5 minutes regardless of database size.

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
| Large | 500 GB+ | Dedicated high-memory VPS + Patroni HA | PgBouncer, table partitioning for `agent_run_logs` and `audit_log` |

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
| Database | Postgres 18 in Docker (self-hosted) | Yes | Same Docker image works on any VPS provider |
| Backup storage | S3-compatible via rclone | Yes | AWS S3, GCS, Hetzner Object Storage, MinIO |
| WAL archiving | WAL-G | Yes | Supports S3, GCS, Azure Blob, local filesystem |
| Node provisioning | cloud-init | Yes | Supported by every major cloud provider |
| CI/CD | GitHub Actions + SSH | Yes | Just needs SSH access to the target VPS |
| Private networking | Provider VPC/vNetwork | **Provider-specific** | Concept is universal; API/config differs |
| Auto-scaling | `CloudProvider` interface | **Provider-specific impl** | Interface is ours; implementations wrap vendor SDKs |
| Load balancer (Phase 3c) | Provider managed LB | **Provider-specific** | Every major provider offers one; config differs but concept is identical |

**What you'd need to change to switch providers:**
1. Provision new VPSes on the new provider (same specs)
2. Set up a private network (different API, same concept)
3. Update `DEPLOY_HOST` in GitHub Actions secrets
4. Update `rclone` config if backup storage endpoint changes
5. (Phase 4 only) Implement the `CloudProvider` interface for the new provider

Application code, Docker Compose files, Caddy config, Postgres config, backup
scripts, and CI/CD workflows all stay identical.

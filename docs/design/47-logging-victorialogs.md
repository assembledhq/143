# Design: Centralized Logging with VictoriaLogs

> **Status:** Proposed | **Last reviewed:** 2026-04-14

Self-hosted VictoriaLogs + Grafana stack on a dedicated Hetzner logging server, with Vector collectors on the app and worker servers.

## Motivation

- **No existing logging**: We currently have no centralized log collection. Debugging requires SSH-ing into servers and tailing Docker logs manually.
- **Cost**: VictoriaLogs is free and open-source. A dedicated Hetzner CX22 costs ~€4/month.
- **Performance**: VictoriaLogs uses [significantly less memory, CPU, and query latency than Loki](https://docs.victoriametrics.com/victorialogs/benchmarks/). Single binary, zero config.
- **Full-text search**: Unlike Loki (label-only indexing), VictoriaLogs indexes per-token, enabling fast full-text search and high-cardinality fields (user_id, trace_id, org_id).
- **Control**: Logs stay on our infrastructure. No vendor dependency for a critical debugging tool.

## Architecture

```
┌──────────────────────┐     ┌──────────────────────┐
│     App Server        │     │    Worker Server      │
│     (Hetzner)         │     │    (Hetzner)          │
│                       │     │                       │
│  ┌─────────────────┐  │     │  ┌─────────────────┐  │
│  │ api             │  │     │  │ worker           │  │
│  │ frontend        │  │     │  │ sandbox          │  │
│  │ caddy           │  │     │  │ containers       │  │
│  └────────┬────────┘  │     │  └────────┬────────┘  │
│           │ stdout     │     │           │ stdout     │
│  ┌────────▼────────┐  │     │  ┌────────▼────────┐  │
│  │ Vector          │  │     │  │ Vector          │  │
│  │ (log collector) │  │     │  │ (log collector) │  │
│  └────────┬────────┘  │     │  └────────┬────────┘  │
└───────────┼────────────┘     └───────────┼───────────┘
            │                              │
            │   Hetzner Private Network    │
            └──────────────┬───────────────┘
                          │
              ┌───────────▼───────────┐
              │   Logging Server       │
              │   (Hetzner CX22)       │
              │   4 GB RAM / €4/mo     │
              │                        │
              │  ┌──────────────────┐  │
              │  │ VictoriaLogs     │  │
              │  │ :9428            │  │
              │  │ ~512 MB RAM      │  │
              │  └──────────────────┘  │
              │                        │
              │  ┌──────────────────┐  │
              │  │ Grafana          │  │
              │  │ :3000            │  │
              │  │ ~300 MB RAM      │  │
              │  └──────────────────┘  │
              └────────────────────────┘
```

## Logging Server Setup

### Server Specs

- **Hetzner CX22**: 2 vCPU, 4 GB RAM, 40 GB disk, ~€4/month
- VictoriaLogs (~512 MB) + Grafana (~300 MB) = ~1 GB RAM, leaving headroom
- Disk: 40 GB is sufficient for 30 days retention. With ~3 containers on app and ~2 on worker, estimated volume is ~0.5–1 GB/day (~15–30 GB over 30 days). If average daily volume exceeds ~1 GB/day (visible via the disk usage alert), upgrade to CX32 (80 GB disk, ~€8/month).

### Docker Compose (`docker-compose.logging.yml`)

```yaml
services:
  victorialogs:
    image: victoriametrics/victoria-logs:v1.50.0
    volumes:
      - vlogs-data:/victoria-logs-data
    command:
      - -storageDataPath=/victoria-logs-data
      - -retentionPeriod=30d  # VictoriaLogs automatically deletes data older than this; no manual cleanup needed
      - -httpListenAddr=:9428
    ports:
      - "${PRIVATE_IP:-10.0.0.3}:9428:9428"  # bind to private network IP only
    restart: unless-stopped
    deploy:
      resources:
        limits:
          memory: 1G
          cpus: "1.0"

  grafana:
    image: grafana/grafana:13.0.0
    environment:
      GF_INSTALL_PLUGINS: victoriametrics-logs-datasource
      GF_SECURITY_ADMIN_PASSWORD: ${GRAFANA_ADMIN_PASSWORD}  # injected via `sops exec-env secrets.enc.env -- docker compose up`
      GF_SERVER_ROOT_URL: ${GRAFANA_ROOT_URL:-http://localhost:3000}  # set to https://logs.143.dev if exposing via Caddy
    volumes:
      - grafana-data:/var/lib/grafana
      - ./deploy/grafana/provisioning:/etc/grafana/provisioning:ro
    ports:
      - "127.0.0.1:3000:3000"  # localhost only — access via SSH tunnel or Caddy
    depends_on:
      - victorialogs
    restart: unless-stopped
    deploy:
      resources:
        limits:
          memory: 512M
          cpus: "1.0"

volumes:
  vlogs-data:
  grafana-data:
```

### Grafana Datasource Provisioning

```yaml
# deploy/grafana/provisioning/datasources/victorialogs.yml
apiVersion: 1
datasources:
  - name: VictoriaLogs
    type: victoriametrics-logs-datasource
    access: proxy
    url: http://victorialogs:9428
    isDefault: true
```

## Vector Collector Setup

Vector runs on each app/worker server, collects Docker container logs, and ships them to VictoriaLogs over the Hetzner private network.

### Vector Config (`deploy/vector.yaml`)

```yaml
sources:
  docker_logs:
    type: docker_logs
    # Automatically collects logs from all Docker containers

transforms:
  enrich:
    type: remap
    inputs: ["docker_logs"]
    source: |
      # Extract Docker Compose service name
      .service = .label."com.docker.compose.service" ?? "unknown"

      # Parse structured JSON logs from our Go services (zerolog output).
      # Intentionally merge parsed fields into the root — this overwrites Vector's
      # built-in .timestamp and .message with the zerolog values, which is what the
      # VictoriaLogs sink expects (_time_field: "timestamp", _msg_field: "message").
      parsed, err = parse_json(.message)
      if err == null {
        . = merge(., parsed)
      }

      # Add server identity
      .server = get_env_var("SERVER_ROLE") ?? "unknown"
      .hostname = get_hostname()

  # Drop health check requests after JSON parsing so we can match on structured fields.
  # Filtering on .path avoids accidentally dropping app logs that mention health endpoints.
  filter_noise:
    type: filter
    inputs: ["enrich"]
    condition:
      type: vrl
      source: |
        path = string(.path) ?? ""
        path != "/healthz" && path != "/readyz"

sinks:
  victorialogs:
    type: http
    inputs: ["filter_noise"]
    uri: "http://${VICTORIALOGS_HOST:-10.0.0.3}:9428/insert/jsonline"
    encoding:
      codec: json
    query_string_parameters:
      _stream_fields: "service,server,hostname"
      _msg_field: "message"
      _time_field: "timestamp"
    healthcheck:
      enabled: false
    buffer:
      type: disk
      max_size: 268435456  # 256 MB disk buffer — survives logging server restarts
      when_full: drop_newest  # prefer dropping logs over back-pressuring app containers
                              # Note: dropped events are silently lost. Vector emits internal
                              # metrics (component_discarded_events_total) that can be monitored.
```

### Add Vector to App Server (`docker-compose.app.yml`)

Add the `vector` service and `vector-buffer` volume to the existing compose file:

```yaml
  vector:
    image: timberio/vector:0.54.0-alpine
    environment:
      SERVER_ROLE: app
      VICTORIALOGS_HOST: ${VICTORIALOGS_HOST:-10.0.0.3}
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
      - ./deploy/vector.yaml:/etc/vector/vector.yaml:ro
      - vector-buffer:/var/lib/vector  # persist disk buffer across restarts
    command: ["vector", "--config", "/etc/vector/vector.yaml", "--api.enabled=true"]
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://localhost:8686/health"]
      interval: 30s
      timeout: 5s
      retries: 3
    restart: unless-stopped
    deploy:
      resources:
        limits:
          memory: 128M
          cpus: "0.5"

volumes:
  vector-buffer:
```

### Add Vector to Worker Server (`docker-compose.worker.yml`)

Same as app, with `SERVER_ROLE: worker`. To avoid drift between the two nearly-identical Vector blocks, consider extracting to a shared `docker-compose.vector.yml` and using `include` in each server's compose file. For now, copy the block above and change `SERVER_ROLE`.

## Network Security

VictoriaLogs must not be exposed to the public internet.

### Option A: Hetzner Private Network (Recommended)

1. Create a Hetzner Cloud Network (e.g., `10.0.0.0/24`)
2. Attach all three servers (app, worker, logging)
3. Bind VictoriaLogs to the private IP only (shown in compose above)
4. Vector on app/worker connects via private IP
5. No firewall rules needed — traffic stays on the private network

> **Note:** Hetzner private networks are not encrypted at the network layer. Plain HTTP between Vector and VictoriaLogs is acceptable because our logs do not contain secrets or PII (auth tokens are never logged, and user-facing content stays in the database). If this changes, add mTLS between Vector and VictoriaLogs.

### Option B: WireGuard (If Using Dedicated Servers)

If using Hetzner dedicated servers (not cloud), set up a WireGuard mesh:

```
app-server    (10.10.0.1) ──┐
worker-server (10.10.0.2) ──┼── WireGuard mesh ── logging-server (10.10.0.3)
```

### Grafana Access

Expose Grafana via Caddy on the logging server with basic auth or through an SSH tunnel:

```bash
# SSH tunnel (simplest, no public exposure)
ssh -L 3000:localhost:3000 logging-server

# Or via Caddy with basic auth
logs.143.dev {
    basicauth {
        admin $2a$14$...  # bcrypt hash
    }
    reverse_proxy grafana:3000
}
```

## Rollout Plan

### Step 0: Switch Zerolog to JSON Output in Production

The current `NewLogger` in `internal/logging/logger.go` uses `zerolog.ConsoleWriter` (human-readable text). The Vector `remap` transform uses `parse_json()` to extract structured fields from log lines, so it needs JSON input. Update `NewLogger` to use zerolog's default JSON encoder when `ENV=production` (or equivalent), e.g. `zerolog.New(os.Stdout)` instead of `zerolog.New(zerolog.ConsoleWriter{...})`.

Vector will still collect and ship logs without this change, but all fields will land in a single `message` string — no structured field extraction (level, service, org_id, etc.) until the switch is made.

### Step 1: Provision Logging Server

The logging server follows the same provisioning pattern as app, worker, and db nodes. This requires changes to:

- **Makefile**: Add `provision-logging` and `deploy-logging` targets
- **`deploy/scripts/provision.sh`**: Add `logging` role — lightweight bootstrap (Docker only, no gVisor, no kernel tuning), `GRAFANA_ADMIN_PASSWORD` as the only secret (no DB credentials or age key)
- **`deploy/scripts/deploy.sh`**: Add `logging` role with `grafana` as the health service
- **`deploy/scripts/bootstrap.sh`**: Accept `logging` as a valid role
- **`deploy/cloud-init/logging.yml`**: Cloud-init template for automated provisioning
- **`deploy/fleet-hosts.txt.example`**: Add `logging` entry

Key differences from other roles:
- **No gVisor** — logging server doesn't run sandboxes
- **No DB credentials** — logging server doesn't connect to Postgres
- **No GHCR login needed** — VictoriaLogs and Grafana are public Docker Hub images
- **Minimal secrets** — only `GRAFANA_ADMIN_PASSWORD`

Provisioning workflow:

1. Provision Hetzner CX22
2. Set up Hetzner Cloud Network between all servers (app, worker, db, logging)
3. Run `make provision-logging HOST=<ip> SSH_KEY=~/.ssh/143-deploy`
4. Verify Grafana is accessible and VictoriaLogs is reachable from the private network

### Step 2: Deploy Vector Collectors

1. Add Vector service to `docker-compose.app.yml` on the app server
2. Add Vector service to `docker-compose.worker.yml` on the worker server
3. Deploy both — Vector starts collecting Docker logs immediately
4. Verify logs appear in Grafana

### Step 3: Set Up Dashboards and Alerts

1. Create Grafana dashboards for key views (errors by service, agent run logs, request logs)
2. Configure Grafana alerts (error rate spikes, agent run failures, sandbox OOM)
3. Update `09-observability.md` and `10-infrastructure.md` to reference VictoriaLogs

## LogsQL Query Examples

These examples use raw LogsQL syntax. The Grafana VictoriaLogs datasource plugin wraps LogsQL — query syntax in the Grafana Explore UI may differ slightly (e.g., time range is controlled by the Grafana time picker rather than `_time` filters).

```
# All logs from the worker server
server:worker

# Errors from the API service
service:api AND level:error

# Agent run logs for a specific run
agent_run_id:"run-abc123"

# Logs from a specific org in the last hour
org_id:"org-456" AND _time:[now-1h,now]

# Full-text search across all logs
"timeout waiting for sandbox"

# High-cardinality field search (not possible with Loki)
trace_id:"tr-789" OR request_id:"req-012"

# Numeric range filter (e.g. response times over 1 second)
service:api AND response_time_ms:range(1000, Inf)
```

## Alerting

VictoriaLogs supports alerting via `vmalert` (VictoriaMetrics alerting engine) which evaluates LogsQL rules and fires to Alertmanager.

For now, Grafana alerting on log queries is sufficient:

- **Error rate spike**: Alert if `level:error` count exceeds threshold in 5-min window
- **Agent run failures**: Alert if `"agent run failed"` appears > 3 times in 15 min
- **Sandbox OOM**: Alert on `"out of memory"` in worker logs
- **Logging server disk usage**: Alert if disk usage on the logging server exceeds 80%. Use Vector's built-in `host_metrics` source with the `filesystem` collector on the logging server — this is cleaner than a cron job and avoids the problem of getting cron output into a Docker log stream. Add to `docker-compose.logging.yml`'s Vector config:
  ```yaml
  sources:
    host_metrics:
      type: host_metrics
      collectors: [filesystem]
      scrape_interval_secs: 300
  ```
  Grafana alert on the `filesystem_used_ratio` metric exceeding 0.8.

## Resource Budget

| Component | Server | RAM | CPU | Disk |
|-----------|--------|-----|-----|------|
| VictoriaLogs | logging | ~512 MB | 0.5 | 30 GB (logs) |
| Grafana | logging | ~300 MB | 0.3 | 1 GB (dashboards) |
| Vector | app | ~40 MB (limit: 128 MB) | 0.1 | 256 MB (buffer) |
| Vector | worker | ~40 MB (limit: 128 MB) | 0.1 | 256 MB (buffer) |
| **Total new** | | **~900 MB** | **~1.0** | **~31 GB** |

## Cost

| Item | Monthly Cost |
|------|-------------|
| Hetzner CX22 (logging server) | ~€4 |
| Hetzner Cloud Network | Free |
| VictoriaLogs + Grafana + Vector | Free (open source) |
| **Total** | **~€4/month** |

Replaces manual SSH + `docker logs` debugging.

## Future Considerations

- **Hetzner Object Storage**: If log volume grows, configure VictoriaLogs to offload older data to Hetzner Object Storage (~€0.005/GB/month) for cheap long-term retention
- **VictoriaMetrics**: If we move off Datadog for metrics, VictoriaMetrics (same team) is the natural companion — same operational model, same Grafana integration
- **Cluster mode**: If we add more servers and log volume exceeds single-node capacity, VictoriaLogs supports a cluster mode with separate insert/query/storage roles

---

## Appendix: Implementation Reference

Ready-to-use configs and script diffs for implementation. These follow the existing patterns in `deploy/scripts/` and `deploy/cloud-init/`.

### A.1: Makefile Additions

```makefile
#   make provision-logging HOST=10.0.0.5       SSH_KEY=~/.ssh/143-deploy

provision-logging:
	@test -n "$(HOST)" || { echo "HOST is required. Usage: make provision-logging HOST=<ip> SSH_KEY=<path>"; exit 1; }
	@test -n "$(SSH_KEY)" || { echo "SSH_KEY is required."; exit 1; }
	./deploy/scripts/provision.sh logging $(HOST) $(SSH_KEY) $(if $(REPROVISION),--reprovision)

#   make deploy-logging HOST=10.0.0.5       SSH_KEY=~/.ssh/143-deploy

deploy-logging:
	@test -n "$(HOST)" || { echo "HOST is required."; exit 1; }
	@test -n "$(SSH_KEY)" || { echo "SSH_KEY is required."; exit 1; }
	./deploy/scripts/deploy.sh logging $(HOST) $(SSH_KEY)
```

### A.2: provision.sh Changes

Add `logging` to the role validation:

```bash
case "$ROLE" in
  app)     COMPOSE_FILE="docker-compose.app.yml" ;;
  worker)  COMPOSE_FILE="docker-compose.worker.yml" ;;
  db)      COMPOSE_FILE="docker-compose.db.yml" ;;
  logging) COMPOSE_FILE="docker-compose.logging.yml" ;;
  *)       echo "Unknown role: $ROLE (expected: app, worker, db, logging)"; exit 1 ;;
esac
```

Update secret validation — logging doesn't need DB_PASSWORD or DB_HOST:

```bash
: "${GHCR_TOKEN:?GHCR_TOKEN is required (set it or add to .env.production.enc)}"
if [ "$ROLE" != "logging" ]; then
  : "${DB_PASSWORD:?DB_PASSWORD is required (set it or add to .env.production.enc)}"
fi
if [ "$ROLE" != "db" ] && [ "$ROLE" != "logging" ]; then
  : "${DB_HOST:?DB_HOST is required for $ROLE role (set it or add to .env.production.enc)}"
fi
if [ "$ROLE" = "logging" ]; then
  : "${GRAFANA_ADMIN_PASSWORD:?GRAFANA_ADMIN_PASSWORD is required for logging role (set it or add to .env.production.enc)}"
fi
```

Add bootstrap block (after the `db` block, before `else`):

```bash
elif [ "$ROLE" = "logging" ]; then
  # Logging nodes just need Docker — no gVisor, no special kernel tuning
  ssh "${SSH_OPTS[@]}" root@"$HOST" << 'BOOTSTRAP_LOGGING'
    set -euo pipefail
    id deploy &>/dev/null || adduser --disabled-password --gecos "" deploy
    mkdir -p /home/deploy/.ssh /opt/143
    [ -f /root/.ssh/authorized_keys ] && cp /root/.ssh/authorized_keys /home/deploy/.ssh/
    chown -R deploy:deploy /home/deploy/.ssh /opt/143
    chmod 700 /home/deploy/.ssh
    command -v docker &>/dev/null || (curl -fsSL https://get.docker.com | sh)
    usermod -aG docker deploy
    echo "Bootstrap complete (logging)."
BOOTSTRAP_LOGGING
```

Add secrets block (before the `db` block):

```bash
if [ "$ROLE" = "logging" ]; then
  # Logging nodes only need the Grafana admin password — no DB or age key
  printf 'GRAFANA_ADMIN_PASSWORD=%s\n' "$GRAFANA_ADMIN_PASSWORD" \
    | ssh "${SSH_OPTS[@]}" root@"$HOST" 'cat > /opt/143/.env && chown deploy:deploy /opt/143/.env && chmod 600 /opt/143/.env'
elif [ "$ROLE" = "db" ]; then
```

Update image pull — logging uses public images:

```bash
  db|logging)
    # Public images (postgres, victorialogs, grafana) are pulled automatically by compose
    ;;
```

### A.3: deploy.sh Changes

Add `logging` to the role case:

```bash
  logging)
    COMPOSE_FILE="docker-compose.logging.yml"
    HEALTH_SERVICE="grafana"
    ;;
```

Add secrets block (before the `db` block):

```bash
  if [ "$ROLE" = "logging" ]; then
    printf 'GRAFANA_ADMIN_PASSWORD=%s\n' "${GRAFANA_ADMIN_PASSWORD:-}" \
      | ssh "${SSH_OPTS[@]}" deploy@"$HOST" 'cat > /opt/143/.env && chmod 600 /opt/143/.env'
  elif [ "$ROLE" = "db" ]; then
```

### A.4: Cloud-Init Template (`deploy/cloud-init/logging.yml`)

```yaml
#cloud-config
# Logging node bootstrap (VictoriaLogs + Grafana).
# Receives logs from Vector collectors on app/worker nodes via Hetzner private network.
#
# Provision from your local machine:
#   make provision-logging HOST=10.0.0.5 SSH_KEY=~/.ssh/143-deploy
#
# Or manually substitute and pass as user-data:
#   export SSH_PUBLIC_KEY="$(cat ~/.ssh/143-deploy.pub)"
#   export GHCR_TOKEN="ghp_xxxx"
#   export GRAFANA_ADMIN_PASSWORD="your-grafana-password"
#   export REPO_URL="https://github.com/assembledhq/143.git"
#   envsubst < deploy/cloud-init/logging.yml > /tmp/user-data.yml

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
  - git
  - jq

runcmd:
  # Set up GHCR access (token written to file to avoid leaking in cloud-init logs)
  - su - deploy -c 'cat /opt/143/.ghcr-token | docker login ghcr.io -u deploy --password-stdin && rm -f /opt/143/.ghcr-token'

  # Clone repo to get compose files and configs.
  - su - deploy -c 'git clone --depth 1 ${REPO_URL} /tmp/143-repo'
  - su - deploy -c 'cp /tmp/143-repo/docker-compose.logging.yml /opt/143/'
  - su - deploy -c 'cp -r /tmp/143-repo/deploy /opt/143/deploy'
  - rm -rf /tmp/143-repo

  # Start the stack and wait for Grafana health
  - su - deploy -c 'cd /opt/143 && docker compose -f docker-compose.logging.yml up -d --remove-orphans'
  - |
    echo "Waiting for Grafana health check..."
    HEALTHY=false
    for i in $(seq 1 30); do
      if wget -qO- http://localhost:3000/api/health > /dev/null 2>&1; then
        echo "Health check passed."
        HEALTHY=true
        break
      fi
      sleep 2
    done
    if [ "$HEALTHY" != "true" ]; then
      echo "ERROR: Grafana health check timed out after 60s."
      exit 1
    fi

write_files:
  - path: /opt/143/.env
    owner: deploy:deploy
    permissions: '0600'
    content: |
      GRAFANA_ADMIN_PASSWORD=${GRAFANA_ADMIN_PASSWORD}

  - path: /opt/143/.ghcr-token
    owner: deploy:deploy
    permissions: '0600'
    content: ${GHCR_TOKEN}
```

### A.5: Fleet Hosts Update (`deploy/fleet-hosts.txt.example`)

```
db      10.0.0.3
app     10.0.0.2
worker  10.0.0.4
logging 10.0.0.5
```

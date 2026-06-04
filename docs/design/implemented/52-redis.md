# Design: Redis as Optional Cache and Pub/Sub Layer

> **Status:** Implemented | **Last reviewed:** 2026-04-21

## 1. Motivation

143 currently uses PostgreSQL as the sole stateful backend for everything: job queue, session storage, auth tokens, rate limiting, real-time log streaming, and scheduler coordination. This works well at small scale but creates pressure points as node count and session volume grow:

| Problem | Current approach | Bottleneck |
|---------|-----------------|------------|
| Real-time log streaming | SSE handler polls Postgres every 1s per connected client | O(clients) queries/sec on session_messages |
| Rate limiting | In-memory token buckets per node | Not shared across nodes; each node allows full rate independently |
| Job notifications | Workers poll `jobs` table every 5s | Latency floor of 5s; wasted queries when queue is empty |
| Session status broadcasts | Each SSE client polls session row every 1s | Redundant reads for popular sessions |
| Auth session validation | `SELECT ... FROM auth_sessions WHERE token = $1` on every request | Hot path hits Postgres on every API call |

Redis addresses all of these with purpose-built primitives (pub/sub, sorted sets, expiring keys) while remaining operationally simple and horizontally familiar.

---

## 2. Proposed Uses

### 2.1 Redis Streams for Real-Time Events (highest value)

**Problem:** The SSE log-streaming endpoint (`GET /api/v1/sessions/{id}/logs`) polls Postgres every 1 second per connected client. With 50 concurrent viewers on 20 active sessions, that's up to 1,000 queries/sec of pure polling overhead (fewer if viewers cluster on popular sessions, but per-row contention is worse in that case).

**Solution:** Use Redis Streams per session (not Pub/Sub — Streams provide message persistence and replay from a given ID). A single **fan-out goroutine per node per session** reads the stream at the tail and broadcasts entries to local SSE clients via bounded Go channels.

**Connection and fan-out model:**

- When the agent orchestrator writes a log entry to Postgres, it also `XADD`s to `stream:session:{id}:logs`.
- One goroutine per node per active session calls `XREAD BLOCK` starting from `$` (tail). It does **not** try to serve catch-up reads — it only broadcasts newly arriving entries.
- The goroutine maintains a small in-memory **ring buffer** of the last ~1000 entries it has seen. When a new SSE client connects with a recent `last-seen-id`, the handler first replays from the ring buffer; beyond that, it performs a one-shot `XRANGE` catch-up read (bounded by MAXLEN); beyond that still, it falls back to a Postgres backfill. Once caught up, the client registers with the live fan-out.
- Each SSE client registers a **bounded channel** (buffer size ~256 entries) with the fan-out goroutine. When the fan-out tries to send and the channel is full, the client is considered slow: the fan-out closes the channel and removes the client. The SSE handler sees the close, sends an SSE `error: slow consumer` event, and disconnects. The client reconnects with its last-seen ID and goes through the catch-up path above.
- The fan-out goroutine never anchors its read position on any individual client. It always reads from tail. Slow clients cannot hold back the stream position or cause unbounded buffer growth in the shared goroutine — bounded per-client channels absorb the mismatch, and drop-on-full is the back-pressure signal.
- When no clients remain and the session is not producing entries, the fan-out goroutine exits. If new entries arrive after exit (before teardown), the next connecting client re-starts the goroutine and catches up via XRANGE.
- Status changes use the same pattern on `stream:session:{id}:status`.
- Streams are trimmed on every `XADD` with `MAXLEN ~ 10000` for logs. We intentionally choose a **count-based write-path trim only** because Redis `XADD` supports either `MAXLEN` or `MINID` trim, not both in one call. `MAXLEN` is the important invariant because it gives a hard memory ceiling. Individual log entries larger than **4KB are truncated** in the Redis copy (full payload stays in Postgres) — this bounds worst-case memory for pathological entries (huge tool outputs, long compiler errors). At a ~500B-after-truncation average, MAXLEN 10000 caps per-session memory at ~5MB.
- We do **not** guarantee that active but quiet sessions lose entries older than 60 minutes while still running. That is acceptable for v1 because Postgres remains the source of truth and `MAXLEN` is the stronger operational guardrail. After a session reaches a terminal state, `EXPIREAT` removes the whole stream 1 hour later. If active-session age-based trimming becomes necessary later, add a periodic `XTRIM MINID` maintenance pass rather than trying to overload the write path.

**Live failover model:** Existing SSE connections do not hot-swap from Redis fan-out into Postgres polling on the same socket. When the fan-out reader detects a Redis read error or subscription teardown, it closes all registered client channels with a retryable reason. Each SSE handler emits an `event: error` / `data: retry` frame if possible and closes the HTTP stream. The browser reconnects with its `Last-Event-ID` (or `?last_event_id=`), and the normal connect path chooses the best available catch-up source:
1. ring buffer replay if the watermark is still local,
2. `XRANGE` from Redis if Redis is reachable,
3. Postgres backfill if Redis is unavailable or the watermark is older than the stream window.

This keeps the server-side state machine simple: reconnect is the failover boundary, and the durable watermark is always the last event ID already delivered to the client.

**Why fan-out instead of connection-per-client:** A naive approach where each SSE client runs its own `XREAD BLOCK` would hold one Redis connection per client. With 50 clients across 10 nodes, that's 500 blocked connections just for log streaming. The fan-out pattern reduces this to one connection per node per active session — typically 10-20 connections total instead of hundreds.

**Why Streams over Pub/Sub:** Plain Pub/Sub is fire-and-forget — if a subscriber disconnects and reconnects, all messages during the gap are lost. For log streaming this means users see gaps in output. Streams solve this by persisting messages and supporting replay from an arbitrary offset, which maps directly to "give me logs since entry X."

**Dual-write consistency:** Log entries are written to Postgres first, then `XADD`'d to Redis. If the XADD fails, already-connected viewers may miss the entry until their next reconnect and catch-up pass reads from Postgres. This is acceptable — Postgres is the source of truth — but it means live viewers can briefly see "holes" in the stream. A log line that goes `"XADD failed: {stream}"` on every such failure makes this diagnosable.

**Fallback:** If Redis is unavailable, fall back to the current 1s Postgres polling. The SSE handler already works this way, so Redis becomes a performance optimization, not a hard dependency. See §8.1 for the fallback-load analysis.

### 2.2 Distributed Rate Limiting

**Problem:** The current token-bucket rate limiter (`internal/api/middleware/ratelimit.go`) uses per-node in-memory maps. With N nodes behind a load balancer, an org can make `N * 100` requests/sec instead of 100.

> **Status:** Gated. We should not enable distributed rate limiting on the same eviction domain as large session streams. If Redis is configured with `allkeys-lru` and the same instance holds both stream data and limiter keys, memory pressure can evict active limiter counters and silently relax limits for the busiest orgs. That is acceptable for an accelerator, but not for a control-plane safeguard.

**Enablement rule:** Phase 4 only turns on once rate-limit keys are isolated from stream eviction risk, typically via a **small dedicated Redis instance for control-plane keys**. Until then, keep the existing per-node in-memory limiter.

**Solution (when the gate is met):** Use a simple **fixed-window** counter with `INCR` + TTL. A single Redis key per org per window (`143:ratelimit:org:{id}:{window}`) ensures a global view. No Lua script needed — standard Redis commands are sufficient for our scale:

```go
// Fixed-window rate limiter using INCR + TTL.
// windowKey = fmt.Sprintf("143:ratelimit:org:%s:%d", orgID, time.Now().Unix()/windowSecs)
func (c *Client) CheckRateLimit(ctx context.Context, windowKey string, limit int64, windowSecs int) (allowed bool, err error) {
    count, err := c.rdb.Incr(ctx, windowKey).Result()
    if err != nil {
        return false, err // caller falls back to in-memory
    }
    if count == 1 {
        // First request in this window — set expiry so the key self-cleans.
        // If EXPIRE fails after INCR succeeds, log and count it: correctness
        // for this request is still preserved, but the key may outlive the
        // window. A lightweight periodic cleanup can scan for ttl-less
        // ratelimit keys if this ever shows up in metrics.
        if err := c.rdb.Expire(ctx, windowKey, time.Duration(windowSecs*2)*time.Second).Err(); err != nil {
            c.logger.Warn().Err(err).Str("key", windowKey).Msg("rate limit TTL set failed")
        }
    }
    return count <= limit, nil
}
```

**Why not a Lua script?** A GCRA (Generic Cell Rate Algorithm) Lua script would provide smoother rate limiting without fixed-window boundary bursts. But at 143's scale (tens of orgs, <1000 req/sec), the added complexity isn't justified. The simple `INCR`/`EXPIRE` approach is easy to understand, debug, and test. If boundary bursts become a problem later (an org gaming the window edge), GCRA can be swapped in without changing the middleware interface.

**Boundary burst mitigation:** The worst case is 2x burst at window edges (100 requests at second 59, 100 more at second 61). For API rate limiting this is acceptable — we're protecting against sustained abuse, not microsecond-precision fairness. If needed, a two-window interpolation (count from current + previous window weighted by elapsed fraction) eliminates most of this without introducing Lua.

**Failure semantics:** Redis is the authoritative limiter on every request when available. The per-node in-memory limiter is **only** used when Redis is unavailable (circuit breaker open or connection error). There is no "short-circuit-on-in-memory-first" path — that would allow a single node to silently accept above-limit traffic until its own bucket fills, defeating the point of global coordination. When Redis is down, we accept that each node independently allows up to the per-node limit (current behavior).

**TTL orphan handling:** If `INCR` succeeds but TTL application does not, the request may still be evaluated using the returned count, but the event must be logged and counted with a dedicated metric. If this shows up in production, add a periodic cleanup for `143:ratelimit:*` keys with no TTL. We should not silently rely on key eviction for limiter correctness.

### 2.3 Job Queue Notifications

**Problem:** Workers poll the `jobs` table every 5 seconds. This means: (a) up to 5s latency before a job starts, and (b) N workers * 1 query / 5s of wasted Postgres load when the queue is empty.

**Solution:** After inserting a job, `PUBLISH` to `jobs:notify`. Workers `SUBSCRIBE` and attempt to claim a job immediately on notification. Keep the 5s poll as a safety net (leader election, Redis reconnect, missed messages).

**Important:** Redis pub/sub is fire-and-forget. The Postgres `FOR UPDATE SKIP LOCKED` query remains the source of truth for job claiming. Redis just reduces wake-up latency.

**Thundering herd:** Every worker wakes on every `PUBLISH` and races to `FOR UPDATE SKIP LOCKED` — N-1 lose the race. At N=3 workers this is fine; at N=20+ it becomes wasted Postgres load on every job insertion. If worker count grows past ~10, revisit: either (a) use a Redis-side claim hint (`SETNX jobs:claim:{id}` before the Postgres claim, with a short TTL), or (b) move to a proper Redis-based queue (Streams with consumer groups). Not urgent today.

### 2.4 Short-Lived Caches

> **Status:** Deferred pending measurement. Auth session lookups are single indexed Postgres queries (typically 1–2ms). Before implementing this, measure whether auth is actually a hot path in p99 API latency. If it isn't, the complexity isn't worth it. Track `auth_session_lookup_duration_seconds` in Prometheus and revisit.

**Problem (if measurement confirms it):** Auth session tokens hit Postgres on every API request. GitHub installation tokens are re-fetched frequently.

**Solution:**
- Cache auth session lookups in Redis with TTL matching `expires_at` (or shorter). Invalidate on logout/revocation by deleting the key.
- Cache GitHub App installation tokens (TTL = token expiry - buffer).
- Cache Codex OAuth tokens similarly.

**Invalidation model:** Single-session logout and per-token revocation use `DEL 143:auth:token:{hash}`. For the rare "revoke all sessions for user X" admin operation (no product surface for this yet), run a one-off `SCAN` with a `MATCH 143:auth:token:*` filter and delete matching keys whose stored user_id equals the target. This is slow but acceptable because it's an offline maintenance path, not an every-request hot path.

We intentionally do **not** introduce a per-user generation counter. Doing so would add a second Redis `GET` to every authenticated request forever — doubling the Redis cost of the hot path — to save a rare admin operation. If bulk revocation becomes a common product operation later, reconsider.

**Fallback:** Cache miss falls through to Postgres. Redis unavailability = every request is a cache miss = current behavior.

### 2.5 Stream Cleanup

Streams expire "1h after session ends" via `EXPIREAT` (see Section 9). The session teardown path must call `EXPIREAT` on the `:logs`, `:status`, and `:events` streams when a session reaches a terminal state. However, if a session crashes without clean teardown, these streams leak.

**Safety net:** Run a periodic cleanup goroutine (e.g., every 10 minutes) that:
1. Queries Postgres for sessions in a terminal state (`completed`, `failed`, `cancelled`) that ended more than 1 hour ago, **limited to 500 rows per tick** (`LIMIT 500`). This prevents a backlog from creating a single-tick spike that slams Redis and Postgres.
2. For each, checks whether the corresponding stream keys still exist (`EXISTS`).
3. Deletes any orphaned streams.

If a tick processes the full 500-row batch, the next tick runs immediately (not after the 10-minute delay) until the backlog is drained. Track `redis_cleanup_batch_size` to detect sustained backlogs.

This is cheap — it's a single Postgres query plus a few `EXISTS` + `DEL` calls per tick — and ensures streams don't accumulate indefinitely after ungraceful shutdowns.

### 2.6 Future: Session Presence and Active Connections

As the platform grows, Redis can track which nodes hold active sandbox connections (`SADD node:{id}:sessions {session_id}`), enabling smarter load balancing and session affinity without custom protocols.

---

## 3. Architecture

### 3.1 Connection Model

```
┌─────────────────────────────────────────────────────────┐
│                   Load Balancer                          │
└────────────┬──────────────┬──────────────┬──────────────┘
             │              │              │
        ┌────▼────┐   ┌────▼────┐   ┌────▼────┐
        │ Node 1  │   │ Node 2  │   │ Node 3  │
        │ API     │   │ API     │   │ API     │
        │ Worker  │   │ Worker  │   │ Worker  │
        └──┬───┬──┘   └──┬───┬──┘   └──┬───┬──┘
           │   │          │   │          │   │
           │   └──────────┼───┼──────────┼───┘
           │              │   │          │
      ┌────▼────┐    ┌───▼───▼───┐  ┌──▼───────┐
      │Postgres │    │   Redis    │  │  Docker   │
      │ (primary│    │ (cache +   │  │  Daemon   │
      │  store) │    │  pub/sub)  │  │ (sandbox) │
      └─────────┘    └───────────┘  └───────────┘
```

Key principle: **Redis is an accelerator, not a source of truth.** Every piece of data in Redis is either:
- Derivable from Postgres (caches), or
- Ephemeral by nature (pub/sub messages, rate limit windows)

If Redis disappears, the system degrades to current behavior, not to failure.

For rollout purposes, treat the features in two classes:
- **Safe accelerator features:** session streams and job notifications. These improve latency and efficiency but do not weaken correctness if Redis degrades.
- **Control-plane features:** distributed rate limiting. These only ship once their eviction and failure semantics are explicitly acceptable.

### 3.2 Go Client Library

Use [`github.com/redis/go-redis/v9`](https://github.com/redis/go-redis), the official Go client. It supports:
- Connection pooling (built-in)
- Pub/Sub with automatic reconnection
- Sentinel and Cluster modes for HA
- Context-aware commands (cancellation, timeouts)
- Lua scripting for atomic operations (available if needed in the future)

**Topology choice:** Use `go-redis` through its `redis.UniversalClient` abstraction from day 1, not a concrete `*redis.Client`. `UniversalClient` can represent a standalone server, Failover/Sentinel deployment, or Cluster deployment behind the same interface. That keeps the application wrapper stable even if infrastructure changes later.

### 3.3 Redis Client Wrapper

Create `internal/cache/redis.go`:

```go
package cache

import (
    "context"
    "time"

    "github.com/redis/go-redis/v9"
    "github.com/rs/zerolog"
)

// Client wraps go-redis with fallback behavior and circuit breaking.
type Client struct {
    rdb     redis.UniversalClient
    breaker *CircuitBreaker
    logger  zerolog.Logger
}

// Config holds Redis connection parameters.
type Config struct {
    // Topology: standalone | sentinel | cluster
    Topology string `env:"REDIS_TOPOLOGY" envDefault:"standalone"`

    // Standalone / single-endpoint managed Redis.
    URL string // REDIS_URL (redis://host:port/db or rediss:// for TLS)

    // Sentinel / Cluster can provide multiple seed addresses.
    Addrs []string // REDIS_ADDRS=host1:6379,host2:6379

    // Sentinel-specific settings.
    MasterName string // REDIS_MASTER_NAME

    Password string // REDIS_PASSWORD (optional, for ACL-based auth)
    PoolSize int    // command pool size only; see Section 9.1
    Timeouts TimeoutConfig
}

type TimeoutConfig struct {
    Dial  time.Duration // default: 5s
    Read  time.Duration // default: 3s
    Write time.Duration // default: 3s
}

func New(cfg Config, logger zerolog.Logger) *Client {
    var rdb redis.UniversalClient
    switch cfg.Topology {
    case "", "standalone":
        opts, err := redis.ParseURL(cfg.URL)
        if err != nil {
            logger.Error().Err(err).Str("url", cfg.URL).Msg("invalid Redis URL, running without Redis")
            return nil
        }
        if cfg.Password != "" {
            opts.Password = cfg.Password
        }
        if cfg.PoolSize > 0 {
            opts.PoolSize = cfg.PoolSize
        }
        // Apply timeouts...
        rdb = redis.NewClient(opts)
    case "sentinel":
        opts := &redis.FailoverOptions{
            MasterName:    cfg.MasterName,
            SentinelAddrs: cfg.Addrs,
            Password:      cfg.Password,
            PoolSize:      cfg.PoolSize,
            // Apply timeouts...
        }
        rdb = redis.NewFailoverClient(opts)
    case "cluster":
        opts := &redis.ClusterOptions{
            Addrs:    cfg.Addrs,
            Password: cfg.Password,
            PoolSize: cfg.PoolSize,
            // Apply timeouts...
        }
        rdb = redis.NewClusterClient(opts)
    default:
        logger.Error().Str("topology", cfg.Topology).Msg("invalid Redis topology, running without Redis")
        return nil
    }
    breaker := NewCircuitBreaker()
    client := &Client{rdb: rdb, breaker: breaker, logger: logger}
    // Ping to verify connectivity, but don't fail startup — Redis is optional.
    // A startup ping failure forces the breaker open immediately so command-path
    // callers cleanly skip Redis until the first half-open probe window.
    if err := rdb.Ping(context.Background()).Err(); err != nil {
        logger.Warn().Err(err).Msg("Redis ping failed on startup, will retry via circuit breaker")
        breaker.ForceOpen()
    } else {
        logger.Info().Msg("Redis connected")
    }
    return client
}

// Available reports whether Redis is likely reachable.
// Uses the circuit breaker state instead of issuing a PING on every call,
// avoiding an extra network round-trip in the hot path.
func (c *Client) Available() bool {
    if c == nil || c.rdb == nil {
        return false
    }
    return c.breaker.Allow()
}
```

The rest of the application interacts through this wrapper. When `Client` is nil (Redis not configured), callers skip Redis paths entirely.

**Why this preserves zero-code-change migration:** The application code only depends on the wrapper plus `redis.UniversalClient` operations. Moving from a single VPS Redis to managed standalone Redis, Sentinel, or Cluster is then a configuration change in `REDIS_TOPOLOGY` and connection settings, not an application refactor.

**Circuit breaker scope:** The breaker governs **short-lived command calls only** (rate-limit INCR, auth cache GET, XADD, PUBLISH, XRANGE catch-up reads). It does **not** gate long-lived subscriber connections (fan-out `XREAD BLOCK`, job-notify `SUBSCRIBE`). Those are managed by `go-redis`'s own reconnection logic plus a per-subscriber supervisor goroutine that re-establishes the subscription on error.

Rationale: treating a 30-second blocked `XREAD` as "still healthy" while a command call times out in 3s prevents one slow subscriber from tripping the breaker and cascading into an auth-cache miss storm on Postgres. Conversely, a broken subscription should not block hot-path commands from using Redis.

When the breaker opens, in-flight blocked subscribers are left alone; they will naturally notice the connection failure and the supervisor will restart them once the breaker closes. Command-path callers see `Available() == false` and skip Redis for the cooldown window.

---

## 4. Configuration

Add to `internal/config/config.go`:

```go
// Redis (optional — system degrades gracefully without it)
RedisTopology   string `env:"REDIS_TOPOLOGY"   envDefault:"standalone"` // standalone | sentinel | cluster
RedisURL        string `env:"REDIS_URL"`                               // standalone endpoint, e.g. redis://localhost:6379/0
RedisAddrs      string `env:"REDIS_ADDRS"`                             // comma-separated host:port list for sentinel/cluster
RedisMasterName string `env:"REDIS_MASTER_NAME"`                       // sentinel master name
RedisPassword   string `env:"REDIS_PASSWORD"`                          // ACL password (if not in URL)
RedisPoolSize   int    `env:"REDIS_POOL_SIZE"    envDefault:"0"`       // command pool size only; 0 = library default
```

**Important:** Redis is opt-in. For `standalone`, `REDIS_URL` is required. For `sentinel` and `cluster`, `REDIS_ADDRS` is required (and `REDIS_MASTER_NAME` is also required for `sentinel`). If the required settings for the selected topology are missing, the `cache.Client` is nil and all Redis-dependent code paths fall back to existing behavior.

### 4.1 Environment Files

`.env` (development defaults):
```
REDIS_TOPOLOGY=standalone
REDIS_URL=redis://redis:6379/0
```

`.env.production.enc` (SOPS-encrypted, production):
```
REDIS_TOPOLOGY=standalone
REDIS_URL=rediss://clustercfg.xxx.cache.amazonaws.com:6379/0
REDIS_PASSWORD=<encrypted>
```

---

## 5. Docker Compose Integration

Redis will be added to the dev `docker-compose.yml` alongside Postgres as part of Phase 2 (see §7). This section describes the intended service definition — it has **not** been applied to `docker-compose.yml` yet:

```yaml
services:
  redis:
    image: redis:8.6-alpine
    ports:
      - "6379:6379"
    volumes:
      - redisdata:/data
    healthcheck:
      test: ["CMD", "redis-cli", "ping"]
      interval: 5s
      timeout: 3s
      retries: 5
    deploy:
      resources:
        limits:
          memory: 256M
          cpus: "0.5"
    command: redis-server --save 60 1 --loglevel warning --maxmemory 200mb --maxmemory-policy allkeys-lru

  server:
    depends_on:
      postgres:
        condition: service_healthy
      redis:
        condition: service_healthy
    environment:
      DATABASE_URL: postgres://onefortythree:dev@postgres:5432/onefortythree?sslmode=disable
      REDIS_URL: redis://redis:6379/0
      # ... existing env vars ...

volumes:
  redisdata:
  # ... existing volumes ...
```

**Resource budget:** Redis is extremely memory-efficient. 256MB handles millions of small keys, but stream entries dominate this workload. In the shared-instance rollout, `allkeys-lru` is acceptable only because the first phases are stream and pub/sub acceleration, not authoritative control-plane decisions. The alpine image is ~13MB.

### 5.1 Scaling with Docker Compose

For local multi-node testing, use ordinary Compose scaling rather than the `deploy.replicas` Swarm field:

```yaml
# docker-compose.override.yml (optional)
services:
  server-worker:
    extends:
      service: server
    environment:
      MODE: worker
    ports: []  # no API port needed
```

Then start multiple workers with:

```bash
docker compose up --scale server-worker=2
```

All nodes connect to the same Redis instance. Pub/Sub and rate-limit keys are inherently shared.

---

## 6. Production Deployment

### 6.1 Self-Hosted (Hetzner / VPS)

For self-hosted deployments, Redis runs as a dedicated node alongside the existing db, app, worker, and logging nodes. The full setup — including `docker-compose.redis.yml`, `deploy/cloud-init/redis.yml`, VPS sizing, firewall rules, and how to wire `REDIS_URL` into app/worker nodes — is documented in [36-docker-agents-vps-architecture.md, Step 3d](../36-docker-agents-vps-architecture.md#step-3d-add-a-dedicated-redis-node).

**Quick summary:**
- Provision a small VPS (Hetzner CX22, 2 vCPU / 4 GB, ~€4/month)
- Run `docker-compose.redis.yml` (Redis 8.6 with password auth)
- Set `REDIS_TOPOLOGY=standalone` and `REDIS_URL=redis://:PASSWORD@REDIS_PRIVATE_IP:6379/0` on all app/worker nodes
- Block port 6379 from public internet via firewall (private network only)

**Single-point-of-failure tradeoff:** A single Hetzner VPS means every Redis outage (crash, host reboot, network blip) triggers a platform-wide failover to Postgres. The graceful-degradation design absorbs this correctly, but the outage isn't free — see §8.1 for the fallback-load budget. Options if that becomes a problem:
1. **Redis Sentinel on 2–3 VPSes** (~€12/month, auto-failover, still self-hosted) — moderate ops burden (quorum config, Sentinel on each app node).
2. **Move to managed Redis with replica** (§6.3, ~$50/month) — zero ops burden, automatic failover, TLS included.

At 143's current scale the single-VPS path is defensible because fallback is safe and outages are rare. Track `redis_fallback_total` in production; if it fires more than a couple times a month, it's time to upgrade.

### 6.2 Migrating to Hosted/Managed Redis

The design explicitly supports a zero-code-change migration from self-hosted Redis to a managed service **because the wrapper is topology-agnostic from day 1**.

### 6.3 Managed Options

| Provider | Service | Notes |
|----------|---------|-------|
| AWS | ElastiCache (Redis OSS) or MemoryDB | Cluster mode, automatic failover, encryption in transit |
| GCP | Memorystore for Redis | Standard or Cluster tier |
| Azure | Azure Cache for Redis | Basic through Premium tiers |
| Render/Railway/Fly | Managed Redis add-on | Simpler, good for small scale |

### 6.4 What Changes

| Concern | Docker Compose | Hosted |
|---------|---------------|--------|
| `REDIS_TOPOLOGY` | `standalone` | `standalone`, `sentinel`, or `cluster` |
| `REDIS_URL` | `redis://redis:6379/0` | `rediss://host:6379/0` for standalone managed Redis |
| `REDIS_ADDRS` | (unused) | `host1:6379,host2:6379,...` for Sentinel or Cluster |
| `REDIS_MASTER_NAME` | (unused) | required for Sentinel |
| `REDIS_PASSWORD` | (none) | Set via ACL or AUTH |
| Persistence | RDB snapshots to volume | Managed replication + snapshots |
| HA | Single instance | Multi-AZ replicas, auto-failover |
| Code changes | None | None |

The wrapper handles TLS (`rediss://` scheme), authentication, and topology selection through configuration. No application code changes are required when changing Redis deployment mode.

### 6.5 Cluster Mode Considerations (future, not needed today)

Redis Cluster is only relevant once we outgrow a single-master topology. At 143's current scale (tens of orgs, <1000 req/sec, <1GB working set), a single Redis instance plus optional replica covers us comfortably. The hash-tag conventions below are kept so that migrating to Cluster is a configuration change rather than a rekey exercise — but no Cluster-specific work is needed for Phases 1–6.

If/when we move to Redis Cluster (multiple shards):
- All keys for a given entity should use hash tags to ensure co-location on the same shard. The hash tag is the content between `{` and `}`: e.g., `143:stream:{ses:abc}:logs` and `143:stream:{ses:abc}:status` hash on `ses:abc`. **Note:** be careful with hash tag content — if the tagged portion is the same for many entities (e.g., a literal like `{session}`), all keys land on one shard. The entity ID must be inside the braces.
- Pub/Sub in cluster mode broadcasts to all shards (supported since Redis 7+).
- `go-redis` supports `NewClusterClient` as a drop-in; switch is config-only.

---

## 7. Implementation Plan

### Phase 1: Provision Hetzner Infrastructure
1. Provision a CX22 VPS (2 vCPU / 4 GB) on Hetzner Cloud for Redis
2. Attach VPS to the existing private network (Hetzner Cloud Networks)
3. Deploy `docker-compose.redis.yml` with password auth (see [36-docker-agents-vps-architecture.md, Step 3d](../36-docker-agents-vps-architecture.md#step-3d-add-a-dedicated-redis-node))
4. Configure firewall: block port 6379 from public internet, allow from private network CIDR only
5. Set `REDIS_TOPOLOGY=standalone` and `REDIS_URL=redis://:PASSWORD@REDIS_PRIVATE_IP:6379/0` in `.env` on all app/worker VPS nodes
6. Verify connectivity: `redis-cli -h REDIS_PRIVATE_IP -a PASSWORD ping` from an app node

### Phase 2: Application Integration
1. Add `redis:8.6-alpine` to `docker-compose.yml` (dev) — see §5 for the intended service definition
2. Add `REDIS_TOPOLOGY`, `REDIS_URL`, `REDIS_ADDRS`, `REDIS_MASTER_NAME`, and `REDIS_PASSWORD` to `internal/config/config.go`
3. Create `internal/cache/redis.go` wrapper with circuit breaker and health check
4. Wire into `main.go` — create client on startup, pass to services
5. Add Redis ping to `/healthz` endpoint (non-fatal: report status but don't fail health check)

### Phase 3: Redis Streams for SSE
1. Create `internal/cache/streams.go` with `StreamPublish()` (XADD) and `StreamRange()` (XRANGE for catch-up) helpers
2. Implement fan-out manager: one goroutine per node per active session calls `XREAD BLOCK` from tail (`$`); maintains a ~1000-entry ring buffer of recently seen entries; SSE clients register bounded send channels (~256 capacity); goroutine exits when no local clients remain
3. Modify log-writing path to `XADD` to `stream:session:{id}:logs` (with `MAXLEN ~ 10000`) after DB insert
4. Modify SSE handler to accept `?last_event_id=...` on reconnect. On connect: (a) replay from ring buffer if last-seen is in range, (b) else `XRANGE` catch-up from stream, (c) else Postgres backfill. Then register with fan-out.
5. Implement slow-consumer handling: if fan-out send-to-client would block, close the client channel. SSE handler sends `event: error` / `data: slow consumer` and disconnects; client reconnects via the catch-up path.
6. Modify session status updates to `XADD` to `stream:session:{id}:status`
7. Publish narrow session UI events to `stream:session:{id}:events`, including `thread.inbox.*`, `thread.runtime.*`, and `session.workspace.generation_changed`
8. Add `EXPIREAT` calls in session teardown path for `:logs`, `:status`, and `:events` streams
9. Add periodic cleanup goroutine (every 10 min, batched at 500 rows — see §2.5) to delete orphaned streams for terminated sessions

### Phase 4: Distributed Rate Limiting (gated)
1. Create `internal/cache/ratelimit.go` with fixed-window `INCR`/`EXPIRE` counter
2. Provision a small dedicated Redis deployment for control-plane keys, or explicitly defer the phase
3. Modify rate-limit middleware to check Redis on every request when available
4. When the circuit breaker reports Redis unavailable, fall back to per-node in-memory buckets (current behavior). Do not run both in parallel — one or the other, chosen by breaker state.

### Phase 5: Job Notifications
1. After job insertion, `PUBLISH` to `jobs:notify`
2. Worker subscribes to `jobs:notify`; on message, immediately attempt job claim
3. Keep 5s poll interval as safety net

### Phase 6: Auth Token Cache (deferred — validate with metrics first)
**Gate:** Before implementing, confirm from production metrics that auth session lookup is a meaningful p99 API latency contributor. If it's in the noise (likely, for a single indexed Postgres query), skip this phase.

If implemented:
1. On successful auth session lookup, `SET 143:auth:token:{hash} {user_json} EX {ttl}`
2. On API request, check Redis first; cache miss falls through to Postgres and populates the cache
3. On single logout / token revocation, `DEL 143:auth:token:{hash}`
4. Bulk revocation ("revoke all for user X") is a `SCAN`-based maintenance path, not a request-path concern — see §2.4

---

## 8. Failure Modes and Graceful Degradation

| Failure | Impact | Mitigation |
|---------|--------|------------|
| Redis down | New SSE connections fall back to Postgres catch-up/polling after reconnect; rate limiting becomes per-node; job latency stays at 5s; auth hits Postgres | All current behavior. Zero data loss. |
| Redis slow | Timeouts (3s read/write) trigger fallback | Circuit breaker opens on sustained error rate and skips Redis for a short cooldown |
| Redis full | `OOM` errors on write or eviction pressure on shared-instance data | Shared-instance rollout only covers accelerator features; distributed rate limiting stays disabled until its keys are isolated from stream eviction risk |
| Network partition | Some nodes lose Redis, others don't | Each node independently falls back. No split-brain risk because Redis is not authoritative. |

### Circuit Breaker

Uses an **error-rate over a sliding window** rather than a consecutive-failure count. Consecutive counts are brittle under bursty workloads: a 5-in-a-row failure threshold can trip from a normal network blip coinciding with low-volume traffic, while high-volume traffic with 50% failure rate might never accumulate 5 consecutive failures because successes keep resetting the counter.

**Parameters:**
- **Window:** 10 seconds
- **Min samples before opening:** 20 (don't trip the breaker on 1-of-2 failures)
- **Error-rate threshold:** 50% over the window
- **Cooldown:** 10 seconds (shorter than the original 30s — half-open probes are cheap, and we prefer to return to healthy Redis quickly)
- **Half-open probe:** single request; success → closed, failure → open (cooldown restarted)

```go
// internal/cache/breaker.go

// States: closed (0) = healthy, open (1) = failing, half-open (2) = probing
const (
    stateClosed   int32 = 0
    stateOpen     int32 = 1
    stateHalfOpen int32 = 2
)

type CircuitBreaker struct {
    state    atomic.Int32
    openedAt atomic.Int64 // unix nanos

    // Sliding-window counters — bucketed by second, rolled every tick.
    window     time.Duration // e.g. 10s
    minSamples int32         // e.g. 20
    errorRate  float64       // e.g. 0.5
    cooldown   time.Duration // e.g. 10s

    mu      sync.Mutex
    buckets []bucket // ring of 1-second buckets covering `window`
}

type bucket struct {
    ts       int64
    attempts int32
    failures int32
}

// Allow returns true if the caller may issue a Redis command.
// Uses CAS so exactly one goroutine transitions open → half-open per cooldown.
func (cb *CircuitBreaker) Allow() bool {
    switch cb.state.Load() {
    case stateClosed:
        return true
    case stateOpen:
        if time.Since(time.Unix(0, cb.openedAt.Load())) > cb.cooldown {
            if cb.state.CompareAndSwap(stateOpen, stateHalfOpen) {
                return true // this goroutine is the probe
            }
        }
        return false
    case stateHalfOpen:
        return false
    }
    return false
}

// RecordSuccess: in half-open, closes the breaker. Always records in the
// current-second bucket.
func (cb *CircuitBreaker) RecordSuccess() {
    cb.record(false)
    cb.state.CompareAndSwap(stateHalfOpen, stateClosed)
}

// RecordFailure: records in the current-second bucket. Opens the breaker if
// error rate over the window exceeds threshold and we have enough samples.
// In half-open, a single failure re-opens immediately.
func (cb *CircuitBreaker) RecordFailure() {
    cb.record(true)
    if cb.state.Load() == stateHalfOpen {
        cb.openedAt.Store(time.Now().UnixNano())
        cb.state.Store(stateOpen)
        return
    }
    attempts, failures := cb.windowStats()
    if attempts >= cb.minSamples && float64(failures)/float64(attempts) >= cb.errorRate {
        cb.openedAt.Store(time.Now().UnixNano())
        cb.state.CompareAndSwap(stateClosed, stateOpen)
    }
}

// ForceOpen is used at startup when the initial connectivity probe fails.
// This bypasses the sliding-window sample threshold so the command path
// immediately falls back instead of burning requests until the breaker trips.
func (cb *CircuitBreaker) ForceOpen() {
    cb.openedAt.Store(time.Now().UnixNano())
    cb.state.Store(stateOpen)
}
```

Caller contract: wrap every short-lived Redis command in `breaker.Allow()` / `breaker.RecordSuccess()` / `breaker.RecordFailure()`. Context timeouts (3s read/write from `TimeoutConfig`) count as failures.

### 8.1 Postgres Fallback Load Analysis

When the breaker opens or Redis goes down, all Redis-backed paths fall back to their non-Redis behavior. Estimate the worst-case Postgres load so the primary store is sized for it:

| Path | Peak fallback load (per node, worst case) | Notes |
|------|--------------------------------------------|-------|
| SSE log streaming | 1 query/sec × active sessions on that node | e.g. 20 sessions = 20 QPS/node. Existing behavior — Postgres already handles this. |
| Rate limiting | 0 extra queries | In-memory bucket doesn't touch Postgres. |
| Job notifications | 1 query / (5s × worker count) — i.e. current polling | Existing behavior. |
| Auth cache (if Phase 6 lands) | 1 query per API request | Pre-cache behavior. |

**Aggregate:** For a 10-node cluster with 20 active sessions per node and 100 req/s per node of authenticated API traffic, Redis-down adds:
- 200 SSE poll QPS (already the pre-Redis baseline)
- 1,000 auth lookup QPS (if Phase 6 is live — these were previously cache hits in Redis)

Both are well within what a correctly-indexed Postgres handles, but the auth-lookup bump is worth knowing about. If Phase 6 is implemented, ensure the `auth_sessions.token` index is present and `EXPLAIN` shows an index-only scan. Track `pg_stat_statements` for `SELECT ... FROM auth_sessions` during synthetic Redis-down tests.

**What isn't absorbed by graceful degradation:** A prolonged Redis outage during a traffic spike *does* put more load on Postgres than the Redis-up steady state. The design accepts this: the point of Redis is to make the common case cheap, not to create a harder failure mode. But it means "Redis-down is survivable" is not the same as "Redis-down is free."

---

## 9. Key Namespace and TTL Strategy

All Redis keys use the `143:` prefix to avoid collisions if Redis is shared with other services in the future.

**Hash tag strategy (Cluster mode — future, not needed at current scale):** Only session stream keys use hash tags (`{ses:ID}`) to co-locate the `:logs`, `:status`, and `:events` streams for the same session on one shard. Auth and rate-limit keys are accessed independently and don't need co-location, so they intentionally omit hash tags — this distributes them evenly across shards. Until we actually run a Redis Cluster (§6.5), the hash tags are inert (a standalone Redis ignores them).

| Key pattern | Type | TTL | Size estimate | Purpose |
|-------------|------|-----|---------------|---------|
| `143:stream:{ses:ID}:logs` | Stream | `MAXLEN ~ 10000` on every `XADD`; entries clamped to 4KB; whole stream expires via `EXPIREAT` 1h after session ends (set by teardown path; orphans cleaned by periodic goroutine — see §2.5) | ~500B per entry (~5MB max) | Log streaming |
| `143:stream:{ses:ID}:status` | Stream | `MAXLEN ~ 100`; same idle expiry | ~200B per entry | Status broadcasts |
| `143:stream:{ses:ID}:events` | Stream | `MAXLEN ~ 1000`; same idle expiry | ~300B per entry | Narrow session UI events |
| `143:ratelimit:org:{id}:{window}` | String (counter) | `EXPIRE` 2x window duration; self-cleans; only used once limiter keys are isolated from stream eviction | ~32B | Rate limiting |
| `143:auth:token:{hash}` | String | Matches `expires_at` (typ. 24h) | ~1KB | Auth session cache (Phase 6; deferred — see §2.4) |
| `143:jobs:notify` | Pub/Sub channel | N/A (ephemeral) | N/A | Job wake-up signal |

**MAXLEN configuration:** The default of 10000 covers typical sessions (5MB × 20 active sessions = 100MB per node's memory footprint contribution). For log-heavy agent sessions (e.g. verbose compilation output at 100+ entries/sec), operators can override via a `session_type.stream_maxlen` config — memory is cheap relative to the UX cost of a user seeing a gap when they briefly background a tab.

**Read-after-write consistency (future managed deployments with replicas):** The use cases here are safe under eventual consistency: rate-limit counters are read and written on the same shard master (no replica reads involved), pub/sub is fire-and-forget, streams are typically read by different processes than the writer (and the write-then-read gap doesn't violate correctness — just adds latency), and auth cache invalidations are `DEL`s on master. If we ever add a read-replica for load-spreading, revisit cases where the same request writes and then reads within ~50ms.

### 9.1 Connection Pool Sizing

The `go-redis` default pool size is `10 * runtime.GOMAXPROCS`. This covers normal command traffic, but Redis Streams subscribers (`XREAD BLOCK`) and Pub/Sub subscribers each hold a dedicated connection for the lifetime of the subscription.

This section needs two separate numbers:

- **Command pool size:** how many concurrent short-lived command calls (`GET`, `INCR`, `XADD`, `XRANGE`, `PUBLISH`) the node should support.
- **Total expected Redis connections per node:** command pool size plus long-lived stream-reader and Pub/Sub connections.

**Formulas:**
- `command_pool_size = expected_peak_concurrent_command_calls`
- `total_connections_per_node = command_pool_size + active_stream_fan_out_goroutines + pubsub_subscriptions`

For a node with 20 active sessions (each with a fan-out goroutine) and subscribing to `jobs:notify`:
- Command pool size: 20
- Stream readers: 20 (one `XREAD BLOCK` per active session per node, **not** per SSE client — see fan-out pattern in Section 2.1)
- Pub/Sub subscriptions: 1 (jobs channel)
- **Total expected connections per node: ~41**

With 10 nodes, that's ~410 total connections — well within Redis's default 10,000 connection limit. Monitor both the command pool metrics and the total connection count in production to validate sizing.

**Configuration:** Set `REDIS_POOL_SIZE` to the **command pool size only**. Stream-reader and Pub/Sub connections are separate long-lived connections and should be budgeted in capacity planning, not folded into the configured pool size.

### 9.2 Revisiting Stream Sizing Later

The defaults above (MAXLEN 10000, 4KB entry clamp) are sized for the initial rollout. If/when we need to scale well past the initial target, re-check two things: **average entry size after truncation** (the `session_log_entry_bytes` histogram — add it when Phase 3 lands), and **peak concurrent active sessions**. Rough budget per active session is `MAXLEN × avg_entry_size × 1.3 (Redis overhead)`; multiply by concurrent sessions for total working-set memory. If that exceeds ~60% of Redis memory, either lower MAXLEN for noisier session types, tighten the entry clamp, or upsize Redis. If active-session age-based trimming becomes important later, add an explicit periodic `XTRIM MINID` pass rather than changing the write path. Day-one instinct is fine; measure before re-tuning.

---

## 10. Observability

- **Metrics** (Prometheus, already in use):
  - `redis_commands_total{command}` — command volume
  - `redis_command_duration_seconds{command}` — latency histogram
  - `redis_connection_pool_size` / `redis_connection_pool_idle` — pool health
  - `redis_pubsub_channels` — active subscription count
  - `redis_fallback_total{reason}` — how often we degrade to non-Redis path
  - `session_log_entry_bytes` — histogram of log-entry size at XADD time (post-truncation); feeds §9.2 sizing decisions

- **Logging** (zerolog, already in use):
  - Log Redis connection/disconnection at `info` level
  - Log fallback activations at `warn` level
  - Log circuit breaker state changes at `error` level

- **Alerting** (recommended thresholds):
  - `redis_fallback_total` rate > 0 sustained for 5 min: warn (Redis may be degraded)
  - `redis_fallback_total` rate > 0 sustained for 30 min: page (extended Redis outage, investigate)
  - `redis_command_duration_seconds` p99 > 100ms: warn (Redis latency elevated)
  - Circuit breaker state changes (`redis_circuit_breaker_state`): log at `error`, alert on open → useful for correlating with Redis infrastructure events

- **Health check**:
  - `/healthz` includes `"redis": "ok"` or `"redis": "unavailable"` (never fails the health check)

---

## 11. Security

- **No secrets in Redis.** Auth tokens stored as hashed keys; the cached value is the session metadata, not the raw token.
- **TLS in production.** Use `rediss://` URL scheme; `go-redis` handles TLS automatically.
- **ACL authentication.** Use `REDIS_PASSWORD` with Redis 6+ ACL system.
- **Network isolation.** In Docker Compose, Redis is on the internal network (no port exposure needed in production). In cloud, use VPC-internal endpoints.

---

## 12. Cost and Resource Estimate

| Environment | Setup | Memory | HA | Cost |
|-------------|-------|--------|----|------|
| Development | `redis:8.6-alpine` in Docker Compose | 256MB limit | none | $0 (local) |
| Self-hosted prod — single node | Dedicated CX22 VPS running `docker-compose.redis.yml` | 512MB limit | none (SPOF; see §6.1) | ~€4/month (~$5) |
| Self-hosted prod — Sentinel | 2–3 CX22 VPSes with Redis Sentinel | 512MB each | auto-failover, self-managed | ~€8–12/month |
| Small managed (1-3 nodes) | Single Redis instance (e.g., ElastiCache `cache.t4g.micro`) | 0.5GB | managed snapshots | ~$12/month (~256 max connections — fine for small rollouts, but re-check before roughly 6+ nodes at the §9.1 connection budget) |
| Medium managed (3-10 nodes) | Redis with replica (e.g., `cache.t4g.small` + replica) | 1.5GB x2 | multi-AZ auto-failover | ~$50/month |
| Large managed (10+ nodes) | Redis Cluster (3 shards + replicas) | 3GB per shard | sharded + replicated | ~$200/month |

Redis memory usage for 143's workload will be minimal: pub/sub messages are transient, rate-limit keys are small integers with TTLs, and cached tokens are <1KB each.

---

## 13. Testing Strategy

Redis integration must be tested at two levels: unit tests with a mock, and integration tests with a real Redis instance.

### 13.1 Unit Tests (miniredis)

Use [`github.com/alicebob/miniredis/v2`](https://github.com/alicebob/miniredis) — a pure-Go in-memory Redis server that runs in-process. No external dependencies, instant startup, deterministic time control.

```go
func TestStreamPublish(t *testing.T) {
    t.Parallel()

    mr := miniredis.RunT(t)
    rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
    client := &cache.Client{/* wrap rdb */}

    client.StreamPublish(ctx, "stream:session:abc:logs", map[string]any{"msg": "hello"})

    // miniredis supports XADD/XREAD — verify the stream entry exists
    require.True(t, mr.Exists("stream:session:abc:logs"), "stream publish should create the Redis stream key")
}
```

Use miniredis for: client wrapper logic, circuit breaker behavior, rate limit INCR/EXPIRE correctness, key TTL/expiry logic.

### 13.2 Integration Tests (testcontainers)

Use [`github.com/testcontainers/testcontainers-go`](https://github.com/testcontainers/testcontainers-go) to spin up a real `redis:8.6-alpine` container for integration tests that need full Redis behavior (stream blocking, real pub/sub timing, connection pool exhaustion).

```go
func TestSSEReconnectReplay(t *testing.T) {
    if testing.Short() {
        t.Skip("integration test requires Docker")
    }
    ctx := context.Background()
    container, _ := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
        ContainerRequest: testcontainers.ContainerRequest{
            Image:        "redis:8.6-alpine",
            ExposedPorts: []string{"6379/tcp"},
            WaitingFor:   wait.ForLog("Ready to accept connections"),
        },
        Started: true,
    })
    defer container.Terminate(ctx)
    // ... test that SSE handler replays missed stream entries on reconnect
}
```

### 13.3 Fallback Path Tests

**Critical:** Every Redis-dependent code path has a fallback. All three states must be tested:

| Code path | Redis-up | Redis-down (start) | Redis dies mid-session |
|-----------|----------|---------------------|------------------------|
| SSE log streaming | `XREAD BLOCK` delivers entries | 1s Postgres poll works for new connections | Existing connection is closed with retryable error; reconnect uses `Last-Event-ID`, catches up from Redis or Postgres, and resumes without duplicate delivery |
| Rate limiting | Global fixed-window counter enforces limit | Per-node in-memory limiter enforces per-node limit | Switch from Redis to in-memory happens within breaker cooldown; no request is double-counted |
| Job notifications | Instant wake on `PUBLISH` | 5s poll claims jobs | In-flight Subscribe reconnects automatically; poll continues as safety net |
| Auth token cache (if Phase 6) | Cache hit skips Postgres | Cache miss falls through | Hot path reverts to Postgres within breaker cooldown; no stale reads served |

**Testing the transition:** For each path, a testcontainers-based test should:
1. Start with Redis up, verify Redis path is active
2. Kill the Redis container (or block its network with `docker network disconnect`)
3. Issue requests during the outage, verify fallback path serves them within the breaker cooldown
4. Restart Redis, verify the breaker closes and the Redis path resumes

This catches bugs that neither pure Redis-up nor pure Redis-down tests find — e.g. in-flight subscriber connections hanging, breaker oscillation, or cache invalidations being silently dropped during the outage window.

For pure Redis-down tests (no transition), either pass a nil `cache.Client` or configure the circuit breaker to start in open state.

---

## 14. Alternatives Considered

| Alternative | Why not |
|-------------|---------|
| **Postgres LISTEN/NOTIFY** | Built-in, no new dependency. But: no message persistence, one channel per connection, doesn't scale well with many subscribers. Would solve pub/sub but not caching or rate limiting. |
| **KeyDB** | Redis-compatible, multithreaded. Less ecosystem support, smaller community. Could be a drop-in replacement later if needed. |
| **Valkey** | Redis fork (Linux Foundation). API-compatible. Could be swapped in via config change. Worth watching. |
| **In-memory + gossip** | Build our own distributed cache. High complexity, bug-prone, doesn't add enough value over Redis. |
| **No change** | Works today. But SSE polling and per-node rate limiting will become real problems at 5-10 nodes. |

**Postgres LISTEN/NOTIFY** deserves special mention: it could handle the pub/sub use case (Phase 2) without any new dependency. However, it has limitations:
- Each LISTEN requires a dedicated Postgres connection (not from the pool)
- No message buffering — if a listener disconnects, messages are lost
- Payload size limited to 8KB
- Doesn't address caching or rate limiting needs

Redis is the better investment because it solves multiple problems with one dependency.

---

## 15. Deployment Model: Redis is a Shared Service, Not Per-Container

Redis runs as a **single, separate service** — not embedded inside each application container. All app nodes (API servers, workers) connect to the same Redis instance over the network.

```
┌──────────┐  ┌──────────┐  ┌──────────┐
│  Node 1  │  │  Node 2  │  │  Node 3  │
│  (app)   │  │  (app)   │  │  (worker) │
└────┬─────┘  └────┬─────┘  └────┬─────┘
     │              │              │
     └──────────────┼──────────────┘
                    │
              ┌─────▼─────┐
              │   Redis   │   ← single instance, shared by all nodes
              │  (6379)   │
              └───────────┘
```

**Why not per-container?** The entire value of Redis here (shared rate limits, cross-node pub/sub, global cache) requires a single shared instance. Per-container Redis would be equivalent to the in-memory approach we already have.

**Spinning up a dedicated Redis node:** To run Redis on its own dedicated host/container (e.g., for resource isolation or scaling):
1. Deploy `redis:8.6-alpine` on a separate Docker host, VM, or as a managed service
2. Set the topology-appropriate Redis connection settings on all app nodes (`REDIS_URL` for standalone, or `REDIS_ADDRS`/`REDIS_MASTER_NAME` for Sentinel or Cluster; use `rediss://` where TLS is available)
3. No code changes — the wrapper connects using whatever topology-specific settings are configured

**Docker Compose (dev):** Redis is defined as a top-level service in `docker-compose.yml`. It shares the Docker network with the app but runs in its own container. To move it to a separate machine in the future, just change the topology-specific connection settings.

**Production:** Use a managed service (ElastiCache, Memorystore, etc.) — see Section 6. The app containers never run Redis internally.

---

## 16. Redis Version Requirements

This design targets **Redis 8.x** (`redis:8.6-alpine` in Docker Compose). Redis 8.6 is the latest stable release, bringing performance, memory efficiency, and observability improvements. All features used here (Streams, Pub/Sub, `INCR`/`EXPIRE`, `EXPIREAT`, cluster-mode Pub/Sub) are fully supported in Redis 8.x.

Redis 7.x would also work — all APIs used here are stable since Redis 7.0. However, since 8.x is the current stable line, there's no reason to target an older version.

**Valkey compatibility:** All features used here (Streams, Pub/Sub, `INCR`/`EXPIRE`, `EXPIREAT`) are part of the Redis API that Valkey preserves. Switching to Valkey is a drop-in image change.

---

## 17. Rolling Deployment Considerations

Because Redis is optional and all code paths have fallbacks, rolling deployments are safe:

- **Adding Redis (first deploy):** Old nodes without the Redis code path simply ignore Redis. New nodes start using it as they roll in. No coordination required — both old and new nodes continue to read/write Postgres as the source of truth.
- **Redis config changes:** Changing the selected Redis connection settings during a rolling deploy means some nodes briefly point to the old Redis and others to the new one. This is fine — cached data is reconstructable, and pub/sub/stream messages on the old instance are simply missed until reconnect or fallback catch-up closes the gap.
- **Removing Redis (rollback):** Clear the required connection settings for the selected topology (for example `REDIS_URL=""` in standalone mode) and redeploy. All nodes revert to pre-Redis behavior.

---

## 18. Decision

**Recommended approach:** Add Redis as an optional, gracefully-degrading dependency. Start with Phase 1 (Hetzner provisioning), Phase 2 (application integration), and Phase 3 (Redis Streams for SSE) — these deliver the highest value by eliminating the SSE polling floor. Phase 5 (job notifications) can follow once worker wake-up latency matters, and Phase 4 (distributed rate limiting) only lands once control-plane keys are isolated from stream eviction risk. Phase 6 (auth cache) is explicitly **gated on measurement** — confirm from metrics that auth is a p99 contributor before implementing.

The key design constraint — Redis is never authoritative — means we get performance benefits without operational risk. The migration path from Docker Compose to hosted Redis is a config change, not a code change.

**Operational decisions still open (see review notes):**
- Self-hosted single VPS vs. Sentinel/managed HA for production Redis (see §6.1, §12)
- Final SSE MAXLEN default (currently 10000; bump if agent logs are chattier than expected)
- Whether to implement Phase 6 at all (may never be needed)

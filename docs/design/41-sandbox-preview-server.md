# Design: Sandbox Preview Server

> **Status:** Draft | **Last reviewed:** 2026-03-23

This document describes how 143.dev can expose a live preview of code running inside a sandbox without giving untrusted preview content the same browser origin as the main app.

## Motivation

Today, 143.dev runs coding agents in isolated sandboxes and surfaces the result as diffs. That is enough for backend-heavy work, but not for frontend changes where the real question is "what does it look like when it runs?" A preview panel lets reviewers visually inspect the output of a sandboxed app before they approve the change.

The key constraint: preview content is **untrusted**. It may be agent-generated, repository-defined, or both. The design must treat the browser rendering boundary as seriously as the sandbox boundary.

## Design Goals

1. Show a live HTTP preview for a sandboxed session inside the web app.
2. Keep preview content isolated from the main app's cookies, storage, and API origin.
3. Work in both single-node and multi-node deployments.
4. Keep the transport provider-agnostic so Docker, E2B, and future backends can support it.
5. Let both agents and humans visually interact with the running preview — agents can capture screenshots, inspect DOM state, and self-verify; humans can click on elements and give visual feedback that is passed directly to the agent.
6. Keep the initial scope narrow enough to ship safely.

## Product Defaults

- **Internal-only access by default**. Preview URLs are available only to authenticated 143 users in the same org.
- **Trusted-internal with hard guardrails**. Internal access does not relax browser-origin isolation, sandboxing, or egress controls.
- **Explicit single-node MVP**. Phase 1 runs the preview gateway, preview manager, and Docker runtime on one machine, but keeps node-aware data and interfaces so multi-node routing can be added later.

## Non-Goals for MVP

1. Arbitrary desktop streaming (no VNC / noVNC).
2. Custom user-defined sidecar containers or Docker Compose-style orchestration.
3. Previewing apps that require production secrets or external private infrastructure.
4. Direct browser access to sandbox container ports.
5. Visual regression diffing as a CI/review gate (automated pass/fail checks with thresholds). Note: semantic diffs are available as agent tools for self-verification, but not as automated gating checks in MVP.

## What Preview Replaces

Sandbox preview is intended to replace local setup for **reviewing and iterating on supported repos**, especially frontend-heavy changes where a human needs to inspect behavior before approving it.

It is **not** a general-purpose browser IDE or a complete replacement for local development. Users can ask 143.dev to make changes, inspect the running result, and give follow-up guidance without cloning the repo locally, but complex multi-service debugging may still require a traditional development environment.

For MVP, the target repos are:

- single frontend apps (React, Vue, Svelte with Vite/Next.js/Nuxt/etc.)
- framework-integrated full-stack apps where one process serves both frontend and API (Next.js API routes, Remix loaders, Nuxt server routes, Rails with Hotwire)
- monorepos with a frontend and backend that can run as separate processes inside a single sandbox, communicating over `localhost` (e.g., React SPA + Express/Flask/Django/Rails API)
- SPAs / SSR apps that can boot from repo-local dependencies and mocked or managed non-production data
- apps that connect to external staging databases or APIs via managed destinations (e.g., a staging RDS instance)
- apps that need a local database or cache, using platform-provided infrastructure services (PostgreSQL, Redis, MySQL)
- repos where the app can start without production-only secrets

For MVP, the non-target repos are:

- apps that require custom Docker Compose or Kubernetes orchestration
- apps that need infrastructure services not in the platform-provided set (e.g., Elasticsearch, Kafka, custom containers)
- apps that depend on private infrastructure that is not represented as a managed preview destination or a platform infrastructure template
- separate-repo architectures where frontend and backend live in different repositories

## Preview Readiness

To make the feature understandable to non-engineers, preview support should be presented as a repo readiness state rather than a binary success/failure:

- `ready` - the repo can preview with the default `bootstrap` config (including any platform infrastructure declared in the config)
- `admin_setup_required` - the repo can preview after an admin attaches managed credentials or managed destinations
- `not_supported` - the repo is not a fit for preview MVP (e.g., requires unsupported infrastructure templates or custom containers)

The UI should surface this readiness state before a user clicks `Start Preview` so people know whether preview is expected to work without extra setup.

## Core Decision

**Preview must not be served from the main app origin.**

The earlier same-origin iframe idea is unsafe for a multi-tenant product because untrusted preview JavaScript could otherwise inherit the authenticated app origin. That would let preview code interact with app APIs and browser state in ways that bypass the intended sandbox boundary.

Instead:

- The main app stays on the normal app origin, for example `app.143.dev`
- Each preview gets its own hostname, for example `<preview-id>.preview.143.dev`
- Preview access uses a short-lived signed preview token that is scoped to one preview instance
- Preview responses never share the app session cookie or CSRF cookie space
- Shared preview origins are not allowed

## High-Level Architecture

```
┌──────────────────────────────────────────────────────────────┐
│ Browser                                                      │
│                                                              │
│  app.143.dev                                                 │
│  ┌──────────────────┐   iframe / new tab   ┌──────────────┐  │
│  │ Session Detail   │ ───────────────────▶ │ preview.143  │  │
│  │ Diff + Controls  │                      │ .dev         │  │
│  └────────┬─────────┘                      └──────┬───────┘  │
│           │ API                                         │      │
└───────────┼──────────────────────────────────────────────┼──────┘
            │                                              │
┌───────────▼──────────────────┐              ┌────────────▼──────────┐
│ 143 API Server                │              │ Preview Gateway        │
│                                │              │ validates preview token│
│ start/stop/status endpoints    │              │ proxies HTTP + WS     │
│ preview token minting          │              │ strips sensitive hdrs │
└───────────┬───────────────────┘              └────────────┬──────────┘
            │ internal control plane                           │
┌───────────▼──────────────────────────────────────────────────▼────────┐
│ Worker Node / Sandbox Provider                                        │
│                                                                        │
│ starts preview services inside sandbox                                  │
│ owns preview lifecycle                                                 │
│ exposes provider-specific stream back to gateway                       │
└───────────┬────────────────────────────────────────────────────────────┘
            │
┌───────────▼────────────────────────────────────────────────────────────┐
│ Sandbox                                                                │
│                                                                        │
│ repo checkout + agent changes + preview services (primary + support)    │
└────────────────────────────────────────────────────────────────────────┘
```

## Request Flow

1. A user opens a session on `app.143.dev` and clicks `Start Preview` (or arrives via the "Launch Preview" link on a PR comment, which deep-links to the session with `?preview=1` to auto-trigger start).
2. The API server validates org access and reads the repo preview config.
3. The owning worker node starts preview services inside the sandbox. For multi-service configs, services are started in dependency order (support services first, then primary). `HOST=0.0.0.0` is injected into each service's environment by default.
4. The API server stores a `preview_instance` record with associated `preview_services` rows. The frontend streams startup progress (Build → Init → Start) via the session WebSocket channel, showing per-service status for multi-service configs.
5. Once the preview is ready, the frontend mints a bootstrap token via `POST /api/v1/sessions/{id}/preview/bootstrap`.
6. The frontend sets the iframe `src` to `https://<preview-id>.preview.143.dev/bootstrap` (a static bootstrap page, no token in URL).
7. The bootstrap page signals readiness via `postMessage`. The app origin sends the token to the iframe via `postMessage` with origin validation.
8. The bootstrap page exchanges the token for a preview-domain-only session cookie via a same-origin POST, then navigates to the preview root.
9. The preview gateway proxies HTTP and WebSocket traffic to the owning worker/provider stream.
10. Idle previews are stopped automatically based on activity-aware timeouts; the browser must request a fresh bootstrap token to resume.

The bootstrap token is one-time and short-lived. It never appears in a URL, browser history, or server access logs. The preview domain does not receive the main app's session cookie.

Per-preview hostnames require wildcard DNS and TLS for the preview zone, but they remove a large class of cross-preview browser-isolation problems that a shared preview origin would create.

## Preview Configuration

Each preview configuration may contain **one or more services**. One service is designated as the `primary` — this is the service the preview gateway proxies browser traffic to. All other services are **support services** that run alongside the primary inside the same sandbox, reachable via `localhost`.

Repo config lives in `.143/preview.json`:

### Single-Service Example (SPA or Framework-Integrated Full-Stack)

```json
{
  "version": "3",
  "name": "frontend",
  "command": ["npm", "run", "dev"],
  "cwd": "frontend",
  "port": 3000,
  "env": {
    "NODE_ENV": "development"
  },
  "ready": {
    "http_path": "/",
    "timeout_seconds": 90
  },
  "credentials": {
    "mode": "none"
  },
  "network": {
    "mode": "managed",
    "destinations": []
  }
}
```

The single-service format uses `command`, `port`, `cwd`, `env`, and `ready` at the top level. The preview manager normalizes it internally to the multi-service format with a single entry designated as `primary`.

### Multi-Service Example (Frontend + Backend + Staging DB)

```json
{
  "version": "3",
  "name": "Full Stack Preview",
  "primary": "frontend",
  "services": {
    "frontend": {
      "command": ["npm", "run", "dev"],
      "cwd": "frontend",
      "port": 3000,
      "env": {
        "NODE_ENV": "development",
        "REACT_APP_API_URL": "http://localhost:4000"
      },
      "ready": {
        "http_path": "/",
        "timeout_seconds": 90
      }
    },
    "backend": {
      "command": ["python", "manage.py", "runserver", "0.0.0.0:4000"],
      "cwd": "backend",
      "port": 4000,
      "env": {
        "DJANGO_SETTINGS_MODULE": "config.settings.preview"
      },
      "ready": {
        "http_path": "/health",
        "timeout_seconds": 60
      }
    }
  },
  "credentials": {
    "mode": "managed_env",
    "credential_set": "repo-main-preview",
    "env": ["DATABASE_URL"],
    "inject_into": ["backend"]
  },
  "network": {
    "mode": "managed",
    "destinations": [
      "preview_db"
    ]
  }
}
```

In this example:

- The frontend runs on port 3000 and is the `primary` — the preview gateway proxies browser traffic here
- The backend runs on port 4000 inside the same sandbox — the frontend reaches it at `http://localhost:4000`
- The backend connects to a staging RDS instance via the `preview_db` managed destination
- `DATABASE_URL` is injected only into the `backend` service via `credentials.inject_into`, so the frontend process never sees database credentials
- Both services share the same filesystem (the repo checkout + agent changes)

### Multi-Service Example With Platform Infrastructure (Frontend + Backend + Local PostgreSQL)

For repos that don't have a staging database available, the config can request platform-provided infrastructure instead of a managed destination:

```json
{
  "version": "3",
  "name": "Full Stack (Local DB)",
  "primary": "frontend",
  "services": {
    "frontend": {
      "command": ["npm", "run", "dev"],
      "cwd": "frontend",
      "port": 3000,
      "env": {
        "NODE_ENV": "development",
        "REACT_APP_API_URL": "http://localhost:4000"
      },
      "ready": {
        "http_path": "/",
        "timeout_seconds": 90
      }
    },
    "backend": {
      "command": ["python", "manage.py", "runserver", "0.0.0.0:4000"],
      "cwd": "backend",
      "port": 4000,
      "env": {
        "DJANGO_SETTINGS_MODULE": "config.settings.preview"
      },
      "ready": {
        "http_path": "/health",
        "timeout_seconds": 60
      }
    }
  },
  "infrastructure": {
    "db": {
      "template": "postgres-16",
      "init_script": "db/seed.sql",
      "inject_env": {
        "DATABASE_URL": "postgres://{user}:{password}@{host}:{port}/{database}"
      },
      "inject_into": ["backend"]
    }
  },
  "credentials": {
    "mode": "none"
  },
  "network": {
    "mode": "managed",
    "destinations": []
  }
}
```

In this example:

- A platform-provided PostgreSQL 16 container runs alongside the sandbox
- `db/seed.sql` is executed against the database after it starts, populating it with test data from the repo
- `DATABASE_URL` is constructed by the preview manager using the infrastructure container's actual connection details and injected into the `backend` service
- No managed destination or external staging database is needed — the preview is fully self-contained
- The `credentials.mode` is `none` because all credentials are auto-generated by the platform infrastructure, not admin-managed secrets

A config can use both `infrastructure` and `network.destinations` simultaneously. For example, a config might use a platform-provided PostgreSQL but connect to an external staging Stripe API via a managed destination.

### Config Rules

| Field | Type | Notes |
|------|------|-------|
| `name` | string | Human label for the preview |
| `primary` | string | Key into `services` map designating which service the gateway proxies to. Required when `services` is present. |
| `services` | object | Map of service name → service config. Optional — if absent, the config is a single-service preview using top-level `command`/`port`/etc. |
| `services.<svc>.command` | string[] | Executed as argv, not shell-interpolated |
| `services.<svc>.cwd` | string | Relative to `/workspace`; must stay within repo root |
| `services.<svc>.port` | int | Port the service binds to inside the sandbox |
| `services.<svc>.env` | object | Non-secret literal env values for this service |
| `services.<svc>.ready` | object | Per-service readiness probe config |
| `infrastructure` | object | Map of infra name → platform infrastructure config. Optional. |
| `infrastructure.<name>.template` | string | Platform-provided template name (e.g., `postgres-16`, `redis-7`, `mysql-8`) |
| `infrastructure.<name>.init_script` | string | Path to a SQL or setup script, relative to repo root. Optional. |
| `infrastructure.<name>.inject_env` | object | Env var templates using `{host}`, `{port}`, `{user}`, `{password}`, `{database}` placeholders |
| `infrastructure.<name>.inject_into` | string[] | Which services receive the injected env vars. Defaults to all services if omitted. |
| `credentials` | object | Managed credential reference or `none` |
| `credentials.inject_into` | string[] | Which services receive credential env vars. Defaults to all services if omitted. |
| `network` | object | Named managed destinations |

### Multi-Service Process Model

All services in a config run as separate OS processes inside the **same sandbox container**. They share:

- The same filesystem (repo checkout + agent changes)
- The same `localhost` network namespace — services reach each other via `localhost:<port>`
- The same sandbox-level security controls (gVisor, capabilities, network restrictions)

The preview manager supervises all service processes as a lightweight process group. It does not use systemd, supervisord, or any external process manager — it manages child processes directly via Go's `os/exec`.

Service startup order:

1. Support services start first, in declaration order
2. The preview manager waits for each support service's readiness probe to pass before starting the next
3. The primary service starts last
4. The preview is `ready` only when **all** services pass their readiness probes

If any service fails to become ready within its timeout, the entire preview transitions to `failed` with a diagnostic indicating which service failed and why.

Service limits:

- Maximum **4 services** per config in MVP (1 primary + 3 support)
- Each service gets its own readiness probe and health check
- All services share the preview-level resource limits (memory, CPU) — there are no per-service cgroup splits in MVP

### Why Multi-Service In The Same Sandbox

The alternative — running each service in its own container with a shared Docker network — was considered and rejected for MVP because:

- It breaks the provider-agnostic transport goal (Docker networking ≠ E2B networking)
- It multiplies resource overhead (each container has its own cgroup, filesystem layer, and runtime)
- It adds orchestration complexity (dependency ordering, cross-container health checks, network namespace setup)
- `localhost` communication between processes in the same sandbox is simpler, faster, and matches how developers run things locally

Same-sandbox multi-process covers the most common case — monorepo with frontend + backend — without the infrastructure cost of container orchestration.

### Platform Infrastructure Services

Many full-stack apps need a database or cache to function. Not every org will have a staging RDS instance or managed Redis available as an external destination. To cover this gap, the preview system supports **platform-provided infrastructure services** — lightweight, ephemeral containers that 143 manages alongside the sandbox.

#### How It Works

1. The repo config declares infrastructure needs in the `infrastructure` field
2. When the preview starts, the preview manager provisions infrastructure containers **before** starting any application services
3. Infrastructure containers run on a **shared Docker network** with the sandbox — they are reachable via a hostname like `preview-db-{preview-id}` or `localhost` (via port mapping)
4. The preview manager auto-generates credentials (random username/password) and constructs connection strings using the `inject_env` templates
5. If an `init_script` is specified, the preview manager runs it against the infrastructure after the container is healthy but before starting application services
6. Infrastructure containers are torn down when the preview stops or expires

#### Available Templates

Platform infrastructure templates are maintained by 143 and versioned independently from user repos. MVP templates:

| Template | Image | Default Port | Notes |
|----------|-------|-------------|-------|
| `postgres-16` | `postgres:16-alpine` | 5432 | Auto-creates a database named `preview` |
| `postgres-15` | `postgres:15-alpine` | 5432 | Auto-creates a database named `preview` |
| `redis-7` | `redis:7-alpine` | 6379 | No auth by default |
| `mysql-8` | `mysql:8-lts` | 3306 | Auto-creates a database named `preview` |

Templates are not user-extensible in MVP. Custom infrastructure requires managed destinations pointing at external services.

#### Infrastructure vs Managed Destinations

The choice between infrastructure and managed destinations is context-dependent:

| | Platform Infrastructure | Managed Destination |
|---|---|---|
| **When to use** | No staging DB exists, or you want isolated ephemeral data per preview | A staging or shared dev DB already exists |
| **Data** | Ephemeral — seeded from `init_script` on each preview start, destroyed on stop | Persistent — shared across previews and potentially other environments |
| **Credentials** | Auto-generated by 143, injected at runtime | Admin-configured in 143 UI, scoped to credential sets |
| **Network** | Sidecar container on shared Docker network | External egress via managed destination allowlist |
| **Cost** | Additional memory/CPU per preview (see resource limits) | No additional per-preview resource cost |
| **Isolation** | Full isolation — each preview gets its own database instance | Shared — multiple previews may hit the same DB |

A config can use **both** simultaneously. For example, a config might use platform-provided PostgreSQL for data isolation while connecting to an external staging Stripe API via a managed destination.

#### Infrastructure Startup And Lifecycle

Infrastructure containers are started during the **Init** phase, before application services:

1. **Build**: prepare the sandbox filesystem (unchanged)
2. **Init**:
   a. Provision infrastructure containers from templates
   b. Wait for each infrastructure container to pass its health check (database accepting connections)
   c. Run `init_script` if specified
   d. Generate credentials and connection strings
   e. Inject credentials into the appropriate services via `inject_env` + `inject_into`
3. **Start**: launch application services in dependency order (unchanged)

If any infrastructure container fails to become healthy within 60 seconds, the preview transitions to `failed` with a diagnostic like: "PostgreSQL did not become ready within 60 seconds. This is likely a platform issue — try restarting the preview."

Infrastructure containers are health-checked independently of application services. If an infrastructure container crashes after the preview is `ready`, the preview transitions to `unhealthy` with a diagnostic indicating which infrastructure service failed.

#### Infrastructure Resource Limits

Infrastructure containers have their own resource limits, separate from the application services cgroup:

| Template | Default Memory | Default CPU | Max Memory |
|----------|---------------|-------------|------------|
| `postgres-*` | 256 MB | 0.25 cores | 512 MB |
| `redis-*` | 128 MB | 0.1 cores | 256 MB |
| `mysql-*` | 384 MB | 0.25 cores | 768 MB |

These limits are not configurable by users in MVP — they are set by the platform based on the template. Infrastructure resource consumption counts toward the node-level capacity, which affects the effective concurrency cap.

Maximum **2 infrastructure services** per config in MVP. Combined with the 4-service limit for application services, a preview can run up to 6 total processes/containers.

#### Security Model For Infrastructure

Platform infrastructure uses auto-generated, per-preview credentials. These are **not** stored in credential sets or managed by admins — they are ephemeral and exist only for the lifetime of the preview.

Security properties:

- Credentials are random and unique per preview instance
- Credentials are injected only at runtime, never in cacheable layers or committed config
- `inject_into` scopes which application services see the connection strings (same model as managed credentials)
- Infrastructure containers have no external network access — they are only reachable from the sandbox via the shared Docker network
- Infrastructure containers do not have access to the repo filesystem — they cannot read source code or agent changes
- Init scripts are copied from the repo into the infrastructure container as a one-time file transfer at init time. The preview manager reads the script from the sandbox filesystem and pipes it into the infrastructure container's client tool (e.g., `psql < seed.sql`). The infrastructure container does not get a volume mount to the repo — only the specific init script content is transferred

#### Trust Split For Infrastructure Config

Infrastructure config follows the same trust split as the rest of the preview config:

- **Base branch**: `infrastructure` block (template names, `inject_env`, `inject_into`)
- **Session diff**: `init_script` path (so the diff can change seed data to match the change under review)

A diff cannot add or remove infrastructure services, change templates, or modify injection targets. It can only change which init script runs.

For configs with `credentials.mode != none` (connected previews), `init_script` is also pinned to the base branch — the same "pin everything for connected previews" rule applies.

### Design Constraints

- `command` is an argv array, not a shell string
- readiness is HTTP-based, not stdout-regex-based
- secrets are not committed in the repo config
- `credentials` and `network` are config-level, not per-service, to keep the trust model simple
- `inject_into` is the only per-service credential scoping mechanism

The config model gives us a controlled way to support:

- `bootstrap` previews with no third-party credentials (single or multi-service)
- `staging_like` previews with managed non-production credentials scoped to specific services
- optional faster `lightweight` configs later

### Managed Credentials And Managed Destinations

Managed mode is the default.

- Repo config references a credential set name; it does not contain secrets
- Repo config references named destinations such as `preview_db` or `stripe_test`; it does not define raw egress rules in the common path
- Repo-scoped credentials are attached in the 143 UI by org admins
- Raw host allowlists or custom secret-fetch flows are an advanced fallback, not the default setup path

### Trust Split For Preview Config

Preview config is untrusted repo content. Not all fields should be read from the same revision.

- **Security-sensitive fields** come from the base branch version of `.143/preview.json` plus admin-managed settings
- **Runtime behavior fields** come from the session diff so the preview matches the change under review

For MVP, read these from the **base branch**:

- `credentials` (including `inject_into`)
- `network`
- `infrastructure` (template names, `inject_env`, `inject_into`)
- `primary` (which service the gateway proxies to)
- the set of service names in `services` (a diff cannot add or remove services)
- the set of infrastructure names in `infrastructure` (a diff cannot add or remove infrastructure)
- future secret-fetch or init hooks if they are introduced

For MVP, read these from the **session diff**:

- per-service `command`, `cwd`, `port`, `env`, and `ready`
- `infrastructure.*.init_script` (so the diff can change seed data to match the change under review)

This prevents a malicious or buggy diff from weakening egress or swapping in a more privileged credential set while still allowing the previewed app behavior to reflect the actual change.

### Preventing Diff-Controlled Launch In Connected Previews

The remaining risk is that a diff can still change the process that receives preview credentials if the preview config allows connected access.

**Hard requirement for MVP**: for any config with `credentials.mode != none` or non-empty managed destinations, all launch fields for **all services** (`command`, `cwd`, `port`, `env`, and `ready`) MUST be read from the **base branch** instead of the session diff. Only `restricted` / `bootstrap` previews may use diff-defined launch behavior. This is enforced in code with no admin override.

- `bootstrap` / `restricted`: allow diff-defined per-service `command`, `cwd`, `port`, `env`, and `ready`
- `staging_like` / any connected config: pin all launch fields for all services to base branch or an admin-approved template

This applies to **all services in the config**, not just the ones receiving credentials. Even though `inject_into` may scope credentials to the backend, a malicious diff could change the frontend's `command` to a process that reads the backend's environment from `/proc`. Since all services share a sandbox, the trust boundary is the config, not the individual service.

This is a hard rule, not a recommendation, because the alternative — allowing a diff to control any process in a sandbox that receives credentials — is a class of vulnerability, not a configuration choice. The preview manager must reject any attempt to start a connected preview where launch fields differ between the diff and base branch versions of the config.

## Backend Components

### 1. Preview Manager

A new service owns preview lifecycle:

- start preview (including multi-service startup orchestration)
- stop preview (all services in the config)
- report status (aggregate and per-service)
- mint bootstrap tokens
- enforce TTLs and concurrency caps

This is separate from HTTP handlers so the lifecycle logic does not leak into routers.

It is also responsible for:

- resolving the selected preview config and normalizing single-service configs to the multi-service format
- provisioning and tearing down platform infrastructure containers (PostgreSQL, Redis, etc.)
- generating ephemeral credentials for infrastructure services and constructing connection strings
- running init scripts against infrastructure containers
- managing the process group for all services in a config (start, stop, health check, restart)
- routing credentials (both managed and infrastructure-generated) to the correct services based on `inject_into`
- enforcing repo/org preview quotas
- recording node ownership even in single-node mode

For multi-service configs, the preview manager acts as a lightweight process supervisor. It holds references to all child processes, monitors their exit status, and coordinates ordered startup and shutdown. If a support service crashes, the preview manager should:

1. Transition the preview to `unhealthy` status
2. Surface which service failed in the UI
3. Allow the user to restart the entire preview (not individual services in MVP)

### 2. Provider-Agnostic Preview Transport

The API server should **not** resolve container IPs directly. That only works for same-host Docker.

Instead, add a preview-capable interface on the sandbox side:

```go
type PreviewCapableProvider interface {
    StartPreview(ctx context.Context, sb *Sandbox, cfg PreviewConfig) (*PreviewInstance, error)
    StopPreview(ctx context.Context, previewID string) error
    DialPreview(ctx context.Context, previewID string) (PreviewStream, error)
}
```

`StartPreview` handles the full lifecycle: provision infrastructure containers (if any), wait for infrastructure health, run init scripts, then start application services in dependency order. The `PreviewConfig` includes the full resolved service map and infrastructure declarations. The provider exposes a stream to the primary service's port.

`DialPreview` always connects to the **primary service's port**. Support services are never directly exposed to the gateway — they are only reachable from other processes inside the sandbox via `localhost`. This keeps the transport interface simple: one preview = one stream, regardless of how many services run inside.

`DialPreview` is intentionally abstract. In Docker it may attach to a worker-local port forward. In E2B it may use the provider's tunnel API. In a multi-node deployment it may proxy through the worker that owns the sandbox.

The API/gateway layer cares only that it can stream bytes for HTTP and WebSocket traffic. It should not know whether the preview is backed by a container IP, a VM tunnel, or another transport.

### 3. Preview Gateway

The preview gateway runs on the preview origin and does exactly three things:

1. Validate preview access
2. Proxy HTTP and WebSocket traffic
3. Inject / strip security-sensitive headers

It does **not** use the main app's session middleware. Access is established by the bootstrap token exchange.

### 4. Worker Routing

In `MODE=all`, the API server and preview-owning worker may be the same process.

In multi-node mode:

- the `preview_instances` record stores `worker_node_id`
- the gateway routes preview traffic to that worker over an authenticated internal hop
- the worker opens the provider-specific preview stream locally

This mirrors the general cluster model more closely than a direct container IP lookup.

### 5. Headless Browser (Preview Inspector)

A headless Chromium instance runs on the worker node (not inside the sandbox) and is used for two purposes:

1. **Agent-facing screenshot and DOM inspection** — the agent captures visual state and console errors from the preview to self-verify its work
2. **Human-facing Design Mode** — the reviewer clicks on elements in the preview and gives visual feedback that is translated into agent context

The headless browser connects to the preview through the same internal transport as the preview gateway (via `DialPreview`), so it sees exactly what a real browser would see. It runs outside the sandbox to keep the sandbox footprint small and to avoid giving untrusted preview code access to browser automation APIs.

```go
type PreviewInspector interface {
    // CaptureScreenshot takes a viewport screenshot of the preview at the given URL path.
    // Returns PNG bytes and page metadata (title, console errors, URL).
    CaptureScreenshot(ctx context.Context, previewID string, opts ScreenshotOpts) (*ScreenshotResult, error)

    // CaptureDOM returns a serialized snapshot of the DOM at the given URL path,
    // including computed styles for selected elements and the component tree if
    // source maps are available.
    CaptureDOM(ctx context.Context, previewID string, opts DOMCaptureOpts) (*DOMSnapshot, error)

    // ReadConsole returns buffered console messages (errors, warnings, logs)
    // captured since the last read or since the page was loaded.
    ReadConsole(ctx context.Context, previewID string) ([]ConsoleMessage, error)

    // InspectElement returns metadata about the DOM element at the given
    // coordinates: computed styles, component name (if React/Vue devtools
    // protocol is available), bounding box, and surrounding DOM context.
    InspectElement(ctx context.Context, previewID string, x, y int) (*ElementInfo, error)

    // StartScreencast begins recording frames from the preview at the given FPS.
    // Uses Chromium's Page.startScreencast CDP method.
    StartScreencast(ctx context.Context, previewID string, fps int) (screencastID string, err error)

    // StopScreencast ends recording and returns the assembled video/GIF.
    StopScreencast(ctx context.Context, screencastID string) (*ScreencastResult, error)

    // ExecuteInteraction runs a sequence of browser interactions (click, type,
    // navigate, wait) against the preview and returns the result of each step,
    // including screenshots captured at specified checkpoints.
    ExecuteInteraction(ctx context.Context, previewID string, steps []InteractionStep) (*InteractionResult, error)

    // CaptureMultiViewport takes simultaneous screenshots at multiple viewport
    // sizes (e.g., mobile, tablet, desktop) in a single call.
    CaptureMultiViewport(ctx context.Context, previewID string, opts MultiViewportOpts) (*MultiViewportResult, error)

    // ComputeVisualDiff compares two snapshots (before/after a code change) and
    // returns structured information about what changed visually and in the DOM.
    ComputeVisualDiff(ctx context.Context, previewID string, beforeSnapshotID, afterSnapshotID string) (*VisualDiff, error)
}

type ScreenshotOpts struct {
    Path       string // URL path to navigate to before capture, default "/"
    ViewportW  int    // default 1280
    ViewportH  int    // default 720
    FullPage   bool   // capture full scrollable page, default false
    Delay      time.Duration // wait after load before capture, default 1s
}

type ScreenshotResult struct {
    PNG           []byte
    PageTitle     string
    ConsoleErrors []ConsoleMessage
    URL           string
    CapturedAt    time.Time
}

type ScreencastResult struct {
    Format     string // "gif" or "webm"
    Data       []byte
    Duration   time.Duration
    FrameCount int
}

type InteractionStep struct {
    Action    string            // "click", "type", "navigate", "wait", "scroll", "select"
    Selector  string            // CSS selector for click/type/select targets
    Value     string            // text to type, URL to navigate to, option to select
    WaitFor   string            // CSS selector or "networkidle" or "load" — wait condition after action
    Timeout   time.Duration     // max wait for this step, default 10s
    Screenshot bool             // capture a screenshot after this step completes
}

type InteractionResult struct {
    Steps       []StepResult
    TotalTime   time.Duration
    FinalURL    string
    ConsoleErrors []ConsoleMessage
}

type StepResult struct {
    StepIndex   int
    Action      string
    Success     bool
    Error       string            // empty if success
    Screenshot  *ScreenshotResult // nil if Screenshot was false on the step
    Duration    time.Duration
    URL         string            // URL after step completed
}

type MultiViewportOpts struct {
    Path      string           // URL path to navigate to, default "/"
    Viewports []ViewportSpec   // list of viewports to capture
    Delay     time.Duration    // wait after load before capture, default 1s
}

type ViewportSpec struct {
    Name    string // e.g., "mobile", "tablet", "desktop"
    Width   int
    Height  int
}

type MultiViewportResult struct {
    Captures []ViewportCapture
}

type ViewportCapture struct {
    Viewport      ViewportSpec
    Screenshot    ScreenshotResult
    ConsoleErrors []ConsoleMessage
}

type VisualDiff struct {
    BeforeSnapshotID string
    AfterSnapshotID  string
    PixelDiffPercent float64              // percentage of pixels that changed
    DiffRegions      []DiffRegion         // bounding boxes of changed areas
    DOMChanges       []DOMChange          // structural DOM differences
    StyleChanges     []StyleChange        // computed style differences on matched elements
    OverlayPNG       []byte               // screenshot with changed regions highlighted in red
    Summary          string               // human-readable summary, e.g. "header height increased by 24px, nav items wrapped to second line"
}

type DiffRegion struct {
    BoundingBox Rect
    Severity    string // "minor" (<5% area change), "major" (>5%), "new" (element added), "removed"
}

type DOMChange struct {
    Selector   string
    ChangeType string // "added", "removed", "text_changed", "attribute_changed", "moved"
    Before     string // previous value (empty for "added")
    After      string // new value (empty for "removed")
}

type StyleChange struct {
    Selector string
    Property string
    Before   string
    After    string
    Token    string // design token name if applicable
}

type ElementInfo struct {
    TagName        string
    ComponentName  string            // React/Vue/Svelte component name
    ComponentFile  string            // source file path resolved via devtools hook + source maps
    ComponentLine  int               // line number in source file
    Props          map[string]any    // component props (React/Vue only, from devtools hook)
    ComponentTree  []string          // ancestor component chain, e.g. ["App", "Layout", "Header"]
    BoundingBox    Rect
    ComputedStyles map[string]string // key CSS properties
    DesignTokens   map[string]string // resolved tokens, e.g. {"background-color": "bg-blue-500", "color": "--color-primary"}
    InnerText      string            // truncated to 500 chars
    Attributes     map[string]string
    DOMPath        string            // CSS selector path to element
    ParentContext  string            // surrounding DOM structure (2 levels up)
}
```

#### Headless Browser Lifecycle

- The headless browser is **not** started when the preview starts. It is started **on demand** when the first screenshot or DOM inspection is requested.
- One headless browser instance is shared across all active previews on the same worker node. Each preview gets its own browser context (isolated cookies, storage) within the shared instance.
- The browser is shut down after 5 minutes of inactivity to free resources.
- The headless browser has no access to the main app session, managed credentials, or any state beyond what the preview gateway exposes.

#### Resource Overhead

| Resource | Per-Worker Overhead |
|----------|-------------------|
| Memory | ~150-250 MB for the shared Chromium instance |
| CPU | Minimal when idle; spikes during screenshot capture |
| Startup | ~2-3 seconds for first browser launch; <500ms for new browser context |

This overhead is per-worker, not per-preview. A worker running 3 previews uses one shared headless browser instance.

#### Component Resolver

DOM-level inspection (`elementFromPoint` returning a `<div>`) is not sufficient for meaningful Design Mode feedback. The agent needs to know that the `<div>` is a `<Header>` component defined in `src/components/Header.tsx:14` with props `{ title: "Dashboard", showNav: true }`.

The Preview Inspector achieves this by injecting a lightweight **component resolver script** (~2KB) into each preview page via the preview gateway. The script detects the framework in use and extracts component metadata:

| Framework | Detection | Hook Used | Data Extracted |
|-----------|-----------|-----------|---------------|
| React 16+ | `window.__REACT_DEVTOOLS_GLOBAL_HOOK__` | React fiber tree walker | Component name, props, source file (if available), parent component chain |
| Vue 3 | `window.__VUE_DEVTOOLS_GLOBAL_HOOK__` | Component instance tree | Component name, props, source file, parent chain |
| Vue 2 | `window.__VUE_DEVTOOLS_GLOBAL_HOOK__` | Legacy instance API | Component name, props (limited source info) |
| Svelte | `__svelte_meta` on elements | Element metadata | Component name, source file |
| Angular | `ng.getComponent()` | Debug API | Component class name, template file |
| None detected | — | Source map fallback | CSS selector path, computed styles only |

The resolver script registers a global function `__143_resolveElement(element)` that the headless browser calls after `elementFromPoint`. It returns:

```json
{
  "componentName": "Header",
  "componentFile": "src/components/Header.tsx",
  "componentLine": 14,
  "props": { "title": "Dashboard", "showNav": true },
  "componentTree": ["App", "DashboardLayout", "Header"],
  "framework": "react"
}
```

**Trust model**: The resolver script runs in the preview origin (untrusted). Its output is treated as **untrusted hints** — the headless browser validates that the reported component file exists in the repo and that the component name is reasonable (alphanumeric, < 100 chars). If validation fails, the field is omitted from `ElementInfo` and the system falls back to DOM-only information. The agent receives whatever metadata is available, and can use source maps as a secondary resolution path.

The resolver script is injected by the preview gateway as a `<script>` tag in every HTML response, similar to how the bootstrap script handles `postMessage` for activity heartbeats. It is framework-detection code only — it does not modify the page, intercept events, or send data anywhere. It only runs when called by the headless browser via `evaluateHandle`.

#### Design Token Awareness

When a reviewer adjusts a color or spacing value in Visual Editing, the agent should generate code using the project's design system tokens (e.g., `bg-blue-500` in Tailwind) rather than raw values (e.g., `background-color: #3b82f6`). This requires the preview system to understand what tokens exist in the project.

**Token extraction** happens during the **Build** phase and is cached per preview instance:

| Source | How Tokens Are Extracted | Token Format |
|--------|------------------------|-------------|
| Tailwind CSS | Parse `tailwind.config.js` / `tailwind.config.ts` to extract the theme (colors, spacing, typography, etc.) | `{ "bg-blue-500": "#3b82f6", "p-4": "1rem", ... }` |
| CSS custom properties | Scan CSS files for `--variable-name: value` declarations | `{ "--color-primary": "#3b82f6", "--spacing-lg": "2rem", ... }` |
| Theme files | Detect common patterns: `theme.js`, `tokens.json`, `design-tokens.yaml` | Parsed into `{ name: value }` map |

The extracted token map is stored in memory on the worker (not in the database) and provided to the Preview Inspector. When `InspectElement` resolves an element's computed styles, it reverse-maps values to tokens:

- The element has `background-color: rgb(59, 130, 246)` → matches Tailwind token `bg-blue-500`
- The element has `padding: 16px` → matches Tailwind token `p-4`

These mappings appear in `ElementInfo.DesignTokens` so the agent knows to write `className="bg-blue-500"` instead of `style={{ backgroundColor: '#3b82f6' }}`.

**Visual Editing controls** use the token map to present a **token picker** alongside raw value inputs:

- Color picker shows the project's color palette (extracted from Tailwind/CSS vars) as swatches
- Spacing controls show the project's spacing scale as preset options
- Typography controls show the project's font sizes / weights as named options

When the reviewer picks a token, the `visual_edit` message sent to the agent includes `{ property: "background-color", token: "bg-primary-500", rawValue: "#3b82f6" }` so the agent can use whichever representation the codebase prefers.

If no design tokens are detected, the controls fall back to raw value inputs only.

#### Screencast Recording

For agent verification flows that involve navigation (checking multiple pages, testing a form submission flow), a static screenshot timeline is insufficient. The Preview Inspector supports recording a lightweight screencast using Chromium's built-in `Page.startScreencast` CDP method.

**How it works:**
1. The agent calls `preview_screencast_start` with a desired FPS (default: 4)
2. The Preview Inspector begins capturing frames from the headless browser
3. The agent performs its verification (navigating pages, waiting for transitions)
4. The agent calls `preview_screencast_stop`
5. The Preview Inspector assembles frames into a GIF (for short recordings <10s) or WebM (for longer recordings)
6. The result is stored in blob storage and attached to the session as a `preview_screencast` event

**Constraints:**
- Maximum recording duration: 30 seconds
- Maximum FPS: 4 (sufficient for navigation verification, not video-quality)
- Maximum file size: 10 MB
- One active screencast per preview at a time
- Screencasts are stored with the same retention policy as screenshots (session lifetime + 24h)

**Use cases:**
- Agent navigates from login → dashboard → settings to verify a change doesn't break other pages
- Agent fills out a form and verifies the success state
- Agent tests a responsive layout by resizing the viewport mid-recording

The reviewer sees screencasts in the session timeline alongside static screenshots. They play inline (no external player needed) and provide significantly richer context than a series of static images.

### 6. Startup Phases

Even in MVP, preview startup should be modeled as three phases:

1. **Build**: prepare or reuse the runnable filesystem/image state (shared across all services — one repo checkout)
2. **Init**: provision infrastructure containers (if any), wait for infrastructure health, run init scripts, generate and inject credentials (both managed and infrastructure-generated) into the appropriate services per `inject_into`
3. **Start**: launch application services in dependency order (support services first, then primary) and wait for all readiness probes to pass

The Init phase is where infrastructure and credential setup happens. This ensures that by the time application services start, all databases are populated and all connection strings are injected.

For multi-service configs, the Start phase emits per-service status events so the frontend can show which services are ready and which are still starting. The preview transitions to `ready` only when all services and infrastructure pass their readiness/health checks. If any service or infrastructure container fails, the preview transitions to `failed` with a diagnostic identifying the failing component.

Phase 1 does not need the full immutable image cache pipeline yet, but the lifecycle should already distinguish these phases so later caching and diagnostics do not require redesigning the contract.

### 7. Fast Startup

Preview startup time is the single biggest UX bottleneck. A typical first-time preview for a React + Express app takes 30-90 seconds (npm install + build + start). For the PR review workflow, where a reviewer clicks "Launch Preview" and expects to see something quickly, this delay is unacceptable. The system uses three strategies to minimize startup time.

#### Filesystem Snapshot Caching

After a successful preview startup (all three phases complete), the system takes a **filesystem snapshot** of the sandbox state — node_modules installed, build artifacts ready, infrastructure initialized. The snapshot is keyed by:

```
snapshot_key = hash(lockfile_contents + base_commit + preview_config_hash)
```

On subsequent preview starts for the same repo with the same dependencies:

1. The preview manager checks for a matching snapshot
2. If found, restores the filesystem from snapshot (skipping the Build phase entirely)
3. Runs only Init (if infrastructure is needed) and Start phases
4. **Result**: startup drops from 30-90 seconds to 5-15 seconds

Snapshots are stored on the worker's local disk (SSD) with an LRU eviction policy. Default cache size: 20 GB per worker, with each snapshot typically 200 MB - 1 GB depending on node_modules size.

**Invalidation**: snapshots are invalidated when:
- The lockfile changes (new dependencies)
- The base commit changes (new code) — but see "partial invalidation" below
- The preview config changes (different services or settings)

**Partial invalidation**: when only the base commit changes but the lockfile is the same, the system can restore the snapshot and apply only the new file changes (git diff) on top. This handles the common case where a PR is rebased or new commits are pushed without changing dependencies.

#### Progressive Preview

For multi-service configs, the frontend service often starts faster than the backend. Rather than waiting for all services to be ready, the system supports **progressive preview**:

1. As soon as the primary service (frontend) passes its readiness probe, the preview transitions to `partially_ready` and the frontend displays the preview iframe
2. The UI shows a toast overlay: "Backend starting... (API calls may fail until ready)"
3. When all services pass readiness, the preview transitions to `ready` and the toast disappears
4. If the frontend depends on backend data, it will show its own loading/error states naturally — this is actually useful for the agent to see, as it reveals how the app handles backend unavailability

Progressive preview is opt-in per config via a `progressive: true` flag. It is most useful for SPAs with client-side routing that can render a shell before API data is available.

#### Startup Time Targets

| Scenario | Target | Strategy |
|----------|--------|----------|
| First preview for repo (cold) | < 90 seconds | Warm sandbox pool (skip container start) |
| Repeat preview, same dependencies | < 15 seconds | Filesystem snapshot restore + Start only |
| Re-launch after idle timeout (same session) | < 10 seconds | Snapshot restore + Start only (Init skipped if infra containers still running) |
| Progressive preview (frontend ready) | < 30 seconds (cold), < 10 seconds (cached) | Show frontend as soon as primary service ready |

## Data Model

Use a dedicated `preview_instances` table rather than extra columns on `sessions`.

Suggested fields:

| Column | Type | Notes |
|-------|------|-------|
| `id` | uuid | PK |
| `session_id` | uuid | FK -> sessions |
| `org_id` | uuid | tenant scope |
| `profile_name` | text | `bootstrap`, `staging_like`, etc. |
| `name` | text | usually `frontend` in MVP |
| `status` | text | `starting`, `ready`, `stopped`, `failed`, `expired` |
| `provider` | text | `docker`, `e2b`, etc. |
| `worker_node_id` | text | node that owns the preview |
| `preview_handle` | text | provider-specific opaque handle |
| `primary_service` | text | name of the primary service the gateway proxies to |
| `port` | int | primary service's container-local port snapshot |
| `config_digest` | text | snapshot of the resolved preview config |
| `base_commit_sha` | text | exact base commit for the session |
| `last_accessed_at` | timestamptz | idle timeout enforcement |
| `expires_at` | timestamptz | hard TTL |
| `stopped_at` | timestamptz | explicit stop time |
| `last_path` | text | last proxied request path for navigation restore on restart |
| `memory_limit_mb` | int | resolved memory limit for the preview process |
| `cpu_limit_millis` | int | resolved CPU limit for the preview process |
| `error` | text | startup/runtime failure |
| `created_at` | timestamptz | |

Indexes:

- `(org_id, session_id, created_at DESC)` for session lookups
- `(worker_node_id, status)` for cleanup and routing
- unique partial index on active preview per session for MVP

This keeps preview lifecycle state out of the main `sessions` table.

### Preview Services

For multi-service configs, use a `preview_services` table to track per-service state:

| Column | Type | Notes |
|-------|------|-------|
| `id` | uuid | PK |
| `preview_instance_id` | uuid | FK -> preview_instances |
| `service_name` | text | key from the `services` map (e.g., `frontend`, `backend`) |
| `role` | text | `primary` or `support` |
| `status` | text | `starting`, `ready`, `stopped`, `failed` |
| `command` | text[] | resolved command argv |
| `cwd` | text | resolved working directory |
| `port` | int | port this service binds to |
| `pid` | int | OS process ID inside the sandbox (nullable, for diagnostics) |
| `error` | text | per-service failure message |
| `created_at` | timestamptz | |

Index: `(preview_instance_id, service_name)` unique.

For single-service configs (normalized internally), this table contains one row. The preview manager uses this table to track which services are running, which have failed, and to produce per-service diagnostics in the UI.

The `preview_instances.status` field reflects the **aggregate** state: `ready` only when all services and infrastructure are ready, `failed` if any service or infrastructure fails, `unhealthy` if any becomes unhealthy after initial readiness.

### Preview Infrastructure

For configs that use platform infrastructure, use a `preview_infrastructure` table:

| Column | Type | Notes |
|-------|------|-------|
| `id` | uuid | PK |
| `preview_instance_id` | uuid | FK -> preview_instances |
| `infra_name` | text | key from the `infrastructure` map (e.g., `db`, `cache`) |
| `template` | text | resolved template name (e.g., `postgres-16`) |
| `container_id` | text | Docker container ID |
| `status` | text | `provisioning`, `healthy`, `unhealthy`, `stopped`, `failed` |
| `host` | text | hostname or IP on the shared Docker network |
| `port` | int | port the infrastructure service is listening on |
| `credentials_hash` | text | hash of the auto-generated credentials (for audit, not the actual values) |
| `error` | text | failure message |
| `created_at` | timestamptz | |

Index: `(preview_instance_id, infra_name)` unique.

Infrastructure credentials are stored in memory only — they are generated at preview start, injected into application service environments, and discarded when the preview stops. The `credentials_hash` field exists for audit purposes only (verifying that credentials were generated and injected).

### Preview Snapshots

The screenshot timeline is backed by a `preview_snapshots` table:

| Column | Type | Notes |
|-------|------|-------|
| `id` | uuid | PK |
| `preview_instance_id` | uuid | FK -> preview_instances |
| `trigger` | text | `baseline`, `agent_change`, `agent_explicit`, `user_request`, `design_mode` |
| `url_path` | text | page URL path at time of capture |
| `blob_ref` | text | reference to PNG in blob storage (S3 key) |
| `viewport_width` | int | viewport width used for capture |
| `viewport_height` | int | viewport height used for capture |
| `console_errors` | jsonb | console errors present at capture time |
| `file_changes` | jsonb | files modified since the previous snapshot (nullable) |
| `created_at` | timestamptz | |

Index: `(preview_instance_id, created_at)` for timeline queries.

Snapshots are retained for the session lifetime + 24 hours. Maximum 50 per preview instance. PNG data is stored in blob storage, not in PostgreSQL.

### Preview Logs

Preview lifecycle logs should live in a dedicated `preview_logs` table keyed by `preview_instance_id`, with:

- `level`
- `step` (`build`, `init`, `start`, `proxy`, `cleanup`)
- `message`
- `metadata`
- `created_at`

These logs are separate from agent logs. They exist to explain why preview setup failed or why a running preview expired or restarted.

### Secret Handling For Logs

Preview logging needs two tiers:

1. **Lifecycle logs**: structured events emitted by the preview manager and gateway. These are the default logs surfaced in the UI and stored in `preview_logs`.
2. **Process output**: raw stdout/stderr from the previewed app or setup commands. This is higher risk and must not be treated like normal lifecycle logs.

MVP policy:

- `preview_logs` never store environment values, full command lines with inline secrets, request headers, or raw credential material
- injected secret values are registered with the preview runtime and redacted before any line is persisted or streamed
- the redactor should match exact secret values plus common encodings of those values such as URL-encoded and base64 variants
- if a line still appears high-risk after redaction, store a fixed message like `"[redacted high-risk process output]"` instead of the original text
- raw process output for connected previews is not shown to `viewer`
- raw process output for connected previews is either:
  - not persisted at all, only streamed ephemerally to `member` / `admin` while the preview is active, or
  - stored encrypted with a short retention window and restricted to `member` / `admin`

Recommended MVP choice: persist only redacted lifecycle logs for everyone, and make raw process output ephemeral plus `member` / `admin` only.

### Preview Access Sessions

If the bootstrap exchange uses DB-backed preview sessions, use a dedicated `preview_access_sessions` table rather than overloading app sessions.

Suggested fields:

- `id`
- `org_id`
- `user_id`
- `preview_instance_id`
- `session_token_hash`
- `issued_at`
- `expires_at`
- `revoked_at`
- `last_accessed_at`
- `created_at`

The preview access session must be bound to exactly one preview instance and must be revoked when the preview stops or expires.

Use a host-only cookie such as `__Host-preview_session` on each per-preview hostname so preview access state cannot be reused across preview hostnames.

### Preview Startup Cache

To support fast startup via filesystem snapshots, track cached snapshots:

- `id`
- `org_id`
- `repo_id`
- `snapshot_key` (hash of lockfile + base commit + preview config)
- `blob_path` (reference to snapshot in local disk or blob storage)
- `size_bytes`
- `created_at`
- `last_used_at`
- `worker_node_id` (which worker has this snapshot on local disk)

This table is used by the preview manager to find a matching snapshot before starting the Build phase. Snapshots are evicted LRU when the worker's cache exceeds the configured limit (default 20 GB).

### PR Preview State

To support the PR comment lifecycle without keeping previews running, track PR preview state:

- `id`
- `org_id`
- `repo_id`
- `pr_number`
- `github_comment_id` (the PR comment to update in place)
- `last_preview_instance_id` (most recent preview for this PR)
- `last_screenshot_blob_path` (thumbnail for the PR comment)
- `last_visual_diff_blob_path` (overlay image, if computed)
- `base_snapshot_key` (cached base branch snapshot for visual diff)
- `status` (`never_started`, `running`, `stopped`, `merged`, `closed`)
- `created_at`
- `updated_at`

This row is created when a PR is opened for a session that has preview configured. It is updated whenever a preview starts, stops, or produces new artifacts. The `github_comment_id` is set after the first PR comment is posted and used for subsequent updates.

## API

All preview endpoints stay under `/api/v1/` and use the standard response envelope.

```
POST   /api/v1/sessions/{id}/preview
GET    /api/v1/sessions/{id}/preview
DELETE /api/v1/sessions/{id}/preview
POST   /api/v1/sessions/{id}/preview/restart
GET    /api/v1/sessions/{id}/preview/logs
GET    /api/v1/sessions/{id}/preview/services
POST   /api/v1/sessions/{id}/preview/screenshot
POST   /api/v1/sessions/{id}/preview/inspect
GET    /api/v1/sessions/{id}/preview/console
GET    /api/v1/sessions/{id}/preview/snapshots
POST   /api/v1/sessions/{id}/preview/design-feedback
POST   /api/v1/sessions/{id}/preview/interact
POST   /api/v1/sessions/{id}/preview/multi-viewport
POST   /api/v1/sessions/{id}/preview/visual-diff
POST   /api/v1/sessions/{id}/preview/assert
GET    /api/v1/repos/{owner}/{repo}/preview/detect
```

The `GET .../preview` response includes the aggregate preview status and a `services` array with per-service status, port, and role. The `GET .../preview/services` endpoint returns detailed per-service state including individual health status, last error, and process uptime. For single-service previews, the `services` array contains one entry.

The visual interaction endpoints:
- `POST .../preview/screenshot` — capture a screenshot at a given URL path and viewport size. Used by agents via tools and by the frontend for Design Mode. Returns PNG bytes and page metadata.
- `POST .../preview/inspect` — given `(x, y)` coordinates relative to the preview viewport, returns `ElementInfo` for the DOM element at that point. Used by Design Mode when the reviewer clicks on an element.
- `GET .../preview/console` — returns buffered console messages since last read. Used by agents after making changes.
- `GET .../preview/snapshots` — returns the screenshot timeline for the preview instance, including metadata and blob URLs.
- `POST .../preview/design-feedback` — submits a Design Mode feedback message (selected elements, annotations, instruction text, screenshot) to the agent's context.

The verification endpoints:
- `POST .../preview/interact` — execute a sequence of browser interaction steps (click, type, navigate, wait) with optional checkpoint screenshots. Used by agents to verify multi-step flows. Accepts an array of `InteractionStep` objects; returns per-step results.
- `POST .../preview/multi-viewport` — capture screenshots at multiple viewport sizes in a single call. Accepts an array of viewport specs; returns an array of `ViewportCapture` objects. Default viewports: mobile (375px), tablet (768px), desktop (1280px).
- `POST .../preview/visual-diff` — compare two snapshot IDs and return a `VisualDiff` with pixel diff percentage, DOM changes, style changes, and an overlay image highlighting changed regions.
- `POST .../preview/assert` — run an array of visual assertions against the current preview state. Returns structured pass/fail results per assertion.

### RBAC

| Action | Role |
|------|------|
| View preview status, logs, and screenshot timeline | `viewer` |
| Start or resume a `bootstrap` / `restricted` preview | `member` |
| Start or resume a connected preview (`staging_like` or similar) | `member` |
| Stop or restart a preview | `member` |
| View raw process output for connected previews | `member` |
| Capture screenshots and read console output | `member` |
| Use Design Mode (click elements, annotate, send feedback) | `member` |
| Use Visual Editing controls | `member` |
| Run interaction replay, multi-viewport capture, visual diff, and assertions | `member` |
| Configure preview configs, credentials, quotas, and defaults | `admin` |

In the current role model, `member` is the editor-equivalent role. Starting a preview is treated as an editor action because it causes sandbox execution and may initiate connected preview flows.

## Frontend UX

The session page on `app.143.dev` renders:

- `Start Preview` / `Stop Preview`
- Preview status (with per-service breakdown for multi-service configs)
- Responsive width presets
- `Open in new tab`
- **Design Mode toggle** — switches between interact mode (normal iframe) and design mode (overlay captures clicks)
- **Screenshot timeline** — scrollable strip of snapshots below the preview, showing visual evolution
- **Console errors badge** — count of unread console errors, click to expand

The iframe `src` points at the preview origin, not the app origin.

`Open in new tab` is allowed only because the preview uses an isolated origin. The earlier same-origin version of this feature was not safe.

### Startup Progress

Preview startup takes 10-90 seconds. A spinner with no context will feel broken. The UI should stream phase-level progress during startup:

1. Show the current phase: **Building** → **Initializing** → **Starting**
2. Stream the last few lines of build/init output in a collapsible terminal view below the progress indicator
3. Show estimated time remaining based on historical startup times for the same repo and config. Format as "Usually ready in ~25s" rather than a countdown, since estimates are approximate.
4. If no historical data exists yet, show an indeterminate progress bar with the phase label only

The preview manager should emit structured phase-transition events that the frontend consumes via the existing session WebSocket channel. These events are separate from `preview_logs` rows — they are ephemeral UI signals, not persisted records.

### Failure Diagnostics

When preview enters `failed` status, the UI should show:

1. **Which phase failed** — Build, Init, or Start — as a prominent label
2. **The last 20 lines of process output** from the failed phase, redacted per the log policy in the Secret Handling For Logs section
3. **A suggested fix** when the failure matches a known pattern

Known failure patterns the preview manager should detect and surface:

| Pattern | Suggested Fix |
|---------|--------------|
| Port not reachable after timeout | "The dev server did not respond on port {port}. Check that your dev server binds to `0.0.0.0`, not `localhost`. You can set `HOST=0.0.0.0` in the preview config env." |
| `EADDRINUSE` in process output | "Port {port} is already in use inside the sandbox. Check for conflicting processes or change the port in `.143/preview.json`." |
| `MODULE_NOT_FOUND` or `Cannot find module` | "A required dependency is missing. Ensure `npm install` or equivalent runs during the Build phase." |
| OOM kill (exit code 137) | "The preview process exceeded its memory limit ({limit}MB). Try a lighter dev server configuration or request a higher limit." |
| Non-zero exit within 5 seconds of start | "The dev server exited immediately. Check the process output below for configuration errors." |
| `ECONNREFUSED` on a support service port | "The {service} service is not responding on port {port}. It may have crashed or failed to start. Check the {service} process output." |
| Support service ready but primary fails to connect | "The frontend cannot reach the backend at localhost:{port}. Ensure the backend service is configured to bind to `0.0.0.0`." |
| Infrastructure container fails to start | "PostgreSQL failed to start. This is likely a platform issue — try restarting the preview." |
| Init script fails (`psql` exit code non-zero) | "Database seed script `{init_script}` failed. Check the script for syntax errors or missing tables." |
| `ECONNREFUSED` on infrastructure port during app startup | "The {service} service cannot connect to {infra_name} at {host}:{port}. The database may still be initializing — try restarting." |

This pattern list should be maintained as a registry in the preview manager, not hardcoded in the frontend, so new patterns can be added without frontend deploys.

### Activity-Aware Timeouts

Static idle timeouts cause the most common UX complaint: a reviewer reads code for a few minutes, switches back to the preview, and it is gone.

Replace the static 5-minute idle timeout with an **activity-aware timer**:

1. The preview gateway tracks the timestamp of the last meaningful traffic, defined as any HTTP request or WebSocket data frame (excluding ping/pong keepalives)
2. The frontend injects a lightweight visibility observer via `postMessage` from an injected bootstrap script on the preview origin. When the iframe becomes visible (Page Visibility API), the frontend sends a heartbeat to the app origin, which forwards an activity ping to the preview manager
3. The idle timeout resets on either gateway traffic or visibility heartbeats
4. Default idle timeout: **15 minutes** of no activity (up from 5 minutes)

For the hard TTL:

- Default: 30 minutes
- If the gateway has seen activity in the last 5 minutes when TTL would expire, auto-extend by 30 minutes
- Maximum extended TTL: **2 hours**
- The UI should show a subtle "Preview expires in 5 min" warning at the 25-minute mark, with a one-click "Extend" button

This ensures active reviews are never interrupted while still reclaiming resources from truly abandoned previews.

### Hot Reload After Agent Changes

When the agent writes files inside the sandbox, those writes land on the same filesystem the dev server watches. Most modern dev servers (Vite, Next.js, webpack) use `inotify` / `fsevents` and will detect changes automatically.

However, two cases need explicit handling:

1. **Provider-specific file delivery**: if the sandbox provider delivers agent changes via a mechanism that does not trigger filesystem watch events (e.g., direct block-level snapshot restore), the preview manager must emit a synthetic `touch` on changed files after delivery to wake the file watcher
2. **Full rebuild required**: some changes (new dependencies in `package.json`, config file changes) require a restart rather than HMR. The preview manager should detect changes to known restart-trigger files and prompt the user: "Dependencies changed — Restart preview to apply?"

The design does not support auto-restarting on file changes in MVP. The user must explicitly restart if HMR is insufficient.

### Agent Visual Feedback Loop

The agent should be able to see and react to the running preview. Without this, the agent writes code and hopes it looks right — the human reviewer is the only one who catches visual bugs. With visual feedback, the agent can self-verify its work before the reviewer sees it.

#### Agent Preview Tools

The preview system exposes tools to the agent via the standard agent tool interface. These tools use the headless browser (Preview Inspector) running on the worker node.

| Tool | Description | When Used |
|------|-------------|-----------|
| `preview_screenshot` | Capture a viewport screenshot at a given URL path | After making changes, to verify the visual result |
| `preview_screenshot_full` | Capture a full-page screenshot | For pages with below-fold content |
| `preview_console` | Read console errors and warnings | After changes, to catch runtime errors the agent introduced |
| `preview_element` | Inspect a specific element by CSS selector | When the agent needs to verify a specific component's styles or content |
| `preview_accessibility` | Run basic accessibility checks (color contrast, missing alt text, ARIA) | After UI changes, to catch a11y regressions |
| `preview_screencast_start` | Begin recording a screencast at 2-4 FPS | Before multi-page verification flows |
| `preview_screencast_stop` | Stop recording and return the assembled GIF/WebM | After completing the verification flow |
| `preview_interact` | Execute a sequence of browser interactions (click, type, navigate, wait) with optional checkpoint screenshots | To verify multi-step flows like form submission, navigation, or login |
| `preview_multi_viewport` | Capture simultaneous screenshots at mobile (375px), tablet (768px), and desktop (1440px) viewports | After layout changes, to catch responsive design regressions |
| `preview_visual_diff` | Compare two snapshots and return structured information about pixel changes, DOM changes, and style changes | After making a change, to understand exactly what the code change affected visually |
| `preview_assert` | Run a set of visual assertions against the current preview state and return pass/fail results | After changes, to self-verify that the result matches expectations |

The agent gets **read-only observation tools** (screenshots, console, DOM inspection) plus **limited interaction tools** (`preview_interact`) for verification purposes. Interaction tools can click, type, and navigate, but only to verify that the app works correctly — the agent's primary mode of operation is writing code, not manipulating the UI directly. The interaction tools exist so the agent can test flows that require user input (form submission, login, navigation) without requiring a human to manually verify them.

#### Self-Verification Flow

When the preview is running and the agent makes a code change, the recommended agent flow is:

1. Agent writes code changes to the sandbox filesystem
2. HMR or file watcher picks up the changes and the dev server updates
3. Agent waits a brief stabilization period (1-2 seconds after the last HMR WebSocket message)
4. Agent calls `preview_screenshot` to capture the current state
5. Agent calls `preview_visual_diff` to compare the new state against the previous snapshot — this tells the agent exactly what changed (pixel regions, DOM mutations, style shifts) so it can verify that the change matches intent and catch unintended side effects
6. Agent calls `preview_assert` to run structured assertions (see below)
7. Agent evaluates the screenshot, diff, and assertion results against the user's request
8. If the result doesn't match expectations, the agent iterates (back to step 1)
9. Once satisfied, the agent presents the final screenshot to the user alongside the diff

The screenshot is included in the agent's context as an image, so the agent can reason about layout, colors, typography, spacing, and visual hierarchy. The visual diff and assertion results provide structured data that complements the visual reasoning.

#### Self-Verification Assertions

The agent can define and run **structured assertions** against the preview state using `preview_assert`. Assertions are ephemeral — they are not persisted as test files, but rather used by the agent within the current session to verify its own work.

Assertion types:

| Type | What It Checks | Example |
|------|---------------|---------|
| `element_exists` | A CSS selector matches at least one element | `{ "selector": ".checkout-button", "visible": true }` |
| `element_text` | An element's text content matches (exact or contains) | `{ "selector": "h1", "contains": "Dashboard" }` |
| `element_style` | An element's computed style matches | `{ "selector": ".header", "property": "background-color", "value": "#3b82f6" }` |
| `element_count` | The number of elements matching a selector | `{ "selector": ".card", "min": 3, "max": 10 }` |
| `no_console_errors` | No new console errors since last check | `{}` |
| `page_title` | The page title matches | `{ "contains": "Settings" }` |
| `viewport_screenshot_match` | A region of the screenshot matches expectations (described in natural language — the agent evaluates this) | `{ "region": { "x": 0, "y": 0, "w": 1280, "h": 80 }, "description": "blue header with white text and logo on the left" }` |

The agent composes assertions based on what the user requested. For example, if the user says "add a red delete button to the card footer," the agent would assert:

```json
[
  { "type": "element_exists", "selector": ".card-footer .delete-button", "visible": true },
  { "type": "element_style", "selector": ".card-footer .delete-button", "property": "background-color", "value": "rgb(239, 68, 68)" },
  { "type": "element_text", "selector": ".card-footer .delete-button", "contains": "Delete" },
  { "type": "no_console_errors" }
]
```

The assertion results are structured pass/fail, so the agent can programmatically decide whether to iterate or present the result:

```json
{
  "passed": 3,
  "failed": 1,
  "results": [
    { "type": "element_exists", "passed": true },
    { "type": "element_style", "passed": true },
    { "type": "element_text", "passed": true },
    { "type": "no_console_errors", "passed": false, "detail": "TypeError: Cannot read property 'map' of undefined at CardFooter.tsx:22" }
  ]
}
```

This transforms the agent from "make a change and hope it looks right" to "make a change, define what success looks like, and verify it." The assertion layer catches both visual issues and runtime errors in a single structured step.

#### Console Error Detection

After each code change, the agent should call `preview_console` to check for new errors. If the change introduced console errors (especially `TypeError`, `ReferenceError`, or React/Vue rendering errors), the agent should attempt to fix them before presenting the result. This catches a common class of bugs where the page appears to render but has runtime errors.

#### Automatic Post-Change Screenshot

The preview manager can optionally capture a screenshot automatically after detecting an HMR update or file change. This is enabled per-session and works as follows:

1. The preview gateway detects an HMR WebSocket message indicating a module update
2. After a 2-second stabilization delay, the preview manager calls `CaptureScreenshot` via the Preview Inspector
3. The screenshot and any new console errors are attached to the session as a `preview_snapshot` event
4. The agent receives the snapshot in its context if it is currently active
5. The reviewer can see a timeline of snapshots in the session UI, showing how the preview evolved as the agent made changes

This creates an automatic visual audit trail without requiring the agent to explicitly request screenshots.

### Design Mode (Visual Feedback From Reviewer)

Design Mode lets the reviewer interact with the preview visually and pass precise, element-level feedback to the agent. Instead of typing "make the header bigger," the reviewer clicks on the header element and types "make this bigger" — the agent receives the element context (component name, CSS selector, computed styles, bounding box) alongside the instruction.

This is the reviewer-facing counterpart to the agent's visual feedback tools. Together they form a complete visual loop: the agent sees what it built, the reviewer points at what needs to change, and the agent receives both visual and structural context for the fix.

#### How Design Mode Works

Design Mode runs entirely in the **app origin** (`app.143.dev`), not in the preview iframe. It uses a transparent overlay on top of the preview iframe to capture click and annotation events, then uses the Preview Inspector's server-side headless browser to resolve what the user clicked on.

```
┌─────────────────────────────────────────────────────────────────┐
│ app.143.dev (Session Page)                                       │
│                                                                   │
│  ┌─────────────────────────────────────────────────────────────┐ │
│  │ Design Mode Overlay (transparent, captures clicks)          │ │
│  │                                                              │ │
│  │  ┌───────────────────────────────────────────────────────┐  │ │
│  │  │ Preview iframe (<preview-id>.preview.143.dev)         │  │ │
│  │  │                                                        │  │ │
│  │  │  [rendered preview content]                            │  │ │
│  │  │                                                        │  │ │
│  │  └───────────────────────────────────────────────────────┘  │ │
│  │                                                              │ │
│  │  ┌─────────────┐  ┌──────────────────────────────────────┐  │ │
│  │  │ [x] Element │  │ Describe your change...         [Send]│  │ │
│  │  │  .header    │  │                                       │  │ │
│  │  └─────────────┘  └──────────────────────────────────────┘  │ │
│  └─────────────────────────────────────────────────────────────┘ │
│                                                                   │
└─────────────────────────────────────────────────────────────────┘
```

Step by step:

1. The reviewer toggles Design Mode via a button in the session UI
2. A transparent overlay appears on top of the preview iframe, intercepting pointer events
3. When the reviewer clicks on a point in the overlay, the frontend sends the `(x, y)` coordinates to the API server
4. The API server calls `InspectElement(previewID, x, y)` on the Preview Inspector, which uses the headless browser to resolve the click coordinates to a DOM element
5. The Preview Inspector returns `ElementInfo`: component name, CSS selector path, computed styles, bounding box, inner text, and surrounding DOM context
6. The overlay highlights the selected element (draws a border based on the bounding box) and shows a floating panel with the element name and a text input
7. The reviewer types their instruction (e.g., "make this bigger", "change the color to blue", "add padding")
8. The instruction is sent to the agent as a structured `design_mode_feedback` message containing:
   - The screenshot at the moment of selection (with the element highlighted)
   - The `ElementInfo` (component name, CSS selector, computed styles)
   - The user's natural language instruction
   - The file path and line number of the component (if source maps are available)
9. The agent receives this as rich context and can make targeted code changes

#### Why The Overlay Instead of In-Iframe Interaction

The preview iframe runs on an isolated origin and is sandboxed without `allow-same-origin`. This means the app origin **cannot** inject JavaScript into the preview iframe or read its DOM directly. This is a hard security constraint — we cannot relax it for Design Mode.

The solution is a two-layer approach:

- **Client-side (overlay)**: captures click coordinates relative to the iframe viewport. This requires no cross-origin access — it only needs to know where the user clicked on the overlay.
- **Server-side (headless browser)**: resolves those coordinates to DOM elements. The headless browser loads the same page the user sees and performs `document.elementFromPoint(x, y)` in a privileged context that has full DOM access.

This keeps the security boundary intact while still providing element-level precision.

#### Annotations

Beyond clicking single elements, the reviewer can draw annotations on the overlay:

- **Rectangles**: to indicate a region ("rearrange items in this area")
- **Arrows**: to indicate relationships ("move this next to that")
- **Freehand**: to circle or underline ("this text is wrong")

Annotations are captured as SVG paths relative to the iframe viewport. They are rendered onto the screenshot image before being sent to the agent, so the agent sees a single annotated screenshot.

#### Multi-Element Selection

The reviewer can select multiple elements before sending feedback:

1. Click element A → highlighted
2. Shift-click element B → both highlighted
3. Type instruction: "swap the positions of these two"
4. The agent receives both `ElementInfo` objects and the instruction

#### Element Reordering

When the reviewer selects an element in Design Mode that is a child of a flex or grid container, the overlay shows **directional move controls** (up/down/left/right arrows) alongside the element highlight. Clicking an arrow sends a structured `reorder` message to the agent:

```json
{
  "type": "reorder",
  "element": { /* ElementInfo */ },
  "parent": { /* ElementInfo of the container */ },
  "direction": "up",
  "siblings": ["NavLink:Home", "NavLink:Dashboard", "NavLink:Settings"]
}
```

The agent receives enough context to find the component in source and reorder the JSX/HTML children. The sibling list (with component names) helps the agent understand the current order without needing to read the full file first.

This covers the 80% case of layout reordering (list items, nav links, grid children, card order) without the complexity of full freeform drag-and-drop, which requires solving coordinate mapping between the overlay and the actual DOM layout engine. Full drag-and-drop is deferred to a later phase.

#### Design Mode Constraints

- Design Mode requires the preview to be in `ready` status
- Design Mode is available to `member` role (same as starting a preview)
- The overlay intercepts pointer events, so the reviewer cannot interact with the preview while Design Mode is active. There is a toggle to switch between "interact" mode (normal iframe interaction) and "design" mode (overlay captures clicks)
- Design Mode uses the same headless browser instance as the agent's screenshot tools — no additional resource overhead

### Visual Editing (Style Tweaks)

For simple visual changes (colors, spacing, typography, layout), the reviewer can make edits directly in the Design Mode overlay without writing a natural language instruction. This creates a fast feedback loop for visual polish.

#### How Visual Editing Works

When the reviewer selects an element in Design Mode, the floating panel shows the element's key computed styles alongside interactive controls:

| Control | What It Edits | UI |
|---------|--------------|-----|
| Color picker | `color`, `background-color`, `border-color` | Native color input + project token swatches (from Tailwind/CSS vars if detected) |
| Spacing sliders | `margin`, `padding` (per-side) | Four-directional slider + project spacing scale presets |
| Typography | `font-size`, `font-weight`, `line-height`, `letter-spacing` | Numeric inputs + project typography presets |
| Layout | `display`, `flex-direction`, `justify-content`, `align-items`, `gap` | Segmented controls (e.g., row/column toggle) |
| Size | `width`, `height`, `max-width` | Numeric input with unit selector (px, rem, %) |
| Border radius | `border-radius` | Slider |

#### Two-Phase Editing Model

Visual edits happen in two phases, inspired by Cursor's approach:

**Phase 1 — Visual Loop (instant, no code changes):**
The reviewer adjusts a style control. The frontend sends the new CSS value to the preview iframe via a `postMessage` bridge that applies it as an inline style override. The reviewer sees the change instantly in the iframe. No code has been modified yet.

The `postMessage` bridge works despite the origin isolation because the preview's bootstrap script registers a listener that accepts style-override messages from the app origin. The bridge only accepts a whitelisted set of CSS properties — it cannot execute arbitrary JavaScript or modify the DOM beyond inline styles.

**Phase 2 — Code Loop (agent writes the actual change):**
When the reviewer clicks "Apply", the accumulated style changes are sent to the agent as a structured `visual_edit` message containing:
- The element's `ElementInfo` (component name, file path, CSS selector)
- A list of `{property, oldValue, newValue}` tuples
- A before/after screenshot pair

The `visual_edit` message includes design token information when available:

```json
{
  "element": { /* ElementInfo with ComponentFile, DesignTokens */ },
  "changes": [
    {
      "property": "background-color",
      "oldValue": "#3b82f6",
      "newValue": "#ef4444",
      "oldToken": "bg-blue-500",
      "newToken": "bg-red-500"
    }
  ],
  "beforeScreenshot": "blob://...",
  "afterScreenshot": "blob://..."
}
```

The agent uses the token names to generate idiomatic code changes — `bg-blue-500` → `bg-red-500` in Tailwind, `var(--color-primary)` → `var(--color-danger)` in CSS custom properties, etc. When no token matches the new value, the agent falls back to raw values. The dev server's HMR picks up the changes, and the agent captures a verification screenshot to confirm the code change matches the visual intent.

#### Why Not Direct Code Generation From Visual Edits

The visual editing controls produce CSS property changes, not code changes. Translating `background-color: #ff0000` into code depends on the codebase:
- Tailwind: change `bg-blue-500` to `bg-red-500`
- CSS Modules: edit the `.header` class in `Header.module.css`
- styled-components: edit the template literal in the component file
- Inline styles: edit the `style` prop

The agent is the right layer to make this translation because it already understands the codebase's styling conventions. The visual editing UI's job is to capture the reviewer's intent precisely, not to generate code.

#### Visual Editing Constraints

- Visual edits in Phase 1 are ephemeral — if the page reloads (HMR, navigation), they are lost. This is by design: the reviewer is previewing the change, not committing it.
- The `postMessage` bridge for style overrides is limited to CSS properties only. It cannot add/remove DOM elements, change text content, or execute scripts.
- Visual editing is not available for non-CSS changes (adding a new button, changing text, restructuring layout). Those require natural language instructions via the Design Mode text input.

### Agent-Driven Screenshot Timeline

As the agent iterates on changes, the system builds a visual timeline that both the agent and reviewer can reference.

#### How It Works

1. When the preview becomes `ready`, the preview manager captures an **initial baseline screenshot** automatically
2. After each agent code change that triggers an HMR update, a new screenshot is captured (with a 2-second stabilization delay)
3. Each screenshot is stored as a `preview_snapshot` with metadata:
   - `preview_instance_id`
   - `trigger` (`baseline`, `agent_change`, `agent_explicit`, `user_request`, `design_mode`)
   - `url_path` (the page that was captured)
   - `png_data` (stored in blob storage, not the database)
   - `console_errors` (any errors present at capture time)
   - `file_changes` (list of files the agent modified since the previous snapshot)
   - `created_at`
4. The session UI shows a scrollable timeline of snapshots below the preview iframe. The reviewer can click any snapshot to see the state at that point, the associated file changes, and any console errors.
5. The agent can reference previous snapshots in its context when reasoning about changes ("the header was correct in snapshot 3 but broke in snapshot 4").

#### Storage And Retention

- Screenshots are stored in blob storage (S3 or equivalent), not in PostgreSQL
- The `preview_snapshots` table stores metadata and a blob reference
- Snapshots are retained for the lifetime of the session plus 24 hours (for post-review reference)
- Maximum 50 snapshots per preview instance (oldest are evicted if exceeded)
- Each screenshot is ~100-500 KB (PNG, 1280x720 viewport)

### Semantic Diff Awareness

Static screenshots tell the agent what the preview looks like, but not what *changed*. After every code edit, the agent needs to understand the delta — both visually and structurally — to verify that the change matches intent and to catch unintended side effects.

#### How Semantic Diffs Work

The `preview_visual_diff` tool compares two preview snapshots (typically the before and after of a code change) and returns a structured `VisualDiff`:

1. **Pixel diff**: The headless browser captures both states at the same viewport size. A pixel-level comparison identifies regions that changed, expressed as bounding boxes with severity ("minor" for small shifts, "major" for large repaints, "new" for added elements, "removed" for missing elements). An overlay image highlights changed regions in red.
2. **DOM diff**: The system serializes the DOM tree of both snapshots and computes structural differences — elements added, removed, moved, or changed (text content, attributes). Each change is tagged with a CSS selector so the agent can map it back to code.
3. **Style diff**: For elements that exist in both snapshots, the system compares computed styles and reports changes. If design tokens are available, the diff includes token names (e.g., "background-color changed from `bg-blue-500` to `bg-red-500`").
4. **Summary**: A human-readable summary string that describes the visual impact in plain language: "Header height increased by 24px, causing nav items to wrap to a second line. Card grid shifted down by 24px."

#### What The Agent Sees

After making a change, the agent's context includes:

```json
{
  "pixel_diff_percent": 12.3,
  "diff_regions": [
    { "bounding_box": { "x": 0, "y": 0, "w": 1280, "h": 80 }, "severity": "major" },
    { "bounding_box": { "x": 0, "y": 80, "w": 1280, "h": 640 }, "severity": "minor" }
  ],
  "dom_changes": [
    { "selector": ".header", "change_type": "attribute_changed", "before": "class=\"header bg-blue-500\"", "after": "class=\"header bg-red-500\"" }
  ],
  "style_changes": [
    { "selector": ".header", "property": "background-color", "before": "#3b82f6", "after": "#ef4444", "token": "bg-red-500" }
  ],
  "summary": "Header background changed from blue to red. No other layout or content changes detected."
}
```

This tells the agent: "Your change did exactly what was intended and nothing else." Or, critically: "Your change to the header also shifted the card grid layout — investigate."

#### When Semantic Diffs Run

- **Automatically** after every HMR-triggered auto-screenshot (diffed against the previous snapshot)
- **On demand** when the agent calls `preview_visual_diff` with two specific snapshot IDs
- **In assertion flows** when the agent wants to verify that a change had no unintended side effects beyond the target elements

The diff computation runs in the headless browser on the worker node. For a typical 1280x720 viewport, the pixel diff takes ~200ms and the DOM/style diff takes ~100ms.

### Interaction Replay

The agent needs to verify flows that require user input — form submission, login, navigation sequences, dropdown menus, modal dialogs. Without interaction capabilities, the agent can only verify static page loads. With `preview_interact`, the agent can script multi-step browser interactions and capture the result at each checkpoint.

#### Interaction Model

The agent composes a sequence of `InteractionStep` objects and executes them in a single `preview_interact` call. Each step performs one browser action and optionally waits for a condition and captures a screenshot:

```json
[
  { "action": "navigate", "value": "/login" },
  { "action": "type", "selector": "#email", "value": "test@example.com" },
  { "action": "type", "selector": "#password", "value": "password123" },
  { "action": "click", "selector": "#login-button", "wait_for": "networkidle", "screenshot": true },
  { "action": "wait", "wait_for": ".dashboard-content", "timeout": "5s", "screenshot": true }
]
```

The result includes per-step success/failure, screenshots at each checkpoint, the final URL, and any console errors introduced during the interaction:

```json
{
  "steps": [
    { "step_index": 0, "action": "navigate", "success": true, "url": "/login" },
    { "step_index": 1, "action": "type", "success": true },
    { "step_index": 2, "action": "type", "success": true },
    { "step_index": 3, "action": "click", "success": true, "screenshot": { "png": "...", "page_title": "Dashboard" }, "url": "/dashboard" },
    { "step_index": 4, "action": "wait", "success": true, "screenshot": { "png": "...", "page_title": "Dashboard" }, "url": "/dashboard" }
  ],
  "total_time": "3.2s",
  "final_url": "/dashboard",
  "console_errors": []
}
```

#### Use Cases

| Scenario | Interaction Sequence | What The Agent Verifies |
|----------|---------------------|------------------------|
| Login flow | Navigate → type email → type password → click submit → wait for dashboard | Successful redirect, dashboard renders, no errors |
| Form validation | Navigate → click submit (empty form) → check error messages → fill fields → submit → check success | Validation messages appear, successful submission after fix |
| Navigation | Click nav link → wait for page → click another link → wait | All routes render without errors |
| Modal dialog | Click trigger → wait for modal → interact with modal content → close | Modal opens/closes correctly, content renders |
| Pagination | Navigate to list → click "next" → verify page 2 content → click "previous" → verify page 1 | Pagination works, content changes correctly |

#### Safety Constraints

- **Max steps per interaction**: 20 (prevents runaway interaction scripts)
- **Max total duration**: 60 seconds
- **No external navigation**: interactions cannot navigate outside the preview origin
- **No file uploads**: the `type` action works only on text inputs, not file inputs
- **Rate limited**: max 10 `preview_interact` calls per minute per preview
- **Idempotent intent**: interactions are for verification, not for mutating application state in ways the agent depends on. The agent should not use interactions to "set up" state that its code changes depend on — instead, it should write the code correctly and use interactions to verify the result.

#### Interaction Recording From Design Mode

When a human reviewer interacts with the preview in "interact" mode (Design Mode toggled off), the frontend can optionally record the interaction sequence (clicks, typed text, navigation) as a replayable script. The agent can then re-run this recorded interaction after making code changes to verify that the human's workflow still works.

This creates a lightweight, ephemeral regression check without requiring the reviewer to re-test manually:

1. Reviewer interacts with preview (browsing pages, filling forms)
2. Frontend records interaction steps (with timestamps and selectors)
3. Reviewer toggles Design Mode and gives feedback: "fix the spacing on this form"
4. Agent makes changes and replays the recorded interaction
5. Agent verifies all steps still succeed and the visual result is correct

Recorded interactions are stored in memory for the session lifetime — they are not persisted as test files.

### Multi-Viewport Preview

Frontend changes frequently break on viewports the developer didn't check. A responsive design change that looks perfect on desktop may wrap incorrectly on mobile or overflow on tablet. The `preview_multi_viewport` tool captures the preview at multiple viewport sizes in a single call, giving the agent a comprehensive view of how the change renders across screen sizes.

#### Default Viewports

When the agent calls `preview_multi_viewport` without specifying custom viewports, it captures three standard breakpoints:

| Name | Width | Height | Represents |
|------|-------|--------|------------|
| `mobile` | 375 | 812 | iPhone SE / typical mobile |
| `tablet` | 768 | 1024 | iPad / typical tablet |
| `desktop` | 1280 | 720 | Standard desktop (same as default single screenshot) |

The agent can override these or add custom viewports (e.g., `ultrawide` at 2560x1080) via the `viewports` parameter.

#### How It Works

1. The headless browser creates three browser contexts (or resizes a single context sequentially) for each viewport spec
2. Each context navigates to the same URL path and waits for load + stabilization
3. Screenshots are captured at each viewport size
4. Console errors are collected per-viewport (a bug might only manifest at a certain viewport)
5. Results are returned as an array of `ViewportCapture` objects

The multi-viewport capture reuses the existing headless browser instance. Capturing all three viewports takes ~3-5 seconds total (sequential resize approach) or ~2-3 seconds (parallel contexts, higher memory).

#### Agent Integration

The recommended agent flow for responsive changes:

1. Agent makes a layout/styling change
2. Agent calls `preview_multi_viewport` to capture all three viewports
3. Agent evaluates each viewport screenshot against the user's request
4. If any viewport has issues, the agent iterates with a targeted fix
5. Agent presents the multi-viewport result to the user: "Here's how it looks on mobile, tablet, and desktop"

This is particularly valuable for:
- CSS grid/flexbox changes
- Typography adjustments (font sizes that work on desktop but are too small on mobile)
- Navigation changes (hamburger menu vs. full nav bar)
- Image/card layout changes (column count, wrapping behavior)

#### State Injection

Beyond viewport sizes, the agent can capture the preview in different **application states** to verify edge cases:

| State | How It's Set | What It Catches |
|-------|-------------|----------------|
| Empty state | Agent navigates to a route with no data (or uses `preview_interact` to clear data) | Missing empty state UI, broken layouts with no content |
| Error state | Agent uses `preview_interact` to trigger an error (e.g., disconnect network, submit invalid data) | Error boundary rendering, error message display |
| Loading state | Agent captures immediately after navigation (before data loads) | Loading spinner/skeleton display, layout shift during load |
| Dark mode | Agent uses `preview_interact` to toggle theme (if the app supports it) | Color contrast issues, missing dark mode styles |

State injection is not a separate tool — it's a pattern that combines `preview_interact` (to set up the state) with `preview_screenshot` or `preview_multi_viewport` (to capture it). The agent is responsible for knowing how to trigger different states in the specific app being previewed.

#### Multi-Viewport Resource Constraints

- Maximum viewports per call: 5
- All viewports share the same headless browser instance on the worker
- Multi-viewport captures count toward the same snapshot storage limits (each viewport is one snapshot)
- The headless browser restores its default viewport (1280x720) after multi-viewport capture

## Security Model

### 1. Browser-Origin Isolation

This is the most important rule.

- Main app cookies are scoped to the app origin
- Preview cookies are scoped to the preview origin
- The preview domain must never receive the main app session or CSRF cookie
- The iframe must not use `allow-same-origin`

Recommended iframe sandbox for MVP:

```html
sandbox="allow-scripts allow-forms allow-modals allow-downloads allow-popups allow-popups-to-escape-sandbox"
```

`allow-popups` and `allow-popups-to-escape-sandbox` are included because some previewed apps rely on popup windows for OAuth flows, payment modals, or similar patterns. Popups open on the preview origin (isolated), so this does not weaken the security boundary with the main app.

Note: the iframe sandbox does **not** include `allow-same-origin`. This means the preview iframe cannot read its own `document.cookie` or `localStorage` normally. However, the preview's bootstrap script (served by the preview gateway) can still use `postMessage` to communicate with the app origin. This is used by the Visual Editing `postMessage` bridge for ephemeral style overrides and by the activity-aware timeout heartbeat. The `message` event listener in the bootstrap script validates the sender origin before accepting any messages.

That is restrictive enough to avoid handing the preview the app origin while still allowing most modern dev servers to function.

### 2. Preview Session Binding

After bootstrap, the gateway creates preview access state that is bound to:

- `org_id`
- `user_id`
- `preview_instance_id`
- `issued_at`
- `expires_at`
- optional `revoked_at`

Hard requirements:

- preview access state must be valid for one preview instance only
- preview access state must not be reusable across unrelated previews in the same browser
- preview access state must be revoked when the preview stops, expires, or the user loses org access
- preview cookies, if used, must be `HttpOnly`, `Secure`, and `SameSite=Strict`
- preview cookies, if used, must be host-only on the per-preview hostname

Per-preview hostnames are required. Shared preview origins are not allowed.

### 3. Header Handling

The preview gateway should:

- strip `Set-Cookie` from sandbox responses unless explicitly required for preview-domain-only state
- inject `X-Frame-Options` / CSP equivalents that only allow framing from the app origin
- set a restrictive `Permissions-Policy` disabling camera, microphone, geolocation, clipboard, and other powerful browser features
- avoid forwarding hop-by-hop headers blindly

### 4. Browser-Side Exfiltration Controls

The previewed app executes untrusted JavaScript in the reviewer's browser. Separate origin protects the main app's cookies and storage, but it does **not** by itself prevent the preview from sending data to external destinations, capturing input entered into the preview, or attempting to persist on the preview origin.

The preview gateway must inject a restrictive Content Security Policy for preview responses.

Suggested MVP CSP shape:

```http
Content-Security-Policy:
  default-src 'self' blob: data:;
  script-src 'self' 'unsafe-inline' 'unsafe-eval';
  style-src 'self' 'unsafe-inline';
  img-src 'self' data: blob:;
  font-src 'self' data:;
  connect-src 'self' wss://*.preview.143.dev;
  form-action 'self';
  navigate-to 'self';
  object-src 'none';
  base-uri 'none';
  frame-ancestors https://app.143.dev;
  worker-src 'none';
```

Notes:

- `script-src` may still need dev-server-compatible allowances such as `'unsafe-eval'` for HMR in MVP
- `connect-src` is scoped to `wss://*.preview.143.dev` rather than bare `ws:` / `wss:` to prevent WebSocket connections to arbitrary external hosts, which would be a data exfiltration vector. This still allows HMR connections to the preview's own origin
- browser egress to third-party origins should be forbidden in MVP unless the product later grows an explicit browser-egress policy
- if a preview needs browser-visible access to an external API, prefer routing it through the preview gateway rather than allowing arbitrary direct browser requests
- service workers are forbidden in MVP because a top-level preview tab must not be able to persist control over the preview origin

The gateway should also set:

- `Referrer-Policy: no-referrer`
- `X-Content-Type-Options: nosniff`
- `Cross-Origin-Opener-Policy: same-origin`

### 5. Preview Access Tokens

Preview access is based on a signed, short-lived token scoped to:

- org
- session
- preview instance
- expiry

#### Token Exchange Via `postMessage`

The original design placed the bootstrap token in the iframe `src` URL (`/bootstrap/<token>`). While the redirect clears the URL, the token still appears in browser history, server access logs, and potentially referrer headers during the redirect window.

The recommended approach uses `postMessage` to keep the token out of URLs entirely:

1. The app origin makes `POST /api/v1/sessions/{id}/preview/bootstrap` to mint a one-time token
2. The iframe `src` is set to a static bootstrap page on the preview origin: `https://<preview-id>.preview.143.dev/bootstrap`
3. The bootstrap page registers a `message` event listener and signals readiness via `postMessage` to the parent
4. The app origin sends the token to the iframe via `postMessage`, validating the target origin matches the expected preview hostname
5. The bootstrap page exchanges the token for a session cookie via a same-origin `POST /bootstrap/exchange` on the preview domain
6. On success, the bootstrap page navigates to the preview root URL

This keeps the token out of browser history, referrer headers, and server access logs. The `postMessage` origin check on both sides prevents cross-origin token interception.

The gateway should reject token exchange requests that do not arrive as same-origin POST requests from the bootstrap page.

### 6. Access Control

Previews are internal-only in MVP.

- The app origin authenticates the user through the normal 143 session
- The API server verifies org membership before minting a bootstrap token
- The preview gateway accepts only bootstrap-token-derived preview state, not raw unauthenticated access
- GitHub comments and deployment URLs should link to 143-controlled routes, not a bypass URL

This keeps access control simple without weakening the isolated preview origin model.

### 7. Revocation Plan

Revocation needs to be checked on both normal requests and long-lived connections.

MVP plan:

1. The preview cookie contains an opaque session identifier, not self-contained authorization.
2. On every HTTP request and WebSocket upgrade, the preview gateway resolves the preview access session and verifies:
   - matching hostname / preview instance
   - `revoked_at IS NULL`
   - `expires_at > now()`
   - the user still belongs to the org
   - the user's role still satisfies the preview's required access level
3. The gateway may use a very short positive cache, but revocation must invalidate that cache immediately on the same node.
4. Existing WebSocket connections get a periodic recheck, for example every 30-60 seconds, and are closed if the session is revoked or expires.
5. Preview stop / expiry revokes all access sessions for that preview instance in one transaction.

Revocation triggers:

- preview stopped
- preview expired
- user removed from org
- role downgraded below required access
- explicit user logout from preview
- admin manual revoke

Single-node MVP implementation:

- gateway reads from `preview_access_sessions`
- preview manager revokes sessions directly in the same database
- gateway keeps an in-memory map of active WebSocket connections by `preview_access_session_id` and closes them on revoke

Multi-node follow-up:

- publish revocation events over an internal bus or `LISTEN/NOTIFY`
- every gateway node evicts cached sessions and closes matching live connections

### 8. Credential And Egress Policy

Preview execution has two trust tiers:

| Tier | When Used | Policy |
|------|-----------|--------|
| `restricted` | generated `bootstrap` previews and repos with no approved connected setup | no third-party credentials; in MVP only repo-local runtime behavior, with broader isolated resources reserved for later phases |
| `trusted_internal` | repos that an admin has explicitly enabled | short-lived non-production credentials and approved named destinations allowed |

Hard rules for both tiers:

- production credentials are forbidden
- preview secrets are injected only at runtime, never in cacheable build layers
- preview logs must redact injected secret values
- RFC1918 and production destinations stay blocked unless explicitly approved as non-production managed destinations

This section governs **sandbox-side** network egress. Browser-side egress is separately governed by the preview gateway's CSP and response-header policy.

### 9. Sandbox Security Still Matters

The sandbox still runs untrusted code, so the existing hardening remains required:

- gVisor in production
- dropped Linux capabilities
- non-root user
- network restrictions
- resource limits

But sandbox hardening alone is not enough. The browser render path is a separate trust boundary.

### 10. Repo Config Guardrails

Preview config must be treated as untrusted repo content:

- `cwd` for every service must resolve inside the repo root
- `port` for every service must be within an allowed range and unique across the config
- `command` for every service must be executed without shell interpolation
- repo config cannot inline secrets
- credential set selection, `inject_into`, and managed destinations must come from base-branch config plus admin-managed settings
- `primary` must reference a service name that exists in the `services` map
- `inject_into` must reference only service names that exist in the `services` map
- the `services` map must contain at most 4 entries
- the `infrastructure` map must contain at most 2 entries
- `infrastructure.*.template` must reference a platform-provided template name
- `infrastructure.*.init_script` must resolve inside the repo root
- `infrastructure.*.inject_into` must reference only service names that exist in the `services` map
- a diff cannot add, remove, or rename services — the set of service names is read from the base branch
- a diff cannot add, remove, or rename infrastructure — the set of infrastructure names and templates is read from the base branch

### 11. Security Of Visual Feedback And Design Mode

The visual feedback features introduce new attack surfaces that must be handled carefully.

#### Headless Browser Isolation

The headless Chromium instance runs on the worker node, outside the sandbox. It loads preview content the same way a reviewer's browser would — through the preview gateway. This means:

- The headless browser sees the same CSP, header stripping, and origin isolation as any other preview consumer
- The headless browser does **not** have access to the main app session, managed credentials, or sandbox internals
- If preview JavaScript attempts to exploit the headless browser (e.g., Chromium zero-day), the blast radius is the worker node, not the sandbox. This is acceptable because the worker already has higher privilege than the sandbox.
- The headless browser should run with `--no-sandbox` disabled (i.e., Chromium's own sandbox should remain enabled) and with a restrictive seccomp profile

#### Design Mode `postMessage` Bridge

The Visual Editing feature uses a `postMessage` bridge to apply ephemeral style overrides in the preview iframe. This bridge must be tightly constrained:

- The preview's bootstrap script registers a `message` event listener that **only** accepts messages from the app origin (`https://app.143.dev`)
- The bridge accepts a strict schema: `{ type: "style_override", selector: string, property: string, value: string }`
- The `property` field must be on a whitelist of CSS properties (no `content`, no `-webkit-*` that could affect behavior, no custom properties that map to JavaScript)
- The `selector` field must match an element already identified by `InspectElement` — arbitrary selectors are rejected
- The bridge cannot execute arbitrary JavaScript, modify DOM structure, add/remove elements, or change event handlers
- The bridge applies changes as inline styles only — it does not modify stylesheets

If the preview's bootstrap script is tampered with (because it is served from the preview origin, which serves untrusted content), the bridge may not function correctly. This is acceptable — the preview JavaScript cannot use the bridge to attack the app origin because `postMessage` to the app origin is already blocked by the CSP (`connect-src` does not include the app origin).

#### Component Resolver Script Is Untrusted

The component resolver script (`__143_resolveElement`) runs in the preview origin and returns metadata about React/Vue/Svelte components. This output is **untrusted** because:

- The preview JavaScript could override the resolver function to return false component names or file paths
- A malicious preview could return paths that look like sensitive files to confuse the agent

Mitigations:
- The headless browser validates that reported `ComponentFile` paths exist in the repo and are within the repo root
- `ComponentName` is sanitized (alphanumeric + common separators, < 100 chars)
- `Props` values are JSON-serialized and truncated (max 1KB) — they are informational hints for the agent, not trusted input
- If validation fails, the field is omitted and the system falls back to DOM-only information
- The resolver output is never used for access control, credential routing, or security decisions — it only enriches the agent's context

#### Design Token Extraction Is Read-Only

Token extraction during the Build phase reads Tailwind config, CSS files, and theme files from the repo. This is a read-only scan — it does not execute JavaScript from `tailwind.config.js`. Instead, it uses a static parser that extracts the `theme` object from the config file's AST. This avoids arbitrary code execution during the Build phase.

If the config file uses dynamic JavaScript (e.g., `theme.extend.colors = generateColors()`) that cannot be statically analyzed, the token map will be incomplete. This is acceptable — the system falls back to raw value inputs with no token suggestions.

#### Screenshot Content Is Untrusted

Screenshots and screencasts captured by the headless browser contain rendered untrusted content. When these are displayed in the app origin UI (timeline, Design Mode) or included in agent context:

- They are rendered as `<img>` or `<video>` tags, not embedded as HTML. There is no risk of script execution from PNG/GIF/WebM files.
- Blob storage is separate from main app assets
- Screenshots and screencasts are scoped to the preview instance and covered by the same RBAC as the preview itself

#### Interaction Replay Security

The `preview_interact` tool gives the agent the ability to click, type, and navigate in the headless browser. This is a more powerful capability than read-only screenshots and requires additional constraints:

- **No external navigation**: interactions cannot navigate outside the preview origin (`*.preview.143.dev`). Attempts to navigate to external URLs are blocked and the step fails.
- **No credential typing**: the interaction step's `value` field is logged in the session audit trail. Agents must not type managed credentials into form fields — if the app requires authentication, the preview config should handle it via environment variables or seed data, not via form-based login in the headless browser.
- **Rate limiting**: max 10 `preview_interact` calls per minute per preview. Each call is limited to 20 steps and 60 seconds total. This prevents the interaction tool from being used as a denial-of-service vector against the worker node.
- **No file uploads or downloads**: the headless browser cannot upload files or download artifacts during interaction steps.
- **Same headless browser instance**: interactions run in the same headless browser instance and browser context as screenshots and Design Mode. No additional resource overhead beyond the interaction execution time.
- **Audit trail**: every interaction sequence is logged with the full step list and results, visible in the session audit log.

#### Visual Diff And Assertion Trust Model

The `preview_visual_diff` and `preview_assert` tools operate on data captured by the headless browser (screenshots, DOM snapshots) and run comparison logic on the worker node. Their outputs are informational — they help the agent make better decisions, but they are not used for security-critical decisions:

- Visual diff regions and DOM change lists are derived from headless browser captures, which contain untrusted rendered content. The diff computation itself runs on the worker (trusted), but the data it operates on is untrusted.
- Assertion results are advisory — a "passed" assertion does not grant any additional privileges or bypass any checks. The agent uses assertion results to decide whether to iterate, but the human reviewer always has final say.
- The `viewport_screenshot_match` assertion type (which asks the agent to evaluate whether a screenshot region matches a natural language description) is a best-effort heuristic. It is not a deterministic check and should not be relied upon for critical verification.

### 12. Tenancy And Audit Requirements

Preview data is tenant-scoped data and must follow the same isolation model as the rest of the platform.

Required controls:

- `preview_instances`, `preview_services`, `preview_infrastructure`, `preview_logs`, and `preview_access_sessions` include `org_id` (directly or via FK to `preview_instances`)
- all preview API reads and writes filter by `org_id`
- preview tables are covered by tenancy audit tests
- preview tables are covered by Row-Level Security where feasible
- preview start, stop, restart, token mint, and token exchange events are written to `audit_log`

In multi-node mode, gateway-to-worker preview traffic must use authenticated service-to-service transport. Prefer mTLS or signed short-lived service tokens over trusting the internal network by default.

## PR Preview Integration

Preview environments are most valuable when they're connected to the code review workflow. Rather than treating previews as a standalone feature accessed through the session UI, the system should integrate directly into the GitHub PR lifecycle — while being careful not to waste resources on idle previews.

### On-Demand PR Previews

Previews are **not** auto-started when a PR is created. Instead, the system posts a PR comment with a **"Launch Preview"** button (a deep link to the 143 session page with `?preview=1`). Clicking the link navigates to the 143 session, which starts the preview on demand.

```
┌──────────────────────────────────────────────────────────┐
│ 🔍 Preview available for this PR                          │
│                                                            │
│ [Launch Preview] — starts a live preview of this change    │
│                                                            │
│ Last preview: 2 hours ago (stopped — idle timeout)         │
│ Preview snapshots: 12 screenshots from last session        │
│ [View Screenshot Timeline]                                 │
└──────────────────────────────────────────────────────────┘
```

This avoids burning resources on every PR. The preview starts when someone actually wants to look at it, and stops automatically via the existing idle timeout (15 min) and hard TTL (30 min, extendable to 2h).

### PR Comment Lifecycle

The system maintains a **single, updating PR comment** (not one per preview session) that reflects the current state:

| PR State | Comment Content |
|----------|----------------|
| PR opened, no preview yet | "Launch Preview" button + link to 143 session |
| Preview running | Live preview link + "View in 143" deep link + current screenshot thumbnail |
| Preview stopped (idle timeout) | "Re-launch Preview" button + screenshot timeline from last session + "Last active: 5 min ago" |
| Agent made changes after review feedback | Updated screenshot thumbnail + "Agent updated preview — 3 files changed" |
| PR merged/closed | Final screenshot timeline preserved as static artifacts |

The comment is posted via the GitHub API using the org's GitHub integration. It is scoped to the PR — one comment per PR, updated in place.

### Agent-Driven PR Review Loop

When a reviewer leaves a comment on the PR (e.g., "the button color is wrong"), the agent can:

1. Read the PR review comment via the existing GitHub integration
2. Start or resume the preview (if stopped)
3. Make the code fix in the sandbox
4. Wait for HMR to update the preview
5. Capture a screenshot and run self-verification assertions
6. Post a reply to the PR comment with the screenshot: "Fixed — here's the updated preview"
7. Push the code change to the PR branch

This creates a **visual review loop embedded in the PR**: reviewer comments → agent fixes → preview updates → agent posts screenshot proof → reviewer verifies. The preview only runs while the agent is actively working or the reviewer is actively looking at it.

### Visual Diff Summary On PR

When a preview is running for a PR, the system can optionally capture a **base branch vs. feature branch visual diff** and post it as a PR comment section:

```
### Visual Changes Detected

3 regions changed across 1 page:

| Region | Change | Severity |
|--------|--------|----------|
| Header (0,0 → 1280,80) | Background color: blue → red | Major |
| Button (.card-footer .btn) | Padding increased by 8px | Minor |
| Footer (0,640 → 1280,720) | New "Terms" link added | New element |

[View full visual diff overlay →]
```

This runs by starting the preview for both the base branch and the feature branch (sequentially, not simultaneously — the base branch preview is captured as a snapshot, then the sandbox switches to the feature branch). The diff is computed using the existing `ComputeVisualDiff` infrastructure.

**Resource note**: the base branch capture is a one-time cost per PR (cached as a snapshot keyed by `base_commit + preview_config_hash`). It does not require keeping two previews running simultaneously.

### GitHub Deployment Status

When a preview starts, 143 should:

1. Update the session UI with preview status and logs
2. Create or update a GitHub deployment pointing at a protected 143 URL (the session page, not the raw preview gateway)
3. Update the PR comment with the current preview state
4. Set a GitHub commit status (`preview/143`) to `pending` while starting, `success` when ready, `inactive` when stopped

The deployment URL always points to `app.143.dev/sessions/{id}?preview=1`, which handles authentication and preview bootstrapping. Raw preview gateway URLs are never exposed in PR comments.

### Preview Artifacts After Sandbox Teardown

When a preview stops (idle timeout, hard TTL, or manual stop), the screenshot timeline, assertion results, and visual diff summaries are **preserved as static artifacts** on the PR comment. Reviewers can browse the visual history even after the sandbox is torn down.

These artifacts are stored in blob storage with the same retention policy as screenshots (session lifetime + 24h). For PRs that remain open, artifacts are retained until the PR is merged or closed, up to a maximum of 7 days after the last preview session.

When a reviewer clicks "Re-launch Preview" on a stopped preview, the system uses fast startup (filesystem snapshot caching, see below) to minimize wait time.

## Scaling Model

### What Scales Well

- single frontend repos (React, Vue, Svelte, etc.)
- framework-integrated full-stack apps (Next.js, Remix, Nuxt, Rails)
- monorepo frontend + backend with up to 4 services running in one sandbox
- apps connecting to external staging databases or APIs via managed destinations
- apps using platform-provided PostgreSQL, Redis, or MySQL for local ephemeral data
- static sites with a local dev server

### What Does Not Fit MVP

- apps requiring more than 4 co-located application services or more than 2 infrastructure services
- apps needing infrastructure not in the platform-provided set (Elasticsearch, Kafka, RabbitMQ, etc.)
- apps that only boot via Docker Compose or Kubernetes with complex container orchestration
- separate-repo architectures where frontend and backend are in different repositories
- terminal / desktop apps

### Operational Constraints

Previews are expensive because they keep file watchers and application processes alive. MVP should enforce:

- on-demand start only (from session UI or PR "Launch Preview" link — never auto-started)
- one active preview per session
- org-level cap on concurrent previews
- node-level cap on concurrent previews
- per-user cap on concurrent previews
- idle timeout, default 15 minutes (activity-aware, see Frontend UX section)
- hard TTL, default 30 minutes (auto-extendable to 2 hours on active use)

No auto-start in MVP. If a user wants a preview, they request it explicitly — either from the session page or by clicking the "Launch Preview" link on the PR comment. The PR comment persists screenshot artifacts from the last session, so reviewers can browse the visual history without re-launching. When they do re-launch, filesystem snapshot caching (see Fast Startup) minimizes the wait.

### Per-Preview Resource Limits

Each preview process must run under explicit resource limits enforced at the container / cgroup level. Without these, a single runaway dev server (e.g., webpack in a large monorepo with `--watch`) can consume all available memory on a worker node and degrade other sandboxes.

Resource limits apply to the **entire preview** (all services in the config), not per-service. All services share a single cgroup within the sandbox.

Default limits per preview:

| Resource | Single-Service Default | Multi-Service Default | Maximum |
|----------|----------------------|----------------------|---------|
| Memory | 512 MB | 1024 MB | 2048 MB |
| CPU | 0.5 cores | 1.0 cores | 2.0 cores |
| File watchers (`fs.inotify.max_user_watches`) | 65536 | 131072 | 131072 |

The preview manager automatically applies the multi-service defaults when a config contains more than one service. These are enforced via Docker `--memory` and `--cpus` flags (or equivalent cgroup settings for other providers). The `PreviewConfig` should include a `resource_limits` field:

```go
type ResourceLimits struct {
    MemoryMB int // default 512 (single) or 1024 (multi), max 2048
    CPUMillis int // default 500 (single) or 1000 (multi), max 2000
}
```

When any process in the preview is OOM-killed (exit code 137), the preview manager should transition the preview to `failed` status with a diagnostic indicating which service was likely affected (based on which process exited) and the memory limit that was exceeded.

Infrastructure containers have separate resource limits (see Platform Infrastructure Services section) that are **additive** to the application service limits. A multi-service preview with a PostgreSQL sidecar uses ~1024 MB (app services) + ~256 MB (PostgreSQL) = ~1280 MB total.

### Concurrency Caps

Concrete defaults for MVP:

| Scope | Default Cap | Rationale |
|-------|-------------|-----------|
| Per user | 2 | Prevents one person from monopolizing preview capacity |
| Per org | 5 | Conservative starting point; ~6.5 GB RAM worst case (all multi-service + infra) |
| Per worker node | 3 | Assumes 8 GB reserved for preview processes + infrastructure + shared headless browser on a standard worker |

Multi-service previews with infrastructure consume significantly more resources than single-service previews. The concurrency caps count preview instances, not services or infrastructure containers — a preview with 2 services + PostgreSQL counts as 1 toward the cap. This keeps the model simple, but the node-level cap should be set conservatively to account for the higher resource footprint of infrastructure-heavy previews.

These caps should be configurable by admins at the org level. The preview manager should return a clear error when a cap is hit: "Your org has reached its limit of 5 concurrent previews. Stop an existing preview to start a new one." The UI should show the current count and cap.

### Process Health Checks

The preview manager should poll **each service's** health endpoint after the initial readiness check succeeds:

- Poll each service's `ready.http_path` on its respective port every **10 seconds**
- If **3 consecutive checks** fail for any service, transition that service to `unhealthy` in `preview_services` and the preview instance to `unhealthy`
- The UI should show which service failed: "Backend stopped responding — Restart preview?" with a one-click restart button
- If the service recovers on its own (next health check passes), transition back to `ready`

For multi-service previews, a single unhealthy support service makes the entire preview unhealthy because the primary service likely depends on it. The UI should clearly indicate which service is the source of the problem.

This catches the common case where a dev server crashes mid-session due to a syntax error, bad import, or memory pressure, and gives the user a clear recovery path.

### Process Recycling

Long-running dev servers with HMR enabled are prone to memory leaks. After a configurable `max_uptime` (default: 60 minutes), the preview manager should:

1. Gracefully stop all application service processes (SIGTERM, then SIGKILL after 10 seconds)
2. Tear down and re-provision infrastructure containers (to ensure clean database state and avoid stale connections)
3. Re-run init scripts against fresh infrastructure
4. Restart all application services in dependency order using the same resolved config
5. Wait for all readiness probes to pass
6. Resume proxying

In MVP, recycling restarts **everything** — infrastructure and all application services — to avoid inconsistencies (e.g., a backend restart that tries to reconnect to an infrastructure container with stale credentials or corrupted state).

This happens transparently. The UI should show a brief "Preview restarting..." indicator. The `last_path` on the preview instance (see Edge Cases section) should be preserved so the user returns to where they were.

## More Realistic MVP Boundary

Phase 1 should support only:

1. Docker provider
2. Preview served from an isolated preview origin
3. Single-service and multi-service configs (up to 4 application services per config, all in one sandbox)
4. Platform-provided infrastructure services (PostgreSQL, Redis, MySQL) as sidecar containers, up to 2 per config
5. Explicit single-node deployment (`MODE=all`)
6. On-demand start / stop (manual from session UI, or via "Launch Preview" link on PR comment)
7. Active sessions or short-lived post-run review windows
8. WebSocket support for HMR when the underlying dev server uses it
9. `bootstrap` and `staging_like` trust tiers
10. Managed credential sets and named destinations with `inject_into` scoping
11. Agent visual feedback: `preview_screenshot`, `preview_console`, `preview_element` tools
12. Auto-screenshot on HMR updates with screenshot timeline
13. Design Mode: element selection, annotations, element reordering, natural language feedback to agent
14. Visual Editing: style tweaks with instant preview, design token awareness, and agent-driven code application
15. Component resolver: framework-aware element inspection (React, Vue, Svelte, Angular) with component name, file path, props, and ancestor chain
16. Design token extraction from Tailwind config, CSS custom properties, and theme files
17. Agent screencast recording for multi-page verification flows
18. Self-verification assertions: structured pass/fail checks the agent runs against the preview to verify its own work
19. Semantic diff awareness: automatic before/after comparison showing pixel changes, DOM mutations, and style shifts after each code edit
20. Agent-driven interaction replay: scripted browser interactions (click, type, navigate) for verifying multi-step flows like form submission and login
21. Multi-viewport capture: simultaneous screenshots at mobile (375px), tablet (768px), and desktop (1280px) viewports
22. PR preview integration: on-demand preview launch from PR comment, single updating PR comment with preview state and screenshot thumbnails, preview artifacts preserved after sandbox teardown
23. Filesystem snapshot caching: cache the sandbox filesystem state after successful startup, keyed by lockfile + base commit, to skip the Build phase on subsequent preview starts
24. Filesystem snapshot caching: restore node_modules and build artifacts from cached snapshots on repeat previews (keyed by lockfile + base commit + config)
25. Progressive preview: show frontend preview before backend is fully ready (opt-in per config)

Phase 1 should explicitly exclude:

1. Same-origin preview under `/api/v1/...`
2. Custom user-defined sidecar containers or Docker Compose-style orchestration
3. Visual regression diffing as a CI/review gate (automated before/after comparison with configurable pass/fail thresholds — semantic diffs are available as agent tools but not as gating checks)
4. Public tunnel exposure
5. Preview for completed historical sessions after sandbox teardown
6. Per-service restart (only full-preview restart in MVP)
7. User-extensible infrastructure templates (only platform-provided templates)
8. Full freeform drag-and-drop layout editing (directional reorder controls are in MVP; freeform drag is a later phase)
9. User-extensible design token sources (only Tailwind config, CSS custom properties, and common theme file patterns in MVP)
10. Cross-session failure memory (learning from past preview failures to improve future agent behavior)
11. Recorded interaction persistence as test files (interaction replay is ephemeral per-session, not saved as Playwright/Cypress tests)
12. Auto-start previews on PR creation (previews are always on-demand; no background resource consumption for inactive PRs)
13. Base-vs-feature visual diff as automated PR check (available on-demand when preview is running, but not as an automated CI gate)
14. Agent auto-responding to PR review comments (agent can be instructed to do this, but no automated trigger in MVP)

## Future Expansion

### Phase 2: Multi-Node, Provider Support, And PR Workflow

1. Worker-to-gateway preview streaming for split API/worker deployments
2. E2B or other provider implementations of `DialPreview`
3. Better metrics on preview startup time, duration, and failures
4. Immutable image caching keyed by base commit + resolved preview config
5. Richer setup diagnostics for build/init/start phases
6. Per-service restart (restart only the failed service without stopping the entire preview)
7. Automated base-vs-feature visual diff as a PR status check (run on every preview start, post results as a commit status)
8. Agent auto-trigger on PR review comments (agent monitors for new review comments and auto-starts a fix cycle with preview verification)
9. Cross-worker snapshot sharing (store filesystem snapshots in blob storage so any worker can restore them, not just the original worker)

### Phase 3: Extended Infrastructure And Templates

1. User-extensible infrastructure templates (custom Docker images, not just platform-provided)
2. Admin-managed infrastructure template libraries per org
3. Infrastructure snapshot and restore (pause a preview's database, snapshot it, restore on next preview start for faster iteration)
4. Data provisioning beyond init scripts (managed seed datasets, snapshot-from-staging workflows)

### Phase 4: Auto-Detection And Setup

1. Automatic preview config generation based on repo structure detection (framework markers, `package.json` scripts, port hints, presence of `docker-compose.yml`)
2. Optional `lightweight` configs and broader setup auto-detection
3. One-click "add preview config" scaffolding when detection identifies a supported repo shape
4. Auto-detection of infrastructure needs (e.g., `DATABASE_URL` in `.env.example` suggests PostgreSQL)

### Phase 5: Advanced Verification And Visual Regression

Phase 1 includes agent-driven interaction, multi-viewport capture, semantic diffs, and self-verification assertions. Phase 5 extends these into automated CI/review gates and advanced capabilities.

1. **Visual regression as a review gate**: promote semantic diffs from an agent tool to an automated check that runs on every preview update, with configurable diff thresholds (e.g., "fail if >5% pixel change outside the target component"). Integrated into the review workflow as pass/fail status checks.
2. **Cross-session failure memory**: when an agent breaks the preview (build fails, runtime error, visual regression), record what file was changed, what the error was, and how it was fixed. Over time, build a per-repo knowledge base that agents consult before making similar changes. (See Future Work below for details.)
3. **Multi-page route capture**: agent tool to capture screenshots of all routes defined in a route manifest (e.g., `react-router` config or Next.js pages directory) in a single call, producing a comprehensive visual audit.
4. **Accessibility regression**: automated a11y scanning that compares the preview against the base branch preview, flagging regressions introduced by the change.
5. **Freeform drag-and-drop**: full drag-and-drop layout editing with coordinate mapping between the overlay and the DOM layout engine.
6. **Design system editor integration**: deeper integration with Figma/design tools for token import and visual consistency checking.
7. **Interaction-to-test export**: convert ephemeral recorded interactions into persistent Playwright/Cypress test files that can be committed to the repo.
8. **Preview-as-test-environment**: run the repo's actual e2e test suite (Playwright, Cypress) against the preview URL, with results surfaced in the session UI.

That work should come only after the Phase 1 verification tools are solid and validated by real usage.

## Alternatives Considered

### Same-Origin Reverse Proxy Under The Main App

- **Pro:** simplest implementation
- **Con:** unsafe for untrusted preview content in a multi-tenant browser app
- **Verdict:** reject

### Direct Host Port Exposure

- **Pro:** simple transport
- **Con:** weak auth story, firewall complexity, provider coupling
- **Verdict:** reject

### noVNC / Full Desktop Streaming

- **Pro:** renders anything
- **Con:** high bandwidth, poor UX for routine web review
- **Verdict:** out of scope for this feature

## Edge Cases

This section documents known edge cases and their mitigations. These should be addressed in MVP unless noted otherwise.

### Dev Server Binds To `localhost`

Many dev servers default to binding on `127.0.0.1`, which makes the primary service unreachable from the preview gateway proxy outside the container network namespace.

**Mitigation (MVP):** The preview manager should inject `HOST=0.0.0.0` into the **primary** service's environment by default. Most Node dev servers (Vite, Next.js, Create React App, webpack-dev-server) respect this variable. The injected value should appear in the resolved config but can be overridden by the service's explicit `env` field.

For support services, `localhost` binding is fine — they are only reached by other processes inside the same sandbox. However, if a support service's readiness probe is failing, the preview manager should note that the probe is hitting `localhost`, which is expected to work for intra-sandbox services, and focus diagnostics on process crashes or startup errors instead.

For dev servers that do not respect the `HOST` variable, the preview manager should detect the condition (readiness probe fails on `0.0.0.0:$PORT` but the process is still running) and surface a diagnostic: "Dev server may be bound to localhost. Add `HOST=0.0.0.0` to your dev server configuration or preview config env."

### Static Asset Caching

All preview traffic is proxied through the gateway. For asset-heavy apps (images, fonts, large JS bundles), this creates redundant round-trips on every page load.

**Mitigation (MVP):** The preview gateway should maintain a per-preview in-memory LRU cache (default: 50 MB cap) for responses that meet all of these criteria:

- HTTP GET request
- Response status 200
- Response includes a `Cache-Control` header with `max-age > 0`, or the request path matches a static file extension (`.js`, `.css`, `.png`, `.jpg`, `.gif`, `.svg`, `.woff2`, `.woff`, `.ttf`)
- Response body is smaller than 5 MB

Cache entries are evicted when the preview stops. This is a simple optimization that avoids the complexity of a shared cache layer while meaningfully reducing latency for repeat asset loads.

### Navigation State On Restart

When a preview restarts (manual restart, process recycling, or crash recovery), the user's in-app navigation state (e.g., they were on `/settings/profile`) is lost because the dev server restarts at the root.

**Mitigation (MVP):** The preview gateway should track the last proxied request path per preview instance in a `last_path` field on `preview_instances`. After a restart, once the readiness probe passes, the gateway should issue a 302 redirect to the stored `last_path` on the next incoming request. This is best-effort — client-side routing state stored in memory or React context will still be lost — but it handles the common case of URL-driven navigation.

### Concurrent Viewers

Multiple users can view the same preview simultaneously. Each gets a separate bootstrap token and preview access session, but they share the same preview process.

**Mitigation (MVP):** This is expected behavior and should work without special handling. Document that:

- Concurrency caps count preview *instances*, not *viewers*
- If one viewer's actions cause the preview app to change state (e.g., filling out a form), other viewers see the result
- The preview process is not duplicated per viewer

### Preview App Writes To Filesystem

The previewed app may write files to the sandbox filesystem (e.g., SQLite databases, file uploads, generated assets). These writes share the same filesystem as the agent.

**Mitigation (MVP):** Document this as expected behavior. If both the agent and preview are active simultaneously, add a subtle warning in the UI: "The preview and agent share the same filesystem. Changes made by the preview are visible to the agent and vice versa." No filesystem isolation between agent and preview in MVP.

### Port Conflicts Between Services

In multi-service configs, two services could be configured to use the same port, or a service could conflict with a port already in use by the sandbox runtime.

**Mitigation (MVP):** The preview manager must validate that all service ports in a config are unique before starting any processes. If a conflict is detected, the preview transitions to `failed` immediately with a diagnostic: "Port {port} is used by both {service_a} and {service_b}. Each service must use a unique port." The allowed port range (e.g., 1024-65535) should be enforced, and ports used by sandbox infrastructure should be reserved.

### Credential Leakage Between Services

All services in a config share a sandbox. Even though `inject_into` scopes which services receive credential env vars, a malicious or buggy process could read another process's environment via `/proc/<pid>/environ`.

**Mitigation (MVP):** This is an accepted risk in the current trust model. The trust boundary is the sandbox, not the individual process. The `inject_into` mechanism is a defense-in-depth measure that prevents accidental exposure (e.g., a frontend dev server logging all env vars on startup) but does not protect against intentional reads. This is acceptable because:

- Connected previews require admin approval of the config
- All launch fields are pinned to the base branch for connected previews (preventing diff-injected processes from reading secrets)
- The sandbox itself runs in gVisor with dropped capabilities

If stronger per-service isolation is needed in the future, it requires sidecar containers (Phase 3).

### Infrastructure Container Cleanup On Failure

If a preview fails during the Init or Start phase, infrastructure containers may be left running without any application services consuming them.

**Mitigation (MVP):** The preview manager must ensure infrastructure containers are torn down whenever the preview transitions to `failed`, `stopped`, or `expired`, regardless of which phase the failure occurred in. Use a deferred cleanup pattern: register infrastructure containers for cleanup at provisioning time, and execute cleanup on any terminal state transition. If cleanup itself fails (e.g., Docker API error), log the failure and rely on a background sweeper that periodically finds orphaned preview infrastructure containers (identified by label) and removes them.

### Init Script Runs Against Empty Database

The `init_script` runs once when the preview starts. If the preview is restarted (manual restart, process recycling), the infrastructure container is also restarted from scratch and the init script runs again. This means every restart pays the full seed cost.

**Mitigation (MVP):** This is accepted behavior. For large seed scripts, document that startup time will be longer. In Phase 3, infrastructure snapshot/restore can eliminate re-seeding by snapshotting after the first successful init and restoring on subsequent starts.

### Screenshot Capture Timing

Screenshots captured immediately after an HMR update may show intermediate rendering states (loading spinners, partially hydrated components, layout shifts).

**Mitigation (MVP):** The 2-second stabilization delay after detecting an HMR WebSocket message handles most cases. For pages with async data loading, the agent can use `preview_screenshot` with a custom `Delay` parameter to wait longer. The auto-screenshot system should also check for the `load` and `networkidle` states via the headless browser before capturing. If the page has visible loading indicators (detected via heuristics like spinner CSS classes or `aria-busy`), extend the delay up to 5 seconds.

### Design Mode Coordinate Mismatch

The Design Mode overlay captures click coordinates relative to the iframe viewport. If the reviewer's browser viewport size differs from the headless browser's viewport size, the coordinates may resolve to the wrong element.

**Mitigation (MVP):** The frontend should send the iframe's actual rendered dimensions alongside the click coordinates. The Preview Inspector should set the headless browser's viewport to match before calling `elementFromPoint`. The overlay should also handle iframe scrolling — if the reviewer has scrolled within the iframe, the click coordinates must be offset by the scroll position. The headless browser should be scrolled to the same position before element resolution.

### Wildcard Domain Hostname Abuse

Per-preview hostnames use wildcard DNS (`*.preview.143.dev`). A compromised or malicious preview could have a hostname like `login.preview.143.dev`, which could be used for internal phishing.

**Mitigation (MVP):** Preview IDs should be opaque UUIDs, not user-controllable strings. The preview manager must never allow user-supplied preview hostnames. Since preview IDs are system-generated UUIDs, hostnames will look like `a1b2c3d4.preview.143.dev`, which is not useful for phishing.

## Open Questions

1. Should preview bootstrap state live in a signed JWT, a DB-backed session, or both?
2. Do we want the preview gateway inside the main Go binary or as a separate service on the preview origin?
3. Is Phase 1 limited to `MODE=all`, or do we also support split API/worker deployments immediately?
4. Should org-level preview env configs be stored in versioned settings tables?
5. Do we want a "warm review sandbox" snapshot so preview can be restarted after a short stop without rerunning the agent?
6. Should the agent's `preview_screenshot` tool be automatically called after every code change, or should the agent decide when to use it? Auto-capture creates a richer timeline but adds latency to the agent's edit loop.
7. Should Design Mode visual edits support an "undo" stack (revert the last style tweak before applying to code), or is the two-phase model (visual preview → agent applies) sufficient?
8. For the `postMessage` bridge used by Visual Editing, should we limit the whitelist to a fixed set of ~30 CSS properties, or allow any property that is not on a denylist? The allowlist is safer; the denylist is more flexible.
9. Should the headless browser (Preview Inspector) use Playwright or Puppeteer? Playwright has better cross-browser support and a more modern API, but Puppeteer has lower overhead for Chromium-only use.
10. How should screenshot storage scale for orgs with high preview volume? Blob storage with aggressive TTL (24h post-session) keeps costs low, but some orgs may want longer retention for audit purposes.
11. Should PR preview artifacts (screenshot timeline, visual diff) be retained longer than the standard 24h for open PRs? The current proposal is "until PR is merged/closed, up to 7 days after last session" — is this sufficient for slow-moving reviews?
12. Should filesystem snapshot cache size be org-configurable or fixed platform-wide? More cache = faster starts for diverse repos, but higher disk cost per worker.
13. Should filesystem snapshots be worker-local only (fast but not shared) or also uploaded to blob storage (shared across workers but slower restore)? Phase 1 proposes worker-local only; Phase 2 adds cross-worker sharing.
14. For the PR "Launch Preview" button, should it be a GitHub Actions-style button (requires GitHub App permissions to handle the click) or a simple deep link to the 143 session page? The deep link is simpler but requires the user to be logged into 143.

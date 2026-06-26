# Preview Environments

A preview is a live, iframed view of your app running inside a 143 session. When an agent edits your frontend, you see the result next to the diff instead of having to pull the branch locally.

This guide is specifically about the `preview` section inside `.143/config.json`. For the repo-level file overview, including `bootstrap` and `validation`, see [Repo config](./repo-config.md).

This guide covers how to add preview support to a repo. For the underlying architecture (preview gateway, trust split, isolation model), see [`design/implemented/44-sandbox-preview-server.md`](design/implemented/44-sandbox-preview-server.md).

## Dogfood preview

143 ships its own `.143/config.json`, `.143/preview-start.sh`, and `.143/seed.sql` so a reviewer can spin up 143 inside 143 to click through the UI. This is the environment exposed at `143.dev`.

**How to launch it locally:**

1. Boot the local stack (`docker compose up` or the repo-specific make target).
2. Open a session against the 143 repo (or anything on `main`).
3. Click **Start Preview**.

**Demo credentials** (the admin login is shown on the login page when `DEMO_MODE=true`):

- Email: `preview-admin@143.dev`
- Password: `preview`

Additional seeded users use the same password:

| Email | Role |
|---|---|
| `preview-member@143.dev` | `member` |
| `preview-builder@143.dev` | `builder` |
| `preview-viewer@143.dev` | `viewer` |

The banner renders whatever `DEMO_EMAIL` / `DEMO_PASSWORD` the server was started with (defaults match the values above and the seeded admin in `.143/seed.sql`). If you override those env vars, regenerate the bcrypt hash in `seed.sql` in lockstep or the banner will point at credentials that don't log in.

**What you can and cannot do in the dogfood:**

| Works | Does not work |
|---|---|
| Browse seeded sessions / PR previews / activity | **Start session** (no Docker socket — the button will error) |
| Sign in as the seeded admin | **Connect GitHub** on Settings → Integrations (no OAuth app; button is hidden) |
| Open the session detail, messages, logs | **Import repositories** (no GitHub App; install-redirect no-ops) |
| View the seeded PR preview panel and its linked PR | **Comment on a PR / retry CI / merge** (all route through the GitHub API) |
| Navigate projects, sessions list, activity feed | **Start Preview** from a new session (needs a live sandbox) |

The UI is populated by fixed rows in `.143/seed.sql`; the preview system itself is not actually running underneath them. This is a deliberate tradeoff — giving the dogfood a Docker socket would expand the attack surface far beyond what's warranted for a public demo. If you need a real end-to-end test, run 143 on your own machine with a configured GitHub App.

Set `DEMO_MODE=true` on the server when launching a dogfood environment. This enables the login-page credential banner and short-circuits GitHub client construction so stubbed handlers return cleanly instead of 500-ing.

**Keeping the seed healthy:**

Run this before merging changes to `.143/seed.sql` or migrations that can affect the demo workspace:

```bash
make demo-seed-check
```

The check creates a temporary sibling database, runs migrations, applies `.143/seed.sql` twice, verifies required seeded rows, checks idempotency, scans for unsafe demo content, and drops the temporary database. It refuses production-looking database URLs unless explicitly overridden.

To apply the same seed to a long-lived demo database:

```bash
DEMO_MODE=true \
ALLOW_DEMO_SEED_APPLY=true \
DEMO_SEED_DATABASE_URL=postgres://... \
make demo-seed-apply
```

`demo-seed-apply` runs migrations by default and refuses production-looking targets or databases with non-demo organizations unless explicitly overridden. It is for public/demo deployments or intentional local throwaway databases, not production.

The dogfood frontend runs as a production Next build inside the preview (`npm run build`, then the generated standalone server). The launch script stages `.next/static` and `public` into the standalone output before booting so generated CSS, JavaScript chunks, fonts, and public images are served by the production server. Avoid `next dev` here: its HMR stream is not useful for reviewers and can interact badly with the preview gateway's HTML instrumentation on App Router pages.

**How the dogfood handles `SESSION_SECRET`:** The preview runs inside a 143 session sandbox, which has no access to sops-encrypted production secrets, so the secret is generated at boot from `/dev/urandom` and cached at `/tmp/143-preview/session_secret`. Server restarts within the same sandbox reuse the cached value, so a reviewer stays signed in. A full sandbox recycle generates a fresh secret — reviewers just re-sign-in with the public demo credentials.

**Why `MODE=api` and not `MODE=all`:** The dogfood sandbox has no Docker socket, so the background worker mode (which spawns session sandboxes and previews) cannot function. Running it would only produce worker-loop errors in the logs. Any UI that polls job status will therefore show the seeded snapshot forever — no background processing advances it.

## Quickstart

Add `.143/config.json` at the root of your repo. Preview config lives under the top-level `preview` key because this file also carries other repo-level settings:

```json
{
  "preview": {
    "name": "my-app",
    "primary": "app",
    "services": {
      "app": {
        "command": ["npm", "run", "dev"],
        "port": 3000,
        "ready": { "http_path": "/" }
      }
    },
    "credentials": { "mode": "none" },
    "network": { "mode": "managed" }
  }
}
```

That's it. Open a session against the repo, click **Start Preview**, and the panel proxies to `http://localhost:3000` inside the sandbox.

## How Previews Work

When you click Start Preview on a session:

1. The preview manager loads the repo's `.143/config.json` and reads its `preview` section.
2. It provisions any declared [infrastructure](#infrastructure) (Postgres, Redis, MySQL) as sidecar containers.
3. It runs `preview.install`, if configured, before any app service starts.
4. It starts each declared service inside the sandbox as an OS process, in dependency order.
5. Each service must pass its readiness probe before the next starts.
6. Once all services are ready, the preview is reachable from the session page via an isolated domain (`<preview-id>.preview.143.dev`) — **not** the 143 app origin.

Services share the sandbox's filesystem and `localhost` network namespace, so they can talk to each other directly. Nothing in the preview shares cookies, storage, or API origin with the 143 app.

## Config Reference

### Preview section fields

| Field | Required | Notes |
|-------|----------|-------|
| `preview.version` | no | Optional version marker. |
| `preview.name` | no | Human label shown in the UI. Recommended. |
| `preview.primary` | yes for multi-service | Key from `preview.services` that the gateway proxies browser traffic to. |
| `preview.install` | no | Optional platform-managed dependency install phase. See [Install](#install). |
| `preview.services` | yes for multi-service | Map of service name → [service config](#services). |
| `preview.infrastructure` | no | Map of infra name → [infrastructure config](#infrastructure). Max 2. |
| `preview.credentials` | yes | [Credential config](#credentials). Use `{"mode": "none"}` if no secrets needed. |
| `preview.network` | yes | [Network config](#network). Use `{"mode": "managed"}` for the default sandbox egress policy. |
| `preview.resources` | no | CPU, memory, and ephemeral disk requests/limits. See [Resources](#resources). |
| `preview.progressive` | no | When `true`, a multi-service preview can become partially ready as soon as the primary service is ready. |
| `preview.command` | yes for single-service | Single-service shorthand only. |
| `preview.cwd` | no | Single-service shorthand only. |
| `preview.port` | yes for single-service | Single-service shorthand only. |
| `preview.env` | no | Single-service shorthand only. |
| `preview.ready` | no | Single-service shorthand only. Defaults to `/` and `90` seconds when omitted. |

143 supports two valid preview shapes:

- single-service shorthand using top-level `command` / `port` / `ready`
- multi-service config using `primary` + `services`

Do not mix both shapes in the same config.

### Single-service shorthand

This is valid when your preview is just one service:

```json
{
  "preview": {
    "name": "frontend",
    "command": ["npm", "run", "dev"],
    "cwd": "frontend",
    "port": 3000,
    "ready": { "http_path": "/" },
    "credentials": { "mode": "none" },
    "network": { "mode": "managed" }
  }
}
```

143 normalizes this internally into a single-entry `services` map.

### Resources

Use `preview.resources` when a repo needs more than the topology defaults. The names match Kubernetes resource names:

```json
{
  "preview": {
    "resources": {
      "requests": {
        "cpu": "500m",
        "memory": "768Mi",
        "ephemeral-storage": "5Gi"
      },
      "limits": {
        "cpu": "1",
        "memory": "1Gi",
        "ephemeral-storage": "10Gi"
      }
    }
  }
}
```

`limits` win when present. If a limit is omitted, the matching request is used. If both are omitted, 143 uses topology defaults. CPU accepts Kubernetes-style values such as `500m`, `1`, or `1.5`. Memory and `ephemeral-storage` use the same byte-size parser and accept values such as `512Mi`, `1Gi`, `500mb`, or `5gb`.

Hard caps are `2` CPU cores, `1Gi` memory, and `10Gi` ephemeral storage. Requests must be less than or equal to limits.

### Install

Use `preview.install` when a preview needs dependencies before services can boot. This keeps dependency installation out of service `command` scripts and gives 143 a first-class cache marker, cleanup policy, timeout, and error code.

```json
{
  "preview": {
    "install": {
      "command": ["npm", "ci", "--no-audit", "--no-fund"],
      "cwd": ".",
      "lockfiles": ["package-lock.json"],
      "clean_paths": ["node_modules", "packages/*/node_modules"],
      "verify_paths": ["node_modules/.bin/next"],
      "timeout_seconds": 420
    }
  }
}
```

| Field | Type | Notes |
|-------|------|-------|
| `command` | `string[]` | Required when `install` is present. Runs before services start. |
| `cwd` | `string` | Command working directory, relative to the repo root. Defaults to repo root. |
| `lockfiles` | `string[]` | Repo-relative files included in the install cache key. Missing files fail the install phase. |
| `clean_paths` | `string[]` | Repo-relative paths or simple globs to remove before reinstalling. Nothing is auto-deleted unless listed here. |
| `verify_paths` | `string[]` | Repo-relative paths that must exist for a cached install to be reused. |
| `cache.enabled` | `boolean` | Optional. Defaults to `true`; set to `false` to opt out of dependency artifact caching. |
| `cache.paths` | `string[]` | Optional additive dependency/build cache paths to persist in addition to `clean_paths` and inferred paths. Requires `lockfiles`. |
| `cache.package_manager.enabled` | `boolean` | Optional. Defaults to `true`; set to `false` to opt out of package-manager global caching. |
| `cache.package_manager.include` | `string[]` | Optional package-manager IDs to include in addition to inference: `npm`, `pnpm`, `yarn`, `bun`, `pip`, `uv`, `poetry`, `go`. |
| `cache.package_manager.paths` | `string[]` | Optional HomeDir-relative package-manager cache paths. Absolute paths, `..`, and globs are rejected. |
| `cache.prewarm.enabled` | `boolean` | Optional per-config prewarm toggle. Runtime rollout is also gated by `PREVIEW_CACHE_PREWARM_ENABLED`. |
| `timeout_seconds` | `int` | Defaults to 420 seconds. Max 1800 seconds. |

143 computes a cache key from the install config, lockfile contents, and sandbox runtime. If the platform-owned marker under `.143/cache/preview-install/` exists and every `verify_paths` entry exists, install is skipped. Otherwise 143 removes only `clean_paths`, runs `command`, and writes the marker only after the command succeeds.

Declaring `verify_paths` also unlocks the cold-start fast path: when the marker is absent but a dependency artifact cache hit matches the exact install command, lockfile contents, and sandbox image, 143 restores the cached artifacts and — if every `verify_paths` entry exists afterwards — writes the marker and skips `command` entirely. Without `verify_paths`, cold starts always run `command`.

Because the cache key contains no commit, install output is reused across commits whenever the install config and lockfiles are unchanged. `preview.install.command` must therefore produce output that depends only on those inputs — dependency installation, not application builds. A build step inside install that reads source files would be skipped at a newer commit and serve stale artifacts; keep source-dependent builds in the service `command`, where they run on every start.

Session previews also use dependency artifact and package-manager global caches when object storage is configured. The dependency artifact path set is WorkDir-relative:

```text
clean_paths + cache.paths + inferred paths from known dependency files
```

Platform-owned preview cache paths are excluded from dependency caching. Do not declare `.143/cache` or any descendant as a dependency cache path. `clean_paths` may still remove broad build output before a fresh install, but those paths are not automatically safe to persist as reusable artifacts.

Initial inferred paths:

| Dependency file | Inferred cache path |
|---|---|
| `package-lock.json`, `npm-shrinkwrap.json`, `pnpm-lock.yaml`, `yarn.lock`, `bun.lock`, `bun.lockb` | `node_modules` |
| `poetry.lock`, `uv.lock`, `Pipfile.lock`, `pdm.lock`, `requirements.txt`, `requirements-dev.txt` | `.venv` |
| `go.mod`, `go.sum` | `vendor` |

Inference is relative to the lockfile directory, so `frontend/package-lock.json` infers `frontend/node_modules`, `services/api/poetry.lock` infers `services/api/.venv`, and `go.mod` infers `vendor`. `cache.paths` is additive and is useful for specific repo-local build paths such as `.next/cache` or `.turbo/cache`.

Package-manager global caches are separate from dependency artifacts and restore relative to the sandbox `HomeDir`, not the repo `WorkDir`.

| Package manager | HomeDir-relative cache paths |
|---|---|
| npm | `.npm` |
| pnpm | `.local/share/pnpm/store` |
| yarn | `.yarn/berry/cache` |
| bun | `.bun/install/cache` |
| pip | `.cache/pip` |
| uv | `.cache/uv` |
| poetry | `.cache/pypoetry` |
| go | `go/pkg/mod`, `.cache/go-build` |

Use `cache.package_manager.paths` only for additional package-manager caches under the sandbox home directory, for example `.cache/custom-package-manager`. 143 rejects broad or sensitive home paths such as `.`, `.ssh`, `.gnupg`, `.codex`, `.claude`, `.config/gh`, and `.143`.

Do not cache source directories, secret files, `.git`, `.143/cache`, or preview install markers. Large caches are best handled by narrowing `cache.paths` to high-value dependency/build-cache directories or setting `cache.enabled: false` for that preview config. Raising `preview.resources.limits.ephemeral-storage` can help final extracted dependencies fit, but it should not be required for cache restore correctness and is capped by platform policy. `requirements.txt` can be unsafe when it contains unpinned ranges such as `flask>=2.0`; pin dependencies, use a real lockfile, or set `cache.enabled: false`. Mutable preview image tags such as `latest` can also produce stale caches; prefer immutable digests or versioned tags.

Deployment operators enable shared dependency caching by setting `PREVIEW_DEPENDENCY_CACHE_BUCKET`. Package-manager global cache support defaults on when the shared cache is configured and can be disabled with `PREVIEW_PACKAGE_MANAGER_CACHE_ENABLED=false`. Worker-local L1 cache defaults to `/var/cache/143/preview-dependency-cache` and is host-mounted by the production worker compose file; set `PREVIEW_DEPENDENCY_CACHE_LOCAL_DIR=off` to disable only the worker-local L1 layer while keeping shared object-storage restores enabled. Low-priority prewarming is disabled by default; enable it with `PREVIEW_CACHE_PREWARM_ENABLED=true`.

Npm workspace example:

```json
{
  "preview": {
    "install": {
      "command": ["npm", "ci", "--no-audit", "--no-fund"],
      "lockfiles": ["package-lock.json"],
      "clean_paths": ["node_modules", "packages/*/node_modules"],
      "verify_paths": ["node_modules/.bin/next"]
    }
  }
}
```

Pnpm example:

```json
{
  "preview": {
    "install": {
      "command": ["pnpm", "install", "--frozen-lockfile"],
      "lockfiles": ["pnpm-lock.yaml"],
      "clean_paths": ["node_modules", "apps/*/node_modules", "packages/*/node_modules"],
      "verify_paths": ["node_modules/.modules.yaml"]
    }
  }
}
```

Opt out when a repo cannot safely reuse dependency artifacts:

```json
{
  "preview": {
    "install": {
      "command": ["npm", "ci"],
      "lockfiles": ["package-lock.json"],
      "clean_paths": ["node_modules"],
      "cache": { "enabled": false }
    }
  }
}
```

If dependencies look stale, change the lockfile so the cache key changes, or temporarily opt out with `cache.enabled: false`.

### Services

Each service runs as an OS process inside the shared sandbox container.

| Field | Type | Notes |
|-------|------|-------|
| `command` | `string[]` | argv — executed directly, not through a shell. Use `["sh", "-c", "..."]` if you need shell features. |
| `cwd` | `string` | Working directory, relative to the repo root. Must stay inside the repo. |
| `port` | `int` | Port the service binds to. Must be 1024–65535. |
| `env` | `object` | Non-secret env vars. For secrets, see [Credentials](#credentials). |
| `ready` | `object` | Readiness probe. Preview is `ready` only after all services pass theirs. |
| `ready.http_path` | `string` | Path to GET. A 2xx or 3xx response counts as ready. |
| `ready.timeout_seconds` | `int` | Max wait before the service is marked failed. Defaults to 90. |

Constraints:

- Max 4 services per config (1 primary + up to 3 support).
- Ports must be unique across services within a config.
- Support services start first (in declaration order); the primary starts last.

### Infrastructure

Platform-managed sidecar containers for databases/caches. Ephemeral — provisioned when the preview starts, destroyed when it stops.

```json
{
  "preview": {
    "infrastructure": {
      "db": {
        "template": "postgres-17",
        "init_script": "db/seed.sql",
        "inject_env": {
          "DATABASE_URL": "postgres://{{username}}:{{password}}@{{host}}:{{port}}/{{database}}?sslmode=disable"
        },
        "inject_into": ["server"]
      }
    }
  }
}
```

| Field | Notes |
|-------|-------|
| `template` | Platform-provided image. See [templates](#available-templates). |
| `init_script` | Path (relative to repo root) to a SQL or shell script piped into the container after it's healthy. Optional. |
| `inject_env` | Env vars constructed from placeholders and injected into services. |
| `inject_into` | Which services receive the injected env vars. Defaults to all services. |

Placeholders supported in `inject_env` values (double braces): `{{username}}`, `{{password}}`, `{{host}}`, `{{port}}`, `{{database}}`.

#### Available templates

| Template | Image | Default Port |
|----------|-------|--------------|
| `postgres-17` | `postgres:17-alpine` | 5432 |
| `postgres-16` | `postgres:16-alpine` | 5432 |
| `redis-7` | `redis:7-alpine` | 6379 |
| `mysql-8` | `mysql:8.4` | 3306 |

Credentials are auto-generated per preview and never stored. The sidecar is only reachable from the sandbox — no external network access, no mount into the repo.

### Credentials

Use `preview.credentials.mode: "none"` unless the app needs secrets (API keys, staging DB URLs).

Non-secret env vars belong in `preview.services.<svc>.env`. For secrets, an org admin creates a named **credential set** in 143's admin UI and the repo config references it:

```json
{
  "preview": {
    "credentials": {
      "mode": "managed_env",
      "credential_set": "repo-staging",
      "env": ["DATABASE_URL", "STRIPE_KEY"],
      "inject_into": ["server"]
    }
  }
}
```

- `credential_set` — the admin-created set to pull from.
- `env` — allowlist of env var names to inject. Only listed values are exposed.
- `inject_into` — which services see the values. Scoping matters — any service receiving a credential becomes a connected preview (see [Trust split](#trust-split)).

The repo never contains secret values. Agents never see them — the platform injects them at process start.

### Network

`preview.network.mode` controls sandbox egress.

- `"managed"` (default) — Only platform-approved destinations are reachable.
- `""` — Same as `"managed"`.

`preview.network.destinations` lists named managed destinations the preview may reach (e.g., a staging Postgres or a partner API). Admins configure what each name resolves to.

```json
{
  "preview": {
    "network": {
      "mode": "managed",
      "destinations": ["staging_db", "stripe_api"]
    }
  }
}
```

Any service using a destination or `credentials.mode != "none"` makes the preview **connected**. Connected previews have stricter trust rules — see below.

## Multi-Service Example

```json
{
  "preview": {
    "name": "Full Stack",
    "primary": "frontend",
    "services": {
      "frontend": {
        "command": ["npm", "run", "dev"],
        "cwd": "frontend",
        "port": 3000,
        "env": { "API_URL": "http://localhost:8080" },
        "ready": { "http_path": "/", "timeout_seconds": 120 }
      },
      "server": {
        "command": ["sh", "-c", "./bin/migrate up && ./bin/server"],
        "port": 8080,
        "env": { "LOG_LEVEL": "info" },
        "ready": { "http_path": "/health", "timeout_seconds": 90 }
      }
    },
    "infrastructure": {
      "db": {
        "template": "postgres-17",
        "init_script": "db/seed.sql",
        "inject_env": {
          "DATABASE_URL": "postgres://{{username}}:{{password}}@{{host}}:{{port}}/{{database}}?sslmode=disable"
        },
        "inject_into": ["server"]
      }
    },
    "credentials": { "mode": "none" },
    "network": { "mode": "managed" }
  }
}
```

The frontend proxies `/api/*` to the server at `localhost:8080`. The server gets `DATABASE_URL` injected and uses a shell `command` to chain `migrate` then `server` — the ready probe only passes once `server` is listening, so ordering is enforced naturally.

For frontend frameworks that bake public environment variables into the browser bundle, keep preview API calls same-origin unless you intentionally own cross-origin auth. In Next.js, unset `NEXT_PUBLIC_API_URL` (or set it to an empty string) and use a server-side rewrite such as `/api/* -> http://localhost:8080`; otherwise the preview page can read a preview-domain CSRF cookie while posting to a different API origin that does not receive that cookie.

For production preview domains such as `<preview-id>.preview.143.dev`, the public wildcard DNS must resolve to the app node and the edge proxy must be able to obtain a wildcard certificate. In 143's production setup that means:

1. `*.preview.<your-domain>` points at the app node that runs Caddy.
2. `PREVIEW_ORIGIN_TEMPLATE` is set to `https://{id}.preview.<your-domain>`.
3. Caddy is built with a DNS provider plugin and the wildcard host uses the ACME DNS challenge. For Cloudflare, provide a scoped API token with `Zone:Read` and `DNS:Edit` on the zone and set `CLOUDFLARE_API_TOKEN` in the app host env bundle.

## Platform-Injected Env

Every service receives:

| Var | Value |
|-----|-------|
| `HOST` | `0.0.0.0` — most frameworks honor this for bind address. Override in your service `env` if your app reads `HOST` for something else. |
| `ONEFORTYTHREE` | `true` — always injected by the platform. Apps can use this to detect that they are running on 143. This name is reserved; preview configs and secret bundle env outputs that declare it fail validation. |
| `ONEFORTYTHREE_ENV` | `preview` — always injected for preview runtimes. Apps should use this to skip work that is not needed to serve the preview, such as background consumers, schedulers, profilers, analytics/telemetry exporters, and expensive startup warmers. This name is reserved; preview configs and secret bundle env outputs that declare it fail validation. |
| `PREVIEW_ORIGIN` | The public URL the gateway serves this preview on, e.g. `http://<id>.preview.localhost:9090`. Set this as your app's external base URL (e.g. `BASE_URL`, `FRONTEND_URL`) so redirects and absolute links point at the preview instead of `localhost`. Overrides any user-declared value. |

## Trust Split

Preview config is untrusted repo content. Not every field is read from the same git revision.

Fields read from the **base branch** (ignore agent diffs):

- `credentials`, `network`, `infrastructure` structure, `primary`, the set of service names.

Fields read from the **session diff** (reflect agent changes):

- Per-service `command`, `cwd`, `port`, `env`, `ready`.
- `infrastructure.*.init_script` — so seed data can change alongside schema changes.
- `install` — for non-connected previews, dependency install behavior can change with the app branch.
- `resources` — for non-connected previews, bounded resource requests and limits can change with the app branch.

For connected previews (anything with `credentials.mode != "none"` or non-empty `network.destinations`), **everything** pins to the base branch. A diff can't change launch behavior when secrets are in scope. This is enforced in code, not by policy.

Practical implication: if you want the agent to be able to iterate on `command`/`port`/`env`, keep `credentials.mode` as `"none"` and use platform infrastructure instead of a managed destination.

## Limits

| Limit | Value |
|-------|-------|
| Services per config | 4 |
| Infrastructure per config | 2 |
| Idle timeout | 15 min (extended by user activity) |
| Hard TTL | 30 min (extendable to 2 hr) |
| Previews per user | 2 concurrent |
| Previews per org | 5 concurrent |
| Memory | 384Mi single-service, 768Mi multi-service, 1024Mi multi-service with infrastructure |
| CPU | 0.5 core single-service, 1 core multi-service, 2 cores multi-service with infrastructure |
| Ephemeral storage | 10Gi default and max |

## Troubleshooting

| Symptom | Likely cause |
|---------|--------------|
| `network.mode "restricted" is not supported` | Only `"managed"` or `""` are valid. |
| `primary does not reference a service` | `primary` key must match a key in `services`. |
| `port N conflicts with service X` | Two services declared the same port. Each needs a unique port inside the sandbox. |
| `ready.http_path contains invalid characters` | Path must match `/[a-zA-Z0-9/_.\-?&=%]*`. No shell metacharacters. |
| Service times out on readiness | Increase `ready.timeout_seconds`. For heavy builds, first-start can exceed 90s. |
| `PREVIEW_INSTALL_FAILED` | `preview.install.command` failed before services started. Expand startup logs for the captured install output tail. |
| OOM / exit 137 during build | The preview exceeded its memory cap while building. Reduce build parallelism, trim source maps, split build steps, or serve a production/static build instead of keeping build tooling resident. |
| OOM / exit 137 after the preview was ready | The running service exceeded its memory cap. Use `ONEFORTYTHREE_ENV=preview` in the app to disable background workers, schedulers, profilers, telemetry, and other non-serving processes. |
| `EADDRINUSE` in logs | Another service in the same config already bound that port. Ports share the sandbox's network namespace. |
| Preview works locally but not inside the sandbox | Service is binding to `127.0.0.1`. Bind to `0.0.0.0` (the gateway injects `HOST=0.0.0.0` for most frameworks). |
| Infrastructure placeholder showing as literal `{{username}}` | Double braces are required, and the name is `username` (not `user`). |
| `init_script` runs against an empty database | `init_script` runs before any app service. If your seed assumes schema, run migrations from inside a service `command` (see the multi-service example above). |

## FAQ

**Can I use Docker Compose?** No. Each service runs as a process in the same sandbox, not as a separate container. This keeps the transport provider-agnostic (Docker, E2B, etc.) and cheaper.

**Can I add a custom infrastructure image?** Not in MVP. Use a managed destination to reach an external staging instance, or stick to the platform templates.

**How do I test config changes?** Commit `.143/config.json` and start a new session. There's no dry-run yet — invalid configs surface as a `PREVIEW_START_FAILED` error with the validation message.

**Can I see npm's full debug log?** 143 captures the preview process output tail in `preview_logs`, not package-manager internal files such as `/home/sandbox/.npm/_logs/...`. If you need that file in the preview UI, make the repo's install command print it on failure.

**Will increasing `ephemeral-storage` fix `/var/tmp` build failures?** Not always. The disk limit applies to the container root filesystem. If a tool writes to a tmpfs-backed path, configure it to use rootfs-backed cache paths instead, for example `GOTMPDIR` and `GOCACHE` for Go builds.

**Does the preview use my production secrets?** No. Secrets come from admin-configured credential sets, never from the repo or agent. Without a `credentials` block, the preview has no secrets at all.

**What if my app needs to know its public URL?** For most frameworks, relative URLs work. When an app needs an absolute origin, use the platform-injected `PREVIEW_ORIGIN` env var as the external base URL for the preview so redirects and absolute links resolve back to the isolated preview host instead of `localhost`.

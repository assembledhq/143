# Repo Config

`.143/config.json` is the repo-level configuration file for 143.

Put it at the root of your repository:

```text
your-repo/
  .143/
    config.json
```

This file lets you tell 143 how to:

- start a live preview of your app
- install supported sandbox tools before agent work
- run bootstrap commands before agent work
- run extra deterministic validation commands during validation

Think of it as the repo's contract with 143: if someone opens a session against the repo, this is the file 143 reads to understand how that repo should behave.

## Before you start

Keep these rules in mind:

- The file must be valid JSON. No comments, no trailing commas.
- Commit it to the repo so 143 can read it in sessions.
- Never put secrets directly in the file. Use managed preview credentials instead.
- Use `dependencies` only for supported tools that need to be available on `PATH` before bootstrap or validation commands run.
- Use `bootstrap` and `validation` only for deterministic setup and checks.
- Use `preview` only when you want a live app preview in the session UI.

## Quickstart

If you only want validation setup, start here:

```json
{
  "dependencies": {
    "golangci-lint": "2.10.1"
  },
  "bootstrap": {
    "commands": ["npm ci"]
  },
  "validation": {
    "commands": ["npm run lint:js", "npm run test -- --runInBand"]
  }
}
```

If you also want a preview:

```json
{
  "preview": {
    "name": "web",
    "primary": "web",
    "services": {
      "web": {
        "command": ["npm", "run", "dev"],
        "port": 3000,
        "ready": { "http_path": "/" }
      }
    },
    "credentials": { "mode": "none" },
    "network": { "mode": "managed" }
  },
  "bootstrap": {
    "commands": ["npm ci"]
  },
  "validation": {
    "commands": ["npm run lint:js"]
  }
}
```

## How To Think About The File

There are four top-level sections today:

- `preview`: how 143 starts and routes a live preview
- `dependencies`: supported tools for 143 to install before agent work
- `bootstrap`: commands to prepare the workspace
- `validation`: extra commands to run during validation

You can use any one of them on its own, or combine them in a single file.

## Common Patterns

### Frontend app with preview

```json
{
  "preview": {
    "name": "frontend",
    "primary": "frontend",
    "install": {
      "command": ["npm", "ci", "--no-audit", "--no-fund"],
      "lockfiles": ["frontend/package-lock.json"],
      "clean_paths": ["frontend/node_modules"],
      "verify_paths": ["frontend/node_modules/.bin/next"]
    },
    "services": {
      "frontend": {
        "command": ["npm", "run", "dev"],
        "cwd": "frontend",
        "port": 3000,
        "ready": { "http_path": "/", "timeout_seconds": 120 }
      }
    },
    "credentials": { "mode": "none" },
    "network": { "mode": "managed" }
  }
}
```

Use this when the repo has a single web app and no extra local services.

### Monorepo with install + lint

```json
{
  "bootstrap": {
    "commands": ["pnpm install --frozen-lockfile"]
  },
  "validation": {
    "commands": ["pnpm lint", "pnpm test"]
  }
}
```

Use this when the repo does not need preview support, but does need predictable setup and checks.

### Go repo with pinned golangci-lint

```json
{
  "dependencies": {
    "golangci-lint": "2.10.1"
  },
  "validation": {
    "commands": ["golangci-lint run ./...", "go test ./..."]
  }
}
```

Use this when the repo's validation expects a specific linter version that is not part of the base sandbox image.

### Full-stack preview with a local Postgres sidecar

```json
{
  "preview": {
    "name": "full-stack",
    "primary": "frontend",
    "services": {
      "frontend": {
        "command": ["npm", "run", "dev"],
        "cwd": "frontend",
        "port": 3000,
        "env": {
          "API_URL": "http://localhost:8080"
        },
        "ready": { "http_path": "/" }
      },
      "server": {
        "command": ["go", "run", "./cmd/server"],
        "port": 8080,
        "ready": { "http_path": "/healthz" }
      }
    },
    "infrastructure": {
      "db": {
        "template": "postgres-17",
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

Use this when your app needs a database but you do not want to point previews at a shared staging database.

## Choosing The Right Section

Use `dependencies` when a validation or bootstrap command needs a supported tool installed into the sandbox before it runs.

Good examples:

- `"golangci-lint": "2.10.1"`

Use `bootstrap` when the repo needs a one-time setup step before normal work can succeed.

Good examples:

- `npm ci`
- `pnpm install --frozen-lockfile`
- `bundle install`

Use `preview.install` when dependency installation is needed specifically before preview services start.

Good examples:

- `npm ci --no-audit --no-fund`
- `pnpm install --frozen-lockfile`

Use `validation` when you want extra deterministic checks as part of validation.

Good examples:

- `npm run lint`
- `go test ./...`
- `cargo test`

Use `preview` when seeing the running app in the session UI matters.

Good examples:

- frontend-heavy work
- full-stack flows that are easier to verify in-browser
- repos where reviewers need to click through the result

## Good Defaults

If you're unsure, these defaults are usually right:

- Prefer `dependencies` for supported tool binaries that 143 can install consistently.
- Start with `bootstrap.commands` if installs are required.
- Prefer `preview.install` over service `command` scripts for preview dependency installs.
- Add `validation.commands` for fast, deterministic checks.
- Keep preview credentials as `{ "mode": "none" }` unless secrets are actually required.
- Use platform-managed infrastructure before wiring previews to shared staging systems.
- Keep preview configs simple: one service if you can, multiple only when you need them.

## Common Mistakes

- Putting secrets directly in `env` or in the config file itself
- Using `"latest"` or blank strings for dependency versions
- Using blank command strings in `bootstrap.commands` or `validation.commands`
- Reusing the same port across preview services
- Setting `cwd` or `init_script` to a path outside the repo
- Forgetting that preview config belongs under the top-level `preview` key
- Running `npm install` or `pnpm install` inside every preview service command instead of using `preview.install`

## Related Guides

- For preview behavior, trust rules, and examples: [Preview environments](./previews.md)

## API Reference

This section describes the current config surface supported by the repo config parser and preview parser.

### Top-Level Shape

```json
{
  "preview": { "...": "optional" },
  "dependencies": { "...": "optional" },
  "bootstrap": { "...": "optional" },
  "validation": { "...": "optional" }
}
```

| Field | Type | Required | Notes |
|---|---|---|---|
| `preview` | object | no | Required only if you want preview support. |
| `dependencies` | object | no | Optional supported sandbox tool installs, keyed by tool name. |
| `bootstrap` | object | no | Optional repo setup commands. |
| `validation` | object | no | Optional extra validation commands. |

### `dependencies`

```json
{
  "dependencies": {
    "golangci-lint": "2.10.1"
  }
}
```

| Field | Type | Required | Notes |
|---|---|---|---|
| `dependencies.<tool>` | `string` | yes | Exact version pin for a supported tool. |

Rules:

- Supported tools today: `golangci-lint`.
- Dependency names and versions are trimmed.
- Blank versions and `"latest"` are rejected.
- Unknown dependency names are logged and skipped so one typo does not abort the session.

### `bootstrap`

```json
{
  "bootstrap": {
    "commands": ["npm ci"]
  }
}
```

| Field | Type | Required | Notes |
|---|---|---|---|
| `bootstrap.commands` | `string[]` | no | Command list. Each item must be a non-empty string after trimming whitespace. |

Rules:

- Blank strings are rejected.
- Leading and trailing whitespace is trimmed.
- Keep commands deterministic and safe to run repeatedly.

### `validation`

```json
{
  "validation": {
    "commands": ["npm run lint:js", "npm test"]
  }
}
```

| Field | Type | Required | Notes |
|---|---|---|---|
| `validation.commands` | `string[]` | no | Extra deterministic validation commands. Each item must be a non-empty string after trimming whitespace. |

Rules:

- Blank strings are rejected.
- Leading and trailing whitespace is trimmed.
- Prefer fast, deterministic checks over long-running or flaky commands.

### `preview`

Preview config lives inside the top-level `preview` key.

This guide covers the repo-level shape of the file. The full preview-specific reference lives in [Preview environments](./previews.md).

143 supports two preview shapes:

- single-service shorthand
- multi-service config

The multi-service shape is the normalized internal format.

### `preview`: single-service shorthand

```json
{
  "preview": {
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
      "mode": "managed"
    }
  }
}
```

In this form, 143 treats the preview as a single service and normalizes it internally into a one-entry `services` map.

### `preview`: multi-service shape

```json
{
  "preview": {
    "name": "full-stack",
    "primary": "frontend",
    "services": {
      "frontend": {
        "command": ["npm", "run", "dev"],
        "cwd": "frontend",
        "port": 3000,
        "ready": {
          "http_path": "/",
          "timeout_seconds": 120
        }
      },
      "backend": {
        "command": ["go", "run", "./cmd/server"],
        "port": 8080,
        "ready": {
          "http_path": "/healthz"
        }
      }
    },
    "credentials": {
      "mode": "none"
    },
    "network": {
      "mode": "managed"
    }
  }
}
```

### `preview` fields

| Field | Type | Required | Notes |
|---|---|---|---|
| `preview.version` | string | no | Optional version marker. Accepted by the parser; useful for explicit config revisions. |
| `preview.name` | string | no | Human-readable label. Recommended. |
| `preview.primary` | string | yes for multi-service | Must match a key in `preview.services`. |
| `preview.install` | object | no | Optional dependency install phase that runs before services. |
| `preview.services` | object | yes for multi-service | Map of service name to service config. |
| `preview.infrastructure` | object | no | Map of infrastructure name to infrastructure config. |
| `preview.credentials` | object | yes | Use `{ "mode": "none" }` when no secrets are needed. |
| `preview.network` | object | yes | Use `{ "mode": "managed" }` for the default behavior. |
| `preview.resources` | object | no | Optional CPU, memory, and `ephemeral-storage` requests/limits using Kubernetes-style quantity strings. |
| `preview.progressive` | boolean | no | Opt-in progressive readiness for multi-service previews. |
| `preview.command` | `string[]` | yes for single-service | Single-service shorthand only. |
| `preview.cwd` | string | no | Single-service shorthand only. |
| `preview.port` | number | yes for single-service | Single-service shorthand only. |
| `preview.env` | object | no | Single-service shorthand only. |
| `preview.ready` | object | no | Single-service shorthand only. Defaults to `http_path: "/"` and `timeout_seconds: 90` when omitted in single-service mode. |

Rules:

- Use either single-service shorthand or `services`, not both.
- At least one service is required.
- `primary` must point to a real service.
- Maximum 4 services per preview config.
- Maximum 2 infrastructure entries per preview config.

### `preview.install`

```json
{
  "preview": {
    "install": {
      "command": ["pnpm", "install", "--frozen-lockfile"],
      "cwd": ".",
      "lockfiles": ["pnpm-lock.yaml"],
      "clean_paths": ["node_modules", "apps/*/node_modules", "packages/*/node_modules"],
      "verify_paths": ["node_modules/.modules.yaml"],
      "timeout_seconds": 420
    }
  }
}
```

| Field | Type | Required | Notes |
|---|---|---|---|
| `preview.install.command` | `string[]` | yes | argv for the install command. |
| `preview.install.cwd` | string | no | Command working directory, relative to repo root. Defaults to repo root. |
| `preview.install.lockfiles` | `string[]` | no | Repo-relative files included in the cache key. |
| `preview.install.clean_paths` | `string[]` | no | Repo-relative paths or simple globs to remove before reinstalling. 143 never deletes undeclared paths. |
| `preview.install.verify_paths` | `string[]` | no | Repo-relative paths that must exist before a cached install can be reused. |
| `preview.install.cache.enabled` | boolean | no | Defaults to true. Set to false to disable dependency artifact restore/save. |
| `preview.install.cache.paths` | `string[]` | no | Additive repo-relative dependency/build cache paths, such as `.next/cache`, `.pnpm-store`, or `.turbo/cache`. Requires `lockfiles`. |
| `preview.install.timeout_seconds` | number | no | Defaults to 420. Max 1800. |

Use this instead of putting package-manager installs in `preview.services.*.command`. 143 writes a platform-owned success marker under `.143/cache/preview-install/` only after the command exits successfully. If the marker is missing, lockfile/config hash changes, or a verify path is missing, 143 removes only `clean_paths` and reruns the install.

Session preview dependency caching is default-on when `lockfiles` and effective cache paths exist. Effective paths are `clean_paths + cache.paths + inferred paths from known dependency files`. JavaScript lockfiles infer `node_modules`, Python lockfiles infer `.venv`, and `go.mod`/`go.sum` infer `vendor`, relative to the lockfile directory.

Never cache source directories, secret files, `.git`, or `.143/cache/preview-install`. Repos using unpinned `requirements.txt` entries or mutable preview image tags should pin inputs or opt out with `cache.enabled: false`.

For the nested preview reference, use [Preview environments](./previews.md). That guide owns:

- `services`
- `install`
- `ready`
- `infrastructure`
- `credentials`
- `network`
- trust split and connected preview behavior
- limits, troubleshooting, and preview-specific FAQ

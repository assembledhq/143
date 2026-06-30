# Public Demo Environment

The public demo is a separate, read-only deployment for `demo.143.dev`. It uses the same server/frontend images as production, but runs a deliberately reduced one-host stack:

- Caddy
- frontend
- API in `MODE=api`
- local Postgres
- local Redis
- migrate job
- demo seed job

It does not run workers, sandboxes, Chrome, preview gateway wildcard routing, GitHub App credentials, or LLM keys.

## Demo Auth Contract

`DEMO_MODE=true` enables direct demo entry through:

```text
POST /api/v1/auth/demo
```

The request has no body and never accepts or exposes a password. It signs in `DEMO_ENTRY_EMAIL`, which defaults to `preview-viewer@143.dev`, and requires that user to be a viewer in the seeded demo org.

`GET /api/v1/auth/providers` advertises demo entry with `demo: true` and `demo_read_only`, but never returns `demo_email` or `demo_password`.

Seeded demo users have `password_hash = NULL`; direct entry is the only demo auth path.

## Read-Only Boundary

`DEMO_READ_ONLY=true` installs a router-level guard for state-changing API requests. Safe methods are allowed. The only unsafe routes allowed are:

- `POST /api/v1/auth/demo`
- `POST /api/v1/auth/logout`

Blocked writes return `403` with `DEMO_READ_ONLY`.

## Demo Manifest

The authenticated demo UI reads:

```text
GET /api/v1/demo/manifest
```

The response exposes safe seeded identifiers and route URLs for the guided replay page. Constants live with `internal/demoseed` so seed assertions and the API manifest stay aligned.

## Operations

The demo host keeps `/opt/143-demo/.env.demo` locally with generated `DB_PASSWORD`, `SESSION_SECRET`, and `CSRF_SIGNING_KEY`. There is no `.env.demo.enc`; production encrypted env bundles are not read or written by demo provisioning.

Canonical operations are:

```bash
make provision-demo HOST=<ip>
make deploy-demo HOST=<ip> TAG=<sha>
make demo-reset HOST=<ip>
make public-demo-smoke HOST=<ip>
```

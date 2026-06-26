# Design: Demo Seed Workspace

> **Status:** Implemented | **Last reviewed:** 2026-06-26

The public/demo workspace is backed by the same canonical seed used by the dogfood preview: `.143/seed.sql`. The seed gives reviewers, screenshots, and future public-demo deployments a repeatable product state without opening the real internal organization or relying on production data.

## Contract

- `.143/seed.sql` is the source of truth for the demo organization, users, repositories, projects, sessions, messages, logs, preview rows, and PR state.
- The seed must be idempotent. Fixed identity rows should use `ON CONFLICT DO UPDATE` when they need to converge. Child rows without stable uniqueness should delete only seed-owned rows first, then reinsert.
- The demo seed must remain synthetic and publishable: only `@143.dev` demo emails, approved public URLs, no tokens, no private key material, no production env references, no customer data, and no real incident payloads.
- Seeded runtime rows must be inert. Illusory previews use sentinel values such as `provider = 'seeded'` and `worker_node_id = 'seeded'` so live workers do not claim or recycle them.

## Tooling

`make demo-seed-check` validates the canonical seed without mutating the source database. It creates a temporary sibling Postgres database, runs all migrations, applies `.143/seed.sql` twice, verifies safety/idempotency, asserts required demo stories, and drops the temporary database. It refuses production-looking database URLs unless `DEMO_SEED_ALLOW_PRODUCTION_URL=true` is set.

`make demo-seed-apply` applies the same seed to a long-lived demo database. It is guarded and requires:

```bash
DEMO_MODE=true \
ALLOW_DEMO_SEED_APPLY=true \
DEMO_SEED_DATABASE_URL=postgres://... \
make demo-seed-apply
```

The apply path runs migrations by default, scans the seed before writing, refuses production-looking database URLs unless an explicit second override is set, and refuses databases that already contain non-demo organizations unless `DEMO_SEED_ALLOW_NONDEMO_ORGS=true` is set.

## Update Workflow

When a product feature needs richer public/demo coverage:

1. Add or update the relevant rows in `.143/seed.sql`.
2. Keep all new content synthetic and safe to publish.
3. Run `make demo-seed-check`.
4. Apply to the demo workspace with `make demo-seed-apply` only from an intentional demo deployment or local throwaway database.
5. When frontend generated fixtures exist, regenerate them from the checked seed database rather than hand-maintaining separate mock data.

## Technical Contracts

**Database schema:** No schema changes. The seed targets existing product tables and depends on the normal migration chain being current before application.

**API contract:** No API changes. This is developer/demo-operations tooling only.

## Future Work

- Generate frontend MSW fixtures from the migrated seeded database so tests and demo screenshots share the same default data.
- Add a CI job that runs `make demo-seed-check` on every PR touching migrations, `.143/seed.sql`, demo docs, or seeded frontend fixtures.

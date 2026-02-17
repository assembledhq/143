Use docs/design/overall.md as the overall design of the system, think of it as a living doc that should get parts added to it, but only high level details. As more designs are made, put them into folders and subdocs inside of docs/design, and build them out. Please keep track of updates and keep them updated as designs are completed.

## Backend Architecture (Go)

**Key libraries**: `go-chi/chi` (router), `jackc/pgx` (Postgres driver + connection pooling), `rs/zerolog` (structured logging), `go-playground/validator` (request validation), `golang-migrate/migrate` (schema migrations). See `docs/design/02-api-server.md` for full dependency list.

**Direct pgx store functions for all DB queries**: One store struct per domain area in `internal/db/` (e.g. `issues.go`, `agent_runs.go`, `jobs.go`). All stores accept a `DBTX` interface (satisfied by both `pgxpool.Pool` and `pgx.Tx`). Use `pgx.CollectRows` + `pgx.RowToStructByName` for list queries. SQL lives as string literals inside Go functions, co-located with scanning and error handling. No ORM, no codegen, no separate `.sql` files.

**Service layer**: Handlers call services, services call the DB layer. Business logic belongs in `internal/services/`, never in HTTP handlers. Services are defined as interfaces for testability (mock with `go.uber.org/mock`).

**Logging**: Use `zerolog` for all log output. Never use `fmt.Printf` or `log.Println`. Logs are JSON-structured and shipped to Mezmo.

**Multi-tenancy**: Every table has an `org_id` column (FK to `organizations`). Every query MUST filter by `org_id`. Auth middleware extracts org from the session and sets it in request context. Missing an `org_id` filter is a data isolation bug.

**API response format**: Lists return `{data: [...], meta: {next_cursor}}` with cursor-based pagination. Errors return `{error: {code, message, details}}`. All routes under `/api/v1/`.

**Job queue**: Postgres-backed async work queue using the `jobs` table. Workers claim jobs with `SELECT ... FOR UPDATE SKIP LOCKED` — no external queue needed. Jobs have `status`, `attempts`, `max_attempts`, and exponential backoff on failure.

## Frontend Architecture (Next.js / React)

**Key libraries**: `shadcn/ui` (copy-paste components on Radix UI + Tailwind, not an npm dependency), `@tanstack/react-query` (server state), `nuqs` (URL search params state for filters). See `docs/design/03-frontend.md` for full list.

**Server state**: All API calls go through TanStack Query hooks (`useQuery`, `useMutation`). No raw fetch. TanStack Query handles caching, deduplication, and background refetching.

**Filter state**: Use `nuqs` to store filter/search state in URL params so views are bookmarkable and shareable.

## Test-First Development (Mandatory)

**All code changes MUST have tests written BEFORE the implementation.** Write the failing test first, confirm it fails, then implement. No PR should be opened without corresponding tests.

**Backend (Go)**: `go test`, `stretchr/testify`, `net/http/httptest`, `pashagolub/pgxmock/v4`, `testcontainers/testcontainers-go`, `go.uber.org/mock`. See `docs/design/02-api-server.md` §Testing for patterns and examples.

**Frontend (Next.js/React)**: Vitest, `@testing-library/react`, `@testing-library/user-event`, `@testing-library/jest-dom`, `msw`, `@playwright/test`. See `docs/design/03-frontend.md` §Testing for patterns and examples.

### Coverage Requirements

- **Backend**: minimum 70% line coverage
- **Frontend**: minimum 80% line coverage
- New code without tests will not be merged

## Insert-Only Versioned Settings Pattern

For settings/config tables that need change history, use insert-only versioning instead of `updated_at`.

**How it works**: To update, deactivate the current row (`active = false`), then insert a new active row with merged values. To delete, deactivate the current row and insert a new inactive row. All historical versions are preserved.

**Schema requirements**:
- `active boolean NOT NULL DEFAULT true`
- `created_at` timestamp (no `updated_at`)
- Unique constraints must be partial indexes filtered on `WHERE active = true`

**Model requirements**: Use `models.Optional[T]` in update request types so unset fields can be distinguished from explicit values. Use `Optional.GetValueWithDefault(existing)` / `Optional.GetPtrWithDefault(existing)` when merging.

**Implementation pattern**:
1. `Update...Settings` (exported): wraps in `models.Transact()`, orchestrates inactivate + insert.
2. `inactivate...Settings` (unexported): `UPDATE SET active = false ... RETURNING <columns>` via `Suffix(...)` + `QueryRowContext` to get previous values.
3. `insert...Settings` (unexported): merge optionals with returned values, insert new active row.

### When to use

Use for **settings/config tables** where change history is valuable and the table has no inbound foreign keys from child tables (or FKs reference a logical identity key, not the PK).

Do **NOT** use for operational/lifecycle entities, external entity mirrors, computed/cached data, running counters, version history tables, or tables that are FK targets for child tables.

### Tables using this pattern

| Table | Logical identity |
|-------|-----------------|
| `review_patterns` | (org_id, repo, rule) |
| `prompt_overrides` | (org_id, template_id, scope_type, repository_id, issue_type, phase) |
| `eval_release_gates` | (org_id, gate_name) |

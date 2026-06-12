Use docs/design/overall.md as the overall design of the system, think of it as a living doc that should get parts added to it, but only high level details. As more designs are made, put them into folders and subdocs inside of docs/design, and build them out. Please keep track of updates and keep them updated as designs are completed.

## Debugging Production

When investigating bugs or unexpected behavior, three Make targets give read-only access to prod. All require `SSH_KEY` (defaults to `~/.ssh/143-deploy`) and resolve hosts/credentials from `.env.production.enc` via sops.

### Querying the database

- **`make db-query Q='SELECT ...'`** — runs a one-shot SQL query against the prod Postgres as the `readonly` role. SELECT-only, every connection is a read-only txn, bounded by a 30s `statement_timeout`. Use single quotes around `Q` and escape literal `$` as `$$` (Make eats single `$`). For an interactive session, use `make db-psql`.

### Searching logs

Logs are shipped via Vector to a VictoriaLogs instance and visualized in Grafana. Use the CLI for scripted/agent searches and the UI for interactive exploration:

- **`make logs-query Q='<LogsQL>' [LIMIT=100]`** — runs a one-shot LogsQL query against VictoriaLogs and prints NDJSON to stdout. Always include a `_time:` filter — without one VictoriaLogs scans the full 30-day retention. Common fields: `service`, `level`, `org_id`, `agent_run_id`, `request_id`, `trace_id`. Examples:

  ```bash
  make logs-query Q='service:api AND level:error AND _time:[now-1h,now]'
  make logs-query Q='agent_run_id:"run-abc123" AND _time:[now-24h,now]' LIMIT=500
  make logs-query Q='"timeout waiting for sandbox" AND _time:[now-15m,now]' | jq -r '.message'
  ```

  See `docs/design/implemented/47-logging-victorialogs.md` for more examples and the [LogsQL reference](https://docs.victoriametrics.com/victorialogs/logsql/).

- **`make logs`** — opens an SSH tunnel to the prod Grafana instance at <http://localhost:9999> for interactive UI-based exploration. Press Ctrl+C to close the tunnel. Prefer `make logs-query` for anything scriptable.

## Backend Architecture (Go)

**Key libraries**: `go-chi/chi` (router), `jackc/pgx` (Postgres driver + connection pooling), `rs/zerolog` (structured logging), `go-playground/validator` (request validation), `golang-migrate/migrate` (schema migrations). See `docs/design/implemented/02-api-server.md` for full dependency list.

**Direct pgx store functions for all DB queries**: One store struct per domain area in `internal/db/` (e.g. `issues.go`, `agent_runs.go`, `jobs.go`). All stores accept a `DBTX` interface (satisfied by both `pgxpool.Pool` and `pgx.Tx`). Use `pgx.CollectRows` + `pgx.RowToStructByName` for list queries. SQL lives as string literals inside Go functions, co-located with scanning and error handling. No ORM, no codegen, no separate `.sql` files.

**Service layer**: Handlers call services, services call the DB layer. Business logic belongs in `internal/services/`, never in HTTP handlers. Services are defined as interfaces for testability (mock with `go.uber.org/mock`).

**LLM prompts live in Go templates**: All LLM system prompts must be stored as `.template` files in `internal/prompts/templates/`, rendered via the `prompts` package (`internal/prompts/prompts.go`). Never inline prompt strings as Go constants or string literals in service code. Add a corresponding exported render function in `prompts.go` for each new template.

**Logging**: Use `zerolog` for all log output. Never use `fmt.Printf` or `log.Println`. Logs are JSON-structured and shipped to Mezmo.

**Error handling**: Never discard errors with `_ =` in Go or empty `.catch()` in TypeScript. If an error cannot be propagated (e.g., best-effort cleanup after the main operation succeeded), log it at `Warn` level with context. In HTTP handlers, use `zerolog.Ctx(r.Context())` to get the request-scoped logger (enriched with org_id, user_id, request_id by the `LogContext` middleware). In services, use `s.logger`. If an error CAN be propagated, return it — prefer bubbling errors to the top of the call stack. Transaction rollback in `defer` is the one exception: `defer func() { _ = tx.Rollback(ctx) }()` is acceptable because rollback after commit is a no-op. Frontend: at minimum log with `console.error`; prefer surfacing errors through TanStack Query error states.

**Multi-tenancy**: Every tenant-scoped table has an `org_id uuid NOT NULL` column. Every query MUST filter by `org_id`. Auth middleware extracts org from the session and sets it in request context. Missing an `org_id` filter is a data isolation bug. DB-backed `REFERENCES organizations(id)` foreign keys are the default for normal product/control-plane tables, but very high-write append-only, event, log, cache, telemetry, and runtime-state tables may omit parent FKs after an explicit design review. See `docs/design/96-foreign-key-policy-and-hot-table-audit.md`.

Two lints enforce this, both run in CI via `make lint-tenancy`:

- **`cmd/lint-schema`** — scans `migrations/*.up.sql` (down migrations are not scanned by design — they restore prior state). Every `CREATE TABLE` must declare `org_id uuid NOT NULL REFERENCES organizations(id)` unless exempted. Schema-qualified (`public.foo`) and double-quoted (`"foo"`) table names are recognized and normalized for allowlist/display. Two exemption paths: (1) tables with no org_id at all — add to `allowedNoOrgID` in `cmd/lint-schema/main.go` with a justification, or use `-- lint:no-org-id reason="..."` anywhere in the CREATE TABLE statement; (2) tables with `org_id NOT NULL` but intentionally no FK — use `-- lint:allow-hot-table-no-fk reason="..."` (for reviewed high-write tables where the write path validates parent ownership in code). Both `reason="..."` clauses are required. Default to the full FK form; defense-in-depth beats transitive scoping.
- **`cmd/lint-stores`** — scans exported methods on `*XxxStore` types under `internal/db/`. The linter only polices receivers whose type declaration it actually finds in the scanned files, so a helper type elsewhere that happens to end in `Store` will not be caught. Each method must either take an `orgID uuid.UUID` parameter (the name must end in `orgid` case-insensitively, e.g. `orgID`/`OrgID`/`org_id`/`srcOrgID`) or receive a `*models.X` carrier whose struct has an `OrgID` field — either declared directly or inherited via an embedded type within the `models` package (the lint pre-scans `internal/models/*.go` and resolves embeddings to a fixed point). Methods that are legitimately cross-org (pre-auth lookups, system cleanup, scheduler scans) opt out with a doc comment on the line above `func`; a bare marker without a `reason="..."` clause is itself a lint violation:

  ```go
  // lint:allow-no-orgid reason="pre-auth login lookup by email"
  func (s *UserStore) GetByEmail(ctx context.Context, email string) (models.User, error) { ... }
  ```

When writing a new store method: add `orgID uuid.UUID` as the first arg after `ctx`, filter every SQL clause by `org_id`, and pass `middleware.OrgIDFromContext(r.Context())` from the handler. Do NOT add `lint:allow-no-orgid` unless the method truly must run across orgs.

When writing a migration for a high-write table, do not add foreign keys mechanically. Classify the table first. Control-plane and moderate-write product tables should keep DB-backed FKs. Hot append-only/event/log/cache/telemetry/runtime tables can omit parent FKs only when the write path validates parent existence and org ownership, orphan cleanup is acceptable or explicit, and the migration/design doc explains the operational reason. Avoid `ON DELETE CASCADE` from `organizations`; org deletion should be an explicit workflow, not an accidental fanout.

A **git pre-commit hook** (`.githooks/pre-commit`, installed by `./setup.sh` or `make hooks-install`) runs these lints against staged files before every commit so violations surface in seconds instead of after a CI round-trip. Bypass with `git commit --no-verify` only for WIP snapshots; CI enforces the same rules regardless.

**API response format**: Lists return `{data: [...], meta: {next_cursor}}` with cursor-based pagination. Errors return `{error: {code, message, details}}`. All routes under `/api/v1/`.

**Enum response fields use typed strings**: For model fields that represent enums (especially API response fields like provider/status/state/type), define a dedicated typed string in `internal/models` with named constants and a `Validate() error` method. Prefer `IntegrationProvider`/`IntegrationStatus`-style types over raw `string` fields. When writing new enum-like fields, add table-driven validation tests.

**Job queue**: Postgres-backed async work queue using the `jobs` table. Workers claim jobs with `SELECT ... FOR UPDATE SKIP LOCKED` — no external queue needed. Jobs have `status`, `attempts`, `max_attempts`, and exponential backoff on failure.

## Frontend Architecture (Next.js / React)

**Key libraries**: `shadcn/ui` (copy-paste components on Radix UI + Tailwind, not an npm dependency), `@tanstack/react-query` (server state), `nuqs` (URL search params state for filters). See `docs/design/03-frontend.md` for full list.

**shadcn/ui first — no raw HTML elements**: Always use shadcn/ui components for all interactive and structural UI. Never use raw HTML elements (`<button>`, `<input>`, `<select>`, `<textarea>`, `<table>`, etc.) when a shadcn/ui component exists. Use `<Button>` not `<button>`, `<Input>` not `<input>`, `<Card>` not `<div className="border ...">`, and so on. This is critical because shadcn components include consistent styling (e.g., `cursor-pointer`, focus rings, disabled states) that raw elements lack, and using the components everywhere makes it easy to update the design system in one place. Install new components with `npx shadcn@latest add <component>`. When styling, use shadcn's semantic design tokens (`text-foreground`, `text-muted-foreground`, `bg-card`, `border-border`, etc.) instead of hardcoded hex colors. This keeps the UI consistent and theme-able.

**Shared components**: Reusable app-level components live in `src/components/`. Current shared components:
- `PageHeader` — consistent page title + description. Use on every page.
- `EmptyState` — centered icon + title + description + optional action. Use for all empty/zero-data states.

**Server state**: All API calls go through TanStack Query hooks (`useQuery`, `useMutation`). No raw fetch. TanStack Query handles caching, deduplication, and background refetching.

**Filter state**: Use `nuqs` to store filter/search state in URL params so views are bookmarkable and shareable.

**Verify after every frontend change**: After modifying any frontend code, always run these checks from the `frontend/` directory before considering the work done:
1. `npm run typecheck` — TypeScript must pass with zero errors
2. `npm run lint` — ESLint must pass with zero errors
3. `npm run build` — the production build must succeed

Do not skip any of these steps. A change that breaks types, lint, or the build is not complete.

## Go Toolchain: `go vet` and `go fix`

**Always run `go vet`** after writing or modifying Go code. `go vet` catches subtle bugs that the compiler doesn't — misuse of `printf` format strings, unreachable code, struct tags with typos, copying locks, and more. Run it from the repo root:

```bash
go vet ./...
```

Fix every issue `go vet` reports before considering the code done. Common catches:
- `printf`-style format/arg mismatches (e.g., `%d` with a string arg)
- Struct field tags with bad syntax (e.g., missing quotes, wrong separators)
- Copying a `sync.Mutex` or `sync.WaitGroup` by value
- Unreachable code after `return`, `panic`, or infinite loops
- Incorrect usage of `atomic` operations
- Unused results of certain function calls

**Use `go fix`** when upgrading Go versions or updating APIs. `go fix` rewrites source code to use newer API signatures after a Go release deprecates old ones. Run it when:
- Upgrading the Go version in `go.mod` — run `go fix ./...` afterward to migrate deprecated API calls
- After updating a dependency that has changed its API surface

```bash
go fix ./...
```

`go fix` modifies files in place, so review the diff afterward. It is safe to run repeatedly — if nothing needs fixing, it makes no changes.

**Verification checklist for Go code changes**:
1. `go vet ./...` — must pass with zero issues
2. `go build ./...` — must compile cleanly
3. `go test ./...` — tests must pass

Do not skip `go vet`. A change that passes compilation but fails `go vet` is not complete.

## Test-First Development (Mandatory)

**All code changes MUST have tests written BEFORE the implementation.** Write the failing test first, confirm it fails, then implement. No PR should be opened without corresponding tests.

### Workflow

1. Write a test file for the new function/method/component
2. Run the test, confirm it **fails** (red)
3. Write the minimum implementation to make it pass (green)
4. Refactor if needed, confirm tests still pass
5. Only then move on to the next function

### Coverage Requirements (enforced in CI)

- **Backend**: minimum **70%** line coverage — CI will fail PRs below this
- **Frontend**: minimum **80%** line coverage — CI will fail PRs below this
- New code without tests will not be merged
- Coverage is checked by GitHub Actions on every PR via `go test -coverprofile` and `vitest --coverage`

### Backend Test Patterns (Go)

**Libraries**: `go test`, `stretchr/testify` (`require` package — not `assert`), `net/http/httptest`, `pashagolub/pgxmock/v4`, `go.uber.org/mock`.

**Table-driven tests are the default.** Every test with more than one case MUST use a slice of test cases (often called `tests` or `tt`). This keeps tests readable, makes it trivial to add new cases, and works naturally with `t.Parallel()`.

**Use `require`, not `assert`.** Always use `require.Equal`, `require.NoError`, etc. from `github.com/stretchr/testify/require`. The `require` package fails the test immediately on failure, which prevents cascading nil-pointer panics and confusing output. Never use the `assert` package.

**Always compare exact expected values.** Put the full expected value in the test case struct (e.g., `expected []models.Issue`) and compare with `require.Equal(t, tt.expected, actual, "message")`. Avoid partial checks like `require.Len` when you can compare the whole value — this catches field-level regressions, not just counts.

**Always include a message.** Every `require.*` call MUST have a descriptive message string as the last argument. The message should describe what behavior is being verified, not just restate the assertion. Good: `"should return both issues for org"`. Bad: `"length should be 2"`.

**Use `t.Parallel()` everywhere.** Call `t.Parallel()` at the top of every `Test*` function AND inside every `t.Run` subtest. To make this possible:
- Design test functions as pure input/output — pass all dependencies as arguments, never rely on package-level mutable state.
- Each test case must construct its own fixtures (mocks, request objects, expected results) inside the subtest. No shared mutable state across cases.
- If a test truly cannot run in parallel (e.g., it touches a shared external resource with no isolation), document why with a comment.

**Fixture pattern**: Define fixture helpers that return fresh, isolated test data. Prefer factory functions (e.g., `newTestIssue(overrides)`) over global `var` fixtures. This ensures each parallel subtest gets its own copy.

```go
func TestIssueStore_ListByOrg(t *testing.T) {
    t.Parallel()

    tests := []struct {
        name      string
        orgID     string
        setupMock func(mock pgxmock.PgxPoolIface)
        expected  []models.Issue
        expectErr bool
    }{
        {
            name:  "returns issues for org",
            orgID: "org-1",
            setupMock: func(mock pgxmock.PgxPoolIface) {
                mock.ExpectQuery("SELECT .* FROM issues WHERE org_id").
                    WithArgs(pgxmock.AnyArg()).
                    WillReturnRows(pgxmock.NewRows([]string{"id", "org_id"}).
                        AddRow("issue-1", "org-1").
                        AddRow("issue-2", "org-1"))
            },
            expected: []models.Issue{
                {ID: "issue-1", OrgID: "org-1"},
                {ID: "issue-2", OrgID: "org-1"},
            },
        },
        {
            name:  "returns empty for org with no issues",
            orgID: "org-empty",
            setupMock: func(mock pgxmock.PgxPoolIface) {
                mock.ExpectQuery("SELECT .* FROM issues WHERE org_id").
                    WithArgs(pgxmock.AnyArg()).
                    WillReturnRows(pgxmock.NewRows([]string{"id", "org_id"}))
            },
            expected: []models.Issue{},
        },
        {
            name:  "returns error on db failure",
            orgID: "org-1",
            setupMock: func(mock pgxmock.PgxPoolIface) {
                mock.ExpectQuery("SELECT .* FROM issues WHERE org_id").
                    WithArgs(pgxmock.AnyArg()).
                    WillReturnError(fmt.Errorf("connection refused"))
            },
            expectErr: true,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            t.Parallel()

            mock, _ := pgxmock.NewPool()
            defer mock.Close()
            store := db.NewIssueStore(mock)
            tt.setupMock(mock)

            issues, err := store.ListByOrg(ctx, tt.orgID, db.IssueFilters{})
            if tt.expectErr {
                require.Error(t, err, "ListByOrg should return an error")
                return
            }
            require.NoError(t, err, "ListByOrg should not return an error")
            require.Equal(t, tt.expected, issues, "ListByOrg should return the expected issues")
            require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
        })
    }
}
```

**Handler tests** (`internal/api/handlers/*_test.go`): Use `httptest.NewRecorder()` + `chi.NewRouteContext()`. Mock the store, call the handler directly, assert status code and response body. Same table-driven + `t.Parallel()` pattern applies.

```go
func TestIssueHandler_List(t *testing.T) {
    t.Parallel()

    tests := []struct {
        name         string
        setupStore   func(ctrl *gomock.Controller) *mocks.MockIssueStore
        expectedCode int
        expectedBody models.ListResponse[models.Issue]
    }{
        {
            name: "returns issues successfully",
            setupStore: func(ctrl *gomock.Controller) *mocks.MockIssueStore {
                s := mocks.NewMockIssueStore(ctrl)
                s.EXPECT().ListByOrg(gomock.Any(), gomock.Any(), gomock.Any()).
                    Return([]models.Issue{
                        {ID: "issue-1", OrgID: "org-1"},
                        {ID: "issue-2", OrgID: "org-1"},
                    }, nil)
                return s
            },
            expectedCode: http.StatusOK,
            expectedBody: models.ListResponse[models.Issue]{
                Data: []models.Issue{
                    {ID: "issue-1", OrgID: "org-1"},
                    {ID: "issue-2", OrgID: "org-1"},
                },
            },
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            t.Parallel()

            ctrl := gomock.NewController(t)
            store := tt.setupStore(ctrl)
            handler := handlers.NewIssueHandler(store)

            rr := httptest.NewRecorder()
            handler.List(rr, req)
            require.Equal(t, tt.expectedCode, rr.Code, "handler should return expected status code")

            var resp models.ListResponse[models.Issue]
            require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp), "response body should be valid JSON")
            require.Equal(t, tt.expectedBody, resp, "handler should return the expected response body")
        })
    }
}
```

**Middleware tests** (`internal/api/middleware/*_test.go`): Test that middleware correctly blocks/allows requests and sets context values.

**Multi-tenancy invariant**: Every store method that queries a table with `org_id` MUST have a test verifying the `org_id` filter is applied. The multi-tenancy audit test (`internal/db/tenancy_test.go`) scans all SQL for org_id presence.

### Frontend Test Patterns (React)

**Libraries**: `vitest`, `@testing-library/react`, `@testing-library/user-event`, `@testing-library/jest-dom`, `msw`.

**Component tests** (`src/**/*.test.tsx`): Render the component, assert on visible content, simulate user interactions.

```tsx
it('renders issues in the data table', async () => {
  server.use(http.get('/api/v1/issues', () => HttpResponse.json({ data: mockIssues, meta: {} })));
  render(<IssuesPage />);
  expect(await screen.findByText('TypeError: Cannot read properties')).toBeInTheDocument();
  expect(screen.getByText('critical')).toBeInTheDocument();
});
```

**API client tests** (`src/lib/__tests__/api.test.ts`): Test error handling, parameter construction, response parsing.

**MSW for API mocking**: Use `msw` (Mock Service Worker) to intercept network requests in tests. Define handlers in `src/test/mocks/handlers.ts`.

## Integration Tools (Sentry, Linear, Notion, Slack)

**Use `143-tools` CLI for sandbox agents, not the MCP server.** Both binaries share the same `ToolRegistry` (`internal/services/mcp/tools.go`), so tool coverage is identical. The CLI is preferred because it costs ~200-800 tokens (vs much heavier MCP protocol framing), LLMs already know how to use CLIs from training data, and there's no subprocess lifecycle to manage.

The orchestrator handles CLI injection automatically: `buildIntegrationSkills()` generates a markdown skills doc, `resolveAgentEnv()` passes credentials as env vars. Adding a new tool to `tools.go` makes it available in both CLI and MCP with no extra work.

**When to use MCP (`143-mcp`)**: IDE integrations that speak MCP protocol, external JSON-RPC clients, interactive development tools. Never for sandbox agents.

See `internal/services/mcp/AGENTS.md` for detailed implementation guidance.

## Security Patterns

**RBAC**: The `middleware.RequireRole(roles ...string)` middleware enforces role-based access. Apply it in the router after `Auth` middleware. Three roles: `admin` (full access), `member` (read + write), `viewer` (read-only). Webhook and health endpoints are exempt.

**Rate limiting**: `middleware.RateLimit(opts)` applies per-org and per-IP token bucket limits. Default: 100 req/s per org, 20 req/s per IP. Returns 429 with `Retry-After` header.

**Webhook signatures**: All inbound webhooks MUST verify HMAC-SHA256 signatures. The webhook secret is stored in `integrations.config.webhook_secret`. Invalid signatures return 401 immediately.

**Input validation**: Request body size capped at 1MB (`middleware.MaxBodySize`). All handler input structs should validate required fields and acceptable values before processing.

**Multi-tenancy**: Every DB query MUST filter by `org_id`. Missing an `org_id` filter is a P0 data isolation bug. The automated tenancy test catches this.

## Insert-Only Versioned Settings Pattern

For settings/config tables that need change history, use insert-only versioning instead of `updated_at`.

**How it works**: To update, deactivate the current row (`active = false`), then insert a new active row with merged values. To delete, deactivate the current row and insert a new inactive row. All historical versions are preserved.

**Schema requirements**:
- `active boolean NOT NULL DEFAULT true`
- `created_at` timestamp (no `updated_at`)
- Unique constraints must be partial indexes filtered on `WHERE active = true`

**Model requirements**: Use `models.Optional[T]` in update request types so unset fields can be distinguished from explicit values. Use `Optional.GetValueWithDefault(existing)` / `Optional.GetPtrWithDefault(existing)` when merging.

**Implementation pattern**:
1. `Update...Settings` (exported): wraps in a **transaction** (`db.TxStarter.Begin()`), orchestrates inactivate + insert.
2. `inactivate...Settings` (unexported): `UPDATE SET active = false ... RETURNING <columns>` via the transaction to get previous values.
3. `insert...Settings` (unexported): merge optionals with returned values, insert new active row within the same transaction.

**Always use a transaction.** The inactivate + insert must be atomic. If the process crashes between the UPDATE and INSERT without a transaction, the old row is deactivated with no replacement, leaving the data in an inconsistent state. Use `db.TxStarter` (which extends `DBTX` with `Begin()`) for stores that need insert-only versioning. Pattern:
```go
tx, err := s.db.Begin(ctx)
if err != nil { return err }
defer tx.Rollback(ctx)
// ... UPDATE SET active = false using tx ...
// ... INSERT new active row using tx ...
return tx.Commit(ctx)
```

### When to use

Use for **settings/config tables** where change history is valuable and the table has no inbound foreign keys from child tables (or FKs reference a logical identity key, not the PK).

Do **NOT** use for operational/lifecycle entities, external entity mirrors, computed/cached data, running counters, version history tables, or tables that are FK targets for child tables.

### Tables using this pattern

| Table | Logical identity |
|-------|-----------------|
| `review_patterns` | (org_id, repo, rule) |
| `prompt_overrides` | (org_id, template_id, scope_type, repository_id, issue_type, phase) |
| `eval_release_gates` | (org_id, gate_name) |
| `tuning_config_versions` | (org_id, config_scope, scope_key) |

## Database Triggers

The `trg_project_task_counts_update` trigger (migration 000047) fires on ALL column updates to `project_tasks`, not just status changes. This is because PostgreSQL does not allow `REFERENCING` transition tables with column-list triggers (`AFTER UPDATE OF status`). The recount logic is idempotent so correctness is unaffected, but be aware that updating non-status columns (e.g. `branch_name`, `pr_url`) will also trigger a recount of `total_tasks`, `completed_tasks`, and `failed_tasks` on the parent project.

## Production Secrets Guardrail

Treat `.env.production.enc` as protected production configuration. Do not edit, regenerate, stage, or commit it unless the user explicitly asks for a production secret/config change. Read-only decrypts through the Make targets above are fine. Before finishing any work that involved prod debugging, check `git status --short -- .env.production.enc` and leave it clean unless the requested task was specifically to update production env.

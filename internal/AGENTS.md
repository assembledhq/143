# Internal Backend Guidelines

## Multi-tenancy: `org_id` is non-negotiable

Two automated lints run in `make lint-tenancy` (and in CI on every PR that touches backend or migrations):

1. **Schema lint** (`cmd/lint-schema`). A new migration that adds a `CREATE TABLE` without an `org_id uuid NOT NULL REFERENCES organizations(id)` column will fail CI. Schema-qualified (`public.foo`) and double-quoted (`"foo"`) table names are recognized and normalized. If a table is genuinely cross-org, allowlist it in `cmd/lint-schema/main.go` with a one-line reason — don't paper over with the inline `-- lint:no-org-id reason="..."` escape hatch unless it's a one-off. The inline escape may appear anywhere inside the `CREATE TABLE ( ... )` statement (header line or a dedicated comment line in the body).

2. **Store lint** (`cmd/lint-stores`). Every exported method on `*XxxStore` under `internal/db/` must either:
   - take `orgID uuid.UUID` explicitly (preferred). The parameter name must end in `orgid` case-insensitively (`orgID`, `OrgID`, `org_id`, `srcOrgID`, `targetOrgID`), or
   - take a `*models.X` / `models.X` carrier that has an `OrgID` field — either declared directly on the struct **or** inherited via an embedded type (e.g. `models.Session` embeds `BaseEntity`, and `BaseEntity` declares `OrgID`). The lint pre-scans `internal/models/*.go` and resolves embeddings to a fixed point; only embedding within the `models` package counts (cross-package embeddings are not followed), or
   - be annotated with `// lint:allow-no-orgid reason="..."` on the line directly above `func`. A bare `// lint:allow-no-orgid` without a `reason="..."` clause is itself a lint violation — the reason must travel with the exception.

When you write a new store method, default to the first option. The flow from HTTP handler to DB is:

```go
// handler
orgID := middleware.OrgIDFromContext(r.Context())
rows, err := h.store.ListByOrg(r.Context(), orgID, filters)

// store
func (s *FooStore) ListByOrg(ctx context.Context, orgID uuid.UUID, f FooFilters) ([]models.Foo, error) {
    query := `SELECT ... FROM foos WHERE org_id = @org_id AND deleted_at IS NULL`
    args := pgx.NamedArgs{"org_id": orgID}
    ...
}
```

**Never** rely on `OrgIDFromContext` *inside* a store method — take it as a parameter so the dependency is visible in the signature and the tenancy test can verify it.

The existing test `internal/db/tenancy_test.go` is a third layer of defense: it reads every SQL literal and requires `org_id` in any query that touches a multi-tenant table.

## Prefer Non-Mutating Code

**Default to immutability.** When transforming data, return a new struct value rather than mutating the input in place.

- Return a new struct (or `*T` constructed with `&T{...}`) from transformation functions instead of taking a pointer and writing through it.
- For response-building or enrichment helpers, prefer idempotent functions that accept the current response value and return an enriched copy. This keeps handler state flow explicit and makes repeated calls safe.
- Build new slices with `append` onto a fresh slice rather than mutating an argument; same for maps — copy then write.
- Avoid setter methods that mutate the receiver — prefer `WithFoo(v) T` style that returns a copy.
- Don't expose mutable references across package boundaries. If you must return a slice/map, return a copy or document that the caller must not mutate it.

**Mutation is the exception, not the default.** Only reach for mutating code when there is a real, measured performance reason — e.g., a hot loop where allocations show up in a profile, or building a large structure incrementally where copying each step would be O(n²). When you do mutate, keep the mutation local and add a short comment explaining why immutability was rejected.

When in doubt, write the immutable version first. It's almost always fast enough, and it eliminates an entire class of aliasing and data-race bugs (especially important for anything shared across goroutines).

## Settings Documents: Merge Patch, Not Replace

Endpoints that update a JSONB settings/preferences document (user settings, org settings, and anything similar added later) must accept an **RFC 7386 JSON merge patch**, not a full replacement document: omitted fields keep their stored value, `null` clears a field, nested objects merge per key. Full-document replace forces clients to rebuild the body from their local cache, which silently clobbers concurrent edits from other tabs (cross-tab last-write-wins).

Apply the patch server-side as a read-merge-write inside a transaction with the row locked (`SELECT ... FOR UPDATE`) so concurrent patches compose, and validate the merged document before committing. Reference implementation: `UserStore.MergeSettings` (`internal/db/users.go`) + `models.ApplyUserSettingsMergePatch` (`internal/models/user_settings.go`), wired up in `AuthHandler.UpdateSettings`. The frontend-side rules live in `frontend/AGENTS.md` ("Settings Mutations: Patch, Don't Replace").

## No N+1 Queries

Never query the database inside a loop. Always batch using `ANY()`, JOINs, or bulk fetches. If a batch store method doesn't exist yet, create one using the `ANY()` pattern in the db package.

## Error Logging

Use `zerolog` for all log output. Never use `fmt.Printf`, `fmt.Println`, or the standard `log` package in production code paths.

### Rule: Always use `writeError` for error responses — it logs automatically

`writeError` in `handlers/helpers.go` both logs and writes the JSON error response. It logs at `Error` level for 5xx and `Info` level for 4xx, using the request-scoped logger (enriched with `org_id`, `user_id`, `request_id`).

```go
// Signature:
//   writeError(w, r, status, code, message, errs ...error)

// 500 — pass the error as the last argument so it appears in logs:
if err != nil {
    writeError(w, r, http.StatusInternalServerError, "CREATE_FAILED", "failed to create widget", err)
    return
}

// 4xx — no error variable needed, but the code+message are still logged at Info:
writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid widget ID")
```

Do NOT add a separate `zerolog.Ctx(r.Context()).Error()` line before `writeError` — that would double-log.

### Rule: Use request-scoped loggers in HTTP handlers

When logging outside of `writeError` (e.g., warnings for best-effort failures), use `zerolog.Ctx(r.Context())` — never `h.logger`. The request-scoped logger is enriched with `org_id`, `user_id`, and `request_id` by the `LogContext` middleware, so all log entries are automatically correlated.

```go
// CORRECT — includes org_id, user_id, request_id
zerolog.Ctx(r.Context()).Warn().Err(err).Msg("best-effort operation failed")

// WRONG — missing request context
h.logger.Warn().Err(err).Msg("best-effort operation failed")
```

The `h.logger` field is acceptable in non-HTTP contexts (background goroutines, initialization) where there is no request context.

### Rule: Use injected loggers in services

Services receive a `zerolog.Logger` via their constructor. Use `s.logger` for all logging — never import `github.com/rs/zerolog/log` (the global logger). The global logger lacks structured context like `org_id`.

```go
// CORRECT
s.logger.Warn().Err(err).Msg("operation failed")

// WRONG — global logger, no context
log.Warn().Err(err).Msg("operation failed")
```

### Rule: Log best-effort failures at Warn level

When an operation is best-effort (e.g., updating job status after the job already ran, recording a webhook delivery), log failures at `Warn` level so they are visible but don't trigger error alerts:

```go
if _, err := w.db.Exec(ctx, `UPDATE jobs SET status = 'succeeded' ...`, jobID); err != nil {
    w.logger.Warn().Err(err).Str("job_id", jobID.String()).Msg("failed to mark job as succeeded")
}
```

### Rule: Never silently discard errors

Avoid `_, _ = someFunc()` or `_ = someFunc()` without either logging or commenting why the error is intentionally ignored. If an error truly cannot be handled, add a log line or a comment:

```go
// Intentionally ignored: body is optional, fields default below.
_ = json.NewDecoder(r.Body).Decode(&body)
```

### Log level guidelines

| Level | When to use |
|-------|-------------|
| `Error` | Unexpected failures that indicate bugs or infrastructure issues (DB errors, failed API calls returning 500) |
| `Warn` | Degraded but recoverable situations (best-effort cleanup failed, fallback triggered, token refresh failed) |
| `Info` | Significant business events (job completed, session created, auth flow completed) |
| `Debug` | Verbose diagnostic output (skipped auto-trigger gates, cache hits/misses) |

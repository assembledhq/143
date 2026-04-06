## API Error Response Pattern

- Use `writeError` from `internal/api/handlers/helpers.go` for handler errors.
- Return the standard `models.ErrorResponse` shape: `{ "error": { "code", "message", "details" } }`.
- Always provide a stable machine-readable error `code` and a user-safe `message`.
- Do not return raw internal errors to clients.
- When adding new handlers, keep errors JSON and consistent so middleware logging can extract `error.code` and `error.message` for 5xx responses.

## Status Code Guidance

- Use 4xx for caller/input/auth/permission problems.
- Use 5xx for internal/server dependency failures.
- For 5xx responses, include a specific error code (for example `AUTH_INITIATE_FAILED`) and a user-safe message.

## Org ID Scoping

Authenticated handlers must call `middleware.OrgIDFromContext(r.Context())` (not `user.OrgID`) and pass it to all org-scoped store calls. Exempt: public routes, webhooks, internal API (uses `claims.OrgID`), and handlers with no org-scoped data. A test in `handlers/org_id_lint_test.go` enforces this — update its allowlist for new exemptions.

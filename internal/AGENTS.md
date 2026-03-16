# Internal Backend Guidelines

## No N+1 Queries — Batch and Join Instead

Never execute a query inside a loop. N+1 patterns silently destroy performance as data grows and are a P1 fix. Instead:
- **JOIN** related data in a single SQL query when fetching a parent with its children (e.g., issues with their labels, agent runs with their steps). Use `LEFT JOIN` when the child may not exist.
- **Batch with `WHERE ... IN`** when you need related records for a list of IDs. Collect the IDs first, then issue one `SELECT ... WHERE id = ANY($1)` query using a `pgx` array parameter.
- **Use subqueries or CTEs** for aggregations (counts, latest timestamps) instead of fetching all rows and computing in Go.
- **Cursor-based pagination at the SQL level** — never fetch all rows and slice in Go.

If you find yourself writing `for _, item := range items { rows := store.GetFoo(ctx, item.ID) }`, stop and refactor to `store.GetFoosByIDs(ctx, ids)` or add a JOIN to the original query. Store functions that accept a slice of IDs (e.g., `ListByIDs(ctx, orgID string, ids []string)`) are the standard pattern for batch lookups. If a batch store method doesn't exist yet, create one using the `ANY()` pattern in the db package. Every new store method should be reviewed for N+1 risk before merging.

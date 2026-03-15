# Internal Backend Guidelines

## Query Performance: No N+1 Queries

Never issue a database query inside a loop. Every query must fetch data in bulk using one of the patterns below.

### Preferred Patterns

**Batch reads with `ANY()`** — when you have a list of IDs, fetch all rows in a single query:

```sql
SELECT * FROM issues WHERE id = ANY(@issue_ids)
```

Use the corresponding `ListByIDs()` store methods (see `internal/db/issues.go`, `internal/db/session_store.go` for examples).

**JOINs** — when you need related data from multiple tables, join at the SQL level instead of fetching each relation separately:

```sql
SELECT i.*, ps.score
FROM issues i
LEFT JOIN priority_scores ps ON ps.issue_id = i.id
WHERE i.org_id = @org_id
```

**Aggregations in SQL** — use `COUNT()`, `SUM()`, `COUNT() FILTER (WHERE ...)` to compute stats in one query instead of fetching rows and counting in Go. See `internal/db/pm_decision_log.go` for examples.

**Bulk in-memory joining** — when a SQL JOIN is impractical, fetch both collections upfront and join them in Go using a map:

```go
items, _ := store.ListByOrg(ctx, orgID)
ids := extractIDs(items)
related, _ := relatedStore.ListByIDs(ctx, ids)
relatedByID := indexByID(related)
for i := range items {
    items[i].Related = relatedByID[items[i].ID]
}
```

### What to Avoid

```go
// BAD: N+1 — fires len(issues) separate queries
for _, issue := range issues {
    score, _ := priorityStore.GetByIssueID(ctx, issue.ID)
    // ...
}
```

```go
// GOOD: single bulk fetch
scores, _ := priorityStore.ListByIssueIDs(ctx, issueIDs)
```

If a `ListByIDs` or batch method does not exist in the store yet, add one rather than looping with single-row fetches. Follow the existing `ANY()` pattern in the db package.

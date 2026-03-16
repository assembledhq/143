# Internal Backend Guidelines

## No N+1 Queries

Never query the database inside a loop. Always batch using `ANY()`, JOINs, or bulk fetches. If a batch store method doesn't exist yet, create one using the `ANY()` pattern in the db package.

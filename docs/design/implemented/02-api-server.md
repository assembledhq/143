# Design: Go API Server

> **Status:** Implemented | **Last reviewed:** 2026-03-25

This document describes the Go backend architecture for 143.dev.

## Project Structure

```
/
├── cmd/
│   └── server/
│       └── main.go              # entrypoint
├── internal/
│   ├── api/
│   │   ├── router.go            # chi router setup, middleware
│   │   ├── middleware/
│   │   │   ├── auth.go
│   │   │   ├── rbac.go          # role-based access control (admin, member, viewer)
│   │   │   ├── ratelimit.go     # per-org and per-IP rate limiting
│   │   │   ├── logging.go       # request logging + Mezmo shipping
│   │   │   ├── metrics.go       # Datadog request metrics
│   │   │   ├── cors.go
│   │   │   └── org_context.go   # extracts org from auth, sets on context
│   │   └── handlers/
│   │       ├── issues.go
│   │       ├── agent_runs.go     # includes trace, failure, similar endpoints
│   │       ├── pull_requests.go
│   │       ├── integrations.go
│   │       ├── settings.go
│   │       ├── dashboard.go
│   │       ├── cluster.go       # cluster health, node list
│   │       ├── webhooks.go      # inbound webhooks from Sentry, Linear, GitHub
│   │       ├── experiments.go   # deploy impact + agent config experiments
│   │       ├── analytics.go     # failure analytics
│   │       ├── patterns.go      # detected run patterns
│   │       ├── prompts.go       # prompt templates, versions, promotion, rollback
│   │       ├── evals.go         # datasets, eval runs, release gates
│   │       └── costs.go         # cost summaries, budget, forecast, ROI
│   ├── db/
│   │   ├── db.go                # connection pool, DBTX interface, transaction helpers
│   │   ├── issues.go            # issue store functions
│   │   ├── agent_runs.go        # agent run store functions
│   │   ├── jobs.go              # job queue store functions
│   │   └── migrations/          # golang-migrate SQL files
│   ├── models/                  # domain types (Issue, AgentRun, etc.)
│   ├── services/
│   │   ├── ingestion/
│   │   │   ├── service.go       # orchestrates ingestion
│   │   │   ├── sentry.go        # Sentry adapter
│   │   │   ├── linear.go        # Linear adapter
│   │   │   └── support.go       # support ticket adapter (Zendesk, Intercom, etc.)
│   │   ├── prioritization/
│   │   │   └── service.go       # scoring algorithm
│   │   ├── agent/
│   │   │   ├── orchestrator.go  # manages agent run lifecycle
│   │   │   ├── provider.go      # SandboxProvider interface
│   │   │   ├── providers/
│   │   │   │   ├── docker.go    # Docker + gVisor provider (default)
│   │   │   │   └── e2b.go       # E2B cloud provider (optional)
│   │   │   ├── adapters/        # per-agent-type adapters (claude_code, codex, etc.)
│   │   │   └── tracing.go       # trace event capture and storage
│   │   ├── validation/
│   │   │   └── service.go       # validation pipeline
│   │   ├── github/
│   │   │   └── service.go       # PR creation, status checks, deploy detection
│   │   ├── observability/
│   │   │   └── service.go       # experiment lifecycle, impact measurement
│   │   ├── promptconfig/
│   │   │   └── service.go       # prompt composition, override resolver, rollout
│   │   ├── evals/
│   │   │   └── service.go       # dataset ingestion, eval execution, gate checks
│   │   ├── costs/
│   │   │   └── service.go       # cost rollups, budget tracking, forecasting
│   │   └── debugging/
│   │       ├── classifier.go    # failure classification service
│   │       ├── experiments.go   # agent config experiment lifecycle
│   │       ├── patterns.go      # cross-run pattern detection
│   │       └── similarity.go    # run similarity matching
│   ├── cluster/
│   │   ├── node.go              # node registration, heartbeat
│   │   ├── health.go            # cluster health monitoring, dead node cleanup
│   │   └── scheduler_lock.go    # Postgres advisory lock for scheduler leader election
│   ├── logging/
│   │   ├── logger.go            # zerolog setup, multi-writer (stdout + Mezmo)
│   │   └── mezmo.go             # Mezmo ingestion API writer
│   ├── monitoring/
│   │   ├── datadog.go           # Datadog StatsD client, metric helpers
│   │   └── traces.go            # dd-trace-go setup, span helpers
│   ├── worker/
│   │   ├── worker.go            # background worker loop
│   │   ├── scheduler.go         # cron-like job scheduler
│   │   └── jobs/
│   │       ├── ingest.go
│   │       ├── prioritize.go
│   │       ├── run_agent.go
│   │       ├── validate.go
│   │       ├── open_pr.go
│   │       ├── evaluate.go
│   │       ├── classify_failure.go  # post-run failure classification
│   │       ├── detect_patterns.go   # periodic cross-run pattern detection
│   │       ├── run_evals.go         # eval run executor + promotion gate checks
│   │       ├── compute_cost_summary.go # per-fix cost rollups
│   │       ├── update_budget_period.go # increment budget counters per completed run
│   │       ├── forecast_budget.go      # hourly budget forecasting
│   │       └── create_budget_period.go # ensure upcoming budget periods exist
│   └── config/
│       └── config.go            # env-based configuration
├── migrations/
│   ├── 000001_init.up.sql
│   └── 000001_init.down.sql
├── go.mod
├── go.sum
└── Dockerfile
```

## Key Dependencies

| Package | Purpose |
|---------|---------|
| `go-chi/chi` | HTTP router — lightweight, stdlib-compatible, middleware-friendly |
| `jackc/pgx` (v5) | PostgreSQL driver — connection pooling, `CollectRows`/`RowToStructByName` for type-safe scanning, no ORM or codegen |
| `golang-migrate/migrate` | Schema migrations |
| `rs/zerolog` | Structured logging (stdout + Mezmo writer) |
| `go-playground/validator` | Request validation |
| `docker/docker` | Docker SDK for container management (agent sandboxes) |
| `DataDog/datadog-go` | Datadog StatsD client for custom metrics |
| `DataDog/dd-trace-go` | Datadog APM tracing (auto-instruments chi, pgx) |

### Testing Dependencies

| Package | Purpose |
|---------|---------|
| `testing` (stdlib) | Test framework — all tests use Go's built-in `testing` package |
| `net/http/httptest` (stdlib) | HTTP handler testing — test chi handlers without a running server |
| `stretchr/testify` | Assertions (`require`, `assert`) and test suites — the de facto standard for Go tests |
| `pashagolub/pgxmock/v4` | pgx mock driver — unit test database queries without a real Postgres connection |
| `testcontainers/testcontainers-go` | Integration tests — spin up real Postgres (and other services) in Docker containers for integration tests |
| `go.uber.org/mock` | Interface mock generation — `mockgen` generates type-safe mocks for service interfaces |

## Database Access Pattern

All database queries use pgx v5 directly via structured store functions. No ORM, no codegen.

### DBTX Interface

Every store accepts a `DBTX` interface, satisfied by both `pgxpool.Pool` (normal operation) and `pgx.Tx` (inside transactions). This also works with `pgxmock` for unit tests.

```go
// internal/db/db.go

// DBTX is the interface satisfied by pgxpool.Pool, pgx.Tx, and pgxmock.
type DBTX interface {
    Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
    Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
    QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}
```

### Store Functions

One store struct per domain area. SQL lives as string literals inside Go functions, co-located with scanning and error handling.

```go
// internal/db/issues.go

type IssueStore struct {
    db DBTX
}

func NewIssueStore(db DBTX) *IssueStore {
    return &IssueStore{db: db}
}

type Issue struct {
    ID        uuid.UUID      `db:"id"`
    OrgID     uuid.UUID      `db:"org_id"`
    Title     string         `db:"title"`
    Source    string         `db:"source"`
    Status    string         `db:"status"`
    CreatedAt time.Time      `db:"created_at"`
}

func (s *IssueStore) ListByOrg(ctx context.Context, orgID uuid.UUID, limit, offset int) ([]Issue, error) {
    rows, _ := s.db.Query(ctx, `
        SELECT id, org_id, title, source, status, created_at
        FROM issues
        WHERE org_id = $1
        ORDER BY created_at DESC
        LIMIT $2 OFFSET $3`,
        orgID, limit, offset,
    )
    return pgx.CollectRows(rows, pgx.RowToStructByName[Issue])
}

func (s *IssueStore) GetByID(ctx context.Context, orgID, issueID uuid.UUID) (Issue, error) {
    rows, _ := s.db.Query(ctx, `
        SELECT id, org_id, title, source, status, created_at
        FROM issues
        WHERE org_id = $1 AND id = $2`,
        orgID, issueID,
    )
    return pgx.CollectOneRow(rows, pgx.RowToStructByName[Issue])
}
```

### Using Stores with Transactions

Stores work with the existing `Transact()` helper by constructing a new store instance with the `pgx.Tx`:

```go
func (svc *IngestionService) IngestAndPrioritize(ctx context.Context, payload SentryPayload) error {
    return pgx.BeginFunc(ctx, svc.pool, func(tx pgx.Tx) error {
        issueStore := db.NewIssueStore(tx)
        jobStore := db.NewJobStore(tx)

        issue, err := issueStore.Create(ctx, issueFromPayload(payload))
        if err != nil {
            return err
        }
        return jobStore.Enqueue(ctx, "prioritize", issue.ID)
    })
}
```

### Testing Stores with pgxmock

```go
func TestIssueStore_ListByOrg(t *testing.T) {
    mock, err := pgxmock.NewPool()
    require.NoError(t, err)
    defer mock.Close()

    orgID := uuid.New()
    mock.ExpectQuery("SELECT .+ FROM issues").
        WithArgs(orgID, 50, 0).
        WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "title", "source", "status", "created_at"}).
            AddRow(uuid.New(), orgID, "Login broken", "sentry", "open", time.Now()))

    store := db.NewIssueStore(mock)
    issues, err := store.ListByOrg(context.Background(), orgID, 50, 0)

    require.NoError(t, err)
    assert.Len(t, issues, 1)
    require.NoError(t, mock.ExpectationsWereMet())
}
```

## Router & API Design

Using `chi` for the HTTP router. All API routes are under `/api/v1/`.

### Route Groups

```
/api/v1/
├── /auth
│   ├── POST   /login
│   └── POST   /logout
│
├── /issues
│   ├── GET    /                    # list issues (filterable, paginated)
│   ├── GET    /:id                 # get issue details
│   ├── PATCH  /:id                 # update status, manual triage
│   ├── GET    /:id/events          # list events for issue
│   ├── GET    /:id/complexity      # get complexity estimate for an issue
│   ├── POST   /:id/estimate        # trigger complexity estimation
│   └── POST   /:id/run-agent      # manually trigger agent run (accepts skip_complexity_check override)
│
├── /agent-runs
│   ├── GET    /                    # list runs (filterable)
│   ├── GET    /:id                 # get run details
│   ├── GET    /:id/logs            # stream logs (SSE)
│   ├── GET    /:id/trace           # get structured trace events for a run
│   ├── GET    /:id/failure         # get failure classification for a run
│   ├── POST   /:id/classify-failure # trigger failure classification (usually automatic)
│   ├── GET    /:id/similar         # find similar runs for comparison
│   ├── GET    /:id/questions       # list pending questions from the agent
│   ├── POST   /:id/answer         # answer an agent question (resumes the run)
│   ├── POST   /:id/guide          # provide guidance on a paused run (approve, approve_with_guidance, retry_with_guidance, dismiss)
│   ├── GET    /:id/resume-info    # get sandbox connection info for local resume (one-time token, session ID, CLI command)
│   ├── POST   /:id/cancel         # cancel a running agent
│   └── POST   /:id/retry          # retry a failed run
│
├── /pull-requests
│   ├── GET    /                    # list PRs
│   └── GET    /:id                 # get PR details + validation results
│
├── /experiments
│   ├── GET    /                    # list deploy impact experiments
│   ├── GET    /:id                 # get experiment results
│   ├── POST   /agent-configs      # create agent config experiment
│   ├── GET    /agent-configs       # list agent config experiments
│   ├── GET    /agent-configs/:id   # get agent config experiment details + results
│   └── PATCH  /agent-configs/:id   # update experiment (start, stop)
│
├── /prompts
│   ├── GET    /templates            # list global templates
│   ├── GET    /versions             # list versions (filter by template/scope/state)
│   ├── POST   /versions             # create org draft override
│   ├── PATCH  /versions/:id         # update draft override
│   ├── POST   /versions/:id/submit  # move draft -> candidate
│   ├── POST   /versions/:id/promote # promote candidate -> active (gated)
│   └── POST   /versions/:id/rollback # rollback active pointer
│
├── /evals
│   ├── GET    /datasets             # list eval datasets
│   ├── POST   /datasets             # create dataset metadata
│   ├── POST   /datasets/:id/examples # ingest examples (payload encrypted server-side)
│   ├── GET    /runs                 # list eval runs
│   ├── POST   /runs                 # start eval run
│   ├── GET    /runs/:id             # eval run summary
│   ├── GET    /runs/:id/results     # per-example results
│   ├── GET    /release-gates        # gate config
│   └── PATCH  /release-gates        # update thresholds and canary stages
│
├── /integrations
│   ├── GET    /                    # list configured integrations
│   ├── POST   /                    # add integration
│   ├── PATCH  /:id                 # update integration config
│   ├── DELETE /:id                 # remove integration
│   └── POST   /:id/test           # test connectivity
│
├── /settings
│   ├── GET    /                    # get org settings (includes execution_aggressiveness, issue_type_overrides)
│   └── PATCH  /                    # update settings (accepts all routing/execution settings)
│
├── /analytics
│   └── GET    /                    # aggregate failure analytics (category, code, trends)
│
├── /costs
│   ├── GET    /summary             # token + cost breakdown over time range
│   ├── GET    /per-fix             # list fixes with token/cost/impact data
│   ├── GET    /budget              # current budget period status
│   ├── PATCH  /budget              # update budget settings and thresholds
│   ├── GET    /forecast            # usage forecast for current budget period
│   └── GET    /roi                 # cost-efficiency and impact correlation
│
├── /patterns
│   ├── GET    /                    # list detected run patterns
│   └── PATCH  /:id                 # acknowledge, apply, or dismiss a pattern
│
├── /dashboard
│   └── GET    /stats               # aggregated stats for dashboard
│
├── /cluster
│   ├── GET    /nodes               # list all nodes (role, status, heartbeat)
│   └── GET    /health              # cluster health summary
│
└── /webhooks                       # inbound webhooks (no auth — use webhook secrets)
    ├── POST   /sentry
    ├── POST   /linear
    └── POST   /github
```

### Request/Response Format

All API responses follow a consistent envelope:

```json
{
  "data": { ... },
  "meta": {
    "page": 1,
    "per_page": 50,
    "total": 230
  }
}
```

Errors:

```json
{
  "error": {
    "code": "VALIDATION_ERROR",
    "message": "invalid issue status",
    "details": { ... }
  }
}
```

### Pagination

Cursor-based pagination for list endpoints:

```
GET /api/v1/issues?cursor=<opaque>&limit=50
```

Returns `meta.next_cursor` in the response for the next page.

**PM plans cursor format**: `/api/v1/pm/plans` returns a cursor in the form
`<created_at>|<id>` where `created_at` is `RFC3339Nano` in UTC (e.g.
`2026-03-02T05:12:34.123456789Z|c0a8012e-7e6d-4f2e-9bbd-9a6b0b2f0e9a`). Clients
should still treat the cursor as opaque and pass it back as-is.

## Middleware Stack

Applied in order:

1. **RequestID** — generates unique request ID, adds to context and response headers
2. **DDTrace** — Datadog APM trace middleware (creates spans per request, propagates trace context)
3. **Logger** — structured request/response logging with zerolog (ships to stdout + Mezmo)
4. **Metrics** — Datadog StatsD metrics (request count, duration, status code)
5. **RateLimit** — per-org and per-IP rate limiting (see [20-security-architecture.md](20-security-architecture.md) for tiers)
6. **Recoverer** — panic recovery, returns 500
7. **CORS** — configurable allowed origins (permissive in dev, locked down in prod)
8. **Auth** — validates session/token (HttpOnly, Secure, SameSite cookies), sets user on context (skipped for `/webhooks`)
9. **OrgContext** — extracts org_id from authenticated user, sets on context
10. **RBAC** — applied per-route to enforce role-based access (admin, member, viewer). See [20-security-architecture.md](20-security-architecture.md)

## Authentication

Session-based auth for the web UI. The frontend sends session cookies with security attributes: `HttpOnly`, `Secure` (production), `SameSite=Lax`. Sessions expire after 24 hours and have a 30-minute idle timeout. Session IDs are rotated after login to prevent session fixation.

For API access (e.g., CI integrations), support scoped API key auth via `Authorization: Bearer <key>` header. API keys are stored as bcrypt hashes, support explicit scopes (e.g., `issues:read`, `agent-runs:trigger`), and can have optional expiration dates. See [20-security-architecture.md](20-security-architecture.md) for details.

Initial implementation: simple session table in Postgres. Can be swapped for OAuth/OIDC later.

## Background Workers

The server process runs background workers in-process using goroutines. A simple job queue backed by Postgres (using `SELECT ... FOR UPDATE SKIP LOCKED` for distributed locking).

### Job Table

Canonical schema lives in [01-database-schema.md](01-database-schema.md) (`jobs` table). The API/worker layer must use that schema exactly.

```sql
-- abridged; see doc 01 for complete schema and indexes
CREATE TABLE jobs (
    id uuid PRIMARY KEY,
    org_id uuid NOT NULL REFERENCES organizations(id),
    queue text NOT NULL,
    job_type text NOT NULL,
    payload jsonb NOT NULL,
    priority int NOT NULL DEFAULT 0,
    status text NOT NULL DEFAULT 'pending', -- pending, running, succeeded, failed, cancelled, dead_letter
    attempts int NOT NULL DEFAULT 0,
    max_attempts int NOT NULL DEFAULT 3,
    run_at timestamptz NOT NULL DEFAULT now(),
    locked_by_node_id text,
    locked_at timestamptz,
    last_error text,
    dedupe_key text,
    retry_window_started_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz
);
```

### Job Types

| Job Type | Trigger | Description |
|----------|---------|-------------|
| `ingest_sync` | Scheduled (every 5 min) | Poll Sentry/Linear APIs for new issues |
| `ingest_webhook` | Webhook received | Process a single inbound webhook payload |
| `prioritize` | After ingestion | Recompute priority scores |
| `estimate_complexity` | After prioritization for eligible issues | Estimate issue complexity and type via LLM |
| `run_agent` | Manual or auto after prioritization + complexity estimation | Launch agent in sandbox (with aggressiveness checks) |
| `validate` | After agent completes | Run validation pipeline |
| `open_pr` | After validation passes | Create GitHub PR |
| `evaluate_experiment` | Scheduled (after deploy) | Check post-deploy metrics |
| `classify_failure` | After agent run fails or validation fails | Classify failure root cause via LLM |
| `detect_patterns` | Scheduled (every 6 hours) | Scan completed runs for cross-run patterns |
| `run_evals` | Manual/scheduled/pre-promotion | Execute eval suite and write run results |
| `check_release_gates` | After `run_evals` | Compare eval metrics to thresholds and decide promotion eligibility |
| `compute_cost_summary` | After PR merge or experiment completion | Aggregate per-fix token and optional dollar costs |
| `update_budget_period` | After each run completion | Update token/dollar usage counters for active budget period |
| `forecast_budget` | Scheduled (hourly) | Forecast budget consumption and activate throttling when needed |
| `create_budget_period` | Scheduled (daily) | Ensure current and next budget period rows exist per org |

### Scheduler

A simple in-process scheduler that enqueues periodic jobs:

```go
scheduler.Every(5 * time.Minute).Do("ingest_sync")
scheduler.Every(1 * time.Hour).Do("evaluate_experiment")
scheduler.Every(6 * time.Hour).Do("detect_patterns")
scheduler.Every(24 * time.Hour).Do("run_evals", map[string]any{
    "trigger_type": "scheduled",
    "dataset_type": "shadow",
})
scheduler.Every(1 * time.Hour).Do("forecast_budget")
scheduler.Every(24 * time.Hour).Do("create_budget_period")
```

## Server-Sent Events (SSE)

The `/agent-runs/:id/logs` endpoint uses SSE to stream agent run logs to the frontend in real time. The handler tails the `agent_run_logs` table and pushes new rows as events.

## Configuration

All configuration via environment variables, loaded into a typed config struct at startup:

```
DATABASE_URL=postgres://...
PORT=8080
SESSION_SECRET=...
GITHUB_APP_ID=...
GITHUB_APP_PRIVATE_KEY=...
SENTRY_WEBHOOK_SECRET=...
LINEAR_WEBHOOK_SECRET=...
LOG_LEVEL=info
SANDBOX_IMAGE=143-sandbox:latest
SANDBOX_TIMEOUT=300
EVAL_ENCRYPTION_KEY=base64:...
EVAL_PRIVATE_DATA_REDACTION=true
```

## Testing

**Test-first development is mandatory.** Write tests before implementing any new feature, endpoint, or service. Tests are written using Go's built-in `testing` package with `testify` for assertions.

### Test Structure

Tests live next to the code they test, following Go conventions:

```
internal/
├── api/
│   ├── handlers/
│   │   ├── issues.go
│   │   ├── issues_test.go          # handler unit tests
│   │   ├── agent_runs.go
│   │   └── agent_runs_test.go
│   ├── middleware/
│   │   ├── auth.go
│   │   └── auth_test.go
├── services/
│   ├── ingestion/
│   │   ├── service.go
│   │   ├── service_test.go         # unit tests (mocked DB)
│   │   ├── sentry.go
│   │   └── sentry_test.go
│   ├── prioritization/
│   │   ├── service.go
│   │   └── service_test.go
├── db/
│   ├── issues.go                    # issue store functions
│   ├── issues_test.go               # store tests against real Postgres
├── integration_test/               # integration tests (testcontainers)
│   ├── api_test.go                 # full HTTP request/response tests
│   ├── ingestion_test.go
│   └── helpers_test.go             # shared test fixtures, setup
```

### Running Tests

```bash
# Run all unit tests
go test ./...

# Run tests with race detection (required in CI)
go test -race ./...

# Run tests with coverage
go test -coverprofile=coverage.out ./...
go tool cover -html=coverage.out

# Run only integration tests (requires Docker)
go test -tags=integration ./internal/integration_test/...

# Run a specific test
go test -run TestCreateIssue ./internal/api/handlers/...
```

### Test Patterns

#### Handler Tests (httptest)

Test HTTP handlers using `httptest` and `chi`:

```go
func TestListIssues(t *testing.T) {
    // Arrange: set up mock service and router
    mockSvc := &mockIssueService{
        issues: []models.Issue{{ID: "1", Title: "Test issue"}},
    }
    r := chi.NewRouter()
    h := handlers.NewIssueHandler(mockSvc)
    r.Get("/api/v1/issues", h.List)

    req := httptest.NewRequest("GET", "/api/v1/issues?status=open", nil)
    w := httptest.NewRecorder()

    // Act
    r.ServeHTTP(w, req)

    // Assert
    require.Equal(t, http.StatusOK, w.Code)
    var resp api.ListResponse
    require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
    assert.Len(t, resp.Data, 1)
}
```

#### Service Tests (pgxmock)

Test service logic with a mocked database:

```go
func TestPrioritizeIssue(t *testing.T) {
    mock, err := pgxmock.NewPool()
    require.NoError(t, err)
    defer mock.Close()

    mock.ExpectQuery("SELECT .+ FROM issues").
        WithArgs("issue-1").
        WillReturnRows(pgxmock.NewRows([]string{"id", "title", "severity"}).
            AddRow("issue-1", "Login broken", "critical"))

    svc := prioritization.NewService(mock)
    score, err := svc.ComputeScore(context.Background(), "issue-1")

    require.NoError(t, err)
    assert.Greater(t, score, 0.0)
    require.NoError(t, mock.ExpectationsWereMet())
}
```

#### Integration Tests (testcontainers)

Test the full stack against a real Postgres database:

```go
//go:build integration

func TestIssueCreationIntegration(t *testing.T) {
    ctx := context.Background()

    // Start a real Postgres container
    pgContainer, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
        ContainerRequest: testcontainers.ContainerRequest{
            Image:        "postgres:17",
            ExposedPorts: []string{"5432/tcp"},
            Env:          map[string]string{"POSTGRES_PASSWORD": "test", "POSTGRES_DB": "test143"},
            WaitingFor:   wait.ForListeningPort("5432/tcp"),
        },
        Started: true,
    })
    require.NoError(t, err)
    defer pgContainer.Terminate(ctx)

    // Run migrations, create service, and test
    dbURL := getConnectionString(t, pgContainer)
    pool := setupDB(t, dbURL)
    svc := ingestion.NewService(pool)

    issue, err := svc.IngestSentryEvent(ctx, sentryPayload)
    require.NoError(t, err)
    assert.Equal(t, "sentry", issue.Source)

    // Verify it persisted
    fetched, err := svc.GetIssue(ctx, issue.ID)
    require.NoError(t, err)
    assert.Equal(t, issue.Title, fetched.Title)
}
```

#### Interface Mocking (mockgen)

Generate mocks for service interfaces:

```go
// internal/services/ingestion/service.go
//go:generate mockgen -source=service.go -destination=mock_service_test.go -package=ingestion

type IngestionService interface {
    IngestSentryEvent(ctx context.Context, payload SentryPayload) (*models.Issue, error)
    IngestLinearIssue(ctx context.Context, payload LinearPayload) (*models.Issue, error)
}
```

### Test Coverage Requirements

- **Minimum 70% line coverage** for all packages
- **Handlers**: every route must have tests for success, validation error, auth error, and not-found cases
- **Services**: business logic must be tested with both happy paths and error paths
- **Database queries**: database store functions must have integration tests against real Postgres
- Coverage is tracked in CI and reported on PRs

## Error Handling

- Handlers return structured error responses with appropriate HTTP status codes.
- All errors are logged with request context (request ID, org ID, user ID).
- Panics are caught by the Recoverer middleware and return 500.
- Background job failures are retried up to `max_attempts` with exponential backoff.

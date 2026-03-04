# Design: 143.dev

[143.dev](http://143.dev) is an open-source platform that turns customer pain and production errors into safe, validated code fixes that ship automatically.

It’s an open-source platform that connects customer pain directly to code fixes.

The system aggregates issues from support, Sentry, and Linear, prioritizes them by real business impact, runs a coding agent to generate a fix, validates it safely, opens a PR, and measures the customer impact after deploy.

# Overall flow

- Step 0: Connect repositories and build codebase context
    - Users sign in with GitHub OAuth and install the 143.dev GitHub App on their organization/repos. The GitHub App (same auth model used by Codex web, Claude Code web, and other modern AI coding platforms) provides fine-grained, short-lived installation tokens for repo access — no personal access tokens needed.
    - For each connected repo, the system automatically builds a **Repository Context Package** — a structured body of knowledge including architecture docs (CLAUDE.md, AGENTS.md), coding conventions extracted from the codebase and past PR reviews, a feature-to-file map (which files own which features), test infrastructure knowledge (how to run tests, what patterns are used), and a dependency map (service boundaries, safe-to-change-in-isolation analysis).
    - The system actively helps teams build and maintain this context: auto-generating it from the codebase, suggesting updates when code changes via push webhooks, and measuring **context quality** (e.g. "your repo has 40% file coverage in context docs, agents working on undocumented areas fail 3x more").
    - This context package is injected into every agent run, giving agents deep understanding of the codebase before they start working. This is arguably the single most important factor in agent success.
- Step 1: Ingest and aggregate customer and engineering context from:
    - Support tickets
    - Sentry errors
    - Linear issues
    - Integration setup is initiated from Settings integration cards (for example, "Connect Linear" creates an active Linear integration record for the org so webhook ingestion can be enabled without manual DB setup).
- Step 2: Prioritize and identify top issues based on business impact
    - The system determines how many customers were affected, regression severity, and optionally (if you integrate Salesforce or some other CRM) the revenue risk.
    - The admins can specify product context (philosophy + direction + focus/avoid areas) to steer prioritization.
    - A **PM agent** now runs on a batch cadence, clusters related issues, and produces a prioritized plan that delegates work to coding agents (replacing per-issue auto-triggering for automation).
- Step 3: Execute a coding agent
    - Admins set a **confidence threshold** that controls which issues the system will auto-attempt. Issues below the threshold require manual triggering.
    - The agent runs in a sandboxed container and produces a code diff.
    - The agent outputs a **confidence score** with its fix. Low-confidence runs are paused for human review before proceeding to validation.
    - If the agent asks a clarifying question during execution, the run pauses and the question surfaces in the Fix Queue. The user can answer in the UI, provide guidance, or **resume the session locally** via CLI (e.g., `143 resume <run-id>` or `claude --resume <session-id>`) to take over the sandbox interactively.
    - When a run fails, the system generates a **human-readable failure explanation** with actionable next steps — see [17-failure-communication.md](17-failure-communication.md). Failures are classified by sub-type and feed back into the system to improve future runs.
- Step 4: Validate correctness
    - The system checks the code and ensures that
        1. it works towards the right product direction
        2. the code is correct
        3. the code is high quality and a simple, minimal diff
        4. the fix includes a regression test that would have caught the original bug (required for Sentry errors and support tickets)
        5. the code passes all CI/CD checks and coverage is not reduced
- Step 5: Open PR and ship
    - The system opens a new PR on github, using whatever Github template already exists
    - It makes sure to attach the relevant Linear issue to the PR title, or references the original sentry issue / customer complaint
    - Sends the PR for human review (depending on the settings, could be a push notification or just puts it out for a group of reviewers).
- Step 6: Observe impact and close the customer loop
    - After a fix is deployed, the system automatically evaluates whether it reduced real customer pain.
    - Each shipped PR captures baseline production metrics before deploy and an observation window after deploy. After a deploy, the system will do automated checks to attribute impact.
    - It will measure:
        - Sentry error rate changes
        - support ticket volume changes
        - latency or reliability improvements
    - Finally the system classifies the outcomes as successful or not.
- Step 7: PR review feedback → agent improvement loop
    - By default, review comments on 143-generated PRs are captured and run through a multi-stage filtering pipeline (structural pre-filter → merge-gate → adoption check → directive detection → classification → dedup). An org setting can expand this to all PRs.
    - When a reviewer requests changes on a 143-generated PR, the system offers to re-run the agent with that feedback incorporated (auto-apply), rather than making the human fix it manually.
    - Generalizable reviewer feedback is accumulated into a per-repo knowledge base and materialized as a `.143/learned-conventions.md` file in the repo — version-controlled, transparent, and editable by the team. The agent reads this file as part of its context for all future runs.
    - Reviewer trust tiers (maintainer, contributor, external) control how quickly patterns are promoted. Adoption evidence (was the suggestion reflected in the final merged code?) further weights pattern confidence.
    - Reviewer acceptance rates are tracked per issue type, so the system learns which categories of fixes the agent handles well vs. poorly.
    - This creates a flywheel: every human review makes every future agent run better.

**The system tracks 7 steps, but the core demo is Steps 1-3-4-5: ingest a Sentry error → run an agent → validate → open a PR. Everything else is optimization on this loop.**

# State Machines

The following state machines define valid status transitions for the core entities. These are authoritative — no code should transition an entity to a status not shown here.

## Issue Status

```
                          ┌───────────────┐
                          │     open      │ ◄── created by ingestion
                          └───────┬───────┘
                                  │ prioritization scores computed
                                  ▼
                          ┌───────────────┐
                     ┌──▶ │    triaged    │ ◄── eligible for agent run
                     │    └───────┬───────┘
                     │            │ agent run started
                     │            ▼
                     │    ┌───────────────┐
                     │    │  in_progress  │ ◄── agent run active
                     │    └───┬───────┬───┘
                     │        │       │
     validation fail │        │       │ PR merged + deploy detected
     or run failed   │        │       ▼
                     │        │  ┌───────────────┐
                     └────────┘  │   observing   │ ◄── experiment running
                                 └───────┬───────┘
                                         │ experiment completed
                                         ▼
                                 ┌───────────────┐
                                 │     fixed     │ ◄── outcome = success
                                 └───────────────┘

Other terminal statuses (reachable from open, triaged, or in_progress):
  - wont_fix  — admin dismisses manually
  - duplicate — dedup merges into another issue
```

Note: If a fix causes a regression (outcome = `regression`), the issue transitions back from `observing` → `triaged` so it can be re-attempted.

## Agent Run Status

```
┌─────────┐   job claimed    ┌─────────┐   agent exits     ┌───────────┐
│ pending │ ───────────────▶ │ running │ ──────────────▶   │ completed │
└────┬────┘                  └────┬────┘                   └─────┬─────┘
     │                            │                              │
     │ admin cancels              │ sandbox crash/timeout        │ validation
     ▼                            │ or agent error               │
┌───────────┐                     ▼                              ▼
│ cancelled │              ┌────────────┐               ┌──────────────┐
└───────────┘              │   failed   │               │  validation  │
                           └────────────┘               │   passed     │
                                                        └──────┬───────┘
                                                               │
                                                    ┌──────────┴──────────┐
                                                    │                     │
                                              confidence             confidence
                                              >= threshold           < threshold
                                                    │                     │
                                                    ▼                     ▼
                                             ┌─────────────┐   ┌──────────────────────┐
                                             │  pr_created  │   │ needs_human_guidance │
                                             └─────────────┘   └──────────────────────┘
                                                                         │
                                                                   admin approves
                                                                         │
                                                                         ▼
                                                                  ┌─────────────┐
                                                                  │  pr_created  │
                                                                  └─────────────┘
```

Note: `skipped` is also a valid status, set when the aggressiveness gate rejects an auto-triggered run before execution.

## Experiment Status

```
┌──────────┐   baseline window      ┌───────────┐   observation window    ┌───────────┐
│ baseline │ ─────────────────────▶ │ observing │ ──────────────────────▶ │ completed │
└──────────┘   ends (= deploy time) └───────────┘   ends                  └───────────┘

Outcome (set on completion): success | no_change | regression | inconclusive
```

If outcome is `regression`, the linked issue transitions back to `triaged`.

## PR Status

```
┌────────┐   approved + merged    ┌────────┐
│  open  │ ─────────────────────▶ │ merged │
└───┬────┘                        └────────┘
    │
    │ author/admin closes
    ▼
┌────────┐
│ closed │
└────────┘
```

# Decision Matrix: Should We Attempt This Issue?

Three controls interact to determine whether an issue gets an automatic agent run. They operate **sequentially** — each is a gate that must pass before the next is evaluated. See [24-design-resolutions.md](24-design-resolutions.md) Resolution 1 for the full flowchart.

```
Issue eligible (score > threshold, status = open/triaged, direction_alignment > -0.5)
        │
        ▼
GATE 1: Autonomy Level (pre-run — "should we auto-trigger?")
        │
        ├── manual      → never auto-trigger; admin must click "Fix This"
        ├── auto_simple  → auto-trigger only for medium/low severity, score < 60
        └── auto_all     → auto-trigger for all eligible issues
        │
        ▼
GATE 2: Aggressiveness (pre-run — "is this issue within our complexity tolerance?")
        │
        ├── issue.complexity_tier <= max_tier_for_aggressiveness_level? → proceed
        └── above max tier? → skip (auto) or warn (manual trigger)
        │
        ▼
AGENT EXECUTES IN SANDBOX
        │
        ▼
GATE 3: Confidence Score (post-run — "do we trust this result?")
        │
        ├── score >= 0.8 (auto_proceed)     → proceed to validation
        ├── score 0.5-0.79 (human_review)   → proceed, flag for review before merge
        └── score < 0.5                      → pause, mark needs_human_guidance
```

**Key rule**: These gates never interact with each other. A high confidence score cannot bypass the aggressiveness gate (different lifecycle stages). A high priority score cannot bypass a low confidence result.

# Failure Recovery

Every failure type has a defined recovery path. This prevents ambiguity during implementation.

## Agent Run Failures

| Failure Type | What Happens | Retry? | Issue Status |
|-------------|-------------|--------|-------------|
| **Sandbox crash** (OOM, infrastructure) | Run marked `failed`, `failure_category = tooling`, `failure_sub_type = sandbox_crash` | Auto-retry once. If second attempt fails, stop and notify. | Stays `in_progress` during retry, returns to `triaged` after final failure |
| **Timeout** | Run marked `failed`, `failure_category = tooling` | No auto-retry. User can retry manually with longer timeout. | Returns to `triaged` |
| **Agent error** (non-zero exit, no diff) | Run marked `failed`, failure analyzed by LLM | No auto-retry. User sees explanation + next steps. | Returns to `triaged` |
| **LLM API error** (rate limit, outage) | Run marked `failed`, `failure_category = tooling`, `failure_sub_type = api_error` | Auto-retry with exponential backoff (max 3 attempts). | Stays `in_progress` during retries, returns to `triaged` after exhaustion |
| **Low confidence** (score < 0.5) | Run marked `needs_human_guidance` | Not a failure — admin reviews and approves/dismisses. | Stays `in_progress` |

## Validation Failures

| Failure Type | What Happens | Retry? | Issue Status |
|-------------|-------------|--------|-------------|
| **Tests fail** (`test_regression`) | Validation marked `failed`, run gets failure explanation | No auto-retry. User can retry or review diff. | Returns to `triaged` |
| **Security violation** | Validation marked `failed` | Never auto-retry. Cannot be overridden. | Returns to `triaged` |
| **Direction/quality/correctness fail** | Validation marked `failed` | No auto-retry. Admin can override (except security). | Returns to `triaged` |
| **CI failure** | Validation marked `failed` | No auto-retry. May be flaky CI — user can retry. | Returns to `triaged` |

## Pipeline Failures

| Failure Type | What Happens | Retry? |
|-------------|-------------|--------|
| **Webhook processing fails** | `webhook_deliveries.status = failed`, attempts incremented | Up to 3 retries with exponential backoff (1s, 4s, 16s). After exhaustion: logged, polling worker catches it on next sync. |
| **Polling sync fails** | `integration_sync_runs.status = failed` | Next scheduled sync (every 5 min). After 3 consecutive failures: integration status set to `error`, alert shown in UI. |
| **PR creation fails** | Job retried | Up to 3 attempts. After exhaustion: run stays `completed` with no PR, admin notified. |
| **Experiment evaluation fails** | Experiment stays in `observing` | Retried on next evaluation cycle. After 3 failures: outcome set to `inconclusive`. |

## Post-Deploy Regression

When an experiment outcome is `regression`:
1. Issue transitions from `observing` → `triaged` (making it eligible for re-attempt)
2. A `production_learnings` record is created with `severity = high`
3. Admin is notified with the regression details
4. The system does NOT automatically revert the PR — revert is a manual admin action
5. The learning is injected into future agent runs on similar issues

# Tech Stack

## Backend: Go

- **HTTP Router**: `go-chi/chi` — lightweight, stdlib-compatible
- **Database Driver**: `jackc/pgx` — fastest Go Postgres driver
- **Database Access**: Direct pgx v5 — type-safe store functions with `CollectRows`/`RowToStructByName`, no ORM or codegen
- **Migrations**: `golang-migrate/migrate`
- **Logging**: `rs/zerolog` -> Mezmo (log aggregation)
- **Monitoring**: Datadog (`DataDog/datadog-go` + `DataDog/dd-trace-go`) for metrics, APM, alerting
- **Container Management**: Docker SDK (`docker/docker`)

## Frontend: Next.js + React + shadcn/ui

**Framework Decision**: Next.js (App Router) was chosen over Nuxt (Vue) and SvelteKit because:

1. **shadcn/ui is native React** — no adaptation layer needed. Vue and Svelte ports exist but are less mature.
2. **AI ecosystem** — Vercel AI SDK, React Server Components for streaming agent logs, and the broadest AI tooling support all target React/Next.js first.
3. **Contributor base** — React has the largest community, making it easiest for open-source contributors.

Key libraries:
- **UI Components**: shadcn/ui (Radix UI + Tailwind)
- **Server State**: TanStack Query (React Query)
- **Charts**: Recharts
- **Validation**: Zod
- **Icons**: Lucide React

## Database: PostgreSQL 18

Single system of record. Bundled in Docker Compose for local dev, swappable to managed Postgres (RDS, Cloud SQL) for production.

## Logging & Monitoring

- **Mezmo**: Primary log aggregation. Structured JSON logs shipped via Mezmo's ingestion API. Used for log search, alerting, and archival.
- **Datadog**: Primary monitoring/observability. Custom metrics (HTTP, job queue, agent runs, cluster health), APM distributed tracing, pre-built dashboards, and alerting. Also used as a metrics source for Step 6 experiment evaluation (pull production latency/error rates to measure fix impact).

# Design Documents

| # | Document | Description | Status |
|---|----------|-------------|--------|
| 01 | [Database Schema](01-database-schema.md) | PostgreSQL tables, indexes, relationships | Draft |
| 02 | [API Server](02-api-server.md) | Go backend structure, routes, middleware, workers | Draft |
| 03 | [Frontend](03-frontend.md) | Next.js + shadcn/ui architecture, pages, data fetching | Draft |
| 04 | [Ingestion Pipeline](04-ingestion.md) | Webhooks, polling, normalization, deduplication | Draft |
| 05 | [Prioritization Engine](05-prioritization.md) | Scoring algorithm, admin controls, auto-triggering | Draft |
| 06 | [Agent Orchestrator](06-agent-orchestrator.md) | Sandbox management, agent adapters, log streaming | Draft |
| 07 | [Validation Pipeline](07-validation.md) | LLM-based code review, CI checks, fail-fast | Draft |
| 08 | [PR & Ship](08-pr-and-ship.md) | GitHub PR creation, review, deploy detection | Draft |
| 09 | [Observability](09-observability.md) | Impact measurement, experiments, outcome classification | Draft |
| 10 | [Infrastructure](10-infrastructure.md) | Docker, deployment, horizontal scaling, Mezmo, Datadog | Draft |
| 11 | [Review Feedback Loop](11-review-feedback-loop.md) | PR review capture, auto-apply, review patterns KB, acceptance tracking | Draft |
| 12 | [Smart Issue Routing](12-smart-routing.md) | Complexity estimation, execution aggressiveness, confidence scoring | Draft |
| 13 | [Repository Onboarding](13-repository-onboarding.md) | GitHub OAuth + App auth, repo connection, cloning strategy | Draft |
| 14 | [Codebase Context Layer](14-codebase-context.md) | Context packages, file maps, conventions, quality scoring | Draft |
| 15 | [Time to First Fix](15-time-to-first-fix.md) | Demo mode, quick-win scan, progress UX, onboarding optimization | Draft |
| 16 | [AI Agent Evaluation System](16-ai-agent-evals.md) | Offline/online eval architecture, grader design, release gates, and automated eval flywheel | Draft |
| 17 | [Failure Communication](17-failure-communication.md) | Failure taxonomy, human-readable explanations, system learning from failures, trust progression, fix rate transparency | Draft |
| 18 | [Fix Quality Feedback Loop](18-fix-quality-feedback.md) | Production outcome analysis, ineffective fix learning, anti-pattern detection | Draft |
| 20 | [Security Architecture](20-security-architecture.md) | Threat model, sandbox hardening, prompt injection defense, secret management, RBAC | Draft |
| 21 | [First-Run Experience](21-first-run-experience.md) | Onboarding flow, quick-start issue scan, time-to-value optimization | Draft |
| 22 | [Notification System](22-notifications.md) | Event taxonomy, multi-channel delivery, user preferences, escalation | Draft |
| 23 | [Auto-Closing Feedback Loops](23-auto-closing-feedback-loops.md) | Self-tuning loops for complexity calibration, agent defaults, context, conventions | Draft |
| 24 | [Design Resolutions](24-design-resolutions.md) | Cross-document clarifications, conflict resolutions, decision flowcharts | Draft |
| 28 | [AI Product Manager Agent](28-agent-ticket-prioritization.md) | Batch PM agent that analyzes all issues, produces prioritized work plans, and delegates to coding agents | Draft |

# Build Order

The system should be built in phases. Each phase produces a usable milestone. The ordering principle is: **get to "Sentry error → PR" as fast as possible.** That's the demo. That's the tweet. That's the moment a user decides this product is real.

## Phase 1: Foundation + Repo Onboarding (docs: 01, 02, 03, 10, 13) — COMPLETE

Build the skeleton that everything else plugs into, including GitHub authentication and repo connection.

1. **Database schema + migrations** (01) — ✅ Two migration files: `000001_init` (orgs, users, sessions, repos, integrations, jobs, nodes, audit_log) and `000002_core_domain` (issues, agent_runs, validations, PRs, deploys)
2. **Go API server scaffold** (02) — ✅ Chi v5 router with 8+ middleware (auth, CORS, logging, rate limiting, metrics, RBAC, body limits, webhook signature verification). Handlers for health, auth, repos, webhooks, issues, runs, settings.
3. **GitHub OAuth flow** (13) — ✅ Login/callback/logout with state token CSRF protection, user upsert, 30-day sessions, HttpOnly cookies
4. **GitHub App setup** (13) — ✅ Installation webhook handling (created/deleted). JWT token generation + caching. Manifest-based app creation endpoint not yet implemented (convenience feature, not blocking).
5. **Repository management** (13) — ✅ Full CRUD store, UpsertFromGitHub for idempotent webhook sync, DisconnectByInstallationID for cleanup, installation token management
6. **Frontend scaffold** (03) — ✅ Next.js 16 + App Router + shadcn/ui + TanStack Query. Pages: Overview, Issues, Runs, Settings, Analytics (placeholder), Costs (placeholder). Vitest test suite with MSW mocks.
7. **Docker Compose + Makefile** (10) — ✅ Postgres 17 + Go server (air hot reload) + Next.js frontend. Makefile with dev, test, migrate, build, lint targets.
8. **Success metrics instrumentation** — ✅ Prometheus metrics middleware (http_requests_total, http_request_duration_seconds, http_requests_in_flight). `/metrics` endpoint. CI/CD via GitHub Actions with coverage gates (70% backend, 80% frontend).

**Milestone**: ✅ You can start the app, sign in with GitHub, connect repositories, and see connected repos in the dashboard. Core metrics are being captured from the first run.

## Phase 2: Sentry Ingestion (doc: 04) — COMPLETE

Connect Sentry first. It's the highest-signal, most automated source — stack traces give agents exactly what they need.

1. **Sentry webhook endpoint** — ✅ `HandleSentry()` in ingestion_webhooks.go with signature verification, delivery tracking, supports created/regression events
2. **Sentry adapter** — ✅ `SentryAdapter` parses webhooks, extracts stack traces, severity mapping, occurrence/customer counts, tags, timestamps. Full test coverage.
3. **Normalization + deduplication** — ✅ `NormalizedIssue` struct with `sha256(source:externalID)` fingerprinting, `ON CONFLICT` upsert with smart merging (increment occurrences, max customer count, update severity)
4. **Polling worker** — ✅ `SentryAPIClient` in sentry_api.go with project issues polling, `sync_sentry` job handler in worker/handlers.go, uses `integration_sync_runs` for tracking sync state
5. **Issues UI** — ✅ Data table with severity/status/source badges, occurrence count, customer count, relative timestamps. Filter dropdowns for status, source, and severity fully implemented in frontend. Backend supports cursor pagination.

**Milestone**: ✅ Sentry errors appear in the dashboard via both webhooks and polling sync. Issues can be filtered and browsed with full UI controls.

## Phase 3: Agent Execution + Validation + PR (docs: 06, 07, 08, 17) — COMPLETE

**This is the "aha moment."** Connect a repo, see a Sentry error, click "Fix This," get a PR. Ship these three together because none is useful alone.

The core execution pipeline is fully wired end-to-end. DB schema, stores, API handlers, services, and frontend are all implemented. All 6 validation checks are now implemented.

1. **Sandbox container management** — ✅ Docker SDK integration in `providers/docker.go` with full container lifecycle (Create/CloneRepo/Exec/ReadFile/WriteFile/Destroy). gVisor runtime support, security hardening (dropped capabilities, read-only rootfs, non-root user, PID limits, tmpfs with noexec). Configurable CPU/memory/timeout limits.
2. **Claude Code adapter** — ✅ `adapters/claude_code.go` implements AgentAdapter interface. `PreparePrompt()` builds system+user prompts with stack trace extraction and file hints. `Execute()` runs Claude Code CLI in sandbox, parses streaming JSON output, collects git diff. Prompt injection defense included.
3. **Agent orchestrator** — ✅ `orchestrator.go` implements full run lifecycle: concurrency check per org → status update → fetch issue/repo → get adapter → prepare prompt → create sandbox → clone repo → execute agent with log streaming → confidence gating → enqueue follow-up jobs (validate or analyze_failure) → cleanup. Worker handlers (`run_agent`, `validate`, `open_pr`, `analyze_failure`) are wired to services.
4. **Basic context injection** — ✅ `PreparePrompt()` injects repository conventions from ContextDocs. `extractFileHints()` pulls file paths from Sentry stack trace frames. `extractStackTrace()` produces human-readable stack traces from Sentry raw data.
5. **Confidence scoring** — ✅ Claude Code adapter extracts confidence_score, confidence_reasoning, and risk_factors from agent JSON output. Orchestrator applies threshold gating: score < 0.5 → `needs_human_guidance`, score >= 0.5 → proceed to validation.
6. **Human-in-the-loop** — ✅ Orchestrator detects "question" log entries, creates `AgentRunQuestion` records, sets run status to `awaiting_input`. API endpoints exist for listing questions (`GET /runs/{id}/questions`) and answering them (`POST /runs/{qid}/answer`).
7. **Log streaming** — ✅ SSE endpoint (`GET /runs/{id}/logs`) in `runs.go` with HTTP Flusher for real-time streaming. Sends existing logs first, then polls every 1s for new entries. Frontend `LogViewer` component connects via EventSource with auto-reconnection.
8. **Full validation pipeline** — ✅ `validation/service.go` implements all 6 checks in fail-fast order: (1) **direction_check** — LLM verifies fix aligns with issue and product direction, (2) **correctness_check** — LLM verifies logical correctness, no introduced bugs, (3) **regression_test_check** — LLM verifies regression test is included, (4) **security_scan** — regex-based secret/SQLi detection, (5) **quality_check** — diff size limits (warn >200, fail >500 lines), (6) **ci_check** — detects project type and runs tests. LLM checks use an injectable `LLMClient` interface for testability. Diffs wrapped in `<code_diff>` tags for prompt injection defense. Graceful fallback to "skipped" when LLM is not configured. Validate method accepts issue context for LLM checks.
9. **PR creation** — ✅ `github/pr.go` implements full GitHub API flow: get base branch SHA → create branch → parse diff → create blobs/tree/commit → update ref → create PR → add labels → store in DB → update run and issue status. PR body includes agent summary, issue metadata, and validation results.
10. **PR tracking** — ✅ Full `PullRequestStore` with CRUD operations. Webhook handlers process `pull_request` events (merged/closed tracking, deploy record creation) and `pull_request_review` events (approval/changes_requested tracking).
11. **Failure communication** (17) — ✅ Rule-based `FailureService` in `failure.go` classifies 9 failure types (timeout, sandbox crash, API error, build failure, empty diff, test regression, security violation, large diff, missing context). Each produces human-readable explanation, category, sub-type, next steps, and retry recommendation. Persisted to DB and displayed in frontend.
12. **Fix Queue UI** — ✅ Runs list page with grouped tabs (All/Active/Needs Review/Failed/Completed), status badges, confidence scores, duration display. Run detail page with tabs: Overview (status/confidence/timestamps/result), Logs (live streaming LogViewer), Diff (DiffViewer component), Validation (results table for all 6 checks), PR (GitHub link/status/review status/branch/body). Failure details section shows explanation, category, next steps as bulleted list, and retry button.

**Milestone**: ✅ The core "Sentry error → Fix This → agent run → validation → PR" pipeline is fully complete including all 6 validation checks.

## Phase 4: Prioritization + Routing (docs: 05, 12) — COMPLETE

Now that fixes are flowing, rank issues so the most impactful ones surface first.

1. **Scoring algorithm** — ✅ `prioritization/service.go` implements full composite scoring with configurable weights (default: customer_impact=0.35, severity=0.25, recency=0.20, revenue_risk=0.20). Sub-scores: `computeCustomerImpact` (log2-scaled from affected_customer_count), `computeSeverity` (critical=100 → low=25), `computeRecency` (exponential decay with 168h half-life). Direction alignment via LLM call clamped to [-1,1], applied as `score * (1 + 0.3*alignment)`. Eligibility gating: direction > -0.5, status open/triaged, score > org threshold. Stores results via `PriorityScoreStore.Upsert` and `ComplexityEstimateStore.Upsert`. Auto-enqueued after issue ingestion via `ingestion/service.go`.
2. **Complexity estimation** — ✅ `prioritization/service.go` `EstimateComplexity` uses LLM to classify issues into 5 tiers (trivial/simple/moderate/complex/very_complex) with confidence scores, issue type, reasoning, estimated files/tokens. Heuristic fallback based on severity when LLM unavailable.
3. **Auto-trigger** — ✅ `CheckAutoTrigger` implements 4 sequential gates: (1) autonomy level must be "auto_all" or "auto_simple", (2) if auto_simple, complexity tier must be ≤2, (3) aggressiveness tier limit (`conservative=2, moderate=3, aggressive=4, maximum=5`) must not be exceeded, (4) concurrent running agent count must be below org's max_concurrent cap. On pass, enqueues a `run_agent` job.
4. **DB stores** — ✅ `db/priority_scores.go` with Upsert (ON CONFLICT issue_id DO UPDATE), GetByIssueID, ListByOrg (with eligible_only filter, ORDER BY score DESC), DeleteByIssueID. `db/complexity_estimates.go` with Upsert, GetByIssueID, ListByOrg (with optional maxTier filter).
5. **API endpoints** — ✅ `handlers/priority.go` exposes: GET `/api/v1/issues/{id}/priority` (viewer+), GET `/api/v1/issues/{id}/complexity` (viewer+), GET `/api/v1/priority-scores` with `eligible_only` filter (viewer+), POST `/api/v1/issues/{id}/reprioritize` (admin-only, enqueues prioritize job with dedup key).
6. **Worker handler** — ✅ `worker/handlers.go` `prioritize` handler calls `ComputeScore` → `EstimateComplexity` → `CheckAutoTrigger` in sequence. Validate handler updated to fetch issue and pass to validation service for LLM context.
7. **Settings UI** — ✅ Settings page rewritten with: Agent Execution controls (autonomy level select, aggressiveness select, max concurrent input), Confidence Thresholds (auto-proceed and human review sliders), Prioritization section (product direction textarea, priority weight grid with real-time sum validation, minimum score threshold). Save via PATCH with success/error feedback.
8. **Priority display** — ✅ Issues page enhanced with: priority score badge (green ≥70, yellow ≥40, gray <40), complexity tier badge (green trivial/simple, yellow moderate, red complex/very_complex), eligibility indicator dot (green/gray), sort dropdown (Last seen / Priority). Priority sort uses LEFT JOIN with priority_scores, ORDER BY score DESC NULLS LAST.
9. **Issues sort by priority** — ✅ `db/issues.go` IssueFilters extended with Sort field. When `Sort == "priority"`, query uses LEFT JOIN on priority_scores table. Frontend passes sort param via `useQueryState`.

**Milestone**: ✅ Full prioritization pipeline: ingestion → auto-score → complexity estimate → auto-trigger gates → agent run. Settings UI gives orgs control over autonomy, aggressiveness, weights, and direction.

## Phase 5: Observability + Impact (docs: 09, 18) — NOT STARTED (partial deploy tracking exists)

Close the loop — measure whether fixes actually helped.

`deploys` table exists and deploy records are already created automatically when PRs are merged (via `HandlePullRequestEvent` in `github/pr.go`). Experiments/metrics tables are missing. No experiment or outcome logic:

1. **Deploy detection** — ⚠️ PARTIAL. Deploy records are created automatically on PR merge (github/pr.go:255-264) with commit SHA and environment. However, there is no external deploy webhook handler (e.g., from CI/CD systems) for non-PR deploys.
2. **Baseline + observation metric collection** — ❌ No experiments table, no metric collection
3. **Outcome classification** — ❌ No comparison or classification logic
4. **Impact display** — ❌ No impact UI
5. **Production outcome feedback loop** (18) — ❌ No outcome analysis or learning injection

**Milestone**: ❌ Unblocked — Phases 3 and 4 are complete. PRs are shipping and prioritization is live.

## Phase 6: Review Feedback Loop (doc: 11) — COMPLETE

Turn human PR reviews into agent improvements.

1. **Review comment capture + processing pipeline** — ✅ `review_comments` table with migration, `ReviewCommentStore` (Create with ON CONFLICT dedup, GetByID, ListByOrg with filters, ListByPullRequest, ListActionableByPullRequest, UpdateClassification, MarkApplied, CountPendingByPR). Webhook handlers capture both `pull_request_review` (top-level review body) and `pull_request_review_comment` (inline diff comments). Multi-stage processing pipeline in `feedback/service.go`: structural pre-filter (bot accounts, short comments, emoji-only, CI patterns) → LLM classification (actionable, category, generalizable, generalized rule) → pattern dedup. Job queue integration via `process_review_comment` and `update_review_patterns` handlers.
2. **Auto-apply feedback** — ✅ `RevisionContext` type added to `AgentInput`. Claude Code adapter injects revision instructions (formatted feedback, comment summary, previous diff) into system prompt for revision runs. `PRService.PushRevision` method pushes commits to existing PR branch via GitHub API (get head SHA → create blobs/tree/commit → update ref → post summary comment). `FormatRevisionFeedback` in feedback service formats actionable comments for prompt injection.
3. **Review patterns KB** — ✅ `review_patterns` table with insert-only versioning pattern. `ReviewPatternStore` (Create, GetByID, ListByRepo with status filter, ListActiveByRepo, FindMatchingRule case-insensitive, UpdatePattern with insert-only versioning, IncrementOccurrence with auto-promotion candidate→active at 2+ occurrences). API endpoints: GET `/review-patterns/*` (viewer+), PATCH `/review-patterns/{id}` status update (admin), PUT `/review-patterns/{id}` rule edit (admin). Frontend API client wired.
4. **Curated context document** — ✅ `GenerateConventionsDoc` in feedback service produces `.143/learned-conventions.md` content grouped by category (Style, Logic, Edge Cases, Architecture, Testing, Security, Performance, Nits) with occurrence counts. API endpoint for review comments listing: GET `/review-comments` with pull_request_id and filter_status filters.

**Milestone**: ✅ Full review feedback loop: webhook capture → processing pipeline → pattern KB → conventions doc generation → revision context injection.

## Phase 7: Codebase Context — Advanced (doc: 14) — NOT STARTED

Deepen the context layer based on what you've learned from real agent runs about what context actually matters.

`repositories` table has a `context_quality` column ready, but context package tables (`repo_context_packages`, `repo_context_entries`, `repo_file_map`) are not created:

1. **File map generation** — ❌ No tables, no LLM classification
2. **Convention extraction** — ❌ No extraction logic
3. **Test infrastructure discovery** — ❌ No discovery logic
4. **Quality scoring** — ❌ DB column exists on repos, no scoring algorithm
5. **Incremental updates** — ❌ No push webhook context updates
6. **Context UI** — ❌ No context UI

**Milestone**: ❌ Unblocked — Phases 3-4 provide real agent runs and prioritization data to learn from.

## Phase 8: Evals + Quality Gates (doc: 16) — NOT STARTED

Now that you have real production data and observed failure modes, build the evaluation system on solid ground.

No eval infrastructure tables exist. Entirely future work:

1. **Eval taxonomy + schema** — ❌ No eval tables
2. **Dataset pipeline** — ❌ No dataset infrastructure
3. **Grader stack** — ❌ No grader implementation
4. **Release gates + rollout** — ❌ No release gate tables or logic
5. **Continuous eval flywheel** — ❌ No flywheel

**Milestone**: ❌ Partially unblocked — Phases 3-4 are complete, Phase 5 still needed for full production data.

## Future: Additional Ingestion Sources

After the core loop is proven with Sentry:

- **Linear ingestion** — webhook + polling adapter, issue type classification
- **Support tool ingestion** — Zendesk/Intercom adapters, customer pain extraction
- **Additional agent adapters** — Codex, Gemini CLI, custom agents
- **Time to First Fix optimization** (doc 15) — demo mode, quick-win scan, progress UX

# Architecture

143.dev is designed to be:

- Open source first
- Self-hostable in minutes, but scalable to multi-machine production with a one-line setup command
- Simple in local development
- [If needed] to be extensible into a hosted enterprise cloud in the future

## Horizontal Scaling Model

143.dev uses a **symmetric, peer-based architecture** with Postgres as the sole coordination layer. There is no special "primary" node — every node runs the same binary and can serve any role:

- **`--mode=all`** (default): Runs API + workers + scheduler. Multiple `all` nodes can run simultaneously — the scheduler uses a Postgres advisory lock so only one instance runs cron jobs, with automatic failover if that node dies.
- **`--mode=api`**: API + UI only. Stateless. Scale behind a load balancer.
- **`--mode=worker`**: Job processing + agent sandboxes only. Scale for compute.

No service discovery or orchestrator required. A new node joins the cluster by pointing at the same `DATABASE_URL`. See [10-infrastructure.md](10-infrastructure.md) for full details.

## Systems

### PostgreSQL

Postgres will serve as the primary system of record. It will store:

- Ingested issues (support, Sentry, Linear)
- Prioritization metadata
- Agent runs
- Validation results
- PR links and deploy events
- Experiments and outcomes
- Audit trail

Initially, Postgres will be bundled into the single setup container, but we’ll build it so that it’s easy to migrate to RDS or some hosted Postgres system in the future.

### Coding container runtime

Each agent run will execute inside of an isolated sandbox. Each will include:

- resource limits (CPU, memory, time)
- restricted filesystem
- controlled network access

### Web application container

The main 143.dev container includes:

- API server
- web UI
- background worker loop
- job scheduler
- post-deploy experiment evaluator
- Integration logic for:
    - Github: PR creation, status checks, deploy signals
    - Sentry: Issue webhooks as well as retrieval of issues via the API. Also linkage of issues to Github PRs in the PR body.
    - Linear: Webhooks and retrieval of issues via the API. Also linkage of issues to Github PRs in the PR title.

# Dashboard onboarding UX

- The Overview dashboard keeps users in setup context when configuring coding agents.
- In the "Connect your coding agent" card, the **Settings** action opens an in-place modal for common agent edits (default agent selection and provider credentials).
- The modal includes a secondary path to advanced agent settings at `/settings/agents` for deeper configuration.
- The UX goal is fast in-flow completion first, with a clear handoff to advanced controls when needed.

# **Why 143?**

The name comes from the XP-80 Shooting Star project. In 1943, a small team at Lockheed Skunk Works designed and built the first US jet fighter in exactly **143 days**. They did it by killing the bureaucracy and giving a small, autonomous team the freedom to execute.

That's the logic behind **143.dev**.

Fixing production bugs usually sucks because of the overhead like parsing logs, reproducing state, and context switching, not necessarily because the fix itself is hard. We use **autonomous AI agents** to handle that grunt work. The agents act like your Skunk Works team: they isolate the issue, verify the root cause, and tee up the solution so you can just ship it.

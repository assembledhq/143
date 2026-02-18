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
- Step 2: Prioritize and identify top issues based on business impact
    - The system determines how many customers were affected, regression severity, and optionally (if you integrate Salesforce or some other CRM) the revenue risk.
    - The admins can specify the product direction they want to move towards, to make sure that any issues that don’t jive with the product direction are filtered out.
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

## Phase 2: Sentry Ingestion (doc: 04) — ~80% COMPLETE

Connect Sentry first. It's the highest-signal, most automated source — stack traces give agents exactly what they need.

1. **Sentry webhook endpoint** — ✅ `HandleSentry()` in ingestion_webhooks.go with signature verification, delivery tracking, supports created/regression events
2. **Sentry adapter** — ✅ `SentryAdapter` parses webhooks, extracts stack traces, severity mapping, occurrence/customer counts, tags, timestamps. Full test coverage.
3. **Normalization + deduplication** — ✅ `NormalizedIssue` struct with `sha256(source:externalID)` fingerprinting, `ON CONFLICT` upsert with smart merging (increment occurrences, max customer count, update severity)
4. **Polling worker** — ❌ NOT STARTED. No Sentry API client, no polling job handler, `integration_sync_runs` table exists but unused. Needed as catch-all for missed webhooks.
5. **Issues UI** — ⚠️ PARTIAL. Data table shows severity/status/source badges, occurrence count, customer count, relative timestamps. Backend supports status/source/severity filters + cursor pagination, but **filter UI controls not exposed in frontend**.

**Milestone**: ⚠️ Sentry errors appear in the dashboard via webhooks, but polling sync (catch-all) and frontend filter controls are missing.

## Phase 3: Agent Execution + Validation + PR (docs: 06, 07, 08, 17) — NOT STARTED (schema only)

**This is the "aha moment."** Connect a repo, see a Sentry error, click "Fix This," get a PR. Ship these three together because none is useful alone.

DB schema for all Phase 3 entities exists (agent_runs, agent_run_logs, agent_run_questions, validations, pull_requests, deploys) and basic API handlers exist for listing/getting runs, but the entire execution pipeline is unimplemented:

1. **Sandbox container management** — ❌ No Docker/gVisor integration, no container lifecycle code
2. **Claude Code adapter** — ❌ No agent adapter interface or Claude Code CLI integration
3. **Agent orchestrator** — ❌ No run lifecycle management or concurrency control. Worker handlers are stubs.
4. **Basic context injection** — ❌ No CLAUDE.md/AGENTS.md reading or stack trace file extraction
5. **Confidence scoring** — ⚠️ DB field + frontend display exist, but no scoring logic or threshold gating
6. **Human-in-the-loop** — ⚠️ `agent_run_questions` table exists, but no API endpoints, no orchestrator pause logic, no frontend UI
7. **Log streaming** — ❌ No SSE endpoint, no log channel handling, no frontend EventSource client
8. **Basic validation** — ❌ `validations` table exists but no validation service, no test/security/diff checks
9. **PR creation** — ❌ GitHub service exists as stub but no branch/commit/PR creation logic
10. **PR tracking** — ⚠️ `pull_requests` table exists, webhook handler scaffolded, but no PR store or status update logic
11. **Failure communication** (17) — ⚠️ DB fields exist (failure_explanation, failure_category, failure_next_steps), frontend displays them, but no failure analysis service
12. **Fix Queue UI** — ⚠️ Basic runs list page exists with status/confidence/failure display. Missing: run detail page with tabs (logs, diff, PR), live log viewer, agent questions UI.

**Milestone**: ❌ Not yet achievable. This is the next major phase to build.

## Phase 4: Prioritization + Routing (docs: 05, 12) — NOT STARTED (schema only)

Now that fixes are flowing, rank issues so the most impactful ones surface first.

DB tables exist for `priority_scores` and `complexity_estimates`, but no business logic is implemented:

1. **Scoring algorithm** — ❌ `priority_scores` table ready, `prioritize` job handler is a stub. No scoring logic, no direction alignment LLM call.
2. **Auto-trigger** — ❌ No auto-trigger logic, no autonomy level enforcement, no concurrency cap
3. **Settings UI** — ❌ Settings page exists but only shows integration stubs. No priority weight, product direction, or auto-fix controls.
4. **Priority display** — ❌ Issues table shows severity/status but not priority scores or explainability

**Milestone**: ❌ Depends on Phase 3 completion first (fixes must be flowing before prioritization matters).

## Phase 5: Observability + Impact (docs: 09, 18) — NOT STARTED

Close the loop — measure whether fixes actually helped.

`deploys` table exists in schema but experiments/metrics tables are missing. No implementation:

1. **Deploy detection** — ❌ No webhook handler for deploy events
2. **Baseline + observation metric collection** — ❌ No experiments table, no metric collection
3. **Outcome classification** — ❌ No comparison or classification logic
4. **Impact display** — ❌ No impact UI
5. **Production outcome feedback loop** (18) — ❌ No outcome analysis or learning injection

**Milestone**: ❌ Depends on Phase 3 (PRs must be shipping before impact can be measured).

## Phase 6: Review Feedback Loop (doc: 11) — NOT STARTED

Turn human PR reviews into agent improvements.

`agent_runs` has `parent_run_id` and `revision_context` columns ready for revision runs, but `review_comments` and `review_patterns` tables are not yet created:

1. **Review comment capture + processing pipeline** — ❌ No review comment table, no capture logic
2. **Auto-apply feedback** — ❌ DB columns ready in agent_runs, but no revision run logic
3. **Review patterns KB** — ❌ No review_patterns table or pattern extraction
4. **Curated context document** — ❌ No `.143/learned-conventions.md` generation

**Milestone**: ❌ Depends on Phase 3 (PRs must exist to receive reviews).

## Phase 7: Codebase Context — Advanced (doc: 14) — NOT STARTED

Deepen the context layer based on what you've learned from real agent runs about what context actually matters.

`repositories` table has a `context_quality` column ready, but context package tables (`repo_context_packages`, `repo_context_entries`, `repo_file_map`) are not created:

1. **File map generation** — ❌ No tables, no LLM classification
2. **Convention extraction** — ❌ No extraction logic
3. **Test infrastructure discovery** — ❌ No discovery logic
4. **Quality scoring** — ❌ DB column exists on repos, no scoring algorithm
5. **Incremental updates** — ❌ No push webhook context updates
6. **Context UI** — ❌ No context UI

**Milestone**: ❌ Depends on Phase 3 having real agent runs to learn from.

## Phase 8: Evals + Quality Gates (doc: 16) — NOT STARTED

Now that you have real production data and observed failure modes, build the evaluation system on solid ground.

No eval infrastructure tables exist. Entirely future work:

1. **Eval taxonomy + schema** — ❌ No eval tables
2. **Dataset pipeline** — ❌ No dataset infrastructure
3. **Grader stack** — ❌ No grader implementation
4. **Release gates + rollout** — ❌ No release gate tables or logic
5. **Continuous eval flywheel** — ❌ No flywheel

**Milestone**: ❌ Depends on having real production data from Phases 3-5.

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

# **Why 143?**

The name comes from the XP-80 Shooting Star project. In 1943, a small team at Lockheed Skunk Works designed and built the first US jet fighter in exactly **143 days**. They did it by killing the bureaucracy and giving a small, autonomous team the freedom to execute.

That's the logic behind **143.dev**.

Fixing production bugs usually sucks because of the overhead like parsing logs, reproducing state, and context switching, not necessarily because the fix itself is hard. We use **autonomous AI agents** to handle that grunt work. The agents act like your Skunk Works team: they isolate the issue, verify the root cause, and tee up the solution so you can just ship it.

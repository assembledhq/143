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
- Step 3: Estimate complexity and route to the right execution strategy
    - Before running an agent, the system estimates issue complexity using the issue description, stack traces, codebase context, and historical outcomes.
    - The system classifies the issue type (bug fix, error handling, performance, refactor, feature gap, security) to select the right agent prompt strategy and validation criteria.
    - Admins set a **confidence threshold** that controls which issues the system will attempt. Issues below the threshold are automatically skipped (for auto-triggered runs) or flagged with a warning (for manual triggers).
- Step 4: Execute a coding agent
    - The admins define the level of autonomy (e.g. human kickoff only vs. automate kickoff for simple issues vs automatic kickoff for everything) and the spend they want (e.g. low token mode vs high token mode).
    - Admins choose their preferred coding agent (Claude Code, Codex, Gemini CLI, etc.) and model. The system always uses the configured agent and model.
    - The agent runs in a sandbox and produces a code diff.
    - The agent outputs a **confidence score** with its fix. Low-confidence runs are paused for human guidance before proceeding to validation.
- Step 5: Validate correctness
    - The system checks the code and ensures that
        1. it works towards the right product direction
        2. the code is correct
        3. the code is high quality and a simple, minimal diff
        4. the fix includes a regression test that would have caught the original bug (required for Sentry errors and support tickets)
        5. the code passes all CI/CD checks and coverage is not reduced
- Step 6: Open PR and ship
    - The system opens a new PR on github, using whatever Github template already exists
    - It makes sure to attach the relevant Linear issue to the PR title, or references the original sentry issue / customer complaint
    - Sends the PR for human review (depending on the settings, could be a push notification or just puts it out for a group of reviewers).
- Step 7: Observe impact and close the customer loop
    - After a fix is deployed, the system automatically evaluates whether it reduced real customer pain.
    - Each shipped PR captures baseline production metrics before deploy and an observation window after deploy. After a deploy, the system will do automated checks to attribute impact.
    - It will measure:
        - Sentry error rate changes
        - support ticket volume changes
        - latency or reliability improvements
    - Finally the system classifies the outcomes as successful or not.
- Step 8: PR review feedback → agent improvement loop
    - By default, review comments on 143-generated PRs are captured and run through a multi-stage filtering pipeline (structural pre-filter → merge-gate → adoption check → directive detection → classification → dedup). An org setting can expand this to all PRs.
    - When a reviewer requests changes on a 143-generated PR, the system offers to re-run the agent with that feedback incorporated (auto-apply), rather than making the human fix it manually.
    - Generalizable reviewer feedback is accumulated into a per-repo knowledge base and materialized as a `.143/learned-conventions.md` file in the repo — version-controlled, transparent, and editable by the team. The agent reads this file as part of its context for all future runs.
    - Reviewer trust tiers (maintainer, contributor, external) control how quickly patterns are promoted. Adoption evidence (was the suggestion reflected in the final merged code?) further weights pattern confidence.
    - Reviewer acceptance rates are tracked per issue type, so the system learns which categories of fixes the agent handles well vs. poorly.
    - This creates a flywheel: every human review makes every future agent run better.

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
| 17 | [Failure Communication](17-failure-communication.md) | Human-readable failure explanations, fix rate transparency, next-step suggestions | Draft |
| 18 | [Fix Quality Feedback Loop](18-fix-quality-feedback.md) | Production outcome analysis, ineffective fix learning, anti-pattern detection | Draft |
| 20 | [Security Architecture](20-security-architecture.md) | Threat model, sandbox hardening, prompt injection defense, secret management, RBAC | Draft |
| 21 | [First-Run Experience](21-first-run-experience.md) | Onboarding flow, quick-start issue scan, time-to-value optimization | Draft |
| 22 | [Notification System](22-notifications.md) | Event taxonomy, multi-channel delivery, user preferences, escalation | Draft |

# Build Order

The system should be built in phases. Each phase produces a usable milestone.

## Phase 0: AI Agent Evaluation System (doc: 16)

Define evaluation infrastructure before scaling autonomous execution.

1. **Eval taxonomy + schema** — define outcome, trace, policy, and impact eval dimensions with standard failure codes
2. **Dataset pipeline** — build golden, shadow, and adversarial sets from curated and production-derived issues
3. **Grader stack** — implement deterministic checks plus calibrated LLM-as-judge graders
4. **Release gates + rollout** — enforce offline gates and staged canary rollout with auto-rollback
5. **Continuous eval flywheel** — convert production failures into new eval cases automatically

**Milestone**: Every agent configuration change is gated by reproducible evals and continuously monitored with automatic feedback into the eval suite.

## Phase 1: Foundation + Repo Onboarding (docs: 01, 02, 03, 10, 13)

Build the skeleton that everything else plugs into, including GitHub authentication and repo connection.

1. **Database schema + migrations** (01) — set up Postgres, create initial tables (including `repositories`, `repo_context_packages`, `repo_context_entries`, `repo_file_map`)
2. **Go API server scaffold** (02) — chi router, middleware, health checks, basic CRUD for orgs/users
3. **GitHub OAuth flow** (13) — "Sign in with GitHub" for user authentication
4. **GitHub App setup** (13) — manifest-based app creation or manual setup, installation webhook handling
5. **Repository management** (13) — connect repos, store in `repositories` table, manage installation tokens
6. **Frontend scaffold** (03) — Next.js project, shadcn/ui setup, layout, empty pages, repo connection UI
7. **Docker Compose + Makefile** (10) — `docker compose up` runs Postgres + server + frontend

**Milestone**: You can start the app, sign in with GitHub, connect repositories, and see connected repos in the dashboard.

## Phase 1.5: Codebase Context (doc: 14)

Build the context layer that makes agents effective. This runs in parallel with ingestion setup.

1. **Context discovery** — scan connected repos for CLAUDE.md, AGENTS.md, CODEOWNERS, linter/formatter configs, CI config
2. **File map generation** — LLM-based classification of files into features and components, import graph analysis
3. **Convention extraction** — infer coding conventions from code samples and configs
4. **Test infrastructure discovery** — detect test frameworks, test commands, test patterns
5. **Dependency map** — build import graph, detect service dependencies, analyze component boundaries
6. **Quality scoring** — compute context quality score, generate improvement insights
7. **Incremental updates** — push webhook handler updates context on code changes
8. **Context UI** — view context quality, file map, conventions, and improvement suggestions

**Milestone**: Each connected repo has an auto-generated context package with a quality score. Teams can see what's well-documented vs. gaps. Context is injected into agent runs.

## Phase 2: Ingestion (doc: 04)

Connect to external systems and start filling the database.

1. **Webhook endpoints** — receive Sentry, Linear, GitHub webhooks
2. **Source adapters** — Sentry and Linear first (most common)
3. **Normalization + deduplication** — unified issue pipeline
4. **Polling workers** — scheduled sync jobs
5. **Issues UI** — data table with filters on the issues page

**Milestone**: Issues from Sentry and Linear appear in the dashboard in real time.

## Phase 3: Prioritization & Complexity Estimation (docs: 05, 12)

Rank issues so the most impactful ones surface, and estimate complexity before agent execution.

1. **Scoring algorithm** — compute priority scores
2. **Complexity estimation** — LLM-based classification of issue difficulty (trivial → very complex) and issue type (bug fix, performance, security, etc.)
3. **Settings UI** — weight configuration, product direction text, execution aggressiveness slider
4. **Dashboard stats** — top issues, aggregate counts
5. **Priority display** — scores, complexity tier, and explainability in the issues table

**Milestone**: Issues are ranked by business impact and complexity. Admins can tune the ranking and set how aggressively the system attempts fixes.

## Phase 4: Agent Execution (docs: 06, 12, 14)

The core differentiator — run coding agents to fix issues, with confidence gating and deep codebase context.

1. **Sandbox container management** — create, run, destroy Docker containers
2. **Claude Code adapter** — first agent integration
3. **Agent orchestrator** — run lifecycle, concurrency control, aggressiveness check
4. **Context injection** (14) — assemble relevant context from the repo context package (architecture docs, conventions, file map, test infra) and inject into agent prompts
5. **Agent & model selection** — admins choose their preferred agent (Claude Code, Codex, Gemini CLI) and model
6. **Confidence scoring** — agent outputs confidence score, low-confidence runs paused for human guidance
7. **Log streaming** — SSE endpoint + live log viewer in the UI
8. **Agent runs UI** — run list, detail page with logs, diff viewer, confidence indicator
9. **Execution strategy settings** — aggressiveness slider, confidence thresholds

**Milestone**: Click "Fix this" on an issue and watch an AI agent generate a code fix in real time. The agent has deep context about the repo's architecture, conventions, and file structure. Issues beyond the admin's aggressiveness setting are skipped. Low-confidence fixes are flagged for human review.

## Phase 4.5: Time to First Fix (doc: 15)

Optimize the path from sign-up to first successful fix.

1. **Demo mode** — sample repo with planted bugs, embedded demo walkthrough
2. **Quick-win scan** — after first Sentry connection, surface 3-5 easy-to-fix issues
3. **Progress UX** — phase-based progress view instead of raw log streaming
4. **Failure communication** (17) — human-readable failure explanations with next steps

**Milestone**: A new user sees a real, validated code fix within 15 minutes of signing up.

## Phase 5: Validation + Regression Guardrails (doc: 07)

Ensure generated code is correct before it becomes a PR.

1. **LLM-based checks** — direction, correctness, quality
2. **CI check with coverage** — run the test suite, collect coverage data, track coverage delta
3. **Validation UI** — check results on the agent run detail page
4. **Manual override** — admins can override failed checks

**Milestone**: Agent-generated code is automatically reviewed before shipping, with regression-test and coverage guardrails enforced.

## Phase 6: PR & Ship (doc: 08)

Open real pull requests on GitHub.

1. **GitHub App setup** — authentication, permissions
2. **Branch + commit creation** — via GitHub API
3. **PR creation** — formatted body, labels, cross-references
4. **PR tracking** — webhook-based status updates
5. **PRs UI** — PR list and detail pages

**Milestone**: Validated fixes automatically become GitHub PRs ready for human review.

## Phase 7: Observability (doc: 09)

Close the loop — measure whether fixes actually helped.

1. **Experiment creation** — triggered after deploy detection
2. **Baseline + observation metric collection** — from Sentry, support tools
3. **Evaluation + classification** — compare before/after
4. **Observability UI** — PR-detail deploy impact section + analytics charts/outcome views
5. **Impact dashboard** — aggregate success rate, total impact
6. **Production outcome feedback loop** (18) — analyze ineffective/regression outcomes, generate learnings, inject into agent context

**Milestone**: After a fix deploys, the system automatically reports whether it reduced customer pain. Ineffective fixes feed back into agent context to prevent repeated mistakes.

## Phase 8: Review Feedback Loop (doc: 11)

Close the learning loop — turn human PR reviews into agent improvements.

1. **Review comment capture + processing pipeline** — extend PR webhook handler, create `review_comments` table, implement structural pre-filter + single LLM pass
2. **Auto-apply feedback** — revision runs, push-to-existing-PR, revision prompt injection for 143-generated PRs
3. **Review patterns KB** — `review_patterns` table, text-match dedup logic, admin UI
4. **Curated context document** — `.143/learned-conventions.md` generation, PR-based updates, manual edit preservation

**Milestone**: Every human review automatically improves all future agent runs for that repo. Learned conventions are version-controlled in the repo and editable by the team. Acceptance rates trend upward over time.

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
- Step-6 experiment evaluator
- Integration logic for:
    - Github: PR creation, status checks, deploy signals
    - Sentry: Issue webhooks as well as retrieval of issues via the API. Also linkage of issues to Github PRs in the PR body.
    - Linear: Webhooks and retrieval of issues via the API. Also linkage of issues to Github PRs in the PR title.

# **Why 143?**

The name comes from the XP-80 Shooting Star project. In 1943, a small team at Lockheed Skunk Works designed and built the first US jet fighter in exactly **143 days**. They did it by killing the bureaucracy and giving a small, autonomous team the freedom to execute.

That's the logic behind **143.dev**.

Fixing production bugs usually sucks because of the overhead like parsing logs, reproducing state, and context switching, not necessarily because the fix itself is hard. We use **autonomous AI agents** to handle that grunt work. The agents act like your Skunk Works team: they isolate the issue, verify the root cause, and tee up the solution so you can just ship it.

# Design: 143.dev

> **Status:** Partially Implemented | **Last reviewed:** 2026-06-16

143.dev is shared coding-agent infrastructure for engineering teams. It turns production errors, issue-tracker work, PR feedback, automations, and human requests into repo-scoped coding sessions that run in isolated sandboxes, produce reviewable diffs, launch previews, and publish branches or pull requests through the team's normal GitHub workflow.

This document is the product and architecture map. It should explain the overall system at a high level and link to detailed design docs for contracts, state machines, UI specifics, rollout plans, and operational procedures.

## Product Model

- **Organization** is the tenant boundary. Repositories, integrations, API clients, credentials, usage, audit logs, sessions, automations, and projects are all scoped to one `org_id`.
- **User membership** controls access within an organization. Roles include admin, engineer/member, builder, and viewer; builder access is intentionally narrower around repository, settings, eval, and shipping surfaces.
- **Repository** is the GitHub-backed work boundary. A repository provides source code, branch/PR targets, preview config, optional runtime settings, and learned conventions.
- **Issue** is an input signal and prioritization record. Issues can come from Sentry, Linear, GitHub-triggered work, PM planning, or future support integrations, but a coding session no longer requires exactly one issue.
- **Session** is the core execution primitive. A session owns conversation history, linked issue context, sandbox state, snapshots, working branch, diff/review state, previews, and branch/PR publishing.
- **Thread/tab** is an agent lane inside a session. Tabs share the session workspace and final branch/PR, but each tab has its own transcript, runtime state, inbox, model/provider choice, cancellation path, and human-input state.
- **Automation** is a team-owned recurring or event-triggered instruction that creates sessions through the same execution pipeline as manual work.
- **Project** is the higher-level planning surface for PM-proposed or human-authored multi-step work. Projects group related tasks and can feed sessions over time.
- **Preview** is a temporary isolated web runtime for a session or branch. It is addressed by a preview origin, controlled by backend state, and backed by a worker-owned sandbox/runtime.
- **Branch or PR** is the publish artifact. 143 creates branches and PRs through GitHub while preserving repository templates, keeping PR descriptions concise and problem-first, and leaving repository-native CI/CD as the validation source of truth.

## Core Flow

1. **Set up the organization.** Users authenticate with GitHub or supported auth flows, join an org, connect repositories, configure integrations, and add personal or team coding-agent credentials.
2. **Capture work.** Work can start from manual sessions, Sentry issues, Linear links or assignments, GitHub-triggered automations, scheduled automations, PM agent proposals, the external API, or the `143-tools` CLI.
3. **Prioritize and plan.** Normalized issues are scored by business impact, severity, recency, direction, and complexity. The PM agent can cluster related work and propose projects when batch planning is a better fit than per-issue execution.
4. **Execute in a sandbox.** The API persists intent in Postgres, workers claim durable jobs, and coding agents run inside gVisor-backed Docker sandboxes with bounded CPU, memory, disk, network, and runtime policy.
5. **Collaborate during execution.** Users can send follow-ups, attach files/images, add structured workspace references, answer agent questions, review diffs, run review loops, and coordinate multiple agent tabs in the same session workspace.
6. **Preview and inspect.** Session and branch previews launch from backend-controlled preview state, expose apps on isolated origins, surface startup logs and freshness state, and can use admin-managed preview secret bundles.
7. **Publish.** Users can publish a branch or open/update a PR. PR creation uses the same durable backend path from the UI, automations, and agent CLI tools; direct sandbox `gh pr create` calls are not the platform contract.
8. **Repair and learn.** PR health syncs from GitHub, repair actions create continuation sessions or turns, review feedback can be turned into learned repo conventions, and usage/eval/audit data feeds operating decisions.

The original demo loop is still the simplest way to understand the product: **Sentry error -> coding session -> pull request**. The broader product generalizes that loop to manual work, issue trackers, automations, projects, previews, and review feedback.

## Architecture

```text
Browser / CLI / external API
        |
        v
Next.js frontend + Go API (/api/v1)
        |
        +--> PostgreSQL: source of truth, tenancy, jobs, sessions, audit, usage
        +--> Redis: optional cache, pub/sub, SSE fan-out, live wakeups
        +--> Object storage: session snapshots, uploaded artifacts, preview caches
        |
        v
Workers and durable session executors
        |
        +--> gVisor/Docker sandboxes with coding-agent CLIs and 143-tools
        +--> preview runtimes and worker-owned preview routing
        +--> GitHub, Sentry, Linear, logs, and other integration APIs

Vector -> VictoriaLogs / Grafana for centralized logs, dashboards, and alerts
```

### Application Layer

- The backend is a Go API server using chi, pgx stores, service interfaces, zerolog, validator, and Postgres-backed jobs. Detailed contracts live in [implemented/02-api-server.md](implemented/02-api-server.md).
- The frontend is a Next.js app using shadcn/ui, Tailwind, TanStack Query, `nuqs`, and shared app components. Detailed frontend architecture lives in [03-frontend.md](03-frontend.md).
- Public browser routes and official bearer-token API calls share the `/api/v1` contract, but API-token requests resolve to org-scoped API-client principals with explicit scopes. See [implemented/94-external-api-sessions-automations.md](implemented/94-external-api-sessions-automations.md).
- The local/team CLI is a first-class entry point for install, auth, local gateway work, and sandbox agent tools. See [implemented/96-cli-local-install-and-team-auth.md](implemented/96-cli-local-install-and-team-auth.md) and [implemented/95-hierarchical-agent-tools-cli.md](implemented/95-hierarchical-agent-tools-cli.md).

### Data And Coordination

- PostgreSQL is the source of truth for org data, repositories, issues, sessions, threads, jobs, credentials, PRs, previews, usage, and audit records. Every tenant-owned table and query is scoped by `org_id`.
- The job queue is Postgres-backed. Workers claim work with `SELECT ... FOR UPDATE SKIP LOCKED`, and durable state transitions are committed before Redis wakeups or SSE notifications.
- Redis is an optional acceleration layer for cache, pub/sub, SSE fan-out, and coordination. Losing Redis should degrade live updates, not lose durable work. See [implemented/52-redis.md](implemented/52-redis.md).
- Session snapshots and multi-node recovery use shared object storage so workers and API nodes do not depend on one machine's local disk. See [implemented/54-s3-session-snapshots.md](implemented/54-s3-session-snapshots.md).

### Runtime Plane

- Workers run coding-agent jobs, continuation turns, preview starts, ingestion syncs, PM planning, automations, and repair work.
- Sandboxes are the permission boundary for agent execution. They run with resource limits, gVisor isolation in production, controlled network policy, and per-session GitHub credential access through a worker-owned auth broker rather than long-lived tokens in the container environment.
- The sandbox image installs the supported coding-agent CLIs, including Codex, Claude Code, OpenCode, Amp, and Pi. Runtime credentials are resolved from ordered personal/team auth stacks with health and rate-limit state tracked separately from credential config.
- Long-running sessions survive routine deploys through durable session executors, leases, checkpointed recovery, snapshots, and worker drain/spin-down controls. See [implemented/82-durable-session-executors.md](implemented/82-durable-session-executors.md).
- Multiple agent tabs can run inside one shared session sandbox when they should share a branch and filesystem. Independent PR streams should remain separate sessions. See [implemented/88-shared-sandbox-thread-runtimes.md](implemented/88-shared-sandbox-thread-runtimes.md).

### Integration Plane

- GitHub is the repository, branch, PR, check, mergeability, review, and deploy-signal integration. Repository-native CI/CD is authoritative after 143 publishes a branch or PR.
- Sentry is the primary production-error ingestion source and still anchors the canonical "error to PR" loop.
- Linear integration supports issue linking, bidirectional session updates, and agent-triggered work from assignments or mentions. See [implemented/62-linear-session-linking.md](implemented/62-linear-session-linking.md) and [implemented/69-linear-agent.md](implemented/69-linear-agent.md).
- Slack is a first-class collaboration surface for mentions, bot DMs, App Home, human-input responses, session notifications, preview controls, PR actions, and notification fanout while 143 remains the canonical transcript and execution system. Slack callbacks use the shared durable webhook-ingress ledger before Slack-specific dispatch. See [future/92-slackbot-product-surface.md](future/92-slackbot-product-surface.md), [future/101-slackbot-implementation-plan.md](future/101-slackbot-implementation-plan.md), and [implemented/100-slack-webhook-ingress-durability.md](implemented/100-slack-webhook-ingress-durability.md).
- External API clients and sandbox-injected `143-tools` commands use platform-scoped credentials and durable backend workflows instead of bypassing product state.
- Centralized logs flow through Vector to VictoriaLogs/Grafana. Operators and agents can query logs through bounded provider-aware tools. See [implemented/47-logging-victorialogs.md](implemented/47-logging-victorialogs.md) and [implemented/90-agent-log-provider-tools.md](implemented/90-agent-log-provider-tools.md).

## Major System Areas

| Area | Current shape | Detailed docs |
| --- | --- | --- |
| Identity, teams, and security | GitHub auth, org memberships, RBAC, CSRF protection, webhook signatures, rate limits, audit logs, verified-domain and GitHub-org auto-join | [implemented/13-repository-onboarding.md](implemented/13-repository-onboarding.md), [implemented/20-security-architecture.md](implemented/20-security-architecture.md), [implemented/26-team-management.md](implemented/26-team-management.md), [implemented/34-audit-logs.md](implemented/34-audit-logs.md), [implemented/97-verified-domains-auto-join.md](implemented/97-verified-domains-auto-join.md), [implemented/98-github-org-auto-join.md](implemented/98-github-org-auto-join.md) |
| Repositories and ingestion | GitHub repositories, Sentry ingestion, Linear linking/agent triggers, normalized issues, dedupe, polling/webhooks | [implemented/04-ingestion.md](implemented/04-ingestion.md), [implemented/13-repository-onboarding.md](implemented/13-repository-onboarding.md), [implemented/62-linear-session-linking.md](implemented/62-linear-session-linking.md), [implemented/69-linear-agent.md](implemented/69-linear-agent.md) |
| Slack collaboration | Signed callbacks, durable webhook ingress, Slack-started sessions, App Home, human-input delivery, notification fanout, preview/PR controls | [future/92-slackbot-product-surface.md](future/92-slackbot-product-surface.md), [future/101-slackbot-implementation-plan.md](future/101-slackbot-implementation-plan.md), [implemented/100-slack-webhook-ingress-durability.md](implemented/100-slack-webhook-ingress-durability.md) |
| Prioritization and planning | Business-impact scoring, complexity estimation, Autopilot queue, PM agent proposals, projects | [implemented/05-prioritization.md](implemented/05-prioritization.md), [implemented/28-agent-ticket-prioritization.md](implemented/28-agent-ticket-prioritization.md), [implemented/29-projects.md](implemented/29-projects.md), [implemented/41-pm-project-proposals.md](implemented/41-pm-project-proposals.md), [implemented/75-autopilot-issue-and-run-queue.md](implemented/75-autopilot-issue-and-run-queue.md) |
| Sessions and agents | Issue-less sessions, multi-issue links, structured references, attachments, slash commands, human input, cancellation, multiple tabs, durable runtimes | [implemented/06-agent-orchestrator.md](implemented/06-agent-orchestrator.md), [implemented/59-session-issue-decoupling-and-multi-issue-linking.md](implemented/59-session-issue-decoupling-and-multi-issue-linking.md), [implemented/68-sandbox-agent-tabs-and-threads.md](implemented/68-sandbox-agent-tabs-and-threads.md), [implemented/78-agent-human-input-requests.md](implemented/78-agent-human-input-requests.md), [implemented/79-session-attachment-delivery.md](implemented/79-session-attachment-delivery.md), [implemented/82-durable-session-executors.md](implemented/82-durable-session-executors.md), [implemented/88-shared-sandbox-thread-runtimes.md](implemented/88-shared-sandbox-thread-runtimes.md) |
| Agent credentials and adapters | Ordered personal/team auth stacks, runtime health, rate-limit fallback, Codex default, Claude Code, OpenCode, Amp, Pi | [implemented/26-codex-default-agent.md](implemented/26-codex-default-agent.md), [implemented/57-coding-agent-settings-rethink.md](implemented/57-coding-agent-settings-rethink.md), [implemented/58-amp-pi-coding-agent-auth-alignment.md](implemented/58-amp-pi-coding-agent-auth-alignment.md), [implemented/78-coding-agent-rate-limit-fallback.md](implemented/78-coding-agent-rate-limit-fallback.md), [implemented/91-versioned-coding-credentials-runtime-state.md](implemented/91-versioned-coding-credentials-runtime-state.md), [implemented/95-opencode-agent-adapter.md](implemented/95-opencode-agent-adapter.md) |
| Review, publish, and PR health | In-app diff review (including desktop full-screen mode persisted per user in `diff_viewer_full_screen`), branch-only publish, PR creation/update, PR health sync, repair actions keyed to the PR head SHA, review-agent loops, repository CI as source of truth | [implemented/08-pr-and-ship.md](implemented/08-pr-and-ship.md), [implemented/36-code-review-display.md](implemented/36-code-review-display.md), [implemented/40-pr-creation-revamp.md](implemented/40-pr-creation-revamp.md), [implemented/61-pr-state-sync-and-repair-actions.md](implemented/61-pr-state-sync-and-repair-actions.md), [implemented/71-remove-validation-stage.md](implemented/71-remove-validation-stage.md), [implemented/74-pr-repair-in-progress-ux.md](implemented/74-pr-repair-in-progress-ux.md), [implemented/78-review-agent-loops.md](implemented/78-review-agent-loops.md), [implemented/80-session-branch-only-publish.md](implemented/80-session-branch-only-publish.md) |
| Previews | Session previews, branch/PR previews, worker-owned routing, durable starts, preview settings/secrets, freshness, dependency caches, health dashboard | [implemented/44-sandbox-preview-server.md](implemented/44-sandbox-preview-server.md), [implemented/48-worker-owned-preview-routing.md](implemented/48-worker-owned-preview-routing.md), [implemented/77-durable-preview-start.md](implemented/77-durable-preview-start.md), [implemented/83-branch-and-pr-previews.md](implemented/83-branch-and-pr-previews.md), [implemented/88-preview-settings-page.md](implemented/88-preview-settings-page.md), [implemented/89-session-preview-freshness.md](implemented/89-session-preview-freshness.md), [implemented/93-session-preview-dependency-cache.md](implemented/93-session-preview-dependency-cache.md), [implemented/99-preview-health-dashboard.md](implemented/99-preview-health-dashboard.md) |
| Automations and external entry points | Team-owned scheduled/event automations, goal-first creation, review loops, Slack actions, external API, CLI-triggered platform workflows | [implemented/31-automations-tab.md](implemented/31-automations-tab.md), [implemented/48-automations-separation.md](implemented/48-automations-separation.md), [implemented/84-automation-goal-first-ux.md](implemented/84-automation-goal-first-ux.md), [implemented/94-external-api-sessions-automations.md](implemented/94-external-api-sessions-automations.md), [implemented/95-hierarchical-agent-tools-cli.md](implemented/95-hierarchical-agent-tools-cli.md), [future/101-slackbot-implementation-plan.md](future/101-slackbot-implementation-plan.md) |
| Operations, usage, and evals | Central logs, platform health dashboards, usage/cost rollups, worker capacity signals, session-backed eval task tooling | [implemented/46-billing-usage-dashboard.md](implemented/46-billing-usage-dashboard.md), [implemented/47-logging-victorialogs.md](implemented/47-logging-victorialogs.md), [implemented/66-usage-breakdown-by-agent-model-reasoning.md](implemented/66-usage-breakdown-by-agent-model-reasoning.md), [implemented/90-worker-spin-down-ops.md](implemented/90-worker-spin-down-ops.md), [implemented/96-session-backed-eval-tools.md](implemented/96-session-backed-eval-tools.md) |

## Design Invariants

- **Tenant isolation is non-negotiable.** All tenant-owned data is scoped to `org_id`, and store methods should take `orgID` unless they are explicitly pre-auth or system-wide.
- **Sessions are the execution source of truth.** Issues, Linear tickets, Sentry errors, projects, and automations provide context or initiation, but execution state belongs to sessions and session threads.
- **Durable state precedes live updates.** UI optimism is allowed, but backend truth must be committed before jobs, SSE events, Redis messages, or preview wakeups are treated as authoritative.
- **Workers own live runtimes.** API nodes coordinate and persist state; workers own sandbox processes, preview runtimes, and runtime-local recovery.
- **Repository-native CI/CD validates published branches.** 143 may review diffs, run agent review loops, and sync PR health, but it no longer owns a separate validation stage.
- **PR repair availability follows the branch head.** For the current PR head SHA, `active_repairs` is the server-owned in-progress state that suppresses duplicate repair and merge actions across health-version churn; `health_version` remains launch provenance.
- **Agent tools must use platform paths.** Sandbox agents, automations, and external clients should call `143-tools` or `/api/v1`, so auth, audit, templates, Linear links, PR state, dedupe, and policy checks stay consistent.
- **Untrusted app previews stay isolated.** Previewed apps run on preview origins, not the main app origin, and preview secrets are delivered through preview-specific backend controls.
- **Credentials are visible, scoped, and revocable.** Coding-agent credentials, API tokens, GitHub tokens, preview secrets, and CLI tokens each have explicit ownership, scope, runtime state, and audit surfaces.

## Known Broad Gaps

- **Post-deploy impact measurement is still partial.** Deploy records exist around PR merge events, but the full experiment model, metric collection, outcome classification, and customer-impact feedback loop are not complete. See [backlog/09-observability.md](backlog/09-observability.md) and [future/18-fix-quality-feedback.md](future/18-fix-quality-feedback.md).
- **Advanced codebase context remains future work.** Sessions use repository conventions, structured references, issue context, diffs, and learned review patterns, but deeper file maps, convention extraction, and context quality scoring are not yet a complete product surface. See [future/14-codebase-context.md](future/14-codebase-context.md).
- **Multi-organization membership is not the default product shape.** The system is already org-scoped, but the long-term "one user, many org memberships" flow is only partially represented in current product surfaces. See [future/50-multi-organization-membership.md](future/50-multi-organization-membership.md).
- **Public documentation is not yet a first-party product surface.** The intended `/docs` site exists as a future design, while internal design docs remain separate from customer-facing documentation. See [future/85-public-docs-fumadocs/README.md](future/85-public-docs-fumadocs/README.md).

## What Belongs Outside This File

- Detailed database schemas and API contracts belong in [implemented/01-database-schema.md](implemented/01-database-schema.md), [implemented/02-api-server.md](implemented/02-api-server.md), and feature-specific design docs.
- UI micro-interactions, mobile layouts, keyboard shortcuts, and component behavior belong in [03-frontend.md](03-frontend.md) or the relevant implemented UX design doc.
- State machines, failure matrices, worker drain mechanics, preview routing details, and rollout plans belong in the specific subsystem docs listed above.
- Historical build-order notes should stay in implementation docs or changelogs, not in this overview.

## Why 143?

The name comes from the XP-80 Shooting Star project. In 1943, a small Lockheed Skunk Works team designed and built the first U.S. jet fighter in 143 days by removing bureaucracy and giving a small autonomous team room to execute.

143.dev applies the same idea to software maintenance: reduce the coordination overhead around bugs, logs, context gathering, branch setup, previews, review loops, and PR prep so engineers can focus on deciding what should ship.

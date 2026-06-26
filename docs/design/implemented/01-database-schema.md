# Design: Database Schema

> **Status:** Implemented | **Last reviewed:** 2026-03-25

This document defines the PostgreSQL schema for 143.dev. All entities flow through the pipeline: ingestion -> prioritization -> agent run -> validation -> PR -> deploy -> observation.

## Migration Strategy

Use [golang-migrate/migrate](https://github.com/golang-migrate/migrate) for schema migrations. Migrations live in `migrations/` as numbered SQL files (e.g. `000001_init.up.sql`, `000001_init.down.sql`).

## Core Tables

### `organizations`

Multi-tenancy root. Each self-hosted instance has at least one org.

| Column | Type | Notes |
|--------|------|-------|
| id | uuid | PK |
| name | text | |
| settings | jsonb | org-wide config (autonomy level, token budget, product direction, execution aggressiveness, issue type overrides, etc.) |
| created_at | timestamptz | |
| updated_at | timestamptz | |

### `users`

| Column | Type | Notes |
|--------|------|-------|
| id | uuid | PK |
| org_id | uuid | FK -> organizations |
| email | text | unique |
| name | text | |
| role | text | `admin`, `member`, `viewer` |
| github_id | bigint | GitHub user ID (from OAuth) |
| github_login | text | GitHub username |
| avatar_url | text | GitHub avatar URL |
| created_at | timestamptz | |

### `integrations`

Stores credentials and config for each third-party connection.

| Column | Type | Notes |
|--------|------|-------|
| id | uuid | PK |
| org_id | uuid | FK -> organizations |
| provider | text | `sentry`, `linear`, `github`, `zendesk`, `intercom`, `salesforce` |
| config | jsonb | encrypted at rest, contains API keys, webhook secrets, org/project IDs |
| status | text | `active`, `disabled`, `error` |
| last_synced_at | timestamptz | |
| created_at | timestamptz | |

## Ingestion Tables

### `webhook_deliveries`

Durable record of inbound webhook attempts for idempotency, replay, and debugging. A webhook can be accepted by HTTP but still fail downstream; this table captures that lifecycle.

| Column | Type | Notes |
|--------|------|-------|
| id | uuid | PK |
| org_id | uuid | FK -> organizations |
| integration_id | uuid | FK -> integrations |
| provider | text | `sentry`, `linear`, `github`, `zendesk`, `intercom` |
| delivery_id | text | provider delivery identifier (when provided) |
| event_type | text | provider event type (`issue`, `pull_request`, etc.) |
| signature_valid | boolean | webhook signature verification result |
| received_at | timestamptz | when HTTP request was received |
| processed_at | timestamptz | when downstream processing finished |
| status | text | `received`, `processed`, `failed`, `ignored` |
| attempts | int | processing attempts count |
| error | text | latest failure reason |
| payload | jsonb | raw webhook body |
| headers | jsonb | selected request headers for diagnostics |
| created_at | timestamptz | |

**Indexes:**
- `(org_id, received_at DESC)` — recent webhook activity
- `(integration_id, received_at DESC)` — per-integration troubleshooting
- `(provider, delivery_id)` unique where `delivery_id IS NOT NULL` — idempotency
- `(status, received_at)` — retry/replay workers

### `integration_sync_runs`

Execution history for polling-based ingestion. Tracks what was attempted, what succeeded, and why a sync failed.

| Column | Type | Notes |
|--------|------|-------|
| id | uuid | PK |
| org_id | uuid | FK -> organizations |
| integration_id | uuid | FK -> integrations |
| started_at | timestamptz | |
| completed_at | timestamptz | |
| status | text | `running`, `success`, `partial`, `failed` |
| issues_fetched | int | raw items fetched from provider API |
| issues_upserted | int | issues inserted/updated after dedupe |
| events_inserted | int | issue event rows written |
| api_calls | int | total outbound API calls |
| rate_limited_count | int | number of 429/rate-limit responses |
| error | text | failure reason when status is `failed`/`partial` |
| metadata | jsonb | provider-specific diagnostics and cursors |
| created_at | timestamptz | |

**Indexes:**
- `(integration_id, started_at DESC)` — sync run history
- `(org_id, started_at DESC)` — org-level ingestion health
- `(status, started_at DESC)` — identify failing syncs quickly

### `issues`

The unified, normalized issue table. Every ingested item (support ticket, Sentry error, Linear issue) gets normalized into this table. This is the central entity that flows through the pipeline.

| Column | Type | Notes |
|--------|------|-------|
| id | uuid | PK |
| org_id | uuid | FK -> organizations |
| external_id | text | ID from the source system |
| source | text | `sentry`, `linear`, `support` |
| source_integration_id | uuid | FK -> integrations |
| repository_id | uuid | FK -> repositories, nullable. Linked repo for this issue. |
| title | text | normalized title |
| description | text | normalized description/body |
| raw_data | jsonb | original payload from the source |
| status | text | `open`, `triaged`, `in_progress`, `fixed`, `wont_fix`, `duplicate` |
| first_seen_at | timestamptz | when the issue first appeared |
| last_seen_at | timestamptz | most recent occurrence |
| occurrence_count | int | total occurrences (e.g. Sentry event count) |
| affected_customer_count | int | distinct customers affected |
| severity | text | `critical`, `high`, `medium`, `low` — from source or inferred |
| tags | text[] | labels, tags from source |
| fingerprint | text | deduplication fingerprint |
| created_at | timestamptz | |
| updated_at | timestamptz | |

**Indexes:**
- `(org_id, fingerprint)` unique — deduplication
- `(org_id, source, external_id)` unique — prevent re-ingestion
- `(org_id, status)` — filtering
- `(org_id, last_seen_at DESC)` — recency sorting
- `(repository_id)` — issues per repo

### `issue_events`

Individual occurrences or updates to an issue. For Sentry this is individual error events; for support this is individual tickets linked to the same root cause.

| Column | Type | Notes |
|--------|------|-------|
| id | uuid | PK |
| issue_id | uuid | FK -> issues |
| external_id | text | event ID from source |
| event_type | text | `occurrence`, `comment`, `status_change`, `assignment` |
| data | jsonb | event-specific payload (stack trace, customer info, etc.) |
| customer_id | text | optional, external customer identifier |
| occurred_at | timestamptz | |
| created_at | timestamptz | |

## Prioritization Tables

### `priority_scores`

Computed priority for each issue. Recalculated periodically or on new events.

| Column | Type | Notes |
|--------|------|-------|
| id | uuid | PK |
| issue_id | uuid | FK -> issues, unique |
| org_id | uuid | FK -> organizations |
| score | float | composite priority score (0-100) |
| customer_impact_score | float | based on affected_customer_count, recurrence |
| severity_score | float | based on severity level |
| recency_score | float | based on last_seen_at |
| revenue_risk_score | float | optional, from CRM integration |
| direction_alignment | float | how well this aligns with product direction (-1 to 1) |
| factors | jsonb | breakdown of scoring factors for explainability |
| eligible_for_agent | boolean | passes product direction filter |
| computed_at | timestamptz | |

**Indexes:**
- `(org_id, score DESC)` — top issues query
- `(org_id, eligible_for_agent, score DESC)` — agent-eligible issues

### `priority_overrides`

Manual admin adjustments to prioritization and eligibility. This table captures intentional human decisions separately from computed scores.

| Column | Type | Notes |
|--------|------|-------|
| id | uuid | PK |
| org_id | uuid | FK -> organizations |
| issue_id | uuid | FK -> issues |
| created_by_user_id | uuid | FK -> users |
| override_type | text | `boost`, `suppress`, `force_eligible`, `force_ineligible`, `set_score` |
| score_delta | float | optional adjustment (for `boost`/`suppress`) |
| manual_score | float | optional absolute score (for `set_score`) |
| reason | text | required operator note |
| expires_at | timestamptz | optional; null means persistent until cleared |
| cleared_at | timestamptz | when override was removed |
| cleared_by_user_id | uuid | FK -> users, nullable |
| created_at | timestamptz | |

**Indexes:**
- `(org_id, issue_id, created_at DESC)` — override history per issue
- `(org_id, expires_at)` where `expires_at IS NOT NULL` — expiry sweeps
- `(org_id, issue_id)` where `cleared_at IS NULL` — active override lookup

## Complexity Estimation Tables

### `complexity_estimates`

Pre-run complexity estimation for each issue. Computed after prioritization, before agent execution. See [12-smart-routing.md](../backlog/12-smart-routing.md).

| Column | Type | Notes |
|--------|------|-------|
| id | uuid | PK |
| issue_id | uuid | FK -> issues, unique |
| org_id | uuid | FK -> organizations |
| tier | int | 1-5 (trivial, simple, moderate, complex, very_complex) |
| label | text | `trivial`, `simple`, `moderate`, `complex`, `very_complex` |
| confidence | float | estimator's confidence in the tier (0-1) |
| issue_type | text | `bug_fix`, `error_handling`, `performance`, `refactor`, `feature_gap`, `security` |
| reasoning | text | LLM reasoning for the classification |
| estimated_files | text[] | files likely involved |
| estimated_tokens | int | predicted token usage |
| model_used | text | which model performed the estimation |
| computed_at | timestamptz | |
| created_at | timestamptz | |

**Indexes:**
- `(org_id, tier)` — filter by complexity
- `(issue_id)` unique — one estimate per issue

## Agent Run Tables

### `agent_runs`

Each attempt to fix an issue via a coding agent.

| Column | Type | Notes |
|--------|------|-------|
| id | uuid | PK |
| issue_id | uuid | FK -> issues |
| org_id | uuid | FK -> organizations |
| agent_type | text | `claude_code`, `codex`, `cursor`, etc. |
| status | text | `pending`, `running`, `awaiting_input`, `needs_human_guidance`, `resumed_locally`, `completed`, `failed`, `cancelled`, `skipped` |
| autonomy_level | text | `manual`, `auto_simple`, `auto_all` |
| token_mode | text | `low`, `high` |
| complexity_tier | int | snapshot of the complexity tier at run time |
| container_id | text | sandbox container identifier |
| started_at | timestamptz | |
| completed_at | timestamptz | |
| token_usage | jsonb | `{input_tokens, output_tokens, total_cost}` |
| failure_explanation | text | Human-readable 1-3 sentence explanation. See [17-failure-communication.md](17-failure-communication.md) |
| failure_category | text | `context`, `complexity`, `tooling`, `validation` (null if not failed). See [17-failure-communication.md](17-failure-communication.md) |
| failure_next_steps | text[] | Actionable suggestions for the user |
| failure_retry_advised | boolean | Whether retrying is likely to help |
| parent_run_id | uuid | FK -> agent_runs, nullable. Set for revision runs triggered by review feedback |
| revision_context | jsonb | review feedback that triggered this revision run (null for initial runs) |
| error | text | failure reason if applicable |
| result_summary | text | agent-generated summary of what it did |
| diff | text | the generated code diff |
| diff_stats | jsonb | pre-computed `{ added, removed, files_changed }` |
| diff_history | jsonb | array of `{ pass, diff, diff_stats, created_at }` for multi-pass review |
| created_at | timestamptz | |

**Indexes:**
- `(org_id, status, created_at DESC)` — Fix Queue queries (active, failed, shipped runs)
- `(org_id, issue_id)` — all runs for an issue
- `(org_id, created_at DESC)` — recent run history
- `(parent_run_id)` where `parent_run_id IS NOT NULL` — revision run lookups

**Constraints:**
- `parent_run_id` is a self-referential FK: `FOREIGN KEY (parent_run_id) REFERENCES agent_runs(id)`. This prevents orphaned revision runs and enforces referential integrity for the revision chain.

### `session_pm_context`

PM and project execution context for a coding session. These fields are hydrated into session API responses, but they are stored outside the core `sessions` row so PM/project metadata does not widen the execution-state table.

| Column | Type | Notes |
|--------|------|-------|
| session_id | uuid | PK, FK -> sessions ON DELETE CASCADE |
| org_id | uuid | FK -> organizations |
| pm_plan_id | uuid | FK -> pm_plans, nullable |
| pm_approach | text | PM or automation prompt seed/guidance |
| pm_reasoning | text | PM reasoning shown in context UI |
| project_task_id | uuid | FK -> project_tasks, nullable |
| created_at | timestamptz | |
| updated_at | timestamptz | |

**Indexes:**
- `(org_id, session_id)` — tenant-scoped point lookup
- `(org_id, pm_plan_id)` where `pm_plan_id IS NOT NULL` — PM plan sessions
- `(org_id, project_task_id)` where `project_task_id IS NOT NULL` — project task sessions

### `session_automation_links`

Links sessions to automation runs without storing automation ownership on the core `sessions` table. One automation run may link to multiple sessions.

| Column | Type | Notes |
|--------|------|-------|
| session_id | uuid | PK, FK -> sessions ON DELETE CASCADE |
| org_id | uuid | FK -> organizations |
| automation_run_id | uuid | FK -> automation_runs |
| created_at | timestamptz | |

**Indexes:**
- `(org_id, session_id)` — tenant-scoped point lookup
- `(org_id, automation_run_id, created_at DESC)` — sessions per automation run

### `agent_run_logs`

Streaming logs from an agent run for real-time UI display.

| Column | Type | Notes |
|--------|------|-------|
| id | bigserial | PK |
| agent_run_id | uuid | FK -> agent_runs |
| timestamp | timestamptz | |
| level | text | `info`, `debug`, `error`, `tool_use`, `output`, `question` |
| message | text | |
| metadata | jsonb | tool calls, file paths, etc. |

**Indexes:**
- `(agent_run_id, timestamp)` — log streaming

### `session_threads`

Per-thread state for multi-agent sessions. Each thread runs a separate agent process in the same shared container/filesystem.

| Column | Type | Notes |
|--------|------|-------|
| id | uuid | PK |
| session_id | uuid | FK -> sessions (agent_runs) ON DELETE CASCADE |
| org_id | uuid | FK -> organizations |
| agent_type | text | `claude_code`, `codex`, etc. |
| model_override | text | optional per-thread model |
| label | text | human-readable thread name (e.g. "Backend API") |
| instructions | text | thread-specific instructions |
| file_scope | text[] | optional file/directory focus |
| status | text | `pending`, `running`, `idle`, `awaiting_input`, `completed`, `failed`, `cancelled` |
| agent_session_id | text | provider session identifier |
| current_turn | int | incremented each turn |
| last_activity_at | timestamptz | |
| result_summary | text | what this thread accomplished |
| diff | text | thread's generated diff |
| failure_explanation | text | |
| failure_category | text | |
| started_at | timestamptz | |
| completed_at | timestamptz | |
| created_at | timestamptz | |

**Indexes:**
- `(session_id)` — list threads for a session
- `(org_id, status)` — operational queries

**Notes:**
- `agent_run_logs` and `session_messages` both have a nullable `thread_id` (FK -> session_threads) column added to scope logs/messages to a specific thread. NULL means session-level.
- Maximum 4 threads per session, enforced in application layer.

### `agent_run_questions`

Questions asked by the agent during execution. When the agent encounters ambiguity, it emits a question; the run pauses until answered.

| Column | Type | Notes |
|--------|------|-------|
| id | uuid | PK |
| agent_run_id | uuid | FK -> agent_runs |
| org_id | uuid | FK -> organizations |
| question_text | text | The question in plain text |
| options | text[] | Optional multiple-choice answers (null = free text) |
| context | text | What the agent was doing when it asked |
| blocks_phase | text | Which phase is blocked (`analysis`, `implementation`, `testing`) |
| answer_text | text | The human's answer (null until answered) |
| answered_by | uuid | FK -> users (null until answered) |
| answered_at | timestamptz | Null until answered |
| status | text | `pending`, `answered`, `timed_out`, `skipped` |
| created_at | timestamptz | |

**Indexes:**
- `(agent_run_id, created_at)` — list questions for a run in order
- `(org_id, status)` — find unanswered questions across all runs (for Fix Queue)

## Validation Tables

### `validations`

Results of the validation step for an agent run.

| Column | Type | Notes |
|--------|------|-------|
| id | uuid | PK |
| agent_run_id | uuid | FK -> agent_runs |
| org_id | uuid | FK -> organizations |
| status | text | `pending`, `running`, `passed`, `failed` |
| direction_check | text | `pass`, `fail`, `skip` |
| correctness_check | text | `pass`, `fail`, `skip` |
| quality_check | text | `pass`, `fail`, `skip` |
| security_scan | text | `pass`, `fail`, `skip` |
| regression_test_check | text | `pass`, `fail`, `skip` |
| coverage_delta | jsonb | `{before: {line_pct, branch_pct}, after: {line_pct, branch_pct}, delta: {line_pct, branch_pct}}` |
| ci_check | text | `pass`, `fail`, `skip`, `pending` |
| details | jsonb | per-check details, LLM reasoning, CI output |
| started_at | timestamptz | |
| completed_at | timestamptz | |
| created_at | timestamptz | |

## PR & Deploy Tables

### `pull_requests`

| Column | Type | Notes |
|--------|------|-------|
| id | uuid | PK |
| agent_run_id | uuid | FK -> agent_runs |
| org_id | uuid | FK -> organizations |
| github_pr_number | int | |
| github_pr_url | text | |
| github_repo | text | `owner/repo` |
| title | text | |
| body | text | |
| status | text | `open`, `merged`, `closed` |
| review_status | text | `pending`, `approved`, `changes_requested` |
| merged_at | timestamptz | |
| created_at | timestamptz | |
| updated_at | timestamptz | |

### `deploys`

Tracks deploy events detected after PR merge.

| Column | Type | Notes |
|--------|------|-------|
| id | uuid | PK |
| pull_request_id | uuid | FK -> pull_requests |
| org_id | uuid | FK -> organizations |
| environment | text | `production`, `staging` |
| deployed_at | timestamptz | |
| commit_sha | text | |
| created_at | timestamptz | |

## Review Feedback Tables

### `review_comments`

Individual review comments captured from 143-generated GitHub PRs, processed through a structural pre-filter and single LLM classification pass. See [11-review-feedback-loop.md](../backlog/11-review-feedback-loop.md).

| Column | Type | Notes |
|--------|------|-------|
| id | uuid | PK |
| pull_request_id | uuid | FK -> pull_requests |
| org_id | uuid | FK -> organizations |
| github_comment_id | bigint | GitHub comment identifier |
| reviewer | text | GitHub username |
| body | text | raw comment text |
| diff_path | text | file path the comment is on |
| diff_position | int | line position in the diff |
| filter_status | text | `pending`, `filtered_structural`, `filtered_not_actionable`, `accepted` |
| category | text | `style`, `logic_bug`, `edge_case`, `wrong_approach`, `missing_test`, `security`, `performance`, `nit` (null until classified) |
| actionable | boolean | default true |
| generalizable | boolean | default false |
| generalized_rule | text | repo-level rule extracted from this comment |
| summary | text | LLM-generated one-liner |
| applied | boolean | was this feedback applied via revision run? |
| created_at | timestamptz | |

**Indexes:**
- `(pull_request_id)` — comments per PR
- `(org_id, category)` — category analytics
- `(org_id, filter_status)` — pipeline monitoring

### `session_review_comments`

Inline review comments left by users on session diffs, feeding directives back into the agent's next pass. See [36-code-review-display.md](36-code-review-display.md).

| Column | Type | Notes |
|--------|------|-------|
| id | uuid | PK |
| session_id | uuid | FK -> sessions (CASCADE) |
| org_id | uuid | FK -> organizations |
| user_id | uuid | FK -> users |
| file_path | text | file the comment targets |
| line_number | int | line number in that file |
| diff_side | text | `old` or `new` |
| body | text | comment markdown |
| resolved | boolean | default false |
| resolved_at | timestamptz | |
| resolved_by_pass | int | agent pass that addressed this comment |
| pass_number | int | which agent pass this comment targets |
| created_at | timestamptz | |
| updated_at | timestamptz | |

**Indexes:**
- `(org_id, session_id)` — comments per session (org-scoped)
- `(session_id, file_path)` — comments per file in a session

### `review_patterns`

Per-repo knowledge base of recurring reviewer preferences, extracted from review comments. Active patterns are materialized into a `.143/learned-conventions.md` file in the repo. Promotion is simple: 2+ occurrences promotes a candidate to active.

| Column | Type | Notes |
|--------|------|-------|
| id | uuid | PK |
| org_id | uuid | FK -> organizations |
| repo | text | `owner/repo` |
| rule | text | the generalized instruction |
| category | text | same categories as review_comments |
| source_comment_ids | uuid[] | review_comments that produced this rule |
| occurrence_count | int | default 1 |
| status | text | `candidate`, `active`, `dismissed` |
| manually_curated | boolean | default false. True if admin edited the rule or it came from a manual file edit |
| active | boolean | NOT NULL DEFAULT true. Insert-only versioning flag |
| created_at | timestamptz | |

**Indexes:**
- `(org_id, repo, status)` where `active = true` — active patterns per repo
- `(org_id, repo, rule)` unique where `active = true` — deduplication

## Prompt and Eval Configuration Tables

### `prompt_templates`

Global default prompt definitions shipped by 143.dev. These are not tenant-specific.

| Column | Type | Notes |
|--------|------|-------|
| id | uuid | PK |
| key | text | stable identifier, e.g. `agent.default.implementation` |
| description | text | human-readable purpose |
| default_content | text | upstream default prompt content |
| created_at | timestamptz | |
| updated_at | timestamptz | |

**Indexes:**
- `(key)` unique — lookup by stable prompt key

### `prompt_versions`

Versioned prompt content. Can be global defaults (`org_id` null) or org-specific overrides (`org_id` set).

| Column | Type | Notes |
|--------|------|-------|
| id | uuid | PK |
| org_id | uuid | FK -> organizations, nullable for global defaults |
| template_id | uuid | FK -> prompt_templates |
| scope_type | text | `global`, `repository`, `issue_type`, `phase`, `repository_issue_type`, `repository_phase`, `issue_type_phase`, `repository_issue_type_phase` |
| repository_id | uuid | FK -> repositories, nullable |
| issue_type | text | nullable |
| phase | text | nullable (`planning`, `implementation`, `validation`, `review`) |
| state | text | `draft`, `candidate`, `active`, `archived` |
| content | text | prompt text |
| content_hash | text | sha256 hash for reproducibility |
| based_on_version_id | uuid | FK -> prompt_versions, nullable |
| created_by_user_id | uuid | FK -> users, nullable for system-authored defaults |
| created_at | timestamptz | |
| updated_at | timestamptz | |

**Indexes:**
- `(template_id, org_id, created_at DESC)` — version history
- `(org_id, scope_type, repository_id, issue_type, phase, state)` — active resolver path
- unique partial index on active org override tuple (`org_id`, `template_id`, `scope_type`, `repository_id`, `issue_type`, `phase`) where `state = 'active'`

### `prompt_overrides`

Tracks effective override pointers by org and scope for fast runtime resolution.

| Column | Type | Notes |
|--------|------|-------|
| id | uuid | PK |
| org_id | uuid | FK -> organizations |
| template_id | uuid | FK -> prompt_templates |
| scope_type | text | same scope enum as `prompt_versions` |
| repository_id | uuid | FK -> repositories, nullable |
| issue_type | text | nullable |
| phase | text | nullable |
| active_version_id | uuid | FK -> prompt_versions |
| rollout_percentage | int | 0-100 for canary traffic |
| active | boolean | NOT NULL DEFAULT true. Insert-only versioning flag |
| created_at | timestamptz | |

**Indexes:**
- unique (`org_id`, `template_id`, `scope_type`, `repository_id`, `issue_type`, `phase`) where `active = true` — one active pointer per scope tuple
- `(org_id, created_at DESC)` — recent config changes

### `eval_datasets`

Per-org eval dataset metadata. Raw private payloads are stored in `eval_examples`.

| Column | Type | Notes |
|--------|------|-------|
| id | uuid | PK |
| org_id | uuid | FK -> organizations |
| repository_id | uuid | FK -> repositories, nullable for org-wide datasets |
| name | text | dataset name |
| dataset_type | text | `golden`, `shadow`, `adversarial` |
| visibility | text | `private`, `public_fixture` |
| status | text | `active`, `archived` |
| description | text | |
| source_summary | text | provenance summary |
| created_by_user_id | uuid | FK -> users |
| created_at | timestamptz | |
| updated_at | timestamptz | |

**Indexes:**
- `(org_id, dataset_type, status)` — gate selection
- `(org_id, repository_id, created_at DESC)` — repo-scoped datasets

### `eval_examples`

Individual eval examples. Sensitive payload fields are encrypted by the app before insert.

| Column | Type | Notes |
|--------|------|-------|
| id | uuid | PK |
| dataset_id | uuid | FK -> eval_datasets |
| org_id | uuid | FK -> organizations |
| external_ref | text | source identifier (ticket/event ID), nullable |
| tags | text[] | slice tags (`issue_type:bug_fix`, `risk:medium`) |
| input_encrypted | bytea | encrypted eval input |
| expected_output_encrypted | bytea | encrypted expected output/rubric hints |
| ground_truth_encrypted | bytea | encrypted canonical answer, nullable |
| payload_hash | text | content hash for dedupe/integrity |
| created_at | timestamptz | |

**Indexes:**
- `(dataset_id)` — dataset membership
- `(org_id, created_at DESC)` — ingestion history
- `(org_id, payload_hash)` unique — dedupe

### `eval_runs`

Execution records for eval jobs tied to exact prompt/model/config versions.

| Column | Type | Notes |
|--------|------|-------|
| id | uuid | PK |
| org_id | uuid | FK -> organizations |
| trigger_type | text | `manual`, `pre_promotion`, `scheduled`, `rollback_check` |
| status | text | `queued`, `running`, `succeeded`, `failed`, `cancelled` |
| prompt_version_id | uuid | FK -> prompt_versions |
| model | text | model used |
| adapter | text | `claude_code`, `codex`, `opencode`, etc. |
| config_snapshot | jsonb | routing/validation settings used |
| summary_metrics | jsonb | pass@1, pass@k, per-slice metrics |
| started_at | timestamptz | |
| completed_at | timestamptz | |
| created_at | timestamptz | |

**Indexes:**
- `(org_id, created_at DESC)` — eval history
- `(org_id, prompt_version_id, created_at DESC)` — promotion checks
- `(status, created_at DESC)` — runner dashboards

### `eval_run_results`

Per-example results for each eval run.

| Column | Type | Notes |
|--------|------|-------|
| id | uuid | PK |
| eval_run_id | uuid | FK -> eval_runs |
| eval_example_id | uuid | FK -> eval_examples |
| org_id | uuid | FK -> organizations |
| outcome | text | `pass`, `fail`, `error`, `unknown` |
| failure_code | text | structured code (`context_missing`, `policy_violation`, etc.), nullable |
| deterministic_scores | jsonb | deterministic grader outputs |
| llm_judge_scores | jsonb | LLM judge outputs |
| trace_ref | text | reference to run trace record/artifact |
| created_at | timestamptz | |

**Indexes:**
- `(eval_run_id)` — results for a run
- `(org_id, failure_code, created_at DESC)` — failure trending
- `(org_id, outcome, created_at DESC)` — pass/fail tracking

### `eval_release_gates`

Threshold policies that must pass before promotion to active prompt versions.

| Column | Type | Notes |
|--------|------|-------|
| id | uuid | PK |
| org_id | uuid | FK -> organizations |
| gate_name | text | e.g. `prompt_promotion_default` |
| enabled | boolean | |
| min_pass_at_1 | float | threshold for pass@1 |
| min_pass_at_k | float | threshold for pass@k |
| max_policy_violations | int | hard ceiling for high-severity policy failures |
| max_regression_delta | float | allowed drop vs current active baseline |
| canary_stages | jsonb | staged rollout percentages, default `[10,30,100]` |
| rollback_rules | jsonb | online rollback threshold config |
| updated_by_user_id | uuid | FK -> users |
| active | boolean | NOT NULL DEFAULT true. Insert-only versioning flag |
| created_at | timestamptz | |

**Indexes:**
- `(org_id, gate_name)` unique where `active = true` — one config per named gate

## Observability Tables

### `experiments`

Each deployed fix is an experiment that measures impact.

| Column | Type | Notes |
|--------|------|-------|
| id | uuid | PK |
| deploy_id | uuid | FK -> deploys |
| issue_id | uuid | FK -> issues |
| org_id | uuid | FK -> organizations |
| status | text | `baseline`, `observing`, `completed` |
| baseline_start | timestamptz | start of baseline measurement window |
| baseline_end | timestamptz | end of baseline (= deploy time) |
| observation_start | timestamptz | deploy time |
| observation_end | timestamptz | end of observation window |
| baseline_metrics | jsonb | `{error_rate, ticket_volume, latency_p50, latency_p99}` |
| observation_metrics | jsonb | same shape as baseline |
| outcome | text | `success`, `no_change`, `regression`, `inconclusive` |
| outcome_details | jsonb | statistical details, confidence intervals |
| created_at | timestamptz | |
| updated_at | timestamptz | |

## Production Learning Tables

### `production_learnings`

Learnings from production outcomes (post-deploy impact measurement). When a fix is deployed and the outcome is classified, the system generates learnings that are injected into future agent runs. See [18-fix-quality-feedback.md](18-fix-quality-feedback.md).

| Column | Type | Notes |
|--------|------|-------|
| id | uuid | PK |
| org_id | uuid | FK -> organizations |
| repo | text | `owner/repo` |
| experiment_id | uuid | FK -> experiments |
| agent_run_id | uuid | FK -> agent_runs |
| issue_id | uuid | FK -> issues |
| issue_type | text | |
| outcome_type | text | `success`, `ineffective`, `regression` |
| error_pattern | text | issue fingerprint for matching similar issues |
| approach_summary | text | what the agent did |
| learning | text | generalized learning (1 sentence directive) |
| analysis_detail | text | full LLM analysis |
| impact_metrics | jsonb | before/after metrics snapshot |
| severity | text | `low`, `medium`, `high` (default `medium`) |
| status | text | `active`, `superseded`, `dismissed` (default `active`) |
| created_at | timestamptz | |

**Indexes:**
- `(org_id, repo, status)` — active learnings per repo
- `(org_id, error_pattern)` where `status = 'active'` — pattern matching
- `(org_id, issue_type, outcome_type)` — analytics

## Notification Tables

### `notification_preferences`

Per-user notification delivery settings. Controls which events a user receives and through which channels.

| Column | Type | Notes |
|--------|------|-------|
| id | uuid | PK |
| user_id | uuid | FK -> users |
| org_id | uuid | FK -> organizations |
| channel | text | `email`, `slack`, `in_app` |
| event_category | text | `run_completed`, `run_failed`, `review_requested`, `deploy_detected`, `regression_detected`, `needs_guidance` |
| enabled | boolean | default true |
| quiet_hours_start | time | nullable, user's local time |
| quiet_hours_end | time | nullable |
| created_at | timestamptz | |
| updated_at | timestamptz | |

**Indexes:**
- `(user_id, channel, event_category)` unique — one preference per user/channel/event
- `(org_id, event_category)` — org-level preference lookups

### `notifications`

Individual notification delivery records. One row per notification per channel.

| Column | Type | Notes |
|--------|------|-------|
| id | uuid | PK |
| org_id | uuid | FK -> organizations |
| user_id | uuid | FK -> users |
| channel | text | `email`, `slack`, `in_app` |
| event_type | text | specific event (e.g., `agent_run.failed`, `pr.merged`, `experiment.regression`) |
| event_category | text | category for preference matching |
| title | text | notification title |
| body | text | notification body (markdown) |
| resource_type | text | e.g., `agent_run`, `pull_request`, `experiment` |
| resource_id | uuid | FK to the relevant resource |
| status | text | `pending`, `sent`, `delivered`, `failed`, `read` |
| sent_at | timestamptz | |
| read_at | timestamptz | |
| error | text | delivery failure reason |
| metadata | jsonb | channel-specific data (Slack ts, email message-id, etc.) |
| created_at | timestamptz | |

**Indexes:**
- `(user_id, status, created_at DESC)` — user's notification inbox
- `(org_id, event_type, created_at DESC)` — event-type analytics
- `(status, created_at)` where `status = 'pending'` — delivery queue

## Cost Tracking Tables

### `org_token_usage`

Rolling token and cost tracking per org. Updated after each agent run completes. Used for budget enforcement and cost visibility.

| Column | Type | Notes |
|--------|------|-------|
| id | uuid | PK |
| org_id | uuid | FK -> organizations |
| period | text | `daily`, `monthly` |
| period_start | date | start of the tracking period |
| input_tokens | bigint | total input tokens consumed |
| output_tokens | bigint | total output tokens consumed |
| estimated_cost_usd | numeric(10,4) | estimated cost based on model pricing |
| run_count | int | number of agent runs in this period |
| model_breakdown | jsonb | `{"claude-opus": {input: N, output: N, cost: X}, ...}` |
| created_at | timestamptz | |
| updated_at | timestamptz | |

**Indexes:**
- `(org_id, period, period_start)` unique — one row per org per period
- `(org_id, period_start DESC)` — recent usage lookup

## Audit Trail

### `audit_log`

Immutable, append-only log of all significant actions for compliance and debugging. A database trigger prevents UPDATE and DELETE operations — see [20-security-architecture.md](20-security-architecture.md).

| Column | Type | Notes |
|--------|------|-------|
| id | bigserial | PK |
| org_id | uuid | FK -> organizations |
| actor_type | text | `user`, `system`, `agent` |
| actor_id | text | user ID or `system` |
| action | text | e.g. `issue.created`, `agent_run.started`, `pr.opened`, `validation.overridden` |
| resource_type | text | e.g. `issue`, `agent_run`, `pull_request` |
| resource_id | uuid | |
| details | jsonb | action-specific data |
| created_at | timestamptz | |

**Immutability trigger:**
```sql
CREATE OR REPLACE FUNCTION prevent_audit_log_modification()
RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'audit_log is append-only: % operations are not allowed', TG_OP;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER audit_log_immutable
    BEFORE UPDATE OR DELETE ON audit_log
    FOR EACH ROW
    EXECUTE FUNCTION prevent_audit_log_modification();
```

**Indexes:**
- `(org_id, created_at DESC)` — recent activity
- `(org_id, resource_type, resource_id)` — resource history

## Cluster Tables

### `jobs`

Durable, database-backed async work queue for the full pipeline (`ingest_webhook`, `prioritize`, `run_agent`, `validate`, `open_pr`, `evaluate_experiment`). Workers claim jobs using `FOR UPDATE SKIP LOCKED`.

| Column | Type | Notes |
|--------|------|-------|
| id | uuid | PK |
| org_id | uuid | FK -> organizations |
| queue | text | queue name (`ingestion`, `prioritization`, `agent`, `validation`, `pr`, `observability`) |
| job_type | text | e.g. `ingest_webhook`, `run_agent`, `open_pr` |
| payload | jsonb | typed job input payload |
| priority | int | higher number = higher priority (default 0) |
| status | text | `pending`, `running`, `succeeded`, `failed`, `cancelled`, `dead_letter` |
| attempts | int | attempts made so far |
| max_attempts | int | retry ceiling |
| run_at | timestamptz | scheduled run time (for delayed jobs/retries) |
| locked_by_node_id | text | FK -> nodes.id, nullable |
| locked_at | timestamptz | when worker claimed job |
| last_error | text | latest error message |
| dedupe_key | text | optional idempotency key for coalescing duplicates |
| created_at | timestamptz | |
| updated_at | timestamptz | |
| completed_at | timestamptz | nullable |

**Indexes:**
- `(status, run_at, priority DESC)` — dequeue path
- `(queue, status, run_at, priority DESC)` — queue-specific workers
- `(org_id, created_at DESC)` — org job history
- `(locked_by_node_id, locked_at)` — dead-worker recovery
- `(queue, dedupe_key)` unique where `dedupe_key IS NOT NULL AND status IN ('pending', 'running')` — in-flight dedupe

### `nodes`

Tracks active nodes in the cluster for health monitoring and dead node cleanup. Not used for coordination — that's handled by Postgres advisory locks and `FOR UPDATE SKIP LOCKED`.

| Column | Type | Notes |
|--------|------|-------|
| id | text | PK — hostname or UUID |
| mode | text | `all`, `api`, `worker` |
| host | text | reachable address |
| started_at | timestamptz | |
| last_heartbeat_at | timestamptz | |
| status | text | `active`, `draining`, `dead` |
| metadata | jsonb | version, CPU count, memory, active sandbox count |

Heartbeat every 30s. Node is considered `dead` after 2 min with no heartbeat. Any worker-capable node scans for jobs locked by dead nodes and re-queues them.

## Repository & Codebase Context Tables

### `repositories`

Tracks connected GitHub repositories. See [13-repository-onboarding.md](13-repository-onboarding.md).

| Column | Type | Notes |
|--------|------|-------|
| id | uuid | PK |
| org_id | uuid | FK -> organizations |
| integration_id | uuid | FK -> integrations |
| github_id | bigint | GitHub's numeric repo ID |
| full_name | text | `owner/repo` |
| default_branch | text | default `main` |
| private | boolean | |
| language | text | primary language |
| description | text | |
| clone_url | text | HTTPS clone URL |
| installation_id | bigint | GitHub App installation ID |
| status | text | `active`, `paused`, `disconnected` |
| last_synced_at | timestamptz | last full context sync |
| context_quality | float | 0-100 quality score |
| settings | jsonb | per-repo overrides |
| created_at | timestamptz | |
| updated_at | timestamptz | |

**Indexes:**
- `(org_id, github_id)` unique — one record per repo
- `(org_id, status)` — active repo listing
- `(org_id, full_name)` — lookup by name

### `repo_context_packages`

One record per repository. Stores context package metadata and quality score. See [14-codebase-context.md](14-codebase-context.md).

| Column | Type | Notes |
|--------|------|-------|
| id | uuid | PK |
| repository_id | uuid | FK -> repositories, unique |
| org_id | uuid | FK -> organizations |
| version | int | incremented on each rebuild |
| status | text | `building`, `ready`, `stale`, `error` |
| quality_score | float | 0-100, composite quality metric |
| quality_details | jsonb | breakdown per dimension |
| file_coverage | float | % of files covered by context |
| last_built_at | timestamptz | |
| build_duration | interval | |
| commit_sha | text | repo HEAD when context was built |
| error | text | |
| created_at | timestamptz | |
| updated_at | timestamptz | |

**Indexes:**
- `(repository_id)` unique — one package per repo
- `(org_id)` — org-level context listing

### `repo_context_entries`

Individual context entries within a package (architecture docs, conventions, test config, etc.).

| Column | Type | Notes |
|--------|------|-------|
| id | uuid | PK |
| package_id | uuid | FK -> repo_context_packages |
| org_id | uuid | FK -> organizations |
| entry_type | text | `architecture_doc`, `convention`, `file_map`, `test_config`, `dependency_map` |
| scope | text | file path or directory (null = repo-wide) |
| title | text | human-readable title |
| content | text | the actual context content (markdown) |
| source | text | `discovered`, `generated`, `user_authored`, `review_pattern` |
| confidence | float | how confident this is accurate (0-1) |
| last_validated | timestamptz | when last verified against codebase |
| stale | boolean | default false |
| metadata | jsonb | source-specific metadata |
| created_at | timestamptz | |
| updated_at | timestamptz | |

**Indexes:**
- `(package_id, entry_type)` — entries by type
- `(package_id, scope)` — entries by directory/file scope

### `repo_file_map`

Maps files to features, components, and ownership.

| Column | Type | Notes |
|--------|------|-------|
| id | uuid | PK |
| package_id | uuid | FK -> repo_context_packages |
| org_id | uuid | FK -> organizations |
| file_path | text | |
| feature | text | which feature this file belongs to |
| component | text | architectural component (e.g. `api`, `auth`, `billing`) |
| description | text | what this file does |
| change_frequency | float | commits per month |
| last_modified | timestamptz | |
| dependencies | text[] | files this file imports |
| dependents | text[] | files that import this file |
| test_files | text[] | associated test files |
| test_coverage_pct | float | line coverage percentage for this file (0-100). Updated from coverage snapshots |
| owners | text[] | CODEOWNERS entries |
| created_at | timestamptz | |
| updated_at | timestamptz | |

**Indexes:**
- `(package_id, file_path)` unique — one entry per file
- `(package_id, feature)` — files by feature
- `(package_id, component)` — files by component

## Advanced Self-Tuning Tables

### `tuning_config_versions`

Insert-only configuration versioning. Each row is an immutable snapshot of a configuration value. New configs are inserted; old ones are never mutated (except the `active` flag). Enables point-in-time rollback to any previous configuration.

| Column | Type | Notes |
|--------|------|-------|
| id | uuid | PK |
| org_id | uuid | FK -> organizations |
| config_scope | text | `complexity_calibration`, `agent_defaults`, `conventions`, `context_package`, `tuning_settings` |
| scope_key | text | scoping key within the scope (e.g. repo name, issue type, or `*` for org-wide) |
| version | int | monotonically increasing per (org_id, config_scope, scope_key) |
| config_snapshot | jsonb | full config value at this version (complete, not a delta) |
| active | boolean | NOT NULL DEFAULT true. Insert-only versioning flag |
| decision_id | uuid | FK -> tuning_decisions, nullable. Which tuning decision created this version |
| created_by | text | `system`, `user:<user_id>`, or `rollback` |
| created_at | timestamptz | immutable: rows are never updated, only new rows are inserted |

**Indexes:**
- `(org_id, config_scope, scope_key)` unique where `active = true` — one active version per scope
- `(org_id, config_scope, scope_key, created_at DESC)` — point-in-time queries
- `(org_id, config_scope, scope_key, version)` unique — version sequence

### `tuning_decisions`

Audit log of every automated tuning decision — whether applied, proposed, or rejected. References a `tuning_config_versions` row when applied.

| Column | Type | Notes |
|--------|------|-------|
| id | uuid | PK |
| org_id | uuid | FK -> organizations |
| loop_type | text | `experiment_config`, `complexity_calibration`, `context_enrichment`, `convention_apply`, `rollback` |
| trigger_type | text | `failure_pattern`, `experiment_result`, `calibration_drift`, `context_failure`, `review_pattern_promotion`, `regression_rollback`, `manual_rollback` |
| trigger_ref | jsonb | reference to what triggered this decision |
| decision | text | human-readable summary |
| config_scope | text | which config scope this affects |
| scope_key | text | which scope key this affects |
| before_version_id | uuid | FK -> tuning_config_versions, nullable. Config version before this change (null for first version) |
| after_version_id | uuid | FK -> tuning_config_versions, nullable. Config version after this change (null if not yet applied) |
| confidence | float | decision confidence (0-1) |
| status | text | `pending`, `approved`, `applied`, `rejected`, `rolled_back`, `superseded` |
| approved_by | uuid | FK -> users, nullable |
| applied_at | timestamptz | when the decision was applied |
| cost_impact | jsonb | estimated cost impact (`{estimated_token_delta, cost_increase_pct, reasoning}`) |
| created_at | timestamptz | immutable except for status transitions |

**Indexes:**
- `(org_id, loop_type, created_at DESC)` — decisions per loop
- `(org_id, status, created_at DESC)` — pending approval queue
- `(org_id, applied_at DESC)` where `status = 'applied'` — recent changes for regression checking

## Entity Relationships

```
organizations
  └── integrations
        └── webhook_deliveries
        └── integration_sync_runs
        └── repositories
              └── repo_context_packages
                    └── repo_context_entries
                    └── repo_file_map
  └── users
  └── issues (has repository_id FK)
        └── issue_events
        └── priority_scores
        └── priority_overrides
        └── complexity_estimates
        └── agent_runs
              └── agent_run_logs
              └── validations
              └── pull_requests
                    └── review_comments
                    └── deploys
                          └── experiments
                                └── production_learnings
  └── review_patterns
  └── eval_datasets
        └── eval_examples
  └── prompt_versions
  └── prompt_overrides
  └── eval_runs
        └── eval_run_results
  └── eval_release_gates
  └── tuning_config_versions
        └── tuning_decisions.decision_id (nullable)
  └── tuning_decisions
        └── tuning_config_versions.decision_id (nullable)
        └── tuning_config_versions.before_version_id (nullable)
        └── tuning_config_versions.after_version_id (nullable)
  └── jobs
  └── notification_preferences
  └── notifications
  └── org_token_usage

prompt_templates (global defaults)
  └── prompt_versions

nodes (independent — not org-scoped)
  └── jobs.locked_by_node_id (nullable)
```

## Notes

- All tables use UUIDs for primary keys (except log tables which use bigserial for performance).
- All timestamps are `timestamptz` (UTC).
- `jsonb` columns are used for flexible, schema-evolving data (raw payloads, config, metrics). Frequently queried fields should be promoted to proper columns as patterns stabilize.
- **Insert-only versioned settings**: Settings/config tables use an insert-only versioning pattern instead of `updated_at`. On update, the current row is deactivated (`active = false`) and a new row is inserted (`active = true`) with merged values. All historical versions are preserved. Tables using this pattern: `review_patterns`, `prompt_overrides`, `eval_release_gates`, `tuning_config_versions`. Unique constraints on these tables are partial indexes filtered on `active = true`. See AGENTS.md for the full implementation pattern.
- **Insert-only versioning vs audit log**: These two mechanisms are complementary, not competing. Use **insert-only versioning** when application code needs to read previous versions (rollback, canary traffic splitting, A/B testing). Use the **audit log** when you only need a compliance trail of "who did what when" — it's a write-and-forget append that is never queried for application logic. Tables like `organizations.settings`, `prompt_templates`, and `eval_datasets` change rarely and don't need rollback — the audit log is sufficient for their change history.
- **Tables that intentionally use `updated_at`**: Operational/lifecycle entities where in-place updates are the natural model: `issues` (status tracked via `issue_events`), `pull_requests` (synced from GitHub), `experiments` (lifecycle entity), `jobs` (transient work queue with locking), `repositories` (external entity sync), `prompt_versions` (already IS the version history), `repo_context_packages`/`repo_context_entries`/`repo_file_map` (computed/cached data rebuilt on context refresh).
- **Row-Level Security (RLS)**: Enabled as defense-in-depth on sensitive tables (`integrations`, `agent_runs`, `issues`, `pull_requests`, `organizations`, `users`, `eval_datasets`, `eval_examples`, `eval_runs`, `eval_run_results`). The application sets `app.current_org_id` on each connection and RLS policies enforce org isolation. Primary access control remains in the Go application layer via `org_id` filtering. See [20-security-architecture.md](20-security-architecture.md).
- **Data retention**: Raw payloads (`webhook_deliveries.payload`), agent run logs, and traces have configurable retention policies (defaults: 30 days for webhooks, 90 days for logs/traces). A scheduled `data_retention_cleanup` job runs daily and purges expired data while retaining metadata for analytics. The job processes tables in order: `agent_run_logs` (older than 90 days), `webhook_deliveries` (payload nullified after 30 days, metadata rows kept for 1 year), `audit_log` (partitioned by month, archived after 1 year). See [20-security-architecture.md](20-security-architecture.md).
- **Encryption**: The `integrations.config` column (containing API keys and credentials) is encrypted at rest using envelope encryption with `ENCRYPTION_MASTER_KEY`. See [20-security-architecture.md](20-security-architecture.md).
- **Private eval data**: `eval_examples` encrypted payload fields (`input_encrypted`, `expected_output_encrypted`, `ground_truth_encrypted`) are application-layer encrypted before insert. Only metadata is queryable in plaintext. See [16-ai-agent-evals.md](16-ai-agent-evals.md).
- **Unimplemented tables**: The following tables are defined in this document but not yet created via migrations: `experiments`, `production_learnings`, `notification_preferences`, `notifications`, `org_token_usage`, `priority_overrides`, `prompt_templates`, `prompt_versions`, `prompt_overrides`, `eval_examples`, `eval_run_results`, `repo_context_packages`, `repo_context_entries`, `repo_file_map`, `tuning_config_versions`, `tuning_decisions`. These represent planned features. Do not assume they exist in the database.

# Design: Database Schema

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
| slug | text | unique |
| settings | jsonb | org-wide config (autonomy level, token budget, product direction, execution aggressiveness, confidence thresholds, issue type overrides, etc.) |
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

Pre-run complexity estimation for each issue. Computed after prioritization, before agent execution. See [12-smart-routing.md](12-smart-routing.md).

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
| status | text | `pending`, `running`, `completed`, `failed`, `cancelled`, `skipped`, `needs_human_guidance` |
| autonomy_level | text | `manual`, `auto_simple`, `auto_all` |
| token_mode | text | `low`, `high` |
| complexity_tier | int | snapshot of the complexity tier at run time |
| confidence_score | float | agent's self-assessed confidence (0-1) |
| confidence_reasoning | text | agent's explanation of confidence |
| risk_factors | text[] | agent-identified risks |
| container_id | text | sandbox container identifier |
| started_at | timestamptz | |
| completed_at | timestamptz | |
| token_usage | jsonb | `{input_tokens, output_tokens, total_cost}` |
| failure_category | text | `context`, `reasoning`, `tooling`, `validation`, null if not failed |
| failure_code | text | specific code (e.g., `insufficient_context`, `wrong_root_cause`) |
| failure_reasoning | text | LLM explanation of why the run failed |
| failure_recommendations | text[] | actionable suggestions |
| experiment_id | uuid | FK -> agent_config_experiments, nullable |
| experiment_variant | text | which variant this run was assigned to |
| execution_mode | text | `batch` (default), `guided`, `investigate`, `pair`. See [18-interactive-sessions.md](18-interactive-sessions.md) |
| session_id | uuid | FK -> interactive_sessions, nullable. Set for interactive runs |
| test_gen_phase | text | `none`, `generating`, `completed`, `failed`. Tracks proactive test generation status. See [19-test-health.md](19-test-health.md) |
| parent_run_id | uuid | FK -> agent_runs, nullable. Set for revision runs triggered by review feedback |
| revision_context | jsonb | review feedback that triggered this revision run (null for initial runs) |
| error | text | failure reason if applicable |
| result_summary | text | agent-generated summary of what it did |
| diff | text | the generated code diff |
| created_at | timestamptz | |

### `agent_run_logs`

Streaming logs from an agent run for real-time UI display.

| Column | Type | Notes |
|--------|------|-------|
| id | bigserial | PK |
| agent_run_id | uuid | FK -> agent_runs |
| timestamp | timestamptz | |
| level | text | `info`, `debug`, `error`, `tool_use`, `output` |
| message | text | |
| metadata | jsonb | tool calls, file paths, etc. |

**Indexes:**
- `(agent_run_id, timestamp)` — log streaming

### `agent_run_traces`

Structured trace events for each agent run step. Captures the agent's decision-making process alongside regular log entries. See [15-run-debugging.md](15-run-debugging.md).

| Column | Type | Notes |
|--------|------|-------|
| id | bigserial | PK |
| agent_run_id | uuid | FK -> agent_runs |
| org_id | uuid | FK -> organizations |
| sequence | int | ordering within the run |
| timestamp | timestamptz | |
| phase | text | `context_gathering`, `analysis`, `implementation`, `testing`, `review` |
| action | text | `read_file`, `search`, `edit_file`, `run_command`, `think`, `plan` |
| input | jsonb | what the agent received |
| output_summary | jsonb | summarized result (not full file contents — those are in logs) |
| decision | text | agent's reasoning for what to do next |
| tokens_used | int | |
| duration_ms | int | |

**Indexes:**
- `(agent_run_id, sequence)` — trace replay
- `(org_id, phase)` — phase-level analytics

### `agent_config_experiments`

A/B experiments on agent configurations. Allows testing different prompts, context strategies, and settings against real outcomes. See [15-run-debugging.md](15-run-debugging.md).

| Column | Type | Notes |
|--------|------|-------|
| id | uuid | PK |
| org_id | uuid | FK -> organizations |
| name | text | human-readable experiment name |
| description | text | what this experiment tests |
| status | text | `draft`, `running`, `completed`, `stopped` |
| variants | jsonb | array of variant definitions (name, weight, config overrides) |
| metrics | text[] | which metrics to track |
| min_runs_per_variant | int | minimum sample size |
| results | jsonb | per-variant metric results, updated as runs complete |
| created_by_user_id | uuid | FK -> users |
| started_at | timestamptz | |
| completed_at | timestamptz | |
| created_at | timestamptz | |
| updated_at | timestamptz | |

**Indexes:**
- `(org_id, status)` — active experiments
- `(org_id, created_at DESC)` — experiment history

### `run_patterns`

Detected patterns from cross-run analysis. Compares successful and failed runs on similar issues to identify systemic problems. See [15-run-debugging.md](15-run-debugging.md).

| Column | Type | Notes |
|--------|------|-------|
| id | uuid | PK |
| org_id | uuid | FK -> organizations |
| pattern_type | text | `context_diff`, `approach_diff`, `complexity_mismatch` |
| description | text | human-readable pattern description |
| successful_run_id | uuid | FK -> agent_runs |
| failed_run_id | uuid | FK -> agent_runs |
| diff_summary | text | what was different |
| recommendation | text | actionable suggestion |
| repo | text | `owner/repo` |
| issue_type | text | issue type this pattern applies to |
| status | text | `detected`, `acknowledged`, `applied`, `dismissed` |
| created_at | timestamptz | |

**Indexes:**
- `(org_id, repo, status)` — active patterns per repo
- `(org_id, pattern_type)` — pattern type analytics

## Interactive Session Tables

### `interactive_sessions`

Tracks interactive agent sessions (guided, investigate, pair modes). See [18-interactive-sessions.md](18-interactive-sessions.md).

| Column | Type | Notes |
|--------|------|-------|
| id | uuid | PK |
| agent_run_id | uuid | FK -> agent_runs |
| org_id | uuid | FK -> organizations |
| user_id | uuid | FK -> users. The human participant |
| mode | text | `guided`, `investigate`, `pair` |
| status | text | `active`, `waiting_for_human`, `waiting_for_agent`, `completed`, `abandoned`, `timed_out` |
| started_at | timestamptz | |
| last_activity | timestamptz | |
| completed_at | timestamptz | |
| idle_timeout | interval | default 30 minutes |
| metadata | jsonb | mode-specific config (e.g., pair branch name) |
| created_at | timestamptz | |

**Indexes:**
- `(agent_run_id)` — session for a run
- `(user_id, status)` — user's active sessions
- `(org_id, status)` where status IN ('active', 'waiting_for_human', 'waiting_for_agent') — active sessions

### `session_messages`

All messages exchanged during an interactive session — agent questions, human answers, directives, status updates.

| Column | Type | Notes |
|--------|------|-------|
| id | uuid | PK |
| session_id | uuid | FK -> interactive_sessions |
| sender | text | `agent`, `human`, `system` |
| message_type | text | `question`, `answer`, `directive`, `status`, `checkpoint` |
| content | text | the message text |
| metadata | jsonb | structured data (options for questions, file refs, etc.) |
| created_at | timestamptz | |

**Indexes:**
- `(session_id, created_at)` — message history

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

Individual review comments captured from GitHub PRs, processed through a multi-stage filtering pipeline and classified by an LLM. By default, only comments on 143-generated PRs are captured; an org setting enables capture from all PRs. See [11-review-feedback-loop.md](11-review-feedback-loop.md).

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
| source_pr_type | text | `143_generated` (default) or `human_authored` |
| filter_status | text | `pending`, `filtered_structural`, `filtered_unmerged`, `filtered_not_directive`, `accepted` |
| adoption_evidence | boolean | was the suggestion adopted in the final merged code? |
| category | text | `style`, `logic_bug`, `edge_case`, `wrong_approach`, `missing_test`, `unnecessary_change`, `security`, `performance`, `nit` (null until classified) |
| severity | text | `low`, `medium`, `high` |
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
- `(org_id)` where `generalizable = true AND generalized_rule IS NOT NULL` — pattern extraction

### `review_patterns`

Per-repo knowledge base of recurring reviewer preferences, extracted from review comments. Active patterns are materialized into a `.143/learned-conventions.md` file in the repo.

| Column | Type | Notes |
|--------|------|-------|
| id | uuid | PK |
| org_id | uuid | FK -> organizations |
| repo | text | `owner/repo` |
| rule | text | the generalized instruction |
| category | text | same categories as review_comments |
| source_comment_ids | uuid[] | review_comments that produced this rule |
| occurrence_count | int | default 1 |
| confidence | float | default 0.5 |
| status | text | `candidate`, `active`, `dismissed` |
| manually_curated | boolean | default false. True if admin edited the rule or it came from a manual file edit |
| active | boolean | NOT NULL DEFAULT true. Insert-only versioning flag |
| created_at | timestamptz | |

**Indexes:**
- `(org_id, repo, status)` where `active = true` — active patterns per repo
- `(org_id, repo, rule)` unique where `active = true` — deduplication

### `reviewer_trust`

Admin-assigned trust tiers for reviewers. Controls how quickly a reviewer's generalizable comment is promoted to an active pattern.

| Column | Type | Notes |
|--------|------|-------|
| id | uuid | PK |
| org_id | uuid | FK -> organizations |
| repo | text | `owner/repo`, or `*` for org-wide |
| reviewer | text | GitHub username |
| trust_tier | text | `maintainer`, `contributor` (default), `external` |
| set_by_user_id | uuid | FK -> users |
| notes | text | |
| active | boolean | NOT NULL DEFAULT true. Insert-only versioning flag |
| created_at | timestamptz | |

**Indexes:**
- `(org_id, repo, reviewer)` unique where `active = true` — one trust tier per reviewer per repo

### `review_outcomes`

Tracks reviewer acceptance rates per PR for analytics.

| Column | Type | Notes |
|--------|------|-------|
| id | uuid | PK |
| pull_request_id | uuid | FK -> pull_requests |
| org_id | uuid | FK -> organizations |
| repo | text | `owner/repo` |
| issue_source | text | sentry, linear, support |
| issue_severity | text | critical, high, medium, low |
| review_result | text | `approved`, `changes_requested`, `rejected` |
| revision_count | int | default 0 |
| reviewer | text | GitHub username |
| reviewer_trust_tier | text | snapshot of trust tier at review time |
| time_to_review | interval | time from PR open to first review |
| comment_count | int | default 0 |
| comment_categories | text[] | categories of comments received |
| created_at | timestamptz | |

**Indexes:**
- `(org_id, repo)` — per-repo analytics
- `(org_id, review_result)` — acceptance rate queries

## Test Health Tables

### `test_coverage_snapshots`

Point-in-time coverage data collected from CI runs. Each snapshot captures per-file and aggregate coverage for a repository at a specific commit. See [19-test-health.md](19-test-health.md).

| Column | Type | Notes |
|--------|------|-------|
| id | uuid | PK |
| repository_id | uuid | FK -> repositories |
| org_id | uuid | FK -> organizations |
| agent_run_id | uuid | FK -> agent_runs, nullable. Null for baseline snapshots |
| commit_sha | text | commit at which coverage was measured |
| branch | text | branch name |
| aggregate_line_pct | float | overall line coverage (0-100) |
| aggregate_branch_pct | float | overall branch coverage (0-100) |
| per_file | jsonb | `[{file, line_pct, branch_pct, lines_covered, lines_total}]` |
| tool | text | `go_cover`, `jest`, `pytest_cov`, `jacoco`, etc. |
| raw_report | text | path to stored raw coverage report (S3/local) |
| created_at | timestamptz | |

**Indexes:**
- `(repository_id, created_at DESC)` — coverage trend queries
- `(agent_run_id)` — coverage for a specific run
- `(repository_id, branch, created_at DESC)` — per-branch trends

### `test_executions`

Individual test case results from CI runs. Used for flaky test detection and slow test identification.

| Column | Type | Notes |
|--------|------|-------|
| id | uuid | PK |
| coverage_snapshot_id | uuid | FK -> test_coverage_snapshots |
| org_id | uuid | FK -> organizations |
| test_name | text | fully qualified test name |
| test_file | text | file containing the test |
| suite | text | test suite or package |
| status | text | `passed`, `failed`, `skipped`, `errored` |
| duration_ms | int | execution time |
| error_message | text | failure message if applicable |
| retry_count | int | default 0. Number of retries in this run |
| created_at | timestamptz | |

**Indexes:**
- `(coverage_snapshot_id)` — tests per snapshot
- `(org_id, test_name, created_at DESC)` — per-test history for flaky detection
- `(org_id, status, created_at DESC)` — failure queries
- `(org_id, duration_ms DESC)` — slow test identification

### `test_health_issues`

Detected test suite health problems (flaky tests, slow tests, coverage gaps). Auto-detected by cross-run analysis.

| Column | Type | Notes |
|--------|------|-------|
| id | uuid | PK |
| repository_id | uuid | FK -> repositories |
| org_id | uuid | FK -> organizations |
| issue_type | text | `flaky`, `slow`, `coverage_gap`, `always_failing` |
| test_name | text | fully qualified test name (null for coverage_gap) |
| test_file | text | file containing the test (null for coverage_gap) |
| target_file | text | file with coverage gap (for coverage_gap type) |
| severity | text | `low`, `medium`, `high` |
| details | jsonb | type-specific data (flaky: flip count/window; slow: p50/p99 duration; coverage_gap: current_pct/target_pct) |
| status | text | `open`, `acknowledged`, `fixed`, `dismissed` |
| first_detected_at | timestamptz | |
| last_seen_at | timestamptz | |
| resolved_at | timestamptz | |
| created_at | timestamptz | |
| updated_at | timestamptz | |

**Indexes:**
- `(repository_id, issue_type, status)` — dashboard queries
- `(org_id, severity, status)` — org-level health view
- `(repository_id, test_name)` where `test_name IS NOT NULL` — per-test issue lookup

### `regression_test_coverage`

Tracks whether agent-generated fixes include regression tests that reproduce the original bug. Linked to validation results.

| Column | Type | Notes |
|--------|------|-------|
| id | uuid | PK |
| agent_run_id | uuid | FK -> agent_runs |
| org_id | uuid | FK -> organizations |
| issue_source | text | `sentry`, `linear`, `support` |
| regression_test_present | boolean | was a regression test included? |
| test_name | text | name of the regression test (null if not present) |
| test_file | text | file containing the regression test |
| reproduces_bug | text | `yes`, `no`, `uncertain` — does the test reproduce the original issue? |
| reasoning | text | LLM explanation |
| created_at | timestamptz | |

**Indexes:**
- `(agent_run_id)` — regression test for a run
- `(org_id, issue_source, regression_test_present)` — compliance tracking by source

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
| adapter | text | `claude_code`, `codex`, `gemini_cli`, etc. |
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

## Cost Intelligence Tables

### `cost_summaries`

Materialized per-fix rollups for token usage and optional dollar costs.

| Column | Type | Notes |
|--------|------|-------|
| id | uuid | PK |
| org_id | uuid | FK -> organizations |
| agent_run_id | uuid | FK -> agent_runs |
| issue_id | uuid | FK -> issues |
| total_tokens | bigint | total tokens across run + validation |
| input_tokens | bigint | |
| output_tokens | bigint | |
| cached_tokens | bigint | default 0 |
| llm_cost_usd | numeric | nullable for subscription billing |
| compute_seconds | int | sandbox runtime |
| compute_cost_usd | numeric | nullable |
| review_seconds | int | nullable; human review time |
| review_cost_usd | numeric | nullable |
| total_cost_usd | numeric | nullable if any component is non-dollar-accounted |
| experiment_outcome | text | denormalized from `experiments.outcome` |
| impact_score | float | denormalized from priority/impact signals |
| created_at | timestamptz | |

**Indexes:**
- `(org_id, created_at DESC)` — org rollups and trends
- `(issue_id)` — cost by issue
- `(agent_run_id)` — cost for a specific run

### `budget_periods`

Per-org budget windows with usage and forecast counters.

| Column | Type | Notes |
|--------|------|-------|
| id | uuid | PK |
| org_id | uuid | FK -> organizations |
| period_start | date | period start |
| period_end | date | period end |
| token_budget | bigint | primary budget cap |
| tokens_used | bigint | running total |
| tokens_forecasted | bigint | forecast to period end |
| dollar_budget_usd | numeric | nullable |
| dollars_spent_usd | numeric | nullable |
| dollars_forecasted_usd | numeric | nullable |
| throttle_active | boolean | default false |
| updated_at | timestamptz | |

**Indexes:**
- `(org_id, period_start)` unique — one row per org period
- `(org_id, updated_at DESC)` — current budget status queries

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

Insert-only configuration versioning. Each row is an immutable snapshot of a configuration value. New configs are inserted; old ones are never mutated (except the `is_active` flag). Enables point-in-time rollback to any previous configuration.

| Column | Type | Notes |
|--------|------|-------|
| id | uuid | PK |
| org_id | uuid | FK -> organizations |
| config_scope | text | `complexity_calibration`, `agent_defaults`, `conventions`, `context_package`, `tuning_settings` |
| scope_key | text | scoping key within the scope (e.g. repo name, issue type, or `*` for org-wide) |
| version | int | monotonically increasing per (org_id, config_scope, scope_key) |
| config_snapshot | jsonb | full config value at this version (complete, not a delta) |
| is_active | boolean | only one version per (org_id, config_scope, scope_key) is active at a time |
| decision_id | uuid | FK -> tuning_decisions, nullable. Which tuning decision created this version |
| created_by | text | `system`, `user:<user_id>`, or `rollback` |
| created_at | timestamptz | immutable: rows are never updated, only new rows are inserted |

**Indexes:**
- `(org_id, config_scope, scope_key)` unique where `is_active = true` — one active version per scope
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
| before_version | int | version number before this change (null for first version) |
| after_version | int | version number after this change (null if not yet applied) |
| before_snapshot | jsonb | config before |
| after_snapshot | jsonb | config after |
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
              └── test_coverage_snapshots
                    └── test_executions
              └── test_health_issues
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
              └── agent_run_traces
              └── interactive_sessions
                    └── session_messages
              └── validations
              └── regression_test_coverage
              └── pull_requests
                    └── review_comments
                    └── review_outcomes
                    └── deploys
                          └── experiments
  └── agent_config_experiments
        └── agent_runs.experiment_id (nullable)
  └── run_patterns
  └── review_patterns
  └── reviewer_trust
  └── eval_datasets
        └── eval_examples
  └── prompt_versions
  └── prompt_overrides
  └── eval_runs
        └── eval_run_results
  └── eval_release_gates
  └── cost_summaries
  └── budget_periods
  └── tuning_config_versions
        └── tuning_decisions.decision_id (nullable)
  └── tuning_decisions
        └── tuning_config_versions.decision_id (nullable)
  └── jobs

prompt_templates (global defaults)
  └── prompt_versions

nodes (independent — not org-scoped)
  └── jobs.locked_by_node_id (nullable)
```

## Notes

- All tables use UUIDs for primary keys (except log tables which use bigserial for performance).
- All timestamps are `timestamptz` (UTC).
- `jsonb` columns are used for flexible, schema-evolving data (raw payloads, config, metrics). Frequently queried fields should be promoted to proper columns as patterns stabilize.
- **Insert-only versioned settings**: Settings/config tables use an insert-only versioning pattern instead of `updated_at`. On update, the current row is deactivated (`active = false`) and a new row is inserted (`active = true`) with merged values. All historical versions are preserved. Tables using this pattern: `review_patterns`, `reviewer_trust`, `prompt_overrides`, `eval_release_gates`. Unique constraints on these tables are partial indexes filtered on `active = true`. See AGENTS.md for the full implementation pattern.
- **Candidates for insert-only versioning extraction**: The following tables contain settings that would benefit from insert-only versioning but cannot use it directly because they are FK targets for child tables: `organizations` (settings jsonb — consider extracting to a separate `org_settings` table), `prompt_templates` (default_content — consider referencing by `key` instead of `id`), `eval_datasets` (metadata — consider extracting mutable fields to a separate settings table). These tables retain `updated_at` for now.
- **Tables that intentionally use `updated_at`**: Operational/lifecycle entities where in-place updates are the natural model: `issues` (status tracked via `issue_events`), `pull_requests` (synced from GitHub), `agent_config_experiments` (lifecycle entity), `experiments` (lifecycle entity), `test_health_issues` (issue tracking lifecycle), `jobs` (transient work queue with locking), `repositories` (external entity sync), `prompt_versions` (already IS the version history), `repo_context_packages`/`repo_context_entries`/`repo_file_map` (computed/cached data rebuilt on context refresh), `budget_periods` (running counters with incremental updates).
- **Row-Level Security (RLS)**: Enabled as defense-in-depth on sensitive tables (`integrations`, `agent_runs`, `issues`, `pull_requests`, `organizations`, `users`, `eval_datasets`, `eval_examples`, `eval_runs`, `eval_run_results`, `cost_summaries`, `budget_periods`). The application sets `app.current_org_id` on each connection and RLS policies enforce org isolation. Primary access control remains in the Go application layer via `org_id` filtering. See [20-security-architecture.md](20-security-architecture.md).
- **Data retention**: Raw payloads (`webhook_deliveries.payload`), agent run logs, and traces have configurable retention policies (defaults: 30 days for webhooks, 90 days for logs/traces). A daily cleanup job purges expired data while retaining metadata for analytics. See [20-security-architecture.md](20-security-architecture.md).
- **Encryption**: The `integrations.config` column (containing API keys and credentials) is encrypted at rest using envelope encryption with `ENCRYPTION_MASTER_KEY`. See [20-security-architecture.md](20-security-architecture.md).
- **Private eval data**: `eval_examples` encrypted payload fields (`input_encrypted`, `expected_output_encrypted`, `ground_truth_encrypted`) are application-layer encrypted before insert. Only metadata is queryable in plaintext. See [16-ai-agent-evals.md](16-ai-agent-evals.md).

# Design: Agent Run Replays & Debugging

This document describes how 143.dev captures, classifies, and surfaces agent run behavior to help teams understand why runs fail and systematically improve outcomes.

## Problem

When an agent run fails — bad code, validation rejection, PR rejected by a reviewer — the team needs to understand _why_. Today's agent logs show what happened (tool calls, file edits) but not the decision-making process. Without structured failure analysis, the same mistakes repeat, and there's no systematic way to test whether a prompt change or model switch actually improves things.

## Overview

The debugging system adds four capabilities:

1. **Structured run traces** — not just logs, but a step-by-step decision trace capturing what the agent read, what it considered, what it decided, and why
2. **Failure classification** — automated categorization of why a run failed (context problem, reasoning problem, tooling problem)
3. **Agent config experiments** — A/B testing framework for prompts, models, context packages, measured by real outcomes
4. **Cross-run pattern analysis** — compare successful and failed runs on similar issues to detect systemic problems

## Structured Run Traces

The existing `agent_run_logs` table captures streaming log entries during execution. The trace system enriches this with structured decision events that capture the agent's reasoning, not just its actions.

### Trace Events

Each trace event represents a discrete step in the agent's work. The adapter captures these alongside regular log entries:

```go
type TraceEvent struct {
    Timestamp   time.Time
    Phase       string                 // "context_gathering", "analysis", "implementation", "testing", "review"
    Action      string                 // "read_file", "search", "edit_file", "run_command", "think", "plan"
    Input       map[string]interface{} // what the agent received (file path, search query, etc.)
    Output      map[string]interface{} // what came back (file contents summary, search results count, etc.)
    Decision    string                 // what the agent decided to do next and why (from agent reasoning)
    TokensUsed  int                    // tokens consumed by this step
    DurationMs  int                    // wall-clock time for this step
}
```

### Phases

Each run is broken into logical phases:

| Phase | Description | What to Capture |
|-------|-------------|-----------------|
| `context_gathering` | Agent reads files, searches codebase, studies stack traces | Which files read, search queries used, how much of the codebase explored |
| `analysis` | Agent reasons about the root cause | Root cause hypothesis, alternative hypotheses considered, which evidence supported each |
| `implementation` | Agent writes the fix | Files edited, lines changed, approach taken vs. alternatives |
| `testing` | Agent writes/runs tests | Tests added, test results, coverage impact |
| `review` | Agent self-reviews the diff | Self-identified issues, confidence reasoning |

### How Traces Are Captured

For agents that expose structured output (e.g., Claude Code's tool use stream), the adapter parses each tool call into a `TraceEvent`. For agents that only produce text logs, the adapter does a best-effort parse using known patterns (file reads, shell commands, etc.).

Trace events are stored in the `agent_run_traces` table (see Database Changes) and are queryable via the API.

### Trace Viewer UI

The run detail page gets a new "Trace" tab alongside the existing "Logs" and "Diff" tabs:

- **Timeline view**: vertical timeline of trace events, grouped by phase, with expandable details
- **Decision points**: highlighted moments where the agent made a choice (e.g., "chose to edit file A instead of file B")
- **Context map**: visual summary of what files/functions the agent read vs. what it changed — helps spot cases where the agent didn't read enough context or read irrelevant context
- **Token usage breakdown**: pie chart of tokens spent per phase — helps identify if the agent is spending too much on context gathering vs. implementation

## Failure Classification

When a run fails (validation failure, crash, timeout, PR rejection), the system automatically classifies the root cause. This classification drives both dashboards and improvement recommendations.

### Failure Categories

| Category | Code | Description | Signal |
|----------|------|-------------|--------|
| Context problem | `insufficient_context` | Agent didn't read enough relevant code | Few files read, key files missing from trace |
| Context problem | `irrelevant_context` | Agent read too many unrelated files, wasting tokens | High token usage on context_gathering, files read not related to fix |
| Context problem | `missing_info` | Issue description or stack trace lacked needed detail | Agent couldn't identify root cause, expressed uncertainty |
| Reasoning problem | `wrong_root_cause` | Agent identified wrong root cause despite having the info | Trace shows agent read correct files but drew wrong conclusion |
| Reasoning problem | `wrong_approach` | Agent identified correct root cause but chose wrong fix strategy | Fix attempts correct problem area but with wrong technique |
| Reasoning problem | `incomplete_fix` | Agent fixed part of the problem but missed related cases | Diff is partial, tests catch missing edge cases |
| Reasoning problem | `overcomplicated` | Agent produced an overly complex fix when a simpler one existed | Large diff, quality check failure citing unnecessary changes |
| Tooling problem | `timeout` | Agent ran out of time | Container timeout |
| Tooling problem | `sandbox_error` | Container or network error unrelated to the fix | Non-zero exit code from sandbox infrastructure, not from agent |
| Tooling problem | `token_limit` | Agent hit token budget before completing | Token usage at max, truncated output |
| Validation problem | `ci_failure` | Fix broke existing tests or lint | CI check failed |
| Validation problem | `direction_mismatch` | Fix didn't align with product direction | Direction check failed |

### Classification Method

Failure classification runs as a post-processing step after a run ends in a non-success state. It uses an LLM call with:

- The run trace (phases, key decisions)
- The diff (if any was produced)
- The validation failure details (if applicable)
- The issue context

```go
type FailureClassification struct {
    Category       string   // top-level: "context", "reasoning", "tooling", "validation"
    Code           string   // specific code from the table above
    Confidence     float64  // 0-1
    Reasoning      string   // LLM explanation
    Recommendations []string // actionable suggestions (e.g., "add more file context", "use a different model")
}
```

The classification is stored on the `agent_runs` record and displayed in the run detail page.

### Failure Dashboard

A new section in the dashboard aggregates failure data:

- **Failure rate by category** — pie chart showing distribution of context / reasoning / tooling / validation failures
- **Failure rate over time** — line chart showing whether failures are trending up or down
- **Top failure codes** — ranked list of most common specific failure codes
- **Failure rate by issue type** — matrix showing which issue types (bug_fix, performance, etc.) fail most often and why
- **Failure rate by complexity tier** — shows correlation between estimated complexity and actual outcome

## Agent Config Experiments

To systematically improve agent outcomes, admins can run A/B experiments on agent configurations: different prompts, context strategies, models, or settings.

### Experiment Design

An agent config experiment defines two or more "variants" and a traffic split:

```json
{
  "name": "expanded_context_test",
  "description": "Test whether giving the agent 3x more file context improves fix rate",
  "status": "running",
  "variants": [
    {
      "name": "control",
      "weight": 50,
      "config": {}
    },
    {
      "name": "expanded_context",
      "weight": 50,
      "config": {
        "context_file_limit": 30,
        "prompt_addendum": "Read broadly before making changes. Explore at least 10 files related to the issue."
      }
    }
  ],
  "metrics": ["fix_rate", "validation_pass_rate", "pr_approval_rate", "avg_cost", "avg_tokens"],
  "min_runs_per_variant": 30,
  "started_at": "2025-01-15T00:00:00Z"
}
```

### How Experiments Work

1. Admin creates an experiment via the settings UI or API
2. When a new agent run is triggered, the orchestrator checks for active experiments
3. The run is assigned to a variant based on the weight distribution (deterministic hash on issue ID for consistency — same issue always gets the same variant)
4. The variant's config overrides are applied to the agent input (prompt addendum, context limits, etc.)
5. The run's variant assignment is recorded on the `agent_runs` record
6. After enough runs accumulate, the experiment results page shows per-variant metrics

### Experiment Metrics

| Metric | Description |
|--------|-------------|
| `fix_rate` | % of runs that produce a diff (agent didn't give up or error out) |
| `validation_pass_rate` | % of completed runs that pass all validation checks |
| `pr_approval_rate` | % of PRs that get approved without changes requested |
| `avg_cost` | average cost per run (tokens * price) |
| `avg_tokens` | average token usage per run |
| `avg_duration` | average wall-clock time per run |
| `confidence_score_avg` | average agent confidence score |
| `failure_category_distribution` | breakdown of failure categories per variant |

### Statistical Significance

The experiment system uses a simple frequentist approach:

- Minimum sample size per variant (configurable, default: 30 runs)
- Chi-squared test for rate metrics (fix_rate, pass_rate, approval_rate)
- T-test for continuous metrics (cost, tokens, duration)
- Results shown as "significant" (p < 0.05), "trending" (p < 0.20), or "not enough data"

### Experiment Lifecycle

```
draft → running → completed
                → stopped (admin cancelled early)
```

- **Draft**: experiment defined but not active. Runs are not assigned to variants.
- **Running**: actively assigning runs to variants. Can be stopped early.
- **Completed**: min sample size reached for all variants. Results are final. No new runs are assigned.

## Cross-Run Pattern Analysis

The system compares successful and failed runs on similar issues to detect patterns that explain why some issues get fixed and others don't.

### Similarity Matching

Two runs are "similar" if they share:

- Same repo
- Same issue type (from complexity estimation)
- Same or adjacent complexity tier
- Similar issue description (cosine similarity of embeddings above a threshold, or same Sentry error group)

### Pattern Detection

The analysis service periodically scans completed runs and identifies patterns:

```go
type RunPattern struct {
    PatternType   string   // "context_diff", "approach_diff", "complexity_mismatch"
    Description   string   // human-readable description
    SuccessfulRun uuid.UUID
    FailedRun     uuid.UUID
    Diff          string   // what was different between the two runs
    Recommendation string  // actionable suggestion
}
```

Examples of detected patterns:

- **Context diff**: "Successful runs on nil-pointer issues in this repo consistently read the test file first. Failed runs skip it." → Recommendation: add test file to default context
- **Approach diff**: "Successful runs on API handler bugs add input validation. Failed runs try to fix the downstream function." → Recommendation: add prompt guidance about input validation patterns
- **Complexity mismatch**: "Issues classified as 'simple' in this repo fail 60% of the time. Re-classification as 'moderate' would match actual outcomes." → Recommendation: adjust complexity estimation for this repo

### Pattern Surfacing

Detected patterns are shown in:

- **Run detail page**: "Similar issues" section showing comparable successful/failed runs with a side-by-side trace diff
- **Issue type analytics**: aggregate patterns per issue type
- **Settings recommendations**: the system suggests prompt or config changes based on detected patterns

## Database Changes

### New: `agent_run_traces` table

Structured trace events for each agent run step.

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

### New: `agent_config_experiments` table

A/B experiments on agent configurations.

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

### New: `run_patterns` table

Detected patterns from cross-run analysis.

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

### Updated: `agent_runs` table

Add columns for failure classification and experiment tracking:

| New Column | Type | Notes |
|-----------|------|-------|
| failure_category | text | `context`, `reasoning`, `tooling`, `validation`, null if not failed |
| failure_code | text | specific code (e.g., `insufficient_context`, `wrong_root_cause`) |
| failure_reasoning | text | LLM explanation of why the run failed |
| failure_recommendations | text[] | actionable suggestions |
| experiment_id | uuid | FK -> agent_config_experiments, nullable |
| experiment_variant | text | which variant this run was assigned to |

## API Changes

### New Endpoints

```
GET  /api/v1/agent-runs/:id/trace           # get structured trace events for a run
GET  /api/v1/agent-runs/:id/failure          # get failure classification for a run
POST /api/v1/agent-runs/:id/classify-failure # trigger failure classification (usually automatic)

GET  /api/v1/agent-runs/:id/similar          # find similar runs (successful and failed) for comparison

GET  /api/v1/experiments/agent-configs        # list agent config experiments
POST /api/v1/experiments/agent-configs        # create a new experiment
GET  /api/v1/experiments/agent-configs/:id    # get experiment details + results
PATCH /api/v1/experiments/agent-configs/:id   # update experiment (start, stop)

GET  /api/v1/patterns                        # list detected run patterns
PATCH /api/v1/patterns/:id                   # acknowledge, apply, or dismiss a pattern

GET  /api/v1/analytics                        # aggregate failure analytics (for analytics page)
```

## Frontend Changes

### Run Detail Page — Integration

The run detail page (`/runs/[id]`) integrates debugging signals into the existing IA:
- **Overview** includes failure classification, recommendations, and similar runs for failed runs.
- **Logs** includes a "Show detailed trace" button that opens a **slide-out drawer** (`run-trace-drawer.tsx`) with the structured trace view (phase timeline + token usage). The drawer preserves the log stream on the left for cross-referencing.
- No separate trace/failure top-level tabs are required.

### New Pages

#### Agent Config Experiments (`/settings/experiments`)

Experiments are a separate settings page (not embedded in agent config) because they are a distinct workflow (create → run → analyze → decide) that can overwhelm users who just want to tweak a threshold. See `03-frontend.md` for the full settings architecture.

- **Experiment list**: name, status, variants, progress (runs completed / min required)
- **Experiment detail**: per-variant metrics comparison, statistical significance indicators, traffic split visualization
- **Create experiment form**: name, description, variant definitions (name, weight, config overrides as JSON), metrics to track, min sample size

#### Failure Analytics (`/analytics`)

- **Failure rate by category** — pie chart
- **Failure trends over time** — line chart with category breakdown
- **Top failure codes** — ranked table with counts and example runs
- **Detected patterns** — list of patterns with accept/dismiss actions

Intentionally omitted from the analytics page: **failure matrix** (available as CSV export) and **impact over time** (available via API). See `03-frontend.md` for rationale.

### Project Structure Additions

```
frontend/src/
├── app/
│   ├── runs/[id]/
│   │   └── page.tsx                     # updated: overview/logs integration for trace/failure
│   ├── settings/
│   │   └── experiments/
│   │       └── page.tsx                 # agent config experiments (separate settings page)
│   └── analytics/
│       └── page.tsx                     # failure analytics dashboard
├── components/
│   ├── runs/
│   │   ├── run-trace-drawer.tsx         # slide-out drawer for structured trace
│   │   ├── run-failure-card.tsx         # failure classification display
│   │   └── run-similarity-card.tsx      # similar runs comparison
│   ├── experiments/
│   │   ├── experiment-table.tsx
│   │   ├── experiment-results.tsx       # per-variant metrics
│   │   └── create-experiment-form.tsx
│   └── analytics/
│       ├── failure-category-chart.tsx
│       ├── failure-trends-chart.tsx
│       └── pattern-list.tsx
└── hooks/
    ├── use-run-trace.ts
    ├── use-experiments.ts               # updated: add agent config experiment hooks
    └── use-failure-analytics.ts
```

## Backend Structure Additions

```
internal/
├── api/handlers/
│   ├── agent_runs.go                    # updated: add trace, failure, similar endpoints
│   ├── experiments.go                   # updated: add agent config experiment handlers
│   └── analytics.go                     # new: failure analytics handlers
├── services/
│   ├── agent/
│   │   ├── orchestrator.go              # updated: experiment variant assignment
│   │   └── tracing.go                   # new: trace event capture and storage
│   └── debugging/
│       ├── classifier.go               # new: failure classification service
│       ├── experiments.go              # new: agent config experiment lifecycle
│       ├── patterns.go                 # new: cross-run pattern detection
│       └── similarity.go              # new: run similarity matching
└── worker/jobs/
    ├── classify_failure.go              # new: post-run failure classification job
    └── detect_patterns.go              # new: periodic pattern detection job
```

## Job Changes

### New: `classify_failure` job

Triggered automatically when a run ends in `failed`, `skipped`, or when validation fails. Runs the LLM classifier and stores the result.

### New: `detect_patterns` job

Scheduled periodically (every 6 hours). Scans recent completed runs, finds similar pairs, and detects patterns. Creates `run_patterns` records for new findings.

### Updated: `run_agent` job

Before execution, checks for active agent config experiments. If one is active, assigns the run to a variant and applies config overrides.

## Metrics

New Datadog metrics:

- `143.failures.rate` (gauge) — overall failure rate, tagged by `category` and `code`
- `143.failures.classification_time_ms` (histogram) — time to classify a failure
- `143.experiments.active_count` (gauge) — number of running agent config experiments
- `143.experiments.runs_per_variant` (counter) — runs assigned per variant
- `143.patterns.detected_count` (counter) — new patterns detected
- `143.traces.events_per_run` (histogram) — number of trace events per run

## Build Order

This feature should be built as Phase 9, after the review feedback loop is in place. It depends on having a corpus of completed runs (both successful and failed) to analyze.

1. **Structured traces** — extend the agent adapter to emit trace events, add the trace table and viewer UI
2. **Failure classification** — add the classifier job, failure columns on agent_runs, failure tab on run detail page
3. **Failure analytics dashboard** — aggregate failure data, build the analytics page
4. **Agent config experiments** — add the experiment table, variant assignment in orchestrator, results page
5. **Cross-run pattern detection** — add similarity matching, pattern detection job, pattern list UI

**Milestone**: Teams can replay any agent run step-by-step, understand why failures happen, run controlled experiments on agent configs, and get automated recommendations for improvement.

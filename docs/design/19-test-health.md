# Design: Test Health & Generation

This document describes how 143.dev treats test infrastructure as a first-class concern — measuring test coverage, proactively generating tests, tracking test suite health, and requiring regression tests for every fix. Every fix that ships with a test makes future fixes in that area more reliable.

## Overview

Agent success is deeply coupled to test quality. An agent fixing code in an area with 0% test coverage is flying blind — it can't verify its fix, and the validation pipeline's CI check is weak in that region. The current design treats tests as a given; this system actively improves the test foundation.

Four components:

1. **Test Coverage Scoring** — per-file and per-feature coverage data surfaced in the codebase context package
2. **Proactive Test Generation** — the agent generates missing tests for the affected area before or alongside a fix
3. **Test Suite Health Dashboard** — flaky test detection, slow test identification, coverage trends over time
4. **Regression Test Validation** — every fix must include a test that would have caught the original bug

## 1. Test Coverage Scoring

### Coverage Collection

Every time CI runs in a sandbox (during validation or proactive test generation), the system collects coverage data. Most test frameworks support coverage output:

```go
func (v *Validator) RunCIWithCoverage(ctx context.Context, sandbox *Sandbox) (*CIResult, error) {
    switch {
    case fileExists(sandbox, "go.mod"):
        run(sandbox, "go test -coverprofile=coverage.out ./...")
        run(sandbox, "go tool cover -func=coverage.out -o coverage.txt")
    case fileExists(sandbox, "package.json"):
        run(sandbox, "npx jest --coverage --coverageReporters=json-summary")
    case fileExists(sandbox, "requirements.txt"):
        run(sandbox, "pytest --cov --cov-report=json")
    }

    // Parse the coverage output into a structured format
    return parseCoverageReport(sandbox)
}
```

### Coverage Data Model

```sql
CREATE TABLE test_coverage_snapshots (
    id              uuid PRIMARY KEY,
    repository_id   uuid NOT NULL REFERENCES repositories(id),
    org_id          uuid NOT NULL REFERENCES organizations(id),
    commit_sha      text NOT NULL,
    source          text NOT NULL,         -- 'validation_run', 'proactive_gen', 'scheduled_scan'
    agent_run_id    uuid REFERENCES agent_runs(id),  -- nullable, set when from a validation run
    overall_line_pct    float NOT NULL,    -- total line coverage percentage
    overall_branch_pct  float,            -- total branch coverage (if available)
    file_coverage   jsonb NOT NULL,        -- per-file coverage: [{path, lines_covered, lines_total, line_pct, branch_pct}]
    feature_coverage jsonb,               -- per-feature aggregation (from file map): [{feature, line_pct, files_measured}]
    collected_at    timestamptz NOT NULL DEFAULT now(),
    created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_coverage_repo ON test_coverage_snapshots (repository_id, collected_at DESC);
CREATE INDEX idx_coverage_agent_run ON test_coverage_snapshots (agent_run_id) WHERE agent_run_id IS NOT NULL;
```

### Integration with Codebase Context (doc 14)

Coverage data is surfaced in the context package so agents know where the gaps are. The `repo_file_map` table already has a `test_files` column; we extend the context with per-file coverage:

```go
func (b *ContextBuilder) EnrichFileMapWithCoverage(ctx context.Context, repo *models.Repository) error {
    // Get the latest coverage snapshot
    snapshot, _ := b.db.GetLatestCoverage(ctx, repo.ID)
    if snapshot == nil {
        return nil // no coverage data yet
    }

    for _, fc := range snapshot.FileCoverage {
        b.db.UpdateFileMapCoverage(ctx, repo.ContextPackageID, fc.Path, fc.LinePct)
    }
    return nil
}
```

New column on `repo_file_map`:

```sql
ALTER TABLE repo_file_map ADD COLUMN test_coverage_pct float;
-- null = unknown, 0 = no tests covering this file, 100 = fully covered
```

### Coverage in Agent Prompts

When assembling context for an agent run, the orchestrator includes coverage information for relevant files:

```go
// In the agent prompt's relevant files section:
// - `internal/api/users.go` — handles user CRUD (feature: auth, component: api)
//   Tests: internal/api/users_test.go
//   Test coverage: 72%
//
// - `internal/api/billing.go` — handles billing webhooks (feature: billing, component: api)
//   Tests: none
//   Test coverage: 0% ⚠️ — consider generating tests before fixing
```

This lets the agent know when it's working in a poorly-tested area and should be more cautious or generate tests first.

### Coverage Quality Score Integration

Coverage data feeds into the context quality score (doc 14):

```go
{
    Name:   "test_coverage",
    Weight: 0.15,  // replaces part of the existing test_infrastructure weight
    Score:  b.scoreTestCoverage(ctx, pkg),
    // Score based on: overall coverage > 60% (40pts), no critical files at 0% (30pts),
    // coverage trending up (30pts)
}
```

## 2. Proactive Test Generation

### The Problem

When an agent fixes a bug in an area with no tests, it can't verify the fix works. The CI check passes (no tests to fail), but the fix might be wrong. Worse, the same bug can recur because there's no regression test.

### The Solution

Before or alongside generating a fix, the agent can proactively generate tests for the affected area. This serves two purposes:
- The fix is validated against real tests (not just LLM review)
- The test lives on after the fix, improving future agent success in that area

### Proactive Test Generation Flow

```
Issue selected for agent run
        │
        ▼
  Check coverage of affected files
        │
        ▼
  Coverage < threshold?  ──no──▶  Normal fix flow
        │
       yes
        │
        ▼
  Agent generates tests first
  (separate prompt focused on
   understanding + testing the
   existing behavior)
        │
        ▼
  Run tests in sandbox
  (verify they pass on current code)
        │
        ▼
  Agent generates fix
  (with new tests as context)
        │
        ▼
  Run tests again
  (verify fix doesn't break
   existing tests + new test
   for the bug now passes)
        │
        ▼
  Validation pipeline
  (with coverage data from both runs)
```

### Two-Phase Agent Prompt

When proactive test generation is triggered, the agent receives a two-phase prompt:

**Phase 1: Test Generation**

```
## Phase 1: Generate Tests

Before fixing this issue, generate tests for the affected area.
The following files have low or no test coverage:

{files_with_low_coverage}

Current behavior of these files (from reading the code):
- Understand what each function does
- Write tests that verify the CURRENT behavior (before your fix)
- Focus on the code paths related to the reported issue

Test infrastructure for this repo:
{test_config}

Test patterns used in this repo:
{test_samples}

Output: test files only. Do not modify any source files yet.
```

**Phase 2: Fix with Test Context**

```
## Phase 2: Fix the Issue

Now fix the reported issue. You have the following tests available:
{generated_tests}

Requirements:
1. Fix the underlying bug
2. Add or modify a test that specifically reproduces the original bug
   and verifies your fix resolves it
3. All existing tests (including the ones you just generated) must still pass
```

### Configuration

Proactive test generation is configurable per org:

```json
{
  "test_generation": {
    "enabled": true,
    "coverage_threshold": 30,
    "max_test_gen_tokens": 50000,
    "include_in_pr": true
  }
}
```

| Setting | Default | Description |
|---------|---------|-------------|
| `enabled` | `true` | Enable proactive test generation |
| `coverage_threshold` | `30` | Trigger test gen when affected files have < this % coverage |
| `max_test_gen_tokens` | `50000` | Token budget for test generation phase |
| `include_in_pr` | `true` | Include generated tests in the PR diff alongside the fix |

### Agent Run Tracking

Proactive test generation is tracked in the `agent_runs` table:

```sql
ALTER TABLE agent_runs ADD COLUMN test_gen_phase jsonb;
-- {
--   "triggered": true,
--   "reason": "coverage_below_threshold",
--   "files_targeted": ["internal/api/users.go"],
--   "tests_generated": ["internal/api/users_test.go"],
--   "pre_coverage_pct": 12,
--   "post_coverage_pct": 68,
--   "tokens_used": 15200
-- }
```

## 3. Test Suite Health Dashboard

### Data Collection

Every CI run in a sandbox produces test execution data. The system already runs tests during validation (doc 07) — we capture richer data from each run.

```go
type TestExecutionResult struct {
    TotalTests   int
    Passed       int
    Failed       int
    Skipped      int
    Duration     time.Duration
    Tests        []TestResult
}

type TestResult struct {
    Name       string
    Package    string        // go package, jest describe, pytest module
    Status     string        // passed, failed, skipped, error
    Duration   time.Duration
    Error      string        // failure message if failed
    FilePath   string        // test file
}
```

### Data Model

```sql
CREATE TABLE test_executions (
    id              uuid PRIMARY KEY,
    repository_id   uuid NOT NULL REFERENCES repositories(id),
    org_id          uuid NOT NULL REFERENCES organizations(id),
    agent_run_id    uuid REFERENCES agent_runs(id),  -- nullable, set when from validation
    commit_sha      text NOT NULL,
    total_tests     int NOT NULL,
    passed          int NOT NULL,
    failed          int NOT NULL,
    skipped         int NOT NULL,
    duration_ms     int NOT NULL,
    test_results    jsonb NOT NULL,         -- array of individual test results
    executed_at     timestamptz NOT NULL DEFAULT now(),
    created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_test_exec_repo ON test_executions (repository_id, executed_at DESC);
CREATE INDEX idx_test_exec_agent_run ON test_executions (agent_run_id) WHERE agent_run_id IS NOT NULL;
```

### Flaky Test Detection

A test is flaky if it produces different results on the same code. The system detects this by comparing test results across runs on the same commit or across runs where the test file wasn't modified:

```go
func (t *TestHealthService) DetectFlakyTests(ctx context.Context, repoID uuid.UUID) ([]FlakyTest, error) {
    // Query test_executions for the last 30 days
    // Group by test name
    // A test is flaky if it has both pass and fail results
    // on commits where the test file was not modified

    results, _ := t.db.Query(ctx, `
        SELECT test_name, test_file,
               COUNT(*) FILTER (WHERE status = 'passed') as pass_count,
               COUNT(*) FILTER (WHERE status = 'failed') as fail_count,
               COUNT(*) as total_runs
        FROM test_executions te,
             jsonb_to_recordset(te.test_results) AS t(name text, status text, file_path text)
        WHERE te.repository_id = $1
          AND te.executed_at > now() - interval '30 days'
        GROUP BY test_name, test_file
        HAVING COUNT(*) FILTER (WHERE status = 'passed') > 0
           AND COUNT(*) FILTER (WHERE status = 'failed') > 0
    `, repoID)

    return results
}
```

Flaky tests are surfaced in the dashboard and can be automatically excluded from CI checks during validation (with a warning) to prevent them from blocking good fixes.

### Slow Test Detection

Tests that take disproportionately long slow down validation and reduce agent throughput:

```go
func (t *TestHealthService) DetectSlowTests(ctx context.Context, repoID uuid.UUID) ([]SlowTest, error) {
    // Find tests whose duration is > 2 standard deviations above the mean
    // for their test file, across the last 30 days of runs
}
```

### Test Health Data Model

```sql
CREATE TABLE test_health_issues (
    id              uuid PRIMARY KEY,
    repository_id   uuid NOT NULL REFERENCES repositories(id),
    org_id          uuid NOT NULL REFERENCES organizations(id),
    test_name       text NOT NULL,
    test_file       text NOT NULL,
    issue_type      text NOT NULL,         -- 'flaky', 'slow', 'always_skipped'
    severity        text NOT NULL,         -- 'low', 'medium', 'high'
    details         jsonb NOT NULL,        -- type-specific details
    first_detected  timestamptz NOT NULL,
    last_seen       timestamptz NOT NULL,
    occurrences     int NOT NULL DEFAULT 1,
    status          text NOT NULL DEFAULT 'open',  -- open, acknowledged, fixed, dismissed
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_test_health_repo ON test_health_issues (repository_id, status);
CREATE UNIQUE INDEX idx_test_health_dedup ON test_health_issues (repository_id, test_name, issue_type);
```

### Dashboard

The test suite health dashboard shows:

- **Coverage overview** — overall coverage percentage, trend over time (line chart), per-feature breakdown (heatmap)
- **Coverage gaps** — files/features with 0% coverage, ranked by change frequency (high churn + no tests = highest risk)
- **Flaky tests** — list with pass/fail ratio, last occurrence, impacted agent runs
- **Slow tests** — list with median/p95 duration, percentage of total CI time
- **Test generation impact** — how much coverage has been added by proactive test generation, trend over time
- **Coverage by fix** — each shipped PR's coverage delta (did this fix improve or reduce coverage?)

## 4. Regression Test Validation

### Enhanced Test Coverage Check

The current validation pipeline (doc 07) has a "Test Coverage Check" that uses LLM analysis to determine if tests are needed. This is enhanced to be more specific about regression tests:

```
You are reviewing a code fix for test adequacy.

Issue: {issue.title} — {issue.description}
Issue source: {issue.source} (sentry error / linear issue / support ticket)
Diff: {diff}

Test coverage of affected files:
{per_file_coverage}

Existing test files for affected code:
{existing_test_files}

New/modified test files in this diff:
{test_files_in_diff}

Evaluate:

1. REGRESSION TEST: Does this diff include a test that specifically reproduces the original
   bug/error? A regression test should:
   - Set up the conditions that caused the original issue
   - Assert the correct behavior (that was previously broken)
   - Fail on the code BEFORE the fix, pass on the code AFTER the fix
   Verdict: PRESENT / MISSING / NOT_APPLICABLE (with reasoning)

2. COVERAGE IMPACT: Does this diff improve, maintain, or reduce test coverage
   for the affected files?
   Verdict: IMPROVED / MAINTAINED / REDUCED (with delta estimate)

3. TEST QUALITY: If tests were added or modified, are they meaningful?
   - Do they test real behavior, not just mock implementations?
   - Do they cover edge cases?
   - Do they follow the repo's test patterns?
   Verdict: GOOD / ADEQUATE / POOR / NO_TESTS (with reasoning)

Overall verdict: PASS or FAIL
A FAIL should be issued if:
  - A regression test is missing for a bug fix (source = sentry or support)
  - Test coverage was reduced
  - Tests were added but are meaningless
```

### Regression Test Requirement

The key behavioral change: **bug fixes from Sentry or support sources require a regression test**. This is enforced as a validation check, not just a suggestion.

The check is strongest for:
- **Sentry errors**: The stack trace gives exact reproduction conditions. A regression test should exercise the same code path.
- **Support tickets**: The customer report describes the failure scenario. A regression test should model it.
- **Linear issues**: These may be feature requests or refactors where a regression test is less applicable. The check is softer here.

### Validation Pipeline Changes

The existing test coverage check in the validation pipeline is replaced with the enhanced version:

```go
checks := []struct {
    name string
    fn   func(context.Context, *models.AgentRun) (string, string, error)
}{
    {"direction_check", v.checkDirection},
    {"correctness_check", v.checkCorrectness},
    {"quality_check", v.checkQuality},
    {"regression_test_check", v.checkRegressionTest},  // replaces test_coverage_check
    {"ci_check", v.checkCI},
}
```

The `regression_test_check` field replaces `test_coverage_check` in the `validations` table:

```sql
ALTER TABLE validations ADD COLUMN regression_test_check text; -- pass, fail, skip
ALTER TABLE validations ADD COLUMN coverage_delta float;       -- coverage change from this fix
```

### Compounding Effect

The compounding effect is tracked explicitly. Each shipped fix with a regression test is a data point:

```sql
CREATE TABLE regression_test_coverage (
    id              uuid PRIMARY KEY,
    pull_request_id uuid NOT NULL REFERENCES pull_requests(id),
    repository_id   uuid NOT NULL REFERENCES repositories(id),
    org_id          uuid NOT NULL REFERENCES organizations(id),
    issue_id        uuid NOT NULL REFERENCES issues(id),
    test_file       text NOT NULL,          -- the regression test file
    test_name       text NOT NULL,          -- the specific test
    covers_files    text[] NOT NULL,        -- source files this test covers
    coverage_delta  float,                  -- coverage change from this test
    created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_regression_tests_repo ON regression_test_coverage (repository_id);
CREATE INDEX idx_regression_tests_issue ON regression_test_coverage (issue_id);
```

This table lets us answer: "How many of our production issues are now covered by regression tests from previous fixes?" — a direct measure of the compounding effect.

## Integration with Existing Pipeline

### Connections to Other Design Docs

**Validation Pipeline (doc 07)**:
- `test_coverage_check` is replaced by `regression_test_check` which is more specific and includes regression test requirement
- CI check now collects coverage data as a side effect
- Coverage delta is tracked per validation

**Codebase Context Layer (doc 14)**:
- `repo_file_map` gains `test_coverage_pct` column
- Coverage data feeds into the context quality score
- Coverage information is included in agent prompts for relevant files
- Component 4 (Test Infrastructure) is enhanced with coverage data

**Agent Orchestrator (doc 06)**:
- `AgentInput` gains coverage context for relevant files
- Two-phase prompt for proactive test generation when coverage is below threshold
- `agent_runs` gains `test_gen_phase` column for tracking

**Observability (doc 09)**:
- Coverage trend is a repo health metric alongside experiment outcomes
- Coverage delta per deployed fix is tracked

**PR & Ship (doc 08)**:
- PR body includes coverage delta badge
- Regression test presence is noted in the PR summary

**Database Schema (doc 01)**:
- New tables: `test_coverage_snapshots`, `test_executions`, `test_health_issues`, `regression_test_coverage`
- New columns: `repo_file_map.test_coverage_pct`, `agent_runs.test_gen_phase`, `validations.regression_test_check`, `validations.coverage_delta`

### Job Queue

New job types:

| Job Type | Queue | Trigger |
|----------|-------|---------|
| `collect_coverage` | `testing` | After CI runs in validation, parse and store coverage data |
| `detect_flaky_tests` | `testing` | Scheduled daily, analyze test result consistency |
| `detect_slow_tests` | `testing` | Scheduled daily, analyze test duration outliers |
| `generate_proactive_tests` | `agent` | Before fix, when affected files have low coverage |
| `update_test_health` | `testing` | After test execution, update health issues |

## Build Order

Test health is built across multiple phases, since it enhances components that are built at different times:

### Phase 5 Enhancement: Regression Test Validation

Built alongside the validation pipeline:

1. **Enhanced test coverage check** — replace the basic LLM check with the regression test-aware version
2. **Coverage collection** — parse coverage output from CI runs, store in `test_coverage_snapshots`
3. **Coverage delta tracking** — compute coverage change per validation

### Phase 10: Full Test Health (doc: 19)

Built after the core pipeline is operational and generating data:

1. **Test execution tracking** — store individual test results from every CI run
2. **Flaky test detection** — analyze test result consistency, surface flaky tests
3. **Slow test detection** — identify duration outliers
4. **Coverage integration with context** — enrich `repo_file_map` with coverage data, include in agent prompts
5. **Proactive test generation** — two-phase agent prompt, coverage threshold trigger
6. **Test health dashboard** — coverage trends, flaky/slow test lists, test generation impact
7. **Regression test compounding** — track `regression_test_coverage`, measure compounding effect

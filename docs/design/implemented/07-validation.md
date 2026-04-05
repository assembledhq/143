# Design: Validation Pipeline

> **Status:** Implemented | **Last reviewed:** 2026-03-25

This document describes how 143.dev validates the code produced by an agent run before it becomes a PR.

## Overview

After an agent run completes with a diff, the validation pipeline runs a series of checks. All checks must pass before a PR is opened. If any check fails, the run is marked as needing attention and the admin is notified.

## Validation Checks

The pipeline runs these checks in order:

### 1. Direction Check

**Purpose**: Ensure the fix aligns with the org's product direction and doesn't introduce unwanted scope.

**Method**: LLM-based review. Send the diff + issue context + product direction text to an LLM and ask:

- Does this fix address the reported issue?
- Is it aligned with the stated product direction?
- Does it introduce unrelated changes?

**Output**: `pass` / `fail` with reasoning.

**Prompt template**:
```
You are a code reviewer. Given the following:
- Issue: {issue.title} — {issue.description}
- Product direction: {org.product_direction}
- Diff: {diff}

Answer:
1. Does this diff fix the reported issue? (yes/no + reasoning)
2. Is this aligned with the product direction? (yes/no + reasoning)
3. Does it introduce unrelated changes? (yes/no + reasoning)

Verdict: PASS or FAIL
```

### 2. Correctness Check

**Purpose**: Verify the code is logically correct and doesn't introduce bugs.

**Method**: LLM-based review with a focus on correctness:

- Check for logic errors, off-by-one errors, null pointer issues
- Verify error handling
- Check for race conditions (if concurrent code)
- Verify edge cases

**Prompt template**:
```
You are a senior engineer reviewing a code diff for correctness.

Diff: {diff}
Context: This fixes {issue.title}

Check for:
1. Logic errors
2. Missing error handling
3. Edge cases not handled
4. Race conditions
5. Breaking changes to existing behavior

Verdict: PASS or FAIL with specific issues found.
```

### 3. Quality Check

**Purpose**: Ensure the diff is minimal, clean, and follows project conventions.

**Method**: LLM-based review + static analysis:

- Diff should be minimal (no unnecessary changes)
- Code should follow existing project style
- No commented-out code or debug statements
- No hardcoded values that should be configurable

**Static checks (automated)**:
- Diff size: warn if > 200 lines changed (suggest the agent was too aggressive)
- No files outside the expected scope were modified

### 4. Security Scan

**Purpose**: Detect security vulnerabilities, leaked secrets, and data exfiltration patterns in the agent-generated diff before it becomes a PR.

**Method**: Automated static analysis run inside the sandbox:

1. **Secret scanning** — run `gitleaks` on the diff to detect accidentally committed API keys, tokens, passwords, and other credentials.
2. **SAST** — run `semgrep` with auto-configuration to detect common security vulnerabilities (SQL injection, XSS, command injection, path traversal, etc.).
3. **Exfiltration pattern detection** — custom scan of the diff for suspicious patterns:
   - Outbound HTTP requests to non-allowlisted domains
   - Base64 encoding of file contents or environment variables
   - Writing secrets or env vars to committed files
   - Subprocess spawning with shell commands that pipe data externally
   - DNS-based exfiltration patterns

**Output**: `pass` / `fail` with specific findings.

**Fail behavior**: Any detected secret or security vulnerability is a hard failure. Exfiltration patterns are flagged with high severity. See [20-security-architecture.md](20-security-architecture.md) for full details.

### 5. Regression Test Check

**Purpose**: Ensure the fix includes a regression test that would have caught the original bug.

**Method**: LLM analysis with test coverage context:

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

**Regression test requirement by source:**
- **Sentry errors**: Required. The stack trace gives exact reproduction conditions.
- **Support tickets**: Required. The customer report describes the failure scenario.
- **Linear issues**: Recommended but not required. These may be feature requests or refactors.

**Output**: `pass` / `fail` with reasoning, plus a coverage delta estimate.

### 6. CI/CD Check

**Purpose**: Ensure the code compiles, lints, and passes existing tests. Also collects test coverage data.

**Method**: Run the project's CI pipeline in the sandbox with coverage enabled:

```go
func (v *Validator) RunCIWithCoverage(ctx context.Context, sandbox *Sandbox) (*CIResult, error) {
    // Detect project type and run appropriate commands with coverage
    switch {
    case fileExists(sandbox, "go.mod"):
        run(sandbox, "go build ./...")
        run(sandbox, "go test -coverprofile=coverage.out ./...")
        run(sandbox, "go vet ./...")
    case fileExists(sandbox, "package.json"):
        run(sandbox, "npm ci")
        run(sandbox, "npm run lint")
        run(sandbox, "npx jest --coverage --coverageReporters=json-summary")
    case fileExists(sandbox, "requirements.txt"):
        run(sandbox, "pip install -r requirements.txt")
        run(sandbox, "pytest --cov --cov-report=json")
    // ... more project types
    }

    coverage := parseCoverageReport(sandbox)
    v.db.CreateCoverageSnapshot(ctx, coverage)

    return result
}
```

Alternatively, if the repo has a `.143.ci` config or a Makefile target, use that:

```bash
make ci   # or: ./scripts/ci.sh
```

**Output**: `pass` if all commands exit 0, `fail` with the failing command's output.

## Pipeline Execution

```go
func (v *Validator) Validate(ctx context.Context, agentRun *models.AgentRun) error {
    validation := &models.Validation{
        AgentRunID: agentRun.ID,
        Status:     "running",
    }
    v.db.CreateValidation(ctx, validation)

    checks := []struct {
        name string
        fn   func(context.Context, *models.AgentRun) (string, string, error)
    }{
        {"direction_check", v.checkDirection},
        {"correctness_check", v.checkCorrectness},
        {"quality_check", v.checkQuality},
        {"security_scan", v.checkSecurity},
        {"regression_test_check", v.checkRegressionTest},
        {"ci_check", v.checkCI},
    }

    allPassed := true
    for _, check := range checks {
        result, detail, err := check.fn(ctx, agentRun)
        if err != nil {
            result = "fail"
            detail = err.Error()
        }
        // Update the specific check field on the validation record
        v.db.UpdateValidationCheck(ctx, validation.ID, check.name, result, detail)
        if result == "fail" {
            allPassed = false
            break // fail fast — no point running CI if correctness fails
        }
    }

    if allPassed {
        validation.Status = "passed"
        // Enqueue PR creation
        v.jobs.Enqueue(ctx, "open_pr", map[string]interface{}{"agent_run_id": agentRun.ID})
    } else {
        validation.Status = "failed"
    }
    v.db.UpdateValidation(ctx, validation)
    return nil
}
```

## Fail-Fast Behavior

The pipeline stops at the first failure. This saves LLM tokens and CI time. The order is intentional:

1. **Direction** first — if the fix is off-topic, no point checking correctness
2. **Correctness** second — if the code is wrong, no point checking quality
3. **Quality** third — style issues before spending time on security scanning
4. **Security scan** fourth — detect secrets, vulnerabilities, and exfiltration before running CI
5. **Regression test** fifth — check before running CI
6. **CI** last — most expensive check, only runs if everything else passes

## LLM Configuration

Validation LLM calls use a separate, cheaper model than the coding agent:

- Default: Claude Sonnet or equivalent (fast, cost-effective for review tasks)
- The model and temperature are configurable per org
- All LLM calls include the full diff context + issue context
- Token usage for validation is tracked separately from agent run usage

## Handling Failures

When validation fails:

1. The `agent_run` stays in `completed` status (the agent did its job)
2. The `validation` record is set to `failed` with details
3. The issue status is updated to `triaged` (back in the queue)
4. The admin sees the failure in the UI with the specific check that failed and the LLM's reasoning
5. The admin can:
   - Retry the agent run (maybe with different prompt or agent type)
   - Manually fix and override (mark validation as passed)
   - Dismiss the issue

## Prompt Injection Defense in Validation

Validation LLM prompts wrap the diff in explicit delimiters (`<code_diff>` tags) and include instructions that the content is code to be reviewed, not instructions to follow. This prevents injected instructions in the agent's diff from influencing validation outcomes. See [20-security-architecture.md](20-security-architecture.md) for the full defense model.

## Manual Override

Only **admin** users can override a failed validation. The RBAC middleware enforces this (see [20-security-architecture.md](20-security-architecture.md)):

```
POST /api/v1/validations/:id/override
{
  "reason": "False positive — the direction check was wrong, this fix is on-topic"
}
```

This marks the validation as `passed` (with an immutable audit log entry) and enqueues PR creation. Security scan failures **cannot** be overridden — they always require a new agent run.

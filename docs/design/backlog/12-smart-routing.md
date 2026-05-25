# Design: Smart Issue Routing

> **Status:** Backlog | **Last reviewed:** 2026-05-06

This document describes how 143.dev estimates issue complexity and routes issues to the right execution strategy.

## Problem

Not all issues are the same. A typo in an error message and a race condition in a distributed queue require fundamentally different agent strategies and resource budgets. The system needs to know which issues are worth attempting given the admin's risk tolerance.

## Overview

The smart routing system adds three capabilities:

1. **Complexity estimation** — predict issue difficulty before running an agent
2. **Execution aggressiveness control** — admin-configurable slider that determines how far the system goes (simple fixes only vs. attempt everything)
3. **Explicit post-run outcomes** — execution status, diff presence, failures, validation, and PR/review state determine what happens after a run

The admin selects their preferred coding agent (Claude Code, Codex, Gemini CLI, etc.) and model separately in the agent settings. Smart routing does not change which agent or model is used — it controls _which issues_ are attempted.

## Complexity Estimation

Before an agent run is launched, the system estimates the complexity of the issue. This happens after prioritization and before agent execution.

### Estimation Inputs

The estimator uses:

- **Issue metadata**: title, description, severity, source, tags
- **Stack trace analysis** (Sentry issues): depth, number of frames in user code, presence of concurrency primitives
- **Codebase signals**: number of files likely involved (from stack trace or file references), file size, recent churn
- **Historical data**: past agent run outcomes on similar issues (same repo, same issue type, similar severity)

### Complexity Tiers

| Tier | Label | Description | Examples |
|------|-------|-------------|----------|
| 1 | `trivial` | Single-line or few-line fix, obvious from context | Typos, wrong constant, missing null check, off-by-one |
| 2 | `simple` | Localized fix in 1-2 files, clear root cause | Missing error handling, wrong API parameter, simple logic bug |
| 3 | `moderate` | Multi-file change, requires understanding of component interactions | Refactoring a function signature, fixing a state management bug |
| 4 | `complex` | Architectural change or deep investigation needed | Race conditions, performance regressions, cross-service bugs |
| 5 | `very_complex` | Multi-system issue, unclear root cause, high risk of side effects | Data corruption bugs, distributed system failures, security vulnerabilities |

### Estimation Method

The complexity estimator is an LLM call (using a fast/cheap model like Haiku) that receives the issue context and returns a structured response:

```json
{
  "tier": 2,
  "label": "simple",
  "confidence": 0.82,
  "reasoning": "Stack trace points to a single function in api/handlers/users.go with a nil pointer dereference. The fix likely requires adding a nil check.",
  "estimated_files": ["api/handlers/users.go"],
  "estimated_tokens": 30000,
  "issue_type": "bug_fix"
}
```

### Issue Type Classification

The estimator also classifies the issue type, which determines the agent prompt strategy and validation criteria:

| Issue Type | Description | Agent Strategy |
|-----------|-------------|----------------|
| `bug_fix` | Fix broken behavior | Targeted prompt, regression test required |
| `error_handling` | Missing or incorrect error handling | Focus on error paths, add error tests |
| `performance` | Slowness, high resource usage | Profiling-aware prompt, benchmark tests |
| `refactor` | Code quality improvement | Broader context needed, style-focused validation |
| `feature_gap` | Missing functionality causing customer pain | Requires more exploration, higher token budget |
| `security` | Security vulnerability | Security-focused review, stricter validation |

## Execution Aggressiveness

Admins configure how aggressively the system attempts fixes via a setting in the settings panel. This is the key control for balancing cost vs. coverage.

### Aggressiveness Levels

| Level | Name | Description | Tiers Attempted |
|-------|------|-------------|-----------------|
| 1 | Conservative | Only attempt issues with high fix likelihood | Tier 1-2 only |
| 2 | Moderate | Attempt most issues, skip the hardest | Tier 1-3 |
| 3 | Aggressive | Attempt everything, including hard issues | Tier 1-4 |
| 4 | Maximum | Attempt all issues regardless of complexity | Tier 1-5 |

The aggressiveness level is stored in `organizations.settings` as `execution_aggressiveness` (integer 1-4, default: 2).

### How Aggressiveness Interacts with Autonomy

The existing `autonomy_level` controls _when_ runs trigger (manual vs. auto). The `execution_aggressiveness` controls _which_ issues are attempted. They're independent:

- `autonomy_level = manual` + `aggressiveness = 3`: Admin manually triggers runs, but the system will attempt hard issues when triggered
- `autonomy_level = auto_all` + `aggressiveness = 1`: System auto-triggers but only for trivial/simple issues

### Auto-Skip Logic

When the system considers triggering an agent run (either auto or manual), it checks:

```
if issue.complexity_tier > aggressiveness_level_max_tier:
    if auto-triggered:
        skip issue, mark as "too_complex_for_current_settings"
    if manually triggered:
        warn admin: "This issue is estimated as {tier}. Your current
        aggressiveness is set to {level}. Run anyway?"
```

## Post-Run Outcome Routing

Agent runs record explicit outcomes instead of an opaque trust score:

- success with a usable diff proceeds to PR creation
- human-input requests pause the session as `awaiting_input`
- infrastructure, agent, validation, and no-diff failures are recorded with concrete failure categories and next steps

### Admin Notification

When a run requires operator action:

- The run is marked `needs_human_guidance`
- The admin sees a notification in the dashboard
- The admin can review the result and decide to:
  - Approve and continue to validation
  - Retry with a different agent or model
  - Dismiss and handle manually

## Settings UI

The agent settings page (`/settings/agents`) is extended with a new "Execution Strategy" section:

### Agent & Model Selection

Admins choose their preferred coding agent and model in the existing agent config section:

- **Agent type**: Claude Code, Codex, Gemini CLI, or custom
- **Model**: whichever model the chosen agent supports (e.g., for Claude Code: Opus, Sonnet, Haiku)

The system always uses the admin's configured agent and model for all runs. Smart routing does not override this — it only controls which issues are attempted.

### Aggressiveness Slider

A labeled slider with 4 positions:

```
Conservative ──── Moderate ──── Aggressive ──── Maximum
     1                2              3              4

"Only simple     "Most issues,   "Attempt hard   "Attempt
 fixes"          skip hardest"   issues too"     everything"
```

Each position shows:
- Description of what it does
- Estimated cost impact (e.g., "~$X/month based on your issue volume")
- Expected coverage (e.g., "covers ~40% / ~70% / ~90% / ~100% of incoming issues")

### Per-Issue-Type Overrides (Advanced)

An expandable "Advanced" section where admins can override settings per issue type:

| Issue Type | Max Tier |
|-----------|----------|
| bug_fix | Use default |
| performance | Tier 3 max |
| security | Tier 4 max |

## Database Changes

### New: `complexity_estimates` table

Stores the pre-run complexity estimation for each issue.

| Column | Type | Notes |
|--------|------|-------|
| id | uuid | PK |
| issue_id | uuid | FK -> issues, unique |
| org_id | uuid | FK -> organizations |
| tier | int | 1-5 |
| label | text | `trivial`, `simple`, `moderate`, `complex`, `very_complex` |
| confidence | float | estimator's confidence in the tier (0-1) |
| issue_type | text | `bug_fix`, `error_handling`, `performance`, `refactor`, `feature_gap`, `security` |
| reasoning | text | LLM reasoning for the classification |
| estimated_files | text[] | files likely involved |
| estimated_tokens | int | predicted token usage |
| model_used | text | which model did the estimation |
| computed_at | timestamptz | |
| created_at | timestamptz | |

**Indexes:**
- `(org_id, tier)` — filter by complexity
- `(issue_id)` unique — one estimate per issue

### Updated: `agent_runs` table

Add columns for routing:

| New Column | Type | Notes |
|-----------|------|-------|
| complexity_tier | int | snapshot of the complexity tier at run time |

### Updated: `organizations.settings` JSONB

Add new fields to the settings object:

```json
{
  "execution_aggressiveness": 2,
  "issue_type_overrides": {
    "security": {
      "max_tier": 4
    }
  }
}
```

## API Changes

### New Endpoints

```
GET  /api/v1/issues/:id/complexity    # get complexity estimate for an issue
POST /api/v1/issues/:id/estimate      # trigger complexity estimation
```

### Updated Endpoints

```
PATCH /api/v1/settings    # now accepts execution_aggressiveness, issue_type_overrides

POST /api/v1/issues/:id/run-agent    # now accepts optional overrides:
  {
    "skip_complexity_check": true     # bypass aggressiveness filter
  }

POST /api/v1/agent-runs/:id/guide    # approve, provide guidance, or dismiss a needs_human_guidance run
GET  /api/v1/agent-runs/:id/resume-info  # get sandbox connection info for local resume
```

## Job Changes

### Updated: `run_agent` job

The `run_agent` job now:

1. Fetches the complexity estimate (or computes one if missing)
2. Checks aggressiveness level — skips if issue is too complex
3. Runs the agent using the admin's configured agent type and model
4. Enqueues follow-up publication work according to the session's validation policy and user action

### New: `estimate_complexity` job

Triggered after prioritization for newly eligible issues. Runs the LLM estimator and stores the result.

## Metrics

New Datadog metrics for monitoring:

- `143.complexity.tier` (histogram) — distribution of estimated complexity tiers
- `143.complexity.estimation_time_ms` (histogram) — time to estimate complexity
- `143.routing.skip_rate` (gauge) — % of issues skipped due to aggressiveness setting, tagged by `tier`

## Build Order

This feature spans multiple phases and should be integrated into the existing build order:

1. **Phase 3 addition**: Add complexity estimation to the prioritization pipeline (after scoring, before agent eligibility)
2. **Phase 4 addition**: Add the execution strategy settings to the agent settings UI

# Design: First-Run Experience & Time-to-Value

> **Status:** Implemented | **Last reviewed:** 2026-03-19

This document describes how 143.dev gets new users from sign-up to their first shipped fix as fast as possible. The goal: value in under 10 minutes, "aha moment" in under 5.

## Overview

The current onboarding flow has a dead zone: after connecting a repo, users wait for context to build and integrations to ingest before anything useful happens. This is where developer tools lose people.

This design fixes that by:

1. **Parallelizing setup** — context builds in the background while users explore
2. **Finding fixable issues immediately** — using data the GitHub App already has, no extra integrations required
3. **Guiding users to their first fix** — a focused onboarding flow that leads to a real PR
4. **Showing progress everywhere** — no silent waiting, always communicating what's happening

## Onboarding Flow

```
Sign in with GitHub
        │
        ▼
┌───────────────┐
│  Install App  │  User picks org + repos
│  + Select     │
│    Repos      │
└───────┬───────┘
        │
        ├──────────────────────────────────────────────┐
        │ (parallel)                                   │
        ▼                                              ▼
┌───────────────┐                            ┌─────────────────┐
│ Build Context │  background job            │  Quick-Start    │
│ (async)       │  ~1-5 min depending        │  Issue Scan     │
│               │  on repo size              │  (fast, <30s)   │
└───────┬───────┘                            └────────┬────────┘
        │                                             │
        │                                             ▼
        │                                    ┌─────────────────┐
        │                                    │  Show "Quick    │
        │                                    │  Wins" List     │
        │                                    │  (user picks)   │
        │                                    └────────┬────────┘
        │                                             │
        │                                             ▼
        │                                    ┌─────────────────┐
        │                                    │  "Fix This"     │
        ├───────────────────────────────────▶│  Agent Run      │
        │ context ready (or partial)         │  (live logs)    │
                                             └────────┬────────┘
                                                      │
                                                      ▼
                                             ┌─────────────────┐
                                             │  PR Opens       │
                                             │  "AHA MOMENT"   │
                                             └─────────────────┘
```

The critical insight: the Quick-Start Issue Scan runs in parallel with context building and uses only data available from the GitHub App. No Sentry or Linear connection is required for the first fix.

## Quick-Start Issue Scan

After a repo is connected, the system immediately scans for issues the agent is likely to fix successfully — before context is fully built, before Sentry or Linear are connected.

### Data Sources (GitHub App Only)

The GitHub App installation already provides enough signal to find fixable issues:

| Source | How | Signal Quality |
|--------|-----|----------------|
| **Open GitHub Issues** | `GET /repos/{owner}/{repo}/issues?labels=bug&state=open&sort=updated` | Good — especially with "bug" label |
| **Recent CI failures** | `GET /repos/{owner}/{repo}/check-runs?status=completed&conclusion=failure` | High — concrete test failures |
| **Dependabot/security alerts** | `GET /repos/{owner}/{repo}/dependabot/alerts?state=open` | High — well-scoped, often simple |
| **Recent error-related commits** | Search commit messages for "fix", "bug", "error", "revert" in last 30 days | Medium — indicates active pain areas |

When Sentry or Linear are also connected (either during initial setup or because they were configured earlier), those sources are included too and ranked higher because they carry customer-impact signal.

### Candidate Scoring

Each quick-start candidate gets a lightweight score to surface the best "first fix":

```go
type QuickStartCandidate struct {
    Source        string          // "github_issue", "ci_failure", "dependabot", "sentry", "linear"
    ExternalID    string
    Title         string
    Description   string
    FilesAffected []string        // files referenced in the issue/error
    Score         float64         // 0-100 quick-start suitability score
    Rationale     string          // human-readable explanation of why this is a good first fix
    EstComplexity string          // "trivial", "simple", "moderate" — only surface easy ones
}
```

Scoring weights:

| Factor | Weight | Logic |
|--------|--------|-------|
| Estimated simplicity | 0.40 | Prefer trivial/simple issues. Skip anything estimated as moderate+. |
| File locality | 0.25 | Prefer issues touching 1-3 files over broad changes. |
| Recency | 0.15 | Prefer recently active issues (more relevant, easier to verify). |
| Signal clarity | 0.20 | Stack traces, test failures, and clear error messages score higher than vague descriptions. |

### Complexity Pre-Filter

Quick-start candidates are aggressively filtered for simplicity. The first fix must succeed — a failed first run kills confidence in the product.

```go
func (s *QuickStartService) FilterCandidates(ctx context.Context, candidates []QuickStartCandidate) []QuickStartCandidate {
    var filtered []QuickStartCandidate
    for _, c := range candidates {
        // Only surface issues likely to succeed on first try
        if c.EstComplexity == "trivial" || c.EstComplexity == "simple" {
            filtered = append(filtered, c)
        }
    }
    // Return top 5, sorted by score descending
    sort.Slice(filtered, func(i, j int) bool {
        return filtered[i].Score > filtered[j].Score
    })
    if len(filtered) > 5 {
        filtered = filtered[:5]
    }
    return filtered
}
```

### LLM-Based Candidate Evaluation

For the top candidates from the initial scan, a lightweight LLM call evaluates feasibility. This is a single batch call, not per-candidate.

```go
func (s *QuickStartService) EvaluateCandidates(ctx context.Context, repo *models.Repository, candidates []QuickStartCandidate) ([]QuickStartCandidate, error) {
    prompt := fmt.Sprintf(`You are evaluating issues for a first-time user of an AI code fixing tool.
The user just connected the repository %s. We need to find ONE issue that:
1. Is very likely fixable by an AI agent (simple bug fix, clear error, well-scoped)
2. Will produce a small, clean diff (1-3 files, <50 lines changed)
3. Has clear acceptance criteria (test exists, or error message is obvious)

For each candidate, output:
- complexity: "trivial", "simple", or "skip"
- confidence: 0.0-1.0 that an agent can fix this
- rationale: one sentence explaining why this is or isn't a good first fix

Candidates:
%s`, repo.FullName, formatCandidates(candidates))

    // Use a fast, cheap model (Haiku-class) for this evaluation
    // ...
}
```

### No Candidates Found

If the scan finds zero suitable candidates (new repo, no open bugs, no CI failures), the system offers alternatives:

1. **"Try a sample fix"** — The system picks a safe, low-risk improvement it can make to the codebase (add missing error handling, fix a linter warning, improve a log message). This demonstrates the agent's capability without requiring a real issue.
2. **"Connect Sentry for real issues"** — Prompt to connect Sentry, which usually has a backlog of unresolved errors.
3. **"Create a test issue"** — Link to create a GitHub Issue that the user knows how to verify.

```go
type QuickStartFallback struct {
    Type        string // "sample_fix", "connect_integration", "create_issue"
    Title       string
    Description string
    ActionURL   string // deep link to the appropriate page
}

func (s *QuickStartService) GetFallbacks(ctx context.Context, repo *models.Repository) []QuickStartFallback {
    return []QuickStartFallback{
        {
            Type:        "sample_fix",
            Title:       "Try a sample fix",
            Description: "We'll find a small improvement to demonstrate how 143.dev works.",
            ActionURL:   fmt.Sprintf("/repos/%s/quick-start/sample", repo.ID),
        },
        {
            Type:        "connect_integration",
            Title:       "Connect Sentry to find real bugs",
            Description: "Sentry errors are the highest-signal source for automated fixes.",
            ActionURL:   "/settings/integrations",
        },
    }
}
```

## Onboarding Checklist

A persistent checklist component shown on the dashboard until completed. Tracks progress across sessions.

### Data Model

```sql
CREATE TABLE onboarding_progress (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id         uuid NOT NULL REFERENCES organizations(id),
    user_id        uuid NOT NULL REFERENCES users(id),
    steps          jsonb NOT NULL DEFAULT '{}',
    completed_at   timestamptz,              -- set when all steps done
    dismissed_at   timestamptz,              -- set if user dismisses
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_onboarding_progress_user ON onboarding_progress (org_id, user_id);
```

The `steps` JSONB tracks individual step completion:

```json
{
  "github_connected": { "completed_at": "2025-01-15T10:00:00Z" },
  "repo_connected":   { "completed_at": "2025-01-15T10:01:00Z", "repo_id": "abc-123" },
  "context_built":    { "completed_at": "2025-01-15T10:05:00Z" },
  "first_run":        { "completed_at": "2025-01-15T10:08:00Z", "run_id": "def-456" },
  "first_pr":         { "completed_at": "2025-01-15T10:09:00Z", "pr_url": "https://github.com/..." },
  "sentry_connected": null,
  "linear_connected": null
}
```

### Steps

| Step | Required | Trigger | What Happens |
|------|----------|---------|--------------|
| `github_connected` | Yes | OAuth callback completes | Auto-completed on sign-in |
| `repo_connected` | Yes | First repo added via GitHub App | Shows repo list with context build status |
| `context_built` | Yes | Background job completes | Progress bar shown during wait |
| `first_run` | Yes | User clicks "Fix this" on any issue | Live log viewer opens |
| `first_pr` | Yes | Agent run succeeds, PR opens | PR link shown with celebration state |
| `sentry_connected` | No | Sentry integration configured | Prompted after first PR ships |
| `linear_connected` | No | Linear integration configured | Prompted after first PR ships |

The first five steps are the critical path. Sentry and Linear are prompted after the first PR ships — when the user is already bought in and wants more.

### UI Component

The checklist renders as a sidebar card on the dashboard:

```
┌──────────────────────────────────────┐
│  Get started with 143.dev            │
│                                      │
│  ✅  Sign in with GitHub             │
│  ✅  Connect a repository            │
│  ⏳  Building codebase context...    │
│      ████████░░░░░ 65%               │
│  ○   Fix your first issue            │
│  ○   Ship your first PR              │
│                                      │
│  ─────────────────────────────       │
│  Optional:                           │
│  ○  Connect Sentry                   │
│  ○  Connect Linear                   │
│                                      │
│                        [Dismiss]     │
└──────────────────────────────────────┘
```

After the `context_built` step completes (or reaches a usable threshold — see below), the "Fix your first issue" step becomes actionable and shows the quick-start candidates.

### Partial Context: Don't Block on Full Build

Full context building can take several minutes for large repos. The user shouldn't wait.

The system defines a **minimum viable context** threshold: enough to attempt a simple fix.

```go
const (
    // Minimum context quality to attempt a quick-start run.
    // Full context build targets 100, but a basic scan (architecture docs,
    // primary language detection, test command detection) gets to ~25 fast.
    MinQuickStartContextQuality = 25.0
)

func (s *QuickStartService) IsReadyForFirstRun(ctx context.Context, repoID uuid.UUID) (bool, float64, error) {
    repo, err := s.db.GetRepository(ctx, repoID)
    if err != nil {
        return false, 0, err
    }
    if repo.ContextQuality == nil {
        return false, 0, nil
    }
    return *repo.ContextQuality >= MinQuickStartContextQuality, *repo.ContextQuality, nil
}
```

Context build phases:

| Phase | Time | Quality Score | What's Available |
|-------|------|---------------|------------------|
| 1. Language + framework detection | ~5s | 10 | Primary language, package manager, framework |
| 2. Architecture doc scan | ~10s | 20 | CLAUDE.md, AGENTS.md, README contents |
| 3. Test command detection | ~15s | 25 | How to run tests, test framework, CI config |
| 4. File map (first pass) | ~1-2 min | 50 | Feature-to-file classification for top-level dirs |
| 5. Convention extraction | ~2-3 min | 70 | Coding conventions, linter configs |
| 6. Full file map + imports | ~3-5 min | 90 | Complete file map, dependency graph |
| 7. Quality scoring | ~5s | 100 | Final quality score computed |

The user can attempt their first fix after Phase 3 (quality ≥ 25). The agent will have less context than a fully-built repo, but for simple issues this is sufficient. The context continues building in the background and will be available for subsequent runs.

## First-Run Agent Experience

When the user clicks "Fix this" on a quick-start candidate, the experience is optimized for first impressions.

### What's Different from Normal Runs

| Aspect | Normal Run | First Run |
|--------|-----------|-----------|
| Log viewer | Collapsed by default | Expanded, front and center |
| Progress | Status badge only | Step-by-step progress with descriptions |
| On failure | Error classification + retry option | Guided recovery: "Here's what happened, try another issue?" |
| On success | PR link in run detail | Celebration state + PR preview inline + "What's next?" prompt |
| Timeout | Standard (configurable) | Slightly shorter (3 min) — better to fail fast than make user wait |

### Progress Steps Shown to User

During the first run, the UI shows clear progress with plain-language descriptions:

```
┌──────────────────────────────────────────────────┐
│  Fixing: "Fix null pointer in user API handler"  │
│                                                  │
│  ✅  Cloning repository                          │
│  ✅  Loading codebase context                    │
│  ⏳  Analyzing the issue...                      │
│      Agent is reading the stack trace and         │
│      locating the affected files.                │
│  ○   Generating fix                              │
│  ○   Running tests                               │
│  ○   Validating code quality                     │
│  ○   Opening pull request                        │
│                                                  │
│  ┌──────────────────────────────────────┐        │
│  │ > Reading internal/api/users.go...   │        │
│  │ > Found nil check missing on line 47 │        │
│  │ > Generating fix...                  │        │
│  └──────────────────────────────────────┘        │
│                Live agent log                     │
└──────────────────────────────────────────────────┘
```

### Success State

When the first run succeeds and a PR opens:

```
┌──────────────────────────────────────────────────┐
│                                                  │
│  Your first fix is ready for review!             │
│                                                  │
│  ┌──────────────────────────────────────┐        │
│  │  PR #42: Fix null pointer in user    │        │
│  │          API handler                 │        │
│  │                                      │        │
│  │  +3 lines / -1 line  ·  1 file       │        │
│  │                                      │        │
│  │  [View on GitHub]  [View Diff]       │        │
│  └──────────────────────────────────────┘        │
│                                                  │
│  What's next?                                    │
│  ┌──────────┐ ┌──────────┐ ┌──────────────┐     │
│  │ Fix      │ │ Connect  │ │ Explore      │     │
│  │ another  │ │ Sentry   │ │ settings     │     │
│  │ issue    │ │          │ │              │     │
│  └──────────┘ └──────────┘ └──────────────┘     │
│                                                  │
└──────────────────────────────────────────────────┘
```

### Failure Handling

First-run failures need special care. A failed first run can lose the user permanently.

```go
type FirstRunFailureResponse struct {
    FailureType    string   // "complexity_too_high", "test_failure", "context_insufficient", "timeout"
    UserMessage    string   // plain-language explanation
    Suggestion     string   // what to try next
    AlternativeIDs []string // other quick-start candidates to try
}

func (s *QuickStartService) HandleFirstRunFailure(ctx context.Context, run *models.AgentRun) *FirstRunFailureResponse {
    switch classifyFailure(run) {
    case "timeout":
        return &FirstRunFailureResponse{
            FailureType: "timeout",
            UserMessage: "The agent ran out of time on this issue. This usually means it's more complex than expected.",
            Suggestion:  "Try a simpler issue — we've picked some alternatives below.",
        }
    case "test_failure":
        return &FirstRunFailureResponse{
            FailureType: "test_failure",
            UserMessage: "The agent generated a fix, but it didn't pass the existing tests.",
            Suggestion:  "This can happen when tests depend on specific behavior. Try another issue, or review the agent's attempt.",
        }
    default:
        return &FirstRunFailureResponse{
            FailureType: "unknown",
            UserMessage: "The agent wasn't able to fix this one. Not every issue is automatable — that's expected.",
            Suggestion:  "Try one of these alternatives, or connect Sentry to find issues with clearer stack traces.",
        }
    }
}
```

## Post-First-Fix Prompts

After the first PR ships, the system prompts for deeper setup:

### Integration Upsell (Non-Blocking)

```
┌──────────────────────────────────────────────────┐
│  Nice — your first fix shipped!                  │
│                                                  │
│  Want 143.dev to find issues automatically?      │
│  Connect your error and issue tracking tools     │
│  and fixes will be suggested as issues come in.  │
│                                                  │
│  ┌──────────┐ ┌──────────┐                       │
│  │ Connect  │ │ Connect  │  [Maybe later]        │
│  │ Sentry   │ │ Linear   │                       │
│  └──────────┘ └──────────┘                       │
└──────────────────────────────────────────────────┘
```

### Autonomy Level Prompt

After 3 successful fixes (not on the first), prompt the user to configure autonomy:

```
┌──────────────────────────────────────────────────┐
│  143.dev has shipped 3 fixes so far.             │
│                                                  │
│  Want to automate more?                          │
│                                                  │
│  ○ Manual only — I click "Fix" each time         │
│  ○ Auto-fix simple bugs — fix trivial/simple     │
│    issues automatically, I review the PR         │
│  ○ Fix everything — attempt all issues,          │
│    I review the PRs                              │
│                                                  │
│            [Save preference]                     │
└──────────────────────────────────────────────────┘
```

## API Endpoints

```
/api/v1/
├── /onboarding
│   ├── GET    /progress              # get current user's onboarding state
│   ├── PATCH  /progress              # update step completion
│   └── POST   /dismiss               # dismiss the checklist
│
├── /quick-start
│   ├── GET    /repos/:id/candidates  # get quick-start candidates for a repo
│   ├── POST   /repos/:id/evaluate    # trigger LLM evaluation of candidates
│   ├── POST   /repos/:id/sample-fix  # trigger a sample fix (fallback)
│   └── GET    /repos/:id/status      # context build progress + readiness
```

## Background Jobs

| Job | Trigger | Purpose |
|-----|---------|---------|
| `quick_start_scan` | Repo connected | Scan GitHub API for fixable issues |
| `quick_start_evaluate` | Scan completes | LLM evaluation of top candidates |
| `build_repo_context` | Repo connected (existing) | Build full context package |

The `quick_start_scan` job is enqueued at the same time as `build_repo_context` but runs faster (~30s vs ~1-5 min) because it only queries the GitHub API — no cloning or LLM analysis.

## Metrics to Track

| Metric | Definition | Target |
|--------|------------|--------|
| Time to first fix attempt | Sign-up → user clicks "Fix this" | < 5 minutes |
| First fix success rate | % of first runs that produce a merged PR | > 60% |
| Onboarding completion rate | % of users who complete all required steps | > 70% |
| Day-1 retention | % of users who return the day after sign-up | > 40% |
| Setup-to-value | Sign-up → first PR opened | < 10 minutes |

## Connection with Other Design Docs

**Repository Onboarding (doc 13)**:
- Quick-start scan runs immediately after the `installation_repositories` webhook handler creates repo records
- Uses the same `GitHubTokenManager` for API calls

**Codebase Context (doc 14)**:
- Context build runs in parallel with quick-start scan
- Minimum viable context threshold (quality ≥ 25) gates when the user can trigger a first run
- Context build phases are exposed via the onboarding progress UI

**Agent Orchestrator (doc 06)**:
- First runs use the same orchestrator with slightly shorter timeout (3 min vs default 5 min)
- First-run failures use a specialized failure handler that prioritizes user-friendly messaging

**Smart Routing (doc 12)**:
- Quick-start candidates are pre-filtered to trivial/simple complexity only
- This is more conservative than normal routing to maximize first-fix success rate

**Database Schema (doc 01)**:
- New `onboarding_progress` table

# Design: Time to First Fix

> **Status:** Not Started | **Last reviewed:** 2026-04-21

The most critical moment in the 143.dev experience is: **user installs, connects a repo, and sees their first successful fix**. Every minute this takes, a percentage of users are lost. This document designs the path from sign-up to "wow" as short as possible.

## The Goal

A new user should see a real, validated code fix within **15 minutes** of signing up. Not a demo. A real fix to a real bug in their real codebase.

## Demo Mode: Show Before You Ask

Before the user connects their own repo, show them what 143 does. A pre-built demo walkthrough using a sample repo:

1. **Landing page**: "Watch 143 fix a real Sentry error in 90 seconds" with an embedded replay of an actual agent run (abridged).
2. **Try it yourself**: A sandbox environment with a sample repo and a pre-seeded Sentry error. The user clicks "Fix this," watches the agent run, sees the PR. No sign-up required.
3. **Now connect your own repo**: After the demo, the user is primed to connect their own code.

### Demo Repo

Maintain a public sample repo (`github.com/143dev/sample-app`) with:

- A small Go or Node.js application
- 3-5 intentionally planted bugs with corresponding Sentry error payloads
- Pre-built codebase context package
- Known-good agent fixes for each bug (used to verify the demo works)

The demo environment uses the same sandbox infrastructure as production runs, but with pre-seeded issue data and a cloned sample repo.

## Quick-Start Issue Scan

After the user connects their first repo and Sentry integration, the system immediately scans for "quick wins" — issues that are highly likely to be fixable:

### Scan Criteria

```go
type QuickWinCandidate struct {
    Issue           *models.Issue
    FixLikelihood   float64  // 0-1, estimated probability of a successful fix
    EstimatedTime   string   // "~2 minutes", "~5 minutes"
    Reason          string   // why this is a good first fix
}

func (s *FirstRunService) FindQuickWins(ctx context.Context, repoID uuid.UUID) ([]QuickWinCandidate, error) {
    // 1. Fetch recent Sentry errors for this repo
    issues, _ := s.db.GetRecentIssues(ctx, repoID, "sentry", 50)

    // 2. Score each for quick-win potential
    var candidates []QuickWinCandidate
    for _, issue := range issues {
        score := s.scoreQuickWin(issue)
        if score > 0.7 {
            candidates = append(candidates, QuickWinCandidate{
                Issue:         issue,
                FixLikelihood: score,
                EstimatedTime: s.estimateTime(score),
                Reason:        s.explainWhy(issue, score),
            })
        }
    }

    // 3. Return top 5 sorted by fix likelihood
    sort.Slice(candidates, func(i, j int) bool {
        return candidates[i].FixLikelihood > candidates[j].FixLikelihood
    })
    if len(candidates) > 5 {
        candidates = candidates[:5]
    }
    return candidates, nil
}
```

### Quick-Win Scoring

Issues score higher when they have:

| Signal | Weight | Rationale |
|--------|--------|-----------|
| Clear stack trace with file/line | 0.3 | Agent can pinpoint the code |
| Single file involved | 0.2 | Simpler fix scope |
| Error is a null/nil dereference, type error, or missing check | 0.2 | These are the most reliably fixable bug types |
| File has existing tests | 0.15 | Agent can verify its fix |
| Low occurrence count (< 100) | 0.05 | Less risk if the fix is wrong |
| Recent (last 7 days) | 0.1 | Relevant to the user right now |

### Quick-Win UI

After repo connection, instead of an empty dashboard, the user sees:

```
+----------------------------------------------------------+
|  We found 3 issues that look easy to fix                 |
|                                                          |
|  1. NullPointerError in api/users.go:142                 |
|     12 occurrences this week, affects 8 users            |
|     Estimated fix time: ~2 minutes                       |
|     [Fix This]                                           |
|                                                          |
|  2. IndexOutOfBoundsError in pkg/parser.go:87            |
|     5 occurrences this week, affects 3 users             |
|     Estimated fix time: ~3 minutes                       |
|     [Fix This]                                           |
|                                                          |
|  3. TypeError in handlers/webhook.go:203                 |
|     45 occurrences this week, affects 22 users           |
|     Estimated fix time: ~4 minutes                       |
|     [Fix This]                                           |
+----------------------------------------------------------+
```

The user clicks "Fix This" on whichever looks most familiar, and immediately enters the agent run experience.

## Progress Communication: The Waiting Experience

The agent takes 2-5 minutes. This is the most emotionally fragile moment. The user needs to feel that something meaningful is happening, not that they're watching a loading spinner.

### Phase-Based Progress

Instead of raw log streaming (which is overwhelming for a first-time user), show a simplified phase view:

```
+----------------------------------------------------------+
|  Fixing: NullPointerError in api/users.go:142            |
|                                                          |
|  [============================--------]  75%             |
|                                                          |
|  [done]  Understanding the codebase         12s          |
|          Read 8 files, identified root cause              |
|                                                          |
|  [done]  Analyzing the bug                   8s          |
|          Null check missing on user.Profile               |
|                                                          |
|  [>>>>]  Writing the fix...                  --           |
|          Editing api/users.go                             |
|                                                          |
|  [ -- ]  Running tests                                   |
|  [ -- ]  Validating the fix                              |
+----------------------------------------------------------+
```

### Design Principles for Progress UI

1. **Phase names are human-readable**, not technical ("Understanding the codebase" not "context_gathering")
2. **Each phase has a one-line summary** when complete ("Read 8 files, identified root cause")
3. **Elapsed time per phase** gives a sense of progress
4. **Progress bar** is estimated (based on historical run times for similar complexity)
5. **Expandable detail**: clicking a completed phase shows the raw logs for power users
6. **No wall of text**: the detailed log stream is available via a "Show detailed logs" toggle, but hidden by default for first-time users

### Completion Celebration

When the fix is done and validated:

```
+----------------------------------------------------------+
|  Fix ready!                                              |
|                                                          |
|  Root cause: Missing null check on user.Profile          |
|  when the user hasn't completed onboarding.              |
|                                                          |
|  Changes: api/users.go (+3 lines, -1 line)               |
|  Tests: Added regression test in api/users_test.go       |
|  Validation: All 6 checks passed                         |
|                                                          |
|  [View Diff]  [Open PR]                                  |
|                                                          |
|  This fix would have prevented 12 errors affecting       |
|  8 users this week.                                      |
+----------------------------------------------------------+
```

Key elements:
- **Root cause in plain English** (not just "fixed the bug")
- **Minimal diff summary** (files changed, lines added/removed)
- **Impact statement** ("would have prevented X errors affecting Y users")
- **Clear next action** ("Open PR" button)

## Onboarding Funnel

### Steps and Expected Timing

| Step | Expected Time | Blocker Risk |
|------|--------------|--------------|
| 1. Sign in with GitHub | 30s | Low |
| 2. Install GitHub App | 60s | Medium (org admin approval) |
| 3. Connect first repo | 15s | Low |
| 4. Connect Sentry | 60s | Medium (needs Sentry API token) |
| 5. Quick-win scan | 10s (automatic) | Low |
| 6. Click "Fix This" | 5s | Low |
| 7. Watch agent run | 2-5 min | Medium (agent might fail) |
| 8. See PR | 10s | Low |
| **Total** | **~8 minutes** | |

### Handling Blockers

- **Step 2 (GitHub App install)**: If the user isn't an org admin, show "You'll need an org admin to approve this. Here's a link to send them." Don't dead-end.
- **Step 4 (Sentry connection)**: Make this optional for the first run. If no Sentry, the system can still scan for open Linear issues or let the user paste a stack trace manually.
- **Step 7 (Agent failure)**: If the first fix attempt fails, automatically try the next quick-win candidate. Don't show the user a failure on their very first experience. If all candidates fail, show the failure gracefully (see [17-failure-communication.md](../implemented/17-failure-communication.md)).

## Data Model

No new tables. Uses existing tables:

- `issues` — quick-win candidates come from here
- `agent_runs` — the first fix is a normal agent run
- `organizations.settings` — tracks onboarding state

New field in `organizations.settings`:

```json
{
  "onboarding": {
    "status": "completed",
    "first_fix_run_id": "uuid",
    "first_fix_completed_at": "2026-01-15T10:30:00Z",
    "quick_win_scan_completed": true,
    "demo_completed": false
  }
}
```

## Build Order

This is part of **Phase 1** (Foundation). The time-to-first-fix experience should be built alongside the initial scaffold, not as an afterthought:

1. **Demo repo + demo mode** — sample repo with planted bugs, embedded demo walkthrough
2. **Quick-win scan** — scoring function, scan trigger after first Sentry connection
3. **Quick-win UI** — the "we found 3 issues" card on first login
4. **Phase-based progress view** — simplified progress UI for agent runs (replaces raw logs as default for new users)
5. **Completion celebration** — root cause summary, impact statement, PR button

## Connection with Other Design Docs

**Repository Onboarding (doc 13)**: The quick-win scan triggers immediately after repo + Sentry connection.

**Agent Orchestrator (doc 06)**: First runs use the same orchestrator. No special handling needed.

**Codebase Context (doc 14)**: For the first fix, context building runs in parallel with the quick-win scan. If context isn't ready when the user clicks "Fix This," the agent runs with whatever context is available (may reduce fix quality, but speed matters more for the first experience).

**Frontend (doc 03)**: The progress phase view becomes the default run detail view for all users (not just first-timers). Power users can toggle to raw logs.

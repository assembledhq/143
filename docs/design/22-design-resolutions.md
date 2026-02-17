# Design Resolutions: Cross-Document Clarifications

This document resolves conflicts, ambiguities, and gaps identified during design review. Each resolution includes the specific change to make in the referenced doc(s). Engineers should treat this document as authoritative where it contradicts earlier docs — those docs should be updated to match.

---

## Resolution 1: Confidence vs. Aggressiveness Decision Flowchart

**Docs affected**: 05-prioritization.md, 06-agent-orchestrator.md, 12-smart-routing.md

**Resolution**: Aggressiveness and confidence are **sequential gates**, not independent controls. Aggressiveness gates **before** the run (can we attempt it?). Confidence gates **after** the run (what do we do with the result?).

### Decision Flowchart

```
Issue eligible for agent run
        │
        ▼
┌─────────────────────────────────┐
│  GATE 1: Aggressiveness Check   │
│  (pre-run, doc 12)              │
│                                 │
│  issue.complexity_tier          │
│    <= max_tier_for_level?       │
│                                 │
│  Per-issue-type overrides are   │
│  CAPPED by global aggressiveness│
│  (see Resolution 7)            │
└───────────┬─────────────────────┘
            │
         ┌──┴──┐
        yes    no ──▶ skip (auto) or warn (manual)
         │
         ▼
┌─────────────────────────────────┐
│  Budget gate (doc 17)           │
│  Token budget not exhausted?    │
└───────────┬─────────────────────┘
            │
         ┌──┴──┐
        yes    no ──▶ skip (auto) or warn (manual)
         │
         ▼
┌─────────────────────────────────┐
│  EXECUTE: Agent runs in sandbox │
│  (doc 06)                       │
└───────────┬─────────────────────┘
            │
            ▼
┌─────────────────────────────────┐
│  GATE 2: Confidence Check       │
│  (post-run, doc 12)             │
│                                 │
│  score >= auto_proceed (0.8):   │
│    → proceed to validation      │
│                                 │
│  score >= human_review (0.5):   │
│    → proceed, flag for review   │
│                                 │
│  score < human_review (0.5):    │
│    → pause, needs_human_guidance│
└─────────────────────────────────┘
```

**Key rule**: A high confidence score does NOT let a run bypass the aggressiveness gate. Aggressiveness controls which tiers are *attempted*. Confidence controls what happens with the *result*. They are never in tension — they operate at different lifecycle stages.

**Add to doc 12**, section "How Aggressiveness Interacts with Autonomy":

> **Relationship to confidence scoring**: Aggressiveness is a pre-run gate; confidence is a post-run gate. They operate sequentially, never interact. A tier-4 issue blocked by aggressiveness level 2 will never run, regardless of what confidence score it might produce. Conversely, a tier-1 issue that passes the aggressiveness gate can still be paused by a low confidence score after execution.

---

## Resolution 2: Mid-Run Escalation Mechanism

**Docs affected**: overall.md, 06-agent-orchestrator.md, 18-interactive-sessions.md

**Resolution**: Mid-run escalation from batch to guided is a **post-run escalation**, not a true mid-run mode switch. The overall.md language is aspirational but the implementation is post-run.

### Mechanism

When a batch run completes with a low confidence score (below `guided_escalation_threshold`, default 0.5):

1. The run is marked `needs_human_guidance` (existing behavior)
2. If `auto_escalate_to_guided` is enabled in org settings, the system **automatically creates a guided session** using the same issue, with the failed run's context injected
3. The human is notified via UI/Slack: "Agent attempted this issue but isn't confident. A guided session has been started for you."
4. The guided session's sandbox is pre-loaded with the repo and the agent's partial findings from the batch run

This is NOT a mid-run mode switch. The batch run completes (or fails), and a new guided session is created.

**Update doc 06**, section "Interactive Execution Modes", replace the paragraph about `auto_escalate_to_guided`:

```go
// Post-run escalation to guided mode.
// This is NOT a mid-run mode switch — the batch run completes first,
// then a new guided session is created with the batch run's context.
func (o *Orchestrator) maybeEscalateToGuided(ctx context.Context, run *models.AgentRun, result *AgentResult) error {
    settings, _ := o.db.GetOrgSettings(ctx, run.OrgID)
    if !settings.InteractiveSessions.AutoEscalateToGuided {
        return nil
    }
    if result.ConfidenceScore >= settings.InteractiveSessions.GuidedEscalationThreshold {
        return nil
    }

    // Create a guided session with the batch run's context
    session, err := o.sessionManager.CreateSession(ctx, CreateSessionRequest{
        IssueID:           run.IssueID,
        OrgID:             run.OrgID,
        UserID:            o.getOrgAdminID(ctx, run.OrgID),
        Mode:              "guided",
        AgentType:         run.AgentType,
        EscalatedFromRun:  &run.ID,
        PriorContext:      result.Summary,
    })
    if err != nil {
        return err
    }

    o.notify.AdminAlert(ctx, run.OrgID, "guided_session_escalated", map[string]interface{}{
        "issue_id":   run.IssueID,
        "session_id": session.ID,
        "reason":     "Low confidence on batch run",
    })
    return nil
}
```

**Update overall.md**, step 4, replace "auto-escalate from batch when agent detects ambiguity" with:

> When a batch run completes with low confidence, the system can automatically create a guided session for human collaboration (see doc 06, post-run escalation).

---

## Resolution 3: Token Accounting Breakdown

**Docs affected**: 01-database-schema.md, 06-agent-orchestrator.md, 07-validation.md, 17-cost-intelligence.md, 19-test-health.md

**Resolution**: All LLM token usage is tracked with explicit phase attribution. The `cost_summaries` table gets a `token_breakdown` field.

### Schema Change

**Add to `cost_summaries` table (doc 01)**:

| Column | Type | Notes |
|--------|------|-------|
| token_breakdown | jsonb | Per-phase token accounting |

```json
{
  "agent":      { "input": 10000, "output": 3000 },
  "validation": { "input": 4000,  "output": 1200 },
  "test_gen":   { "input": 3000,  "output": 800 },
  "complexity": { "input": 500,   "output": 200 },
  "context":    { "input": 2000,  "output": 600 }
}
```

### Where Each Phase's Tokens Are Captured

| Phase | Source | Stored In | Rolled Up By |
|-------|--------|-----------|--------------|
| `agent` | Agent adapter output | `agent_runs.token_usage` | `compute_cost_summary` job |
| `validation` | Validation LLM calls (direction, correctness, quality, regression checks) | `validations.details` → add `token_usage` key | `compute_cost_summary` job |
| `test_gen` | Proactive test generation (doc 19 two-phase prompt) | `agent_runs.token_usage` (same run, test_gen_phase) | `compute_cost_summary` job |
| `complexity` | Complexity estimation LLM call | `complexity_estimates` → add `tokens_used` column | `compute_cost_summary` job |
| `context` | File classification during context build | `repo_context_packages.build_metadata` → add `tokens_used` key | Tracked separately, not per-fix |

**Add to doc 07 (validation)**: Each LLM-based validation check (direction, correctness, quality, regression test) must record its token usage in the `validations.details` JSONB under a `token_usage` key:

```json
{
  "direction_check": { "result": "pass", "token_usage": { "input": 800, "output": 300 } },
  "correctness_check": { "result": "pass", "token_usage": { "input": 1200, "output": 400 } },
  ...
}
```

**Add to doc 05 (prioritization)**: The direction alignment LLM call must use a Haiku-class model (same as complexity estimation) and track tokens. Add a `tokens_used` column to `priority_scores`.

---

## Resolution 4: Test Generation Orchestrator Integration

**Docs affected**: 06-agent-orchestrator.md, 19-test-health.md

**Resolution**: Test generation is an explicit step in the orchestrator workflow, between sandbox setup and agent execution.

### Updated Orchestrator Flow

**Add to doc 06**, `RunAgent` function, between steps 8 (clone repo) and 9 (execute agent):

```go
    // 8.5. Proactive test generation (if coverage is low)
    // See 19-test-health.md for the two-phase prompt design.
    if settings.TestHealth.ProactiveTestGenEnabled {
        coverage, _ := o.db.GetFileCoverage(ctx, run.RepositoryID, estimate.EstimatedFiles)
        if coverage.AveragePct < settings.TestHealth.CoverageThreshold { // default: 30%
            o.db.UpdateAgentRun(ctx, run.ID, map[string]interface{}{
                "test_gen_phase": "generating",
            })

            testGenResult, err := adapter.GenerateTests(ctx, sandbox, &TestGenInput{
                Issue:          issue,
                AffectedFiles:  estimate.EstimatedFiles,
                CoverageData:   coverage,
            })

            if err != nil || !testGenResult.TestsCompile {
                // Test gen failed — proceed without tests. Don't block the fix.
                o.db.UpdateAgentRun(ctx, run.ID, map[string]interface{}{
                    "test_gen_phase": "failed",
                })
                log.Warn().Err(err).Msg("proactive test generation failed, proceeding without")
            } else {
                o.db.UpdateAgentRun(ctx, run.ID, map[string]interface{}{
                    "test_gen_phase": "completed",
                })
                // Tests are committed in the sandbox — agent will see them
            }
        }
    }

    // 9. Execute agent with log streaming (existing step)
```

### State Transitions for `test_gen_phase`

```
none ──▶ generating ──▶ completed
                   └──▶ failed
```

- `none`: No test generation attempted (coverage above threshold or feature disabled)
- `generating`: Test generation in progress
- `completed`: Tests generated and committed in sandbox; agent will incorporate them
- `failed`: Test generation failed; agent proceeds without (logged as warning, not blocking)

**Key design decision**: Failed test generation does NOT block the fix run. The agent proceeds without the generated tests. This avoids a single failure mode (test gen) blocking the entire pipeline.

---

## Resolution 5: Prompt Versioning Strategy Clarification

**Docs affected**: 01-database-schema.md, 16-ai-agent-evals.md

**Resolution**: `prompt_versions` and `prompt_overrides` intentionally use different patterns because they serve different purposes. This is correct and should be documented explicitly.

### Why They Differ

| Table | Pattern | Reason |
|-------|---------|--------|
| `prompt_versions` | `state` enum (draft/candidate/active/archived) | Lifecycle entity with a promotion workflow. Drafts need to be edited in-place. Candidates need eval runs before promotion. This is inherently stateful. |
| `prompt_overrides` | Insert-only with `active` boolean | Pointer table: "which version is active for this scope?" Changes must be auditable with full history. No in-place edits — new pointer = new row. |

**Add to doc 01**, after the `prompt_overrides` table definition:

> **Why different patterns**: `prompt_versions` uses a `state` column because it's a lifecycle entity — drafts are edited, candidates are tested, then promoted. `prompt_overrides` uses insert-only versioning because it's a pointer table mapping scopes to versions — every change to "which version is active" must be preserved in history. These tables work together: `prompt_versions` manages content lifecycle, `prompt_overrides` manages routing.

### Relationship to `tuning_config_versions` (doc 21)

Prompts are NOT managed by `tuning_config_versions`. The prompt system (doc 16) has its own versioning via `prompt_versions` + `prompt_overrides`. The tuning system (doc 21) manages agent configuration, complexity calibration, conventions, and context packages — but not prompts.

**Add to doc 21**, after the `tuning_config_versions` table definition:

> **Scope boundary**: Prompts are versioned separately via `prompt_versions` + `prompt_overrides` (doc 16). The `tuning_config_versions` table does NOT include a `prompts` scope. This is intentional — prompts have a promotion lifecycle (draft → candidate → active) with eval gates that doesn't fit the insert-only pattern. If an auto-closing loop (e.g., Loop A) wants to modify prompts, it must go through the prompt promotion workflow (create draft → run evals → promote).

---

## Resolution 6: Revision Run Triggering Conditions

**Docs affected**: 06-agent-orchestrator.md, 08-pr-and-ship.md, 11-review-feedback-loop.md

**Resolution**: Revision runs are triggered by reviewer feedback on 143-generated PRs, using a specific flow documented below.

### Trigger Flow

```
GitHub sends `pull_request_review` webhook
  with action = "submitted" and review.state = "changes_requested"
        │
        ▼
Is this a 143-generated PR?  ──no──▶  ignore (or process for patterns if all_prs mode)
        │
       yes
        │
        ▼
Has this PR hit max_revisions?  ──yes──▶  notify admin "max revisions reached"
        │
       no
        │
        ▼
Classify review comments (stages 4-5 of filtering pipeline, skip merge-gate)
        │
        ▼
Any actionable comments?  ──no──▶  notify admin, done
        │
       yes
        │
        ▼
Check auto_apply setting:
  - "off"    → notify admin, done
  - "prompt" → notify admin, wait for approval
  - "auto"   → check reviewer trust tier:
                  - maintainer/contributor → proceed
                  - external → require admin approval
        │
        ▼
Create revision run:
  - new agent_run with parent_run_id = original run
  - revision_context = {
      formatted_feedback: sanitized comment summaries (ALL actionable comments, aggregated),
      previous_diff: parent run's diff,
      comment_summary: one-line summary for commit message
    }
  - Goes through normal validation pipeline
  - On success, pushes commits to existing PR branch (not new PR)
```

### Key Design Decisions

1. **Aggregated, not per-comment**: All actionable comments from the review are bundled into a single revision run. We don't create one run per comment.
2. **Only `changes_requested` reviews**: Simple approval or comment-only reviews don't trigger revisions.
3. **Sanitization mandatory**: Review comment text is sanitized via `SanitizeReviewComment()` (doc 20) before injection into the revision prompt.
4. **Same validation pipeline**: Revision runs go through the full validation pipeline (direction, correctness, quality, security, CI). No shortcuts.

---

## Resolution 7: Aggressiveness Override Precedence

**Docs affected**: 12-smart-routing.md

**Resolution**: Per-issue-type overrides are **capped** by the global aggressiveness level. They can only make things more restrictive, not less.

### Rule

```
effective_max_tier = min(
    global_aggressiveness_max_tier,
    issue_type_override_max_tier  // if set, otherwise global
)
```

**Example**: If global aggressiveness = 2 (max tier 3) and the security issue type override says max tier 4, the effective max for security issues is still tier 3. The override is capped.

However, overrides CAN make things more restrictive: if the performance issue type override says max tier 2, then performance issues are limited to tier 2 even though global allows tier 3.

**Add to doc 12**, section "Per-Issue-Type Overrides (Advanced)":

> **Precedence rule**: Per-issue-type overrides are capped by the global aggressiveness level. An override can restrict a type to a lower tier than the global setting, but it cannot raise the ceiling above the global setting. If an admin wants to attempt tier-4 security issues, they must set the global aggressiveness to at least level 3 (aggressive).

---

## Resolution 8: Installation Token Caching

**Docs affected**: 08-pr-and-ship.md, 13-repository-onboarding.md

**Resolution**: All docs must use the 5-minute buffer approach from doc 13. Token is refreshed when it expires within 5 minutes.

### Canonical Implementation

```go
func (tm *GitHubTokenManager) GetInstallationToken(ctx context.Context, installationID int64) (string, error) {
    tm.mu.RLock()
    cached, ok := tm.cache[installationID]
    tm.mu.RUnlock()

    // Refresh if missing or expires within 5 minutes
    if ok && time.Until(cached.ExpiresAt) > 5*time.Minute {
        return cached.Token, nil
    }

    // Generate new token
    token, expiresAt, err := tm.createInstallationToken(ctx, installationID)
    if err != nil {
        return "", err
    }

    tm.mu.Lock()
    tm.cache[installationID] = &cachedToken{Token: token, ExpiresAt: expiresAt}
    tm.mu.Unlock()

    return token, nil
}
```

**Update doc 08**, line ~40: Change "cache token until expiry" to "cache token until 5 minutes before expiry (see doc 13 for canonical implementation)".

---

## Resolution 9: Deploy Detection Fallback Chain

**Docs affected**: 08-pr-and-ship.md, 09-observability.md

**Resolution**: Deploy detection uses an ordered fallback chain with explicit timeouts.

### Fallback Chain

```
PR merged
    │
    ▼
Method 1: GitHub Deployments API (default)
  Listen for `deployment_status` webhook with state = "success"
  Timeout: 30 minutes after merge
    │
    ├── received ──▶ create deploy record, start experiment
    │
    └── timed out ──▶ fall through
                        │
                        ▼
Method 2: Merge-based assumption
  If no deployment API is configured or method 1 timed out:
  Assume deploy happened 10 minutes after merge to default branch
    │
    ▼
  Create deploy record with deployed_at = merged_at + 10 min
  Start experiment with this assumed deploy time
```

### Custom webhook (optional override)

If `deploy_detection_method = "custom_webhook"` is set in org settings, Methods 1 and 2 are skipped entirely. The system waits for a `POST /api/v1/webhooks/deploy` call from the org's CI/CD pipeline. Timeout: 2 hours. If no webhook arrives, the deploy is marked as "undetected" and no experiment is created.

**Add to doc 08** as a new section "Deploy Detection Fallback Chain" after the existing deploy detection section.

**Add to doc 09**: If deploy is never detected (custom webhook timeout), the experiment is created with status `skipped` and outcome `inconclusive` with reason `deploy_not_detected`.

---

## Resolution 10: Experiment Variant Assignment Determinism

**Docs affected**: 06-agent-orchestrator.md, 15-run-debugging.md

**Resolution**: Variant assignment uses a deterministic hash of issue ID + experiment ID.

### Implementation

```go
func (o *Orchestrator) assignVariant(experiment *models.AgentConfigExperiment, issueID uuid.UUID) *Variant {
    // Deterministic: same issue + same experiment = same variant, always.
    // Uses FNV hash for speed. The experiment ID is included so that
    // different experiments on the same issue can get different variants.
    h := fnv.New32a()
    h.Write(issueID[:])
    h.Write(experiment.ID[:])
    hash := h.Sum32()

    // Weighted selection based on hash
    totalWeight := 0
    for _, v := range experiment.Variants {
        totalWeight += v.Weight
    }
    bucket := hash % uint32(totalWeight)

    cumulative := 0
    for _, v := range experiment.Variants {
        cumulative += v.Weight
        if bucket < uint32(cumulative) {
            return &v
        }
    }
    return &experiment.Variants[len(experiment.Variants)-1]
}
```

**Key properties**:
- Same issue + same experiment = same variant (reproducible)
- Different experiments on the same issue can get different variants (independent)
- Changing variant weights mid-experiment changes assignment for some issues (acceptable — document this)

**Add to doc 15**, section "How Experiments Work", after "deterministic hash on issue ID":

> The hash function is `FNV-32a(issue_id || experiment_id)`. This ensures reproducibility: re-running the same issue in the same experiment always assigns the same variant. Note: editing variant weights on a running experiment will change assignment for some issues. Avoid weight changes mid-experiment for clean results.

---

## Resolution 11: Coverage Delta — Estimation vs. Actual

**Docs affected**: 07-validation.md, 19-test-health.md

**Resolution**: The validation pipeline uses **actual measured coverage** from CI for pass/fail decisions, not estimates. The "estimate" terminology in doc 07 is misleading and should be removed.

### How It Works

1. **Before agent run**: Baseline coverage is captured from the most recent `test_coverage_snapshots` entry for the repo's default branch
2. **During CI check**: CI runs with coverage enabled. The actual post-fix coverage is collected
3. **Delta computation**: `delta = post_fix_coverage - baseline_coverage`
4. **Storage**: Both baseline and post-fix values are stored in `validations.coverage_delta` JSONB

The regression test check (doc 07 step 5) verifies that a regression test *exists* for the original bug (LLM analysis). The CI check (doc 07 step 6) verifies that coverage didn't *decrease* (actual measurement).

**Update doc 07**: Replace "coverage delta estimate" with "coverage delta" throughout. Add a note:

> **Coverage measurement**: Coverage deltas are computed from actual CI measurements, not estimates. The baseline comes from the most recent `test_coverage_snapshots` entry. The post-fix measurement comes from the CI run during validation.

---

## Resolution 12: Session Timeout Enforcement

**Docs affected**: 02-api-server.md, 06-agent-orchestrator.md, 18-interactive-sessions.md

**Resolution**: Session timeout is enforced by the `SessionManager` per-session goroutine (already designed in doc 18) AND a scheduled cleanup job as a safety net.

### Per-Session Timeout (Primary)

Doc 18 already includes `watchIdleTimeout()` as a per-session goroutine (lines 710-736). This is the primary enforcement mechanism. It checks every minute and times out idle sessions.

### Scheduled Cleanup Job (Safety Net)

If the server crashes, per-session goroutines die. A scheduled job catches orphaned sessions.

**Add to doc 02**, Job Types table:

| Job Type | Trigger | Description |
|----------|---------|-------------|
| `session_timeout_check` | Scheduled (every 5 min) | Find sessions where `last_activity + idle_timeout < now()` and status is active/waiting. Mark as `timed_out`. Destroy associated sandbox. |

**Add to doc 18**, Job Queue table (already has `session_timeout_check` — confirm it's listed).

### Implementation

```go
func (w *SessionTimeoutWorker) Run(ctx context.Context) error {
    orphanedSessions, _ := w.db.GetOrphanedSessions(ctx)
    // WHERE status IN ('active', 'waiting_for_human', 'waiting_for_agent')
    // AND last_activity + idle_timeout < now()

    for _, session := range orphanedSessions {
        w.db.UpdateSessionStatus(ctx, session.ID, "timed_out")
        // Best-effort sandbox cleanup — may already be gone
        if err := w.orchestrator.CleanupSession(ctx, &session); err != nil {
            log.Warn().Err(err).Str("session_id", session.ID.String()).Msg("failed to cleanup orphaned session sandbox")
        }
    }
    return nil
}
```

---

## Resolution 13: Monorepo Context Build Cost Management

**Docs affected**: 14-codebase-context.md

**Resolution**: Repos with >5,000 source files use a sampling strategy instead of exhaustive classification.

### Sampling Strategy

```go
func (b *ContextBuilder) ClassifyFiles(ctx context.Context, repo *models.Repository, allFiles []string) error {
    sourceFiles := filterSourceFiles(allFiles) // exclude vendor, node_modules, generated, etc.

    if len(sourceFiles) <= 5000 {
        // Small/medium repos: classify everything
        return b.classifyAllFiles(ctx, repo, sourceFiles)
    }

    // Large repos: prioritize and sample
    prioritized := b.prioritizeFiles(ctx, repo, sourceFiles)
    // Priority order:
    // 1. Files changed in last 90 days (git log)
    // 2. Files referenced in existing issues
    // 3. Files matching common patterns (handlers, models, services, tests)
    // 4. Random sample of remaining files (up to 5000 total)

    return b.classifyAllFiles(ctx, repo, prioritized[:min(len(prioritized), 5000)])
}
```

### Cost Estimate

At 50 files per LLM batch call:
- 5,000 files = 100 calls (~$2 at Haiku pricing)
- 50,000 files exhaustive = 1,000 calls (~$20) — too expensive for frequent rebuilds

With sampling, even a 50k-file monorepo costs ~$2 per full rebuild.

**Add to doc 14**, after the file classification section:

> **Large repo optimization**: Repos with more than 5,000 source files use a prioritized sampling strategy. Files changed recently, referenced in issues, or matching common code patterns are classified first. Remaining files are sampled up to a 5,000-file cap. Incremental updates still classify changed files individually, so coverage grows over time.

---

## Resolution 14: Context Injection Token Budget

**Docs affected**: 14-codebase-context.md, 06-agent-orchestrator.md

**Resolution**: Context injection is capped at 8,000 tokens with a priority-based truncation strategy.

### Token Budget

```go
const MaxContextTokens = 8000

func (o *Orchestrator) AssembleContext(ctx context.Context, repo *models.Repository, issue *models.Issue) (*AgentContext, error) {
    ac := &AgentContext{}
    budget := MaxContextTokens

    // Priority 1: Architecture docs (repo-wide) — up to 2000 tokens
    archDocs := o.getArchDocs(ctx, repo)
    archTokens := min(estimateTokens(archDocs), 2000)
    ac.ArchitectureDocs = truncateToTokens(archDocs, archTokens)
    budget -= archTokens

    // Priority 2: Coding conventions — up to 1000 tokens
    conventions := o.getConventions(ctx, repo)
    convTokens := min(estimateTokens(conventions), min(1000, budget))
    ac.Conventions = truncateToTokens(conventions, convTokens)
    budget -= convTokens

    // Priority 3: Review patterns — up to 500 tokens
    patterns := o.getReviewPatterns(ctx, repo)
    patTokens := min(estimateTokens(patterns), min(500, budget))
    ac.ReviewPatterns = truncateToTokens(patterns, patTokens)
    budget -= patTokens

    // Priority 4: Targeted file map — uses remaining budget
    relevantFiles := o.GetRelevantFiles(ctx, repo, issue)
    fileMap := o.getFileMapEntries(ctx, repo, relevantFiles)
    ac.FileMap = truncateToTokens(fileMap, budget)

    return ac, nil
}
```

**Add to doc 14**, section "Injecting Context into Agent Runs":

> **Token budget**: Context injection is capped at 8,000 tokens to leave room for the issue description and agent instructions. Priorities: architecture docs (up to 2,000), conventions (up to 1,000), review patterns (up to 500), targeted file map (remaining budget). For well-documented repos that exceed 8,000 tokens, lower-priority content is truncated.

---

## Resolution 15: Sandbox Image Build and Versioning

**Docs affected**: 06-agent-orchestrator.md, 10-infrastructure.md

**Resolution**: The sandbox image is built from a Dockerfile in the repo and versioned alongside the application.

### Sandbox Image Lifecycle

```
sandbox/
├── Dockerfile          # sandbox image definition
├── install-agents.sh   # installs Claude Code, Codex CLI, Gemini CLI
└── versions.json       # pinned agent CLI versions
```

### `versions.json`

```json
{
  "claude_code": "1.0.23",
  "codex_cli": "0.4.1",
  "gemini_cli": "2.1.0"
}
```

### Build Process

```dockerfile
# sandbox/Dockerfile
FROM ubuntu:24.04

# System dependencies
RUN apt-get update && apt-get install -y git curl nodejs npm python3 pip

# Install agent CLIs at pinned versions
COPY install-agents.sh versions.json /tmp/
RUN /tmp/install-agents.sh

# Non-root user
RUN useradd -m -s /bin/bash sandbox
USER sandbox
WORKDIR /workspace
```

### Versioning Rules

1. The sandbox image is tagged `143-sandbox:<git-sha>` and `143-sandbox:latest`
2. Agent CLI versions are pinned in `versions.json` and updated via PRs (not automatic)
3. The image is rebuilt on any change to `sandbox/` directory
4. CI builds and pushes the image; production nodes pull it
5. For self-hosted deployments, `./setup.sh` builds the image locally

**Add to doc 10**, after the Dockerfiles section:

> **Sandbox image**: The sandbox image is defined in `sandbox/Dockerfile` with agent CLIs pinned to specific versions in `sandbox/versions.json`. The image is rebuilt on changes to the `sandbox/` directory and tagged with the git SHA. Self-hosted deployments build the image locally via `./setup.sh`.

---

## Resolution 16: Database HA, Backup, and Connection Pools

**Docs affected**: 10-infrastructure.md

**Resolution**: Add explicit guidance for database resilience.

### Backup Strategy

**Add to doc 10**, section "Backup & Recovery":

```
# Automated daily backups (add to cron or systemd timer)
pg_dump --format=custom --compress=9 $DATABASE_URL > /backups/143-$(date +%Y%m%d).dump

# Retention: 7 daily, 4 weekly, 3 monthly
# RTO target: 30 minutes (restore from backup)
# RPO target: 24 hours (daily backup) or near-zero (with WAL archiving)

# For near-zero RPO, enable WAL archiving:
# archive_mode = on
# archive_command = 'cp %p /wal-archive/%f'
```

### Connection Pool Sizing

| Deployment | Max Connections | Pool Size per Node | Notes |
|------------|----------------|-------------------|-------|
| Single node | 100 (Postgres default) | 25 | Leaves room for admin connections and pg_dump |
| 2-4 nodes | 200 (`max_connections`) | 25 per node | Total pool = nodes * 25 |
| 5+ nodes | 400 (`max_connections`) | 20 per node | Consider PgBouncer at this scale |

**Configuration**:
```
DATABASE_MAX_CONNS=25          # pool size per node
DATABASE_MIN_CONNS=5           # minimum idle connections
DATABASE_MAX_CONN_LIFETIME=1h  # connection recycling
```

### HA Recommendations

> **Self-hosted**: For single-node deployments, Postgres on the same machine is acceptable for low-traffic orgs. For production, use a managed Postgres service (AWS RDS, Google Cloud SQL, etc.) with automated backups, point-in-time recovery, and read replicas.
>
> **143.dev does not implement multi-master or automatic failover** — this is delegated to the managed Postgres provider. The application reconnects automatically via pgx's connection pool.

---

## Resolution 17: Worker Crash Recovery

**Docs affected**: 02-api-server.md, 10-infrastructure.md

**Resolution**: Crash recovery relies on Postgres connection close behavior + node heartbeat + dead node cleanup.

### Recovery Chain

1. **Worker crashes mid-job**: The Postgres connection closes. `FOR UPDATE` locks held by that connection are automatically released by Postgres.
2. **Node heartbeat stops**: After 2 minutes without a heartbeat, any worker-capable node marks the crashed node as `dead`.
3. **Dead node cleanup**: The cleanup routine finds jobs locked by dead nodes and re-queues them:

```go
func (h *HealthMonitor) CleanupDeadNodes(ctx context.Context) error {
    deadNodes, _ := h.db.GetDeadNodes(ctx, 2*time.Minute)

    for _, node := range deadNodes {
        // Re-queue jobs that were locked by this dead node
        count, _ := h.db.RequeueJobsLockedByNode(ctx, node.ID)
        // UPDATE jobs SET status = 'pending', locked_by_node_id = NULL, locked_at = NULL
        // WHERE locked_by_node_id = $1 AND status = 'running'

        h.db.MarkNodeDead(ctx, node.ID)
        log.Info().Str("node_id", node.ID).Int("requeued_jobs", count).Msg("cleaned up dead node")
    }
    return nil
}
```

### Job-Level Timeout

In addition to node-level recovery, each job type has a maximum execution time. Jobs running longer than their timeout are considered stuck:

```go
func (w *Worker) checkStuckJobs(ctx context.Context) error {
    // Jobs running longer than their timeout
    stuckJobs, _ := w.db.GetStuckJobs(ctx, map[string]time.Duration{
        "run_agent":    35 * time.Minute,  // max sandbox timeout + 5 min buffer
        "validate":     15 * time.Minute,
        "open_pr":      5 * time.Minute,
        "ingest_webhook": 2 * time.Minute,
        // ... other job types
    })
    for _, job := range stuckJobs {
        w.db.RequeueJob(ctx, job.ID)
    }
    return nil
}
```

**Add to doc 10**, section "Job Queue Distribution":

> **Crash recovery**: If a worker crashes, Postgres releases its `FOR UPDATE` locks on connection close. The dead node cleanup routine (runs every 30s on all worker-capable nodes) detects dead nodes after 2 minutes without heartbeat and re-queues their jobs. Additionally, a stuck job detector re-queues jobs that exceed their type-specific timeout.

---

## Resolution 18: Failure Taxonomy Mapping

**Docs affected**: 07-validation.md, 15-run-debugging.md, 19-test-health.md

**Resolution**: The three failure systems serve different purposes but should have an explicit mapping.

### Taxonomy Overview

| System | Purpose | Granularity | When |
|--------|---------|-------------|------|
| **Validation checks** (doc 07) | Gate: should this fix become a PR? | Per-check pass/fail | During validation pipeline |
| **Failure classification** (doc 15) | Diagnosis: why did the run fail? | Root cause analysis | After run completes (post-hoc) |
| **Test health issues** (doc 19) | Monitoring: is the test suite healthy? | Per-test tracking | Ongoing, cross-run |

### Mapping Table

| Validation Check Failure | Maps to Failure Classification | Test Health Issue |
|-------------------------|-------------------------------|-------------------|
| `direction_check = fail` | `direction_mismatch` | — |
| `correctness_check = fail` | `wrong_root_cause` or `incomplete_fix` | — |
| `quality_check = fail` | `overcomplicated` | — |
| `security_scan = fail` | (not classified — security failures are absolute) | — |
| `regression_test_check = fail` | `incomplete_fix` | `coverage_gap` (if systematic) |
| `ci_check = fail` (test failure) | `ci_failure` | `flaky` (if test was flaky) |
| `ci_check = fail` (lint failure) | `ci_failure` | — |

### Interaction Rules

1. **Validation failure triggers classification**: When `validations.status = 'failed'`, the `classify_failure` job is enqueued. The classifier uses the validation details as input.
2. **Flaky tests don't fail validation**: If a CI test failure matches a known `test_health_issues` entry with `issue_type = 'flaky'`, the validation check logs a warning but does NOT fail. The validation result includes `{flaky_test_warning: true, test_name: "..."}`.
3. **The analytics page (doc 03) uses failure classification codes** as the primary taxonomy. Validation check results are displayed in the run detail page, not in aggregate analytics.

**Add to doc 15**, after the Failure Categories table:

> **Relationship to validation checks**: Failure classification runs after validation. A validation failure (e.g., `ci_check = fail`) is an input to the classifier, which determines the root cause (e.g., `ci_failure` because of a flaky test vs. `incomplete_fix` because the agent broke something). The analytics dashboard aggregates by classification code, not validation check.

> **Relationship to test health**: If a CI failure is caused by a known flaky test (from `test_health_issues`), the validation pipeline logs a warning but does not fail. The classifier records `ci_failure` with a note that the test is known-flaky. This prevents flaky tests from blocking agent fixes while still tracking the signal.

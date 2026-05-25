# Design Resolutions: Cross-Document Clarifications

> **Status:** Implemented | **Last reviewed:** 2026-03-25

This document resolves conflicts, ambiguities, and gaps identified during design review. Each resolution includes the specific change to make in the referenced doc(s). Engineers should treat this document as authoritative where it contradicts earlier docs — those docs should be updated to match.

---

## Resolution 1: Aggressiveness Decision Flowchart

**Docs affected**: 05-prioritization.md, 06-agent-orchestrator.md, 12-smart-routing.md

**Resolution**: Aggressiveness is a pre-run gate that decides whether an issue should be attempted automatically. Post-run confidence gates were removed from the session lifecycle.

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
│  EXECUTE: Agent runs in sandbox │
│  (doc 06)                       │
└───────────┬─────────────────────┘
            │
            ▼
┌─────────────────────────────────┐
│  COMPLETE: Persist result       │
│  and follow validation policy   │
└─────────────────────────────────┘
```

**Key rule**: Aggressiveness controls which tiers are *attempted*. Completed coding-agent runs do not emit or gate on an LLM self-rating.

**Add to doc 12**, section "How Aggressiveness Interacts with Autonomy":

> **Relationship to execution**: Aggressiveness is a pre-run gate. A tier-4 issue blocked by aggressiveness level 2 will not run automatically.

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

### Relationship to `tuning_config_versions` (doc 23)

Prompts are NOT managed by `tuning_config_versions`. The prompt system (doc 16) has its own versioning via `prompt_versions` + `prompt_overrides`. The tuning system (doc 23) manages agent configuration, complexity calibration, conventions, and context packages — but not prompts.

**Add to doc 23**, after the `tuning_config_versions` table definition:

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
  "claude_code": "2.1.34",
  "codex_cli": "0.115.0",
  "gemini_cli": "0.34.0"
}
```

### Build Process

See `sandbox/Dockerfile` for the full implementation. Key points:
- Base image: `ubuntu:26.04`
- Node.js 24 LTS via NodeSource (required by all three CLIs)
- Uses `jq` to parse `versions.json` (no Python dependency)
- `install-agents.sh` installs all three CLIs at pinned versions via `npm install -g`
- No `build-essential` needed — none of the CLIs require native compilation

```
docker build -t 143-sandbox:latest sandbox/
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

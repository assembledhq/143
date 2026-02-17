# Design: Fix Quality Feedback Loop (Production Impact -> Agent Improvement)

The review feedback loop (doc 11) captures feedback from PR reviews. But there's a more important signal: **did the fix actually work in production?** This document closes the loop between post-deploy observability and future agent runs.

## Problem

Today the pipeline is: agent fix -> deploy -> measure impact (doc 09). The observability system classifies outcomes as `success`, `no_change`, `regression`, or `inconclusive`. But this classification is terminal — it updates a dashboard and stops. The agent that produced the fix never learns from the production outcome.

This means:
- If a fix deployed but didn't reduce errors, the agent will try the same approach on similar issues
- If a fix caused a regression, there's no mechanism to avoid the same mistake
- Production outcomes are the highest-signal feedback available, but they're unused for agent improvement

## Solution: Production Outcome Feedback Loop

```
Fix deployed
     |
     v
Observe metrics (doc 09)
     |
     v
Classify outcome
     |
     +--- success ---> Record what worked, reinforce patterns
     |
     +--- no_change --> Analyze why, generate learning
     |
     +--- regression -> Generate anti-pattern, flag approach
     |
     +--- inconclusive -> Log, no action
     |
     v
Feed learning into agent context for future runs on similar issues
```

### How It Works

After the observability system (doc 09) classifies an experiment outcome, a new job processes the result and generates learnings:

```go
func (s *FeedbackService) ProcessOutcome(ctx context.Context, experiment *models.Experiment) error {
    run, _ := s.db.GetAgentRun(ctx, experiment.AgentRunID)
    issue, _ := s.db.GetIssue(ctx, experiment.IssueID)

    switch experiment.Outcome {
    case "success":
        return s.recordSuccessPattern(ctx, run, issue, experiment)
    case "no_change":
        return s.analyzeIneffectiveFix(ctx, run, issue, experiment)
    case "regression":
        return s.recordAntiPattern(ctx, run, issue, experiment)
    case "inconclusive":
        return nil // no actionable learning
    }
    return nil
}
```

## Success: Reinforce What Worked

When a fix is deployed and the error rate drops, the system records what approach worked:

```go
func (s *FeedbackService) recordSuccessPattern(ctx context.Context, run *models.AgentRun, issue *models.Issue, experiment *models.Experiment) error {
    learning := &models.ProductionLearning{
        OrgID:           run.OrgID,
        Repo:            run.Repo,
        IssueType:       issue.IssueType,
        OutcomeType:     "success",
        ErrorPattern:    issue.Fingerprint,
        ApproachSummary: run.ResultSummary,
        ImpactMetrics:   experiment.OutcomeDetails,
        Learning:        fmt.Sprintf("Fix approach for '%s' errors: %s. Reduced error rate by %.0f%%.",
            issue.ErrorType(), run.ResultSummary, experiment.ImprovementPct()),
    }
    return s.db.CreateProductionLearning(ctx, learning)
}
```

Success patterns are lightweight — they confirm that the agent's default behavior works well for this type of issue. No prompt changes needed.

## No Change: Analyze Why the Fix Didn't Help

This is the most interesting case. The agent produced code that passed validation and code review, but didn't actually fix the production problem. An LLM analyzes why:

```go
func (s *FeedbackService) analyzeIneffectiveFix(ctx context.Context, run *models.AgentRun, issue *models.Issue, experiment *models.Experiment) error {
    analysis, _ := s.llm.Analyze(ctx, fmt.Sprintf(`
A fix was deployed for this issue but production metrics did not improve.

Issue: %s
Issue source: %s (with %d occurrences)
Agent's fix summary: %s
Agent's diff: %s

Baseline metrics (7 days before deploy): %s
Observation metrics (7 days after deploy): %s

Analyze why this fix did not reduce the error/issue rate. Common reasons:
- The fix addressed a symptom, not the root cause
- The error has multiple triggers and the fix only addressed one
- The issue was already declining naturally (not caused by the code)
- The fix was correct but the metric doesn't capture the improvement

Provide:
1. Most likely reason (1-2 sentences)
2. What the agent should have done differently (1-2 sentences)
3. A generalized learning for future similar issues (1 sentence, phrased as a directive)
`, issue.Title, issue.Source, issue.OccurrenceCount, run.ResultSummary, run.Diff,
        experiment.BaselineMetrics, experiment.ObservationMetrics))

    learning := &models.ProductionLearning{
        OrgID:           run.OrgID,
        Repo:            run.Repo,
        IssueType:       issue.IssueType,
        OutcomeType:     "ineffective",
        ErrorPattern:    issue.Fingerprint,
        ApproachSummary: run.ResultSummary,
        ImpactMetrics:   experiment.OutcomeDetails,
        Learning:        analysis.GeneralizedLearning,
        AnalysisDetail:  analysis.FullAnalysis,
    }
    return s.db.CreateProductionLearning(ctx, learning)
}
```

### Example Ineffective Fix Analysis

**Issue**: `TimeoutError in payment_processor.go:89`
**Agent's fix**: Added a retry with exponential backoff
**Production outcome**: No change in error rate

**Analysis**: "The fix added retry logic, but the timeout is caused by a downstream service that is consistently slow during peak hours. Retrying a slow service doesn't help — it adds load. The root cause is that the timeout threshold (5s) is too low for peak-hour latency (p99 = 8s)."

**Generalized learning**: "When fixing timeout errors, check whether the timeout threshold matches the actual latency distribution before adding retry logic."

## Regression: Record Anti-Patterns

When a fix makes things worse, the system records a strong anti-pattern that prevents similar mistakes:

```go
func (s *FeedbackService) recordAntiPattern(ctx context.Context, run *models.AgentRun, issue *models.Issue, experiment *models.Experiment) error {
    analysis, _ := s.llm.Analyze(ctx, fmt.Sprintf(`
A fix was deployed and caused a REGRESSION. Production metrics worsened.

Issue: %s
Agent's fix summary: %s
Agent's diff: %s

Baseline metrics: %s
Observation metrics (WORSENED): %s

Analyze what went wrong and produce a generalized anti-pattern that the agent should avoid in future runs.
`, issue.Title, run.ResultSummary, run.Diff,
        experiment.BaselineMetrics, experiment.ObservationMetrics))

    learning := &models.ProductionLearning{
        OrgID:           run.OrgID,
        Repo:            run.Repo,
        IssueType:       issue.IssueType,
        OutcomeType:     "regression",
        ErrorPattern:    issue.Fingerprint,
        ApproachSummary: run.ResultSummary,
        ImpactMetrics:   experiment.OutcomeDetails,
        Learning:        analysis.GeneralizedLearning,
        AnalysisDetail:  analysis.FullAnalysis,
        Severity:        "high",
    }
    return s.db.CreateProductionLearning(ctx, learning)
}
```

Regression anti-patterns are surfaced more prominently in agent prompts and in the `.143/learned-conventions.md` file.

## Injecting Learnings Into Agent Context

Production learnings are surfaced to the agent in two ways:

### 1. Per-Run Context Injection

When the orchestrator prepares context for a new agent run, it checks for relevant production learnings:

```go
func (o *Orchestrator) assembleProductionContext(ctx context.Context, issue *models.Issue) string {
    // Find learnings from similar issues (same error pattern, same repo, same issue type)
    learnings, _ := o.db.GetRelevantLearnings(ctx, issue.OrgID, issue.Repo, issue.Fingerprint, issue.IssueType)

    if len(learnings) == 0 {
        return ""
    }

    var context strings.Builder
    context.WriteString("## Production Learnings\n\n")
    context.WriteString("Previous fix attempts for similar issues produced these learnings:\n\n")

    for _, l := range learnings {
        icon := "info"
        if l.OutcomeType == "regression" {
            icon = "warning"
        }
        context.WriteString(fmt.Sprintf("- [%s] %s\n", icon, l.Learning))
    }

    // If there was a previous failed attempt on this exact issue
    if prev, _ := o.db.GetPreviousRunForIssue(ctx, issue.ID); prev != nil && prev.Outcome == "no_change" {
        context.WriteString(fmt.Sprintf("\n**Previous attempt on this exact issue was ineffective.**\n"))
        context.WriteString(fmt.Sprintf("Previous approach: %s\n", prev.ResultSummary))
        context.WriteString(fmt.Sprintf("Why it didn't work: %s\n", prev.AnalysisDetail))
        context.WriteString("Try a different approach.\n")
    }

    return context.String()
}
```

### 2. Curated Context Document

High-confidence learnings (from regressions and repeated ineffective fixes) are added to the `.143/learned-conventions.md` file alongside review-derived patterns:

```markdown
# 143 Learned Conventions

## From PR Reviews
- Always wrap errors with fmt.Errorf("context: %w", err)
  (4 occurrences, reviewers: @alice, @bob)

## From Production Outcomes
- [regression] Do not add retry logic to payment processor calls without
  first checking if the downstream service latency justifies retries
  (learned from fix #PR-342, which caused a 15% increase in error rate)
- [ineffective] When fixing timeout errors, check the actual latency
  distribution before adjusting timeout thresholds
  (learned from fix #PR-298, which did not reduce error rate)
```

## Data Model

### `production_learnings`

```sql
CREATE TABLE production_learnings (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id            uuid NOT NULL REFERENCES organizations(id),
    repo              text NOT NULL,
    experiment_id     uuid NOT NULL REFERENCES experiments(id),
    agent_run_id      uuid NOT NULL REFERENCES agent_runs(id),
    issue_id          uuid NOT NULL REFERENCES issues(id),
    issue_type        text,
    outcome_type      text NOT NULL,          -- 'success', 'ineffective', 'regression'
    error_pattern     text,                   -- issue fingerprint for matching similar issues
    approach_summary  text NOT NULL,          -- what the agent did
    learning          text NOT NULL,          -- generalized learning (1 sentence directive)
    analysis_detail   text,                   -- full LLM analysis (for display)
    impact_metrics    jsonb,                  -- before/after metrics snapshot
    severity          text NOT NULL DEFAULT 'medium', -- 'low', 'medium', 'high'
    status            text NOT NULL DEFAULT 'active', -- 'active', 'superseded', 'dismissed'
    created_at        timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_production_learnings_repo ON production_learnings (org_id, repo, status);
CREATE INDEX idx_production_learnings_pattern ON production_learnings (org_id, error_pattern) WHERE status = 'active';
CREATE INDEX idx_production_learnings_type ON production_learnings (org_id, issue_type, outcome_type);
```

## Integration with Observability (Doc 09)

The experiment evaluation job (doc 09) is extended with one additional step:

```go
func (o *ObservabilityService) EvaluateExperiment(ctx context.Context, experiment *models.Experiment) error {
    // ... existing: collect metrics, compare, classify outcome ...

    // NEW: trigger production feedback analysis
    o.jobs.Enqueue(ctx, "process_production_outcome", map[string]interface{}{
        "experiment_id": experiment.ID,
    })

    return nil
}
```

## Integration with Review Feedback Loop (Doc 11)

The curated context document (`.143/learned-conventions.md`) now has two sources:

1. **PR review patterns** (from doc 11) — conventions learned from human code review
2. **Production learnings** (from this doc) — patterns learned from production outcomes

Both are written to the same file, in separate sections, by the same regeneration job.

## Job Queue

| Job Type | Queue | Trigger |
|----------|-------|---------|
| `process_production_outcome` | `feedback` | After experiment evaluation completes (doc 09) |
| `regenerate_conventions_doc` | `feedback` | After production learnings change (shared with doc 11) |

## Build Order

This is part of **Phase 7** (Observability). It extends the experiment evaluation system with feedback:

1. **Outcome processing job** — LLM analysis of ineffective/regression outcomes
2. **Production learnings table** — storage and retrieval
3. **Context injection** — include relevant learnings in agent prompts
4. **Conventions doc integration** — add production learnings to `.143/learned-conventions.md`
5. **Learnings dashboard** — view production learnings per repo, manage status

## Connection with Other Design Docs

**Observability (doc 09)**: Extends experiment evaluation with the feedback processing step.

**Review Feedback Loop (doc 11)**: Shares the conventions document and regeneration mechanism.

**Agent Orchestrator (doc 06)**: Production context is assembled alongside codebase context at run time.

**Codebase Context (doc 14)**: Production learnings become part of the context package.

## Conflict Resolution: Review Patterns vs. Production Learnings

The `.143/learned-conventions.md` file receives input from two sources: PR review patterns (doc 11) and production learnings (this document). These can contradict each other. For example, reviewers might establish a pattern "always add retry logic for external calls" while production data shows that retries on a specific service caused cascading failures.

### Precedence Rules

When a conflict is detected during conventions doc regeneration, the following precedence applies:

1. **Production regressions always win.** A learning with `outcome_type = regression` and `severity = high` overrides any review pattern on the same topic. Production data is higher-signal than code review opinions.

2. **Manually curated rules always win.** If a team member manually edited a rule in `.143/learned-conventions.md` (detected by `manually_curated = true` on the review pattern), the manual edit is preserved regardless of conflicting automated learnings.

3. **Production ineffective learnings are additive, not overriding.** A learning with `outcome_type = ineffective` adds a caveat to an existing review pattern rather than replacing it. Example: review pattern says "wrap errors with context" → production learning adds "but avoid wrapping errors in hot paths where the allocation cost matters."

4. **When in doubt, surface both.** If the system cannot determine precedence (e.g., two automated learnings disagree), both are included in the conventions doc with a `[needs review]` tag and the admin is notified.

### Conflict Detection

The regeneration job detects conflicts using simple keyword overlap between review pattern rules and production learning text:

```go
func detectConflicts(patterns []ReviewPattern, learnings []ProductionLearning) []Conflict {
    var conflicts []Conflict
    for _, learning := range learnings {
        if learning.OutcomeType == "success" {
            continue // successes don't conflict
        }
        for _, pattern := range patterns {
            if topicOverlap(pattern.Rule, learning.Learning) > 0.5 {
                conflicts = append(conflicts, Conflict{
                    Pattern:  pattern,
                    Learning: learning,
                })
            }
        }
    }
    return conflicts
}
```

When a conflict is found, the conventions doc includes both with context:

```markdown
## Error Handling
- Always wrap errors with fmt.Errorf("context: %w", err)
  (4 occurrences · reviewers: @alice, @bob)
  ⚠️ Production caveat: Avoid wrapping errors in hot paths where
  allocation cost matters (learned from fix #PR-401, no_change outcome)
```

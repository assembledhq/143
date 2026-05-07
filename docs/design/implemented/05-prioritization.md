# Design: Prioritization Engine

> **Status:** Implemented | **Last reviewed:** 2026-03-25

This document describes how 143.dev scores and ranks ingested issues to determine which ones should be fixed first.

## Overview

After issues are ingested, the prioritization engine computes a composite score (0-100) for each issue. The score determines:

1. Which issues appear at the top of the dashboard
2. Which issues are eligible for automatic agent runs
3. Which issues should be brought to admin attention

## Scoring Algorithm

The composite score is a weighted sum of sub-scores:

```
score = w1 * customer_impact_score
      + w2 * severity_score
      + w3 * recency_score
      + w4 * revenue_risk_score
      + direction_alignment_modifier
```

### Default Weights

| Weight | Default | Description |
|--------|---------|-------------|
| w1 | 0.35 | customer_impact_score weight |
| w2 | 0.25 | severity_score weight |
| w3 | 0.20 | recency_score weight |
| w4 | 0.20 | revenue_risk_score weight |

Weights are configurable per org in `organizations.settings`.

### Sub-Scores

#### Customer Impact Score (0-100)

Based on the number of unique customers affected and the frequency of occurrence.

```
raw = log2(affected_customer_count + 1) * 10 + log2(occurrence_count + 1) * 5
customer_impact_score = min(raw, 100)
```

Uses log scale so that the difference between 1 and 10 customers matters more than the difference between 1000 and 1010.

#### Severity Score (0-100)

Direct mapping from severity level:

| Severity | Score |
|----------|-------|
| critical | 100 |
| high | 75 |
| medium | 50 |
| low | 25 |

#### Recency Score (0-100)

How recently the issue was last seen. Uses exponential decay:

```
hours_since_last_seen = now() - last_seen_at (in hours)
recency_score = 100 * exp(-hours_since_last_seen / 168)  // 168 hours = 1 week half-life
```

Issues seen in the last hour score ~100, issues a week old score ~37, issues a month old score ~2.

#### Revenue Risk Score (0-100)

Only populated if a CRM integration (e.g., Salesforce) is connected. Based on the total ARR of affected customers.

```
revenue_risk_score = min(total_affected_arr / org_total_arr * 1000, 100)
```

If no CRM is connected, this sub-score is 0 and its weight is redistributed to the other scores.

### Direction Alignment Modifier

The admin sets a "product direction" text in org settings (e.g., "Focus on enterprise reliability and API performance"). The system uses an LLM call to classify each issue's alignment with this direction:

```
direction_alignment = LLM_classify(issue.title, issue.description, org.product_direction)
// Returns a float from -1.0 to 1.0
```

- **+1.0**: Strongly aligned with product direction
- **0.0**: Neutral
- **-1.0**: Opposite to product direction

The modifier adjusts the final score:

```
final_score = score * (1 + 0.3 * direction_alignment)
```

Issues aligned with product direction get up to a 30% boost; misaligned issues get up to a 30% penalty.

### Eligibility Filter

An issue is `eligible_for_agent = true` if:

1. `direction_alignment > -0.5` (not strongly misaligned)
2. `status` is `open` or `triaged`
3. No active agent run already in progress for this issue
4. Score is above the org's minimum threshold (default: 20)

## Complexity Estimation

After priority scoring and before agent eligibility determination, the system estimates the complexity of each eligible issue. See [12-smart-routing.md](../backlog/12-smart-routing.md) for full details.

The complexity estimator classifies each issue into a tier (1-5: trivial → very complex) and an issue type (bug_fix, error_handling, performance, refactor, feature_gap, security). This classification:

- Determines whether the issue is within the org's **execution aggressiveness** setting
- Selects the initial **model tier** (fast, capable, powerful) for progressive execution
- Provides the agent with context about expected difficulty

Complexity estimation is an LLM call using a fast/cheap model (Haiku-class) and is stored in the `complexity_estimates` table.

## Computation Trigger

Priority scores are recomputed:

1. **After ingestion** — when new issues arrive or existing issues get updated
2. **Periodically** — every hour, recalculate all scores (recency score changes over time)
3. **On settings change** — when admin updates weights or product direction

The `prioritize` job handles this. It operates in bulk:

```go
func (s *PrioritizationService) RecomputeScores(ctx context.Context, orgID uuid.UUID) error {
    issues, err := s.db.GetOpenIssues(ctx, orgID)
    // compute scores for each issue
    // batch upsert into priority_scores
    // estimate complexity for newly eligible issues (enqueue estimate_complexity jobs)
    // enqueue agent runs for newly eligible issues (if autonomy allows and within aggressiveness)
}
```

## Auto-Trigger Agent Runs

After prioritization and complexity estimation, if the org's autonomy level allows automatic agent runs:

- **`auto_all`**: Automatically enqueue `run_agent` jobs for all eligible issues (respecting concurrency cap and **execution aggressiveness** — issues above the max complexity tier for the org's aggressiveness level are skipped).
- **`auto_simple`**: Only auto-trigger for issues with `severity` of `medium` or lower and `score < 60`. Also subject to aggressiveness filtering.
- **`manual`**: Never auto-trigger. Admin must manually trigger via the UI. When manually triggering a run for an issue above the aggressiveness threshold, the admin sees a warning but can proceed.

The concurrency cap (default: 3 concurrent agent runs per org) prevents runaway spending.

## Admin Controls

Admins can:

- **Adjust weights** via the settings page
- **Set product direction** free-text that influences alignment scoring
- **Set minimum score threshold** for agent eligibility
- **Override priority** — manually boost or suppress specific issues
- **Set autonomy level** — controls auto-triggering
- **Set execution aggressiveness** — controls which complexity tiers are attempted (see [12-smart-routing.md](../backlog/12-smart-routing.md))
- **Set confidence thresholds** — control when runs are auto-approved vs. paused for human review

## Score Explainability

The `priority_scores.factors` JSONB column stores a breakdown:

```json
{
  "customer_impact": { "score": 72, "affected_customers": 45, "occurrences": 312 },
  "severity": { "score": 75, "level": "high" },
  "recency": { "score": 88, "last_seen_hours_ago": 2.3 },
  "revenue_risk": { "score": 0, "reason": "no_crm_integration" },
  "direction_alignment": { "score": 0.6, "reasoning": "Aligns with API reliability focus" },
  "final_score": 78.2,
  "weights_used": { "w1": 0.35, "w2": 0.25, "w3": 0.20, "w4": 0.20 }
}
```

This is shown in the UI so admins understand why an issue is ranked where it is.

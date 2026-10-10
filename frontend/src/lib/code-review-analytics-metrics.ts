export const CODE_REVIEW_ANALYTICS_METRICS = [
  "prs_reviewed",
  "approved_by_143",
  "approval_rate",
  "median_rounds_to_approval",
  "average_rounds_to_approval",
  "p95_rounds_to_approval",
] as const;

export type CodeReviewAnalyticsMetric = typeof CODE_REVIEW_ANALYTICS_METRICS[number];

export const DEFAULT_CODE_REVIEW_ANALYTICS_METRIC: CodeReviewAnalyticsMetric = "average_rounds_to_approval";

export const CODE_REVIEW_ANALYTICS_METRIC_LABELS: Record<CodeReviewAnalyticsMetric, string> = {
  prs_reviewed: "PRs reviewed",
  approved_by_143: "Automatically approved",
  approval_rate: "PR approval rate",
  median_rounds_to_approval: "Median rounds to approval",
  average_rounds_to_approval: "Average rounds to approval",
  p95_rounds_to_approval: "P95 rounds to approval",
};

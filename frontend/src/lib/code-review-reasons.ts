export const ALL_CODE_REVIEW_REASONS = "all";

export const CODE_REVIEW_REASON_CODES = [
  "reviewer_disabled",
  "context_unavailable",
  "head_changed",
  "files_limit_exceeded",
  "lines_limit_exceeded",
  "checks_failing",
  "required_check_failing",
  "description_failed",
  "branch_out_of_date",
  "fork_ineligible",
  "author_ineligible",
  "unresolved_human_review",
  "blocking_findings",
  "reviewer_disagreement",
  "scope_mismatch",
  "unresolved_uncertainty",
  "prompt_injection",
  "sensitive_path",
  "path_outside_scope",
  "blocked_path",
  "policy_path_changed",
  "excluded_category",
  "reviewer_quorum",
  "orchestrator_synthesis_invalid",
  "orchestrator_escalation",
  "orchestrator_context_stale",
  "architecture",
  "ownership",
  "operational_risk",
  "sensitive_change",
  "policy_requirement",
] as const;

export type CodeReviewReasonCode = (typeof CODE_REVIEW_REASON_CODES)[number];

export const CODE_REVIEW_REASON_LABELS: Record<CodeReviewReasonCode, string> = {
  reviewer_disabled: "Automatic approval was disabled",
  context_unavailable: "PR context was unavailable",
  head_changed: "PR changed during review",
  files_limit_exceeded: "File-count limit exceeded",
  lines_limit_exceeded: "Line-count limit exceeded",
  checks_failing: "Required checks were not passing",
  required_check_failing: "A named required check was not passing",
  description_failed: "PR description requirements were not met",
  branch_out_of_date: "Branch was out of date",
  fork_ineligible: "Fork PRs were not eligible",
  author_ineligible: "Author was not eligible",
  unresolved_human_review: "Unresolved human review remained",
  blocking_findings: "Reviewers found a blocking issue",
  reviewer_disagreement: "Reviewer agents disagreed",
  scope_mismatch: "Change scope did not match the PR",
  unresolved_uncertainty: "Important uncertainty remained",
  prompt_injection: "Prompt-injection risk was detected",
  sensitive_path: "Sensitive paths changed",
  path_outside_scope: "Paths were outside the allowed scope",
  blocked_path: "Blocked paths changed",
  policy_path_changed: "Policy paths changed",
  excluded_category: "An excluded change category applied",
  reviewer_quorum: "Reviewer quorum was not met",
  orchestrator_synthesis_invalid: "Final synthesis was unavailable",
  orchestrator_escalation: "Final synthesis needed human review",
  orchestrator_context_stale: "Final synthesis used stale PR context",
  architecture: "Architecture needed human judgment",
  ownership: "Ownership needed human judgment",
  operational_risk: "Operational risk needed human judgment",
  sensitive_change: "Sensitive change needed human judgment",
  policy_requirement: "An approval-policy requirement needed human judgment",
};

export function codeReviewReasonLabel(code: string): string {
  const known = CODE_REVIEW_REASON_LABELS[code as CodeReviewReasonCode];
  if (known) return known;
  const readable = code.replaceAll("_", " ");
  return readable.charAt(0).toUpperCase() + readable.slice(1);
}

export function codeReviewReasonDescription(reason: {
  code: string;
  actual?: number;
  limit?: number;
  subject?: string;
}): string {
  const label = codeReviewReasonLabel(reason.code);
  const subject = reason.subject?.trim();
  if (subject) return `${label}: ${subject}`;
  // The API omits zero-valued counts, so render whichever side is present
  // instead of dropping the measurement entirely.
  if (reason.actual !== undefined && reason.limit !== undefined) {
    return `${label} (${reason.actual.toLocaleString()} of ${reason.limit.toLocaleString()})`;
  }
  if (reason.actual !== undefined) return `${label} (${reason.actual.toLocaleString()})`;
  if (reason.limit !== undefined) return `${label} (limit ${reason.limit.toLocaleString()})`;
  return label;
}

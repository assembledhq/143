export const ALL_CODE_REVIEW_REASONS = "all";

export const CODE_REVIEW_REASON_CODES = [
  "reviewer_disabled",
  "context_unavailable",
  "head_changed",
  "files_limit_exceeded",
  "lines_limit_exceeded",
  "additions_limit_exceeded",
  "deletions_limit_exceeded",
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
  additions_limit_exceeded: "Additions limit exceeded",
  deletions_limit_exceeded: "Deletions limit exceeded",
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

export const INFORMATIONAL_CODE_REVIEW_REASON_CODES = ["size_safeguard"] as const;

export function isInformationalCodeReviewReason(code: string): boolean {
  return (INFORMATIONAL_CODE_REVIEW_REASON_CODES as readonly string[]).includes(code);
}

export function blockingCodeReviewReasons<T extends { code: string }>(reasons: readonly T[]): T[] {
  return reasons.filter((reason) => !isInformationalCodeReviewReason(reason.code));
}

export function informationalCodeReviewReasons<T extends { code: string }>(reasons: readonly T[]): T[] {
  return reasons.filter((reason) => isInformationalCodeReviewReason(reason.code));
}

const RISK_TIER_SOURCE_LABELS: Record<string, string> = {
  authoritative: "authoritative",
  missing: "no risk-tier check",
  untrusted_app: "risk-tier check from another app",
  stale_head: "risk-tier check for another commit",
  unparsable: "unreadable risk-tier check",
  shadow_mode: "shadow-mode risk-tier check",
  lookup_failed: "risk-tier lookup failed",
};

export function sizeSafeguardSummary(decision: {
  outcome: string;
  source: string;
  tier?: string;
  mode?: string;
  check_run_id?: number;
}): string {
  const verb = decision.outcome === "waived" ? "waived" : "applied";
  const basis = decision.source === "authoritative" && decision.tier
    ? `risk tier ${decision.tier}`
    : RISK_TIER_SOURCE_LABELS[decision.source] ?? decision.source.replaceAll("_", " ");
  const run = decision.check_run_id ? `, check run ${decision.check_run_id}` : "";
  const mode = decision.mode ? `, ${decision.mode} mode` : "";
  return `Size limits ${verb}: ${basis}${mode}${run}`;
}

type SizeSafeguardMeasurement = { actual: number; limit: number; exceeded: boolean };

const SIZE_SAFEGUARD_MEASUREMENTS = [
  ["files", "Files", "files_limit_exceeded"],
  ["additions", "Additions", "additions_limit_exceeded"],
  ["deletions", "Deletions", "deletions_limit_exceeded"],
] as const;

export function sizeSafeguardMeasurements(decision: {
  files?: SizeSafeguardMeasurement;
  additions?: SizeSafeguardMeasurement;
  deletions?: SizeSafeguardMeasurement;
  exceeded_limits?: readonly { code: string; actual?: number; limit?: number }[];
}): string[] {
  const measured = SIZE_SAFEGUARD_MEASUREMENTS.flatMap(([key, label, code]) => {
    const measurement = decision[key];
    if (!measurement) return [];
    return [{ code, line: `${label}: ${measurement.actual.toLocaleString()} of ${measurement.limit.toLocaleString()}${measurement.exceeded ? " (over limit)" : ""}` }];
  });
  const measuredCodes = new Set<string>(measured.map(({ code }) => code));
  const exceededOnly = (decision.exceeded_limits ?? []).filter((limit) => !measuredCodes.has(limit.code)).map(codeReviewReasonDescription);
  return [...measured.map(({ line }) => line), ...exceededOnly];
}

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

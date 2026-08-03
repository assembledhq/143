"use client";

import Link from "next/link";
import type { ReactNode } from "react";
import { ChartNoAxesColumnIncreasing } from "lucide-react";
import { DataTableSummaryRow } from "@/components/data-table-summary-row";
import { EmptyState } from "@/components/empty-state";
import { MetricInfoTooltip } from "@/components/metric-info-tooltip";
import { SectionGroup } from "@/components/section-group";
import { Badge } from "@/components/ui/badge";
import { SortableTableHeader, sortDirectionAriaValue } from "@/components/sortable-table-header";
import { Card, CardContent } from "@/components/ui/card";
import { ErrorNotice } from "@/components/ui/error-notice";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import type { CodeReviewAnalytics } from "@/lib/types";
type AuthorSort = "author" | "reviews" | "approved" | "not_approved" | "approval_rate" | "first_round" | "median_rounds" | "median_additions" | "median_deletions";

const NON_APPROVAL_REASON_LABELS: Record<string, string> = {
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
  blocking_findings: "Reviewers found a blocking issue",
  reviewer_disagreement: "Reviewer agents disagreed",
  scope_mismatch: "Change scope did not match the PR",
  unresolved_uncertainty: "Important uncertainty remained",
  prompt_injection: "Prompt-injection risk was detected",
  sensitive_path: "Sensitive paths changed",
  path_outside_scope: "Paths were outside the allowed scope",
  blocked_path: "Blocked paths changed",
  reviewer_quorum: "Reviewer quorum was not met",
  orchestrator_synthesis_invalid: "Final synthesis was unavailable",
  orchestrator_context_stale: "Final synthesis used stale PR context",
  architecture: "Architecture needed human judgment",
  ownership: "Ownership needed human judgment",
  operational_risk: "Operational risk needed human judgment",
  sensitive_change: "Sensitive change needed human judgment",
  policy_requirement: "An approval-policy requirement needed human judgment",
};

const APPROVAL_ROUND_LABELS: Record<CodeReviewAnalytics["approval_rounds"][number]["bucket"], string> = {
  round_1: "Approved in round 1",
  round_2: "Approved in round 2",
  round_3: "Approved in round 3",
  round_4_plus: "Approved in round 4+",
  not_yet_approved: "Not yet approved",
};

function percentage(value: number, total: number): string {
  if (total <= 0) return "—";
  return `${Math.round((value / total) * 100)}%`;
}

function roundedMetric(value: number | null): string {
  if (value === null || !Number.isFinite(value)) return "—";
  return Math.round(value).toLocaleString();
}

function decimalMetric(value: number | null): string {
  if (value === null || !Number.isFinite(value)) return "—";
  return value.toLocaleString(undefined, {
    minimumFractionDigits: 1,
    maximumFractionDigits: 1,
  });
}

function signedRoundedMetric(value: number | null, sign: "+" | "-"): string {
  const formatted = roundedMetric(value);
  return formatted === "—" ? formatted : `${sign}${formatted}`;
}

function medianAriaLabel(value: number | null, sign: "+" | "-", noun: string): string {
  const formatted = signedRoundedMetric(value, sign);
  return formatted === "—" ? `No ${noun} data overall` : `${formatted} ${noun} overall`;
}

function reasonLabel(code: string): string {
  const known = NON_APPROVAL_REASON_LABELS[code];
  if (known) return known;
  const readable = code.replaceAll("_", " ");
  return readable.charAt(0).toUpperCase() + readable.slice(1);
}

function authorReviewsHref({
  author,
  outcome,
  repository,
  range,
}: {
  author: string;
  outcome?: "automatically_approved" | "completed_not_approved";
  repository?: string;
  range: string;
}): string {
  const params = new URLSearchParams({
    tab: "reviews",
    author,
    range,
  });
  if (outcome) {
    params.set("status", "completed");
    params.set("outcome", outcome);
  }
  if (repository) params.set("repository", repository);
  return `/code-reviews?${params.toString()}`;
}

function AuthorReviewCountLink({
  author,
  count,
  label,
  outcome,
  repository,
  range,
}: {
  author: string;
  count: number;
  label: string;
  outcome?: "automatically_approved" | "completed_not_approved";
  repository?: string;
  range: string;
}) {
  return (
    <Link
      href={authorReviewsHref({ author, outcome, repository, range })}
      className="font-medium text-primary underline-offset-4 hover:underline focus-visible:rounded-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      aria-label={`${count.toLocaleString()} ${label} by ${author}`}
    >
      {count.toLocaleString()}
    </Link>
  );
}

function MetricCard({
  label,
  value,
  context,
  definition,
}: {
  label: string;
  value: string;
  context?: string;
  definition?: string;
}) {
  return (
    <Card>
      <CardContent className="space-y-1.5">
        <div className="flex items-center gap-1 text-xs font-medium text-muted-foreground">
          <span>{label}</span>
          {definition ? <MetricInfoTooltip label={label} definition={definition} /> : null}
        </div>
        <p className="text-2xl font-semibold tabular-nums text-foreground">{value}</p>
        {context ? <p className="text-xs text-muted-foreground">{context}</p> : null}
      </CardContent>
    </Card>
  );
}

// Derived from the same report as the tables below, so the headline numbers
// always agree with them. The reviews tab's cards deliberately describe current
// review activity only and answer a different question.
function ApprovalOutcomeCards({ summary }: { summary: CodeReviewAnalytics["summary"] }) {
  return (
    <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4" aria-label="Approval outcomes">
      <MetricCard
        label="PRs reviewed"
        value={summary.prs_reviewed.toLocaleString()}
        definition="Unique pull requests first sent to 143 during the selected time period."
      />
      <MetricCard
        label="Approved by 143"
        value={summary.approved_by_143.toLocaleString()}
        definition="Reviewed pull requests where 143 posted an approval on GitHub."
      />
      <MetricCard
        label="Approval rate"
        value={percentage(summary.approved_by_143, summary.prs_reviewed)}
        definition="The percentage of reviewed pull requests where 143 posted an approval on GitHub."
      />
      <MetricCard
        label="Median rounds to approval"
        value={decimalMetric(summary.median_rounds_to_approval)}
        definition="The median number of distinct completed revisions before 143 first posted an approval, among approved pull requests."
      />
    </div>
  );
}

export function CodeReviewAnalyticsReport({
  analytics,
  isLoading,
  isError,
  onRetry,
  authorSort,
  authorSortOrder,
  onAuthorSort,
  reviewLinkFilters,
  filters,
}: {
  analytics?: CodeReviewAnalytics;
  isLoading: boolean;
  isError: boolean;
  onRetry: () => void;
  authorSort: AuthorSort;
  authorSortOrder: "asc" | "desc";
  onAuthorSort: (sort: AuthorSort, order: "asc" | "desc") => void;
  reviewLinkFilters: {
    repository?: string;
    range: string;
  };
  filters: ReactNode;
}) {
  if (!analytics && isLoading) {
    return (
      <div className="space-y-4">
        {filters}
        <p className="py-12 text-center text-sm text-muted-foreground">Loading code review analytics…</p>
      </div>
    );
  }
  if (!analytics) {
    return (
      <div className="space-y-4">
        {filters}
        <ErrorNotice
          title="Analytics unavailable"
          description="The selected code review report could not be loaded."
          action={{ label: "Retry", onClick: onRetry }}
        />
      </div>
    );
  }

  const { summary } = analytics;
  const authorHeader = (label: string, sort: AuthorSort) => {
    const active = authorSort === sort;
    return (
      <SortableTableHeader
        label={label}
        direction={active ? authorSortOrder : false}
        align={sort === "author" ? "left" : "right"}
        // The author table always has an ordering, so allowUnsorted stays off
        // and the callback only ever receives a direction.
        onSort={(next) => { if (next) onAuthorSort(sort, next); }}
      />
    );
  };
  if (summary.prs_reviewed === 0) {
    return (
      <div className="space-y-3">
        {isError ? (
          <ErrorNotice
            title="Analytics may be out of date"
            description="Showing the last successful report because the latest refresh failed."
            action={{ label: "Retry", onClick: onRetry }}
          />
        ) : null}
        {filters}
        <EmptyState
          icon={ChartNoAxesColumnIncreasing}
          title="No PRs first sent to 143 in this time window"
          description="Choose a longer time window or another repository to analyze PR outcomes."
        />
      </div>
    );
  }

  return (
    <div className="space-y-6" aria-busy={isLoading}>
      {isError ? (
        <ErrorNotice
          title="Analytics may be out of date"
          description="Showing the last successful report because the latest refresh failed."
          action={{ label: "Retry", onClick: onRetry }}
        />
      ) : null}

      <ApprovalOutcomeCards summary={summary} />
      {filters}

      <SectionGroup
        title="Approval by round"
        description="Each PR appears once, based on the first distinct completed head that received a posted 143 approval."
      >
        <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-5" aria-label="Approval by round">
          {analytics.approval_rounds.map((bucket) => (
            <MetricCard
              key={bucket.bucket}
              label={APPROVAL_ROUND_LABELS[bucket.bucket]}
              value={bucket.prs.toLocaleString()}
              context={`${percentage(bucket.prs, summary.prs_reviewed)} of PRs reviewed`}
            />
          ))}
        </div>
      </SectionGroup>

      <SectionGroup
        title="Usage by PR author"
        description="Unique PR outcomes grouped by the author captured from the first available assessment."
      >
        {analytics.authors.length === 0 ? (
          <EmptyState
            icon={ChartNoAxesColumnIncreasing}
            title="No author attribution available"
            description="Completed reviews in this report could not be matched to a pull request author."
            variant="inline"
          />
        ) : (
          <>
          <Card className="overflow-x-auto">
            <Table aria-label="Code review analytics by PR author">
              <TableHeader>
                <TableRow>
                  {([
                    ["PR author", "author"],
                    ["PRs", "reviews"],
                    ["Approved", "approved"],
                    ["Not approved", "not_approved"],
                    ["Approval rate", "approval_rate"],
                    ["First-round approval", "first_round"],
                    ["Median rounds", "median_rounds"],
                    ["Median additions", "median_additions"],
                    ["Median deletions", "median_deletions"],
                  ] as const).map(([label, sort]) => (
                    <TableHead
                      key={sort}
                      className={sort === "author" ? undefined : "text-right"}
                      aria-sort={sortDirectionAriaValue(authorSort === sort ? authorSortOrder : false)}
                    >
                      {authorHeader(label, sort)}
                    </TableHead>
                  ))}
                </TableRow>
              </TableHeader>
              <TableBody>
                {analytics.authors.map((author) => (
                  <TableRow key={author.author}>
                    <TableCell className="font-medium">{author.author}</TableCell>
                    <TableCell className="text-right tabular-nums">
                      <AuthorReviewCountLink
                        author={author.author}
                        count={author.prs_reviewed}
                        label="reviewed PRs"
                        repository={reviewLinkFilters.repository}
                        range={reviewLinkFilters.range}
                      />
                    </TableCell>
                    <TableCell className="text-right tabular-nums">
                      <AuthorReviewCountLink
                        author={author.author}
                        count={author.approved_by_143}
                        label="PRs approved by 143"
                        outcome="automatically_approved"
                        repository={reviewLinkFilters.repository}
                        range={reviewLinkFilters.range}
                      />
                    </TableCell>
                    <TableCell className="text-right tabular-nums">
                      <AuthorReviewCountLink
                        author={author.author}
                        count={author.not_approved}
                        label="not approved PRs"
                        outcome="completed_not_approved"
                        repository={reviewLinkFilters.repository}
                        range={reviewLinkFilters.range}
                      />
                    </TableCell>
                    <TableCell className="text-right tabular-nums">
                      {percentage(author.approved_by_143, author.prs_reviewed)}
                    </TableCell>
                    <TableCell className="text-right tabular-nums">{author.approved_first_round.toLocaleString()}</TableCell>
                    <TableCell className="text-right tabular-nums">{decimalMetric(author.median_rounds_to_approval)}</TableCell>
                    <TableCell className="text-right tabular-nums">{signedRoundedMetric(author.median_additions, "+")}</TableCell>
                    <TableCell className="text-right tabular-nums">{signedRoundedMetric(author.median_deletions, "-")}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
              <DataTableSummaryRow
                description="Across all PRs first sent to 143 in the current repository and time filters."
                cells={[
                  {
                    content: summary.prs_reviewed.toLocaleString(),
                    className: "text-right",
                    ariaLabel: `${summary.prs_reviewed.toLocaleString()} PRs reviewed overall`,
                  },
                  {
                    content: summary.approved_by_143.toLocaleString(),
                    className: "text-right",
                    ariaLabel: `${summary.approved_by_143.toLocaleString()} PRs approved by 143 overall`,
                  },
                  {
                    content: summary.not_approved.toLocaleString(),
                    className: "text-right",
                    ariaLabel: `${summary.not_approved.toLocaleString()} PRs not approved overall`,
                  },
                  {
                    content: percentage(summary.approved_by_143, summary.prs_reviewed),
                    className: "text-right",
                    ariaLabel: `${percentage(summary.approved_by_143, summary.prs_reviewed)} overall approval rate`,
                  },
                  {
                    content: summary.approved_first_round.toLocaleString(),
                    className: "text-right",
                    ariaLabel: `${summary.approved_first_round.toLocaleString()} PRs approved in the first round overall`,
                  },
                  {
                    content: decimalMetric(summary.median_rounds_to_approval),
                    className: "text-right",
                    ariaLabel: summary.median_rounds_to_approval === null
                      ? "No rounds to approval data overall"
                      : `${decimalMetric(summary.median_rounds_to_approval)} median rounds to approval overall`,
                  },
                  {
                    content: signedRoundedMetric(summary.median_additions, "+"),
                    className: "text-right",
                    ariaLabel: medianAriaLabel(summary.median_additions, "+", "median additions"),
                  },
                  {
                    content: signedRoundedMetric(summary.median_deletions, "-"),
                    className: "text-right",
                    ariaLabel: medianAriaLabel(summary.median_deletions, "-", "median deletions"),
                  },
                ]}
              />
            </Table>
          </Card>
          <p className="text-xs text-muted-foreground">
            Median additions and deletions come from the{" "}
            {summary.prs_with_change_breakdown.toLocaleString()} of{" "}
            {summary.prs_reviewed.toLocaleString()} PRs whose representative assessment captured a change
            breakdown. Treat a small sample as directional.
          </p>
          </>
        )}
      </SectionGroup>

      <div className="grid gap-6 xl:grid-cols-2">
        <SectionGroup
          title="Why PRs were not approved right away"
          description="Each reason counts at most once per PR across non-approval rounds before the first posted approval."
        >
          {analytics.non_approval_reasons.length === 0 ? (
            <p className="py-6 text-center text-sm text-muted-foreground">
              No structured non-approval reasons were captured in this time window.
            </p>
          ) : (
            <Card>
              <CardContent className="divide-y divide-border p-0">
                {analytics.non_approval_reasons.map((reason) => (
                  <div key={reason.code} className="flex items-center justify-between gap-4 px-4 py-3">
                    <span className="text-sm text-foreground">{reasonLabel(reason.code)}</span>
                    <Badge variant="secondary">{reason.prs.toLocaleString()} PRs</Badge>
                  </div>
                ))}
              </CardContent>
            </Card>
          )}
        </SectionGroup>

        <SectionGroup
          title="PR findings and operational outcomes"
          description="Findings and decision outcomes use one representative assessment per PR."
        >
          <div className="grid gap-3 sm:grid-cols-3 xl:grid-cols-1 2xl:grid-cols-3">
            <MetricCard
              label="PRs with findings"
              value={summary.prs_with_findings.toLocaleString()}
              context={`${percentage(summary.prs_with_findings, summary.prs_with_completed_round)} of PRs with a completed round`}
            />
            <MetricCard
              label="Blocking findings"
              value={summary.prs_with_blocking_findings.toLocaleString()}
              context="PRs with at least one P0 or P1"
            />
            <MetricCard
              label="Findings per PR"
              value={summary.prs_with_completed_round > 0 ? (summary.total_findings / summary.prs_with_completed_round).toFixed(1) : "—"}
              context={`${summary.total_findings.toLocaleString()} findings total`}
            />
          </div>
          <p className="text-xs text-muted-foreground">
            Representative decisions: {summary.needs_human_review.toLocaleString()} needed human review,{" "}
            {summary.comment_only.toLocaleString()} were comment-only, {summary.blocked.toLocaleString()} were blocked, and{" "}
            {summary.approval_not_posted.toLocaleString()} approval decisions were not posted.
            {" "}Operational attempt counts may overlap: {summary.prs_with_failed_attempt.toLocaleString()} PRs had a failed attempt and{" "}
            {summary.prs_with_stale_attempt.toLocaleString()} had a stale attempt.
          </p>
        </SectionGroup>
      </div>
    </div>
  );
}

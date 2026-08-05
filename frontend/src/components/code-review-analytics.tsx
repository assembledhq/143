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
import { codeReviewReasonLabel } from "@/lib/code-review-reasons";
import type { CodeReviewAnalytics, CodeReviewInsights } from "@/lib/types";
type AuthorSort = "author" | "reviews" | "approved" | "not_approved" | "approval_rate" | "first_round" | "median_rounds" | "median_additions" | "median_deletions";

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

function reasonReviewsHref({
  reason,
  repository,
  range,
}: {
  reason: string;
  repository?: string;
  range: string;
}): string {
  const params = new URLSearchParams({
    tab: "reviews",
    outcome: "completed_not_approved",
    reason,
    status: "all",
    range,
  });
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
  insights,
  insightsIsLoading = false,
  insightsIsError = false,
  onRetryInsights,
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
  insights?: CodeReviewInsights;
  insightsIsLoading?: boolean;
  insightsIsError?: boolean;
  onRetryInsights?: () => void;
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

      {insights ? <PolicyFeedbackInsights insights={insights} /> : null}
      {!insights && insightsIsLoading ? <p className="py-6 text-center text-sm text-muted-foreground">Loading decision feedback…</p> : null}
      {!insights && insightsIsError ? (
        <ErrorNotice
          title="Decision feedback unavailable"
          description="Policy feedback metrics could not be loaded. The approval report above is unaffected."
          action={onRetryInsights ? { label: "Retry", onClick: onRetryInsights } : undefined}
        />
      ) : null}

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

      <SectionGroup
        title="Direct review requests by user"
        description="For the selected PR cohort, trusted comments that directly mentioned the configured 143 code reviewer. GitHub redeliveries count once."
      >
        {analytics.comment_requests_by_user.length === 0 ? (
          <p className="py-6 text-center text-sm text-muted-foreground">
            No direct comment requests were captured for this PR cohort.
          </p>
        ) : (
          <Card className="overflow-x-auto">
            <Table aria-label="Direct code review requests by GitHub user">
              <TableHeader>
                <TableRow>
                  <TableHead>GitHub user</TableHead>
                  <TableHead className="text-right">Direct comment requests</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {analytics.comment_requests_by_user.map((requester) => (
                  <TableRow key={requester.github_login}>
                    <TableCell className="font-medium">{requester.github_login}</TableCell>
                    <TableCell className="text-right tabular-nums">
                      {requester.requests.toLocaleString()}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
              <DataTableSummaryRow
                description="Direct comment requests across all PRs first sent to 143 in the current repository and time filters."
                cells={[{
                  content: analytics.comment_requests_total.toLocaleString(),
                  className: "text-right",
                  ariaLabel: `${analytics.comment_requests_total.toLocaleString()} direct comment requests overall`,
                }]}
              />
            </Table>
          </Card>
        )}
      </SectionGroup>

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
                  <Link
                    key={reason.code}
                    href={reasonReviewsHref({
                      reason: reason.code,
                      repository: reviewLinkFilters.repository,
                      range: reviewLinkFilters.range,
                    })}
                    className="group flex items-center justify-between gap-4 px-4 py-3 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring hover:bg-muted/50"
                    aria-label={`View ${reason.prs.toLocaleString()} PRs where ${codeReviewReasonLabel(reason.code).toLowerCase()}`}
                  >
                    <span className="text-sm text-foreground underline-offset-4 group-hover:underline">
                      {codeReviewReasonLabel(reason.code)}
                    </span>
                    <Badge variant="secondary">{reason.prs.toLocaleString()} PRs</Badge>
                  </Link>
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

function PolicyFeedbackInsights({ insights }: { insights: CodeReviewInsights }) {
  const flipRate = insights.reassessments > 0
    ? percentage(insights.reassessment_flips, insights.reassessments)
    : "—";
  const directionLabel = (direction: string) => direction === "should_have_approved" ? "Should have approved" : "Should not have approved";
  const duration = (seconds?: number) => {
    if (seconds === undefined || !Number.isFinite(seconds)) return "—";
    if (seconds < 60) return `${Math.round(seconds)}s`;
    if (seconds < 3600) return `${Math.round(seconds / 60)}m`;
    return `${(seconds / 3600).toFixed(1)}h`;
  };
  const settingHref = (reasonCode: string) => {
    if (reasonCode === "files_limit_exceeded") return "/code-reviews?tab=policy#policy-max-files-changed";
    if (reasonCode === "lines_limit_exceeded") return "/code-reviews?tab=policy#policy-max-lines-changed";
    return "/code-reviews?tab=policy";
  };
  return (
    <SectionGroup
      title="Decision feedback"
      description={insights.ranking_enabled
        ? "The policy-owner queue is ranked from explainable disagreement signals."
        : "The queue stays chronological until one organization records at least 10 eligible disputes per month for two complete months."}
    >
      <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <MetricCard label="Decisions observed" value={insights.decisions.toLocaleString()} />
        <MetricCard label="Objection rate" value={`${Math.round(insights.objection_rate * 100)}%`} context={`${insights.disputes.toLocaleString()} objections · ${insights.upheld_disputes.toLocaleString()} upheld`} />
        <MetricCard label="Reassessments" value={insights.reassessments.toLocaleString()} context={`${flipRate} changed decision · $${insights.reassessment_cost_usd.toFixed(2)} spend`} />
        <MetricCard label="Early policy stops" value={insights.deterministic_early_stops.toLocaleString()} context={`${insights.reviewer_runs_avoided.toLocaleString()} reviewer runs avoided`} />
        <MetricCard label="Full reviews after early stop" value={insights.full_review_requests_after_early_stop.toLocaleString()} context="Same-head explicit requests" />
        <MetricCard
          label="Median resolution"
          value={duration(insights.median_adjudication_seconds)}
          context="Filed to adjudicated"
        />
        <MetricCard label="Median decision" value={duration(insights.median_decision_seconds)} context="Review started to decision" />
        <MetricCard label="Owner time / resolution" value={insights.policy_owner_minutes_per_resolution === undefined ? "—" : `${insights.policy_owner_minutes_per_resolution.toFixed(1)}m`} context="Measured active queue interaction" />
        <MetricCard label="Outcomes fresh through" value={insights.projection_fresh_through ? new Date(insights.projection_fresh_through).toLocaleString() : "—"} context={insights.projection_updated_at ? `Projection updated ${new Date(insights.projection_updated_at).toLocaleString()}` : "No projected outcomes"} />
        <MetricCard label="Queue ordering" value={insights.ranking_enabled ? "Ranked" : "Chronological"} context="Ranking requires sustained dispute volume" />
      </div>
      <div className="grid gap-6 xl:grid-cols-2">
        <InsightCountTable title="Objection directions" label="Direction" items={insights.directions.map((item) => ({ key: item.direction, label: directionLabel(item.direction), count: item.count }))} />
        <InsightCountTable title="Objection kinds" label="Kind" items={insights.dispute_kinds.map((item) => ({ key: item.kind, label: item.kind.replaceAll("_", " "), count: item.count }))} />
      </div>
      {insights.reasons.length > 0 ? (
        <Card>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Reason</TableHead>
                <TableHead className="text-right">Decisions</TableHead>
                <TableHead className="text-right">Objections</TableHead>
                <TableHead className="text-right">Rate</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {insights.reasons.map((reason) => (
                <TableRow key={reason.reason_code}>
                  <TableCell>{codeReviewReasonLabel(reason.reason_code)}</TableCell>
                  <TableCell className="text-right tabular-nums">{reason.decisions.toLocaleString()}</TableCell>
                  <TableCell className="text-right tabular-nums">{reason.disputes.toLocaleString()}</TableCell>
                  <TableCell className="text-right tabular-nums">{Math.round(reason.dispute_rate * 100)}%</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Card>
      ) : null}
      {insights.actual_vs_limit.length > 0 ? (
        <Card>
          <CardContent className="space-y-2">
            {insights.actual_vs_limit.map((item) => (
              <p key={`${item.reason_code}:${item.actual}:${item.limit}`} className="text-sm text-muted-foreground">
                <Link href={settingHref(item.reason_code)} className="font-medium text-foreground underline-offset-4 hover:underline">
                  {codeReviewReasonLabel(item.reason_code)}
                </Link>{": "}{item.actual.toLocaleString()} actual vs {item.limit.toLocaleString()} allowed ({item.count.toLocaleString()} decisions)
              </p>
            ))}
          </CardContent>
        </Card>
      ) : null}
      {insights.flip_buckets.length > 0 ? (
        <Card>
          <Table>
            <TableHeader><TableRow><TableHead>Reassessment attempt</TableHead><TableHead>Input</TableHead><TableHead className="text-right">Runs</TableHead><TableHead className="text-right">Flip rate</TableHead></TableRow></TableHeader>
            <TableBody>{insights.flip_buckets.map((bucket) => (
              <TableRow key={`${bucket.attempt}:${bucket.input_change}`}><TableCell>Attempt {bucket.attempt}</TableCell><TableCell className="capitalize">{bucket.input_change}</TableCell><TableCell className="text-right tabular-nums">{bucket.reassessments.toLocaleString()}</TableCell><TableCell className="text-right tabular-nums">{percentage(bucket.flips, bucket.reassessments)}</TableCell></TableRow>
            ))}</TableBody>
          </Table>
        </Card>
      ) : null}
      {insights.policy_decision_mix.length > 0 ? (
        <Card>
          <Table>
            <TableHeader><TableRow><TableHead>Policy version</TableHead><TableHead>Decision</TableHead><TableHead className="text-right">Decisions</TableHead></TableRow></TableHeader>
            <TableBody>{insights.policy_decision_mix.map((item) => (
              <TableRow key={`${item.policy_id}:${item.decision}`}><TableCell><span className="font-medium">v{item.policy_version}</span><span className="ml-2 font-mono text-xs text-muted-foreground">{item.policy_id.slice(0, 8)}</span></TableCell><TableCell className="capitalize">{item.decision.replaceAll("_", " ")}</TableCell><TableCell className="text-right tabular-nums">{item.count.toLocaleString()}</TableCell></TableRow>
            ))}</TableBody>
          </Table>
        </Card>
      ) : null}
    </SectionGroup>
  );
}

function InsightCountTable({ title, label, items }: { title: string; label: string; items: Array<{ key: string; label: string; count: number }> }) {
  if (items.length === 0) return <Card><CardContent><p className="text-sm text-muted-foreground">No {title.toLowerCase()} in this period.</p></CardContent></Card>;
  return (
    <Card>
      <CardContent className="pb-0"><p className="text-sm font-medium text-foreground">{title}</p></CardContent>
      <Table>
        <TableHeader><TableRow><TableHead>{label}</TableHead><TableHead className="text-right">Objections</TableHead></TableRow></TableHeader>
        <TableBody>{items.map((item) => <TableRow key={item.key}><TableCell className="capitalize">{item.label}</TableCell><TableCell className="text-right tabular-nums">{item.count.toLocaleString()}</TableCell></TableRow>)}</TableBody>
      </Table>
    </Card>
  );
}

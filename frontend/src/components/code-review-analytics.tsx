"use client";

import { ChartNoAxesColumnIncreasing } from "lucide-react";
import { EmptyState } from "@/components/empty-state";
import { SectionGroup } from "@/components/section-group";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { ErrorNotice } from "@/components/ui/error-notice";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import type {
  CodeReviewAnalytics,
  CodeReviewSizeBucket,
} from "@/lib/types";

const SIZE_BUCKET_LABELS: Record<CodeReviewSizeBucket, string> = {
  "0_49": "0–49 total lines",
  "50_199": "50–199 total lines",
  "200_499": "200–499 total lines",
  "500_plus": "500+ total lines",
};

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

function percentage(value: number, total: number): string {
  if (total <= 0) return "—";
  return `${Math.round((value / total) * 100)}%`;
}

function roundedMetric(value: number | null): string {
  if (value === null || !Number.isFinite(value)) return "—";
  return Math.round(value).toLocaleString();
}

function signedRoundedMetric(value: number | null, sign: "+" | "-"): string {
  const formatted = roundedMetric(value);
  return formatted === "—" ? formatted : `${sign}${formatted}`;
}

function reasonLabel(code: string): string {
  const known = NON_APPROVAL_REASON_LABELS[code];
  if (known) return known;
  const readable = code.replaceAll("_", " ");
  return readable.charAt(0).toUpperCase() + readable.slice(1);
}

function MetricCard({
  label,
  value,
  context,
}: {
  label: string;
  value: string;
  context: string;
}) {
  return (
    <Card>
      <CardContent className="space-y-1.5">
        <p className="text-xs font-medium text-muted-foreground">{label}</p>
        <p className="text-2xl font-semibold tabular-nums text-foreground">{value}</p>
        <p className="text-xs text-muted-foreground">{context}</p>
      </CardContent>
    </Card>
  );
}

function LoadingReport() {
  return (
    <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4" aria-label="Loading code review analytics">
      {["Reviews completed", "Automatically approved", "Not approved", "Approval rate"].map((label) => (
        <MetricCard key={label} label={label} value="—" context="Loading selected time window" />
      ))}
    </div>
  );
}

function ApprovalOutcomeCards({ summary }: { summary: CodeReviewAnalytics["summary"] }) {
  return (
    <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4" aria-label="Approval outcomes">
      <MetricCard
        label="Reviews completed"
        value={summary.reviews_completed.toLocaleString()}
        context={`${summary.reviews_requested.toLocaleString()} total attempts`}
      />
      <MetricCard
        label="Automatically approved"
        value={summary.automatically_approved.toLocaleString()}
        context={`${percentage(summary.automatically_approved, summary.reviews_completed)} of completed reviews`}
      />
      <MetricCard
        label="Not approved"
        value={summary.not_approved.toLocaleString()}
        context={`${percentage(summary.not_approved, summary.reviews_completed)} of completed reviews`}
      />
      <MetricCard
        label="Approval rate"
        value={percentage(summary.automatically_approved, summary.reviews_completed)}
        context={`${summary.failed_reviews.toLocaleString()} failed · ${summary.stale_reviews.toLocaleString()} stale`}
      />
    </div>
  );
}

export function CodeReviewAnalyticsReport({
  analytics,
  isLoading,
  isError,
  onRetry,
}: {
  analytics?: CodeReviewAnalytics;
  isLoading: boolean;
  isError: boolean;
  onRetry: () => void;
}) {
  if (!analytics && isLoading) {
    return <LoadingReport />;
  }
  if (!analytics) {
    return (
      <ErrorNotice
        title="Analytics unavailable"
        description="The selected code review report could not be loaded."
        action={{ label: "Retry", onClick: onRetry }}
      />
    );
  }

  const { summary } = analytics;
  if (summary.reviews_requested === 0) {
    return (
      <div className="space-y-3">
        {isError ? (
          <ErrorNotice
            title="Analytics may be out of date"
            description="Showing the last successful report because the latest refresh failed."
            action={{ label: "Retry", onClick: onRetry }}
          />
        ) : null}
        <EmptyState
          icon={ChartNoAxesColumnIncreasing}
          title="No review attempts in this time window"
          description="Choose a longer time window or another repository to analyze approval behavior."
        />
      </div>
    );
  }

  if (summary.reviews_completed === 0) {
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
        <EmptyState
          icon={ChartNoAxesColumnIncreasing}
          title="No completed reviews in this time window"
          description="The attempts in this window failed or became stale before reaching an approval decision."
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

      <SectionGroup
        title="Usage by PR author"
        description="Who has the most completed review assessments on their pull requests, and how often those assessments lead to an automatic approval."
      >
        {analytics.authors.length === 0 ? (
          <p className="py-6 text-center text-sm text-muted-foreground">No author attribution is available for this report.</p>
        ) : (
          <Card className="overflow-x-auto">
            <Table aria-label="Code review analytics by PR author">
              <TableHeader>
                <TableRow>
                  <TableHead>PR author</TableHead>
                  <TableHead className="text-right">Reviews</TableHead>
                  <TableHead className="text-right">Approved</TableHead>
                  <TableHead className="text-right">Not approved</TableHead>
                  <TableHead className="text-right">Approval rate</TableHead>
                  <TableHead className="text-right">Median additions</TableHead>
                  <TableHead className="text-right">Median deletions</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {analytics.authors.map((author) => (
                  <TableRow key={author.author}>
                    <TableCell className="font-medium">{author.author}</TableCell>
                    <TableCell className="text-right tabular-nums">{author.reviews_completed.toLocaleString()}</TableCell>
                    <TableCell className="text-right tabular-nums">{author.automatically_approved.toLocaleString()}</TableCell>
                    <TableCell className="text-right tabular-nums">{author.not_approved.toLocaleString()}</TableCell>
                    <TableCell className="text-right tabular-nums">
                      {percentage(author.automatically_approved, author.reviews_completed)}
                    </TableCell>
                    <TableCell className="text-right tabular-nums">{signedRoundedMetric(author.median_additions, "+")}</TableCell>
                    <TableCell className="text-right tabular-nums">{signedRoundedMetric(author.median_deletions, "-")}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </Card>
        )}
      </SectionGroup>

      <SectionGroup
        title="PR size and policy fit"
        description={`Addition and deletion metrics include ${summary.reviews_with_change_breakdown.toLocaleString()} of ${summary.reviews_completed.toLocaleString()} completed reviews with a captured breakdown. Total-line buckets and policy limits use additions plus deletions; total-line and file data is available for ${summary.reviews_with_size_data.toLocaleString()} reviews.`}
      >
        <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
          <MetricCard
            label="Average additions"
            value={roundedMetric(summary.average_additions)}
            context="Lines added per completed review"
          />
          <MetricCard
            label="Median additions"
            value={roundedMetric(summary.median_additions)}
            context="Less sensitive to unusually additive PRs"
          />
          <MetricCard
            label="Average deletions"
            value={roundedMetric(summary.average_deletions)}
            context="Lines deleted per completed review"
          />
          <MetricCard
            label="Median deletions"
            value={roundedMetric(summary.median_deletions)}
            context="Less sensitive to unusually subtractive PRs"
          />
          <MetricCard
            label="Median files changed"
            value={roundedMetric(summary.median_files_changed)}
            context={`Average ${roundedMetric(summary.average_files_changed)} files`}
          />
          <MetricCard
            label="Above captured size limits"
            value={summary.reviews_above_size_limit.toLocaleString()}
            context={`${summary.approvals_above_size_limit.toLocaleString()} were automatically approved`}
          />
        </div>
        {analytics.size_buckets.length > 0 ? (
          <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4" aria-label="Approval rate by pull request size">
            {analytics.size_buckets.map((bucket) => (
              <Card key={bucket.bucket}>
                <CardContent className="space-y-2">
                  <p className="text-sm font-medium text-foreground">{SIZE_BUCKET_LABELS[bucket.bucket]}</p>
                  <div className="flex items-baseline justify-between gap-3">
                    <span className="text-2xl font-semibold tabular-nums">
                      {percentage(bucket.automatically_approved, bucket.reviews_completed)}
                    </span>
                    <Badge variant="secondary">{bucket.reviews_completed.toLocaleString()} reviews</Badge>
                  </div>
                  <p className="text-xs text-muted-foreground">
                    {bucket.automatically_approved.toLocaleString()} automatically approved
                  </p>
                </CardContent>
              </Card>
            ))}
          </div>
        ) : null}
      </SectionGroup>

      <div className="grid gap-6 xl:grid-cols-2">
        <SectionGroup
          title="Why reviews were not approved"
          description="Most common captured policy or reviewer signals. One review can contribute to more than one reason; older reviews without structured reason data do not contribute."
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
                    <Badge variant="secondary">{reason.reviews.toLocaleString()}</Badge>
                  </div>
                ))}
              </CardContent>
            </Card>
          )}
        </SectionGroup>

        <SectionGroup
          title="Review findings"
          description="How often the reviewer surfaced code-level concerns, separate from deterministic policy safeguards."
        >
          <div className="grid gap-3 sm:grid-cols-3 xl:grid-cols-1 2xl:grid-cols-3">
            <MetricCard
              label="Reviews with findings"
              value={summary.reviews_with_findings.toLocaleString()}
              context={`${percentage(summary.reviews_with_findings, summary.reviews_completed)} of completed reviews`}
            />
            <MetricCard
              label="Blocking findings"
              value={summary.reviews_with_blocking_findings.toLocaleString()}
              context="Reviews with at least one P0 or P1"
            />
            <MetricCard
              label="Findings per review"
              value={(summary.total_findings / summary.reviews_completed).toFixed(1)}
              context={`${summary.total_findings.toLocaleString()} findings total`}
            />
          </div>
          <p className="text-xs text-muted-foreground">
            Completed decisions: {summary.needs_human_review.toLocaleString()} needed human review,{" "}
            {summary.comment_only.toLocaleString()} were comment-only, {summary.blocked.toLocaleString()} were blocked, and{" "}
            {summary.approval_not_posted.toLocaleString()} approval decisions were not posted.
          </p>
        </SectionGroup>
      </div>
    </div>
  );
}

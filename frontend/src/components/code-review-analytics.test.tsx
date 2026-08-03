import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { CodeReviewAnalyticsReport } from "@/components/code-review-analytics";
import type { CodeReviewAnalytics } from "@/lib/types";

function emptyAnalytics(prsReviewed = 0): CodeReviewAnalytics {
  return {
    summary: {
      prs_reviewed: prsReviewed,
      prs_with_completed_round: 0,
      approved_by_143: 0,
      not_approved: 0,
      approved_first_round: 0,
      median_rounds_to_approval: null,
      needs_human_review: 0,
      comment_only: 0,
      blocked: 0,
      approval_not_posted: 0,
      prs_with_failed_attempt: prsReviewed,
      prs_with_stale_attempt: 0,
      prs_with_change_breakdown: 0,
      median_additions: null,
      median_deletions: null,
      prs_with_findings: 0,
      prs_with_blocking_findings: 0,
      total_findings: 0,
    },
    approval_rounds: [
      { bucket: "round_1", prs: 0 },
      { bucket: "round_2", prs: 0 },
      { bucket: "round_3", prs: 0 },
      { bucket: "round_4_plus", prs: 0 },
      { bucket: "not_yet_approved", prs: prsReviewed },
    ],
    authors: prsReviewed > 0 ? [{
      author: "Unknown",
      prs_reviewed: prsReviewed,
      approved_by_143: 0,
      not_approved: 0,
      approved_first_round: 0,
      median_rounds_to_approval: null,
      median_additions: null,
      median_deletions: null,
    }] : [],
    non_approval_reasons: [],
  };
}

function renderReport(analytics: CodeReviewAnalytics) {
  render(
    <CodeReviewAnalyticsReport
      analytics={analytics}
      isLoading={false}
      isError={false}
      onRetry={vi.fn()}
      authorSort="reviews"
      authorSortOrder="desc"
      onAuthorSort={vi.fn()}
      reviewLinkFilters={{ range: "30d" }}
      filters={null}
    />,
  );
}

describe("CodeReviewAnalyticsReport PR cohort states", () => {
  it("shows headline metric definitions in tooltips without subdescriptions", async () => {
    const user = userEvent.setup();
    const analytics = emptyAnalytics(4);
    analytics.summary.approved_by_143 = 2;
    analytics.summary.median_rounds_to_approval = 2;
    renderReport(analytics);

    const outcomes = screen.getByLabelText("Approval outcomes");
    expect(within(outcomes).queryByText("First sent to 143 in this period")).not.toBeInTheDocument();
    expect(within(outcomes).queryByText("50% of PRs reviewed")).not.toBeInTheDocument();
    expect(within(outcomes).queryByText("Approved PRs only")).not.toBeInTheDocument();

    const trigger = within(outcomes).getByRole("button", { name: "About Median rounds to approval" });
    await user.hover(trigger);
    expect(await screen.findByRole("tooltip")).toHaveTextContent(
      "The median number of distinct completed revisions before 143 first posted an approval, among approved pull requests.",
    );

    expect(within(outcomes).getByRole("button", { name: "About PRs reviewed" })).toBeInTheDocument();
    expect(within(outcomes).getByRole("button", { name: "About Approved by 143" })).toBeInTheDocument();
    expect(within(outcomes).getByRole("button", { name: "About Approval rate" })).toBeInTheDocument();
  });

  it("shows PR-oriented empty copy when the cohort has no PRs", () => {
    renderReport(emptyAnalytics());

    expect(screen.getByText("No PRs first sent to 143 in this time window")).toBeInTheDocument();
    expect(screen.getByText(/another repository to analyze PR outcomes/)).toBeInTheDocument();
  });

  it("shows every round bucket and an empty median when no PR has approval", () => {
    renderReport(emptyAnalytics(3));

    const outcomes = screen.getByLabelText("Approval outcomes");
    expect(within(outcomes).getByText("Median rounds to approval")).toBeInTheDocument();
    expect(within(outcomes).getByText("—")).toBeInTheDocument();

    const rounds = screen.getByLabelText("Approval by round");
    for (const label of [
      "Approved in round 1",
      "Approved in round 2",
      "Approved in round 3",
      "Approved in round 4+",
      "Not yet approved",
    ]) {
      expect(within(rounds).getByText(label)).toBeInTheDocument();
    }
    expect(within(rounds).getByText("3")).toBeInTheDocument();
    expect(screen.getByText(/3 PRs had a failed attempt/)).toBeInTheDocument();
  });

  it("shows median rounds to approval with one decimal place", () => {
    const analytics = emptyAnalytics(3);
    analytics.summary.approved_by_143 = 2;
    analytics.summary.median_rounds_to_approval = 1.5;
    analytics.authors[0]!.approved_by_143 = 2;
    analytics.authors[0]!.median_rounds_to_approval = 1.5;
    renderReport(analytics);

    const outcomes = screen.getByLabelText("Approval outcomes");
    expect(within(outcomes).getByText("1.5")).toBeInTheDocument();

    const authorTable = screen.getByRole("table", { name: "Code review analytics by PR author" });
    expect(within(authorTable).getAllByText("1.5")).toHaveLength(2);
    expect(within(authorTable).getByLabelText("1.5 median rounds to approval overall")).toHaveTextContent("1.5");
  });

  it("reports how many PRs backed the author medians", () => {
    const analytics = emptyAnalytics(4);
    analytics.summary.prs_with_change_breakdown = 1;
    renderReport(analytics);

    expect(screen.getByText(/1 of 4 PRs whose representative assessment captured a change/))
      .toBeInTheDocument();
  });
});

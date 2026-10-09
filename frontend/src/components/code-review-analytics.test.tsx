import { render, screen, waitFor, within } from "@testing-library/react";
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
      average_rounds_to_approval: null,
      p95_rounds_to_approval: null,
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
    comment_requests_total: 0,
    comment_requests_by_user: [],
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
  it("distinguishes PR approval from review approval and defines the median population", async () => {
    const user = userEvent.setup();
    const analytics = emptyAnalytics(4);
    analytics.summary.approved_by_143 = 2;
    analytics.summary.median_rounds_to_approval = 2;
    renderReport(analytics);

    const outcomes = screen.getByLabelText("Approval outcomes");
    expect(within(outcomes).getByText("Unique PRs first sent in this period")).toBeInTheDocument();
    expect(within(outcomes).getByText("2 of 4 unique PRs")).toBeInTheDocument();
    expect(within(outcomes).getByText("50%")).toBeInTheDocument();
    expect(within(outcomes).getAllByText("Approved PRs only")).toHaveLength(3);

    const trigger = within(outcomes).getByRole("button", { name: "About Median rounds to approval" });
    await user.hover(trigger);
    expect(await screen.findByRole("tooltip")).toHaveTextContent(
      "The median number of completed review sessions up to and including the first posted approval, among approved PRs. Repeat reviews of the same revision count separately; failed, stale, cancelled, and unfinished reviews do not count.",
    );

    expect(within(outcomes).getByRole("button", { name: "About PRs reviewed" })).toBeInTheDocument();
    const approvalTrigger = within(outcomes).getByRole("button", { name: "About Automatically approved" });
    await user.unhover(trigger);
    await user.hover(approvalTrigger);
    await waitFor(() => expect(screen.getByRole("tooltip")).toHaveTextContent(
      "Each PR counts once. Evidence-only rechecks are excluded.",
    ));
    const rateTrigger = within(outcomes).getByRole("button", { name: "About PR approval rate" });
    await user.unhover(approvalTrigger);
    await user.hover(rateTrigger);
    await waitFor(() => expect(screen.getByRole("tooltip")).toHaveTextContent(
      "Automatically approved PRs divided by unique PRs first sent to 143 during the selected period. Uses all later rounds and includes PRs still awaiting approval. The Reviews tab counts completed review sessions instead.",
    ));
    await user.unhover(rateTrigger);
    await user.hover(within(outcomes).getByRole("button", { name: "About P95 rounds to approval" }));
    await waitFor(() => expect(screen.getByRole("tooltip")).toHaveTextContent(
      "At least 95% of approved PRs received their first posted approval within this many completed review sessions.",
    ));
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
    expect(within(outcomes).getAllByText("—")).toHaveLength(3);

    const rounds = screen.getByLabelText("Approval by round");
    expect(screen.getByText(
      "Each PR appears once, by the number of completed reviews up to and including its first posted 143 approval. Repeat reviews of the same revision count separately.",
    )).toBeInTheDocument();
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

    const authorUsage = screen.getByText("Usage by PR author");
    const approvalByRound = screen.getByText("Approval by round");
    expect(outcomes.compareDocumentPosition(authorUsage) & Node.DOCUMENT_POSITION_FOLLOWING)
      .toBeTruthy();
    expect(authorUsage.compareDocumentPosition(approvalByRound) & Node.DOCUMENT_POSITION_FOLLOWING)
      .toBeTruthy();
  });

  it("shows median rounds to approval with one decimal place", () => {
    const analytics = emptyAnalytics(3);
    analytics.summary.approved_by_143 = 2;
    analytics.summary.median_rounds_to_approval = 1.5;
    analytics.summary.average_rounds_to_approval = 2.3333333333333335;
    analytics.summary.p95_rounds_to_approval = 4;
    analytics.authors[0]!.approved_by_143 = 2;
    analytics.authors[0]!.median_rounds_to_approval = 1.5;
    renderReport(analytics);

    const outcomes = screen.getByLabelText("Approval outcomes");
    expect(within(outcomes).getByText("1.5")).toBeInTheDocument();
    expect(within(outcomes).getByText("Average rounds to approval")).toBeInTheDocument();
    expect(within(outcomes).getByText("2.3")).toBeInTheDocument();
    expect(within(outcomes).getByText("P95 rounds to approval")).toBeInTheDocument();
    expect(within(outcomes).getByText("4")).toBeInTheDocument();

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

  it("shows direct comment request totals grouped by GitHub user", () => {
    const analytics = emptyAnalytics(4);
    analytics.comment_requests_total = 5;
    analytics.comment_requests_by_user = [
      { github_login: "anya", requests: 3 },
      { github_login: "sam", requests: 2 },
    ];
    renderReport(analytics);

    expect(screen.getByText("Direct review requests by user")).toBeInTheDocument();
    expect(screen.getByText("Direct comment requests")).toBeInTheDocument();
    const requestTable = screen.getByRole("table", { name: "Direct code review requests by GitHub user" });
    expect(within(requestTable).getByText("anya")).toBeInTheDocument();
    expect(within(requestTable).getByText("sam")).toBeInTheDocument();
    expect(within(requestTable).getByLabelText("5 direct comment requests overall")).toHaveTextContent("5");
  });

  it("shows an inline empty state when the cohort has no direct comment requests", () => {
    renderReport(emptyAnalytics(2));

    expect(screen.getByText("No direct comment requests were captured for this PR cohort.")).toBeInTheDocument();
  });
});

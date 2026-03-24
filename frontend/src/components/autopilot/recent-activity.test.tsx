import { describe, it, expect, vi } from "vitest";
import { renderWithProviders, screen } from "@/test/test-utils";
import { RecentActivity } from "./recent-activity";
import type { PMPlan, PMDecisionSummary } from "@/lib/types";

vi.mock("next/navigation", () => ({
  usePathname: () => "/autopilot",
  useRouter: () => ({
    push: vi.fn(),
    replace: vi.fn(),
  }),
}));

function makePlan(overrides: Partial<PMPlan> = {}): PMPlan {
  return {
    id: "plan-1",
    org_id: "org-1",
    status: "completed",
    analysis: "analysis",
    tasks: [],
    clusters: [],
    skipped_issues: [],
    issues_reviewed: 0,
    triggered_by: "user",
    created_at: new Date().toISOString(),
    ...overrides,
  };
}

describe("RecentActivity", () => {
  it("shows empty state when no plans", () => {
    renderWithProviders(
      <RecentActivity plans={[]} summary={undefined} />
    );

    expect(screen.getByText("Recent activity")).toBeInTheDocument();
    expect(
      screen.getByText("No activity yet. Run an analysis to get started.")
    ).toBeInTheDocument();
  });

  it('shows "Today" for plans created today', () => {
    const plan = makePlan({
      issues_reviewed: 5,
      created_at: new Date().toISOString(),
    });

    renderWithProviders(
      <RecentActivity plans={[plan]} summary={undefined} />
    );

    expect(screen.getByText("Today")).toBeInTheDocument();
  });

  it("shows total issues reviewed", () => {
    const plans = [
      makePlan({ id: "p1", issues_reviewed: 7 }),
      makePlan({ id: "p2", issues_reviewed: 3 }),
    ];

    renderWithProviders(
      <RecentActivity plans={plans} summary={undefined} />
    );

    expect(screen.getByText(/Analyzed 10 issues/)).toBeInTheDocument();
  });

  it("shows delegated count when present", () => {
    const plan = makePlan({
      issues_reviewed: 5,
      tasks: [
        { rank: 1, issue_ids: ["i1"], title: "Fix bug", reasoning: "", approach: "", risk: "low", complexity: "simple", confidence: "high", status: "delegated" },
        { rank: 2, issue_ids: ["i2"], title: "Add feature", reasoning: "", approach: "", risk: "low", complexity: "simple", confidence: "high", status: "pending" },
        { rank: 3, issue_ids: ["i3"], title: "Refactor", reasoning: "", approach: "", risk: "low", complexity: "simple", confidence: "high", status: "delegated" },
      ],
    });

    renderWithProviders(
      <RecentActivity plans={[plan]} summary={undefined} />
    );

    expect(screen.getByText(/2 delegated/)).toBeInTheDocument();
  });

  it("shows skipped count when present", () => {
    const plan = makePlan({
      issues_reviewed: 4,
      skipped_issues: [
        { issue_id: "i1", reason: "too complex", detail: "" },
        { issue_id: "i2", reason: "duplicate", detail: "" },
        { issue_id: "i3", reason: "not actionable", detail: "" },
      ],
    });

    renderWithProviders(
      <RecentActivity plans={[plan]} summary={undefined} />
    );

    expect(screen.getByText(/3 skipped/)).toBeInTheDocument();
  });

  it("shows overall summary when summary has delegated tasks", () => {
    const plan = makePlan({ issues_reviewed: 5 });
    const summary: PMDecisionSummary = {
      total_delegated: 10,
      succeeded: 7,
      failed: 2,
      still_open: 1,
    };

    renderWithProviders(
      <RecentActivity plans={[plan]} summary={summary} />
    );

    expect(
      screen.getByText("Overall: 7/10 sessions succeeded")
    ).toBeInTheDocument();
  });

  it("hides overall summary when no summary provided", () => {
    const plan = makePlan({ issues_reviewed: 5 });

    renderWithProviders(
      <RecentActivity plans={[plan]} summary={undefined} />
    );

    expect(screen.queryByText(/Overall:/)).not.toBeInTheDocument();
  });

  it("hides overall summary when summary has 0 delegated", () => {
    const plan = makePlan({ issues_reviewed: 5 });
    const summary: PMDecisionSummary = {
      total_delegated: 0,
      succeeded: 0,
      failed: 0,
      still_open: 0,
    };

    renderWithProviders(
      <RecentActivity plans={[plan]} summary={summary} />
    );

    expect(screen.queryByText(/Overall:/)).not.toBeInTheDocument();
  });
});

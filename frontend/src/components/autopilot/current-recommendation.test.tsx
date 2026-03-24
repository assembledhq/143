import { describe, it, expect, vi } from "vitest";
import { renderWithProviders, screen, userEvent } from "@/test/test-utils";
import { CurrentRecommendation } from "./current-recommendation";
import type { PMPlan } from "@/lib/types";

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
    analysis: "Found 5 critical issues across 3 repositories.",
    tasks: [
      {
        rank: 1,
        issue_ids: ["issue-aaa-111"],
        title: "Fix auth regression",
        reasoning: "Blocking users from logging in",
        approach: "Patch the OAuth flow",
        risk: "Low",
        complexity: "Medium",
        confidence: "high",
        session_id: "session-1",
        status: "delegated",
      },
      {
        rank: 2,
        issue_ids: ["issue-bbb-222"],
        title: "Update rate limiter",
        reasoning: "Causing 429 errors for customers",
        approach: "Adjust token bucket config",
        risk: "Medium",
        complexity: "Low",
        confidence: "medium",
        session_id: "session-2",
        status: "pending",
      },
    ],
    clusters: [
      {
        issue_ids: ["issue-ccc-333", "issue-ddd-444"],
        root_cause: "Shared database connection pool exhaustion",
        strategy: "Increase pool size and add connection timeout",
      },
    ],
    skipped_issues: [
      {
        issue_id: "issue-eee-555",
        reason: "low_priority",
        detail: "Cosmetic UI alignment issue with no user impact",
      },
    ],
    issues_reviewed: 10,
    triggered_by: "manual",
    created_at: "2026-03-20T10:00:00Z",
    completed_at: "2026-03-20T10:05:00Z",
    ...overrides,
  };
}

describe("CurrentRecommendation", () => {
  it("shows empty state when no plan is provided", () => {
    renderWithProviders(<CurrentRecommendation plan={undefined} />);

    expect(
      screen.getByText(
        "Run my first analysis and I'll tell you which ones matter most..."
      )
    ).toBeInTheDocument();
  });

  it("shows analysis text from plan", () => {
    renderWithProviders(<CurrentRecommendation plan={makePlan()} />);

    expect(screen.getByText("Situation analysis")).toBeInTheDocument();
    expect(
      screen.getByText("Found 5 critical issues across 3 repositories.")
    ).toBeInTheDocument();
  });

  it('shows "No analysis provided." when plan.analysis is empty', () => {
    renderWithProviders(
      <CurrentRecommendation plan={makePlan({ analysis: "" })} />
    );

    expect(screen.getByText("No analysis provided.")).toBeInTheDocument();
  });

  it("shows priority tasks with correct count badge", () => {
    renderWithProviders(<CurrentRecommendation plan={makePlan()} />);

    expect(screen.getByText("Priority tasks")).toBeInTheDocument();
    expect(screen.getByText("2 slots used")).toBeInTheDocument();
  });

  it("shows issue clusters when present", () => {
    renderWithProviders(<CurrentRecommendation plan={makePlan()} />);

    expect(screen.getByText("Issue clusters")).toBeInTheDocument();
    expect(
      screen.getByText("Shared database connection pool exhaustion")
    ).toBeInTheDocument();
    expect(
      screen.getByText("Increase pool size and add connection timeout")
    ).toBeInTheDocument();
    // Cluster issue IDs are sliced to 8 chars
    expect(screen.getByText("issue-cc")).toBeInTheDocument();
    expect(screen.getByText("issue-dd")).toBeInTheDocument();
  });

  it("hides clusters section when plan.clusters is empty", () => {
    renderWithProviders(
      <CurrentRecommendation plan={makePlan({ clusters: [] })} />
    );

    expect(screen.queryByText("Issue clusters")).not.toBeInTheDocument();
  });

  it("shows skipped issues toggle when present", () => {
    renderWithProviders(<CurrentRecommendation plan={makePlan()} />);

    expect(screen.getByText("1 skipped issues")).toBeInTheDocument();
  });

  it("clicking the toggle reveals skipped issue details", async () => {
    const user = userEvent.setup();
    renderWithProviders(<CurrentRecommendation plan={makePlan()} />);

    // Details should not be visible initially
    expect(
      screen.queryByText("Cosmetic UI alignment issue with no user impact")
    ).not.toBeInTheDocument();

    // Click the toggle button
    await user.click(screen.getByText("1 skipped issues"));

    // Now details should be visible
    expect(
      screen.getByText("Cosmetic UI alignment issue with no user impact")
    ).toBeInTheDocument();
    // Issue ID badge (sliced to 8 chars)
    expect(screen.getByText("issue-ee")).toBeInTheDocument();
    // Reason badge (underscores replaced with spaces)
    expect(screen.getByText("low priority")).toBeInTheDocument();
  });

  it("hides skipped section when plan.skipped_issues is empty", () => {
    renderWithProviders(
      <CurrentRecommendation plan={makePlan({ skipped_issues: [] })} />
    );

    expect(screen.queryByText(/skipped issues/)).not.toBeInTheDocument();
  });
});

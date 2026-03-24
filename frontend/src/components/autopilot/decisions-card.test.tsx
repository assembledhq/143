import { describe, it, expect, vi } from "vitest";
import { renderWithProviders, screen } from "@/test/test-utils";
import { DecisionsCard } from "./decisions-card";
import type { PMDecisionView } from "@/lib/types";

vi.mock("next/navigation", () => ({
  usePathname: () => "/autopilot",
  useRouter: () => ({
    push: vi.fn(),
    replace: vi.fn(),
  }),
}));

function makeDecision(overrides: Partial<PMDecisionView> = {}): PMDecisionView {
  return {
    id: "d-1",
    plan_id: "p-1",
    decision: "delegate",
    reasoning: "High impact issue",
    created_at: "2026-03-15T10:00:00Z",
    ...overrides,
  };
}

describe("DecisionsCard", () => {
  it("shows loading state", () => {
    renderWithProviders(
      <DecisionsCard decisions={[]} isLoading={true} />,
    );

    expect(screen.getByText("Loading decisions...")).toBeInTheDocument();
  });

  it("shows empty state when no decisions", () => {
    renderWithProviders(
      <DecisionsCard decisions={[]} isLoading={false} />,
    );

    expect(
      screen.getByText(
        "No decisions yet. Run an analysis to start building decision history.",
      ),
    ).toBeInTheDocument();
  });

  it("shows decisions with issue titles", () => {
    const decisions: PMDecisionView[] = [
      makeDecision({ id: "d-1", issue_title: "Fix billing timeout" }),
      makeDecision({ id: "d-2", issue_title: "Refactor auth module" }),
    ];

    renderWithProviders(
      <DecisionsCard decisions={decisions} isLoading={false} />,
    );

    expect(screen.getByText("Fix billing timeout")).toBeInTheDocument();
    expect(screen.getByText("Refactor auth module")).toBeInTheDocument();
  });

  it("shows Delegated / Skipped / Clustered badges correctly", () => {
    const decisions: PMDecisionView[] = [
      makeDecision({ id: "d-1", decision: "delegate", issue_title: "Task A" }),
      makeDecision({ id: "d-2", decision: "skip", issue_title: "Task B" }),
      makeDecision({ id: "d-3", decision: "cluster", issue_title: "Task C" }),
    ];

    renderWithProviders(
      <DecisionsCard decisions={decisions} isLoading={false} />,
    );

    expect(screen.getByText("Delegated")).toBeInTheDocument();
    expect(screen.getByText("Skipped")).toBeInTheDocument();
    expect(screen.getByText("Clustered")).toBeInTheDocument();
  });

  it("shows outcome correctly (Succeeded / Failed / Still open)", () => {
    const decisions: PMDecisionView[] = [
      makeDecision({ id: "d-1", outcome: "succeeded", issue_title: "A" }),
      makeDecision({ id: "d-2", outcome: "failed", issue_title: "B" }),
      makeDecision({ id: "d-3", outcome: undefined, issue_title: "C" }),
    ];

    renderWithProviders(
      <DecisionsCard decisions={decisions} isLoading={false} />,
    );

    expect(screen.getByText("Succeeded")).toBeInTheDocument();
    expect(screen.getByText("Failed")).toBeInTheDocument();
    expect(screen.getByText("Still open")).toBeInTheDocument();
  });

  it("shows overflow count when more than 5 decisions", () => {
    const decisions: PMDecisionView[] = Array.from({ length: 8 }, (_, i) =>
      makeDecision({ id: `d-${i}`, issue_title: `Issue ${i + 1}` }),
    );

    renderWithProviders(
      <DecisionsCard decisions={decisions} isLoading={false} />,
    );

    expect(screen.getByText("3 more decisions not shown")).toBeInTheDocument();
  });

  it("truncates to 5 decisions max", () => {
    const decisions: PMDecisionView[] = Array.from({ length: 7 }, (_, i) =>
      makeDecision({ id: `d-${i}`, issue_title: `Issue ${i + 1}` }),
    );

    renderWithProviders(
      <DecisionsCard decisions={decisions} isLoading={false} />,
    );

    // First 5 should be visible
    for (let i = 1; i <= 5; i++) {
      expect(screen.getByText(`Issue ${i}`)).toBeInTheDocument();
    }
    // 6th and 7th should not be rendered
    expect(screen.queryByText("Issue 6")).not.toBeInTheDocument();
    expect(screen.queryByText("Issue 7")).not.toBeInTheDocument();
  });
});

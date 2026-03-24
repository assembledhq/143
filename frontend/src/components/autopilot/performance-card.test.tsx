import { describe, it, expect, vi } from "vitest";
import { renderWithProviders, screen } from "@/test/test-utils";
import { PerformanceCard } from "./performance-card";
import type { PMDecisionSummary } from "@/lib/types";

vi.mock("next/navigation", () => ({
  usePathname: () => "/autopilot",
  useRouter: () => ({
    push: vi.fn(),
    replace: vi.fn(),
  }),
}));

describe("PerformanceCard", () => {
  it("shows empty state when summary is undefined", () => {
    renderWithProviders(<PerformanceCard summary={undefined} />);

    expect(
      screen.getByText(
        "No delegated tasks yet. Performance data will appear after the PM agent runs.",
      ),
    ).toBeInTheDocument();
  });

  it("shows empty state when total_delegated is 0", () => {
    const summary: PMDecisionSummary = {
      total_delegated: 0,
      succeeded: 0,
      failed: 0,
      still_open: 0,
    };

    renderWithProviders(<PerformanceCard summary={summary} />);

    expect(
      screen.getByText(
        "No delegated tasks yet. Performance data will appear after the PM agent runs.",
      ),
    ).toBeInTheDocument();
  });

  it("shows success rate percentage", () => {
    const summary: PMDecisionSummary = {
      total_delegated: 10,
      succeeded: 7,
      failed: 2,
      still_open: 1,
    };

    renderWithProviders(<PerformanceCard summary={summary} />);

    expect(screen.getByText("70%")).toBeInTheDocument();
    expect(screen.getByText("success rate")).toBeInTheDocument();
  });

  it("shows succeeded/failed badge counts", () => {
    const summary: PMDecisionSummary = {
      total_delegated: 10,
      succeeded: 8,
      failed: 2,
      still_open: 0,
    };

    renderWithProviders(<PerformanceCard summary={summary} />);

    expect(screen.getByText("8 succeeded")).toBeInTheDocument();
    expect(screen.getByText("2 failed")).toBeInTheDocument();
  });

  it("shows 'still open' badge only when > 0", () => {
    const summaryWithOpen: PMDecisionSummary = {
      total_delegated: 10,
      succeeded: 6,
      failed: 1,
      still_open: 3,
    };

    const { unmount } = renderWithProviders(
      <PerformanceCard summary={summaryWithOpen} />,
    );

    expect(screen.getByText("3 still open")).toBeInTheDocument();
    unmount();

    const summaryWithoutOpen: PMDecisionSummary = {
      total_delegated: 10,
      succeeded: 8,
      failed: 2,
      still_open: 0,
    };

    renderWithProviders(
      <PerformanceCard summary={summaryWithoutOpen} />,
    );

    expect(screen.queryByText(/still open/)).not.toBeInTheDocument();
  });

  it("shows delegated fraction text", () => {
    const summary: PMDecisionSummary = {
      total_delegated: 12,
      succeeded: 9,
      failed: 2,
      still_open: 1,
    };

    renderWithProviders(<PerformanceCard summary={summary} />);

    expect(
      screen.getByText("9/12 delegated tasks succeeded"),
    ).toBeInTheDocument();
  });
});

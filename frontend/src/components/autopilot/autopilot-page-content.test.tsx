import { describe, expect, it, vi } from "vitest";
import { renderWithProviders, screen } from "@/test/test-utils";
import { AutopilotPageContent } from "./autopilot-page-content";
import type { AutopilotQueueRow } from "@/lib/types";

const replaceMock = vi.fn();

vi.mock("next/navigation", () => ({
  useRouter: () => ({
    push: vi.fn(),
    replace: replaceMock,
  }),
}));

vi.mock("@/hooks/use-analyze", () => ({
  useAnalyze: () => ({
    handleAnalyze: vi.fn(),
    isAnalyzing: false,
    isPending: false,
  }),
}));

const queueRow: AutopilotQueueRow = {
  id: "issue-1",
  rank: 1,
  source: { type: "sentry", key: "SENTRY-123456789" },
  title: "Mobile checkout descriptions should not overlap source badges",
  repo: { id: "repo-1", name: "web" },
  issue_status: "open",
  customer_impact: { label: "High", count: 42 },
  implementation_ease: "Easy",
  low_hanging_fruit: {
    label: "High",
    reasons: ["clear reproduction"],
    cluster_size: 1,
  },
  display_run_state: "not_started",
  available_action: "start_run",
};

vi.mock("./use-autopilot-page-data", () => ({
  useAutopilotPageData: () => ({
    isLoading: false,
    isSetupComplete: true,
    pmStatus: { is_running: false },
    settings: {},
    viewModel: {
      statusLine: "Ready",
      directionSummary: "No direction set",
      focusAreas: [],
      documentsSummary: "No documents",
      weightsSummary: "Default weights",
    },
    queue: {
      data: [queueRow],
      meta: {
        summary: {
          top_issue_id: "issue-1",
          autorunnable_count: 1,
          needs_review_count: 0,
          open_pr_count: 0,
          active_run_count: 0,
          ranked_issue_count: 1,
        },
      },
    },
    queueLoading: false,
    hasNextQueuePage: false,
    fetchNextQueuePage: vi.fn(),
    isFetchingNextQueuePage: false,
  }),
}));

vi.mock("./autopilot-steering-sheet", () => ({
  AutopilotSteeringSheet: () => null,
}));

vi.mock("./autopilot-documents-sheet", () => ({
  AutopilotDocumentsSheet: () => null,
}));

vi.mock("@/components/autopilot-proposal-card", () => ({
  AutopilotProposalCard: () => null,
}));

describe("AutopilotPageContent", () => {
  it("lets the queue table keep normal column widths on mobile", async () => {
    renderWithProviders(<AutopilotPageContent />);

    const table = screen.getByRole("table");
    expect(table).toHaveClass("w-full", "min-w-[64rem]", "table-auto");
    expect(table).not.toHaveClass("table-fixed");
    expect(await screen.findByText("Source")).toBeInTheDocument();
  });
});

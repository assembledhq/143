import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import { SessionDetailLoadingSkeleton } from "./session-detail-loading-skeleton";

describe("SessionDetailLoadingSkeleton", () => {
  it("renders an all-shimmer header when no metadata is known", () => {
    render(<SessionDetailLoadingSkeleton />);

    expect(screen.getByTestId("session-detail-loading-skeleton")).toBeInTheDocument();
    expect(screen.queryByRole("heading")).not.toBeInTheDocument();
  });

  it("renders known title, status, and agent in the header while the rest loads", () => {
    render(
      <SessionDetailLoadingSkeleton
        metadata={{
          title: "Fix the flaky deploy",
          statusLabel: "Running",
          statusColor: "bg-primary/10 text-primary",
          agentType: "claude_code",
        }}
      />,
    );

    expect(screen.getByTestId("session-detail-loading-skeleton")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Fix the flaky deploy" })).toBeInTheDocument();
    const statusPill = screen.getByText("Running");
    expect(statusPill).toHaveClass("bg-primary/10", "text-primary");
    expect(screen.getByText("Claude Code")).toBeInTheDocument();
  });

  it("keeps the conversation area as a skeleton even with metadata", () => {
    render(
      <SessionDetailLoadingSkeleton
        metadata={{
          title: "Fix the flaky deploy",
          statusLabel: "Running",
          statusColor: "bg-primary/10 text-primary",
        }}
      />,
    );

    expect(screen.getByTestId("session-timeline-skeleton")).toBeInTheDocument();
  });
});

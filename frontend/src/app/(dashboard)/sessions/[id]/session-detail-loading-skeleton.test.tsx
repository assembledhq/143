import { describe, it, expect } from "vitest";
import { render, screen, within } from "@testing-library/react";
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

  it("shows the title in the mobile top bar while the rest loads", () => {
    render(
      <SessionDetailLoadingSkeleton
        metadata={{
          title: "Fix the flaky deploy",
          statusLabel: "Running",
          statusColor: "bg-primary/10 text-primary",
        }}
      />,
    );

    const mobileBar = screen.getByTestId("session-detail-skeleton-mobile-top-bar");
    expect(within(mobileBar).getByText("Fix the flaky deploy")).toBeInTheDocument();
  });

  it("renders an all-shimmer mobile top bar when no metadata is known", () => {
    render(<SessionDetailLoadingSkeleton />);

    const mobileBar = screen.getByTestId("session-detail-skeleton-mobile-top-bar");
    expect(mobileBar).toBeInTheDocument();
    expect(mobileBar).toHaveTextContent("");
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

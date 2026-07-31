import { describe, it, expect, vi } from "vitest";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { SessionDetailLoadingSkeleton } from "./session-detail-loading-skeleton";
import {
  SESSION_COMPOSER_SURFACE_HEIGHT_CLASSNAME,
  SESSION_DETAIL_PANEL_DEFAULT_WIDTH,
  SESSION_THREAD_STRIP_HEIGHT_CLASSNAME,
  SESSION_WORKSPACE_MIN_WIDTH_CLASSNAME,
} from "./session-detail-geometry";

describe("SessionDetailLoadingSkeleton", () => {
  it("renders an all-shimmer header when no metadata is known", () => {
    render(<SessionDetailLoadingSkeleton />);

    expect(screen.getByTestId("session-detail-loading-skeleton")).toBeInTheDocument();
    expect(screen.getByTestId("session-detail-loading-skeleton")).toHaveAttribute(
      "data-session-transition",
      "initial",
    );
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
    expect(screen.getByTestId("session-detail-loading-skeleton")).toHaveAttribute(
      "data-session-transition",
      "provisional",
    );
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
    expect(screen.getByTestId("session-composer-loading")).toBeInTheDocument();
    // The composer is unusable because there is no textbox yet, not because
    // of an aria-disabled on a plain container (which AT ignores).
    expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
    expect(screen.getByTestId("session-composer-loading-surface")).toHaveClass(
      SESSION_COMPOSER_SURFACE_HEIGHT_CLASSNAME,
    );
  });

  it("preserves workspace geometry preferences during a provisional transition", () => {
    render(
      <SessionDetailLoadingSkeleton
        detailPanelOpen
        detailPanelWidth={428}
        metadata={{
          title: "Target session",
          statusLabel: "Running",
          statusColor: "bg-primary/10 text-primary",
        }}
      />,
    );

    expect(screen.getByTestId("session-conversation-workspace-loading")).toHaveClass(
      SESSION_WORKSPACE_MIN_WIDTH_CLASSNAME,
    );
    expect(screen.getByTestId("session-thread-strip-loading")).toHaveClass(
      SESSION_THREAD_STRIP_HEIGHT_CLASSNAME,
    );
    expect(screen.getByTestId("session-detail-panel-loading")).toHaveStyle({ width: "428px" });
  });

  it("defaults the reserved detail panel to the loaded workspace's default width", () => {
    render(<SessionDetailLoadingSkeleton />);

    expect(screen.getByTestId("session-detail-panel-loading")).toHaveStyle({
      width: `${SESSION_DETAIL_PANEL_DEFAULT_WIDTH}px`,
    });
  });

  it("does not reuse the loaded workspace's test ids", () => {
    render(<SessionDetailLoadingSkeleton />);

    expect(screen.queryByTestId("session-conversation-workspace")).not.toBeInTheDocument();
    expect(screen.queryByTestId("session-detail-panel")).not.toBeInTheDocument();
  });

  it("keeps a failed target in the stable frame and offers retry", () => {
    const onRetry = vi.fn();
    render(
      <SessionDetailLoadingSkeleton
        metadata={{
          title: "Target session",
          statusLabel: "Failed",
          statusColor: "bg-destructive/10 text-destructive",
        }}
        errorMessage="The detail request failed."
        onRetry={onRetry}
      />,
    );

    const frame = screen.getByTestId("session-detail-loading-skeleton");
    expect(screen.getByTestId("session-detail-transition-error")).toHaveTextContent(
      "The detail request failed.",
    );
    expect(frame).toHaveAttribute("data-session-state", "error");
    expect(frame).toHaveAttribute("data-session-transition", "provisional");
    expect(frame).not.toHaveAttribute("aria-busy");
    expect(screen.queryByTestId("session-timeline-skeleton")).not.toBeInTheDocument();
    // Metadata is still worth preserving, so the reserved chrome stays.
    expect(screen.getByRole("heading", { name: "Target session" })).toBeInTheDocument();
    expect(screen.getByTestId("session-composer-loading")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(onRetry).toHaveBeenCalledOnce();
  });

  it("marks the retry pending while the refetch is in flight", () => {
    const onRetry = vi.fn();
    render(
      <SessionDetailLoadingSkeleton
        metadata={{
          title: "Target session",
          statusLabel: "Failed",
          statusColor: "bg-destructive/10 text-destructive",
        }}
        errorMessage="The detail request failed."
        onRetry={onRetry}
        retrying
      />,
    );

    const retry = screen.getByRole("button", { name: "Retry" });
    expect(retry).toBeDisabled();
    expect(retry).toHaveAttribute("data-loading", "true");
    // The error stays on screen during the retry, so the frame is what tells
    // assistive tech that something is happening.
    expect(screen.getByTestId("session-detail-loading-skeleton")).toHaveAttribute(
      "aria-busy",
      "true",
    );
    fireEvent.click(retry);
    expect(onRetry).not.toHaveBeenCalled();
  });

  it("drops the shimmer chrome for a cold failure with nothing to preserve", () => {
    render(
      <SessionDetailLoadingSkeleton
        errorMessage="The session could not be found."
        onRetry={() => {}}
      />,
    );

    expect(screen.getByTestId("session-detail-transition-error")).toHaveTextContent(
      "The session could not be found.",
    );
    // Nothing is pending and there is no metadata behind them, so reserving
    // boxes for chrome that will never arrive would just read as "loading".
    expect(screen.queryByTestId("session-thread-strip-loading")).not.toBeInTheDocument();
    expect(screen.queryByTestId("session-composer-loading")).not.toBeInTheDocument();
    expect(screen.queryByTestId("session-detail-panel-loading")).not.toBeInTheDocument();
    expect(screen.queryByTestId("session-detail-skeleton-mobile-top-bar")).not.toBeInTheDocument();
    expect(screen.getByTestId("session-detail-loading-skeleton")).not.toHaveAttribute("aria-busy");
  });
});

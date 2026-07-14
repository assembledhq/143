import { describe, it, expect, vi, beforeEach } from "vitest";
import { renderWithProviders, screen, userEvent } from "@/test/test-utils";

import type { AutomationRun, AutomationRunStatus } from "@/lib/types";
import { RunCard } from "./run-card";

const pushMock = vi.fn();

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: pushMock, replace: vi.fn() }),
}));

beforeEach(() => {
  pushMock.mockClear();
});

function makeRun(overrides: Partial<AutomationRun> = {}): AutomationRun {
  return {
    id: "run-1",
    live_version: 1,
    automation_id: "auto-1",
    triggered_at: "2026-04-30T00:00:00Z",
    triggered_by: "schedule",
    goal_snapshot: "g",
    status: "completed" as AutomationRunStatus,
    completed_at: "2026-04-30T00:00:30Z",
    created_at: "2026-04-30T00:00:00Z",
    updated_at: "2026-04-30T00:00:30Z",
    ...overrides,
  };
}

describe("RunCard click-through", () => {
  it("navigates to /sessions/{id} when the card body is clicked", async () => {
    const user = userEvent.setup();
    const run = makeRun({
      session: {
        id: "sess-123",
        title: "Refactor diff viewer",
        status: "completed",
        failure_retry_advised: false,
        pr_creation_state: "succeeded",
      },
    });

    renderWithProviders(<RunCard run={run} />);

    // Click the card surface (not the inner action button) — the
    // role="button" landing here is the whole-card affordance.
    await user.click(screen.getByRole("button", { name: /open session for completed run/i }));

    expect(pushMock).toHaveBeenCalledWith("/sessions/sess-123");
  });

  it("activates with Enter on the focused card", async () => {
    const user = userEvent.setup();
    const run = makeRun({
      session: {
        id: "sess-456",
        status: "completed",
        failure_retry_advised: false,
        pr_creation_state: "idle",
      },
    });

    renderWithProviders(<RunCard run={run} />);

    const card = screen.getByRole("button", { name: /open session for completed run/i });
    card.focus();
    await user.keyboard("{Enter}");

    expect(pushMock).toHaveBeenCalledWith("/sessions/sess-456");
  });

  it("Review PR link does not also fire whole-card navigation", async () => {
    const user = userEvent.setup();
    const run = makeRun({
      session: {
        id: "sess-789",
        title: "Refactor",
        status: "completed",
        failure_retry_advised: false,
        pr_creation_state: "succeeded",
        pr: {
          number: 1213,
          url: "https://github.com/example/repo/pull/1213",
          status: "open",
          ci_status: "success",
        },
      },
    });

    renderWithProviders(<RunCard run={run} />);

    const reviewLink = screen.getByRole("link", { name: /review pr/i });
    expect(reviewLink).toHaveAttribute("href", "https://github.com/example/repo/pull/1213");
    expect(reviewLink).toHaveAttribute("target", "_blank");
    expect(reviewLink).toHaveAttribute("rel", "noopener noreferrer");

    await user.click(reviewLink);

    // The card-level router.push should not fire — the click was
    // handled by the anchor and stopPropagation prevents the wrapping
    // div from also navigating.
    expect(pushMock).not.toHaveBeenCalled();
  });

  it("uses 'Reply to agent' as the CTA when the session is awaiting human guidance", () => {
    const run = makeRun({
      status: "failed",
      session: {
        id: "sess-help",
        status: "needs_human_guidance",
        failure_retry_advised: false,
        pr_creation_state: "idle",
      },
    });

    renderWithProviders(<RunCard run={run} />);

    expect(screen.getByRole("button", { name: /reply to agent/i })).toBeInTheDocument();
    expect(screen.getByTestId("run-card-layout")).toHaveClass("flex-col", "sm:flex-row");
    // The headline copy should match the amber attention treatment, not
    // the red "Failed" treatment — both the run.status (failed) and the
    // session.status (needs_human_guidance) are present, and the linked
    // session wins.
    expect(screen.getByText(/needs your input/i)).toBeInTheDocument();
  });

  it("renders execution-row metadata for scheduled completed runs", () => {
    const run = makeRun({
      result_summary: "Removed 2 stale retries from payments spec",
      session: {
        id: "sess-pr",
        title: "Removed 2 stale retries from payments spec",
        status: "completed",
        failure_retry_advised: false,
        pr_creation_state: "succeeded",
        diff_stats: { added: 12, removed: 4, files_changed: 3 },
        pr: {
          number: 412,
          url: "https://github.com/example/repo/pull/412",
          status: "open",
          ci_status: "success",
        },
      },
    });

    renderWithProviders(<RunCard run={run} />);

    expect(screen.getByText(/scheduled run/i)).toBeInTheDocument();
    expect(screen.getByText(/linked session/i)).toBeInTheDocument();
    expect(screen.getByText(/3 files changed/i)).toBeInTheDocument();
    expect(screen.getByText(/pr #412/i)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /review pr/i })).toBeInTheDocument();
  });

  it("uses a compact Session CTA for completed runs without PRs", () => {
    const run = makeRun({
      result_summary: "Updated stale retry handling",
      session: {
        id: "sess-open",
        title: "Updated stale retry handling",
        status: "completed",
        failure_retry_advised: false,
        pr_creation_state: "idle",
      },
    });

    renderWithProviders(<RunCard run={run} />);

    expect(screen.getByRole("button", { name: /open session for completed run/i })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^open session$/i })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /^session$/i })).toHaveClass("h-7", "px-2");
  });

  it("renders manual-run metadata and failure detail in the row body", () => {
    const run = makeRun({
      status: "failed",
      result_summary: "Security sweep found dependency issue",
      session: {
        id: "sess-fail",
        status: "failed",
        title: "Security sweep found dependency issue",
        failure_explanation: "pnpm audit failed because lockfile was out of date",
        failure_retry_advised: true,
        pr_creation_state: "failed",
      },
      triggered_by: "manual",
    });

    renderWithProviders(<RunCard run={run} />);

    expect(screen.getByText(/manual run/i)).toBeInTheDocument();
    expect(screen.getByText(/linked session/i)).toBeInTheDocument();
    expect(screen.getByText(/pnpm audit failed because lockfile was out of date/i)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /retry on session/i })).toBeInTheDocument();
  });

  it("renders a non-interactive thin row for completed_noop without a session", () => {
    const run = makeRun({ status: "completed_noop", completed_at: undefined });
    renderWithProviders(<RunCard run={run} />);
    // Quiet rows without a linked session should not be clickable —
    // there's nowhere meaningful to navigate to.
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
    expect(screen.getByText(/no work needed/i)).toBeInTheDocument();
  });

  it("renders a non-interactive thin row for pending runs", () => {
    const run = makeRun({ status: "pending", completed_at: undefined });
    renderWithProviders(<RunCard run={run} />);
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
    expect(screen.getByText(/pending/i)).toBeInTheDocument();
  });

  it("shows manual provenance for pending manual runs", () => {
    const run = makeRun({
      status: "pending",
      completed_at: undefined,
      triggered_by: "manual",
    });

    renderWithProviders(<RunCard run={run} />);

    expect(screen.getByText(/manual run · waiting to start/i)).toBeInTheDocument();
  });

  it("shows GitHub provenance for pending event-triggered runs", () => {
    const run = makeRun({
      status: "pending",
      completed_at: undefined,
      triggered_by: "github",
    });

    renderWithProviders(<RunCard run={run} />);

    expect(screen.getByText(/github event · waiting to start/i)).toBeInTheDocument();
  });
});

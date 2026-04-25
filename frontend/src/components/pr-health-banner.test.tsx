import { describe, expect, it, vi } from "vitest";

import type { PullRequestHealthResponse } from "@/lib/types";
import { renderWithProviders, screen, userEvent } from "@/test/test-utils";

import { PRHealthBanner } from "./pr-health-banner";

const baseHealth: PullRequestHealthResponse = {
  pull_request_id: "pr-123",
  pull_request_number: 42,
  repository: "acme/widgets",
  url: "https://github.com/acme/widgets/pull/42",
  status: "open",
  head_sha: "head-sha",
  base_sha: "base-sha",
  health_version: 2,
  merge_state: "clean",
  has_conflicts: false,
  failing_test_count: 0,
  needs_agent_action: false,
  github_state_synced_at: "2026-04-24T00:00:00.000Z",
  summary: "PR #42 is healthy.",
  checks: [],
  can_resolve_conflicts: false,
  can_fix_tests: false,
  can_merge: false,
  enrichment_status: "ready",
  enrichment_requested: true,
  enrichment_ready: true,
  conflict_detail_available: false,
  failing_test_detail_available: false,
  obsolete_active_repair_sessions: false,
};

describe("PRHealthBanner", () => {
  it("uses text-sm sizing for the header and metadata copy", () => {
    renderWithProviders(
      <PRHealthBanner
        health={baseHealth}
        pendingAction={null}
        repairError={null}
        mergeAuthRequired={false}
        onFixTests={vi.fn()}
        onResolveConflicts={vi.fn()}
        onMerge={vi.fn()}
      />,
    );

    expect(screen.getByText("PR health")).toHaveClass("text-sm");
    expect(screen.getByText("PR #42 · acme/widgets")).toHaveClass("text-sm");
  });

  it("hides the Merge button when can_merge is false", () => {
    renderWithProviders(
      <PRHealthBanner
        health={baseHealth}
        pendingAction={null}
        repairError={null}
        mergeAuthRequired={false}
        onFixTests={vi.fn()}
        onResolveConflicts={vi.fn()}
        onMerge={vi.fn()}
      />,
    );

    expect(screen.queryByRole("button", { name: /^Merge$/ })).toBeNull();
  });

  it("renders the Merge button when can_merge is true and invokes onMerge", async () => {
    const onMerge = vi.fn();
    renderWithProviders(
      <PRHealthBanner
        health={{ ...baseHealth, can_merge: true }}
        pendingAction={null}
        repairError={null}
        mergeAuthRequired={false}
        onFixTests={vi.fn()}
        onResolveConflicts={vi.fn()}
        onMerge={onMerge}
      />,
    );

    const button = screen.getByRole("button", { name: /^Merge$/ });
    expect(button).not.toBeDisabled();
    await userEvent.setup().click(button);
    expect(onMerge).toHaveBeenCalledTimes(1);
  });

  it("shows a Merging… label and disables the button while pendingAction is merge", () => {
    renderWithProviders(
      <PRHealthBanner
        health={{ ...baseHealth, can_merge: true }}
        pendingAction="merge"
        repairError={null}
        mergeAuthRequired={false}
        onFixTests={vi.fn()}
        onResolveConflicts={vi.fn()}
        onMerge={vi.fn()}
      />,
    );

    const button = screen.getByRole("button", { name: /Merging…/ });
    expect(button).toBeDisabled();
  });

  it("disables the Merge button when another repair action is pending", () => {
    renderWithProviders(
      <PRHealthBanner
        health={{ ...baseHealth, can_merge: true, can_fix_tests: true }}
        pendingAction="fix_tests"
        repairError={null}
        mergeAuthRequired={false}
        onFixTests={vi.fn()}
        onResolveConflicts={vi.fn()}
        onMerge={vi.fn()}
      />,
    );

    expect(screen.getByRole("button", { name: /^Merge$/ })).toBeDisabled();
  });

  it("shows a reconnect hint when merge requires GitHub user auth", () => {
    renderWithProviders(
      <PRHealthBanner
        health={{ ...baseHealth, can_merge: true }}
        pendingAction={null}
        repairError={null}
        mergeAuthRequired
        onFixTests={vi.fn()}
        onResolveConflicts={vi.fn()}
        onMerge={vi.fn()}
      />,
    );

    expect(screen.getByText("Connect your GitHub account to merge this pull request as yourself.")).toBeInTheDocument();
  });
});

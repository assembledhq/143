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
  checks_confirmed: false,
  can_resolve_conflicts: false,
  can_fix_tests: false,
  can_merge: false,
  enrichment_status: "ready",
  enrichment_requested: true,
  enrichment_ready: true,
  conflict_detail_available: false,
  failing_test_detail_available: false,
  obsolete_active_repair_sessions: false,
  active_repairs: [],
  merge_when_ready: { state: "off" },
};

describe("PRHealthBanner", () => {
  it("uses text-sm for the header and text-xs for non-header copy", () => {
    renderWithProviders(
      <PRHealthBanner
        health={{ ...baseHealth, checks_confirmed: true }}
        pendingAction={null}
        repairError={null}
        mergeAuthRequired={false}
        onFixTests={vi.fn()}
        onResolveConflicts={vi.fn()}
        onMerge={vi.fn()}
      />,
    );

    expect(screen.getByText("PR health")).toHaveClass("text-sm");
    expect(screen.getByText("PR #42 · acme/widgets")).toHaveClass("text-xs");
    expect(screen.getByText("PR #42 is healthy.")).toHaveClass("text-xs");
  });

  it("keeps the Merge button visible but disabled when can_merge is false", async () => {
    renderWithProviders(
      <PRHealthBanner
        health={{ ...baseHealth, checks_confirmed: true }}
        pendingAction={null}
        repairError={null}
        mergeAuthRequired={false}
        onFixTests={vi.fn()}
        onResolveConflicts={vi.fn()}
        onMerge={vi.fn()}
      />,
    );

    const button = screen.getByRole("button", { name: /^Merge$/ });
    expect(button).toBeDisabled();

    expect(button).toHaveAttribute("title", "GitHub is not allowing this PR to merge yet.");
  });

  it("shows pending mergeability as a disabled Merge button state", () => {
    renderWithProviders(
      <PRHealthBanner
        health={{
          ...baseHealth,
          merge_state: "mergeability_pending",
          checks_confirmed: true,
          summary: "PR #42 is waiting for GitHub to finish checking mergeability.",
        }}
        pendingAction={null}
        repairError={null}
        mergeAuthRequired={false}
        onFixTests={vi.fn()}
        onResolveConflicts={vi.fn()}
        onMerge={vi.fn()}
      />,
    );

    expect(screen.queryByText("Checking mergeability")).not.toBeInTheDocument();

    const button = screen.getByRole("button", { name: /Checking mergeability…/ });
    expect(button).toBeDisabled();
    expect(button).toHaveAttribute("title", "Waiting for GitHub to check mergeability.");
  });

  it("renders an optional Review action in the PR action row", async () => {
    const onReview = vi.fn();
    renderWithProviders(
      <PRHealthBanner
        health={baseHealth}
        pendingAction={null}
        repairError={null}
        mergeAuthRequired={false}
        onFixTests={vi.fn()}
        onResolveConflicts={vi.fn()}
        onMerge={vi.fn()}
        onQueueMergeWhenReady={vi.fn()}
        reviewAction={{
          disabled: false,
          spinning: false,
          onClick: onReview,
        }}
      />,
    );

    const button = screen.getByRole("button", { name: /^Review$/ });
    expect(button).not.toBeDisabled();
    await userEvent.setup().click(button);
    expect(onReview).toHaveBeenCalledTimes(1);
  });

  it("orders PR detail actions as Merge, Resolve conflicts, Review, then Push changes", () => {
    renderWithProviders(
      <PRHealthBanner
        health={{
          ...baseHealth,
          can_merge: true,
          checks_confirmed: true,
          can_resolve_conflicts: true,
          checks: [
            { name: "Unit tests", category: "test", status: "passed" },
          ],
        }}
        pendingAction={null}
        repairError={null}
        mergeAuthRequired={false}
        onFixTests={vi.fn()}
        onResolveConflicts={vi.fn()}
        onMerge={vi.fn()}
        reviewAction={{
          disabled: false,
          spinning: false,
          onClick: vi.fn(),
        }}
        pushChanges={{
          label: "Push changes",
          disabled: false,
          spinning: false,
          showError: false,
          onClick: vi.fn(),
        }}
      />,
    );

    const labels = screen.getAllByRole("button")
      .map((button) => button.textContent)
      .filter((label) => label !== "");
    expect(labels).toEqual(
      ["Merge", "Resolve conflicts", "Review", "Push changes"],
    );
  });

  it("keeps merge disabled while allowing merge when ready from the dropdown", async () => {
    const onQueue = vi.fn();
    renderWithProviders(
      <PRHealthBanner
        health={{
          ...baseHealth,
          checks: [{ name: "Unit tests", category: "test", status: "pending" }],
          checks_confirmed: true,
          can_merge: false,
        }}
        pendingAction={null}
        repairError={null}
        mergeAuthRequired={false}
        onFixTests={vi.fn()}
        onResolveConflicts={vi.fn()}
        onMerge={vi.fn()}
        onQueueMergeWhenReady={onQueue}
      />,
    );

    expect(screen.getByRole("button", { name: "Merge" })).toBeDisabled();
    await userEvent.setup().click(screen.getByRole("button", { name: "More merge actions" }));
    await userEvent.setup().click(await screen.findByRole("menuitem", { name: "Merge when ready" }));
    expect(onQueue).toHaveBeenCalledTimes(1);
  });

  it("matches the Create PR split button sizing and menu alignment", async () => {
    renderWithProviders(
      <PRHealthBanner
        health={{
          ...baseHealth,
          checks: [{ name: "Unit tests", category: "test", status: "pending" }],
          checks_confirmed: true,
          can_merge: false,
        }}
        pendingAction={null}
        repairError={null}
        mergeAuthRequired={false}
        onFixTests={vi.fn()}
        onResolveConflicts={vi.fn()}
        onMerge={vi.fn()}
        onQueueMergeWhenReady={vi.fn()}
      />,
    );

    const moreActions = screen.getByRole("button", { name: "More merge actions" });
    expect(moreActions).toHaveClass("h-7", "w-7", "rounded-l-none");
    expect(moreActions).not.toHaveClass("h-9", "w-8");

    await userEvent.setup().click(moreActions);
    const menuItem = await screen.findByRole("menuitem", { name: "Merge when ready" });
    expect(menuItem.closest("[data-slot='dropdown-menu-content']")).toHaveAttribute(
      "data-align",
      "end",
    );
  });

  it("shows queued merge when ready state and allows cancelling", async () => {
    const onCancel = vi.fn();
    renderWithProviders(
      <PRHealthBanner
        health={{
          ...baseHealth,
          merge_when_ready: { state: "queued", requested_head_sha: "head-sha" },
        }}
        pendingAction={null}
        repairError={null}
        mergeAuthRequired={false}
        onFixTests={vi.fn()}
        onResolveConflicts={vi.fn()}
        onMerge={vi.fn()}
        onCancelMergeWhenReady={onCancel}
      />,
    );

    expect(screen.getByText("Merge when ready is on. Waiting for checks to pass.")).toBeInTheDocument();
    await userEvent.setup().click(screen.getByRole("button", { name: "Cancel" }));
    expect(onCancel).toHaveBeenCalledTimes(1);
  });

  it("shows the disabled Review action reason in a hover tooltip", async () => {
    renderWithProviders(
      <PRHealthBanner
        health={baseHealth}
        pendingAction={null}
        repairError={null}
        mergeAuthRequired={false}
        onFixTests={vi.fn()}
        onResolveConflicts={vi.fn()}
        onMerge={vi.fn()}
        reviewAction={{
          disabled: true,
          spinning: false,
          title: "Review can start after the current turn finishes",
          onClick: vi.fn(),
        }}
      />,
    );

    const button = screen.getByRole("button", { name: /^Review$/ });
    expect(button).toBeDisabled();

    await userEvent.setup().hover(button.parentElement as HTMLElement);

    expect(await screen.findByRole("tooltip", { name: "Review can start after the current turn finishes" })).toBeInTheDocument();
  });

	it("renders the Merge button when can_merge is true and invokes onMerge", async () => {
		const onMerge = vi.fn();
		renderWithProviders(
			<PRHealthBanner
				health={{
					...baseHealth,
					checks_confirmed: true,
					can_merge: true,
					checks: [
						{ name: "Unit tests", category: "test", status: "passed" },
					],
				}}
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
				health={{
					...baseHealth,
					checks_confirmed: true,
					can_merge: true,
					checks: [
						{ name: "Unit tests", category: "test", status: "passed" },
					],
				}}
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
				health={{
					...baseHealth,
					checks_confirmed: true,
					can_merge: true,
					can_fix_tests: true,
					checks: [
						{ name: "Unit tests", category: "test", status: "passed" },
					],
				}}
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
				health={{
					...baseHealth,
					checks_confirmed: true,
					can_merge: true,
					checks: [
						{ name: "Unit tests", category: "test", status: "passed" },
					],
				}}
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

  it("keeps the Merge button visible but disabled until checks are explicitly confirmed as passed", async () => {
    renderWithProviders(
      <PRHealthBanner
        health={{ ...baseHealth, can_merge: true, checks_confirmed: false, checks: [] }}
        pendingAction={null}
        repairError={null}
        mergeAuthRequired={false}
        onFixTests={vi.fn()}
				onResolveConflicts={vi.fn()}
				onMerge={vi.fn()}
			/>,
		);

    const button = screen.getByRole("button", { name: /^Merge$/ });
    expect(button).toBeDisabled();

    expect(button).toHaveAttribute("title", "Waiting for GitHub to confirm required checks.");
  });

  it("shows the Merge button when GitHub has confirmed that no CI checks are configured", () => {
    renderWithProviders(
      <PRHealthBanner
        health={{ ...baseHealth, can_merge: true, checks_confirmed: true, checks: [] }}
        pendingAction={null}
        repairError={null}
        mergeAuthRequired={false}
        onFixTests={vi.fn()}
        onResolveConflicts={vi.fn()}
        onMerge={vi.fn()}
      />,
    );

    expect(screen.getByRole("button", { name: /^Merge$/ })).toBeInTheDocument();
  });

  it("shows CI job statuses when hovering the failing tests badge", async () => {
    renderWithProviders(
      <PRHealthBanner
        health={{
          ...baseHealth,
          failing_test_count: 1,
          checks: [
            { name: "Unit tests", category: "test", status: "failed", details_url: "https://ci.example.com/unit-tests" },
            { name: "E2E tests", category: "test", status: "pending", details_url: "https://ci.example.com/e2e-tests" },
            { name: "Lint", category: "lint", status: "passed", details_url: "https://ci.example.com/lint" },
          ],
        }}
        pendingAction={null}
        repairError={null}
        mergeAuthRequired={false}
        onFixTests={vi.fn()}
        onResolveConflicts={vi.fn()}
        onMerge={vi.fn()}
      />,
    );

    const user = userEvent.setup();
    await user.hover(screen.getByText("1/3 failed"));

    expect(await screen.findByText("CI jobs")).toBeInTheDocument();
    expect(screen.getByText("Unit tests")).toBeInTheDocument();
    expect(screen.getByText("E2E tests")).toBeInTheDocument();
    expect(screen.getByText("Lint")).toBeInTheDocument();
    expect(screen.getAllByText("Failed").length).toBeGreaterThan(0);
    expect(screen.getAllByText("Pending").length).toBeGreaterThan(0);
    expect(screen.getAllByText("Passed").length).toBeGreaterThan(0);
  });

  it("renders each hover-card check as an external link when details URLs are available", async () => {
    renderWithProviders(
      <PRHealthBanner
        health={{
          ...baseHealth,
          failing_test_count: 2,
          checks: [
            { name: "Unit tests", category: "test", status: "failed", details_url: "https://ci.example.com/unit-tests" },
            { name: "E2E tests", category: "test", status: "failed", details_url: "https://ci.example.com/e2e-tests" },
          ],
        }}
        pendingAction={null}
        repairError={null}
        mergeAuthRequired={false}
        onFixTests={vi.fn()}
        onResolveConflicts={vi.fn()}
        onMerge={vi.fn()}
      />,
    );

    const user = userEvent.setup();
    await user.hover(screen.getByText("2/2 failed"));

    const unitLink = await screen.findByRole("link", { name: /Unit tests/i });
    const e2eLink = screen.getByRole("link", { name: /E2E tests/i });

    expect(unitLink).toHaveAttribute("href", "https://ci.example.com/unit-tests");
    expect(unitLink).toHaveAttribute("target", "_blank");
    expect(unitLink).toHaveAttribute("rel", expect.stringContaining("noopener"));
    expect(e2eLink).toHaveAttribute("href", "https://ci.example.com/e2e-tests");
    expect(e2eLink).toHaveAttribute("target", "_blank");
    expect(e2eLink).toHaveAttribute("rel", expect.stringContaining("noreferrer"));
  });

  it("shows failed-over-total summary and normalizes missing legacy statuses to pending", async () => {
    renderWithProviders(
      <PRHealthBanner
        health={{
          ...baseHealth,
          failing_test_count: 1,
          checks: [
            { name: "Unit tests", category: "test", status: "failed" },
            { name: "E2E tests", category: "test", status: "pending" },
            { name: "Legacy check", category: "lint" } as unknown as NonNullable<PullRequestHealthResponse["checks"]>[number],
          ],
        }}
        pendingAction={null}
        repairError={null}
        mergeAuthRequired={false}
        onFixTests={vi.fn()}
        onResolveConflicts={vi.fn()}
        onMerge={vi.fn()}
      />,
    );

    const user = userEvent.setup();
    await user.hover(screen.getByText("1/3 failed"));

    expect(await screen.findByText("CI jobs")).toBeInTheDocument();
    expect(screen.getByText("Legacy check")).toBeInTheDocument();
    expect(screen.getAllByText("Pending").length).toBeGreaterThanOrEqual(2);
  });

  it("does not render a separate conflicts badge when the summary already covers merge conflicts", () => {
    renderWithProviders(
      <PRHealthBanner
        health={{
          ...baseHealth,
          has_conflicts: true,
          can_resolve_conflicts: true,
          summary: "PR #42 is blocked by merge conflicts.",
        }}
        pendingAction={null}
        repairError={null}
        mergeAuthRequired={false}
        onFixTests={vi.fn()}
        onResolveConflicts={vi.fn()}
        onMerge={vi.fn()}
      />,
    );

    expect(screen.getByText("PR #42 is blocked by merge conflicts.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Resolve conflicts" })).toBeInTheDocument();
    expect(screen.queryByText(/^conflicts$/)).toBeNull();
  });

  it("renders a Push changes button when pushChanges is provided and triggers onClick", async () => {
    const onClick = vi.fn();
    renderWithProviders(
      <PRHealthBanner
        health={baseHealth}
        pendingAction={null}
        repairError={null}
        mergeAuthRequired={false}
        onFixTests={vi.fn()}
        onResolveConflicts={vi.fn()}
        onMerge={vi.fn()}
        pushChanges={{
          label: "Push changes",
          disabled: false,
          spinning: false,
          showError: false,
          onClick,
        }}
      />,
    );

    const button = screen.getByRole("button", { name: "Push changes" });
    expect(button).toBeEnabled();
    await userEvent.click(button);
    expect(onClick).toHaveBeenCalledTimes(1);
  });

  it("disables the Push changes button while another action is pending", () => {
    renderWithProviders(
      <PRHealthBanner
        health={{
          ...baseHealth,
          can_resolve_conflicts: true,
        }}
        pendingAction="resolve_conflicts"
        repairError={null}
        mergeAuthRequired={false}
        onFixTests={vi.fn()}
        onResolveConflicts={vi.fn()}
        onMerge={vi.fn()}
        pushChanges={{
          label: "Push changes",
          disabled: false,
          spinning: false,
          showError: false,
          onClick: vi.fn(),
        }}
      />,
    );

    expect(screen.getByRole("button", { name: "Push changes" })).toBeDisabled();
  });

  it("renders the Push changes button in spinning/Pushing state and disables it", () => {
    renderWithProviders(
      <PRHealthBanner
        health={baseHealth}
        pendingAction={null}
        repairError={null}
        mergeAuthRequired={false}
        onFixTests={vi.fn()}
        onResolveConflicts={vi.fn()}
        onMerge={vi.fn()}
        pushChanges={{
          label: "Pushing…",
          disabled: true,
          spinning: true,
          showError: false,
          onClick: vi.fn(),
          title: "Pushing changes to the PR branch",
        }}
      />,
    );

    const button = screen.getByRole("button", { name: /Pushing/ });
    expect(button).toBeDisabled();
    expect(button).toHaveAttribute("title", "Pushing changes to the PR branch");
  });

  it("replaces Fix tests with a running state and open-session action for an active fix-tests repair on another session", () => {
    const onOpenRepairSession = vi.fn();

    renderWithProviders(
      <PRHealthBanner
        health={{
          ...baseHealth,
          can_fix_tests: true,
          can_merge: true,
          active_repairs: [{
            action_type: "fix_tests",
            session_id: "session-repair-123",
            session_status: "running",
            health_version: 2,
          }],
        }}
        currentSessionId="session-current"
        pendingAction={null}
        repairError={null}
        mergeAuthRequired={false}
        onFixTests={vi.fn()}
        onResolveConflicts={vi.fn()}
        onMerge={vi.fn()}
        onOpenRepairSession={onOpenRepairSession}
      />,
    );

    expect(screen.getByText("Fix tests running")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Fix tests" })).toBeNull();
    expect(screen.getByRole("button", { name: /^Merge$/ })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Open repair session" })).toBeInTheDocument();
  });

  it("suppresses both repair CTAs when resolve conflicts is already running for the current PR state", () => {
    renderWithProviders(
      <PRHealthBanner
        health={{
          ...baseHealth,
          can_resolve_conflicts: true,
          can_fix_tests: true,
          active_repairs: [{
            action_type: "resolve_conflicts",
            session_id: "session-current",
            session_status: "running",
            health_version: 2,
          }],
        }}
        currentSessionId="session-current"
        pendingAction={null}
        repairError={null}
        mergeAuthRequired={false}
        onFixTests={vi.fn()}
        onResolveConflicts={vi.fn()}
        onMerge={vi.fn()}
        onOpenRepairSession={vi.fn()}
      />,
    );

    expect(screen.getByText("Resolve conflicts running")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Resolve conflicts" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Fix tests" })).toBeNull();
    expect(screen.getByRole("button", { name: /^Merge$/ })).toBeDisabled();
    expect(screen.queryByRole("button", { name: "Open repair session" })).toBeNull();
    expect(screen.queryByText("Resolve conflicts first. CI may need to rerun afterward.")).toBeNull();
  });

  it("treats an active repair as non-healthy even when the repair action booleans are suppressed", () => {
    const { container } = renderWithProviders(
      <PRHealthBanner
        health={{
          ...baseHealth,
          needs_agent_action: true,
          summary: "PR #42 has an active repair session.",
          active_repairs: [{
            action_type: "fix_tests",
            session_id: "session-current",
            session_status: "running",
            health_version: 2,
          }],
        }}
        currentSessionId="session-current"
        pendingAction={null}
        repairError={null}
        mergeAuthRequired={false}
        onFixTests={vi.fn()}
        onResolveConflicts={vi.fn()}
        onMerge={vi.fn()}
        onOpenRepairSession={vi.fn()}
      />,
    );

    expect(container.querySelector("svg.lucide-git-pull-request")).toBeInTheDocument();
    expect(container.querySelector("svg.lucide-check-circle-2")).toBeNull();
  });
});

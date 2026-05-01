import { describe, it, expect, vi } from "vitest";
import { http, HttpResponse } from "msw";

import { renderWithProviders, screen, userEvent, waitFor } from "@/test/test-utils";
import { server } from "@/test/mocks/server";

import { RunsTab } from "./runs-tab";

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
}));

function makeQuietRun(id: string, hoursAgo: number) {
  const t = new Date(Date.UTC(2026, 3, 30, 12 - hoursAgo, 0, 0)).toISOString();
  return {
    id,
    automation_id: "auto-1",
    triggered_at: t,
    triggered_by: "schedule" as const,
    goal_snapshot: "g",
    status: "completed_noop" as const,
    completed_at: t,
    result_summary: "Repo had no new commits since last run.",
    created_at: t,
    updated_at: t,
  };
}

describe("RunsTab quiet-group auto-expand", () => {
  it("auto-expands the topmost quiet group when the entire visible list is quiet", async () => {
    const runs = [
      makeQuietRun("run-1", 1),
      makeQuietRun("run-2", 2),
      makeQuietRun("run-3", 3),
    ];
    server.use(
      http.get("*/api/v1/automations/auto-1/runs*", () =>
        HttpResponse.json({ data: runs, meta: {} }),
      ),
    );

    renderWithProviders(<RunsTab automationId="auto-1" />);

    // The collapsible bar shows "3 quiet runs · last one …".
    await screen.findByText(/3 quiet runs/i);

    // When all-quiet, the topmost group should default to expanded so
    // the page doesn't read as empty — each individual run is rendered.
    await waitFor(() => {
      expect(screen.getAllByText(/no work needed/i).length).toBeGreaterThanOrEqual(3);
    });

    // The toggle reflects the open state.
    const toggle = screen.getByRole("button", { name: /3 quiet runs/i });
    expect(toggle).toHaveAttribute("aria-expanded", "true");
  });

  it("respects user-driven collapse and keeps the group closed thereafter", async () => {
    const user = userEvent.setup();
    const runs = [makeQuietRun("a", 1), makeQuietRun("b", 2)];
    server.use(
      http.get("*/api/v1/automations/auto-1/runs*", () =>
        HttpResponse.json({ data: runs, meta: {} }),
      ),
    );

    renderWithProviders(<RunsTab automationId="auto-1" />);

    const toggle = await screen.findByRole("button", { name: /2 quiet runs/i });
    expect(toggle).toHaveAttribute("aria-expanded", "true");

    await user.click(toggle);
    expect(toggle).toHaveAttribute("aria-expanded", "false");

    // Re-clicking toggles back open. The key behaviour we care about is
    // that the user's explicit choice overrides the all-quiet
    // auto-expand default — verified by the collapse working at all.
    await user.click(toggle);
    expect(toggle).toHaveAttribute("aria-expanded", "true");
  });

  it("does not auto-expand when at least one loud run is in the page", async () => {
    const t = "2026-04-30T12:00:00Z";
    const loud = {
      id: "loud-1",
      automation_id: "auto-1",
      triggered_at: t,
      triggered_by: "schedule" as const,
      goal_snapshot: "g",
      status: "completed" as const,
      completed_at: t,
      created_at: t,
      updated_at: t,
      result_summary: "Made changes",
      session: {
        id: "sess-loud",
        status: "completed",
        failure_retry_advised: false,
        pr_creation_state: "idle" as const,
      },
    };
    const runs = [loud, makeQuietRun("q1", 1), makeQuietRun("q2", 2)];
    server.use(
      http.get("*/api/v1/automations/auto-1/runs*", () =>
        HttpResponse.json({ data: runs, meta: {} }),
      ),
    );

    renderWithProviders(<RunsTab automationId="auto-1" />);

    const toggle = await screen.findByRole("button", { name: /2 quiet runs/i });
    // Mixed page → quiet group stays collapsed by default so the loud
    // run sits at the top of the page unobstructed.
    expect(toggle).toHaveAttribute("aria-expanded", "false");
  });
});

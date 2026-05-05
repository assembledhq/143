import { describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";

import { AgentTabStrip } from "./agent-tab-strip";
import { renderWithProviders, userEvent } from "@/test/test-utils";
import type { SessionThread } from "@/lib/types";

const statusConfig = {
  pending: { label: "Pending" },
  running: { label: "Running" },
  idle: { label: "Idle" },
  awaiting_input: { label: "Awaiting input" },
  completed: { label: "Completed" },
  failed: { label: "Failed" },
  cancelled: { label: "Cancelled" },
};

function makeThread(overrides: Partial<SessionThread>): SessionThread {
  return {
    id: "thread-1",
    session_id: "session-1",
    org_id: "org-1",
    agent_type: "codex",
    label: "Main tab",
    status: "idle",
    current_turn: 3,
    created_at: "2026-05-05T00:00:00Z",
    cost_cents: 125,
    pending_message_count: 0,
    ...overrides,
  };
}

describe("AgentTabStrip", () => {
  it("shows a simple tooltip without cost and keeps idle tabs on the primary blue", async () => {
    const user = userEvent.setup();
    const thread = makeThread({ pending_message_count: 2 });

    const { container } = renderWithProviders(
      <AgentTabStrip
        threads={[thread]}
        activeThreadId={thread.id}
        overlapsByThreadId={new Map([[thread.id, ["frontend/src/app.tsx"]]])}
        statusConfig={statusConfig}
        onActiveThreadChange={vi.fn()}
        onAddTab={vi.fn()}
        onCancelThread={vi.fn()}
        onForkThread={vi.fn()}
        onRevertThread={vi.fn()}
        cancelPendingThreadId={null}
      />,
    );

    const trigger = screen.getByRole("tab", { name: /main tab/i });
    const idleDot = container.querySelector(".bg-primary");

    expect(idleDot).not.toBeNull();

    await user.hover(trigger);

    expect(await screen.findByRole("tooltip")).toHaveTextContent("Idle");
    expect(screen.getByRole("tooltip")).toHaveTextContent("2 messages queued");
    expect(screen.getByRole("tooltip")).toHaveTextContent("Overlap with another tab");
    expect(screen.getByRole("tooltip")).not.toHaveTextContent("Cost:");
    expect(screen.getByRole("tooltip")).not.toHaveTextContent("$1.25");
  });
});

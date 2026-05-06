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
  it("renders a quiet single-agent header with a trailing add button instead of a tab strip", async () => {
    const user = userEvent.setup();
    const thread = makeThread({ pending_message_count: 2 });
    const onAddTab = vi.fn();

    const { container } = renderWithProviders(
      <AgentTabStrip
        threads={[thread]}
        activeThreadId={thread.id}
        overlapsByThreadId={new Map([[thread.id, ["frontend/src/app.tsx"]]])}
        statusConfig={statusConfig}
        onActiveThreadChange={vi.fn()}
        onAddTab={onAddTab}
        onCancelThread={vi.fn()}
        onForkThread={vi.fn()}
        onRevertThread={vi.fn()}
        cancelPendingThreadId={null}
      />,
    );

    const idleDot = container.querySelector(".bg-primary");
    const addButton = screen.getByRole("button", { name: "Add agent tab" });

    expect(idleDot).not.toBeNull();
    expect(screen.queryByRole("tablist", { name: "Agent tabs" })).not.toBeInTheDocument();
    expect(screen.getByText("Codex")).toBeInTheDocument();
    expect(screen.getByText("Idle")).toBeInTheDocument();
    expect(screen.queryByText("Main tab")).not.toBeInTheDocument();

    await user.hover(screen.getByText("Codex"));

    expect(await screen.findByRole("tooltip")).toHaveTextContent("Idle");
    expect(screen.getByRole("tooltip")).toHaveTextContent("2 messages queued");
    expect(screen.getByRole("tooltip")).toHaveTextContent("Overlap with another tab");
    expect(screen.getByRole("tooltip")).not.toHaveTextContent("Cost:");
    expect(screen.getByRole("tooltip")).not.toHaveTextContent("$1.25");

    await user.click(addButton);

    expect(onAddTab).toHaveBeenCalledTimes(1);
  });

  it("shows the single-agent tooltip when the header receives keyboard focus", async () => {
    const user = userEvent.setup();
    const thread = makeThread({ pending_message_count: 1 });

    renderWithProviders(
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

    await user.tab();

    expect(await screen.findByRole("tooltip")).toHaveTextContent("Idle");
    expect(screen.getByRole("tooltip")).toHaveTextContent("1 message queued");
  });

  it("keeps the full tab strip once a second tab exists", () => {
    const threads = [
      makeThread({ id: "thread-1", label: "Main tab" }),
      makeThread({ id: "thread-2", label: "Review", status: "running", agent_type: "claude_code" }),
    ];

    renderWithProviders(
      <AgentTabStrip
        threads={threads}
        activeThreadId={threads[0].id}
        overlapsByThreadId={new Map()}
        statusConfig={statusConfig}
        onActiveThreadChange={vi.fn()}
        onAddTab={vi.fn()}
        onCancelThread={vi.fn()}
        onForkThread={vi.fn()}
        onRevertThread={vi.fn()}
        cancelPendingThreadId={null}
      />,
    );

    expect(screen.getByRole("tablist", { name: "Agent tabs" })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: /main tab/i })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: /review/i })).toBeInTheDocument();
  });
});

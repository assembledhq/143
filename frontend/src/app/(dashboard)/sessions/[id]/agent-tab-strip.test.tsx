import { useMemo, useState } from "react";
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
        viewedThreadIds={new Set([thread.id])}
        overlapsByThreadId={new Map([[thread.id, ["frontend/src/app.tsx"]]])}
        statusConfig={statusConfig}
        onActiveThreadChange={vi.fn()}
        onAddTab={onAddTab}
        onRevertThread={vi.fn()}
        onArchiveThread={vi.fn()}
        archivePendingThreadId={null}
      />,
    );

    const idleDot = container.querySelector(".bg-primary");
    const addButton = screen.getByRole("button", { name: "Add agent tab" });
    const label = screen.getByText("Main tab");

    expect(idleDot).toBeNull();
    expect(screen.queryByRole("tablist", { name: "Agent tabs" })).not.toBeInTheDocument();
    expect(label).toBeInTheDocument();
    expect(label).toHaveClass("text-xs");
    expect(label).not.toHaveClass("text-sm");
    expect(screen.getByRole("group", { name: "Codex Idle" })).toBeInTheDocument();
    expect(addButton).toHaveClass("h-7");
    expect(addButton).toHaveClass("w-7");
    expect(addButton).toHaveClass("opacity-70");

    await user.hover(label);

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
        viewedThreadIds={new Set([thread.id])}
        overlapsByThreadId={new Map([[thread.id, ["frontend/src/app.tsx"]]])}
        statusConfig={statusConfig}
        onActiveThreadChange={vi.fn()}
        onAddTab={vi.fn()}
        onRevertThread={vi.fn()}
        onArchiveThread={vi.fn()}
        archivePendingThreadId={null}
      />,
    );

    await user.tab();

    expect(await screen.findByRole("tooltip")).toHaveTextContent("Idle");
    expect(screen.getByRole("tooltip")).toHaveTextContent("1 message queued");
  });

  it("keeps the full tab strip once a second tab exists", () => {
    const threads = [
      makeThread({ id: "thread-1", label: "Main tab" }),
      makeThread({ id: "thread-2", label: "Review", status: "completed", agent_type: "claude_code" }),
    ];

    renderWithProviders(
      <AgentTabStrip
        threads={threads}
        activeThreadId={threads[0].id}
        viewedThreadIds={new Set([threads[0].id])}
        overlapsByThreadId={new Map()}
        statusConfig={statusConfig}
        onActiveThreadChange={vi.fn()}
        onAddTab={vi.fn()}
        onRevertThread={vi.fn()}
        onArchiveThread={vi.fn()}
        archivePendingThreadId={null}
      />,
    );

    const tabList = screen.getByRole("tablist", { name: "Agent tabs" });
    const activeTab = screen.getByRole("tab", { selected: true });

    expect(tabList).toBeInTheDocument();
    expect(tabList).toHaveAttribute("data-variant", "line");
    expect(tabList).not.toHaveClass("bg-muted/60");
    expect(activeTab).toHaveTextContent(/Main tab/i);
    expect(activeTab).not.toHaveTextContent(/Idle/i);
    expect(activeTab).toHaveClass("data-[state=active]:text-primary");
    expect(activeTab).toHaveClass("data-[state=active]:bg-transparent");
    expect(screen.getByRole("tab", { name: /review/i })).not.toHaveTextContent(/Completed/i);
    expect(screen.getByRole("button", { name: "Close Main tab" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Close Review tab" })).toBeInTheDocument();
  });

  it("shows only the revert action in the desktop tab actions menu", async () => {
    const user = userEvent.setup();
    const threads = [
      makeThread({ id: "thread-1", label: "Main tab", diff: "diff --git a/a b/a" }),
      makeThread({ id: "thread-2", label: "Review", status: "completed", agent_type: "claude_code" }),
    ];

    renderWithProviders(
      <AgentTabStrip
        threads={threads}
        activeThreadId={threads[0].id}
        viewedThreadIds={new Set([threads[0].id])}
        overlapsByThreadId={new Map()}
        statusConfig={statusConfig}
        onActiveThreadChange={vi.fn()}
        onAddTab={vi.fn()}
        onRevertThread={vi.fn()}
        onArchiveThread={vi.fn()}
        archivePendingThreadId={null}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Tab actions" }));

    expect(screen.queryByRole("menuitem", { name: "Cancel turn" })).not.toBeInTheDocument();
    expect(screen.queryByRole("menuitem", { name: "Fork this tab" })).not.toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: "Revert this tab's changes" })).toBeInTheDocument();
  });

  it("archives the selected non-running tab from the close affordance beside the strip", async () => {
    const user = userEvent.setup();
    const threads = [
      makeThread({ id: "thread-1", label: "Main tab", status: "completed" }),
      makeThread({ id: "thread-2", label: "Review", status: "completed" }),
    ];
    const onArchiveThread = vi.fn();

    renderWithProviders(
      <AgentTabStrip
        threads={threads}
        activeThreadId={threads[1].id}
        viewedThreadIds={new Set(threads.map((thread) => thread.id))}
        overlapsByThreadId={new Map()}
        statusConfig={statusConfig}
        onActiveThreadChange={vi.fn()}
        onAddTab={vi.fn()}
        onRevertThread={vi.fn()}
        onArchiveThread={onArchiveThread}
        archivePendingThreadId={null}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Close Review tab" }));

    expect(onArchiveThread).toHaveBeenCalledWith("thread-2");
  });

  it("keeps a blue dot on unseen tabs until they are selected", async () => {
    const user = userEvent.setup();
    const threads = [
      makeThread({ id: "thread-1", label: "Main tab" }),
      makeThread({ id: "thread-2", label: "Review" }),
    ];

    function Harness() {
      const [activeThreadId, setActiveThreadId] = useState(threads[0].id);
      const [viewedThreadIds, setViewedThreadIds] = useState(() => new Set([threads[0].id]));
      const viewed = useMemo(() => viewedThreadIds, [viewedThreadIds]);

      return (
        <AgentTabStrip
          threads={threads}
          activeThreadId={activeThreadId}
          viewedThreadIds={viewed}
          overlapsByThreadId={new Map()}
          statusConfig={statusConfig}
          onActiveThreadChange={(threadId) => {
            setActiveThreadId(threadId);
            setViewedThreadIds((current) => new Set(current).add(threadId));
          }}
          onAddTab={vi.fn()}
          onRevertThread={vi.fn()}
          onArchiveThread={vi.fn()}
          archivePendingThreadId={null}
        />
      );
    }

    const { container } = renderWithProviders(<Harness />);

    expect(container.querySelectorAll(".bg-primary").length).toBe(1);

    await user.click(screen.getByRole("tab", { name: "Review" }));

    expect(container.querySelector(".bg-primary")).toBeNull();
  });
});

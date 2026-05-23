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

  it("keeps the single-agent tooltip trigger scoped to the visible tab label", () => {
    const thread = makeThread({ label: "Main" });

    renderWithProviders(
      <AgentTabStrip
        threads={[thread]}
        activeThreadId={thread.id}
        viewedThreadIds={new Set([thread.id])}
        overlapsByThreadId={new Map()}
        statusConfig={statusConfig}
        onActiveThreadChange={vi.fn()}
        onAddTab={vi.fn()}
        onRevertThread={vi.fn()}
        onArchiveThread={vi.fn()}
        archivePendingThreadId={null}
      />,
    );

    const trigger = screen.getByRole("group", { name: "Codex Idle" });

    expect(trigger).not.toHaveClass("flex-1");
    expect(trigger.parentElement).toHaveClass("flex-1");
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

    // The scroll/clip wrapper lives on the parent div so the active-tab
    // underline (positioned just below the trigger) isn't clipped.
    const scrollWrapper = tabList.parentElement;
    expect(scrollWrapper).toHaveClass("overflow-x-auto");
    expect(scrollWrapper).toHaveClass("overflow-y-hidden");
    expect(scrollWrapper).toHaveClass("pb-1");
    expect(activeTab).toHaveTextContent(/Main tab/i);
    expect(activeTab).toHaveAttribute("data-state", "active");
    expect(activeTab).not.toHaveTextContent(/Idle/i);
    expect(activeTab).toHaveClass("data-[state=active]:text-primary");
    expect(activeTab).toHaveClass("data-[state=active]:bg-transparent");
    expect(activeTab).toHaveClass("after:bg-[image:var(--gradient-primary)]");
    expect(activeTab).toHaveClass("group-data-[variant=line]/tabs-list:data-[state=active]:after:opacity-100");
    expect(activeTab).not.toHaveClass("after:bg-none");
    expect(screen.getByRole("tab", { name: /review/i })).not.toHaveTextContent(/Completed/i);
    expect(screen.getByRole("button", { name: "Close Main tab" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Close Review tab" })).toBeInTheDocument();
  });

  it("keeps the bar at the same height in single-tab and multi-tab modes", () => {
    // Regression guard: the bar's vertical sizing comes from `py-2` on the outer
    // wrapper and `min-h-9` on the inner flex. Both modes must apply the same
    // height-determining classes; otherwise the bar visibly jumps when a second
    // tab appears (which is what happened before #893/#910 — the active-tab
    // underline needed bottom padding that only existed in multi-tab mode).
    const baseProps = {
      viewedThreadIds: new Set<string>(),
      overlapsByThreadId: new Map<string, string[]>(),
      statusConfig,
      onActiveThreadChange: vi.fn(),
      onAddTab: vi.fn(),
      onRevertThread: vi.fn(),
      onArchiveThread: vi.fn(),
      archivePendingThreadId: null,
    };

    const heightOnlyClasses = (el: HTMLElement) =>
      el.className
        .split(/\s+/)
        .filter((cls) => /^(h-|min-h-|max-h-|py-|pt-|pb-)\S/.test(cls))
        .sort()
        .join(" ");

    const captureBarShell = () => {
      const addBtn = screen.getByRole("button", { name: "Add agent tab" });
      const innerFlex = addBtn.parentElement;
      const outerWrapper = innerFlex?.parentElement;
      if (!innerFlex || !outerWrapper) {
        throw new Error("Could not locate bar shell from add-tab button");
      }
      return {
        outer: heightOnlyClasses(outerWrapper),
        inner: heightOnlyClasses(innerFlex),
      };
    };

    const single = renderWithProviders(
      <AgentTabStrip
        threads={[makeThread({ id: "thread-1", label: "Solo tab" })]}
        activeThreadId="thread-1"
        {...baseProps}
      />,
    );
    const singleShell = captureBarShell();
    single.unmount();

    renderWithProviders(
      <AgentTabStrip
        threads={[
          makeThread({ id: "thread-1", label: "Main tab" }),
          makeThread({ id: "thread-2", label: "Review", agent_type: "claude_code" }),
        ]}
        activeThreadId="thread-1"
        {...baseProps}
      />,
    );
    const multiShell = captureBarShell();

    // Height-determining classes must agree across modes — that's what keeps
    // the bar from jumping. The specific value isn't sacred; the equality is.
    expect(multiShell.outer).toBe(singleShell.outer);
    expect(multiShell.inner).toBe(singleShell.inner);

    // The inner flex must keep *some* explicit height affordance — without
    // one, single-tab collapses to the icon-button intrinsic height (32px)
    // while multi-tab grows to fit the underline (36px).
    expect(singleShell.inner).not.toBe("");
    expect(multiShell.inner).not.toBe("");
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

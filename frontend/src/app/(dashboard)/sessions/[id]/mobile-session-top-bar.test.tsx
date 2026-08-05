import { describe, expect, it, vi } from "vitest";
import { screen, within } from "@testing-library/react";

import { renderWithProviders, userEvent } from "@/test/test-utils";
import type { SessionThread } from "@/lib/types";
import { MobileSessionTopBar } from "./mobile-session-top-bar";

vi.mock("next/link", () => ({
  default: ({ children, href, ...props }: React.ComponentProps<"a"> & { href: string }) => (
    <a href={href} {...props}>{children}</a>
  ),
}));

vi.mock("next/navigation", () => ({
  useSearchParams: () => new URLSearchParams(),
}));

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
    diff: "diff --git a/src/app.ts b/src/app.ts",
    ...overrides,
  };
}

describe("MobileSessionTopBar", () => {
  it("keeps details directly accessible while moving tab actions into a session actions sheet", async () => {
    const user = userEvent.setup();

    renderWithProviders(
      <MobileSessionTopBar
        sessionTitle="Mobile session title"
        detailButtonLabel="Open session details"
        backTo="/sessions"
        threads={[
          makeThread({ id: "thread-1", label: "Main tab" }),
          makeThread({ id: "thread-2", label: "Review", status: "running", agent_type: "claude_code" }),
        ]}
        activeThreadId="thread-1"
        viewedThreadIds={new Set(["thread-1"])}
        onOpenDetails={vi.fn()}
        onActiveThreadChange={vi.fn()}
        onAddThread={vi.fn()}
        onRenameSession={vi.fn()}
        onRevertThread={vi.fn()}
        onArchiveThread={vi.fn()}
        archivePendingThreadId={null}
        nonInteractiveThreadIds={new Set()}
      />,
    );

    expect(screen.getByRole("link", { name: "Back to sessions" })).toHaveClass("size-11", "sm:size-11");
    expect(screen.getByRole("button", { name: "Open session details" })).toHaveClass("size-11", "sm:size-11");
    expect(screen.getByRole("button", { name: "Open session actions" })).toHaveClass("size-11", "sm:size-11");
    expect(screen.queryByRole("tablist", { name: "Agent tabs" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Add agent tab" })).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Open session actions" }));

    const actionsSheet = await screen.findByRole("dialog", { name: "Session actions" });
    const tabsSection = within(actionsSheet).getByRole("region", { name: "Tabs" });
    expect(within(tabsSection).getByText("Switch or add a conversation lane.")).toBeInTheDocument();
    expect(within(actionsSheet).getByRole("button", { name: "Switch to Main tab" })).toBeInTheDocument();
    expect(within(actionsSheet).getByRole("button", { name: "Switch to Review (unread)" })).toBeInTheDocument();
    expect(within(tabsSection).getByRole("button", { name: "Add agent tab" })).toBeInTheDocument();
    expect(within(actionsSheet).getByRole("button", { name: "Rename session" })).toBeInTheDocument();
    expect(within(actionsSheet).getByText("Active tab")).toBeInTheDocument();
  });

  it("places the session actions button to the left of the session details button in the mobile header", () => {
    const { container } = renderWithProviders(
      <MobileSessionTopBar
        sessionTitle="Mobile session title"
        detailButtonLabel="Open session details"
        backTo="/sessions"
        threads={[makeThread({ id: "thread-1", label: "Main tab" })]}
        activeThreadId="thread-1"
        viewedThreadIds={new Set(["thread-1"])}
        onOpenDetails={vi.fn()}
        onActiveThreadChange={vi.fn()}
        onAddThread={vi.fn()}
        onRenameSession={vi.fn()}
        onRevertThread={vi.fn()}
        onArchiveThread={vi.fn()}
        archivePendingThreadId={null}
      />,
    );

    const headerButtons = Array.from(container.querySelectorAll("div.sticky button")).map((button) =>
      button.getAttribute("aria-label"),
    );

    expect(headerButtons).toEqual(["Open session actions", "Open session details"]);
  });

  it("keeps the add tab action in the tabs section even with a single tab", async () => {
    const user = userEvent.setup();

    renderWithProviders(
      <MobileSessionTopBar
        sessionTitle="Mobile session title"
        detailButtonLabel="Open session details"
        backTo="/sessions"
        threads={[makeThread({ id: "thread-1", label: "Main tab" })]}
        activeThreadId="thread-1"
        viewedThreadIds={new Set(["thread-1"])}
        onOpenDetails={vi.fn()}
        onActiveThreadChange={vi.fn()}
        onAddThread={vi.fn()}
        onRenameSession={vi.fn()}
        onRevertThread={vi.fn()}
        onArchiveThread={vi.fn()}
        archivePendingThreadId={null}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Open session actions" }));

    const actionsSheet = await screen.findByRole("dialog", { name: "Session actions" });
    const tabsSection = within(actionsSheet).getByRole("region", { name: "Tabs" });
    const sessionSection = within(actionsSheet).getByRole("region", { name: "Session" });

    expect(within(tabsSection).getByRole("button", { name: "Switch to Main tab" })).toBeInTheDocument();
    expect(within(tabsSection).getByRole("button", { name: "Add agent tab" })).toBeInTheDocument();
    expect(within(sessionSection).queryByRole("button", { name: "Add agent tab" })).not.toBeInTheDocument();
  });

  it("routes thread switching and the revert action through the session actions sheet", async () => {
    const user = userEvent.setup();
    const onActiveThreadChange = vi.fn();
    const onAddThread = vi.fn();
    const onRenameSession = vi.fn();
    const onRevertThread = vi.fn();

    renderWithProviders(
      <MobileSessionTopBar
        sessionTitle="Mobile session title"
        detailButtonLabel="Open session details"
        backTo="/sessions"
        threads={[
          makeThread({ id: "thread-1", label: "Main tab", status: "running" }),
          makeThread({ id: "thread-2", label: "Review", status: "awaiting_input", agent_type: "claude_code", diff: "" }),
        ]}
        activeThreadId="thread-1"
        viewedThreadIds={new Set(["thread-1"])}
        onOpenDetails={vi.fn()}
        onActiveThreadChange={onActiveThreadChange}
        onAddThread={onAddThread}
        onRenameSession={onRenameSession}
        onRevertThread={onRevertThread}
        onArchiveThread={vi.fn()}
        archivePendingThreadId={null}
        nonInteractiveThreadIds={new Set()}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Open session actions" }));

    let actionsSheet = await screen.findByRole("dialog", { name: "Session actions" });
    // Awaiting input and never viewed, so the row announces both states.
    await user.click(
      within(actionsSheet).getByRole("button", { name: "Switch to Review (needs attention) (unread)" }),
    );

    await user.click(screen.getByRole("button", { name: "Open session actions" }));
    actionsSheet = await screen.findByRole("dialog", { name: "Session actions" });
    await user.click(within(actionsSheet).getByRole("button", { name: "Add agent tab" }));

    await user.click(screen.getByRole("button", { name: "Open session actions" }));
    actionsSheet = await screen.findByRole("dialog", { name: "Session actions" });
    await user.click(within(actionsSheet).getByRole("button", { name: "Rename session" }));

    await user.click(screen.getByRole("button", { name: "Open session actions" }));
    actionsSheet = await screen.findByRole("dialog", { name: "Session actions" });
    expect(within(actionsSheet).queryByRole("button", { name: "Cancel turn" })).not.toBeInTheDocument();
    expect(within(actionsSheet).queryByRole("button", { name: "Fork this tab" })).not.toBeInTheDocument();
    await user.click(within(actionsSheet).getByRole("button", { name: "Revert this tab's changes" }));

    expect(onActiveThreadChange).toHaveBeenCalledWith("thread-2");
    expect(onAddThread).toHaveBeenCalledTimes(1);
    expect(onRenameSession).toHaveBeenCalledTimes(1);
    expect(onRevertThread).toHaveBeenCalledWith("thread-1");
  });

  it("keeps one status dot per mobile row, announces unread, and exposes close actions for closable tabs", async () => {
    const user = userEvent.setup();
    const onArchiveThread = vi.fn();

    renderWithProviders(
      <MobileSessionTopBar
        sessionTitle="Mobile session title"
        detailButtonLabel="Open session details"
        backTo="/sessions"
        threads={[
          makeThread({ id: "thread-1", label: "Main tab", status: "completed" }),
          makeThread({ id: "thread-2", label: "Review", status: "completed", agent_type: "claude_code" }),
        ]}
        activeThreadId="thread-1"
        viewedThreadIds={new Set(["thread-1"])}
        onOpenDetails={vi.fn()}
        onActiveThreadChange={vi.fn()}
        onAddThread={vi.fn()}
        onRenameSession={vi.fn()}
        onRevertThread={vi.fn()}
        onArchiveThread={onArchiveThread}
        archivePendingThreadId={null}
        nonInteractiveThreadIds={new Set()}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Open session actions" }));

    const actionsSheet = await screen.findByRole("dialog", { name: "Session actions" });
    expect(within(actionsSheet).getByText("Tabs")).toBeInTheDocument();
    expect(within(actionsSheet).getByRole("button", { name: "Close Main tab" })).toBeInTheDocument();
    expect(within(actionsSheet).getByRole("button", { name: "Close Review tab" })).toBeInTheDocument();
    // One dot per row, toned by status (both threads are completed), and
    // unread rides on the row's accessible name plus a brighter label.
    expect(actionsSheet.querySelectorAll('[data-slot="status-indicator"]')).toHaveLength(2);
    expect(actionsSheet.querySelectorAll('[data-slot="status-indicator-core"].bg-success')).toHaveLength(2);
    const activeRow = within(actionsSheet).getByRole("button", { name: "Switch to Main tab" });
    const unreadRow = within(actionsSheet).getByRole("button", { name: "Switch to Review (unread)" });
    expect(unreadRow.querySelector("span.truncate")).toHaveClass("text-foreground");
    // Weight never changes, so reading a row cannot resize it.
    expect(unreadRow.querySelector("span.truncate")).toHaveClass("font-medium");
    expect(activeRow.querySelector("span.truncate")).toHaveClass("font-medium");

    await user.click(within(actionsSheet).getByRole("button", { name: "Close Review tab" }));

    expect(onArchiveThread).toHaveBeenCalledWith("thread-2");
  });

  // Only settled lanes the user has already seen should recede: dimming the
  // lane they are on, or one that is still working, buries the rows that matter.
  it("keeps the active and working rows prominent while settled read rows recede", async () => {
    const user = userEvent.setup();

    renderWithProviders(
      <MobileSessionTopBar
        sessionTitle="Mobile session title"
        detailButtonLabel="Open session details"
        backTo="/sessions"
        threads={[
          makeThread({ id: "thread-1", label: "Main tab", status: "completed" }),
          makeThread({ id: "thread-2", label: "Worker", status: "running", agent_type: "claude_code" }),
          makeThread({ id: "thread-3", label: "Docs", status: "completed", agent_type: "claude_code" }),
        ]}
        activeThreadId="thread-1"
        viewedThreadIds={new Set(["thread-1", "thread-2", "thread-3"])}
        onOpenDetails={vi.fn()}
        onActiveThreadChange={vi.fn()}
        onAddThread={vi.fn()}
        onRenameSession={vi.fn()}
        onRevertThread={vi.fn()}
        onArchiveThread={vi.fn()}
        archivePendingThreadId={null}
        nonInteractiveThreadIds={new Set()}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Open session actions" }));

    const actionsSheet = await screen.findByRole("dialog", { name: "Session actions" });
    const activeRow = within(actionsSheet).getByRole("button", { name: "Switch to Main tab" });
    const runningRow = within(actionsSheet).getByRole("button", { name: "Switch to Worker" });
    const settledRow = within(actionsSheet).getByRole("button", { name: "Switch to Docs" });

    expect(activeRow.querySelector("span.truncate")).toHaveClass("text-foreground");
    expect(runningRow.querySelector("span.truncate")).toHaveClass("text-foreground");
    expect(settledRow.querySelector("span.truncate")).toHaveClass("text-muted-foreground");
  });

  // The desktop strip escalates a settled failure to a warning dot; the sheet
  // has to agree, or the same thread reads as healthy on mobile.
  it("warns on a settled thread that carries a failure, matching the desktop strip", async () => {
    const user = userEvent.setup();

    renderWithProviders(
      <MobileSessionTopBar
        sessionTitle="Mobile session title"
        detailButtonLabel="Open session details"
        backTo="/sessions"
        threads={[
          makeThread({ id: "thread-1", label: "Main tab", status: "completed" }),
          makeThread({
            id: "thread-2",
            label: "Review",
            status: "completed",
            agent_type: "claude_code",
            failure_category: "claude_code_auth_expired",
            failure_explanation: "claude subscription is marked invalid; reconnect required",
          }),
        ]}
        activeThreadId="thread-1"
        viewedThreadIds={new Set(["thread-1", "thread-2"])}
        onOpenDetails={vi.fn()}
        onActiveThreadChange={vi.fn()}
        onAddThread={vi.fn()}
        onRenameSession={vi.fn()}
        onRevertThread={vi.fn()}
        onArchiveThread={vi.fn()}
        archivePendingThreadId={null}
        nonInteractiveThreadIds={new Set()}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Open session actions" }));

    const actionsSheet = await screen.findByRole("dialog", { name: "Session actions" });
    const failedRow = within(actionsSheet).getByRole("button", {
      name: "Switch to Review (needs attention)",
    });
    const healthyRow = within(actionsSheet).getByRole("button", { name: "Switch to Main tab" });

    expect(failedRow.querySelector('[data-slot="status-indicator-core"]')).toHaveClass("bg-warning");
    expect(healthyRow.querySelector('[data-slot="status-indicator-core"]')).toHaveClass("bg-success");
  });

  it("does not allow switching to a pending preview thread from the actions sheet", async () => {
    const user = userEvent.setup();
    const onActiveThreadChange = vi.fn();

    renderWithProviders(
      <MobileSessionTopBar
        sessionTitle="Mobile session title"
        detailButtonLabel="Open session details"
        backTo="/sessions"
        threads={[
          makeThread({ id: "thread-1", label: "Main tab" }),
          makeThread({ id: "__pending-thread__", label: "Codex 2", status: "pending", current_turn: 0 }),
        ]}
        activeThreadId="thread-1"
        viewedThreadIds={new Set(["thread-1"])}
        onOpenDetails={vi.fn()}
        onActiveThreadChange={onActiveThreadChange}
        onAddThread={vi.fn()}
        onRenameSession={vi.fn()}
        onRevertThread={vi.fn()}
        onArchiveThread={vi.fn()}
        archivePendingThreadId={null}
        nonInteractiveThreadIds={new Set(["__pending-thread__"])}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Open session actions" }));

    const actionsSheet = await screen.findByRole("dialog", { name: "Session actions" });
    const pendingThreadButton = within(actionsSheet).getByRole("button", { name: "Switch to Codex 2 (unread)" });

    expect(pendingThreadButton).toBeDisabled();

    await user.click(pendingThreadButton);

    expect(onActiveThreadChange).not.toHaveBeenCalled();
  });
});

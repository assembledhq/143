import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { ChatTimeline, formatMessageTime } from "./chat-timeline";
import type { TimelineEntry } from "@/lib/timeline";
import type { SessionMessage, SessionLog } from "@/lib/types";

function makeMessage(overrides: Partial<SessionMessage> & { id: number }): SessionMessage {
  return {
    session_id: "s1",
    org_id: "o1",
    turn_number: 1,
    role: "assistant",
    content: "Hello from assistant",
    created_at: "2026-01-01T00:00:01Z",
    ...overrides,
  };
}

function makeLog(overrides: Partial<SessionLog> & { id: number; level: string }): SessionLog {
  return {
    session_id: "s1",
    message: "log message",
    metadata: null,
    turn_number: 1,
    created_at: "2026-01-01T00:00:01Z",
    ...overrides,
  };
}

describe("formatMessageTime", () => {
  it("returns time only for today's date", () => {
    const now = new Date();
    const todayISO = now.toISOString();
    const result = formatMessageTime(todayISO);
    // Should contain a colon (time) but not a month abbreviation
    expect(result).toMatch(/\d{1,2}:\d{2}/);
    expect(result).not.toMatch(/Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec/);
  });

  it("returns date and time for a past date", () => {
    const result = formatMessageTime("2024-06-15T14:30:00Z");
    // Should contain both a month and a time
    expect(result).toMatch(/Jun/);
    expect(result).toMatch(/15/);
    expect(result).toMatch(/\d{1,2}:\d{2}/);
  });

  it("returns empty string for invalid date", () => {
    expect(formatMessageTime("")).toBe("");
    expect(formatMessageTime("not-a-date")).toBe("");
  });
});

describe("ChatTimeline", () => {
  it("renders message bubbles", () => {
    const entries: TimelineEntry[] = [
      { kind: "message", data: makeMessage({ id: 1, content: "User said hi", role: "user" }) },
      { kind: "message", data: makeMessage({ id: 2, content: "Assistant replied" }) },
    ];
    render(<ChatTimeline entries={entries} isRunning={false} />);
    expect(screen.getByText("User said hi")).toBeInTheDocument();
    expect(screen.getByText("Assistant replied")).toBeInTheDocument();
  });

  it("renders tool group collapsed by default, expands on click", async () => {
    const entries: TimelineEntry[] = [
      {
        kind: "tool_group",
        toolUse: makeLog({
          id: 1,
          level: "tool_use",
          message: "using tool: Read",
          metadata: { tool: "Read", input: { file_path: "/repo/app.ts" } },
        }),
        toolResult: makeLog({ id: 2, level: "output", message: "file contents here", metadata: { type: "tool_result" } }),
      },
    ];
    render(<ChatTimeline entries={entries} isRunning={false} />);

    // Derived label visible on the row.
    expect(screen.getByText("Read app.ts")).toBeInTheDocument();
    // Result hidden initially
    expect(screen.queryByText("file contents here")).not.toBeInTheDocument();

    // Click to expand
    await userEvent.click(screen.getByText("Read app.ts"));
    expect(screen.getByText("file contents here")).toBeInTheDocument();
  });

  it("renders error entries with error styling", () => {
    const entries: TimelineEntry[] = [
      { kind: "error", data: makeLog({ id: 1, level: "error", message: "Something broke" }) },
    ];
    render(<ChatTimeline entries={entries} isRunning={false} />);
    expect(screen.getByText("Something broke")).toBeInTheDocument();
  });

  it("renders hidden logs behind a toggle", async () => {
    const entries: TimelineEntry[] = [
      { kind: "log", data: makeLog({ id: 1, level: "info", message: "Info log one" }) },
      { kind: "log", data: makeLog({ id: 2, level: "debug", message: "Debug log two" }) },
    ];
    render(<ChatTimeline entries={entries} isRunning={false} />);

    // Hidden by default
    expect(screen.queryByText("Info log one")).not.toBeInTheDocument();
    expect(screen.getByText(/2 log entries/)).toBeInTheDocument();

    // Click to reveal
    await userEvent.click(screen.getByText(/2 log entries/));
    expect(screen.getByText("Info log one")).toBeInTheDocument();
    expect(screen.getByText("Debug log two")).toBeInTheDocument();
  });

  it("shows working indicator when running", () => {
    render(<ChatTimeline entries={[]} isRunning={true} />);
    expect(screen.getByText("Agent is working...")).toBeInTheDocument();
  });

  it("shows stopping indicator instead of working indicator after stop is requested", () => {
    render(<ChatTimeline entries={[]} isRunning={true} stoppingLabel="Stopping agent..." />);
    expect(screen.getByText("Stopping agent...")).toBeInTheDocument();
    expect(screen.queryByText("Agent is working...")).not.toBeInTheDocument();
  });

  it("does not show working indicator when not running", () => {
    render(<ChatTimeline entries={[]} isRunning={false} />);
    expect(screen.queryByText("Agent is working...")).not.toBeInTheDocument();
  });

  it("shows stopped indicator when the session has stopped", () => {
    render(<ChatTimeline entries={[]} isRunning={false} stoppedLabel="Session stopped" />);
    expect(screen.getByText("Session stopped")).toBeInTheDocument();
    expect(screen.queryByText("Agent is working...")).not.toBeInTheDocument();
  });

  it("shows diff summary when diffStats has changes", () => {
    render(
      <ChatTimeline
        entries={[]}
        isRunning={false}
        diffStats={{ added: 42, removed: 7, files_changed: 3 }}
      />
    );
    expect(screen.getByText("+42")).toBeInTheDocument();
    expect(screen.getByText("-7")).toBeInTheDocument();
    expect(screen.getByText("3 files changed")).toBeInTheDocument();
  });

  it("renders diff summary below the working indicator when both are shown", () => {
    render(
      <ChatTimeline
        entries={[]}
        isRunning={true}
        diffStats={{ added: 42, removed: 7, files_changed: 3 }}
      />
    );

    const workingIndicator = screen.getByText("Agent is working...");
    const diffSummary = screen.getByText("3 files changed");

    expect(
      workingIndicator.compareDocumentPosition(diffSummary) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
  });

  it("does not show diff summary when diffStats is null", () => {
    render(
      <ChatTimeline entries={[]} isRunning={false} diffStats={null} />
    );
    expect(screen.queryByText(/files? changed/)).not.toBeInTheDocument();
  });

  it("does not show diff summary when added and removed are both zero", () => {
    render(
      <ChatTimeline
        entries={[]}
        isRunning={false}
        diffStats={{ added: 0, removed: 0, files_changed: 0 }}
      />
    );
    expect(screen.queryByText(/files? changed/)).not.toBeInTheDocument();
  });

  it("calls onDiffClick when diff summary is clicked", async () => {
    const onClick = vi.fn();
    render(
      <ChatTimeline
        entries={[]}
        isRunning={false}
        diffStats={{ added: 10, removed: 5, files_changed: 2 }}
        onDiffClick={onClick}
      />
    );

    await userEvent.click(screen.getByText("2 files changed"));
    expect(onClick).toHaveBeenCalledOnce();
  });

  it("uses singular 'file' when only one file changed", () => {
    render(
      <ChatTimeline
        entries={[]}
        isRunning={false}
        diffStats={{ added: 1, removed: 0, files_changed: 1 }}
      />
    );
    expect(screen.getByText("1 file changed")).toBeInTheDocument();
  });

  it("renders image attachments as thumbnails on user messages", () => {
    const entries: TimelineEntry[] = [
      {
        kind: "message",
        data: makeMessage({
          id: 10,
          role: "user",
          content: "See this screenshot",
          attachments: ["/uploads/org-1/screenshot.png"],
        }),
      },
    ];
    render(<ChatTimeline entries={entries} isRunning={false} />);
    expect(screen.getByText("See this screenshot")).toBeInTheDocument();
    expect(screen.getByAltText("Attached image")).toBeInTheDocument();
  });

  it("renders a visible Linear issue tag for picker-added references in the transcript", () => {
    const entries: TimelineEntry[] = [
      {
        kind: "message",
        data: makeMessage({
          id: 17,
          role: "user",
          content: "Please take this on.",
          references: [{ kind: "app", id: "ACS-44", display: "linear issue" }],
        }),
      },
    ];

    render(<ChatTimeline entries={entries} isRunning={false} />);

    expect(screen.getByText("Please take this on.")).toBeInTheDocument();
    expect(screen.getByText("Linear")).toBeInTheDocument();
    expect(screen.getByText("ACS-44")).toBeInTheDocument();
  });

  it("renders non-image attachments as file links", () => {
    const entries: TimelineEntry[] = [
      {
        kind: "message",
        data: makeMessage({
          id: 11,
          role: "user",
          content: "Here is a log",
          attachments: ["/uploads/org-1/debug.txt"],
        }),
      },
    ];
    render(<ChatTimeline entries={entries} isRunning={false} />);
    expect(screen.getByText("debug.txt")).toBeInTheDocument();
    const link = screen.getByText("debug.txt").closest("a");
    expect(link).toHaveAttribute("href", "/uploads/org-1/debug.txt");
  });

  it("opens and closes lightbox when clicking an image attachment", async () => {
    const entries: TimelineEntry[] = [
      {
        kind: "message",
        data: makeMessage({
          id: 12,
          role: "user",
          content: "",
          attachments: ["/uploads/org-1/photo.jpg"],
        }),
      },
    ];
    render(<ChatTimeline entries={entries} isRunning={false} />);

    // Click thumbnail to open lightbox.
    await userEvent.click(screen.getByAltText("Attached image"));
    // Lightbox shows a larger image with close button.
    expect(screen.getByRole("button", { name: "Close image preview" })).toBeInTheDocument();

    // Close via button.
    await userEvent.click(screen.getByRole("button", { name: "Close image preview" }));
    expect(screen.queryByRole("button", { name: "Close image preview" })).not.toBeInTheDocument();
  });

  it("renders attachments on assistant messages", () => {
    const entries: TimelineEntry[] = [
      {
        kind: "message",
        data: makeMessage({
          id: 13,
          role: "assistant",
          content: "Generated this image",
          attachments: ["/uploads/org-1/output.png"],
        }),
      },
    ];
    render(<ChatTimeline entries={entries} isRunning={false} />);
    expect(screen.getByAltText("Attached image")).toBeInTheDocument();
  });

  it("does not render attachment grid when attachments is empty", () => {
    const entries: TimelineEntry[] = [
      {
        kind: "message",
        data: makeMessage({ id: 14, role: "user", content: "No attachments", attachments: [] }),
      },
    ];
    render(<ChatTimeline entries={entries} isRunning={false} />);
    expect(screen.getByText("No attachments")).toBeInTheDocument();
    expect(screen.queryByAltText("Attached image")).not.toBeInTheDocument();
  });

  it("renders day separators inline instead of sticky", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-01-02T12:00:00Z"));

    const entries: TimelineEntry[] = [
      {
        kind: "message",
        data: makeMessage({ id: 15, created_at: "2026-01-01T08:00:00Z", content: "Yesterday message" }),
      },
      {
        kind: "message",
        data: makeMessage({ id: 16, created_at: "2026-01-02T08:00:00Z", content: "Today message" }),
      },
    ];

    render(<ChatTimeline entries={entries} isRunning={false} />);

    const yesterdayLabel = screen.getByText("Yesterday");
    const todayLabel = screen.getByText("Today");

    expect(yesterdayLabel).toBeInTheDocument();
    expect(todayLabel).toBeInTheDocument();
    expect(yesterdayLabel.parentElement?.parentElement).not.toHaveClass("sticky");
    expect(yesterdayLabel.parentElement?.parentElement).not.toHaveClass("top-0");

    vi.useRealTimers();
  });
});

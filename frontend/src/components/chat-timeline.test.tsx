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
        toolUse: makeLog({ id: 1, level: "tool_use", message: "using tool: Read", metadata: { tool: "Read" } }),
        toolResult: makeLog({ id: 2, level: "output", message: "file contents here", metadata: { type: "tool_result" } }),
      },
    ];
    render(<ChatTimeline entries={entries} isRunning={false} />);

    // Tool name badge visible
    expect(screen.getByText("Read")).toBeInTheDocument();
    // Result hidden initially
    expect(screen.queryByText("file contents here")).not.toBeInTheDocument();

    // Click to expand
    await userEvent.click(screen.getByText("Read"));
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
    expect(screen.getByText(/Show 2 log entries/)).toBeInTheDocument();

    // Click to reveal
    await userEvent.click(screen.getByText(/Show 2 log entries/));
    expect(screen.getByText("Info log one")).toBeInTheDocument();
    expect(screen.getByText("Debug log two")).toBeInTheDocument();
  });

  it("shows working indicator when running", () => {
    render(<ChatTimeline entries={[]} isRunning={true} />);
    expect(screen.getByText("Agent is working...")).toBeInTheDocument();
  });

  it("does not show working indicator when not running", () => {
    render(<ChatTimeline entries={[]} isRunning={false} />);
    expect(screen.queryByText("Agent is working...")).not.toBeInTheDocument();
  });
});

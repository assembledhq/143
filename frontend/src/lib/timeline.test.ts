import { describe, it, expect } from "vitest";
import { buildTimeline, flattenTimelineResponse } from "./timeline";
import type { SessionMessage, SessionLog } from "./types";

function makeMessage(overrides: Partial<SessionMessage> & { id: number; created_at: string }): SessionMessage {
  return {
    session_id: "s1",
    org_id: "o1",
    turn_number: 1,
    role: "assistant",
    content: "hello",
    ...overrides,
  };
}

function makeLog(overrides: Partial<SessionLog> & { id: number; created_at: string; level: string }): SessionLog {
  return {
    session_id: "s1",
    message: "log msg",
    metadata: null,
    turn_number: 1,
    ...overrides,
  };
}

describe("buildTimeline", () => {
  it("returns message entries for messages only", () => {
    const messages = [makeMessage({ id: 1, created_at: "2026-01-01T00:00:01Z" })];
    const result = buildTimeline(messages, []);
    expect(result).toHaveLength(1);
    expect(result[0].kind).toBe("message");
  });

  it("pairs tool_use with subsequent tool_result into tool_group", () => {
    const logs = [
      makeLog({ id: 1, created_at: "2026-01-01T00:00:01Z", level: "tool_use", message: "using tool: Read", metadata: { tool: "Read" } }),
      makeLog({ id: 2, created_at: "2026-01-01T00:00:02Z", level: "output", message: "file contents here", metadata: { type: "tool_result" } }),
    ];
    const result = buildTimeline([], logs);
    expect(result).toHaveLength(1);
    expect(result[0].kind).toBe("tool_group");
    if (result[0].kind === "tool_group") {
      expect(result[0].toolUse.id).toBe(1);
      expect(result[0].toolResult?.id).toBe(2);
    }
  });

  it("pairs tool uses with matching call_id even when results are not adjacent", () => {
    const logs = [
      makeLog({ id: 1, created_at: "2026-01-01T00:00:01Z", level: "tool_use", message: "using tool: Read", metadata: { tool: "Read", call_id: "call-a" } }),
      makeLog({ id: 2, created_at: "2026-01-01T00:00:02Z", level: "tool_use", message: "using tool: Bash", metadata: { tool: "Bash", call_id: "call-b" } }),
      makeLog({ id: 3, created_at: "2026-01-01T00:00:03Z", level: "output", message: "read result", metadata: { type: "tool_result", call_id: "call-a" } }),
      makeLog({ id: 4, created_at: "2026-01-01T00:00:04Z", level: "output", message: "bash result", metadata: { type: "tool_result", call_id: "call-b" } }),
    ];
    const result = buildTimeline([], logs);
    expect(result).toHaveLength(2);
    expect(result.map((entry) => entry.kind)).toEqual(["tool_group", "tool_group"]);
    if (result[0].kind === "tool_group" && result[1].kind === "tool_group") {
      expect(result[0].toolUse.id).toBe(1);
      expect(result[0].toolResult?.id).toBe(3);
      expect(result[1].toolUse.id).toBe(2);
      expect(result[1].toolResult?.id).toBe(4);
    }
  });

  it("handles tool_use without a following tool_result", () => {
    const logs = [
      makeLog({ id: 1, created_at: "2026-01-01T00:00:01Z", level: "tool_use", message: "using tool: Write", metadata: { tool: "Write" } }),
      makeLog({ id: 2, created_at: "2026-01-01T00:00:02Z", level: "info", message: "done" }),
    ];
    const result = buildTimeline([], logs);
    expect(result).toHaveLength(2);
    expect(result[0].kind).toBe("tool_group");
    if (result[0].kind === "tool_group") {
      expect(result[0].toolResult).toBeUndefined();
    }
    expect(result[1].kind).toBe("log");
  });

  it("classifies error-level logs as error entries", () => {
    const logs = [
      makeLog({ id: 1, created_at: "2026-01-01T00:00:01Z", level: "error", message: "something broke" }),
    ];
    const result = buildTimeline([], logs);
    expect(result).toHaveLength(1);
    expect(result[0].kind).toBe("error");
  });

  it("shows streamed assistant output logs as assistant_output when no persisted message exists", () => {
    const logs = [
      makeLog({ id: 1, created_at: "2026-01-01T00:00:01Z", level: "output", message: "assistant text" }),
    ];
    const result = buildTimeline([], logs);
    expect(result).toHaveLength(1);
    expect(result[0].kind).toBe("assistant_output");
  });

  it("shows both output logs and assistant message when they contain different content", () => {
    const messages = [
      makeMessage({ id: 1, created_at: "2026-01-01T00:00:03Z", content: "Fixed the bug." }),
    ];
    const logs = [
      makeLog({ id: 1, created_at: "2026-01-01T00:00:01Z", level: "output", message: "Analyzing..." }),
      makeLog({ id: 2, created_at: "2026-01-01T00:00:02Z", level: "output", message: "Found the issue." }),
    ];
    const result = buildTimeline(messages, logs);
    // Individual output logs + the final assistant message are all shown
    expect(result).toHaveLength(3);
    expect(result[0].kind).toBe("assistant_output");
    expect(result[1].kind).toBe("assistant_output");
    expect(result[2].kind).toBe("message");
  });

  it("suppresses exact duplicate output log when assistant transcript exists", () => {
    const messages = [
      makeMessage({ id: 1, created_at: "2026-01-01T00:00:03Z", content: "Found the issue." }),
    ];
    const logs = [
      makeLog({ id: 1, created_at: "2026-01-01T00:00:01Z", level: "output", message: "Analyzing..." }),
      makeLog({ id: 2, created_at: "2026-01-01T00:00:02Z", level: "output", message: "Found the issue." }),
    ];
    const result = buildTimeline(messages, logs);
    // The duplicate output log is suppressed in favor of the assistant message.
    expect(result).toHaveLength(2);
    expect(result[0].kind).toBe("assistant_output");
    expect(result[1].kind).toBe("message");
  });

  it("suppresses duplicate output log when the only difference is trailing whitespace", () => {
    const messages = [
      makeMessage({ id: 1, created_at: "2026-01-01T00:00:03Z", content: "Found the issue.\n" }),
    ];
    const logs = [
      makeLog({ id: 1, created_at: "2026-01-01T00:00:01Z", level: "output", message: "Analyzing..." }),
      makeLog({ id: 2, created_at: "2026-01-01T00:00:02Z", level: "output", message: "Found the issue." }),
    ];
    const result = buildTimeline(messages, logs);
    expect(result).toHaveLength(2);
    expect(result[0].kind).toBe("assistant_output");
    expect(result[1].kind).toBe("message");
  });

  it("keeps output log when the difference is leading indentation", () => {
    const messages = [
      makeMessage({ id: 1, created_at: "2026-01-01T00:00:03Z", content: "  Found the issue." }),
    ];
    const logs = [
      makeLog({ id: 1, created_at: "2026-01-01T00:00:01Z", level: "output", message: "Analyzing..." }),
      makeLog({ id: 2, created_at: "2026-01-01T00:00:02Z", level: "output", message: "Found the issue." }),
    ];
    const result = buildTimeline(messages, logs);
    expect(result).toHaveLength(3);
    expect(result[0].kind).toBe("assistant_output");
    expect(result[1].kind).toBe("assistant_output");
    expect(result[2].kind).toBe("message");
  });

  it("treats assistant_final output logs as visible output", () => {
    const logs = [
      makeLog({ id: 1, created_at: "2026-01-01T00:00:01Z", level: "output", message: "assistant text", metadata: { type: "assistant_final" } }),
    ];
    const result = buildTimeline([], logs);
    expect(result).toHaveLength(1);
    expect(result[0].kind).toBe("assistant_output");
  });

  it("keeps output-level logs with metadata.type as log entries", () => {
    const logs = [
      makeLog({ id: 1, created_at: "2026-01-01T00:00:01Z", level: "output", message: "tool result", metadata: { type: "tool_result" } }),
    ];
    // This would normally be consumed by a tool_group, but without a preceding tool_use it stays as a log entry.
    const result = buildTimeline([], logs);
    expect(result).toHaveLength(1);
    expect(result[0].kind).toBe("log");
  });

  it("sorts all items by created_at timestamp", () => {
    const messages = [makeMessage({ id: 1, created_at: "2026-01-01T00:00:03Z", content: "msg" })];
    const logs = [
      makeLog({ id: 1, created_at: "2026-01-01T00:00:01Z", level: "info", message: "first" }),
      makeLog({ id: 2, created_at: "2026-01-01T00:00:05Z", level: "info", message: "last" }),
    ];
    const result = buildTimeline(messages, logs);
    expect(result).toHaveLength(3);
    expect(result[0].kind).toBe("log"); // info at :01
    expect(result[1].kind).toBe("message"); // msg at :03
    expect(result[2].kind).toBe("log"); // info at :05
  });

  it("handles interleaved messages and logs correctly", () => {
    const messages = [
      makeMessage({ id: 1, created_at: "2026-01-01T00:00:00Z", role: "user", content: "fix the bug" }),
      makeMessage({ id: 2, created_at: "2026-01-01T00:00:10Z", role: "assistant", content: "done" }),
    ];
    const logs = [
      makeLog({ id: 1, created_at: "2026-01-01T00:00:02Z", level: "tool_use", message: "using tool: Read", metadata: { tool: "Read" } }),
      makeLog({ id: 2, created_at: "2026-01-01T00:00:03Z", level: "output", message: "file contents", metadata: { type: "tool_result" } }),
      makeLog({ id: 3, created_at: "2026-01-01T00:00:05Z", level: "error", message: "oops" }),
    ];
    const result = buildTimeline(messages, logs);
    expect(result.map((e) => e.kind)).toEqual(["message", "tool_group", "error", "message"]);
  });

  it("returns empty for empty inputs", () => {
    expect(buildTimeline([], [])).toEqual([]);
  });
});

describe("flattenTimelineResponse", () => {
  it("preserves human input requests separately from messages and logs", () => {
    const request = {
      id: "hir-1",
      org_id: "org-1",
      session_id: "session-1",
      turn_number: 1,
      agent_type: "claude_code" as const,
      request_kind: "action_choice" as const,
      status: "answered" as const,
      title: "Choose next action",
      body: "What next?",
      choices: [],
      created_at: "2026-01-01T00:00:00Z",
    };

    const flattened = flattenTimelineResponse([
      {
        kind: "human_input",
        created_at: request.created_at,
        human_input_request: request,
      },
    ]);

    expect(flattened.messages).toEqual([]);
    expect(flattened.logs).toEqual([]);
    expect(flattened.humanInputs).toEqual([request]);
  });
});

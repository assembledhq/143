import { describe, it, expect } from "vitest";
import { buildTimeline, flattenTimelineResponse, flattenTranscriptWindows } from "./timeline";
import type { SessionMessage, SessionLog, SessionTranscriptTurn, HumanInputRequest } from "./types";

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
  const message = overrides.message ?? "log msg";
  return {
    session_id: "s1",
    message,
    metadata: null,
    turn_number: 1,
    message_bytes: overrides.message_bytes ?? message.length,
    message_chars: overrides.message_chars ?? message.length,
    message_truncated: overrides.message_truncated ?? false,
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

  it("keeps hidden error logs behind the log toggle", () => {
    const logs = [
      makeLog({
        id: 1,
        created_at: "2026-01-01T00:00:01Z",
        level: "error",
        message: "benign diagnostic",
        metadata: { visibility: "hidden", diagnostic_class: "benign_runtime_diagnostic" },
      }),
    ];
    const result = buildTimeline([], logs);
    expect(result).toHaveLength(1);
    expect(result[0].kind).toBe("log");
  });

  it("does not classify raw Codex-looking errors without backend visibility metadata", () => {
    const logs = [
      makeLog({
        id: 1,
        created_at: "2026-01-01T00:00:01Z",
        level: "error",
        message: "2026-05-22T05:52:30.204805Z ERROR codex_core::tools::router: error=apply_patch verification failed: Failed to find expected lines in /home/sandbox/143/frontend/src/app/(dashboard)/sessions/[id]/session-detail-content.tsx:\n    const formattedMessage = composerPlanMode && activeThread?.agent_type === \"claude_code\"",
      }),
    ];
    const result = buildTimeline([], logs);
    expect(result).toHaveLength(1);
    expect(result[0].kind).toBe("error");
  });

  it("keeps backend-classified Codex diagnostics behind the log toggle", () => {
    const logs = [
      makeLog({
        id: 1,
        created_at: "2026-01-01T00:00:01Z",
        level: "error",
        message: "2026-05-22T05:52:30.204805Z ERROR codex_core::tools::router: error=apply_patch verification failed: Failed to find expected lines in /home/sandbox/143/internal/db/autopilot_queue.go:\nfunc ptrTime(t time.Time) *time.Time {\n\treturn &t\n}",
        metadata: { visibility: "hidden", diagnostic_class: "benign_runtime_diagnostic", diagnostic_source: "codex" },
      }),
      makeLog({
        id: 2,
        created_at: "2026-01-01T00:00:02Z",
        level: "error",
        message: "Reconnecting... 2/5 (stream disconnected before completion: failed to lookup address information: Try again)",
        metadata: { visibility: "hidden", diagnostic_class: "benign_runtime_diagnostic", diagnostic_source: "codex" },
      }),
      makeLog({
        id: 3,
        created_at: "2026-01-01T00:00:03Z",
        level: "error",
        message: "2026-06-12T08:54:38.209896Z ERROR codex_api::endpoint::responses_websocket: failed to connect to websocket: IO error: failed to lookup address information: Try again, url: wss://chatgpt.com/backend-api/codex/responses",
        metadata: { visibility: "hidden", diagnostic_class: "benign_runtime_diagnostic", diagnostic_source: "codex" },
      }),
      makeLog({
        id: 4,
        created_at: "2026-01-01T00:00:04Z",
        level: "error",
        message: "2026-06-12T02:31:46.399958Z ERROR codex_models_manager::manager: failed to refresh available models: timeout waiting for child process to exit",
        metadata: { visibility: "hidden", diagnostic_class: "benign_runtime_diagnostic", diagnostic_source: "codex" },
      }),
    ];
    const result = buildTimeline([], logs);
    expect(result).toHaveLength(4);
    expect(result.map((entry) => entry.kind)).toEqual(["log", "log", "log", "log"]);
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

describe("flattenTranscriptWindows", () => {
  const humanInput: HumanInputRequest = {
    id: "hir-1",
    org_id: "org-1",
    session_id: "session-1",
    turn_number: 1,
    agent_type: "claude_code",
    request_kind: "action_choice",
    status: "answered",
    title: "Choose next action",
    body: "What next?",
    choices: [],
    created_at: "2026-01-01T00:00:30Z",
  };

  it("unwraps the embedded message/log/human-input records from each turn", () => {
    const message = makeMessage({ id: 1, created_at: "2026-01-01T00:00:00Z", role: "user", content: "hi" });
    const toolUse = makeLog({ id: 10, created_at: "2026-01-01T00:00:10Z", level: "tool_use" });
    const turns: SessionTranscriptTurn[] = [
      {
        turn_number: 1,
        started_at: message.created_at,
        entries: [
          { id: "msg_1", kind: "message", created_at: message.created_at, message_id: 1, message },
          { id: "tuse_10", kind: "tool_use", created_at: toolUse.created_at, log_id: 10, log: toolUse },
          { id: "hiq_hir-1", kind: "human_input", created_at: humanInput.created_at, human_input: humanInput },
        ],
      },
    ];

    const { messages, logs, humanInputs, messageEntryIds, logEntryIds, humanInputEntryIds } = flattenTranscriptWindows(turns);

    expect(messages).toEqual([message]);
    expect(logs).toEqual([toolUse]);
    expect(humanInputs).toEqual([humanInput]);
    expect(messageEntryIds.get(1)).toBe("msg_1");
    expect(logEntryIds.get(10)).toBe("tuse_10");
    expect(humanInputEntryIds.get("hir-1")).toBe("hiq_hir-1");
  });

  it("attaches transcript entry ids to rendered timeline entries", () => {
    const message = makeMessage({ id: 1, created_at: "2026-01-01T00:00:00Z", role: "user", content: "hi" });
    const toolUse = makeLog({ id: 10, created_at: "2026-01-01T00:00:10Z", level: "tool_use" });
    const toolResult = makeLog({
      id: 11,
      created_at: "2026-01-01T00:00:11Z",
      level: "output",
      metadata: { type: "tool_result" },
    });
    const turns: SessionTranscriptTurn[] = [
      {
        turn_number: 1,
        started_at: message.created_at,
        entries: [
          { id: "msg_1", kind: "message", created_at: message.created_at, message_id: 1, message },
          { id: "tuse_10", kind: "tool_use", created_at: toolUse.created_at, log_id: 10, log: toolUse },
          { id: "tres_11", kind: "tool_result", created_at: toolResult.created_at, log_id: 11, log: toolResult },
        ],
      },
    ];

    const { messages, logs, messageEntryIds, logEntryIds } = flattenTranscriptWindows(turns);
    const entries = buildTimeline(messages, logs, { messageEntryIds, logEntryIds });

    expect(entries.map((entry) => entry.transcriptEntryId)).toEqual(["msg_1", "tuse_10"]);
  });

  it("de-duplicates records that repeat across overlapping turns/pages", () => {
    const message = makeMessage({ id: 1, created_at: "2026-01-01T00:00:00Z" });
    const log = makeLog({ id: 10, created_at: "2026-01-01T00:00:10Z", level: "output" });
    const entry = {
      message: { id: "msg_1", kind: "message" as const, created_at: message.created_at, message_id: 1, message },
      log: { id: "log_10", kind: "log" as const, created_at: log.created_at, log_id: 10, log },
    };
    const turns: SessionTranscriptTurn[] = [
      { turn_number: 1, started_at: message.created_at, entries: [entry.message, entry.log] },
      { turn_number: 1, started_at: message.created_at, entries: [entry.message, entry.log] },
    ];

    const { messages, logs } = flattenTranscriptWindows(turns);

    expect(messages.map((m) => m.id)).toEqual([1]);
    expect(logs.map((l) => l.id)).toEqual([10]);
  });

  it("skips entries that carry no embedded record and tolerates an empty input", () => {
    const turns: SessionTranscriptTurn[] = [
      {
        turn_number: 2,
        started_at: "2026-01-01T00:01:00Z",
        // e.g. a milestone/checkpoint marker with only a summary, no record.
        entries: [{ id: "milestone_1", kind: "milestone", created_at: "2026-01-01T00:01:00Z", summary: "Plan approved" }],
      },
    ];

    expect(flattenTranscriptWindows(turns)).toMatchObject({ messages: [], logs: [], humanInputs: [] });
    expect(flattenTranscriptWindows(undefined)).toMatchObject({ messages: [], logs: [], humanInputs: [] });
  });
});

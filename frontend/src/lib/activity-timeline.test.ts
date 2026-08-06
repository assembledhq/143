import { describe, expect, it } from "vitest";
import { buildActivityTimelineNodes, sanitizeActivityLabel } from "./activity-timeline";
import type { TimelineEntry } from "./timeline";
import type { SessionLog, SessionTranscriptTurn, ThreadInboxDeliveryState } from "./types";

function log(id: number, level: string, phase?: string): SessionLog {
  return {
    id, session_id: "session-1", thread_id: "thread-1", level, message: `log ${id}`,
    metadata: null, turn_number: 1, created_at: `2026-08-03T00:00:0${id}Z`,
    message_bytes: 5, message_chars: 5, message_truncated: false, activity_phase_id: phase,
  };
}

describe("buildActivityTimelineNodes", () => {
  it("groups explicit phase activity while leaving final responses visible", () => {
    const entries: TimelineEntry[] = [
      { kind: "tool_group", toolUse: log(1, "tool_use", "phase-1") },
      { kind: "log", data: log(2, "info", "phase-1") },
      { kind: "assistant_output", data: log(3, "output", "phase-1") },
    ];
    const turns: SessionTranscriptTurn[] = [{
      turn_number: 1, started_at: "2026-08-03T00:00:01Z", entries: [], phases: [{
        id: "phase-1", anchor_id: "aph_phase-1", phase_number: 1, status: "completed", trigger_kind: "initial",
        boundary_reason: "final_response", started_at: "2026-08-03T00:00:01Z",
        completed_at: "2026-08-03T00:00:03Z", tool_call_count: 1,
      }],
    }];

    expect(buildActivityTimelineNodes(entries, turns)).toEqual([
      { kind: "phase", phase: { ...turns[0].phases![0], turnNumber: 1, entries: entries.slice(0, 2), inferredHistorical: false } },
      { kind: "visible", entry: entries[2] },
    ]);
  });

  it("infers historical activity without inventing duration", () => {
    const entries: TimelineEntry[] = [
      { kind: "tool_group", toolUse: log(1, "tool_use") },
      { kind: "log", data: log(2, "info") },
    ];
    expect(buildActivityTimelineNodes(entries, [])).toEqual([{
      kind: "historical_activity",
      activity: {
        id: "historical-1-tool-1", turnNumber: 1, entries, toolCallCount: 1, inferredHistorical: true,
      },
    }]);
  });

  it("omits steering until the runtime applies it, then renders the normal message", () => {
    const message = {
      id: 7, session_id: "session-1", org_id: "org-1", thread_id: "thread-1", turn_number: 2,
      role: "user" as const, content: "also update the tests", created_at: "2026-08-03T00:00:07Z",
      inbox_sequence: 4, delivery_state: "pending" as const,
    };
    const entry: TimelineEntry = { kind: "message", data: message };
    expect(buildActivityTimelineNodes([entry], [])).toEqual([]);

    const appliedEntry: TimelineEntry = {
      kind: "message",
      data: { ...message, delivery_state: "acked", applied_at: "2026-08-03T00:00:08Z" },
    };
    expect(buildActivityTimelineNodes([appliedEntry], [])).toEqual([{ kind: "visible", entry: appliedEntry }]);
  });

  it.each([
    ["pending" as const],
    ["delivering" as const],
    ["delivered" as const],
    ["acked" as const],
    ["unknown_delivery" as const],
    ["dead_letter" as const],
  ])("omits steering still in delivery state %s", (deliveryState) => {
    const entry: TimelineEntry = {
      kind: "message",
      data: {
        id: 7, session_id: "session-1", org_id: "org-1", thread_id: "thread-1", turn_number: 2,
        role: "user" as const, content: "also update the tests", created_at: "2026-08-03T00:00:07Z",
        inbox_sequence: 4, delivery_state: deliveryState,
      },
    };
    expect(buildActivityTimelineNodes([entry], [])).toEqual([]);
  });

  // Seed messages and watermark-committed messages reach 'acked' without a
  // delivery batch, so the backend stamps applied_at for them at ack time.
  // This is the shape the transcript sees for a message that started a run.
  it("renders a run-starting message acked outside the batch path", () => {
    const entry: TimelineEntry = {
      kind: "message",
      data: {
        id: 1, session_id: "session-1", org_id: "org-1", thread_id: "thread-1", turn_number: 1,
        role: "user" as const, content: "fix transcript scrolling", created_at: "2026-08-03T00:00:01Z",
        inbox_sequence: 1, delivery_state: "acked" as const, applied_at: "2026-08-03T00:00:01Z",
      },
    };
    expect(buildActivityTimelineNodes([entry], [])).toEqual([{ kind: "visible", entry }]);
  });

  // applied_at is only written when an inbox batch actually starts, so it
  // cannot be the sole signal: an entry that never reaches a phase would be
  // hidden from the transcript forever. Anything outside the known unapplied
  // states must fail open rather than drop user-authored content.
  it("keeps a user message whose delivery state is not a known unapplied state", () => {
    const entry: TimelineEntry = {
      kind: "message",
      data: {
        id: 7, session_id: "session-1", org_id: "org-1", thread_id: "thread-1", turn_number: 2,
        role: "user" as const, content: "also update the tests", created_at: "2026-08-03T00:00:07Z",
        inbox_sequence: 4, delivery_state: "some_future_state" as unknown as ThreadInboxDeliveryState,
      },
    };
    expect(buildActivityTimelineNodes([entry], [])).toEqual([{ kind: "visible", entry }]);
  });

  it("keeps an assistant message that carries a delivery state", () => {
    const entry: TimelineEntry = {
      kind: "message",
      data: {
        id: 8, session_id: "session-1", org_id: "org-1", thread_id: "thread-1", turn_number: 2,
        role: "assistant" as const, content: "done", created_at: "2026-08-03T00:00:09Z",
        delivery_state: "pending" as const,
      },
    };
    expect(buildActivityTimelineNodes([entry], [])).toEqual([{ kind: "visible", entry }]);
  });

  it("does not split historical activity around omitted steering", () => {
    const before = { kind: "log", data: log(1, "info") } satisfies TimelineEntry;
    const queued = {
      kind: "message",
      data: {
        id: 7, session_id: "session-1", org_id: "org-1", thread_id: "thread-1", turn_number: 1,
        role: "user" as const, content: "also update the tests", created_at: "2026-08-03T00:00:02Z",
        inbox_sequence: 4, delivery_state: "pending" as const,
      },
    } satisfies TimelineEntry;
    const after = { kind: "log", data: log(2, "info") } satisfies TimelineEntry;

    expect(buildActivityTimelineNodes([before, queued, after], [])).toEqual([{
      kind: "historical_activity",
      activity: {
        id: "historical-1-log-1", turnNumber: 1, entries: [before, after], toolCallCount: 0, inferredHistorical: true,
      },
    }]);
  });

  it("derives durable visible notices around interruption and recovery phases", () => {
    const turns: SessionTranscriptTurn[] = [{
      turn_number: 1,
      started_at: "2026-08-03T00:00:01Z",
      entries: [],
      phases: [
        {
          id: "phase-1", anchor_id: "aph_phase-1", phase_number: 1, status: "interrupted",
          boundary_reason: "maintenance", trigger_kind: "initial", started_at: "2026-08-03T00:00:01Z",
          completed_at: "2026-08-03T00:00:03Z", tool_call_count: 0,
        },
        {
          id: "phase-2", anchor_id: "aph_phase-2", phase_number: 2, status: "running",
          trigger_kind: "recovery", started_at: "2026-08-03T00:00:05Z", tool_call_count: 0,
        },
      ],
    }];

    expect(buildActivityTimelineNodes([], turns)).toEqual([
      { kind: "phase", phase: { ...turns[0].phases![0], turnNumber: 1, entries: [], inferredHistorical: false } },
      {
        kind: "boundary_notice",
        notice: {
          id: "interruption-phase-1", phaseID: "phase-1", kind: "interruption",
          label: "Execution paused for maintenance.", createdAt: "2026-08-03T00:00:03Z",
        },
      },
      {
        kind: "boundary_notice",
        notice: {
          id: "recovery-phase-2", phaseID: "phase-2", kind: "recovery",
          label: "Runtime recovered and execution resumed.", createdAt: "2026-08-03T00:00:05Z",
        },
      },
      { kind: "phase", phase: { ...turns[0].phases![1], turnNumber: 1, entries: [], inferredHistorical: false } },
    ]);
  });
});

describe("sanitizeActivityLabel", () => {
  it("redacts credentials and terminal controls into a bounded single line", () => {
    expect(sanitizeActivityLabel("\u001b[31mRunning\nAPI_TOKEN=secret-value https://me:pass@example.com?a=1&token=secret", 80)).toBe(
      "Running API_TOKEN=[redacted] https://[redacted]@example.com?a=1&token=[redacted]",
    );
    expect(sanitizeActivityLabel("password: hunter2 AKIA1234567890ABCDEF")).toBe("password=[redacted] [redacted]");
  });
});

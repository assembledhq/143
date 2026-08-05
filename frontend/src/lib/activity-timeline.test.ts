import { describe, expect, it } from "vitest";
import { activityToolCount, buildActivityTimelineNodes, sanitizeActivityLabel } from "./activity-timeline";
import type { TimelineEntry } from "./timeline";
import type { SessionLog, SessionTranscriptTurn } from "./types";

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

  it("keeps unacknowledged steering in a queued delivery lane", () => {
    const message = {
      id: 7, session_id: "session-1", org_id: "org-1", thread_id: "thread-1", turn_number: 2,
      role: "user" as const, content: "also update the tests", created_at: "2026-08-03T00:00:07Z",
      inbox_sequence: 4, delivery_state: "pending" as const,
    };
    const entry: TimelineEntry = { kind: "message", data: message };
    expect(buildActivityTimelineNodes([entry], [])).toEqual([{
      kind: "queued_delivery",
      delivery: { id: "delivery-4", inboxSequence: 4, deliveryState: "queued", entry },
    }]);
  });

  it("keeps an applied steering message between the phases it separates", () => {
    const oldPhaseTool: TimelineEntry = { kind: "tool_group", toolUse: log(1, "tool_use", "phase-1") };
    const appliedMessage: TimelineEntry = {
      kind: "message",
      data: {
        id: 7, session_id: "session-1", org_id: "org-1", thread_id: "thread-1", turn_number: 1,
        role: "user", content: "use the restored anchor", created_at: "2026-08-02T23:59:00Z",
        applied_at: "2026-08-03T00:00:02Z", inbox_sequence: 4, delivery_state: "acked",
      },
    };
    const resumedTool: TimelineEntry = { kind: "tool_group", toolUse: log(3, "tool_use", "phase-2") };
    const turns: SessionTranscriptTurn[] = [{
      turn_number: 1, started_at: "2026-08-03T00:00:01Z", entries: [], phases: [
        {
          id: "phase-1", anchor_id: "aph_phase-1", phase_number: 1, status: "completed", trigger_kind: "initial",
          boundary_reason: "steered", started_at: "2026-08-03T00:00:01Z", completed_at: "2026-08-03T00:00:02Z", tool_call_count: 1,
        },
        {
          id: "phase-2", anchor_id: "aph_phase-2", phase_number: 2, status: "running", trigger_kind: "inbox_batch",
          started_at: "2026-08-03T00:00:02Z", tool_call_count: 1,
        },
      ],
    }];

    expect(buildActivityTimelineNodes([oldPhaseTool, appliedMessage, resumedTool], turns)).toEqual([
      { kind: "phase", phase: { ...turns[0].phases![0], turnNumber: 1, entries: [oldPhaseTool], inferredHistorical: false } },
      { kind: "visible", entry: appliedMessage },
      {
        kind: "phase",
        phase: {
          ...turns[0].phases![1], turnNumber: 1, entries: [resumedTool], inferredHistorical: false,
          latestActivityLabel: "log 3", provisionalToolCallCount: 1,
        },
      },
    ]);
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
      { kind: "phase", phase: { ...turns[0].phases![1], turnNumber: 1, entries: [], inferredHistorical: false, provisionalToolCallCount: 0 } },
    ]);
  });

  it("derives a deduplicated provisional tool count only for a running phase", () => {
    const entries: TimelineEntry[] = [
      { kind: "tool_group", toolUse: log(1, "tool_use", "phase-1") },
      { kind: "tool_group", toolUse: log(2, "tool_use", "phase-1") },
    ];
    const turns: SessionTranscriptTurn[] = [{
      turn_number: 1, started_at: "2026-08-03T00:00:01Z", entries: [], phases: [{
        id: "phase-1", anchor_id: "aph_phase-1", phase_number: 1, status: "running", trigger_kind: "initial",
        started_at: "2026-08-03T00:00:01Z", tool_call_count: 1,
      }],
    }];

    expect(buildActivityTimelineNodes(entries, turns)).toEqual([{
      kind: "phase",
      phase: {
        ...turns[0].phases![0], turnNumber: 1, entries, inferredHistorical: false,
        latestActivityLabel: "log 2", provisionalToolCallCount: 2,
      },
    }]);
  });

  it("never promotes content the backend marked undisplayable into the active label", () => {
    const rawResult = log(1, "output", "phase-1");
    rawResult.message = "SECRET_RESULT=do-not-summarize";
    rawResult.metadata = { type: "tool_result" };
    const hiddenDiagnostic = log(2, "info", "phase-1");
    hiddenDiagnostic.message = "codex stream disconnected, retrying";
    hiddenDiagnostic.metadata = { visibility: "hidden", diagnostic_class: "benign_runtime_diagnostic" };
    const entries: TimelineEntry[] = [
      { kind: "log", data: rawResult },
      { kind: "log", data: hiddenDiagnostic },
    ];
    const turns: SessionTranscriptTurn[] = [{
      turn_number: 1, started_at: rawResult.created_at, entries: [], phases: [{
        id: "phase-1", anchor_id: "aph_phase-1", phase_number: 1, status: "running", trigger_kind: "initial",
        started_at: rawResult.created_at, tool_call_count: 0,
      }],
    }];

    expect(buildActivityTimelineNodes(entries, turns)).toEqual([{
      kind: "phase",
      phase: { ...turns[0].phases![0], turnNumber: 1, entries, inferredHistorical: false, provisionalToolCallCount: 0 },
    }]);
  });
});

describe("activityToolCount", () => {
  const runningPhase = {
    id: "phase-1", anchor_id: "aph_phase-1", phase_number: 1, status: "running" as const, trigger_kind: "initial" as const,
    started_at: "2026-08-03T00:00:01Z", tool_call_count: 1, turnNumber: 1, entries: [], inferredHistorical: false as const,
  };

  it("prefers the locally derived count while the server counter lags a running phase", () => {
    expect(activityToolCount({ ...runningPhase, provisionalToolCallCount: 3 })).toBe(3);
    expect(activityToolCount({ ...runningPhase, tool_call_count: 5, provisionalToolCallCount: 3 })).toBe(5);
    expect(activityToolCount(runningPhase)).toBe(1);
  });

  it("settles on the authoritative server count once the phase closes", () => {
    expect(activityToolCount({
      ...runningPhase, status: "completed", tool_call_count: 4, provisionalToolCallCount: 9,
    })).toBe(4);
  });

  it("uses the inferred count for historical activity", () => {
    expect(activityToolCount({
      id: "historical-1", turnNumber: 1, entries: [], toolCallCount: 2, inferredHistorical: true,
    })).toBe(2);
  });
});

describe("sanitizeActivityLabel", () => {
  it("redacts credentials and terminal controls into a bounded single line", () => {
    expect(sanitizeActivityLabel("\u001b[31mRunning\nAPI_TOKEN=secret-value https://me:pass@example.com?a=1&token=secret", 80)).toBe(
      "Running API_TOKEN=[redacted] https://[redacted]@example.com?a=1&token=[redacted]",
    );
    expect(sanitizeActivityLabel("password: hunter2 AKIA1234567890ABCDEF")).toBe("password=[redacted] [redacted]");
    expect(sanitizeActivityLabel("Connecting postgres://dbuser:dbpass@db.example/app?sslmode=require&x-amz-signature=signed")).toBe(
      "Connecting postgres://[redacted]@db.example/app?sslmode=require&x-amz-signature=[redacted]",
    );
  });

  it("matches sensitive query parameter names on segment boundaries", () => {
    expect(sanitizeActivityLabel("GET /v1?apikey=abc&token_secret=def&x-amz-credential=ghi")).toBe(
      "GET /v1?apikey=[redacted]&token_secret=[redacted]&x-amz-credential=[redacted]",
    );
    // Words that merely contain a sensitive substring must survive intact,
    // otherwise ordinary labels turn into noise.
    expect(sanitizeActivityLabel("GET /zoo?monkey=1&keyboard=us&author=ada&sslmode=require")).toBe(
      "GET /zoo?monkey=1&keyboard=us&author=ada&sslmode=require",
    );
  });

  it("keeps a redaction inside its own query parameter", () => {
    // Regression: the NAME=value rule used to run to the next whitespace, so a
    // secret mid-query erased every parameter after it.
    expect(sanitizeActivityLabel("GET /v1?SECRET=abc&page=2&sort=asc")).toBe(
      "GET /v1?SECRET=[redacted]&page=2&sort=asc",
    );
  });

  it("redacts temporary AWS access keys and app-level Slack tokens", () => {
    expect(sanitizeActivityLabel("assume ASIA1234567890ABCDEF then post xapp-1-A01-2345678901-abcdef")).toBe(
      "assume [redacted] then post [redacted]",
    );
    expect(sanitizeActivityLabel("refresh xoxe-1-abcdefghijkl")).toBe("refresh [redacted]");
  });
});

import { describe, it, expect, vi, afterEach } from "vitest";
import {
  formatDuration,
  getInitialComposerSelectedModel,
  getPendingEditableThreadUpdate,
  trackInFlightAgentUpdate,
} from "./session-detail-content";
import type { SessionThread } from "@/lib/types";

const start = "2026-01-01T00:00:00.000Z";
const plus = (ms: number) => new Date(new Date(start).getTime() + ms).toISOString();

describe("formatDuration", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("returns '-' when startedAt is missing", () => {
    expect(formatDuration(undefined)).toBe("-");
    expect(formatDuration("")).toBe("-");
  });

  it("formats sub-minute durations as seconds", () => {
    expect(formatDuration(start, plus(0))).toBe("0s");
    expect(formatDuration(start, plus(45_000))).toBe("45s");
    expect(formatDuration(start, plus(59_999))).toBe("59s");
  });

  it("formats sub-hour durations as minutes and seconds", () => {
    expect(formatDuration(start, plus(60_000))).toBe("1m 0s");
    expect(formatDuration(start, plus(5 * 60_000 + 17_000))).toBe("5m 17s");
    expect(formatDuration(start, plus(59 * 60_000 + 59_000))).toBe("59m 59s");
  });

  it("formats sub-day durations as hours and minutes", () => {
    expect(formatDuration(start, plus(60 * 60_000))).toBe("1h 0m");
    expect(formatDuration(start, plus(17 * 60 * 60_000 + 57 * 60_000 + 17_000))).toBe("17h 57m");
    expect(formatDuration(start, plus(23 * 60 * 60_000 + 59 * 60_000))).toBe("23h 59m");
  });

  it("formats multi-day durations as days and hours", () => {
    expect(formatDuration(start, plus(24 * 60 * 60_000))).toBe("1d 0h");
    expect(formatDuration(start, plus(3 * 24 * 60 * 60_000 + 5 * 60 * 60_000))).toBe("3d 5h");
    expect(formatDuration(start, plus(45 * 24 * 60 * 60_000 + 23 * 60 * 60_000))).toBe("45d 23h");
  });

  it("uses current time when completedAt is omitted", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date(plus(2 * 60 * 60_000 + 30 * 60_000)));
    expect(formatDuration(start)).toBe("2h 30m");
  });
});

describe("getPendingEditableThreadUpdate", () => {
  const editableThread: SessionThread = {
    id: "thread-1",
    session_id: "session-1",
    org_id: "org-1",
    agent_type: "codex",
    label: "Codex 2",
    status: "idle",
    current_turn: 0,
    model_override: "gpt-5.4",
    created_at: "2026-01-01T00:00:00.000Z",
    cost_cents: 0,
    pending_message_count: 0,
  };

  it("does not clear an existing model override when the composer model is untouched", () => {
    expect(getPendingEditableThreadUpdate(editableThread, true, "")).toBeUndefined();
  });

  it("returns an update when the user selects a different model", () => {
    expect(getPendingEditableThreadUpdate(editableThread, true, "gpt-5.4-mini")).toEqual({
      label: "Codex 2",
      model: "gpt-5.4-mini",
    });
  });
});

describe("getInitialComposerSelectedModel", () => {
  const baseThread: SessionThread = {
    id: "thread-1",
    session_id: "session-1",
    org_id: "org-1",
    agent_type: "codex",
    label: "Codex 2",
    status: "idle",
    current_turn: 0,
    created_at: "2026-01-01T00:00:00.000Z",
    cost_cents: 0,
    pending_message_count: 0,
  };

  it("uses a created thread's inherited model override as the composer selection", () => {
    expect(getInitialComposerSelectedModel({ ...baseThread, model_override: "gpt-5.4" })).toBe("gpt-5.4");
  });

  it("uses the default composer selection when the created thread has no override", () => {
    expect(getInitialComposerSelectedModel(baseThread)).toBe("");
  });
});

describe("trackInFlightAgentUpdate", () => {
  it("clears the tracked promise and consumes rejections", async () => {
    const ref: { current: Promise<unknown> | null } = { current: null };
    const promise = Promise.reject(new Error("patch failed"));

    trackInFlightAgentUpdate(ref, promise);
    await expect(promise).rejects.toThrow("patch failed");
    await new Promise((resolve) => setTimeout(resolve, 0));

    expect(ref.current).toBeNull();
  });
});

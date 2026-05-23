import { describe, it, expect, vi, afterEach } from "vitest";
import {
  filterThreadLogsForLoadedMessages,
  flattenThreadMessageWindows,
  formatDuration,
  getInitialComposerSelectedModel,
  getPendingEditableThreadUpdate,
  hasCleanReviewLoopForSnapshot,
  invalidateSessionHumanInputRequests,
  trackInFlightAgentUpdate,
} from "./session-detail-content";
import type { SessionLog, SessionMessage, SessionReviewLoop, SessionThread, ThreadMessageWindowResponse } from "@/lib/types";

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

describe("hasCleanReviewLoopForSnapshot", () => {
  const baseLoop: SessionReviewLoop = {
    id: "loop-1",
    org_id: "org-1",
    session_id: "session-1",
    status: "clean",
    source: "manual",
    agent_type: "codex",
    max_passes: 2,
    fix_mode: "minimal",
    completed_passes: 1,
    review_required: false,
    started_at: "2026-01-01T00:00:00.000Z",
    completed_at: "2026-01-01T00:01:00.000Z",
  };

  it("requires a clean review loop on the current session snapshot", () => {
    expect(hasCleanReviewLoopForSnapshot([
      { ...baseLoop, latest_checkpoint_key: "snap-older" },
      { ...baseLoop, id: "loop-2", status: "failed", latest_checkpoint_key: "snap-current" },
    ], "snap-current")).toBe(false);

    expect(hasCleanReviewLoopForSnapshot([
      { ...baseLoop, latest_checkpoint_key: "snap-current" },
    ], "snap-current")).toBe(true);
  });

  it("does not allow missing snapshot or missing review checkpoint", () => {
    expect(hasCleanReviewLoopForSnapshot([{ ...baseLoop, latest_checkpoint_key: undefined }], "snap-current")).toBe(false);
    expect(hasCleanReviewLoopForSnapshot([{ ...baseLoop, latest_checkpoint_key: "snap-current" }], undefined)).toBe(false);
  });
});

describe("invalidateSessionHumanInputRequests", () => {
  it("invalidates the shared prefix so thread-scoped all-status queries refresh", () => {
    const queryClient = {
      invalidateQueries: vi.fn(),
    };

    invalidateSessionHumanInputRequests(queryClient, "session-1");

    expect(queryClient.invalidateQueries).toHaveBeenCalledWith({
      queryKey: ["session", "session-1", "human-input-requests"],
    });
  });
});

describe("thread message windows", () => {
  function message(id: number, turn: number): SessionMessage {
    return {
      id,
      session_id: "session-1",
      org_id: "org-1",
      thread_id: "thread-1",
      turn_number: turn,
      role: "user",
      content: `message ${id}`,
      created_at: start,
    };
  }

  function log(id: number, turn: number): SessionLog {
    return {
      id,
      session_id: "session-1",
      thread_id: "thread-1",
      level: "output",
      message: `log ${id}`,
      turn_number: turn,
      created_at: start,
      metadata: null,
    };
  }

  it("flattens newest-first window pages into chronological transcript order", () => {
    const pages: ThreadMessageWindowResponse[] = [
      {
        data: [message(3, 2), message(4, 2)],
        meta: { has_older: true, next_older_cursor: "3", thread_status: "idle" },
      },
      {
        data: [message(1, 1), message(2, 1)],
        meta: { has_older: false, thread_status: "idle" },
      },
    ];

    expect(flattenThreadMessageWindows(pages).map((item) => item.id)).toEqual([1, 2, 3, 4]);
  });

  it("keeps thread logs only for turns represented by loaded message windows", () => {
    expect(filterThreadLogsForLoadedMessages(
      [log(10, 1), log(20, 2), log(30, 3)],
      [message(1, 1), message(2, 3)],
    ).map((item) => item.id)).toEqual([10, 30]);
  });

  it("keeps logs when a legacy thread has no persisted messages", () => {
    expect(filterThreadLogsForLoadedMessages(
      [log(10, 1), log(20, 1)],
      [],
    ).map((item) => item.id)).toEqual([10, 20]);
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

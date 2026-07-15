import { describe, it, expect, vi, afterEach } from "vitest";
import { pollMs } from "@/lib/poll-intervals";
import {
  formatDuration,
  getDisplayStatus,
  deriveEffectivePRStatus,
  hasMeaningfulDuration,
  getInitialComposerSelectedModel,
  getPendingEditableThreadUpdate,
  hasCleanReviewLoopForSnapshot,
  invalidateSessionHumanInputRequests,
  applyThreadInboxEventToThreads,
  applyThreadRuntimeEventToThreads,
  capLiveSessionLogMessage,
  liveLogsForTimeline,
  mergeSessionLogListResponse,
  mergeSessionDetailStatusUpdate,
  mergePendingMessages,
  messageReconciliationKey,
  statusConfig,
  trackInFlightAgentUpdate,
  buildChromeThreads,
  getPullRequestHealthRefetchInterval,
} from "./session-detail-state";
import type { PullRequestHealthResponse, SessionDetail, SessionLog, SessionMessage, SessionReviewLoop, SessionThread } from "@/lib/types";

const start = "2026-01-01T00:00:00.000Z";
const plus = (ms: number) => new Date(new Date(start).getTime() + ms).toISOString();

const baseHealth: PullRequestHealthResponse = {
  pull_request_id: "pr-1",
  pull_request_number: 42,
  repository: "assembledhq/143",
  url: "https://github.com/assembledhq/143/pull/42",
  status: "open",
  head_sha: "head",
  base_sha: "base",
  health_version: 1,
  sync_status: "synced",
  merge_state: "clean",
  has_conflicts: false,
  failing_test_count: 0,
  needs_agent_action: false,
  summary: "healthy",
  checks: [],
  checks_confirmed: true,
  can_resolve_conflicts: false,
  can_fix_tests: false,
  can_merge: true,
  enrichment_status: "ready",
  enrichment_requested: false,
  enrichment_ready: true,
  conflict_detail_available: false,
  failing_test_detail_available: false,
  merge_when_ready: { state: "off" },
};

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

describe("getPullRequestHealthRefetchInterval", () => {
  it("does not poll blocked PR health for disconnected repositories", () => {
    expect(getPullRequestHealthRefetchInterval({
      ...baseHealth,
      sync_status: "blocked",
      sync_blocker: "repository_disconnected",
      merge_state: "unknown",
    })).toBe(false);
  });

  it("keeps polling real pending mergeability", () => {
    expect(getPullRequestHealthRefetchInterval({
      ...baseHealth,
      sync_status: "pending",
      merge_state: "mergeability_pending",
    })).toBe(pollMs(5_000));
  });
});

describe("hasMeaningfulDuration", () => {
  it("is false when either timestamp is missing", () => {
    expect(hasMeaningfulDuration(undefined, plus(5000))).toBe(false);
    expect(hasMeaningfulDuration(start, undefined)).toBe(false);
    expect(hasMeaningfulDuration(undefined, undefined)).toBe(false);
  });

  it("is false for durations under one second", () => {
    expect(hasMeaningfulDuration(start, plus(0))).toBe(false);
    expect(hasMeaningfulDuration(start, plus(999))).toBe(false);
  });

  it("is true once the duration reaches one second", () => {
    expect(hasMeaningfulDuration(start, plus(1000))).toBe(true);
    expect(hasMeaningfulDuration(start, plus(5000))).toBe(true);
  });
});

describe("getDisplayStatus", () => {
  it("maps a pr_created session to merged styling when the PR is merged", () => {
    expect(getDisplayStatus("pr_created", "merged")).toBe(statusConfig.pr_merged);
  });

  it("maps a pr_created session to closed styling when the PR is closed", () => {
    expect(getDisplayStatus("pr_created", "closed")).toBe(statusConfig.pr_closed);
  });

  it("keeps the pr_created styling when the PR is still open or unknown", () => {
    expect(getDisplayStatus("pr_created", "open")).toBe(statusConfig.pr_created);
    expect(getDisplayStatus("pr_created", null)).toBe(statusConfig.pr_created);
    expect(getDisplayStatus("pr_created")).toBe(statusConfig.pr_created);
  });

  it("looks up non-pr statuses directly and ignores any PR status", () => {
    expect(getDisplayStatus("running", "merged")).toBe(statusConfig.running);
    expect(getDisplayStatus("failed")).toBe(statusConfig.failed);
  });

  it("falls back to the pending config for an unknown status", () => {
    expect(getDisplayStatus("totally-unknown" as never)).toBe(statusConfig.pending);
  });
});

describe("deriveEffectivePRStatus", () => {
  it("prefers merged from either the PR or its health status", () => {
    expect(deriveEffectivePRStatus("open", "merged")).toBe("merged");
    expect(deriveEffectivePRStatus("merged", null)).toBe("merged");
  });

  it("prefers closed when nothing is merged", () => {
    expect(deriveEffectivePRStatus("open", "closed")).toBe("closed");
    expect(deriveEffectivePRStatus("closed", null)).toBe("closed");
  });

  it("ranks merged above closed when both appear", () => {
    expect(deriveEffectivePRStatus("closed", "merged")).toBe("merged");
    expect(deriveEffectivePRStatus("merged", "closed")).toBe("merged");
  });

  it("falls back to the raw PR status, then undefined", () => {
    expect(deriveEffectivePRStatus("open", null)).toBe("open");
    expect(deriveEffectivePRStatus(null, null)).toBeUndefined();
    expect(deriveEffectivePRStatus()).toBeUndefined();
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

describe("thread SSE event reducers", () => {
  const threads: SessionThread[] = [
    {
      id: "thread-1",
      session_id: "session-1",
      org_id: "org-1",
      agent_type: "codex",
      label: "Main",
      status: "running",
      current_turn: 1,
      created_at: "2026-01-01T00:00:00.000Z",
      cost_cents: 0,
      pending_message_count: 0,
    },
    {
      id: "thread-2",
      session_id: "session-1",
      org_id: "org-1",
      agent_type: "claude_code",
      label: "Review",
      status: "idle",
      current_turn: 0,
      created_at: "2026-01-01T00:00:00.000Z",
      cost_cents: 0,
      pending_message_count: 0,
    },
  ];

  it("updates only the matching thread inbox count", () => {
    const next = applyThreadInboxEventToThreads(threads, {
      session_id: "session-1",
      thread_id: "thread-2",
      org_id: "org-1",
      pending_message_count: 3,
    });

    expect(next[0]).toBe(threads[0]);
    expect(next[1].pending_message_count).toBe(3);
  });

  it("updates only the matching thread runtime fields", () => {
    const next = applyThreadRuntimeEventToThreads(threads, {
      session_id: "session-1",
      thread_id: "thread-1",
      org_id: "org-1",
      status: "idle",
      current_turn: 2,
      pending_message_count: 0,
      last_activity_at: "2026-01-01T00:05:00.000Z",
    });

    expect(next[0].status).toBe("idle");
    expect(next[0].current_turn).toBe(2);
    expect(next[0].last_activity_at).toBe("2026-01-01T00:05:00.000Z");
    expect(next[1]).toBe(threads[1]);
  });
});

describe("mergeSessionDetailStatusUpdate", () => {
  const baseThread: SessionThread = {
    id: "thread-1",
    session_id: "session-1",
    org_id: "org-1",
    agent_type: "codex",
    label: "Main",
    status: "running",
    current_turn: 1,
    created_at: "2026-01-01T00:00:00.000Z",
    cost_cents: 0,
    pending_message_count: 0,
  };

  const baseDetail: SessionDetail = {
    id: "session-1",
    org_id: "org-1",
    agent_type: "codex",
    status: "running",
    autonomy_level: "semi",
    token_mode: "standard",
    current_turn: 1,
    last_activity_at: "2026-01-01T00:00:00.000Z",
    sandbox_state: "running",
    changesets: [],
    created_at: "2026-01-01T00:00:00.000Z",
    threads: [baseThread],
  };

  it("preserves threads from an initial SSE status payload when the detail cache is empty", () => {
    const next = mergeSessionDetailStatusUpdate(undefined, baseDetail);

    expect(next.data.threads).toEqual([baseThread]);
  });

  it("settles existing running threads when an old status payload omits thread detail", () => {
    const next = mergeSessionDetailStatusUpdate(
      { data: baseDetail },
      {
        ...baseDetail,
        status: "idle",
        threads: [],
      },
    );

    expect(next.data.status).toBe("idle");
    expect(next.data.threads).toEqual([{ ...baseThread, status: "idle" }]);
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

describe("buildChromeThreads", () => {
  const baseThread: SessionThread = {
    id: "thread-1",
    session_id: "session-1",
    org_id: "org-1",
    agent_type: "codex",
    label: "Main",
    status: "idle",
    current_turn: 0,
    created_at: "2026-01-01T00:00:00.000Z",
    cost_cents: 0,
    pending_message_count: 0,
  };

  it("appends a pending preview while the matching real thread is not present", () => {
    const pending = {
      ...baseThread,
      id: "__pending-thread__",
      label: "Codex 2",
      status: "pending" as const,
      created_at: "2026-01-01T00:00:01.000Z",
    };

    expect(buildChromeThreads([baseThread], pending)).toEqual([baseThread, pending]);
  });

  it("drops the pending preview once the real created thread is in the session detail cache", () => {
    const pending = {
      ...baseThread,
      id: "__pending-thread__",
      label: "Codex 2",
      status: "pending" as const,
      model_override: "gpt-5.4",
      created_at: "2026-01-01T00:00:01.000Z",
    };
    const created = {
      ...baseThread,
      id: "thread-2",
      label: "Codex 2",
      model_override: "gpt-5.4",
      created_at: "2026-01-01T00:00:02.000Z",
    };

    expect(buildChromeThreads([baseThread, created], pending)).toEqual([baseThread, created]);
  });

  it("drops the pending preview when its id directly matches a thread (review-loop case)", () => {
    const reviewThread = {
      ...baseThread,
      id: "thread-review-1",
      label: "Review",
      status: "pending" as const,
      created_at: "2026-01-01T00:00:01.000Z",
    };
    const realThread = { ...baseThread, id: "thread-review-1", label: "Review" };

    expect(buildChromeThreads([baseThread, realThread], reviewThread)).toEqual([baseThread, realThread]);
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

describe("live log merging helpers", () => {
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
      message_bytes: `log ${id}`.length,
      message_chars: `log ${id}`.length,
      message_truncated: false,
    };
  }

  it("merges streamed logs into cached log responses without duplicating ids", () => {
    const result = mergeSessionLogListResponse(
      {
        data: [log(10, 1), log(30, 3)],
        meta: { next_cursor: "older" },
      },
      [log(20, 2), { ...log(30, 3), message: "canonical update" }],
    );

    expect(result.meta).toEqual({ next_cursor: "older" });
    expect(result.data.map((item) => item.id)).toEqual([10, 20, 30]);
    expect(result.data[2].message).toBe("canonical update");
  });

  it("caps live streamed log buffers while keeping the newest entries", () => {
    const result = mergeSessionLogListResponse(
      {
        data: [log(10, 1), log(20, 1)],
        meta: {},
      },
      [log(30, 2), log(40, 2)],
      3,
    );

    expect(result.data.map((item) => item.id)).toEqual([20, 30, 40]);
  });

  it("caps oversized live streamed log messages before caching them", () => {
    const result = capLiveSessionLogMessage({
      ...log(99, 2),
      message: "a".repeat(40 * 1024),
      message_bytes: 40 * 1024,
      message_chars: 40 * 1024,
    });

    expect(result.message).toHaveLength(32 * 1024);
    expect(result.message_truncated).toBe(true);
    expect(result.message_bytes).toBe(40 * 1024);
    expect(result.message_chars).toBe(40 * 1024);
  });

  it("drops stale live logs for settled timelines", () => {
    const liveLogs = [log(50, 3)];

    expect(liveLogsForTimeline(true, liveLogs)).toEqual(liveLogs);
    expect(liveLogsForTimeline(false, liveLogs)).toEqual([]);
  });

});

describe("messageReconciliationKey", () => {
  function message(overrides: Partial<SessionMessage> = {}): SessionMessage {
    return {
      id: 1,
      session_id: "session-1",
      org_id: "org-1",
      thread_id: "thread-1",
      turn_number: 1,
      role: "user",
      content: "hello",
      created_at: start,
      ...overrides,
    };
  }

  it("ignores id and timestamp so an optimistic message matches its persisted twin", () => {
    expect(messageReconciliationKey(message({ id: 1, created_at: start }))).toBe(
      messageReconciliationKey(message({ id: 999, created_at: plus(5000) })),
    );
  });

  it("treats an omitted thread_id the same as an explicit null thread", () => {
    const omitted = message();
    delete omitted.thread_id;
    expect(messageReconciliationKey(omitted)).toBe(
      messageReconciliationKey({ ...message(), thread_id: undefined }),
    );
  });

  it("distinguishes messages that differ in reconciled content", () => {
    expect(messageReconciliationKey(message({ content: "a" }))).not.toBe(
      messageReconciliationKey(message({ content: "b" })),
    );
    expect(messageReconciliationKey(message({ role: "user" }))).not.toBe(
      messageReconciliationKey(message({ role: "assistant" })),
    );
  });
});

describe("mergePendingMessages", () => {
  function message(overrides: Partial<SessionMessage> = {}): SessionMessage {
    return {
      id: 1,
      session_id: "session-1",
      org_id: "org-1",
      thread_id: "thread-1",
      turn_number: 1,
      role: "user",
      content: "hello",
      created_at: start,
      ...overrides,
    };
  }

  it("returns the base list unchanged when there are no pending messages", () => {
    const base = [message()];
    expect(mergePendingMessages(base, [])).toBe(base);
  });

  it("appends pending messages that are not already present", () => {
    const base = [message({ id: 1, content: "first" })];
    const pending = [message({ id: 2, content: "second", turn_number: 2 })];

    expect(mergePendingMessages(base, pending).map((m) => m.content)).toEqual(["first", "second"]);
  });

  it("skips pending messages whose id already exists in the base list", () => {
    const base = [message({ id: 7, content: "canonical" })];
    const pending = [message({ id: 7, content: "stale optimistic copy" })];

    expect(mergePendingMessages(base, pending)).toEqual(base);
  });

  it("skips an optimistic message once its persisted twin lands with a new id", () => {
    const base = [message({ id: 42, content: "echo", turn_number: 3 })];
    const pending = [message({ id: -1, content: "echo", turn_number: 3 })];

    expect(mergePendingMessages(base, pending)).toEqual(base);
  });

  it("does not append the same pending message twice", () => {
    const pending = message({ id: -1, content: "dupe" });

    expect(mergePendingMessages([], [pending, pending])).toEqual([pending]);
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

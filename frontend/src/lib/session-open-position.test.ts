import { describe, expect, it } from "vitest";
import { buildTimeline, type TimelineEntry } from "./timeline";
import type { SessionLog, SessionMessage } from "./types";
import {
  findLatestAssistantTurnStartIndex,
  getSessionActiveThreadStorageKey,
  getSessionScrollStorageKey,
  readStoredSessionAnchorPosition,
  readStoredSessionActiveThread,
  readStoredSessionScrollPosition,
  resolveInitialSessionThreadId,
  resolveInitialSessionAnchor,
  writeStoredSessionAnchorPosition,
  writeStoredSessionActiveThread,
  writeStoredSessionScrollPosition,
} from "./session-open-position";

const viewerScope = { userId: "user-1", orgId: "org-1" };

function makeMessage(overrides: Partial<SessionMessage>): SessionMessage {
  return {
    id: overrides.id ?? 1,
    session_id: overrides.session_id ?? "session-1",
    org_id: overrides.org_id ?? "org-1",
    user_id: overrides.user_id,
    turn_number: overrides.turn_number ?? 0,
    role: overrides.role ?? "user",
    content: overrides.content ?? "message",
    created_at: overrides.created_at ?? "2026-04-22T10:00:00Z",
  };
}

function makeLog(overrides: Partial<SessionLog>): SessionLog {
  const message = overrides.message ?? "assistant output";
  return {
    id: overrides.id ?? 1,
    session_id: overrides.session_id ?? "session-1",
    level: overrides.level ?? "output",
    message,
    created_at: overrides.created_at ?? "2026-04-22T10:00:01Z",
    turn_number: overrides.turn_number ?? 1,
    metadata: overrides.metadata ?? null,
    message_bytes: overrides.message_bytes ?? message.length,
    message_chars: overrides.message_chars ?? message.length,
    message_truncated: overrides.message_truncated ?? false,
  };
}

describe("session-open-position", () => {
  it("builds a stable per-session scroll storage key", () => {
    expect(getSessionScrollStorageKey("sess-123", viewerScope)).toBe("session-scroll-position:org-1:user-1:sess-123");
  });

  it("builds distinct scroll storage keys for different threads in one session", () => {
    expect(getSessionScrollStorageKey("sess-123", viewerScope, "thread-a")).toBe(
      "session-scroll-position:org-1:user-1:sess-123:thread-a",
    );
    expect(getSessionScrollStorageKey("sess-123", viewerScope, "thread-a")).not.toBe(
      getSessionScrollStorageKey("sess-123", viewerScope, "thread-b"),
    );
  });

  it("builds a stable per-session active-thread storage key", () => {
    expect(getSessionActiveThreadStorageKey("sess-123", viewerScope)).toBe(
      "session-active-thread:org-1:user-1:sess-123",
    );
  });

  it("reads a stored thread-specific session position when present", () => {
    const storage = new Map<string, string>([
      ["session-scroll-position:org-1:user-1:sess-123:thread-a", JSON.stringify({ version: 1, scrollTop: 240 })],
    ]);

    expect(readStoredSessionScrollPosition(storage, "sess-123", viewerScope, "thread-a")).toBe(240);
    expect(readStoredSessionScrollPosition(storage, "sess-123", viewerScope, "thread-b")).toBeNull();
  });

  it("reads a stored active thread when present", () => {
    const storage = new Map<string, string>([
      ["session-active-thread:org-1:user-1:sess-123", JSON.stringify({ version: 1, threadId: "thread-b" })],
    ]);

    expect(readStoredSessionActiveThread(storage, "sess-123", viewerScope)).toBe("thread-b");
  });

  it("scopes storage keys by viewer identity", () => {
    expect(
      getSessionScrollStorageKey("sess-123", { userId: "user-1", orgId: "org-1" }),
    ).not.toBe(
      getSessionScrollStorageKey("sess-123", { userId: "user-2", orgId: "org-1" }),
    );
  });

  it("returns null for missing or invalid stored positions", () => {
    const emptyStorage = new Map<string, string>();
    const invalidStorage = new Map<string, string>([["session-scroll-position:org-1:user-1:sess-123", "-20"]]);

    expect(readStoredSessionScrollPosition(emptyStorage, "sess-123", viewerScope)).toBeNull();
    expect(readStoredSessionScrollPosition(invalidStorage, "sess-123", viewerScope)).toBeNull();
  });

  it("reads a stored session position when present", () => {
    const storage = new Map<string, string>([["session-scroll-position:org-1:user-1:sess-123", "480"]]);

    expect(readStoredSessionScrollPosition(storage, "sess-123", viewerScope)).toBe(480);
  });

  it("ignores legacy zero scroll positions so old top-of-page state does not mask new reopen behavior", () => {
    const storage = new Map<string, string>([["session-scroll-position:org-1:user-1:sess-123", "0"]]);

    expect(readStoredSessionScrollPosition(storage, "sess-123", viewerScope)).toBeNull();
  });

  it("reads structured saved positions including an intentional top-of-thread value", () => {
    const storage = new Map<string, string>([["session-scroll-position:org-1:user-1:sess-123", JSON.stringify({ version: 1, scrollTop: 0 })]]);

    expect(readStoredSessionScrollPosition(storage, "sess-123", viewerScope)).toBe(0);
  });

  it("reads and writes structured anchor positions for fast restore", () => {
    const storage = new Map<string, string>();

    writeStoredSessionAnchorPosition(storage, "sess-123", viewerScope, {
      anchor: { kind: "entry", id: "msg_456" },
      offsetPx: 12.4,
      scrollTopFallback: 980.7,
    }, "thread-a");

    expect(storage.get("session-scroll-position:org-1:user-1:sess-123:thread-a")).toBe(JSON.stringify({
      version: 3,
      anchor_entry_id: "msg_456",
      offset_px: 12,
      scroll_top_fallback: 981,
    }));
    expect(readStoredSessionAnchorPosition(storage, "sess-123", viewerScope, "thread-a")).toEqual({
      anchor: { kind: "entry", id: "msg_456" },
      offsetPx: 12,
      scrollTopFallback: 981,
    });
    expect(readStoredSessionScrollPosition(storage, "sess-123", viewerScope, "thread-a")).toBe(981);
  });

  it("reads legacy message anchors for backward-compatible restores", () => {
    const storage = new Map<string, string>([[
      "session-scroll-position:org-1:user-1:sess-123:thread-a",
      JSON.stringify({
        version: 2,
        anchor: { kind: "message", id: 456 },
        offset_px: 12,
        scroll_top_fallback: 981,
      }),
    ]]);

    expect(readStoredSessionAnchorPosition(storage, "sess-123", viewerScope, "thread-a")).toEqual({
      anchor: { kind: "message", id: 456 },
      offsetPx: 12,
      scrollTopFallback: 981,
    });
  });

  it("reads and writes non-message transcript entry anchors", () => {
    const storage = new Map<string, string>();

    writeStoredSessionAnchorPosition(storage, "sess-123", viewerScope, {
      anchor: { kind: "entry", id: "tres_991" },
      offsetPx: 4,
      scrollTopFallback: 1200,
    }, "thread-a");

    expect(readStoredSessionAnchorPosition(storage, "sess-123", viewerScope, "thread-a")).toEqual({
      anchor: { kind: "entry", id: "tres_991" },
      offsetPx: 4,
      scrollTopFallback: 1200,
    });
  });

  it("ignores invalid structured anchor positions", () => {
    const storage = new Map<string, string>([[
      "session-scroll-position:org-1:user-1:sess-123:thread-a",
      JSON.stringify({ version: 2, anchor: { kind: "message", id: -1 }, offset_px: 0, scroll_top_fallback: 100 }),
    ]]);

    expect(readStoredSessionAnchorPosition(storage, "sess-123", viewerScope, "thread-a")).toBeNull();
  });

  it("stores normalized scroll positions for a session", () => {
    const storage = new Map<string, string>();

    writeStoredSessionScrollPosition(storage, "sess-123", viewerScope, 319.8);

    expect(storage.get("session-scroll-position:org-1:user-1:sess-123")).toBe(JSON.stringify({ version: 1, scrollTop: 320 }));
  });

  it("stores normalized scroll positions for a thread-specific view", () => {
    const storage = new Map<string, string>();

    writeStoredSessionScrollPosition(storage, "sess-123", viewerScope, 101.2, "thread-a");

    expect(storage.get("session-scroll-position:org-1:user-1:sess-123:thread-a")).toBe(
      JSON.stringify({ version: 1, scrollTop: 101 }),
    );
  });

  it("stores the active thread for a session", () => {
    const storage = new Map<string, string>();

    writeStoredSessionActiveThread(storage, "sess-123", viewerScope, "thread-b");

    expect(storage.get("session-active-thread:org-1:user-1:sess-123")).toBe(
      JSON.stringify({ version: 1, threadId: "thread-b" }),
    );
  });

  it("prefers the stored active thread when it still exists", () => {
    expect(
      resolveInitialSessionThreadId(
        [{ id: "thread-a" }, { id: "thread-b" }],
        "thread-b",
      ),
    ).toBe("thread-b");
  });

  it("falls back to the first visible thread when the stored active thread is missing", () => {
    expect(
      resolveInitialSessionThreadId(
        [{ id: "thread-a" }, { id: "thread-b" }],
        "thread-z",
      ),
    ).toBe("thread-a");
  });

  it("does not read another viewer's saved session position", () => {
    const storage = new Map<string, string>([["session-scroll-position:org-1:user-1:sess-123", "480"]]);

    expect(
      readStoredSessionScrollPosition(storage, "sess-123", { userId: "user-2", orgId: "org-1" }),
    ).toBeNull();
  });

  it("finds the first visible entry for the latest assistant turn", () => {
    const messages: SessionMessage[] = [
      makeMessage({ id: 1, role: "user", turn_number: 1, content: "first ask", created_at: "2026-04-22T10:00:00Z" }),
      makeMessage({ id: 2, role: "user", turn_number: 2, content: "second ask", created_at: "2026-04-22T10:01:00Z" }),
    ];
    const logs: SessionLog[] = [
      makeLog({ id: 11, level: "tool_use", turn_number: 1, message: "rg", created_at: "2026-04-22T10:00:10Z" }),
      makeLog({ id: 12, level: "output", turn_number: 1, message: "done", created_at: "2026-04-22T10:00:20Z" }),
      makeLog({ id: 21, level: "tool_use", turn_number: 2, message: "git diff", created_at: "2026-04-22T10:01:10Z" }),
      makeLog({ id: 22, level: "output", turn_number: 2, message: "latest answer", created_at: "2026-04-22T10:01:20Z" }),
    ];

    const entries = buildTimeline(messages, logs);

    expect(findLatestAssistantTurnStartIndex(entries)).toBe(4);
  });

  it("skips hidden log entries when anchoring the latest assistant turn", () => {
    const messages: SessionMessage[] = [
      makeMessage({ id: 1, role: "user", turn_number: 1, content: "first ask", created_at: "2026-04-22T10:00:00Z" }),
      makeMessage({ id: 2, role: "user", turn_number: 2, content: "second ask", created_at: "2026-04-22T10:01:00Z" }),
    ];
    const logs: SessionLog[] = [
      makeLog({ id: 11, level: "output", turn_number: 1, message: "done", created_at: "2026-04-22T10:00:20Z" }),
      makeLog({ id: 21, level: "info", turn_number: 2, message: "starting latest turn", created_at: "2026-04-22T10:01:05Z" }),
      makeLog({ id: 22, level: "tool_use", turn_number: 2, message: "git diff", created_at: "2026-04-22T10:01:10Z" }),
      makeLog({ id: 23, level: "output", turn_number: 2, message: "latest answer", created_at: "2026-04-22T10:01:20Z" }),
    ];

    const entries = buildTimeline(messages, logs);

    expect(findLatestAssistantTurnStartIndex(entries)).toBe(4);
  });

  it("returns null when there is no assistant turn to anchor to", () => {
    const entries: TimelineEntry[] = [
      { kind: "message", data: makeMessage({ id: 1, role: "user", turn_number: 1 }) },
    ];

    expect(findLatestAssistantTurnStartIndex(entries)).toBeNull();
  });

  it("prefers restoring the saved position over any generic default", () => {
    const entries: TimelineEntry[] = [];

    expect(
      resolveInitialSessionAnchor({
        entries,
        isActive: false,
        storedScrollTop: 320,
      }),
    ).toEqual({ kind: "saved_position", scrollTop: 320 });
  });

  it("opens active sessions at the live edge when there is no saved position", () => {
    const entries: TimelineEntry[] = [];

    expect(
      resolveInitialSessionAnchor({
        entries,
        isActive: true,
        storedScrollTop: null,
      }),
    ).toEqual({ kind: "live_edge" });
  });

  it("anchors inactive sessions to the latest assistant turn when there is no saved position", () => {
    const messages: SessionMessage[] = [
      makeMessage({ id: 1, role: "user", turn_number: 1, created_at: "2026-04-22T10:00:00Z" }),
      makeMessage({ id: 2, role: "user", turn_number: 2, created_at: "2026-04-22T10:01:00Z" }),
    ];
    const logs: SessionLog[] = [
      makeLog({ id: 11, level: "output", turn_number: 1, created_at: "2026-04-22T10:00:20Z" }),
      makeLog({ id: 21, level: "tool_use", turn_number: 2, created_at: "2026-04-22T10:01:10Z" }),
      makeLog({ id: 22, level: "output", turn_number: 2, created_at: "2026-04-22T10:01:20Z" }),
    ];

    expect(
      resolveInitialSessionAnchor({
        entries: buildTimeline(messages, logs),
        isActive: false,
        storedScrollTop: null,
      }),
    ).toEqual({ kind: "entry", entryIndex: 3 });
  });

  it("falls back to the live edge when an inactive session has no assistant turn", () => {
    const entries: TimelineEntry[] = [
      { kind: "message", data: makeMessage({ id: 1, role: "user", turn_number: 1 }) },
    ];

    expect(
      resolveInitialSessionAnchor({
        entries,
        isActive: false,
        storedScrollTop: null,
      }),
    ).toEqual({ kind: "live_edge" });
  });
});

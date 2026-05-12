import type { TimelineEntry } from "./timeline";

export const SESSION_SCROLL_STORAGE_PREFIX = "session-scroll-position:";
export const SESSION_ACTIVE_THREAD_STORAGE_PREFIX = "session-active-thread:";

export interface SessionScrollViewerScope {
  userId: string;
  orgId?: string | null;
}

export type InitialSessionAnchor =
  | { kind: "saved_position"; scrollTop: number }
  | { kind: "live_edge" }
  | { kind: "entry"; entryIndex: number };

interface ScrollStorageReader {
  get(key: string): string | undefined;
}

interface StoredSessionScrollPosition {
  version: 1;
  scrollTop: number;
}

interface StoredSessionActiveThread {
  version: 1;
  threadId: string;
}

interface ResolveInitialSessionAnchorInput {
  entries: TimelineEntry[];
  isActive: boolean;
  storedScrollTop: number | null;
}

interface ScrollStorageWriter {
  set(key: string, value: string): void;
}

export function getSessionScrollStorageKey(
  sessionId: string,
  viewerScope: SessionScrollViewerScope,
  threadId?: string | null,
): string {
  const orgPart = viewerScope.orgId ?? "no-org";
  const threadSuffix = threadId ? `:${threadId}` : "";
  return `${SESSION_SCROLL_STORAGE_PREFIX}${orgPart}:${viewerScope.userId}:${sessionId}${threadSuffix}`;
}

export function getSessionActiveThreadStorageKey(
  sessionId: string,
  viewerScope: SessionScrollViewerScope,
): string {
  const orgPart = viewerScope.orgId ?? "no-org";
  return `${SESSION_ACTIVE_THREAD_STORAGE_PREFIX}${orgPart}:${viewerScope.userId}:${sessionId}`;
}

export function readStoredSessionScrollPosition(
  storage: Pick<Storage, "getItem"> | ScrollStorageReader,
  sessionId: string,
  viewerScope: SessionScrollViewerScope,
  threadId?: string | null,
): number | null {
  const key = getSessionScrollStorageKey(sessionId, viewerScope, threadId);
  const rawValue =
    "getItem" in storage ? storage.getItem(key) : storage.get(key) ?? null;

  if (!rawValue) return null;

  if (rawValue.startsWith("{")) {
    try {
      const parsed = JSON.parse(rawValue) as Partial<StoredSessionScrollPosition>;
      if (parsed.version !== 1 || !Number.isFinite(parsed.scrollTop) || parsed.scrollTop! < 0) {
        return null;
      }
      return parsed.scrollTop!;
    } catch {
      return null;
    }
  }

  const parsed = Number(rawValue);
  if (!Number.isFinite(parsed) || parsed <= 0) {
    return null;
  }

  return parsed;
}

export function writeStoredSessionScrollPosition(
  storage: Pick<Storage, "setItem"> | ScrollStorageWriter,
  sessionId: string,
  viewerScope: SessionScrollViewerScope,
  scrollTop: number,
  threadId?: string | null,
): void {
  if (!Number.isFinite(scrollTop) || scrollTop < 0) {
    return;
  }

  const key = getSessionScrollStorageKey(sessionId, viewerScope, threadId);
  const normalizedValue = JSON.stringify({
    version: 1,
    scrollTop: Math.round(scrollTop),
  } satisfies StoredSessionScrollPosition);

  if ("setItem" in storage) {
    storage.setItem(key, normalizedValue);
    return;
  }

  storage.set(key, normalizedValue);
}

export function readStoredSessionActiveThread(
  storage: Pick<Storage, "getItem"> | ScrollStorageReader,
  sessionId: string,
  viewerScope: SessionScrollViewerScope,
): string | null {
  const key = getSessionActiveThreadStorageKey(sessionId, viewerScope);
  const rawValue =
    "getItem" in storage ? storage.getItem(key) : storage.get(key) ?? null;

  if (!rawValue) return null;

  if (rawValue.startsWith("{")) {
    try {
      const parsed = JSON.parse(rawValue) as Partial<StoredSessionActiveThread>;
      if (parsed.version !== 1 || typeof parsed.threadId !== "string" || parsed.threadId.length === 0) {
        return null;
      }
      return parsed.threadId;
    } catch {
      return null;
    }
  }

  return rawValue.length > 0 ? rawValue : null;
}

export function writeStoredSessionActiveThread(
  storage: Pick<Storage, "setItem"> | ScrollStorageWriter,
  sessionId: string,
  viewerScope: SessionScrollViewerScope,
  threadId: string,
): void {
  if (threadId.length === 0) {
    return;
  }

  const key = getSessionActiveThreadStorageKey(sessionId, viewerScope);
  const normalizedValue = JSON.stringify({
    version: 1,
    threadId,
  } satisfies StoredSessionActiveThread);

  if ("setItem" in storage) {
    storage.setItem(key, normalizedValue);
    return;
  }

  storage.set(key, normalizedValue);
}

export function resolveInitialSessionThreadId(
  threads: Array<{ id: string }>,
  storedThreadId: string | null,
): string | null {
  if (storedThreadId && threads.some((thread) => thread.id === storedThreadId)) {
    return storedThreadId;
  }

  return threads[0]?.id ?? null;
}

export function findLatestAssistantTurnStartIndex(entries: TimelineEntry[]): number | null {
  let latestAssistantTurn: number | null = null;

  for (let i = entries.length - 1; i >= 0; i -= 1) {
    const turnNumber = getAssistantTurnNumber(entries[i]);
    if (turnNumber === null) continue;
    latestAssistantTurn = turnNumber;
    break;
  }

  if (latestAssistantTurn === null) {
    return null;
  }

  for (let i = 0; i < entries.length; i += 1) {
    if (
      getAssistantTurnNumber(entries[i]) === latestAssistantTurn &&
      isVisibleAssistantAnchorEntry(entries[i])
    ) {
      return i;
    }
  }

  return null;
}

export function resolveInitialSessionAnchor({
  entries,
  isActive,
  storedScrollTop,
}: ResolveInitialSessionAnchorInput): InitialSessionAnchor {
  if (storedScrollTop !== null) {
    return { kind: "saved_position", scrollTop: storedScrollTop };
  }

  if (isActive) {
    return { kind: "live_edge" };
  }

  const latestAssistantEntryIndex = findLatestAssistantTurnStartIndex(entries);
  if (latestAssistantEntryIndex !== null) {
    return { kind: "entry", entryIndex: latestAssistantEntryIndex };
  }

  return { kind: "live_edge" };
}

function getAssistantTurnNumber(entry: TimelineEntry): number | null {
  switch (entry.kind) {
    case "message":
      return entry.data.role === "assistant" ? entry.data.turn_number : null;
    case "assistant_output":
    case "error":
    case "log":
      return entry.data.turn_number;
    case "plan_output":
    case "plan_message":
      return entry.turnNumber;
    case "tool_group":
      return entry.toolUse.turn_number;
  }
}

function isVisibleAssistantAnchorEntry(entry: TimelineEntry): boolean {
  return entry.kind !== "log";
}

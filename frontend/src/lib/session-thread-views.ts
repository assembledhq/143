const SESSION_VIEWED_THREADS_KEY_PREFIX = "session-viewed-threads";

function storageKey(sessionId: string): string {
  return `${SESSION_VIEWED_THREADS_KEY_PREFIX}:${sessionId}`;
}

export function readStoredViewedThreadIds(storage: Storage, sessionId: string): Set<string> {
  const raw = storage.getItem(storageKey(sessionId));
  if (!raw) {
    return new Set();
  }

  try {
    const parsed = JSON.parse(raw);
    if (!Array.isArray(parsed)) {
      return new Set();
    }

    return new Set(parsed.filter((value): value is string => typeof value === "string"));
  } catch {
    return new Set();
  }
}

export function writeStoredViewedThreadIds(
  storage: Storage,
  sessionId: string,
  threadIds: Iterable<string>,
): void {
  const uniqueThreadIds = [...new Set(threadIds)].sort();
  storage.setItem(storageKey(sessionId), JSON.stringify(uniqueThreadIds));
}

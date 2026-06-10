import type { SessionScrollViewerScope } from "./session-open-position";

// Last-known viewer identity, persisted by use-auth whenever /auth/me
// resolves. Warm-start code paths (e.g. prefetching the active thread's
// message window before auth settles) need it to reconstruct localStorage
// keys that are scoped by user/org. It is a hint, not a source of truth:
// consumers must tolerate the authenticated scope coming back different —
// optimistic work keyed off a stale scope is simply discarded.
const VIEWER_SCOPE_CACHE_KEY = "143:last-viewer-scope";

interface StoredViewerScope {
  version: 1;
  userId: string;
  orgId: string | null;
}

export function readCachedViewerScope(
  storage: Pick<Storage, "getItem">,
): SessionScrollViewerScope | null {
  let raw: string | null;
  try {
    raw = storage.getItem(VIEWER_SCOPE_CACHE_KEY);
  } catch {
    return null;
  }
  if (!raw) return null;

  try {
    const parsed = JSON.parse(raw) as Partial<StoredViewerScope>;
    if (parsed.version !== 1 || typeof parsed.userId !== "string" || parsed.userId.length === 0) {
      return null;
    }
    return {
      userId: parsed.userId,
      orgId: typeof parsed.orgId === "string" && parsed.orgId.length > 0 ? parsed.orgId : null,
    };
  } catch {
    return null;
  }
}

export function writeCachedViewerScope(
  storage: Pick<Storage, "setItem">,
  scope: SessionScrollViewerScope,
): void {
  if (scope.userId.length === 0) return;

  const normalizedValue = JSON.stringify({
    version: 1,
    userId: scope.userId,
    orgId: scope.orgId ?? null,
  } satisfies StoredViewerScope);

  try {
    storage.setItem(VIEWER_SCOPE_CACHE_KEY, normalizedValue);
  } catch {
    // Quota or privacy-mode errors just lose the warm-start hint.
  }
}

// Called on logout so the next user on this browser can't trigger
// prefetches keyed off the previous user's stored state.
export function clearCachedViewerScope(storage: Pick<Storage, "removeItem">): void {
  try {
    storage.removeItem(VIEWER_SCOPE_CACHE_KEY);
  } catch {
    // Best-effort: a stale hint only ever costs a wasted, access-checked fetch.
  }
}

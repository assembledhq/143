"use client";

import { createContext, useCallback, useContext, useEffect, useRef, useState } from "react";

/**
 * How long to keep a resolved placeholder alive before force-removing it.
 * The sidebar renders the real session data through the resolved row's stable
 * key for this window, then the optimistic entry is dropped and the real
 * session takes over via the regular session list path.
 * 10s comfortably exceeds a typical post-invalidation refetch.
 */
const RESOLUTION_FALLBACK_MS = 10_000;

/**
 * Minimal shape for a session that hasn't been saved to the backend yet.
 * Only includes the fields the sidebar needs to render a placeholder entry.
 *
 * If the sidebar starts displaying additional fields, add them here so the
 * compiler reminds callers to supply them.
 */
export interface OptimisticSession {
  /** Temporary client-side id (e.g. "optimistic-<timestamp>"). */
  id: string;
  /** Text shown as the session title in the sidebar. */
  title: string;
  status: "pending";
  created_at: string;
  /**
   * Set once the create mutation returns with a real session id. The sidebar
   * hides optimistic rows whose resolvedId matches a session already in the
   * server-returned list, then removes them — avoiding a double-render flash.
   */
  resolvedId?: string;
}

interface OptimisticSessionsContextValue {
  optimisticSessions: OptimisticSession[];
  /** Add a placeholder session. Returns the temporary id. */
  addOptimisticSession: (title: string) => string;
  /** Remove a placeholder (on error, or after the real session is visible). */
  removeOptimisticSession: (id: string) => void;
  /** Link an optimistic placeholder to the real session id returned by the server. */
  markOptimisticResolved: (id: string, resolvedId: string) => void;
}

const OptimisticSessionsContext = createContext<OptimisticSessionsContextValue | null>(null);

export function OptimisticSessionsProvider({ children }: { children: React.ReactNode }) {
  const [sessions, setSessions] = useState<OptimisticSession[]>([]);
  const fallbackTimers = useRef<Map<string, ReturnType<typeof setTimeout>>>(new Map());

  const clearFallback = useCallback((id: string) => {
    const handle = fallbackTimers.current.get(id);
    if (handle !== undefined) {
      clearTimeout(handle);
      fallbackTimers.current.delete(id);
    }
  }, []);

  const addOptimisticSession = useCallback((title: string) => {
    const id = `optimistic-${crypto.randomUUID()}`;
    setSessions((prev) => [
      { id, title, status: "pending" as const, created_at: new Date().toISOString() },
      ...prev,
    ]);
    return id;
  }, []);

  const removeOptimisticSession = useCallback((id: string) => {
    clearFallback(id);
    setSessions((prev) => prev.filter((s) => s.id !== id));
  }, [clearFallback]);

  const markOptimisticResolved = useCallback((id: string, resolvedId: string) => {
    setSessions((prev) => prev.map((s) => (s.id === id ? { ...s, resolvedId } : s)));
    clearFallback(id);
    const handle = setTimeout(() => {
      fallbackTimers.current.delete(id);
      setSessions((prev) => prev.filter((s) => s.id !== id));
    }, RESOLUTION_FALLBACK_MS);
    fallbackTimers.current.set(id, handle);
  }, [clearFallback]);

  useEffect(() => {
    const timers = fallbackTimers.current;
    return () => {
      for (const handle of timers.values()) clearTimeout(handle);
      timers.clear();
    };
  }, []);

  return (
    <OptimisticSessionsContext.Provider value={{ optimisticSessions: sessions, addOptimisticSession, removeOptimisticSession, markOptimisticResolved }}>
      {children}
    </OptimisticSessionsContext.Provider>
  );
}

export function useOptimisticSessions() {
  const ctx = useContext(OptimisticSessionsContext);
  if (!ctx) {
    throw new Error("useOptimisticSessions must be used within an OptimisticSessionsProvider");
  }
  return ctx;
}

const EMPTY_SESSIONS: OptimisticSession[] = [];
const noopAdd = () => `optimistic-${Date.now()}`;
const noopRemove = () => {};
const noopResolve = () => {};
const FALLBACK: OptimisticSessionsContextValue = {
  optimisticSessions: EMPTY_SESSIONS,
  addOptimisticSession: noopAdd,
  removeOptimisticSession: noopRemove,
  markOptimisticResolved: noopResolve,
};

/** Same as useOptimisticSessions but returns no-op stubs when outside a provider. */
export function useOptimisticSessionsSafe(): OptimisticSessionsContextValue {
  const ctx = useContext(OptimisticSessionsContext);
  return ctx ?? FALLBACK;
}

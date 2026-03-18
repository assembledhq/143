"use client";

import { createContext, useCallback, useContext, useState } from "react";

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
}

interface OptimisticSessionsContextValue {
  optimisticSessions: OptimisticSession[];
  /** Add a placeholder session. Returns the temporary id. */
  addOptimisticSession: (title: string) => string;
  /** Remove a placeholder (on success or error). */
  removeOptimisticSession: (id: string) => void;
}

const OptimisticSessionsContext = createContext<OptimisticSessionsContextValue | null>(null);

export function OptimisticSessionsProvider({ children }: { children: React.ReactNode }) {
  const [sessions, setSessions] = useState<OptimisticSession[]>([]);

  const addOptimisticSession = useCallback((title: string) => {
    const id = `optimistic-${crypto.randomUUID()}`;
    setSessions((prev) => [
      { id, title, status: "pending" as const, created_at: new Date().toISOString() },
      ...prev,
    ]);
    return id;
  }, []);

  const removeOptimisticSession = useCallback((id: string) => {
    setSessions((prev) => prev.filter((s) => s.id !== id));
  }, []);

  return (
    <OptimisticSessionsContext.Provider value={{ optimisticSessions: sessions, addOptimisticSession, removeOptimisticSession }}>
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

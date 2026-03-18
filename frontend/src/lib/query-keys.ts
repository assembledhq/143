/**
 * Centralized React Query key factories.
 *
 * Using a single source of truth for query keys prevents typo-based bugs and
 * makes it easy to invalidate related caches consistently.
 */
export const queryKeys = {
  sessions: {
    all: ["sessions"] as const,
    list: (repo?: string | null) => ["sessions", repo] as const,
    detail: (id: string) => ["session", id] as const,
    validation: (id: string) => ["session", id, "validation"] as const,
    pr: (id: string) => ["session", id, "pr"] as const,
    messages: (id: string) => ["session", id, "messages"] as const,
  },
  settings: {
    all: ["settings"] as const,
  },
  team: {
    members: ["team", "members"] as const,
  },
} as const;

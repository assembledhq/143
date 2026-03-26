/**
 * Centralized React Query key factories.
 *
 * Using a single source of truth for query keys prevents typo-based bugs and
 * makes it easy to invalidate related caches consistently.
 *
 * NOTE: Adoption is incremental — many files still use inline string arrays.
 * When touching a file that uses hardcoded query keys, migrate them here.
 */
export const queryKeys = {
  sessions: {
    all: ["sessions"] as const,
    list: (repo?: string | null) => ["sessions", repo] as const,
    detail: (id: string) => ["session", id] as const,
    validation: (id: string) => ["session", id, "validation"] as const,
    pr: (id: string) => ["session", id, "pr"] as const,
    messages: (id: string) => ["session", id, "messages"] as const,
    threads: (id: string) => ["session", id, "threads"] as const,
    threadDetail: (sessionId: string, threadId: string) => ["session", sessionId, "thread", threadId] as const,
    threadMessages: (sessionId: string, threadId: string) => ["session", sessionId, "thread", threadId, "messages"] as const,
    threadLogs: (sessionId: string, threadId: string) => ["session", sessionId, "thread", threadId, "logs"] as const,
  },
  repositories: {
    all: ["repositories"] as const,
    branches: (id: string) => ["repositories", id, "branches"] as const,
  },
  settings: {
    all: ["settings"] as const,
    agentDefaults: ["agent-defaults"] as const,
  },
  codexAuth: {
    status: ["codex-auth-status"] as const,
  },
  integrations: {
    all: ["integrations"] as const,
  },
  pm: {
    status: ["pm", "status"] as const,
    latest: ["pm", "latest"] as const,
    documents: ["pm", "documents"] as const,
  },
  team: {
    members: ["team", "members"] as const,
  },
} as const;

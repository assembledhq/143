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
    counts: (repo?: string | null, peopleKey?: string | null) =>
      ["sessions", "counts", repo, peopleKey] as const,
    detail: (id: string) => ["session", id] as const,
    diff: (id: string, revision?: string | null) => ["session", id, "diff", revision ?? null] as const,
    timeline: (id: string) => ["session", id, "timeline"] as const,
    pr: (id: string) => ["session", id, "pr"] as const,
    messages: (id: string) => ["session", id, "messages"] as const,
    humanInputRequests: (id: string, status?: string | null, threadId?: string | null) =>
      ["session", id, "human-input-requests", status ?? null, threadId ?? null] as const,
    composerFiles: (id: string, query: string) => ["session", id, "composer", "files", query] as const,
    threads: (id: string) => ["session", id, "threads"] as const,
    threadDetail: (sessionId: string, threadId: string) => ["session", sessionId, "thread", threadId] as const,
    threadMessages: (sessionId: string, threadId: string) => ["session", sessionId, "thread", threadId, "messages"] as const,
    threadLogs: (sessionId: string, threadId: string) => ["session", sessionId, "thread", threadId, "logs"] as const,
    threadRecoverableInbox: (sessionId: string, threadId: string) => ["session", sessionId, "thread", threadId, "recoverable-inbox"] as const,
    threadFileEvents: (id: string) => ["session", id, "thread-file-events"] as const,
    reviewLoops: (id: string) => ["session", id, "review-loops"] as const,
  },
  repositories: {
    all: ["repositories"] as const,
    summary: ["repositories", "summary"] as const,
    branches: (id: string) => ["repositories", id, "branches"] as const,
    previewSecretBundles: (id: string) => ["repositories", id, "preview-secret-bundles"] as const,
  },
  sessionComposer: {
    files: (repositoryId: string, branch: string, query: string) => ["session-composer", "files", repositoryId, branch, query] as const,
    slashCommands: (agentType: string, repositoryId: string, branch: string, query: string) =>
      ["session-composer", "slash-commands", agentType, repositoryId, branch, query] as const,
  },
  projects: {
    all: ["projects"] as const,
    list: (params?: { repo?: string | null; search?: string }) => ["projects", params] as const,
  },
  settings: {
    all: ["settings"] as const,
    network: ["settings", "network"] as const,
  },
  credentials: {
    all: ["credentials"] as const,
    resolved: ["credentials", "resolved"] as const,
    teamDefaults: ["credentials", "team-defaults"] as const,
  },
  codexAuth: {
    status: ["codex-auth-status"] as const,
  },
  integrations: {
    all: ["integrations"] as const,
    githubRepositories: (installationId?: number | null) => ["integrations", "github", "repositories", installationId ?? null] as const,
    linearAgentStatus: ["integrations", "linear", "agent"] as const,
    linearAgentMappings: ["integrations", "linear", "agent", "mappings"] as const,
    slackChannels: ["slack-channels"] as const,
  },
  autopilot: {
    queue: (params: Record<string, string | number | null | undefined>) => ["autopilot", "queue", params] as const,
  },
  pm: {
    status: ["pm", "status"] as const,
    latest: ["pm", "latest"] as const,
    documents: ["pm", "documents"] as const,
  },
  team: {
    members: ["team", "members"] as const,
  },
  auth: {
    memberships: ["auth", "memberships"] as const,
  },
  invitations: {
    pending: ["invitations", "pending"] as const,
  },
  usage: {
    summary: (params: { start: string; end: string }) =>
      ["usage", "summary", params] as const,
    timeseries: (params: { start: string; end: string; group_by?: string; stack_by?: string; user_id?: string; capacity?: string; agent?: string; model?: string; reasoning?: string }) =>
      ["usage", "timeseries", params] as const,
    breakdown: (params: { start: string; end: string; dimension?: string; sort?: string; agent?: string; model?: string; reasoning?: string }) =>
      ["usage", "breakdown", params] as const,
  },
  evals: {
    tasks: (params?: Record<string, string | undefined>) => ["evals", "tasks", params] as const,
    taskDetail: (id: string) => ["evals", "task", id] as const,
    runs: (taskId: string) => ["evals", "task", taskId, "runs"] as const,
    runDetail: (id: string) => ["evals", "run", id] as const,
    batches: ["evals", "batches"] as const,
    batch: (id: string) => ["evals", "batch", id] as const,
    bootstrapCandidates: ["evals", "bootstrap", "candidates"] as const,
    bootstrapRun: (id: string) => ["evals", "bootstrap", "run", id] as const,
  },
  previews: {
    apiTokens: ["preview-api-tokens"] as const,
  },
} as const;

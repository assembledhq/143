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
    logDetail: (sessionId: string, logId: number) => ["session", sessionId, "logs", logId, "detail"] as const,
    pr: (id: string, changesetId?: string | null) => changesetId
      ? ["session", id, "pr", changesetId] as const
      : ["session", id, "pr"] as const,
    messages: (id: string) => ["session", id, "messages"] as const,
    humanInputRequests: (id: string, status?: string | null, threadId?: string | null) =>
      ["session", id, "human-input-requests", status ?? null, threadId ?? null] as const,
    composerFiles: (id: string, query: string) => ["session", id, "composer", "files", query] as const,
    threads: (id: string) => ["session", id, "threads"] as const,
    threadDetail: (sessionId: string, threadId: string) => ["session", sessionId, "thread", threadId] as const,
    threadMessages: (sessionId: string, threadId: string) => ["session", sessionId, "thread", threadId, "messages"] as const,
    threadLogs: (sessionId: string, threadId: string) => ["session", sessionId, "thread", threadId, "logs"] as const,
    threadTranscript: (sessionId: string, threadId: string, anchorKey?: string | null) =>
      anchorKey
        ? ["session", sessionId, "thread", threadId, "transcript", anchorKey] as const
        : ["session", sessionId, "thread", threadId, "transcript"] as const,
    threadRecoverableInbox: (sessionId: string, threadId: string) => ["session", sessionId, "thread", threadId, "recoverable-inbox"] as const,
    threadFileEvents: (id: string) => ["session", id, "thread-file-events"] as const,
    reviewLoops: (id: string) => ["session", id, "review-loops"] as const,
    readiness: (id: string, changesetId?: string | null) => ["session", id, "readiness", changesetId ?? null] as const,
  },
  repositories: {
    all: ["repositories"] as const,
    summary: ["repositories", "summary"] as const,
    branches: (id: string, query = "") => ["repositories", id, "branches", query] as const,
    previewSecretBundles: (id: string) => ["repositories", id, "preview-secret-bundles"] as const,
  },
  codeReviews: {
    all: ["code-reviews"] as const,
    lists: () => ["code-reviews", "list"] as const,
    list: (params?: unknown) => ["code-reviews", "list", params ?? null] as const,
    policy: (repositoryId?: string | null) => ["code-reviews", "policy", repositoryId ?? null] as const,
    githubTrigger: (repositoryId?: string | null) => ["code-reviews", "github-trigger", repositoryId ?? null] as const,
    templates: ["code-reviews", "templates"] as const,
    promptExamples: ["code-reviews", "prompt-examples"] as const,
    evidence: (sessionId: string) => ["code-reviews", "evidence", sessionId] as const,
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
    agentCapabilities: ["settings", "agent", "capabilities"] as const,
    agentCapabilityCatalog: ["agent-capabilities"] as const,
    network: ["settings", "network"] as const,
    runtimeStatus: ["settings", "runtime", "status"] as const,
    prReadinessPolicy: (repositoryId?: string | null) => ["settings", "pr-readiness-policy", repositoryId ?? null] as const,
    prReadinessCustomChecks: (repositoryId?: string | null) => ["settings", "pr-readiness-custom-checks", repositoryId ?? null] as const,
  },
  // Unified coding-credentials caches. The scope segment matches the API's
  // scope param ("org" | "personal" | "resolved"); invalidating
  // codingCredentials.all sweeps every scope at once.
  codingCredentials: {
    all: ["coding-credentials"] as const,
    list: (scope: "org" | "personal" | "resolved") => ["coding-credentials", scope] as const,
  },
  codexAuth: {
    status: ["codex-auth-status"] as const,
  },
  integrations: {
    all: ["integrations"] as const,
    githubRepositories: (installationId?: number | null) => ["integrations", "github", "repositories", installationId ?? null] as const,
    linearAgentStatus: ["integrations", "linear", "agent"] as const,
    linearAgentMappings: ["integrations", "linear", "agent", "mappings"] as const,
    slackHealth: ["integrations", "slack", "health"] as const,
    slackSettings: ["integrations", "slack", "settings"] as const,
    slackUserLinks: ["integrations", "slack", "user-links"] as const,
    slackChannels: ["slack-channels"] as const,
    pagerDuty: ["integrations", "pagerduty"] as const,
    pagerDutyMappings: (integrationId?: string | null) => ["integrations", "pagerduty", "mappings", integrationId ?? null] as const,
    pagerDutyIncidents: (integrationId?: string | null) => ["integrations", "pagerduty", "incidents", integrationId ?? null] as const,
  },
  automations: {
    all: ["automations"] as const,
    detail: (id: string) => ["automation", id] as const,
    eventTriggers: (id: string) => ["automations", id, "event-triggers"] as const,
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
    domains: ["team", "domains"] as const,
    githubOrgs: ["team", "github-orgs"] as const,
  },
  organizations: {
    joinable: ["organizations", "joinable"] as const,
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
  apiKeys: {
    clients: ["api-keys", "clients"] as const,
    tokens: (clientId: string) => ["api-keys", "clients", clientId, "tokens"] as const,
  },
} as const;

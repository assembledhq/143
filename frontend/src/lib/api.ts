import { getActiveOrgId, ORG_MEMBERSHIP_REVOKED_EVENT } from './active-org';

const API_BASE = process.env.NEXT_PUBLIC_API_URL || '';
const SENTRY_CLIENT_ID = process.env.NEXT_PUBLIC_SENTRY_CLIENT_ID || '';
const SENTRY_REDIRECT_URI = process.env.NEXT_PUBLIC_SENTRY_REDIRECT_URI || '';

export class ApiError extends Error {
  constructor(public code: string, message: string, public details?: unknown) {
    super(message);
    this.name = 'ApiError';
  }
}

async function parseSuccessBody<T>(res: Response): Promise<T> {
  if (res.status === 204 || res.status === 205) {
    return undefined as T;
  }

  const text = await res.text();
  if (text.length === 0) {
    return undefined as T;
  }

  return JSON.parse(text) as T;
}

function getCSRFToken(): string {
  const match = document.cookie
    .split('; ')
    .find(row => row.startsWith('csrf_token='));
  return match ? decodeURIComponent(match.substring('csrf_token='.length)) : '';
}

// N parallel requests after a membership revocation all see the header, and
// without a guard each one would fire a fresh event → fresh toast. Collapse
// bursts into a single dispatch per short window; listeners still get woken
// up for any later revocation that lands after the window closes.
let lastRevokedDispatchAt = 0;
const REVOKED_DISPATCH_MIN_INTERVAL_MS = 1000;
function maybeDispatchRevoked(): void {
  const now = Date.now();
  if (now - lastRevokedDispatchAt < REVOKED_DISPATCH_MIN_INTERVAL_MS) return;
  lastRevokedDispatchAt = now;
  window.dispatchEvent(new CustomEvent(ORG_MEMBERSHIP_REVOKED_EVENT));
}

async function request<T>(path: string, options?: RequestInit): Promise<T> {
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    ...(options?.headers as Record<string, string>),
  };

  // Attach CSRF token on state-changing requests.
  const method = options?.method?.toUpperCase() || 'GET';
  if (method !== 'GET' && method !== 'HEAD') {
    headers['X-CSRF-Token'] = getCSRFToken();
  }

  // Only attach the active-org header on org-scoped routes. Auth endpoints
  // (login, register, logout, me, memberships) are user-scoped — they operate
  // on session/user state regardless of the selected workspace, so sending a
  // stale org id here would only give the server a way to misattribute the
  // request or echo back an irrelevant header. Creating a new org (POST
  // /api/v1/organizations) is also user-scoped: the handler runs outside
  // OrgContext, and forwarding a just-revoked active-org id would trip the
  // upstream auth middleware into emitting X-Org-Membership-Revoked *during*
  // the create flow, firing a confusing "your access changed" toast.
  const activeOrgId = getActiveOrgId();
  const isUserScopedRoute =
    path.startsWith('/api/v1/auth/') ||
    (method === 'POST' && path === '/api/v1/organizations');
  if (activeOrgId && !isUserScopedRoute) {
    headers['X-Active-Org-ID'] = activeOrgId;
  }

  const res = await fetch(`${API_BASE}${path}`, {
    ...options,
    credentials: 'include',
    headers,
  });

  if (typeof window !== 'undefined' && res.headers.get('X-Org-Membership-Revoked') === '1') {
    maybeDispatchRevoked();
  }

  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    throw new ApiError(
      body?.error?.code || 'UNKNOWN',
      body?.error?.message || res.statusText,
      body?.error?.details
    );
  }

  return parseSuccessBody<T>(res);
}

function get<T>(path: string): Promise<T> {
  return request<T>(path);
}

function post<T>(path: string, body?: unknown): Promise<T> {
  return request<T>(path, {
    method: 'POST',
    body: body ? JSON.stringify(body) : undefined,
  });
}

function patch<T>(path: string, body: unknown): Promise<T> {
  return request<T>(path, {
    method: 'PATCH',
    body: JSON.stringify(body),
  });
}

function del<T>(path: string): Promise<T> {
  return request<T>(path, { method: 'DELETE' });
}

async function uploadFile(file: File): Promise<{ url: string; file_name: string; content_type: string }> {
  const formData = new FormData();
  formData.append('file', file);

  const headers: Record<string, string> = {
    'X-CSRF-Token': getCSRFToken(),
  };
  const activeOrgId = getActiveOrgId();
  if (activeOrgId) {
    headers['X-Active-Org-ID'] = activeOrgId;
  }
  // Do NOT set Content-Type — the browser sets it with the multipart boundary.

  const res = await fetch(`${API_BASE}/api/v1/uploads`, {
    method: 'POST',
    credentials: 'include',
    headers,
    body: formData,
  });

  if (typeof window !== 'undefined' && res.headers.get('X-Org-Membership-Revoked') === '1') {
    maybeDispatchRevoked();
  }

  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    throw new ApiError(
      body?.error?.code || 'UNKNOWN',
      body?.error?.message || res.statusText,
      body?.error?.details
    );
  }

  return res.json();
}

export const api = {
  uploads: {
    upload: uploadFile,
  },
  auth: {
    providers: () => get<import('./types').SingleResponse<import('./types').AuthProviders>>('/api/v1/auth/providers'),
    me: () => get<import('./types').SingleResponse<import('./types').User>>('/api/v1/auth/me'),
    updateSettings: (body: import('./types').UserSettingsUpdateRequest) =>
      patch<import('./types').SingleResponse<import('./types').User>>('/api/v1/auth/me/settings', body),
    login: (invitation?: string) => {
      const searchParams = new URLSearchParams();
      if (invitation) searchParams.set('invitation', invitation);
      if (window.location.pathname) searchParams.set('return_to', window.location.pathname);
      const qs = searchParams.toString();
      window.location.href = `${API_BASE}/api/v1/auth/github/login${qs ? `?${qs}` : ''}`;
    },
    loginGoogle: (invitation?: string) => {
      const searchParams = new URLSearchParams();
      if (invitation) searchParams.set('invitation', invitation);
      if (window.location.pathname) searchParams.set('return_to', window.location.pathname);
      const qs = searchParams.toString();
      window.location.href = `${API_BASE}/api/v1/auth/google/login${qs ? `?${qs}` : ''}`;
    },
    loginSentry: () => {
      const params = new URLSearchParams({
        client_id: SENTRY_CLIENT_ID,
        response_type: 'code',
        redirect_uri: SENTRY_REDIRECT_URI,
      });
      window.location.href = `https://sentry.io/oauth/authorize/?${params.toString()}`;
    },
    loginEmail: (email: string, password: string) =>
      post<import('./types').SingleResponse<import('./types').User>>('/api/v1/auth/login', { email, password }),
    register: (email: string, password: string, name: string, invitation?: string) =>
      post<import('./types').SingleResponse<import('./types').User>>('/api/v1/auth/register', { email, password, name, ...(invitation && { invitation }) }),
    claimInvitation: (token: string) =>
      post<import('./types').SingleResponse<import('./types').ClaimInvitationResponse>>('/api/v1/invitations/claim', { token }),
    setActiveOrg: (orgId: string) =>
      post('/api/v1/auth/active-org', { org_id: orgId }),
    logout: () => post('/api/v1/auth/logout'),
    memberships: () =>
      get<import('./types').SingleResponse<import('./types').MembershipsResponse>>('/api/v1/auth/memberships'),
  },
  organizations: {
    create: (name: string) =>
      post<import('./types').SingleResponse<import('./types').OrganizationCreated>>('/api/v1/organizations', { name }),
  },
  repositories: {
    list: (opts?: { includeDisconnected?: boolean }) => {
      const qs = opts?.includeDisconnected ? '?include_disconnected=true' : '';
      return get<import('./types').ListResponse<import('./types').Repository>>(`/api/v1/repositories${qs}`);
    },
    get: (id: string) => get<import('./types').SingleResponse<import('./types').Repository>>(`/api/v1/repositories/${id}`),
    update: (id: string, data: Record<string, unknown>) =>
      patch<import('./types').SingleResponse<import('./types').Repository>>(`/api/v1/repositories/${id}`, data),
    delete: (id: string) => del(`/api/v1/repositories/${id}`),
    disconnect: (id: string) =>
      post<import('./types').SingleResponse<import('./types').Repository>>(`/api/v1/repositories/${id}/disconnect`),
    reconnect: (id: string) =>
      post<import('./types').SingleResponse<import('./types').Repository>>(`/api/v1/repositories/${id}/reconnect`),
    summary: () => get<import('./types').ListResponse<import('./types').RepoSummary>>('/api/v1/repositories/summary'),
    branches: (id: string) => get<import('./types').ListResponse<{ name: string; protected: boolean }>>(`/api/v1/repositories/${id}/branches`),
    detectPreview: (owner: string, repo: string) => get<import('./preview-types').PreviewDetectionResult>(`/api/v1/repos/${owner}/${repo}/preview/detect`),
  },
  pullRequests: {
    getHealth: (id: string) => get<import('./types').SingleResponse<import('./types').PullRequestHealthResponse>>(`/api/v1/pull-requests/${id}/health`),
    fixTests: (id: string) => post<import('./types').SingleResponse<import('./types').PullRequestRepairResponse>>(`/api/v1/pull-requests/${id}/repair/fix-tests`),
    resolveConflicts: (id: string) => post<import('./types').SingleResponse<import('./types').PullRequestRepairResponse>>(`/api/v1/pull-requests/${id}/repair/resolve-conflicts`),
    merge: (id: string) => post<import('./types').SingleResponse<import('./types').PullRequestMergeResponse>>(`/api/v1/pull-requests/${id}/merge`),
  },
  sessionComposer: {
    files: (repositoryId: string, branch: string, query: string) => {
      const searchParams = new URLSearchParams({ repository_id: repositoryId, q: query });
      if (branch) {
        searchParams.set("branch", branch);
      }
      return get<import('./types').ListResponse<import('./types').SessionInputReference>>(`/api/v1/session-composer/files?${searchParams.toString()}`);
    },
    slashCommands: (params: { agentType: string; query?: string; repositoryId?: string; branch?: string }) => {
      const searchParams = new URLSearchParams({ agent_type: params.agentType });
      if (params.query) searchParams.set("q", params.query);
      if (params.repositoryId) searchParams.set("repository_id", params.repositoryId);
      if (params.branch) searchParams.set("branch", params.branch);
      return get<import('./types').SlashCommandListResponse>(`/api/v1/session-composer/slash-commands?${searchParams.toString()}`);
    },
    slashCommandDetail: (params: { agentType: string; name: string; repositoryId: string; branch?: string }) => {
      const searchParams = new URLSearchParams({
        agent_type: params.agentType,
        name: params.name,
        repository_id: params.repositoryId,
      });
      if (params.branch) searchParams.set("branch", params.branch);
      return get<import('./types').SlashCommandDetailResponse>(`/api/v1/session-composer/slash-commands/details?${searchParams.toString()}`);
    },
  },
  issues: {
    list: (params?: { status?: string; source?: string; severity?: string; sort?: string; cursor?: string; limit?: number }) => {
      const searchParams = new URLSearchParams();
      if (params?.status) searchParams.set('status', params.status);
      if (params?.source) searchParams.set('source', params.source);
      if (params?.severity) searchParams.set('severity', params.severity);
      if (params?.sort) searchParams.set('sort', params.sort);
      if (params?.cursor) searchParams.set('cursor', params.cursor);
      if (params?.limit) searchParams.set('limit', String(params.limit));
      const qs = searchParams.toString();
      return get<import('./types').ListResponse<import('./types').Issue>>(`/api/v1/issues${qs ? `?${qs}` : ''}`);
    },
    get: (id: string) => get<import('./types').SingleResponse<import('./types').Issue>>(`/api/v1/issues/${id}`),
    triggerFix: (issueId: string, options?: { agent_type?: string; autonomy_level?: string; token_mode?: string }) =>
      post<import('./types').SingleResponse<import('./types').Session>>(`/api/v1/issues/${issueId}/fix`, options),
  },
  pm: {
    // Cursor format for /pm/plans: "<created_at RFC3339Nano>|<uuid>" (treat as opaque).
    analyze: () => post<{ data: { job_id: string } }>('/api/v1/pm/analyze'),
    current: () => get<import('./types').SingleResponse<import('./types').PMCurrentRecommendation>>('/api/v1/pm/current'),
    list: (params?: { cursor?: string; limit?: number }) => {
      const searchParams = new URLSearchParams();
      if (params?.cursor) searchParams.set('cursor', params.cursor);
      if (params?.limit != null) searchParams.set('limit', String(params.limit));
      const qs = searchParams.toString();
      return get<import('./types').ListResponse<import('./types').PMPlan>>(`/api/v1/pm/plans${qs ? `?${qs}` : ''}`);
    },
    latest: () => get<import('./types').SingleResponse<import('./types').PMPlan | null>>('/api/v1/pm/plans/latest'),
    get: (id: string) => get<import('./types').SingleResponse<import('./types').PMPlan>>(`/api/v1/pm/plans/${id}`),
    decisions: (params?: { cursor?: string; limit?: number; decision_type?: string; outcome?: string }) => {
      const searchParams = new URLSearchParams();
      if (params?.cursor) searchParams.set('cursor', params.cursor);
      if (params?.limit != null) searchParams.set('limit', String(params.limit));
      if (params?.decision_type) searchParams.set('decision_type', params.decision_type);
      if (params?.outcome) searchParams.set('outcome', params.outcome);
      const qs = searchParams.toString();
      return get<import('./types').PMDecisionsResponse>(`/api/v1/pm/decisions${qs ? `?${qs}` : ''}`);
    },
    status: () => get<import('./types').SingleResponse<import('./types').PMStatus>>('/api/v1/pm/status'),
    // Documents
    listDocuments: () =>
      get<import('./types').ListResponse<import('./types').PMDocument>>('/api/v1/pm/documents'),
    getDocument: (docId: string) =>
      get<import('./types').SingleResponse<import('./types').PMDocument>>(`/api/v1/pm/documents/${docId}`),
    createDocument: (body: { title: string; content?: string; doc_type?: string; source_type?: string; source_url?: string; source_id?: string; source_meta?: Record<string, unknown> }) =>
      post<import('./types').SingleResponse<import('./types').PMDocument>>('/api/v1/pm/documents', body),
    updateDocument: (docId: string, body: Record<string, unknown>) =>
      patch<import('./types').SingleResponse<import('./types').PMDocument>>(`/api/v1/pm/documents/${docId}`, body),
    deleteDocument: (docId: string) =>
      del(`/api/v1/pm/documents/${docId}`),
  },
  sessions: {
    list: (params?: { status?: string; cursor?: string; limit?: number; repository_id?: string; triggered_by_user_id?: string; triggered_by_user_ids?: string[]; search?: string; include_archived?: boolean; only_archived?: boolean }) => {
      const searchParams = new URLSearchParams();
      if (params?.status) searchParams.set('status', params.status);
      if (params?.cursor) searchParams.set('cursor', params.cursor);
      if (params?.limit) searchParams.set('limit', String(params.limit));
      if (params?.repository_id) searchParams.set('repository_id', params.repository_id);
      if (params?.triggered_by_user_ids?.length) searchParams.set('triggered_by_user_ids', params.triggered_by_user_ids.join(','));
      if (params?.triggered_by_user_id) searchParams.set('triggered_by_user_id', params.triggered_by_user_id);
      if (params?.search) searchParams.set('search', params.search);
      if (params?.only_archived) searchParams.set('only_archived', 'true');
      else if (params?.include_archived) searchParams.set('include_archived', 'true');
      const qs = searchParams.toString();
      return get<import('./types').ListResponse<import('./types').SessionListItem>>(`/api/v1/sessions${qs ? `?${qs}` : ''}`);
    },
    counts: (params?: { repository_id?: string; triggered_by_user_id?: string; triggered_by_user_ids?: string[] }) => {
      const searchParams = new URLSearchParams();
      if (params?.repository_id) searchParams.set('repository_id', params.repository_id);
      if (params?.triggered_by_user_ids?.length) searchParams.set('triggered_by_user_ids', params.triggered_by_user_ids.join(','));
      if (params?.triggered_by_user_id) searchParams.set('triggered_by_user_id', params.triggered_by_user_id);
      const qs = searchParams.toString();
      return get<import('./types').SingleResponse<import('./types').SessionCounts>>(`/api/v1/sessions/counts${qs ? `?${qs}` : ''}`);
    },
    recordView: (sessionId: string) => post<{ status: string }>(`/api/v1/sessions/${sessionId}/view`, {}),
    get: (id: string) => get<import('./types').SingleResponse<import('./types').SessionDetail>>(`/api/v1/sessions/${id}`),
    getDiff: (id: string) => get<import('./types').SingleResponse<import('./types').SessionDiff>>(`/api/v1/sessions/${id}/diff`),
    update: (id: string, body: { title: string }) =>
      patch<import('./types').SingleResponse<import('./types').Session>>(`/api/v1/sessions/${id}`, body),
    getLogs: (sessionId: string) => get<import('./types').ListResponse<import('./types').SessionLog>>(`/api/v1/sessions/${sessionId}/logs`),
    getTimeline: (sessionId: string) => get<import('./types').ListResponse<import('./types').SessionTimelineEntry>>(`/api/v1/sessions/${sessionId}/timeline`),
    getPR: (sessionId: string) => get<import('./types').SingleResponse<import('./types').PullRequest | null>>(`/api/v1/sessions/${sessionId}/pr`),
    createPR: (sessionId: string, options?: { draft?: boolean; authorMode?: 'auto' | 'user' | 'app'; resumeToken?: string }) =>
      post<{ status: string }>(`/api/v1/sessions/${sessionId}/pr`, options ? {
        ...(options.draft !== undefined ? { draft: options.draft } : {}),
        ...(options.authorMode ? { author_mode: options.authorMode } : {}),
        ...(options.resumeToken ? { resume_token: options.resumeToken } : {}),
      } : undefined),
    pushChangesToPR: (sessionId: string, options?: { authorMode?: 'auto' | 'user' | 'app'; resumeToken?: string }) =>
      post<{ status: string }>(`/api/v1/sessions/${sessionId}/pr/push`, options ? {
        ...(options.authorMode ? { author_mode: options.authorMode } : {}),
        ...(options.resumeToken ? { resume_token: options.resumeToken } : {}),
      } : undefined),
    getQuestions: (sessionId: string) => get<import('./types').ListResponse<import('./types').SessionQuestion>>(`/api/v1/sessions/${sessionId}/questions`),
    answerQuestion: (sessionId: string, questionId: string, answer: string) =>
      post<import('./types').SingleResponse<import('./types').SessionQuestion>>(`/api/v1/sessions/${sessionId}/questions/${questionId}/answer`, { answer }),
    createManual: (body: { message: string; images?: string[]; references?: import('./types').SessionInputReference[]; commands?: import('./types').SessionInputCommand[]; agent_type?: string; model?: string; reasoning_effort?: 'low' | 'medium' | 'high' | 'xhigh' | 'max'; autonomy_level?: string; token_mode?: string; repository_id?: string; branch?: string; linear_private?: boolean; linear_state_sync_disabled?: boolean }) =>
      post<import('./types').SingleResponse<import('./types').Session>>('/api/v1/sessions/manual', body),
    getMessages: (sessionId: string) =>
      get<import('./types').ListResponse<import('./types').SessionMessage>>(`/api/v1/sessions/${sessionId}/messages`),
    sendMessage: (sessionId: string, body: { message: string; images?: string[]; references?: import('./types').SessionInputReference[]; commands?: import('./types').SessionInputCommand[]; planMode?: boolean; model?: string; resolveReviewCommentIDs?: string[] }) =>
      post<import('./types').SingleResponse<import('./types').SessionMessage>>(
        `/api/v1/sessions/${sessionId}/messages`,
        {
          message: body.message,
          images: body.images,
          references: body.references && body.references.length > 0 ? body.references : undefined,
          commands: body.commands && body.commands.length > 0 ? body.commands : undefined,
          plan_mode: body.planMode || undefined,
          ...(body.model ? { model: body.model } : {}),
          resolve_review_comment_ids: body.resolveReviewCommentIDs && body.resolveReviewCommentIDs.length > 0 ? body.resolveReviewCommentIDs : undefined,
        },
      ),
    endSession: (sessionId: string) =>
      post<import('./types').SingleResponse<import('./types').Session>>(`/api/v1/sessions/${sessionId}/end`),
    retry: (sessionId: string) =>
      post<import('./types').SingleResponse<import('./types').Session>>(`/api/v1/sessions/${sessionId}/retry`),
    cancelSession: (sessionId: string) =>
      post<import('./types').SingleResponse<import('./types').Session>>(`/api/v1/sessions/${sessionId}/cancel`),
    archive: (sessionId: string) =>
      post<{ status: string }>(`/api/v1/sessions/${sessionId}/archive`, {}),
    unarchive: (sessionId: string) =>
      post<{ status: string }>(`/api/v1/sessions/${sessionId}/unarchive`, {}),
    // Thread endpoints
    listThreads: (sessionId: string) =>
      get<import('./types').ListResponse<import('./types').SessionThread>>(`/api/v1/sessions/${sessionId}/threads`),
    getThread: (sessionId: string, threadId: string) =>
      get<import('./types').SingleResponse<import('./types').SessionThread>>(`/api/v1/sessions/${sessionId}/threads/${threadId}`),
    createThread: (sessionId: string, body: { agent_type?: string; model?: string; label: string; instructions?: string; file_scope?: string[] }) =>
      post<import('./types').SingleResponse<import('./types').SessionThread>>(`/api/v1/sessions/${sessionId}/threads`, body),
    // model: omit to keep the existing override, pass null to clear it, pass a
    // string to set/validate it. The backend distinguishes these three states.
    updateThread: (sessionId: string, threadId: string, body: { agent_type?: string; model?: string | null; label: string }) =>
      patch<import('./types').SingleResponse<import('./types').SessionThread>>(`/api/v1/sessions/${sessionId}/threads/${threadId}`, body),
    archiveThread: (sessionId: string, threadId: string) =>
      post<import('./types').SingleResponse<import('./types').SessionThread>>(`/api/v1/sessions/${sessionId}/threads/${threadId}/archive`, {}),
    sendThreadMessage: (sessionId: string, threadId: string, body: { message: string; images?: string[]; references?: import('./types').SessionInputReference[]; commands?: import('./types').SessionInputCommand[]; planMode?: boolean; resolveReviewCommentIDs?: string[] }) =>
      post<import('./types').SingleResponse<import('./types').SessionMessage>>(
        `/api/v1/sessions/${sessionId}/threads/${threadId}/messages`,
        {
          message: body.message,
          images: body.images,
          references: body.references && body.references.length > 0 ? body.references : undefined,
          commands: body.commands && body.commands.length > 0 ? body.commands : undefined,
          plan_mode: body.planMode || undefined,
          resolve_review_comment_ids: body.resolveReviewCommentIDs && body.resolveReviewCommentIDs.length > 0 ? body.resolveReviewCommentIDs : undefined,
        },
      ),
    endThread: (sessionId: string, threadId: string) =>
      post<import('./types').SingleResponse<import('./types').SessionThread>>(`/api/v1/sessions/${sessionId}/threads/${threadId}/end`),
    cancelThread: (sessionId: string, threadId: string) =>
      post<import('./types').SingleResponse<import('./types').SessionThread>>(`/api/v1/sessions/${sessionId}/threads/${threadId}/cancel`),
    forkThread: (sessionId: string, threadId: string, body: { label?: string } = {}) =>
      post<import('./types').SingleResponse<import('./types').ForkResult>>(`/api/v1/sessions/${sessionId}/threads/${threadId}/fork`, body),
    revertThread: (sessionId: string, threadId: string) =>
      post<import('./types').SingleResponse<import('./types').ForkResult>>(`/api/v1/sessions/${sessionId}/threads/${threadId}/revert`),
    getThreadMessages: (sessionId: string, threadId: string) =>
      get<import('./types').ListResponse<import('./types').SessionMessage>>(`/api/v1/sessions/${sessionId}/threads/${threadId}/messages`),
    getThreadLogs: (sessionId: string, threadId: string) =>
      get<import('./types').ListResponse<import('./types').SessionLog>>(`/api/v1/sessions/${sessionId}/threads/${threadId}/logs`),
    listThreadFileEvents: (sessionId: string, since?: string) => {
      const qs = since ? `?since=${encodeURIComponent(since)}` : '';
      return get<import('./types').ListResponse<import('./types').SessionThreadFileEvent>>(`/api/v1/sessions/${sessionId}/thread-file-events${qs}`);
    },
    listReviewComments: (sessionId: string) =>
      get<import('./types').ListResponse<import('./types').SessionReviewComment>>(`/api/v1/sessions/${sessionId}/review-comments`),
    createReviewComment: (sessionId: string, body: { file_path: string; line_number: number; side?: string; body: string }) =>
      post<import('./types').SingleResponse<import('./types').SessionReviewComment>>(`/api/v1/sessions/${sessionId}/review-comments`, body),
    updateReviewComment: (sessionId: string, commentId: string, body: { body?: string; resolved?: boolean }) =>
      patch<import('./types').SingleResponse<import('./types').SessionReviewComment>>(`/api/v1/sessions/${sessionId}/review-comments/${commentId}`, body),
    deleteReviewComment: (sessionId: string, commentId: string) =>
      del(`/api/v1/sessions/${sessionId}/review-comments/${commentId}`),
    sendReviewComments: (sessionId: string) =>
      post<import('./types').SingleResponse<{ message: string; sent: boolean }>>(`/api/v1/sessions/${sessionId}/review-comments/send`),
    composerFiles: (sessionId: string, query: string) => {
      const params = new URLSearchParams({ q: query });
      return get<import('./types').ListResponse<import('./types').SessionInputReference>>(`/api/v1/sessions/${sessionId}/composer/files?${params.toString()}`);
    },
    listFiles: (sessionId: string, path?: string) => {
      const params = new URLSearchParams();
      if (path) params.set('path', path);
      const qs = params.toString();
      return get<import('./types').ListResponse<import('./types').FileEntry>>(`/api/v1/sessions/${sessionId}/files${qs ? `?${qs}` : ''}`);
    },
    getFileContent: (sessionId: string, path: string) =>
      get<import('./types').SingleResponse<import('./types').FileContent>>(`/api/v1/sessions/${sessionId}/files/content?path=${encodeURIComponent(path)}`),
    getFileContext: (sessionId: string, path: string, line: number, above?: number, below?: number) => {
      const params = new URLSearchParams({ path, line: String(line) });
      if (above != null) params.set('above', String(above));
      if (below != null) params.set('below', String(below));
      return get<import('./types').SingleResponse<import('./types').FileContextResponse>>(`/api/v1/sessions/${sessionId}/files/context?${params.toString()}`);
    },
    preview: {
      get: (sessionId: string) =>
        get<import('./types').SingleResponse<import('./preview-types').PreviewStatusResponse>>(`/api/v1/sessions/${sessionId}/preview`)
          .then(r => r.data),
      start: (sessionId: string, config?: Record<string, unknown>) =>
        post<import('./types').SingleResponse<import('./preview-types').PreviewInstance>>(`/api/v1/sessions/${sessionId}/preview`, config ? { config } : undefined)
          .then(r => r.data),
      stop: (sessionId: string) => del(`/api/v1/sessions/${sessionId}/preview`),
      restart: (sessionId: string) => post(`/api/v1/sessions/${sessionId}/preview/restart`),
      bootstrap: (sessionId: string) =>
        post<import('./types').SingleResponse<{ token: string; preview_id: string }>>(`/api/v1/sessions/${sessionId}/preview/bootstrap`)
          .then(r => r.data),
      extend: (sessionId: string) => post(`/api/v1/sessions/${sessionId}/preview/extend`),
      services: (sessionId: string) =>
        get<import('./types').ListResponse<import('./preview-types').PreviewService>>(`/api/v1/sessions/${sessionId}/preview/services`)
          .then(r => r.data ?? []),
      console: (sessionId: string) =>
        get<import('./types').ListResponse<import('./preview-types').ConsoleMessage>>(`/api/v1/sessions/${sessionId}/preview/console`)
          .then(r => r.data ?? []),
      inspect: (sessionId: string, x: number, y: number) =>
        post<import('./types').SingleResponse<import('./preview-types').ElementInfo>>(`/api/v1/sessions/${sessionId}/preview/inspect`, { x, y })
          .then(r => r.data),
      designFeedback: (sessionId: string, feedback: import('./preview-types').DesignModeFeedback) => post(`/api/v1/sessions/${sessionId}/preview/design-feedback`, feedback),
    },
  },
  settings: {
    get: () => get<import('./types').SingleResponse<import('./types').Organization>>('/api/v1/settings'),
    update: (data: Record<string, unknown>) => patch<import('./types').SingleResponse<import('./types').Organization>>('/api/v1/settings', data),
    getLLMDefaults: () => get<{ data: Record<string, string> }>('/api/v1/settings/llm-defaults'),
    getLLMModels: () => get<{ data: Record<string, string[]> }>('/api/v1/settings/llm-models'),
  },
  credentials: {
    list: () => get<import('./types').ListResponse<import('./types').CredentialSummary>>('/api/v1/settings/credentials'),
    update: (provider: string, config: Record<string, unknown>) =>
      request<import('./types').SingleResponse<import('./types').CredentialSummary>>(`/api/v1/settings/credentials/${provider}`, {
        method: 'PUT',
        body: JSON.stringify(config),
      }),
    delete: (provider: string) => del(`/api/v1/settings/credentials/${provider}`),
  },
  userCredentials: {
    listPersonal: () =>
      get<import('./types').ListResponse<import('./types').UserCredentialSummary>>('/api/v1/settings/credentials/personal'),
    upsertPersonal: (provider: string, config: Record<string, unknown>, isTeamDefault?: boolean) =>
      request<import('./types').SingleResponse<import('./types').UserCredentialSummary>>(`/api/v1/settings/credentials/personal/${provider}`, {
        method: 'PUT',
        body: JSON.stringify({ config, is_team_default: isTeamDefault ?? false }),
      }),
    deletePersonal: (provider: string) =>
      del(`/api/v1/settings/credentials/personal/${provider}`),
    listTeamDefaults: () =>
      get<import('./types').ListResponse<import('./types').UserCredentialSummary>>('/api/v1/settings/credentials/team'),
    setTeamDefault: (provider: string, userId: string) =>
      request(`/api/v1/settings/credentials/team/${provider}`, {
        method: 'PUT',
        body: JSON.stringify({ user_id: userId }),
      }),
    removeTeamDefault: (provider: string) =>
      del(`/api/v1/settings/credentials/team/${provider}`),
    listResolved: () =>
      get<import('./types').ListResponse<import('./types').ResolvedCredential>>('/api/v1/settings/credentials/resolved'),
  },
  // Unified coding-credentials API — replaces the split userCredentials +
  // codingAuths surface. See docs/design/future/65-unified-coding-credentials.md.
  // The legacy `codingAuths` and `userCredentials` clients below still work
  // (their writes are mirrored into coding_credentials by the backend) and
  // remain in use by /settings/agent until the cleanup PR.
  codingCredentials: {
    list: (scope: 'org' | 'personal' | 'resolved' = 'personal') =>
      get<import('./types').ListResponse<import('./types').CodingCredentialSummary>>(
        `/api/v1/coding-credentials?scope=${scope}`,
      ),
    create: (body: {
      scope: 'org' | 'personal';
      agent: string;
      auth_type: 'api_key';
      label?: string;
      api_key?: string;
      api_type?: string;
      base_url?: string;
      agent_defaults?: Record<string, string>;
    }) =>
      post<import('./types').CodingCredentialSummary>('/api/v1/coding-credentials', body),
    update: (id: string, body: { scope: 'org' | 'personal'; label?: string; status?: string }) =>
      request<import('./types').CodingCredentialSummary>(`/api/v1/coding-credentials/${id}`, {
        method: 'PATCH',
        body: JSON.stringify(body),
      }),
    delete: (id: string, scope: 'org' | 'personal' = 'personal') =>
      del(`/api/v1/coding-credentials/${id}?scope=${scope}`),
    move: (id: string, body: { scope: 'org' | 'personal'; before_id?: string; after_id?: string; to_top?: boolean; to_bottom?: boolean }) =>
      request(`/api/v1/coding-credentials/${id}/move`, {
        method: 'PATCH',
        body: JSON.stringify(body),
      }),
    reorder: (scope: 'org' | 'personal', orderedIDs: string[]) =>
      request('/api/v1/coding-credentials/reorder', {
        method: 'PATCH',
        body: JSON.stringify({ scope, ordered_ids: orderedIDs }),
      }),
  },
  codingAuths: {
    list: () =>
      get<import('./types').ListResponse<import('./types').CodingAuth>>('/api/v1/settings/coding-auths'),
    create: (body: {
      agent: string;
      auth_type: string;
      label?: string;
      api_key?: string;
      api_type?: string;
      base_url?: string;
      agent_defaults?: Record<string, string>;
    }) =>
      post<import('./types').SingleResponse<import('./types').CodingAuth>>('/api/v1/settings/coding-auths', body),
    reorder: (ids: string[]) =>
      request('/api/v1/settings/coding-auths/reorder', {
        method: 'PATCH',
        body: JSON.stringify({ ids }),
      }),
    update: (id: string, body: { label?: string }) =>
      request<import('./types').SingleResponse<import('./types').CodingAuth>>(`/api/v1/settings/coding-auths/${id}`, {
        method: 'PATCH',
        body: JSON.stringify(body),
      }),
    delete: (id: string) => del(`/api/v1/settings/coding-auths/${id}`),
  },
  integrations: {
    list: () => get<import('./types').ListResponse<import('./types').Integration>>('/api/v1/integrations'),
    loginGitHub: () => {
      window.location.href = `${API_BASE}/api/v1/integrations/github/login`;
    },
    loginLinear: () => {
      window.location.href = `${API_BASE}/api/v1/integrations/linear/login`;
    },
    connectLinear: () => post<import('./types').SingleResponse<import('./types').Integration>>('/api/v1/integrations/linear/connect'),
    loginSlack: () => {
      window.location.href = `${API_BASE}/api/v1/integrations/slack/login`;
    },
    connectSlack: () => post<import('./types').SingleResponse<import('./types').Integration>>('/api/v1/integrations/slack/connect'),
    listSlackChannels: () => get<{ data: Array<{ id: string; name: string; selected: boolean }> }>('/api/v1/integrations/slack/channels'),
    updateSlackChannels: (channelIds: string[]) => request('/api/v1/integrations/slack/channels', {
      method: 'PATCH',
      body: JSON.stringify({ channel_ids: channelIds }),
    }),
    connectNotion: (accessToken: string) => post<import('./types').SingleResponse<import('./types').Integration>>('/api/v1/integrations/notion/connect', { access_token: accessToken }),
    connectCircleCI: (authToken: string, projectSlug: string) =>
      post<import('./types').SingleResponse<import('./types').Integration>>('/api/v1/integrations/circleci/connect', {
        auth_token: authToken,
        project_slug: projectSlug,
      }),
    disconnect: (provider: string) => del(`/api/v1/integrations/${provider}/disconnect`),
    syncGitHub: () => post<{ data: { repos_synced: number; errors: number } }>('/api/v1/integrations/github/sync'),
  },
  codexAuth: {
    // `scope` defaults to "org" on the server; pass "personal" to write the
    // pending-auth row against the caller's user_id in coding_credentials so
    // the resulting subscription appears in the user's personal stack.
    initiate: (label?: string, scope?: 'org' | 'personal') =>
      post<import('./types').SingleResponse<import('./types').CodexDeviceAuth>>(
        '/api/v1/settings/codex-auth/initiate',
        { label: label ?? '', ...(scope ? { scope } : {}) },
      ),
    status: (label?: string, scope?: 'org' | 'personal') => {
      const params = new URLSearchParams();
      if (label) params.set('label', label);
      if (scope) params.set('scope', scope);
      const qs = params.toString();
      return get<import('./types').SingleResponse<import('./types').CodexAuthStatus>>(
        `/api/v1/settings/codex-auth/status${qs ? `?${qs}` : ''}`,
      );
    },
    listSubscriptions: () => get<import('./types').ListResponse<import('./types').CodexSubscription>>('/api/v1/settings/codex-auth/subscriptions'),
    removeSubscription: (id: string) => del(`/api/v1/settings/codex-auth/subscriptions/${id}`),
    // Disconnects every ChatGPT subscription for the org. Used by the
    // single-subscription UI (account settings, agent settings editor) where
    // there is no per-subscription picker.
    disconnectAll: () => post<import('./types').SingleResponse<{ disconnected: boolean }>>('/api/v1/settings/codex-auth/disconnect'),
  },
  claudeCodeAuth: {
    // Starts a PKCE auth flow. The response's authorize_url is opened in the
    // user's browser; after logging in the user pastes `<code>#<state>` back
    // and the caller invokes complete() with it. `scope` defaults to "org"
    // on the server; "personal" routes the pending row into the caller's
    // own user-scoped credential stack.
    initiate: (label: string, scope?: 'org' | 'personal') =>
      post<import('./types').SingleResponse<import('./types').ClaudeCodeInitiateResponse>>(
        '/api/v1/settings/claude-code-auth/initiate',
        { label, ...(scope ? { scope } : {}) },
      ),
    complete: (label: string, code: string, scope?: 'org' | 'personal') =>
      post<import('./types').SingleResponse<import('./types').ClaudeCodeCompleteResponse>>(
        '/api/v1/settings/claude-code-auth/complete',
        { label, code, ...(scope ? { scope } : {}) },
      ),
    listSubscriptions: () => get<import('./types').ListResponse<import('./types').ClaudeCodeSubscription>>('/api/v1/settings/claude-code-auth/subscriptions'),
    removeSubscription: (id: string) => del(`/api/v1/settings/claude-code-auth/subscriptions/${id}`),
    // Disconnects every Claude subscription for the org while leaving any
    // Anthropic API-key fallback in place.
    disconnectAll: () => post<import('./types').SingleResponse<{ disconnected: boolean }>>('/api/v1/settings/claude-code-auth/disconnect'),
  },
  githubStatus: {
    get: () => get<{ connected: boolean; has_repo_scope: boolean; github_login?: string; pr_authorship_mode: string; pr_draft_default: boolean }>('/api/v1/users/me/github-status'),
    connect: (resumeToken?: string) => {
      const searchParams = new URLSearchParams();
      if (resumeToken) searchParams.set('resume_token', resumeToken);
      const qs = searchParams.toString();
      window.location.href = `${API_BASE}/api/v1/users/me/github/connect${qs ? `?${qs}` : ''}`;
    },
    disconnect: () => post('/api/v1/users/me/github/disconnect'),
  },
  priority: {
    getForIssue: (issueId: string) => get<import('./types').SingleResponse<import('./types').PriorityScore>>(`/api/v1/issues/${issueId}/priority`),
    getComplexity: (issueId: string) => get<import('./types').SingleResponse<import('./types').ComplexityEstimate>>(`/api/v1/issues/${issueId}/complexity`),
    list: (params?: { eligible_only?: boolean; limit?: number }) => {
      const searchParams = new URLSearchParams();
      if (params?.eligible_only) searchParams.set('eligible_only', 'true');
      if (params?.limit) searchParams.set('limit', String(params.limit));
      const qs = searchParams.toString();
      return get<import('./types').ListResponse<import('./types').PriorityScore>>(`/api/v1/priority-scores${qs ? `?${qs}` : ''}`);
    },
    reprioritize: (issueId: string) => post(`/api/v1/issues/${issueId}/reprioritize`),
  },
  memories: {
    listByRepo: (repo: string, params?: { status?: string; cursor?: string }) => {
      const searchParams = new URLSearchParams();
      if (params?.status) searchParams.set('status', params.status);
      if (params?.cursor) searchParams.set('cursor', params.cursor);
      const qs = searchParams.toString();
      return get<import('./types').ListResponse<import('./types').Memory>>(`/api/v1/memories/${repo}${qs ? `?${qs}` : ''}`);
    },
    updateStatus: (id: string, status: 'active' | 'dismissed') =>
      patch<import('./types').SingleResponse<import('./types').Memory>>(`/api/v1/memories/${id}`, { status }),
    updateRule: (id: string, rule: string) => {
      return request<import('./types').SingleResponse<import('./types').Memory>>(`/api/v1/memories/${id}`, {
        method: 'PUT',
        body: JSON.stringify({ rule }),
      });
    },
    create: (memory: { repo: string; rule: string; category?: string; scope?: string; file_patterns?: string[] }) =>
      post<import('./types').SingleResponse<import('./types').Memory>>('/api/v1/memories', memory),
  },
  // Invitations addressed to the current user, across orgs. Distinct from
  // `team.listInvitations` (which lists invitations the *current org's admins*
  // have sent out): these are the invites *I* can claim, surfaced in the org
  // switcher's "Pending invitations" section.
  invitations: {
    listPending: () =>
      get<import('./types').ListResponse<import('./types').PendingInvitationForUser>>(
        '/api/v1/invitations/pending',
      ),
    accept: (id: string) =>
      post<import('./types').SingleResponse<{ org_id: string; role: string }>>(
        `/api/v1/invitations/${id}/accept`,
      ),
    decline: (id: string) => post<void>(`/api/v1/invitations/${id}/decline`),
  },
  team: {
    listMembers: () => get<import('./types').ListResponse<import('./types').User>>('/api/v1/team/members'),
    changeRole: (id: string, role: string) =>
      patch<import('./types').SingleResponse<import('./types').User>>(`/api/v1/team/members/${id}/role`, { role }),
    removeMember: (id: string) => del<void>(`/api/v1/team/members/${id}`),
    listInvitations: () =>
      get<import('./types').ListResponse<import('./types').InvitationResponse>>('/api/v1/team/invitations'),
    createInvitation: (body: { email?: string; github_username?: string; role: string }) =>
      post<import('./types').SingleResponse<import('./types').InvitationResponse>>('/api/v1/team/invitations', body),
    revokeInvitation: (id: string) => del<void>(`/api/v1/team/invitations/${id}`),
    githubInviteStatus: () =>
      get<import('./types').SingleResponse<import('./types').GitHubInviteStatus>>('/api/v1/team/github/status'),
    searchGitHubUsers: (q: string) =>
      get<import('./types').ListResponse<import('./types').GitHubUserSuggestion>>(
        `/api/v1/team/github/users?q=${encodeURIComponent(q)}`,
      ),
  },
  projects: {
    list: (params?: { status?: string; cursor?: string; limit?: number; repository_id?: string; search?: string; proposed_by_pm?: boolean; created_by?: string; created_by_ids?: string[]; include_archived?: boolean; only_archived?: boolean }) => {
      const searchParams = new URLSearchParams();
      if (params?.status) searchParams.set('status', params.status);
      if (params?.cursor) searchParams.set('cursor', params.cursor);
      if (params?.limit) searchParams.set('limit', String(params.limit));
      if (params?.repository_id) searchParams.set('repository_id', params.repository_id);
      if (params?.search) searchParams.set('search', params.search);
      if (params?.proposed_by_pm !== undefined) searchParams.set('proposed_by_pm', String(params.proposed_by_pm));
      if (params?.created_by_ids?.length) searchParams.set('created_by_ids', params.created_by_ids.join(','));
      if (params?.created_by) searchParams.set('created_by', params.created_by);
      if (params?.only_archived) searchParams.set('only_archived', 'true');
      else if (params?.include_archived) searchParams.set('include_archived', 'true');
      const qs = searchParams.toString();
      return get<import('./types').ListResponse<import('./types').Project>>(`/api/v1/projects${qs ? `?${qs}` : ''}`);
    },
    get: (id: string) => get<import('./types').SingleResponse<import('./types').ProjectDetail>>(`/api/v1/projects/${id}`),
    create: (body: { title: string; goal: string; repository_id: string; scope?: string; completion_criteria?: string; execution_mode?: string; max_concurrent?: number; priority?: number; base_branch?: string; agent_type?: string; model?: string }) =>
      post<import('./types').SingleResponse<import('./types').Project>>('/api/v1/projects', body),
    update: (id: string, body: Record<string, unknown>) =>
      patch<import('./types').SingleResponse<import('./types').Project>>(`/api/v1/projects/${id}`, body),
    del: (id: string) => del(`/api/v1/projects/${id}`),
    start: (id: string) => post(`/api/v1/projects/${id}/start`),
    archive: (projectId: string) =>
      post<{ status: string }>(`/api/v1/projects/${projectId}/archive`, {}),
    unarchive: (projectId: string) =>
      post<{ status: string }>(`/api/v1/projects/${projectId}/unarchive`, {}),
    proposalSummary: () =>
      get<import('./types').SingleResponse<import('./types').ProposalSummary>>('/api/v1/projects/proposals/summary'),
    runNow: (id: string) => post<import('./types').SingleResponse<{ job_id: string }>>(`/api/v1/projects/${id}/run`),
    createTask: (projectId: string, body: { title: string; description?: string; approach?: string }) =>
      post<import('./types').SingleResponse<import('./types').ProjectTask>>(`/api/v1/projects/${projectId}/tasks`, body),
    updateTask: (projectId: string, taskId: string, body: Record<string, unknown>) =>
      patch<import('./types').SingleResponse<import('./types').ProjectTask>>(`/api/v1/projects/${projectId}/tasks/${taskId}`, body),
    deleteTask: (projectId: string, taskId: string) => del(`/api/v1/projects/${projectId}/tasks/${taskId}`),
    retryTask: (projectId: string, taskId: string) =>
      post<import('./types').SingleResponse<import('./types').ProjectTask>>(`/api/v1/projects/${projectId}/tasks/${taskId}/retry`),
    listCycles: (projectId: string, params?: { limit?: number }) => {
      const searchParams = new URLSearchParams();
      if (params?.limit) searchParams.set('limit', String(params.limit));
      const qs = searchParams.toString();
      return get<import('./types').ListResponse<import('./types').ProjectCycle>>(`/api/v1/projects/${projectId}/cycles${qs ? `?${qs}` : ''}`);
    },
    getCycle: (projectId: string, cycleId: string) =>
      get<import('./types').SingleResponse<import('./types').ProjectCycle>>(`/api/v1/projects/${projectId}/cycles/${cycleId}`),
    // Attachments
    listAttachments: (projectId: string) =>
      get<import('./types').ListResponse<import('./types').ProjectAttachment>>(`/api/v1/projects/${projectId}/attachments`),
    createAttachment: (projectId: string, body: { file_name: string; file_url: string; file_type?: string; thumbnail_url?: string; file_size?: number; category?: string; caption?: string }) =>
      post<import('./types').SingleResponse<import('./types').ProjectAttachment>>(`/api/v1/projects/${projectId}/attachments`, body),
    updateAttachment: (projectId: string, attachmentId: string, body: Record<string, unknown>) =>
      patch<import('./types').SingleResponse<import('./types').ProjectAttachment>>(`/api/v1/projects/${projectId}/attachments/${attachmentId}`, body),
    deleteAttachment: (projectId: string, attachmentId: string) =>
      del(`/api/v1/projects/${projectId}/attachments/${attachmentId}`),
    // Specs
    listSpecs: (projectId: string) =>
      get<import('./types').ListResponse<import('./types').ProjectSpec>>(`/api/v1/projects/${projectId}/specs`),
    createSpec: (projectId: string, body: { title: string; content?: string; spec_type?: string }) =>
      post<import('./types').SingleResponse<import('./types').ProjectSpec>>(`/api/v1/projects/${projectId}/specs`, body),
    getSpec: (projectId: string, specId: string) =>
      get<import('./types').SingleResponse<import('./types').ProjectSpec>>(`/api/v1/projects/${projectId}/specs/${specId}`),
    updateSpec: (projectId: string, specId: string, body: Record<string, unknown>) =>
      patch<import('./types').SingleResponse<import('./types').ProjectSpec>>(`/api/v1/projects/${projectId}/specs/${specId}`, body),
    deleteSpec: (projectId: string, specId: string) =>
      del(`/api/v1/projects/${projectId}/specs/${specId}`),
    // AI
    aiImprove: (projectId: string, body: { target: string; spec_id?: string; prompt?: string }) =>
      post<import('./types').SingleResponse<import('./types').AIImprovementResponse>>(`/api/v1/projects/${projectId}/ai/improve`, body),
    aiGenerate: (body: { description: string }) =>
      post<{ data: import('./types').GeneratedProject }>('/api/v1/projects/ai/generate', body),
  },
  automations: {
    list: (params?: { enabled?: boolean; cursor?: string; limit?: number; search?: string }) => {
      const searchParams = new URLSearchParams();
      if (params?.enabled !== undefined) searchParams.set('enabled', String(params.enabled));
      if (params?.cursor) searchParams.set('cursor', params.cursor);
      if (params?.limit) searchParams.set('limit', String(params.limit));
      if (params?.search) searchParams.set('search', params.search);
      const qs = searchParams.toString();
      return get<import('./types').ListResponse<import('./types').Automation>>(`/api/v1/automations${qs ? `?${qs}` : ''}`);
    },
    get: (id: string) =>
      get<import('./types').SingleResponse<import('./types').Automation>>(`/api/v1/automations/${id}`),
    create: (body: Record<string, unknown>) =>
      post<import('./types').SingleResponse<import('./types').Automation>>('/api/v1/automations', body),
    update: (id: string, body: Record<string, unknown>) =>
      patch<import('./types').SingleResponse<import('./types').Automation>>(`/api/v1/automations/${id}`, body),
    del: (id: string) => del(`/api/v1/automations/${id}`),
    pause: (id: string) =>
      post<import('./types').SingleResponse<import('./types').Automation>>(`/api/v1/automations/${id}/pause`),
    resume: (id: string) =>
      post<import('./types').SingleResponse<import('./types').Automation>>(`/api/v1/automations/${id}/resume`),
    runNow: (id: string) =>
      post<import('./types').SingleResponse<import('./types').AutomationRun>>(`/api/v1/automations/${id}/run`),
    bulk: (body: { action: 'pause' | 'resume' | 'delete'; automation_ids?: string[] }) =>
      post<import('./types').AutomationBulkResponse>('/api/v1/automations/bulk', body),
    listRuns: (id: string, params?: { cursor?: string; limit?: number }) => {
      const searchParams = new URLSearchParams();
      if (params?.cursor) searchParams.set('cursor', params.cursor);
      if (params?.limit) searchParams.set('limit', String(params.limit));
      const qs = searchParams.toString();
      return get<import('./types').ListResponse<import('./types').AutomationRun>>(`/api/v1/automations/${id}/runs${qs ? `?${qs}` : ''}`);
    },
    getRun: (id: string, runId: string) =>
      get<import('./types').SingleResponse<import('./types').AutomationRun>>(`/api/v1/automations/${id}/runs/${runId}`),
    stats: (id: string, params?: { since?: string; until?: string }) => {
      const searchParams = new URLSearchParams();
      if (params?.since) searchParams.set('since', params.since);
      if (params?.until) searchParams.set('until', params.until);
      const qs = searchParams.toString();
      return get<import('./types').SingleResponse<import('./types').AutomationRunStats>>(`/api/v1/automations/${id}/stats${qs ? `?${qs}` : ''}`);
    },
  },
  auditLogs: {
    list: (params?: {
      actor_type?: string;
      action?: string;
      action_prefix?: string;
      resource_type?: string;
      resource_id?: string;
      user_id?: string;
      session_id?: string;
      project_id?: string;
      since?: string;
      until?: string;
      cursor?: string;
      limit?: number;
    }) => {
      const searchParams = new URLSearchParams();
      if (params?.actor_type) searchParams.set('actor_type', params.actor_type);
      if (params?.action) searchParams.set('action', params.action);
      if (params?.action_prefix) searchParams.set('action_prefix', params.action_prefix);
      if (params?.resource_type) searchParams.set('resource_type', params.resource_type);
      if (params?.resource_id) searchParams.set('resource_id', params.resource_id);
      if (params?.user_id) searchParams.set('user_id', params.user_id);
      if (params?.session_id) searchParams.set('session_id', params.session_id);
      if (params?.project_id) searchParams.set('project_id', params.project_id);
      if (params?.since) searchParams.set('since', params.since);
      if (params?.until) searchParams.set('until', params.until);
      if (params?.cursor) searchParams.set('cursor', params.cursor);
      if (params?.limit) searchParams.set('limit', String(params.limit));
      const qs = searchParams.toString();
      return get<import('./types').ListResponse<import('./types').AuditLog>>(`/api/v1/audit-logs${qs ? `?${qs}` : ''}`);
    },
    get: (id: number) => get<import('./types').SingleResponse<import('./types').AuditLog>>(`/api/v1/audit-logs/${id}`),
  },
  evals: {
    // Tasks
    listTasks: (params?: { source?: string; complexity?: string; archived?: string; tags?: string; cursor?: string; limit?: number }) => {
      const searchParams = new URLSearchParams();
      if (params?.source) searchParams.set('source', params.source);
      if (params?.complexity) searchParams.set('complexity', params.complexity);
      if (params?.archived) searchParams.set('archived', params.archived);
      if (params?.tags) searchParams.set('tags', params.tags);
      if (params?.cursor) searchParams.set('cursor', params.cursor);
      if (params?.limit) searchParams.set('limit', String(params.limit));
      const qs = searchParams.toString();
      return get<import('./types').ListResponse<import('./types').EvalTask>>(`/api/v1/evals/tasks${qs ? `?${qs}` : ''}`);
    },
    getTask: (id: string) => get<import('./types').SingleResponse<import('./types').EvalTask>>(`/api/v1/evals/tasks/${id}`),
    createTask: (body: {
      repo_id: string;
      name: string;
      description: string;
      base_commit_sha: string;
      solution_commit_sha?: string;
      solution_diff?: string;
      issue_description: string;
      issue_context?: Record<string, unknown>;
      scoring_criteria: import('./types').ScoringCriterion[];
      pass_threshold: number;
      source?: string;
      source_pr_number?: number;
      complexity: string;
      tags?: string[];
    }) => post<import('./types').SingleResponse<import('./types').EvalTask>>('/api/v1/evals/tasks', body),
    updateTask: (id: string, body: Record<string, unknown>) =>
      patch<import('./types').SingleResponse<import('./types').EvalTask>>(`/api/v1/evals/tasks/${id}`, body),
    archiveTask: (id: string) => del(`/api/v1/evals/tasks/${id}`),
    // Runs
    startRun: (taskId: string, body: { model: string; config_ref?: string; context_overrides?: Record<string, unknown> }) =>
      post<import('./types').SingleResponse<import('./types').EvalRun>>(`/api/v1/evals/tasks/${taskId}/runs`, body),
    listRuns: (taskId: string, params?: { cursor?: string; limit?: number }) => {
      const searchParams = new URLSearchParams();
      if (params?.cursor) searchParams.set('cursor', params.cursor);
      if (params?.limit) searchParams.set('limit', String(params.limit));
      const qs = searchParams.toString();
      return get<import('./types').ListResponse<import('./types').EvalRun>>(`/api/v1/evals/tasks/${taskId}/runs${qs ? `?${qs}` : ''}`);
    },
    getRun: (id: string) => get<import('./types').SingleResponse<import('./types').EvalRun>>(`/api/v1/evals/runs/${id}`),
    // Batch
    listBatches: (params?: { limit?: number }) => {
      const searchParams = new URLSearchParams();
      if (params?.limit) searchParams.set('limit', String(params.limit));
      const qs = searchParams.toString();
      return get<import('./types').ListResponse<import('./types').EvalBatch>>(`/api/v1/evals/batch${qs ? `?${qs}` : ''}`);
    },
    startBatch: (body: { name: string; task_ids: string[]; configs: Array<{ model: string; config_ref?: string }> }) =>
      post<import('./types').SingleResponse<import('./types').EvalBatch>>('/api/v1/evals/batch', body),
    getBatch: (id: string) => get<import('./types').SingleResponse<import('./types').EvalBatchDetail>>(`/api/v1/evals/batch/${id}`),
    // Bootstrap
    bootstrap: (body: { repo_id: string }) =>
      post<import('./types').SingleResponse<import('./types').EvalBootstrapRun>>('/api/v1/evals/bootstrap', body),
    getBootstrapCandidates: (params?: { repo_id?: string; bootstrap_run_id?: string }) => {
      const searchParams = new URLSearchParams();
      if (params?.repo_id) searchParams.set('repo_id', params.repo_id);
      if (params?.bootstrap_run_id) searchParams.set('bootstrap_run_id', params.bootstrap_run_id);
      const qs = searchParams.toString();
      return get<import('./types').SingleResponse<import('./types').EvalBootstrapRun>>(`/api/v1/evals/bootstrap/candidates${qs ? `?${qs}` : ''}`);
    },
    acceptBootstrapCandidates: (body: { bootstrap_run_id: string; candidate_indices: number[] }) =>
      post<import('./types').ListResponse<import('./types').EvalTask>>('/api/v1/evals/bootstrap/accept', body),
  },
  usage: {
    getSummary: (params?: { start?: string; end?: string }) => {
      const searchParams = new URLSearchParams();
      if (params?.start) searchParams.set('start', params.start);
      if (params?.end) searchParams.set('end', params.end);
      const qs = searchParams.toString();
      return get<import('./types').SingleResponse<import('./types').UsageSummary>>(`/api/v1/usage${qs ? `?${qs}` : ''}`);
    },
    getTimeseries: (params: { start: string; end: string; group_by?: string; stack_by?: string; user_id?: string; capacity?: string; agent?: string; model?: string; reasoning?: string }) => {
      const searchParams = new URLSearchParams({ start: params.start, end: params.end });
      if (params.group_by) searchParams.set('group_by', params.group_by);
      if (params.stack_by) searchParams.set('stack_by', params.stack_by);
      if (params.user_id) searchParams.set('user_id', params.user_id);
      if (params.capacity) searchParams.set('capacity', params.capacity);
      if (params.agent) searchParams.set('agent', params.agent);
      if (params.model) searchParams.set('model', params.model);
      if (params.reasoning) searchParams.set('reasoning', params.reasoning);
      return get<import('./types').SingleResponse<import('./types').UsageTimeseriesResponse>>(`/api/v1/usage/timeseries?${searchParams.toString()}`);
    },
    getBreakdown: (params: { start: string; end: string; dimension?: string; sort?: string; limit?: number; agent?: string; model?: string; reasoning?: string }) => {
      const searchParams = new URLSearchParams({ start: params.start, end: params.end });
      if (params.dimension) searchParams.set('dimension', params.dimension);
      if (params.sort) searchParams.set('sort', params.sort);
      if (params.limit) searchParams.set('limit', String(params.limit));
      if (params.agent) searchParams.set('agent', params.agent);
      if (params.model) searchParams.set('model', params.model);
      if (params.reasoning) searchParams.set('reasoning', params.reasoning);
      return get<import('./types').ListResponse<import('./types').UsageBreakdownRow>>(`/api/v1/usage/breakdown?${searchParams.toString()}`);
    },
    getExportUrl: (params: { start: string; end: string; granularity?: string; dimension?: string; tz?: string; agent?: string; model?: string; reasoning?: string }) => {
      const searchParams = new URLSearchParams({ start: params.start, end: params.end });
      if (params.granularity) searchParams.set('granularity', params.granularity);
      if (params.dimension) searchParams.set('dimension', params.dimension);
      if (params.tz) searchParams.set('tz', params.tz);
      if (params.agent) searchParams.set('agent', params.agent);
      if (params.model) searchParams.set('model', params.model);
      if (params.reasoning) searchParams.set('reasoning', params.reasoning);
      // window.open() can't send X-Active-Org-ID, so for multi-org users the
      // backend's session-hint org may not match the actively-viewed org.
      // Pass org_id explicitly; backend membership-checks it.
      const activeOrgId = getActiveOrgId();
      if (activeOrgId) searchParams.set('org_id', activeOrgId);
      return `${API_BASE}/api/v1/usage/export?${searchParams.toString()}`;
    },
  },
  reviewComments: {
    list: (params?: { pull_request_id?: string; filter_status?: string; cursor?: string }) => {
      const searchParams = new URLSearchParams();
      if (params?.pull_request_id) searchParams.set('pull_request_id', params.pull_request_id);
      if (params?.filter_status) searchParams.set('filter_status', params.filter_status);
      if (params?.cursor) searchParams.set('cursor', params.cursor);
      const qs = searchParams.toString();
      return get<import('./types').ListResponse<import('./types').ReviewComment>>(`/api/v1/review-comments${qs ? `?${qs}` : ''}`);
    },
  },
};

const API_BASE = process.env.NEXT_PUBLIC_API_URL || '';
const SENTRY_CLIENT_ID = process.env.NEXT_PUBLIC_SENTRY_CLIENT_ID || '';
const SENTRY_REDIRECT_URI = process.env.NEXT_PUBLIC_SENTRY_REDIRECT_URI || '';

class ApiError extends Error {
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

  const res = await fetch(`${API_BASE}${path}`, {
    ...options,
    credentials: 'include',
    headers,
  });

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
  // Do NOT set Content-Type — the browser sets it with the multipart boundary.

  const res = await fetch(`${API_BASE}/api/v1/uploads`, {
    method: 'POST',
    credentials: 'include',
    headers,
    body: formData,
  });

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
    logout: () => post('/api/v1/auth/logout'),
  },
  repositories: {
    list: () => get<import('./types').ListResponse<import('./types').Repository>>('/api/v1/repositories'),
    get: (id: string) => get<import('./types').SingleResponse<import('./types').Repository>>(`/api/v1/repositories/${id}`),
    update: (id: string, data: Record<string, unknown>) =>
      patch<import('./types').SingleResponse<import('./types').Repository>>(`/api/v1/repositories/${id}`, data),
    delete: (id: string) => del(`/api/v1/repositories/${id}`),
    summary: () => get<import('./types').ListResponse<import('./types').RepoSummary>>('/api/v1/repositories/summary'),
    branches: (id: string) => get<import('./types').ListResponse<{ name: string; protected: boolean }>>(`/api/v1/repositories/${id}/branches`),
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
    latest: () => get<import('./types').SingleResponse<import('./types').PMPlan>>('/api/v1/pm/plans/latest'),
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
    list: (params?: { status?: string; cursor?: string; limit?: number; repository_id?: string; triggered_by_user_id?: string; search?: string }) => {
      const searchParams = new URLSearchParams();
      if (params?.status) searchParams.set('status', params.status);
      if (params?.cursor) searchParams.set('cursor', params.cursor);
      if (params?.limit) searchParams.set('limit', String(params.limit));
      if (params?.repository_id) searchParams.set('repository_id', params.repository_id);
      if (params?.triggered_by_user_id) searchParams.set('triggered_by_user_id', params.triggered_by_user_id);
      if (params?.search) searchParams.set('search', params.search);
      const qs = searchParams.toString();
      return get<import('./types').ListResponse<import('./types').SessionListItem>>(`/api/v1/sessions${qs ? `?${qs}` : ''}`);
    },
    recordView: (sessionId: string) => post<{ status: string }>(`/api/v1/sessions/${sessionId}/view`, {}),
    get: (id: string) => get<import('./types').SingleResponse<import('./types').SessionDetail>>(`/api/v1/sessions/${id}`),
    getLogs: (sessionId: string) => get<import('./types').ListResponse<import('./types').SessionLog>>(`/api/v1/sessions/${sessionId}/logs`),
    getValidation: (sessionId: string) => get<import('./types').SingleResponse<import('./types').Validation>>(`/api/v1/sessions/${sessionId}/validation`),
    getPR: (sessionId: string) => get<import('./types').SingleResponse<import('./types').PullRequest>>(`/api/v1/sessions/${sessionId}/pr`),
    createPR: (sessionId: string, options?: { draft?: boolean }) =>
      post<{ status: string }>(`/api/v1/sessions/${sessionId}/pr`, options),
    getQuestions: (sessionId: string) => get<import('./types').ListResponse<import('./types').SessionQuestion>>(`/api/v1/sessions/${sessionId}/questions`),
    answerQuestion: (sessionId: string, questionId: string, answer: string) =>
      post<import('./types').SingleResponse<import('./types').SessionQuestion>>(`/api/v1/sessions/${sessionId}/questions/${questionId}/answer`, { answer }),
    createManual: (body: { message: string; images?: string[]; agent_type?: string; model?: string; autonomy_level?: string; token_mode?: string; repository_id?: string; branch?: string }) =>
      post<import('./types').SingleResponse<import('./types').Session>>('/api/v1/sessions/manual', body),
    getMessages: (sessionId: string) =>
      get<import('./types').ListResponse<import('./types').SessionMessage>>(`/api/v1/sessions/${sessionId}/messages`),
    sendMessage: (sessionId: string, message: string, images?: string[], planMode?: boolean, model?: string) =>
      post<import('./types').SingleResponse<import('./types').SessionMessage>>(`/api/v1/sessions/${sessionId}/messages`, { message, images, plan_mode: planMode || undefined, ...(model ? { model } : {}) }),
    endSession: (sessionId: string) =>
      post<import('./types').SingleResponse<import('./types').Session>>(`/api/v1/sessions/${sessionId}/end`),
    retry: (sessionId: string) =>
      post<import('./types').SingleResponse<import('./types').Session>>(`/api/v1/sessions/${sessionId}/retry`),
    cancelSession: (sessionId: string) =>
      post<import('./types').SingleResponse<import('./types').Session>>(`/api/v1/sessions/${sessionId}/cancel`),
    // Thread endpoints
    listThreads: (sessionId: string) =>
      get<import('./types').ListResponse<import('./types').SessionThread>>(`/api/v1/sessions/${sessionId}/threads`),
    getThread: (sessionId: string, threadId: string) =>
      get<import('./types').SingleResponse<import('./types').SessionThread>>(`/api/v1/sessions/${sessionId}/threads/${threadId}`),
    createThread: (sessionId: string, body: { agent_type?: string; model?: string; label: string; instructions?: string; file_scope?: string[] }) =>
      post<import('./types').SingleResponse<import('./types').SessionThread>>(`/api/v1/sessions/${sessionId}/threads`, body),
    sendThreadMessage: (sessionId: string, threadId: string, message: string, images?: string[]) =>
      post<import('./types').SingleResponse<import('./types').SessionMessage>>(`/api/v1/sessions/${sessionId}/threads/${threadId}/messages`, { message, images }),
    endThread: (sessionId: string, threadId: string) =>
      post<import('./types').SingleResponse<import('./types').SessionThread>>(`/api/v1/sessions/${sessionId}/threads/${threadId}/end`),
    getThreadMessages: (sessionId: string, threadId: string) =>
      get<import('./types').ListResponse<import('./types').SessionMessage>>(`/api/v1/sessions/${sessionId}/threads/${threadId}/messages`),
    getThreadLogs: (sessionId: string, threadId: string) =>
      get<import('./types').ListResponse<import('./types').SessionLog>>(`/api/v1/sessions/${sessionId}/threads/${threadId}/logs`),
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
  },
  settings: {
    get: () => get<import('./types').SingleResponse<import('./types').Organization>>('/api/v1/settings'),
    update: (data: Record<string, unknown>) => patch<import('./types').SingleResponse<import('./types').Organization>>('/api/v1/settings', data),
    getAgentDefaults: () => get<{ data: Record<string, Record<string, string>> }>('/api/v1/settings/agent-defaults'),
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
    disconnect: (provider: string) => del(`/api/v1/integrations/${provider}/disconnect`),
    syncGitHub: () => post<{ data: { repos_synced: number; errors: number } }>('/api/v1/integrations/github/sync'),
  },
  codexAuth: {
    initiate: () => post<import('./types').SingleResponse<import('./types').CodexDeviceAuth>>('/api/v1/settings/codex-auth/initiate'),
    status: () => get<import('./types').SingleResponse<import('./types').CodexAuthStatus>>('/api/v1/settings/codex-auth/status'),
    disconnect: () => post('/api/v1/settings/codex-auth/disconnect'),
  },
  githubStatus: {
    get: () => get<{ connected: boolean; has_repo_scope: boolean; github_login?: string; pr_authorship_mode: string; pr_draft_default: boolean }>('/api/v1/users/me/github-status'),
    connect: () => { window.location.href = `${API_BASE}/api/v1/users/me/github/connect`; },
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
  team: {
    listMembers: () => get<import('./types').ListResponse<import('./types').User>>('/api/v1/team/members'),
    changeRole: (id: string, role: string) =>
      patch<import('./types').SingleResponse<import('./types').User>>(`/api/v1/team/members/${id}/role`, { role }),
    removeMember: (id: string) => del<void>(`/api/v1/team/members/${id}`),
    listInvitations: () =>
      get<import('./types').ListResponse<import('./types').InvitationResponse>>('/api/v1/team/invitations'),
    createInvitation: (email: string, role: string) =>
      post<import('./types').SingleResponse<import('./types').InvitationResponse>>('/api/v1/team/invitations', { email, role }),
    revokeInvitation: (id: string) => del<void>(`/api/v1/team/invitations/${id}`),
  },
  projects: {
    list: (params?: { status?: string; cursor?: string; limit?: number; repository_id?: string; search?: string }) => {
      const searchParams = new URLSearchParams();
      if (params?.status) searchParams.set('status', params.status);
      if (params?.cursor) searchParams.set('cursor', params.cursor);
      if (params?.limit) searchParams.set('limit', String(params.limit));
      if (params?.repository_id) searchParams.set('repository_id', params.repository_id);
      if (params?.search) searchParams.set('search', params.search);
      const qs = searchParams.toString();
      return get<import('./types').ListResponse<import('./types').Project>>(`/api/v1/projects${qs ? `?${qs}` : ''}`);
    },
    get: (id: string) => get<import('./types').SingleResponse<import('./types').ProjectDetail>>(`/api/v1/projects/${id}`),
    create: (body: { title: string; goal: string; repository_id: string; scope?: string; completion_criteria?: string; execution_mode?: string; max_concurrent?: number; priority?: number; base_branch?: string; agent_type?: string; model?: string; schedule_enabled?: boolean; schedule_interval?: number; schedule_unit?: string }) =>
      post<import('./types').SingleResponse<import('./types').Project>>('/api/v1/projects', body),
    update: (id: string, body: Record<string, unknown>) =>
      patch<import('./types').SingleResponse<import('./types').Project>>(`/api/v1/projects/${id}`, body),
    del: (id: string) => del(`/api/v1/projects/${id}`),
    start: (id: string) => post(`/api/v1/projects/${id}/start`),
    pause: (id: string) => post(`/api/v1/projects/${id}/pause`),
    resume: (id: string) => post(`/api/v1/projects/${id}/resume`),
    approve: (id: string) => post(`/api/v1/projects/${id}/approve`),
    dismiss: (id: string, reason?: string) => post(`/api/v1/projects/${id}/dismiss`, reason ? { reason } : undefined),
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

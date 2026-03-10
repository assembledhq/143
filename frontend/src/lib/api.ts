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

export const api = {
  auth: {
    providers: () => get<import('./types').SingleResponse<import('./types').AuthProviders>>('/api/v1/auth/providers'),
    me: () => get<import('./types').SingleResponse<import('./types').User>>('/api/v1/auth/me'),
    login: (invitation?: string) => {
      const params = invitation ? `?invitation=${encodeURIComponent(invitation)}` : '';
      window.location.href = `${API_BASE}/api/v1/auth/github/login${params}`;
    },
    loginGoogle: (invitation?: string) => {
      const params = invitation ? `?invitation=${encodeURIComponent(invitation)}` : '';
      window.location.href = `${API_BASE}/api/v1/auth/google/login${params}`;
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
      post<import('./types').SingleResponse<import('./types').AgentRun>>(`/api/v1/issues/${issueId}/fix`, options),
  },
  runs: {
    list: (params?: { status?: string; cursor?: string; limit?: number }) => {
      const searchParams = new URLSearchParams();
      if (params?.status) searchParams.set('status', params.status);
      if (params?.cursor) searchParams.set('cursor', params.cursor);
      if (params?.limit) searchParams.set('limit', String(params.limit));
      const qs = searchParams.toString();
      return get<import('./types').ListResponse<import('./types').AgentRun>>(`/api/v1/runs${qs ? `?${qs}` : ''}`);
    },
    get: (id: string) => get<import('./types').SingleResponse<import('./types').AgentRun>>(`/api/v1/runs/${id}`),
    getLogs: (runId: string) => get<import('./types').ListResponse<import('./types').AgentRunLog>>(`/api/v1/runs/${runId}/logs`),
    getValidation: (runId: string) => get<import('./types').SingleResponse<import('./types').Validation>>(`/api/v1/runs/${runId}/validation`),
    getPR: (runId: string) => get<import('./types').SingleResponse<import('./types').PullRequest>>(`/api/v1/runs/${runId}/pr`),
    getQuestions: (runId: string) => get<import('./types').ListResponse<import('./types').AgentRunQuestion>>(`/api/v1/runs/${runId}/questions`),
    answerQuestion: (runId: string, questionId: string, answer: string) =>
      post<import('./types').SingleResponse<import('./types').AgentRunQuestion>>(`/api/v1/runs/${runId}/questions/${questionId}/answer`, { answer }),
  },
  pm: {
    // Cursor format for /pm/plans: "<created_at RFC3339Nano>|<uuid>" (treat as opaque).
    analyze: () => post<{ data: { job_id: string } }>('/api/v1/pm/analyze'),
    list: (params?: { cursor?: string; limit?: number }) => {
      const searchParams = new URLSearchParams();
      if (params?.cursor) searchParams.set('cursor', params.cursor);
      if (params?.limit != null) searchParams.set('limit', String(params.limit));
      const qs = searchParams.toString();
      return get<import('./types').ListResponse<import('./types').PMPlan>>(`/api/v1/pm/plans${qs ? `?${qs}` : ''}`);
    },
    latest: () => get<import('./types').SingleResponse<import('./types').PMPlan>>('/api/v1/pm/plans/latest'),
    get: (id: string) => get<import('./types').SingleResponse<import('./types').PMPlan>>(`/api/v1/pm/plans/${id}`),
    decisions: (params?: { cursor?: string; limit?: number }) => {
      const searchParams = new URLSearchParams();
      if (params?.cursor) searchParams.set('cursor', params.cursor);
      if (params?.limit != null) searchParams.set('limit', String(params.limit));
      const qs = searchParams.toString();
      return get<import('./types').PMDecisionsResponse>(`/api/v1/pm/decisions${qs ? `?${qs}` : ''}`);
    },
    status: () => get<import('./types').SingleResponse<import('./types').PMStatus>>('/api/v1/pm/status'),
  },
  sessions: {
    list: (params?: { limit?: number }) => {
      const searchParams = new URLSearchParams();
      if (params?.limit) searchParams.set('limit', String(params.limit));
      const qs = searchParams.toString();
      return get<import('./types').SessionsListResponse>(`/api/v1/sessions${qs ? `?${qs}` : ''}`);
    },
    get: (id: string) => get<import('./types').SingleResponse<import('./types').AgentSession>>(`/api/v1/sessions/${id}`),
    createManual: (body: { message: string; images?: string[]; agent_type?: string; autonomy_level?: string; token_mode?: string }) =>
      post<import('./types').SingleResponse<import('./types').AgentSession>>('/api/v1/sessions/manual', body),
  },
  settings: {
    get: () => get<import('./types').SingleResponse<import('./types').Organization>>('/api/v1/settings'),
    update: (data: Record<string, unknown>) => patch<import('./types').SingleResponse<import('./types').Organization>>('/api/v1/settings', data),
    getAgentDefaults: () => get<{ data: Record<string, Record<string, string>> }>('/api/v1/settings/agent-defaults'),
  },
  integrations: {
    list: () => get<import('./types').ListResponse<import('./types').Integration>>('/api/v1/integrations'),
    loginLinear: () => {
      window.location.href = `${API_BASE}/api/v1/integrations/linear/login`;
    },
    connectLinear: () => post<import('./types').SingleResponse<import('./types').Integration>>('/api/v1/integrations/linear/connect'),
  },
  codexAuth: {
    initiate: () => post<import('./types').SingleResponse<import('./types').CodexDeviceAuth>>('/api/v1/settings/codex-auth/initiate'),
    status: () => get<import('./types').SingleResponse<import('./types').CodexAuthStatus>>('/api/v1/settings/codex-auth/status'),
    disconnect: () => post('/api/v1/settings/codex-auth/disconnect'),
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
  reviewPatterns: {
    listByRepo: (repo: string, params?: { status?: string; cursor?: string }) => {
      const searchParams = new URLSearchParams();
      if (params?.status) searchParams.set('status', params.status);
      if (params?.cursor) searchParams.set('cursor', params.cursor);
      const qs = searchParams.toString();
      return get<import('./types').ListResponse<import('./types').ReviewPattern>>(`/api/v1/review-patterns/${repo}${qs ? `?${qs}` : ''}`);
    },
    updateStatus: (id: string, status: 'active' | 'dismissed') =>
      patch<import('./types').SingleResponse<import('./types').ReviewPattern>>(`/api/v1/review-patterns/${id}`, { status }),
    updateRule: (id: string, rule: string) => {
      return request<import('./types').SingleResponse<import('./types').ReviewPattern>>(`/api/v1/review-patterns/${id}`, {
        method: 'PUT',
        body: JSON.stringify({ rule }),
      });
    },
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
    list: (params?: { status?: string; cursor?: string; limit?: number }) => {
      const searchParams = new URLSearchParams();
      if (params?.status) searchParams.set('status', params.status);
      if (params?.cursor) searchParams.set('cursor', params.cursor);
      if (params?.limit) searchParams.set('limit', String(params.limit));
      const qs = searchParams.toString();
      return get<import('./types').ListResponse<import('./types').Project>>(`/api/v1/projects${qs ? `?${qs}` : ''}`);
    },
    get: (id: string) => get<import('./types').SingleResponse<import('./types').ProjectDetail>>(`/api/v1/projects/${id}`),
    create: (body: { title: string; goal: string; repository_id: string; scope?: string; completion_criteria?: string; execution_mode?: string; max_concurrent?: number; priority?: number; base_branch?: string }) =>
      post<import('./types').SingleResponse<import('./types').Project>>('/api/v1/projects', body),
    update: (id: string, body: Record<string, unknown>) =>
      patch<import('./types').SingleResponse<import('./types').Project>>(`/api/v1/projects/${id}`, body),
    del: (id: string) => del(`/api/v1/projects/${id}`),
    start: (id: string) => post(`/api/v1/projects/${id}/start`),
    pause: (id: string) => post(`/api/v1/projects/${id}/pause`),
    resume: (id: string) => post(`/api/v1/projects/${id}/resume`),
    approve: (id: string) => post(`/api/v1/projects/${id}/approve`),
    dismiss: (id: string) => post(`/api/v1/projects/${id}/dismiss`),
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

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

async function request<T>(path: string, options?: RequestInit): Promise<T> {
  const res = await fetch(`${API_BASE}${path}`, {
    ...options,
    credentials: 'include',
    headers: {
      'Content-Type': 'application/json',
      ...options?.headers,
    },
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
    getValidation: (runId: string) => get<import('./types').SingleResponse<import('./types').Validation>>(`/api/v1/runs/${runId}/validation`),
    getPR: (runId: string) => get<import('./types').SingleResponse<import('./types').PullRequest>>(`/api/v1/runs/${runId}/pr`),
    getQuestions: (runId: string) => get<import('./types').ListResponse<import('./types').AgentRunQuestion>>(`/api/v1/runs/${runId}/questions`),
    answerQuestion: (runId: string, questionId: string, answer: string) =>
      post<import('./types').SingleResponse<import('./types').AgentRunQuestion>>(`/api/v1/runs/${runId}/questions/${questionId}/answer`, { answer }),
  },
  settings: {
    get: () => get<import('./types').SingleResponse<import('./types').Organization>>('/api/v1/settings'),
    update: (data: Record<string, unknown>) => patch<import('./types').SingleResponse<import('./types').Organization>>('/api/v1/settings', data),
    getAgentDefaults: () => get<{ data: Record<string, Record<string, string>> }>('/api/v1/settings/agent-defaults'),
  },
  integrations: {
    list: () => get<import('./types').ListResponse<import('./types').Integration>>('/api/v1/integrations'),
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

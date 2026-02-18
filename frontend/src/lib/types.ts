export interface Organization {
  id: string;
  name: string;
  slug: string;
  settings: Record<string, unknown>;
  created_at: string;
  updated_at: string;
}

export interface User {
  id: string;
  org_id: string;
  email: string;
  name: string;
  role: string;
  github_id?: number;
  github_login?: string;
  avatar_url?: string;
  created_at: string;
}

export interface Repository {
  id: string;
  org_id: string;
  integration_id: string;
  github_id: number;
  full_name: string;
  default_branch: string;
  private: boolean;
  language?: string;
  description?: string;
  clone_url: string;
  installation_id: number;
  status: string;
  last_synced_at?: string;
  context_quality?: number;
  settings: Record<string, unknown>;
  created_at: string;
  updated_at: string;
}

export interface Integration {
  id: string;
  org_id: string;
  provider: string;
  status: string;
  last_synced_at?: string;
  created_at: string;
}

export interface Issue {
  id: string;
  org_id: string;
  external_id: string;
  source: string;
  source_integration_id?: string;
  repository_id?: string;
  title: string;
  description?: string;
  status: string;
  first_seen_at: string;
  last_seen_at: string;
  occurrence_count: number;
  affected_customer_count: number;
  severity: string;
  tags?: string[];
  fingerprint: string;
  created_at: string;
  updated_at: string;
}

export interface AgentRun {
  id: string;
  issue_id: string;
  org_id: string;
  agent_type: string;
  status: string;
  autonomy_level: string;
  token_mode: string;
  complexity_tier?: number;
  confidence_score?: number;
  confidence_reasoning?: string;
  risk_factors?: string[];
  started_at?: string;
  completed_at?: string;
  token_usage?: Record<string, unknown>;
  failure_explanation?: string;
  failure_category?: string;
  failure_next_steps?: string[];
  failure_retry_advised?: boolean;
  parent_run_id?: string;
  error?: string;
  result_summary?: string;
  diff?: string;
  created_at: string;
}

export interface ListResponse<T> {
  data: T[];
  meta: {
    next_cursor?: string;
  };
}

export interface SingleResponse<T> {
  data: T;
}

export interface ErrorResponse {
  error: {
    code: string;
    message: string;
    details?: unknown;
  };
}

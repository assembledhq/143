export interface Organization {
  id: string;
  name: string;
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
  google_id?: string;
  created_at: string;
}

export interface AuthProviders {
  github: boolean;
  google: boolean;
  email: boolean;
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
  priority_score?: number;
  priority_eligible?: boolean;
  complexity_tier?: number;
  complexity_label?: string;
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
  pm_plan_id?: string;
  pm_approach?: string;
  pm_reasoning?: string;
  error?: string;
  result_summary?: string;
  diff?: string;
  created_at: string;
}

export interface Validation {
  id: string;
  agent_run_id: string;
  org_id: string;
  status: string;
  direction_check: string | null;
  direction_check_details: string | null;
  correctness_check: string | null;
  correctness_check_details: string | null;
  quality_check: string | null;
  quality_check_details: string | null;
  security_scan: string | null;
  security_scan_details: string | null;
  regression_test_check: string | null;
  regression_test_check_details: string | null;
  ci_check: string | null;
  ci_check_details: string | null;
  created_at: string;
  updated_at: string;
}

export interface AgentRunLog {
  id: number;
  agent_run_id: string;
  level: string;
  message: string;
  metadata: Record<string, unknown> | null;
  created_at: string;
}

export interface AgentRunQuestion {
  id: string;
  agent_run_id: string;
  org_id: string;
  question_text: string;
  options: string[] | null;
  context: string | null;
  blocks_phase: string | null;
  status: string;
  answer_text: string | null;
  answered_at: string | null;
  answered_by: string | null;
  created_at: string;
}

export interface PullRequest {
  id: string;
  agent_run_id: string;
  org_id: string;
  github_pr_number: number;
  github_pr_url: string;
  github_repo: string;
  title: string;
  body: string;
  status: string;
  branch_name: string;
  review_status: string | null;
  merged_at: string | null;
  closed_at: string | null;
  created_at: string;
  updated_at: string;
}

export interface ReviewComment {
  id: string;
  pull_request_id: string;
  org_id: string;
  github_comment_id: number;
  reviewer: string;
  body: string;
  diff_path?: string;
  diff_position?: number;
  filter_status: string;
  category?: string;
  actionable: boolean;
  generalizable: boolean;
  generalized_rule?: string;
  summary?: string;
  applied: boolean;
  created_at: string;
}

export interface ReviewPattern {
  id: string;
  org_id: string;
  repo: string;
  rule: string;
  category: string;
  source_comment_ids: string[];
  occurrence_count: number;
  status: string;
  manually_curated: boolean;
  active: boolean;
  created_at: string;
}

export interface OrgSettings {
  autonomy_level?: 'manual' | 'auto_simple' | 'auto_all';
  execution_aggressiveness?: number;
  max_concurrent_runs?: number;
  pm_schedule_hours?: number;
  pm_model?: string;
  priority_weights?: {
    customer_impact?: number;
    severity?: number;
    recency?: number;
    revenue_risk?: number;
  };
  min_priority_threshold?: number;
  product_direction?: string;
  product_context?: ProductContext;
  agent_config?: Record<string, Record<string, string>>;
  default_agent_type?: 'codex' | 'claude_code' | 'gemini_cli';
}

export interface ProductContext {
  philosophy: string;
  direction: string;
  focus_areas?: string[];
  avoid_areas?: string[];
}

/** Per-repository PM agent overrides. All fields are optional — omitted means "inherit from org". */
export interface RepoPMSettings {
  product_context?: ProductContext;
  pm_schedule_hours?: number;
  pm_model?: string;
  priority_weights?: {
    customer_impact?: number;
    severity?: number;
    recency?: number;
    revenue_risk?: number;
  };
  min_priority_threshold?: number;
}

/** Strongly-typed repository settings JSONB. */
export interface RepoSettings {
  pm?: RepoPMSettings;
}

export interface PMTask {
  rank: number;
  issue_ids: string[];
  title: string;
  reasoning: string;
  approach: string;
  risk: string;
  complexity: string;
  confidence: string;
  agent_run_id?: string;
  status?: string;
}

export interface PMCluster {
  issue_ids: string[];
  root_cause: string;
  strategy: string;
}

export interface PMSkipEntry {
  issue_id: string;
  reason: string;
  detail: string;
}

export interface PMPlan {
  id: string;
  org_id: string;
  status: string;
  analysis: string;
  tasks: PMTask[];
  clusters: PMCluster[];
  skipped_issues: PMSkipEntry[];
  issues_reviewed: number;
  product_context_snapshot?: ProductContext;
  token_usage?: Record<string, unknown>;
  triggered_by: string;
  created_at: string;
  completed_at?: string;
}

export type AgentSessionType = 'plan' | 'manual';
export type AgentSessionStatus = 'active' | 'completed' | 'failed';
export type AgentSessionTriggeredBy = 'scheduled' | 'manual' | 'fix_this';
export type AgentRunStatus = 'pending' | 'running' | 'awaiting_input' | 'needs_human_guidance' | 'completed' | 'pr_created' | 'failed' | 'cancelled' | 'skipped';
export type PMTaskComplexity = 'trivial' | 'simple' | 'moderate' | 'complex';
export type PMTaskConfidence = 'high' | 'medium' | 'low';
export type PMTaskStatus = 'pending' | 'delegated' | 'skipped_capacity';

export interface AgentSessionTask {
  rank: number;
  title: string;
  issue_ids: string[];
  complexity?: PMTaskComplexity;
  confidence?: PMTaskConfidence;
  reasoning?: string;
  approach?: string;
  risk?: string;
  status?: PMTaskStatus;
  agent_run_id?: string;
  run_status?: AgentRunStatus;
  run_result_summary?: string;
  run_confidence_score?: number;
  run_started_at?: string;
  run_completed_at?: string;
}

export interface AgentSession {
  id: string;
  type: AgentSessionType;
  status: AgentSessionStatus;
  triggered_by: AgentSessionTriggeredBy;
  title: string;
  analysis?: string;
  tasks: AgentSessionTask[];
  clusters?: PMCluster[];
  skipped_issues?: PMSkipEntry[];
  issues_reviewed?: number;
  task_count: number;
  active_run_count: number;
  completed_run_count: number;
  failed_run_count: number;
  created_at: string;
  completed_at?: string;
}

export interface CodexAuthStatus {
  status: 'pending' | 'completed' | 'expired' | 'error' | 'none';
  account_type?: string;
  message?: string;
}

export interface CodexDeviceAuth {
  user_code: string;
  verification_uri: string;
  expires_in: number;
}

export interface InvitationResponse {
  id: string;
  email: string;
  role: string;
  status: string;
  invited_by: {
    id: string;
    name: string;
  };
  expires_at: string;
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

export interface PriorityScore {
  id: string;
  issue_id: string;
  org_id: string;
  score: number;
  customer_impact_score: number;
  severity_score: number;
  recency_score: number;
  revenue_risk_score: number;
  direction_alignment: number;
  factors?: Record<string, unknown>;
  eligible_for_agent: boolean;
  computed_at: string;
}

export interface ComplexityEstimate {
  id: string;
  issue_id: string;
  org_id: string;
  tier: number;
  label: string;
  confidence: number;
  issue_type?: string;
  reasoning?: string;
  estimated_files?: string[];
  estimated_tokens?: number;
  model_used?: string;
  computed_at: string;
  created_at: string;
}

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

export interface Session {
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
  parent_session_id?: string;
  pm_plan_id?: string;
  title?: string;
  pm_approach?: string;
  pm_reasoning?: string;
  project_task_id?: string;
  model_override?: string;
  triggered_by_user_id?: string;
  agent_session_id?: string;
  current_turn: number;
  last_activity_at?: string;
  sandbox_state: string;
  snapshot_key?: string;
  target_branch?: string;
  error?: string;
  result_summary?: string;
  diff?: string;
  created_at: string;
}

export interface Validation {
  id: string;
  session_id: string;
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

export type ThreadStatus = 'pending' | 'running' | 'idle' | 'awaiting_input' | 'completed' | 'failed' | 'cancelled';

export interface SessionThread {
  id: string;
  session_id: string;
  org_id: string;
  agent_type: string;
  model_override?: string;
  label: string;
  instructions?: string;
  file_scope?: string[];
  status: ThreadStatus;
  agent_session_id?: string;
  current_turn: number;
  last_activity_at?: string;
  confidence_score?: number;
  result_summary?: string;
  diff?: string;
  failure_explanation?: string;
  failure_category?: string;
  started_at?: string;
  completed_at?: string;
  created_at: string;
}

export interface SessionDetail extends Session {
  threads: SessionThread[];
}

export interface SessionLog {
  id: number;
  session_id: string;
  thread_id?: string;
  level: string;
  message: string;
  metadata: Record<string, unknown> | null;
  turn_number: number;
  created_at: string;
}

export interface SessionMessage {
  id: number;
  session_id: string;
  org_id: string;
  thread_id?: string;
  user_id?: string;
  turn_number: number;
  role: 'user' | 'assistant';
  content: string;
  attachments?: string[];
  token_usage?: Record<string, unknown>;
  created_at: string;
}

export interface SessionQuestion {
  id: string;
  session_id: string;
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
  session_id: string;
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

export interface Memory {
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
  scope: string;
  source: string;
  last_used_at?: string;
  times_reinforced: number;
  file_patterns?: string[];
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
  llm_model?: string;
  llm_reasoning_effort?: 'low' | 'medium' | 'high' | '';
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
  session_id?: string;
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

export type SessionStatus = 'pending' | 'running' | 'idle' | 'awaiting_input' | 'needs_human_guidance' | 'completed' | 'pr_created' | 'failed' | 'cancelled' | 'skipped';
export type PMTaskComplexity = 'trivial' | 'simple' | 'moderate' | 'complex';
export type PMTaskConfidence = 'high' | 'medium' | 'low';
export type PMTaskStatus = 'pending' | 'delegated' | 'skipped_capacity';

// PM Decision types for the decisions view
export type PMDecisionType = 'delegate' | 'skip' | 'cluster';
export type PMDecisionOutcome = 'succeeded' | 'failed' | 'still_open';

export interface PMDecisionView {
  id: string;
  plan_id: string;
  issue_id?: string;
  issue_title?: string;
  project_id?: string;
  project_title?: string;
  decision: PMDecisionType;
  reasoning: string;
  outcome?: PMDecisionOutcome;
  created_at: string;
}

export interface PMDecisionSummary {
  total_delegated: number;
  succeeded: number;
  failed: number;
  still_open: number;
}

export interface PMDecisionsResponse {
  data: PMDecisionView[];
  summary: PMDecisionSummary;
  meta: { next_cursor?: string };
}

export interface PMStatus {
  is_running: boolean;
  last_run_at?: string;
  last_run_status?: string;
  issues_reviewed: number;
  success_rate: number;
  success_count: number;
  total_delegated: number;
  next_run_in?: string;
  next_run_at?: string;
  last_error?: string;
  last_failed_at?: string;
}

export interface SessionsListResponse {
  data: Session[];
  meta: { next_cursor?: string };
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

export interface CredentialSummary {
  provider: string;
  configured: boolean;
  status?: string;
  masked_key?: string;
  last_verified_at?: string;
  api_type?: string;
  app_name?: string;
  app_id?: number;
  account_type?: string;
}

export interface UserCredentialSummary {
  provider: string;
  configured: boolean;
  is_team_default: boolean;
  masked_key?: string;
  set_by_user_id?: string;
  set_by_user_name?: string;
  status?: string;
  last_verified_at?: string;
}

export interface ResolvedCredential {
  provider: string;
  source: string;
  masked_key?: string;
}

export interface RepoSummary {
  repository_id: string;
  full_name: string;
  active_session_count: number;
  latest_session_status: string | null;
  active_project_count: number;
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

// Project types
export type ProjectStatus = 'proposed' | 'draft' | 'planning' | 'active' | 'paused' | 'completed' | 'cancelled';
export type ProjectExecMode = 'sequential' | 'parallel' | 'dependency_graph';
export type ProjectTaskStatus = 'pending' | 'blocked' | 'delegated' | 'running' | 'completed' | 'failed' | 'skipped' | 'cancelled';

export interface ApproachRecord {
  task_title: string;
  approach: string;
  outcome: string;
  lesson?: string;
}

export interface Project {
  id: string;
  org_id: string;
  repository_id: string;
  title: string;
  goal: string;
  scope?: string;
  completion_criteria?: string;
  status: ProjectStatus;
  priority: number;
  execution_mode: ProjectExecMode;
  max_concurrent: number;
  auto_merge: boolean;
  base_branch: string;
  current_phase?: string;
  lessons_learned?: string[];
  approach_history?: ApproachRecord[];
  total_tasks: number;
  completed_tasks: number;
  failed_tasks: number;
  proposed_by_pm: boolean;
  source_issue_ids?: string[];
  proposal_reasoning?: string;
  schedule_enabled: boolean;
  schedule_interval: number;
  schedule_unit: 'hours' | 'days' | 'weeks';
  next_run_at?: string;
  created_by?: string;
  created_at: string;
  updated_at: string;
  completed_at?: string;
}

export interface ProjectTask {
  id: string;
  project_id: string;
  org_id: string;
  title: string;
  description?: string;
  approach?: string;
  reasoning?: string;
  sort_order: number;
  depends_on?: string[];
  batch_number: number;
  status: ProjectTaskStatus;
  complexity?: string;
  confidence?: string;
  session_id?: string;
  issue_id?: string;
  branch_name?: string;
  pr_url?: string;
  outcome_notes?: string;
  retry_count: number;
  max_retries: number;
  created_at: string;
  updated_at: string;
  completed_at?: string;
}

export interface ProjectCycle {
  id: string;
  project_id: string;
  org_id: string;
  pm_plan_id?: string;
  cycle_number: number;
  analysis: string;
  decisions: Record<string, unknown>;
  progress_pct?: number;
  tasks_completed_this_cycle: number;
  tasks_failed_this_cycle: number;
  tasks_created_this_cycle: number;
  created_at: string;
}

export interface ProjectAttachment {
  id: string;
  project_id: string;
  org_id: string;
  file_name: string;
  file_url: string;
  file_type: string;
  thumbnail_url?: string;
  file_size?: number;
  category: string;
  caption?: string;
  sort_order: number;
  uploaded_by?: string;
  created_at: string;
  updated_at: string;
}

export interface ProjectSpec {
  id: string;
  project_id: string;
  org_id: string;
  title: string;
  content: string;
  spec_type: string;
  sort_order: number;
  version: number;
  created_by?: string;
  created_at: string;
  updated_at: string;
}

export interface PMDocument {
  id: string;
  org_id: string;
  title: string;
  content: string;
  doc_type: string;
  sort_order: number;
  source_type: string;
  source_url?: string;
  source_id?: string;
  source_meta?: Record<string, unknown>;
  last_synced_at?: string;
  created_by?: string;
  created_at: string;
  updated_at: string;
}

export interface AISuggestion {
  type: string;
  title: string;
  description: string;
  priority: string;
}

export interface GeneratedProject {
  title: string;
  goal: string;
  scope?: string;
  completion_criteria?: string;
  execution_mode: string;
}

export interface AIImprovementResponse {
  suggestions: AISuggestion[];
  summary: string;
}

// Audit log types
export type AuditActorType = 'user' | 'agent' | 'system' | 'webhook';
export type AuditResourceType = 'session' | 'project' | 'project_task' | 'issue' | 'pm_plan' | 'pm_decision' | 'settings' | 'team_member' | 'invitation' | 'integration' | 'credential' | 'user';

export interface AuditLog {
  id: number;
  org_id: string;
  actor_type: AuditActorType;
  actor_id: string;
  user_id?: string;
  action: string;
  resource_type: AuditResourceType;
  resource_id?: string;
  details?: Record<string, unknown>;
  request_id?: string;
  ip_address?: string;
  user_agent?: string;
  session_id?: string;
  project_id?: string;
  created_at: string;
}

export interface ProjectDetail {
  project: Project;
  tasks: ProjectTask[];
  recent_cycles: ProjectCycle[];
  attachments: ProjectAttachment[];
  specs: ProjectSpec[];
}

export const projectStatusConfig: Record<string, { color: string; label: string }> = {
  proposed: { color: "bg-purple-500/10 text-purple-700 dark:text-purple-400", label: "Proposed" },
  draft: { color: "bg-muted text-muted-foreground", label: "Draft" },
  planning: { color: "bg-yellow-500/10 text-yellow-700 dark:text-yellow-400", label: "Planning" },
  active: { color: "bg-blue-500/10 text-blue-700 dark:text-blue-400", label: "Active" },
  paused: { color: "bg-orange-500/10 text-orange-700 dark:text-orange-400", label: "Paused" },
  completed: { color: "bg-emerald-500/10 text-emerald-700 dark:text-emerald-400", label: "Completed" },
  cancelled: { color: "bg-red-500/10 text-red-700 dark:text-red-400", label: "Cancelled" },
};

export const projectStatusDotColor: Record<string, string> = {
  proposed: "bg-purple-500",
  draft: "bg-muted-foreground/50",
  planning: "bg-yellow-500",
  active: "bg-blue-500",
  paused: "bg-orange-500",
  completed: "bg-emerald-500",
  cancelled: "bg-red-500",
};

export interface Organization {
  id: string;
  name: string;
  settings: Record<string, unknown>;
  created_at: string;
  updated_at: string;
}

export interface MembershipSummary {
  org_id: string;
  org_name: string;
  role: string;
}

export interface MembershipsResponse {
  active_org_id: string;
  active_role: string;
  memberships: MembershipSummary[];
}

export interface ClaimInvitationResponse {
  org_id: string;
  role: string;
}

export interface OrganizationCreated {
  id: string;
  name: string;
  role: string;
  created_at: string;
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
  // Whether the account's current email is attested (OAuth provider claim,
  // verification link, or emailed-invite claim). Gates the "verify your
  // email" prompt and email-domain auto-join.
  email_verified?: boolean;
  settings?: UserSettings;
  created_at: string;
}

export interface UserSettings {
  coding_agent_model_default?: string;
  coding_agent_reasoning_defaults?: Partial<Record<"codex" | "claude_code", "low" | "medium" | "high" | "xhigh" | "max">>;
  diff_viewer_full_screen?: boolean;
}

export interface ThreadMessageWindowMeta {
  next_older_cursor?: string;
  has_older: boolean;
  next_newer_cursor?: string;
  has_newer?: boolean;
  anchor_message_id?: number;
  anchor_found?: boolean;
  latest_assistant_message_id?: number;
  live_edge_message_id?: number;
  window_position?: "latest" | "older" | "newer" | "around";
  thread_status: ThreadStatus;
}

export interface ThreadMessageWindowResponse {
  data: SessionMessage[];
  meta: ThreadMessageWindowMeta;
}

// PATCH /api/v1/auth/me/settings is an RFC 7386 JSON merge patch: omitted
// fields keep their stored value, null clears a field, and nested objects
// merge per key. Send only the fields being changed — never a full settings
// document rebuilt from the query cache, which would clobber concurrent
// edits from other tabs.
export interface UserSettingsUpdateRequest {
  coding_agent_model_default?: string | null;
  coding_agent_reasoning_defaults?: Partial<Record<"codex" | "claude_code", "low" | "medium" | "high" | "xhigh" | "max" | null>> | null;
  diff_viewer_full_screen?: boolean | null;
}

export interface AuthProviders {
  github: boolean;
  google: boolean;
  email: boolean;
  demo?: boolean;
  demo_email?: string;
  demo_password?: string;
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

export interface PreviewSecretBundleSource {
  type: "managed";
  values: Record<string, string>;
}

export interface PreviewSecretBundleOutput {
  type: "env" | "file";
  values?: Record<string, string>;
  path?: string;
  format?: "env" | "json" | "raw";
  mode?: string;
  content?: unknown;
  value?: string;
}

export interface PreviewSecretOutputSummary {
  type: string;
  env?: string[];
  path?: string;
  format?: string;
}

export interface PreviewSecretBundleSummary {
  id: string;
  repository_id: string;
  name: string;
  source_type: string;
  exposure_policy: string;
  outputs: PreviewSecretOutputSummary[];
  created_by_user_id: string;
  created_at: string;
}

export interface PreviewSecretBundleUpsertRequest {
  name: string;
  source: PreviewSecretBundleSource;
  outputs: PreviewSecretBundleOutput[];
  exposure_policy?: "preview_runtime";
}

export interface PreviewSecretBundlePatchRequest {
  name?: string;
  source?: PreviewSecretBundleSource;
  outputs?: PreviewSecretBundleOutput[];
  exposure_policy?: "preview_runtime";
}

export interface PreviewSecretBundleTestResult {
  status: "ready" | "failed";
  bundle: PreviewSecretBundleSummary;
  error?: string;
}

export interface PreviewSecretBundleRevealResult {
  bundle: PreviewSecretBundleSummary;
  source: PreviewSecretBundleSource;
  outputs: PreviewSecretBundleOutput[];
}

export interface BranchPreviewCreateRequest {
  repository_id: string;
  branch: string;
  commit_sha?: string;
  preview_config_name?: string | null;
  source?: {
    type: "api" | "manual" | "session" | "pull_request" | "automation";
    external_id?: string;
    url?: string;
  };
  ttl_seconds?: number;
}

export interface BranchPreviewResponse {
  target_id: string;
  preview_id?: string;
  repository_id?: string;
  repository_full_name?: string;
  branch?: string;
  commit_sha?: string;
  preview_config_name?: string;
  source_type?: "api" | "manual" | "session" | "pull_request" | "automation";
  source_url?: string;
  status: string;
  error?: string;
  current_phase?: string;
  phase_steps?: { name: string; status: string }[];
  created_by_user_id?: string;
  created_at?: string;
  source_id?: string;
  request_id?: string;
  new_commits_available?: boolean;
  latest_commit_sha?: string;
  github_branch_url?: string;
  pull_request_url?: string;
  stable_url: string;
  preview_url?: string;
  expires_at?: string;
  stopped_at?: string;
  stopped_reason?: "" | "user" | "expired" | "warm_policy" | "pr_closed" | "drain" | "error";
  resumable?: boolean;
  resume_estimate_seconds?: number;
  services?: import('./preview-types').PreviewService[];
  infrastructure?: import('./preview-types').PreviewInfrastructure[];
  logs?: import('./preview-types').PreviewLog[];
}

export interface PreviewListMeta {
  next_cursor?: string;
  counts?: { running: number; resumable: number; recent: number };
  pool?: { auto_active: number; auto_max: number; user_active: number; user_max: number };
}

export interface PreviewPolicySummary {
  repository_id: string;
  repository_full_name: string;
  auto_mode: "off" | "warm" | "on";
  open_pr_count: number;
  updated_at?: string;
}

export interface BranchPreviewConfigOptions {
  repository_id: string;
  repository_full_name: string;
  ref: string;
  preview_config_name?: string;
  names: string[];
  default_name?: string;
  selected_name?: string;
  requires_selection: boolean;
  readiness: string;
  validation_errors?: string[];
}

export interface PreviewAPIToken {
  id: string;
  org_id: string;
  name: string;
  scopes: string[];
  repository_ids: string[];
  created_by_user_id: string;
  last_used_at?: string;
  revoked_at?: string;
  created_at: string;
}

export interface Integration {
  id: string;
  org_id: string;
  provider: string;
  github_app_installed?: boolean;
  github_installation_id?: number;
  github_account_login?: string;
  github_repo_selection_required?: boolean;
  github_active_repo_count?: number;
  notion_workspace_id?: string;
  notion_workspace_name?: string;
  circleci_project_slug?: string;
  mezmo_dataset?: string;
  mezmo_base_url?: string;
  /**
   * Surfaced by the backend when a provider rejects our access token (e.g.
   * Linear returns 401). Populated by deriveIntegrationStatus on the server
   * — when present, the integrations settings card renders an amber banner
   * with a Reconnect CTA. The reason field is a controlled string from the
   * backend; never render arbitrary provider responses through this surface.
   */
  auth_error?: {
    reason: string;
    at: string;
  };
  status: string;
  last_synced_at?: string;
  created_at: string;
}

export type GitHubRepositoryClaimStatus =
  | "unclaimed"
  | "owned_by_current_org"
  | "owned_by_other_org"
  | "disconnected_in_current_org";

export interface GitHubRepositoryClaimCandidate {
  github_id: number;
  full_name: string;
  default_branch: string;
  private: boolean;
  clone_url: string;
  installation_id: number;
  status: GitHubRepositoryClaimStatus;
  repository_id?: string;
  owner_org_id?: string;
  owner_org_name?: string;
  can_transfer: boolean;
}

export interface LinearAgentStatus {
  enabled: boolean;
  agent_scopes_granted: boolean;
  app_user_name?: string;
  has_linear_integration: boolean;
  default_repo_id?: string;
  available_teams?: LinearTeamKey[];
}

export interface LinearTeamKey {
  org_id: string;
  integration_id: string;
  workspace_id: string;
  team_id: string;
  team_key: string;
  team_name: string;
  refreshed_at: string;
}

export interface LinearTeamRepoMapping {
  id: string;
  org_id: string;
  linear_team_id: string;
  linear_project_id?: string;
  repository_id: string;
  default_branch?: string;
  priority: number;
  created_at: string;
  updated_at: string;
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

export type AutopilotRunState =
  | 'not_started'
  | 'queued'
  | 'running'
  | 'awaiting_input'
  | 'needs_review'
  | 'pr_open'
  | 'merged'
  | 'failed'
  | 'skipped';

export type PullRequestStatus = 'open' | 'closed' | 'merged';
export type PullRequestReviewStatus = 'pending' | 'approved' | 'changes_requested';
export type PullRequestCIStatus = '' | 'success' | 'failure' | 'pending';

export type AutopilotQueueAction =
  | 'start_run'
  | 'view_run'
  | 'review'
  | 'open_pr'
  | 'retry'
  | 'blocked';

export interface AutopilotQueueRow {
  id: string;
  rank: number;
  source: { type: string; key: string };
  title: string;
  issue_url?: string;
  repo?: { id: string; name: string };
  issue_status: string;
  customer_impact: { label: string; count: number };
  implementation_ease: string;
  low_hanging_fruit: {
    label: string;
    reasons: string[];
    cluster_size: number;
  };
  display_run_state: AutopilotRunState;
  latest_session?: {
    id: string;
    title: string;
    updated_at: string;
  };
  latest_agent_run?: {
    id: string;
    status: string;
    trigger_mode: 'auto' | 'manual';
    started_at?: string;
  };
  latest_pr?: {
    id: string;
    number: number;
    url: string;
    status: PullRequestStatus;
    merged_at?: string;
  };
  available_action: AutopilotQueueAction;
  action_disabled_reason?: string | null;
}

export interface AutopilotQueueSummary {
  top_issue_id?: string;
  autorunnable_count: number;
  needs_review_count: number;
  open_pr_count: number;
  active_run_count: number;
  ranked_issue_count: number;
  analyzed_at?: string;
}

export interface AutopilotQueueResponse {
  data: AutopilotQueueRow[];
  meta: {
    next_cursor?: string;
    summary: AutopilotQueueSummary;
  };
}

export interface Session {
  id: string;
  primary_issue_id?: string | null;
  org_id: string;
  origin?: string;
  interaction_mode?: string;
  agent_type: string;
  status: SessionStatus;
  autonomy_level: string;
  token_mode: string;
  complexity_tier?: number;
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
  reasoning_effort?: 'low' | 'medium' | 'high' | 'xhigh' | 'max';
  triggered_by_user_id?: string;
  agent_session_id?: string;
  current_turn: number;
  last_activity_at: string;
  sandbox_state: string;
  snapshot_key?: string;
  recovery_state?: '' | 'queued' | 'recovering' | 'unavailable';
  recovery_queued_at?: string;
  recovery_started_at?: string;
  recovery_attempt_count?: number;
  pr_creation_state?: "idle" | "queued" | "pushing" | "succeeded" | "failed";
  pr_creation_error?: string;
  pr_push_state?: "idle" | "queued" | "pushing" | "succeeded" | "failed";
  pr_push_error?: string;
  branch_creation_state?: "idle" | "queued" | "pushing" | "succeeded" | "failed";
  branch_creation_error?: string;
  branch_url?: string;
  has_unpushed_changes?: boolean;
  target_branch?: string;
  working_branch?: string;
  repository_id?: string;
  linked_issues?: Array<{
    id: string;
    session_id: string;
    issue_id: string;
    role: string;
    position: number;
    issue_title?: string;
    issue_source?: string;
    external_id?: string;
    issue_status?: string;
    // Linear workspace slug (e.g. "acs"). Used to build deep links to
    // linear.app/<slug>/issue/<KEY>. Empty/undefined for non-Linear links.
    issue_workspace_slug?: string;
    // Latest backend-recorded reason a Linear state sync was skipped for
    // this link (if any). Used by the session detail debug chip.
    linear_last_skipped_reason?: string;
  }>;
  // Linear-specific session policy flags. Frozen at session create.
  linear_private?: boolean;
  linear_state_sync_disabled?: boolean;
  linear_identifier_hint?: string;
  // linear_prepare_state is the server-side gate that blocks turn 1 until
  // the primary Linear issue snapshot is captured. The backend emits it on
  // every session payload. The 'failed' state surfaces in
  // linked-issue-chips.tsx as a warning chip so dogfooders see the
  // missing-context signal; 'pending'/'ready' are not yet rendered (the
  // "Preparing Linear context..." indicator is one diff away when we want
  // it).
  linear_prepare_state?: 'none' | 'pending' | 'ready' | 'failed';
  error?: string;
  result_summary?: string;
  runtime_stop_reason?: string;
  runtime_graceful_stop_at?: string;
  diff?: string;
  diff_stats?: { added: number; removed: number; files_changed: number };
  diff_history?: Array<{ pass: number; diff: string; diff_stats: { added: number; removed: number; files_changed: number }; created_at: string }>;
  diff_collected_at?: string;
  latest_diff_snapshot_id?: string;
  workspace_revision?: number;
  workspace_revision_updated_at?: string;
  threads?: SessionThread[];
  archived_at?: string;
  archived_by_user_id?: string;
  automation_run_id?: string;
  created_at: string;
}

export type SessionRetryMode = 'checkpoint' | 'start_over';

export interface RetrySessionRequest {
  mode?: SessionRetryMode;
}

export interface PRSummary {
  status: PullRequestStatus;
  ci_status: PullRequestCIStatus;
  number: number;
  url: string;
}

export interface SessionListItem extends Session {
  last_viewed_at?: string;
  pr_summary?: PRSummary;
}

export type ThreadStatus = 'pending' | 'running' | 'idle' | 'awaiting_input' | 'completed' | 'failed' | 'cancelled';

export type ThreadInboxSummaryState = 'idle' | 'pending' | 'delivering' | 'delivered' | 'unknown_delivery' | 'acked' | 'dead_letter';

export interface ThreadInboxDeliverySummary {
  thread_id: string;
  state: ThreadInboxSummaryState;
  pending_count: number;
  delivering_count: number;
  delivered_count: number;
  unknown_delivery_count: number;
  acked_count: number;
  dead_letter_count: number;
  last_sequence_no: number;
  last_accepted_at?: string;
  last_delivered_at?: string;
  last_acked_at?: string;
  last_error?: string;
}

export type ThreadInboxEntryType = 'user_message' | 'human_input_answer' | 'control';
// '' is emitted by the API when no inbox entry was created (deployment with the
// inbox unwired), keeping the SendThreadMessageResponse delivery_state field
// total without lying about confirmed delivery.
export type ThreadInboxDeliveryState = '' | 'pending' | 'delivering' | 'delivered' | 'unknown_delivery' | 'acked' | 'dead_letter';

export interface ThreadInboxEntry {
  id: string;
  org_id: string;
  session_id: string;
  thread_id: string;
  sequence_no: number;
  message_id?: number;
  client_message_id?: string;
  entry_type: ThreadInboxEntryType;
  payload: unknown;
  delivery_state: ThreadInboxDeliveryState;
  delivery_attempts: number;
  last_error?: string;
  owner_node_id?: string;
  runtime_id?: string;
  accepted_at: string;
  delivered_at?: string;
  acked_at?: string;
  created_at: string;
  updated_at?: string;
}

export interface SendThreadMessageResponse {
  message: SessionMessage;
  inbox_entry?: ThreadInboxEntry;
  thread_status: ThreadStatus;
  delivery_state: ThreadInboxDeliveryState;
}

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
  result_summary?: string;
  diff?: string;
  failure_explanation?: string;
  failure_category?: string;
  started_at?: string;
  completed_at?: string;
  created_at: string;
  created_by_source?: 'user' | 'agent_tool' | 'system';
  created_by_thread_id?: string;
  archived_at?: string;
  base_snapshot_key?: string;
  cost_cents: number;
  pending_message_count: number;
  cancel_requested_at?: string;
  inbox_delivery?: ThreadInboxDeliverySummary;
}

export interface ThreadInboxEvent {
  session_id: string;
  thread_id: string;
  org_id: string;
  pending_message_count: number;
}

export interface ThreadRuntimeEvent {
  session_id: string;
  thread_id: string;
  org_id: string;
  status: ThreadStatus;
  agent_session_id?: string;
  current_turn: number;
  pending_message_count: number;
  last_activity_at?: string;
  started_at?: string;
  completed_at?: string;
}

export interface SessionWorkspaceGenerationChangedEvent {
  session_id: string;
  org_id: string;
  workspace_revision: number;
  workspace_revision_updated_at: string;
  reason?: string;
}

export interface SessionThreadFileEvent {
  id: number;
  org_id: string;
  session_id: string;
  thread_id?: string;
  turn: number;
  path: string;
  event_type: 'created' | 'modified' | 'deleted';
  before_hash?: string;
  after_hash?: string;
  observed_at: string;
}

export type ReviewLoopStatus = 'running' | 'clean' | 'needs_human_decision' | 'failed' | 'cancelled';
export type ReviewLoopSource = 'manual' | 'automation';
export type ReviewLoopFixMode = 'minimal' | 'exhaustive';
export type ReviewLoopPassStatus = 'reviewing' | 'deciding' | 'fixing' | 'clean' | 'needs_fix' | 'failed';
export type ReviewLoopDecision = 'REVIEW_CLEAN' | 'NEEDS_FIX_PASS';

export interface SessionReviewLoop {
  id: string;
  org_id: string;
  session_id: string;
  automation_run_id?: string;
  thread_id?: string;
  status: ReviewLoopStatus;
  source: ReviewLoopSource;
  agent_type: string;
  max_passes: number;
  fix_mode: ReviewLoopFixMode;
  completed_passes: number;
  review_required: boolean;
  bypassed_by_user_id?: string;
  bypass_reason?: string;
  loop_start_checkpoint_key?: string;
  latest_checkpoint_key?: string;
  latest_summary?: string;
  started_by_user_id?: string;
  started_at: string;
  completed_at?: string;
  passes?: SessionReviewLoopPass[];
}

export interface SessionReviewLoopPass {
  id: string;
  org_id: string;
  loop_id: string;
  session_id: string;
  pass_index: number;
  review_message_id?: number;
  decision_message_id?: number;
  fix_message_id?: number;
  status: ReviewLoopPassStatus;
  agent_decision?: ReviewLoopDecision;
  review_output?: string;
  fix_summary?: string;
  review_started_at?: string;
  review_completed_at?: string;
  fix_started_at?: string;
  fix_completed_at?: string;
  summary?: string;
}

export interface ForkResult {
  job_id: string;
}

export interface SessionDetail extends Session {
  threads: SessionThread[];
}

export interface SessionDiff {
  session_id: string;
  diff?: string;
  diff_stats?: { added: number; removed: number; files_changed: number };
  diff_history?: Array<{ pass: number; diff: string; diff_stats: { added: number; removed: number; files_changed: number }; created_at: string }>;
  diff_truncated: boolean;
  diff_history_truncated: boolean;
  diff_chars?: number;
  diff_history_bytes?: number;
  diff_max_chars?: number;
  diff_history_max_bytes?: number;
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

export interface SessionTimelineEntry {
  kind: 'message' | 'assistant_output' | 'tool_group' | 'error' | 'log' | 'plan_output' | 'plan_message' | 'human_input';
  created_at: string;
  message?: SessionMessage;
  log?: SessionLog;
  tool_use?: SessionLog;
  tool_result?: SessionLog;
  human_input_request?: HumanInputRequest;
  turn_number?: number;
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
  references?: SessionInputReference[];
  commands?: SessionInputCommand[];
  token_usage?: Record<string, unknown>;
  source?: 'agent_tool';
  created_at: string;
}

export type SessionInputReferenceKind = "file" | "directory" | "app" | "plugin";

export interface SessionInputReference {
  kind: SessionInputReferenceKind;
  token?: string;
  path?: string;
  id?: string;
  display: string;
}

export type SessionComposerAgentType = "claude_code" | "codex" | "gemini_cli" | "amp" | "pi";

export type SessionInputCommandSource = "builtin" | "project";

export interface SessionInputCommand {
  kind: "command";
  agent_type: SessionComposerAgentType;
  name: string;
  token: string;
  display: string;
  description?: string;
  arguments?: string;
  source?: SessionInputCommandSource;
}

export interface SlashCommandGroup {
  source: SessionInputCommandSource;
  label: string;
  items: SessionInputCommand[];
}

export interface SlashCommandListResponse {
  groups: SlashCommandGroup[];
}

export interface SlashCommandDetailResponse {
  command: SessionInputCommand;
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

export type HumanInputRequestKind = "free_text" | "single_choice" | "multi_choice" | "tool_approval" | "action_choice";
export type HumanInputRequestStatus = "pending" | "answered" | "cancelled" | "expired" | "superseded";

export interface HumanInputChoice {
  id: string;
  label: string;
  description?: string;
  preview?: string;
  kind?: string;
  destructive?: boolean;
}

export interface HumanInputRequest {
  id: string;
  org_id: string;
  session_id: string;
  thread_id?: string | null;
  turn_number: number;
  agent_type: string;
  provider_request_id?: string | null;
  request_kind: HumanInputRequestKind;
  status: HumanInputRequestStatus;
  title: string;
  body: string;
  context?: string | null;
  blocks_phase?: string | null;
  choices: HumanInputChoice[];
  response_schema?: unknown;
  provider_payload?: unknown;
  answer_text?: string | null;
  answer_payload?: unknown;
  answered_by?: string | null;
  answered_at?: string | null;
  expires_at?: string | null;
  created_at: string;
}

export interface HumanInputAnswerBody {
  answer_text?: string;
  selected_choice_ids?: string[];
  answer_payload?: unknown;
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
  status: PullRequestStatus;
  branch_name: string;
  review_status: PullRequestReviewStatus | null;
  ci_status: PullRequestCIStatus;
  merged_at: string | null;
  closed_at: string | null;
  created_at: string;
  updated_at: string;
}

export interface PullRequestCheckSummary {
  name: string;
  category: "test" | "lint" | "build" | "deploy" | "unknown";
  status: "passed" | "failed" | "pending";
  provider?: string;
  details_url?: string;
  summary?: string;
}

export interface PullRequestActiveRepair {
  action_type: "fix_tests" | "resolve_conflicts";
  session_id: string;
  thread_id?: string;
  session_status: SessionStatus;
  health_version: number;
}

export interface PullRequestRepairRequest {
  thread_id?: string;
}

export type PullRequestMergeWhenReadyState =
  | "off"
  | "queued"
  | "merging"
  | "succeeded"
  | "failed"
  | "cancelled";

export interface PullRequestMergeWhenReadyStatus {
  state: PullRequestMergeWhenReadyState;
  requested_by_user_id?: string;
  requested_at?: string;
  requested_head_sha?: string;
  requested_health_version?: number;
  last_error?: string;
}

export interface PullRequestHealthResponse {
  pull_request_id: string;
  pull_request_number: number;
  repository: string;
  url: string;
  status: PullRequestStatus;
  head_sha: string;
  base_sha: string;
  health_version: number;
  merge_state: "unknown" | "mergeability_pending" | "clean" | "conflicted" | "behind" | "blocked";
  has_conflicts: boolean;
  failing_test_count: number;
  needs_agent_action: boolean;
  github_state_synced_at?: string;
  summary: string;
  checks?: PullRequestCheckSummary[];
  checks_confirmed: boolean;
  can_resolve_conflicts: boolean;
  can_fix_tests: boolean;
  can_merge: boolean;
  active_repairs?: PullRequestActiveRepair[];
  enrichment_status: "not_requested" | "pending" | "ready" | "failed" | "stale";
  enrichment_requested: boolean;
  enrichment_ready: boolean;
  conflict_detail_available: boolean;
  failing_test_detail_available: boolean;
  obsolete_active_repair_sessions?: boolean;
  merge_when_ready: PullRequestMergeWhenReadyStatus;
}

export interface PullRequestRepairResponse {
  session_id: string;
  thread_id?: string;
  mode: "existing" | "resumed" | "reconstructed";
  reused_in_flight: boolean;
  head_sha: string;
  base_sha: string;
  health_version: number;
  repair_action_type: "fix_tests" | "resolve_conflicts";
}

export interface PullRequestMergeResponse {
  merged: boolean;
  sha: string;
  message: string;
  merge_method: "merge" | "squash" | "rebase";
}

export interface PullRequestUpdatedEvent {
  pull_request_id: string;
  version: number;
  head_sha: string;
  base_sha: string;
  synced_at: string;
}

export interface SessionReviewComment {
  id: string;
  session_id: string;
  org_id: string;
  user_id: string;
  file_path: string;
  line_number: number;
  diff_side: 'old' | 'new';
  body: string;
  resolved: boolean;
  resolved_at?: string;
  resolved_by_pass?: number;
  pass_number: number;
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
  max_session_duration_seconds?: number;
  preview_max_previews_per_user?: number;
  preview_auto_pool_max_active?: number;
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
  llm_reasoning_effort?: 'low' | 'medium' | 'high' | 'xhigh' | 'max' | '';
  agent_config?: Record<string, Record<string, string>>;
  default_agent_type?: 'codex' | 'claude_code' | 'gemini_cli' | 'amp' | 'pi';
  pr_authorship?: 'user_preferred' | 'app_only' | 'user_required';
  pr_draft_default?: boolean;
  auto_archive_on_pr_close?: boolean;
  coding_agent_tab_tools_enabled?: boolean;
  builder_permissions?: {
    require_review_before_pr?: boolean;
  };
  sandbox_network?: {
    static_egress_enabled?: boolean;
  };
  sandbox_lifecycle?: {
    completed_session_retention_minutes?: number;
    idle_preview_ttl_minutes?: number;
    preview_holds_sandbox?: boolean;
  };
  sandbox_resources?: {
    agent_default_tier?: SandboxResourceTier;
    preview_default_tier?: SandboxResourceTier;
    allow_repo_resource_requests?: boolean;
    preview_max_tier?: SandboxResourceTier;
    preview_max_cpu_millis?: number;
    preview_max_memory_mib?: number;
    preview_max_ephemeral_disk_mib?: number;
  };
}

export type SandboxResourceTier = 'small' | 'standard' | 'large';

export interface NetworkSettingsStatus {
  static_egress_available: boolean;
  static_egress_enabled: boolean;
  static_egress_public_ip?: string;
  static_egress_unavailable_reason?: string;
}

export interface RuntimeSettingsStatus {
  static_egress: {
    available: boolean;
    enabled: boolean;
    public_ip?: string;
  };
  capacity: {
    state: 'normal' | 'limited';
    active_agent_runs: number;
    max_concurrent_agent_runs: number;
    active_previews: number;
    max_previews_per_user: number;
  };
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

// Presentation-friendly recommendation from /api/v1/pm/current
export interface PMCurrentRecommendation {
  analysis: string;
  tasks: PMTask[];
  clusters: PMCluster[];
  skipped_issues: PMSkipEntry[];
  context_stats: PMContextStats;
  decision_summary: PMDecisionSummary;
  analyzed_at: string;
  completed_at?: string;
  status: string;
  triggered_by: string;
}

export interface PMContextStats {
  issues_reviewed: number;
  in_flight_runs_checked: number;
  past_outcomes_reviewed: number;
  recent_prs_checked: number;
  past_decisions_reviewed: number;
  commits_analyzed: number;
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
  last_failed_session_id?: string;
}

export interface SessionsListResponse {
  data: Session[];
  meta: { next_cursor?: string };
}

// SessionCounts comes from /api/v1/sessions/counts. Each bucket is capped at
// `cap` server-side, so values equal to `cap` should be rendered as e.g. "99+".
export interface SessionCounts {
  all: number;
  active: number;
  archived: number;
  cap: number;
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

export type CodexSubscriptionStatus = 'active' | 'pending_auth' | 'invalid' | 'disabled';

export interface CodexSubscription {
  id: string;
  label: string;
  account_type?: string;
  status: CodexSubscriptionStatus;
  last_used_at?: string;
  created_by?: string;
  created_at?: string;
}

export interface ClaudeCodeInitiateResponse {
  authorize_url: string;
  state: string;
}

export interface ClaudeCodeCompleteResponse {
  account_type?: string;
}

export type ClaudeCodeSubscriptionStatus = 'active' | 'pending_auth' | 'invalid' | 'disabled';

export interface ClaudeCodeSubscription {
  id: string;
  label: string;
  account_type?: string;
  status: ClaudeCodeSubscriptionStatus;
  last_used_at?: string;
  created_by?: string;
  created_at?: string;
}

export interface InvitationResponse {
  id: string;
  email?: string | null;
  github_username?: string | null;
  acceptance_method: 'email' | 'github' | 'either';
  role: string;
  status: string;
  invited_by: {
    id: string;
    name: string;
  };
  expires_at: string;
  created_at: string;
}

// JoinToken is a multi-use, revocable org join link backing the CLI install
// one-liner (`curl .../install/<token> | sh`). Only the display prefix is
// ever returned after creation.
export interface JoinToken {
  id: string;
  token_prefix: string;
  name: string;
  role: string;
  max_uses?: number | null;
  use_count: number;
  expires_at?: string | null;
  status: 'active' | 'revoked' | 'expired' | 'exhausted';
  created_at: string;
}

// CreatedJoinToken is the create response: the plaintext token and the
// ready-to-paste install command, shown exactly once.
export interface CreatedJoinToken {
  id: string;
  token: string;
  token_prefix: string;
  role: string;
  name: string;
  expires_at?: string | null;
  max_uses?: number | null;
  install_command: string;
}

// CliToken is one row in the user's own "CLI sessions" list — a per-device
// credential minted by `143-tools login`.
export interface CliToken {
  id: string;
  token_prefix: string;
  device_name: string;
  expires_at: string;
  last_used_at?: string | null;
  last_used_ip?: string | null;
  created_at: string;
}

// PendingInvitationForUser is the invitee-facing shape: the recipient of an
// invitation only needs to know which org they're being invited into, by whom,
// at what role, and when it expires. The token is intentionally omitted —
// accept/decline are id-routed and re-validated server-side.
export interface PendingInvitationForUser {
  id: string;
  org_id: string;
  org_name: string;
  role: string;
  invited_by: {
    id: string;
    name: string;
  };
  expires_at: string;
  created_at: string;
}

export type OrgDomainStatus = 'pending' | 'verified';

// OrganizationDomain is one verified-domain row from /api/v1/team/domains.
// The server decorates the row with the exact DNS TXT record to publish
// (dns_record_name / dns_record_value) so the UI never reconstructs the
// format itself.
export interface OrganizationDomain {
  id: string;
  org_id: string;
  domain: string;
  verification_token: string;
  status: OrgDomainStatus;
  auto_join_enabled: boolean;
  created_at: string;
  verified_at?: string | null;
  last_checked_at?: string | null;
  failed_checks: number;
  dns_record_name: string;
  dns_record_value: string;
}

// JoinableOrganization is a workspace the current user may join because
// their provider-verified email domain matches the org's verified
// auto-join domain.
export interface JoinableOrganization {
  org_id: string;
  org_name: string;
  domain: string;
}

// JoinableOrgsResponse wraps the joinable list with the hint that the
// user's domain IS captured but their email isn't verified yet — the org
// identity stays hidden until they prove the address.
export interface JoinableOrgsResponse {
  data: JoinableOrganization[];
  email_verification_required: boolean;
}

// ConfirmEmailVerificationResponse is the verify-email confirm payload.
export interface ConfirmEmailVerificationResponse {
  verified: boolean;
  joined_org?: JoinableOrganization | null;
}

export interface GitHubInviteStatus {
  connected: boolean;
}

export interface GitHubUserSuggestion {
  login: string;
  avatar_url?: string;
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

// ResolvedCredential is a provider-keyed view of the caller's effective
// credentials, derived from the unified coding-credentials resolved stack.
// "personal" rows belong to the requesting user, "org" rows are the shared
// fallback; "none" marks a provider with no usable credential. The legacy
// "team_default" source is gone — org-scoped credentials fill that role.
export interface ResolvedCredential {
  provider: string;
  source: "personal" | "org" | "none";
  masked_key?: string;
}

export type CodingAuthAgent = "codex" | "claude_code" | "gemini_cli" | "amp" | "pi";
export type CodingAuthType = "subscription" | "api_key";
export type CodingAuthStatus = "healthy" | "rate_limited" | "needs_reauth" | "invalid";

// CodingCredentialScope is the scope dimension of the unified
// coding-credentials API: "org" rows are visible to every member of the org as
// a fallback; "personal" rows belong to the requesting user only and run ahead
// of any org row in the resolver.
export type CodingCredentialScope = "org" | "personal";

// CodingCredentialSummary is the on-the-wire representation of a row from the
// unified coding_credentials table. Mirrors models.CodingCredentialSummary.
export interface CodingCredentialSummary {
  id: string;
  org_id: string;
  user_id?: string;
  scope: CodingCredentialScope;
  priority: number;
  agent: CodingAuthAgent;
  auth_type: CodingAuthType;
  provider: string;
  label: string;
  status: CodingAuthStatus;
  is_default: boolean;
  usage_note?: string;
  last_verified_at?: string;
  rate_limited_until?: string;
  rate_limit_message?: string;
  created_by?: string;
  created_at: string;
  updated_at: string;
}

export interface RepoSummary {
  repository_id: string;
  full_name: string;
  active_session_count: number;
  latest_session_status: SessionStatus | null;
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
export type ProjectStatus = 'draft' | 'active' | 'completed';
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
  similar_projects?: ProposalOverlap[];
  created_by?: string;
  created_at: string;
  updated_at: string;
  completed_at?: string;
  archived_at?: string;
}

export interface ProposalOverlap {
  project_id: string;
  title: string;
  overlap_score: number;
  overlap_type: string;
  explanation: string;
}

export interface ProposalSummary {
  count: number;
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
export type AuditResourceType = 'session' | 'project' | 'project_task' | 'automation' | 'issue' | 'pm_plan' | 'pm_decision' | 'settings' | 'team_member' | 'invitation' | 'integration' | 'credential' | 'user';

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
  draft: { color: "bg-muted text-muted-foreground", label: "Draft" },
  active: { color: "bg-info/10 text-info", label: "Active" },
  completed: { color: "bg-success/10 text-success", label: "Done" },
};

export const projectStatusDotColor: Record<string, string> = {
  draft: "bg-muted-foreground/50",
  active: "bg-info",
  completed: "bg-success",
};

// --- Session file browsing types ---

export interface FileEntry {
  path: string;
  type: 'file' | 'dir';
  size: number;
}

export interface FileContent {
  path: string;
  content: string;
  language: string;
  truncated: boolean;
}

export interface FileLine {
  number: number;
  content: string;
}

export interface FileContextResponse {
  lines: FileLine[];
  start_line: number;
  end_line: number;
  has_more_above: boolean;
  has_more_below: boolean;
  total_lines: number;
}

// --- Eval types ---

export type EvalTaskSource = 'manual' | 'pr_bootstrap' | 'failure_derived';
export type EvalComplexity = 'trivial' | 'simple' | 'moderate' | 'complex';
export type GraderType = 'code_check' | 'llm_judge';
export type EvalRunStatus = 'pending' | 'running' | 'grading' | 'completed' | 'failed';
export type EvalBatchStatus = 'pending' | 'running' | 'completed' | 'failed';
export type EvalBootstrapStatus = 'pending' | 'running' | 'completed' | 'failed';
export type EvalBootstrapCandidateStatus = 'proposed' | 'accepted' | 'rejected' | 'needs_revision';

export interface ScoringCriterion {
  name: string;
  notes: string;
  grader_type: GraderType;
  grader_config?: Record<string, unknown>;
  weight: number;
  required: boolean;
}

export interface CriterionResult {
  name: string;
  grader_type?: GraderType;
  score: number;
  pass: boolean;
  details?: string;
  reasoning?: string;
}

export interface EvalTask {
  id: string;
  org_id: string;
  repo_id: string;
  name: string;
  description: string;
  base_commit_sha: string;
  solution_commit_sha?: string;
  solution_diff?: string;
  issue_description: string;
  issue_context?: Record<string, unknown>;
  server_deploy_sha?: string;
  pm_document_set_pin_id?: string;
  org_settings_version_id?: string;
  memory_snapshot?: Record<string, unknown>;
  sandbox_image_digest?: string;
  context_overrides?: Record<string, unknown>;
  scoring_criteria: ScoringCriterion[];
  pass_threshold: number;
  source: EvalTaskSource;
  source_pr_number?: number;
  complexity: EvalComplexity;
  snapshot_broken: boolean;
  tags?: string[];
  created_by?: string;
  created_at: string;
  updated_at: string;
  archived_at?: string;
}

export interface EvalRun {
  id: string;
  task_id: string;
  org_id: string;
  batch_id?: string;
  session_id?: string;
  thread_id?: string;
  input_manifest?: Record<string, unknown>;
  model: string;
  server_deploy_sha?: string;
  pm_document_set_pin_id?: string;
  config_ref?: string;
  context_overrides?: Record<string, unknown>;
  agent_diff?: string;
  agent_trace?: Record<string, unknown>;
  token_usage?: Record<string, unknown>;
  criterion_results?: CriterionResult[];
  final_score?: number;
  passed?: boolean;
  status: EvalRunStatus;
  duration_seconds?: number;
  sandbox_id?: string;
  started_at?: string;
  completed_at?: string;
  error_message?: string;
  created_at: string;
}

export interface EvalBatch {
  id: string;
  org_id: string;
  name: string;
  status: EvalBatchStatus;
  task_count: number;
  run_count: number;
  created_by?: string;
  created_at: string;
  completed_at?: string;
}

export interface EvalBatchDetail extends EvalBatch {
  runs: EvalRun[];
  gate_decisions?: EvalReleaseGateDecision[];
}

export interface EvalBootstrapCandidate {
  id?: string;
  pr_number: number;
  pr_title: string;
  base_commit_sha: string;
  solution_commit_sha: string;
  solution_diff: string;
  issue_description: string;
  scoring_criteria: ScoringCriterion[];
  complexity: EvalComplexity;
  fitness_score: number;
  fitness_reasoning: string;
  status?: EvalBootstrapCandidateStatus;
  rejection_reason?: string;
  evidence?: Record<string, unknown>;
  warnings?: string[];
  validation_warnings?: EvalValidationWarning[];
  accepted_task_id?: string;
  created_task_id?: string;
}

export interface EvalValidationWarning {
  code: string;
  severity: 'info' | 'warning' | 'error';
  message: string;
  suggestion?: string;
  blocking: boolean;
}

export interface EvalBootstrapRun {
  id: string;
  org_id: string;
  repo_id: string;
  status: EvalBootstrapStatus;
  candidates?: EvalBootstrapCandidate[];
  session_id?: string;
  thread_id?: string;
  created_by?: string;
  created_at: string;
  completed_at?: string;
  error_message?: string;
}

export type EvalDatasetType = 'golden' | 'shadow' | 'adversarial';
export type EvalDatasetStatus = 'active' | 'archived';

export interface EvalDataset {
  id: string;
  org_id: string;
  repository_id?: string;
  name: string;
  dataset_type: EvalDatasetType;
  status: EvalDatasetStatus;
  description: string;
  source_summary: string;
  created_by_user_id?: string;
  created_at: string;
  updated_at: string;
  task_count: number;
}

export interface EvalDatasetTask {
  id: string;
  org_id: string;
  dataset_id: string;
  task_id: string;
  slice_key: string;
  created_at: string;
}

export interface EvalReleaseGate {
  id: string;
  org_id: string;
  gate_name: string;
  enabled: boolean;
  dataset_id?: string;
  min_pass_at_1: number;
  min_pass_at_k: number;
  max_policy_violations: number;
  max_regression_delta: number;
  canary_stages?: unknown;
  rollback_rules?: unknown;
  updated_by_user_id?: string;
  active: boolean;
  created_at: string;
}

export interface EvalReleaseGateDecision {
  id: string;
  org_id: string;
  batch_id: string;
  gate_id: string;
  status: 'passed' | 'failed' | 'no_data';
  reason: string;
  metrics?: Record<string, unknown>;
  created_at: string;
}

// Lightweight signal arriving over the per-batch SSE stream. Mirrors
// models.EvalBatchUpdatedEvent. Consumers refetch the full EvalBatchDetail on
// receipt rather than reading fields from the event itself, so payload size
// stays bounded for large batches.
export interface EvalBatchUpdatedEvent {
  batch_id: string;
  org_id: string;
  status: EvalBatchStatus;
  updated_at: string;
}

// Lightweight signal arriving over the per-bootstrap-run SSE stream. Mirrors
// models.EvalBootstrapUpdatedEvent.
export interface EvalBootstrapUpdatedEvent {
  bootstrap_run_id: string;
  org_id: string;
  status: EvalBootstrapStatus;
  session_id?: string;
  updated_at: string;
}

export const evalComplexityConfig: Record<EvalComplexity, { color: string; label: string }> = {
  trivial: { color: "bg-muted text-muted-foreground", label: "Trivial" },
  simple: { color: "bg-emerald-500/10 text-emerald-700 dark:text-emerald-400", label: "Simple" },
  moderate: { color: "bg-yellow-500/10 text-yellow-700 dark:text-yellow-400", label: "Moderate" },
  complex: { color: "bg-red-500/10 text-red-700 dark:text-red-400", label: "Complex" },
};

export const evalRunStatusConfig: Record<EvalRunStatus, { color: string; label: string }> = {
  pending: { color: "bg-muted text-muted-foreground", label: "Pending" },
  running: { color: "bg-info/10 text-info", label: "Running" },
  grading: { color: "bg-violet-500/10 text-violet-700 dark:text-violet-400", label: "Grading" },
  completed: { color: "bg-success/10 text-success", label: "Completed" },
  failed: { color: "bg-destructive/10 text-destructive", label: "Failed" },
};

export const evalSourceConfig: Record<EvalTaskSource, { label: string }> = {
  manual: { label: "Manual" },
  pr_bootstrap: { label: "PR bootstrap" },
  failure_derived: { label: "Failure derived" },
};

// ── Usage & Billing Dashboard ──────────────────────────────────────────

export interface UsageSummary {
  org_id: string;
  period_start: string;
  period_end: string;
  total_container_minutes: number;
  total_sessions: number;
  peak_concurrent: number;
  by_capacity: CapacityBucket[];
  total_input_tokens: number;
  total_output_tokens: number;
  total_llm_cost_usd: number;
}

export interface CapacityBucket {
  cpu_limit: number;
  memory_limit_mb: number;
  disk_limit_mb: number;
  container_minutes: number;
  session_count: number;
}

export interface UsageTimeseriesBucket {
  hour_utc: string;
  user_id?: string;
  user_name?: string;
  capacity_tier?: string;
  agent_type?: string;
  model_used?: string;
  reasoning_effort?: string;
  series_key?: string;
  series_label?: string;
  total_container_minutes: number;
  total_sessions: number;
  total_container_starts: number;
  peak_concurrent: number;
  avg_duration_sec: number;
  p95_duration_sec: number;
  total_input_tokens: number;
  total_output_tokens: number;
  total_tokens: number;
  total_llm_cost_usd: number;
}

export interface UsageTimeseriesResponse {
  buckets: UsageTimeseriesBucket[];
  period_start: string;
  period_end: string;
}

export interface UsageBreakdownRow {
  key: string;
  label: string;
  total_container_minutes: number;
  total_sessions: number;
  total_container_starts: number;
  peak_concurrent: number;
  total_input_tokens: number;
  total_output_tokens: number;
  total_tokens: number;
  total_llm_cost_usd: number;
  percentage: number;
  share_of_sessions?: number;
  share_of_token_cost?: number;
  share_of_tokens?: number;
}

// Automation types
export type AutomationScheduleType = 'interval' | 'cron';
export type AutomationRunStatus = 'pending' | 'running' | 'completed' | 'completed_noop' | 'failed' | 'skipped';
export type AutomationIdentityScope = 'org' | 'personal';

export interface Automation {
  id: string;
  org_id: string;
  repository_id?: string;
  name: string;
  goal: string;
  scope?: string;
  icon_type: 'emoji';
  icon_value: string;
  agent_type?: string;
  model_override?: string;
  reasoning_effort?: Session["reasoning_effort"];
  execution_mode: string;
  max_concurrent: number;
  base_branch: string;
  identity_scope: AutomationIdentityScope;
  pre_pr_review_loops: number;
  schedule_type: AutomationScheduleType;
  interval_value?: number;
  interval_unit?: 'hours' | 'days' | 'weeks';
  interval_run_at?: string;
  cron_expression?: string;
  timezone: string;
  next_run_at?: string;
  last_run_at?: string;
  enabled: boolean;
  created_by?: string;
  paused_by?: string;
  paused_at?: string;
  priority: number;
  created_at: string;
  updated_at: string;
}

export interface AutomationRun {
  id: string;
  automation_id: string;
  triggered_at: string;
  triggered_by: 'schedule' | 'manual';
  triggered_by_user_id?: string;
  scheduled_time?: string;
  goal_snapshot: string;
  config_snapshot?: Record<string, unknown>;
  status: AutomationRunStatus;
  completed_at?: string;
  result_summary?: string;
  created_at: string;
  updated_at: string;
  // Compact view of the session this run spawned. Populated by the list
  // endpoint via a LATERAL join (see internal/db/automations.go); absent
  // when the run hasn't spawned a session yet (pending/skipped, or
  // mid-flight before the worker creates the session).
  session?: AutomationRunSession;
}

// Mirrors models.PRCreationState. Kept as a literal union so the row UI
// gets exhaustiveness checks when branching on it (e.g. the "Creating PR…"
// pill on completed_no_pr rows).
export type PRCreationState =
  | 'idle'
  | 'queued'
  | 'pushing'
  | 'succeeded'
  | 'failed';

export interface AutomationRunSession {
  id: string;
  title?: string;
  // Mirrors models.SessionStatus values; the row UI keys off this
  // (notably "needs_human_guidance") to choose between failure and
  // attention treatments.
  status: SessionStatus;
  diff_stats?: { added: number; removed: number; files_changed?: number };
  failure_explanation?: string;
  failure_category?: string;
  failure_next_steps?: string[];
  failure_retry_advised: boolean;
  pr_creation_state: PRCreationState;
  pr?: PRSummary;
}

export interface AutomationRunStatsBucket {
  bucket: string;
  total: number;
  completed: number;
  completed_noop: number;
  failed: number;
  skipped: number;
  running: number;
  pending: number;
  avg_duration_seconds: number;
}

export interface AutomationRunStatsTotals {
  total: number;
  completed: number;
  completed_noop: number;
  failed: number;
  skipped: number;
  running: number;
  pending: number;
  success_rate: number;
  avg_duration_seconds: number;
}

export interface AutomationRunStats {
  since: string;
  until: string;
  buckets: AutomationRunStatsBucket[];
  totals: AutomationRunStatsTotals;
}

// AutomationBulkFixupFailure names a cron automation that was resumed by a
// bulk action but whose next_run_at could not be recomputed — usually because
// cron_expression no longer parses. The row was still flipped enabled, but
// the scheduler will skip it until a user edits the expression.
export interface AutomationBulkFixupFailure {
  automation_id: string;
  reason: string;
}

// AutomationBulkResponse is the 200 OK body returned by POST /automations/bulk.
// `affected` lists the automation IDs that actually changed state (cross-org
// or deleted IDs are silently dropped). `fixup_failures` is always present but
// only populated on resume; callers should surface it so users understand why
// a "resumed" automation isn't firing.
export interface AutomationBulkResponse {
  affected: string[];
  fixup_failures: AutomationBulkFixupFailure[];
}

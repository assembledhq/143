// Preview system type definitions matching Go models.

export type PreviewStatus =
  | "starting"
  | "ready"
  | "partially_ready"
  | "unhealthy"
  | "stopped"
  | "failed"
  | "expired"
  | "unavailable";

export const ACTIVE_PREVIEW_STATUSES: PreviewStatus[] = [
  "ready",
  "partially_ready",
  "unhealthy",
  "starting",
];
export const CONTROLLABLE_PREVIEW_STATUSES: PreviewStatus[] = [
  "ready",
  "partially_ready",
  "unhealthy",
];

export function formatPreviewStatus(status: PreviewStatus | string): string {
  return status.replaceAll("_", " ").replace(/\b\w/g, (c) => c.toUpperCase());
}

// Preview error codes returned by the StartPreview endpoint. These map
// verbatim to the backend's writeError code strings in
// internal/api/handlers/preview.go and are a stable public contract:
// changes require coordinating both sides. Renaming one of these is a
// breaking change for any UI that switches on them.
export const PREVIEW_ERROR_CODES = {
  // 503 — per-user / per-org / per-worker concurrency cap hit. Server
  // supplies a user-facing message describing which cap was exceeded.
  CAPACITY_REACHED: "PREVIEW_CAPACITY_REACHED",
  // 410 — no usable snapshot exists (no container, no snapshot key, or
  // the snapshot blob is gone from the store). User needs to send a new
  // message to rebuild the sandbox.
  SNAPSHOT_EXPIRED: "SNAPSHOT_EXPIRED",
  // 409 — the session is not expired, but there is no restorable snapshot
  // available (for example snapshot persistence failed on the worker).
  SNAPSHOT_UNAVAILABLE: "SNAPSHOT_UNAVAILABLE",
  // 409 — this worker has no sandbox provider / snapshot store wired.
  // Configuration issue; needs admin attention.
  NO_SANDBOX: "NO_SANDBOX",
  // 409 — a concurrent hydrate (typically a continue_session turn) won the
  // sandbox attach race. Transient; a retry once the turn finishes
  // resolves it.
  SANDBOX_BUSY: "SANDBOX_BUSY",
  // 502 — the API could not complete its RPC to the worker (EOF, network
  // partition, or worker WriteTimeout overrun). Distinct from the worker
  // returning a structured error: here we never got a response body, so
  // there's no underlying code to surface — but the failure is transient
  // and a retry will usually succeed.
  WORKER_REQUEST_FAILED: "PREVIEW_WORKER_REQUEST_FAILED",
  // 500 — internal failure while hydrating (restore error, provider
  // outage, DB write failure). Generic; details in the underlying message.
  HYDRATE_FAILED: "PREVIEW_HYDRATE_FAILED",
  // 422 — the repo has no committed .143/config.json with a preview
  // section and the client
  // didn't supply an explicit config, so the backend has nothing to launch.
  // User fix is to commit the config file (see docs/guides/previews.md).
  NO_CONFIG: "PREVIEW_NO_CONFIG",
  // 422 — .143/config.json exists, but the preview section cannot be parsed or
  // fails structural validation. Backend message includes the specific config
  // error and the recovery action.
  CONFIG_INVALID: "PREVIEW_CONFIG_INVALID",
  // 422 — a preview infrastructure container's image is not on the worker
  // and the on-demand pull failed (registry unreachable, image renamed,
  // rate-limit, no egress). The user-visible message names the image so an
  // operator can pull manually or fix registry access.
  INFRA_IMAGE_UNAVAILABLE: "PREVIEW_INFRA_IMAGE_UNAVAILABLE",
  // 422 — Docker accepted the create call but the container failed to
  // start (resource limits, label conflict, daemon error).
  INFRA_START_FAILED: "PREVIEW_INFRA_START_FAILED",
  // 422 — the infrastructure container started but its health check
  // (pg_isready, redis-cli ping, etc.) never passed within the timeout.
  INFRA_UNHEALTHY: "PREVIEW_INFRA_UNHEALTHY",
  // 422 — a user-supplied init script (seed SQL etc.) returned a non-zero
  // exit code or could not be read from the workspace.
  INIT_SCRIPT_FAILED: "PREVIEW_INIT_SCRIPT_FAILED",
  // 422 — preview.install failed before any services were started.
  INSTALL_FAILED: "PREVIEW_INSTALL_FAILED",
  // 422 — an application service was launched but its readiness probe
  // never passed within the configured timeout. The service likely
  // crashed at boot or is bound to a different port than it declares in
  // .143/config.json.
  SERVICE_NOT_READY: "PREVIEW_SERVICE_NOT_READY",
  // 503 — the preview URL is still valid, but the owning worker runtime is
  // gone or its lease expired. Restarting the preview creates a new runtime.
  RUNTIME_UNAVAILABLE: "PREVIEW_RUNTIME_UNAVAILABLE",
} as const;

export type PreviewErrorCode =
  (typeof PREVIEW_ERROR_CODES)[keyof typeof PREVIEW_ERROR_CODES];

export interface PreviewInstance {
  id: string;
  live_version: number;
  session_id: string;
  org_id: string;
  user_id: string;
  status: PreviewStatus;
  profile_name: string;
  name: string;
  provider: string;
  worker_node_id: string;
  preview_handle: string;
  primary_service: string;
  port: number;
  config_digest: string;
  base_commit_sha: string;
  last_accessed_at: string;
  stopped_at?: string;
  expires_at: string;
  last_path: string;
  memory_limit_mb: number;
  cpu_limit_millis: number;
  disk_limit_mb: number;
  error?: string;
  unavailable_reason?:
    | "owner_lost"
    | "deploy_drain_timeout"
    | "host_maintenance"
    | "emergency_force"
    | "lease_expired"
    | "endpoint_unreachable";
  created_at: string;
  updated_at: string;
  source_workspace_revision?: number;
  source_workspace_revision_updated_at?: string;
  runtime_workspace_revision?: number;
  runtime_workspace_revision_updated_at?: string;
  runtime_workspace_revision_source?: PreviewRuntimeRevisionSource;
  // When set and in the future, the backend has flagged this preview for an
  // imminent restart (recycle grace period). The frontend surfaces a warning
  // so users can save state before the restart.
  recycle_scheduled_at?: string;
}

export type PreviewFreshnessState =
  | "current"
  | "live_updated"
  | "restart_required"
  | "out_of_date"
  | "updating"
  | "unknown";

export type PreviewRuntimeRevisionSource =
  | ""
  | "launch"
  | "recycle"
  | "hmr"
  | "file_event";

export type PreviewRestartReasonKind =
  | "dependency_changed"
  | "preview_config_changed"
  | "build_config_changed"
  | "environment_config_changed"
  | "database_schema_changed";

export interface PreviewRestartReason {
  kind: PreviewRestartReasonKind;
  path?: string;
  detail?: string;
}

export interface PreviewFreshness {
  state: PreviewFreshnessState;
  current_workspace_revision: number;
  current_workspace_revision_updated_at: string;
  preview_workspace_revision?: number;
  preview_workspace_revision_updated_at?: string;
  runtime_workspace_revision?: number;
  runtime_workspace_revision_updated_at?: string;
  runtime_workspace_revision_source?: PreviewRuntimeRevisionSource;
  restart_required?: boolean;
  restart_reasons?: PreviewRestartReason[];
  reason?: string;
}

export type PreviewServiceRole = "primary" | "support";
export type PreviewServiceStatus = "starting" | "ready" | "failed" | "stopped";

export interface PreviewService {
  id: string;
  preview_instance_id: string;
  service_name: string;
  role: PreviewServiceRole;
  status: PreviewServiceStatus;
  command: string[];
  cwd: string;
  port: number;
  pid?: number;
  error?: string;
  created_at: string;
}

export type PreviewInfraStatus =
  | "provisioning"
  | "healthy"
  | "unhealthy"
  | "failed";

export interface PreviewInfrastructure {
  id: string;
  preview_instance_id: string;
  infra_name: string;
  template: string;
  container_id: string;
  status: PreviewInfraStatus;
  host: string;
  port: number;
  error?: string;
  created_at: string;
}

export interface PreviewLog {
  id: string;
  preview_instance_id: string;
  org_id: string;
  level: string;
  step: string;
  message: string;
  metadata?: unknown;
  created_at: string;
}

export interface PreviewStatusResponse {
  instance?: PreviewInstance;
  services: PreviewService[];
  infrastructure?: PreviewInfrastructure[];
  preview_origin?: string;
  freshness?: PreviewFreshness;
  startup_estimate?: PreviewStartupEstimate;
  prewarm?: PreviewPrewarmStatus;
}

export interface PreviewPrewarmStatus {
  state: "warming" | "warm" | "failed" | string;
  workspace_revision: number;
  resume_estimate_seconds?: number;
  preview_id?: string;
  error?: string;
}

export interface PreviewStartupEstimate {
  label: string;
  p50_seconds: number;
  sample_count: number;
  confidence: "low" | "medium" | "high" | string;
}

export interface EnsurePreviewResponse {
  action: "started" | "restarted" | "already_starting";
  instance: PreviewInstance;
}

// Console messages

export interface ConsoleMessage {
  level: "log" | "info" | "warning" | "error";
  text: string;
  source?: string;
  line_no?: number;
  url?: string;
  time: string;
}

// Element inspection

export interface Rect {
  x: number;
  y: number;
  width: number;
  height: number;
}

export interface ElementInfo {
  tag_name: string;
  component_name?: string;
  component_file?: string;
  component_line?: number;
  props?: Record<string, unknown>;
  component_tree?: string[];
  bounding_box: Rect;
  computed_styles?: Record<string, string>;
  design_tokens?: Record<string, string>;
  inner_text?: string;
  attributes?: Record<string, string>;
  dom_path: string;
  parent_context?: string;
  framework?: string;
}

// Preview detection

export type PreviewReadiness =
  | "ready"
  | "admin_setup_required"
  | "partial"
  | "not_supported";

export interface MissingCredential {
  credential_set: string;
  env_vars: string[];
}

export interface MissingSecretBundle {
  bundle: string;
  services?: string[];
  env?: string[];
  files?: string[];
  status: string;
}

export interface PreviewDetectionResult {
  readiness: PreviewReadiness;
  config_name?: string;
  services?: string[];
  primary_service?: string;
  infrastructure?: string[];
  missing_credentials?: MissingCredential[];
  missing_secret_bundles?: MissingSecretBundle[];
  missing_destinations?: string[];
  validation_errors?: string[];
}

// Design Mode

export interface Annotation {
  type: string;
  path: string;
}

export interface DesignModeFeedback {
  type: "design_mode_feedback" | "visual_edit";
  elements: ElementInfo[];
  instruction?: string;
  annotations?: Annotation[];
  screenshot_ref?: string;
  direction?: string;
  parent?: ElementInfo;
  siblings?: string[];
  style_edits?: StyleEdit[];
}

export interface StyleEdit {
  property: string;
  old_value: string;
  new_value: string;
  old_token?: string;
  new_token?: string;
}

export interface VisualEdit {
  element: ElementInfo;
  changes: StyleEdit[];
  before_screenshot?: string;
  after_screenshot?: string;
}

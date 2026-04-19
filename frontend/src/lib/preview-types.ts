// Preview system type definitions matching Go models.

export type PreviewStatus =
  | "starting"
  | "ready"
  | "partially_ready"
  | "unhealthy"
  | "stopped"
  | "failed"
  | "expired";

export interface PreviewInstance {
  id: string;
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
  error?: string;
  created_at: string;
  updated_at: string;
  // When set and in the future, the backend has flagged this preview for an
  // imminent restart (recycle grace period). The frontend surfaces a warning
  // so users can save state before the restart.
  recycle_scheduled_at?: string;
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

export interface PreviewStatusResponse {
  instance: PreviewInstance;
  services: PreviewService[];
  infrastructure?: PreviewInfrastructure[];
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

export type PreviewReadiness = "ready" | "partial" | "not_supported";

export interface MissingCredential {
  credential_set: string;
  env_vars: string[];
}

export interface PreviewDetectionResult {
  readiness: PreviewReadiness;
  config_name?: string;
  services?: string[];
  primary_service?: string;
  infrastructure?: string[];
  missing_credentials?: MissingCredential[];
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

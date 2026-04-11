// Preview system type definitions matching Go models.

export type PreviewPhase =
  | "pending"
  | "building"
  | "initializing"
  | "starting"
  | "ready"
  | "partially_ready"
  | "stopping"
  | "stopped"
  | "failed";

export type PreviewTrigger =
  | "baseline"
  | "agent_change"
  | "user_request"
  | "hmr_update"
  | "periodic";

export interface PreviewInstance {
  id: string;
  session_id: string;
  org_id: string;
  phase: PreviewPhase;
  preview_url: string;
  started_at?: string;
  ready_at?: string;
  stopped_at?: string;
  expires_at?: string;
  error?: string;
  failure_pattern?: string;
  build_log?: string;
  created_at: string;
  updated_at: string;
}

export interface PreviewService {
  name: string;
  type: "frontend" | "backend" | "database" | "other";
  status: "pending" | "starting" | "ready" | "failed" | "stopped";
  port: number;
  url?: string;
  health_endpoint?: string;
  error?: string;
}

export interface PreviewInfrastructure {
  container_id?: string;
  cpu_millicores: number;
  memory_mb: number;
  disk_mb: number;
  network_policy?: string;
}

export interface PreviewSnapshot {
  id: string;
  instance_id: string;
  trigger: PreviewTrigger;
  screenshot_url: string;
  thumbnail_url?: string;
  viewport_width: number;
  viewport_height: number;
  console_error_count: number;
  changed_files?: string[];
  created_at: string;
}

export interface PreviewLog {
  id: string;
  instance_id: string;
  service: string;
  level: "info" | "warn" | "error" | "debug";
  message: string;
  timestamp: string;
}

export interface PreviewStatus {
  instance: PreviewInstance;
  services: PreviewService[];
  infrastructure: PreviewInfrastructure;
  snapshots: PreviewSnapshot[];
  active_connections: number;
}

// Screenshot capture

export interface ScreenshotOpts {
  viewport_width?: number;
  viewport_height?: number;
  full_page?: boolean;
  selector?: string;
  delay_ms?: number;
}

export interface ScreenshotResult {
  url: string;
  width: number;
  height: number;
  timestamp: string;
}

// Console messages

export interface ConsoleMessage {
  level: "log" | "info" | "warn" | "error";
  text: string;
  source?: string;
  line_number?: number;
  timestamp: string;
}

// Interaction replay

export type InteractionStepType =
  | "click"
  | "type"
  | "scroll"
  | "hover"
  | "select"
  | "navigate"
  | "wait"
  | "keypress";

export interface InteractionStep {
  type: InteractionStepType;
  selector?: string;
  x?: number;
  y?: number;
  value?: string;
  delay_ms?: number;
}

export interface StepResult {
  step_index: number;
  success: boolean;
  error?: string;
  screenshot_url?: string;
  duration_ms: number;
}

export interface InteractionResult {
  success: boolean;
  steps: StepResult[];
  final_screenshot_url?: string;
}

// Multi-viewport

export interface ViewportSpec {
  name: string;
  width: number;
  height: number;
}

export interface MultiViewportOpts {
  viewports: ViewportSpec[];
  url?: string;
  full_page?: boolean;
  delay_ms?: number;
}

export interface ViewportCapture {
  viewport: ViewportSpec;
  screenshot_url: string;
  console_errors: ConsoleMessage[];
}

export interface MultiViewportResult {
  captures: ViewportCapture[];
  timestamp: string;
}

// Visual diff

export interface DiffRegion {
  x: number;
  y: number;
  width: number;
  height: number;
  change_type: "added" | "removed" | "modified";
  similarity: number;
}

export interface DOMChange {
  selector: string;
  change_type: "added" | "removed" | "modified" | "text_changed" | "attribute_changed";
  old_value?: string;
  new_value?: string;
}

export interface StyleChange {
  selector: string;
  property: string;
  old_value: string;
  new_value: string;
}

export interface VisualDiff {
  before_snapshot_id: string;
  after_snapshot_id: string;
  diff_image_url: string;
  similarity_score: number;
  regions: DiffRegion[];
  dom_changes: DOMChange[];
  style_changes: StyleChange[];
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
  class_list: string[];
  id?: string;
  text_content?: string;
  bounding_box: Rect;
  computed_styles: Record<string, string>;
  attributes: Record<string, string>;
  parent_selector?: string;
  children_count: number;
  component_name?: string;
  component_file?: string;
}

// Design Mode

export interface Annotation {
  type: "rectangle" | "arrow" | "freehand" | "text";
  points: Array<{ x: number; y: number }>;
  color?: string;
  label?: string;
}

export interface StyleEdit {
  property: string;
  value: string;
}

export interface VisualEdit {
  selector: string;
  styles: StyleEdit[];
}

export interface DesignModeFeedback {
  instruction: string;
  selected_elements: Array<{
    selector: string;
    bounding_box: Rect;
  }>;
  annotations: Annotation[];
  visual_edits: VisualEdit[];
  screenshot_url?: string;
}

// Assertions

export type AssertionType =
  | "element_exists"
  | "element_visible"
  | "text_contains"
  | "style_matches"
  | "no_console_errors"
  | "responsive_layout"
  | "accessibility"
  | "performance";

export interface PreviewAssertion {
  type: AssertionType;
  selector?: string;
  expected?: string;
  property?: string;
  viewport?: ViewportSpec;
  description?: string;
}

export interface AssertionResult {
  assertion: PreviewAssertion;
  passed: boolean;
  actual?: string;
  message?: string;
  screenshot_url?: string;
}

export interface AssertionResults {
  results: AssertionResult[];
  passed: number;
  failed: number;
  total: number;
}

// Preview detection

export interface PreviewDetectionResult {
  supported: boolean;
  framework?: string;
  start_command?: string;
  build_command?: string;
  port?: number;
  config_file?: string;
  services?: Array<{
    name: string;
    type: string;
    port: number;
    start_command: string;
  }>;
}

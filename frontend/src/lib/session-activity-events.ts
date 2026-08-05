import { api } from "./api";
import type { SessionActivityDetail, SessionActivityPhaseStatus } from "./types";

export interface SessionActivityUIEvent {
  event: "preference_changed" | "capsule_expanded" | "capsule_collapsed" | "auto_collapse_suppressed" | "anchor_expanded" | "scroll_restore_failed" | "unexpected_scroll_delta" | "completed_phase_rendered" | "transcript_window_rendered" | "latest_final_response_positioned";
  detail?: SessionActivityDetail;
  status?: SessionActivityPhaseStatus | "historical";
  reason?: string;
  trigger?: "manual" | "child_open" | "text_selecting" | "viewport_inspecting" | "anchor" | "preference";
  viewport_class?: "mobile" | "desktop";
  tool_count_bucket?: "0" | "1" | "2-5" | "6-20" | "21+";
  duration_bucket?: "unknown" | "<10s" | "10-59s" | "1-5m" | "5-20m" | "20m+";
  value_bucket?: "0" | "1-5" | "6-10" | "11-25" | "26-50" | "51-100" | "101+" | "0-47px" | "48-95px" | "96-191px" | "192-383px" | "384-767px" | "768px+";
}

export function recordSessionActivityEvent(event: SessionActivityUIEvent): void {
  void api.sessionActivity.recordEvent(event).catch((error) => console.error("Failed to record session activity event", error));
}

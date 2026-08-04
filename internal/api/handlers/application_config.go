package handlers

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/assembledhq/143/internal/metrics"
	"github.com/rs/zerolog"
)

type ApplicationConfigResponse struct {
	SessionActivityCapsulesEnabled bool      `json:"session_activity_capsules_enabled"`
	Revision                       string    `json:"revision"`
	UpdatedAt                      time.Time `json:"updated_at"`
}

type ApplicationConfigHandler struct {
	config ApplicationConfigResponse
}

func NewApplicationConfigHandler(sessionActivityCapsulesEnabled bool) *ApplicationConfigHandler {
	digest := sha256.Sum256([]byte(fmt.Sprintf("session_activity_capsules_enabled=%t", sessionActivityCapsulesEnabled)))
	return &ApplicationConfigHandler{config: ApplicationConfigResponse{
		SessionActivityCapsulesEnabled: sessionActivityCapsulesEnabled,
		Revision:                       fmt.Sprintf("%x", digest[:12]),
		UpdatedAt:                      time.Now().UTC(),
	}}
}

func (h *ApplicationConfigHandler) Config() ApplicationConfigResponse {
	return h.config
}

func (h *ApplicationConfigHandler) Get(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"data": h.config})
}

func (h *ApplicationConfigHandler) RecordSessionActivityEvent(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Event           string `json:"event"`
		Detail          string `json:"detail"`
		Status          string `json:"status"`
		Reason          string `json:"reason"`
		Trigger         string `json:"trigger"`
		ViewportClass   string `json:"viewport_class"`
		ToolCountBucket string `json:"tool_count_bucket"`
		DurationBucket  string `json:"duration_bucket"`
		ValueBucket     string `json:"value_bucket"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid session activity event body")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid session activity event body")
		return
	}
	if !oneOf(body.Event, "preference_changed", "capsule_expanded", "capsule_collapsed", "auto_collapse_suppressed", "anchor_expanded", "scroll_restore_failed", "unexpected_scroll_delta", "completed_phase_rendered", "transcript_window_rendered", "latest_final_response_positioned") ||
		!oneOfOrEmpty(body.Detail, "compact", "detailed") ||
		!oneOfOrEmpty(body.Status, "running", "completed", "failed", "cancelled", "interrupted", "historical") ||
		!oneOfOrEmpty(body.Reason, "final_response", "human_input", "approval", "plan_approval", "steered", "maintenance", "runtime_lost", "capacity_suspended", "interrupted", "stopped", "cancelled", "error") ||
		!oneOfOrEmpty(body.Trigger, "manual", "child_open", "text_selecting", "viewport_inspecting", "anchor", "preference") ||
		!oneOfOrEmpty(body.ViewportClass, "mobile", "desktop") ||
		!oneOfOrEmpty(body.ToolCountBucket, "0", "1", "2-5", "6-20", "21+") ||
		!oneOfOrEmpty(body.DurationBucket, "unknown", "<10s", "10-59s", "1-5m", "5-20m", "20m+") ||
		!oneOfOrEmpty(body.ValueBucket, "0", "1-5", "6-10", "11-25", "26-50", "51-100", "101+", "0-47px", "48-95px", "96-191px", "192-383px", "384-767px", "768px+") {
		writeError(w, r, http.StatusBadRequest, "INVALID_EVENT", "invalid session activity event")
		return
	}
	metrics.RecordSessionActivityUIEvent(r.Context(), body.Event, body.Detail, body.Status, body.Reason, body.Trigger, body.ViewportClass, body.ToolCountBucket, body.DurationBucket, body.ValueBucket)
	if body.Event == "scroll_restore_failed" || body.Event == "unexpected_scroll_delta" {
		zerolog.Ctx(r.Context()).Warn().
			Str("session_activity_event", body.Event).
			Str("activity_detail", body.Detail).
			Str("interaction_trigger", body.Trigger).
			Str("viewport_class", body.ViewportClass).
			Msg("session activity transcript viewport integrity event")
	}
	w.WriteHeader(http.StatusNoContent)
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

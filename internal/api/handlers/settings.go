package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/rs/zerolog"
)

// OrgSettingsInvalidator drops cached org settings so that a write here is
// observed by the orchestrator's Amp/Pi config lookup immediately, rather
// than waiting for the cache TTL to expire. Declared locally (not imported
// from services/agent) so this handler doesn't pull in the agent package.
type OrgSettingsInvalidator interface {
	InvalidateOrg(orgID uuid.UUID)
}

type StaticEgressWorkerChecker interface {
	HasStaticEgressCapableWorker(ctx context.Context, publicIP string) (bool, error)
}

type SettingsHandler struct {
	orgStore     *db.OrganizationStore
	llmDefaults  map[string]string // provider name → masked key (from server env)
	audit        *db.AuditEmitter
	logger       zerolog.Logger
	invalidator  OrgSettingsInvalidator
	staticEgress StaticEgressStatus
	workers      StaticEgressWorkerChecker
}

// StaticEgressStatus is platform-level availability for the opt-in static
// sandbox egress path.
type StaticEgressStatus struct {
	Available         bool
	PublicIP          string
	UnavailableReason string
}

type networkStatusResponse struct {
	StaticEgressAvailable         bool   `json:"static_egress_available"`
	StaticEgressEnabled           bool   `json:"static_egress_enabled"`
	StaticEgressPublicIP          string `json:"static_egress_public_ip,omitempty"`
	StaticEgressUnavailableReason string `json:"static_egress_unavailable_reason,omitempty"`
}

// SetAuditEmitter injects the audit emitter for logging settings events.
func (h *SettingsHandler) SetAuditEmitter(audit *db.AuditEmitter) {
	h.audit = audit
}

// SetLogger wires a logger used by marshalAuditDetails so a failure to
// JSON-encode the settings diff surfaces in logs instead of silently
// dropping the audit payload.
func (h *SettingsHandler) SetLogger(logger zerolog.Logger) {
	h.logger = logger
}

// SetOrgSettingsInvalidator injects the cache invalidator. When set, the
// Update handler will call InvalidateOrg after a successful write so the
// orchestrator's Amp/Pi env lookup picks up the new config on the next
// session start without waiting for the cache TTL.
func (h *SettingsHandler) SetOrgSettingsInvalidator(invalidator OrgSettingsInvalidator) {
	h.invalidator = invalidator
}

// SetStaticEgressStatus injects platform-level static egress availability.
func (h *SettingsHandler) SetStaticEgressStatus(status StaticEgressStatus) {
	h.staticEgress = status
}

func (h *SettingsHandler) SetStaticEgressWorkerChecker(workers StaticEgressWorkerChecker) {
	h.workers = workers
}

func NewSettingsHandler(orgStore *db.OrganizationStore, llmDefaults map[string]string) *SettingsHandler {
	return &SettingsHandler{
		orgStore:    orgStore,
		llmDefaults: llmDefaults,
		logger:      zerolog.Nop(),
	}
}

func (h *SettingsHandler) Get(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	org, err := h.orgStore.GetByID(r.Context(), orgID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "organization not found")
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.Organization]{Data: org})
}

// GetLLMDefaults returns which LLM providers have platform-level API keys
// configured, with keys masked. This lets the frontend show whether a platform
// fallback is available when the org hasn't configured their own key.
func (h *SettingsHandler) GetLLMDefaults(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"data": h.llmDefaults,
	})
}

// GetLLMModels returns the available LLM models grouped by provider.
// This is the source of truth — the frontend should use this instead of
// maintaining its own hardcoded list.
func (h *SettingsHandler) GetLLMModels(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"data": models.LLMModelsByProvider()})
}

// GetNetworkStatus returns the customer-facing network settings status.
func (h *SettingsHandler) GetNetworkStatus(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	org, err := h.orgStore.GetByID(r.Context(), orgID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "organization not found")
		return
	}
	settings, err := models.ParseOrgSettings(org.Settings)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INVALID_SETTINGS", "failed to parse organization settings", err)
		return
	}
	status, reason, availabilityErr := h.staticEgressAvailability(r.Context(), false)
	if availabilityErr != nil {
		h.logger.Warn().Err(availabilityErr).Msg("failed to verify static egress worker availability")
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[networkStatusResponse]{Data: networkStatusResponse{
		StaticEgressAvailable:         status.Available,
		StaticEgressEnabled:           settings.SandboxNetwork.StaticEgressEnabled,
		StaticEgressPublicIP:          status.PublicIP,
		StaticEgressUnavailableReason: reason,
	}})
}

func (h *SettingsHandler) staticEgressAvailability(ctx context.Context, requireWorkerChecker bool) (StaticEgressStatus, string, error) {
	status := h.staticEgress
	reason := status.UnavailableReason
	if status.Available {
		if h.workers == nil {
			if requireWorkerChecker {
				status.Available = false
				reason = "static egress worker availability checker is not configured"
			}
		} else {
			hasWorker, workerErr := h.workers.HasStaticEgressCapableWorker(ctx, status.PublicIP)
			if workerErr != nil {
				status.Available = false
				reason = "failed to verify static egress worker availability"
				return status, reason, workerErr
			}
			if !hasWorker {
				status.Available = false
				reason = "not all active session workers are static-egress-capable for the configured public IP"
			}
		}
	}
	if !status.Available && reason == "" {
		reason = "static egress gateway is not configured for this environment"
	}
	return status, reason, nil
}

func staticEgressEnableTransition(beforeRaw, afterRaw json.RawMessage) (bool, error) {
	before, err := models.ParseOrgSettings(beforeRaw)
	if err != nil {
		return false, err
	}
	after, err := models.ParseOrgSettings(afterRaw)
	if err != nil {
		return false, err
	}
	return !before.SandboxNetwork.StaticEgressEnabled && after.SandboxNetwork.StaticEgressEnabled, nil
}

func (h *SettingsHandler) Update(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	logger := zerolog.Ctx(r.Context())
	var req struct {
		Name     *string          `json:"name"`
		Settings *json.RawMessage `json:"settings"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}

	if req.Settings != nil {
		var parsedSettings models.OrgSettings
		if err := json.Unmarshal(*req.Settings, &parsedSettings); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_SETTINGS", "invalid settings JSON", err)
			return
		}
		if err := models.ValidateSettingsModels(parsedSettings); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_SETTINGS", err.Error())
			return
		}
		if parsedSettings.LLMModel != "" {
			if err := models.ValidateLLMModelAccess(parsedSettings.LLMModel, nil, h.platformLLMProviders()); err != nil {
				writeError(w, r, http.StatusBadRequest, "INVALID_SETTINGS", err.Error())
				return
			}
		}
	}
	if req.Name != nil {
		trimmed := strings.TrimSpace(*req.Name)
		if trimmed == "" {
			writeError(w, r, http.StatusBadRequest, "MISSING_NAME", "Name is required.")
			return
		}
		if len([]rune(trimmed)) > maxOrgNameLen {
			writeError(w, r, http.StatusBadRequest, "NAME_TOO_LONG", "Name must be 120 characters or fewer.")
			return
		}
		req.Name = &trimmed
	}

	org, err := h.orgStore.GetByID(r.Context(), orgID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "organization not found")
		return
	}

	// Snapshot the pre-update values so the audit entry can record a precise
	// before/after diff. The mutations below replace these in place, so the
	// capture has to happen up front.
	beforeName := org.Name
	beforeSettings := append(json.RawMessage(nil), org.Settings...)

	if req.Name != nil {
		org.Name = *req.Name
	}
	if req.Settings != nil {
		merged, mergeErr := mergeSettingsJSON(org.Settings, *req.Settings)
		if mergeErr != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "failed to merge settings")
			return
		}
		if _, parseErr := models.ParseOrgSettings(merged); parseErr != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_SETTINGS", "invalid organization settings", parseErr)
			return
		}
		org.Settings = merged
		enableStaticEgress, transitionErr := staticEgressEnableTransition(beforeSettings, org.Settings)
		if transitionErr != nil {
			writeError(w, r, http.StatusInternalServerError, "INVALID_SETTINGS", "failed to parse organization settings", transitionErr)
			return
		}
		if enableStaticEgress {
			status, reason, availabilityErr := h.staticEgressAvailability(r.Context(), true)
			if availabilityErr != nil {
				h.logger.Warn().Err(availabilityErr).Msg("failed to verify static egress availability before settings update")
			}
			if !status.Available {
				writeError(w, r, http.StatusServiceUnavailable, "STATIC_EGRESS_UNAVAILABLE", reason, availabilityErr)
				return
			}
		}
	}

	// Build the audit diff from what actually changed. Skip the emit entirely
	// when nothing changed so a no-op PATCH (client re-saving the current
	// values) doesn't pollute the timeline with empty "updated settings" rows.
	changes := map[string]any{}
	if req.Name != nil && org.Name != beforeName {
		changes["name"] = map[string]any{"before": beforeName, "after": org.Name}
	}
	if req.Settings != nil {
		for k, v := range settingsAuditDiff(beforeSettings, org.Settings) {
			changes[k] = v
		}
	}
	changedKeys := sortedSettingsChangeKeys(changes)
	requestSettingsKeys := topLevelSettingsPatchKeys(req.Settings)
	patchLogger := logger.Info().
		Str("path", r.URL.Path).
		Str("origin", r.Header.Get("Origin")).
		Str("referer", r.Referer()).
		Str("user_agent", r.UserAgent()).
		Bool("has_name_patch", req.Name != nil).
		Strs("request_settings_keys", requestSettingsKeys).
		Strs("changed_keys", changedKeys)

	if len(changes) == 0 {
		patchLogger.Bool("noop", true).Msg("settings patch noop")
		writeJSON(w, http.StatusOK, models.SingleResponse[models.Organization]{Data: org})
		return
	}

	if err := h.orgStore.Update(r.Context(), &org); err != nil {
		writeError(w, r, http.StatusInternalServerError, "UPDATE_FAILED", "failed to update organization", err)
		return
	}

	// Drop any cached agent_config for this org so the next Amp/Pi session
	// start observes the write. Skipping invalidation would leave stale env
	// overrides in place until the cache TTL expires.
	if h.invalidator != nil {
		h.invalidator.InvalidateOrg(orgID)
	}

	if len(changes) > 0 {
		orgIDStr := orgID.String()
		emitUserAudit(h.audit, r, models.AuditActionSettingsUpdated, models.AuditResourceSettings, &orgIDStr,
			marshalAuditDetails(h.logger, map[string]any{"changes": changes}))
	}
	patchLogger.Bool("noop", false).Msg("settings patch applied")
	writeJSON(w, http.StatusOK, models.SingleResponse[models.Organization]{Data: org})
}

// mergeSettingsJSON deep-merges a settings patch into the existing settings
// blob. Objects merge recursively (so patching
// `agent_config.codex.OPENAI_API_KEY` leaves sibling providers intact);
// arrays and scalars replace wholesale at whichever level they appear. This
// matches autosave semantics on the client, where every field mutation needs
// to be safe against concurrent edits of unrelated fields.
//
// Top-level shape is strict: the patch MUST be a JSON object. A top-level
// scalar, array, or explicit `null` is rejected rather than silently ignored
// — "no-op" settings PATCHes are a class of bug we want to surface loudly.
//
// Nested values are permissive by design: a nested `null` (e.g.
// `{"product_context": null}`) propagates through as a real JSON `null` that
// replaces the existing value at that key. This is how the client clears a
// nested object — see `TestMergeSettingsJSON_NullIncomingValueReplaces`. Do
// not confuse the two: top-level null is rejected, nested null clears.
func mergeSettingsJSON(existing, patch json.RawMessage) (json.RawMessage, error) {
	// Use UseNumber() on both sides so integers survive the round-trip as
	// json.Number rather than being promoted to float64 and re-serialized.
	// Without this, large integers (>2^53) would lose precision, and any
	// external diffing of the raw settings blob would see spurious changes
	// on values like `4` that Go's default encoder prints identically but
	// our test doubles compare byte-for-byte.
	base, err := decodeJSONObject(existing, true /* allowEmpty */)
	if err != nil {
		return nil, err
	}
	if len(patch) == 0 || bytes.Equal(bytes.TrimSpace(patch), []byte("null")) {
		return nil, errors.New("settings patch must be a JSON object, got null")
	}
	incoming, err := decodeJSONObject(patch, false /* allowEmpty */)
	if err != nil {
		return nil, err
	}
	merged := deepMergeMap(base, incoming)
	return json.Marshal(merged)
}

// decodeJSONObject unmarshals a JSON object into map[string]any while keeping
// numeric literals as json.Number. An empty/nil input is treated as an empty
// object when `allowEmpty` is set; otherwise it's an error.
func decodeJSONObject(raw json.RawMessage, allowEmpty bool) (map[string]any, error) {
	if len(raw) == 0 {
		if allowEmpty {
			return map[string]any{}, nil
		}
		return nil, errors.New("settings patch must be a JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var out map[string]any
	if err := decoder.Decode(&out); err != nil {
		return nil, err
	}
	if out == nil {
		if allowEmpty {
			return map[string]any{}, nil
		}
		return nil, errors.New("settings patch must be a JSON object")
	}
	return out, nil
}

// deepMergeMap returns a new map where keys from `incoming` overlay `base`.
// When both sides hold an object at the same key, it recurses; otherwise the
// incoming value replaces. Arrays are replaced (not index-merged) — removing
// an element from a list is a valid edit and must propagate.
func deepMergeMap(base, incoming map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(incoming))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range incoming {
		if existing, ok := out[k]; ok {
			if existingMap, isMap := existing.(map[string]any); isMap {
				if incomingMap, isIncomingMap := v.(map[string]any); isIncomingMap {
					out[k] = deepMergeMap(existingMap, incomingMap)
					continue
				}
			}
		}
		out[k] = v
	}
	return out
}

func sortedSettingsChangeKeys(changes map[string]any) []string {
	keys := make([]string, 0, len(changes))
	for key := range changes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// platformLLMProviders returns the set of provider names that have a
// platform-default key from the server environment.
func (h *SettingsHandler) platformLLMProviders() map[string]bool {
	out := make(map[string]bool, len(h.llmDefaults))
	for provider := range h.llmDefaults {
		out[provider] = true
	}
	return out
}

func topLevelSettingsPatchKeys(raw *json.RawMessage) []string {
	if raw == nil || len(*raw) == 0 {
		return nil
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(*raw, &payload); err != nil {
		return nil
	}
	keys := make([]string, 0, len(payload))
	for key := range payload {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

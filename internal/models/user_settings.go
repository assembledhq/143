package models

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
)

// UserSettings is the strongly-typed representation of users.settings JSONB.
type UserSettings struct {
	CodingAgentModelDefault      string                        `json:"coding_agent_model_default,omitempty"`
	CodingAgentReasoningDefaults map[AgentType]ReasoningEffort `json:"coding_agent_reasoning_defaults,omitempty"`
	DiffViewerFullScreen         bool                          `json:"diff_viewer_full_screen,omitempty"`
	ManualSessionPlanesHidden    bool                          `json:"manual_session_planes_hidden,omitempty"`
}

// ParseUserSettings deserializes the JSONB settings column into UserSettings.
func ParseUserSettings(raw json.RawMessage) (UserSettings, error) {
	var s UserSettings
	if len(raw) == 0 {
		return s, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&s); err != nil {
		return UserSettings{}, fmt.Errorf("unmarshal user settings: %w", err)
	}
	if err := s.Validate(); err != nil {
		return UserSettings{}, err
	}
	return s, nil
}

// MarshalJSONB validates and serializes the settings document for storage.
func (s UserSettings) MarshalJSONB() (json.RawMessage, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("marshal user settings: %w", err)
	}
	return encoded, nil
}

// userSettingsJSONKeys returns the set of top-level JSON keys a settings
// merge patch may reference, derived from the UserSettings struct tags so it
// can't drift when fields are added.
var userSettingsJSONKeys = sync.OnceValue(func() map[string]struct{} {
	keys := map[string]struct{}{}
	t := reflect.TypeOf(UserSettings{})
	for i := 0; i < t.NumField(); i++ {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name != "" && name != "-" {
			keys[name] = struct{}{}
		}
	}
	return keys
})

// ApplyUserSettingsMergePatch applies an RFC 7386 JSON merge patch to the
// current settings document and returns the validated result. Keys absent
// from the patch keep their stored value, null clears the stored value, and
// nested objects merge per key — so callers send only the fields they are
// changing instead of rebuilding the whole document client-side.
func ApplyUserSettingsMergePatch(current, patch json.RawMessage) (UserSettings, error) {
	var patchDoc map[string]any
	if err := json.Unmarshal(patch, &patchDoc); err != nil || patchDoc == nil {
		return UserSettings{}, fmt.Errorf("settings patch must be a JSON object")
	}
	for key := range patchDoc {
		if _, ok := userSettingsJSONKeys()[key]; !ok {
			return UserSettings{}, fmt.Errorf("unknown settings field %q", key)
		}
	}
	currentDoc := map[string]any{}
	if len(current) > 0 {
		if err := json.Unmarshal(current, &currentDoc); err != nil {
			return UserSettings{}, fmt.Errorf("unmarshal current user settings: %w", err)
		}
	}
	merged, err := json.Marshal(mergeJSONObjects(currentDoc, patchDoc))
	if err != nil {
		return UserSettings{}, fmt.Errorf("marshal merged user settings: %w", err)
	}
	return ParseUserSettings(merged)
}

// mergeJSONObjects implements the RFC 7386 merge rules for two decoded JSON
// objects: null patch values delete the key, object values merge recursively,
// and everything else replaces the target value.
func mergeJSONObjects(target, patch map[string]any) map[string]any {
	merged := make(map[string]any, len(target)+len(patch))
	for key, value := range target {
		merged[key] = value
	}
	for key, value := range patch {
		if value == nil {
			delete(merged, key)
			continue
		}
		patchObj, ok := value.(map[string]any)
		if !ok {
			merged[key] = value
			continue
		}
		targetObj, ok := merged[key].(map[string]any)
		if !ok {
			targetObj = map[string]any{}
		}
		merged[key] = mergeJSONObjects(targetObj, patchObj)
	}
	return merged
}

// Validate returns an error if any user setting is invalid.
func (s UserSettings) Validate() error {
	if s.CodingAgentModelDefault != "" {
		agentType := AgentTypeForModel(s.CodingAgentModelDefault)
		if agentType == "" {
			return fmt.Errorf("coding_agent_model_default: unknown model %q", s.CodingAgentModelDefault)
		}
		if err := ValidateModelForAgentType(agentType, s.CodingAgentModelDefault); err != nil {
			return fmt.Errorf("coding_agent_model_default: %w", err)
		}
	}
	for agentType, effort := range s.CodingAgentReasoningDefaults {
		if err := agentType.Validate(); err != nil {
			return fmt.Errorf("coding_agent_reasoning_defaults.%s: %w", agentType, err)
		}
		if err := effort.Validate(); err != nil {
			return fmt.Errorf("coding_agent_reasoning_defaults.%s: %w", agentType, err)
		}
		if effort == "" {
			return fmt.Errorf("coding_agent_reasoning_defaults.%s: reasoning effort cannot be empty", agentType)
		}
		if !agentType.SupportsReasoningEffortLevel(effort) {
			return fmt.Errorf("coding_agent_reasoning_defaults.%s: reasoning effort %q is not supported", agentType, effort)
		}
	}
	return nil
}

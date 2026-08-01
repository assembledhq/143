package models

import (
	"encoding/json"
	"fmt"
)

// RepositorySettings is the typed subset of repositories.settings owned by
// the platform. Unknown keys remain valid because other repository features
// share the same JSON document.
type RepositorySettings struct {
	PRHandoffMode PRHandoffMode `json:"pr_handoff_mode,omitempty"`
}

func ParseRepositorySettings(raw json.RawMessage) (RepositorySettings, error) {
	settings := RepositorySettings{PRHandoffMode: PRHandoffModePrePublish}
	if len(raw) == 0 {
		return settings, nil
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil || doc == nil {
		return RepositorySettings{}, fmt.Errorf("repository settings must be a JSON object")
	}
	if value, ok := doc["pr_handoff_mode"]; ok && string(value) != "null" {
		if err := json.Unmarshal(value, &settings.PRHandoffMode); err != nil {
			return RepositorySettings{}, fmt.Errorf("pr_handoff_mode must be a string")
		}
	}
	if settings.PRHandoffMode == "" {
		settings.PRHandoffMode = PRHandoffModePrePublish
	}
	if err := settings.PRHandoffMode.Validate(); err != nil {
		return RepositorySettings{}, err
	}
	return settings, nil
}

// ApplyRepositorySettingsMergePatch applies RFC 7386 semantics while
// preserving settings keys owned by other repository features.
func ApplyRepositorySettingsMergePatch(current, patch json.RawMessage) (json.RawMessage, RepositorySettings, error) {
	var patchDoc map[string]any
	if err := json.Unmarshal(patch, &patchDoc); err != nil || patchDoc == nil {
		return nil, RepositorySettings{}, fmt.Errorf("settings patch must be a JSON object")
	}
	if _, removed := patchDoc["pm"]; removed {
		return nil, RepositorySettings{}, fmt.Errorf("repository PM settings are no longer supported")
	}
	currentDoc := map[string]any{}
	if len(current) > 0 {
		if err := json.Unmarshal(current, &currentDoc); err != nil || currentDoc == nil {
			return nil, RepositorySettings{}, fmt.Errorf("current repository settings must be a JSON object")
		}
	}
	merged := mergeJSONObjects(currentDoc, patchDoc)
	encoded, err := json.Marshal(merged)
	if err != nil {
		return nil, RepositorySettings{}, fmt.Errorf("marshal merged repository settings: %w", err)
	}
	settings, err := ParseRepositorySettings(encoded)
	if err != nil {
		return nil, RepositorySettings{}, err
	}
	return encoded, settings, nil
}

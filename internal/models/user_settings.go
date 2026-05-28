package models

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// UserSettings is the strongly-typed representation of users.settings JSONB.
type UserSettings struct {
	CodingAgentDefaultModel      string                        `json:"coding_agent_default_model,omitempty"`
	CodingAgentReasoningDefaults map[AgentType]ReasoningEffort `json:"coding_agent_reasoning_defaults,omitempty"`
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

// Validate returns an error if any user setting is invalid.
func (s UserSettings) Validate() error {
	if s.CodingAgentDefaultModel != "" {
		agentType := AgentTypeForModel(s.CodingAgentDefaultModel)
		if agentType == "" {
			return fmt.Errorf("coding_agent_default_model: unknown model %q", s.CodingAgentDefaultModel)
		}
		if err := ValidateModelForAgentType(agentType, s.CodingAgentDefaultModel); err != nil {
			return fmt.Errorf("coding_agent_default_model: %w", err)
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

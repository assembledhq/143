package adapters

import (
	"encoding/json"
	"strings"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
)

func normalizeGenericHumanInputEvent(line []byte, provider models.AgentType) (agent.HumanInputRequest, bool) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(line, &raw); err != nil {
		return agent.HumanInputRequest{}, false
	}

	eventType := rawString(raw, "type")
	if !isHumanInputEventType(eventType) {
		return agent.HumanInputRequest{}, false
	}

	kind := models.HumanInputRequestKind(rawString(raw, "request_kind", "kind"))
	if kind == "" {
		kind = defaultHumanInputKind(eventType)
	}
	if err := kind.Validate(); err != nil {
		kind = defaultHumanInputKind(eventType)
	}

	choices := rawChoices(raw, "choices", "options", "actions")
	if kind == models.HumanInputRequestKindFreeText && len(choices) > 0 {
		kind = models.HumanInputRequestKindSingleChoice
	}

	title := rawString(raw, "title", "header")
	if title == "" {
		title = defaultHumanInputTitle(provider, kind)
	}
	body := rawString(raw, "body", "question", "message", "content", "prompt")
	if body == "" {
		body = "The agent is waiting for human input."
	}

	var contextText *string
	if context := rawString(raw, "context"); context != "" {
		contextText = &context
	}
	var blocksPhase *string
	if phase := rawString(raw, "blocks_phase", "phase"); phase != "" {
		blocksPhase = &phase
	}

	responseSchema := raw["response_schema"]
	return agent.HumanInputRequest{
		ProviderRequestID: rawString(raw, "provider_request_id", "request_id", "id", "call_id"),
		Kind:              kind,
		Title:             title,
		Body:              body,
		Context:           contextText,
		BlocksPhase:       blocksPhase,
		Choices:           choices,
		ResponseSchema:    responseSchema,
		ProviderPayload:   append(json.RawMessage(nil), line...),
	}, true
}

func isHumanInputEventType(eventType string) bool {
	switch eventType {
	case "human_input_request", "human_input", "question", "approval_request", "tool_approval", "action_choice":
		return true
	default:
		return false
	}
}

func defaultHumanInputKind(eventType string) models.HumanInputRequestKind {
	switch eventType {
	case "approval_request", "tool_approval":
		return models.HumanInputRequestKindToolApproval
	case "action_choice":
		return models.HumanInputRequestKindActionChoice
	default:
		return models.HumanInputRequestKindFreeText
	}
}

func defaultHumanInputTitle(provider models.AgentType, kind models.HumanInputRequestKind) string {
	switch kind {
	case models.HumanInputRequestKindToolApproval:
		return "Approve agent action?"
	case models.HumanInputRequestKindActionChoice:
		return "Choose next action"
	default:
		if provider != "" {
			return string(provider) + " needs input"
		}
		return "Agent needs input"
	}
}

func rawString(raw map[string]json.RawMessage, keys ...string) string {
	for _, key := range keys {
		value, ok := raw[key]
		if !ok || len(value) == 0 {
			continue
		}
		var s string
		if err := json.Unmarshal(value, &s); err == nil && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func rawChoices(raw map[string]json.RawMessage, keys ...string) []models.HumanInputChoice {
	for _, key := range keys {
		value, ok := raw[key]
		if !ok || len(value) == 0 {
			continue
		}
		var choicesRaw []json.RawMessage
		if err := json.Unmarshal(value, &choicesRaw); err != nil {
			continue
		}
		seen := map[string]int{}
		choices := make([]models.HumanInputChoice, 0, len(choicesRaw))
		for _, rawOption := range choicesRaw {
			choice := normalizeClaudeQuestionOption(rawOption, seen)
			if choice.ID != "" && choice.Label != "" {
				choices = append(choices, choice)
			}
		}
		return choices
	}
	return nil
}

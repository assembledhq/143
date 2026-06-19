package models

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/xeipuuv/gojsonschema"
)

// HumanInputRequestKind classifies the shape of input an agent needs from a
// human before it can continue.
type HumanInputRequestKind string

const (
	HumanInputRequestKindFreeText     HumanInputRequestKind = "free_text"
	HumanInputRequestKindSingleChoice HumanInputRequestKind = "single_choice"
	HumanInputRequestKindMultiChoice  HumanInputRequestKind = "multi_choice"
	HumanInputRequestKindToolApproval HumanInputRequestKind = "tool_approval"
	HumanInputRequestKindActionChoice HumanInputRequestKind = "action_choice"
)

func (k HumanInputRequestKind) Validate() error {
	switch k {
	case HumanInputRequestKindFreeText,
		HumanInputRequestKindSingleChoice,
		HumanInputRequestKindMultiChoice,
		HumanInputRequestKindToolApproval,
		HumanInputRequestKindActionChoice:
		return nil
	default:
		return fmt.Errorf("invalid HumanInputRequestKind: %q", k)
	}
}

func (k HumanInputRequestKind) AcceptsFreeText() bool {
	return k == HumanInputRequestKindFreeText
}

// HumanInputRequestStatus captures the lifecycle of a durable human-input
// request.
type HumanInputRequestStatus string

const (
	HumanInputRequestStatusPending    HumanInputRequestStatus = "pending"
	HumanInputRequestStatusAnswered   HumanInputRequestStatus = "answered"
	HumanInputRequestStatusCancelled  HumanInputRequestStatus = "cancelled"
	HumanInputRequestStatusExpired    HumanInputRequestStatus = "expired"
	HumanInputRequestStatusSuperseded HumanInputRequestStatus = "superseded"
)

func (s HumanInputRequestStatus) Validate() error {
	switch s {
	case HumanInputRequestStatusPending,
		HumanInputRequestStatusAnswered,
		HumanInputRequestStatusCancelled,
		HumanInputRequestStatusExpired,
		HumanInputRequestStatusSuperseded:
		return nil
	default:
		return fmt.Errorf("invalid HumanInputRequestStatus: %q", s)
	}
}

type HumanInputSensitivity string

const (
	HumanInputSensitivityTeam      HumanInputSensitivity = "team"
	HumanInputSensitivityPersonal  HumanInputSensitivity = "personal"
	HumanInputSensitivitySensitive HumanInputSensitivity = "sensitive"
)

func (s HumanInputSensitivity) Validate() error {
	switch s {
	case HumanInputSensitivityTeam, HumanInputSensitivityPersonal, HumanInputSensitivitySensitive:
		return nil
	default:
		return fmt.Errorf("invalid HumanInputSensitivity: %q", s)
	}
}

type HumanInputPreferredChannel string

const (
	HumanInputPreferredChannelSlackThread HumanInputPreferredChannel = "slack_thread"
	HumanInputPreferredChannelSlackDM     HumanInputPreferredChannel = "slack_dm"
	HumanInputPreferredChannelWeb         HumanInputPreferredChannel = "web"
)

func (c HumanInputPreferredChannel) Validate() error {
	switch c {
	case HumanInputPreferredChannelSlackThread, HumanInputPreferredChannelSlackDM, HumanInputPreferredChannelWeb:
		return nil
	default:
		return fmt.Errorf("invalid HumanInputPreferredChannel: %q", c)
	}
}

type HumanInputDeliveryTarget struct {
	AssignedUserID   *uuid.UUID                 `json:"assigned_user_id,omitempty"`
	Sensitivity      HumanInputSensitivity      `json:"sensitivity"`
	PreferredChannel HumanInputPreferredChannel `json:"preferred_channel"`
}

type HumanInputChoice struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Preview     string `json:"preview,omitempty"`
	Kind        string `json:"kind,omitempty"`
	Destructive bool   `json:"destructive,omitempty"`
}

type HumanInputRequest struct {
	ID                uuid.UUID                  `db:"id" json:"id"`
	OrgID             uuid.UUID                  `db:"org_id" json:"org_id"`
	SessionID         uuid.UUID                  `db:"session_id" json:"session_id"`
	ThreadID          *uuid.UUID                 `db:"thread_id" json:"thread_id,omitempty"`
	TurnNumber        int                        `db:"turn_number" json:"turn_number"`
	AgentType         AgentType                  `db:"agent_type" json:"agent_type"`
	ProviderRequestID *string                    `db:"provider_request_id" json:"provider_request_id,omitempty"`
	Kind              HumanInputRequestKind      `db:"request_kind" json:"request_kind"`
	Status            HumanInputRequestStatus    `db:"status" json:"status"`
	Title             string                     `db:"title" json:"title"`
	Body              string                     `db:"body" json:"body"`
	Context           *string                    `db:"context" json:"context,omitempty"`
	BlocksPhase       *string                    `db:"blocks_phase" json:"blocks_phase,omitempty"`
	AssignedUserID    *uuid.UUID                 `db:"assigned_user_id" json:"assigned_user_id,omitempty"`
	Sensitivity       HumanInputSensitivity      `db:"sensitivity" json:"sensitivity"`
	PreferredChannel  HumanInputPreferredChannel `db:"preferred_channel" json:"preferred_channel"`
	Choices           []HumanInputChoice         `db:"choices" json:"choices"`
	ResponseSchema    json.RawMessage            `db:"response_schema" json:"response_schema,omitempty"`
	ProviderPayload   json.RawMessage            `db:"provider_payload" json:"provider_payload,omitempty"`
	AnswerText        *string                    `db:"answer_text" json:"answer_text,omitempty"`
	AnswerPayload     json.RawMessage            `db:"answer_payload" json:"answer_payload,omitempty"`
	AnsweredBy        *uuid.UUID                 `db:"answered_by" json:"answered_by,omitempty"`
	AnsweredAt        *time.Time                 `db:"answered_at" json:"answered_at,omitempty"`
	ExpiresAt         *time.Time                 `db:"expires_at" json:"expires_at,omitempty"`
	CreatedAt         time.Time                  `db:"created_at" json:"created_at"`
}

type HumanInputAnswerInput struct {
	AnswerText        *string         `json:"answer_text,omitempty"`
	SelectedChoiceIDs []string        `json:"selected_choice_ids,omitempty"`
	AnswerPayload     json.RawMessage `json:"answer_payload,omitempty"`
}

func (r HumanInputRequest) ValidateAnswer(input HumanInputAnswerInput) error {
	if r.Status != "" && r.Status != HumanInputRequestStatusPending {
		return fmt.Errorf("human input request is %s", r.Status)
	}
	if err := r.Kind.Validate(); err != nil {
		return err
	}
	if err := validateHumanInputAnswerPayload(r.ResponseSchema, input.AnswerPayload); err != nil {
		return err
	}

	knownChoices := make(map[string]bool, len(r.Choices))
	for _, choice := range r.Choices {
		knownChoices[choice.ID] = true
	}
	for _, id := range input.SelectedChoiceIDs {
		if !knownChoices[id] {
			return fmt.Errorf("selected choice %q is not valid for this request", id)
		}
	}

	answerText := ""
	if input.AnswerText != nil {
		answerText = strings.TrimSpace(*input.AnswerText)
	}

	switch r.Kind {
	case HumanInputRequestKindFreeText:
		if answerText == "" {
			return fmt.Errorf("answer_text is required")
		}
		return nil
	case HumanInputRequestKindSingleChoice:
		if len(input.SelectedChoiceIDs) != 1 {
			return fmt.Errorf("exactly one selected_choice_id is required")
		}
		return nil
	case HumanInputRequestKindMultiChoice:
		if len(input.SelectedChoiceIDs) == 0 {
			return fmt.Errorf("at least one selected_choice_id is required")
		}
		return nil
	case HumanInputRequestKindToolApproval, HumanInputRequestKindActionChoice:
		if len(input.SelectedChoiceIDs) == 0 && answerText == "" && len(input.AnswerPayload) == 0 {
			return fmt.Errorf("a selected choice, answer_text, or answer_payload is required")
		}
		return nil
	default:
		return r.Kind.Validate()
	}
}

func validateHumanInputAnswerPayload(schema, payload json.RawMessage) error {
	if len(schema) == 0 {
		return nil
	}
	if len(payload) == 0 {
		return fmt.Errorf("answer_payload is required")
	}
	result, err := gojsonschema.Validate(
		gojsonschema.NewBytesLoader(schema),
		gojsonschema.NewBytesLoader(payload),
	)
	if err != nil {
		return fmt.Errorf("validate answer_payload against response_schema: %w", err)
	}
	if result.Valid() {
		return nil
	}
	parts := make([]string, 0, len(result.Errors()))
	for _, validationErr := range result.Errors() {
		parts = append(parts, validationErr.String())
	}
	return fmt.Errorf("answer_payload does not match response_schema: %s", strings.Join(parts, "; "))
}

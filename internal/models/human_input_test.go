package models

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHumanInputRequestKind_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		value     HumanInputRequestKind
		expectErr bool
	}{
		{name: "free text", value: HumanInputRequestKindFreeText},
		{name: "single choice", value: HumanInputRequestKindSingleChoice},
		{name: "multi choice", value: HumanInputRequestKindMultiChoice},
		{name: "tool approval", value: HumanInputRequestKindToolApproval},
		{name: "action choice", value: HumanInputRequestKindActionChoice},
		{name: "invalid", value: HumanInputRequestKind("bogus"), expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.value.Validate()
			if tt.expectErr {
				require.Error(t, err, "Validate should reject unknown human input request kinds")
				return
			}
			require.NoError(t, err, "Validate should accept known human input request kinds")
		})
	}
}

func TestHumanInputRequestStatus_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		value     HumanInputRequestStatus
		expectErr bool
	}{
		{name: "pending", value: HumanInputRequestStatusPending},
		{name: "answered", value: HumanInputRequestStatusAnswered},
		{name: "cancelled", value: HumanInputRequestStatusCancelled},
		{name: "expired", value: HumanInputRequestStatusExpired},
		{name: "superseded", value: HumanInputRequestStatusSuperseded},
		{name: "invalid", value: HumanInputRequestStatus("bogus"), expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.value.Validate()
			if tt.expectErr {
				require.Error(t, err, "Validate should reject unknown human input request statuses")
				return
			}
			require.NoError(t, err, "Validate should accept known human input request statuses")
		})
	}
}

func TestHumanInputRequestValidateAnswer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		request   HumanInputRequest
		answer    HumanInputAnswerInput
		expectErr bool
	}{
		{
			name:    "free text accepts answer text",
			request: HumanInputRequest{Kind: HumanInputRequestKindFreeText},
			answer:  HumanInputAnswerInput{AnswerText: stringPtr("Use React")},
		},
		{
			name:      "free text rejects empty answer",
			request:   HumanInputRequest{Kind: HumanInputRequestKindFreeText},
			answer:    HumanInputAnswerInput{},
			expectErr: true,
		},
		{
			name: "single choice accepts exactly one known choice",
			request: HumanInputRequest{
				Kind:    HumanInputRequestKindSingleChoice,
				Choices: []HumanInputChoice{{ID: "react", Label: "React"}, {ID: "vue", Label: "Vue"}},
			},
			answer: HumanInputAnswerInput{SelectedChoiceIDs: []string{"react"}},
		},
		{
			name: "single choice rejects two choices",
			request: HumanInputRequest{
				Kind:    HumanInputRequestKindSingleChoice,
				Choices: []HumanInputChoice{{ID: "react", Label: "React"}, {ID: "vue", Label: "Vue"}},
			},
			answer:    HumanInputAnswerInput{SelectedChoiceIDs: []string{"react", "vue"}},
			expectErr: true,
		},
		{
			name: "multi choice accepts multiple known choices",
			request: HumanInputRequest{
				Kind:    HumanInputRequestKindMultiChoice,
				Choices: []HumanInputChoice{{ID: "tests", Label: "Tests"}, {ID: "docs", Label: "Docs"}},
			},
			answer: HumanInputAnswerInput{SelectedChoiceIDs: []string{"tests", "docs"}},
		},
		{
			name: "tool approval rejects unknown choice",
			request: HumanInputRequest{
				Kind:    HumanInputRequestKindToolApproval,
				Choices: []HumanInputChoice{{ID: "approve", Label: "Approve"}, {ID: "deny", Label: "Deny"}},
			},
			answer:    HumanInputAnswerInput{SelectedChoiceIDs: []string{"edit"}},
			expectErr: true,
		},
		{
			name: "structured answer validates against response schema",
			request: HumanInputRequest{
				Kind: HumanInputRequestKindToolApproval,
				ResponseSchema: json.RawMessage(`{
					"type":"object",
					"required":["decision"],
					"properties":{"decision":{"type":"string","enum":["approve","deny"]}}
				}`),
			},
			answer: HumanInputAnswerInput{AnswerPayload: json.RawMessage(`{"decision":"approve"}`)},
		},
		{
			name: "structured answer rejects schema mismatch",
			request: HumanInputRequest{
				Kind: HumanInputRequestKindToolApproval,
				ResponseSchema: json.RawMessage(`{
					"type":"object",
					"required":["decision"],
					"properties":{"decision":{"type":"string","enum":["approve","deny"]}}
				}`),
			},
			answer:    HumanInputAnswerInput{AnswerPayload: json.RawMessage(`{"decision":"edit"}`)},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.request.ValidateAnswer(tt.answer)
			if tt.expectErr {
				require.Error(t, err, "ValidateAnswer should reject invalid answers")
				return
			}
			require.NoError(t, err, "ValidateAnswer should accept valid answers")
		})
	}
}

func stringPtr(s string) *string {
	return &s
}

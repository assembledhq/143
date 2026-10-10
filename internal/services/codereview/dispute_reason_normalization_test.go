package codereview

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestDisputeTriageFiltersLLMReasonCodesBeforeStrictValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                           string
		requested, available, expected []models.CodeReviewRiskReasonCode
		direction                      models.CodeReviewDisputeDirection
		routing                        models.CodeReviewDisputeRouting
		confidence                     float64
		fail                           bool
	}{
		{name: "invented reason removed", requested: []models.CodeReviewRiskReasonCode{"blocking_findings_unresolved_in_review"}, available: []models.CodeReviewRiskReasonCode{models.CodeReviewRiskReasonBlockingFindings}, expected: []models.CodeReviewRiskReasonCode{}},
		{name: "valid unavailable reason removed", requested: []models.CodeReviewRiskReasonCode{models.CodeReviewRiskReasonDescriptionFailed}, available: []models.CodeReviewRiskReasonCode{models.CodeReviewRiskReasonBlockingFindings}, expected: []models.CodeReviewRiskReasonCode{}},
		{name: "known available code deduplicated", requested: []models.CodeReviewRiskReasonCode{models.CodeReviewRiskReasonBlockingFindings, models.CodeReviewRiskReasonBlockingFindings, "invented"}, available: []models.CodeReviewRiskReasonCode{models.CodeReviewRiskReasonBlockingFindings}, expected: []models.CodeReviewRiskReasonCode{models.CodeReviewRiskReasonBlockingFindings}},
		{name: "no available reasons", requested: []models.CodeReviewRiskReasonCode{models.CodeReviewRiskReasonBlockingFindings}, expected: []models.CodeReviewRiskReasonCode{}},
		{name: "invalid direction replaced by stored decision", direction: "invalid", expected: []models.CodeReviewRiskReasonCode{}},
		{name: "invalid routing still rejected", routing: "invalid", fail: true},
		{name: "invalid confidence still rejected", confidence: 2, fail: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			direction := tt.direction
			if direction == "" {
				direction = models.CodeReviewDisputeDirectionShouldHaveApproved
			}
			routing := tt.routing
			if routing == "" {
				routing = models.CodeReviewDisputeRoutingReassess
			}
			confidence := tt.confidence
			if confidence == 0 {
				confidence = .99
			}
			raw, err := json.Marshal(models.CodeReviewDisputeTriageResult{Direction: direction, Routing: routing, Confidence: confidence, ContestedReasonCodes: tt.requested, DisputeKind: "new_evidence", AssertsNewInformation: true})
			require.NoError(t, err, "LLM fixture should encode")
			service := &DisputeService{llm: &disputeLLMStub{response: string(raw)}}
			result, err := service.triageResult(context.Background(), models.CodeReviewDispute{Source: models.CodeReviewDisputeSourceGitHubComment, Decision: models.CodeReviewDecisionBlocked}, tt.available, models.CodeReviewListItem{}, nil, nil)
			if tt.fail {
				require.Error(t, err, "normalization must not relax routing or confidence validation")
				return
			}
			require.NoError(t, err, "invented optional reasons should not exhaust valid triage jobs")
			require.Equal(t, models.CodeReviewDisputeTriageResult{Direction: models.CodeReviewDisputeDirectionShouldHaveApproved, Routing: routing, Confidence: confidence, ContestedReasonCodes: tt.expected, DisputeKind: "new_evidence", AssertsNewInformation: true}, result, "triage should derive direction and constrain reason suggestions while preserving valid routing")
		})
	}
}

func TestDisputeTriageNormalizationPreservesPolicyGates(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                  string
		decision              models.CodeReviewDecision
		reason                models.CodeReviewRiskReasonCode
		association           string
		nonAuthor             bool
		lowConfidence         bool
		reassessmentsDisabled bool
		expected              models.CodeReviewDisputeRouting
	}{
		{name: "filed deterministic reasons survive fallback", decision: models.CodeReviewDecisionBlocked, reason: models.CodeReviewRiskReasonFilesLimitExceeded, association: "MEMBER", expected: models.CodeReviewDisputeRoutingPolicySignalOnly},
		{name: "untrusted filing cannot start reassessment", decision: models.CodeReviewDecisionBlocked, reason: models.CodeReviewRiskReasonBlockingFindings, association: "NONE", expected: models.CodeReviewDisputeRoutingPolicySignalOnly},
		{name: "approval is monotonic", decision: models.CodeReviewDecisionApproved, reason: models.CodeReviewRiskReasonBlockingFindings, association: "MEMBER", expected: models.CodeReviewDisputeRoutingPolicySignalOnly},
		{name: "trusted non-author cannot start reassessment", decision: models.CodeReviewDecisionBlocked, reason: models.CodeReviewRiskReasonBlockingFindings, association: "MEMBER", nonAuthor: true, expected: models.CodeReviewDisputeRoutingPolicySignalOnly},
		{name: "low confidence cannot start reassessment", decision: models.CodeReviewDecisionBlocked, reason: models.CodeReviewRiskReasonBlockingFindings, association: "MEMBER", lowConfidence: true, expected: models.CodeReviewDisputeRoutingPolicySignalOnly},
		{name: "disabled reassessments remain disabled", decision: models.CodeReviewDecisionBlocked, reason: models.CodeReviewRiskReasonBlockingFindings, association: "MEMBER", reassessmentsDisabled: true, expected: models.CodeReviewDisputeRoutingPolicySignalOnly},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID, disputeID := uuid.New(), uuid.New()
			store := &captureDisputeStore{current: models.CodeReviewDispute{ID: disputeID, OrgID: orgID, SessionID: uuid.New(), Source: models.CodeReviewDisputeSourceGitHubComment, Decision: tt.decision, ContestedReasonCodes: []models.CodeReviewRiskReasonCode{tt.reason}, Body: "The evidence changed.", AuthorAssociation: tt.association, RepositoryVisibility: models.CodeReviewRepositoryVisibilityPublic, AuthorIsPRAuthor: !tt.nonAuthor, IntakeStatus: models.CodeReviewDisputeIntakePending, ReassessmentStatus: models.CodeReviewDisputeReassessmentNotRequested}}
			confidence := .99
			if tt.lowConfidence {
				confidence = .5
			}
			raw, err := json.Marshal(models.CodeReviewDisputeTriageResult{
				Direction: "reassess", ContestedReasonCodes: []models.CodeReviewRiskReasonCode{"blocking_findings_unresolved_in_review"},
				DisputeKind: "new_evidence", AssertsNewInformation: true,
				Routing: models.CodeReviewDisputeRoutingReassess, Confidence: confidence, Reply: "Check the evidence.",
			})
			require.NoError(t, err, "encode malformed direction and reason suggestions for each policy gate")
			client := &disputeLLMStub{response: string(raw)}
			service := NewDisputeService(store, disputeReviewStoreStub{reasons: []models.CodeReviewRiskReasonCode{tt.reason}}, disputePullRequestStoreStub{}, &disputeJobStoreStub{}, client, "", zerolog.Nop(), DisputeConfig{ReassessmentsEnabled: !tt.reassessmentsDisabled})
			require.NoError(t, service.Triage(context.Background(), orgID, disputeID), "filtered optional codes must preserve trusted filing fallback")
			require.Equal(t, []models.CodeReviewRiskReasonCode{tt.reason}, store.triage.ContestedReasonCodes, "trusted stored filing reasons should remain the fallback")
			require.Equal(t, tt.expected, store.triage.Routing, "normalization must preserve deterministic, trust, author, approval, confidence and configuration gates")
			require.False(t, store.admitted, "protected routes must not reach reassessment admission")
		})
	}
}

func TestDisputeTriageDirectionComesFromStoredDecision(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		decision      models.CodeReviewDecision
		directionJSON string
		expected      models.CodeReviewDisputeDirection
	}{
		{name: "reassess routing word on blocked decision", decision: models.CodeReviewDecisionBlocked, directionJSON: `"direction":"reassess",`, expected: models.CodeReviewDisputeDirectionShouldHaveApproved},
		{name: "policy routing word on approved decision", decision: models.CodeReviewDecisionApproved, directionJSON: `"direction":"policy_signal_only",`, expected: models.CodeReviewDisputeDirectionShouldNotHaveApproved},
		{name: "unknown direction on blocked decision", decision: models.CodeReviewDecisionBlocked, directionJSON: `"direction":"unknown",`, expected: models.CodeReviewDisputeDirectionShouldHaveApproved},
		{name: "unknown direction on approved decision", decision: models.CodeReviewDecisionApproved, directionJSON: `"direction":"unknown",`, expected: models.CodeReviewDisputeDirectionShouldNotHaveApproved},
		{name: "missing direction on blocked decision", decision: models.CodeReviewDecisionBlocked, expected: models.CodeReviewDisputeDirectionShouldHaveApproved},
		{name: "missing direction on approved decision", decision: models.CodeReviewDecisionApproved, expected: models.CodeReviewDisputeDirectionShouldNotHaveApproved},
		{name: "missing direction on comment only decision", decision: models.CodeReviewDecisionCommentOnly, expected: models.CodeReviewDisputeDirectionShouldHaveApproved},
		{name: "missing direction on human review decision", decision: models.CodeReviewDecisionNeedsHumanReview, expected: models.CodeReviewDisputeDirectionShouldHaveApproved},
		{name: "mismatched direction on blocked decision", decision: models.CodeReviewDecisionBlocked, directionJSON: `"direction":"should_not_have_approved",`, expected: models.CodeReviewDisputeDirectionShouldHaveApproved},
		{name: "mismatched direction on approved decision", decision: models.CodeReviewDecisionApproved, directionJSON: `"direction":"should_have_approved",`, expected: models.CodeReviewDisputeDirectionShouldNotHaveApproved},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client := &disputeLLMStub{response: `{` + tt.directionJSON + `"routing":"reassess","confidence":0.99,"dispute_kind":"new_evidence","asserts_new_information":true,"reply":"Check the evidence."}`}
			service := &DisputeService{llm: client}
			result, err := service.triageResult(context.Background(), models.CodeReviewDispute{Source: models.CodeReviewDisputeSourceGitHubComment, Decision: tt.decision}, nil, models.CodeReviewListItem{}, nil, nil)
			require.NoError(t, err, "stored decision should provide direction before model output validation")
			require.Equal(t, models.CodeReviewDisputeTriageResult{
				Direction: tt.expected, ContestedReasonCodes: []models.CodeReviewRiskReasonCode{},
				Routing: models.CodeReviewDisputeRoutingReassess, Confidence: .99,
				DisputeKind: "new_evidence", AssertsNewInformation: true, Reply: "Check the evidence.",
			}, result, "only direction and absent optional reasons should be normalized before policy gates")
		})
	}
}

func TestDisputeTriageNormalizationPreservesDecodeAndValidationErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, response, expectedError string
	}{
		{name: "malformed JSON", response: `{"direction":"reassess","routing":`, expectedError: "decode code review dispute triage"},
		{name: "wrong direction type", response: `{"direction":42,"routing":"reassess","confidence":0.99}`, expectedError: "decode code review dispute triage"},
		{name: "invalid routing", response: `{"direction":"reassess","routing":"invalid","confidence":0.99}`, expectedError: "invalid CodeReviewDisputeRouting"},
		{name: "missing routing", response: `{"direction":"reassess","confidence":0.99}`, expectedError: "invalid CodeReviewDisputeRouting"},
		{name: "confidence above one", response: `{"direction":"reassess","routing":"reassess","confidence":2}`, expectedError: "confidence must be between 0 and 1"},
		{name: "negative confidence", response: `{"direction":"reassess","routing":"reassess","confidence":-1}`, expectedError: "confidence must be between 0 and 1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			service := &DisputeService{llm: &disputeLLMStub{response: tt.response}}
			_, err := service.triageResult(context.Background(), models.CodeReviewDispute{Source: models.CodeReviewDisputeSourceGitHubComment, Decision: models.CodeReviewDecisionBlocked}, nil, models.CodeReviewListItem{}, nil, nil)
			require.ErrorContains(t, err, tt.expectedError, "direction normalization must preserve the remaining JSON and model validation failures")
		})
	}
}

func TestDisputeTriagePersistsNonNilReasonCodes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, reasonsJSON string
		stored, expected  []models.CodeReviewRiskReasonCode
	}{
		{name: "empty model reasons without stored reasons", reasonsJSON: `[]`, expected: []models.CodeReviewRiskReasonCode{}},
		{name: "null model reasons without stored reasons", reasonsJSON: `null`, expected: []models.CodeReviewRiskReasonCode{}},
		{name: "empty stored reasons remain nonnil", reasonsJSON: `[]`, stored: []models.CodeReviewRiskReasonCode{}, expected: []models.CodeReviewRiskReasonCode{}},
		{name: "invented model reasons filtered to empty", reasonsJSON: `["invented"]`, expected: []models.CodeReviewRiskReasonCode{}},
		{name: "unavailable model reasons filtered to empty", reasonsJSON: `["description_failed"]`, expected: []models.CodeReviewRiskReasonCode{}},
		{name: "stored fallback reasons preserved", reasonsJSON: `["invented"]`, stored: []models.CodeReviewRiskReasonCode{models.CodeReviewRiskReasonDescriptionFailed, models.CodeReviewRiskReasonBlockingFindings}, expected: []models.CodeReviewRiskReasonCode{models.CodeReviewRiskReasonDescriptionFailed, models.CodeReviewRiskReasonBlockingFindings}},
		{name: "valid model reasons preserved", reasonsJSON: `["blocking_findings"]`, expected: []models.CodeReviewRiskReasonCode{models.CodeReviewRiskReasonBlockingFindings}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID, disputeID := uuid.New(), uuid.New()
			stored := slices.Clone(tt.stored)
			store := &captureDisputeStore{current: models.CodeReviewDispute{
				ID: disputeID, OrgID: orgID, SessionID: uuid.New(), Source: models.CodeReviewDisputeSourceGitHubComment,
				Decision: models.CodeReviewDecisionBlocked, ContestedReasonCodes: stored,
				Body: "Please explain the decision.", AuthorAssociation: "MEMBER", AuthorIsPRAuthor: true,
				IntakeStatus: models.CodeReviewDisputeIntakePending, ReassessmentStatus: models.CodeReviewDisputeReassessmentNotRequested,
			}}
			client := &disputeLLMStub{response: `{"direction":"should_have_approved","contested_reason_codes":` + tt.reasonsJSON + `,"routing":"answer_only","confidence":0.99,"reply":"Here is the evidence."}`}
			service := NewDisputeService(store, disputeReviewStoreStub{reasons: []models.CodeReviewRiskReasonCode{models.CodeReviewRiskReasonBlockingFindings}}, disputePullRequestStoreStub{}, &disputeJobStoreStub{}, client, "", zerolog.Nop())
			require.NoError(t, service.Triage(context.Background(), orgID, disputeID), "triage should reach persistence with normalized reason codes")
			require.Equal(t, tt.expected, store.triage.ContestedReasonCodes, "persistence must receive a nonnil array with the exact selected or stored fallback reasons")
			require.Equal(t, models.CodeReviewDisputeRoutingAnswerOnly, store.triage.Routing, "empty or fallback reasons should preserve the answer-only route")
			if len(store.triage.ContestedReasonCodes) > 0 {
				store.triage.ContestedReasonCodes[0] = models.CodeReviewRiskReasonFilesLimitExceeded
			}
			require.Equal(t, tt.stored, store.current.ContestedReasonCodes, "normalized and fallback arrays must not share mutable storage with the filed dispute")
		})
	}
}

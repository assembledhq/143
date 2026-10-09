package codereview

import (
	"context"
	"encoding/json"
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
		{name: "invalid direction still rejected", direction: "invalid", fail: true},
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
				require.Error(t, err, "normalization must not relax routing, direction or confidence validation")
				return
			}
			require.NoError(t, err, "invented optional reasons should not exhaust valid triage jobs")
			require.Equal(t, models.CodeReviewDisputeTriageResult{Direction: direction, Routing: routing, Confidence: confidence, ContestedReasonCodes: tt.expected, DisputeKind: "new_evidence", AssertsNewInformation: true}, result, "only constrained reason suggestions should change")
		})
	}
}

func TestDisputeTriageFilteredReasonsPreservePolicyGates(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		decision    models.CodeReviewDecision
		reason      models.CodeReviewRiskReasonCode
		association string
		expected    models.CodeReviewDisputeRouting
	}{
		{name: "filed deterministic reasons survive fallback", decision: models.CodeReviewDecisionBlocked, reason: models.CodeReviewRiskReasonFilesLimitExceeded, association: "MEMBER", expected: models.CodeReviewDisputeRoutingPolicySignalOnly},
		{name: "untrusted filing cannot start reassessment", decision: models.CodeReviewDecisionBlocked, reason: models.CodeReviewRiskReasonBlockingFindings, association: "NONE", expected: models.CodeReviewDisputeRoutingPolicySignalOnly},
		{name: "approval is monotonic", decision: models.CodeReviewDecisionApproved, reason: models.CodeReviewRiskReasonBlockingFindings, association: "MEMBER", expected: models.CodeReviewDisputeRoutingPolicySignalOnly},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID, disputeID := uuid.New(), uuid.New()
			store := &captureDisputeStore{current: models.CodeReviewDispute{ID: disputeID, OrgID: orgID, SessionID: uuid.New(), Source: models.CodeReviewDisputeSourceGitHubComment, Decision: tt.decision, ContestedReasonCodes: []models.CodeReviewRiskReasonCode{tt.reason}, Body: "The evidence changed.", AuthorAssociation: tt.association, RepositoryVisibility: models.CodeReviewRepositoryVisibilityPublic, AuthorIsPRAuthor: true, IntakeStatus: models.CodeReviewDisputeIntakePending, ReassessmentStatus: models.CodeReviewDisputeReassessmentNotRequested}}
			client := &disputeLLMStub{response: `{"direction":"should_have_approved","contested_reason_codes":["blocking_findings_unresolved_in_review"],"dispute_kind":"new_evidence","asserts_new_information":true,"routing":"reassess","confidence":0.99,"reply":"Check the evidence."}`}
			service := NewDisputeService(store, disputeReviewStoreStub{reasons: []models.CodeReviewRiskReasonCode{tt.reason}}, disputePullRequestStoreStub{}, &disputeJobStoreStub{}, client, "", zerolog.Nop(), DisputeConfig{ReassessmentsEnabled: true})
			require.NoError(t, service.Triage(context.Background(), orgID, disputeID), "filtered optional codes must preserve trusted filing fallback")
			require.Equal(t, []models.CodeReviewRiskReasonCode{tt.reason}, store.triage.ContestedReasonCodes, "trusted stored filing reasons should remain the fallback")
			require.Equal(t, tt.expected, store.triage.Routing, "normalization must preserve deterministic, trust and approval safety gates")
			require.False(t, store.admitted, "protected routes must not reach reassessment admission")
		})
	}
}

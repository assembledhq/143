package worker

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func newWorkerUncertaintyReason() models.CodeReviewUncertaintyReason {
	return models.CodeReviewUncertaintyReason{
		MissingEvidence:    "The installed TanStack query declaration for QueryObserverResult.",
		MaterialImpact:     "The changed result assignment may exclude the pending state and fail type compatibility.",
		ResolutionEvidence: "The QueryObserverResult declaration at the version pinned in the lockfile.",
	}
}

func newWorkerUncertaintySynthesis() codeReviewOrchestratorSynthesis {
	return codeReviewOrchestratorSynthesis{
		ApprovalRecommended:    true,
		DescriptionAssessments: []codeReviewDescriptionAssessment{},
		Findings:               []codeReviewOrchestratorFinding{},
		HumanReviewReasons:     []codeReviewOrchestratorHumanReviewReason{},
		Summary:                "Changes the query result assignment.",
		ReviewSummary:          "The query result assignment follows the declared contract.",
	}
}

func workerUncertaintyPayload(t *testing.T, unresolved bool, reasons any, includeReasons bool) string {
	t.Helper()
	payload := map[string]any{
		"approval_recommended": !unresolved, "description_assessments": []any{}, "findings": []any{}, "human_review_reasons": []any{},
		"scope_mismatch": false, "unresolved_uncertainty": unresolved, "reviewer_disagreement": false, "prompt_injection_detected": false,
		"summary": "Changes the query result assignment.", "review_summary": "The dependency contract is needed to assess the changed assignment.", "risk_notes": []any{},
	}
	if includeReasons {
		payload["uncertainty_reasons"] = reasons
	}
	raw, err := json.Marshal(payload)
	require.NoError(t, err, "uncertainty fixture should encode as synthesis JSON")
	return string(raw)
}

func TestParseCodeReviewUncertaintyContract(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		unresolved     bool
		includeReasons bool
		reasons        any
		expected       []models.CodeReviewUncertaintyReason
		errorText      string
	}{
		{name: "historical false without array"},
		{name: "current false with empty array", includeReasons: true, reasons: []models.CodeReviewUncertaintyReason{}, expected: []models.CodeReviewUncertaintyReason{}},
		{name: "normalizes complete material reason", unresolved: true, includeReasons: true, reasons: []models.CodeReviewUncertaintyReason{{MissingEvidence: " " + newWorkerUncertaintyReason().MissingEvidence + " ", MaterialImpact: " " + newWorkerUncertaintyReason().MaterialImpact + " ", ResolutionEvidence: " " + newWorkerUncertaintyReason().ResolutionEvidence + " "}}, expected: []models.CodeReviewUncertaintyReason{newWorkerUncertaintyReason()}},
		{name: "collapses embedded whitespace", unresolved: true, includeReasons: true, reasons: []models.CodeReviewUncertaintyReason{{MissingEvidence: "The installed\nTanStack query declaration for QueryObserverResult.", MaterialImpact: "The changed result assignment may exclude the pending state\t and fail type compatibility.", ResolutionEvidence: "The QueryObserverResult declaration at the version pinned in\r\n the lockfile."}}, expected: []models.CodeReviewUncertaintyReason{newWorkerUncertaintyReason()}},
		{name: "rejects multiline generic impact", unresolved: true, includeReasons: true, reasons: []models.CodeReviewUncertaintyReason{{MissingEvidence: "The declaration.", MaterialImpact: "Important\n uncertainty\t remained.", ResolutionEvidence: "The pinned declaration."}}, errorText: "must explain the concrete evidence and impact"},
		{name: "rejects oversized input before collapsing whitespace", unresolved: true, includeReasons: true, reasons: []models.CodeReviewUncertaintyReason{{MissingEvidence: "The" + strings.Repeat(" ", 1001) + "declaration.", MaterialImpact: newWorkerUncertaintyReason().MaterialImpact, ResolutionEvidence: newWorkerUncertaintyReason().ResolutionEvidence}}, errorText: "exceeds 1000 characters"},
		{name: "rejects bare true", unresolved: true, errorText: "requires at least one complete uncertainty reason"},
		{name: "rejects true with null array", unresolved: true, includeReasons: true, errorText: "requires at least one complete uncertainty reason"},
		{name: "rejects true with empty array", unresolved: true, includeReasons: true, reasons: []models.CodeReviewUncertaintyReason{}, errorText: "requires at least one complete uncertainty reason"},
		{name: "rejects bare label object", unresolved: true, includeReasons: true, reasons: []any{map[string]string{"summary": "Important uncertainty remained"}}, errorText: "missing missing_evidence"},
		{name: "rejects blank material impact", unresolved: true, includeReasons: true, reasons: []models.CodeReviewUncertaintyReason{{MissingEvidence: "The declaration.", MaterialImpact: " \n", ResolutionEvidence: "The pinned declaration."}}, errorText: "missing material_impact"},
		{name: "rejects false with reasons", includeReasons: true, reasons: []models.CodeReviewUncertaintyReason{newWorkerUncertaintyReason()}, errorText: "require unresolved_uncertainty=true"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synthesis, err := parseCodeReviewOrchestratorSynthesis(workerUncertaintyPayload(t, tt.unresolved, tt.reasons, tt.includeReasons))
			if tt.errorText != "" {
				require.ErrorContains(t, err, tt.errorText, "malformed uncertainty must reach synthesis repair rather than approval")
				require.Equal(t, codeReviewOrchestratorSynthesis{}, synthesis, "rejected uncertainty must not leak an accepted synthesis")
				return
			}
			require.NoError(t, err, "historical clean or complete material uncertainty should remain parseable")
			require.Equal(t, tt.expected, synthesis.UncertaintyReasons, "parser should preserve normalized concrete evidence")
			require.Equal(t, tt.unresolved, synthesis.UnresolvedUncertainty, "parser must preserve the uncertainty decision")
		})
	}
}

func TestCodeReviewUncertaintyReuseValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		unresolved bool
		reasons    []models.CodeReviewUncertaintyReason
		usable     bool
	}{
		{name: "historical clean synthesis", usable: true},
		{name: "complete uncertainty", unresolved: true, reasons: []models.CodeReviewUncertaintyReason{newWorkerUncertaintyReason()}, usable: true},
		{name: "historical bare true", unresolved: true},
		{name: "incomplete saved reason", unresolved: true, reasons: []models.CodeReviewUncertaintyReason{{MissingEvidence: "TanStack declaration"}}},
		{name: "contradictory saved reason", reasons: []models.CodeReviewUncertaintyReason{newWorkerUncertaintyReason()}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synthesis := newWorkerUncertaintySynthesis()
			synthesis.UnresolvedUncertainty, synthesis.UncertaintyReasons = tt.unresolved, tt.reasons
			require.Equal(t, tt.usable, codeReviewOrchestratorSynthesisUsable(synthesis), "persisted validation flags must not bypass the reason contract")
			threadID := uuid.New()
			results := []models.CodeReviewAgentResult{{Role: models.CodeReviewAgentRoleOrchestrator, Status: models.CodeReviewAgentResultStatusCompleted, StructuredResult: marshalCodeReviewOrchestratorStructuredResult(codeReviewOrchestratorStructuredResult{ThreadID: threadID.String(), ReadOnly: true, SynthesisValidated: true, Synthesis: synthesis})}}
			actual, actualThreadID, err := recheckBaselineSynthesis(results)
			_, evidenceUsable := codeReviewOrchestratorEvidence(results)
			require.Equal(t, tt.usable, evidenceUsable, "restored orchestrator evidence must share uncertainty validation")
			if !tt.usable {
				require.Error(t, err, "evidence-only rechecks must reject baselines without material reasons")
				require.Equal(t, codeReviewOrchestratorSynthesis{}, codeReviewOrchestratorSynthesisFromResults(results), "unusable saved uncertainty cannot become a live synthesis")
				return
			}
			require.NoError(t, err, "rechecks may reuse complete evidence or historical clean synthesis")
			require.Equal(t, synthesis, actual, "rechecking other evidence must preserve the concrete uncertainty reason")
			require.Equal(t, threadID, actualThreadID, "recheck must continue the validated baseline thread")
		})
	}
}

func TestCodeReviewRecheckRejectsInvalidUncertaintyBaseline(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		unresolved bool
		reasons    []models.CodeReviewUncertaintyReason
	}{
		{name: "bare uncertainty", unresolved: true},
		{name: "incomplete reason", unresolved: true, reasons: []models.CodeReviewUncertaintyReason{{MissingEvidence: "The generated role map."}}},
		{name: "contradictory reason", reasons: []models.CodeReviewUncertaintyReason{newWorkerUncertaintyReason()}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			input, response := recheckResponseFixture()
			input.BaselineSynthesis.UnresolvedUncertainty, input.BaselineSynthesis.UncertaintyReasons = tt.unresolved, tt.reasons
			raw, err := json.Marshal(response)
			require.NoError(t, err, "recheck fixture should encode")
			input.Raw = string(raw)
			actual, err := validateCodeReviewRecheckResponse(input)
			require.ErrorContains(t, err, "invalid baseline uncertainty evidence", "direct recheck validation must reject incomplete uncertainty")
			require.Equal(t, input.BaselineSynthesis, actual.Synthesis, "invalid recheck must preserve the baseline and never clear its blocker")
		})
	}
}

func TestEvaluateLiveCodeReviewUncertainty(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		reasons     []models.CodeReviewUncertaintyReason
		expected    models.CodeReviewRiskReasonCode
		bodySnippet string
	}{
		{name: "concrete uncertainty is published with other blockers", reasons: []models.CodeReviewUncertaintyReason{newWorkerUncertaintyReason()}, expected: models.CodeReviewRiskReasonUnresolvedUncertainty, bodySnippet: "Missing evidence: The installed TanStack query declaration for QueryObserverResult. Why it matters: The changed result assignment may exclude the pending state and fail type compatibility. To resolve: The QueryObserverResult declaration at the version pinned in the lockfile."},
		{name: "bare uncertainty cannot approve", expected: models.CodeReviewRiskReasonOrchestratorSynthesisInvalid, bodySnippet: "The orchestrator did not produce a valid structured synthesis."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			policy := models.DefaultCodeReviewPolicyConfig()
			policy.Enabled = true
			policy.ApprovalMode = models.CodeReviewApprovalModeApproveAcceptable
			policy.DescriptionPolicy.Requirements = []models.CodeReviewDescriptionRequirement{}
			synthesis := newWorkerUncertaintySynthesis()
			synthesis.UnresolvedUncertainty, synthesis.UncertaintyReasons, synthesis.ScopeMismatch = true, tt.reasons, true
			synthesis.ReviewSummary = "UNTRUSTED summary claims approval and hides the blockers."
			results := []models.CodeReviewAgentResult{
				{Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatusCompleted},
				{Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatusCompleted},
				{Role: models.CodeReviewAgentRoleOrchestrator, Status: models.CodeReviewAgentResultStatusCompleted, StructuredResult: marshalCodeReviewOrchestratorStructuredResult(codeReviewOrchestratorStructuredResult{SynthesisValidated: true, Synthesis: synthesis})},
			}
			decision, body := evaluateLiveCodeReviewOutcome(liveCodeReviewOutcomeInput{Policy: policy, OrchestratorSynthesis: synthesis, AgentResults: results, ChangedFilesAvailable: true, Health: &models.PullRequestHealthResponse{CanMerge: true, ChecksConfirmed: true, MergeState: models.PullRequestMergeStateClean}})
			require.Equal(t, models.CodeReviewDecisionNeedsHumanReview, decision.Decision, "uncertainty or an invalid saved reason must withhold approval")
			require.True(t, codeReviewRiskReasonsContain(decision.RiskReasonDetails, tt.expected), "outcome should preserve material uncertainty or identify invalid synthesis")
			require.True(t, codeReviewRiskReasonsContain(decision.RiskReasonDetails, models.CodeReviewRiskReasonScopeMismatch), "publishing an uncertainty explanation must retain other blockers")
			require.Contains(t, body, tt.bodySnippet, "GitHub should explain the concrete gap or invalid evidence")
			require.NotContains(t, body, synthesis.ReviewSummary, "a blocked outcome must not publish an untrusted summary that claims approval")
		})
	}
}

type uncertaintyRepairStateArg struct{}

func (uncertaintyRepairStateArg) Match(value any) bool {
	raw, ok := value.(json.RawMessage)
	if !ok {
		return false
	}
	state, ok := parseCodeReviewOrchestratorStructuredResult(raw)
	return ok && state.SynthesisRepairPending && !state.SynthesisValidated && strings.Contains(state.Error, "uncertainty")
}

func TestCodeReviewUncertaintyRequestsRepair(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		reasons any
		include bool
	}{
		{name: "bare true"},
		{name: "missing impact", include: true, reasons: []models.CodeReviewUncertaintyReason{{MissingEvidence: "The query declaration.", ResolutionEvidence: "The pinned declaration."}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			raw := workerUncertaintyPayload(t, true, tt.reasons, tt.include)
			_, validationErr := parseCodeReviewOrchestratorSynthesis(raw)
			require.Error(t, validationErr, "incomplete uncertainty should fail the acceptance gate")
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "repair mock should initialize")
			defer mock.Close()
			orgID, sessionID, resultID, threadID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			state := codeReviewOrchestratorStructuredResult{ThreadID: threadID.String()}
			mock.ExpectQuery("UPDATE code_review_agent_results").WithArgs(models.CodeReviewAgentResultStatusRunning, &raw, uncertaintyRepairStateArg{}, orgID, resultID).
				WillReturnRows(newCodeReviewAgentResultRows().AddRow(resultID, orgID, sessionID, "codex", nil, models.CodeReviewAgentRoleOrchestrator, models.CodeReviewAgentResultStatusRunning, &raw, marshalCodeReviewOrchestratorStructuredResult(state), time.Now().UTC()))
			sender := &codeReviewOrchestratorRepairSenderStub{}
			handled, started, err := requestCodeReviewOrchestratorSynthesisRepair(context.Background(), &Stores{CodeReviews: db.NewCodeReviewStore(mock)}, sender, zerolog.Nop(), runCodeReviewPayload{OrgID: orgID, SessionID: sessionID}, models.DefaultCodeReviewPolicyConfig(), nil, models.CodeReviewAgentResult{ID: resultID, OrgID: orgID, SessionID: sessionID, AgentProvider: "codex", Role: models.CodeReviewAgentRoleOrchestrator}, state, 1, raw, validationErr, models.CodeReviewVisualEvidenceSnapshot{})
			require.NoError(t, err, "malformed uncertainty should receive the existing bounded repair turn")
			require.True(t, handled && started, "uncertainty schema errors must be repairable")
			require.Len(t, sender.inputs, 1, "repair must send one correction turn")
			require.Contains(t, sender.inputs[0].Message, validationErr.Error(), "repair should explain the exact missing evidence contract")
			require.Contains(t, sender.inputs[0].Message, `"uncertainty_reasons": []`, "repair should request the current reasons schema")
			require.Contains(t, sender.inputs[0].Message, "do not invent evidence or silently clear the flag", "repair must not convert unsupported uncertainty into approval")
			require.NoError(t, mock.ExpectationsWereMet(), "repair must persist invalid uncertainty as pending rather than accepted")
		})
	}
}

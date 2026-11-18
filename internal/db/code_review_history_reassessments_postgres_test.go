package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCodeReviewHistoryPostgresReassessmentProvenance(t *testing.T) {
	t.Parallel()
	pool := newPriorHistoryPostgres(t)
	ctx := context.Background()
	store := NewCodeReviewHistoryStore(pool)
	orgID, repoID, prID, policyID, currentID, priorID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	baselineID, recheckID, futureID := uuid.New(), uuid.New(), uuid.New()
	resolvedID, retainedID, resultID := uuid.New(), uuid.New(), uuid.New()
	cutoff := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	baselineCompleted := cutoff.Add(-2 * time.Hour)
	seedPriorHistoryRound(t, pool, orgID, repoID, prID, policyID, currentID, cutoff, nil, models.CodeReviewSessionStatusRunning)
	metadataID := seedPriorHistoryRound(t, pool, orgID, repoID, prID, policyID, priorID, cutoff.Add(-3*time.Hour), &baselineCompleted, models.CodeReviewSessionStatusCompleted)
	seedPriorHistoryAssessment(t, pool, orgID, repoID, prID, policyID, priorID, metadataID, baselineID, 1, baselineCompleted, "Original report has two findings")
	_, err := pool.Exec(ctx, `INSERT INTO code_review_findings(id,org_id,session_id,assessment_id,dedupe_key,severity,confidence,summary,body,created_at)
 VALUES($1,$3,$4,$5,'resolved-source','high','high','Original evidence gap','Original evidence',$6),
       ($2,$3,$4,$5,'retained-source','medium','high','Persistent concern','Original code evidence',$6::timestamptz + interval '1 second')`, resolvedID, retainedID, orgID, priorID, baselineID, baselineCompleted.Add(-time.Minute))
	require.NoError(t, err, "seed immutable original findings with full-schema assessment ownership")
	_, err = pool.Exec(ctx, `INSERT INTO code_review_agent_results(id,org_id,session_id,assessment_id,agent_provider,role,status,raw_output,created_at) VALUES($1,$2,$3,$4,'codex','reviewer','completed','Original reviewer output',$5)`, resultID, orgID, priorID, baselineID, baselineCompleted)
	require.NoError(t, err, "seed original reviewer evidence")
	expectedReassessments := []models.CodeReviewFindingReassessment{
		{FindingID: resolvedID, Status: models.CodeReviewFindingResolved, Reason: "New captured evidence resolves the original gap", EvidenceCitations: []models.CodeReviewEvidenceCitation{{EvidenceID: "text-new", Quote: "The boundary case is now covered"}}},
		{FindingID: retainedID, Status: models.CodeReviewFindingRetained, Reason: "Current evidence does not resolve the code concern", EvidenceCitations: []models.CodeReviewEvidenceCitation{}},
	}
	outcome, err := json.Marshal(struct {
		FindingReassessments []models.CodeReviewFindingReassessment `json:"finding_reassessments"`
	}{FindingReassessments: expectedReassessments})
	require.NoError(t, err, "encode selected recheck dispositions")
	assessments := []struct {
		id         uuid.UUID
		generation int
		completed  time.Time
		body       string
		outcome    json.RawMessage
	}{
		{recheckID, 2, cutoff.Add(-time.Hour), "The evidence gap is resolved; the code concern remains", outcome},
		{futureID, 3, cutoff.Add(time.Hour), "A future recheck must not replace history", json.RawMessage(`{"finding_reassessments":[]}`)},
	}
	for _, assessment := range assessments {
		_, err = pool.Exec(ctx, `INSERT INTO code_review_revision_assessments
 (id,org_id,repository_id,repository_full_name,pull_request_id,metadata_id,session_id,policy_id,generation,source_assessment_id,base_sha,base_ref,head_sha,input_version,code_digest,contract_digest,intent_digest,visual_digest,request_digest,gate_digest,input_digest,input_manifest,review_scope,route_reason,status,publication_key,completed_at,result_origin,coverage_complete,decision,acceptable,structured_outcome,rendered_body,publication_state,created_at)
 SELECT $1::uuid,org_id,repository_id,repository_full_name,pull_request_id,metadata_id,session_id,policy_id,$2,id,base_sha,base_ref,head_sha,input_version,code_digest,contract_digest,intent_digest,visual_digest,request_digest,gate_digest,$1::uuid::text,input_manifest,'evidence_only','evidence_changed','completed',$1::uuid::text,$3,'evidence_only',true,'comment_only',true,$4,$5,'not_required',$3::timestamptz - interval '1 minute'
 FROM code_review_revision_assessments WHERE org_id=$6 AND id=$7`, assessment.id, assessment.generation, assessment.completed, assessment.outcome, assessment.body, orgID, baselineID)
		require.NoError(t, err, "seed actual evidence-only assessment with a baseline source foreign key")
	}
	report, err := store.GetPriorRound(ctx, orgID, repoID, currentID, priorID)
	require.NoError(t, err, "retrieve the latest eligible completed reassessment")
	expectedScope := models.CodeReviewScopeEvidenceOnly
	expectedDecision := models.CodeReviewDecisionCommentOnly
	expectedCompleted := cutoff.Add(-time.Hour)
	expectedReport := CodeReviewHistoryReport{
		CodeReviewHistoryRound: CodeReviewHistoryRound{SessionID: priorID, AssessmentID: &recheckID, BaseSHA: "reviewed-base", HeadSHA: "reviewed-head", PolicyID: policyID, Status: models.CodeReviewSessionStatusCompleted, Decision: &expectedDecision, CreatedAt: cutoff.Add(-3 * time.Hour), CompletedAt: &expectedCompleted},
		EvidenceAssessmentID:   &baselineID, ReviewScope: &expectedScope, FinalReviewBody: assessments[0].body, FinalReviewBodyRunes: len([]rune(assessments[0].body)), Cutoff: cutoff,
	}
	var actualReassessments []models.CodeReviewFindingReassessment
	require.NoError(t, json.Unmarshal(report.FindingReassessments, &actualReassessments), "selected assessment dispositions should decode as typed evidence")
	require.Equal(t, expectedReassessments, actualReassessments, "resolved and retained finding decisions must come from the selected pre-cutoff recheck")
	report.FindingReassessments = nil
	report.CreatedAt, report.Cutoff = report.CreatedAt.UTC(), report.Cutoff.UTC()
	if report.CompletedAt != nil {
		completed := report.CompletedAt.UTC()
		report.CompletedAt = &completed
	}
	require.Equal(t, expectedReport, report, "latest report identity must remain distinct from its original findings and outputs")
	findings, err := store.ListPriorFindings(ctx, orgID, priorID, report.EvidenceAssessmentID, report.Cutoff, 50)
	require.NoError(t, err, "load the immutable original findings for historical comparison")
	require.Equal(t, []CodeReviewHistoryFinding{
		{ID: resolvedID, Severity: models.CodeReviewFindingSeverityHigh, Confidence: models.CodeReviewFindingConfidenceHigh, Summary: "Original evidence gap", Body: "Original evidence", BodyRunes: 17},
		{ID: retainedID, Severity: models.CodeReviewFindingSeverityMedium, Confidence: models.CodeReviewFindingConfidenceHigh, Summary: "Persistent concern", Body: "Original code evidence", BodyRunes: 22},
	}, findings, "a resolved original finding must remain available beside its explicit reassessment, not disappear or masquerade as a new finding")
	results, err := store.ListPriorAgentResults(ctx, orgID, priorID, report.EvidenceAssessmentID, report.Cutoff, 20)
	require.NoError(t, err, "load the immutable original reviewer output")
	require.Equal(t, []CodeReviewHistoryAgentResult{{ID: resultID, AgentProvider: "codex", Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatusCompleted, Output: "Original reviewer output", OutputRunes: 24}}, results, "reviewer output must explicitly remain evidence from the original full assessment")
}

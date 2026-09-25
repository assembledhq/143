package db

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var ErrCodeReviewAssessmentConflict = errors.New("code review assessment identity conflicts with captured inputs")
var ErrCodeReviewAssessmentState = errors.New("code review assessment state changed")

type CodeReviewAssessmentStore struct{ db DBTX }

// NewCodeReviewAssessmentStore accepts a pool or pgx.Tx so the caller can
// atomically finalize the legacy review metadata and its full assessment.
func NewCodeReviewAssessmentStore(conn DBTX) *CodeReviewAssessmentStore {
	return &CodeReviewAssessmentStore{db: conn}
}

func canonicalAssessmentManifest(raw json.RawMessage) (json.RawMessage, error) {
	// PostgreSQL jsonb reorders nested object keys, too. Canonicalize the
	// entire manifest without rounding integer identities through float64.
	var object map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&object); err != nil || object == nil || !json.Valid(raw) {
		return nil, fmt.Errorf("assessment manifest must be a JSON object")
	}
	return json.Marshal(object)
}

func validateAssessmentCapture(c models.CodeReviewAssessmentCapture) error {
	if c.OrgID == uuid.Nil || c.RepositoryID == uuid.Nil || c.PullRequestID == uuid.Nil || c.PolicyID == uuid.Nil || c.SessionID == uuid.Nil || c.Generation < 1 || c.InputVersion < 1 {
		return fmt.Errorf("assessment requires org, repository, PR, policy, session, positive generation and input version")
	}
	if err := c.ReviewScope.Validate(); err != nil {
		return err
	}
	if err := c.RouteReason.Validate(); err != nil {
		return err
	}
	if c.ReviewScope == models.CodeReviewScopeEvidenceOnly && c.SourceAssessmentID == nil || c.ReviewScope == models.CodeReviewScopeFull && c.SourceAssessmentID != nil {
		return fmt.Errorf("assessment source must be a full baseline only for evidence-only scope")
	}
	if c.HeadSHA == "" || c.BaseSHA == "" || c.BaseRef == "" || c.CodeDigest == "" || c.ContractDigest == "" || c.IntentDigest == "" || c.VisualDigest == "" || c.RequestDigest == "" || c.GateDigest == "" || c.InputDigest == "" || c.RouteReason == "" || c.PublicationKey == "" {
		return fmt.Errorf("assessment requires a complete captured revision and digest set")
	}
	_, err := canonicalAssessmentManifest(c.InputManifest)
	return err
}

// Create inserts immutable captured inputs. A retry with the same ID,
// generation, or publication key returns the existing row only on exact input
// equality. The caller must hold the PR admission lock for generation ordering.
func (s *CodeReviewAssessmentStore) Create(ctx context.Context, c models.CodeReviewAssessmentCapture) (models.CodeReviewAssessment, bool, error) {
	if err := validateAssessmentCapture(c); err != nil {
		return models.CodeReviewAssessment{}, false, err
	}
	explicitID := c.ID != uuid.Nil
	if c.ID == uuid.Nil {
		c.ID = uuid.New()
	}
	manifest, err := canonicalAssessmentManifest(c.InputManifest)
	if err != nil {
		return models.CodeReviewAssessment{}, false, err
	}
	if c.SourceAssessmentID != nil {
		// Intent and auxiliary runtime/prompt fingerprints are reassessed by
		// the evidence turn. Code coverage is bound to the versioned policy.
		var identity struct {
			Contract struct {
				PolicyID      uuid.UUID `json:"policy_id"`
				PolicyVersion int64     `json:"policy_version"`
				PolicyDigest  string    `json:"policy_digest"`
			} `json:"contract"`
		}
		if err := json.Unmarshal(manifest, &identity); err != nil || identity.Contract.PolicyID != c.PolicyID || identity.Contract.PolicyVersion < 1 || identity.Contract.PolicyDigest == "" {
			return models.CodeReviewAssessment{}, false, fmt.Errorf("evidence assessment requires a captured policy identity: %w", ErrCodeReviewAssessmentConflict)
		}
		var valid bool
		err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM code_review_revision_assessments
 WHERE org_id=$1 AND pull_request_id=$2 AND id=$3 AND review_scope='full' AND status='completed' AND coverage_complete
 AND session_id=$4 AND repository_id=$5 AND policy_id=$6 AND head_sha=$7 AND base_sha=$8 AND base_ref=$9 AND code_digest=$10
 AND input_manifest #>> '{contract,policy_id}' = $6::text
 AND input_manifest #> '{contract,policy_version}' = to_jsonb($11::bigint)
 AND input_manifest #>> '{contract,policy_digest}' = $12)`, c.OrgID, c.PullRequestID, c.SourceAssessmentID, c.SessionID, c.RepositoryID, c.PolicyID, c.HeadSHA, c.BaseSHA, c.BaseRef, c.CodeDigest, identity.Contract.PolicyVersion, identity.Contract.PolicyDigest).Scan(&valid)
		if err != nil {
			return models.CodeReviewAssessment{}, false, err
		}
		if !valid {
			return models.CodeReviewAssessment{}, false, fmt.Errorf("source assessment is not a complete full baseline: %w", ErrCodeReviewAssessmentConflict)
		}
	}
	if c.PreviousAssessmentID != nil {
		var valid bool
		err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM code_review_revision_assessments WHERE org_id=$1 AND pull_request_id=$2 AND id=$3 AND generation<$4)`, c.OrgID, c.PullRequestID, c.PreviousAssessmentID, c.Generation).Scan(&valid)
		if err != nil {
			return models.CodeReviewAssessment{}, false, err
		}
		if !valid {
			return models.CodeReviewAssessment{}, false, fmt.Errorf("invalid predecessor assessment: %w", ErrCodeReviewAssessmentConflict)
		}
	}
	if c.PreviousPublishedAssessmentID != nil {
		var valid bool
		err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM code_review_revision_assessments WHERE org_id=$1 AND pull_request_id=$2 AND id=$3 AND generation<$4 AND status='completed' AND publication_state IN ('confirmed','not_required'))`, c.OrgID, c.PullRequestID, c.PreviousPublishedAssessmentID, c.Generation).Scan(&valid)
		if err != nil {
			return models.CodeReviewAssessment{}, false, err
		}
		if !valid {
			return models.CodeReviewAssessment{}, false, fmt.Errorf("invalid published predecessor assessment: %w", ErrCodeReviewAssessmentConflict)
		}
	}
	rows, err := s.db.Query(ctx, `INSERT INTO code_review_revision_assessments(
 id,org_id,repository_id,repository_full_name,pull_request_id,metadata_id,session_id,policy_id,generation,conversation_id,source_assessment_id,previous_assessment_id,previous_published_assessment_id,
 base_sha,base_ref,head_sha,input_version,code_digest,contract_digest,intent_digest,visual_digest,request_digest,gate_digest,input_digest,input_manifest,review_scope,route_reason,publication_key)
 SELECT $1,$2,$3,p.github_repo,$4,m.id,$5,$6,$7,$8,$9,$10,$11,
 $12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26
 FROM code_review_session_metadata m JOIN pull_requests p ON p.org_id=m.org_id AND p.id=m.pull_request_id
 JOIN repositories r ON r.org_id=m.org_id AND r.id=m.repository_id AND r.full_name=p.github_repo
 JOIN code_review_policies pol ON pol.org_id=m.org_id AND pol.id=m.policy_id AND (pol.repository_id IS NULL OR pol.repository_id=m.repository_id)
 WHERE m.org_id=$2 AND m.session_id=$5 AND m.repository_id=$3 AND m.pull_request_id=$4 AND m.policy_id=$6
 ORDER BY m.created_at DESC,m.id DESC LIMIT 1
 ON CONFLICT DO NOTHING RETURNING *`, c.ID, c.OrgID, c.RepositoryID, c.PullRequestID, c.SessionID, c.PolicyID, c.Generation, c.ConversationID, c.SourceAssessmentID, c.PreviousAssessmentID, c.PreviousPublishedAssessmentID, c.BaseSHA, c.BaseRef, c.HeadSHA, c.InputVersion, c.CodeDigest, c.ContractDigest, c.IntentDigest, c.VisualDigest, c.RequestDigest, c.GateDigest, c.InputDigest, manifest, c.ReviewScope, c.RouteReason, c.PublicationKey)
	if err != nil {
		return models.CodeReviewAssessment{}, false, err
	}
	created, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.CodeReviewAssessment])
	if err == nil {
		return created, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return models.CodeReviewAssessment{}, false, err
	}
	// SELECT/INSERT returning no row can also mean the metadata relationship is
	// absent. Do not report that as successful idempotency.
	rows, err = s.db.Query(ctx, `SELECT * FROM code_review_revision_assessments WHERE org_id=$1 AND (id=$2 OR (pull_request_id=$3 AND generation=$4) OR publication_key=$5) ORDER BY created_at DESC LIMIT 2`, c.OrgID, c.ID, c.PullRequestID, c.Generation, c.PublicationKey)
	if err != nil {
		return models.CodeReviewAssessment{}, false, err
	}
	existing, err := pgx.CollectRows(rows, pgx.RowToStructByName[models.CodeReviewAssessment])
	if err != nil {
		return models.CodeReviewAssessment{}, false, err
	}
	if len(existing) == 0 {
		var metadataExists bool
		err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM code_review_session_metadata m JOIN pull_requests p ON p.org_id=m.org_id AND p.id=m.pull_request_id JOIN repositories r ON r.org_id=m.org_id AND r.id=m.repository_id AND r.full_name=p.github_repo JOIN code_review_policies pol ON pol.org_id=m.org_id AND pol.id=m.policy_id AND (pol.repository_id IS NULL OR pol.repository_id=m.repository_id) WHERE m.org_id=$1 AND m.session_id=$2 AND m.repository_id=$3 AND m.pull_request_id=$4 AND m.policy_id=$5)`, c.OrgID, c.SessionID, c.RepositoryID, c.PullRequestID, c.PolicyID).Scan(&metadataExists)
		if err != nil {
			return models.CodeReviewAssessment{}, false, err
		}
		if metadataExists {
			return models.CodeReviewAssessment{}, false, ErrCodeReviewAssessmentConflict
		}
		return models.CodeReviewAssessment{}, false, pgx.ErrNoRows
	}
	if len(existing) != 1 || !assessmentCaptureEqual(c, manifest, existing[0], explicitID) {
		return models.CodeReviewAssessment{}, false, ErrCodeReviewAssessmentConflict
	}
	return existing[0], true, nil
}

func sameUUID(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func assessmentCaptureEqual(c models.CodeReviewAssessmentCapture, manifest json.RawMessage, a models.CodeReviewAssessment, explicitID bool) bool {
	existingManifest, err := canonicalAssessmentManifest(a.InputManifest)
	return err == nil && (!explicitID || a.ID == c.ID) && a.OrgID == c.OrgID && a.RepositoryID == c.RepositoryID && a.PullRequestID == c.PullRequestID && a.PolicyID == c.PolicyID && a.SessionID == c.SessionID && a.Generation == c.Generation &&
		sameUUID(a.ConversationID, c.ConversationID) && sameUUID(a.SourceAssessmentID, c.SourceAssessmentID) && sameUUID(a.PreviousAssessmentID, c.PreviousAssessmentID) && sameUUID(a.PreviousPublishedAssessmentID, c.PreviousPublishedAssessmentID) &&
		a.BaseSHA == c.BaseSHA && a.BaseRef == c.BaseRef && a.HeadSHA == c.HeadSHA && a.InputVersion == c.InputVersion && a.CodeDigest == c.CodeDigest && a.ContractDigest == c.ContractDigest && a.IntentDigest == c.IntentDigest && a.VisualDigest == c.VisualDigest && a.RequestDigest == c.RequestDigest && a.GateDigest == c.GateDigest && a.InputDigest == c.InputDigest && bytes.Equal(existingManifest, manifest) && a.ReviewScope == c.ReviewScope && a.RouteReason == c.RouteReason && a.PublicationKey == c.PublicationKey
}

func (s *CodeReviewAssessmentStore) get(ctx context.Context, sql string, args ...any) (models.CodeReviewAssessment, error) {
	rows, err := s.db.Query(ctx, sql, args...)
	if err != nil {
		return models.CodeReviewAssessment{}, err
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.CodeReviewAssessment])
}

func (s *CodeReviewAssessmentStore) GetByID(ctx context.Context, orgID, id uuid.UUID) (models.CodeReviewAssessment, error) {
	return s.get(ctx, `SELECT * FROM code_review_revision_assessments WHERE org_id=$1 AND id=$2`, orgID, id)
}

// GetBySessionID returns the latest full baseline, never a recheck sharing it.
func (s *CodeReviewAssessmentStore) GetBySessionID(ctx context.Context, orgID, sessionID uuid.UUID) (models.CodeReviewAssessment, error) {
	return s.get(ctx, `SELECT * FROM code_review_revision_assessments WHERE org_id=$1 AND session_id=$2 AND review_scope='full' ORDER BY created_at DESC,id DESC LIMIT 1`, orgID, sessionID)
}

func (s *CodeReviewAssessmentStore) GetLatestForPR(ctx context.Context, orgID, prID uuid.UUID) (models.CodeReviewAssessment, error) {
	return s.get(ctx, `SELECT * FROM code_review_revision_assessments WHERE org_id=$1 AND pull_request_id=$2 ORDER BY generation DESC LIMIT 1`, orgID, prID)
}

func (s *CodeReviewAssessmentStore) GetLatestPublishedForPR(ctx context.Context, orgID, prID uuid.UUID) (models.CodeReviewAssessment, error) {
	return s.get(ctx, `SELECT * FROM code_review_revision_assessments WHERE org_id=$1 AND pull_request_id=$2 AND status='completed' AND publication_state='confirmed' ORDER BY generation DESC LIMIT 1`, orgID, prID)
}

// ListCurrentForPRs fetches the most recent completed assessment and the one
// active assessment for each requested PR in one org-scoped query.
func (s *CodeReviewAssessmentStore) ListCurrentForPRs(ctx context.Context, orgID uuid.UUID, prIDs []uuid.UUID) (map[uuid.UUID]models.CodeReviewAssessmentSummary, map[uuid.UUID]models.CodeReviewAssessmentSummary, map[uuid.UUID]models.CodeReviewAssessmentSummary, error) {
	current := make(map[uuid.UUID]models.CodeReviewAssessmentSummary)
	active := make(map[uuid.UUID]models.CodeReviewAssessmentSummary)
	failed := make(map[uuid.UUID]models.CodeReviewAssessmentSummary)
	if len(prIDs) == 0 {
		return current, active, failed, nil
	}
	rows, err := s.db.Query(ctx, `WITH latest_completed AS (
 SELECT DISTINCT ON (pull_request_id) id,pull_request_id,session_id,source_assessment_id,status,review_scope,route_reason,decision,acceptable,risk_reason_details,head_sha,publication_state,created_at,completed_at,'current'::text AS kind
 FROM code_review_revision_assessments WHERE org_id=$1 AND pull_request_id=ANY($2::uuid[]) AND status='completed' AND superseded_by_assessment_id IS NULL ORDER BY pull_request_id,generation DESC
), active_assessments AS (
 SELECT id,pull_request_id,session_id,source_assessment_id,status,review_scope,route_reason,decision,acceptable,risk_reason_details,head_sha,publication_state,created_at,completed_at,'active'::text AS kind
 FROM code_review_revision_assessments WHERE org_id=$1 AND pull_request_id=ANY($2::uuid[]) AND status IN ('reserved','running','publishing')
), latest_terminal AS (
 SELECT DISTINCT ON (pull_request_id) id,pull_request_id,session_id,source_assessment_id,status,review_scope,route_reason,decision,acceptable,risk_reason_details,head_sha,publication_state,created_at,completed_at,'failed'::text AS kind
 FROM code_review_revision_assessments WHERE org_id=$1 AND pull_request_id=ANY($2::uuid[]) AND status IN ('completed','failed','superseded','cancelled')
 ORDER BY pull_request_id,generation DESC
), candidates AS (
 SELECT * FROM latest_completed
 UNION ALL
 SELECT * FROM active_assessments
 UNION ALL
 SELECT * FROM latest_terminal WHERE status IN ('failed','superseded'))
 SELECT * FROM candidates`, orgID, prIDs)
	if err != nil {
		return nil, nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var summary models.CodeReviewAssessmentSummary
		var kind string
		if err := rows.Scan(&summary.ID, &summary.PullRequestID, &summary.SessionID, &summary.SourceAssessmentID, &summary.Status, &summary.ReviewScope, &summary.RouteReason, &summary.Decision, &summary.Acceptable, &summary.RiskReasonDetails, &summary.HeadSHA, &summary.PublicationState, &summary.CreatedAt, &summary.CompletedAt, &kind); err != nil {
			return nil, nil, nil, err
		}
		if kind == "current" {
			current[summary.PullRequestID] = summary
		} else if kind == "active" {
			active[summary.PullRequestID] = summary
		} else {
			failed[summary.PullRequestID] = summary
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, nil, err
	}
	return current, active, failed, nil
}

// GetCurrentForPR reflects the latest non-superseded assessment. Explicit PR
// projection wiring can later use this reader while legacy PRs return no row.
func (s *CodeReviewAssessmentStore) GetCurrentForPR(ctx context.Context, orgID, prID uuid.UUID) (models.CodeReviewAssessment, error) {
	return s.get(ctx, `SELECT * FROM code_review_revision_assessments WHERE org_id=$1 AND pull_request_id=$2 AND superseded_by_assessment_id IS NULL AND status IN ('reserved','running','publishing','completed') ORDER BY generation DESC LIMIT 1`, orgID, prID)
}

// LinkFullReviewEvidence runs in the caller's completion transaction. A new
// full session has only its own reviewer/results, and nullable links preserve
// every pre-migration legacy row without manufacturing provenance.
func (s *CodeReviewAssessmentStore) LinkFullReviewEvidence(ctx context.Context, orgID, assessmentID, sessionID uuid.UUID) error {
	var valid bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM code_review_revision_assessments WHERE org_id=$1 AND id=$2 AND session_id=$3 AND review_scope='full' AND status IN ('running','publishing'))`, orgID, assessmentID, sessionID).Scan(&valid); err != nil {
		return err
	}
	if !valid {
		return ErrCodeReviewAssessmentState
	}
	statements := []string{
		`UPDATE code_review_session_metadata SET assessment_id=$2 WHERE org_id=$1 AND session_id=$3 AND assessment_id IS NULL`,
		`UPDATE code_review_agent_results SET assessment_id=$2 WHERE org_id=$1 AND session_id=$3 AND assessment_id IS NULL`,
		`UPDATE code_review_findings SET assessment_id=$2 WHERE org_id=$1 AND session_id=$3 AND assessment_id IS NULL`,
		`UPDATE code_review_prompt_records SET assessment_id=$2::uuid WHERE org_id=$1 AND session_id=$3 AND assessment_id IS NULL AND (metadata->>'assessment_id' IS NULL OR metadata->>'assessment_id'=$2::text)`,
	}
	for _, sql := range statements {
		if _, err := s.db.Exec(ctx, sql, orgID, assessmentID, sessionID); err != nil {
			return err
		}
	}
	return nil
}

// LinkVisualEvidence attaches only the checkpoint whose recorded assessment
// identity matches this assessment. A rejected recheck capture can coexist in
// the same session without becoming part of the completed full baseline.
func (s *CodeReviewAssessmentStore) LinkVisualEvidence(ctx context.Context, orgID, assessmentID uuid.UUID) error {
	result, err := s.db.Exec(ctx, `UPDATE code_review_prompt_records AS p
		SET assessment_id=$2::uuid
		FROM code_review_revision_assessments AS a
		WHERE a.org_id=$1 AND a.id=$2
		  AND p.org_id=a.org_id AND p.session_id=a.session_id
		  AND p.role='visual_evidence'
		  AND p.metadata->>'assessment_id'=$2::text
		  AND p.record_key LIKE 'code-review-prompts/' || a.session_id::text || '/assessments/' || a.id::text || '/%/visual-evidence-v1'
		  AND (p.assessment_id IS NULL OR p.assessment_id=$2)`, orgID, assessmentID)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ErrCodeReviewAssessmentState
	}
	return nil
}

func (s *CodeReviewAssessmentStore) ListAgentResults(ctx context.Context, orgID, assessmentID uuid.UUID) ([]models.CodeReviewAgentResult, error) {
	rows, err := s.db.Query(ctx, `SELECT `+codeReviewAgentResultColumns+` FROM code_review_agent_results WHERE org_id=$1 AND assessment_id=$2 ORDER BY created_at,id`, orgID, assessmentID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.CodeReviewAgentResult])
}

func (s *CodeReviewAssessmentStore) ListFindings(ctx context.Context, orgID, assessmentID uuid.UUID) ([]models.CodeReviewFinding, error) {
	rows, err := s.db.Query(ctx, `SELECT `+codeReviewFindingColumns+` FROM code_review_findings WHERE org_id=$1 AND assessment_id=$2 ORDER BY created_at,id`, orgID, assessmentID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.CodeReviewFinding])
}

func (s *CodeReviewAssessmentStore) ListPromptRecords(ctx context.Context, orgID, assessmentID uuid.UUID) ([]models.CodeReviewPromptRecord, error) {
	rows, err := s.db.Query(ctx, `SELECT `+codeReviewPromptRecordColumns+` FROM code_review_prompt_records WHERE org_id=$1 AND assessment_id=$2 ORDER BY created_at,id`, orgID, assessmentID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.CodeReviewPromptRecord])
}

func (s *CodeReviewAssessmentStore) MarkRunning(ctx context.Context, orgID, id uuid.UUID, generation int64, inputDigest string) error {
	tag, err := s.db.Exec(ctx, `UPDATE code_review_revision_assessments SET status='running' WHERE org_id=$1 AND id=$2 AND generation=$3 AND input_digest=$4 AND status='reserved'`, orgID, id, generation, inputDigest)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrCodeReviewAssessmentState
	}
	return nil
}

func validateAssessmentCompletion(result models.CodeReviewAssessmentCompletion) error {
	if err := result.ResultOrigin.Validate(); err != nil {
		return err
	}
	if err := result.Decision.Validate(); err != nil {
		return err
	}
	if _, err := canonicalAssessmentManifest(result.StructuredOutcome); err != nil {
		return fmt.Errorf("completion requires object structured outcome: %w", err)
	}
	if len(result.RiskReasonDetails) > 0 && !json.Valid(result.RiskReasonDetails) {
		return fmt.Errorf("invalid risk reason details")
	}
	return nil
}

func assessmentJSONEqual(a, b json.RawMessage) bool {
	if len(a) == 0 || len(b) == 0 {
		return len(a) == 0 && len(b) == 0
	}
	var av, bv any
	if json.Unmarshal(a, &av) != nil || json.Unmarshal(b, &bv) != nil {
		return false
	}
	return reflect.DeepEqual(av, bv)
}

// StageOutcome commits the deterministic decision before any external write.
// A retry may restage the exact same result, but may not change it while the
// publication marker is unresolved.
func (s *CodeReviewAssessmentStore) StageOutcome(ctx context.Context, orgID, id uuid.UUID, generation int64, inputDigest string, result models.CodeReviewAssessmentCompletion) error {
	if err := validateAssessmentCompletion(result); err != nil {
		return err
	}
	rows, err := s.db.Query(ctx, `UPDATE code_review_revision_assessments SET result_origin=$5,coverage_complete=$6,decision=$7,acceptable=$8,risk_reason_details=$9,structured_outcome=$10,rendered_body=$11 WHERE org_id=$1 AND id=$2 AND generation=$3 AND input_digest=$4 AND status='running' AND result_origin IS NULL RETURNING id`, orgID, id, generation, inputDigest, result.ResultOrigin, result.CoverageComplete, result.Decision, result.Acceptable, result.RiskReasonDetails, result.StructuredOutcome, result.RenderedBody)
	if err != nil {
		return err
	}
	_, err = pgx.CollectOneRow(rows, pgx.RowTo[uuid.UUID])
	if err == nil {
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	existing, err := s.GetByID(ctx, orgID, id)
	if err != nil {
		return err
	}
	if existing.Generation != generation || existing.InputDigest != inputDigest || existing.Status != models.CodeReviewAssessmentRunning && existing.Status != models.CodeReviewAssessmentPublishing || existing.ResultOrigin == nil || existing.Decision == nil || existing.Acceptable == nil || existing.RenderedBody == nil || *existing.ResultOrigin != result.ResultOrigin || existing.CoverageComplete != result.CoverageComplete || *existing.Decision != result.Decision || *existing.Acceptable != result.Acceptable || *existing.RenderedBody != result.RenderedBody || !assessmentJSONEqual(existing.RiskReasonDetails, result.RiskReasonDetails) || !assessmentJSONEqual(existing.StructuredOutcome, result.StructuredOutcome) {
		return ErrCodeReviewAssessmentConflict
	}
	return nil
}

func (s *CodeReviewAssessmentStore) ReservePublication(ctx context.Context, orgID, id uuid.UUID, generation int64, inputDigest, submittedCommitSHA string) error {
	if submittedCommitSHA == "" {
		return fmt.Errorf("publication requires submitted commit SHA")
	}
	tag, err := s.db.Exec(ctx, `UPDATE code_review_revision_assessments SET status='publishing',publication_state='reserved',submitted_commit_sha=$5 WHERE org_id=$1 AND id=$2 AND generation=$3 AND input_digest=$4 AND status='running' AND publication_state='not_started' AND result_origin IS NOT NULL AND head_sha=$5`, orgID, id, generation, inputDigest, submittedCommitSHA)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrCodeReviewAssessmentState
	}
	return nil
}

// MarkPublicationAttemptUncertain commits before the external send. Once this
// transition succeeds, a missing receipt cannot prove that nothing was sent.
func (s *CodeReviewAssessmentStore) MarkPublicationAttemptUncertain(ctx context.Context, orgID, id uuid.UUID, generation int64, inputDigest string) error {
	tag, err := s.db.Exec(ctx, `UPDATE code_review_revision_assessments SET publication_state='uncertain' WHERE org_id=$1 AND id=$2 AND generation=$3 AND input_digest=$4 AND status='publishing' AND publication_state='reserved' AND result_origin IS NOT NULL AND submitted_commit_sha=head_sha AND publication_receipt IS NULL AND github_review_id IS NULL`, orgID, id, generation, inputDigest)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrCodeReviewAssessmentState
	}
	return nil
}

// NoteUncertainPublication keeps an actionable reason on an active assessment
// without changing its immutable outcome or implying that a send did not occur.
func (s *CodeReviewAssessmentStore) NoteUncertainPublication(ctx context.Context, orgID, id uuid.UUID, generation int64, inputDigest, detail string) error {
	if detail == "" {
		return fmt.Errorf("reconciliation detail is required")
	}
	tag, err := s.db.Exec(ctx, `UPDATE code_review_revision_assessments SET failure_detail=$5 WHERE org_id=$1 AND id=$2 AND generation=$3 AND input_digest=$4 AND status='publishing' AND publication_state='uncertain' AND result_origin IS NOT NULL`, orgID, id, generation, inputDigest, detail)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrCodeReviewAssessmentState
	}
	return nil
}

// SupersedeUnsentPublication is legal only while no send was started. The
// caller must hold the PR publication lock when deciding freshness.
func (s *CodeReviewAssessmentStore) SupersedeUnsentPublication(ctx context.Context, orgID, id uuid.UUID, generation int64, inputDigest, detail string) error {
	if detail == "" {
		return fmt.Errorf("supersede detail is required")
	}
	tag, err := s.db.Exec(ctx, `UPDATE code_review_revision_assessments SET status='superseded',failure_detail=$5,completed_at=now(),superseded_at=now() WHERE org_id=$1 AND id=$2 AND generation=$3 AND input_digest=$4 AND status='publishing' AND publication_state='reserved' AND result_origin IS NOT NULL AND publication_receipt IS NULL AND github_review_id IS NULL`, orgID, id, generation, inputDigest, detail)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrCodeReviewAssessmentState
	}
	return nil
}

// RecordPublication stores the assessment-specific result of a reconciled
// external write. A confirmed receipt cannot be replaced by a later retry.
func (s *CodeReviewAssessmentStore) RecordPublication(ctx context.Context, orgID, id uuid.UUID, generation int64, inputDigest string, state models.CodeReviewPublicationState, receipt json.RawMessage, githubReviewID *int64, githubReviewURL *string) error {
	if state != models.CodeReviewPublicationUncertain && state != models.CodeReviewPublicationConfirmed {
		return fmt.Errorf("unsupported publication result %q", state)
	}
	if len(receipt) > 0 && !json.Valid(receipt) {
		return fmt.Errorf("publication receipt is not valid JSON")
	}
	if state == models.CodeReviewPublicationConfirmed && (githubReviewID == nil || len(receipt) == 0) {
		return fmt.Errorf("confirmed publication requires review identity and receipt")
	}
	tag, err := s.db.Exec(ctx, `UPDATE code_review_revision_assessments SET publication_state=$5,publication_receipt=$6,github_review_id=$7,github_review_url=$8,failure_detail=CASE WHEN $5='confirmed' THEN NULL ELSE failure_detail END WHERE org_id=$1 AND id=$2 AND generation=$3 AND input_digest=$4 AND status='publishing' AND (($5='uncertain' AND publication_state='reserved') OR ($5='confirmed' AND publication_state='uncertain'))`, orgID, id, generation, inputDigest, state, receipt, githubReviewID, githubReviewURL)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrCodeReviewAssessmentState
	}
	return nil
}

// MarkPublicationNotRequired is for completed reviews whose deterministic
// decision has no external write. It cannot race an existing reservation.
func (s *CodeReviewAssessmentStore) MarkPublicationNotRequired(ctx context.Context, orgID, id uuid.UUID, generation int64, inputDigest string) error {
	tag, err := s.db.Exec(ctx, `UPDATE code_review_revision_assessments SET publication_state='not_required' WHERE org_id=$1 AND id=$2 AND generation=$3 AND input_digest=$4 AND status='running' AND publication_state='not_started' AND result_origin IS NOT NULL`, orgID, id, generation, inputDigest)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrCodeReviewAssessmentState
	}
	return nil
}

func (s *CodeReviewAssessmentStore) Complete(ctx context.Context, orgID, id uuid.UUID, generation int64, inputDigest string, result models.CodeReviewAssessmentCompletion) error {
	if err := validateAssessmentCompletion(result); err != nil {
		return err
	}
	tag, err := s.db.Exec(ctx, `UPDATE code_review_revision_assessments SET status='completed',completed_at=now() WHERE org_id=$1 AND id=$2 AND generation=$3 AND input_digest=$4 AND status IN ('running','publishing') AND publication_state IN ('confirmed','not_required') AND result_origin=$5 AND coverage_complete=$6 AND decision=$7 AND acceptable=$8 AND risk_reason_details IS NOT DISTINCT FROM $9::jsonb AND structured_outcome=$10::jsonb AND rendered_body=$11`, orgID, id, generation, inputDigest, result.ResultOrigin, result.CoverageComplete, result.Decision, result.Acceptable, result.RiskReasonDetails, result.StructuredOutcome, result.RenderedBody)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrCodeReviewAssessmentState
	}
	return nil
}

func (s *CodeReviewAssessmentStore) Fail(ctx context.Context, orgID, id uuid.UUID, generation int64, inputDigest, detail string) error {
	if detail == "" {
		return fmt.Errorf("failure detail is required")
	}
	tag, err := s.db.Exec(ctx, `UPDATE code_review_revision_assessments SET status='failed',failure_detail=$5,completed_at=now() WHERE org_id=$1 AND id=$2 AND generation=$3 AND input_digest=$4 AND ((status IN ('reserved','running') AND publication_state='not_started') OR (status='publishing' AND publication_state='reserved' AND publication_receipt IS NULL AND github_review_id IS NULL))`, orgID, id, generation, inputDigest, detail)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrCodeReviewAssessmentState
	}
	return nil
}

func (s *CodeReviewAssessmentStore) Supersede(ctx context.Context, orgID, id uuid.UUID, generation int64, inputDigest, detail string) error {
	if detail == "" {
		return fmt.Errorf("supersede detail is required")
	}
	tag, err := s.db.Exec(ctx, `UPDATE code_review_revision_assessments SET status='superseded',failure_detail=$5,completed_at=now(),superseded_at=now() WHERE org_id=$1 AND id=$2 AND generation=$3 AND input_digest=$4 AND status IN ('reserved','running') AND publication_state='not_started'`, orgID, id, generation, inputDigest, detail)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrCodeReviewAssessmentState
	}
	return nil
}

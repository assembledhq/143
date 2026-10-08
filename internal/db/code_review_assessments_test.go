package db

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestCodeReviewAssessmentStore_ListCurrentForPRsKeepsFailureSeparate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		includeFailure bool
	}{
		{name: "latest failed attempt is separate from completed coverage", includeFailure: true},
		{name: "newer completed attempt clears latest failure", includeFailure: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID, prID, sessionID := uuid.New(), uuid.New(), uuid.New()
			completedID, failedID := uuid.New(), uuid.New()
			created := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
			decision := models.CodeReviewDecisionBlocked
			acceptable := false
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create isolated assessment reader mock")
			defer mock.Close()
			rows := pgxmock.NewRows([]string{"id", "pull_request_id", "session_id", "source_assessment_id", "status", "review_scope", "route_reason", "decision", "acceptable", "risk_reason_details", "head_sha", "publication_state", "created_at", "completed_at", "kind"}).
				AddRow(completedID, prID, sessionID, nil, "completed", "full", "initial_full", &decision, &acceptable, []byte(`[]`), "head", "confirmed", created, &created, "current")
			if tt.includeFailure {
				rows.AddRow(failedID, prID, sessionID, &completedID, "failed", "evidence_only", "visual_changed", nil, nil, nil, "head", "not_required", created.Add(time.Minute), nil, "failed")
			}
			mock.ExpectQuery(`(?s)latest_terminal AS .*status IN \('completed','failed','superseded','cancelled'\).*SELECT \* FROM latest_terminal WHERE status IN \('failed','superseded'\)`).
				WithArgs(orgID, []uuid.UUID{prID}).WillReturnRows(rows)
			current, active, failed, err := NewCodeReviewAssessmentStore(mock).ListCurrentForPRs(context.Background(), orgID, []uuid.UUID{prID})
			require.NoError(t, err, "read completed coverage and latest failed attempt")
			require.Equal(t, completedID, current[prID].ID, "completed coverage remains the current result")
			require.Empty(t, active, "terminal attempts are not active")
			if tt.includeFailure {
				require.Equal(t, failedID, failed[prID].ID, "latest failed attempt is exposed separately")
			} else {
				require.Empty(t, failed, "a newer completed attempt has no latest failure")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "reader query must stay scoped to this organization and PR")
		})
	}
}

func testAssessmentCapture() models.CodeReviewAssessmentCapture {
	return models.CodeReviewAssessmentCapture{
		ID: uuid.New(), OrgID: uuid.New(), RepositoryID: uuid.New(), PullRequestID: uuid.New(), PolicyID: uuid.New(), SessionID: uuid.New(), Generation: 1,
		BaseSHA: "base", BaseRef: "main", HeadSHA: "head", InputVersion: 1,
		CodeDigest: "code", ContractDigest: "contract", IntentDigest: "intent", VisualDigest: "visual", RequestDigest: "request", GateDigest: "gate", InputDigest: "all",
		InputManifest: json.RawMessage(`{"version":1}`), ReviewScope: models.CodeReviewScopeFull, RouteReason: "initial_full", PublicationKey: "assessment:" + uuid.NewString(),
	}
}

func TestCodeReviewAssessmentCaptureValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		change func(*models.CodeReviewAssessmentCapture)
	}{
		{"missing org", func(c *models.CodeReviewAssessmentCapture) { c.OrgID = uuid.Nil }},
		{"missing head", func(c *models.CodeReviewAssessmentCapture) { c.HeadSHA = "" }},
		{"missing digest", func(c *models.CodeReviewAssessmentCapture) { c.GateDigest = "" }},
		{"missing manifest", func(c *models.CodeReviewAssessmentCapture) { c.InputManifest = nil }},
		{"non object manifest", func(c *models.CodeReviewAssessmentCapture) { c.InputManifest = json.RawMessage(`[]`) }},
		{"invalid scope", func(c *models.CodeReviewAssessmentCapture) { c.ReviewScope = "partial" }},
		{"evidence without baseline", func(c *models.CodeReviewAssessmentCapture) { c.ReviewScope = models.CodeReviewScopeEvidenceOnly }},
		{"full with baseline", func(c *models.CodeReviewAssessmentCapture) { id := uuid.New(); c.SourceAssessmentID = &id }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			capture := testAssessmentCapture()
			tt.change(&capture)
			require.Error(t, validateAssessmentCapture(capture), "invalid capture must fail before database write")
		})
	}
}

func TestCodeReviewAssessmentCaptureEquality(t *testing.T) {
	t.Parallel()
	base := testAssessmentCapture()
	manifest, err := canonicalAssessmentManifest(base.InputManifest)
	require.NoError(t, err, "baseline manifest should canonicalize")
	actual := models.CodeReviewAssessment{
		ID: base.ID, OrgID: base.OrgID, RepositoryID: base.RepositoryID, PullRequestID: base.PullRequestID, PolicyID: base.PolicyID, SessionID: base.SessionID, Generation: base.Generation,
		BaseSHA: base.BaseSHA, BaseRef: base.BaseRef, HeadSHA: base.HeadSHA, InputVersion: base.InputVersion,
		CodeDigest: base.CodeDigest, ContractDigest: base.ContractDigest, IntentDigest: base.IntentDigest, VisualDigest: base.VisualDigest, RequestDigest: base.RequestDigest, GateDigest: base.GateDigest, InputDigest: base.InputDigest,
		InputManifest: json.RawMessage(`{"version":1}`), ReviewScope: base.ReviewScope, RouteReason: base.RouteReason, PublicationKey: base.PublicationKey,
	}
	require.True(t, assessmentCaptureEqual(base, manifest, actual, true), "exact identity and inputs should be reusable")
	actual.VisualDigest = "new evidence"
	require.False(t, assessmentCaptureEqual(base, manifest, actual, true), "new visual evidence must conflict with the same assessment identity")
}

func TestCanonicalAssessmentManifest(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, raw, expected string
	}{
		{"nested JSONB order", `{"version":3,"contract":{"policy_version":9007199254740993,"policy_id":"policy","policy_digest":"digest"}}`, `{"contract":{"policy_digest":"digest","policy_id":"policy","policy_version":9007199254740993},"version":3}`},
		{"objects inside arrays", `{"files":[{"patch":"digest","name":"file.go"}]}`, `{"files":[{"name":"file.go","patch":"digest"}]}`},
		{"trailing document", `{} {}`, ""},
		{"null document", `null`, ""},
		{"array document", `[]`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			actual, err := canonicalAssessmentManifest(json.RawMessage(tt.raw))
			if tt.expected == "" {
				require.Error(t, err, "manifest must be exactly one JSON object")
				return
			}
			require.NoError(t, err, "valid manifest should canonicalize")
			require.Equal(t, json.RawMessage(tt.expected), actual, "canonical form must preserve exact numbers and sort all object keys")
		})
	}
}

func TestCodeReviewAssessmentCompleteStateFence(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		rows int64
		want error
	}{
		{"owned running assessment", 1, nil},
		{"stale or terminal assessment", 0, ErrCodeReviewAssessmentState},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create isolated mock")
			defer mock.Close()
			capture := testAssessmentCapture()
			mock.ExpectExec(regexp.QuoteMeta("UPDATE code_review_revision_assessments SET status='completed'")).
				WithArgs(capture.OrgID, capture.ID, capture.Generation, capture.InputDigest, models.CodeReviewResultExecuted, true, models.CodeReviewDecisionBlocked, false, pgxmock.AnyArg(), pgxmock.AnyArg(), "body").
				WillReturnResult(pgxmock.NewResult("UPDATE", tt.rows))
			result := models.CodeReviewAssessmentCompletion{ResultOrigin: models.CodeReviewResultExecuted, CoverageComplete: true, Decision: models.CodeReviewDecisionBlocked, Acceptable: false, StructuredOutcome: json.RawMessage(`{}`), RenderedBody: "body"}
			err = NewCodeReviewAssessmentStore(mock).Complete(context.Background(), capture.OrgID, capture.ID, capture.Generation, capture.InputDigest, result)
			require.True(t, errors.Is(err, tt.want), "completion should honor the state and generation fence")
			require.NoError(t, mock.ExpectationsWereMet(), "completion should use the expected fenced update")
		})
	}
}

func TestCodeReviewAssessmentPublicationSendFence(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		rows int64
		want error
	}{
		{"reserved publication", 1, nil},
		{"stale or already attempted publication", 0, ErrCodeReviewAssessmentState},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create isolated mock")
			defer mock.Close()
			capture := testAssessmentCapture()
			mock.ExpectExec(regexp.QuoteMeta("UPDATE code_review_revision_assessments SET publication_state='uncertain'")).
				WithArgs(capture.OrgID, capture.ID, capture.Generation, capture.InputDigest).
				WillReturnResult(pgxmock.NewResult("UPDATE", tt.rows))
			err = NewCodeReviewAssessmentStore(mock).MarkPublicationAttemptUncertain(context.Background(), capture.OrgID, capture.ID, capture.Generation, capture.InputDigest)
			require.ErrorIs(t, err, tt.want, "publication send must fence against stale or already attempted state")
			require.NoError(t, mock.ExpectationsWereMet(), "publication send should use the fenced update")
		})
	}
}

func TestCodeReviewAssessmentSupersedeUnsentPublicationFence(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		rows int64
		want error
	}{
		{"reserved unsent publication", 1, nil},
		{"uncertain or terminal publication", 0, ErrCodeReviewAssessmentState},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create isolated mock")
			defer mock.Close()
			capture := testAssessmentCapture()
			mock.ExpectExec(regexp.QuoteMeta("UPDATE code_review_revision_assessments SET status='superseded'")).
				WithArgs(capture.OrgID, capture.ID, capture.Generation, capture.InputDigest, "inputs changed").
				WillReturnResult(pgxmock.NewResult("UPDATE", tt.rows))
			err = NewCodeReviewAssessmentStore(mock).SupersedeUnsentPublication(context.Background(), capture.OrgID, capture.ID, capture.Generation, capture.InputDigest, "inputs changed")
			require.ErrorIs(t, err, tt.want, "unsent supersede must reject any possible external send")
			require.NoError(t, mock.ExpectationsWereMet(), "unsent supersede should use the fenced update")
		})
	}
}

package db

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestCodeReviewDisputeStore_CreateAndEnqueueTriageDedupesGitHubSourceWithoutAbortingTransaction(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create the database mock")
	defer mock.Close()

	commentID := int64(991)
	now := time.Date(2026, time.August, 2, 8, 0, 0, 0, time.UTC)
	dispute := models.CodeReviewDispute{
		OrgID: uuid.New(), SessionID: uuid.New(), PullRequestID: uuid.New(), RepositoryID: uuid.New(), PolicyID: uuid.New(),
		ReviewedHeadSHA: "abc123", Decision: models.CodeReviewDecisionBlocked,
		FiledByLogin: "octocat", AuthorAssociation: "MEMBER", AuthorIsPRAuthor: true,
		RepositoryVisibility: models.CodeReviewRepositoryVisibilityPrivate,
		MembershipEvidence:   json.RawMessage(`{"association":"MEMBER"}`),
		Source:               models.CodeReviewDisputeSourceGitHubComment, GitHubCommentID: &commentID,
		SourceBodyHash: "body-hash", SourceVersion: 42, Body: "This should have been approved.",
		SemanticInputHashAtFiling: "semantic-hash", ReplyStatus: models.CodeReviewDisputeReplyPending,
	}
	existing := dispute
	existing.ID = uuid.New()
	existing.IntakeStatus = models.CodeReviewDisputeIntakePending
	existing.ReassessmentStatus = models.CodeReviewDisputeReassessmentNotRequested
	existing.QueueSignals = json.RawMessage(`{}`)
	existing.Version = 1
	existing.CreatedAt = now
	existing.UpdatedAt = now

	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO code_review_decision_disputes").
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows(codeReviewDisputeColumnNames()))
	mock.ExpectQuery("SELECT[\\s\\S]+FROM code_review_decision_disputes[\\s\\S]+github_comment_id").
		WithArgs(dispute.OrgID, commentID, int64(42)).
		WillReturnRows(codeReviewDisputeMockRows(existing))
	mock.ExpectRollback()

	store := NewCodeReviewDisputeStore(mock)
	store.SetJobStore(NewJobStore(mock))
	created, err := store.CreateAndEnqueueTriage(context.Background(), &dispute)

	require.NoError(t, err, "GitHub source redelivery should reuse the immutable dispute row")
	require.False(t, created, "GitHub source redelivery should not report a new filing mutation")
	require.Equal(t, existing, dispute, "dedupe should return the exact previously-created dispute")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func codeReviewDisputeColumnNames() []string {
	raw := strings.ReplaceAll(codeReviewDisputeColumns, "\n", "")
	parts := strings.Split(raw, ",")
	for index := range parts {
		parts[index] = strings.TrimSpace(parts[index])
	}
	return parts
}

func codeReviewDisputeMockRows(dispute models.CodeReviewDispute) *pgxmock.Rows {
	return pgxmock.NewRows(codeReviewDisputeColumnNames()).AddRow(
		dispute.ID, dispute.OrgID, dispute.SessionID, dispute.PullRequestID, dispute.RepositoryID, dispute.PolicyID,
		dispute.ReviewedHeadSHA, dispute.Decision, dispute.Direction, dispute.FiledByUserID, dispute.FiledByLogin, dispute.AuthorAssociation,
		dispute.AuthorIsPRAuthor, dispute.RepositoryVisibility, dispute.MembershipEvidence, dispute.TrustOverride, dispute.Source,
		dispute.GitHubCommentID, dispute.GitHubThreadRootCommentID, dispute.ReplyCommentID, dispute.SourceBodyHash, dispute.SourceVersion,
		dispute.Body, dispute.ContestedReasonCodes, dispute.DisputeKind, dispute.AssertsNewInformation, dispute.Routing, dispute.IntakeStatus,
		dispute.IntakeConfidence, dispute.ReassessmentSessionID, dispute.ReassessmentDecision, dispute.ReassessmentFlipped,
		dispute.ReassessmentStatus, dispute.SemanticInputHashAtFiling, dispute.SemanticInputHashAtRerun,
		dispute.AdjudicationStatus, dispute.AdjudicatedByUserID, dispute.AdjudicatedAt, dispute.AdjudicationNote, dispute.EscalatedAt,
		dispute.EscalatedByUserID, dispute.QueueSignals, dispute.QueuePriority, dispute.ReplyStatus, dispute.ReplyCycleReserved, dispute.StatusDetail,
		dispute.Version, dispute.CreatedAt, dispute.UpdatedAt,
	)
}

func TestCodeReviewDisputeStore_AdmissionGuards(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		existingStatus   string
		equivalentRecent bool
		active           int
		expectedAdmitted bool
		expectDeduped    bool
		expectDenied     bool
	}{
		{name: "reuses existing admitted decision", existingStatus: "admitted", expectedAdmitted: true},
		{name: "dedupes equivalent input inside rolling cooldown", equivalentRecent: true, expectDeduped: true},
		{name: "denies admission at platform active ceiling", active: 2, expectDenied: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "test should create the database mock")
			defer mock.Close()

			dispute := models.CodeReviewDispute{
				ID: uuid.New(), OrgID: uuid.New(), PullRequestID: uuid.New(), RepositoryID: uuid.New(),
			}
			semanticHash := "stable-semantic-hash"
			mock.ExpectBegin()
			mock.ExpectExec("SELECT pg_advisory_xact_lock").WillReturnResult(pgxmock.NewResult("SELECT", 1))
			existingRows := pgxmock.NewRows([]string{"status"})
			if tt.existingStatus != "" {
				existingRows.AddRow(tt.existingStatus)
			}
			mock.ExpectQuery("SELECT status[\\s\\S]+code_review_reassessment_admissions").
				WithArgs(pgx.NamedArgs{"org_id": dispute.OrgID, "dispute_id": dispute.ID}).
				WillReturnRows(existingRows)
			if tt.existingStatus == "" {
				equivalentRows := pgxmock.NewRows([]string{"id"})
				if tt.equivalentRecent {
					equivalentRows.AddRow(uuid.New())
				}
				mock.ExpectQuery("SELECT id[\\s\\S]+created_at >= now\\(\\) - make_interval").
					WithArgs(pgx.NamedArgs{
						"org_id": dispute.OrgID, "pull_request_id": dispute.PullRequestID,
						"semantic_hash": semanticHash, "cooldown_seconds": int64(900),
					}).
					WillReturnRows(equivalentRows)
			}
			if tt.expectDeduped {
				mock.ExpectExec("INSERT INTO code_review_reassessment_admissions[\\s\\S]+'deduped'").
					WithArgs(pgx.NamedArgs{
						"org_id": dispute.OrgID, "dispute_id": dispute.ID, "pull_request_id": dispute.PullRequestID,
						"repository_id": dispute.RepositoryID, "user_id": (*uuid.UUID)(nil), "semantic_hash": semanticHash,
					}).
					WillReturnResult(pgxmock.NewResult("INSERT", 1))
				mock.ExpectExec("UPDATE code_review_decision_disputes[\\s\\S]+reassessment_status = 'deduped'").
					WithArgs(pgx.NamedArgs{"org_id": dispute.OrgID, "id": dispute.ID, "semantic_hash": semanticHash}).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			}
			if tt.expectDenied {
				mock.ExpectQuery("SELECT count\\(\\*\\) FROM code_review_decision_disputes").
					WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(tt.active))
				mock.ExpectExec("INSERT INTO code_review_reassessment_admissions[\\s\\S]+'denied'").
					WithArgs(pgx.NamedArgs{
						"org_id": dispute.OrgID, "dispute_id": dispute.ID, "pull_request_id": dispute.PullRequestID,
						"repository_id": dispute.RepositoryID, "user_id": (*uuid.UUID)(nil), "semantic_hash": semanticHash,
					}).
					WillReturnResult(pgxmock.NewResult("INSERT", 1))
				mock.ExpectExec("UPDATE code_review_decision_disputes[\\s\\S]+routing = 'policy_signal_only'").
					WithArgs(pgx.NamedArgs{"org_id": dispute.OrgID, "id": dispute.ID, "semantic_hash": semanticHash}).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			}
			mock.ExpectCommit()

			store := NewCodeReviewDisputeStore(mock)
			store.SetJobStore(NewJobStore(mock))
			admitted, err := store.AdmitAndEnqueueReassessment(context.Background(), dispute, nil, semanticHash, 15*time.Minute, 2, map[string]any{"dispute_id": dispute.ID})

			require.NoError(t, err, "guarded reassessment admission should commit one durable outcome")
			require.Equal(t, tt.expectedAdmitted, admitted, "admission result should reflect existing, duplicate, and ceiling decisions")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestCodeReviewDisputeStore_EscalateRecordsPerUserDemandSignal(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create the database mock")
	defer mock.Close()
	orgID := uuid.New()
	disputeID := uuid.New()
	userID := uuid.New()
	note := "Please review this policy threshold."
	routing := models.CodeReviewDisputeRoutingPolicySignalOnly
	status := models.CodeReviewDisputeAdjudicationPending
	now := time.Date(2026, time.August, 2, 12, 0, 0, 0, time.UTC)
	dispute := models.CodeReviewDispute{
		ID: disputeID, OrgID: orgID, SessionID: uuid.New(), PullRequestID: uuid.New(), RepositoryID: uuid.New(), PolicyID: uuid.New(),
		Routing: &routing, AdjudicationStatus: &status, EscalatedAt: &now, EscalatedByUserID: &userID,
		QueueSignals: json.RawMessage(`{}`), Version: 2, CreatedAt: now, UpdatedAt: now,
	}
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO code_review_dispute_escalations[\\s\\S]+ON CONFLICT \\(org_id, dispute_id, user_id\\) DO NOTHING").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "id": disputeID, "user_id": userID, "note": note}).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectQuery("UPDATE code_review_decision_disputes[\\s\\S]+escalated_at = COALESCE").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "id": disputeID, "user_id": userID, "note": note}).
		WillReturnRows(codeReviewDisputeMockRows(dispute))
	mock.ExpectCommit()

	actual, err := NewCodeReviewDisputeStore(mock).Escalate(context.Background(), orgID, disputeID, userID, "  "+note+"  ")

	require.NoError(t, err, "eligible policy feedback should be escalated atomically")
	require.Equal(t, dispute, actual, "escalation should return the updated dispute")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestCodeReviewDisputeStore_CompleteReassessmentOnceReportsTransition(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create the database mock")
	defer mock.Close()
	orgID := uuid.New()
	disputeID := uuid.New()
	sessionID := uuid.New()
	decision := models.CodeReviewDecisionApproved
	args := pgx.NamedArgs{
		"org_id": orgID, "id": disputeID, "session_id": sessionID, "decision": &decision,
		"reassessment_status": models.CodeReviewDisputeReassessmentCompleted, "detail": "Decision changed.",
	}
	mock.ExpectExec("UPDATE code_review_decision_disputes[\\s\\S]+reassessment_status IN \\('queued', 'running'\\)").
		WithArgs(args).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE code_review_decision_disputes[\\s\\S]+reassessment_status IN \\('queued', 'running'\\)").
		WithArgs(args).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	store := NewCodeReviewDisputeStore(mock)

	first, err := store.CompleteReassessmentOnce(context.Background(), orgID, disputeID, sessionID, models.CodeReviewSessionStatusCompleted, &decision, "Decision changed.")
	require.NoError(t, err, "the first terminal projection should succeed")
	second, err := store.CompleteReassessmentOnce(context.Background(), orgID, disputeID, sessionID, models.CodeReviewSessionStatusCompleted, &decision, "Decision changed.")
	require.NoError(t, err, "a repeated terminal projection should remain idempotent")

	require.True(t, first, "the first projection should report a state transition")
	require.False(t, second, "a repeated projection should not report another state transition")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestCodeReviewDisputeStore_AdjudicateDemotesUntrustedPendingItem(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create the database mock")
	defer mock.Close()
	orgID := uuid.New()
	disputeID := uuid.New()
	userID := uuid.New()
	untrusted := false
	now := time.Date(2026, time.August, 2, 12, 0, 0, 0, time.UTC)
	dispute := models.CodeReviewDispute{
		ID: disputeID, OrgID: orgID, SessionID: uuid.New(), PullRequestID: uuid.New(), RepositoryID: uuid.New(), PolicyID: uuid.New(),
		TrustOverride: &untrusted, QueueSignals: json.RawMessage(`{}`), Version: 4, CreatedAt: now, UpdatedAt: now,
	}
	update := models.CodeReviewDisputeAdjudicationUpdate{
		ExpectedVersion: 3, TrustOverride: &untrusted, TrustOverridePresent: true,
	}
	mock.ExpectQuery("UPDATE code_review_decision_disputes[\\s\\S]+WHEN @trust_override_present AND adjudication_status = 'pending' THEN NULL").
		WithArgs(pgx.NamedArgs{
			"org_id": orgID, "id": disputeID, "user_id": userID, "expected_version": 3,
			"adjudication_status": (*models.CodeReviewDisputeAdjudicationStatus)(nil), "adjudication_note": (*string)(nil),
			"trust_override_present": true, "trust_override": &untrusted,
		}).
		WillReturnRows(codeReviewDisputeMockRows(dispute))

	actual, err := NewCodeReviewDisputeStore(mock).Adjudicate(context.Background(), orgID, disputeID, userID, update)

	require.NoError(t, err, "an admin should be able to remove pending queue influence with a negative trust override")
	require.Nil(t, actual.AdjudicationStatus, "an untrusted pending item should leave the adjudication queue")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestCodeReviewDisputeStore_ReserveReplyCycle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		alreadyReserved  bool
		cycles           int
		expectedReserved bool
		expectSpend      bool
		expectLoopGuard  bool
	}{
		{name: "reuses an existing reservation", alreadyReserved: true, cycles: 2, expectedReserved: true},
		{name: "spends one cycle for the first reply", cycles: 1, expectedReserved: true, expectSpend: true},
		{name: "stops at the machine cycle ceiling", cycles: 2, expectLoopGuard: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "test should create the database mock")
			defer mock.Close()

			orgID := uuid.New()
			disputeID := uuid.New()
			pullRequestID := uuid.New()
			mock.ExpectBegin()
			mock.ExpectQuery("SELECT dispute.pull_request_id, dispute.reply_cycle_reserved").
				WithArgs(pgx.NamedArgs{"org_id": orgID, "dispute_id": disputeID}).
				WillReturnRows(pgxmock.NewRows([]string{"pull_request_id", "reply_cycle_reserved", "code_review_dispute_cycles_in_epoch"}).
					AddRow(pullRequestID, tt.alreadyReserved, tt.cycles))
			if tt.expectLoopGuard {
				mock.ExpectExec("UPDATE code_review_decision_disputes[\\s\\S]+reply_status = 'failed'").
					WithArgs(pgx.NamedArgs{"org_id": orgID, "dispute_id": disputeID}).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			}
			if tt.expectSpend {
				mock.ExpectExec("UPDATE pull_requests[\\s\\S]+code_review_dispute_cycles_in_epoch = code_review_dispute_cycles_in_epoch \\+ 1").
					WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": pullRequestID}).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				mock.ExpectExec("UPDATE code_review_decision_disputes[\\s\\S]+reply_cycle_reserved = true").
					WithArgs(pgx.NamedArgs{"org_id": orgID, "dispute_id": disputeID}).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			}
			mock.ExpectCommit()

			reserved, err := NewCodeReviewDisputeStore(mock).ReserveReplyCycle(context.Background(), orgID, disputeID, 2)

			require.NoError(t, err, "reply cycle reservation should complete atomically")
			require.Equal(t, tt.expectedReserved, reserved, "reservation result should match the durable loop-guard state")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

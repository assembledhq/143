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
	mock.ExpectQuery("INSERT INTO code_review_decision_disputes[\\s\\S]+reply_comment_id[\\s\\S]+reply_cycle_reserved").
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows(codeReviewDisputeColumnNames()))
	mock.ExpectQuery("SELECT[\\s\\S]+FROM code_review_decision_disputes[\\s\\S]+github_comment_id").
		WithArgs(dispute.OrgID, commentID, int64(42)).
		WillReturnRows(codeReviewDisputeMockRows(existing))
	mock.ExpectRollback()

	store := NewCodeReviewDisputeStore(mock)
	store.SetJobStore(NewJobStore(mock))
	created, err := store.CreateAndEnqueueTriage(context.Background(), &dispute, CodeReviewDisputeIntakeGuard{})

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
	return pgxmock.NewRows(codeReviewDisputeColumnNames()).AddRow(codeReviewDisputeMockValues(dispute)...)
}

func codeReviewDisputeMockValues(dispute models.CodeReviewDispute) []any {
	return []any{
		dispute.ID, dispute.OrgID, dispute.SessionID, dispute.PullRequestID, dispute.RepositoryID, dispute.PolicyID,
		dispute.ReviewedHeadSHA, dispute.Decision, dispute.Direction, dispute.FiledByUserID, dispute.FiledByLogin, dispute.AuthorAssociation,
		dispute.AuthorIsPRAuthor, dispute.RepositoryVisibility, dispute.MembershipEvidence, dispute.TrustOverride, dispute.Source,
		dispute.GitHubCommentID, dispute.GitHubThreadRootCommentID, dispute.ReplyCommentID, dispute.SourceBodyHash, dispute.SourceVersion,
		dispute.SourceUpdatedAt,
		dispute.Body, dispute.ContestedReasonCodes, dispute.DisputeKind, dispute.AssertsNewInformation, dispute.Routing, dispute.IntakeStatus,
		dispute.IntakeConfidence, dispute.ReassessmentSessionID, dispute.ReassessmentDecision, dispute.ReassessmentFlipped,
		dispute.ReassessmentStatus, dispute.SemanticInputHashAtFiling, dispute.SemanticInputHashAtRerun,
		dispute.AdjudicationStatus, dispute.AdjudicatedByUserID, dispute.AdjudicatedAt, dispute.AdjudicationNote, dispute.PolicyOwnerActiveSeconds, dispute.EscalatedAt,
		dispute.EscalatedByUserID, dispute.QueueSignals, dispute.QueuePriority, dispute.ReplyStatus, dispute.ReplyCycleReserved,
		dispute.SupersededByDisputeID, dispute.StatusDetail,
		dispute.Version, dispute.CreatedAt, dispute.UpdatedAt,
	}
}

func TestCodeReviewDisputeStore_CreateAndEnqueueTriageIntakeGuard(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		byLogin       int
		byPullRequest int
		expectInsert  bool
	}{
		{name: "admits under both ceilings", byLogin: 4, byPullRequest: 19, expectInsert: true},
		{name: "declines at the per-login ceiling", byLogin: 5, byPullRequest: 6},
		{name: "declines at the per-pull-request ceiling", byLogin: 1, byPullRequest: 20},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "test should create the database mock")
			defer mock.Close()

			commentID := int64(4242)
			now := time.Date(2026, time.August, 2, 8, 0, 0, 0, time.UTC)
			dispute := models.CodeReviewDispute{
				OrgID: uuid.New(), SessionID: uuid.New(), PullRequestID: uuid.New(), RepositoryID: uuid.New(), PolicyID: uuid.New(),
				ReviewedHeadSHA: "abc123", Decision: models.CodeReviewDecisionBlocked,
				FiledByLogin: "drive-by", AuthorAssociation: "NONE",
				RepositoryVisibility: models.CodeReviewRepositoryVisibilityPublic,
				Source:               models.CodeReviewDisputeSourceGitHubComment, GitHubCommentID: &commentID,
				SourceBodyHash: "body-hash", SourceVersion: 7, Body: "This should have been approved.",
				SemanticInputHashAtFiling: "semantic-hash", ReplyStatus: models.CodeReviewDisputeReplyPending,
			}
			guard := CodeReviewDisputeIntakeGuard{Window: 24 * time.Hour, PerLoginMax: 5, PerPullRequestMax: 20}

			mock.ExpectBegin()
			// The ceilings must be read inside the insert's transaction, behind
			// the per-pull-request lock, or two concurrent deliveries could each
			// see room under the cap.
			mock.ExpectExec("pg_advisory_xact_lock").
				WithArgs(pgx.NamedArgs{
					"namespace": int32(codeReviewDisputeIntakeLockNamespace), "pull_request_key": advisoryLockKeyForUUID(dispute.PullRequestID),
				}).
				WillReturnResult(pgxmock.NewResult("SELECT", 1))
			mock.ExpectQuery("SELECT[\\s\\S]+FROM code_review_decision_disputes[\\s\\S]+github_comment_id").
				WithArgs(dispute.OrgID, commentID, int64(7)).
				WillReturnRows(pgxmock.NewRows(codeReviewDisputeColumnNames()))
			mock.ExpectQuery("count\\(\\*\\) FILTER").
				WithArgs(pgx.NamedArgs{
					"org_id": dispute.OrgID, "pull_request_id": dispute.PullRequestID,
					"filed_by_login": "drive-by", "window_seconds": int64(86400),
				}).
				WillReturnRows(pgxmock.NewRows([]string{"count", "count"}).AddRow(tt.byLogin, tt.byPullRequest))
			if tt.expectInsert {
				created := dispute
				created.ID = uuid.New()
				created.IntakeStatus = models.CodeReviewDisputeIntakePending
				created.ReassessmentStatus = models.CodeReviewDisputeReassessmentNotRequested
				created.QueueSignals = json.RawMessage(`{}`)
				created.Version = 1
				created.CreatedAt = now
				created.UpdatedAt = now
				mock.ExpectQuery("INSERT INTO code_review_decision_disputes[\\s\\S]+reply_comment_id[\\s\\S]+reply_cycle_reserved").
					WithArgs(
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(),
					).
					WillReturnRows(codeReviewDisputeMockRows(created))
				// Editing one GitHub comment files a fresh dispute, so the epoch
				// must not reopen for it: a reset per edit would hand every edit a
				// new reply budget and the loop guard could never fire.
				mock.ExpectExec("UPDATE pull_requests[\\s\\S]+NOT EXISTS[\\s\\S]+code_review_decision_disputes").
					WithArgs(pgx.NamedArgs{
						"org_id": created.OrgID, "pull_request_id": created.PullRequestID,
						"github_comment_id": created.GitHubCommentID, "dispute_id": created.ID,
					}).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				// The new dispute inherits the thread's reply comment, so the
				// rows it replaced must be retired in the same transaction --
				// otherwise two live disputes share one GitHub comment and a
				// late reply job overwrites the current answer.
				mock.ExpectExec("WITH newest AS[\\s\\S]+superseded_by_dispute_id = NULLIF\\(newest.id, dispute.id\\)").
					WithArgs(pgx.NamedArgs{
						"org_id": created.OrgID, "github_comment_id": created.GitHubCommentID,
					}).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				mock.ExpectQuery("INSERT INTO jobs").
					WithArgs(
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
					).
					WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
				mock.ExpectCommit()
			} else {
				mock.ExpectRollback()
			}

			store := NewCodeReviewDisputeStore(mock)
			store.SetJobStore(NewJobStore(mock))
			created, err := store.CreateAndEnqueueTriage(context.Background(), &dispute, guard)

			if tt.expectInsert {
				require.NoError(t, err, "a filing under both ceilings should be stored")
				require.True(t, created, "an admitted filing should report that it was created")
			} else {
				require.ErrorIs(t, err, ErrCodeReviewDisputeIntakeCapped, "a capped filing should report the admission sentinel")
				require.False(t, created, "a capped filing must not be stored")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestAdvisoryLockKeyForUUID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		value    uuid.UUID
		expected int32
	}{
		{name: "preserves a key below the sign bit", value: uuid.MustParse("00112233-0000-0000-0000-000000000000"), expected: 0x00112233},
		{name: "clears the sign bit", value: uuid.MustParse("ff223344-0000-0000-0000-000000000000"), expected: 0x7f223344},
		{name: "accepts the zero key", value: uuid.Nil, expected: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.expected, advisoryLockKeyForUUID(tt.value), "advisory lock key should preserve the masked big-endian UUID prefix")
		})
	}
}

func TestCodeReviewDisputeStore_IntakeGuardDedupesRedeliveryAtTheCeiling(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create the database mock")
	defer mock.Close()

	commentID := int64(4242)
	now := time.Date(2026, time.August, 2, 8, 0, 0, 0, time.UTC)
	dispute := models.CodeReviewDispute{
		OrgID: uuid.New(), SessionID: uuid.New(), PullRequestID: uuid.New(), RepositoryID: uuid.New(), PolicyID: uuid.New(),
		ReviewedHeadSHA: "abc123", Decision: models.CodeReviewDecisionBlocked,
		FiledByLogin: "drive-by", AuthorAssociation: "NONE",
		RepositoryVisibility: models.CodeReviewRepositoryVisibilityPublic,
		Source:               models.CodeReviewDisputeSourceGitHubComment, GitHubCommentID: &commentID,
		SourceBodyHash: "body-hash", SourceVersion: 7, Body: "This should have been approved.",
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
	guard := CodeReviewDisputeIntakeGuard{Window: 24 * time.Hour, PerLoginMax: 5, PerPullRequestMax: 20}

	mock.ExpectBegin()
	mock.ExpectExec("pg_advisory_xact_lock").
		WithArgs(pgx.NamedArgs{
			"namespace": int32(codeReviewDisputeIntakeLockNamespace), "pull_request_key": advisoryLockKeyForUUID(dispute.PullRequestID),
		}).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	// A stored dispute counts toward its own ceiling, so the comment that took
	// the last slot would be judged capped -- and reported as never captured --
	// on any redelivery. The lookup has to precede the count, not follow it.
	mock.ExpectQuery(`SELECT[\s\S]+FROM code_review_decision_disputes[\s\S]+github_comment_id`).
		WithArgs(dispute.OrgID, commentID, int64(7)).
		WillReturnRows(codeReviewDisputeMockRows(existing))
	mock.ExpectRollback()

	store := NewCodeReviewDisputeStore(mock)
	store.SetJobStore(NewJobStore(mock))
	created, err := store.CreateAndEnqueueTriage(context.Background(), &dispute, guard)

	require.NoError(t, err, "a redelivery of a stored dispute is not an admission decision")
	require.False(t, created, "a redelivery should not report a fresh insert")
	require.Equal(t, existing.ID, dispute.ID, "the caller should receive the dispute already on record, not a capped verdict")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
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
			"policy_owner_active_seconds": (*int)(nil),
			"trust_override_present":      true, "trust_override": &untrusted,
		}).
		WillReturnRows(codeReviewDisputeMockRows(dispute))

	actual, err := NewCodeReviewDisputeStore(mock).Adjudicate(context.Background(), orgID, disputeID, userID, update)

	require.NoError(t, err, "an admin should be able to remove pending queue influence with a negative trust override")
	require.Nil(t, actual.AdjudicationStatus, "an untrusted pending item should leave the adjudication queue")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestCodeReviewDisputeStore_AdjudicatePromotionRespectsAdjudicableRouting(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create the database mock")
	defer mock.Close()
	orgID := uuid.New()
	disputeID := uuid.New()
	userID := uuid.New()
	trusted := true
	now := time.Date(2026, time.August, 2, 12, 0, 0, 0, time.UTC)
	answerOnly := models.CodeReviewDisputeRoutingAnswerOnly
	dispute := models.CodeReviewDispute{
		ID: disputeID, OrgID: orgID, SessionID: uuid.New(), PullRequestID: uuid.New(), RepositoryID: uuid.New(), PolicyID: uuid.New(),
		Routing: &answerOnly, IntakeStatus: models.CodeReviewDisputeIntakeTriaged, TrustOverride: &trusted,
		QueueSignals: json.RawMessage(`{}`), Version: 4, CreatedAt: now, UpdatedAt: now,
	}
	update := models.CodeReviewDisputeAdjudicationUpdate{
		ExpectedVersion: 3, TrustOverride: &trusted, TrustOverridePresent: true,
	}
	// The table's CHECK only allows a non-null adjudication_status on a triaged
	// reassess/policy_signal_only dispute. Promoting anything else must record
	// the override alone rather than write 'pending' and trip the constraint.
	mock.ExpectQuery(`intake_status = 'triaged' AND routing IN \('reassess', 'policy_signal_only'\) AND direction IS NOT NULL[\s\S]+THEN COALESCE\(adjudication_status, 'pending'\)`).
		WithArgs(pgx.NamedArgs{
			"org_id": orgID, "id": disputeID, "user_id": userID, "expected_version": 3,
			"adjudication_status": (*models.CodeReviewDisputeAdjudicationStatus)(nil), "adjudication_note": (*string)(nil),
			"policy_owner_active_seconds": (*int)(nil),
			"trust_override_present":      true, "trust_override": &trusted,
		}).
		WillReturnRows(codeReviewDisputeMockRows(dispute))

	actual, err := NewCodeReviewDisputeStore(mock).Adjudicate(context.Background(), orgID, disputeID, userID, update)

	require.NoError(t, err, "promoting a non-adjudicable dispute should record the override without failing")
	require.Nil(t, actual.AdjudicationStatus, "an answer_only dispute must not be given a queue slot the CHECK forbids")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestCodeReviewDisputeStore_ReserveReplyCycle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                 string
		alreadyReserved      bool
		cycles               int
		expectedReserved     bool
		expectedFirstAttempt bool
		expectSpend          bool
		expectLoopGuard      bool
	}{
		{name: "reuses an existing reservation", alreadyReserved: true, cycles: 2, expectedReserved: true},
		{name: "spends one cycle for the first reply", cycles: 1, expectedReserved: true, expectedFirstAttempt: true, expectSpend: true},
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

			reserved, firstAttempt, err := NewCodeReviewDisputeStore(mock).ReserveReplyCycle(context.Background(), orgID, disputeID, 2)

			require.NoError(t, err, "reply cycle reservation should complete atomically")
			require.Equal(t, tt.expectedReserved, reserved, "reservation result should match the durable loop-guard state")
			require.Equal(t, tt.expectedFirstAttempt, firstAttempt, "only the call that takes the reservation may report a first publication attempt")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestCodeReviewDisputeStore_ListQueueContinuesMaterializedSnapshot(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create the database mock")
	defer mock.Close()

	orgID := uuid.New()
	cursor := models.CodeReviewDisputeQueueCursor{
		SnapshotID: uuid.New(),
		Position:   50,
	}
	mock.ExpectQuery("SELECT EXISTS").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "snapshot_id": cursor.SnapshotID}).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(`FROM code_review_dispute_queue_snapshots AS snapshot[\s\S]+snapshot.position > @position`).
		WithArgs(pgx.NamedArgs{
			"org_id": orgID, "limit": 51,
			"snapshot_id": cursor.SnapshotID, "position": cursor.Position,
		}).
		WillReturnRows(pgxmock.NewRows(append(codeReviewDisputeColumnNames(), "snapshot_position")))

	page, err := NewCodeReviewDisputeStore(mock).ListQueue(context.Background(), orgID, models.CodeReviewDisputeListFilters{
		Cursor: &cursor,
	})

	require.NoError(t, err, "queue pagination should continue the captured materialized ordering")
	require.Empty(t, page.Items, "an empty database page should remain empty")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestCodeReviewDisputeStore_ListQueueRejectsExpiredSnapshot(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create the database mock")
	defer mock.Close()

	orgID := uuid.New()
	cursor := models.CodeReviewDisputeQueueCursor{SnapshotID: uuid.New(), Position: 50}
	mock.ExpectQuery("SELECT EXISTS").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "snapshot_id": cursor.SnapshotID}).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))

	_, err = NewCodeReviewDisputeStore(mock).ListQueue(context.Background(), orgID, models.CodeReviewDisputeListFilters{Cursor: &cursor})

	require.ErrorIs(t, err, ErrCodeReviewDisputeQueueCursorExpired, "an expired snapshot should not masquerade as the end of the queue")
	require.NoError(t, mock.ExpectationsWereMet(), "snapshot validation should stop before querying an expired page")
}

func TestCodeReviewDisputeStore_ListQueueMaterializesStableOrdering(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create the database mock")
	defer mock.Close()

	orgID := uuid.New()
	now := time.Date(2026, 8, 4, 7, 0, 0, 0, time.UTC)
	first := models.CodeReviewDispute{ID: uuid.New(), OrgID: orgID, QueuePriority: 100, CreatedAt: now, UpdatedAt: now}
	second := models.CodeReviewDispute{ID: uuid.New(), OrgID: orgID, QueuePriority: 50, CreatedAt: now.Add(-time.Minute), UpdatedAt: now}
	rows := pgxmock.NewRows(append(codeReviewDisputeColumnNames(), "snapshot_position"))
	rows.AddRow(append(codeReviewDisputeMockValues(first), int64(1))...)
	rows.AddRow(append(codeReviewDisputeMockValues(second), int64(2))...)

	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM code_review_dispute_queue_snapshots").
		WithArgs(pgx.NamedArgs{"org_id": orgID}).
		WillReturnResult(pgxmock.NewResult("DELETE", 0))
	mock.ExpectExec("INSERT INTO code_review_dispute_queue_snapshots[\\s\\S]+row_number\\(\\) OVER \\(ORDER BY dispute.queue_priority DESC").
		WithArgs(pgx.NamedArgs{
			"org_id": orgID, "snapshot_id": pgxmock.AnyArg(), "expires_at": pgxmock.AnyArg(),
		}).
		WillReturnResult(pgxmock.NewResult("INSERT", 2))
	mock.ExpectQuery("FROM code_review_dispute_queue_snapshots AS snapshot").
		WithArgs(pgx.NamedArgs{
			"org_id": orgID, "snapshot_id": pgxmock.AnyArg(), "position": int64(0), "limit": 2,
		}).
		WillReturnRows(rows)
	mock.ExpectCommit()

	page, err := NewCodeReviewDisputeStore(mock).ListQueue(context.Background(), orgID, models.CodeReviewDisputeListFilters{Limit: 1})

	require.NoError(t, err, "the first page should materialize and read one stable queue ordering")
	require.Equal(t, []models.CodeReviewDispute{first}, page.Items, "the first page should return the first materialized row")
	require.NotNil(t, page.NextQueueCursor, "a truncated materialized queue should return a continuation cursor")
	require.Equal(t, int64(1), page.NextQueueCursor.Position, "the cursor should advance to the returned snapshot position")
	require.NotEqual(t, uuid.Nil, page.NextQueueCursor.SnapshotID, "the cursor should identify the materialized queue snapshot")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestCodeReviewDisputeStore_DeleteExpiredQueueSnapshots(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "mock pool should initialize")
	defer mock.Close()
	orgID := uuid.New()
	mock.ExpectExec("DELETE FROM code_review_dispute_queue_snapshots[\\s\\S]+org_id = @org_id[\\s\\S]+expires_at <= now\\(\\)").
		WithArgs(orgID).
		WillReturnResult(pgxmock.NewResult("DELETE", 4))

	deleted, err := NewCodeReviewDisputeStore(mock).DeleteExpiredQueueSnapshots(context.Background(), orgID)

	require.NoError(t, err, "expired queue snapshot cleanup should succeed")
	require.Equal(t, int64(4), deleted, "cleanup should report the deleted snapshot rows")
	require.NoError(t, mock.ExpectationsWereMet(), "all snapshot cleanup expectations should be met")
}

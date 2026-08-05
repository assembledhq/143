package db

import (
	"context"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	pgxmock "github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestCodeReviewInsightStore_RankingEnabled(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		enabled  bool
		expected bool
	}{
		{name: "enables ranking after sustained volume", enabled: true, expected: true},
		{name: "keeps a low volume queue flat", enabled: false, expected: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool, err := pgxmock.NewPool()
			require.NoError(t, err, "mock pool should initialize")
			defer pool.Close()
			orgID := uuid.New()
			pool.ExpectQuery("WITH month_counts[\\s\\S]+adjudication_status <> 'expired'[\\s\\S]+superseded_by_dispute_id IS NULL").WithArgs(orgID).
				WillReturnRows(pgxmock.NewRows([]string{"enabled"}).AddRow(tt.enabled))
			actual, err := NewCodeReviewInsightStore(pool).RankingEnabled(context.Background(), orgID)
			require.NoError(t, err, "ranking volume query should succeed")
			require.Equal(t, tt.expected, actual, "ranking should follow the sustained-volume result")
			require.NoError(t, pool.ExpectationsWereMet(), "all ranking database expectations should be met")
		})
	}
}

func TestCodeReviewInsightStore_ProjectDecisionConsumesEarlierHumanObservations(t *testing.T) {
	t.Parallel()

	pool, err := pgxmock.NewPool()
	require.NoError(t, err, "mock pool should initialize")
	defer pool.Close()
	orgID := uuid.New()
	sessionID := uuid.New()
	pool.ExpectExec("INSERT INTO code_review_decision_outcomes[\\s\\S]+lifecycle_observed_at[\\s\\S]+code_review_human_review_observations[\\s\\S]+state = 'changes_requested'[\\s\\S]+code_review_pull_request_lifecycle_observations[\\s\\S]+ON CONFLICT[\\s\\S]+EXCLUDED.lifecycle_observed_at").
		WithArgs(orgID, []uuid.UUID{sessionID}).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	err = NewCodeReviewInsightStore(pool).ProjectDecision(context.Background(), orgID, sessionID)

	require.NoError(t, err, "decision projection should consume durable reviews that arrived before the decision")
	require.NoError(t, pool.ExpectationsWereMet(), "decision projection should query both independent human outcomes in the tenant")
}

func TestCodeReviewInsightStore_RefreshHumanReviewCommentCount(t *testing.T) {
	t.Parallel()

	pool, err := pgxmock.NewPool()
	require.NoError(t, err, "mock pool should initialize")
	defer pool.Close()
	orgID := uuid.New()
	pullRequestID := uuid.New()
	observedAt := time.Date(2026, 8, 4, 8, 0, 0, 0, time.UTC)
	pool.ExpectQuery("SELECT session_id FROM code_review_session_metadata").
		WithArgs(orgID, pullRequestID).
		WillReturnRows(pgxmock.NewRows([]string{"session_id"}))
	pool.ExpectExec("UPDATE code_review_decision_outcomes outcome SET[\\s\\S]+lower\\(comment.reviewer_type\\) = 'user'").
		WithArgs(orgID, pullRequestID, observedAt).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = NewCodeReviewInsightStore(pool).RefreshHumanReviewCommentCount(context.Background(), orgID, pullRequestID, observedAt)

	require.NoError(t, err, "human review comment refresh should update the projected exact count")
	require.NoError(t, pool.ExpectationsWereMet(), "all human review comment projection expectations should be met")
}

func TestCodeReviewInsightStore_PullRequestLifecycleUsesIndependentWatermark(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		rowsAffected     int64
		expectedAccepted bool
		merged           bool
		terminal         bool
		invoke           func(*CodeReviewInsightStore, uuid.UUID, uuid.UUID, time.Time) (bool, error)
	}{
		{
			name: "terminal event uses provider watermark", rowsAffected: 1, expectedAccepted: true, merged: true, terminal: true,
			invoke: func(store *CodeReviewInsightStore, orgID, pullRequestID uuid.UUID, observedAt time.Time) (bool, error) {
				return store.RecordPullRequestTerminal(context.Background(), orgID, pullRequestID, true, nil, observedAt)
			},
		},
		{
			name: "reopen event uses provider watermark", rowsAffected: 1, expectedAccepted: true,
			invoke: func(store *CodeReviewInsightStore, orgID, pullRequestID uuid.UUID, observedAt time.Time) (bool, error) {
				return store.RecordPullRequestOpen(context.Background(), orgID, pullRequestID, observedAt)
			},
		},
		{
			name: "older event leaves the primary mirror unchanged", rowsAffected: 0, expectedAccepted: false,
			invoke: func(store *CodeReviewInsightStore, orgID, pullRequestID uuid.UUID, observedAt time.Time) (bool, error) {
				return store.RecordPullRequestOpen(context.Background(), orgID, pullRequestID, observedAt)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool, err := pgxmock.NewPool()
			require.NoError(t, err, "mock pool should initialize")
			defer pool.Close()
			orgID := uuid.New()
			pullRequestID := uuid.New()
			observedAt := time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC)
			pool.ExpectExec("WITH lifecycle AS[\\s\\S]+INSERT INTO code_review_pull_request_lifecycle_observations[\\s\\S]+observed_at <= EXCLUDED.observed_at[\\s\\S]+UPDATE pull_requests").
				WithArgs(pgx.NamedArgs{
					"org_id": orgID, "pull_request_id": pullRequestID, "merged": tt.merged,
					"merged_at": (*time.Time)(nil), "terminal": tt.terminal, "observed_at": observedAt,
					"merged_status": models.PullRequestStatusMerged, "closed_status": models.PullRequestStatusClosed,
					"open_status": models.PullRequestStatusOpen,
				}).
				WillReturnResult(pgxmock.NewResult("UPDATE", tt.rowsAffected))
			if tt.expectedAccepted {
				pool.ExpectQuery("SELECT session_id FROM code_review_session_metadata").
					WithArgs(orgID, pullRequestID).
					WillReturnRows(pgxmock.NewRows([]string{"session_id"}))
			}

			accepted, err := tt.invoke(NewCodeReviewInsightStore(pool), orgID, pullRequestID, observedAt)

			require.NoError(t, err, "ordered lifecycle projection should process the provider event")
			require.Equal(t, tt.expectedAccepted, accepted, "the lifecycle transition should report whether the provider event won ordering")
			require.NoError(t, pool.ExpectationsWereMet(), "lifecycle projection should persist before any decision outcome exists")
		})
	}
}

func TestCodeReviewInsightStore_ProjectRecentDecisionsBatchesProjection(t *testing.T) {
	t.Parallel()

	pool, err := pgxmock.NewPool()
	require.NoError(t, err, "mock pool should initialize")
	defer pool.Close()
	orgID := uuid.New()
	sessionIDs := []uuid.UUID{uuid.New(), uuid.New()}
	staleBefore := time.Date(2026, 8, 4, 7, 0, 0, 0, time.UTC)
	pool.ExpectQuery("SELECT m.session_id[\\s\\S]+code_review_pull_request_lifecycle_observations[\\s\\S]+o.lifecycle_observed_at[\\s\\S]+LIMIT @limit").
		WithArgs(orgID, staleBefore, 100).
		WillReturnRows(pgxmock.NewRows([]string{"session_id"}).AddRow(sessionIDs[0]).AddRow(sessionIDs[1]))
	pool.ExpectExec("INSERT INTO code_review_decision_outcomes[\\s\\S]+m.session_id = ANY\\(@session_ids\\)").
		WithArgs(orgID, sessionIDs).
		WillReturnResult(pgxmock.NewResult("INSERT", 2))

	projected, err := NewCodeReviewInsightStore(pool).ProjectRecentDecisions(context.Background(), orgID, staleBefore, 100)

	require.NoError(t, err, "recent decision projection should succeed")
	require.Equal(t, int64(2), projected, "recent decision projection should report the selected batch size")
	require.NoError(t, pool.ExpectationsWereMet(), "one bulk write should project the complete selected batch")
}

func TestCodeReviewInsightStore_ListPendingRankCandidatesUsesOneBoundedQuery(t *testing.T) {
	t.Parallel()

	pool, err := pgxmock.NewPool()
	require.NoError(t, err, "mock pool should initialize")
	defer pool.Close()
	orgID := uuid.New()
	disputeID := uuid.New()
	now := time.Date(2026, 8, 4, 7, 0, 0, 0, time.UTC)
	flipped := false
	maintainer := "maintainer"
	columns := []string{"id", "decision", "created_at", "reassessment_status", "reassessment_flipped", "author_is_pr_author", "escalated_at", "outcome_observed_until", "independent_approver_login", "independent_blocking_review_login", "repeat_reason_count", "base_policy_active"}
	pool.ExpectQuery("SELECT d.id[\\s\\S]+CROSS JOIN LATERAL[\\s\\S]+now\\(\\) - interval '14 days'[\\s\\S]+repeat_reason_disputes_14_days[\\s\\S]+ranking_enabled").
		WithArgs(orgID, true, 25).
		WillReturnRows(pgxmock.NewRows(columns).AddRow(disputeID, models.CodeReviewDecisionBlocked, now, models.CodeReviewDisputeReassessmentCompleted, &flipped, false, nil, &now, &maintainer, nil, 3, true))

	candidates, err := NewCodeReviewInsightStore(pool).ListPendingRankCandidates(context.Background(), orgID, 25, true)

	require.NoError(t, err, "rank candidates should load in one aggregate query")
	require.Len(t, candidates, 1, "rank candidates should contain the aggregate row")
	require.Equal(t, disputeID, candidates[0].Dispute.ID, "rank candidate should preserve the dispute identity")
	require.Equal(t, 3, candidates[0].RepeatReasonCount, "rank candidate should preserve the trailing-window repeat count")
	require.NoError(t, pool.ExpectationsWereMet(), "one database query should satisfy the complete ranking candidate")
}

func TestLatestCodeReviewObservationsByReviewerHonorsTerminalReviewState(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 8, 4, 7, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		reviews  []models.CodeReviewHumanReviewObservation
		expected []models.CodeReviewHumanReviewObservation
	}{
		{
			name: "dismissal retires an older blocking review",
			reviews: []models.CodeReviewHumanReviewObservation{
				{GitHubReviewID: 10, ReviewerLogin: "maintainer", State: "changes_requested", SubmittedAt: base},
				{GitHubReviewID: 11, ReviewerLogin: "maintainer", State: "dismissed", SubmittedAt: base.Add(time.Minute)},
			},
			expected: []models.CodeReviewHumanReviewObservation{
				{GitHubReviewID: 11, ReviewerLogin: "maintainer", State: "dismissed", SubmittedAt: base.Add(time.Minute)},
			},
		},
		{
			name: "latest review is chosen case insensitively",
			reviews: []models.CodeReviewHumanReviewObservation{
				{GitHubReviewID: 20, ReviewerLogin: "Reviewer", State: "approved", SubmittedAt: base},
				{GitHubReviewID: 21, ReviewerLogin: "reviewer", State: "CHANGES_REQUESTED", SubmittedAt: base.Add(time.Minute)},
			},
			expected: []models.CodeReviewHumanReviewObservation{
				{GitHubReviewID: 21, ReviewerLogin: "reviewer", State: "changes_requested", SubmittedAt: base.Add(time.Minute)},
			},
		},
		{
			name: "comment-only review does not replace an opinionated review",
			reviews: []models.CodeReviewHumanReviewObservation{
				{GitHubReviewID: 30, ReviewerLogin: "maintainer", State: "approved", SubmittedAt: base},
				{GitHubReviewID: 31, ReviewerLogin: "maintainer", State: "commented", SubmittedAt: base.Add(time.Minute)},
			},
			expected: []models.CodeReviewHumanReviewObservation{
				{GitHubReviewID: 30, ReviewerLogin: "maintainer", State: "approved", SubmittedAt: base},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual := latestCodeReviewObservationsByReviewer(tt.reviews)

			require.Equal(t, tt.expected, actual, "provider reconciliation should retain only each reviewer's true latest state")
		})
	}
}

func TestCodeReviewInsightStore_RecordHumanReviewIsOrderSafe(t *testing.T) {
	t.Parallel()

	pool, err := pgxmock.NewPool()
	require.NoError(t, err, "mock pool should initialize")
	defer pool.Close()
	orgID := uuid.New()
	pullRequestID := uuid.New()
	observedAt := time.Date(2026, 8, 4, 8, 0, 0, 0, time.UTC)
	pool.ExpectQuery("SELECT session_id FROM code_review_session_metadata").
		WithArgs(orgID, pullRequestID).
		WillReturnRows(pgxmock.NewRows([]string{"session_id"}))
	pool.ExpectBegin()
	pool.ExpectQuery("SELECT observed_until[\\s\\S]+FOR UPDATE").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": pullRequestID}).
		WillReturnRows(pgxmock.NewRows([]string{"observed_until"}))
	pool.ExpectExec("INSERT INTO code_review_human_review_observations[\\s\\S]+EXCLUDED.observed_at >= code_review_human_review_observations.observed_at").
		WithArgs(orgID, pullRequestID, int64(101), "maintainer", "User", "MEMBER", "approved", true, observedAt).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectExec("active = observation.active AND observation.github_review_id").
		WithArgs(orgID, pullRequestID, "maintainer").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	pool.ExpectExec("UPDATE code_review_decision_outcomes outcome SET").
		WithArgs(orgID, pullRequestID, observedAt).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	pool.ExpectCommit()

	err = NewCodeReviewInsightStore(pool).RecordHumanReview(
		context.Background(), orgID, pullRequestID, 101,
		"maintainer", "User", "MEMBER", "author", "approved", observedAt,
	)

	require.NoError(t, err, "human review projection should preserve newer events and select one active review per reviewer")
	require.NoError(t, pool.ExpectationsWereMet(), "human review projection should perform every update atomically and tenant-scope each query")
}

func TestCodeReviewInsightStore_DismissHumanReviewUpsertsTombstone(t *testing.T) {
	t.Parallel()

	pool, err := pgxmock.NewPool()
	require.NoError(t, err, "mock pool should initialize")
	defer pool.Close()
	orgID := uuid.New()
	pullRequestID := uuid.New()
	observedAt := time.Date(2026, 8, 4, 8, 0, 0, 0, time.UTC)
	pool.ExpectQuery("SELECT session_id FROM code_review_session_metadata").
		WithArgs(orgID, pullRequestID).
		WillReturnRows(pgxmock.NewRows([]string{"session_id"}))
	pool.ExpectBegin()
	pool.ExpectQuery("SELECT observed_until[\\s\\S]+FOR UPDATE").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": pullRequestID}).
		WillReturnRows(pgxmock.NewRows([]string{"observed_until"}))
	pool.ExpectExec("INSERT INTO code_review_human_review_observations[\\s\\S]+'dismissed'[\\s\\S]+ON CONFLICT[\\s\\S]+THEN false").
		WithArgs(orgID, pullRequestID, int64(101), "maintainer", "User", "MEMBER", true, observedAt).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectExec("UPDATE code_review_decision_outcomes outcome SET").
		WithArgs(orgID, pullRequestID, observedAt).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	pool.ExpectCommit()

	err = NewCodeReviewInsightStore(pool).DismissHumanReview(
		context.Background(), orgID, pullRequestID, 101,
		"maintainer", "User", "MEMBER", "author", observedAt,
	)

	require.NoError(t, err, "a dismissal should persist even when its submitted event has not arrived")
	require.NoError(t, pool.ExpectationsWereMet(), "dismissal should atomically upsert an inactive provider observation")
}

func TestCodeReviewInsightStore_ReconcilePullRequestOutcomeBulkUpsertsReviews(t *testing.T) {
	t.Parallel()

	pool, err := pgxmock.NewPool()
	require.NoError(t, err, "mock pool should initialize")
	defer pool.Close()
	orgID := uuid.New()
	pullRequestID := uuid.New()
	observedAt := time.Date(2026, 8, 4, 9, 0, 0, 0, time.UTC)
	pool.ExpectExec("WITH lifecycle AS[\\s\\S]+INSERT INTO code_review_pull_request_lifecycle_observations[\\s\\S]+UPDATE pull_requests").
		WithArgs(pgx.NamedArgs{
			"org_id": orgID, "pull_request_id": pullRequestID, "merged": false,
			"merged_at": (*time.Time)(nil), "terminal": false, "observed_at": observedAt,
			"merged_status": models.PullRequestStatusMerged, "closed_status": models.PullRequestStatusClosed,
			"open_status": models.PullRequestStatusOpen,
		}).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectQuery("SELECT session_id FROM code_review_session_metadata").
		WithArgs(orgID, pullRequestID).
		WillReturnRows(pgxmock.NewRows([]string{"session_id"}))
	pool.ExpectBegin()
	pool.ExpectQuery("SELECT observed_until[\\s\\S]+FOR UPDATE").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": pullRequestID}).
		WillReturnRows(pgxmock.NewRows([]string{"observed_until"}).AddRow(observedAt.Add(-time.Hour)))
	pool.ExpectExec("UPDATE code_review_human_review_observations SET active = false").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": pullRequestID, "observed_at": observedAt}).
		WillReturnResult(pgxmock.NewResult("UPDATE", 2))
	pool.ExpectExec("WITH observations AS[\\s\\S]+jsonb_to_recordset[\\s\\S]+INSERT INTO code_review_human_review_observations").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": pullRequestID, "observations": pgxmock.AnyArg()}).
		WillReturnResult(pgxmock.NewResult("INSERT", 2))
	pool.ExpectExec("UPDATE code_review_decision_outcomes SET[\\s\\S]+lifecycle_observed_at = @observed_at[\\s\\S]+provider_reconcile_attempted_at[\\s\\S]+lifecycle_observed_at IS NULL OR lifecycle_observed_at <= @observed_at").
		WithArgs(pgx.NamedArgs{
			"org_id": orgID, "pull_request_id": pullRequestID, "merged": false,
			"merged_at": (*time.Time)(nil), "terminal": false, "observed_at": observedAt,
		}).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	pool.ExpectExec("UPDATE code_review_decision_outcomes outcome SET").
		WithArgs(orgID, pullRequestID, observedAt).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	pool.ExpectCommit()

	err = NewCodeReviewInsightStore(pool).ReconcilePullRequestOutcome(context.Background(), orgID, pullRequestID, models.CodeReviewOutcomeSnapshot{
		AuthorLogin: "author", State: "open", ObservedAt: observedAt,
		Reviews: []models.CodeReviewHumanReviewObservation{
			{GitHubReviewID: 101, ReviewerLogin: "one", ReviewerType: "User", AuthorAssociation: "MEMBER", State: "approved", SubmittedAt: observedAt.Add(-time.Minute)},
			{GitHubReviewID: 102, ReviewerLogin: "two", ReviewerType: "User", AuthorAssociation: "MEMBER", State: "changes_requested", SubmittedAt: observedAt},
		},
	})

	require.NoError(t, err, "provider reconciliation should persist the complete review set")
	require.NoError(t, pool.ExpectationsWereMet(), "provider reconciliation should use one bulk review upsert")
}

func TestCodeReviewInsightStore_ReconcilePullRequestOutcomeYieldsToNewerWebhook(t *testing.T) {
	t.Parallel()

	pool, err := pgxmock.NewPool()
	require.NoError(t, err, "mock pool should initialize")
	defer pool.Close()
	orgID := uuid.New()
	pullRequestID := uuid.New()
	snapshotAt := time.Date(2026, 8, 5, 9, 0, 0, 0, time.UTC)
	pool.ExpectExec("WITH lifecycle AS[\\s\\S]+INSERT INTO code_review_pull_request_lifecycle_observations[\\s\\S]+UPDATE pull_requests").
		WithArgs(pgx.NamedArgs{
			"org_id": orgID, "pull_request_id": pullRequestID, "merged": false,
			"merged_at": (*time.Time)(nil), "terminal": false, "observed_at": snapshotAt,
			"merged_status": models.PullRequestStatusMerged, "closed_status": models.PullRequestStatusClosed,
			"open_status": models.PullRequestStatusOpen,
		}).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectQuery("SELECT session_id FROM code_review_session_metadata").
		WithArgs(orgID, pullRequestID).
		WillReturnRows(pgxmock.NewRows([]string{"session_id"}))
	pool.ExpectBegin()
	pool.ExpectQuery("SELECT observed_until[\\s\\S]+FOR UPDATE").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": pullRequestID}).
		WillReturnRows(pgxmock.NewRows([]string{"observed_until"}).AddRow(snapshotAt.Add(time.Minute)))
	pool.ExpectRollback()

	err = NewCodeReviewInsightStore(pool).ReconcilePullRequestOutcome(
		context.Background(), orgID, pullRequestID,
		models.CodeReviewOutcomeSnapshot{ObservedAt: snapshotAt},
	)

	require.NoError(t, err, "stale provider reconciliation should yield to a newer webhook projection")
	require.NoError(t, pool.ExpectationsWereMet(), "stale reconciliation should stop after locking the newer watermark")
}

func TestCodeReviewInsightStore_UpdateDisputeRanksMergesContextInOneStatement(t *testing.T) {
	t.Parallel()
	pool, err := pgxmock.NewPool()
	require.NoError(t, err, "mock pool should initialize")
	defer pool.Close()
	orgID := uuid.New()
	disputeID := uuid.New()
	signals := models.CodeReviewQueueSignals{RankingEnabled: true, ComputedAt: time.Now().UTC()}
	pool.ExpectExec("WITH rank_updates AS").
		WithArgs(pgxmock.AnyArg(), orgID).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	err = NewCodeReviewInsightStore(pool).UpdateDisputeRanks(context.Background(), orgID, []models.CodeReviewRankUpdate{{ID: disputeID, Signals: signals, Priority: 55}})
	require.NoError(t, err, "rank update should preserve intake context while adding computed signals")
	require.NoError(t, pool.ExpectationsWereMet(), "all rank update database expectations should be met")
}

func TestCodeReviewInsightStore_GetInsightsScansAggregateShape(t *testing.T) {
	t.Parallel()
	pool, err := pgxmock.NewPool()
	require.NoError(t, err, "mock pool should initialize")
	defer pool.Close()
	orgID := uuid.New()
	from := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	medianDecision := 90.0
	medianAdjudication := 180.0
	ownerMinutes := 4.5
	columns := []string{
		"decisions", "disputes", "objection_rate", "upheld", "reassessments", "flips", "cost", "owner_minutes",
		"median_decision", "median_adjudication", "fresh_through", "projection_updated",
		"directions", "kinds", "policy_mix", "reasons", "actual_vs_limit", "flip_buckets",
	}
	pool.ExpectQuery("WITH first_requests AS MATERIALIZED[\\s\\S]+min\\(m.created_at\\)[\\s\\S]+first_request.first_requested_at >= @from[\\s\\S]+JOIN outcomes cohort[\\s\\S]+d.superseded_by_dispute_id IS NULL[\\s\\S]+count\\(DISTINCT session_id\\)[\\s\\S]+policy_version").WithArgs(orgID, from).
		WillReturnRows(pgxmock.NewRows(columns).AddRow(
			int64(12), int64(4), 0.25, int64(2), int64(3), int64(1), 1.25, &ownerMinutes,
			&medianDecision, &medianAdjudication, nil, nil,
			[]byte(`[]`), []byte(`[]`), []byte(`[]`), []byte(`[]`), []byte(`[]`), []byte(`[]`),
		))
	pool.ExpectQuery("WITH month_counts[\\s\\S]+adjudication_status <> 'expired'[\\s\\S]+superseded_by_dispute_id IS NULL").WithArgs(orgID).
		WillReturnRows(pgxmock.NewRows([]string{"enabled"}).AddRow(true))

	actual, err := NewCodeReviewInsightStore(pool).GetInsights(context.Background(), orgID, models.CodeReviewInsightFilters{From: &from})
	require.NoError(t, err, "insights aggregate should scan every selected column without drift")
	require.Equal(t, int64(3), actual.Reassessments, "insights should preserve the reassessment count")
	require.Equal(t, int64(1), actual.ReassessmentFlips, "insights should preserve the flip count")
	require.Equal(t, 1.25, actual.ReassessmentCostUSD, "insights should preserve reassessment spend")
	require.Equal(t, 0.25, actual.ObjectionRate, "insights should preserve the decision-level objection rate")
	require.Equal(t, &ownerMinutes, actual.PolicyOwnerMinutesPerResolution, "insights should preserve measured owner time per resolution")
	require.True(t, actual.RankingEnabled, "insights should include the sustained-volume ranking state")
	require.NoError(t, pool.ExpectationsWereMet(), "all insights database expectations should be met")
}

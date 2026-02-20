package db

import (
	"context"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

var rcColumns = []string{
	"id", "pull_request_id", "org_id", "github_comment_id", "reviewer", "body",
	"diff_path", "diff_position", "filter_status", "category", "actionable",
	"generalizable", "generalized_rule", "summary", "applied", "created_at",
}

func rcStrPtr(s string) *string { return &s }
func rcIntPtr(i int) *int       { return &i }

func newReviewCommentRow(id, prID, orgID uuid.UUID, now time.Time) []any {
	return []any{
		id, prID, orgID, int64(1001), "reviewer-alice", "Please fix the nil check",
		rcStrPtr("main.go"), rcIntPtr(42), "pending", (*string)(nil), false,
		false, (*string)(nil), (*string)(nil), false, now,
	}
}

func TestReviewCommentStore_Create_Success(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewReviewCommentStore(mock)
	now := time.Now()
	generatedID := uuid.New()

	c := &models.ReviewComment{
		PullRequestID:   uuid.New(),
		OrgID:           uuid.New(),
		GitHubCommentID: 1001,
		Reviewer:        "reviewer-alice",
		Body:            "Please fix the nil check",
		DiffPath:        rcStrPtr("main.go"),
		DiffPosition:    rcIntPtr(42),
		FilterStatus:    "pending",
	}

	mock.ExpectQuery("INSERT INTO review_comments").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "created_at"}).
				AddRow(generatedID, now),
		)

	err = store.Create(context.Background(), c)
	require.NoError(t, err, "should create review comment without error")
	require.Equal(t, generatedID, c.ID, "should set the generated ID on the review comment")
	require.Equal(t, now, c.CreatedAt, "should set the created_at timestamp on the review comment")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestReviewCommentStore_Create_DuplicateGitHubCommentID(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewReviewCommentStore(mock)
	existingID := uuid.New()
	existingTime := time.Now().Add(-1 * time.Hour)

	c := &models.ReviewComment{
		PullRequestID:   uuid.New(),
		OrgID:           uuid.New(),
		GitHubCommentID: 1001,
		Reviewer:        "reviewer-alice",
		Body:            "Please fix the nil check",
		FilterStatus:    "pending",
	}

	// ON CONFLICT DO UPDATE SET id = review_comments.id returns the existing row on conflict.
	mock.ExpectQuery("INSERT INTO review_comments").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "created_at"}).
				AddRow(existingID, existingTime),
		)

	err = store.Create(context.Background(), c)
	require.NoError(t, err, "should return existing row on duplicate without error")
	require.Equal(t, existingID, c.ID, "should populate the model with the existing row's ID")
	require.Equal(t, existingTime, c.CreatedAt, "should populate the model with the existing row's created_at")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestReviewCommentStore_GetByID_Success(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewReviewCommentStore(mock)
	orgID := uuid.New()
	id := uuid.New()
	prID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM review_comments WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(rcColumns).
				AddRow(newReviewCommentRow(id, prID, orgID, now)...),
		)

	rc, err := store.GetByID(context.Background(), orgID, id)
	require.NoError(t, err, "should retrieve review comment by ID without error")
	require.Equal(t, id, rc.ID, "should return the correct review comment ID")
	require.Equal(t, prID, rc.PullRequestID, "should return the correct pull request ID")
	require.Equal(t, orgID, rc.OrgID, "should return the correct org ID")
	require.Equal(t, int64(1001), rc.GitHubCommentID, "should return the correct GitHub comment ID")
	require.Equal(t, "reviewer-alice", rc.Reviewer, "should return the correct reviewer")
	require.Equal(t, "Please fix the nil check", rc.Body, "should return the correct body")
	require.Equal(t, rcStrPtr("main.go"), rc.DiffPath, "should return the correct diff path")
	require.Equal(t, rcIntPtr(42), rc.DiffPosition, "should return the correct diff position")
	require.Equal(t, "pending", rc.FilterStatus, "should return the correct filter status")
	require.False(t, rc.Actionable, "should return actionable as false")
	require.False(t, rc.Applied, "should return applied as false")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestReviewCommentStore_GetByID_NotFound(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewReviewCommentStore(mock)

	mock.ExpectQuery("SELECT .+ FROM review_comments WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(rcColumns))

	_, err = store.GetByID(context.Background(), uuid.New(), uuid.New())
	require.Error(t, err, "should return an error when review comment is not found")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestReviewCommentStore_ListByOrg(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	prID := uuid.New()
	id1 := uuid.New()
	id2 := uuid.New()
	now := time.Now()

	tests := []struct {
		name      string
		filters   ReviewCommentFilters
		setupMock func(mock pgxmock.PgxPoolIface)
		expected  int
		expectErr bool
	}{
		{
			name:    "returns all comments for org with no filters",
			filters: ReviewCommentFilters{},
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM review_comments WHERE org_id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(rcColumns).
							AddRow(newReviewCommentRow(id1, prID, orgID, now)...).
							AddRow(newReviewCommentRow(id2, prID, orgID, now)...),
					)
			},
			expected: 2,
		},
		{
			name:    "returns comments filtered by pull_request_id",
			filters: ReviewCommentFilters{PullRequestID: &prID},
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM review_comments WHERE org_id .+ AND pull_request_id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(rcColumns).
							AddRow(newReviewCommentRow(id1, prID, orgID, now)...),
					)
			},
			expected: 1,
		},
		{
			name:    "returns comments filtered by filter_status",
			filters: ReviewCommentFilters{FilterStatus: "accepted"},
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM review_comments WHERE org_id .+ AND filter_status").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(rcColumns).
							AddRow(newReviewCommentRow(id1, prID, orgID, now)...),
					)
			},
			expected: 1,
		},
		{
			name:    "returns empty list for org with no comments",
			filters: ReviewCommentFilters{},
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM review_comments WHERE org_id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(rcColumns))
			},
			expected: 0,
		},
		{
			name:    "returns comments with cursor-based pagination",
			filters: ReviewCommentFilters{Cursor: uuid.New().String()},
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM review_comments WHERE org_id .+ AND id <").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(rcColumns).
							AddRow(newReviewCommentRow(id1, prID, orgID, now)...),
					)
			},
			expected: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool without error")
			defer mock.Close()

			store := NewReviewCommentStore(mock)
			tt.setupMock(mock)

			comments, err := store.ListByOrg(context.Background(), orgID, tt.filters)
			if tt.expectErr {
				require.Error(t, err, "ListByOrg should return an error")
				return
			}
			require.NoError(t, err, "ListByOrg should not return an error")
			require.Len(t, comments, tt.expected, "should return expected number of review comments")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestReviewCommentStore_ListByPullRequest_Success(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewReviewCommentStore(mock)
	orgID := uuid.New()
	prID := uuid.New()
	id1 := uuid.New()
	id2 := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM review_comments WHERE org_id .+ AND pull_request_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(rcColumns).
				AddRow(newReviewCommentRow(id1, prID, orgID, now)...).
				AddRow(newReviewCommentRow(id2, prID, orgID, now)...),
		)

	comments, err := store.ListByPullRequest(context.Background(), orgID, prID)
	require.NoError(t, err, "should list review comments by pull request without error")
	require.Len(t, comments, 2, "should return both review comments for the pull request")
	require.Equal(t, id1, comments[0].ID, "first comment should have the correct ID")
	require.Equal(t, id2, comments[1].ID, "second comment should have the correct ID")
	require.Equal(t, prID, comments[0].PullRequestID, "first comment should have the correct pull request ID")
	require.Equal(t, orgID, comments[0].OrgID, "first comment should have the correct org ID")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestReviewCommentStore_ListByPullRequest_Empty(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewReviewCommentStore(mock)

	mock.ExpectQuery("SELECT .+ FROM review_comments WHERE org_id .+ AND pull_request_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(rcColumns))

	comments, err := store.ListByPullRequest(context.Background(), uuid.New(), uuid.New())
	require.NoError(t, err, "should return no error for empty result set")
	require.Empty(t, comments, "should return empty list when no comments exist for pull request")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestReviewCommentStore_ListActionableByPullRequest_Success(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewReviewCommentStore(mock)
	orgID := uuid.New()
	prID := uuid.New()
	id := uuid.New()
	now := time.Now()

	// Construct a row where actionable=true and filter_status='accepted'
	actionableRow := []any{
		id, prID, orgID, int64(1001), "reviewer-alice", "Please fix the nil check",
		rcStrPtr("main.go"), rcIntPtr(42), "accepted", rcStrPtr("style"), true,
		false, (*string)(nil), (*string)(nil), false, now,
	}

	mock.ExpectQuery("SELECT .+ FROM review_comments WHERE org_id .+ AND pull_request_id .+ AND filter_status .+ AND actionable").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(rcColumns).
				AddRow(actionableRow...),
		)

	comments, err := store.ListActionableByPullRequest(context.Background(), orgID, prID)
	require.NoError(t, err, "should list actionable review comments without error")
	require.Len(t, comments, 1, "should return only actionable accepted comments")
	require.Equal(t, id, comments[0].ID, "should return the correct comment ID")
	require.True(t, comments[0].Actionable, "returned comment should be actionable")
	require.Equal(t, "accepted", comments[0].FilterStatus, "returned comment should have accepted filter status")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestReviewCommentStore_UpdateClassification_Success(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewReviewCommentStore(mock)
	orgID := uuid.New()
	id := uuid.New()

	mock.ExpectExec("UPDATE review_comments SET filter_status .+ category .+ actionable .+ generalizable .+ generalized_rule .+ summary .+ WHERE id .+ AND org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	category := "style"
	summary := "Consider using consistent naming conventions"
	err = store.UpdateClassification(
		context.Background(), orgID, id,
		"accepted", &category, true, false, nil, &summary,
	)
	require.NoError(t, err, "should update classification without error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestReviewCommentStore_MarkApplied_Success(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewReviewCommentStore(mock)
	orgID := uuid.New()
	id := uuid.New()

	mock.ExpectExec("UPDATE review_comments SET applied .+ WHERE id .+ AND org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.MarkApplied(context.Background(), orgID, id)
	require.NoError(t, err, "should mark review comment as applied without error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestReviewCommentStore_CountPendingByPR_Success(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewReviewCommentStore(mock)
	orgID := uuid.New()
	prID := uuid.New()

	mock.ExpectQuery("SELECT count\\(\\*\\) FROM review_comments WHERE org_id .+ AND pull_request_id .+ AND filter_status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"count"}).AddRow(3),
		)

	count, err := store.CountPendingByPR(context.Background(), orgID, prID)
	require.NoError(t, err, "should count pending review comments without error")
	require.Equal(t, 3, count, "should return the correct count of pending comments")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

// TestReviewCommentStore_MultiTenancy verifies that org_id filtering is applied
// to all store methods, ensuring data isolation between organizations.
func TestReviewCommentStore_MultiTenancy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface)
		execute   func(store *ReviewCommentStore, orgID uuid.UUID) error
	}{
		{
			name: "GetByID filters by org_id",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM review_comments WHERE id .+ AND org_id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(rcColumns))
			},
			execute: func(store *ReviewCommentStore, orgID uuid.UUID) error {
				// We only care that the query includes org_id; the no-rows error is expected.
				_, _ = store.GetByID(context.Background(), orgID, uuid.New())
				return nil
			},
		},
		{
			name: "ListByOrg filters by org_id",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM review_comments WHERE org_id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(rcColumns))
			},
			execute: func(store *ReviewCommentStore, orgID uuid.UUID) error {
				_, err := store.ListByOrg(context.Background(), orgID, ReviewCommentFilters{})
				return err
			},
		},
		{
			name: "ListByPullRequest filters by org_id",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM review_comments WHERE org_id .+ AND pull_request_id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(rcColumns))
			},
			execute: func(store *ReviewCommentStore, orgID uuid.UUID) error {
				_, err := store.ListByPullRequest(context.Background(), orgID, uuid.New())
				return err
			},
		},
		{
			name: "ListActionableByPullRequest filters by org_id",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM review_comments WHERE org_id .+ AND pull_request_id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(rcColumns))
			},
			execute: func(store *ReviewCommentStore, orgID uuid.UUID) error {
				_, err := store.ListActionableByPullRequest(context.Background(), orgID, uuid.New())
				return err
			},
		},
		{
			name: "UpdateClassification filters by org_id",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectExec("UPDATE review_comments .+ WHERE id .+ AND org_id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			},
			execute: func(store *ReviewCommentStore, orgID uuid.UUID) error {
				return store.UpdateClassification(context.Background(), orgID, uuid.New(), "accepted", nil, false, false, nil, nil)
			},
		},
		{
			name: "MarkApplied filters by org_id",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectExec("UPDATE review_comments SET applied .+ WHERE id .+ AND org_id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			},
			execute: func(store *ReviewCommentStore, orgID uuid.UUID) error {
				return store.MarkApplied(context.Background(), orgID, uuid.New())
			},
		},
		{
			name: "CountPendingByPR filters by org_id",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT count\\(\\*\\) FROM review_comments WHERE org_id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))
			},
			execute: func(store *ReviewCommentStore, orgID uuid.UUID) error {
				_, err := store.CountPendingByPR(context.Background(), orgID, uuid.New())
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool without error")
			defer mock.Close()

			store := NewReviewCommentStore(mock)
			tt.setupMock(mock)

			orgID := uuid.New()
			_ = tt.execute(store, orgID)
			// The key assertion: the mock expectation includes org_id in the query regex.
			// If the store method does not filter by org_id, the expectation will not match
			// and ExpectationsWereMet will fail.
			require.NoError(t, mock.ExpectationsWereMet(), "query must include org_id filter for multi-tenancy isolation")
		})
	}
}

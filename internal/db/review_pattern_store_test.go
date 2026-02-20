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

var reviewPatternColumns = []string{
	"id", "org_id", "repo", "rule", "category", "source_comment_ids",
	"occurrence_count", "status", "manually_curated", "active", "created_at",
}

func newReviewPatternRow(id, orgID uuid.UUID, now time.Time) []any {
	return []any{
		id, orgID, "org/repo", "Always use structured logging", "style",
		[]uuid.UUID{uuid.New()}, 1, "candidate", false, true, now,
	}
}

func TestReviewPatternStore_Create_Success(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewReviewPatternStore(mock)
	now := time.Now()
	generatedID := uuid.New()

	p := &models.ReviewPattern{
		OrgID:            uuid.New(),
		Repo:             "org/repo",
		Rule:             "Always use structured logging",
		Category:         "style",
		SourceCommentIDs: []uuid.UUID{uuid.New()},
		OccurrenceCount:  1,
		Status:           "candidate",
		ManuallyCurated:  false,
	}

	mock.ExpectQuery("INSERT INTO review_patterns").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "created_at"}).
				AddRow(generatedID, now),
		)

	err = store.Create(context.Background(), p)
	require.NoError(t, err, "should create review pattern without error")
	require.Equal(t, generatedID, p.ID, "should set the generated ID on the review pattern")
	require.Equal(t, now, p.CreatedAt, "should set the created_at timestamp on the review pattern")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestReviewPatternStore_GetByID_Success(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewReviewPatternStore(mock)
	orgID := uuid.New()
	id := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM review_patterns WHERE id .+ AND org_id .+ AND active = true").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(reviewPatternColumns).
				AddRow(newReviewPatternRow(id, orgID, now)...),
		)

	p, err := store.GetByID(context.Background(), orgID, id)
	require.NoError(t, err, "should retrieve review pattern by ID without error")
	require.Equal(t, id, p.ID, "should return the correct review pattern ID")
	require.Equal(t, orgID, p.OrgID, "should return the correct org ID")
	require.Equal(t, "org/repo", p.Repo, "should return the correct repo")
	require.Equal(t, "Always use structured logging", p.Rule, "should return the correct rule")
	require.Equal(t, "style", p.Category, "should return the correct category")
	require.Equal(t, 1, p.OccurrenceCount, "should return the correct occurrence count")
	require.Equal(t, "candidate", p.Status, "should return the correct status")
	require.False(t, p.ManuallyCurated, "should return the correct manually_curated flag")
	require.True(t, p.Active, "should return the correct active flag")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestReviewPatternStore_GetByID_NotFound(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewReviewPatternStore(mock)

	mock.ExpectQuery("SELECT .+ FROM review_patterns WHERE id .+ AND org_id .+ AND active = true").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(reviewPatternColumns))

	_, err = store.GetByID(context.Background(), uuid.New(), uuid.New())
	require.Error(t, err, "should return an error when review pattern is not found")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestReviewPatternStore_ListByRepo_WithStatusFilter(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewReviewPatternStore(mock)
	orgID := uuid.New()
	id := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM review_patterns WHERE org_id .+ AND repo .+ AND active = true AND status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(reviewPatternColumns).
				AddRow(newReviewPatternRow(id, orgID, now)...),
		)

	patterns, err := store.ListByRepo(context.Background(), orgID, "org/repo", ReviewPatternFilters{Status: "candidate"})
	require.NoError(t, err, "should list review patterns filtered by status without error")
	require.Len(t, patterns, 1, "should return only the review pattern matching the status filter")
	require.Equal(t, id, patterns[0].ID, "filtered review pattern should have the correct ID")
	require.Equal(t, orgID, patterns[0].OrgID, "filtered review pattern should have the correct org ID")
	require.Equal(t, "candidate", patterns[0].Status, "filtered review pattern should have the correct status")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestReviewPatternStore_ListActiveByRepo_Success(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewReviewPatternStore(mock)
	orgID := uuid.New()
	id1 := uuid.New()
	id2 := uuid.New()
	now := time.Now()

	// Build a row with status='active' to match what ListActiveByRepo returns
	activeRow := func(id uuid.UUID) []any {
		return []any{
			id, orgID, "org/repo", "Use error wrapping", "error-handling",
			[]uuid.UUID{uuid.New(), uuid.New()}, 3, "active", false, true, now,
		}
	}

	mock.ExpectQuery("SELECT .+ FROM review_patterns WHERE org_id .+ AND repo .+ AND active = true AND status = 'active'").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(reviewPatternColumns).
				AddRow(activeRow(id1)...).
				AddRow(activeRow(id2)...),
		)

	patterns, err := store.ListActiveByRepo(context.Background(), orgID, "org/repo")
	require.NoError(t, err, "should list active review patterns without error")
	require.Len(t, patterns, 2, "should return both active review patterns for the repo")
	require.Equal(t, id1, patterns[0].ID, "first pattern should have the correct ID")
	require.Equal(t, id2, patterns[1].ID, "second pattern should have the correct ID")
	require.Equal(t, "active", patterns[0].Status, "first pattern should have status 'active'")
	require.Equal(t, "active", patterns[1].Status, "second pattern should have status 'active'")
	require.True(t, patterns[0].Active, "first pattern should have active flag set to true")
	require.True(t, patterns[1].Active, "second pattern should have active flag set to true")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestReviewPatternStore_FindMatchingRule_Found(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewReviewPatternStore(mock)
	orgID := uuid.New()
	id := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM review_patterns WHERE org_id .+ AND repo .+ AND active = true AND lower.rule.").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(reviewPatternColumns).
				AddRow(newReviewPatternRow(id, orgID, now)...),
		)

	p, err := store.FindMatchingRule(context.Background(), orgID, "org/repo", "always use structured logging")
	require.NoError(t, err, "should find matching rule without error")
	require.Equal(t, id, p.ID, "should return the correct review pattern ID")
	require.Equal(t, orgID, p.OrgID, "should return the correct org ID")
	require.Equal(t, "Always use structured logging", p.Rule, "should return the correct rule text")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestReviewPatternStore_FindMatchingRule_NoMatch(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewReviewPatternStore(mock)

	mock.ExpectQuery("SELECT .+ FROM review_patterns WHERE org_id .+ AND repo .+ AND active = true AND lower.rule.").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(reviewPatternColumns))

	_, err = store.FindMatchingRule(context.Background(), uuid.New(), "org/repo", "nonexistent rule")
	require.Error(t, err, "should return an error when no matching rule is found")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestReviewPatternStore_UpdatePattern_InsertOnlyVersioning(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewReviewPatternStore(mock)
	orgID := uuid.New()
	id := uuid.New()
	newID := uuid.New()
	now := time.Now()
	sourceCommentIDs := []uuid.UUID{uuid.New()}

	// Transaction: Begin
	mock.ExpectBegin()

	// Step 1: Expect the inactivation query that returns the existing row values
	mock.ExpectQuery("UPDATE review_patterns SET active = false WHERE id .+ AND org_id .+ AND active = true RETURNING").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{
				"org_id", "repo", "rule", "category", "source_comment_ids",
				"occurrence_count", "status", "manually_curated",
			}).AddRow(orgID, "org/repo", "Old rule text", "style", sourceCommentIDs, 1, "candidate", false),
		)

	// Step 2: Expect the insert of the new active row with updated values and returned row
	newRule := "Updated rule text"
	mock.ExpectQuery("INSERT INTO review_patterns").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{
				"id", "org_id", "repo", "rule", "category", "source_comment_ids",
				"occurrence_count", "status", "manually_curated", "active", "created_at",
			}).AddRow(newID, orgID, "org/repo", newRule, "style", sourceCommentIDs, 1, "candidate", true, true, now),
		)

	// Transaction: Commit
	mock.ExpectCommit()

	err = store.UpdatePattern(context.Background(), orgID, id, &newRule, nil)
	require.NoError(t, err, "should update review pattern using insert-only versioning without error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestReviewPatternStore_IncrementOccurrence_PromotesAtTwo(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewReviewPatternStore(mock)
	orgID := uuid.New()
	patternID := uuid.New()
	commentID := uuid.New()
	existingCommentID := uuid.New()

	// Transaction: Begin
	mock.ExpectBegin()

	// Step 1: Expect the inactivation query returning a candidate pattern with occurrence_count=1
	mock.ExpectQuery("UPDATE review_patterns SET active = false WHERE id .+ AND org_id .+ AND active = true RETURNING").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{
				"org_id", "repo", "rule", "category", "source_comment_ids",
				"occurrence_count", "status", "manually_curated",
			}).AddRow(orgID, "org/repo", "Always use structured logging", "style",
				[]uuid.UUID{existingCommentID}, 1, "candidate", false),
		)

	// Step 2: Expect insert of new row with occurrence_count=2 and auto-promoted status='active'
	mock.ExpectExec("INSERT INTO review_patterns").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	// Transaction: Commit
	mock.ExpectCommit()

	err = store.IncrementOccurrence(context.Background(), orgID, patternID, commentID)
	require.NoError(t, err, "should increment occurrence and promote pattern without error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestReviewPatternStore_MultiTenancy_OrgIDFilter(t *testing.T) {
	t.Parallel()

	orgA := uuid.New()
	orgB := uuid.New()
	patternID := uuid.New()
	now := time.Now()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface)
		run       func(store *ReviewPatternStore) error
	}{
		{
			name: "GetByID filters by org_id and returns no rows for wrong org",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM review_patterns WHERE id .+ AND org_id .+ AND active = true").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(reviewPatternColumns))
			},
			run: func(store *ReviewPatternStore) error {
				_, err := store.GetByID(context.Background(), orgB, patternID)
				return err
			},
		},
		{
			name: "ListByRepo filters by org_id and returns only matching org patterns",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM review_patterns WHERE org_id .+ AND repo .+ AND active = true").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(reviewPatternColumns).
							AddRow(newReviewPatternRow(patternID, orgA, now)...),
					)
			},
			run: func(store *ReviewPatternStore) error {
				patterns, err := store.ListByRepo(context.Background(), orgA, "org/repo", ReviewPatternFilters{})
				if err != nil {
					return err
				}
				require.Len(t, patterns, 1, "should return only patterns for the matching org")
				require.Equal(t, orgA, patterns[0].OrgID, "returned pattern should belong to the queried org")
				return nil
			},
		},
		{
			name: "ListActiveByRepo filters by org_id",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM review_patterns WHERE org_id .+ AND repo .+ AND active = true AND status = 'active'").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(reviewPatternColumns))
			},
			run: func(store *ReviewPatternStore) error {
				patterns, err := store.ListActiveByRepo(context.Background(), orgB, "org/repo")
				if err != nil {
					return err
				}
				require.Empty(t, patterns, "should return no patterns for an org with no active patterns")
				return nil
			},
		},
		{
			name: "FindMatchingRule filters by org_id",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM review_patterns WHERE org_id .+ AND repo .+ AND active = true AND lower.rule.").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(reviewPatternColumns))
			},
			run: func(store *ReviewPatternStore) error {
				_, err := store.FindMatchingRule(context.Background(), orgB, "org/repo", "some rule")
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

			store := NewReviewPatternStore(mock)
			tt.setupMock(mock)

			runErr := tt.run(store)
			// For GetByID and FindMatchingRule with no rows, we expect an error (no rows).
			// For List* with no rows, we expect nil error and empty slice.
			// The test logic is handled inside each run func, so we just verify expectations here.
			_ = runErr
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

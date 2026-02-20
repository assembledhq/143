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

var complexityEstimateColumns = []string{
	"id", "issue_id", "org_id", "tier", "label", "confidence", "issue_type",
	"reasoning", "estimated_files", "estimated_tokens", "model_used",
	"computed_at", "created_at",
}

func TestComplexityEstimateStore_Upsert_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewComplexityEstimateStore(mock)
	generatedID := uuid.New()
	now := time.Now()

	est := &models.ComplexityEstimate{
		IssueID:    uuid.New(),
		OrgID:      uuid.New(),
		Tier:       1,
		Label:      "simple",
		Confidence: 0.95,
		ComputedAt: now,
	}

	// Upsert uses 11 named args
	mock.ExpectQuery("INSERT INTO complexity_estimates").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(generatedID, now))

	err = store.Upsert(context.Background(), est)
	require.NoError(t, err, "should upsert without error")
	require.Equal(t, generatedID, est.ID, "should set the generated ID on the estimate")
	require.Equal(t, now, est.CreatedAt, "should set the created_at timestamp on the estimate")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestComplexityEstimateStore_GetByIssueID_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewComplexityEstimateStore(mock)
	orgID := uuid.New()
	issueID := uuid.New()
	estID := uuid.New()
	now := time.Now()

	// GetByIssueID uses 2 named args: issue_id, org_id
	mock.ExpectQuery("SELECT .+ FROM complexity_estimates WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(complexityEstimateColumns).
				AddRow(estID, issueID, orgID, 1, "simple", 0.95, nil, nil, nil, nil, nil, now, now),
		)

	result, err := store.GetByIssueID(context.Background(), orgID, issueID)
	require.NoError(t, err, "should get estimate without error")
	require.Equal(t, estID, result.ID, "should return the correct ID")
	require.Equal(t, issueID, result.IssueID, "should return the correct issue ID")
	require.Equal(t, orgID, result.OrgID, "should return the correct org ID")
	require.Equal(t, "simple", result.Label, "should return the correct label")
	require.Equal(t, 0.95, result.Confidence, "should return the correct confidence")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestComplexityEstimateStore_GetByIssueID_NotFound(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewComplexityEstimateStore(mock)

	mock.ExpectQuery("SELECT .+ FROM complexity_estimates WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(complexityEstimateColumns))

	_, err = store.GetByIssueID(context.Background(), uuid.New(), uuid.New())
	require.Error(t, err, "should return an error when estimate is not found")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestComplexityEstimateStore_ListByOrg_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewComplexityEstimateStore(mock)
	orgID := uuid.New()
	now := time.Now()

	// ListByOrg with maxTier=nil uses 1 named arg: org_id
	mock.ExpectQuery("SELECT .+ FROM complexity_estimates WHERE").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(complexityEstimateColumns).
				AddRow(uuid.New(), uuid.New(), orgID, 1, "simple", 0.9, nil, nil, nil, nil, nil, now, now).
				AddRow(uuid.New(), uuid.New(), orgID, 3, "complex", 0.8, nil, nil, nil, nil, nil, now, now),
		)

	results, err := store.ListByOrg(context.Background(), orgID, nil, 10)
	require.NoError(t, err, "should list estimates without error")
	require.Len(t, results, 2, "should return 2 estimates")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestComplexityEstimateStore_ListByOrg_WithMaxTier(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewComplexityEstimateStore(mock)
	orgID := uuid.New()
	now := time.Now()
	maxTier := 2

	// ListByOrg with maxTier set uses 2 named args: org_id, max_tier
	mock.ExpectQuery("SELECT .+ FROM complexity_estimates WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(complexityEstimateColumns).
				AddRow(uuid.New(), uuid.New(), orgID, 1, "simple", 0.9, nil, nil, nil, nil, nil, now, now),
		)

	results, err := store.ListByOrg(context.Background(), orgID, &maxTier, 10)
	require.NoError(t, err, "should list estimates without error with max tier filter")
	require.Len(t, results, 1, "should return 1 estimate at or below tier 2")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestComplexityEstimateStore_ListByOrg_Empty(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewComplexityEstimateStore(mock)

	mock.ExpectQuery("SELECT .+ FROM complexity_estimates WHERE").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(complexityEstimateColumns))

	results, err := store.ListByOrg(context.Background(), uuid.New(), nil, 10)
	require.NoError(t, err, "should list estimates without error for empty result")
	require.Empty(t, results, "should return empty results")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestComplexityEstimateStore_ListByOrg_DefaultLimit(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewComplexityEstimateStore(mock)

	mock.ExpectQuery("SELECT .+ FROM complexity_estimates WHERE").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(complexityEstimateColumns))

	// Pass invalid limit (-1) - should default to 50 internally
	results, err := store.ListByOrg(context.Background(), uuid.New(), nil, -1)
	require.NoError(t, err, "should list estimates without error with default limit")
	require.Empty(t, results, "should return empty results")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

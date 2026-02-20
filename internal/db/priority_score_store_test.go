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

var priorityScoreColumns = []string{
	"id", "issue_id", "org_id", "score", "customer_impact_score", "severity_score",
	"recency_score", "revenue_risk_score", "direction_alignment", "factors",
	"eligible_for_agent", "computed_at",
}

func TestPriorityScoreStore_Upsert_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewPriorityScoreStore(mock)
	generatedID := uuid.New()

	score := &models.PriorityScore{
		IssueID:             uuid.New(),
		OrgID:               uuid.New(),
		Score:               85.5,
		CustomerImpactScore: 20.0,
		SeverityScore:       30.0,
		RecencyScore:        15.0,
		RevenueRiskScore:    10.0,
		DirectionAlignment:  10.5,
		EligibleForAgent:    true,
		ComputedAt:          time.Now(),
	}

	// Upsert uses 11 named args
	mock.ExpectQuery("INSERT INTO priority_scores").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(generatedID))

	err = store.Upsert(context.Background(), score)
	require.NoError(t, err, "should upsert without error")
	require.Equal(t, generatedID, score.ID, "should set the generated ID on the score")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPriorityScoreStore_GetByIssueID_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewPriorityScoreStore(mock)
	orgID := uuid.New()
	issueID := uuid.New()
	scoreID := uuid.New()
	now := time.Now()

	// GetByIssueID uses 2 named args: issue_id, org_id
	mock.ExpectQuery("SELECT .+ FROM priority_scores WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(priorityScoreColumns).
				AddRow(scoreID, issueID, orgID, 85.5, 20.0, 30.0, 15.0, 10.0, 10.5, nil, true, now),
		)

	result, err := store.GetByIssueID(context.Background(), orgID, issueID)
	require.NoError(t, err, "should get score without error")
	require.Equal(t, scoreID, result.ID, "should return the correct ID")
	require.Equal(t, issueID, result.IssueID, "should return the correct issue ID")
	require.Equal(t, orgID, result.OrgID, "should return the correct org ID")
	require.Equal(t, 85.5, result.Score, "should return the correct score")
	require.Equal(t, true, result.EligibleForAgent, "should return the correct eligible_for_agent value")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPriorityScoreStore_GetByIssueID_NotFound(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewPriorityScoreStore(mock)

	mock.ExpectQuery("SELECT .+ FROM priority_scores WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(priorityScoreColumns))

	_, err = store.GetByIssueID(context.Background(), uuid.New(), uuid.New())
	require.Error(t, err, "should return an error when score is not found")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPriorityScoreStore_ListByOrg_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewPriorityScoreStore(mock)
	orgID := uuid.New()
	now := time.Now()

	// ListByOrg with onlyEligible=false uses 1 named arg: org_id
	mock.ExpectQuery("SELECT .+ FROM priority_scores WHERE").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(priorityScoreColumns).
				AddRow(uuid.New(), uuid.New(), orgID, 90.0, 25.0, 30.0, 15.0, 10.0, 10.0, nil, true, now).
				AddRow(uuid.New(), uuid.New(), orgID, 70.0, 15.0, 20.0, 15.0, 10.0, 10.0, nil, false, now),
		)

	results, err := store.ListByOrg(context.Background(), orgID, false, 10)
	require.NoError(t, err, "should list scores without error")
	require.Len(t, results, 2, "should return 2 scores")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPriorityScoreStore_ListByOrg_Empty(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewPriorityScoreStore(mock)

	mock.ExpectQuery("SELECT .+ FROM priority_scores WHERE").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(priorityScoreColumns))

	results, err := store.ListByOrg(context.Background(), uuid.New(), false, 10)
	require.NoError(t, err, "should list scores without error for empty result")
	require.Empty(t, results, "should return empty results")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPriorityScoreStore_ListByOrg_DefaultLimit(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewPriorityScoreStore(mock)

	mock.ExpectQuery("SELECT .+ FROM priority_scores WHERE").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(priorityScoreColumns))

	// Pass invalid limit (0) - should default to 50 internally
	results, err := store.ListByOrg(context.Background(), uuid.New(), false, 0)
	require.NoError(t, err, "should list scores without error with default limit")
	require.Empty(t, results, "should return empty results")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPriorityScoreStore_DeleteByIssueID_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewPriorityScoreStore(mock)

	// DeleteByIssueID uses 2 named args: issue_id, org_id
	mock.ExpectExec("DELETE FROM priority_scores WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("DELETE", 1))

	err = store.DeleteByIssueID(context.Background(), uuid.New(), uuid.New())
	require.NoError(t, err, "should delete without error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

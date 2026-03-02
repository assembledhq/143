package db

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

var pmDecisionLogColumns = []string{
	"id", "org_id", "plan_id", "issue_id", "decision", "reasoning", "outcome", "created_at",
}

func newPMDecisionRow(id, orgID, planID uuid.UUID, issueID *uuid.UUID, now time.Time) []interface{} {
	return []interface{}{
		id,
		orgID,
		planID,
		issueID,
		"delegate",
		"reasoning text",
		nil,
		now,
	}
}

func TestPMDecisionLogStore_Create(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	planID := uuid.New()
	issueID := uuid.New()
	entryID := uuid.New()
	now := time.Now()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	mock.ExpectQuery("INSERT INTO pm_decision_log").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(entryID, now))

	store := NewPMDecisionLogStore(mock)
	entry := &models.PMDecisionLogEntry{
		OrgID:     orgID,
		PlanID:    planID,
		IssueID:   &issueID,
		Decision:  models.PMDecisionTypeDelegate,
		Reasoning: "reasoning text",
	}

	err = store.Create(context.Background(), entry)
	require.NoError(t, err, "Create should succeed")
	require.Equal(t, entryID, entry.ID, "Create should set ID")
	require.WithinDuration(t, now, entry.CreatedAt, time.Second, "Create should set created_at")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPMDecisionLogStore_ListRecentByOrg(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	now := time.Now()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	mock.ExpectQuery("SELECT .+ FROM pm_decision_log WHERE org_id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(pmDecisionLogColumns).
				AddRow(newPMDecisionRow(uuid.New(), orgID, uuid.New(), nil, now)...).
				AddRow(newPMDecisionRow(uuid.New(), orgID, uuid.New(), nil, now)...),
		)

	store := NewPMDecisionLogStore(mock)
	entries, err := store.ListRecentByOrg(context.Background(), orgID, 50)
	require.NoError(t, err, "ListRecentByOrg should succeed")
	require.Len(t, entries, 2, "should return expected number of entries")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPMDecisionLogStore_UpdateOutcome(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	planID := uuid.New()
	issueID := uuid.New()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	mock.ExpectExec("UPDATE pm_decision_log").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	store := NewPMDecisionLogStore(mock)
	err = store.UpdateOutcome(context.Background(), orgID, planID, issueID, models.PMDecisionOutcomeSucceeded)
	require.NoError(t, err, "UpdateOutcome should succeed")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPMDecisionLogStore_ListRecentByOrg_Error(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	mock.ExpectQuery("SELECT .+ FROM pm_decision_log WHERE org_id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnError(fmt.Errorf("db down"))

	store := NewPMDecisionLogStore(mock)
	_, err = store.ListRecentByOrg(context.Background(), orgID, 20)
	require.Error(t, err, "ListRecentByOrg should return error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

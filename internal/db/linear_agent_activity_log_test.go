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

func TestLinearAgentActivityLogStore_Reserve(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	rowID := uuid.New()

	t.Run("first writer wins (Reserved=true)", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		mock.ExpectQuery("INSERT INTO linear_agent_activity_log").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"id", "inserted"}).AddRow(uuid.New(), true))

		store := NewLinearAgentActivityLogStore(mock)
		res, err := store.Reserve(context.Background(), orgID, rowID, "milestone:pr_opened", models.LinearAgentActivityResponse)
		require.NoError(t, err)
		require.True(t, res.Reserved, "first INSERT must report Reserved=true so caller proceeds to emit")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("UNIQUE collision yields Reserved=false (replay/race short-circuit)", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		// xmax != 0 → ON CONFLICT DO UPDATE branch took, meaning the
		// row already existed.
		mock.ExpectQuery("INSERT INTO linear_agent_activity_log").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"id", "inserted"}).AddRow(uuid.New(), false))

		store := NewLinearAgentActivityLogStore(mock)
		res, err := store.Reserve(context.Background(), orgID, rowID, "milestone:pr_opened", models.LinearAgentActivityResponse)
		require.NoError(t, err)
		require.False(t, res.Reserved, "duplicate idem_key must report Reserved=false so caller skips the Linear emit")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("rejects empty idem_key", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		store := NewLinearAgentActivityLogStore(mock)
		_, err = store.Reserve(context.Background(), orgID, rowID, "", models.LinearAgentActivityThought)
		require.Error(t, err, "empty idem_key would defeat the at-most-once semantics; must reject")
	})

	t.Run("rejects unknown activity type", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		store := NewLinearAgentActivityLogStore(mock)
		_, err = store.Reserve(context.Background(), orgID, rowID, "milestone:foo", models.LinearAgentActivityType("not_a_real_type"))
		require.Error(t, err, "the CHECK constraint catches this in DB but rejecting client-side surfaces it as a typed error")
	})
}

func TestLinearAgentActivityLogStore_Complete(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	rowID := uuid.New()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mock.ExpectExec("UPDATE linear_agent_activity_log").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	store := NewLinearAgentActivityLogStore(mock)
	require.NoError(t, store.Complete(context.Background(), orgID, rowID, "linear_act_123"))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestLinearAgentActivityLogStore_ListForAgentSessionHandlesUnconfirmedActivity(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create pgx mock")
	defer mock.Close()

	orgID := uuid.New()
	agentSessionRowID := uuid.New()
	activityID := uuid.New()
	now := time.Now().UTC()

	mock.ExpectQuery("COALESCE\\(linear_activity_id, ''\\)").
		WithArgs(orgID, agentSessionRowID).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "agent_session_row_id", "idem_key", "activity_type", "linear_activity_id", "created_at",
		}).AddRow(
			activityID, orgID, agentSessionRowID, "milestone:reserved", models.LinearAgentActivityThought, "", now,
		))

	activities, err := NewLinearAgentActivityLogStore(mock).ListForAgentSession(context.Background(), orgID, agentSessionRowID)
	require.NoError(t, err, "reserved-but-unconfirmed activities should be listable for the debug surface")
	require.Equal(t, []LinearAgentActivityLog{{
		ID:                activityID,
		OrgID:             orgID,
		AgentSessionRowID: agentSessionRowID,
		IdemKey:           "milestone:reserved",
		ActivityType:      models.LinearAgentActivityThought,
		LinearActivityID:  "",
		CreatedAt:         now,
	}}, activities, "list should include the unconfirmed activity with an empty linear activity id")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

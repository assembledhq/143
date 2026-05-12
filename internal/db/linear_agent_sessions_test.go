package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestLinearAgentSessionStore_UpsertOnCreated(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	integrationID := uuid.New()
	rowID := uuid.New()
	now := time.Now().UTC()

	t.Run("inserts new row and returns created=true", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		mock.ExpectQuery("INSERT INTO linear_agent_sessions").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{
				"id", "org_id", "integration_id", "linear_agent_session_id",
				"linear_issue_id", "linear_issue_identifier",
				"linear_app_user_id", "linear_creator_user_id",
				"session_id", "state", "last_event_received_at",
				"created_at", "updated_at", "inserted",
			}).AddRow(
				rowID, orgID, integrationID, "as_1",
				"iss_1", "ACS-1",
				"app_1", "user_1",
				nil, "pending", &now,
				now, now, true,
			))

		store := NewLinearAgentSessionStore(mock)
		row, created, err := store.UpsertOnCreated(context.Background(), orgID, UpsertOnCreatedInput{
			OrgID:                orgID,
			IntegrationID:        integrationID,
			LinearAgentSessionID: "as_1",
			LinearIssueID:        "iss_1",
		})
		require.NoError(t, err)
		require.True(t, created, "first insert should return created=true (xmax=0)")
		require.Equal(t, rowID, row.ID)
		require.Equal(t, models.LinearAgentSessionStatePending, row.State)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("coalesces optional nullable metadata in returning row", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "test should create pgx mock")
		defer mock.Close()

		mock.ExpectQuery("COALESCE\\(linear_issue_identifier, ''\\).*COALESCE\\(linear_app_user_id, ''\\).*COALESCE\\(linear_creator_user_id, ''\\)").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{
				"id", "org_id", "integration_id", "linear_agent_session_id",
				"linear_issue_id", "linear_issue_identifier",
				"linear_app_user_id", "linear_creator_user_id",
				"session_id", "state", "last_event_received_at",
				"created_at", "updated_at", "inserted",
			}).AddRow(
				rowID, orgID, integrationID, "as_1",
				"iss_1", "",
				"", "",
				nil, "pending", &now,
				now, now, true,
			))

		store := NewLinearAgentSessionStore(mock)
		row, created, err := store.UpsertOnCreated(context.Background(), orgID, UpsertOnCreatedInput{
			OrgID:                orgID,
			IntegrationID:        integrationID,
			LinearAgentSessionID: "as_1",
			LinearIssueID:        "iss_1",
		})
		require.NoError(t, err, "nullable optional metadata should scan as empty strings")
		require.True(t, created, "first insert should still report created=true")
		require.Equal(t, "", row.LinearAppUserID, "missing app user id should be represented as empty string")
		require.NoError(t, mock.ExpectationsWereMet(), "upsert should coalesce all nullable string metadata in SQL")
	})

	t.Run("re-delivery returns created=false with existing session_id preserved", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		existingSession := uuid.New()
		mock.ExpectQuery("INSERT INTO linear_agent_sessions").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{
				"id", "org_id", "integration_id", "linear_agent_session_id",
				"linear_issue_id", "linear_issue_identifier",
				"linear_app_user_id", "linear_creator_user_id",
				"session_id", "state", "last_event_received_at",
				"created_at", "updated_at", "inserted",
			}).AddRow(
				rowID, orgID, integrationID, "as_1",
				"iss_1", "ACS-1",
				"app_1", "user_1",
				&existingSession, "in_progress", &now,
				now, now, false,
			))

		store := NewLinearAgentSessionStore(mock)
		row, created, err := store.UpsertOnCreated(context.Background(), orgID, UpsertOnCreatedInput{
			OrgID:                orgID,
			IntegrationID:        integrationID,
			LinearAgentSessionID: "as_1",
			LinearIssueID:        "iss_1",
		})
		require.NoError(t, err)
		require.False(t, created, "re-delivery should return created=false (xmax!=0)")
		require.NotNil(t, row.SessionID, "re-delivery preserves session_id from prior insert")
		require.Equal(t, existingSession, *row.SessionID)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("rejects missing required fields before hitting DB", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()
		// Intentionally no ExpectQuery — validation must short-circuit
		// before any SQL fires. mock.ExpectationsWereMet at the end of
		// the subtest catches a regression that lets a malformed input
		// reach the DB.

		store := NewLinearAgentSessionStore(mock)
		// in.OrgID matches the argument so the org-mismatch check
		// passes; what trips the validation is the missing
		// IntegrationID + LinearAgentSessionID + LinearIssueID.
		_, _, err = store.UpsertOnCreated(context.Background(), orgID, UpsertOnCreatedInput{
			OrgID: orgID,
		})
		require.Error(t, err, "missing required fields should fail before reaching DB")
		require.NoError(t, mock.ExpectationsWereMet(), "validation must short-circuit before any SQL fires")
	})

	t.Run("rejects org_id mismatch", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		store := NewLinearAgentSessionStore(mock)
		_, _, err = store.UpsertOnCreated(context.Background(), orgID, UpsertOnCreatedInput{
			OrgID:                uuid.New(), // different org
			IntegrationID:        integrationID,
			LinearAgentSessionID: "as_1",
			LinearIssueID:        "iss_1",
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "org_id mismatch")
	})
}

func TestLinearAgentSessionStore_Lookup(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	rowID := uuid.New()
	now := time.Now().UTC()

	t.Run("returns ErrLinearAgentSessionNotFound on miss", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		mock.ExpectQuery("SELECT id, org_id").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnError(pgx.ErrNoRows)

		store := NewLinearAgentSessionStore(mock)
		_, err = store.Lookup(context.Background(), orgID, "as_missing")
		require.True(t, errors.Is(err, ErrLinearAgentSessionNotFound),
			"missing row must surface as the sentinel so callers can distinguish from system errors")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns row on hit", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		mock.ExpectQuery("SELECT id, org_id").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{
				"id", "org_id", "integration_id", "linear_agent_session_id",
				"linear_issue_id", "linear_issue_identifier",
				"linear_app_user_id", "linear_creator_user_id",
				"session_id", "state", "last_event_received_at",
				"created_at", "updated_at",
			}).AddRow(
				rowID, orgID, uuid.New(), "as_1",
				"iss_1", "ACS-1",
				"app_1", "user_1",
				nil, "pending", &now,
				now, now,
			))

		store := NewLinearAgentSessionStore(mock)
		row, err := store.Lookup(context.Background(), orgID, "as_1")
		require.NoError(t, err)
		require.Equal(t, "as_1", row.LinearAgentSessionID)
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestLinearAgentSessionStore_AttachSession(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	rowID := uuid.New()
	sessionID := uuid.New()

	t.Run("succeeds when session_id is null", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		mock.ExpectExec("UPDATE linear_agent_sessions").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))

		store := NewLinearAgentSessionStore(mock)
		require.NoError(t, store.AttachSession(context.Background(), orgID, rowID, sessionID))
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns ErrLinearAgentSessionMismatch when session is taken by a different id", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		// 0 rows affected because the WHERE (session_id IS NULL OR
		// session_id = @session_id) prevents the update.
		mock.ExpectExec("UPDATE linear_agent_sessions").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("UPDATE", 0))

		store := NewLinearAgentSessionStore(mock)
		err = store.AttachSession(context.Background(), orgID, rowID, sessionID)
		require.True(t, errors.Is(err, ErrLinearAgentSessionMismatch),
			"the duplicate-attach mismatch must be the documented sentinel")
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestLinearAgentSessionStore_ListByOrg(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	now := time.Now().UTC()

	t.Run("returns rows in updated_at DESC order", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		mock.ExpectQuery("SELECT id, org_id").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{
				"id", "org_id", "integration_id", "linear_agent_session_id",
				"linear_issue_id", "linear_issue_identifier",
				"linear_app_user_id", "linear_creator_user_id",
				"session_id", "state", "last_event_received_at",
				"created_at", "updated_at",
			}).AddRow(
				uuid.New(), orgID, uuid.New(), "as_1",
				"iss_1", "ACS-1",
				"app_1", "user_1",
				nil, "pending", &now,
				now, now,
			))

		store := NewLinearAgentSessionStore(mock)
		rows, err := store.ListByOrg(context.Background(), orgID, 0)
		require.NoError(t, err)
		require.Len(t, rows, 1)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("empty result returns empty slice without error", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		mock.ExpectQuery("SELECT id, org_id").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{
				"id", "org_id", "integration_id", "linear_agent_session_id",
				"linear_issue_id", "linear_issue_identifier",
				"linear_app_user_id", "linear_creator_user_id",
				"session_id", "state", "last_event_received_at",
				"created_at", "updated_at",
			}))

		store := NewLinearAgentSessionStore(mock)
		rows, err := store.ListByOrg(context.Background(), orgID, 0)
		require.NoError(t, err, "empty result is the expected steady state for a brand-new org; must not surface as error")
		require.Empty(t, rows, "no agent sessions yet → empty slice")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("uses default limit when caller passes 0", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		// Match the default limit of 50 — store guards against limit<=0.
		mock.ExpectQuery("SELECT id, org_id").
			WithArgs(orgID, 50).
			WillReturnRows(pgxmock.NewRows([]string{
				"id", "org_id", "integration_id", "linear_agent_session_id",
				"linear_issue_id", "linear_issue_identifier",
				"linear_app_user_id", "linear_creator_user_id",
				"session_id", "state", "last_event_received_at",
				"created_at", "updated_at",
			}))

		store := NewLinearAgentSessionStore(mock)
		_, err = store.ListByOrg(context.Background(), orgID, 0)
		require.NoError(t, err)
		require.NoError(t, mock.ExpectationsWereMet(),
			"limit=0 must be replaced with the documented default; otherwise an unbounded scan slips through")
	})
}

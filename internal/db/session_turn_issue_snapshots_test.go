package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestSessionTurnIssueSnapshotStore_Create(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionTurnIssueSnapshotStore(mock)
	now := time.Now().UTC()
	snapshotID := uuid.New()
	snapshot := &models.SessionTurnIssueSnapshot{
		OrgID:      uuid.New(),
		SessionID:  uuid.New(),
		TurnNumber: 2,
		LinkedIssues: []models.SessionIssueSnapshotEntry{
			{
				IssueID:     uuid.New(),
				Role:        models.SessionIssueLinkRolePrimary,
				Position:    0,
				Title:       "Fix checkout timeout",
				ExternalID:  "ENG-123",
				Source:      models.IssueSourceLinear,
				Description: "Customers hit a timeout after payment authorization.",
			},
		},
	}

	mock.ExpectQuery("INSERT INTO session_turn_issue_snapshots").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(snapshotID, now))

	err = store.Create(context.Background(), snapshot)
	require.NoError(t, err, "Create should persist the issue snapshot")
	require.Equal(t, snapshotID, snapshot.ID, "Create should populate the generated snapshot id")
	require.JSONEq(t, `[{"issue_id":"`+snapshot.LinkedIssues[0].IssueID.String()+`","role":"primary","position":0,"title":"Fix checkout timeout","external_id":"ENG-123","source":"linear","description":"Customers hit a timeout after payment authorization."}]`, string(snapshot.RawLinkedIssues), "Create should store the encoded linked issue payload on the model")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

// TestSessionTurnIssueSnapshotStore_Create_UpsertsOnTurnConflict guards the
// regression that wedged sessions whose first turn attempt failed after the
// snapshot insert but before UpdateTurnComplete bumped current_turn — every
// retry then computed the same turn_number and tripped the unique constraint
// on (session_id, turn_number) inside resolve issue context. Create must now
// upsert: same id/created_at, refreshed linked_issues.
func TestSessionTurnIssueSnapshotStore_Create_UpsertsOnTurnConflict(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionTurnIssueSnapshotStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()
	snapshotID := uuid.New()
	createdAt := time.Now().UTC()

	firstAttempt := &models.SessionTurnIssueSnapshot{
		OrgID:      orgID,
		SessionID:  sessionID,
		TurnNumber: 3,
		LinkedIssues: []models.SessionIssueSnapshotEntry{
			{IssueID: uuid.New(), Role: models.SessionIssueLinkRolePrimary, Title: "first attempt"},
		},
	}
	secondAttempt := &models.SessionTurnIssueSnapshot{
		OrgID:      orgID,
		SessionID:  sessionID,
		TurnNumber: 3,
		LinkedIssues: []models.SessionIssueSnapshotEntry{
			{IssueID: uuid.New(), Role: models.SessionIssueLinkRolePrimary, Title: "retry with edited links"},
		},
	}

	// The query must declare ON CONFLICT (session_id, turn_number) DO UPDATE so
	// retries of an unfinished turn don't fail with SQLSTATE 23505.
	upsertPattern := `(?s)INSERT INTO session_turn_issue_snapshots.+ON CONFLICT \(session_id, turn_number\) DO UPDATE.+SET linked_issues = EXCLUDED\.linked_issues`

	mock.ExpectQuery(upsertPattern).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(snapshotID, createdAt))
	require.NoError(t, store.Create(context.Background(), firstAttempt), "first attempt should insert successfully")
	require.Equal(t, snapshotID, firstAttempt.ID, "first attempt should populate id from RETURNING")

	// Simulating retry of the same turn: ON CONFLICT path returns the original
	// row's id and created_at via RETURNING. linked_issues on the row is now
	// the second attempt's payload.
	mock.ExpectQuery(upsertPattern).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(snapshotID, createdAt))
	require.NoError(t, store.Create(context.Background(), secondAttempt), "retry must not return a unique-violation error")
	require.Equal(t, snapshotID, secondAttempt.ID, "retry should resolve to the same row id (first-wins identity)")
	require.Equal(t, createdAt, secondAttempt.CreatedAt, "retry should preserve the original created_at")

	require.Contains(t, string(secondAttempt.RawLinkedIssues), "retry with edited links", "retry should persist the latest linked-issue payload (last-wins content)")
	require.NotContains(t, string(secondAttempt.RawLinkedIssues), "first attempt", "retry should overwrite stale linked-issue payload")

	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionTurnIssueSnapshotStore_Create_QueryError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionTurnIssueSnapshotStore(mock)
	snapshot := &models.SessionTurnIssueSnapshot{
		OrgID:        uuid.New(),
		SessionID:    uuid.New(),
		TurnNumber:   1,
		LinkedIssues: []models.SessionIssueSnapshotEntry{{IssueID: uuid.New(), Role: models.SessionIssueLinkRolePrimary}},
	}

	mock.ExpectQuery("INSERT INTO session_turn_issue_snapshots").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(context.Canceled)

	err = store.Create(context.Background(), snapshot)
	require.Error(t, err, "Create should return insert errors")
	require.Contains(t, err.Error(), "insert session turn issue snapshot", "Create should wrap insert errors with context")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionTurnIssueSnapshotStore_GetByID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionTurnIssueSnapshotStore(mock)
	now := time.Now().UTC()
	orgID := uuid.New()
	sessionID := uuid.New()
	snapshotID := uuid.New()
	repoID := uuid.New()
	linkedIssues, err := json.Marshal([]models.SessionIssueSnapshotEntry{
		{
			IssueID:      uuid.New(),
			Role:         models.SessionIssueLinkRolePrimary,
			Position:     0,
			Title:        "Fix checkout timeout",
			ExternalID:   "ENG-123",
			Source:       models.IssueSourceLinear,
			Description:  "Customers hit a timeout after payment authorization.",
			RepositoryID: &repoID,
			Status:       "open",
		},
	})
	require.NoError(t, err, "test setup should marshal linked issue payloads")

	mock.ExpectQuery("SELECT .+ FROM session_turn_issue_snapshots").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "org_id", "session_id", "turn_number", "linked_issues", "created_at"}).
				AddRow(snapshotID, orgID, sessionID, 2, linkedIssues, now),
		)

	snapshot, err := store.GetByID(context.Background(), orgID, snapshotID)
	require.NoError(t, err, "GetByID should decode persisted linked issues")
	require.Len(t, snapshot.LinkedIssues, 1, "GetByID should decode the linked issue array")
	require.Equal(t, "Fix checkout timeout", snapshot.LinkedIssues[0].Title, "GetByID should preserve linked issue metadata")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionTurnIssueSnapshotStore_GetByID_QueryError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionTurnIssueSnapshotStore(mock)
	mock.ExpectQuery("SELECT .+ FROM session_turn_issue_snapshots").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(context.Canceled)

	_, err = store.GetByID(context.Background(), uuid.New(), uuid.New())
	require.Error(t, err, "GetByID should return query errors")
	require.Contains(t, err.Error(), "query session turn issue snapshot", "GetByID should wrap query errors with context")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionTurnIssueSnapshotStore_GetByID_InvalidJSON(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionTurnIssueSnapshotStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()
	snapshotID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM session_turn_issue_snapshots").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "org_id", "session_id", "turn_number", "linked_issues", "created_at"}).
				AddRow(snapshotID, orgID, sessionID, 1, []byte(`{not-json}`), time.Now().UTC()),
		)

	_, err = store.GetByID(context.Background(), orgID, snapshotID)
	require.Error(t, err, "GetByID should return decode errors for invalid linked issue JSON")
	require.Contains(t, err.Error(), "decode linked issues", "GetByID should wrap linked issue decode failures")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionTurnIssueSnapshotStore_GetByTurn_InvalidJSON(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionTurnIssueSnapshotStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM session_turn_issue_snapshots").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "org_id", "session_id", "turn_number", "linked_issues", "created_at"}).
				AddRow(uuid.New(), orgID, sessionID, 2, []byte(`{not-json}`), time.Now().UTC()),
		)

	_, err = store.GetByTurn(context.Background(), orgID, sessionID, 2)
	require.Error(t, err, "GetByTurn should fail when persisted linked issue JSON is invalid")
	require.Contains(t, err.Error(), "decode linked issues", "GetByTurn should wrap linked issue decode failures")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionTurnIssueSnapshotStore_GetByTurn_QueryError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionTurnIssueSnapshotStore(mock)
	mock.ExpectQuery("SELECT .+ FROM session_turn_issue_snapshots").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(context.Canceled)

	_, err = store.GetByTurn(context.Background(), uuid.New(), uuid.New(), 1)
	require.Error(t, err, "GetByTurn should return query errors")
	require.Contains(t, err.Error(), "query session turn issue snapshot by turn", "GetByTurn should wrap query errors with context")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

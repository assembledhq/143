package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

func TestSessionChangesetStoreGetPrimaryScopesByOrgAndSession(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create the database mock")
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()
	changesetID := uuid.New()
	now := time.Now()
	mock.ExpectQuery(`SELECT .+ FROM session_changesets WHERE org_id = .+ AND session_id = .+ AND is_primary`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "session_id", "is_primary", "order_index", "title", "summary",
			"status", "target_branch", "base_branch", "working_branch", "stacked_on_changeset_id",
			"head_sha", "expected_remote_head_sha", "base_head_sha", "pr_creation_state", "pr_creation_error", "created_at", "updated_at",
		}).AddRow(
			changesetID, orgID, sessionID, true, 0, "Primary", "", models.ChangesetStatusPlanned,
			"main", "main", nil, nil, nil, nil, nil, models.PRCreationStateIdle, nil, now, now,
		))

	changeset, err := NewSessionChangesetStore(mock).GetPrimary(context.Background(), orgID, sessionID)
	require.NoError(t, err, "GetPrimary should return the org-scoped primary changeset")
	require.Equal(t, changesetID, changeset.ID, "GetPrimary should return the expected changeset")
	require.Equal(t, orgID, changeset.OrgID, "GetPrimary should preserve tenant ownership")
	require.Equal(t, sessionID, changeset.SessionID, "GetPrimary should preserve session ownership")
	require.True(t, changeset.IsPrimary, "GetPrimary should only return the primary changeset")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionChangesetStoreUpdatePrimaryBranchesScopesByOrgAndSession(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create the database mock")
	defer mock.Close()

	baseSHA := "base-sha"
	workingBranch := "143/feature"
	mock.ExpectExec(`UPDATE session_changesets SET .+ WHERE org_id = .+ AND session_id = .+ AND is_primary`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = NewSessionChangesetStore(mock).UpdatePrimaryBranches(
		context.Background(), uuid.New(), uuid.New(), "main", &workingBranch, &baseSHA,
	)
	require.NoError(t, err, "UpdatePrimaryBranches should update the tenant-scoped primary mirror")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionChangesetStoreTryMarkPRCreationQueued(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		rows       int64
		expectTrue bool
	}{
		{name: "queues available changeset", rows: 1, expectTrue: true},
		{name: "rejects in-flight changeset", rows: 0, expectTrue: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "test should create the database mock")
			defer mock.Close()
			mock.ExpectExec(`UPDATE session_changesets SET pr_creation_state = 'queued'.+WHERE org_id = .+ AND session_id = .+ AND id =`).
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnResult(pgxmock.NewResult("UPDATE", tt.rows))

			queued, err := NewSessionChangesetStore(mock).TryMarkPRCreationQueued(
				context.Background(), uuid.New(), uuid.New(), uuid.New(),
			)
			require.NoError(t, err, "queue CAS should complete without an error")
			require.Equal(t, tt.expectTrue, queued, "queue CAS should report whether it won")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestSessionChangesetStoreRecordPushedHeadUpdatesBothSHAFields(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create the database mock")
	defer mock.Close()
	mock.ExpectExec(`UPDATE session_changesets SET head_sha = .+ expected_remote_head_sha = .+ WHERE org_id = .+ AND session_id = .+ AND id =`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = NewSessionChangesetStore(mock).RecordPushedHead(context.Background(), uuid.New(), uuid.New(), uuid.New(), "head-sha")
	require.NoError(t, err, "successful platform push should update local and expected remote heads")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionChangesetStoreListBySessionScopesAndOrders(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create database mock")
	t.Cleanup(mock.Close)
	orgID, sessionID := uuid.New(), uuid.New()
	changesetID := uuid.New()
	now := time.Now().UTC()
	mock.ExpectQuery(`SELECT .+ FROM session_changesets.+WHERE org_id = .+ AND session_id = .+ORDER BY order_index`).
		WithArgs(orgID, sessionID).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "is_primary", "order_index", "title", "summary", "status", "target_branch",
			"base_branch", "working_branch", "stacked_on_changeset_id", "head_sha", "created_at", "updated_at",
		}).AddRow(changesetID, true, 0, "Foundation", "Base work", "planned", "main", "main", nil, nil, nil, now, now))

	actual, err := NewSessionChangesetStore(mock).ListBySession(context.Background(), orgID, sessionID)
	require.NoError(t, err, "listing changesets should succeed")
	require.Equal(t, []models.ChangesetSummary{{
		ID: changesetID, IsPrimary: true, OrderIndex: 0, Title: "Foundation", Summary: "Base work",
		Status: models.ChangesetStatusPlanned, TargetBranch: "main", BaseBranch: "main", CreatedAt: now, UpdatedAt: now,
	}}, actual, "changesets should retain their stable order and complete summary")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionChangesetStoreUpdateMetadataScopesByTenantAndParent(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create database mock")
	t.Cleanup(mock.Close)
	orgID, sessionID, changesetID := uuid.New(), uuid.New(), uuid.New()
	title := "API integration"
	mock.ExpectQuery(`UPDATE session_changesets SET.+WHERE org_id = .+ AND session_id = .+ AND id = .+RETURNING`).
		WithArgs(&title, (*string)(nil), orgID, sessionID, changesetID).
		WillReturnError(pgx.ErrNoRows)

	_, err = NewSessionChangesetStore(mock).UpdateMetadata(context.Background(), orgID, sessionID, changesetID, &title, nil)
	require.ErrorIs(t, err, pgx.ErrNoRows, "missing tenant-scoped changeset should remain distinguishable")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionChangesetStoreCreateRetriesConcurrentOrderConflict(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create database mock")
	t.Cleanup(mock.Close)
	orgID, sessionID, changesetID := uuid.New(), uuid.New(), uuid.New()
	now := time.Now().UTC()
	query := `INSERT INTO session_changesets .+ RETURNING`
	args := []any{orgID, sessionID, "API", "Endpoints", (*uuid.UUID)(nil)}
	mock.ExpectQuery(query).WithArgs(args...).WillReturnError(&pgconn.PgError{
		Code: "23505", ConstraintName: "session_changesets_org_id_session_id_order_index_key",
	})
	mock.ExpectQuery(query).WithArgs(args...).WillReturnRows(pgxmock.NewRows([]string{
		"id", "org_id", "session_id", "is_primary", "order_index", "title", "summary",
		"status", "target_branch", "base_branch", "working_branch", "stacked_on_changeset_id",
		"head_sha", "expected_remote_head_sha", "base_head_sha", "pr_creation_state", "pr_creation_error", "created_at", "updated_at",
	}).AddRow(
		changesetID, orgID, sessionID, false, 1, "API", "Endpoints", models.ChangesetStatusPlanned,
		"main", "main", nil, nil, nil, nil, nil, models.PRCreationStateIdle, nil, now, now,
	))

	actual, err := NewSessionChangesetStore(mock).Create(context.Background(), orgID, sessionID, "API", "Endpoints", nil)
	require.NoError(t, err, "concurrent order allocation should retry")
	require.Equal(t, changesetID, actual.ID, "retry should return the created changeset")
	require.NoError(t, mock.ExpectationsWereMet(), "create should retry only the order uniqueness conflict")
}

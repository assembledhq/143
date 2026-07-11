package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
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

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

func TestNormalizeSplitPaths(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		paths    []string
		expected []string
	}{
		{name: "sorts and deduplicates", paths: []string{"z.go", " a.go ", "z.go"}, expected: []string{"a.go", "z.go"}},
		{name: "rejects paths outside workspace", paths: []string{"/etc/passwd", "../secret", "a/../secret", "ok/file.go"}, expected: []string{"ok/file.go"}},
		{name: "drops empty paths", paths: []string{"", "  "}, expected: []string{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, normalizeSplitPaths(tt.paths), "normalization should return safe stable repository paths")
		})
	}
}

func TestSplitDiffPaths(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		diff     string
		expected []string
	}{
		{
			name: "extracts modified added deleted and renamed destination paths",
			diff: "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n" +
				"diff --git a/old.go b/new.go\nsimilarity index 100%\nrename from old.go\nrename to new.go\n" +
				"diff --git a/a.go b/a.go\n",
			expected: []string{"a.go", "new.go"},
		},
		{name: "extracts quoted paths", diff: "diff --git \"a/docs/file name.md\" \"b/docs/file name.md\"\n--- \"a/docs/file name.md\"\n+++ \"b/docs/file name.md\"\n", expected: []string{"docs/file name.md"}},
		{name: "ignores malformed headers", diff: "--- a/a.go\n+++ b/a.go\ndiff --git malformed\n", expected: []string{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, splitDiffPaths(tt.diff), "diff parsing should return each source destination path once")
		})
	}
}

func TestSessionChangesetStoreGetSplitStatusRequiresMaterializedDiffs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		materialized *string
		expected     models.ChangesetSplitVerification
		complete     bool
	}{
		{name: "planned assignment is not complete", materialized: nil, expected: models.ChangesetSplitVerificationPlanned, complete: false},
		{name: "matching materialized patch is verified", materialized: stringPtr("diff --git a/api.go b/api.go\n+code\n"), expected: models.ChangesetSplitVerificationVerified, complete: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "test should create database mock")
			t.Cleanup(mock.Close)
			orgID, sessionID, snapshotID, changesetID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			patch := "diff --git a/api.go b/api.go\n+code\n"
			mock.ExpectQuery("SELECT p.status, p.source_diff_snapshot_id, d.diff").WithArgs(orgID, sessionID).
				WillReturnRows(pgxmock.NewRows([]string{"status", "source_diff_snapshot_id", "diff"}).AddRow("draft", snapshotID, patch))
			mock.ExpectQuery("SELECT c.id, p.path, c.materialized_diff").WithArgs(orgID, sessionID).
				WillReturnRows(pgxmock.NewRows([]string{"id", "path", "materialized_diff"}).AddRow(changesetID, "api.go", tt.materialized))
			mock.ExpectQuery("SELECT path, reason, confirmed_by_user_id, created_at").WithArgs(orgID, sessionID).
				WillReturnRows(pgxmock.NewRows([]string{"path", "reason", "confirmed_by_user_id", "created_at"}))
			status, err := NewSessionChangesetStore(mock).GetSplitStatus(context.Background(), orgID, sessionID)
			require.NoError(t, err, "split status should be derived from the frozen source and materialized diffs")
			require.Equal(t, tt.expected, status.Verification, "verification state should reflect whether branch diffs were captured")
			require.Equal(t, tt.complete, status.Complete, "completion should require a matching materialized branch diff")
			require.NoError(t, mock.ExpectationsWereMet(), "all split status queries should remain tenant scoped")
		})
	}
}

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
			"head_sha", "expected_remote_head_sha", "base_head_sha", "worktree_path", "materialization_error", "materialized_diff", "pr_creation_state", "pr_creation_error", "created_at", "updated_at",
		}).AddRow(
			changesetID, orgID, sessionID, true, 0, "Primary", "", models.ChangesetStatusPlanned,
			"main", "main", nil, nil, nil, nil, nil, nil, nil, nil, models.PRCreationStateIdle, nil, now, now,
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
			"base_branch", "working_branch", "stacked_on_changeset_id", "head_sha", "worktree_path", "materialization_error", "created_at", "updated_at",
		}).AddRow(changesetID, true, 0, "Foundation", "Base work", "planned", "main", "main", nil, nil, nil, nil, nil, now, now))

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
		"head_sha", "expected_remote_head_sha", "base_head_sha", "worktree_path", "materialization_error", "materialized_diff", "pr_creation_state", "pr_creation_error", "created_at", "updated_at",
	}).AddRow(
		changesetID, orgID, sessionID, false, 1, "API", "Endpoints", models.ChangesetStatusPlanned,
		"main", "main", nil, nil, nil, nil, nil, nil, nil, nil, models.PRCreationStateIdle, nil, now, now,
	))

	actual, err := NewSessionChangesetStore(mock).Create(context.Background(), orgID, sessionID, "API", "Endpoints", nil)
	require.NoError(t, err, "concurrent order allocation should retry")
	require.Equal(t, changesetID, actual.ID, "retry should return the created changeset")
	require.NoError(t, mock.ExpectationsWereMet(), "create should retry only the order uniqueness conflict")
}

package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

// previewAnyArgs returns n pgxmock.AnyArg() matchers for NamedArgs expansion.
func previewAnyArgs(n int) []any {
	args := make([]any, n)
	for i := range args {
		args[i] = pgxmock.AnyArg()
	}
	return args
}

func previewStringPtr(value string) *string {
	return &value
}

// =============================================================================
// Column lists for mock rows
// =============================================================================

var previewInstanceTestCols = []string{
	"id", "session_id", "preview_target_id", "org_id", "user_id", "profile_name", "name", "status",
	"provider", "worker_node_id", "preview_handle", "primary_service", "port",
	"config_digest", "base_commit_sha", "last_accessed_at", "expires_at", "stopped_at",
	"last_path", "memory_limit_mb", "cpu_limit_millis", "disk_limit_mb", "recycle_config", "recycle_sandbox", "current_phase", "request_id", "error", "created_at", "updated_at", "recycled_at", "recycle_scheduled_at",
	"source_workspace_revision", "source_workspace_revision_updated_at", "unavailable_reason", "preview_holding_container",
}

var previewServiceTestCols = []string{
	"id", "preview_instance_id", "service_name", "role", "status",
	"command", "cwd", "port", "pid", "error", "created_at",
}

var previewInfraTestCols = []string{
	"id", "preview_instance_id", "infra_name", "template",
	"container_id", "status", "host", "port", "credentials_hash", "error", "created_at",
}

var previewSnapshotTestCols = []string{
	"id", "preview_instance_id", "trigger", "url_path", "blob_ref",
	"viewport_width", "viewport_height", "console_errors", "file_changes", "created_at",
}

var previewLogTestCols = []string{
	"id", "preview_instance_id", "org_id", "level", "step", "message",
	"metadata", "created_at",
}

var previewAccessSessionTestCols = []string{
	"id", "org_id", "user_id", "preview_instance_id",
	"session_token_hash", "issued_at", "expires_at", "revoked_at", "last_accessed_at", "created_at",
}

var previewRuntimeTestCols = []string{
	"id", "org_id", "preview_instance_id", "runtime_epoch", "worker_node_id",
	"endpoint_url", "preview_handle", "primary_port", "status", "lease_expires_at",
	"last_heartbeat_at", "drain_requested_at", "stopped_at", "error", "unavailable_reason", "created_at", "updated_at",
}

var previewStartupCacheTestCols = []string{
	"id", "org_id", "repo_id", "snapshot_key", "blob_path",
	"size_bytes", "worker_node_id", "last_used_at", "created_at",
}

var prPreviewStateTestCols = []string{
	"id", "org_id", "repo_id", "pr_number", "github_comment_id",
	"last_preview_instance_id", "last_screenshot_blob_path", "last_visual_diff_blob_path",
	"base_snapshot_key", "status", "created_at", "updated_at",
}

var previewTargetTestCols = []string{
	"id", "org_id", "repository_id", "branch", "commit_sha", "preview_config_name",
	"resolved_config_digest", "source_type", "source_id", "source_url",
	"created_by_user_id", "request_id", "created_at",
}

var previewLinkTestCols = []string{
	"id", "org_id", "preview_target_id", "link_type", "slug", "repository_id",
	"pr_number", "created_at", "updated_at",
}

// Helper to build a standard preview instance row.
func newPreviewInstanceRow(id, sessionID, orgID, userID uuid.UUID, now time.Time) []any {
	return []any{
		id, sessionID, nil, orgID, userID, "bootstrap", "my-preview", "starting",
		"docker", "worker-1", "handle-abc", "web", 3000,
		"sha256:abc", "deadbeef", now, now.Add(30 * time.Minute), nil,
		"/", 512, 500, 10240, []byte(`{"version":"3","name":"my-preview","primary":"web","services":{"web":{"command":["npm","start"],"port":3000,"ready":{"http_path":"/"}}},"credentials":{"mode":"none"},"network":{"mode":"restricted"}}`), []byte(`{"id":"sandbox-1","provider":"docker","work_dir":"/workspace","metadata":{"container_id":"abc"}}`), "reserved", previewStringPtr("req-1"), "", now, now, now, nil,
		(*int64)(nil), (*time.Time)(nil), "",
		false,
	}
}

func newPreviewTargetRow(id, orgID, repoID, userID uuid.UUID, now time.Time) []any {
	return []any{
		id, orgID, repoID, "feature/previews", "0123456789abcdef0123456789abcdef01234567", "default",
		"sha256:config", "manual", "source-1", "https://example.com/source",
		userID, previewStringPtr("req-1"), now,
	}
}

func newPreviewLinkRow(id, orgID, targetID, repoID uuid.UUID, now time.Time) []any {
	return []any{
		id, orgID, targetID, "target", "slug-1", &repoID,
		(*int)(nil), now, now,
	}
}

func newPreviewRuntimeRow(id, orgID, previewID uuid.UUID, now time.Time) []any {
	return []any{
		id, orgID, previewID, 1, "worker-1",
		"http://worker-runtime:8080", "handle-1", 3000, "ready", now.Add(45 * time.Second),
		now, nil, nil, "", "", now, now,
	}
}

// =============================================================================
// Preview Instance Tests
// =============================================================================

func TestPreviewStore_CreatePreviewTarget(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	store := NewPreviewStore(mock)
	now := time.Now()
	targetID := uuid.New()
	orgID := uuid.New()
	repoID := uuid.New()
	userID := uuid.New()

	target := &models.PreviewTarget{
		OrgID:                orgID,
		RepositoryID:         repoID,
		Branch:               "feature/previews",
		CommitSHA:            "0123456789abcdef0123456789abcdef01234567",
		PreviewConfigName:    "default",
		ResolvedConfigDigest: "sha256:config",
		SourceType:           models.PreviewSourceTypeManual,
		SourceID:             "source-1",
		SourceURL:            "https://example.com/source",
		CreatedByUserID:      userID,
	}

	mock.ExpectQuery("INSERT INTO preview_targets").
		WithArgs(previewAnyArgs(11)...).
		WillReturnRows(pgxmock.NewRows(previewTargetTestCols).AddRow(newPreviewTargetRow(targetID, orgID, repoID, userID, now)...))

	err = store.CreatePreviewTarget(context.Background(), target)
	require.NoError(t, err, "CreatePreviewTarget should insert a branch target")
	require.Equal(t, targetID, target.ID, "CreatePreviewTarget should hydrate the generated target ID")
	require.Equal(t, now, target.CreatedAt, "CreatePreviewTarget should hydrate timestamps from the database")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPreviewStore_GetActivePreviewForTargetScopesByOrg(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	store := NewPreviewStore(mock)
	now := time.Now()
	orgID := uuid.New()
	targetID := uuid.New()
	previewID := uuid.New()
	sessionID := uuid.New()
	userID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM preview_instances").
		WithArgs(previewAnyArgs(2)...).
		WillReturnRows(pgxmock.NewRows(previewInstanceTestCols).AddRow(newPreviewInstanceRow(previewID, sessionID, orgID, userID, now)...))

	instance, err := store.GetActivePreviewForTarget(context.Background(), orgID, targetID)
	require.NoError(t, err, "GetActivePreviewForTarget should return an active target preview")
	require.Equal(t, previewID, instance.ID, "GetActivePreviewForTarget should return the matching preview instance")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPreviewStore_GetLatestPreviewForTargetScopesByOrg(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	store := NewPreviewStore(mock)
	now := time.Now()
	orgID := uuid.New()
	targetID := uuid.New()
	previewID := uuid.New()
	sessionID := uuid.New()
	userID := uuid.New()
	row := newPreviewInstanceRow(previewID, sessionID, orgID, userID, now)
	row[2] = &targetID
	row[7] = string(models.PreviewStatusStopped)

	mock.ExpectQuery("SELECT .+ FROM preview_instances").
		WithArgs(previewAnyArgs(2)...).
		WillReturnRows(pgxmock.NewRows(previewInstanceTestCols).AddRow(row...))

	instance, err := store.GetLatestPreviewForTarget(context.Background(), orgID, targetID)
	require.NoError(t, err, "GetLatestPreviewForTarget should return the newest target preview")
	require.Equal(t, previewID, instance.ID, "GetLatestPreviewForTarget should return the matching preview instance")
	require.Equal(t, &targetID, instance.PreviewTargetID, "GetLatestPreviewForTarget should preserve the target ID")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPreviewStore_GetPreviewForPublicHost(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	store := NewPreviewStore(mock)
	now := time.Now()
	orgID := uuid.New()
	targetID := uuid.New()
	previewID := uuid.New()
	sessionID := uuid.New()
	userID := uuid.New()
	stoppedAt := now.Add(-5 * time.Minute)
	row := newPreviewInstanceRow(previewID, sessionID, orgID, userID, now)
	row[2] = &targetID
	row[7] = string(models.PreviewStatusStopped)
	row[17] = &stoppedAt

	mock.ExpectQuery("SELECT .+ FROM preview_instances").
		WithArgs(previewAnyArgs(1)...).
		WillReturnRows(pgxmock.NewRows(previewInstanceTestCols).AddRow(row...))

	instance, err := store.GetPreviewForPublicHost(context.Background(), targetID)
	require.NoError(t, err, "GetPreviewForPublicHost should return the newest runtime for the public host")
	require.Equal(t, previewID, instance.ID, "GetPreviewForPublicHost should return the matching preview instance")
	require.Equal(t, &targetID, instance.PreviewTargetID, "GetPreviewForPublicHost should preserve the target ID")
	require.Equal(t, &stoppedAt, instance.StoppedAt, "GetPreviewForPublicHost should preserve stopped time")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPreviewStore_UpsertPreviewLink(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	store := NewPreviewStore(mock)
	now := time.Now()
	linkID := uuid.New()
	orgID := uuid.New()
	targetID := uuid.New()
	repoID := uuid.New()

	link := &models.PreviewLink{
		OrgID:           orgID,
		PreviewTargetID: targetID,
		LinkType:        models.PreviewLinkTypeTarget,
		Slug:            "slug-1",
		RepositoryID:    &repoID,
	}

	mock.ExpectQuery("INSERT INTO preview_links").
		WithArgs(previewAnyArgs(6)...).
		WillReturnRows(pgxmock.NewRows(previewLinkTestCols).AddRow(newPreviewLinkRow(linkID, orgID, targetID, repoID, now)...))

	err = store.UpsertPreviewLink(context.Background(), link)
	require.NoError(t, err, "UpsertPreviewLink should create or update a stable preview link")
	require.Equal(t, linkID, link.ID, "UpsertPreviewLink should hydrate the link ID")
	require.Equal(t, now, link.UpdatedAt, "UpsertPreviewLink should hydrate updated_at")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPreviewStore_CreatePreviewInstance(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)
	now := time.Now()
	generatedID := uuid.New()
	sessionID := uuid.New()
	orgID := uuid.New()
	userID := uuid.New()

	p := &models.PreviewInstance{
		SessionID:      sessionID,
		OrgID:          orgID,
		UserID:         userID,
		ProfileName:    "bootstrap",
		Name:           "my-preview",
		Status:         models.PreviewStatusStarting,
		Provider:       "docker",
		WorkerNodeID:   "worker-1",
		PreviewHandle:  "handle-abc",
		PrimaryService: "web",
		Port:           3000,
		ConfigDigest:   "sha256:abc",
		BaseCommitSHA:  "deadbeef",
		ExpiresAt:      now.Add(30 * time.Minute),
		LastPath:       "/",
		MemoryLimitMB:  512,
		CPULimitMillis: 500,
		DiskLimitMB:    10240,
		RecycleConfig:  json.RawMessage(`{"version":"3"}`),
		RecycleSandbox: json.RawMessage(`{"id":"sandbox-1"}`),
	}
	revision := int64(7)
	revisionUpdatedAt := now.Add(-time.Minute)
	p.SourceWorkspaceRevision = &revision
	p.SourceWorkspaceRevisionUpdatedAt = &revisionUpdatedAt
	row := newPreviewInstanceRow(generatedID, sessionID, orgID, userID, now)
	for i, column := range previewInstanceTestCols {
		switch column {
		case "source_workspace_revision":
			row[i] = &revision
		case "source_workspace_revision_updated_at":
			row[i] = &revisionUpdatedAt
		}
	}

	mock.ExpectQuery("INSERT INTO preview_instances").
		WithArgs(previewAnyArgs(24)...).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(row...),
		)

	err = store.CreatePreviewInstance(context.Background(), p)
	require.NoError(t, err)
	require.Equal(t, generatedID, p.ID)
	require.Equal(t, 10240, p.DiskLimitMB)
	require.Equal(t, &revision, p.SourceWorkspaceRevision)
	require.Equal(t, &revisionUpdatedAt, p.SourceWorkspaceRevisionUpdatedAt)
	require.Equal(t, now, p.CreatedAt)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewStore_GetPreviewInstance(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface, orgID, id uuid.UUID, now time.Time)
		expectErr bool
	}{
		{
			name: "returns instance when found",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, id uuid.UUID, now time.Time) {
				sessionID := uuid.New()
				userID := uuid.New()
				mock.ExpectQuery("SELECT .+ FROM preview_instances WHERE id").
					WithArgs(previewAnyArgs(2)...).
					WillReturnRows(
						pgxmock.NewRows(previewInstanceTestCols).
							AddRow(newPreviewInstanceRow(id, sessionID, orgID, userID, now)...),
					)
			},
		},
		{
			name: "returns error when not found",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, id uuid.UUID, now time.Time) {
				mock.ExpectQuery("SELECT .+ FROM preview_instances WHERE id").
					WithArgs(previewAnyArgs(2)...).
					WillReturnRows(pgxmock.NewRows(previewInstanceTestCols))
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err)
			defer mock.Close()

			store := NewPreviewStore(mock)
			orgID := uuid.New()
			id := uuid.New()
			now := time.Now()
			tt.setupMock(mock, orgID, id, now)

			instance, err := store.GetPreviewInstance(context.Background(), orgID, id)
			if tt.expectErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, id, instance.ID)
			require.Equal(t, orgID, instance.OrgID)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestPreviewStore_GetActivePreviewForSession(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()
	previewID := uuid.New()
	userID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM preview_instances.+session_id").
		WithArgs(previewAnyArgs(2)...).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(newPreviewInstanceRow(previewID, sessionID, orgID, userID, now)...),
		)

	instance, err := store.GetActivePreviewForSession(context.Background(), orgID, sessionID)
	require.NoError(t, err)
	require.Equal(t, previewID, instance.ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewStore_GetLatestFailedPreviewForSession(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	store := NewPreviewStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()
	previewID := uuid.New()
	userID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM preview_instances.+status = 'failed'.+ORDER BY created_at DESC").
		WithArgs(previewAnyArgs(2)...).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(newPreviewInstanceRow(previewID, sessionID, orgID, userID, now)...),
		)

	instance, err := store.GetLatestFailedPreviewForSession(context.Background(), orgID, sessionID)
	require.NoError(t, err, "GetLatestFailedPreviewForSession should return the latest failed preview")
	require.Equal(t, previewID, instance.ID, "GetLatestFailedPreviewForSession should return the expected preview")
	require.Equal(t, orgID, instance.OrgID, "GetLatestFailedPreviewForSession should preserve org scoping")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPreviewStore_GetLatestTerminalPreviewForSession(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	store := NewPreviewStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()
	previewID := uuid.New()
	userID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM preview_instances.+status IN \\('stopped', 'expired', 'failed', 'unavailable'\\).+ORDER BY created_at DESC").
		WithArgs(previewAnyArgs(2)...).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(newPreviewInstanceRow(previewID, sessionID, orgID, userID, now)...),
		)

	instance, err := store.GetLatestTerminalPreviewForSession(context.Background(), orgID, sessionID)
	require.NoError(t, err, "GetLatestTerminalPreviewForSession should return the latest terminal preview")
	require.Equal(t, previewID, instance.ID, "GetLatestTerminalPreviewForSession should return the expected preview")
	require.Equal(t, orgID, instance.OrgID, "GetLatestTerminalPreviewForSession should preserve org scoping")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPreviewStore_UpdatePreviewStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		rows      int64
		execErr   error
		expectErr bool
	}{
		{name: "updates successfully", rows: 1},
		{name: "not found returns error", rows: 0, expectErr: true},
		{name: "update error returns error", execErr: errors.New("db down"), expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err)
			defer mock.Close()

			store := NewPreviewStore(mock)

			mock.ExpectExec("UPDATE preview_instances SET status").
				WithArgs(previewAnyArgs(5)...).
				WillReturnResult(pgxmock.NewResult("UPDATE", tt.rows)).
				WillReturnError(tt.execErr)

			err = store.UpdatePreviewStatus(context.Background(), uuid.New(), uuid.New(), models.PreviewStatusReady, "")
			if tt.expectErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestPreviewStore_ListBranchPreviewIndex_ResumableUsesWarmCachePredicate(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	store := NewPreviewStore(mock)
	orgID := uuid.New()
	repoID := uuid.New()
	targetID := uuid.New()
	now := time.Now()
	estimate := 30

	rows := pgxmock.NewRows([]string{
		"target_id", "preview_id", "repository_id", "repository_full_name", "branch", "commit_sha", "preview_config_name",
		"source_type", "source_id", "source_url", "status", "created_at", "sort_created_at", "expires_at", "stopped_at", "stopped_reason",
		"current_phase", "error", "resumable", "resume_estimate_seconds",
	}).AddRow(
		targetID, nil, repoID, "acme/app", "feature", "abc123", "",
		models.PreviewSourceTypePullRequest, "acme/app#42@abc123", "https://github.com/acme/app/pull/42",
		string(models.PreviewStatusStopped), now, now, nil, &now, string(models.PreviewStoppedReasonWarmPolicy),
		"stopped", "", true, &estimate,
	)

	mock.ExpectQuery("preview_startup_cache[\\s\\S]+JOIN nodes[\\s\\S]+n\\.status = 'active'").
		WithArgs(previewAnyArgs(8)...).
		WillReturnRows(rows)

	summaries, err := store.ListBranchPreviewIndex(context.Background(), orgID, BranchPreviewIndexFilters{
		Scope: "resumable",
		Limit: 20,
	})
	require.NoError(t, err, "ListBranchPreviewIndex should return resumable previews")
	require.Equal(t, []models.BranchPreviewSummary{
		{
			TargetID:              targetID,
			RepositoryID:          repoID,
			RepositoryFullName:    "acme/app",
			Branch:                "feature",
			CommitSHA:             "abc123",
			SourceType:            models.PreviewSourceTypePullRequest,
			SourceID:              "acme/app#42@abc123",
			SourceURL:             "https://github.com/acme/app/pull/42",
			Status:                string(models.PreviewStatusStopped),
			CreatedAt:             now,
			SortCreatedAt:         now,
			StoppedAt:             &now,
			StoppedReason:         models.PreviewStoppedReasonWarmPolicy,
			CurrentPhase:          "stopped",
			Resumable:             true,
			ResumeEstimateSeconds: &estimate,
		},
	}, summaries, "ListBranchPreviewIndex should hydrate resumability metadata from the SQL result")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPreviewStore_CountBranchPreviewIndexScopes_ResumableUsesWarmCachePredicate(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	store := NewPreviewStore(mock)

	mock.ExpectQuery("preview_startup_cache[\\s\\S]+JOIN nodes[\\s\\S]+resumable").
		WithArgs(previewAnyArgs(5)...).
		WillReturnRows(pgxmock.NewRows([]string{"running", "resumable", "recent"}).AddRow(2, 1, 3))

	counts, err := store.CountBranchPreviewIndexScopes(context.Background(), uuid.New(), BranchPreviewIndexFilters{})
	require.NoError(t, err, "CountBranchPreviewIndexScopes should return scope counts")
	require.Equal(t, map[string]int{"running": 2, "resumable": 1, "recent": 3}, counts, "scope counts should include real resumable rows")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPreviewStore_UpdatePreviewStatus_TerminalBeginError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	store := NewPreviewStore(mock)

	mock.ExpectBegin().WillReturnError(errors.New("begin failed"))

	err = store.UpdatePreviewStatus(context.Background(), uuid.New(), uuid.New(), models.PreviewStatusFailed, "boom")
	require.Error(t, err, "terminal status update should return begin errors")
	require.Contains(t, err.Error(), "begin failed", "terminal status error should include begin failure")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPreviewStore_UpdatePreviewStatus_TerminalCascadesChildren(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	store := NewPreviewStore(mock)

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE preview_instances SET status.+stopped_at.+updated_at").
		WithArgs(previewAnyArgs(5)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE preview_services SET").
		WithArgs(previewAnyArgs(5)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 2))
	mock.ExpectExec("UPDATE preview_infrastructure SET").
		WithArgs(previewAnyArgs(5)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()

	err = store.UpdatePreviewStatus(context.Background(), uuid.New(), uuid.New(), models.PreviewStatusFailed, "boom")
	require.NoError(t, err, "terminal status update should cascade children")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPreviewStore_UpdatePreviewStatus_TerminalClearsPreviewHold(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	store := NewPreviewStore(mock)

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE preview_instances SET status.+preview_holding_container = FALSE.+stopped_at.+updated_at").
		WithArgs(previewAnyArgs(5)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE preview_services SET").
		WithArgs(previewAnyArgs(5)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectExec("UPDATE preview_infrastructure SET").
		WithArgs(previewAnyArgs(5)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectCommit()

	err = store.UpdatePreviewStatus(context.Background(), uuid.New(), uuid.New(), models.PreviewStatusFailed, "boom")
	require.NoError(t, err, "terminal status update should clear the preview sandbox hold")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPreviewStore_UpdatePreviewStatus_TerminalUpdateError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	store := NewPreviewStore(mock)

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE preview_instances SET status.+stopped_at.+updated_at").
		WithArgs(previewAnyArgs(5)...).
		WillReturnError(errors.New("update failed"))
	mock.ExpectRollback()

	err = store.UpdatePreviewStatus(context.Background(), uuid.New(), uuid.New(), models.PreviewStatusFailed, "boom")
	require.Error(t, err, "terminal status update should return parent update errors")
	require.Contains(t, err.Error(), "update failed", "terminal status error should include parent update failure")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPreviewStore_UpdatePreviewStatus_TerminalRollbackOnCascadeError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	store := NewPreviewStore(mock)

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE preview_instances SET status.+stopped_at.+updated_at").
		WithArgs(previewAnyArgs(5)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE preview_services SET").
		WithArgs(previewAnyArgs(5)...).
		WillReturnError(errors.New("service cascade failed"))
	mock.ExpectRollback()

	err = store.UpdatePreviewStatus(context.Background(), uuid.New(), uuid.New(), models.PreviewStatusFailed, "boom")
	require.Error(t, err, "terminal status update should fail when child cascade fails")
	require.Contains(t, err.Error(), "service cascade failed", "terminal status error should include cascade failure")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPreviewStore_UpdatePreviewStatus_TerminalRollbackOnInfraCascadeError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	store := NewPreviewStore(mock)

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE preview_instances SET status.+stopped_at.+updated_at").
		WithArgs(previewAnyArgs(5)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE preview_services SET").
		WithArgs(previewAnyArgs(5)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE preview_infrastructure SET").
		WithArgs(previewAnyArgs(5)...).
		WillReturnError(errors.New("infra cascade failed"))
	mock.ExpectRollback()

	err = store.UpdatePreviewStatus(context.Background(), uuid.New(), uuid.New(), models.PreviewStatusFailed, "boom")
	require.Error(t, err, "terminal status update should fail when infra cascade fails")
	require.Contains(t, err.Error(), "infra cascade failed", "terminal status error should include infra cascade failure")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPreviewStore_UpdatePreviewStatus_TerminalCommitError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	store := NewPreviewStore(mock)

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE preview_instances SET status.+stopped_at.+updated_at").
		WithArgs(previewAnyArgs(5)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE preview_services SET").
		WithArgs(previewAnyArgs(5)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectExec("UPDATE preview_infrastructure SET").
		WithArgs(previewAnyArgs(5)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectCommit().WillReturnError(errors.New("commit failed"))

	err = store.UpdatePreviewStatus(context.Background(), uuid.New(), uuid.New(), models.PreviewStatusFailed, "boom")
	require.Error(t, err, "terminal status update should return commit errors")
	require.Contains(t, err.Error(), "commit failed", "terminal status error should include commit failure")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPreviewStore_UpdatePreviewStatusIfActive_NonTerminal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		rows      int64
		execErr   error
		expected  bool
		expectErr bool
	}{
		{name: "updates active preview", rows: 1, expected: true},
		{name: "already terminal returns false", rows: 0, expected: false},
		{name: "update error returns error", execErr: errors.New("db down"), expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "pgxmock pool should be created")
			defer mock.Close()

			store := NewPreviewStore(mock)

			mock.ExpectExec("UPDATE preview_instances SET status").
				WithArgs(previewAnyArgs(5)...).
				WillReturnResult(pgxmock.NewResult("UPDATE", tt.rows)).
				WillReturnError(tt.execErr)

			updated, err := store.UpdatePreviewStatusIfActive(context.Background(), uuid.New(), uuid.New(), models.PreviewStatusReady, "")
			if tt.expectErr {
				require.Error(t, err, "conditional non-terminal update should return database errors")
				require.False(t, updated, "conditional non-terminal update should not report updates on error")
			} else {
				require.NoError(t, err, "conditional non-terminal update should not error")
				require.Equal(t, tt.expected, updated, "conditional non-terminal update should report whether the row changed")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestPreviewStore_UpdatePreviewStatusIfActive_TerminalBeginError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	store := NewPreviewStore(mock)

	mock.ExpectBegin().WillReturnError(errors.New("begin failed"))

	updated, err := store.UpdatePreviewStatusIfActive(context.Background(), uuid.New(), uuid.New(), models.PreviewStatusFailed, "")
	require.Error(t, err, "terminal conditional update should return begin errors")
	require.False(t, updated, "terminal conditional update should not report updates on begin error")
	require.Contains(t, err.Error(), "begin failed", "terminal conditional error should include begin failure")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPreviewStore_UpdatePreviewStatusIfActive_TerminalCascadesChildren(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	store := NewPreviewStore(mock)

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE preview_instances SET status.+stopped_at.+updated_at").
		WithArgs(previewAnyArgs(5)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE preview_services SET").
		WithArgs(previewAnyArgs(5)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectExec("UPDATE preview_infrastructure SET").
		WithArgs(previewAnyArgs(5)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectCommit()

	updated, err := store.UpdatePreviewStatusIfActive(context.Background(), uuid.New(), uuid.New(), models.PreviewStatusFailed, "")
	require.NoError(t, err, "terminal conditional status update should cascade children")
	require.True(t, updated, "conditional terminal status update should report the row was changed")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPreviewStore_UpdatePreviewStatusIfActive_TerminalClearsPreviewHold(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	store := NewPreviewStore(mock)

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE preview_instances SET status.+preview_holding_container = FALSE.+stopped_at.+updated_at").
		WithArgs(previewAnyArgs(5)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE preview_services SET").
		WithArgs(previewAnyArgs(5)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectExec("UPDATE preview_infrastructure SET").
		WithArgs(previewAnyArgs(5)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectCommit()

	updated, err := store.UpdatePreviewStatusIfActive(context.Background(), uuid.New(), uuid.New(), models.PreviewStatusFailed, "")
	require.NoError(t, err, "terminal conditional status update should clear the preview sandbox hold")
	require.True(t, updated, "conditional terminal status update should report the row was changed")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPreviewStore_UpdatePreviewStatusIfActive_TerminalUpdateError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	store := NewPreviewStore(mock)

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE preview_instances SET status.+stopped_at.+updated_at").
		WithArgs(previewAnyArgs(5)...).
		WillReturnError(errors.New("update failed"))
	mock.ExpectRollback()

	updated, err := store.UpdatePreviewStatusIfActive(context.Background(), uuid.New(), uuid.New(), models.PreviewStatusFailed, "")
	require.Error(t, err, "terminal conditional update should return parent update errors")
	require.False(t, updated, "terminal conditional update should not report updates when parent update fails")
	require.Contains(t, err.Error(), "update failed", "terminal conditional error should include parent update failure")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPreviewStore_UpdatePreviewStatusIfActive_TerminalCascadeError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	store := NewPreviewStore(mock)

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE preview_instances SET status.+stopped_at.+updated_at").
		WithArgs(previewAnyArgs(5)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE preview_services SET").
		WithArgs(previewAnyArgs(5)...).
		WillReturnError(errors.New("service cascade failed"))
	mock.ExpectRollback()

	updated, err := store.UpdatePreviewStatusIfActive(context.Background(), uuid.New(), uuid.New(), models.PreviewStatusFailed, "")
	require.Error(t, err, "terminal conditional update should return cascade errors")
	require.True(t, updated, "terminal conditional update should report parent row changed before cascade failed")
	require.Contains(t, err.Error(), "service cascade failed", "terminal conditional error should include cascade failure")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPreviewStore_UpdatePreviewStatusIfActive_TerminalCommitError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	store := NewPreviewStore(mock)

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE preview_instances SET status.+stopped_at.+updated_at").
		WithArgs(previewAnyArgs(5)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE preview_services SET").
		WithArgs(previewAnyArgs(5)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectExec("UPDATE preview_infrastructure SET").
		WithArgs(previewAnyArgs(5)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectCommit().WillReturnError(errors.New("commit failed"))

	updated, err := store.UpdatePreviewStatusIfActive(context.Background(), uuid.New(), uuid.New(), models.PreviewStatusFailed, "")
	require.Error(t, err, "terminal conditional update should return commit errors")
	require.True(t, updated, "terminal conditional update should report parent row changed before commit failed")
	require.Contains(t, err.Error(), "commit failed", "terminal conditional error should include commit failure")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPreviewStore_UpdatePreviewStatusIfActive_NoCascadeWhenAlreadyTerminal(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	store := NewPreviewStore(mock)

	mock.ExpectBegin()
	// Conditional update affects 0 rows (preview was already terminal).
	mock.ExpectExec("UPDATE preview_instances SET status.+stopped_at.+updated_at").
		WithArgs(previewAnyArgs(5)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	// No cascade expected — children were already updated by the prior terminal write.
	mock.ExpectRollback()

	updated, err := store.UpdatePreviewStatusIfActive(context.Background(), uuid.New(), uuid.New(), models.PreviewStatusFailed, "")
	require.NoError(t, err, "already-terminal conditional update should not error")
	require.False(t, updated, "conditional update should report no row was changed")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPreviewStore_StopPreview(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		rows           int64
		expectErr      bool
		expectCascades bool
	}{
		{name: "stops active preview", rows: 1, expectCascades: true},
		{name: "already stopped returns error", rows: 0, expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err)
			defer mock.Close()

			store := NewPreviewStore(mock)

			mock.ExpectExec("UPDATE preview_instances SET status.+stopped_at.+updated_at").
				WithArgs(previewAnyArgs(3)...).
				WillReturnResult(pgxmock.NewResult("UPDATE", tt.rows))
			if tt.expectCascades {
				mock.ExpectExec("UPDATE preview_services SET").
					WithArgs(previewAnyArgs(5)...).
					WillReturnResult(pgxmock.NewResult("UPDATE", 0))
				mock.ExpectExec("UPDATE preview_infrastructure SET").
					WithArgs(previewAnyArgs(5)...).
					WillReturnResult(pgxmock.NewResult("UPDATE", 0))
				mock.ExpectExec("UPDATE preview_runtimes").
					WithArgs(previewAnyArgs(2)...).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			}

			err = store.StopPreview(context.Background(), uuid.New(), uuid.New())
			if tt.expectErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestPreviewStore_StopPreview_CascadeError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	store := NewPreviewStore(mock)

	mock.ExpectExec("UPDATE preview_instances SET status.+stopped_at.+updated_at").
		WithArgs(previewAnyArgs(3)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE preview_services SET").
		WithArgs(previewAnyArgs(5)...).
		WillReturnError(errors.New("service cascade failed"))

	err = store.StopPreview(context.Background(), uuid.New(), uuid.New())
	require.Error(t, err, "stop preview should return cascade errors")
	require.Contains(t, err.Error(), "service cascade failed", "stop preview error should include cascade failure")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPreviewStore_CascadeChildrenToTerminal_NonTerminalNoop(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	store := NewPreviewStore(mock)

	err = store.cascadeChildrenToTerminal(context.Background(), uuid.New(), uuid.New(), models.PreviewStatusReady, "")
	require.NoError(t, err, "non-terminal parent status should not cascade children")
	require.NoError(t, mock.ExpectationsWereMet(), "no database statements should be executed")
}

func TestPreviewStore_CountActivePreviewsByOrg(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)

	mock.ExpectQuery("SELECT COUNT.+FROM preview_instances WHERE org_id").
		WithArgs(previewAnyArgs(1)...).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(3))

	count, err := store.CountActivePreviewsByOrg(context.Background(), uuid.New())
	require.NoError(t, err)
	require.Equal(t, 3, count)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewStore_ListExpiredPreviews(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)
	now := time.Now()
	id1 := uuid.New()
	id2 := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM preview_instances.+expires_at < .+ORDER BY expires_at").
		WithArgs(previewAnyArgs(1)...).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(newPreviewInstanceRow(id1, uuid.New(), uuid.New(), uuid.New(), now)...).
				AddRow(newPreviewInstanceRow(id2, uuid.New(), uuid.New(), uuid.New(), now)...),
		)

	results, err := store.ListExpiredPreviews(context.Background(), now)
	require.NoError(t, err)
	require.Len(t, results, 2)
	require.Equal(t, id1, results[0].ID)
	require.Equal(t, id2, results[1].ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewStore_ListExpiredPreviewsForWorker(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)
	now := time.Now()
	id1 := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM preview_instances.+worker_node_id = .+expires_at < .+ORDER BY expires_at").
		WithArgs(previewAnyArgs(2)...).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(newPreviewInstanceRow(id1, uuid.New(), uuid.New(), uuid.New(), now)...),
		)

	results, err := store.ListExpiredPreviewsForWorker(context.Background(), "worker-1", now)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, id1, results[0].ID)
	require.Equal(t, "worker-1", results[0].WorkerNodeID)
	require.NoError(t, mock.ExpectationsWereMet())
}

// =============================================================================
// Preview Service Tests
// =============================================================================

func TestPreviewStore_CreatePreviewService(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)
	now := time.Now()
	generatedID := uuid.New()
	previewID := uuid.New()

	svc := &models.PreviewService{
		PreviewInstanceID: previewID,
		ServiceName:       "web",
		Role:              models.PreviewServiceRolePrimary,
		Status:            models.PreviewServiceStatusStarting,
		Command:           []string{"npm", "start"},
		Cwd:               ".",
		Port:              3000,
	}

	mock.ExpectQuery("INSERT INTO preview_services").
		WithArgs(previewAnyArgs(7)...).
		WillReturnRows(
			pgxmock.NewRows(previewServiceTestCols).
				AddRow(generatedID, previewID, "web", "primary", "starting",
					[]string{"npm", "start"}, ".", 3000, nil, "", now),
		)

	err = store.CreatePreviewService(context.Background(), svc)
	require.NoError(t, err, "CreatePreviewService should insert the preview service")
	require.Equal(t, generatedID, svc.ID, "CreatePreviewService should hydrate the generated service ID")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPreviewStore_CreatePreviewService_UpsertsForRetry(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock pool should be created")
	defer mock.Close()

	store := NewPreviewStore(mock)
	now := time.Now()
	generatedID := uuid.New()
	previewID := uuid.New()

	svc := &models.PreviewService{
		PreviewInstanceID: previewID,
		ServiceName:       "frontend",
		Role:              models.PreviewServiceRolePrimary,
		Status:            models.PreviewServiceStatusStarting,
		Command:           []string{"npm", "run", "dev"},
		Cwd:               "frontend",
		Port:              3000,
	}

	mock.ExpectQuery("INSERT INTO preview_services.+ON CONFLICT \\(preview_instance_id, service_name\\).+DO UPDATE").
		WithArgs(previewAnyArgs(7)...).
		WillReturnRows(
			pgxmock.NewRows(previewServiceTestCols).
				AddRow(generatedID, previewID, "frontend", "primary", "starting",
					[]string{"npm", "run", "dev"}, "frontend", 3000, nil, "", now),
		)

	err = store.CreatePreviewService(context.Background(), svc)
	require.NoError(t, err, "CreatePreviewService should be idempotent for a retried launch")
	require.Equal(t, generatedID, svc.ID, "CreatePreviewService should return the existing or inserted row")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPreviewStore_ListServicesByPreview(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)
	now := time.Now()
	orgID := uuid.New()
	previewID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM preview_services.+preview_instance_id").
		WithArgs(previewAnyArgs(2)...).
		WillReturnRows(
			pgxmock.NewRows(previewServiceTestCols).
				AddRow(uuid.New(), previewID, "web", "primary", "ready",
					[]string{"npm", "start"}, ".", 3000, nil, "", now).
				AddRow(uuid.New(), previewID, "worker", "support", "starting",
					[]string{"npm", "run", "worker"}, ".", 4000, nil, "", now),
		)

	svcs, err := store.ListServicesByPreview(context.Background(), orgID, previewID)
	require.NoError(t, err)
	require.Len(t, svcs, 2)
	require.Equal(t, "web", svcs[0].ServiceName)
	require.Equal(t, "worker", svcs[1].ServiceName)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewStore_UpdateServiceStatus(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)

	mock.ExpectExec("UPDATE preview_services SET status").
		WithArgs(previewAnyArgs(5)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.UpdateServiceStatus(context.Background(), uuid.New(), uuid.New(), "web", models.PreviewServiceStatusReady, "")
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// =============================================================================
// Preview Infrastructure Tests
// =============================================================================

func TestPreviewStore_CreatePreviewInfrastructure(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)
	now := time.Now()
	generatedID := uuid.New()
	previewID := uuid.New()

	infra := &models.PreviewInfrastructure{
		PreviewInstanceID: previewID,
		InfraName:         "db",
		Template:          "postgres-17",
		ContainerID:       "ctr-123",
		Status:            models.PreviewInfraStatusProvisioning,
		Host:              "localhost",
		Port:              5432,
		CredentialsHash:   "hash-abc",
	}

	mock.ExpectQuery("INSERT INTO preview_infrastructure").
		WithArgs(previewAnyArgs(8)...).
		WillReturnRows(
			pgxmock.NewRows(previewInfraTestCols).
				AddRow(generatedID, previewID, "db", "postgres-17",
					"ctr-123", "provisioning", "localhost", 5432, "hash-abc", "", now),
		)

	err = store.CreatePreviewInfrastructure(context.Background(), infra)
	require.NoError(t, err, "CreatePreviewInfrastructure should insert the preview infrastructure")
	require.Equal(t, generatedID, infra.ID, "CreatePreviewInfrastructure should hydrate the generated infrastructure ID")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPreviewStore_CreatePreviewInfrastructure_UpsertsForRetry(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock pool should be created")
	defer mock.Close()

	store := NewPreviewStore(mock)
	now := time.Now()
	generatedID := uuid.New()
	previewID := uuid.New()

	infra := &models.PreviewInfrastructure{
		PreviewInstanceID: previewID,
		InfraName:         "db",
		Template:          "postgres-17",
		ContainerID:       "",
		Status:            models.PreviewInfraStatusProvisioning,
		Host:              "",
		Port:              0,
		CredentialsHash:   "",
	}

	mock.ExpectQuery("INSERT INTO preview_infrastructure.+ON CONFLICT \\(preview_instance_id, infra_name\\).+DO UPDATE").
		WithArgs(previewAnyArgs(8)...).
		WillReturnRows(
			pgxmock.NewRows(previewInfraTestCols).
				AddRow(generatedID, previewID, "db", "postgres-17",
					"", "provisioning", "", 0, "", "", now),
		)

	err = store.CreatePreviewInfrastructure(context.Background(), infra)
	require.NoError(t, err, "CreatePreviewInfrastructure should be idempotent for a retried launch")
	require.Equal(t, generatedID, infra.ID, "CreatePreviewInfrastructure should return the existing or inserted row")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPreviewStore_ListInfraByPreview(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)
	now := time.Now()
	orgID := uuid.New()
	previewID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM preview_infrastructure.+preview_instance_id").
		WithArgs(previewAnyArgs(2)...).
		WillReturnRows(
			pgxmock.NewRows(previewInfraTestCols).
				AddRow(uuid.New(), previewID, "db", "postgres-17",
					"ctr-1", "healthy", "localhost", 5432, "h1", "", now).
				AddRow(uuid.New(), previewID, "cache", "redis-7",
					"ctr-2", "healthy", "localhost", 6379, "h2", "", now),
		)

	infras, err := store.ListInfraByPreview(context.Background(), orgID, previewID)
	require.NoError(t, err)
	require.Len(t, infras, 2)
	require.Equal(t, "db", infras[0].InfraName)
	require.Equal(t, "cache", infras[1].InfraName)
	require.NoError(t, mock.ExpectationsWereMet())
}

// =============================================================================
// Preview Snapshot Tests
// =============================================================================

func TestPreviewStore_CreateSnapshot(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)
	now := time.Now()
	generatedID := uuid.New()
	previewID := uuid.New()

	snap := &models.PreviewSnapshot{
		PreviewInstanceID: previewID,
		Trigger:           models.PreviewSnapshotTriggerBaseline,
		URLPath:           "/",
		BlobRef:           "blob://snap-1",
		ViewportWidth:     1280,
		ViewportHeight:    720,
		ConsoleErrors:     json.RawMessage(`[]`),
	}

	mock.ExpectQuery("INSERT INTO preview_snapshots").
		WithArgs(previewAnyArgs(8)...).
		WillReturnRows(
			pgxmock.NewRows(previewSnapshotTestCols).
				AddRow(generatedID, previewID, "baseline", "/", "blob://snap-1",
					1280, 720, json.RawMessage(`[]`), nil, now),
		)

	err = store.CreateSnapshot(context.Background(), snap)
	require.NoError(t, err)
	require.Equal(t, generatedID, snap.ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewStore_ListSnapshotsByPreview(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)
	now := time.Now()
	orgID := uuid.New()
	previewID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM preview_snapshots.+preview_instance_id").
		WithArgs(previewAnyArgs(2)...).
		WillReturnRows(
			pgxmock.NewRows(previewSnapshotTestCols).
				AddRow(uuid.New(), previewID, "baseline", "/", "blob://s1",
					1280, 720, json.RawMessage(`[]`), nil, now).
				AddRow(uuid.New(), previewID, "agent_change", "/page", "blob://s2",
					1280, 720, json.RawMessage(`[{"text":"err"}]`), json.RawMessage(`["a.ts"]`), now.Add(time.Minute)),
		)

	snaps, err := store.ListSnapshotsByPreview(context.Background(), orgID, previewID)
	require.NoError(t, err)
	require.Len(t, snaps, 2)
	require.Equal(t, models.PreviewSnapshotTriggerBaseline, snaps[0].Trigger)
	require.Equal(t, models.PreviewSnapshotTriggerAgentChange, snaps[1].Trigger)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewStore_CountAndDeleteSnapshots(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)
	orgID := uuid.New()
	previewID := uuid.New()

	// Count
	mock.ExpectQuery("SELECT COUNT.+FROM preview_snapshots").
		WithArgs(previewAnyArgs(2)...).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(15))

	count, err := store.CountSnapshotsByPreview(context.Background(), orgID, previewID)
	require.NoError(t, err)
	require.Equal(t, 15, count)

	// Delete oldest
	mock.ExpectQuery("DELETE FROM preview_snapshots WHERE id IN").
		WithArgs(previewAnyArgs(3)...).
		WillReturnRows(pgxmock.NewRows([]string{"blob_ref"}).
			AddRow("/tmp/blobs/snap1.png").
			AddRow("/tmp/blobs/snap2.png"))

	blobRefs, err := store.DeleteOldestSnapshots(context.Background(), orgID, previewID, 10)
	require.NoError(t, err)
	require.Equal(t, []string{"/tmp/blobs/snap1.png", "/tmp/blobs/snap2.png"}, blobRefs)
	require.NoError(t, mock.ExpectationsWereMet())
}

// =============================================================================
// Preview Log Tests
// =============================================================================

func TestPreviewStore_CreatePreviewLog(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)
	now := time.Now()
	generatedID := uuid.New()
	previewID := uuid.New()
	orgID := uuid.New()

	logEntry := &models.PreviewLog{
		PreviewInstanceID: previewID,
		OrgID:             orgID,
		Level:             "info",
		Step:              models.PreviewLogStepBuild,
		Message:           "Building container",
		Metadata:          json.RawMessage(`{"image":"node:20"}`),
	}

	mock.ExpectQuery("INSERT INTO preview_logs").
		WithArgs(previewAnyArgs(6)...).
		WillReturnRows(
			pgxmock.NewRows(previewLogTestCols).
				AddRow(generatedID, previewID, orgID, "info", "build", "Building container",
					json.RawMessage(`{"image":"node:20"}`), now),
		)

	err = store.CreatePreviewLog(context.Background(), logEntry)
	require.NoError(t, err)
	require.Equal(t, generatedID, logEntry.ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestPreviewStore_CreatePreviewLog_NilMetadataDefaultsToEmptyObject is the
// regression guard for the production bug where OnServiceFailed silently
// dropped service-exit logs because Metadata was unset → bound as SQL NULL →
// rejected by the NOT NULL constraint on preview_logs.metadata. The store
// must coerce a nil Metadata into a JSON empty-object so the column's DEFAULT
// '{}' isn't bypassed.
func TestPreviewStore_CreatePreviewLog_NilMetadataDefaultsToEmptyObject(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)
	now := time.Now()
	generatedID := uuid.New()
	previewID := uuid.New()
	orgID := uuid.New()

	logEntry := &models.PreviewLog{
		PreviewInstanceID: previewID,
		OrgID:             orgID,
		Level:             "error",
		Step:              models.PreviewLogStepStart,
		Message:           "service \"frontend\" failed",
		// Metadata intentionally unset — mirrors managerServiceObserver.OnServiceFailed.
	}

	mock.ExpectQuery("INSERT INTO preview_logs").
		WithArgs(
			previewID,
			orgID,
			"error",
			models.PreviewLogStepStart,
			"service \"frontend\" failed",
			json.RawMessage(`{}`),
		).
		WillReturnRows(
			pgxmock.NewRows(previewLogTestCols).
				AddRow(generatedID, previewID, orgID, "error", "start", "service \"frontend\" failed",
					json.RawMessage(`{}`), now),
		)

	require.NoError(t, store.CreatePreviewLog(context.Background(), logEntry))
	require.Equal(t, generatedID, logEntry.ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewStore_ListLogsByPreview(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		afterID *uuid.UUID
		nArgs   int
		pattern string
	}{
		{name: "without cursor", afterID: nil, nArgs: 2, pattern: "SELECT .+ FROM preview_logs.+preview_instance_id.+org_id.+ORDER BY created_at"},
		{name: "with cursor", afterID: func() *uuid.UUID { id := uuid.New(); return &id }(), nArgs: 3, pattern: `SELECT .+ FROM preview_logs.+preview_instance_id.+org_id.+\(created_at, id\) >`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err)
			defer mock.Close()

			store := NewPreviewStore(mock)
			now := time.Now()
			previewID := uuid.New()
			orgID := uuid.New()

			mock.ExpectQuery(tt.pattern).
				WithArgs(previewAnyArgs(tt.nArgs)...).
				WillReturnRows(
					pgxmock.NewRows(previewLogTestCols).
						AddRow(uuid.New(), previewID, orgID, "info", "build", "step 1",
							json.RawMessage(`{}`), now).
						AddRow(uuid.New(), previewID, orgID, "info", "start", "step 2",
							json.RawMessage(`{}`), now.Add(time.Second)),
				)

			logs, err := store.ListLogsByPreview(context.Background(), orgID, previewID, tt.afterID)
			require.NoError(t, err)
			require.Len(t, logs, 2)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestPreviewStore_ListLatestLogsByPreview_ReturnsTailInChronologicalOrder(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create pgx mock")
	defer mock.Close()

	store := NewPreviewStore(mock)
	now := time.Now()
	previewID := uuid.New()
	orgID := uuid.New()

	mock.ExpectQuery(`SELECT .+ FROM \(\s*SELECT .+ FROM preview_logs.+ORDER BY created_at DESC, id DESC\s+LIMIT 200\s*\) latest\s+ORDER BY created_at ASC, id ASC`).
		WithArgs(previewAnyArgs(2)...).
		WillReturnRows(
			pgxmock.NewRows(previewLogTestCols).
				AddRow(uuid.New(), previewID, orgID, "info", "start", "newer tail line",
					json.RawMessage(`{}`), now).
				AddRow(uuid.New(), previewID, orgID, "info", "start", "newest tail line",
					json.RawMessage(`{}`), now.Add(time.Second)),
		)

	logs, err := store.ListLatestLogsByPreview(context.Background(), orgID, previewID)
	require.NoError(t, err, "ListLatestLogsByPreview should return the latest preview log tail")
	require.Equal(t, []string{"newer tail line", "newest tail line"}, []string{logs[0].Message, logs[1].Message}, "tail logs should be returned in chronological display order")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

// =============================================================================
// Preview Access Session Tests
// =============================================================================

func TestPreviewStore_CreateAccessSession(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)
	now := time.Now()
	generatedID := uuid.New()
	orgID := uuid.New()
	userID := uuid.New()
	previewID := uuid.New()

	sess := &models.PreviewAccessSession{
		OrgID:             orgID,
		UserID:            userID,
		PreviewInstanceID: previewID,
		SessionTokenHash:  "sha256:token-hash",
		ExpiresAt:         now.Add(time.Hour),
	}

	mock.ExpectQuery("INSERT INTO preview_access_sessions").
		WithArgs(previewAnyArgs(5)...).
		WillReturnRows(
			pgxmock.NewRows(previewAccessSessionTestCols).
				AddRow(generatedID, orgID, userID, previewID,
					"sha256:token-hash", now, now.Add(time.Hour), nil, now, now),
		)

	err = store.CreateAccessSession(context.Background(), sess)
	require.NoError(t, err)
	require.Equal(t, generatedID, sess.ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewStore_GetAccessSessionByToken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface)
		expectErr bool
	}{
		{
			name: "returns session when found",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				now := time.Now()
				mock.ExpectQuery("SELECT .+ FROM preview_access_sessions.+org_id.+session_token_hash").
					WithArgs(previewAnyArgs(2)...).
					WillReturnRows(
						pgxmock.NewRows(previewAccessSessionTestCols).
							AddRow(uuid.New(), uuid.New(), uuid.New(), uuid.New(),
								"hash", now, now.Add(time.Hour), nil, now, now),
					)
			},
		},
		{
			name: "returns error when not found",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM preview_access_sessions.+org_id.+session_token_hash").
					WithArgs(previewAnyArgs(2)...).
					WillReturnRows(pgxmock.NewRows(previewAccessSessionTestCols))
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err)
			defer mock.Close()

			store := NewPreviewStore(mock)
			tt.setupMock(mock)

			sess, err := store.GetAccessSessionByToken(context.Background(), uuid.New(), "hash")
			if tt.expectErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, sess)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestPreviewStore_GetAccessSessionByTokenUnscoped(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface)
		expectErr bool
	}{
		{
			name: "returns session when found",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				now := time.Now()
				mock.ExpectQuery("SELECT .+ FROM preview_access_sessions.+session_token_hash").
					WithArgs(previewAnyArgs(1)...).
					WillReturnRows(
						pgxmock.NewRows(previewAccessSessionTestCols).
							AddRow(uuid.New(), uuid.New(), uuid.New(), uuid.New(),
								"hash", now, now.Add(time.Hour), nil, now, now),
					)
			},
		},
		{
			name: "returns error when not found",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM preview_access_sessions.+session_token_hash").
					WithArgs(previewAnyArgs(1)...).
					WillReturnRows(pgxmock.NewRows(previewAccessSessionTestCols))
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err)
			defer mock.Close()

			store := NewPreviewStore(mock)
			tt.setupMock(mock)

			sess, err := store.GetAccessSessionByTokenUnscoped(context.Background(), "hash")
			if tt.expectErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, sess)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestPreviewStore_RevokeAccessSession(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)

	mock.ExpectExec("UPDATE preview_access_sessions SET revoked_at").
		WithArgs(previewAnyArgs(2)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.RevokeAccessSession(context.Background(), uuid.New(), uuid.New())
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewStore_RevokeAllForPreview(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)

	mock.ExpectExec("UPDATE preview_access_sessions SET revoked_at.+preview_instance_id").
		WithArgs(previewAnyArgs(2)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 3))

	err = store.RevokeAllForPreview(context.Background(), uuid.New(), uuid.New())
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// =============================================================================
// Startup Cache Tests
// =============================================================================

func TestPreviewStore_UpsertStartupCache(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)
	now := time.Now()
	generatedID := uuid.New()
	orgID := uuid.New()
	repoID := uuid.New()

	entry := &models.PreviewStartupCache{
		OrgID:        orgID,
		RepoID:       repoID,
		SnapshotKey:  "lockfile:abc+commit:def+config:ghi",
		BlobPath:     "/cache/snap.tar.zst",
		SizeBytes:    1024 * 1024,
		WorkerNodeID: "worker-1",
	}

	mock.ExpectQuery("INSERT INTO preview_startup_cache").
		WithArgs(previewAnyArgs(6)...).
		WillReturnRows(
			pgxmock.NewRows(previewStartupCacheTestCols).
				AddRow(generatedID, orgID, repoID, "lockfile:abc+commit:def+config:ghi",
					"/cache/snap.tar.zst", int64(1024*1024), "worker-1", now, now),
		)

	err = store.UpsertStartupCache(context.Background(), entry)
	require.NoError(t, err)
	require.Equal(t, generatedID, entry.ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewStore_UpsertStartupCache_PreservesWorkerScopedConflictTarget(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)
	now := time.Now()
	orgID := uuid.New()
	repoID := uuid.New()

	entry := &models.PreviewStartupCache{
		OrgID:        orgID,
		RepoID:       repoID,
		SnapshotKey:  "key",
		BlobPath:     "/cache/snap.tar.zst",
		SizeBytes:    1024,
		WorkerNodeID: "worker-1",
	}

	mock.ExpectQuery(`INSERT INTO preview_startup_cache(.|\n)+ON CONFLICT \(org_id, repo_id, snapshot_key, worker_node_id\)`).
		WithArgs(previewAnyArgs(6)...).
		WillReturnRows(
			pgxmock.NewRows(previewStartupCacheTestCols).
				AddRow(uuid.New(), orgID, repoID, "key", "/cache/snap.tar.zst", int64(1024), "worker-1", now, now),
		)

	err = store.UpsertStartupCache(context.Background(), entry)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewStore_FindMatchingCache(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface)
		expectErr bool
	}{
		{
			name: "returns cache hit",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				now := time.Now()
				mock.ExpectQuery("SELECT .+ FROM preview_startup_cache.+snapshot_key.+worker_node_id").
					WithArgs(previewAnyArgs(4)...).
					WillReturnRows(
						pgxmock.NewRows(previewStartupCacheTestCols).
							AddRow(uuid.New(), uuid.New(), uuid.New(), "key",
								"/cache/snap.tar.zst", int64(1024), "w1", now, now),
					)
			},
		},
		{
			name: "returns error on cache miss",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM preview_startup_cache.+snapshot_key.+worker_node_id").
					WithArgs(previewAnyArgs(4)...).
					WillReturnRows(pgxmock.NewRows(previewStartupCacheTestCols))
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err)
			defer mock.Close()

			store := NewPreviewStore(mock)
			tt.setupMock(mock)

			entry, err := store.FindMatchingCache(context.Background(), uuid.New(), uuid.New(), "key", "w1")
			if tt.expectErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, entry)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestPreviewStore_FindWarmResumeStartupCacheForTarget_RequiresActiveWorker(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	store := NewPreviewStore(mock)
	now := time.Now()

	mock.ExpectQuery("JOIN preview_targets target[\\s\\S]+JOIN nodes n[\\s\\S]+n\\.status = 'active'").
		WithArgs(previewAnyArgs(2)...).
		WillReturnRows(
			pgxmock.NewRows(previewStartupCacheTestCols).
				AddRow(uuid.New(), uuid.New(), uuid.New(), "key", "/cache/snap.tar.zst", int64(1024), "worker-1", now, now),
		)

	cache, err := store.FindWarmResumeStartupCacheForTarget(context.Background(), uuid.New(), uuid.New())
	require.NoError(t, err, "FindWarmResumeStartupCacheForTarget should return active-worker cache entries")
	require.Equal(t, "worker-1", cache.WorkerNodeID, "warm resume cache should identify the worker holding the snapshot")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPreviewStore_DeleteCache(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)

	mock.ExpectExec("DELETE FROM preview_startup_cache WHERE id").
		WithArgs(previewAnyArgs(2)...).
		WillReturnResult(pgxmock.NewResult("DELETE", 1))

	err = store.DeleteCache(context.Background(), uuid.New(), uuid.New())
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// =============================================================================
// PR Preview State Tests
// =============================================================================

func TestPreviewStore_UpsertPRPreviewState(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)
	now := time.Now()
	generatedID := uuid.New()
	orgID := uuid.New()
	repoID := uuid.New()
	previewID := uuid.New()
	commentID := int64(42)

	state := &models.PRPreviewState{
		OrgID:                  orgID,
		RepoID:                 repoID,
		PRNumber:               123,
		GitHubCommentID:        &commentID,
		LastPreviewInstanceID:  &previewID,
		LastScreenshotBlobPath: "blob://screenshot",
		LastVisualDiffBlobPath: "blob://diff",
		BaseSnapshotKey:        "snap-key",
		Status:                 models.PRPreviewStatusRunning,
	}

	mock.ExpectQuery("INSERT INTO pr_preview_state").
		WithArgs(previewAnyArgs(9)...).
		WillReturnRows(
			pgxmock.NewRows(prPreviewStateTestCols).
				AddRow(generatedID, orgID, repoID, 123, &commentID,
					&previewID, "blob://screenshot", "blob://diff",
					"snap-key", "running", now, now),
		)

	err = store.UpsertPRPreviewState(context.Background(), state)
	require.NoError(t, err)
	require.Equal(t, generatedID, state.ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewStore_GetPRPreviewState(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)
	now := time.Now()
	orgID := uuid.New()
	repoID := uuid.New()
	stateID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM pr_preview_state.+org_id.+repo_id.+pr_number").
		WithArgs(previewAnyArgs(3)...).
		WillReturnRows(
			pgxmock.NewRows(prPreviewStateTestCols).
				AddRow(stateID, orgID, repoID, 42, nil,
					nil, "", "",
					"", "never_started", now, now),
		)

	state, err := store.GetPRPreviewState(context.Background(), orgID, repoID, 42)
	require.NoError(t, err)
	require.Equal(t, stateID, state.ID)
	require.Equal(t, 42, state.PRNumber)
	require.Equal(t, models.PRPreviewStatusNeverStarted, state.Status)
	require.NoError(t, mock.ExpectationsWereMet())
}

// =============================================================================
// Transactional Tests
// =============================================================================

func TestPreviewStore_StopPreviewWithRevocation(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE preview_instances SET status.+stopped_at.+updated_at").
		WithArgs(previewAnyArgs(3)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE preview_services SET").
		WithArgs(previewAnyArgs(5)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectExec("UPDATE preview_infrastructure SET").
		WithArgs(previewAnyArgs(5)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectExec("UPDATE preview_runtimes").
		WithArgs(previewAnyArgs(2)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE preview_access_sessions SET revoked_at").
		WithArgs(previewAnyArgs(2)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 2))
	mock.ExpectCommit()

	err = store.StopPreviewWithRevocation(context.Background(), uuid.New(), uuid.New())
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewStore_StopPreviewWithRevocationAndReason(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	store := NewPreviewStore(mock)

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE preview_instances SET status.+stopped_reason.+stopped_at.+updated_at").
		WithArgs(previewAnyArgs(4)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE preview_services SET").
		WithArgs(previewAnyArgs(5)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectExec("UPDATE preview_infrastructure SET").
		WithArgs(previewAnyArgs(5)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectExec("UPDATE preview_runtimes").
		WithArgs(previewAnyArgs(2)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE preview_access_sessions SET revoked_at").
		WithArgs(previewAnyArgs(2)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 2))
	mock.ExpectCommit()

	err = store.StopPreviewWithRevocationAndReason(context.Background(), uuid.New(), uuid.New(), models.PreviewStoppedReasonUser)
	require.NoError(t, err, "StopPreviewWithRevocationAndReason should stop and revoke atomically")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPreviewStore_UpdatePreviewStoppedReason(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	store := NewPreviewStore(mock)

	mock.ExpectExec("UPDATE preview_instances SET stopped_reason").
		WithArgs(previewAnyArgs(3)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.UpdatePreviewStoppedReason(context.Background(), uuid.New(), uuid.New(), models.PreviewStoppedReasonPRClosed)
	require.NoError(t, err, "UpdatePreviewStoppedReason should persist the supplied reason")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPreviewStore_StopPreviewWithRevocation_RollbackOnError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE preview_instances SET status.+stopped_at.+updated_at").
		WithArgs(previewAnyArgs(3)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0)) // not found
	mock.ExpectRollback()

	err = store.StopPreviewWithRevocation(context.Background(), uuid.New(), uuid.New())
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found or already stopped")
	require.NoError(t, mock.ExpectationsWereMet())
}

// =============================================================================
// WithTx Test
// =============================================================================

func TestPreviewStore_WithTx(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)

	mock.ExpectBegin()
	tx, err := store.Begin(context.Background())
	require.NoError(t, err)

	txStore := store.WithTx(tx)
	require.NotNil(t, txStore)

	mock.ExpectExec("UPDATE preview_instances SET last_accessed_at").
		WithArgs(previewAnyArgs(2)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = txStore.UpdatePreviewAccess(context.Background(), uuid.New(), uuid.New())
	require.NoError(t, err)

	mock.ExpectCommit()
	err = tx.Commit(context.Background())
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// =============================================================================
// Additional Preview Store Tests for Coverage
// =============================================================================

func TestPreviewStore_UpdatePreviewExpiry(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)

	mock.ExpectExec("UPDATE preview_instances SET expires_at").
		WithArgs(previewAnyArgs(3)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.UpdatePreviewExpiry(context.Background(), uuid.New(), uuid.New(), time.Now().Add(time.Hour))
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewStore_UpdateLastPath(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)

	mock.ExpectExec("UPDATE preview_instances SET last_path").
		WithArgs(previewAnyArgs(3)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.UpdateLastPath(context.Background(), uuid.New(), uuid.New(), "/dashboard")
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewStore_CountActivePreviewsByUser(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)

	mock.ExpectQuery("SELECT COUNT").
		WithArgs(previewAnyArgs(2)...).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(3))

	count, err := store.CountActivePreviewsByUser(context.Background(), uuid.New(), uuid.New())
	require.NoError(t, err)
	require.Equal(t, 3, count)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewStore_CountActivePreviewsByWorker(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)

	mock.ExpectQuery("SELECT COUNT[\\s\\S]+lease_expires_at > now\\(\\)").
		WithArgs(previewAnyArgs(1)...).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(2))

	count, err := store.CountActivePreviewsByWorker(context.Background(), "worker-1")
	require.NoError(t, err)
	require.Equal(t, 2, count)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewStore_CreatePreviewRuntime(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	store := NewPreviewStore(mock)
	now := time.Now().UTC()
	runtimeID := uuid.New()
	orgID := uuid.New()
	previewID := uuid.New()

	runtime := &models.PreviewRuntime{
		OrgID:             orgID,
		PreviewInstanceID: previewID,
		RuntimeEpoch:      1,
		WorkerNodeID:      "worker-1",
		EndpointURL:       "http://worker-1:8080",
		Status:            models.PreviewRuntimeStatusStarting,
		LeaseExpiresAt:    now.Add(time.Minute),
	}

	mock.ExpectQuery("INSERT INTO preview_runtimes").
		WithArgs(previewAnyArgs(9)...).
		WillReturnRows(pgxmock.NewRows(previewRuntimeTestCols).AddRow(newPreviewRuntimeRow(runtimeID, orgID, previewID, now)...))

	err = store.CreatePreviewRuntime(context.Background(), runtime)
	require.NoError(t, err, "CreatePreviewRuntime should insert a runtime scoped to org and preview")
	require.Equal(t, runtimeID, runtime.ID, "CreatePreviewRuntime should hydrate the generated runtime ID")
	require.Equal(t, models.PreviewRuntimeStatusReady, runtime.Status, "CreatePreviewRuntime should hydrate runtime status from the database")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPreviewStore_CreateNextPreviewRuntimeStopsPreviousEpoch(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	store := NewPreviewStore(mock)
	now := time.Now().UTC()
	runtimeID := uuid.New()
	orgID := uuid.New()
	previewID := uuid.New()

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE preview_runtimes").
		WithArgs(previewAnyArgs(2)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectQuery("SELECT COALESCE\\(MAX\\(runtime_epoch\\), 0\\) \\+ 1").
		WithArgs(previewAnyArgs(2)...).
		WillReturnRows(pgxmock.NewRows([]string{"runtime_epoch"}).AddRow(3))
	mock.ExpectQuery("INSERT INTO preview_runtimes").
		WithArgs(previewAnyArgs(9)...).
		WillReturnRows(pgxmock.NewRows(previewRuntimeTestCols).AddRow(newPreviewRuntimeRow(runtimeID, orgID, previewID, now)...))
	mock.ExpectCommit()

	runtime, err := store.CreateNextPreviewRuntime(context.Background(), orgID, previewID, "worker-2", "http://worker-2.internal")
	require.NoError(t, err, "CreateNextPreviewRuntime should atomically replace the active runtime")
	require.Equal(t, runtimeID, runtime.ID, "CreateNextPreviewRuntime should return the inserted runtime")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPreviewStore_ReassignPreviewWorkerUpdatesReservationAndRuntime(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	store := NewPreviewStore(mock)
	now := time.Now().UTC()
	runtimeID := uuid.New()
	orgID := uuid.New()
	previewID := uuid.New()

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE preview_instances SET worker_node_id").
		WithArgs(previewAnyArgs(3)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE preview_runtimes").
		WithArgs(previewAnyArgs(2)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectQuery("SELECT COALESCE\\(MAX\\(runtime_epoch\\), 0\\) \\+ 1").
		WithArgs(previewAnyArgs(2)...).
		WillReturnRows(pgxmock.NewRows([]string{"runtime_epoch"}).AddRow(2))
	mock.ExpectQuery("INSERT INTO preview_runtimes").
		WithArgs(previewAnyArgs(9)...).
		WillReturnRows(pgxmock.NewRows(previewRuntimeTestCols).AddRow(newPreviewRuntimeRow(runtimeID, orgID, previewID, now)...))
	mock.ExpectCommit()

	err = store.ReassignPreviewWorker(context.Background(), orgID, previewID, "worker-2", "http://worker-2.internal")
	require.NoError(t, err, "ReassignPreviewWorker should atomically update reservation ownership and runtime routing")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPreviewStore_GetActivePreviewRuntimeScopesByOrgAndLease(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	store := NewPreviewStore(mock)
	now := time.Now().UTC()
	runtimeID := uuid.New()
	orgID := uuid.New()
	previewID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM preview_runtimes").
		WithArgs(previewAnyArgs(2)...).
		WillReturnRows(pgxmock.NewRows(previewRuntimeTestCols).AddRow(newPreviewRuntimeRow(runtimeID, orgID, previewID, now)...))

	runtime, err := store.GetActivePreviewRuntime(context.Background(), orgID, previewID)
	require.NoError(t, err, "GetActivePreviewRuntime should return the live runtime")
	require.Equal(t, runtimeID, runtime.ID, "GetActivePreviewRuntime should return the matching runtime")
	require.Equal(t, "http://worker-runtime:8080", runtime.EndpointURL, "GetActivePreviewRuntime should return the endpoint from the runtime row")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPreviewStore_MarkActivePreviewRuntimesLostByWorkerMarksPreviewUnavailable(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	store := NewPreviewStore(mock)

	mock.ExpectExec("WITH lost AS[\\s\\S]+UPDATE preview_instances[\\s\\S]+preview_holding_container = FALSE").
		WithArgs(previewAnyArgs(3)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 2))

	updated, err := store.MarkActivePreviewRuntimesLostByWorker(context.Background(), "worker-1", "drain timeout")
	require.NoError(t, err, "MarkActivePreviewRuntimesLostByWorker should mark runtimes lost")
	require.Equal(t, int64(2), updated, "MarkActivePreviewRuntimesLostByWorker should return affected preview count")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPreviewStore_MarkActivePreviewRuntimesLostByWorkerRecordsUnavailableReason(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	store := NewPreviewStore(mock)

	mock.ExpectExec("WITH lost AS[\\s\\S]+unavailable_reason = @unavailable_reason[\\s\\S]+stopped_reason = CASE WHEN @unavailable_reason = 'deploy_drain_timeout' THEN 'drain'").
		WithArgs(previewAnyArgs(3)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	updated, err := store.MarkActivePreviewRuntimesLostByWorkerWithReason(context.Background(), "worker-1", "drain timeout", models.PreviewUnavailableReasonDeployDrainTimeout)
	require.NoError(t, err, "MarkActivePreviewRuntimesLostByWorkerWithReason should persist a deploy-specific reason")
	require.Equal(t, int64(1), updated, "MarkActivePreviewRuntimesLostByWorkerWithReason should report updated previews")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPreviewStore_MarkExpiredPreviewRuntimesLostClearsPreviewHold(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	store := NewPreviewStore(mock)

	mock.ExpectExec("WITH lost AS[\\s\\S]+UPDATE preview_instances[\\s\\S]+preview_holding_container = FALSE").
		WithArgs(previewAnyArgs(2)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	updated, err := store.MarkExpiredPreviewRuntimesLost(context.Background(), time.Now(), "preview runtime lease expired")
	require.NoError(t, err, "MarkExpiredPreviewRuntimesLost should mark expired runtimes lost")
	require.Equal(t, int64(1), updated, "MarkExpiredPreviewRuntimesLost should report updated previews")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPreviewStore_ListIdlePreviews(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)
	now := time.Now()
	previewID := uuid.New()
	sessionID := uuid.New()
	orgID := uuid.New()
	userID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM preview_instances.+session_sandbox_holders.+last_accessed_at").
		WithArgs(previewAnyArgs(1)...).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(newPreviewInstanceRow(previewID, sessionID, orgID, userID, now)...),
		)

	previews, err := store.ListIdlePreviews(context.Background(), now.Add(-15*time.Minute))
	require.NoError(t, err)
	require.Len(t, previews, 1)
	require.Equal(t, previewID, previews[0].ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewStore_ListIdlePreviewsForWorker(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)
	now := time.Now()
	previewID := uuid.New()
	sessionID := uuid.New()
	orgID := uuid.New()
	userID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM preview_instances.+worker_node_id = .+session_sandbox_holders.+last_accessed_at").
		WithArgs(previewAnyArgs(2)...).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(newPreviewInstanceRow(previewID, sessionID, orgID, userID, now)...),
		)

	previews, err := store.ListIdlePreviewsForWorker(context.Background(), "worker-1", now.Add(-15*time.Minute))
	require.NoError(t, err)
	require.Len(t, previews, 1)
	require.Equal(t, previewID, previews[0].ID)
	require.Equal(t, "worker-1", previews[0].WorkerNodeID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewStore_ListActivePreviewsRecycledBefore(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	store := NewPreviewStore(mock)
	now := time.Now()
	previewID := uuid.New()
	sessionID := uuid.New()
	orgID := uuid.New()
	userID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM preview_instances.+worker_node_id = @worker_node_id.+recycled_at < @recycled_before.+ORDER BY recycled_at").
		WithArgs(previewAnyArgs(2)...).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(newPreviewInstanceRow(previewID, sessionID, orgID, userID, now)...),
		)

	previews, err := store.ListActivePreviewsRecycledBefore(context.Background(), "worker-1", now.Add(-time.Hour))
	require.NoError(t, err, "ListActivePreviewsRecycledBefore should return matching previews")
	require.Len(t, previews, 1, "ListActivePreviewsRecycledBefore should return one matching preview")
	require.Equal(t, previewID, previews[0].ID, "ListActivePreviewsRecycledBefore should return the expected preview")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPreviewStore_UpdateServicePID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)

	mock.ExpectExec("UPDATE preview_services SET pid").
		WithArgs(previewAnyArgs(4)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.UpdateServicePID(context.Background(), uuid.New(), uuid.New(), "web", 12345)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewStore_UpdateInfraStatus(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)

	mock.ExpectExec("UPDATE preview_infrastructure SET status").
		WithArgs(previewAnyArgs(5)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.UpdateInfraStatus(context.Background(), uuid.New(), uuid.New(), "db", models.PreviewInfraStatusHealthy, "")
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewStore_UpdateAccessSessionActivity(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)

	mock.ExpectExec("UPDATE preview_access_sessions SET last_accessed_at").
		WithArgs(previewAnyArgs(2)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.UpdateAccessSessionActivity(context.Background(), uuid.New(), uuid.New())
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewStore_TouchCache(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)

	mock.ExpectExec("UPDATE preview_startup_cache SET last_used_at").
		WithArgs(previewAnyArgs(2)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.TouchCache(context.Background(), uuid.New(), uuid.New())
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewStore_ListCacheByWorker(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)

	mock.ExpectQuery("SELECT .+ FROM preview_startup_cache").
		WithArgs(previewAnyArgs(1)...).
		WillReturnRows(pgxmock.NewRows(previewStartupCacheTestCols))

	caches, err := store.ListCacheByWorker(context.Background(), "worker-1")
	require.NoError(t, err)
	require.Len(t, caches, 0)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewStore_UpdatePRPreviewStatus(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)

	mock.ExpectExec("UPDATE pr_preview_state SET status").
		WithArgs(previewAnyArgs(3)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.UpdatePRPreviewStatus(context.Background(), uuid.New(), uuid.New(), models.PRPreviewStatusRunning)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewStore_AcquirePreviewHold(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)
	sessionID := uuid.New()
	mock.ExpectQuery(`UPDATE preview_instances\s+SET preview_holding_container = TRUE`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"session_id"}).AddRow(sessionID))

	got, err := store.AcquirePreviewHold(context.Background(), uuid.New(), uuid.New())
	require.NoError(t, err)
	require.Equal(t, sessionID, got)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewStore_AcquirePreviewHold_QueryError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)
	mock.ExpectQuery(`UPDATE preview_instances`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(errors.New("boom"))

	_, err = store.AcquirePreviewHold(context.Background(), uuid.New(), uuid.New())
	require.Error(t, err)
	require.Contains(t, err.Error(), "acquire preview hold")
}

func TestPreviewStore_ReleasePreviewHold(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		containerID   string
		turnHolds     bool
		wantDestroyer bool
	}{
		{"destroys when turn does not hold", "container-1", false, true},
		{"keeps alive when turn still holds", "container-1", true, false},
		{"no-op when container already cleared", "", false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err)
			defer mock.Close()

			store := NewPreviewStore(mock)
			sessionID := uuid.New()
			mock.ExpectQuery(`WITH released AS \(\s*UPDATE preview_instances\s+SET preview_holding_container = FALSE`).
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(
					pgxmock.NewRows([]string{"session_id", "container_id", "turn_holds"}).
						AddRow(sessionID, tt.containerID, tt.turnHolds),
				)

			destroyNow, gotSession, cid, err := store.ReleasePreviewHold(context.Background(), uuid.New(), uuid.New())
			require.NoError(t, err)
			require.Equal(t, tt.wantDestroyer, destroyNow)
			require.Equal(t, sessionID, gotSession)
			require.Equal(t, tt.containerID, cid)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestPreviewStore_ReleasePreviewHold_QueryError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)
	mock.ExpectQuery(`WITH released AS`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(errors.New("boom"))

	_, _, _, err = store.ReleasePreviewHold(context.Background(), uuid.New(), uuid.New())
	require.Error(t, err)
	require.Contains(t, err.Error(), "release preview hold")
}

func TestPreviewStore_UpdatePreviewReservationConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		rows   int64
		wantOk bool
	}{
		{"updates reserved row", 1, true},
		{"no-op when status already changed", 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err)
			defer mock.Close()

			store := NewPreviewStore(mock)

			mock.ExpectExec(`UPDATE preview_instances\s+SET name = @name`).
				WithArgs(previewAnyArgs(10)...).
				WillReturnResult(pgxmock.NewResult("UPDATE", tt.rows))

			ok, err := store.UpdatePreviewReservationConfig(
				context.Background(),
				uuid.New(), uuid.New(),
				"my-preview", "web", "sha256:abc",
				512, 500, 10240,
				[]byte(`{"version":"3"}`), []byte(`{"id":"sandbox-1"}`),
			)
			require.NoError(t, err)
			require.Equal(t, tt.wantOk, ok)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestPreviewStore_UpdatePreviewReservationConfig_ExecError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)

	mock.ExpectExec(`UPDATE preview_instances`).
		WithArgs(previewAnyArgs(10)...).
		WillReturnError(errors.New("db down"))

	_, err = store.UpdatePreviewReservationConfig(
		context.Background(),
		uuid.New(), uuid.New(),
		"my-preview", "web", "sha256:abc",
		512, 500, 10240,
		nil, nil,
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "update preview reservation config")
}

func TestPreviewStore_DeleteExpiredDependencyCacheLocations(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	cutoff := time.Now().UTC()
	mock.ExpectExec("DELETE FROM preview_dependency_cache_locations").
		WithArgs(previewAnyArgs(1)...).
		WillReturnResult(pgxmock.NewResult("DELETE", 7))

	deleted, err := NewPreviewStore(mock).DeleteExpiredDependencyCacheLocations(context.Background(), cutoff)
	require.NoError(t, err, "DeleteExpiredDependencyCacheLocations should delete stale location hints")
	require.Equal(t, int64(7), deleted, "DeleteExpiredDependencyCacheLocations should report deleted rows")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

// branchPreviewSummaryTestCols returns the column list for BranchPreviewSummary
// mock rows, matching the SELECT column order produced by branchPreviewSummarySelect().
func branchPreviewSummaryTestCols() []string {
	return []string{
		"target_id", "preview_id", "repository_id", "repository_full_name", "branch", "commit_sha", "preview_config_name",
		"source_type", "source_id", "source_url", "status", "created_at", "sort_created_at", "expires_at", "stopped_at", "stopped_reason",
		"current_phase", "error", "resumable", "resume_estimate_seconds",
	}
}

// TestPreviewStore_ListBranchPreviewIndex_SingleQueryForMultipleRows verifies
// that listing branch previews issues exactly one SQL query regardless of result
// size (no N+1). The mock expects a single query and returns multiple rows; the
// test fails if the store issues additional queries per row.
func TestPreviewStore_ListBranchPreviewIndex_SingleQueryForMultipleRows(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	store := NewPreviewStore(mock)
	orgID := uuid.New()
	now := time.Now().UTC().Truncate(time.Millisecond)

	// Build 5 rows to prove no N+1 — all returned from a single query.
	resumeEstimate := 30
	rows := pgxmock.NewRows(branchPreviewSummaryTestCols())
	for i := range 5 {
		targetID := uuid.New()
		previewID := uuid.New()
		rows.AddRow(
			targetID, &previewID, uuid.New(), "acme/app",
			fmt.Sprintf("feature/branch-%d", i), "abc123", "",
			"pull_request", fmt.Sprintf("acme/app#%d@abc123", i+1), "https://github.com/acme/app/pull/1",
			"ready", now, now, (*time.Time)(nil), (*time.Time)(nil), "",
			"", "", true, &resumeEstimate,
		)
	}

	mock.ExpectQuery("SELECT .+").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(rows)

	results, err := store.ListBranchPreviewIndex(context.Background(), orgID, BranchPreviewIndexFilters{Limit: 5})
	require.NoError(t, err)
	require.Len(t, results, 5, "all 5 rows should be returned")

	// ExpectationsWereMet verifies exactly one query was issued — no N+1
	require.NoError(t, mock.ExpectationsWereMet(), "exactly one SQL query must be issued for any result size")
}

package handlers

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/reviewartifact"
	"github.com/assembledhq/143/internal/services/sandbox"
	"github.com/assembledhq/143/internal/services/storage"
	"github.com/assembledhq/143/internal/services/workspace"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// mockFileReader is a test implementation of sandbox.FileReader.
type mockFileReader struct {
	listDirFn     func(ctx context.Context, containerID, workDir, dirPath string) ([]sandbox.FileEntry, error)
	readFileFn    func(ctx context.Context, containerID, workDir, filePath string) (string, bool, error)
	readContextFn func(ctx context.Context, containerID, workDir, filePath string, line, above, below int) (sandbox.FileContextResult, error)
}

func (m *mockFileReader) ListDir(ctx context.Context, containerID, workDir, dirPath string) ([]sandbox.FileEntry, error) {
	if m.listDirFn != nil {
		return m.listDirFn(ctx, containerID, workDir, dirPath)
	}
	return nil, nil
}

func (m *mockFileReader) ReadFile(ctx context.Context, containerID, workDir, filePath string) (string, bool, error) {
	if m.readFileFn != nil {
		return m.readFileFn(ctx, containerID, workDir, filePath)
	}
	return "", false, nil
}

func (m *mockFileReader) ReadFileContext(ctx context.Context, containerID, workDir, filePath string, line, above, below int) (sandbox.FileContextResult, error) {
	if m.readContextFn != nil {
		return m.readContextFn(ctx, containerID, workDir, filePath, line, above, below)
	}
	return sandbox.FileContextResult{}, nil
}

func newTestSessionFileHandler(t *testing.T, mock pgxmock.PgxPoolIface, fr sandbox.FileReader) *SessionFileHandler {
	t.Helper()
	return newTestSessionFileHandlerWithCache(t, mock, fr, nil)
}

func newTestSessionFileHandlerWithCache(t *testing.T, mock pgxmock.PgxPoolIface, fr sandbox.FileReader, cache *workspace.SnapshotCache) *SessionFileHandler {
	t.Helper()
	return newTestSessionFileHandlerWithArtifacts(t, mock, fr, cache, nil)
}

func newTestSessionFileHandlerWithArtifacts(t *testing.T, mock pgxmock.PgxPoolIface, fr sandbox.FileReader, cache *workspace.SnapshotCache, artifactReader *reviewartifact.CachedReader) *SessionFileHandler {
	t.Helper()
	// Pass a nil repoStore by default — most tests don't attach a repo to
	// the session, so resolveSandboxWorkDir falls back to /workspace and
	// existing tar-prefix expectations remain valid. Tests that exercise
	// the repo-attached path build their own handler with a mock repoStore.
	return NewSessionFileHandler(
		db.NewSessionStore(mock),
		nil,
		fr,
		cache,
		artifactReader,
		zerolog.Nop(),
	)
}

// newTestSessionFileHandlerWithRepoStore wires a real RepositoryStore (over
// the same pgxmock pool) for tests that exercise the repo-attached
// WorkDir path. The caller is responsible for queuing repository row
// expectations on the mock.
func newTestSessionFileHandlerWithRepoStore(t *testing.T, mock pgxmock.PgxPoolIface, fr sandbox.FileReader, cache *workspace.SnapshotCache) *SessionFileHandler {
	t.Helper()
	return NewSessionFileHandler(
		db.NewSessionStore(mock),
		db.NewRepositoryStore(mock),
		fr,
		cache,
		nil,
		zerolog.Nop(),
	)
}

func withSessionRoute(handler http.HandlerFunc) http.Handler {
	r := chi.NewRouter()
	r.Get("/api/v1/sessions/{id}/files", handler)
	r.Get("/api/v1/sessions/{id}/files/content", handler)
	r.Get("/api/v1/sessions/{id}/files/context", handler)
	return r
}

// sessionColumnsForFiles matches the session store's select columns.
// The existing sessionColumns var from sessions_test.go includes created_at as the last
// column, but the actual SQL also returns diff_stats and diff_history before created_at.
// We redefine the full set here to avoid coupling.
var sessionColumnsForFiles = []string{
	"id", "primary_issue_id", "org_id", "origin", "interaction_mode", "validation_policy", "agent_type", "status", "autonomy_level", "token_mode",
	"complexity_tier",
	"container_id", "worker_node_id", "turn_holding_container", "started_at", "completed_at", "token_usage",
	"failure_explanation", "failure_category", "failure_next_steps", "failure_retry_advised",
	"parent_session_id", "revision_context", "error", "result_summary", "diff",
	"pm_plan_id", "title", "pm_approach", "pm_reasoning",
	"project_task_id", "model_override", "reasoning_effort", "triggered_by_user_id",
	"agent_session_id", "current_turn", "last_activity_at", "sandbox_state", "workspace_generation", "snapshot_key", "pending_snapshot_key", "pending_snapshot_set_at",
	"runtime_soft_deadline_at", "runtime_hard_deadline_at", "runtime_last_progress_at", "runtime_last_progress_type", "runtime_last_progress_strength",
	"runtime_extension_count", "runtime_extension_seconds", "runtime_stop_reason", "runtime_graceful_stop_at",
	"checkpointed_at", "checkpoint_kind", "checkpoint_capability", "checkpoint_size_bytes", "checkpoint_error",
	"recovery_state", "recovery_queued_at", "recovery_started_at", "recovery_attempt_count",
	"target_branch", "working_branch", "base_commit_sha", "repository_id", "diff_stats", "diff_history", "input_manifest", "archived_at", "archived_by_user_id", "automation_run_id", "pr_creation_state", "pr_creation_error", "pr_push_state", "pr_push_error", "branch_creation_state", "branch_creation_error", "branch_url", "diff_collected_at", "latest_diff_snapshot_id", "workspace_revision", "workspace_revision_updated_at",
	"has_unpushed_changes",
	"linear_private", "linear_state_sync_disabled", "linear_identifier_hint", "linear_prepare_state",
	"deleted_at", "capability_snapshot", "git_identity_source", "git_identity_user_id", "created_at",
}

func sessionFileTestRow(values ...interface{}) []interface{} {
	// Legacy fixtures predate four batches of column additions. Inject all
	// of them in their landed positions so callers don't have to update
	// every row literal:
	//   - 3 policy defaults (origin/interaction_mode/validation_policy) at positions 3-5
	//   - workspace_generation immediately after sandbox_state
	//   - 2 nils right after snapshot_key (pending_snapshot_key, pending_snapshot_set_at)
	//   - 4 linear_* defaults (migration 000103) just before deleted_at
	//   - capability_snapshot plus 2 git_identity nils between deleted_at and created_at
	// Callers already supply has_unpushed_changes, so this helper only backfills
	// policy, workspace_generation, pending_snapshot_*, linear_*, capability_snapshot,
	// and git_identity_*.
	if len(values) == len(sessionColumnsForFiles)-3-1-2-4-1-2 {
		row := make([]interface{}, 0, len(values)+13)
		row = append(row, values[:3]...)
		row = append(row, "", "", "")
		// Legacy values[3..38] = agent_type through snapshot_key (36 values).
		row = append(row, values[3:39]...)
		row = append(row, nil, nil) // pending_snapshot_key, pending_snapshot_set_at
		// Legacy values[39..len-3] = runtime through latest_diff_snapshot_id.
		row = append(row, values[39:len(values)-2]...)
		row = append(row, false, false, (*string)(nil), string(models.LinearPrepareStateNone))
		row = append(row, values[len(values)-2]) // deleted_at
		row = append(row, nil)                   // capability_snapshot
		row = append(row, nil, nil)              // git_identity_source, git_identity_user_id
		row = append(row, values[len(values)-1]) // created_at
		row = append(row[:38], append([]interface{}{int64(0)}, row[38:]...)...)
		return row
	}
	if len(values) == len(sessionColumnsForFiles)-1 {
		row := make([]interface{}, 0, len(values)+1)
		row = append(row, values[:38]...)
		row = append(row, int64(0))
		row = append(row, values[38:]...)
		return row
	}
	return values
}

func setupSessionMock(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID, containerID *string) {
	setupSessionMockWithSnapshot(mock, orgID, sessionID, containerID, nil)
}

func setupSessionMockWithSnapshot(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID, containerID *string, snapshotKey *string) {
	setupSessionMockFull(mock, orgID, sessionID, containerID, snapshotKey, nil)
}

// setupSessionMockFull sets up the session SELECT expectation with full
// control over the columns the snapshot fallback path cares about. Pass
// nil for repositoryID to model a session without an attached repo
// (handler resolves WorkDir to /workspace).
func setupSessionMockFull(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID, containerID *string, snapshotKey *string, repositoryID *uuid.UUID) {
	now := time.Now()
	issueID := uuid.New()
	mock.ExpectQuery("SELECT .+ FROM sessions WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionColumnsForFiles).AddRow(sessionFileTestRow(
				sessionID, &issueID, orgID, "claude_code", "running", "supervised", "standard",
				nil,
				containerID, nil, false, &now, nil, nil, // container_id, worker_node_id, turn_holding_container, started_at, completed_at, token_usage
				nil, nil, nil, false, // failure fields
				nil, nil, nil, nil, nil, // parent_session_id through diff
				nil, nil, nil, nil, // pm_plan_id through pm_reasoning
				nil, nil, nil, nil, // project_task_id, model_override, reasoning_effort, triggered_by_user_id
				nil, 0, now, "running", snapshotKey, // agent_session_id, current_turn, last_activity_at, sandbox_state, snapshot_key
				nil,                         // runtime_soft_deadline_at
				nil,                         // runtime_hard_deadline_at
				nil,                         // runtime_last_progress_at
				"",                          // runtime_last_progress_type
				"",                          // runtime_last_progress_strength
				0,                           // runtime_extension_count
				0,                           // runtime_extension_seconds
				"",                          // runtime_stop_reason
				nil,                         // runtime_graceful_stop_at
				nil,                         // checkpointed_at
				"",                          // checkpoint_kind
				"",                          // checkpoint_capability
				int64(0),                    // checkpoint_size_bytes
				nil,                         // checkpoint_error
				"",                          // recovery_state
				nil,                         // recovery_queued_at
				nil,                         // recovery_started_at
				0,                           // recovery_attempt_count
				nil, nil, nil, repositoryID, // target_branch, working_branch, base_commit_sha, repository_id
				nil, nil, // diff_stats, diff_history
				nil,      // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil,            // automation_run_id
				"idle",         // pr_creation_state
				(*string)(nil), // pr_creation_error
				"idle",         // pr_push_state
				(*string)(nil), // pr_push_error
				"idle",         // branch_creation_state
				(*string)(nil), // branch_creation_error
				(*string)(nil), // branch_url
				nil,            // diff_collected_at
				nil,            // latest_diff_snapshot_id
				int64(0),       // workspace_revision
				now,            // workspace_revision_updated_at
				false,          // has_unpushed_changes
				nil,            // deleted_at
				now,            // created_at
			)...),
		)
}

func TestSessionFileHandler_ListFiles(t *testing.T) {
	t.Parallel()

	containerID := "container-abc123"

	tests := []struct {
		name         string
		path         string
		setupMock    func(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID)
		fileReader   *mockFileReader
		expectedCode int
		checkBody    func(t *testing.T, body []byte)
	}{
		{
			name: "lists root directory successfully",
			path: "",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID) {
				setupSessionMock(mock, orgID, sessionID, &containerID)
			},
			fileReader: &mockFileReader{
				listDirFn: func(_ context.Context, cid, _, dirPath string) ([]sandbox.FileEntry, error) {
					require.Equal(t, containerID, cid)
					return []sandbox.FileEntry{
						{Path: "src", Type: "dir", Size: 4096},
						{Path: "main.go", Type: "file", Size: 1234},
					}, nil
				},
			},
			expectedCode: http.StatusOK,
			checkBody: func(t *testing.T, body []byte) {
				var resp models.ListResponse[sandbox.FileEntry]
				require.NoError(t, json.Unmarshal(body, &resp))
				require.Len(t, resp.Data, 2)
				require.Equal(t, "src", resp.Data[0].Path)
				require.Equal(t, "dir", resp.Data[0].Type)
				require.Equal(t, "main.go", resp.Data[1].Path)
				require.Equal(t, "file", resp.Data[1].Type)
			},
		},
		{
			name: "lists subdirectory",
			path: "src/components",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID) {
				setupSessionMock(mock, orgID, sessionID, &containerID)
			},
			fileReader: &mockFileReader{
				listDirFn: func(_ context.Context, _, _, dirPath string) ([]sandbox.FileEntry, error) {
					return []sandbox.FileEntry{
						{Path: "src/components/app.tsx", Type: "file", Size: 500},
					}, nil
				},
			},
			expectedCode: http.StatusOK,
		},
		{
			name: "rejects traversal path",
			path: "../../etc/passwd",
			// Path validation runs before the session lookup, so a bad
			// path returns 400 without touching the database.
			setupMock:    func(_ pgxmock.PgxPoolIface, _, _ uuid.UUID) {},
			fileReader:   &mockFileReader{},
			expectedCode: http.StatusBadRequest,
		},
		{
			name: "returns conflict when no container",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID) {
				setupSessionMock(mock, orgID, sessionID, nil)
			},
			fileReader:   &mockFileReader{},
			expectedCode: http.StatusConflict,
		},
		{
			name: "returns not found when directory doesn't exist",
			path: "nonexistent",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID) {
				setupSessionMock(mock, orgID, sessionID, &containerID)
			},
			fileReader: &mockFileReader{
				listDirFn: func(_ context.Context, _, _, _ string) ([]sandbox.FileEntry, error) {
					return nil, fmt.Errorf("directory not found")
				},
			},
			expectedCode: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err)
			defer mock.Close()

			orgID := uuid.New()
			sessionID := uuid.New()
			handler := newTestSessionFileHandler(t, mock, tt.fileReader)

			tt.setupMock(mock, orgID, sessionID)

			url := fmt.Sprintf("/api/v1/sessions/%s/files", sessionID)
			if tt.path != "" {
				url += "?path=" + tt.path
			}
			req := httptest.NewRequest(http.MethodGet, url, nil)
			ctx := middleware.WithOrgID(req.Context(), orgID)
			req = req.WithContext(ctx)

			w := httptest.NewRecorder()
			withSessionRoute(handler.ListFiles).ServeHTTP(w, req)

			require.Equal(t, tt.expectedCode, w.Code)

			if tt.checkBody != nil {
				tt.checkBody(t, w.Body.Bytes())
			}

			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestSessionFileHandler_GetFileContent(t *testing.T) {
	t.Parallel()

	containerID := "container-abc123"

	tests := []struct {
		name         string
		path         string
		setupMock    func(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID)
		fileReader   *mockFileReader
		expectedCode int
		checkBody    func(t *testing.T, body []byte)
	}{
		{
			name: "reads file content successfully",
			path: "src/main.go",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID) {
				setupSessionMock(mock, orgID, sessionID, &containerID)
			},
			fileReader: &mockFileReader{
				readFileFn: func(_ context.Context, _, _, filePath string) (string, bool, error) {
					return "package main\n\nfunc main() {}\n", false, nil
				},
			},
			expectedCode: http.StatusOK,
			checkBody: func(t *testing.T, body []byte) {
				var resp models.SingleResponse[sandbox.FileContent]
				require.NoError(t, json.Unmarshal(body, &resp))
				require.Equal(t, "src/main.go", resp.Data.Path)
				require.Equal(t, "go", resp.Data.Language)
				require.Contains(t, resp.Data.Content, "package main")
			},
		},
		{
			name: "requires path parameter",
			path: "",
			// Path validation runs before the session lookup, so a missing
			// path returns 400 without touching the database.
			setupMock:    func(_ pgxmock.PgxPoolIface, _, _ uuid.UUID) {},
			fileReader:   &mockFileReader{},
			expectedCode: http.StatusBadRequest,
		},
		{
			name: "returns 404 for nonexistent file",
			path: "nonexistent.go",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID) {
				setupSessionMock(mock, orgID, sessionID, &containerID)
			},
			fileReader: &mockFileReader{
				readFileFn: func(_ context.Context, _, _, _ string) (string, bool, error) {
					return "", false, fmt.Errorf("file not found")
				},
			},
			expectedCode: http.StatusNotFound,
		},
		{
			name: "infers language from extension",
			path: "components/Button.tsx",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID) {
				setupSessionMock(mock, orgID, sessionID, &containerID)
			},
			fileReader: &mockFileReader{
				readFileFn: func(_ context.Context, _, _, _ string) (string, bool, error) {
					return "export function Button() {}", false, nil
				},
			},
			expectedCode: http.StatusOK,
			checkBody: func(t *testing.T, body []byte) {
				var resp models.SingleResponse[sandbox.FileContent]
				require.NoError(t, json.Unmarshal(body, &resp))
				require.Equal(t, "tsx", resp.Data.Language)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err)
			defer mock.Close()

			orgID := uuid.New()
			sessionID := uuid.New()
			handler := newTestSessionFileHandler(t, mock, tt.fileReader)

			tt.setupMock(mock, orgID, sessionID)

			url := fmt.Sprintf("/api/v1/sessions/%s/files/content", sessionID)
			if tt.path != "" {
				url += "?path=" + tt.path
			}
			req := httptest.NewRequest(http.MethodGet, url, nil)
			ctx := middleware.WithOrgID(req.Context(), orgID)
			req = req.WithContext(ctx)

			w := httptest.NewRecorder()
			withSessionRoute(handler.GetFileContent).ServeHTTP(w, req)

			require.Equal(t, tt.expectedCode, w.Code)

			if tt.checkBody != nil {
				tt.checkBody(t, w.Body.Bytes())
			}

			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestSessionFileHandler_GetFileContext(t *testing.T) {
	t.Parallel()

	containerID := "container-abc123"

	tests := []struct {
		name         string
		queryParams  string
		setupMock    func(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID)
		fileReader   *mockFileReader
		expectedCode int
		checkBody    func(t *testing.T, body []byte)
	}{
		{
			name:        "reads context lines successfully",
			queryParams: "path=src/main.go&line=10&above=3&below=3",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID) {
				setupSessionMock(mock, orgID, sessionID, &containerID)
			},
			fileReader: &mockFileReader{
				readContextFn: func(_ context.Context, _, _, _ string, line, above, below int) (sandbox.FileContextResult, error) {
					require.Equal(t, 10, line)
					require.Equal(t, 3, above)
					require.Equal(t, 3, below)
					return sandbox.FileContextResult{
						Lines: []sandbox.FileLine{
							{Number: 7, Content: "line 7"},
							{Number: 8, Content: "line 8"},
							{Number: 9, Content: "line 9"},
							{Number: 10, Content: "line 10"},
							{Number: 11, Content: "line 11"},
							{Number: 12, Content: "line 12"},
							{Number: 13, Content: "line 13"},
						},
						StartLine:    7,
						EndLine:      13,
						HasMoreAbove: true,
						HasMoreBelow: true,
						TotalLines:   20,
					}, nil
				},
			},
			expectedCode: http.StatusOK,
			checkBody: func(t *testing.T, body []byte) {
				type contextResponse struct {
					Lines        []sandbox.FileLine `json:"lines"`
					StartLine    int                `json:"start_line"`
					EndLine      int                `json:"end_line"`
					HasMoreAbove bool               `json:"has_more_above"`
					HasMoreBelow bool               `json:"has_more_below"`
					TotalLines   int                `json:"total_lines"`
				}
				var resp models.SingleResponse[contextResponse]
				require.NoError(t, json.Unmarshal(body, &resp), "response body should unmarshal")
				require.Len(t, resp.Data.Lines, 7, "handler should return all requested context lines")
				require.Equal(t, 7, resp.Data.Lines[0].Number, "response should include the first requested line number")
				require.Equal(t, 13, resp.Data.Lines[6].Number, "response should include the last requested line number")
				require.Equal(t, 7, resp.Data.StartLine, "response should report the returned start line")
				require.Equal(t, 13, resp.Data.EndLine, "response should report the returned end line")
				require.True(t, resp.Data.HasMoreAbove, "response should indicate additional lines exist above the window")
				require.True(t, resp.Data.HasMoreBelow, "response should indicate additional lines exist below the window")
				require.Equal(t, 20, resp.Data.TotalLines, "response should report the total line count")
			},
		},
		{
			name:        "requires path parameter",
			queryParams: "line=10",
			// Path validation runs before the session lookup, so a missing
			// path returns 400 without touching the database.
			setupMock:    func(_ pgxmock.PgxPoolIface, _, _ uuid.UUID) {},
			fileReader:   &mockFileReader{},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:        "caps above/below at 100",
			queryParams: "path=main.go&line=50&above=200&below=200",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID) {
				setupSessionMock(mock, orgID, sessionID, &containerID)
			},
			fileReader: &mockFileReader{
				readContextFn: func(_ context.Context, _, _, _ string, _, above, below int) (sandbox.FileContextResult, error) {
					require.Equal(t, 100, above, "above should be capped at 100")
					require.Equal(t, 100, below, "below should be capped at 100")
					return sandbox.FileContextResult{}, nil
				},
			},
			expectedCode: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err)
			defer mock.Close()

			orgID := uuid.New()
			sessionID := uuid.New()
			handler := newTestSessionFileHandler(t, mock, tt.fileReader)

			tt.setupMock(mock, orgID, sessionID)

			url := fmt.Sprintf("/api/v1/sessions/%s/files/context?%s", sessionID, tt.queryParams)
			req := httptest.NewRequest(http.MethodGet, url, nil)
			ctx := middleware.WithOrgID(req.Context(), orgID)
			req = req.WithContext(ctx)

			w := httptest.NewRecorder()
			withSessionRoute(handler.GetFileContext).ServeHTTP(w, req)

			require.Equal(t, tt.expectedCode, w.Code)

			if tt.checkBody != nil {
				tt.checkBody(t, w.Body.Bytes())
			}

			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestSessionFileHandler_GetFileContextServesReviewArtifact(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()
	artifactKey := "review-artifacts/test/session/artifact.json.gz"
	artifactVersion := 1
	store := stageReviewArtifactForHandlerTest(t, artifactKey, reviewartifact.Artifact{
		Version: reviewartifact.Version,
		Files: map[string]reviewartifact.File{
			"src/main.go": {
				Path:       "src/main.go",
				Content:    "package main\n\nfunc main() {}\n",
				SizeBytes:  int64(len("package main\n\nfunc main() {}\n")),
				TotalLines: 3,
			},
		},
	})
	artifactReader := reviewartifact.NewCachedReader(store, 128*1024*1024)
	handler := newTestSessionFileHandlerWithArtifacts(t, mock, &mockFileReader{
		readContextFn: func(context.Context, string, string, string, int, int, int) (sandbox.FileContextResult, error) {
			t.Fatalf("workspace reader should not be called when review artifact contains the file")
			return sandbox.FileContextResult{}, nil
		},
	}, nil, artifactReader)

	mock.ExpectQuery("SELECT review_artifact_key, review_artifact_version").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"review_artifact_key", "review_artifact_version"}).AddRow(&artifactKey, &artifactVersion))

	url := fmt.Sprintf("/api/v1/sessions/%s/files/context?path=src/main.go&line=2&above=1&below=1", sessionID)
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	withSessionRoute(handler.GetFileContext).ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "handler should serve context from the review artifact")
	var resp models.SingleResponse[sandbox.FileContextResult]
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp), "response should decode")
	require.Equal(t, []sandbox.FileLine{
		{Number: 1, Content: "package main"},
		{Number: 2, Content: ""},
		{Number: 3, Content: "func main() {}"},
	}, resp.Data.Lines, "artifact context should return the requested line window")
	require.Equal(t, 3, resp.Data.TotalLines, "artifact context should include total line count")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionFileHandler_GetFileContextFallsBackWhenReviewArtifactMisses(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()
	containerID := "container-abc123"
	artifactKey := "review-artifacts/test/session/artifact.json.gz"
	artifactVersion := 1
	store := stageReviewArtifactForHandlerTest(t, artifactKey, reviewartifact.Artifact{
		Version: reviewartifact.Version,
		Files: map[string]reviewartifact.File{
			"other.go": {
				Path:       "other.go",
				Content:    "package other\n",
				SizeBytes:  int64(len("package other\n")),
				TotalLines: 1,
			},
		},
	})
	artifactReader := reviewartifact.NewCachedReader(store, 128*1024*1024)
	handler := newTestSessionFileHandlerWithArtifacts(t, mock, &mockFileReader{
		readContextFn: func(_ context.Context, gotContainerID, workDir, filePath string, line, above, below int) (sandbox.FileContextResult, error) {
			require.Equal(t, containerID, gotContainerID, "fallback should use the live container")
			require.Equal(t, "/workspace", workDir, "fallback should use the resolved workspace")
			require.Equal(t, "src/main.go", filePath, "fallback should read the requested path")
			require.Equal(t, 7, line, "fallback should preserve requested line")
			require.Equal(t, 2, above, "fallback should preserve requested above")
			require.Equal(t, 2, below, "fallback should preserve requested below")
			return sandbox.FileContextResult{
				Lines:      []sandbox.FileLine{{Number: 7, Content: "from workspace"}},
				StartLine:  7,
				EndLine:    7,
				TotalLines: 10,
			}, nil
		},
	}, nil, artifactReader)

	mock.ExpectQuery("SELECT review_artifact_key, review_artifact_version").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"review_artifact_key", "review_artifact_version"}).AddRow(&artifactKey, &artifactVersion))
	setupSessionMock(mock, orgID, sessionID, &containerID)

	url := fmt.Sprintf("/api/v1/sessions/%s/files/context?path=src/main.go&line=7&above=2&below=2", sessionID)
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	withSessionRoute(handler.GetFileContext).ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "handler should fall back to the workspace reader")
	var resp models.SingleResponse[sandbox.FileContextResult]
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp), "response should decode")
	require.Equal(t, []sandbox.FileLine{{Number: 7, Content: "from workspace"}}, resp.Data.Lines, "fallback response should come from workspace reader")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

// stageSnapshotForHandlerTest builds a tiny tar.gz snapshot under
// tarPrefix/, stores it via FileSnapshotStore, and returns a SnapshotCache
// pointing at it. The test temp dirs are reaped automatically.
func stageSnapshotForHandlerTest(t *testing.T, key, tarPrefix string, files map[string]string) *workspace.SnapshotCache {
	t.Helper()

	// Build the tar.gz payload in-memory.
	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)
	for name, content := range files {
		hdr := &tar.Header{
			Name:     tarPrefix + "/" + name,
			Mode:     0o644,
			Size:     int64(len(content)),
			Typeflag: tar.TypeReg,
		}
		require.NoError(t, tw.WriteHeader(hdr))
		_, err := tw.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gzw.Close())

	storeDir := t.TempDir()
	store := storage.NewFileSnapshotStore(storeDir)
	require.NoError(t, store.Save(context.Background(), key, &buf))

	cache, err := workspace.NewSnapshotCache(store, t.TempDir(), 0, zerolog.Nop())
	require.NoError(t, err)
	return cache
}

func stageReviewArtifactForHandlerTest(t *testing.T, key string, artifact reviewartifact.Artifact) storage.SnapshotStore {
	t.Helper()
	store := storage.NewFileSnapshotStore(t.TempDir())
	var buf bytes.Buffer
	_, err := reviewartifact.Encode(&buf, artifact)
	require.NoError(t, err, "review artifact should encode")
	require.NoError(t, store.Save(context.Background(), key, bytes.NewReader(buf.Bytes())), "review artifact should be staged")
	return store
}

func TestSessionFileHandler_SnapshotFallback(t *testing.T) {
	t.Parallel()

	const snapshotKey = "snapshots/o/s/workspace.tar.zst"
	const fileBody = "package main\n\nfunc main() {\n\tprintln(\"hi\")\n}\n"

	t.Run("GetFileContext serves from snapshot when no container is attached", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		sessionID := uuid.New()
		key := snapshotKey
		setupSessionMockWithSnapshot(mock, orgID, sessionID, nil, &key)

		cache := stageSnapshotForHandlerTest(t, key, "workspace", map[string]string{
			"src/main.go": fileBody,
		})

		// liveFileReader should never be consulted on this path; pass a
		// noop reader so we'd notice if the dispatch went the wrong way.
		handler := newTestSessionFileHandlerWithCache(t, mock, &mockFileReader{}, cache)

		url := fmt.Sprintf("/api/v1/sessions/%s/files/context?path=src/main.go&line=3&above=1&below=1", sessionID)
		req := httptest.NewRequest(http.MethodGet, url, nil)
		req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))

		w := httptest.NewRecorder()
		withSessionRoute(handler.GetFileContext).ServeHTTP(w, req)

		require.Equal(t, http.StatusOK, w.Code, "snapshot fallback should produce 200, got body: %s", w.Body.String())

		var resp models.SingleResponse[sandbox.FileContextResult]
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		require.Equal(t, 2, resp.Data.StartLine, "context window should center on the requested line")
		require.Equal(t, 4, resp.Data.EndLine)
		require.Equal(t, 5, resp.Data.TotalLines, "snapshot reader must return total line count for the diff UI")
		require.True(t, resp.Data.HasMoreAbove)
		require.True(t, resp.Data.HasMoreBelow)
		require.Len(t, resp.Data.Lines, 3)

		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("prefers the live container when both a container and snapshot are available", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "pgxmock pool should be created")
		defer mock.Close()

		orgID := uuid.New()
		sessionID := uuid.New()
		containerID := "container-live"
		key := snapshotKey
		setupSessionMockWithSnapshot(mock, orgID, sessionID, &containerID, &key)

		cache := stageSnapshotForHandlerTest(t, key, "workspace", map[string]string{
			"src/main.go": "snapshot-body",
		})

		handler := newTestSessionFileHandlerWithCache(t, mock, &mockFileReader{
			readContextFn: func(_ context.Context, gotContainerID, _, filePath string, _, _, _ int) (sandbox.FileContextResult, error) {
				require.Equal(t, containerID, gotContainerID, "file browsing should prefer the live container when it exists")
				require.Equal(t, "src/main.go", filePath, "file browsing should request the same path from the live container")
				return sandbox.FileContextResult{
					Lines:      []sandbox.FileLine{{Number: 1, Content: "live-body"}},
					StartLine:  1,
					EndLine:    1,
					TotalLines: 1,
				}, nil
			},
		}, cache)

		url := fmt.Sprintf("/api/v1/sessions/%s/files/context?path=src/main.go&line=1", sessionID)
		req := httptest.NewRequest(http.MethodGet, url, nil)
		req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))

		w := httptest.NewRecorder()
		withSessionRoute(handler.GetFileContext).ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code, "live container reads should win over stale snapshots")

		var resp models.SingleResponse[sandbox.FileContextResult]
		err = json.Unmarshal(w.Body.Bytes(), &resp)
		require.NoError(t, err, "response should decode")
		require.Equal(t, []sandbox.FileLine{{Number: 1, Content: "live-body"}}, resp.Data.Lines, "file browsing should return live workspace content when both sources exist")
		require.NoError(t, mock.ExpectationsWereMet(), "database expectations should be met")
	})

	t.Run("GetFileContent serves from snapshot", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		sessionID := uuid.New()
		key := snapshotKey
		setupSessionMockWithSnapshot(mock, orgID, sessionID, nil, &key)

		cache := stageSnapshotForHandlerTest(t, key, "workspace", map[string]string{
			"src/main.go": fileBody,
		})
		handler := newTestSessionFileHandlerWithCache(t, mock, &mockFileReader{}, cache)

		url := fmt.Sprintf("/api/v1/sessions/%s/files/content?path=src/main.go", sessionID)
		req := httptest.NewRequest(http.MethodGet, url, nil)
		req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))

		w := httptest.NewRecorder()
		withSessionRoute(handler.GetFileContent).ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code, "snapshot ReadFile should produce 200")

		var resp models.SingleResponse[sandbox.FileContent]
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		require.Equal(t, fileBody, resp.Data.Content)
		require.Equal(t, "go", resp.Data.Language)
	})

	t.Run("ListFiles serves from snapshot", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		sessionID := uuid.New()
		key := snapshotKey
		setupSessionMockWithSnapshot(mock, orgID, sessionID, nil, &key)

		cache := stageSnapshotForHandlerTest(t, key, "workspace", map[string]string{
			"a.go":         "// a",
			"b.go":         "// b",
			"sub/inner.go": "// inner",
		})
		handler := newTestSessionFileHandlerWithCache(t, mock, &mockFileReader{}, cache)

		url := fmt.Sprintf("/api/v1/sessions/%s/files", sessionID)
		req := httptest.NewRequest(http.MethodGet, url, nil)
		req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))

		w := httptest.NewRecorder()
		withSessionRoute(handler.ListFiles).ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code)

		var resp models.ListResponse[sandbox.FileEntry]
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		require.Len(t, resp.Data, 3, "snapshot ListDir should return all top-level entries")
	})

	t.Run("returns 409 when neither container nor snapshot is available", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		sessionID := uuid.New()
		setupSessionMockWithSnapshot(mock, orgID, sessionID, nil, nil)

		// Cache is configured but the session has no snapshot key — same
		// outcome as "no cache wired up at all".
		cache := stageSnapshotForHandlerTest(t, "unrelated", "workspace", map[string]string{"x": "x"})
		handler := newTestSessionFileHandlerWithCache(t, mock, &mockFileReader{}, cache)

		url := fmt.Sprintf("/api/v1/sessions/%s/files/context?path=x&line=1", sessionID)
		req := httptest.NewRequest(http.MethodGet, url, nil)
		req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))

		w := httptest.NewRecorder()
		withSessionRoute(handler.GetFileContext).ServeHTTP(w, req)
		require.Equal(t, http.StatusConflict, w.Code, "no container + no snapshot must keep returning NO_SANDBOX")
	})

	t.Run("returns 409 when snapshot is missing from storage", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		sessionID := uuid.New()
		// Session points at a snapshot key, but the key is not in storage.
		// The cache should surface ErrSnapshotMissing and the handler
		// should map it back to NO_SANDBOX so the frontend disables the
		// expanders gracefully.
		key := "snapshots/o/s/missing"
		setupSessionMockWithSnapshot(mock, orgID, sessionID, nil, &key)

		emptyCache, err := workspace.NewSnapshotCache(storage.NewFileSnapshotStore(t.TempDir()), t.TempDir(), 0, zerolog.Nop())
		require.NoError(t, err)
		handler := newTestSessionFileHandlerWithCache(t, mock, &mockFileReader{}, emptyCache)

		url := fmt.Sprintf("/api/v1/sessions/%s/files/context?path=x&line=1", sessionID)
		req := httptest.NewRequest(http.MethodGet, url, nil)
		req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))

		w := httptest.NewRecorder()
		withSessionRoute(handler.GetFileContext).ServeHTTP(w, req)
		require.Equal(t, http.StatusConflict, w.Code)
	})

	t.Run("repo-attached session resolves WorkDir to home/<user>/<slug>", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		sessionID := uuid.New()
		repoID := uuid.New()
		key := "snapshots/o/s/repo-attached"
		setupSessionMockFull(mock, orgID, sessionID, nil, &key, &repoID)

		// Repo lookup returns a repo whose slug ("repo") drives WorkDir
		// resolution to /home/sandbox/repo. The snapshot tar must therefore
		// be rooted at "home/sandbox/repo" — anchored to the SAME path the
		// orchestrator's tar producer uses, not the legacy "workspace".
		mock.ExpectQuery("SELECT .+ FROM repositories\\s+WHERE id").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(
				pgxmock.NewRows(repositoryRowColumns).
					AddRow(repoRow(repoID, orgID, "owner/repo")...),
			)

		// Build the snapshot under home/sandbox/repo/... — the path the
		// repo-attached orchestrator places the workspace at. If the
		// handler resolved WorkDir back to "workspace", the snapshot
		// reader would not find this file at all.
		cache := stageSnapshotForHandlerTest(t, key, "home/sandbox/repo", map[string]string{
			"src/main.go": fileBody,
		})

		handler := newTestSessionFileHandlerWithRepoStore(t, mock, &mockFileReader{}, cache)

		url := fmt.Sprintf("/api/v1/sessions/%s/files/context?path=src/main.go&line=3&above=1&below=1", sessionID)
		req := httptest.NewRequest(http.MethodGet, url, nil)
		req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))

		w := httptest.NewRecorder()
		withSessionRoute(handler.GetFileContext).ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code, "repo-attached snapshot read must succeed; body: %s", w.Body.String())

		var resp models.SingleResponse[sandbox.FileContextResult]
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		require.Equal(t, 5, resp.Data.TotalLines)
		require.Len(t, resp.Data.Lines, 3)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns 500 SNAPSHOT_UNREADABLE when archive is corrupt", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		sessionID := uuid.New()
		key := "snapshots/o/s/corrupt"
		setupSessionMockWithSnapshot(mock, orgID, sessionID, nil, &key)

		// Stage a payload that is not a valid gzipped tar so extractTarGz
		// surfaces an ErrSnapshotUnreadable through the cache.
		storeDir := t.TempDir()
		store := storage.NewFileSnapshotStore(storeDir)
		require.NoError(t, store.Save(context.Background(), key, bytes.NewReader([]byte("not a tar archive"))))
		cache, err := workspace.NewSnapshotCache(store, t.TempDir(), 0, zerolog.Nop())
		require.NoError(t, err)
		handler := newTestSessionFileHandlerWithCache(t, mock, &mockFileReader{}, cache)

		url := fmt.Sprintf("/api/v1/sessions/%s/files/context?path=src/main.go&line=1", sessionID)
		req := httptest.NewRequest(http.MethodGet, url, nil)
		req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))

		w := httptest.NewRecorder()
		withSessionRoute(handler.GetFileContext).ServeHTTP(w, req)
		require.Equal(t, http.StatusInternalServerError, w.Code, "corrupt snapshot must surface as 500, not 404; body: %s", w.Body.String())
		require.Contains(t, w.Body.String(), "SNAPSHOT_UNREADABLE")
	})

	t.Run("returns 500 SNAPSHOT_UNAVAILABLE when snapshot load fails", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "pgxmock pool should initialize")
		defer mock.Close()

		orgID := uuid.New()
		sessionID := uuid.New()
		key := "snapshots/o/s/load-fails"
		setupSessionMockWithSnapshot(mock, orgID, sessionID, nil, &key)

		cache, err := workspace.NewSnapshotCache(&failingSnapshotLoadStore{}, t.TempDir(), 0, zerolog.Nop())
		require.NoError(t, err, "snapshot cache should initialize")
		handler := newTestSessionFileHandlerWithCache(t, mock, &mockFileReader{}, cache)

		url := fmt.Sprintf("/api/v1/sessions/%s/files/context?path=src/main.go&line=1", sessionID)
		req := httptest.NewRequest(http.MethodGet, url, nil)
		req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))

		w := httptest.NewRecorder()
		withSessionRoute(handler.GetFileContext).ServeHTTP(w, req)
		require.Equal(t, http.StatusInternalServerError, w.Code, "load failure must surface as 500, not 404; body: %s", w.Body.String())
		require.Contains(t, w.Body.String(), "SNAPSHOT_UNAVAILABLE", "response should identify snapshot load failure")
	})
}

// TestSessionFileHandler_RepoLookupFailureReturns500 verifies that a
// repo-attached session whose repo lookup fails surfaces 500
// WORKDIR_UNAVAILABLE instead of silently degrading to /workspace.
// Previously, a transient DB error here would fall back to the wrong
// workdir and the snapshot/live reader would then surface a misleading
// FILE_NOT_FOUND. The handler should refuse rather than serve content
// from a workdir it knows is wrong for repo-attached sessions.
func TestSessionFileHandler_RepoLookupFailureReturns500(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()
	repoID := uuid.New()
	key := "snapshots/o/s/k"
	setupSessionMockFull(mock, orgID, sessionID, nil, &key, &repoID)

	// Repo lookup fails with a generic error — simulates a transient DB blip.
	mock.ExpectQuery("SELECT .+ FROM repositories\\s+WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(fmt.Errorf("transient db error"))

	// Cache is wired but should never be consulted — workdir resolution
	// fails before reader selection.
	cache := stageSnapshotForHandlerTest(t, key, "home/sandbox/repo", map[string]string{
		"src/main.go": "x\n",
	})
	handler := newTestSessionFileHandlerWithRepoStore(t, mock, &mockFileReader{}, cache)

	url := fmt.Sprintf("/api/v1/sessions/%s/files/context?path=src/main.go&line=1", sessionID)
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))

	w := httptest.NewRecorder()
	withSessionRoute(handler.GetFileContext).ServeHTTP(w, req)
	require.Equal(t, http.StatusInternalServerError, w.Code, "repo lookup failure must surface as 500, not 404; body: %s", w.Body.String())
	require.Contains(t, w.Body.String(), "WORKDIR_UNAVAILABLE", "response should identify the failed workdir resolution")
}

// TestSessionFileHandler_RepoSlugIsCached verifies repeated requests
// against the same repo-attached session do not re-issue the repository
// SELECT. The cache is keyed on repository ID and the resolved value is
// effectively immutable for the process lifetime, so a second request
// must serve from cache without an additional DB hit. Without this
// memoization, every "Show 20 above" click during review would issue an
// extra repo lookup against the database.
func TestSessionFileHandler_RepoSlugIsCached(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()
	repoID := uuid.New()
	containerID := "container-abc"
	// pgxmock matches expectations in order, and each handler call
	// issues SELECT sessions THEN (uncached) SELECT repositories. So
	// the expected sequence across two requests is:
	//   req 1: sessions, repositories
	//   req 2: sessions   (slug cache absorbs the repositories lookup)
	setupSessionMockFull(mock, orgID, sessionID, &containerID, nil, &repoID)
	mock.ExpectQuery("SELECT .+ FROM repositories\\s+WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(repositoryRowColumns).
				AddRow(repoRow(repoID, orgID, "owner/repo")...),
		)
	setupSessionMockFull(mock, orgID, sessionID, &containerID, nil, &repoID)

	fr := &mockFileReader{
		readContextFn: func(_ context.Context, _, _, _ string, _, _, _ int) (sandbox.FileContextResult, error) {
			return sandbox.FileContextResult{
				Lines:      []sandbox.FileLine{{Number: 1, Content: "ok"}},
				StartLine:  1,
				EndLine:    1,
				TotalLines: 1,
			}, nil
		},
	}
	handler := newTestSessionFileHandlerWithRepoStore(t, mock, fr, nil)

	for i := 0; i < 2; i++ {
		url := fmt.Sprintf("/api/v1/sessions/%s/files/context?path=src/main.go&line=1", sessionID)
		req := httptest.NewRequest(http.MethodGet, url, nil)
		req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
		w := httptest.NewRecorder()
		withSessionRoute(handler.GetFileContext).ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code, "request %d failed: %s", i, w.Body.String())
	}

	// pgxmock is strict — leftover unmet expectations would mean we issued
	// fewer queries than expected; an extra query would have already
	// failed inside ServeHTTP. ExpectationsWereMet pins both directions.
	require.NoError(t, mock.ExpectationsWereMet(), "second request must hit the slug cache, not the DB")
}

// TestSessionFileHandler_LiveContainerResolvesRepoWorkDir verifies the
// live-container path uses the per-session resolved WorkDir (e.g.
// /home/sandbox/<slug> for repo-attached sessions) rather than the
// legacy hardcoded /workspace. Previously a repo-attached session's
// live container reads pointed at /workspace, which the orchestrator
// no longer uses — silently breaking file context for repo sessions
// even while the container was alive. This test locks in the fix.
func TestSessionFileHandler_LiveContainerResolvesRepoWorkDir(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()
	repoID := uuid.New()
	containerID := "container-abc"
	setupSessionMockFull(mock, orgID, sessionID, &containerID, nil, &repoID)

	mock.ExpectQuery("SELECT .+ FROM repositories\\s+WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(repositoryRowColumns).
				AddRow(repoRow(repoID, orgID, "owner/repo")...),
		)

	// Capture the workDir argument the live reader receives so we can
	// assert it matches the orchestrator's HomeDir + "/" + slug rule.
	var seenWorkDir string
	fr := &mockFileReader{
		readContextFn: func(_ context.Context, _, workDir, _ string, _, _, _ int) (sandbox.FileContextResult, error) {
			seenWorkDir = workDir
			return sandbox.FileContextResult{
				Lines:      []sandbox.FileLine{{Number: 1, Content: "ok"}},
				StartLine:  1,
				EndLine:    1,
				TotalLines: 1,
			}, nil
		},
	}

	handler := newTestSessionFileHandlerWithRepoStore(t, mock, fr, nil)

	url := fmt.Sprintf("/api/v1/sessions/%s/files/context?path=src/main.go&line=1", sessionID)
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))

	w := httptest.NewRecorder()
	withSessionRoute(handler.GetFileContext).ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, "live container read should succeed; body: %s", w.Body.String())
	require.Equal(t, "/home/sandbox/repo", seenWorkDir, "live container reads must use orchestrator-resolved WorkDir, not hardcoded /workspace")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestValidatePath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input    string
		expected string
		valid    bool
	}{
		{"", ".", true},
		{".", ".", true},
		{"/", ".", true},
		{"src/main.go", "src/main.go", true},
		{"/src/main.go", "src/main.go", true},
		{"../etc/passwd", "", false},
		{"../../root", "", false},
		{"src/../lib/utils.go", "lib/utils.go", true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			result, valid := validatePath(tt.input)
			require.Equal(t, tt.valid, valid, "valid mismatch for %q", tt.input)
			if valid {
				require.Equal(t, tt.expected, result)
			}
		})
	}
}

func TestInferLanguage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		path     string
		expected string
	}{
		{"main.go", "go"},
		{"app.tsx", "tsx"},
		{"index.ts", "typescript"},
		{"style.css", "css"},
		{"README.md", "markdown"},
		{"Makefile", "text"},
		{"config.yaml", "yaml"},
		{"query.sql", "sql"},
		{"unknown.xyz", "text"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, inferLanguage(tt.path))
		})
	}
}

type failingSnapshotLoadStore struct{}

func (s *failingSnapshotLoadStore) Save(context.Context, string, io.Reader) error {
	return nil
}

func (s *failingSnapshotLoadStore) Load(context.Context, string, io.Writer) error {
	return fmt.Errorf("object store unavailable")
}

func (s *failingSnapshotLoadStore) Delete(context.Context, string) error {
	return nil
}

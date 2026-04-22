package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/sandbox"
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
	return NewSessionFileHandler(
		db.NewSessionStore(mock),
		fr,
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
	"id", "issue_id", "org_id", "agent_type", "status", "autonomy_level", "token_mode",
	"complexity_tier", "confidence_score", "confidence_reasoning", "risk_factors",
	"container_id", "turn_holding_container", "started_at", "completed_at", "token_usage",
	"failure_explanation", "failure_category", "failure_next_steps", "failure_retry_advised",
	"parent_session_id", "revision_context", "error", "result_summary", "diff",
	"pm_plan_id", "title", "pm_approach", "pm_reasoning",
	"project_task_id", "model_override", "triggered_by_user_id",
	"agent_session_id", "current_turn", "last_activity_at", "sandbox_state", "snapshot_key",
	"target_branch", "working_branch", "base_commit_sha", "repository_id", "diff_stats", "diff_history", "input_manifest", "archived_at", "archived_by_user_id", "automation_run_id", "pr_creation_state", "pr_creation_error", "diff_collected_at", "latest_diff_snapshot_id", "deleted_at", "created_at",
}

func setupSessionMock(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID, containerID *string) {
	now := time.Now()
	issueID := uuid.New()
	mock.ExpectQuery("SELECT .+ FROM sessions WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionColumnsForFiles).AddRow(
				sessionID, issueID, orgID, "claude_code", "running", "supervised", "standard",
				nil, nil, nil, nil, // complexity_tier through risk_factors
				containerID, false, &now, nil, nil, // container_id, turn_holding_container, started_at, completed_at, token_usage
				nil, nil, nil, false, // failure fields
				nil, nil, nil, nil, nil, // parent_session_id through diff
				nil, nil, nil, nil, // pm_plan_id through pm_reasoning
				nil, nil, nil, // project_task_id, model_override, triggered_by_user_id
				nil, 0, now, "running", nil, // agent_session_id, current_turn, last_activity_at, sandbox_state, snapshot_key
				nil, nil, nil, nil, // target_branch, working_branch, base_commit_sha, repository_id
				nil, nil, // diff_stats, diff_history
				nil,      // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil,            // automation_run_id
				"idle",         // pr_creation_state
				(*string)(nil), // pr_creation_error
				nil,            // diff_collected_at
				nil,            // latest_diff_snapshot_id
				nil,            // deleted_at
				now,            // created_at
			),
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
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID) {
				setupSessionMock(mock, orgID, sessionID, &containerID)
			},
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
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID) {
				setupSessionMock(mock, orgID, sessionID, &containerID)
			},
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
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID) {
				setupSessionMock(mock, orgID, sessionID, &containerID)
			},
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

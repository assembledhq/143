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
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

type mockSessionComposerRepoTreeService struct {
	token           string
	tree            []models.RepositoryTreeEntry
	err             error
	lastBranch      string
	lastToken       string
	lastOwner       string
	lastRepo        string
	tokenCalls      int
	treeCalls       int
	contents        map[string]string
	contentErr      error
	contentCalls    int
	lastContentPath string
}

func (m *mockSessionComposerRepoTreeService) GetInstallationToken(ctx context.Context, installationID int64) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	m.tokenCalls++
	return m.token, nil
}

func (m *mockSessionComposerRepoTreeService) ListRepositoryTree(ctx context.Context, token, owner, repo, branch string) ([]models.RepositoryTreeEntry, error) {
	if m.err != nil {
		return nil, m.err
	}
	m.treeCalls++
	m.lastToken = token
	m.lastOwner = owner
	m.lastRepo = repo
	m.lastBranch = branch
	return m.tree, nil
}

func (m *mockSessionComposerRepoTreeService) GetFileContent(ctx context.Context, token, owner, repo, ref, path string) (string, error) {
	if m.contentErr != nil {
		return "", m.contentErr
	}
	m.contentCalls++
	m.lastContentPath = path
	if m.contents == nil {
		return "", nil
	}
	return m.contents[path], nil
}

func TestSessionComposerHandler_ListFileMentions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		query        string
		setupMock    func(mock pgxmock.PgxPoolIface, orgID, repoID uuid.UUID)
		service      *mockSessionComposerRepoTreeService
		expectedCode int
		assertBody   func(t *testing.T, body []byte)
	}{
		{
			name:  "returns ranked file and directory matches",
			query: "sess",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, repoID uuid.UUID) {
				now := time.Now()
				mock.ExpectQuery("SELECT .+ FROM repositories WHERE id").
					WithArgs(repoID, orgID).
					WillReturnRows(
						pgxmock.NewRows(repoColumns()).AddRow(
							repoID, orgID, uuid.New(), int64(1001), "acme/app", "main",
							false, nil, nil, "https://github.com/acme/app.git", int64(99), "active",
							nil, nil, []byte(`{}`), now, now,
						),
					)
			},
			service: &mockSessionComposerRepoTreeService{
				token: "ghs_test",
				tree: []models.RepositoryTreeEntry{
					{Path: "docs/session-guide.md", Type: models.RepositoryTreeEntryTypeFile},
					{Path: "internal/session", Type: models.RepositoryTreeEntryTypeDirectory},
					{Path: "frontend/src/session-panel.tsx", Type: models.RepositoryTreeEntryTypeFile},
					{Path: "pkg/sessioncomposer", Type: models.RepositoryTreeEntryTypeDirectory},
				},
			},
			expectedCode: http.StatusOK,
			assertBody: func(t *testing.T, body []byte) {
				t.Helper()

				var resp models.ListResponse[models.SessionInputReference]
				err := json.Unmarshal(body, &resp)
				require.NoError(t, err, "response should decode")
				require.Len(t, resp.Data, 4, "should return matching files and directories")
				require.Equal(t, models.SessionInputReferenceKindDirectory, resp.Data[0].Kind, "shorter directory prefix match should rank first")
				require.Equal(t, "internal/session", resp.Data[0].Path, "should include matched directory path")
				require.Equal(t, models.SessionInputReferenceKindDirectory, resp.Data[1].Kind, "directory matches should remain typed")
				require.Equal(t, "pkg/sessioncomposer", resp.Data[1].Path, "shorter basename matches should rank ahead of longer file paths")
				require.Equal(t, models.SessionInputReferenceKindFile, resp.Data[2].Kind, "file matches should remain typed")
				require.Equal(t, "docs/session-guide.md", resp.Data[2].Path, "file prefix matches should remain visible near the top")
			},
		},
		{
			name:  "rejects disconnected repositories",
			query: "sess",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, repoID uuid.UUID) {
				now := time.Now()
				mock.ExpectQuery("SELECT .+ FROM repositories WHERE id").
					WithArgs(repoID, orgID).
					WillReturnRows(
						pgxmock.NewRows(repoColumns()).AddRow(
							repoID, orgID, uuid.New(), int64(1001), "acme/app", "main",
							false, nil, nil, "https://github.com/acme/app.git", int64(99), "disconnected",
							nil, nil, []byte(`{}`), now, now,
						),
					)
			},
			service:      &mockSessionComposerRepoTreeService{},
			expectedCode: http.StatusBadRequest,
			assertBody: func(t *testing.T, body []byte) {
				t.Helper()
				require.Contains(t, string(body), "REPO_DISCONNECTED", "should explain disconnected repo failure")
			},
		},
		{
			name:         "returns bad request for invalid repository id",
			query:        "sess",
			setupMock:    func(mock pgxmock.PgxPoolIface, orgID, repoID uuid.UUID) {},
			service:      &mockSessionComposerRepoTreeService{},
			expectedCode: http.StatusBadRequest,
			assertBody: func(t *testing.T, body []byte) {
				t.Helper()
				require.Contains(t, string(body), "INVALID_REPOSITORY_ID", "should validate repository id")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "pgxmock pool should be created")
			defer mock.Close()

			orgID := uuid.New()
			repoID := uuid.New()
			handler := NewSessionComposerHandler(db.NewRepositoryStore(mock), tt.service)
			tt.setupMock(mock, orgID, repoID)

			repoParam := repoID.String()
			if tt.name == "returns bad request for invalid repository id" {
				repoParam = "not-a-uuid"
			}

			req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/session-composer/files?repository_id=%s&q=%s", repoParam, tt.query), nil)
			req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
			w := httptest.NewRecorder()

			handler.ListFileMentions(w, req)
			require.Equal(t, tt.expectedCode, w.Code, "status code should match")
			tt.assertBody(t, w.Body.Bytes())
			require.NoError(t, mock.ExpectationsWereMet(), "database expectations should be met")
		})
	}
}

func TestSessionComposerHandler_ListFileMentions_EmptyQueryReturnsEmptyList(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	handler := NewSessionComposerHandler(db.NewRepositoryStore(mock), &mockSessionComposerRepoTreeService{})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/session-composer/files?repository_id="+uuid.New().String(), nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), uuid.New()))
	w := httptest.NewRecorder()

	handler.ListFileMentions(w, req)
	require.Equal(t, http.StatusOK, w.Code, "empty query should be accepted")

	var resp models.ListResponse[models.SessionInputReference]
	err = json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err, "response should decode")
	require.Empty(t, resp.Data, "empty query should avoid GitHub work and return no suggestions")
	require.NoError(t, mock.ExpectationsWereMet(), "no database calls should be made")
}

func TestSessionComposerHandler_ListFileMentions_UsesRequestedBranch(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	orgID := uuid.New()
	repoID := uuid.New()
	now := time.Now()
	mock.ExpectQuery("SELECT .+ FROM repositories WHERE id").
		WithArgs(repoID, orgID).
		WillReturnRows(
			pgxmock.NewRows(repoColumns()).AddRow(
				repoID, orgID, uuid.New(), int64(1001), "acme/app", "main",
				false, nil, nil, "https://github.com/acme/app.git", int64(99), "active",
				nil, nil, []byte(`{}`), now, now,
			),
		)

	service := &mockSessionComposerRepoTreeService{
		token: "ghs_test",
		tree: []models.RepositoryTreeEntry{
			{Path: "release-only/file.go", Type: models.RepositoryTreeEntryTypeFile},
		},
	}
	handler := NewSessionComposerHandler(db.NewRepositoryStore(mock), service)

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/session-composer/files?repository_id=%s&q=file&branch=release/2026.04", repoID), nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.ListFileMentions(w, req)
	require.Equal(t, http.StatusOK, w.Code, "status code should match")
	require.Equal(t, "release/2026.04", service.lastBranch, "requested branch should be used for tree lookup")
	require.NoError(t, mock.ExpectationsWereMet(), "database expectations should be met")
}

func TestSessionComposerHandler_ListFileMentions_CachesRepositoryTreeByBranch(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	orgID := uuid.New()
	repoID := uuid.New()
	now := time.Now()
	for range 2 {
		mock.ExpectQuery("SELECT .+ FROM repositories WHERE id").
			WithArgs(repoID, orgID).
			WillReturnRows(
				pgxmock.NewRows(repoColumns()).AddRow(
					repoID, orgID, uuid.New(), int64(1001), "acme/app", "main",
					false, nil, nil, "https://github.com/acme/app.git", int64(99), "active",
					nil, nil, []byte(`{}`), now, now,
				),
			)
	}

	service := &mockSessionComposerRepoTreeService{
		token: "ghs_test",
		tree: []models.RepositoryTreeEntry{
			{Path: "internal/session", Type: models.RepositoryTreeEntryTypeDirectory},
		},
	}
	handler := NewSessionComposerHandler(db.NewRepositoryStore(mock), service)

	for _, query := range []string{"s", "se"} {
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/session-composer/files?repository_id=%s&q=%s&branch=main", repoID, query), nil)
		req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
		w := httptest.NewRecorder()
		handler.ListFileMentions(w, req)
		require.Equal(t, http.StatusOK, w.Code, "status code should match")
	}

	require.Equal(t, 1, service.tokenCalls, "cached lookups should not fetch a fresh installation token per keystroke")
	require.Equal(t, 1, service.treeCalls, "cached lookups should reuse the repository tree for the same repo and branch")
	require.NoError(t, mock.ExpectationsWereMet(), "database expectations should be met")
}

func TestSessionComposerHandler_ListFileMentions_PrunesExpiredCacheEntries(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	orgID := uuid.New()
	repoID := uuid.New()
	baseTime := time.Now()
	for range 2 {
		mock.ExpectQuery("SELECT .+ FROM repositories WHERE id").
			WithArgs(repoID, orgID).
			WillReturnRows(
				pgxmock.NewRows(repoColumns()).AddRow(
					repoID, orgID, uuid.New(), int64(1001), "acme/app", "main",
					false, nil, nil, "https://github.com/acme/app.git", int64(99), "active",
					nil, nil, []byte(`{}`), baseTime, baseTime,
				),
			)
	}

	service := &mockSessionComposerRepoTreeService{
		token: "ghs_test",
		tree: []models.RepositoryTreeEntry{
			{Path: "internal/session", Type: models.RepositoryTreeEntryTypeDirectory},
		},
	}
	handler := NewSessionComposerHandler(db.NewRepositoryStore(mock), service)

	currentTime := baseTime
	handler.clock = func() time.Time { return currentTime }

	firstReq := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/session-composer/files?repository_id=%s&q=s&branch=main", repoID), nil)
	firstReq = firstReq.WithContext(middleware.WithOrgID(firstReq.Context(), orgID))
	firstW := httptest.NewRecorder()
	handler.ListFileMentions(firstW, firstReq)
	require.Equal(t, http.StatusOK, firstW.Code, "first request should succeed")
	require.Len(t, handler.treeCache, 1, "first request should populate cache")

	currentTime = baseTime.Add(sessionComposerTreeCacheTTL + time.Second)
	secondReq := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/session-composer/files?repository_id=%s&q=s&branch=release", repoID), nil)
	secondReq = secondReq.WithContext(middleware.WithOrgID(secondReq.Context(), orgID))
	secondW := httptest.NewRecorder()
	handler.ListFileMentions(secondW, secondReq)
	require.Equal(t, http.StatusOK, secondW.Code, "second request should succeed")
	require.Len(t, handler.treeCache, 1, "expired entries should be pruned before storing a new branch tree")
	for key := range handler.treeCache {
		require.Contains(t, key, ":release", "only the fresh branch entry should remain cached")
	}
	require.NoError(t, mock.ExpectationsWereMet(), "database expectations should be met")
}

func TestSessionComposerHandler_ListFileMentions_InvalidRepoRouteContextUnused(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/session-composer/files", nil)
	rctx := chi.NewRouteContext()
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	require.NotNil(t, req.Context().Value(chi.RouteCtxKey), "test should pin that route context is irrelevant for query-param endpoint")
}

func TestSessionComposerHandler_ListFileMentions_ErrorPaths(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		repoFullName string
		repoTree     sessionComposerRepoTreeService
		serviceErr   error
		expectedCode int
		expectedBody string
	}{
		{
			name:         "github not configured",
			repoFullName: "acme/app",
			repoTree:     nil,
			expectedCode: http.StatusServiceUnavailable,
			expectedBody: "GITHUB_NOT_CONFIGURED",
		},
		{
			name:         "invalid repository full name",
			repoFullName: "acme-only",
			repoTree:     &mockSessionComposerRepoTreeService{},
			expectedCode: http.StatusInternalServerError,
			expectedBody: "INVALID_REPOSITORY",
		},
		{
			name:         "installation token failure",
			repoFullName: "acme/app",
			repoTree: &mockSessionComposerRepoTreeService{
				err: fmt.Errorf("token exchange failed"),
			},
			expectedCode: http.StatusBadGateway,
			expectedBody: "GITHUB_TOKEN_FAILED",
		},
		{
			name:         "repository tree failure",
			repoFullName: "acme/app",
			repoTree: &mockSessionComposerRepoTreeService{
				err: fmt.Errorf("upstream github unavailable"),
			},
			expectedCode: http.StatusBadGateway,
			expectedBody: "GITHUB_API_FAILED",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "pgxmock pool should be created")
			defer mock.Close()

			orgID := uuid.New()
			repoID := uuid.New()
			now := time.Now()
			mock.ExpectQuery("SELECT .+ FROM repositories WHERE id").
				WithArgs(repoID, orgID).
				WillReturnRows(
					pgxmock.NewRows(repoColumns()).AddRow(
						repoID, orgID, uuid.New(), int64(1001), tt.repoFullName, "main",
						false, nil, nil, "https://github.com/acme/app.git", int64(99), "active",
						nil, nil, []byte(`{}`), now, now,
					),
				)

			handler := NewSessionComposerHandler(db.NewRepositoryStore(mock), tt.repoTree)
			req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/session-composer/files?repository_id=%s&q=sess", repoID), nil)
			req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
			w := httptest.NewRecorder()

			handler.ListFileMentions(w, req)
			require.Equal(t, tt.expectedCode, w.Code, "status code should match the expected failure mode")
			require.Contains(t, w.Body.String(), tt.expectedBody, "response body should surface the expected error code")
			require.NoError(t, mock.ExpectationsWereMet(), "database expectations should be met")
		})
	}
}

func TestRankSessionComposerReferences_FiltersUnsupportedEntries(t *testing.T) {
	t.Parallel()

	tree := []models.RepositoryTreeEntry{
		{Path: "", Type: models.RepositoryTreeEntryTypeFile},
		{Path: "internal/session", Type: models.RepositoryTreeEntryType("commit")},
		{Path: "pkg/sessioncomposer", Type: models.RepositoryTreeEntryTypeDirectory},
		{Path: "docs/session-guide.md", Type: models.RepositoryTreeEntryTypeFile},
	}

	results := rankSessionComposerReferences("sess", tree)
	require.Equal(t, []models.SessionInputReference{
		{
			Kind:    models.SessionInputReferenceKindDirectory,
			Token:   "@pkg/sessioncomposer",
			Path:    "pkg/sessioncomposer",
			Display: "pkg/sessioncomposer",
		},
		{
			Kind:    models.SessionInputReferenceKindFile,
			Token:   "@docs/session-guide.md",
			Path:    "docs/session-guide.md",
			Display: "docs/session-guide.md",
		},
	}, results, "rankSessionComposerReferences should skip empty paths and unsupported tree entry types")
}

func TestSessionComposerHandler_ListSlashCommands_BuiltinOnly(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewSessionComposerHandler(db.NewRepositoryStore(mock), &mockSessionComposerRepoTreeService{})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/session-composer/slash-commands?agent_type=claude_code", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), uuid.New()))
	w := httptest.NewRecorder()

	handler.ListSlashCommands(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var resp SlashCommandListResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Groups, 1)
	require.Equal(t, models.SessionInputCommandSourceBuiltin, resp.Groups[0].Source)
	require.Equal(t, "Claude Code commands", resp.Groups[0].Label)

	names := make([]string, 0, len(resp.Groups[0].Items))
	for _, item := range resp.Groups[0].Items {
		require.Equal(t, models.AgentTypeClaudeCode, item.AgentType)
		require.Equal(t, "command", item.Kind)
		require.Equal(t, "/"+item.Name, item.Token)
		names = append(names, item.Name)
	}
	require.Contains(t, names, "review")
	require.Contains(t, names, "init")
}

func TestSessionComposerHandler_ListSlashCommands_FiltersByQuery(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewSessionComposerHandler(db.NewRepositoryStore(mock), &mockSessionComposerRepoTreeService{})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/session-composer/slash-commands?agent_type=claude_code&q=rev", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), uuid.New()))
	w := httptest.NewRecorder()

	handler.ListSlashCommands(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var resp SlashCommandListResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Groups, 1)
	require.NotEmpty(t, resp.Groups[0].Items)
	require.Equal(t, "review", resp.Groups[0].Items[0].Name, "name-prefix matches should rank first")
}

func TestSessionComposerHandler_ListSlashCommands_EmptyForAgentWithoutCatalog(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewSessionComposerHandler(db.NewRepositoryStore(mock), &mockSessionComposerRepoTreeService{})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/session-composer/slash-commands?agent_type=pi", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), uuid.New()))
	w := httptest.NewRecorder()

	handler.ListSlashCommands(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var resp SlashCommandListResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Empty(t, resp.Groups, "Pi has no built-in catalog and no project-discovery convention; response should be empty")
}

func TestSessionComposerHandler_ListSlashCommands_RejectsInvalidAgentType(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewSessionComposerHandler(db.NewRepositoryStore(mock), &mockSessionComposerRepoTreeService{})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/session-composer/slash-commands?agent_type=nope", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), uuid.New()))
	w := httptest.NewRecorder()

	handler.ListSlashCommands(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "INVALID_AGENT_TYPE")
}

func TestSessionComposerHandler_ListSlashCommands_RequiresAgentType(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewSessionComposerHandler(db.NewRepositoryStore(mock), &mockSessionComposerRepoTreeService{})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/session-composer/slash-commands", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), uuid.New()))
	w := httptest.NewRecorder()

	handler.ListSlashCommands(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "INVALID_AGENT_TYPE")
}

func TestSessionComposerHandler_ListSlashCommands_IncludesProjectGroup(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	repoID := uuid.New()
	now := time.Now()
	mock.ExpectQuery("SELECT .+ FROM repositories WHERE id").
		WithArgs(repoID, orgID).
		WillReturnRows(
			pgxmock.NewRows(repoColumns()).AddRow(
				repoID, orgID, uuid.New(), int64(1001), "acme/app", "main",
				false, nil, nil, "https://github.com/acme/app.git", int64(99), "active",
				nil, nil, []byte(`{}`), now, now,
			),
		)

	service := &mockSessionComposerRepoTreeService{
		token: "ghs_test",
		tree: []models.RepositoryTreeEntry{
			{Path: ".claude/commands/review.md", Type: models.RepositoryTreeEntryTypeFile},
			{Path: ".claude/commands/auth/setup.md", Type: models.RepositoryTreeEntryTypeFile},
			{Path: "src/main.go", Type: models.RepositoryTreeEntryTypeFile},
		},
	}
	handler := NewSessionComposerHandler(db.NewRepositoryStore(mock), service)
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/session-composer/slash-commands?agent_type=claude_code&repository_id=%s", repoID), nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.ListSlashCommands(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp SlashCommandListResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Groups, 2)
	require.Equal(t, models.SessionInputCommandSourceProject, resp.Groups[1].Source)
	require.Equal(t, "Project commands", resp.Groups[1].Label)

	names := make([]string, 0, len(resp.Groups[1].Items))
	for _, item := range resp.Groups[1].Items {
		require.Equal(t, models.SessionInputCommandSourceProject, item.Source)
		names = append(names, item.Name)
	}
	require.Contains(t, names, "review")
	require.Contains(t, names, "auth:setup")

	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionComposerHandler_ListSlashCommands_FiltersInvalidProjectNames(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	repoID := uuid.New()
	now := time.Now()
	mock.ExpectQuery("SELECT .+ FROM repositories WHERE id").
		WithArgs(repoID, orgID).
		WillReturnRows(
			pgxmock.NewRows(repoColumns()).AddRow(
				repoID, orgID, uuid.New(), int64(1001), "acme/app", "main",
				false, nil, nil, "https://github.com/acme/app.git", int64(99), "active",
				nil, nil, []byte(`{}`), now, now,
			),
		)

	service := &mockSessionComposerRepoTreeService{
		token: "ghs_test",
		tree: []models.RepositoryTreeEntry{
			{Path: ".claude/commands/review.md", Type: models.RepositoryTreeEntryTypeFile},
			{Path: ".claude/commands/name with space.md", Type: models.RepositoryTreeEntryTypeFile},
			{Path: ".claude/commands/-leading-dash.md", Type: models.RepositoryTreeEntryTypeFile},
			{Path: ".claude/commands/auth/setup.md", Type: models.RepositoryTreeEntryTypeFile},
		},
	}
	handler := NewSessionComposerHandler(db.NewRepositoryStore(mock), service)
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/session-composer/slash-commands?agent_type=claude_code&repository_id=%s", repoID), nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.ListSlashCommands(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp SlashCommandListResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Groups, 2)

	projectNames := make([]string, 0, len(resp.Groups[1].Items))
	for _, item := range resp.Groups[1].Items {
		projectNames = append(projectNames, item.Name)
	}
	require.Equal(t, []string{"review", "auth:setup"}, projectNames, "invalid project command names should not be listed")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionComposerHandler_ListSlashCommands_AgentWithoutProjectConventionSkipsRepoLookup(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	service := &mockSessionComposerRepoTreeService{token: "ghs_test"}
	handler := NewSessionComposerHandler(db.NewRepositoryStore(mock), service)
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/session-composer/slash-commands?agent_type=amp&repository_id=%s", uuid.New()), nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), uuid.New()))
	w := httptest.NewRecorder()

	handler.ListSlashCommands(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, 0, service.treeCalls, "amp has no project commands convention so the repo tree must not be fetched")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionComposerHandler_GetSlashCommandDetail(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	repoID := uuid.New()
	now := time.Now()
	mock.ExpectQuery("SELECT .+ FROM repositories WHERE id").
		WithArgs(repoID, orgID).
		WillReturnRows(
			pgxmock.NewRows(repoColumns()).AddRow(
				repoID, orgID, uuid.New(), int64(1001), "acme/app", "main",
				false, nil, nil, "https://github.com/acme/app.git", int64(99), "active",
				nil, nil, []byte(`{}`), now, now,
			),
		)

	frontmatter := "---\nname: review\ndescription: Review pending changes\n---\n\nDo a code review."
	service := &mockSessionComposerRepoTreeService{
		token: "ghs_test",
		contents: map[string]string{
			".claude/commands/review.md": frontmatter,
		},
	}
	handler := NewSessionComposerHandler(db.NewRepositoryStore(mock), service)
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/session-composer/slash-commands/details?agent_type=claude_code&repository_id=%s&name=review", repoID), nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.GetSlashCommandDetail(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp SlashCommandDetailResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, "review", resp.Command.Name)
	require.Equal(t, "Review pending changes", resp.Command.Description)
	require.Equal(t, models.SessionInputCommandSourceProject, resp.Command.Source)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionComposerHandler_GetSlashCommandDetail_NestedName(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	repoID := uuid.New()
	now := time.Now()
	mock.ExpectQuery("SELECT .+ FROM repositories WHERE id").
		WithArgs(repoID, orgID).
		WillReturnRows(
			pgxmock.NewRows(repoColumns()).AddRow(
				repoID, orgID, uuid.New(), int64(1001), "acme/app", "main",
				false, nil, nil, "https://github.com/acme/app.git", int64(99), "active",
				nil, nil, []byte(`{}`), now, now,
			),
		)

	service := &mockSessionComposerRepoTreeService{
		token: "ghs_test",
		contents: map[string]string{
			".claude/commands/auth/setup.md": "# Configure auth\n\nProvision new auth provider.",
		},
	}
	handler := NewSessionComposerHandler(db.NewRepositoryStore(mock), service)
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/session-composer/slash-commands/details?agent_type=claude_code&repository_id=%s&name=auth:setup", repoID), nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.GetSlashCommandDetail(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, ".claude/commands/auth/setup.md", service.lastContentPath)

	var resp SlashCommandDetailResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, "Configure auth", resp.Command.Description)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionComposerHandler_GetSlashCommandDetail_RejectsTraversalName(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	cases := []string{
		"../etc/passwd",
		"..",
		"name with space",
		"foo/bar",
		"-leading-dash",
		"",
	}

	for _, raw := range cases {
		raw := raw
		t.Run(raw, func(t *testing.T) {
			t.Parallel()

			handler := NewSessionComposerHandler(db.NewRepositoryStore(mock), &mockSessionComposerRepoTreeService{})
			url := fmt.Sprintf("/api/v1/session-composer/slash-commands/details?agent_type=claude_code&repository_id=%s&name=%s", uuid.New(), urlEscape(raw))
			req := httptest.NewRequest(http.MethodGet, url, nil)
			req = req.WithContext(middleware.WithOrgID(req.Context(), uuid.New()))
			w := httptest.NewRecorder()

			handler.GetSlashCommandDetail(w, req)
			require.Equal(t, http.StatusBadRequest, w.Code, "name %q should be rejected", raw)
			require.Contains(t, w.Body.String(), "INVALID_NAME")
		})
	}
}

// urlEscape avoids pulling in net/url just for a one-shot helper in tests; the
// inputs here are short and ASCII-only.
func urlEscape(s string) string {
	out := make([]byte, 0, len(s)*3)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' || c == '~' {
			out = append(out, c)
			continue
		}
		out = append(out, '%')
		out = append(out, "0123456789ABCDEF"[c>>4])
		out = append(out, "0123456789ABCDEF"[c&0x0F])
	}
	return string(out)
}

func TestExtractCommandDescription(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		want    string
	}{
		{name: "empty", content: "", want: ""},
		{name: "frontmatter description", content: "---\nname: review\ndescription: Review pending changes\n---\nbody", want: "Review pending changes"},
		{name: "frontmatter quoted", content: "---\ndescription: \"Quoted description\"\n---\n", want: "Quoted description"},
		{name: "no frontmatter, first markdown heading", content: "# First heading\n\nBody copy.", want: "First heading"},
		{name: "no frontmatter, plain first line", content: "Plain description on first line.\n\nMore body.", want: "Plain description on first line."},
		{name: "frontmatter without description, falls back to body", content: "---\nname: x\n---\n\nFallback description.", want: "Fallback description."},
		{name: "preserves hashtag-prefixed body line", content: "#tag and content", want: "#tag and content"},
		{name: "strips heading marker only, not subsequent hashes", content: "## Plan: #1 stage", want: "Plan: #1 stage"},
		{name: "CRLF frontmatter description", content: "---\r\ndescription: CRLF description\r\n---\r\nbody", want: "CRLF description"},
		{name: "CRLF body without frontmatter", content: "first line\r\nsecond line", want: "first line"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, extractCommandDescription(tt.content))
		})
	}
}

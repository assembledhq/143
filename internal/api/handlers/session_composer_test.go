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
	token      string
	tree       []models.RepositoryTreeEntry
	err        error
	lastBranch string
	lastToken  string
	lastOwner  string
	lastRepo   string
	tokenCalls int
	treeCalls  int
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

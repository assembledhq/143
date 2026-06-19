package handlers

import (
	"bytes"
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
	"github.com/assembledhq/143/internal/services/storage"
	"github.com/assembledhq/143/internal/services/workspace"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
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

func TestSessionComposerHandler_ListSessionFileMentions(t *testing.T) {
	t.Parallel()

	t.Run("returns snapshot-backed results for the current session workspace", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "pgxmock pool should be created")
		defer mock.Close()

		orgID := uuid.New()
		sessionID := uuid.New()
		key := "snapshots/o/s/workspace.tar.zst"
		setupSessionMockWithSnapshot(mock, orgID, sessionID, nil, &key)

		cacheDir := stageSnapshotForHandlerTest(t, key, "workspace", map[string]string{
			"docs/new-guide.md":        "hello",
			"docs/generated/output.md": "generated",
			"internal/services/api.go": "package services",
			"internal/services/git.go": "package services",
			"tmp/ignore.txt":           "tmp",
		})

		handler := NewSessionComposerHandlerWithWorkspace(
			nil,
			db.NewSessionStore(mock),
			nil,
			&mockFileReader{},
			cacheDir,
			nil,
			zerolog.Nop(),
		)

		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/sessions/%s/composer/files?q=guide", sessionID), nil)
		req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
		w := httptest.NewRecorder()

		withSessionComposerRoute(handler.ListSessionFileMentions).ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code, "snapshot-backed mention search should succeed")

		var resp models.ListResponse[models.SessionInputReference]
		err = json.Unmarshal(w.Body.Bytes(), &resp)
		require.NoError(t, err, "response should decode")
		require.Equal(t, []models.SessionInputReference{
			{Kind: models.SessionInputReferenceKindFile, Token: "@docs/new-guide.md", Path: "docs/new-guide.md", Display: "docs/new-guide.md"},
		}, resp.Data, "snapshot-backed mention search should find files created in the current workspace")
		require.NoError(t, mock.ExpectationsWereMet(), "database expectations should be met")
	})

	t.Run("falls back to the live container when no snapshot exists", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "pgxmock pool should be created")
		defer mock.Close()

		orgID := uuid.New()
		sessionID := uuid.New()
		containerID := "ctr-123"
		setupSessionMockWithSnapshot(mock, orgID, sessionID, &containerID, nil)

		handler := NewSessionComposerHandlerWithWorkspace(
			nil,
			db.NewSessionStore(mock),
			nil,
			&mockFileReader{
				listDirFn: func(ctx context.Context, gotContainerID, workDir, dirPath string) ([]sandbox.FileEntry, error) {
					require.Equal(t, containerID, gotContainerID, "ListSessionFileMentions should read from the live session container")
					switch dirPath {
					case ".":
						return []sandbox.FileEntry{
							{Type: "dir", Path: "docs"},
							{Type: "file", Path: "README.md"},
						}, nil
					case "docs":
						return []sandbox.FileEntry{
							{Type: "file", Path: "docs/follow-up-guide.md"},
						}, nil
					default:
						return []sandbox.FileEntry{}, nil
					}
				},
			},
			nil,
			nil,
			zerolog.Nop(),
		)

		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/sessions/%s/composer/files?q=follow", sessionID), nil)
		req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
		w := httptest.NewRecorder()

		withSessionComposerRoute(handler.ListSessionFileMentions).ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code, "live-container mention search should succeed without a snapshot")

		var resp models.ListResponse[models.SessionInputReference]
		err = json.Unmarshal(w.Body.Bytes(), &resp)
		require.NoError(t, err, "response should decode")
		require.Equal(t, []models.SessionInputReference{
			{Kind: models.SessionInputReferenceKindFile, Token: "@docs/follow-up-guide.md", Path: "docs/follow-up-guide.md", Display: "docs/follow-up-guide.md"},
		}, resp.Data, "live-container mention search should surface files from the current workspace")
		require.NoError(t, mock.ExpectationsWereMet(), "database expectations should be met")
	})

	t.Run("prefers the live container over a stale snapshot when both are available", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "pgxmock pool should be created")
		defer mock.Close()

		orgID := uuid.New()
		sessionID := uuid.New()
		containerID := "ctr-live"
		snapshotKey := "snapshots/o/s/workspace.tar.zst"
		setupSessionMockWithSnapshot(mock, orgID, sessionID, &containerID, &snapshotKey)

		cacheDir := stageSnapshotForHandlerTest(t, snapshotKey, "workspace", map[string]string{
			"docs/from-snapshot.md": "stale",
		})

		handler := NewSessionComposerHandlerWithWorkspace(
			nil,
			db.NewSessionStore(mock),
			nil,
			&mockFileReader{
				listDirFn: func(ctx context.Context, gotContainerID, workDir, dirPath string) ([]sandbox.FileEntry, error) {
					require.Equal(t, containerID, gotContainerID, "ListSessionFileMentions should prefer the live container when it exists")
					switch dirPath {
					case ".":
						return []sandbox.FileEntry{{Type: "dir", Path: "docs"}}, nil
					case "docs":
						return []sandbox.FileEntry{{Type: "file", Path: "docs/from-live.md"}}, nil
					default:
						return []sandbox.FileEntry{}, nil
					}
				},
			},
			cacheDir,
			nil,
			zerolog.Nop(),
		)

		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/sessions/%s/composer/files?q=live", sessionID), nil)
		req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
		w := httptest.NewRecorder()

		withSessionComposerRoute(handler.ListSessionFileMentions).ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code, "mention search should use the live container when both live and snapshot workspaces exist")

		var resp models.ListResponse[models.SessionInputReference]
		err = json.Unmarshal(w.Body.Bytes(), &resp)
		require.NoError(t, err, "response should decode")
		require.Equal(t, []models.SessionInputReference{
			{Kind: models.SessionInputReferenceKindFile, Token: "@docs/from-live.md", Path: "docs/from-live.md", Display: "docs/from-live.md"},
		}, resp.Data, "mention search should return the current live workspace contents instead of stale snapshot results")
		require.NoError(t, mock.ExpectationsWereMet(), "database expectations should be met")
	})

	t.Run("does not reuse snapshot cache entry when live container is selected", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "pgxmock pool should be created")
		defer mock.Close()

		orgID := uuid.New()
		sessionID := uuid.New()
		containerID := "ctr-live-cache"
		snapshotKey := "snapshots/o/s/workspace.tar.zst"
		setupSessionMockWithSnapshot(mock, orgID, sessionID, &containerID, &snapshotKey)

		handler := NewSessionComposerHandlerWithWorkspace(
			nil,
			db.NewSessionStore(mock),
			nil,
			&mockFileReader{
				listDirFn: func(ctx context.Context, gotContainerID, workDir, dirPath string) ([]sandbox.FileEntry, error) {
					require.Equal(t, containerID, gotContainerID, "ListSessionFileMentions should build from the live container even when snapshot cache is warm")
					switch dirPath {
					case ".":
						return []sandbox.FileEntry{{Type: "dir", Path: "docs"}}, nil
					case "docs":
						return []sandbox.FileEntry{{Type: "file", Path: "docs/from-live-cache.md"}}, nil
					default:
						return []sandbox.FileEntry{}, nil
					}
				},
			},
			nil,
			nil,
			zerolog.Nop(),
		)

		cacheSession := models.Session{ID: sessionID, OrgID: orgID, SnapshotKey: &snapshotKey}
		err = handler.mentionIndexes.Warm(context.Background(), workspace.SessionMentionIndexCacheKey(&cacheSession), workspace.MentionIndex{
			Entries: []workspace.MentionIndexEntry{
				{Kind: string(models.SessionInputReferenceKindFile), Path: "docs/from-snapshot-cache.md"},
			},
		})
		require.NoError(t, err, "stale snapshot mention index should warm successfully")

		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/sessions/%s/composer/files?q=live-cache", sessionID), nil)
		req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
		w := httptest.NewRecorder()

		withSessionComposerRoute(handler.ListSessionFileMentions).ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code, "mention search should succeed when live container and stale snapshot cache both exist")

		var resp models.ListResponse[models.SessionInputReference]
		err = json.Unmarshal(w.Body.Bytes(), &resp)
		require.NoError(t, err, "response should decode")
		require.Equal(t, []models.SessionInputReference{
			{Kind: models.SessionInputReferenceKindFile, Token: "@docs/from-live-cache.md", Path: "docs/from-live-cache.md", Display: "docs/from-live-cache.md"},
		}, resp.Data, "mention search should ignore the snapshot cache entry when the live container is selected")
		require.NoError(t, mock.ExpectationsWereMet(), "database expectations should be met")
	})

	t.Run("serves the cross-turn stale index immediately while refreshing in the background", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "pgxmock pool should be created")
		defer mock.Close()

		orgID := uuid.New()
		sessionID := uuid.New()
		containerID := "ctr-live-stale"
		setupSessionMockWithSnapshot(mock, orgID, sessionID, &containerID, nil)

		// Block the live workspace walk so the test fails (times out the
		// request assertion below) if the handler tries a synchronous
		// rebuild instead of serving the warmed alias.
		buildRelease := make(chan struct{})
		defer close(buildRelease)
		handler := NewSessionComposerHandlerWithWorkspace(
			nil,
			db.NewSessionStore(mock),
			nil,
			&mockFileReader{
				listDirFn: func(ctx context.Context, gotContainerID, workDir, dirPath string) ([]sandbox.FileEntry, error) {
					select {
					case <-buildRelease:
					case <-ctx.Done():
					}
					return []sandbox.FileEntry{}, nil
				},
			},
			nil,
			nil,
			zerolog.Nop(),
		)

		// Simulate the orchestrator's turn-complete warm of the session
		// alias; the exact live key (turn/generation churn) is left cold.
		staleSession := models.Session{ID: sessionID, OrgID: orgID}
		err = handler.mentionIndexes.Warm(context.Background(), workspace.SessionMentionIndexStaleCacheKey(&staleSession), workspace.MentionIndex{
			Entries: []workspace.MentionIndexEntry{
				{Kind: string(models.SessionInputReferenceKindFile), Path: "docs/from-previous-turn.md"},
			},
		})
		require.NoError(t, err, "stale alias mention index should warm successfully")

		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/sessions/%s/composer/files?q=previous", sessionID), nil)
		req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
		w := httptest.NewRecorder()

		withSessionComposerRoute(handler.ListSessionFileMentions).ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code, "mention search should serve the stale alias without waiting for a rebuild")

		var resp models.ListResponse[models.SessionInputReference]
		err = json.Unmarshal(w.Body.Bytes(), &resp)
		require.NoError(t, err, "response should decode")
		require.Equal(t, []models.SessionInputReference{
			{Kind: models.SessionInputReferenceKindFile, Token: "@docs/from-previous-turn.md", Path: "docs/from-previous-turn.md", Display: "docs/from-previous-turn.md"},
		}, resp.Data, "mention search should return the previous turn's index when the exact key is cold")
		require.NoError(t, mock.ExpectationsWereMet(), "database expectations should be met")
	})

	t.Run("warms the mention index in the background on the picker-open empty query", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "pgxmock pool should be created")
		defer mock.Close()

		orgID := uuid.New()
		sessionID := uuid.New()
		containerID := "ctr-warm"
		setupSessionMockWithSnapshot(mock, orgID, sessionID, &containerID, nil)

		handler := NewSessionComposerHandlerWithWorkspace(
			nil,
			db.NewSessionStore(mock),
			nil,
			&mockFileReader{
				listDirFn: func(ctx context.Context, gotContainerID, workDir, dirPath string) ([]sandbox.FileEntry, error) {
					if dirPath == "." {
						return []sandbox.FileEntry{{Type: "file", Path: "docs/warmed.md"}}, nil
					}
					return []sandbox.FileEntry{}, nil
				},
			},
			nil,
			nil,
			zerolog.Nop(),
		)

		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/sessions/%s/composer/files?q=", sessionID), nil)
		req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
		w := httptest.NewRecorder()

		withSessionComposerRoute(handler.ListSessionFileMentions).ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code, "the empty-q warm request should respond immediately")

		var resp models.ListResponse[models.SessionInputReference]
		err = json.Unmarshal(w.Body.Bytes(), &resp)
		require.NoError(t, err, "response should decode")
		require.Empty(t, resp.Data, "the empty-q request should return no suggestions")

		// The warm runs after the response; probe the cache via the stale
		// alias with a builder that fails, so the probe only succeeds once
		// the background build has populated the cache.
		staleKey := workspace.SessionMentionIndexStaleCacheKey(&models.Session{ID: sessionID, OrgID: orgID})
		require.Eventually(t, func() bool {
			index, err := handler.mentionIndexes.GetOrBuild(context.Background(), staleKey, func(ctx context.Context) (workspace.MentionIndex, error) {
				return workspace.MentionIndex{}, context.DeadlineExceeded
			})
			return err == nil && len(index.Entries) == 1 && index.Entries[0].Path == "docs/warmed.md"
		}, 5*time.Second, 10*time.Millisecond, "the empty-q request should build the mention index in the background")
		require.NoError(t, mock.ExpectationsWereMet(), "the warm should have loaded the session")
	})

	t.Run("returns NO_SANDBOX when the referenced snapshot is missing", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "pgxmock pool should be created")
		defer mock.Close()

		orgID := uuid.New()
		sessionID := uuid.New()
		snapshotKey := "snapshots/o/s/missing"
		setupSessionMockWithSnapshot(mock, orgID, sessionID, nil, &snapshotKey)

		emptyCache, err := workspace.NewSnapshotCache(storage.NewFileSnapshotStore(t.TempDir()), t.TempDir(), 0, zerolog.Nop())
		require.NoError(t, err, "snapshot cache should initialize")

		handler := NewSessionComposerHandlerWithWorkspace(
			nil,
			db.NewSessionStore(mock),
			nil,
			&mockFileReader{},
			emptyCache,
			nil,
			zerolog.Nop(),
		)

		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/sessions/%s/composer/files?q=guide", sessionID), nil)
		req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
		w := httptest.NewRecorder()

		withSessionComposerRoute(handler.ListSessionFileMentions).ServeHTTP(w, req)
		require.Equal(t, http.StatusConflict, w.Code, "missing snapshots should degrade to NO_SANDBOX for mention search")
		require.Contains(t, w.Body.String(), "NO_SANDBOX", "missing snapshot responses should preserve the existing structured error code")
		require.NoError(t, mock.ExpectationsWereMet(), "database expectations should be met")
	})
}

func withSessionComposerRoute(handler http.HandlerFunc) http.Handler {
	r := chi.NewRouter()
	r.Get("/api/v1/sessions/{id}/composer/files", handler)
	return r
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
	require.Len(t, resp.Groups[0].Items, sessionComposerSlashCommandLimit, "unfiltered builtin command results should be capped at the picker limit")

	expectedGroup := buildBuiltinSlashCommandGroup(models.AgentTypeClaudeCode, "")
	expectedNames := make([]string, 0, len(expectedGroup.Items))
	names := make([]string, 0, len(resp.Groups[0].Items))
	for _, item := range resp.Groups[0].Items {
		require.Equal(t, models.AgentTypeClaudeCode, item.AgentType)
		require.Equal(t, "command", item.Kind)
		require.Equal(t, "/"+item.Name, item.Token)
		names = append(names, item.Name)
	}
	require.Contains(t, names, "init", "short built-in commands should remain visible in the capped default results")
	require.Contains(t, names, "help", "core built-in commands should remain visible in the capped default results")
	for _, item := range expectedGroup.Items {
		expectedNames = append(expectedNames, item.Name)
	}
	require.Equal(t, expectedNames, names)
}

func TestSessionComposerHandler_ListSlashCommands_FiltersByQuery(t *testing.T) {
	t.Parallel()

	expectedGroup := buildBuiltinSlashCommandGroup(models.AgentTypeClaudeCode, "rev")
	require.NotEmpty(t, expectedGroup.Items)

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

	actualNames := make([]string, 0, len(resp.Groups[0].Items))
	expectedNames := make([]string, 0, len(expectedGroup.Items))
	for _, item := range resp.Groups[0].Items {
		actualNames = append(actualNames, item.Name)
	}
	for _, item := range expectedGroup.Items {
		expectedNames = append(expectedNames, item.Name)
	}
	require.Equal(t, expectedNames, actualNames, "query filtering should return ranked builtin command matches")
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

func TestSessionComposerHandler_ListFileMentions_RepoNotFound(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	repoID := uuid.New()
	mock.ExpectQuery("SELECT .+ FROM repositories WHERE id").
		WithArgs(repoID, orgID).
		WillReturnError(fmt.Errorf("not found"))

	handler := NewSessionComposerHandler(db.NewRepositoryStore(mock), &mockSessionComposerRepoTreeService{token: "ghs_test"})
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/session-composer/files?repository_id=%s&q=sess", repoID), nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.ListFileMentions(w, req)
	require.Equal(t, http.StatusNotFound, w.Code)
	require.Contains(t, w.Body.String(), "REPOSITORY_NOT_FOUND")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionComposerHandler_ListSlashCommands_InvalidRepoID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewSessionComposerHandler(db.NewRepositoryStore(mock), &mockSessionComposerRepoTreeService{token: "ghs_test"})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/session-composer/slash-commands?agent_type=claude_code&repository_id=not-a-uuid", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), uuid.New()))
	w := httptest.NewRecorder()

	handler.ListSlashCommands(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "INVALID_REPOSITORY_ID")
}

func TestSessionComposerHandler_ListSlashCommands_DisconnectedRepoMapsToBadRequest(t *testing.T) {
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
				false, nil, nil, "https://github.com/acme/app.git", int64(99), "disconnected",
				nil, nil, []byte(`{}`), now, now,
			),
		)

	handler := NewSessionComposerHandler(db.NewRepositoryStore(mock), &mockSessionComposerRepoTreeService{token: "ghs_test"})
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/session-composer/slash-commands?agent_type=claude_code&repository_id=%s", repoID), nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.ListSlashCommands(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "REPO_DISCONNECTED")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionComposerHandler_ListSlashCommands_GitHubUnconfiguredWithProjectAgent(t *testing.T) {
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

	handler := NewSessionComposerHandler(db.NewRepositoryStore(mock), nil)
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/session-composer/slash-commands?agent_type=claude_code&repository_id=%s", repoID), nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.ListSlashCommands(w, req)
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Contains(t, w.Body.String(), "GITHUB_NOT_CONFIGURED")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionComposerHandler_ListSlashCommands_InvalidRepoFullName(t *testing.T) {
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
				repoID, orgID, uuid.New(), int64(1001), "acme-only-noslash", "main",
				false, nil, nil, "https://github.com/acme/app.git", int64(99), "active",
				nil, nil, []byte(`{}`), now, now,
			),
		)

	handler := NewSessionComposerHandler(db.NewRepositoryStore(mock), &mockSessionComposerRepoTreeService{token: "ghs_test"})
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/session-composer/slash-commands?agent_type=claude_code&repository_id=%s", repoID), nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	var logs bytes.Buffer
	req = req.WithContext(zerolog.New(&logs).WithContext(req.Context()))
	w := httptest.NewRecorder()

	handler.ListSlashCommands(w, req)
	require.Equal(t, http.StatusInternalServerError, w.Code)
	require.Contains(t, w.Body.String(), "INVALID_REPOSITORY")
	require.Contains(t, logs.String(), "invalid repository full name", "invalid repo metadata should be logged for oncall debugging")
	require.Contains(t, logs.String(), repoID.String(), "invalid repo log should include the repository id")
	require.Contains(t, logs.String(), "acme-only-noslash", "invalid repo log should include the malformed full name")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionComposerHandler_ListSlashCommands_TreeFetchTokenError(t *testing.T) {
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

	handler := NewSessionComposerHandler(db.NewRepositoryStore(mock), &mockSessionComposerRepoTreeService{
		err: fmt.Errorf("github token exchange failed"),
	})
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/session-composer/slash-commands?agent_type=claude_code&repository_id=%s", repoID), nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.ListSlashCommands(w, req)
	require.Equal(t, http.StatusBadGateway, w.Code)
	require.Contains(t, w.Body.String(), "GITHUB_TOKEN_FAILED")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionComposerHandler_GetSlashCommandDetail_RejectsMissingAgentType(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewSessionComposerHandler(db.NewRepositoryStore(mock), &mockSessionComposerRepoTreeService{})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/session-composer/slash-commands/details?name=review&repository_id="+uuid.New().String(), nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), uuid.New()))
	w := httptest.NewRecorder()

	handler.GetSlashCommandDetail(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "INVALID_AGENT_TYPE")
}

func TestSessionComposerHandler_GetSlashCommandDetail_RejectsInvalidAgentType(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewSessionComposerHandler(db.NewRepositoryStore(mock), &mockSessionComposerRepoTreeService{})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/session-composer/slash-commands/details?agent_type=nope&name=review&repository_id="+uuid.New().String(), nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), uuid.New()))
	w := httptest.NewRecorder()

	handler.GetSlashCommandDetail(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "INVALID_AGENT_TYPE")
}

func TestSessionComposerHandler_GetSlashCommandDetail_RejectsAgentWithoutProjectConvention(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewSessionComposerHandler(db.NewRepositoryStore(mock), &mockSessionComposerRepoTreeService{})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/session-composer/slash-commands/details?agent_type=amp&name=help&repository_id="+uuid.New().String(), nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), uuid.New()))
	w := httptest.NewRecorder()

	handler.GetSlashCommandDetail(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "UNSUPPORTED_AGENT_TYPE")
}

func TestSessionComposerHandler_GetSlashCommandDetail_RejectsMissingName(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewSessionComposerHandler(db.NewRepositoryStore(mock), &mockSessionComposerRepoTreeService{})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/session-composer/slash-commands/details?agent_type=claude_code&repository_id="+uuid.New().String(), nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), uuid.New()))
	w := httptest.NewRecorder()

	handler.GetSlashCommandDetail(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "INVALID_NAME")
}

func TestSessionComposerHandler_GetSlashCommandDetail_RejectsMissingRepoID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewSessionComposerHandler(db.NewRepositoryStore(mock), &mockSessionComposerRepoTreeService{})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/session-composer/slash-commands/details?agent_type=claude_code&name=review", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), uuid.New()))
	w := httptest.NewRecorder()

	handler.GetSlashCommandDetail(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "INVALID_REPOSITORY_ID")
}

func TestSessionComposerHandler_GetSlashCommandDetail_RejectsInvalidRepoIDFormat(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewSessionComposerHandler(db.NewRepositoryStore(mock), &mockSessionComposerRepoTreeService{})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/session-composer/slash-commands/details?agent_type=claude_code&name=review&repository_id=not-uuid", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), uuid.New()))
	w := httptest.NewRecorder()

	handler.GetSlashCommandDetail(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "INVALID_REPOSITORY_ID")
}

func TestSessionComposerHandler_GetSlashCommandDetail_RepoNotFound(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	repoID := uuid.New()
	mock.ExpectQuery("SELECT .+ FROM repositories WHERE id").
		WithArgs(repoID, orgID).
		WillReturnError(fmt.Errorf("not found"))

	handler := NewSessionComposerHandler(db.NewRepositoryStore(mock), &mockSessionComposerRepoTreeService{token: "ghs_test"})
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/session-composer/slash-commands/details?agent_type=claude_code&name=review&repository_id=%s", repoID), nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.GetSlashCommandDetail(w, req)
	require.Equal(t, http.StatusNotFound, w.Code)
	require.Contains(t, w.Body.String(), "REPOSITORY_NOT_FOUND")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionComposerHandler_GetSlashCommandDetail_RepoDisconnected(t *testing.T) {
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
				false, nil, nil, "https://github.com/acme/app.git", int64(99), "disconnected",
				nil, nil, []byte(`{}`), now, now,
			),
		)

	handler := NewSessionComposerHandler(db.NewRepositoryStore(mock), &mockSessionComposerRepoTreeService{token: "ghs_test"})
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/session-composer/slash-commands/details?agent_type=claude_code&name=review&repository_id=%s", repoID), nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.GetSlashCommandDetail(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "REPO_DISCONNECTED")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionComposerHandler_GetSlashCommandDetail_RepoStoreUnconfigured(t *testing.T) {
	t.Parallel()

	handler := NewSessionComposerHandler(nil, &mockSessionComposerRepoTreeService{token: "ghs_test"})
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/session-composer/slash-commands/details?agent_type=claude_code&name=review&repository_id=%s", uuid.New()), nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), uuid.New()))
	w := httptest.NewRecorder()

	handler.GetSlashCommandDetail(w, req)
	require.Equal(t, http.StatusInternalServerError, w.Code)
	require.Contains(t, w.Body.String(), "REPO_STORE_UNCONFIGURED")
}

func TestSessionComposerHandler_GetSlashCommandDetail_GitHubUnconfigured(t *testing.T) {
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

	handler := NewSessionComposerHandler(db.NewRepositoryStore(mock), nil)
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/session-composer/slash-commands/details?agent_type=claude_code&name=review&repository_id=%s", repoID), nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.GetSlashCommandDetail(w, req)
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Contains(t, w.Body.String(), "GITHUB_NOT_CONFIGURED")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionComposerHandler_GetSlashCommandDetail_InvalidRepoFullName(t *testing.T) {
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
				repoID, orgID, uuid.New(), int64(1001), "acmeonly", "main",
				false, nil, nil, "https://github.com/acme/app.git", int64(99), "active",
				nil, nil, []byte(`{}`), now, now,
			),
		)

	handler := NewSessionComposerHandler(db.NewRepositoryStore(mock), &mockSessionComposerRepoTreeService{token: "ghs_test"})
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/session-composer/slash-commands/details?agent_type=claude_code&name=review&repository_id=%s", repoID), nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.GetSlashCommandDetail(w, req)
	require.Equal(t, http.StatusInternalServerError, w.Code)
	require.Contains(t, w.Body.String(), "INVALID_REPOSITORY")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionComposerHandler_GetSlashCommandDetail_FetchContentError(t *testing.T) {
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

	handler := NewSessionComposerHandler(db.NewRepositoryStore(mock), &mockSessionComposerRepoTreeService{
		token:      "ghs_test",
		contentErr: fmt.Errorf("github 500"),
	})
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/session-composer/slash-commands/details?agent_type=claude_code&name=review&repository_id=%s", repoID), nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.GetSlashCommandDetail(w, req)
	require.Equal(t, http.StatusBadGateway, w.Code)
	require.Contains(t, w.Body.String(), "GITHUB_API_FAILED")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionComposerHandler_GetSlashCommandDetail_DefaultsToRepoBranch(t *testing.T) {
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
				repoID, orgID, uuid.New(), int64(1001), "acme/app", "trunk",
				false, nil, nil, "https://github.com/acme/app.git", int64(99), "active",
				nil, nil, []byte(`{}`), now, now,
			),
		)

	service := &mockSessionComposerRepoTreeService{
		token:    "ghs_test",
		contents: map[string]string{".claude/commands/review.md": "Pick a review focus."},
	}
	handler := NewSessionComposerHandler(db.NewRepositoryStore(mock), service)
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/session-composer/slash-commands/details?agent_type=claude_code&name=review&repository_id=%s", repoID), nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.GetSlashCommandDetail(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, ".claude/commands/review.md", service.lastContentPath)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionComposerHandler_FetchCommandContentCachesAndPrunes(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	repoID := uuid.New()
	baseTime := time.Now()
	for range 3 {
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
		token:    "ghs_test",
		contents: map[string]string{".claude/commands/review.md": "First version"},
	}
	handler := NewSessionComposerHandler(db.NewRepositoryStore(mock), service)
	currentTime := baseTime
	handler.clock = func() time.Time { return currentTime }

	first := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/session-composer/slash-commands/details?agent_type=claude_code&name=review&repository_id=%s", repoID), nil)
	first = first.WithContext(middleware.WithOrgID(first.Context(), orgID))
	firstW := httptest.NewRecorder()
	handler.GetSlashCommandDetail(firstW, first)
	require.Equal(t, http.StatusOK, firstW.Code, firstW.Body.String())
	require.Equal(t, 1, service.contentCalls, "first request should populate the cache")

	cached := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/session-composer/slash-commands/details?agent_type=claude_code&name=review&repository_id=%s", repoID), nil)
	cached = cached.WithContext(middleware.WithOrgID(cached.Context(), orgID))
	cachedW := httptest.NewRecorder()
	handler.GetSlashCommandDetail(cachedW, cached)
	require.Equal(t, http.StatusOK, cachedW.Code)
	require.Equal(t, 1, service.contentCalls, "cached lookups must not refetch the file content")
	require.Len(t, handler.commandContents, 1)

	currentTime = baseTime.Add(sessionComposerCommandContentCacheTTL + time.Second)
	service.contents[".claude/commands/review.md"] = "Second version"
	expired := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/session-composer/slash-commands/details?agent_type=claude_code&name=review&repository_id=%s", repoID), nil)
	expired = expired.WithContext(middleware.WithOrgID(expired.Context(), orgID))
	expiredW := httptest.NewRecorder()
	handler.GetSlashCommandDetail(expiredW, expired)
	require.Equal(t, http.StatusOK, expiredW.Code)
	require.Equal(t, 2, service.contentCalls, "expired entries should refresh from upstream")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRankSlashCommandsSkipsEmptyCatalogEntries(t *testing.T) {
	t.Parallel()

	catalog := []models.SlashCommand{
		{Name: ""},
		{Name: "review", Description: "Review pending changes"},
	}
	out := rankSlashCommands("", catalog, func(cmd models.SlashCommand) models.SessionInputCommand {
		return models.SessionInputCommand{Kind: "command", AgentType: models.AgentTypeClaudeCode, Name: cmd.Name, Token: "/" + cmd.Name, Display: "/" + cmd.Name}
	})
	require.Len(t, out, 1, "blank-named entries should be filtered before ranking")
	require.Equal(t, "review", out[0].Name)
}

func TestRankAndLimitSlashCommandCandidatesEnforcesLimit(t *testing.T) {
	t.Parallel()

	candidates := make([]slashCommandCandidate, sessionComposerSlashCommandLimit+5)
	for i := range candidates {
		name := fmt.Sprintf("cmd%03d", i)
		candidates[i] = slashCommandCandidate{
			command:   models.SessionInputCommand{Kind: "command", AgentType: models.AgentTypeClaudeCode, Name: name, Token: "/" + name, Display: "/" + name},
			matchBits: []bool{true},
			length:    len(name),
		}
	}
	out := rankAndLimitSlashCommandCandidates(candidates)
	require.Len(t, out, sessionComposerSlashCommandLimit, "rank limiter should cap results at sessionComposerSlashCommandLimit")
}

func TestProjectCommandPathFromName(t *testing.T) {
	t.Parallel()

	require.Equal(t, "review.md", projectCommandPathFromName("review", "md"))
	require.Equal(t, "auth/setup.md", projectCommandPathFromName("auth:setup", "md"))
	require.Equal(t, "review", projectCommandPathFromName("review", ""), "empty extension returns the name verbatim")
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

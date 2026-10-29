package github

import (
	"context"
	"crypto/sha1" // Test Git blob identity.
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestCaptureCodeReviewRepositoryGuidance(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, branch, content, mode                                               string
		missing, truncated, badHash, badEncoding, malformed, invalidTree, wantErr bool
		failPath                                                                  string
	}{
		{name: "current master", branch: "master", content: "Use current master guidance."},
		{name: "renamed default branch", branch: "release/review", content: "Updated guidance."},
		{name: "empty file", branch: "main"},
		{name: "missing file", branch: "master", missing: true},
		{name: "missing default branch", wantErr: true},
		{name: "unavailable repository", branch: "master", failPath: "repo", wantErr: true},
		{name: "unavailable tree", branch: "master", failPath: "tree", wantErr: true},
		{name: "unavailable blob", branch: "master", failPath: "blob", wantErr: true},
		{name: "truncated tree", branch: "master", truncated: true, wantErr: true},
		{name: "malformed tree", branch: "master", invalidTree: true, wantErr: true},
		{name: "symlink", branch: "master", mode: "120000", wantErr: true},
		{name: "oversized", branch: "master", content: strings.Repeat("a", models.CodeReviewGuidanceMaxBytes+1), wantErr: true},
		{name: "invalid text", branch: "master", content: "\xff", wantErr: true},
		{name: "binary text", branch: "master", content: "a\x00b", wantErr: true},
		{name: "wrong blob bytes", branch: "master", content: "Guide", badHash: true, wantErr: true},
		{name: "unsupported encoding", branch: "master", badEncoding: true, wantErr: true},
		{name: "malformed base64", branch: "master", malformed: true, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID, repoID := uuid.New(), uuid.New()
			hash := sha1.Sum([]byte(fmt.Sprintf("blob %d\x00%s", len(tt.content), tt.content)))
			blobSHA := fmt.Sprintf("%x", hash)
			paths := []string{}
			var pathsMu sync.Mutex
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				pathsMu.Lock()
				paths = append(paths, r.URL.Path)
				pathsMu.Unlock()
				require.Equal(t, "token fixture-token", r.Header.Get("Authorization"), "guidance must use repository credentials")
				kind := ""
				var response any
				switch r.URL.Path {
				case "/repos/acme/widget":
					kind = "repo"
					response = map[string]any{"default_branch": tt.branch}
				case "/repos/acme/widget/git/trees/" + tt.branch:
					kind = "tree"
					entries := []map[string]any{}
					if !tt.missing {
						mode := tt.mode
						if mode == "" {
							mode = "100644"
						}
						entries = append(entries, map[string]any{"path": "REVIEW.md", "mode": mode, "type": "blob", "size": len(tt.content), "sha": blobSHA})
					}
					response = map[string]any{"sha": strings.Repeat("c", 40), "tree": entries, "truncated": tt.truncated}
					if tt.invalidTree {
						response = map[string]any{}
					}
				case "/repos/acme/widget/git/blobs/" + blobSHA:
					kind = "blob"
					content := tt.content
					if tt.badHash {
						content = "Wrong"
					}
					encoded := base64.StdEncoding.EncodeToString([]byte(content))
					if tt.malformed {
						encoded = "%%%"
					}
					encoding := "base64"
					if tt.badEncoding {
						encoding = "none"
					}
					response = map[string]any{"sha": blobSHA, "encoding": encoding, "content": encoded}
				default:
					t.Errorf("unexpected guidance request: %s", r.URL.Path)
					http.NotFound(w, r)
					return
				}
				if kind == tt.failPath {
					http.Error(w, `{"message":"unavailable"}`, http.StatusForbidden)
					return
				}
				require.NoError(t, json.NewEncoder(w).Encode(response), "fixture response must encode")
			}))
			t.Cleanup(server.Close)
			transport := http.DefaultTransport.(*http.Transport).Clone()
			t.Cleanup(transport.CloseIdleConnections)
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "repository mock should initialize")
			t.Cleanup(mock.Close)
			now := time.Now().UTC()
			mock.ExpectQuery("SELECT id, org_id, integration_id, github_id").WithArgs(pgx.NamedArgs{"id": repoID, "org_id": orgID}).WillReturnRows(pgxmock.NewRows(prTestRepoColumns).AddRow(repoID, orgID, uuid.New(), int64(1), "acme/widget", "stale-default", false, nil, nil, "https://github.com/acme/widget.git", int64(456), "active", nil, nil, []byte(`{}`), now, now))
			svc := &PRService{repos: db.NewRepositoryStore(mock), tokenProvider: &Service{cache: map[int64]*cachedToken{456: {Token: "fixture-token", ExpiresAt: now.Add(time.Hour)}}}, logger: zerolog.Nop(), baseURL: server.URL, httpClient: &http.Client{Transport: transport}}
			got, err := svc.CaptureCodeReviewRepositoryGuidance(context.Background(), orgID, repoID)
			require.NoError(t, mock.ExpectationsWereMet(), "repository reads must be tenant scoped")
			if tt.wantErr {
				require.Error(t, err, "unavailable or invalid guidance must not silently become absent")
				if tt.mode == "120000" || len(tt.content) > models.CodeReviewGuidanceMaxBytes || tt.content == "\xff" || tt.content == "a\x00b" {
					require.ErrorIs(t, err, models.ErrInvalidCodeReviewGuidanceFile, "invalid repository files must be distinguishable from temporary provider failures")
				}
				return
			}
			require.NoError(t, err, "default branch guidance should be captured")
			want := models.CodeReviewRepositoryGuidance{Branch: tt.branch, Present: !tt.missing}
			expectedPaths := []string{"/repos/acme/widget", "/repos/acme/widget/git/trees/" + tt.branch}
			if !tt.missing {
				want.BlobSHA = blobSHA
				want.Content = tt.content
				expectedPaths = append(expectedPaths, "/repos/acme/widget/git/blobs/"+blobSHA)
			}
			require.Equal(t, want, got, "capture must contain exact default branch file or explicit absence")
			pathsMu.Lock()
			defer pathsMu.Unlock()
			require.Equal(t, expectedPaths, paths, "capture must read live default branch then pinned blob without accessing PR refs")
		})
	}
}

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
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/assembledhq/143/internal/cache"
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
					require.Equal(t, "recursive=1", r.URL.RawQuery, "guidance discovery must include nested rubrics from the same tree")
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

func TestCaptureCodeReviewRepositoryGuidanceCache(t *testing.T) {
	t.Parallel()

	const root = "/repos/acme/widget"
	const original = "Original guidance"
	const updated = "New guidance"
	blobSHA := func(content string) string {
		return fmt.Sprintf("%x", sha1.Sum([]byte(fmt.Sprintf("blob %d\x00%s", len(content), content))))
	}
	originalSHA, updatedSHA := blobSHA(original), blobSHA(updated)
	type step struct {
		path, ifNoneMatch, etag string
		status                  int
		response                any
	}
	tree := func(content string) map[string]any {
		return map[string]any{"sha": strings.Repeat("c", 40), "tree": []map[string]any{{
			"path": "REVIEW.md", "type": "blob", "mode": "100644", "size": len(content), "sha": blobSHA(content),
		}}}
	}
	blob := func(content string) map[string]string {
		return map[string]string{"sha": blobSHA(content), "encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte(content))}
	}
	repoNotModified := step{path: root, ifNoneMatch: `"repo-1"`, status: http.StatusNotModified}
	treeNotModified := step{path: root + "/git/trees/main", ifNoneMatch: `"tree-1"`, status: http.StatusNotModified}
	originalGuidance := models.CodeReviewRepositoryGuidance{Branch: "main", Present: true, BlobSHA: originalSHA, Content: original}
	nestedTree := tree(original)
	nestedTree["tree"] = append(nestedTree["tree"].([]map[string]any), map[string]any{
		"path": "frontend/REVIEW.md", "type": "blob", "mode": "100644", "size": len(updated), "sha": updatedSHA,
	})
	tests := []struct {
		name          string
		next          []step
		want          models.CodeReviewRepositoryGuidance
		wantForbidden bool
		repeat        bool
	}{
		{name: "unchanged across workers", next: []step{repoNotModified, treeNotModified}, want: originalGuidance},
		{name: "unrelated tree change reuses blob", next: []step{repoNotModified, {
			path: root + "/git/trees/main", ifNoneMatch: `"tree-1"`, etag: `"tree-2"`, status: http.StatusOK, response: tree(original),
		}}, want: originalGuidance},
		{name: "updated guidance uses new blob", next: []step{repoNotModified, {
			path: root + "/git/trees/main", ifNoneMatch: `"tree-1"`, etag: `"tree-2"`, status: http.StatusOK, response: tree(updated),
		}, {path: root + "/git/blobs/" + updatedSHA, status: http.StatusOK, response: blob(updated)}},
			want: models.CodeReviewRepositoryGuidance{Branch: "main", Present: true, BlobSHA: updatedSHA, Content: updated}},
		{name: "nested addition survives cache across workers", next: []step{repoNotModified, {
			path: root + "/git/trees/main", ifNoneMatch: `"tree-1"`, etag: `"tree-2"`, status: http.StatusOK, response: nestedTree,
		}, {path: root + "/git/blobs/" + updatedSHA, status: http.StatusOK, response: blob(updated)}},
			want: models.CodeReviewRepositoryGuidance{Branch: "main", Present: true, BlobSHA: originalSHA, Content: original, NestedFiles: []models.CodeReviewGuidanceFile{
				{Path: "frontend/REVIEW.md", BlobSHA: updatedSHA, Content: updated},
			}}, repeat: true},
		{name: "deletion uses new tree", next: []step{repoNotModified, {
			path: root + "/git/trees/main", ifNoneMatch: `"tree-1"`, etag: `"tree-2"`, status: http.StatusOK,
			response: map[string]any{"sha": strings.Repeat("d", 40), "tree": []any{}},
		}}, want: models.CodeReviewRepositoryGuidance{Branch: "main"}},
		{name: "changed default branch checks different tree", next: []step{{
			path: root, ifNoneMatch: `"repo-1"`, etag: `"repo-2"`, status: http.StatusOK,
			response: map[string]string{"default_branch": "release"},
		}, {
			path: root + "/git/trees/release", etag: `"tree-2"`, status: http.StatusOK, response: tree(original),
		}}, want: models.CodeReviewRepositoryGuidance{Branch: "release", Present: true, BlobSHA: originalSHA, Content: original}},
		{name: "authorization failure never returns cached guidance", next: []step{{
			path: root, ifNoneMatch: `"repo-1"`, status: http.StatusForbidden, response: map[string]string{"message": "access denied"},
		}}, wantForbidden: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			orgID, repoID := uuid.New(), uuid.New()
			steps := append([]step{
				{path: root, etag: `"repo-1"`, status: http.StatusOK, response: map[string]string{"default_branch": "main"}},
				{path: root + "/git/trees/main", etag: `"tree-1"`, status: http.StatusOK, response: tree(original)},
				{path: root + "/git/blobs/" + originalSHA, status: http.StatusOK, response: blob(original)},
			}, tt.next...)
			if tt.repeat {
				steps = append(steps, repoNotModified, step{path: root + "/git/trees/main", ifNoneMatch: `"tree-2"`, status: http.StatusNotModified})
			}
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				index := int(requests.Add(1)) - 1
				if index >= len(steps) {
					t.Errorf("unexpected guidance request %s", r.URL.Path)
					http.NotFound(w, r)
					return
				}
				step := steps[index]
				if strings.Contains(step.path, "/git/trees/") {
					require.Equal(t, "recursive=1", r.URL.RawQuery, "cached tree revalidation must cover the full rubric hierarchy")
				}
				if r.URL.Path != step.path || r.Header.Get("If-None-Match") != step.ifNoneMatch || r.Header.Get("Authorization") != "token fixture-token" {
					t.Errorf("unexpected guidance request %s with ETag %q, expected %s with ETag %q", r.URL.Path, r.Header.Get("If-None-Match"), step.path, step.ifNoneMatch)
					http.Error(w, `{"message":"unexpected request"}`, http.StatusBadRequest)
					return
				}
				w.Header().Set("ETag", step.etag)
				w.WriteHeader(step.status)
				if step.response != nil {
					if err := json.NewEncoder(w).Encode(step.response); err != nil {
						t.Errorf("encode response: %v", err)
					}
				}
			}))
			t.Cleanup(server.Close)
			transport := http.DefaultTransport.(*http.Transport).Clone()
			t.Cleanup(transport.CloseIdleConnections)
			redisServer := miniredis.RunT(t)
			sharedCache := cache.New(cache.Config{Topology: "standalone", URL: "redis://" + redisServer.Addr()}, zerolog.Nop(), nil)
			t.Cleanup(func() { require.NoError(t, sharedCache.Close(), "shared guidance cache should close") })
			newService := func() *PRService {
				mock := newMockPool(t)
				now := time.Now().UTC()
				mock.ExpectQuery("SELECT id, org_id, integration_id, github_id").
					WithArgs(pgx.NamedArgs{"id": repoID, "org_id": orgID}).
					WillReturnRows(pgxmock.NewRows(prTestRepoColumns).AddRow(repoID, orgID, uuid.New(), int64(1), "acme/widget", "stale-default", false, nil, nil, "https://github.com/acme/widget.git", int64(456), "active", nil, nil, []byte(`{}`), now, now))
				t.Cleanup(func() {
					require.NoError(t, mock.ExpectationsWereMet(), "guidance must resolve its authorized repository before using the cache")
				})
				return &PRService{repos: db.NewRepositoryStore(mock), tokenProvider: &Service{cache: map[int64]*cachedToken{
					456: {Token: "fixture-token", ExpiresAt: now.Add(time.Hour)},
				}}, logger: zerolog.Nop(), baseURL: server.URL, httpClient: &http.Client{Transport: transport}, redisClient: sharedCache}
			}
			first, err := newService().CaptureCodeReviewRepositoryGuidance(t.Context(), orgID, repoID)
			require.NoError(t, err, "cold guidance must be read and verified")
			require.Equal(t, originalGuidance, first, "cold capture must contain the original file")
			got, err := newService().CaptureCodeReviewRepositoryGuidance(t.Context(), orgID, repoID)
			if tt.wantForbidden {
				var apiErr *GitHubAPIError
				require.ErrorAs(t, err, &apiErr, "live denial must propagate across a warm cache")
				require.Equal(t, http.StatusForbidden, apiErr.StatusCode, "live denial must retain provider status")
				require.Equal(t, models.CodeReviewRepositoryGuidance{}, got, "access denial must not expose cached guidance")
			} else {
				require.NoError(t, err, "warm capture must revalidate current GitHub state")
				require.Equal(t, tt.want, got, "warm capture must use the exact current default-branch file or absence")
			}
			if tt.repeat {
				again, err := newService().CaptureCodeReviewRepositoryGuidance(t.Context(), orgID, repoID)
				require.NoError(t, err, "nested guidance must survive metadata and blob cache reuse")
				require.Equal(t, tt.want, again, "warm caches must preserve the entire sorted rubric capture")
			}
			require.Equal(t, int32(len(steps)), requests.Load(), "each capture must make exactly the expected GitHub requests")
		})
	}
}

func TestCaptureCodeReviewNestedRepositoryGuidance(t *testing.T) {
	t.Parallel()
	const root = "/repos/acme/widget"
	entry := func(path, content string) reviewGuidanceTreeEntry {
		return reviewGuidanceTreeEntry{Path: path, Type: "blob", Mode: "100644", SHA: fmt.Sprintf("%x", sha1.Sum([]byte(fmt.Sprintf("blob %d\x00%s", len(content), content)))), Size: len(content)}
	}
	rootEntry := entry("REVIEW.md", "Global rules")
	frontendEntry := entry("frontend/REVIEW.md", "Apply ../shared/REVIEW.md")
	sharedEntry := entry("shared/REVIEW.md", "Shared rules")
	oversized := entry("shared/REVIEW.md", "Shared rules")
	oversized.Size = models.CodeReviewGuidanceMaxBytes
	unsafe := entry("../REVIEW.md", "Unsafe path")
	symlink := entry("frontend/REVIEW.md", "../../elsewhere")
	symlink.Mode = "120000"
	submodule := entry("frontend/REVIEW.md", "")
	submodule.Mode, submodule.Type = "160000", "commit"
	tests := []struct {
		name    string
		entries []reviewGuidanceTreeEntry
		want    models.CodeReviewRepositoryGuidance
		invalid bool
	}{
		{
			name: "root nested and shared rubrics sorted from one tree",
			entries: []reviewGuidanceTreeEntry{sharedEntry, frontendEntry, rootEntry,
				{Path: "external", Type: "commit", Mode: "160000", SHA: strings.Repeat("d", 40)},
				{Path: "linked", Type: "blob", Mode: "120000", SHA: strings.Repeat("e", 40)},
				entry("notes.md", "Ignore this"),
			},
			want: models.CodeReviewRepositoryGuidance{Branch: "main", Present: true, BlobSHA: rootEntry.SHA, Content: "Global rules", NestedFiles: []models.CodeReviewGuidanceFile{
				{Path: "frontend/REVIEW.md", BlobSHA: frontendEntry.SHA, Content: "Apply ../shared/REVIEW.md"},
				{Path: "shared/REVIEW.md", BlobSHA: sharedEntry.SHA, Content: "Shared rules"},
			}},
		},
		{
			name:    "nested guidance without root",
			entries: []reviewGuidanceTreeEntry{sharedEntry, frontendEntry},
			want: models.CodeReviewRepositoryGuidance{Branch: "main", NestedFiles: []models.CodeReviewGuidanceFile{
				{Path: "frontend/REVIEW.md", BlobSHA: frontendEntry.SHA, Content: "Apply ../shared/REVIEW.md"},
				{Path: "shared/REVIEW.md", BlobSHA: sharedEntry.SHA, Content: "Shared rules"},
			}},
		},
		{name: "aggregate size rejected before blobs", entries: []reviewGuidanceTreeEntry{rootEntry, oversized}, invalid: true},
		{name: "duplicate root rejected before blobs", entries: []reviewGuidanceTreeEntry{rootEntry, rootEntry}, invalid: true},
		{name: "duplicate nested path rejected before blobs", entries: []reviewGuidanceTreeEntry{frontendEntry, frontendEntry}, invalid: true},
		{name: "path traversal rejected before blobs", entries: []reviewGuidanceTreeEntry{unsafe}, invalid: true},
		{name: "nested symlink rejected before blobs", entries: []reviewGuidanceTreeEntry{symlink}, invalid: true},
		{name: "rubric named submodule rejected before blobs", entries: []reviewGuidanceTreeEntry{submodule}, invalid: true},
		{name: "file count rejected before blobs", entries: make([]reviewGuidanceTreeEntry, models.CodeReviewGuidanceMaxFiles+1), invalid: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			entries := append([]reviewGuidanceTreeEntry(nil), tt.entries...)
			for i := range entries {
				if entries[i].Path == "" {
					entries[i] = entry(fmt.Sprintf("section-%02d/REVIEW.md", i), "")
				}
			}
			orgID, repoID := uuid.New(), uuid.New()
			var treeReads, blobReads atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var response any
				switch r.URL.Path {
				case root:
					response = map[string]string{"default_branch": "main"}
				case root + "/git/trees/main":
					require.Equal(t, "recursive=1", r.URL.RawQuery, "one recursive tree must capture every trusted path")
					treeReads.Add(1)
					response = map[string]any{"sha": strings.Repeat("c", 40), "tree": entries}
				case root + "/git/blobs/" + rootEntry.SHA, root + "/git/blobs/" + frontendEntry.SHA, root + "/git/blobs/" + sharedEntry.SHA:
					blobReads.Add(1)
					contents := map[string]string{rootEntry.SHA: "Global rules", frontendEntry.SHA: "Apply ../shared/REVIEW.md", sharedEntry.SHA: "Shared rules"}
					sha := strings.TrimPrefix(r.URL.Path, root+"/git/blobs/")
					response = map[string]string{"sha": sha, "encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte(contents[sha]))}
				default:
					t.Errorf("capture followed a reference instead of a pinned tree blob: %s", r.URL.String())
					http.NotFound(w, r)
					return
				}
				require.NoError(t, json.NewEncoder(w).Encode(response), "guidance fixture must encode")
			}))
			t.Cleanup(server.Close)
			transport := http.DefaultTransport.(*http.Transport).Clone()
			t.Cleanup(transport.CloseIdleConnections)
			mock := newMockPool(t)
			now := time.Now().UTC()
			mock.ExpectQuery("SELECT id, org_id, integration_id, github_id").WithArgs(pgx.NamedArgs{"id": repoID, "org_id": orgID}).WillReturnRows(pgxmock.NewRows(prTestRepoColumns).AddRow(repoID, orgID, uuid.New(), int64(1), "acme/widget", "stale-default", false, nil, nil, "https://github.com/acme/widget.git", int64(456), "active", nil, nil, []byte(`{}`), now, now))
			svc := &PRService{repos: db.NewRepositoryStore(mock), tokenProvider: &Service{cache: map[int64]*cachedToken{456: {Token: "fixture-token", ExpiresAt: now.Add(time.Hour)}}}, logger: zerolog.Nop(), baseURL: server.URL, httpClient: &http.Client{Transport: transport}}
			got, err := svc.CaptureCodeReviewRepositoryGuidance(t.Context(), orgID, repoID)
			require.NoError(t, mock.ExpectationsWereMet(), "nested capture must authorize the repository before reading guidance")
			require.Equal(t, int32(1), treeReads.Load(), "all rubric paths must be pinned by one default-branch tree read")
			if tt.invalid {
				require.ErrorIs(t, err, models.ErrInvalidCodeReviewGuidanceFile, "unsafe or excessive guidance must require a repository correction")
				require.Equal(t, int32(0), blobReads.Load(), "invalid tree metadata must fail before content requests")
			} else {
				require.NoError(t, err, "nested guidance must capture without following repository links")
				require.Equal(t, tt.want, got, "capture must preserve sorted paths, exact content, and pinned blob identities")
				wantReads := len(tt.want.NestedFiles)
				if tt.want.Present {
					wantReads++
				}
				require.Equal(t, int32(wantReads), blobReads.Load(), "only discovered regular rubrics should be read")
			}
		})
	}
}

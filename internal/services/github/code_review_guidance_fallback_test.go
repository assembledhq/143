package github

import (
	"crypto/sha1" // #nosec G505 -- Git fixture identities require SHA-1.
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

type guidanceFallbackResponse struct {
	body   any
	status int
}

func guidanceFallbackBlob(path, content string) reviewGuidanceTreeEntry {
	digest := sha1.Sum([]byte(fmt.Sprintf("blob %d\x00%s", len(content), content))) // #nosec G401 -- Git fixture identities require SHA-1.
	return reviewGuidanceTreeEntry{Path: path, Mode: "100644", Type: "blob", SHA: fmt.Sprintf("%x", digest), Size: len(content)}
}

func guidanceFallbackTree(sha string, truncated bool, entries ...reviewGuidanceTreeEntry) map[string]any {
	if entries == nil {
		entries = []reviewGuidanceTreeEntry{}
	}
	return map[string]any{"sha": sha, "truncated": truncated, "tree": entries}
}

func guidanceFallbackBlobResponse(entry reviewGuidanceTreeEntry, content string) map[string]string {
	return map[string]string{"sha": entry.SHA, "encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte(content))}
}

func newGuidanceFallbackHTTPFixture(t *testing.T, responses map[string]guidanceFallbackResponse) (string, *http.Client, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.URL.RequestURI())
		mu.Unlock()
		response, ok := responses[r.URL.RequestURI()]
		if !ok {
			t.Errorf("unexpected tree traversal request: %s", r.URL.RequestURI())
			http.NotFound(w, r)
			return
		}
		status := response.status
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		require.NoError(t, json.NewEncoder(w).Encode(response.body), "synthetic tree response must encode")
	}))
	t.Cleanup(server.Close)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	t.Cleanup(transport.CloseIdleConnections)
	return server.URL, &http.Client{Transport: transport}, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), requests...)
	}
}

func newGuidanceFallbackService(t *testing.T, baseURL string, client *http.Client, orgID, repoID uuid.UUID) *PRService {
	t.Helper()
	mock := newMockPool(t)
	now := time.Now().UTC()
	mock.ExpectQuery("SELECT id, org_id, integration_id, github_id").WithArgs(pgx.NamedArgs{"id": repoID, "org_id": orgID}).WillReturnRows(pgxmock.NewRows(prTestRepoColumns).AddRow(repoID, orgID, uuid.New(), int64(1), "acme/widget", "stale-default", false, nil, nil, "https://github.com/acme/widget.git", int64(456), "active", nil, nil, []byte(`{}`), now, now))
	t.Cleanup(func() {
		require.NoError(t, mock.ExpectationsWereMet(), "fallback capture must resolve the tenant-scoped repository")
	})
	return &PRService{repos: db.NewRepositoryStore(mock), tokenProvider: &Service{cache: map[int64]*cachedToken{456: {Token: "fixture-token", ExpiresAt: now.Add(time.Hour)}}}, logger: zerolog.Nop(), baseURL: baseURL, httpClient: client}
}

func TestCaptureCodeReviewGuidanceFallback(t *testing.T) {
	t.Parallel()
	const root = "/repos/acme/widget"
	tests := []struct {
		name                              string
		rootFile, nested, split, repeated bool
	}{
		{name: "root and nested", rootFile: true, nested: true},
		{name: "nested without root", nested: true},
		{name: "no rubrics"},
		{name: "split truncated child", rootFile: true, nested: true, split: true},
		{name: "same subtree mounted twice", nested: true, repeated: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rootSHA, childSHA, leafSHA := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
			rootFile := guidanceFallbackBlob("REVIEW.md", "Root rules")
			areaFile := guidanceFallbackBlob("REVIEW.md", "Area rules")
			partial := guidanceFallbackBlob("discarded/REVIEW.md", "Discard this partial tree")
			partial.Mode = "120000"
			rootEntries := []reviewGuidanceTreeEntry{{Path: "ignored.txt", Type: "blob", Mode: "100644", SHA: strings.Repeat("d", 40)}}
			want := models.CodeReviewRepositoryGuidance{Branch: "main"}
			responses := map[string]guidanceFallbackResponse{
				root:                                 {body: map[string]string{"default_branch": "main"}},
				root + "/git/trees/main?recursive=1": {body: guidanceFallbackTree(rootSHA, true, partial)},
				root + "/git/blobs/" + rootFile.SHA:  {body: guidanceFallbackBlobResponse(rootFile, "Root rules")},
				root + "/git/blobs/" + areaFile.SHA:  {body: guidanceFallbackBlobResponse(areaFile, "Area rules")},
			}
			if tt.rootFile {
				rootEntries = append(rootEntries, rootFile)
				want.Present, want.BlobSHA, want.Content = true, rootFile.SHA, "Root rules"
			}
			if tt.nested {
				rootEntries = append(rootEntries, reviewGuidanceTreeEntry{Path: "api", Mode: "040000", Type: "tree", SHA: childSHA})
				filePath := "api/REVIEW.md"
				if tt.split {
					responses[root+"/git/trees/"+childSHA+"?recursive=1"] = guidanceFallbackResponse{body: guidanceFallbackTree(childSHA, true, partial)}
					responses[root+"/git/trees/"+childSHA] = guidanceFallbackResponse{body: guidanceFallbackTree(childSHA, false, reviewGuidanceTreeEntry{Path: "storage", Mode: "040000", Type: "tree", SHA: leafSHA})}
					responses[root+"/git/trees/"+leafSHA+"?recursive=1"] = guidanceFallbackResponse{body: guidanceFallbackTree(leafSHA, false, areaFile)}
					filePath = "api/storage/REVIEW.md"
				} else {
					responses[root+"/git/trees/"+childSHA+"?recursive=1"] = guidanceFallbackResponse{body: guidanceFallbackTree(childSHA, false, areaFile)}
				}
				want.NestedFiles = []models.CodeReviewGuidanceFile{{Path: filePath, BlobSHA: areaFile.SHA, Content: "Area rules"}}
			}
			if tt.repeated {
				rootEntries = append(rootEntries, reviewGuidanceTreeEntry{Path: "web", Mode: "040000", Type: "tree", SHA: childSHA})
				want.NestedFiles = append(want.NestedFiles, models.CodeReviewGuidanceFile{Path: "web/REVIEW.md", BlobSHA: areaFile.SHA, Content: "Area rules"})
			}
			responses[root+"/git/trees/"+rootSHA] = guidanceFallbackResponse{body: guidanceFallbackTree(rootSHA, false, rootEntries...)}
			baseURL, client, requests := newGuidanceFallbackHTTPFixture(t, responses)
			orgID, repoID := uuid.New(), uuid.New()
			got, err := newGuidanceFallbackService(t, baseURL, client, orgID, repoID).CaptureCodeReviewRepositoryGuidance(t.Context(), orgID, repoID)
			require.NoError(t, err, "complete pinned subtree traversal must recover a truncated recursive response")
			require.Equal(t, want, got, "partial guidance must be discarded and complete mounted paths must be preserved")
			branchReads := 0
			for _, request := range requests() {
				if request == root+"/git/trees/main?recursive=1" {
					branchReads++
				}
			}
			require.Equal(t, 1, branchReads, "branch movement must not change the pinned root used during fallback")
		})
	}
}

func TestResolveCodeReviewGuidanceFallbackFailures(t *testing.T) {
	t.Parallel()
	const root = "/repos/acme/widget"
	tests := []struct {
		name                                    string
		requests, entries, responseBytes, depth int
		initialEntries, initialBytes            int
		shallowEntries                          []reviewGuidanceTreeEntry
		shallowSHA                              string
		truncated                               bool
		status                                  int
		wantRequests                            int
	}{
		{name: "initial request counts", requests: 1},
		{name: "initial entry count counts", entries: 1, initialEntries: 2},
		{name: "initial byte count counts", responseBytes: 1, initialBytes: 2},
		{name: "aggregate entry budget", entries: 1, initialEntries: 1, shallowEntries: []reviewGuidanceTreeEntry{{Path: "README.md", Type: "blob", Mode: "100644", SHA: strings.Repeat("d", 40)}}, wantRequests: 1},
		{name: "aggregate byte budget", responseBytes: 120, initialBytes: 100, wantRequests: 1},
		{name: "bounded pending children", requests: 3, shallowEntries: []reviewGuidanceTreeEntry{{Path: "api", Type: "tree", Mode: "040000", SHA: strings.Repeat("b", 40)}, {Path: "web", Type: "tree", Mode: "040000", SHA: strings.Repeat("c", 40)}}, wantRequests: 1},
		{name: "depth budget", depth: -1, shallowEntries: []reviewGuidanceTreeEntry{{Path: "api", Type: "tree", Mode: "040000", SHA: strings.Repeat("b", 40)}}, wantRequests: 1},
		{name: "uninspectable shallow tree", truncated: true, wantRequests: 1},
		{name: "tree identity mismatch", shallowSHA: strings.Repeat("e", 40), wantRequests: 1},
		{name: "child path traversal", shallowEntries: []reviewGuidanceTreeEntry{{Path: "../api", Type: "tree", Mode: "040000", SHA: strings.Repeat("b", 40)}}, wantRequests: 1},
		{name: "nonrecursive child has slash", shallowEntries: []reviewGuidanceTreeEntry{{Path: "api/storage", Type: "tree", Mode: "040000", SHA: strings.Repeat("b", 40)}}, wantRequests: 1},
		{name: "invalid child SHA", shallowEntries: []reviewGuidanceTreeEntry{{Path: "api", Type: "tree", Mode: "040000", SHA: "not-a-sha"}}, wantRequests: 1},
		{name: "directory mode with blob type", shallowEntries: []reviewGuidanceTreeEntry{{Path: "api", Type: "blob", Mode: "040000", SHA: strings.Repeat("b", 40)}}, wantRequests: 1},
		{name: "provider failure retains classification", status: http.StatusServiceUnavailable, wantRequests: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rootSHA := strings.Repeat("a", 40)
			sha := tt.shallowSHA
			if sha == "" {
				sha = rootSHA
			}
			limits := reviewGuidanceTraversalLimits{Requests: 128, Entries: 1000000, ResponseBytes: 64 * 1024 * 1024, Depth: 128}
			if tt.requests > 0 {
				limits.Requests = tt.requests
			}
			if tt.entries > 0 {
				limits.Entries = tt.entries
			}
			if tt.responseBytes > 0 {
				limits.ResponseBytes = tt.responseBytes
			}
			if tt.depth < 0 {
				limits.Depth = 0
			}
			response := guidanceFallbackResponse{body: guidanceFallbackTree(sha, tt.truncated, tt.shallowEntries...), status: tt.status}
			if tt.status != 0 {
				response.body = map[string]string{"message": "temporarily unavailable"}
			}
			baseURL, client, requests := newGuidanceFallbackHTTPFixture(t, map[string]guidanceFallbackResponse{root + "/git/trees/" + rootSHA: response})
			svc := &PRService{baseURL: baseURL, httpClient: client, logger: zerolog.Nop()}
			initial := reviewGuidanceTree{SHA: rootSHA, Truncated: true, EntryCount: tt.initialEntries, ResponseBytes: tt.initialBytes}
			got, err := svc.resolveReviewGuidanceTree(t.Context(), "fixture-token", root, "test-cache", "main", initial, limits)
			if tt.status != 0 {
				var apiErr *GitHubAPIError
				require.ErrorAs(t, err, &apiErr, "transient provider errors must preserve retry classification")
				require.Equal(t, tt.status, apiErr.StatusCode, "provider failure must retain the actual status")
				require.NotErrorIs(t, err, models.ErrIncompleteCodeReviewGuidance, "provider failures must not become deterministic traversal failures")
			} else {
				require.ErrorIs(t, err, models.ErrIncompleteCodeReviewGuidance, "uninspectable or bounded traversal must fail closed")
			}
			require.Nil(t, got, "failed traversal must never return partial rubric entries")
			require.Equal(t, tt.wantRequests, len(requests()), "traversal must stop before excess requests or unsafe child lookups")
		})
	}
}

func TestCaptureCodeReviewGuidanceFallbackCache(t *testing.T) {
	t.Parallel()
	const root = "/repos/acme/widget"
	oldRoot, newRoot, oldChild, newChild := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40), strings.Repeat("d", 40)
	oldFile, newFile := guidanceFallbackBlob("REVIEW.md", "Old area rules"), guidanceFallbackBlob("REVIEW.md", "New area rules")
	var phase atomic.Int32
	var mu sync.Mutex
	requests := map[int32][]string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := phase.Load()
		mu.Lock()
		requests[current] = append(requests[current], r.URL.RequestURI())
		mu.Unlock()
		var response any
		switch r.URL.RequestURI() {
		case root:
			w.Header().Set("ETag", `"repo"`)
			if current > 0 {
				require.Equal(t, `"repo"`, r.Header.Get("If-None-Match"), "new services must revalidate repository access")
				w.WriteHeader(http.StatusNotModified)
				return
			}
			response = map[string]string{"default_branch": "main"}
		case root + "/git/trees/main?recursive=1":
			if current == 1 {
				require.Equal(t, `"old-tree"`, r.Header.Get("If-None-Match"), "warm fallback must revalidate the current root")
				w.WriteHeader(http.StatusNotModified)
				return
			}
			sha, etag := oldRoot, `"old-tree"`
			if current == 2 {
				sha, etag = newRoot, `"new-tree"`
			}
			w.Header().Set("ETag", etag)
			response = guidanceFallbackTree(sha, true)
		case root + "/git/trees/" + oldRoot:
			response = guidanceFallbackTree(oldRoot, false, reviewGuidanceTreeEntry{Path: "api", Mode: "040000", Type: "tree", SHA: oldChild})
		case root + "/git/trees/" + newRoot:
			response = guidanceFallbackTree(newRoot, false, reviewGuidanceTreeEntry{Path: "api", Mode: "040000", Type: "tree", SHA: newChild})
		case root + "/git/trees/" + oldChild + "?recursive=1":
			response = guidanceFallbackTree(oldChild, false, oldFile)
		case root + "/git/trees/" + newChild + "?recursive=1":
			response = guidanceFallbackTree(newChild, false, newFile)
		case root + "/git/blobs/" + oldFile.SHA:
			response = guidanceFallbackBlobResponse(oldFile, "Old area rules")
		case root + "/git/blobs/" + newFile.SHA:
			response = guidanceFallbackBlobResponse(newFile, "New area rules")
		default:
			t.Errorf("unexpected fallback cache request: %s", r.URL.RequestURI())
			http.NotFound(w, r)
			return
		}
		require.NoError(t, json.NewEncoder(w).Encode(response), "cache fixture response must encode")
	}))
	t.Cleanup(server.Close)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	t.Cleanup(transport.CloseIdleConnections)
	redisServer := miniredis.RunT(t)
	sharedCache := cache.New(cache.Config{Topology: "standalone", URL: "redis://" + redisServer.Addr()}, zerolog.Nop(), nil)
	t.Cleanup(func() { require.NoError(t, sharedCache.Close(), "fallback cache fixture should close") })
	orgID, repoID := uuid.New(), uuid.New()
	for _, current := range []int32{0, 1, 2} {
		phase.Store(current)
		svc := newGuidanceFallbackService(t, server.URL, &http.Client{Transport: transport}, orgID, repoID)
		svc.redisClient = sharedCache
		got, err := svc.CaptureCodeReviewRepositoryGuidance(t.Context(), orgID, repoID)
		require.NoError(t, err, "fallback capture must survive revalidation across services")
		file, content := oldFile, "Old area rules"
		if current == 2 {
			file, content = newFile, "New area rules"
		}
		require.Equal(t, models.CodeReviewRepositoryGuidance{Branch: "main", NestedFiles: []models.CodeReviewGuidanceFile{{Path: "api/REVIEW.md", BlobSHA: file.SHA, Content: content}}}, got, "resolved discoveries must be keyed by the pinned root identity")
	}
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []string{root, root + "/git/trees/main?recursive=1"}, requests[1], "validated unchanged roots must reuse the complete traversal and blob cache")
	require.Contains(t, requests[0], root+"/git/trees/"+oldRoot, "cold capture must fully inspect the pinned old root")
	require.Contains(t, requests[2], root+"/git/trees/"+newRoot, "changed roots must not reuse the old resolved discovery")
	require.NotContains(t, requests[2], root+"/git/trees/"+oldRoot, "branch movement must select only the new pinned tree")
}

func TestResolveCodeReviewGuidanceFallbackDoesNotCachePartial(t *testing.T) {
	t.Parallel()
	const root = "/repos/acme/widget"
	rootSHA, childSHA := strings.Repeat("a", 40), strings.Repeat("b", 40)
	rootFile, childFile := guidanceFallbackBlob("REVIEW.md", "Root rules"), guidanceFallbackBlob("REVIEW.md", "Child rules")
	var repaired atomic.Bool
	var rootReads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var response any
		switch r.URL.RequestURI() {
		case root + "/git/trees/" + rootSHA:
			rootReads.Add(1)
			response = guidanceFallbackTree(rootSHA, false, rootFile, reviewGuidanceTreeEntry{Path: "api", Type: "tree", Mode: "040000", SHA: childSHA})
		case root + "/git/trees/" + childSHA + "?recursive=1":
			if !repaired.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				response = map[string]string{"message": "temporarily unavailable"}
			} else {
				response = guidanceFallbackTree(childSHA, false, childFile)
			}
		default:
			t.Errorf("unexpected partial-discovery request: %s", r.URL.RequestURI())
			http.NotFound(w, r)
			return
		}
		require.NoError(t, json.NewEncoder(w).Encode(response), "partial-discovery fixture must encode")
	}))
	t.Cleanup(server.Close)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	t.Cleanup(transport.CloseIdleConnections)
	redisServer := miniredis.RunT(t)
	sharedCache := cache.New(cache.Config{Topology: "standalone", URL: "redis://" + redisServer.Addr()}, zerolog.Nop(), nil)
	t.Cleanup(func() { require.NoError(t, sharedCache.Close(), "partial discovery cache should close") })
	svc := &PRService{baseURL: server.URL, httpClient: &http.Client{Transport: transport}, logger: zerolog.Nop(), redisClient: sharedCache}
	initial := reviewGuidanceTree{SHA: rootSHA, Truncated: true}
	limits := reviewGuidanceTraversalLimits{Requests: 128, Entries: 1000000, ResponseBytes: 64 * 1024 * 1024, Depth: 128}
	partial, err := svc.resolveReviewGuidanceTree(t.Context(), "fixture-token", root, "partial-cache", "main", initial, limits)
	var apiErr *GitHubAPIError
	require.ErrorAs(t, err, &apiErr, "failed child lookup must remain a provider error")
	require.Nil(t, partial, "an already discovered root rubric must not escape a failed traversal")
	repaired.Store(true)
	complete, err := svc.resolveReviewGuidanceTree(t.Context(), "fixture-token", root, "partial-cache", "main", initial, limits)
	require.NoError(t, err, "a later successful traversal must recover all guidance")
	childFile.Path = "api/REVIEW.md"
	require.Equal(t, []reviewGuidanceTreeEntry{rootFile, childFile}, complete, "failed discovery must not cache only the earlier root rubric")
	require.Equal(t, int32(2), rootReads.Load(), "retry must rediscover the root after incomplete capture")
	cached, err := svc.resolveReviewGuidanceTree(t.Context(), "fixture-token", root, "partial-cache", "main", initial, limits)
	require.NoError(t, err, "complete discovery should be reusable")
	require.Equal(t, complete, cached, "only the full resolved discovery may enter the cache")
	require.Equal(t, int32(2), rootReads.Load(), "the successful retry should seed the resolved cache")
}

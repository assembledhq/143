package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestDiscoverCodeReviewTextEvidence(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, body, comments string
		complete             bool
		wantSources          []CodeReviewTextSource
	}{
		{"human discussion and bot excluded", "Purpose\n", `[{"id":10,"html_url":"https://github.com/assembledhq/assembled/pull/42#issuecomment-10","body":"## Testing\nPassed\n","user":{"login":"alice","type":"User"}},{"id":11,"html_url":"https://github.com/assembledhq/assembled/pull/42#issuecomment-11","body":"bot output","user":{"login":"143-app[bot]","type":"Bot"}}]`, true, []CodeReviewTextSource{{Surface: models.CodeReviewEvidenceSurfaceIssueComment, ProviderObjectID: "10", SourceURL: "https://github.com/assembledhq/assembled/pull/42#issuecomment-10", AuthorLogin: "alice", Body: "## Testing\nPassed\n"}}},
		{"oversized PR body incomplete", strings.Repeat("x", codeReviewTextMaxBytes+1), `[]`, false, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload string
				switch r.URL.Path {
				case "/repos/assembledhq/assembled/pulls/42":
					payload = fmt.Sprintf(`{"body":%q,"head":{"sha":"head-sha"}}`, tt.body)
				case "/repos/assembledhq/assembled/issues/42/comments":
					payload = tt.comments
				case "/repos/assembledhq/assembled/pulls/42/reviews", "/repos/assembledhq/assembled/pulls/42/comments":
					payload = `[]`
				default:
					http.NotFound(w, r)
					return
				}
				_, err := fmt.Fprint(w, payload)
				require.NoError(t, err, "GitHub text fixture response should write")
			}))
			t.Cleanup(server.Close)
			orgID, repoID := uuid.New(), uuid.New()
			service, mock := newVisualEvidenceTestService(t, server, orgID, repoID)
			got, err := service.DiscoverCodeReviewTextEvidence(context.Background(), orgID, repoID, 42)
			require.NoError(t, err, "text evidence discovery should return bounded provider inventory")
			require.Equal(t, tt.complete, got.Complete, "capture completeness should reflect source budgets")
			if tt.complete {
				require.Equal(t, tt.body, got.Body, "raw PR body should be retained exactly")
				require.Equal(t, tt.wantSources, got.Sources, "only human discussion should be citable")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "repository lookup should be org scoped")
		})
	}
}

func TestDiscoverCodeReviewEvidenceSharesInventory(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                                 string
		body                                 string
		issueCount, reviewCount, inlineCount int
		complete                             bool
		requests                             int32
	}{
		{name: "all human surfaces and bot filtering", body: "Purpose\n", issueCount: 1, reviewCount: 1, inlineCount: 1, complete: true, requests: 4},
		{name: "pagination shared by text and images", body: "Purpose\n", issueCount: 101, reviewCount: 1, inlineCount: 1, complete: true, requests: 5},
		{name: "oversized description remains incomplete", body: strings.Repeat("x", codeReviewTextMaxBytes+1), issueCount: 1, complete: false, requests: 4},
		{name: "total item budget across surfaces", body: "Purpose\n", issueCount: 201, reviewCount: 201, inlineCount: 101, complete: false, requests: 9},
		{name: "single surface item budget", body: "Purpose\n", issueCount: 501, complete: false, requests: 9},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				require.Equal(t, githubFullJSONMediaType, r.Header.Get("Accept"), "shared inventory must include both raw Markdown and rendered HTML")
				var response any
				if r.URL.Path == "/repos/assembledhq/assembled/pulls/42" {
					response = map[string]any{"number": 42, "html_url": "https://github.com/assembledhq/assembled/pull/42", "body": tt.body, "body_html": `<img src="https://example.test/description.png">`, "head": map[string]string{"sha": "head-sha"}}
				} else {
					count := 0
					switch r.URL.Path {
					case "/repos/assembledhq/assembled/issues/42/comments":
						count = tt.issueCount
					case "/repos/assembledhq/assembled/pulls/42/reviews":
						count = tt.reviewCount
					case "/repos/assembledhq/assembled/pulls/42/comments":
						count = tt.inlineCount
					default:
						http.NotFound(w, r)
						return
					}
					page, err := strconv.Atoi(r.URL.Query().Get("page"))
					require.NoError(t, err, "discussion inventory should request an explicit page")
					items := make([]map[string]any, 0)
					for index := (page - 1) * 100; index < count && index < page*100; index++ {
						actorType := "User"
						if index%3 == 1 {
							actorType = "Bot"
						} else if index%3 == 2 {
							actorType = "Mannequin"
						}
						items = append(items, map[string]any{"id": index + 1, "html_url": fmt.Sprintf("https://github.com/assembledhq/assembled/pull/42#item-%d", index), "body": fmt.Sprintf("Markdown %d", index), "body_html": fmt.Sprintf(`<img src="https://example.test/image-%d.png">`, index), "user": map[string]string{"login": "author", "type": actorType}})
					}
					response = items
				}
				require.NoError(t, json.NewEncoder(w).Encode(response), "shared evidence fixture should encode")
			}))
			t.Cleanup(server.Close)
			orgID, repoID := uuid.New(), uuid.New()
			service, mock := newVisualEvidenceTestService(t, server, orgID, repoID)
			combined, err := service.DiscoverCodeReviewEvidence(context.Background(), orgID, repoID, 42)
			require.NoError(t, err, "shared inventory should capture both evidence forms")
			require.Equal(t, tt.requests, requests.Load(), "each PR/discussion page should be fetched once for the combined capture")
			require.Equal(t, tt.complete, combined.Text.Complete, "combined text should preserve bounded inventory semantics")
			require.NoError(t, mock.ExpectationsWereMet(), "combined capture should load the repository once using its organization")

			textService, textMock := newVisualEvidenceTestService(t, server, orgID, repoID)
			text, err := textService.DiscoverCodeReviewTextEvidence(context.Background(), orgID, repoID, 42)
			require.NoError(t, err, "standalone text inventory should remain supported")
			require.Equal(t, text, combined.Text, "combined text should exactly match standalone evidence, including completeness and provenance")
			require.NoError(t, textMock.ExpectationsWereMet(), "standalone text lookup should remain scoped")
			visualService, visualMock := newVisualEvidenceTestService(t, server, orgID, repoID)
			visual, err := visualService.DiscoverCodeReviewVisualEvidence(context.Background(), orgID, repoID, 42)
			require.NoError(t, err, "standalone visual discovery should remain supported")
			visual.CapturedAt = combined.Visual.CapturedAt
			require.Equal(t, visual, combined.Visual, "combined images should exactly match standalone provenance, ordering, counts and limits")
			require.NoError(t, visualMock.ExpectationsWereMet(), "standalone visual lookup should remain scoped")
			if tt.complete {
				require.Equal(t, 3*tt.requests, requests.Load(), "separate text and visual discovery should require twice as many inventory reads as the combined capture")
			}
		})
	}
}

func TestDiscoverCodeReviewEvidenceRejectsUnavailableSurfaces(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, failedPath, payload string }{
		{name: "pull request unavailable", failedPath: "/pulls/42"},
		{name: "issue comments unavailable", failedPath: "/issues/42/comments"},
		{name: "reviews unavailable", failedPath: "/pulls/42/reviews"},
		{name: "inline comments unavailable", failedPath: "/pulls/42/comments"},
		{name: "malformed discussion", failedPath: "/pulls/42/comments", payload: `{`},
		{name: "missing PR head", failedPath: "/pulls/42", payload: `{"number":42}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				payload := `[]`
				if strings.HasSuffix(r.URL.Path, tt.failedPath) {
					if tt.payload == "" {
						http.Error(w, "unavailable", http.StatusServiceUnavailable)
						return
					}
					payload = tt.payload
				} else if r.URL.Path == "/repos/assembledhq/assembled/pulls/42" {
					payload = `{"number":42,"head":{"sha":"head-sha"}}`
				}
				_, err := fmt.Fprint(w, payload)
				require.NoError(t, err, "failure fixture should write")
			}))
			t.Cleanup(server.Close)
			orgID, repoID := uuid.New(), uuid.New()
			service, mock := newVisualEvidenceTestService(t, server, orgID, repoID)
			discovery, err := service.DiscoverCodeReviewEvidence(context.Background(), orgID, repoID, 42)
			require.Error(t, err, "any unavailable or invalid shared surface must fail the entire discovery")
			require.Equal(t, CodeReviewEvidenceDiscovery{}, discovery, "failed shared reads must never return partial text or visual evidence")
			require.NoError(t, mock.ExpectationsWereMet(), "failed capture should retain scoped repository lookup")
		})
	}
}

func TestCodeReviewTextFromInventoriesBoundsCombinedBytes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                               string
		bodyBytes, firstBytes, secondBytes int
		complete                           bool
	}{
		{name: "exact total budget", bodyBytes: 10, firstBytes: 100, secondBytes: codeReviewTextMaxBytes - 110, complete: true},
		{name: "aggregate discussion exceeds budget", firstBytes: codeReviewTextMaxBytes/2 + 1, secondBytes: codeReviewTextMaxBytes / 2, complete: false},
		{name: "body pushes discussion over budget", bodyBytes: 11, firstBytes: 100, secondBytes: codeReviewTextMaxBytes - 110, complete: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var first, second codeReviewTextInventory
			first.add(models.CodeReviewEvidenceSurfaceIssueComment, []githubVisualEvidenceContent{{Body: strings.Repeat("a", tt.firstBytes), User: &githubVisualEvidenceActor{Login: "author", Type: "User"}}})
			second.add(models.CodeReviewEvidenceSurfaceReviewBody, []githubVisualEvidenceContent{{Body: strings.Repeat("b", tt.secondBytes), User: &githubVisualEvidenceActor{Login: "reviewer", Type: "User"}}})
			actual := codeReviewTextFromInventories("head", strings.Repeat("x", tt.bodyBytes), first, second)
			require.Equal(t, tt.complete, actual.Complete, "aggregate byte budget must cover the PR body and every human discussion surface")
			if !tt.complete {
				require.Equal(t, CodeReviewTextDiscovery{HeadSHA: "head"}, actual, "incomplete inventory must discard partial evidence")
			}
		})
	}
}

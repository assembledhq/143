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
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

func TestDiscoverCodeReviewVisualEvidenceIncludesAllHumanSurfaces(t *testing.T) {
	t.Parallel()

	createdAt := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	updatedAt := createdAt.Add(time.Hour)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "token installation-token", r.Header.Get("Authorization"), "visual evidence discovery should use repository installation auth")
		require.Equal(t, githubFullJSONMediaType, r.Header.Get("Accept"), "visual evidence discovery should request GitHub-rendered HTML")

		var response string
		switch r.URL.Path {
		case "/repos/assembledhq/assembled/pulls/42":
			response = `{
				"number": 42,
				"html_url": "https://github.com/assembledhq/assembled/pull/42",
				"body_html": "<p>Desktop state <img src=\"https://github.com/user-attachments/assets/description\" alt=\"desktop\"></p>",
				"user": {"login": "automation-author", "type": "Bot"},
				"created_at": "2026-08-10T12:00:00Z",
				"updated_at": "2026-08-10T13:00:00Z",
				"head": {"sha": "head-sha"}
			}`
		case "/repos/assembledhq/assembled/issues/42/comments":
			response = `[
				{"id": 10, "html_url": "https://github.com/assembledhq/assembled/pull/42#issuecomment-10", "body_html": "<p>Mobile state <img src=\"/user-attachments/assets/issue\" alt=\"mobile\"></p>", "user": {"login": "human", "type": "User"}, "author_association": "CONTRIBUTOR", "created_at": "2026-08-10T12:00:00Z"},
				{"id": 11, "body_html": "<img src=\"https://example.com/bot.png\">", "user": {"login": "143-app[bot]", "type": "Bot"}},
				{"id": 12, "html_url": "https://github.com/assembledhq/assembled/pull/42#issuecomment-12", "body_html": "<figure><img srcset=\"https://example.com/small.png 1x, https://example.com/mannequin.png 2x\"><figcaption>Imported author evidence</figcaption></figure>", "user": {"login": "legacy-human", "type": "Mannequin"}, "author_association": "NONE", "created_at": "2026-08-10T12:05:00Z"},
				{"id": 13, "body_html": "<img src=\"https://example.com/org.png\">", "user": {"login": "assembledhq", "type": "Organization"}},
				{"id": 14, "body_html": "<img src=\"https://example.com/deleted.png\">", "user": null}
			]`
		case "/repos/assembledhq/assembled/pulls/42/reviews":
			response = `[
				{"id": 20, "html_url": "https://github.com/assembledhq/assembled/pull/42#pullrequestreview-20", "body_html": "<p>Reviewer capture <img src=\"https://example.com/review.png\"></p>", "user": {"login": "reviewer", "type": "User"}, "author_association": "MEMBER", "submitted_at": "2026-08-10T12:10:00Z"},
				{"id": 21, "body_html": "<img src=\"https://example.com/app.png\">", "user": {"login": "ci-app", "type": "App"}}
			]`
		case "/repos/assembledhq/assembled/pulls/42/comments":
			response = `[
				{"id": 30, "html_url": "https://github.com/assembledhq/assembled/pull/42#discussion_r30", "body_html": "<blockquote>Inline state <img src=\"//github.com/user-attachments/assets/inline\" alt=\"inline\"></blockquote>", "user": {"login": "outside-reviewer", "type": "User"}, "author_association": "NONE", "created_at": "2026-08-10T12:15:00Z"}
			]`
		default:
			http.NotFound(w, r)
			return
		}
		_, err := fmt.Fprint(w, response)
		require.NoError(t, err, "GitHub visual evidence fixture should be written")
	}))
	t.Cleanup(server.Close)

	orgID := uuid.New()
	repositoryID := uuid.New()
	service, mock := newVisualEvidenceTestService(t, server, orgID, repositoryID)

	discovery, err := service.DiscoverCodeReviewVisualEvidence(context.Background(), orgID, repositoryID, 42)

	require.NoError(t, err, "visual evidence discovery should read every supported GitHub surface")
	require.False(t, discovery.CapturedAt.IsZero(), "visual evidence discovery should record its immutable capture time")
	discovery.CapturedAt = time.Time{}
	require.Equal(t, models.CodeReviewVisualEvidenceDiscovery{
		Version:           codeReviewVisualEvidenceDiscoveryVersion,
		RepositoryID:      repositoryID,
		Repository:        "assembledhq/assembled",
		PullRequestNumber: 42,
		HeadSHA:           "head-sha",
		SourceCount:       5,
		Sources: []models.CodeReviewVisualEvidenceSource{
			{
				SourceID: codeReviewVisualEvidenceSourceID(models.CodeReviewEvidenceSurfaceDescription, "42", 1, "https://github.com/user-attachments/assets/description"),
				Surface:  models.CodeReviewEvidenceSurfaceDescription, ProviderObjectID: "42", SourceURL: "https://github.com/assembledhq/assembled/pull/42",
				AuthorLogin: "automation-author", AuthorType: models.CodeReviewEvidenceAuthorTypeBot, CreatedAt: &createdAt, UpdatedAt: &updatedAt,
				ImageIndex: 1, ImageURL: "https://github.com/user-attachments/assets/description", AltText: "desktop", ContextText: "Desktop state", Untrusted: true,
			},
			{
				SourceID: codeReviewVisualEvidenceSourceID(models.CodeReviewEvidenceSurfaceIssueComment, "10", 1, "https://github.com/user-attachments/assets/issue"),
				Surface:  models.CodeReviewEvidenceSurfaceIssueComment, ProviderObjectID: "10", SourceURL: "https://github.com/assembledhq/assembled/pull/42#issuecomment-10",
				AuthorLogin: "human", AuthorType: models.CodeReviewEvidenceAuthorTypeUser, AuthorAssociation: "CONTRIBUTOR", CreatedAt: &createdAt,
				ImageIndex: 1, ImageURL: "https://github.com/user-attachments/assets/issue", AltText: "mobile", ContextText: "Mobile state", Untrusted: true,
			},
			{
				SourceID: codeReviewVisualEvidenceSourceID(models.CodeReviewEvidenceSurfaceIssueComment, "12", 1, "https://example.com/mannequin.png"),
				Surface:  models.CodeReviewEvidenceSurfaceIssueComment, ProviderObjectID: "12", SourceURL: "https://github.com/assembledhq/assembled/pull/42#issuecomment-12",
				AuthorLogin: "legacy-human", AuthorType: models.CodeReviewEvidenceAuthorTypeMannequin, AuthorAssociation: "NONE", CreatedAt: ptrVisualEvidenceTime(createdAt.Add(5 * time.Minute)),
				ImageIndex: 1, ImageURL: "https://example.com/mannequin.png", ContextText: "Imported author evidence", Untrusted: true,
			},
			{
				SourceID: codeReviewVisualEvidenceSourceID(models.CodeReviewEvidenceSurfaceReviewBody, "20", 1, "https://example.com/review.png"),
				Surface:  models.CodeReviewEvidenceSurfaceReviewBody, ProviderObjectID: "20", SourceURL: "https://github.com/assembledhq/assembled/pull/42#pullrequestreview-20",
				AuthorLogin: "reviewer", AuthorType: models.CodeReviewEvidenceAuthorTypeUser, AuthorAssociation: "MEMBER", CreatedAt: ptrVisualEvidenceTime(createdAt.Add(10 * time.Minute)),
				ImageIndex: 1, ImageURL: "https://example.com/review.png", ContextText: "Reviewer capture", Untrusted: true,
			},
			{
				SourceID: codeReviewVisualEvidenceSourceID(models.CodeReviewEvidenceSurfaceReviewComment, "30", 1, "https://github.com/user-attachments/assets/inline"),
				Surface:  models.CodeReviewEvidenceSurfaceReviewComment, ProviderObjectID: "30", SourceURL: "https://github.com/assembledhq/assembled/pull/42#discussion_r30",
				AuthorLogin: "outside-reviewer", AuthorType: models.CodeReviewEvidenceAuthorTypeUser, AuthorAssociation: "NONE", CreatedAt: ptrVisualEvidenceTime(createdAt.Add(15 * time.Minute)),
				ImageIndex: 1, ImageURL: "https://github.com/user-attachments/assets/inline", AltText: "inline", ContextText: "Inline state", Untrusted: true,
			},
		},
	}, discovery, "discovery should include description evidence plus every human discussion image in deterministic surface order")
	require.NoError(t, mock.ExpectationsWereMet(), "visual evidence discovery should use an org-scoped repository lookup")
}

func TestDiscoverCodeReviewVisualEvidencePaginatesDiscussion(t *testing.T) {
	t.Parallel()

	var secondPageRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var response any
		switch r.URL.Path {
		case "/repos/assembledhq/assembled/pulls/42":
			response = map[string]any{"number": 42, "html_url": "https://github.com/assembledhq/assembled/pull/42", "body_html": "", "head": map[string]string{"sha": "head-sha"}}
		case "/repos/assembledhq/assembled/issues/42/comments":
			page, err := strconv.Atoi(r.URL.Query().Get("page"))
			require.NoError(t, err, "issue comment request should include a numeric page")
			if page == 1 {
				comments := make([]map[string]any, githubVisualEvidencePageSize)
				for index := range comments {
					comments[index] = map[string]any{"id": index + 1, "body_html": "", "user": map[string]string{"login": "human", "type": "User"}}
				}
				response = comments
			} else {
				secondPageRequests.Add(1)
				response = []map[string]any{{"id": 101, "html_url": "https://github.com/assembledhq/assembled/pull/42#issuecomment-101", "body_html": `<img src="https://example.com/page-two.png">`, "user": map[string]string{"login": "human", "type": "User"}}}
			}
		case "/repos/assembledhq/assembled/pulls/42/reviews", "/repos/assembledhq/assembled/pulls/42/comments":
			response = []any{}
		default:
			http.NotFound(w, r)
			return
		}
		require.NoError(t, json.NewEncoder(w).Encode(response), "paginated GitHub fixture should be encoded")
	}))
	t.Cleanup(server.Close)

	orgID := uuid.New()
	repositoryID := uuid.New()
	service, mock := newVisualEvidenceTestService(t, server, orgID, repositoryID)

	discovery, err := service.DiscoverCodeReviewVisualEvidence(context.Background(), orgID, repositoryID, 42)

	require.NoError(t, err, "visual evidence discovery should paginate until GitHub returns a short page")
	require.Equal(t, int32(1), secondPageRequests.Load(), "visual evidence discovery should request the second issue-comment page")
	require.Equal(t, 1, discovery.SourceCount, "visual evidence discovery should count images found across every page")
	require.Equal(t, []models.CodeReviewVisualEvidenceSource{{
		SourceID: codeReviewVisualEvidenceSourceID(models.CodeReviewEvidenceSurfaceIssueComment, "101", 1, "https://example.com/page-two.png"),
		Surface:  models.CodeReviewEvidenceSurfaceIssueComment, ProviderObjectID: "101", SourceURL: "https://github.com/assembledhq/assembled/pull/42#issuecomment-101",
		AuthorLogin: "human", AuthorType: models.CodeReviewEvidenceAuthorTypeUser, ImageIndex: 1, ImageURL: "https://example.com/page-two.png", Untrusted: true,
	}}, discovery.Sources, "visual evidence discovery should include human images found after the first page")
	require.NoError(t, mock.ExpectationsWereMet(), "paginated discovery should use an org-scoped repository lookup")
}

func TestDiscoverCodeReviewVisualEvidenceBoundsRetainedProvenance(t *testing.T) {
	t.Parallel()

	const discoveredImages = models.CodeReviewVisualEvidenceMaxImages + 8
	var commentHTML strings.Builder
	for index := 1; index <= discoveredImages; index++ {
		_, err := fmt.Fprintf(&commentHTML, `<img src="https://example.com/image-%02d.png">`, index)
		require.NoError(t, err, "visual evidence fixture should render each image")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var response any
		switch r.URL.Path {
		case "/repos/assembledhq/assembled/pulls/42":
			response = map[string]any{"number": 42, "html_url": "https://github.com/assembledhq/assembled/pull/42", "body_html": "", "head": map[string]string{"sha": "head-sha"}}
		case "/repos/assembledhq/assembled/issues/42/comments":
			response = []map[string]any{{
				"id": 101, "html_url": "https://github.com/assembledhq/assembled/pull/42#issuecomment-101",
				"body_html": commentHTML.String(), "user": map[string]string{"login": "human", "type": "User"},
			}}
		case "/repos/assembledhq/assembled/pulls/42/reviews", "/repos/assembledhq/assembled/pulls/42/comments":
			response = []any{}
		default:
			http.NotFound(w, r)
			return
		}
		require.NoError(t, json.NewEncoder(w).Encode(response), "bounded GitHub fixture should be encoded")
	}))
	t.Cleanup(server.Close)

	orgID := uuid.New()
	repositoryID := uuid.New()
	service, mock := newVisualEvidenceTestService(t, server, orgID, repositoryID)

	discovery, err := service.DiscoverCodeReviewVisualEvidence(context.Background(), orgID, repositoryID, 42)

	require.NoError(t, err, "visual evidence discovery should succeed when a human comment exceeds the image limit")
	require.Equal(t, discoveredImages, discovery.SourceCount, "discovery should retain an aggregate count for omitted sources")
	require.Len(t, discovery.Sources, models.CodeReviewVisualEvidenceMaxImages, "discovery should bound persisted and prompted provenance globally")
	require.Equal(t, "https://example.com/image-01.png", discovery.Sources[0].ImageURL, "bounded discovery should retain deterministic earliest provenance")
	require.Equal(t, "https://example.com/image-32.png", discovery.Sources[len(discovery.Sources)-1].ImageURL, "bounded discovery should stop retaining provenance at the global limit")
	require.NoError(t, mock.ExpectationsWereMet(), "bounded discovery should use an org-scoped repository lookup")
}

func TestDiscoverCodeReviewVisualEvidenceFailsWhenASurfaceIsUnavailable(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/assembledhq/assembled/pulls/42":
			_, err := fmt.Fprint(w, `{"number":42,"body_html":"","head":{"sha":"head-sha"}}`)
			require.NoError(t, err, "pull request fixture should be written")
		case "/repos/assembledhq/assembled/pulls/42/reviews":
			http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
		case "/repos/assembledhq/assembled/issues/42/comments", "/repos/assembledhq/assembled/pulls/42/comments":
			_, err := fmt.Fprint(w, `[]`)
			require.NoError(t, err, "empty discussion fixture should be written")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	orgID := uuid.New()
	repositoryID := uuid.New()
	service, mock := newVisualEvidenceTestService(t, server, orgID, repositoryID)

	_, err := service.DiscoverCodeReviewVisualEvidence(context.Background(), orgID, repositoryID, 42)

	require.Error(t, err, "visual evidence discovery should fail rather than silently omit an unavailable GitHub surface")
	require.Contains(t, err.Error(), "list pull request reviews for visual evidence", "visual evidence discovery should identify the incomplete surface")
	require.NoError(t, mock.ExpectationsWereMet(), "failed discovery should still use an org-scoped repository lookup")
}

func TestVisualEvidenceSourcesFromHTML(t *testing.T) {
	t.Parallel()

	metadata := visualEvidenceSourceMetadata{
		Surface: models.CodeReviewEvidenceSurfaceIssueComment, ProviderObjectID: "99", AuthorLogin: "human", AuthorType: models.CodeReviewEvidenceAuthorTypeUser,
	}
	longContext := strings.Repeat("é", maxVisualEvidenceContextRunes+20)
	longAlt := strings.Repeat("a", maxVisualEvidenceContextRunes+20)
	tests := []struct {
		name     string
		html     string
		expected []models.CodeReviewVisualEvidenceSource
	}{
		{
			name: "extracts absolute and relative sources",
			html: `<p>Before <img src="https://example.com/one.png" alt="one"> after</p><img src="/user-attachments/two.png">`,
			expected: []models.CodeReviewVisualEvidenceSource{
				{SourceID: codeReviewVisualEvidenceSourceID(metadata.Surface, "99", 1, "https://example.com/one.png"), Surface: metadata.Surface, ProviderObjectID: "99", AuthorLogin: "human", AuthorType: models.CodeReviewEvidenceAuthorTypeUser, ImageIndex: 1, ImageURL: "https://example.com/one.png", AltText: "one", ContextText: "Before after", Untrusted: true},
				{SourceID: codeReviewVisualEvidenceSourceID(metadata.Surface, "99", 2, "https://github.com/user-attachments/two.png"), Surface: metadata.Surface, ProviderObjectID: "99", AuthorLogin: "human", AuthorType: models.CodeReviewEvidenceAuthorTypeUser, ImageIndex: 2, ImageURL: "https://github.com/user-attachments/two.png", Untrusted: true},
			},
		},
		{
			name:     "uses highest srcset candidate when src is absent",
			html:     `<picture><source srcset="https://example.com/small.webp 1x, https://example.com/large.webp 2x"><img alt="responsive"></picture>`,
			expected: []models.CodeReviewVisualEvidenceSource{{SourceID: codeReviewVisualEvidenceSourceID(metadata.Surface, "99", 1, "https://example.com/large.webp"), Surface: metadata.Surface, ProviderObjectID: "99", AuthorLogin: "human", AuthorType: models.CodeReviewEvidenceAuthorTypeUser, ImageIndex: 1, ImageURL: "https://example.com/large.webp", AltText: "responsive", Untrusted: true}},
		},
		{
			name:     "bounds untrusted context by runes",
			html:     `<p>` + longContext + `<img src="https://example.com/long.png" alt="` + longAlt + `"></p>`,
			expected: []models.CodeReviewVisualEvidenceSource{{SourceID: codeReviewVisualEvidenceSourceID(metadata.Surface, "99", 1, "https://example.com/long.png"), Surface: metadata.Surface, ProviderObjectID: "99", AuthorLogin: "human", AuthorType: models.CodeReviewEvidenceAuthorTypeUser, ImageIndex: 1, ImageURL: "https://example.com/long.png", AltText: strings.Repeat("a", maxVisualEvidenceContextRunes), ContextText: strings.Repeat("é", maxVisualEvidenceContextRunes), Untrusted: true}},
		},
		{name: "ignores images without a source", html: `<p><img alt="missing"></p>`, expected: []models.CodeReviewVisualEvidenceSource{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual := visualEvidenceSourcesFromHTML(metadata, tt.html)

			require.Equal(t, tt.expected, actual, "rendered HTML parsing should produce the expected untrusted image sources")
		})
	}
}

func newVisualEvidenceTestService(t *testing.T, server *httptest.Server, orgID, repositoryID uuid.UUID) (*PRService, pgxmock.PgxPoolIface) {
	t.Helper()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "visual evidence pgxmock pool should initialize")
	t.Cleanup(func() { mock.Close() })
	integrationID := uuid.New()
	now := time.Now().UTC()
	mock.ExpectQuery("SELECT id, org_id, integration_id, github_id").
		WithArgs(pgx.NamedArgs{"id": repositoryID, "org_id": orgID}).
		WillReturnRows(pgxmock.NewRows(prTestRepoColumns).AddRow(
			repositoryID, orgID, integrationID, int64(1001), "assembledhq/assembled", "main",
			false, nil, nil, "https://github.com/assembledhq/assembled.git", int64(456), "active",
			nil, nil, []byte(`{}`), now, now,
		))
	service := &PRService{
		tokenProvider: &Service{cache: map[int64]*cachedToken{
			456: {Token: "installation-token", ExpiresAt: time.Now().Add(time.Hour)},
		}},
		repos:      db.NewRepositoryStore(mock),
		logger:     zerolog.Nop(),
		baseURL:    server.URL,
		httpClient: server.Client(),
	}
	return service, mock
}

func ptrVisualEvidenceTime(value time.Time) *time.Time {
	return &value
}

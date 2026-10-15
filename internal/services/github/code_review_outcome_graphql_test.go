package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/github/identity"
	"github.com/assembledhq/143/internal/services/github/ratelimit"
	githubtelemetry "github.com/assembledhq/143/internal/services/github/telemetry"
)

func TestCodeReviewOutcomeGraphQLSnapshot(t *testing.T) {
	t.Parallel()

	submitted := time.Date(2026, time.September, 15, 12, 0, 0, 0, time.UTC)
	merged := submitted.Add(time.Hour)
	tests := []struct {
		name     string
		state    string
		mergedAt *time.Time
		count    int
		expected models.CodeReviewOutcomeSnapshot
	}{
		{name: "empty open PR uses one request", state: "OPEN", expected: models.CodeReviewOutcomeSnapshot{AuthorLogin: "author", State: "open", Reviews: []models.CodeReviewHumanReviewObservation{}}},
		{name: "closed PR uses one request", state: "CLOSED", count: 1, expected: models.CodeReviewOutcomeSnapshot{AuthorLogin: "author", State: "closed", Reviews: outcomeExpectedReviews(1, submitted)}},
		{name: "merged PR preserves merge time", state: "MERGED", mergedAt: &merged, count: 3, expected: models.CodeReviewOutcomeSnapshot{AuthorLogin: "author", State: "closed", Merged: true, MergedAt: &merged, Reviews: outcomeExpectedReviews(3, submitted)}},
		{name: "exactly one full page needs no empty extra page", state: "OPEN", count: 100, expected: models.CodeReviewOutcomeSnapshot{AuthorLogin: "author", State: "open", Reviews: outcomeExpectedReviews(100, submitted)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			var requestTime time.Time
			service, orgID, repositoryID := newOutcomeGraphQLTestService(t, func(req *http.Request) (*http.Response, error) {
				calls++
				requestTime = time.Now().UTC()
				requireOutcomeGraphQLRequest(t, req, nil)
				return outcomeHTTPResponse(http.StatusOK, outcomeGraphQLPage(tt.state, tt.mergedAt, outcomeReviewNodes(tt.count, submitted), false, nil)), nil
			})
			before := time.Now().UTC()
			actual, err := service.GetCodeReviewOutcomeSnapshot(context.Background(), orgID, repositoryID, 17)
			require.NoError(t, err, "complete GraphQL page should produce an authoritative outcome")
			require.False(t, actual.ObservedAt.Before(before), "read fence should be captured after method entry")
			require.False(t, actual.ObservedAt.After(requestTime), "read fence should precede the first provider request")
			expected := tt.expected
			expected.ObservedAt = actual.ObservedAt
			require.Equal(t, expected, actual, "GraphQL should preserve the complete REST outcome contract")
			require.Equal(t, 1, calls, "lifecycle and up to one hundred reviews should share one provider request")
		})
	}
}

func TestCodeReviewOutcomeGraphQLPaginationAndReadFence(t *testing.T) {
	t.Parallel()

	submitted := time.Date(2026, time.September, 15, 12, 0, 0, 0, time.UTC)
	calls := 0
	var firstRequestAt time.Time
	service, orgID, repositoryID := newOutcomeGraphQLTestService(t, func(req *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			firstRequestAt = time.Now().UTC()
			requireOutcomeGraphQLRequest(t, req, nil)
			return outcomeHTTPResponse(http.StatusOK, outcomeGraphQLPage("OPEN", nil, outcomeReviewNodes(100, submitted), true, ptrString("page-one"))), nil
		}
		require.Equal(t, 2, calls, "one additional page should finish a 101-review PR")
		requireOutcomeGraphQLRequest(t, req, ptrString("page-one"))
		// Lifecycle can change during pagination. Keep the first read's facts
		// and original fence, just as the prior REST details read did.
		return outcomeHTTPResponse(http.StatusOK, outcomeGraphQLPage("MERGED", &submitted, []string{outcomeReviewNode("101", "APPROVED", "reviewer-101", "User", "MEMBER", &submitted)}, false, nil)), nil
	})
	actual, err := service.GetCodeReviewOutcomeSnapshot(context.Background(), orgID, repositoryID, 17)
	require.NoError(t, err, "every review page should be consumed before publishing the outcome")
	require.False(t, actual.ObservedAt.After(firstRequestAt), "later pages must not move the webhook read fence forward")
	require.Equal(t, models.CodeReviewOutcomeSnapshot{
		AuthorLogin: "author", State: "open", ObservedAt: actual.ObservedAt, Reviews: outcomeExpectedReviews(101, submitted),
	}, actual, "all pages should preserve review order and the first lifecycle facts")
	require.Equal(t, 2, calls, "101 reviews should use two GraphQL requests rather than details plus two REST pages")
}

func TestCodeReviewOutcomeGraphQLReviewSemantics(t *testing.T) {
	t.Parallel()

	submitted := time.Date(2026, time.September, 15, 12, 0, 0, 0, time.UTC)
	nodes := []string{
		outcomeReviewNode("9007199254740993", "APPROVED", "human", "User", "MEMBER", &submitted),
		outcomeReviewNode(`"9223372036854775807"`, "CHANGES_REQUESTED", "robot", "Bot", "OWNER", &submitted),
		outcomeReviewNode("3", "DISMISSED", "former-reviewer", "User", "CONTRIBUTOR", nil),
		outcomeReviewNode("4", "COMMENTED", "commenter", "User", "COLLABORATOR", &submitted),
		outcomeReviewNode("5", "PENDING", "pending", "User", "MEMBER", nil),
		strings.Replace(outcomeReviewNode("6", "APPROVED", "deleted", "User", "MEMBER", &submitted), `{"login":"deleted","__typename":"User"}`, "null", 1),
		outcomeReviewNode("7", "APPROVED", "placeholder", "Mannequin", "NONE", &submitted),
		outcomeReviewNode("8", "APPROVED", " ", "User", "MEMBER", &submitted),
	}
	service, orgID, repositoryID := newOutcomeGraphQLTestService(t, func(req *http.Request) (*http.Response, error) {
		requireOutcomeGraphQLRequest(t, req, nil)
		body := outcomeGraphQLPage("OPEN", nil, nodes, false, nil)
		body = strings.Replace(body, `"author":{"login":"author","__typename":"User"}`, `"author":null`, 1)
		return outcomeHTTPResponse(http.StatusOK, body), nil
	})
	actual, err := service.GetCodeReviewOutcomeSnapshot(context.Background(), orgID, repositoryID, 17)
	require.NoError(t, err, "nullable actors and submission time should retain prior filtering semantics")
	require.Equal(t, models.CodeReviewOutcomeSnapshot{State: "open", ObservedAt: actual.ObservedAt, Reviews: []models.CodeReviewHumanReviewObservation{
		{GitHubReviewID: 9007199254740993, ReviewerLogin: "human", ReviewerType: "User", AuthorAssociation: "MEMBER", State: "approved", SubmittedAt: submitted},
		{GitHubReviewID: 9223372036854775807, ReviewerLogin: "robot", ReviewerType: "Bot", AuthorAssociation: "OWNER", State: "changes_requested", SubmittedAt: submitted},
		{GitHubReviewID: 3, ReviewerLogin: "former-reviewer", ReviewerType: "User", AuthorAssociation: "CONTRIBUTOR", State: "dismissed"},
		{GitHubReviewID: 7, ReviewerLogin: "placeholder", ReviewerType: "Mannequin", AuthorAssociation: "NONE", State: "approved", SubmittedAt: submitted},
	}}, actual, "exact int64 identity, actor type and association must survive independent-human classification")
}

func TestCodeReviewOutcomeGraphQLRejectsIncompletePages(t *testing.T) {
	t.Parallel()

	valid := outcomeGraphQLPage("OPEN", nil, []string{outcomeReviewNode("1", "APPROVED", "human", "User", "MEMBER", nil)}, false, nil)
	tests := []struct {
		name string
		body string
	}{
		{name: "malformed JSON", body: `{`},
		{name: "missing data", body: `{}`},
		{name: "null repository", body: `{"data":{"repository":null}}`},
		{name: "null pull request", body: `{"data":{"repository":{"pullRequest":null}}}`},
		{name: "partial data with GraphQL errors", body: strings.TrimSuffix(valid, "}") + `,"errors":[{"message":"partial failure"}]}`},
		{name: "malformed errors envelope", body: strings.TrimSuffix(valid, "}") + `,"errors":{}}`},
		{name: "unknown lifecycle", body: strings.Replace(valid, `"state":"OPEN"`, `"state":"UNKNOWN"`, 1)},
		{name: "missing merge timestamp", body: strings.Replace(valid, `"mergedAt":null,`, "", 1)},
		{name: "missing author", body: strings.Replace(valid, `"author":{"login":"author","__typename":"User"},`, "", 1)},
		{name: "null reviews", body: strings.Replace(valid, `"reviews":{"nodes":[`+outcomeReviewNode("1", "APPROVED", "human", "User", "MEMBER", nil)+`],"pageInfo":{"hasNextPage":false,"endCursor":null}}`, `"reviews":null`, 1)},
		{name: "missing nodes", body: strings.Replace(valid, `"nodes":[`+outcomeReviewNode("1", "APPROVED", "human", "User", "MEMBER", nil)+`],`, "", 1)},
		{name: "null review node", body: strings.Replace(valid, outcomeReviewNode("1", "APPROVED", "human", "User", "MEMBER", nil), "null", 1)},
		{name: "missing pagination flag", body: strings.Replace(valid, `"hasNextPage":false,`, "", 1)},
		{name: "unknown review state", body: strings.Replace(valid, `"state":"APPROVED"`, `"state":"UNKNOWN"`, 1)},
		{name: "missing review ID", body: strings.Replace(valid, `"fullDatabaseId":1,`, "", 1)},
		{name: "null review ID", body: strings.Replace(valid, `"fullDatabaseId":1`, `"fullDatabaseId":null`, 1)},
		{name: "missing actor type", body: strings.Replace(valid, `,"__typename":"User"`, "", 1)},
		{name: "missing association", body: strings.Replace(valid, `"authorAssociation":"MEMBER",`, "", 1)},
		{name: "missing submission time", body: strings.Replace(valid, `"submittedAt":null,`, "", 1)},
		{name: "invalid submission time", body: strings.Replace(valid, `"submittedAt":null`, `"submittedAt":"yesterday"`, 1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			service, orgID, repositoryID := newOutcomeGraphQLTestService(t, func(req *http.Request) (*http.Response, error) {
				calls++
				requireOutcomeGraphQLRequest(t, req, nil)
				return outcomeHTTPResponse(http.StatusOK, tt.body), nil
			})
			actual, err := service.GetCodeReviewOutcomeSnapshot(context.Background(), orgID, repositoryID, 17)
			require.Error(t, err, "incomplete authority must never retire prior reviews")
			require.Equal(t, models.CodeReviewOutcomeSnapshot{}, actual, "failed capture must not publish any partial outcome")
			require.Equal(t, 1, calls, "invalid capture must not trigger REST fallback or another GraphQL attempt")
		})
	}
}

func TestCodeReviewOutcomeGraphQLLinkedEnterpriseActors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name             string
		prAuthor         bool
		reviewer         bool
		expectedAuthor   string
		expectedReviewer string
	}{
		{name: "enterprise reviewer uses canonical user", reviewer: true, expectedAuthor: "author", expectedReviewer: "canonical"},
		{name: "enterprise PR author uses canonical user", prAuthor: true, expectedAuthor: "canonical", expectedReviewer: "human"},
		{name: "enterprise self review preserves identical author and reviewer", prAuthor: true, reviewer: true, expectedAuthor: "canonical", expectedReviewer: "canonical"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			service, orgID, repositoryID := newOutcomeGraphQLTestService(t, func(req *http.Request) (*http.Response, error) {
				requireOutcomeGraphQLRequest(t, req, nil)
				body := outcomeGraphQLPage("OPEN", nil, []string{outcomeReviewNode("1", "APPROVED", "human", "User", "MEMBER", nil)}, false, nil)
				enterprise := `{"login":"employee@example.test","__typename":"EnterpriseUserAccount","user":{"login":"canonical","__typename":"User"}}`
				if tt.prAuthor {
					body = strings.Replace(body, `{"login":"author","__typename":"User"}`, enterprise, 1)
				}
				if tt.reviewer {
					body = strings.Replace(body, `{"login":"human","__typename":"User"}`, enterprise, 1)
				}
				return outcomeHTTPResponse(http.StatusOK, body), nil
			})
			actual, err := service.GetCodeReviewOutcomeSnapshot(context.Background(), orgID, repositoryID, 17)
			require.NoError(t, err, "linked enterprise identity should use the canonical User in the same response")
			require.Equal(t, models.CodeReviewOutcomeSnapshot{
				AuthorLogin: tt.expectedAuthor, State: "open", ObservedAt: actual.ObservedAt,
				Reviews: []models.CodeReviewHumanReviewObservation{{GitHubReviewID: 1, ReviewerLogin: tt.expectedReviewer, ReviewerType: "User", AuthorAssociation: "MEMBER", State: "approved"}},
			}, actual, "enterprise email must never substitute for canonical GitHub author or reviewer identity")
		})
	}
}

func TestCodeReviewOutcomeGraphQLInvalidEnterpriseUser(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, user string }{
		{name: "null linked user", user: "null"},
		{name: "missing login", user: `{"__typename":"User"}`},
		{name: "blank canonical login", user: `{"login":" ","__typename":"User"}`},
		{name: "missing user type", user: `{"login":"canonical"}`},
		{name: "non-user linked type", user: `{"login":"canonical","__typename":"Bot"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			service, orgID, repositoryID := newOutcomeGraphQLTestService(t, func(req *http.Request) (*http.Response, error) {
				requireOutcomeGraphQLRequest(t, req, nil)
				body := outcomeGraphQLPage("OPEN", nil, []string{outcomeReviewNode("1", "APPROVED", "human", "User", "MEMBER", nil)}, false, nil)
				body = strings.Replace(body, `{"login":"human","__typename":"User"}`, `{"login":"employee@example.test","__typename":"EnterpriseUserAccount","user":`+tt.user+`}`, 1)
				return outcomeHTTPResponse(http.StatusOK, body), nil
			})
			actual, err := service.GetCodeReviewOutcomeSnapshot(context.Background(), orgID, repositoryID, 17)
			require.Error(t, err, "invalid linked User cannot establish canonical enterprise review identity")
			require.Equal(t, models.CodeReviewOutcomeSnapshot{}, actual, "uncertain enterprise identity must leave prior review facts unchanged")
		})
	}
}

func TestCodeReviewOutcomeGraphQLUnsupportedActors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		actorType string
		prAuthor  bool
	}{
		{name: "unlinked enterprise PR author is not a canonical user login", actorType: "EnterpriseUserAccount", prAuthor: true},
		{name: "unlinked enterprise reviewer cannot silently lose independence", actorType: "EnterpriseUserAccount"},
		{name: "unknown PR author", actorType: "FutureActor", prAuthor: true},
		{name: "unknown reviewer", actorType: "FutureActor"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			service, orgID, repositoryID := newOutcomeGraphQLTestService(t, func(req *http.Request) (*http.Response, error) {
				requireOutcomeGraphQLRequest(t, req, nil)
				actorType := tt.actorType
				if tt.prAuthor {
					actorType = "User"
				}
				body := outcomeGraphQLPage("OPEN", nil, []string{outcomeReviewNode("1", "APPROVED", "human", actorType, "MEMBER", nil)}, false, nil)
				if tt.prAuthor {
					body = strings.Replace(body, `"author":{"login":"author","__typename":"User"}`, `"author":{"login":"author@example.test","__typename":"`+tt.actorType+`"}`, 1)
				}
				return outcomeHTTPResponse(http.StatusOK, body), nil
			})
			actual, err := service.GetCodeReviewOutcomeSnapshot(context.Background(), orgID, repositoryID, 17)
			require.Error(t, err, "uncertain actor mapping must leave existing author and independence facts unchanged")
			require.Equal(t, models.CodeReviewOutcomeSnapshot{}, actual, "unsupported actor cannot publish a misleading complete snapshot")
		})
	}
}

func TestCodeReviewOutcomeGraphQLInvalidIDs(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{"0", "-1", "1.1", "1e3", "9223372036854775808", `"9223372036854775808"`, `""`, `"not-an-id"`, `"+1"`, "true", "{}"} {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			service, orgID, repositoryID := newOutcomeGraphQLTestService(t, func(_ *http.Request) (*http.Response, error) {
				return outcomeHTTPResponse(http.StatusOK, outcomeGraphQLPage("OPEN", nil, []string{outcomeReviewNode(raw, "APPROVED", "human", "User", "MEMBER", nil)}, false, nil)), nil
			})
			actual, err := service.GetCodeReviewOutcomeSnapshot(context.Background(), orgID, repositoryID, 17)
			require.Error(t, err, "invalid or overflowing IDs must fail closed without floating point rounding")
			require.Equal(t, models.CodeReviewOutcomeSnapshot{}, actual, "invalid review identity cannot publish a complete snapshot")
		})
	}
}

func TestCodeReviewOutcomeGraphQLCursorFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cursors []*string
	}{
		{name: "missing next cursor", cursors: []*string{nil}},
		{name: "empty next cursor", cursors: []*string{ptrString(" ")}},
		{name: "same cursor", cursors: []*string{ptrString("a"), ptrString("a")}},
		{name: "cursor cycle", cursors: []*string{ptrString("a"), ptrString("b"), ptrString("a")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			service, orgID, repositoryID := newOutcomeGraphQLTestService(t, func(req *http.Request) (*http.Response, error) {
				var expectedCursor *string
				if calls > 0 {
					expectedCursor = tt.cursors[calls-1]
				}
				requireOutcomeGraphQLRequest(t, req, expectedCursor)
				cursor := tt.cursors[calls]
				calls++
				return outcomeHTTPResponse(http.StatusOK, outcomeGraphQLPage("OPEN", nil, []string{}, true, cursor)), nil
			})
			actual, err := service.GetCodeReviewOutcomeSnapshot(context.Background(), orgID, repositoryID, 17)
			require.Error(t, err, "nonadvancing pagination cannot prove a complete review inventory")
			require.Equal(t, models.CodeReviewOutcomeSnapshot{}, actual, "cursor failures must discard accumulated pages")
			require.Equal(t, len(tt.cursors), calls, "cursor failure must stop before revisiting a page")
		})
	}
}

func TestCodeReviewOutcomeGraphQLLaterPageFailures(t *testing.T) {
	t.Parallel()

	valid := outcomeGraphQLPage("OPEN", nil, []string{}, false, nil)
	tests := []struct {
		name    string
		status  int
		body    string
		readErr error
		cancel  bool
	}{
		{name: "HTTP failure", status: http.StatusInternalServerError, body: `{"message":"unavailable"}`},
		{name: "GraphQL partial data", status: http.StatusOK, body: strings.TrimSuffix(valid, "}") + `,"errors":[{"message":"unavailable"}]}`},
		{name: "malformed JSON", status: http.StatusOK, body: `{`},
		{name: "interrupted response", status: http.StatusOK, body: valid, readErr: io.ErrUnexpectedEOF},
		{name: "oversized page", status: http.StatusOK, body: `{"data":{"padding":"` + strings.Repeat("x", int(codeReviewOutcomeResponseLimit)) + `"}}`},
		{name: "caller cancellation", status: http.StatusOK, body: valid, cancel: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			closed := false
			bytesRead := 0
			service, orgID, repositoryID := newOutcomeGraphQLTestService(t, func(req *http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					requireOutcomeGraphQLRequest(t, req, nil)
					return outcomeHTTPResponse(http.StatusOK, outcomeGraphQLPage("OPEN", nil, []string{outcomeReviewNode("1", "APPROVED", "human", "User", "MEMBER", nil)}, true, ptrString("next"))), nil
				}
				require.Equal(t, 2, calls, "failed page must not be retried or replaced by REST")
				requireOutcomeGraphQLRequest(t, req, ptrString("next"))
				response := outcomeHTTPResponse(tt.status, tt.body)
				response.Body = &outcomeReadCloser{reader: strings.NewReader(tt.body), err: tt.readErr, closed: &closed, bytesRead: &bytesRead, onRead: func() {
					if tt.cancel {
						cancel()
					}
				}}
				return response, nil
			})
			actual, err := service.GetCodeReviewOutcomeSnapshot(ctx, orgID, repositoryID, 17)
			require.Error(t, err, "any failed later page must leave the capture unapplied")
			require.Equal(t, models.CodeReviewOutcomeSnapshot{}, actual, "first-page lifecycle and reviews must not escape a failed capture")
			require.Equal(t, 2, calls, "capture must issue only the requested pages")
			require.True(t, closed, "failed response body must be closed")
			if int64(len(tt.body)) > codeReviewOutcomeResponseLimit {
				require.Equal(t, int(codeReviewOutcomeResponseLimit)+1, bytesRead, "oversized response must stop after the fixed byte limit and one overflow sentinel")
			}
			if tt.cancel {
				require.ErrorIs(t, err, context.Canceled, "caller cancellation must retain its terminal retry meaning")
			}
			if tt.readErr != nil {
				require.ErrorIs(t, err, tt.readErr, "body-read failure must remain unwrap-able")
			}
		})
	}
}

func TestCodeReviewOutcomeGraphQLResolvedIdentityAndContext(t *testing.T) {
	t.Parallel()

	jobID, prID, assessmentID := uuid.New(), uuid.New(), uuid.New()
	fallbackToken := "synthetic-" + uuid.NewString()
	issuedAt := time.Now().UTC()
	credential, registered := githubtelemetry.RegisterCredential(fallbackToken, githubtelemetry.CredentialRegistration{
		Profile: githubtelemetry.CredentialProfileInstallation, AppID: 1, InstallationID: 456,
		IssuedAt: issuedAt, ExpiresAt: issuedAt.Add(time.Hour),
	})
	require.True(t, registered, "isolated synthetic credential should register its exact issuer")
	var logs bytes.Buffer
	service, orgID, repositoryID := newOutcomeGraphQLTestService(t, func(req *http.Request) (*http.Response, error) {
		requireOutcomeGraphQLRequest(t, req, nil)
		require.Equal(t, "bearer "+fallbackToken, req.Header.Get("Authorization"), "request should authenticate using the actual fallback token")
		metadata, ok := githubtelemetry.RequestMetadataFromContext(req.Context())
		require.True(t, ok, "request must retain attribution metadata")
		require.Equal(t, githubtelemetry.RequestMetadata{
			Kind: githubtelemetry.RequestKindAPI, AuthType: githubtelemetry.AuthTypeAppInstallation,
			InstallationID: 456, Caller: "code_review_outcome", OrgID: req.Context().Value(outcomeOrgContextKey{}).(string),
			PullRequestID: prID, AssessmentID: assessmentID, SessionID: "parent-session", Repository: "example/repo", SyncReason: "repair",
		}, metadata, "actual issuer must augment parent org, PR, session and assessment context")
		actualJobID, ok := jobctx.JobIDFromContext(req.Context())
		require.True(t, ok, "outcome request must retain its job identity")
		require.Equal(t, jobID, actualJobID, "one-PR request must retain the exact parent job")
		response := outcomeHTTPResponse(http.StatusOK, outcomeGraphQLPage("OPEN", nil, []string{}, false, nil))
		response.Header.Set("X-RateLimit-Resource", "graphql")
		return response, nil
	})
	service.httpClient = githubtelemetry.NewControlledHTTPClient(time.Second, zerolog.New(&logs), nil, "pr_service", service.httpClient.Transport)
	resolutions := []int64{}
	resolver := identity.NewResolver(outcomeTokenSource(func(_ context.Context, installationID int64) (string, error) {
		resolutions = append(resolutions, installationID)
		if installationID == 123 {
			return "", &GitHubAPIError{StatusCode: http.StatusNotFound}
		}
		require.Equal(t, int64(456), installationID, "stale repository installation must fall back to its integration")
		return fallbackToken, nil
	}), zerolog.Nop())
	resolver.SetIntegrations(outcomeIntegrationSource{integration: models.Integration{OrgID: orgID, Config: json.RawMessage(`{"installation_id":456}`)}})
	service.cachedResolver = resolver
	ctx := jobctx.WithJobID(context.WithValue(context.Background(), outcomeOrgContextKey{}, orgID.String()), jobID)
	ctx = githubtelemetry.WithRequestMetadata(ctx, githubtelemetry.RequestMetadata{
		OrgID: orgID.String(), PullRequestID: prID, AssessmentID: assessmentID, SessionID: "parent-session", Repository: "example/repo", SyncReason: "repair",
	})
	actual, err := service.GetCodeReviewOutcomeSnapshot(ctx, orgID, repositoryID, 17)
	require.NoError(t, err, "fallback issuer should use the normal outcome transport")
	require.Equal(t, models.CodeReviewOutcomeSnapshot{AuthorLogin: "author", State: "open", ObservedAt: actual.ObservedAt, Reviews: []models.CodeReviewHumanReviewObservation{}}, actual, "identity fallback should preserve outcome facts")
	require.Equal(t, []int64{123, 456}, resolutions, "resolution should try the recorded installation and exact integration fallback once")
	var record map[string]any
	require.NoError(t, json.Unmarshal(logs.Bytes(), &record), "one outcome request should emit one structured transport summary")
	expectedScope := map[string]any{
		"github_method": "POST", "github_route": "/graphql", "github_rate_limit_resource": "graphql",
		"github_caller": "code_review_outcome", "github_auth_type": githubtelemetry.AuthTypeAppInstallation,
		"github_installation_id": float64(456), "github_token_app_id": float64(1), "github_token_installation_id": float64(456),
		"github_token_generation_id": credential.GenerationID, "github_token_correlation_status": "known",
		"org_id": orgID.String(), "pull_request_id": prID.String(), "assessment_id": assessmentID.String(), "job_id": jobID.String(),
		"session_id": "parent-session", "github_repository": "example/repo", "github_sync_reason": "repair",
	}
	actualScope := make(map[string]any, len(expectedScope))
	for name := range expectedScope {
		actualScope[name] = record[name]
	}
	require.Equal(t, expectedScope, actualScope, "actual transport log must retain issuer, resource, caller and parent scope")
}

func TestCodeReviewOutcomeGraphQLThrottleUsesSharedControl(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.September, 15, 12, 0, 0, 0, time.UTC)
	controller, err := ratelimit.NewController(ratelimit.Config{
		Mode: ratelimit.ModeEnforce, Environment: "test", AppID: 1, InstallationAllowlist: []int64{123},
		Logger: zerolog.Nop(), Now: func() time.Time { return now }, Owner: "outcome-graphql",
	})
	require.NoError(t, err, "shared controller should initialize")
	outbound := 0
	service, orgID, repositoryID := newOutcomeGraphQLTestService(t, func(req *http.Request) (*http.Response, error) {
		outbound++
		requireOutcomeGraphQLRequest(t, req, nil)
		response := outcomeHTTPResponse(http.StatusOK, `{"data":null,"errors":[{"message":"API rate limit exceeded","extensions":{"code":"RATE_LIMITED"}}]}`)
		response.Header.Set("X-RateLimit-Resource", "graphql")
		response.Header.Set("X-RateLimit-Remaining", "0")
		response.Header.Set("X-RateLimit-Reset", fmt.Sprint(now.Add(time.Minute).Unix()))
		return response, nil
	}, 2)
	service.httpClient = githubtelemetry.NewControlledHTTPClient(time.Second, zerolog.Nop(), controller, "pr_service", service.httpClient.Transport)
	actual, err := service.GetCodeReviewOutcomeSnapshot(context.Background(), orgID, repositoryID, 17)
	var graphQLError *GitHubGraphQLError
	require.True(t, errors.As(err, &graphQLError), "HTTP-200 GraphQL throttle must remain a typed provider error")
	require.Equal(t, models.CodeReviewOutcomeSnapshot{}, actual, "throttle must not publish incomplete authority")
	actual, err = service.GetCodeReviewOutcomeSnapshot(context.Background(), orgID, repositoryID, 17)
	var deferral *ratelimit.Deferral
	require.True(t, errors.As(err, &deferral), "next request should observe shared installation cooldown")
	require.Equal(t, models.CodeReviewOutcomeSnapshot{}, actual, "deferred capture must remain unapplied")
	require.Equal(t, 1, outbound, "cooldown must prevent a second outbound provider request")
}

func TestCodeReviewOutcomeGraphQLSecondaryThrottleUsesSharedControl(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.September, 15, 12, 0, 0, 0, time.UTC)
	controller, err := ratelimit.NewController(ratelimit.Config{
		Mode: ratelimit.ModeEnforce, Environment: "test", AppID: 1, InstallationAllowlist: []int64{123},
		Logger: zerolog.Nop(), Now: func() time.Time { return now }, Owner: "outcome-graphql-secondary",
	})
	require.NoError(t, err, "shared controller should initialize")
	outbound := 0
	service, orgID, repositoryID := newOutcomeGraphQLTestService(t, func(req *http.Request) (*http.Response, error) {
		outbound++
		requireOutcomeGraphQLRequest(t, req, nil)
		response := outcomeHTTPResponse(http.StatusOK, `{"data":null,"errors":[{"message":"You have exceeded a secondary rate limit.","type":"RATE_LIMITED"}]}`)
		response.Header.Set("Retry-After", "37")
		response.Header.Set("X-RateLimit-Resource", "graphql")
		return response, nil
	}, 2)
	service.httpClient = githubtelemetry.NewControlledHTTPClient(time.Second, zerolog.Nop(), controller, "pr_service", service.httpClient.Transport)
	actual, err := service.GetCodeReviewOutcomeSnapshot(context.Background(), orgID, repositoryID, 17)
	var graphQLError *GitHubGraphQLError
	require.ErrorAs(t, err, &graphQLError, "HTTP-200 secondary throttle must preserve its typed GraphQL envelope")
	require.Equal(t, models.CodeReviewOutcomeSnapshot{}, actual, "secondary throttle cannot publish partial authority")
	classification := ClassifyRetry(err, now)
	require.True(t, classification.RateLimited, "GraphQL Retry-After should classify as rate limiting")
	require.True(t, classification.ControllerManaged, "shared controller should own the exact secondary retry deadline")
	require.NotNil(t, classification.RetryAt, "managed secondary error should retain its retry timestamp")
	require.False(t, classification.RetryAt.Before(now.Add(37*time.Second)), "managed cooldown cannot retry before GitHub's Retry-After")
	_, err = service.GetCodeReviewOutcomeSnapshot(context.Background(), orgID, repositoryID, 17)
	var deferred *ratelimit.Deferral
	require.ErrorAs(t, err, &deferred, "secondary throttle must defer the next outcome call before outbound HTTP")
	require.Equal(t, 1, outbound, "secondary cooldown must prevent duplicate outgoing attempts")
}

func TestCodeReviewOutcomeGraphQLBoundedRecoveryProbe(t *testing.T) {
	t.Parallel()
	valid := outcomeGraphQLPage("OPEN", nil, []string{}, false, nil)
	tests := []struct {
		name      string
		body      string
		readErr   error
		recovered bool
	}{
		{name: "malformed response", body: `{`},
		{name: "oversized response", body: `{"data":{"padding":"` + strings.Repeat("x", int(codeReviewOutcomeResponseLimit)) + `"}}`},
		{name: "interrupted response", body: valid, readErr: io.ErrUnexpectedEOF},
		{name: "complete application error keeps existing recovery contract", body: strings.TrimSuffix(valid, "}") + `,"errors":[{"message":"a field is unavailable"}]}`, recovered: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			now := time.Date(2026, time.September, 15, 12, 0, 0, 0, time.UTC)
			controller, err := ratelimit.NewController(ratelimit.Config{
				Mode: ratelimit.ModeEnforce, Environment: "test", AppID: 1, InstallationAllowlist: []int64{123},
				Logger: zerolog.Nop(), Now: func() time.Time { return now }, Owner: "outcome-graphql-probe",
			})
			require.NoError(t, err, "isolated probe controller should initialize")
			seed, err := controller.Before(context.Background(), ratelimit.Scope{InstallationID: 123})
			require.NoError(t, err, "seed request should be admitted")
			now = controller.Observe(context.Background(), seed, ratelimit.Observation{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": {"1"}}}).Deferral.RetryAt
			outbound := 0
			closed := false
			service, orgID, repositoryID := newOutcomeGraphQLTestService(t, func(req *http.Request) (*http.Response, error) {
				outbound++
				requireOutcomeGraphQLRequest(t, req, nil)
				_, err := controller.Before(context.Background(), ratelimit.Scope{InstallationID: 123})
				var deferred *ratelimit.Deferral
				require.ErrorAs(t, err, &deferred, "response reader must retain exclusive probe ownership until its decoder completes")
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: &outcomeReadCloser{reader: strings.NewReader(tt.body), err: tt.readErr, closed: &closed}}, nil
			})
			service.httpClient = githubtelemetry.NewControlledHTTPClient(3*time.Second, zerolog.Nop(), controller, "pr_service", service.httpClient.Transport)
			actual, err := service.GetCodeReviewOutcomeSnapshot(context.Background(), orgID, repositoryID, 17)
			require.Error(t, err, "failed capture or complete application error should remain a provider failure")
			require.Equal(t, models.CodeReviewOutcomeSnapshot{}, actual, "failed outcome capture must remain unapplied even when a complete envelope proves recovery")
			permit, err := controller.Before(context.Background(), ratelimit.Scope{InstallationID: 123})
			require.NoError(t, err, "reader should release its own expired cooldown probe lease")
			require.Equal(t, !tt.recovered, permit.Probe, "only a complete GraphQL envelope may restore ordinary admission")
			require.True(t, closed, "all failed capture bodies must close")
			require.Equal(t, 1, outbound, "probe observation must never replay the provider request")
		})
	}
}

type outcomeOrgContextKey struct{}
type outcomeTokenSource func(context.Context, int64) (string, error)

func (source outcomeTokenSource) GetInstallationToken(ctx context.Context, id int64) (string, error) {
	return source(ctx, id)
}

type outcomeIntegrationSource struct{ integration models.Integration }

func (source outcomeIntegrationSource) GetByID(context.Context, uuid.UUID) (models.Integration, error) {
	return source.integration, nil
}

type outcomeReadCloser struct {
	reader    io.Reader
	err       error
	closed    *bool
	bytesRead *int
	onRead    func()
}

func (reader *outcomeReadCloser) Read(p []byte) (int, error) {
	if reader.onRead != nil {
		reader.onRead()
	}
	n, err := reader.reader.Read(p)
	if reader.bytesRead != nil {
		*reader.bytesRead += n
	}
	if err == io.EOF && reader.err != nil {
		return n, reader.err
	}
	return n, err
}
func (reader *outcomeReadCloser) Close() error { *reader.closed = true; return nil }

func newOutcomeGraphQLTestService(t *testing.T, transport roundTripFunc, lookups ...int) (*PRService, uuid.UUID, uuid.UUID) {
	t.Helper()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "isolated repository mock should initialize")
	orgID, repositoryID := uuid.New(), uuid.New()
	t.Cleanup(func() {
		require.NoError(t, mock.ExpectationsWereMet(), "outcome lookup must use its exact tenant and repository")
		mock.Close()
	})
	count := 1
	if len(lookups) > 0 {
		count = lookups[0]
	}
	for range count {
		expectOutcomeRepository(t, mock, orgID, repositoryID)
	}
	return &PRService{
		repos: db.NewRepositoryStore(mock), logger: zerolog.Nop(), baseURL: "https://api.github.com",
		tokenProvider: &Service{cache: map[int64]*cachedToken{123: {Token: "installation-token", ExpiresAt: time.Now().Add(time.Hour)}}},
		httpClient:    &http.Client{Transport: transport},
	}, orgID, repositoryID
}

func expectOutcomeRepository(t *testing.T, mock pgxmock.PgxPoolIface, orgID, repositoryID uuid.UUID) {
	t.Helper()
	now := time.Now().UTC()
	mock.ExpectQuery("SELECT id, org_id, integration_id, github_id").WithArgs(pgx.NamedArgs{"id": repositoryID, "org_id": orgID}).
		WillReturnRows(pgxmock.NewRows(prTestRepoColumns).AddRow(
			repositoryID, orgID, uuid.New(), int64(1001), "example/repo", "main", false, nil, nil,
			"https://github.com/example/repo.git", int64(123), "active", nil, nil, []byte(`{}`), now, now,
		))
}

func requireOutcomeGraphQLRequest(t *testing.T, req *http.Request, cursor *string) {
	t.Helper()
	require.Equal(t, http.MethodPost, req.Method, "outcome reader should use GraphQL without REST fallback")
	require.Equal(t, "/graphql", req.URL.Path, "lifecycle and reviews should share the GraphQL endpoint")
	var payload struct {
		Query     string `json:"query"`
		Variables struct {
			Owner  string  `json:"owner"`
			Name   string  `json:"name"`
			Number int     `json:"number"`
			Cursor *string `json:"cursor"`
		} `json:"variables"`
	}
	require.NoError(t, json.NewDecoder(req.Body).Decode(&payload), "query payload should be valid JSON")
	require.Equal(t, codeReviewOutcomeQuery, payload.Query, "query should request the reviewed complete outcome fields")
	require.Equal(t, "example", payload.Variables.Owner, "query should preserve repository owner")
	require.Equal(t, "repo", payload.Variables.Name, "query should preserve repository name")
	require.Equal(t, 17, payload.Variables.Number, "query should preserve pull request number")
	require.Equal(t, cursor, payload.Variables.Cursor, "review cursor should advance only to the provider's next page")
}

func outcomeHTTPResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}
func outcomeGraphQLPage(state string, mergedAt *time.Time, nodes []string, hasNext bool, cursor *string) string {
	mergedJSON := outcomeTimestampJSON(mergedAt)
	cursorJSON := "null"
	if cursor != nil {
		cursorJSON = strconv.Quote(*cursor)
	}
	return fmt.Sprintf(`{"data":{"repository":{"pullRequest":{"state":%q,"mergedAt":%s,"author":{"login":"author","__typename":"User"},"reviews":{"nodes":[%s],"pageInfo":{"hasNextPage":%t,"endCursor":%s}}}}}}`, state, mergedJSON, strings.Join(nodes, ","), hasNext, cursorJSON)
}
func outcomeReviewNode(id, state, login, actorType, association string, submitted *time.Time) string {
	submittedJSON := outcomeTimestampJSON(submitted)
	return fmt.Sprintf(`{"fullDatabaseId":%s,"state":%q,"submittedAt":%s,"authorAssociation":%q,"author":{"login":%q,"__typename":%q}}`, id, state, submittedJSON, association, login, actorType)
}
func outcomeReviewNodes(count int, submitted time.Time) []string {
	nodes := make([]string, 0, count)
	for id := 1; id <= count; id++ {
		nodes = append(nodes, outcomeReviewNode(fmt.Sprint(id), "APPROVED", fmt.Sprintf("reviewer-%d", id), "User", "MEMBER", &submitted))
	}
	return nodes
}
func outcomeExpectedReviews(count int, submitted time.Time) []models.CodeReviewHumanReviewObservation {
	result := make([]models.CodeReviewHumanReviewObservation, 0, count)
	for id := 1; id <= count; id++ {
		result = append(result, models.CodeReviewHumanReviewObservation{GitHubReviewID: int64(id), ReviewerLogin: fmt.Sprintf("reviewer-%d", id), ReviewerType: "User", AuthorAssociation: "MEMBER", State: "approved", SubmittedAt: submitted})
	}
	return result
}

func outcomeTimestampJSON(value *time.Time) string {
	if value == nil {
		return "null"
	}
	return strconv.Quote(value.Format(time.RFC3339Nano))
}

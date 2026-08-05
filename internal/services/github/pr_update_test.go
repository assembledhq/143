package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/db"
)

func TestPRServiceUpdateSessionPullRequest(t *testing.T) {
	t.Parallel()

	mock := newMockPool(t)
	now := time.Now()
	orgID, sessionID, prID, changesetID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	existingBody := "Old description\n\nPreview: https://143.dev/previews/github/acme/repo/pull/42?launch=1"
	pullRequestColumns := append(append([]string(nil), handlerPRColumns...), "changeset_id")
	pullRequestRow := append(newPRTestRow(prID, &sessionID, orgID, "acme/repo", now, &existingBody), &changesetID)

	mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE session_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(pullRequestColumns).AddRow(pullRequestRow...))
	mock.ExpectQuery("SELECT .+ FROM repositories WHERE org_id =").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(prTestRepoColumns).AddRow(
			uuid.New(), orgID, uuid.New(), int64(1001), "acme/repo", "main", false, nil, nil,
			"https://github.com/acme/repo.git", int64(123), "active", nil, nil, []byte(`{}`), now, now,
		))
	mock.ExpectExec("UPDATE pull_requests").
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	var captured map[string]any
	tokenService, err := NewService(143, testPrivateKeyPEM(t))
	require.NoError(t, err, "GitHub service should initialize with a test key")
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/app/installations/123/access_tokens":
			return &http.Response{
				StatusCode: http.StatusCreated,
				Body:       io.NopCloser(strings.NewReader(`{"token":"installation-token"}`)),
				Header:     make(http.Header),
			}, nil
		case "/repos/acme/repo/pulls/42":
			require.Equal(t, http.MethodPatch, req.Method, "metadata update should PATCH the existing Pull Request")
			require.Equal(t, "token installation-token", req.Header.Get("Authorization"), "metadata update should use the server-held installation token")
			require.NoError(t, json.NewDecoder(req.Body).Decode(&captured), "GitHub request should contain valid JSON")
			responseJSON, marshalErr := json.Marshal(map[string]any{
				"number":   42,
				"html_url": "https://github.com/acme/repo/pull/42",
				"title":    "Profile startup paths",
				"body":     captured["body"],
				"head":     map[string]any{"sha": "head-sha", "ref": "feature/profile"},
				"base":     map[string]any{"sha": "base-sha"},
			})
			require.NoError(t, marshalErr, "GitHub response fixture should encode")
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(string(responseJSON))),
				Header:     make(http.Header),
			}, nil
		default:
			return nil, fmt.Errorf("unexpected request path %s", req.URL.Path)
		}
	})}
	tokenService.httpClient = client
	service := &PRService{
		tokenProvider: tokenService,
		pullRequests:  db.NewPullRequestStore(mock),
		repos:         db.NewRepositoryStore(mock),
		httpClient:    client,
		logger:        zerolog.Nop(),
	}
	title, body := "Profile startup paths", "Expanded description"
	expectedBody := upsertPublicationMarker(body, sessionID, &changesetID)
	expectedBody = upsertPRPreviewFooter(expectedBody, "https://143.dev/previews/github/acme/repo/pull/42?launch=1")

	result, err := service.UpdateSessionPullRequest(context.Background(), orgID, sessionID, UpdateSessionPullRequestParams{
		Title: &title,
		Body:  &body,
	})

	require.NoError(t, err, "current-session Pull Request metadata should update")
	require.Equal(t, title, captured["title"], "GitHub request should contain the replacement title")
	require.Equal(t, expectedBody, captured["body"], "GitHub request should preserve platform-owned publication and preview metadata")
	require.Equal(t, prID, result.PullRequestID, "result should identify the locally mirrored Pull Request")
	require.Equal(t, 42, result.PullRequestNumber, "result should identify the GitHub Pull Request")
	require.NoError(t, mock.ExpectationsWereMet(), "all tenant-scoped database expectations should be met")
}

func TestExisting143PreviewFooterLink(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		body     string
		expected string
	}{
		{
			name:     "stable 143 preview",
			body:     "Description\n\nPreview: https://143.dev/previews/github/acme/repo/pull/42?launch=1",
			expected: "https://143.dev/previews/github/acme/repo/pull/42?launch=1",
		},
		{
			name:     "legacy preview origin",
			body:     "Description\n\nPreview: https://target-1.preview.143.dev",
			expected: "https://target-1.preview.143.dev",
		},
		{name: "repository template field", body: "Preview: https://staging.example.com"},
		{name: "invalid URL", body: "Preview: TBD"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, existing143PreviewFooterLink(tt.body), "only platform-owned 143 preview footers should be preserved")
		})
	}
}

func TestPRServiceUpdateSessionPullRequestValidatesInput(t *testing.T) {
	t.Parallel()

	emptyTitle := "   "
	longTitle := strings.Repeat("x", maxPRTitleLen+1)
	tests := []struct {
		name   string
		orgID  uuid.UUID
		runID  uuid.UUID
		params UpdateSessionPullRequestParams
	}{
		{name: "missing identity", params: UpdateSessionPullRequestParams{Body: ptrString("body")}},
		{name: "missing metadata", orgID: uuid.New(), runID: uuid.New()},
		{name: "empty title", orgID: uuid.New(), runID: uuid.New(), params: UpdateSessionPullRequestParams{Title: &emptyTitle}},
		{name: "long title", orgID: uuid.New(), runID: uuid.New(), params: UpdateSessionPullRequestParams{Title: &longTitle}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := (&PRService{}).UpdateSessionPullRequest(context.Background(), tt.orgID, tt.runID, tt.params)
			require.Error(t, err, "invalid Pull Request metadata should fail before accessing dependencies")
		})
	}
}

func TestPRServiceUpdateSessionPullRequestClassifiesMissingPrimaryPR(t *testing.T) {
	t.Parallel()

	mock := newMockPool(t)
	mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE session_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(handlerPRColumns))
	service := &PRService{
		tokenProvider: &Service{},
		pullRequests:  db.NewPullRequestStore(mock),
		repos:         db.NewRepositoryStore(mock),
	}
	body := "Updated description"

	_, err := service.UpdateSessionPullRequest(context.Background(), uuid.New(), uuid.New(), UpdateSessionPullRequestParams{Body: &body})

	require.Error(t, err, "session without a primary Pull Request should fail")
	require.True(t, errors.Is(err, ErrSessionPullRequestNotFound), "missing primary Pull Request should return the typed not-found error")
	require.NoError(t, mock.ExpectationsWereMet(), "not-found classification should only query the token-scoped session Pull Request")
}

func TestPullRequestWritePermissionMissing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name: "GitHub integration permission rejection",
			err: &GitHubAPIError{
				StatusCode: http.StatusForbidden,
				Body:       []byte(`{"message":"Resource not accessible by integration"}`),
			},
			expected: true,
		},
		{
			name: "unrelated forbidden response",
			err: &GitHubAPIError{
				StatusCode: http.StatusForbidden,
				Body:       []byte(`{"message":"branch policy denied the update"}`),
			},
		},
		{
			name: "same message with non-forbidden status",
			err: &GitHubAPIError{
				StatusCode: http.StatusBadGateway,
				Body:       []byte(`{"message":"Resource not accessible by integration"}`),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, pullRequestWritePermissionMissing(tt.err), "permission classifier should only match GitHub App write-scope failures")
		})
	}
}

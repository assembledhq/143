package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/db"
)

func TestPRService_MarkPullRequestReady(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		draft             bool
		currentHead       string
		expectedHead      string
		expectedMutations int
		expectError       bool
	}{
		{name: "draft is marked ready", draft: true, currentHead: "reviewed", expectedHead: "reviewed", expectedMutations: 1},
		{name: "moved draft head is left unreviewed", draft: true, currentHead: "moved", expectedHead: "reviewed", expectError: true},
		{name: "ready pull request is an idempotent no-op", draft: false, currentHead: "reviewed", expectedHead: "reviewed", expectedMutations: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mutationCount := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/repos/assembledhq/143/pulls/42":
					require.Equal(t, "token installation-token", r.Header.Get("Authorization"), "GitHub REST calls should use the repository installation token")
					require.Equal(t, http.MethodGet, r.Method, "draft readiness should first read the current pull request")
					_ = json.NewEncoder(w).Encode(map[string]any{
						"node_id": "PR_node", "draft": tt.draft, "head": map[string]any{"sha": tt.currentHead},
					})
				case "/graphql":
					require.Equal(t, "bearer installation-token", r.Header.Get("Authorization"), "GitHub GraphQL calls should use the repository installation token")
					mutationCount++
					require.Equal(t, http.MethodPost, r.Method, "draft readiness should use the GraphQL mutation endpoint")
					_ = json.NewEncoder(w).Encode(map[string]any{
						"data": map[string]any{"markPullRequestReadyForReview": map[string]any{
							"pullRequest": map[string]any{"id": "PR_node", "isDraft": false},
						}},
					})
				default:
					t.Fatalf("unexpected GitHub request path: %s", r.URL.Path)
				}
			}))
			t.Cleanup(server.Close)

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "test should create a database mock")
			t.Cleanup(mock.Close)
			orgID, sessionID, prID, repoID, integrationID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
			now := time.Now().UTC()
			mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE id").
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(pgxmock.NewRows(prTestPullRequestColumns).AddRow(newPRTestRow(prID, &sessionID, orgID, "assembledhq/143", now, nil)...))
			mock.ExpectQuery("SELECT .+ FROM repositories[\\s\\S]+full_name").
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(pgxmock.NewRows([]string{
					"id", "org_id", "integration_id", "github_id", "full_name", "default_branch", "private",
					"language", "description", "clone_url", "installation_id", "status", "last_synced_at",
					"context_quality", "settings", "created_at", "updated_at",
				}).AddRow(
					repoID, orgID, integrationID, int64(143), "assembledhq/143", "main", true,
					nil, nil, "https://github.com/assembledhq/143.git", int64(99), "active", nil,
					nil, json.RawMessage(`{}`), now, now,
				))
			tokenService := &Service{cache: map[int64]*cachedToken{
				99: {Token: "installation-token", ExpiresAt: now.Add(time.Hour)},
			}}
			service := &PRService{
				tokenProvider: tokenService,
				pullRequests:  db.NewPullRequestStore(mock),
				repos:         db.NewRepositoryStore(mock),
				baseURL:       server.URL,
				httpClient:    server.Client(),
				logger:        zerolog.Nop(),
			}

			err = service.MarkPullRequestReady(context.Background(), orgID, prID, tt.expectedHead)
			if tt.expectError {
				require.ErrorContains(t, err, "head moved after review", "draft readiness should fail closed when GitHub reports another head")
			} else {
				require.NoError(t, err, "draft readiness should converge successfully")
			}
			require.Equal(t, tt.expectedMutations, mutationCount, "draft readiness should mutate GitHub only when the pull request remains a draft")
			require.NoError(t, mock.ExpectationsWereMet(), "draft readiness should use tenant-scoped pull request and repository lookups")
		})
	}
}

package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/config"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	automationevents "github.com/assembledhq/143/internal/services/automations"
	ghservice "github.com/assembledhq/143/internal/services/github"
)

type failingCheckRunTriggerer struct {
	err   error
	calls []automationevents.GitHubEventTriggerRequest
}

func (f *failingCheckRunTriggerer) TriggerGitHubEvent(_ context.Context, req automationevents.GitHubEventTriggerRequest) error {
	f.calls = append(f.calls, req)
	return f.err
}
func (*failingCheckRunTriggerer) RememberKnownLabels(automationevents.GitHubEventTriggerRequest) {}

func TestWebhookCheckRunPreservesOptInRedelivery(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name              string
		failure           error
		status            int
		repositoryFailure bool
	}{
		{name: "opted in lookup or queue failure", failure: fmt.Errorf("%w: provider unavailable", automationevents.ErrCheckRunTrigger), status: http.StatusInternalServerError},
		{name: "repository discovery failure", failure: fmt.Errorf("database unavailable"), status: http.StatusInternalServerError, repositoryFailure: true},
		{name: "automation discovery failure", failure: fmt.Errorf("%w: list unavailable", automationevents.ErrCheckRunTrigger), status: http.StatusInternalServerError},
		{name: "legacy trigger failure", failure: fmt.Errorf("legacy unavailable"), status: http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "initialize the webhook database fixture")
			defer mock.Close()
			org, repo := uuid.New(), uuid.New()
			now := time.Now()
			mock.ExpectQuery("SELECT r.id AS repository_id").WithArgs(pgx.NamedArgs{"github_id": int64(123)}).
				WillReturnRows(pgxmock.NewRows([]string{"repository_id", "org_id", "org_name", "github_id", "full_name", "status"}).AddRow(repo, org, "Example", int64(123), "example/project", "active"))
			repositoryQuery := mock.ExpectQuery("SELECT id, org_id, integration_id, github_id").WithArgs(pgx.NamedArgs{"org_id": org, "full_name": "example/project"})
			if tt.repositoryFailure {
				repositoryQuery.WillReturnError(tt.failure)
			} else {
				repositoryQuery.WillReturnRows(pgxmock.NewRows(repoColumns()).AddRow(repo, org, uuid.New(), int64(123), "example/project", "main", true, nil, nil, "https://example.invalid/project.git", int64(456), "active", nil, nil, []byte(`{}`), now, now))
			}
			if tt.status == http.StatusOK {
				mock.ExpectQuery("SELECT .+ FROM pull_requests").WithArgs(pgx.NamedArgs{"org_id": org, "github_repo": "example/project", "github_pr_number": 5}).WillReturnError(pgx.ErrNoRows)
			}
			triggerer := &failingCheckRunTriggerer{err: tt.failure}
			service := ghservice.NewPRService(nil, db.NewPullRequestStore(mock), nil, nil, nil, db.NewRepositoryStore(mock), nil, zerolog.Nop())
			service.SetAutomationEventTriggerer(triggerer)
			handler := NewWebhookHandler(&config.Config{}, nil, nil, db.NewRepositoryStore(mock), nil, service)
			body := `{"action":"completed","repository":{"id":123,"full_name":"example/project"},"check_run":{"id":1,"name":"review-policy","app":{"id":123},"head_sha":"head","conclusion":"neutral","pull_requests":[{"number":5,"base":{"ref":"main"}}]}}`
			req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/github", strings.NewReader(body))
			req.Header.Set("X-GitHub-Delivery", "original-delivery")
			response := httptest.NewRecorder()
			handler.handleCheckRun(response, req, []byte(body))
			require.Equal(t, tt.status, response.Code, "only opted-in failures should preserve a failed webhook delivery")
			if tt.status == http.StatusInternalServerError {
				require.Contains(t, response.Body.String(), "CHECK_RUN_FAILED", "HTTP failures must use the stable webhook error code")
			}
			if tt.repositoryFailure {
				require.Empty(t, triggerer.calls, "failed discovery cannot dispatch")
			} else {
				require.Len(t, triggerer.calls, 1, "the source completion should be routed once")
				require.Equal(t, "original-delivery", triggerer.calls[0].ProviderEventID, "redelivery must retain its original provider identity")
				require.Equal(t, models.AutomationGitHubEventCheckRunCompleted, triggerer.calls[0].Event, "the raw completed check should reach the trigger")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "the failed completion must not fall through to unrelated processing")
		})
	}
}

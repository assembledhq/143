package github

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
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

func TestPRServiceListCodeReviewCheckRuns(t *testing.T) {
	t.Parallel()
	head := "abcdef1111111111111111111111111111111111"
	tests := []struct {
		name        string
		pages       map[int]string
		expected    []models.CodeReviewCheckRun
		expectError bool
	}{
		{
			name: "single page maps every field the consumer reads",
			pages: map[int]string{1: `{"total_count":2,"check_runs":[
				{"id":11,"name":"risk-tier","head_sha":"` + head + `","status":"completed","conclusion":"success","app":{"id":101,"slug":"example-risk-tier"},"output":{"text":"payload"}},
				{"id":12,"name":"risk-tier","head_sha":"` + head + `","status":"in_progress","conclusion":null,"app":{"id":15368,"slug":"github-actions"},"output":{"text":null}}]}`},
			expected: []models.CodeReviewCheckRun{
				{ID: 11, Name: "risk-tier", HeadSHA: head, Status: "completed", Conclusion: "success", AppID: 101, AppSlug: "example-risk-tier", OutputText: "payload"},
				{ID: 12, Name: "risk-tier", HeadSHA: head, Status: "in_progress", AppID: 15368, AppSlug: "github-actions"},
			},
		},
		{
			name: "follows pages until total count is reached",
			pages: map[int]string{
				1: `{"total_count":101,"check_runs":[` + codeReviewCheckRunPage(1, 100, head) + `]}`,
				2: `{"total_count":101,"check_runs":[` + codeReviewCheckRunPage(101, 1, head) + `]}`,
			},
			expected: codeReviewCheckRunModels(1, 101, head),
		},
		{
			name:     "empty page ends pagination",
			pages:    map[int]string{1: `{"total_count":5,"check_runs":[]}`},
			expected: []models.CodeReviewCheckRun{},
		},
		{
			name:        "malformed response",
			pages:       map[int]string{1: `{"check_runs":`},
			expectError: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID, repositoryID, integrationID := uuid.New(), uuid.New(), uuid.New()
			now := time.Now().UTC()
			mock := newMockPool(t)
			mock.ExpectQuery("SELECT .+ FROM repositories.+WHERE id = @id AND org_id = @org_id").
				WithArgs(pgx.NamedArgs{"id": repositoryID, "org_id": orgID}).
				WillReturnRows(pgxmock.NewRows(prTestRepoColumns).AddRow(
					repositoryID, orgID, integrationID, int64(1), "acme/app", "master", false, nil, nil,
					"https://github.com/acme/app.git", int64(123), "active", nil, nil, []byte(`{}`), now, now,
				))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "/repos/acme/app/commits/"+head+"/check-runs", r.URL.Path, "the lookup should read check runs for the assessed head")
				require.Equal(t, "risk-tier", r.URL.Query().Get("check_name"), "the lookup should filter by the configured check name")
				require.Equal(t, "all", r.URL.Query().Get("filter"), "the lookup should return every run so the consumer chooses the newest trusted one")
				require.Equal(t, "token install-token", r.Header.Get("Authorization"), "the lookup should use the repository installation token")
				page, err := strconv.Atoi(r.URL.Query().Get("page"))
				require.NoError(t, err, "every request should name its page")
				body, ok := tt.pages[page]
				require.True(t, ok, "the lookup should not request page %d", page)
				_, err = w.Write([]byte(body))
				require.NoError(t, err, "fixture response should be written")
			}))
			t.Cleanup(server.Close)
			service := &PRService{
				tokenProvider: &Service{cache: map[int64]*cachedToken{123: {Token: "install-token", ExpiresAt: now.Add(time.Hour)}}},
				repos:         db.NewRepositoryStore(mock),
				baseURL:       server.URL, httpClient: server.Client(), logger: zerolog.New(io.Discard),
			}
			runs, err := service.ListCodeReviewCheckRuns(context.Background(), orgID, repositoryID, head, models.DefaultCodeReviewRiskTierCheckName)
			if tt.expectError {
				require.Error(t, err, "an unreadable check run listing should fail the lookup")
				return
			}
			require.NoError(t, err, "the check run listing should succeed")
			require.Equal(t, tt.expected, runs, "the lookup should return every listed run")
			require.NoError(t, mock.ExpectationsWereMet(), "the repository should be loaded once")
		})
	}
}

func TestPRServiceListCodeReviewCheckRunsRejectsUnboundedListings(t *testing.T) {
	t.Parallel()
	head := "abcdef1111111111111111111111111111111111"
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		page, err := strconv.Atoi(r.URL.Query().Get("page"))
		require.NoError(t, err, "every request should name its page")
		_, err = fmt.Fprintf(w, `{"total_count":5000,"check_runs":[%s]}`, codeReviewCheckRunPage((page-1)*100+1, 100, head))
		require.NoError(t, err, "fixture response should be written")
	}))
	t.Cleanup(server.Close)
	service := &PRService{baseURL: server.URL, httpClient: server.Client(), logger: zerolog.New(io.Discard)}
	_, err := service.listCodeReviewCheckRuns(context.Background(), "install-token", "acme", "app", head, models.DefaultCodeReviewRiskTierCheckName)
	require.EqualError(t, err, `GitHub reports more than 1000 "risk-tier" check runs for one commit`, "an unbounded listing should fail closed instead of choosing from a partial set")
	require.Equal(t, int32(codeReviewCheckRunsMaxPages), requests.Load(), "the lookup should stop at its page budget")
}

func TestPRServiceListCodeReviewCheckRunsRequiresConfiguration(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		service   *PRService
		head      string
		checkName string
	}{
		{name: "nil service", service: nil, head: "abc", checkName: "risk-tier"},
		{name: "missing token provider", service: &PRService{repos: db.NewRepositoryStore(newMockPool(t))}, head: "abc", checkName: "risk-tier"},
		{name: "missing head", service: &PRService{repos: db.NewRepositoryStore(newMockPool(t)), tokenProvider: &Service{}}, head: " ", checkName: "risk-tier"},
		{name: "missing check name", service: &PRService{repos: db.NewRepositoryStore(newMockPool(t)), tokenProvider: &Service{}}, head: "abc", checkName: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := tt.service.ListCodeReviewCheckRuns(context.Background(), uuid.New(), uuid.New(), tt.head, tt.checkName)
			require.Error(t, err, "an unconfigured or underspecified lookup should fail instead of reporting no check runs")
		})
	}
}

func codeReviewCheckRunPage(firstID, count int, head string) string {
	body := ""
	for id := firstID; id < firstID+count; id++ {
		if body != "" {
			body += ","
		}
		body += fmt.Sprintf(`{"id":%d,"name":"risk-tier","head_sha":"%s","status":"completed","conclusion":"success","app":{"id":101,"slug":"example-risk-tier"},"output":{"text":"t"}}`, id, head)
	}
	return body
}

func codeReviewCheckRunModels(firstID, count int, head string) []models.CodeReviewCheckRun {
	runs := make([]models.CodeReviewCheckRun, 0, count)
	for id := firstID; id < firstID+count; id++ {
		runs = append(runs, models.CodeReviewCheckRun{ID: int64(id), Name: "risk-tier", HeadSHA: head, Status: "completed", Conclusion: "success", AppID: 101, AppSlug: "example-risk-tier", OutputText: "t"})
	}
	return runs
}

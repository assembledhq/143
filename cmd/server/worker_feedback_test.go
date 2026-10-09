package main

import (
	"context"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type workerFeedbackLLMStub struct{ calls int }

func (s *workerFeedbackLLMStub) Complete(context.Context, string, string) (string, error) {
	s.calls++
	return `{"actionable":false,"category":"nit","summary":"Acknowledgment","generalizable":false}`, nil
}

func TestWorkerFeedbackServiceProcessesComments(t *testing.T) {
	t.Parallel()
	pool, err := pgxmock.NewPool()
	require.NoError(t, err, "create isolated wiring database")
	defer pool.Close()
	orgID, commentID := uuid.New(), uuid.New()
	now := time.Now()
	llm := &workerFeedbackLLMStub{}
	feedbackService := newWorkerFeedbackService(pool, db.NewJobStore(pool), llm, zerolog.Nop())
	require.NotNil(t, feedbackService, "startup must supply feedback handlers with their real service")
	pool.ExpectQuery("SELECT .+ FROM review_comments WHERE id = @id AND org_id = @org_id").WithArgs(pgx.NamedArgs{"id": commentID, "org_id": orgID}).WillReturnRows(pgxmock.NewRows([]string{"id", "pull_request_id", "org_id", "github_comment_id", "reviewer", "reviewer_type", "body", "diff_path", "diff_position", "filter_status", "category", "actionable", "generalizable", "generalized_rule", "summary", "applied", "created_at"}).AddRow(commentID, uuid.New(), orgID, int64(42), "human", "User", "Consider checking the error returned by this call.", nil, nil, "pending", nil, false, false, nil, nil, false, now))
	category, summary := "nit", "Acknowledgment"
	pool.ExpectExec("UPDATE review_comments SET filter_status").WithArgs(pgx.NamedArgs{"id": commentID, "org_id": orgID, "filter_status": "filtered_not_actionable", "category": &category, "actionable": false, "generalizable": false, "generalized_rule": (*string)(nil), "summary": &summary}).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	require.NoError(t, feedbackService.ProcessComment(context.Background(), commentID, orgID), "wired feedback service should classify and persist review comments")
	require.Equal(t, 1, llm.calls, "startup must pass the configured LLM into feedback classification")
	require.NoError(t, pool.ExpectationsWereMet(), "startup dependencies should execute the tenant-scoped feedback operations")
}

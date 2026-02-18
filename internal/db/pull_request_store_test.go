package db

import (
	"context"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var prColumns = []string{
	"id", "agent_run_id", "org_id", "github_pr_number", "github_pr_url", "github_repo",
	"title", "body", "status", "review_status", "merged_at", "created_at", "updated_at",
}

func newPRRow(id, agentRunID, orgID uuid.UUID, now time.Time) []any {
	return []any{
		id, agentRunID, orgID, 42, "https://github.com/org/repo/pull/42", "org/repo",
		"Fix bug", (*string)(nil), "open", "pending", (*time.Time)(nil), now, now,
	}
}

func TestPullRequestStore_Create_Success(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPullRequestStore(mock)
	now := time.Now()
	generatedID := uuid.New()

	pr := &models.PullRequest{
		AgentRunID:     uuid.New(),
		OrgID:          uuid.New(),
		GitHubPRNumber: 42,
		GitHubPRURL:    "https://github.com/org/repo/pull/42",
		GitHubRepo:     "org/repo",
		Title:          "Fix bug",
		Status:         "open",
		ReviewStatus:   "pending",
	}

	mock.ExpectQuery("INSERT INTO pull_requests").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).
				AddRow(generatedID, now, now),
		)

	err = store.Create(context.Background(), pr)
	require.NoError(t, err)
	assert.Equal(t, generatedID, pr.ID)
	assert.Equal(t, now, pr.CreatedAt)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestPullRequestStore_GetByID_Success(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPullRequestStore(mock)
	orgID := uuid.New()
	id := uuid.New()
	agentRunID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(prColumns).
				AddRow(newPRRow(id, agentRunID, orgID, now)...),
		)

	pr, err := store.GetByID(context.Background(), orgID, id)
	require.NoError(t, err)
	assert.Equal(t, id, pr.ID)
	assert.Equal(t, 42, pr.GitHubPRNumber)
	assert.Equal(t, "open", pr.Status)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestPullRequestStore_GetByID_NotFound(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPullRequestStore(mock)

	mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(prColumns))

	_, err = store.GetByID(context.Background(), uuid.New(), uuid.New())
	assert.Error(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestPullRequestStore_GetByAgentRunID_Success(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPullRequestStore(mock)
	orgID := uuid.New()
	id := uuid.New()
	agentRunID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE agent_run_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(prColumns).
				AddRow(newPRRow(id, agentRunID, orgID, now)...),
		)

	pr, err := store.GetByAgentRunID(context.Background(), orgID, agentRunID)
	require.NoError(t, err)
	assert.Equal(t, id, pr.ID)
	assert.Equal(t, agentRunID, pr.AgentRunID)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestPullRequestStore_UpdateStatus_Closed(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPullRequestStore(mock)
	orgID := uuid.New()
	id := uuid.New()

	mock.ExpectExec("UPDATE pull_requests SET status .+ updated_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.UpdateStatus(context.Background(), orgID, id, "closed")
	require.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestPullRequestStore_UpdateStatus_Merged(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPullRequestStore(mock)
	orgID := uuid.New()
	id := uuid.New()

	mock.ExpectExec("UPDATE pull_requests SET status .+ merged_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.UpdateStatus(context.Background(), orgID, id, "merged")
	require.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestPullRequestStore_ListByOrg_Success(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPullRequestStore(mock)
	orgID := uuid.New()
	id1 := uuid.New()
	id2 := uuid.New()
	agentRunID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE org_id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(prColumns).
				AddRow(newPRRow(id1, agentRunID, orgID, now)...).
				AddRow(newPRRow(id2, agentRunID, orgID, now)...),
		)

	prs, err := store.ListByOrg(context.Background(), orgID, PullRequestFilters{})
	require.NoError(t, err)
	assert.Len(t, prs, 2)
	assert.Equal(t, id1, prs[0].ID)
	assert.Equal(t, id2, prs[1].ID)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestPullRequestStore_ListByOrg_WithStatusFilter(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPullRequestStore(mock)
	orgID := uuid.New()
	id := uuid.New()
	agentRunID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE org_id .+ AND status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(prColumns).
				AddRow(newPRRow(id, agentRunID, orgID, now)...),
		)

	prs, err := store.ListByOrg(context.Background(), orgID, PullRequestFilters{Status: "open"})
	require.NoError(t, err)
	assert.Len(t, prs, 1)
	assert.NoError(t, mock.ExpectationsWereMet())
}

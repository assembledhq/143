package github

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
)

func TestPRServiceBuildPullRequestHealthResponseUsesCurrentSummaryForRepairActions(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgx mock pool")
	defer mock.Close()

	orgID := uuid.New()
	pullRequestID := uuid.New()
	now := time.Now().UTC()
	summary := models.PullRequestHealthSummary{
		MergeState:       models.PullRequestMergeStateConflicted,
		HasConflicts:     true,
		FailingTestCount: 2,
		NeedsAgentAction: true,
	}
	summaryJSON, err := json.Marshal(summary)
	require.NoError(t, err, "should marshal current health summary")

	mock.ExpectQuery("SELECT .+ FROM pull_request_health_current WHERE org_id = .+ AND pull_request_id = .+").
		WithArgs(pgx.NamedArgs{
			"org_id":          orgID,
			"pull_request_id": pullRequestID,
		}).
		WillReturnRows(pgxmock.NewRows([]string{
			"pull_request_id",
			"org_id",
			"version",
			"head_sha",
			"base_sha",
			"summary_json",
			"summary_preview_json",
			"enrichment_status",
			"enriched_at",
			"created_at",
			"updated_at",
		}).AddRow(
			pullRequestID,
			orgID,
			int64(3),
			"head-new",
			"base-new",
			summaryJSON,
			summaryJSON,
			models.PullRequestHealthEnrichmentStatusNotRequested,
			nil,
			now,
			now,
		))

	service := &PRService{
		pullRequests: db.NewPullRequestStore(mock),
		logger:       zerolog.New(io.Discard),
	}

	resp, err := service.buildPullRequestHealthResponse(context.Background(), models.PullRequest{
		ID:               pullRequestID,
		OrgID:            orgID,
		GitHubPRNumber:   42,
		GitHubRepo:       "assembledhq/143",
		GitHubPRURL:      "https://github.com/assembledhq/143/pull/42",
		Status:           "open",
		MergeState:       models.PullRequestMergeStateUnknown,
		HasConflicts:     false,
		FailingTestCount: 0,
		NeedsAgentAction: false,
		HealthVersion:    1,
	})
	require.NoError(t, err, "buildPullRequestHealthResponse should succeed")
	require.Equal(t, models.PullRequestMergeStateConflicted, resp.MergeState, "response should use merge state from current health summary")
	require.True(t, resp.CanResolveConflicts, "response should advertise conflict repair when current summary reports conflicts")
	require.True(t, resp.CanFixTests, "response should advertise test repair when current summary reports failing tests")
	require.Equal(t, int64(3), resp.HealthVersion, "response should use health version from current summary")
	require.NoError(t, mock.ExpectationsWereMet(), "all health current queries should be executed")
}

func TestPRServiceBuildRepairRevisionContextUsesCurrentHealthSummary(t *testing.T) {
	t.Parallel()

	service := &PRService{}
	pr := models.PullRequest{
		GitHubPRNumber: 42,
		GitHubRepo:     "assembledhq/143",
		MergeState:     models.PullRequestMergeStateUnknown,
		HasConflicts:   false,
	}
	current := models.PullRequestHealthCurrent{
		HeadSHA: "head-new",
		BaseSHA: "base-new",
	}
	summary := models.PullRequestHealthSummary{
		MergeState:       models.PullRequestMergeStateConflicted,
		HasConflicts:     true,
		FailingTestCount: 1,
	}

	revisionContextJSON, err := service.buildRepairRevisionContext(
		pr,
		current,
		summary,
		models.PullRequestHealthSnapshot{},
		models.PullRequestRepairActionTypeResolveConflicts,
	)
	require.NoError(t, err, "buildRepairRevisionContext should succeed")

	var revisionContext agent.RevisionContext
	err = json.Unmarshal(revisionContextJSON, &revisionContext)
	require.NoError(t, err, "revision context JSON should decode")
	require.NotNil(t, revisionContext.RepairContext, "revision context should include repair context")
	require.Equal(t, models.PullRequestMergeStateConflicted, revisionContext.RepairContext.MergeState, "repair context should use merge state from the selected health summary")
	require.True(t, revisionContext.RepairContext.HasConflicts, "repair context should use conflict state from the selected health summary")
	require.Equal(t, "head-new", revisionContext.RepairContext.HeadSHA, "repair context should preserve the selected head SHA")
	require.Equal(t, "base-new", revisionContext.RepairContext.BaseSHA, "repair context should preserve the selected base SHA")
}

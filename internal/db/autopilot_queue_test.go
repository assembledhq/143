package db

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

func TestAutopilotQueueStore_ListQueue(t *testing.T) {
	t.Parallel()

	orgID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	issueID := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	repoID := uuid.MustParse("33333333-3333-3333-3333-333333333333")
	sessionID := uuid.MustParse("44444444-4444-4444-4444-444444444444")
	prID := uuid.MustParse("55555555-5555-5555-5555-555555555555")
	now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		filters   AutopilotQueueFilters
		setupMock func(pgxmock.PgxPoolIface)
		expected  AutopilotQueuePage
		expectErr bool
	}{
		{
			name: "returns org-scoped queue rows with display state precedence",
			filters: AutopilotQueueFilters{
				Limit: 10,
				Sort:  "rank",
			},
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("FROM issues i").
					WithArgs(pgx.NamedArgs{"org_id": orgID, "limit": 11, "offset": 0, "manual_source": models.IssueSourceManual}).
					WillReturnRows(pgxmock.NewRows([]string{
						"id", "rank", "source_type", "source_key", "title", "issue_url", "repo_id", "repo_name", "issue_status",
						"customer_impact_label", "customer_impact_count", "implementation_ease", "low_hanging_fruit_label",
						"low_hanging_fruit_reasons", "cluster_size", "session_id", "session_title", "session_updated_at",
						"session_status", "session_origin", "session_started_at", "session_completed_at", "pr_id", "pr_number",
						"pr_url", "pr_status", "pr_merged_at", "sort_score", "impact_score", "ease_score", "last_seen_at",
					}).AddRow(
						issueID.String(), int64(1), "sentry", "SENTRY-123", "Auth token expiry causes retry loop",
						"https://sentry.io/organizations/acme/issues/123", repoID.String(), "acme/api", "triaged", "High", 42, "High", "Very high",
						[]string{"high customer impact", "straightforward implementation", "recent activity"}, int64(1),
						sessionID.String(), "Fix auth token expiry", now, "running", "automation", now.Add(-10*time.Minute), nil,
						prID.String(), 12, "https://github.com/acme/api/pull/12", "open", nil,
						float64(91), float64(80), float64(75), now,
					))
			},
			expected: AutopilotQueuePage{
				Rows: []models.AutopilotQueueRow{
					{
						ID:          issueID,
						Rank:        1,
						Source:      models.AutopilotIssueSource{Type: models.IssueSourceSentry, Key: "SENTRY-123"},
						Title:       "Auth token expiry causes retry loop",
						IssueURL:    ptrString("https://sentry.io/organizations/acme/issues/123"),
						Repo:        &models.AutopilotRepoRef{ID: repoID, Name: "acme/api"},
						IssueStatus: "triaged",
						CustomerImpact: models.AutopilotCustomerImpact{
							Label: "High",
							Count: 42,
						},
						ImplementationEase: "High",
						LowHangingFruit: models.AutopilotLowHangingFruit{
							Label:       "Very high",
							Reasons:     []string{"high customer impact", "straightforward implementation", "recent activity"},
							ClusterSize: 1,
						},
						DisplayRunState: models.AutopilotRunStateRunning,
						LatestSession: &models.AutopilotSessionRef{
							ID:        sessionID,
							Title:     "Fix auth token expiry",
							UpdatedAt: now,
						},
						LatestAgentRun: &models.AutopilotAgentRunRef{
							ID:          sessionID,
							Status:      "running",
							TriggerMode: models.AutopilotTriggerModeAuto,
							StartedAt:   ptrTime(now.Add(-10 * time.Minute)),
						},
						LatestPR: &models.AutopilotPullRequestRef{
							ID:     prID,
							Number: 12,
							URL:    "https://github.com/acme/api/pull/12",
							Status: "open",
						},
						AvailableAction: models.AutopilotQueueActionViewRun,
					},
				},
				Summary: models.AutopilotQueueSummary{
					TopIssueID:        &issueID,
					AutorunnableCount: 0,
					NeedsReviewCount:  0,
					OpenPRCount:       0,
					ActiveRunCount:    1,
					RankedIssueCount:  1,
					AnalyzedAt:        &now,
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "pgxmock pool should be created")
			defer mock.Close()
			tt.setupMock(mock)

			store := NewAutopilotQueueStore(mock)
			actual, err := store.ListQueue(context.Background(), orgID, tt.filters)
			if tt.expectErr {
				require.Error(t, err, "ListQueue should return an error")
				return
			}

			require.NoError(t, err, "ListQueue should not return an error")
			require.Equal(t, tt.expected, actual, "ListQueue should return the expected projection")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestAutopilotQueueDisplayStatePrecedence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		row    autopilotQueueDBRow
		action models.AutopilotQueueAction
		state  models.AutopilotRunState
	}{
		{
			name: "running beats open pr",
			row: autopilotQueueDBRow{
				ID:            uuid.NewString(),
				SessionStatus: sql.NullString{String: "running", Valid: true},
				PRStatus:      sql.NullString{String: string(models.PullRequestStatusOpen), Valid: true},
			},
			action: models.AutopilotQueueActionViewRun,
			state:  models.AutopilotRunStateRunning,
		},
		{
			name: "human guidance beats open pr",
			row: autopilotQueueDBRow{
				ID:            uuid.NewString(),
				SessionStatus: sql.NullString{String: "needs_human_guidance", Valid: true},
				PRStatus:      sql.NullString{String: string(models.PullRequestStatusOpen), Valid: true},
			},
			action: models.AutopilotQueueActionReview,
			state:  models.AutopilotRunStateNeedsReview,
		},
		{
			name: "open pr beats completed session",
			row: autopilotQueueDBRow{
				ID:            uuid.NewString(),
				SessionStatus: sql.NullString{String: "completed", Valid: true},
				PRStatus:      sql.NullString{String: string(models.PullRequestStatusOpen), Valid: true},
			},
			action: models.AutopilotQueueActionOpenPR,
			state:  models.AutopilotRunStatePROpen,
		},
		{
			name:   "not started can start when repo is present",
			row:    autopilotQueueDBRow{ID: uuid.NewString(), RepoID: sql.NullString{String: uuid.NewString(), Valid: true}},
			action: models.AutopilotQueueActionStartRun,
			state:  models.AutopilotRunStateNotStarted,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			row := tt.row.toModel()
			require.Equal(t, tt.state, row.DisplayRunState, "row should use expected display state")
			require.Equal(t, tt.action, row.AvailableAction, "row should use expected dominant action")
		})
	}
}

func TestBuildAutopilotQueueQuery(t *testing.T) {
	t.Parallel()

	orgID := uuid.MustParse("11111111-1111-1111-1111-111111111111")

	tests := []struct {
		name             string
		filters          AutopilotQueueFilters
		expectedSnippets []string
		expectedArgs     pgx.NamedArgs
	}{
		{
			name: "projects rank in scan order",
			expectedSnippets: []string{
				"SELECT\n\t\t\ti.id,\n\t\t\ti.rank,\n\t\t\ti.source_type",
				"i.source <> @manual_source",
				"i.raw_data#>>'{data,url}'",
			},
			expectedArgs: pgx.NamedArgs{
				"org_id":        orgID,
				"limit":         51,
				"offset":        0,
				"manual_source": models.IssueSourceManual,
			},
		},
		{
			name: "applies run and automation filters before pagination",
			filters: AutopilotQueueFilters{
				RunState:   models.AutopilotRunStateRunning,
				Automation: "autorun_attempted",
			},
			expectedSnippets: []string{
				"WHERE i.display_run_state = @run_state AND i.trigger_mode = @trigger_mode",
				"LIMIT @limit OFFSET @offset",
			},
			expectedArgs: pgx.NamedArgs{
				"org_id":        orgID,
				"limit":         51,
				"offset":        0,
				"manual_source": models.IssueSourceManual,
				"run_state":     models.AutopilotRunStateRunning,
				"trigger_mode":  models.AutopilotTriggerModeAuto,
			},
		},
		{
			name: "applies ready-to-run automation filter before pagination",
			filters: AutopilotQueueFilters{
				Automation: "ready_to_run",
			},
			expectedSnippets: []string{
				"WHERE i.available_action = @available_action",
			},
			expectedArgs: pgx.NamedArgs{
				"org_id":           orgID,
				"limit":            51,
				"offset":           0,
				"manual_source":    models.IssueSourceManual,
				"available_action": models.AutopilotQueueActionStartRun,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			query, args := buildAutopilotQueueQuery(orgID, tt.filters, 51, 0)

			for _, snippet := range tt.expectedSnippets {
				require.Contains(t, query, snippet, "query should include expected SQL fragment")
			}
			require.Equal(t, tt.expectedArgs, args, "query should include expected named args")
		})
	}
}

func ptrTime(v time.Time) *time.Time {
	return &v
}

func ptrString(v string) *string {
	return &v
}

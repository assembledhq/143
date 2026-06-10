package github

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/cache"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
)

type repairJobPayloadArg struct {
	wantThreadID      uuid.UUID
	wantPullRequestID uuid.UUID
	wantAction        models.PullRequestRepairActionType
	wantHealthVersion int64
}

func (a repairJobPayloadArg) Match(value interface{}) bool {
	var payloadBytes []byte
	switch v := value.(type) {
	case []byte:
		payloadBytes = v
	case string:
		payloadBytes = []byte(v)
	default:
		return false
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return false
	}
	return payload["thread_id"] == a.wantThreadID.String() &&
		payload["pull_request_id"] == a.wantPullRequestID.String() &&
		payload["command_type"] == string(a.wantAction) &&
		payload["health_version"] == float64(a.wantHealthVersion)
}

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

func TestPRServiceBuildPullRequestHealthResponseIncludesActiveRepairs(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgx mock pool")
	defer mock.Close()

	orgID := uuid.New()
	pullRequestID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	terminalSessionID := uuid.New()
	now := time.Now().UTC()
	summary := models.PullRequestHealthSummary{
		MergeState:       models.PullRequestMergeStateClean,
		HasConflicts:     false,
		FailingTestCount: 1,
		NeedsAgentAction: true,
		ChecksConfirmed:  true,
		Checks: []models.PullRequestCheckSummary{
			{Name: "unit tests", Category: models.PullRequestCheckCategoryTest, Status: models.PullRequestCheckStatusFailed},
		},
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
			int64(7),
			"head-new",
			"base-new",
			summaryJSON,
			summaryJSON,
			models.PullRequestHealthEnrichmentStatusReady,
			nil,
			now,
			now,
		))
	mock.ExpectQuery("SELECT .+ FROM pull_request_health_snapshots WHERE org_id = .+ AND pull_request_id = .+ AND version = .+").
		WithArgs(pgx.NamedArgs{
			"org_id":          orgID,
			"pull_request_id": pullRequestID,
			"version":         int64(7),
		}).
		WillReturnRows(pgxmock.NewRows(prHealthSnapshotTestColumns).AddRow(
			pullRequestID, orgID, int64(7), "head-new", "base-new", summaryJSON, nil, []byte(`{"checks":[{"name":"unit tests"}]}`), 24, models.PullRequestHealthEnrichmentStatusReady, &now, now,
		))
	mock.ExpectQuery("SELECT .+ FROM pull_request_repair_runs").
		WithArgs(pgx.NamedArgs{
			"org_id":          orgID,
			"pull_request_id": pullRequestID,
			"health_version":  int64(7),
		}).
		WillReturnRows(pgxmock.NewRows(prRepairRunTestColumns).
			AddRow(uuid.New(), orgID, pullRequestID, sessionID, &threadID, models.PullRequestRepairActionTypeFixTests, int64(7), models.PullRequestRepairWorkspaceModeSnapshotContinuation, true, nil, now, now).
			AddRow(uuid.New(), orgID, pullRequestID, terminalSessionID, (*uuid.UUID)(nil), models.PullRequestRepairActionTypeResolveConflicts, int64(7), models.PullRequestRepairWorkspaceModePRHeadReconstruction, true, nil, now, now))
	mock.ExpectQuery("SELECT .+ FROM sessions WHERE org_id = .+ AND id = ANY\\(@ids\\) AND deleted_at IS NULL").
		WithArgs(pgx.NamedArgs{
			"org_id": orgID,
			"ids":    []uuid.UUID{sessionID, terminalSessionID},
		}).
		WillReturnRows(pgxmock.NewRows(prHealthSessionColumns).
			AddRow(newPRHealthSessionRow(sessionID, orgID, now, "running")...).
			AddRow(newPRHealthSessionRow(terminalSessionID, orgID, now, models.SessionStatusCompleted)...))

	service := &PRService{
		pullRequests: db.NewPullRequestStore(mock),
		sessions:     db.NewSessionStore(mock),
		logger:       zerolog.New(io.Discard),
	}

	resp, err := service.buildPullRequestHealthResponse(context.Background(), models.PullRequest{
		ID:               pullRequestID,
		OrgID:            orgID,
		GitHubPRNumber:   42,
		GitHubRepo:       "assembledhq/143",
		GitHubPRURL:      "https://github.com/assembledhq/143/pull/42",
		Status:           "open",
		MergeState:       models.PullRequestMergeStateClean,
		HasConflicts:     false,
		FailingTestCount: 0,
		NeedsAgentAction: false,
		HealthVersion:    7,
	})
	require.NoError(t, err, "buildPullRequestHealthResponse should succeed")
	require.Len(t, resp.ActiveRepairs, 1, "buildPullRequestHealthResponse should only include active repairs whose linked sessions are still non-terminal")
	require.Equal(t, models.PullRequestRepairActionTypeFixTests, resp.ActiveRepairs[0].ActionType, "buildPullRequestHealthResponse should surface the running repair action")
	require.Equal(t, sessionID, resp.ActiveRepairs[0].SessionID, "buildPullRequestHealthResponse should surface the running repair session")
	require.Equal(t, &threadID, resp.ActiveRepairs[0].ThreadID, "buildPullRequestHealthResponse should surface the running repair thread")
	require.Equal(t, models.SessionStatusRunning, resp.ActiveRepairs[0].SessionStatus, "buildPullRequestHealthResponse should surface the linked session status")
	require.False(t, resp.CanMerge, "buildPullRequestHealthResponse should suppress merge while a repair is active for the current health version")
	require.NoError(t, mock.ExpectationsWereMet(), "all active repair health queries should be executed")
}

func TestPRServiceBuildPullRequestHealthResponseLoadsSnapshotDetails(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgx mock pool")
	defer mock.Close()

	orgID := uuid.New()
	pullRequestID := uuid.New()
	now := time.Now().UTC()
	summary := models.PullRequestHealthSummary{
		MergeState:       models.PullRequestMergeStateClean,
		HasConflicts:     false,
		FailingTestCount: 1,
		NeedsAgentAction: true,
	}
	summaryJSON, err := json.Marshal(summary)
	require.NoError(t, err, "should marshal current health summary")

	mock.ExpectQuery("SELECT .+ FROM pull_request_health_current WHERE org_id = .+ AND pull_request_id = .+").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": pullRequestID}).
		WillReturnRows(pgxmock.NewRows(prHealthCurrentTestColumns).AddRow(
			pullRequestID, orgID, int64(4), "head-ready", "base-ready", summaryJSON, summaryJSON, models.PullRequestHealthEnrichmentStatusReady, nil, now, now,
		))
	mock.ExpectQuery("SELECT .+ FROM pull_request_health_snapshots WHERE org_id = .+ AND pull_request_id = .+ AND version = .+").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": pullRequestID, "version": int64(4)}).
		WillReturnRows(pgxmock.NewRows(prHealthSnapshotTestColumns).AddRow(
			pullRequestID, orgID, int64(4), "head-ready", "base-ready", summaryJSON, []byte(`{"conflict":true}`), []byte(`{"checks":[1]}`), 12, models.PullRequestHealthEnrichmentStatusReady, nil, now,
		))

	service := &PRService{
		pullRequests: db.NewPullRequestStore(mock),
		logger:       zerolog.New(io.Discard),
	}
	headSHA := "stale-head"
	baseSHA := "stale-base"
	resp, err := service.buildPullRequestHealthResponse(context.Background(), models.PullRequest{
		ID:               pullRequestID,
		OrgID:            orgID,
		GitHubPRNumber:   52,
		GitHubRepo:       "assembledhq/143",
		GitHubPRURL:      "https://github.com/assembledhq/143/pull/52",
		Status:           "open",
		MergeState:       models.PullRequestMergeStateUnknown,
		HasConflicts:     false,
		FailingTestCount: 0,
		NeedsAgentAction: false,
		HeadSHA:          &headSHA,
		BaseSHA:          &baseSHA,
		HealthVersion:    2,
	})
	require.NoError(t, err, "buildPullRequestHealthResponse should succeed")
	require.Equal(t, "head-ready", resp.HeadSHA, "response should use the current health head SHA")
	require.Equal(t, "base-ready", resp.BaseSHA, "response should use the current health base SHA")
	require.True(t, resp.EnrichmentReady, "response should mark ready enrichment")
	require.True(t, resp.ConflictDetailAvailable, "response should expose conflict detail availability from the snapshot")
	require.True(t, resp.FailingTestDetailAvailable, "response should expose failing test detail availability from the snapshot")
	require.NoError(t, mock.ExpectationsWereMet(), "all health snapshot expectations should be met")
}

func TestPRServiceBuildPullRequestHealthResponseNormalizesLegacyCheckStatuses(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgx mock pool")
	defer mock.Close()

	orgID := uuid.New()
	pullRequestID := uuid.New()
	now := time.Now().UTC()
	legacySummaryJSON := []byte(`{
		"merge_state":"clean",
		"has_conflicts":false,
		"failing_test_count":1,
		"needs_agent_action":true,
		"checks":[
			{"name":"unit tests","category":"test"},
			{"name":"e2e","category":"test"},
			{"name":"eslint","category":"lint"}
		]
	}`)

	mock.ExpectQuery("SELECT .+ FROM pull_request_health_current WHERE org_id = .+ AND pull_request_id = .+").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": pullRequestID}).
		WillReturnRows(pgxmock.NewRows(prHealthCurrentTestColumns).AddRow(
			pullRequestID, orgID, int64(4), "head-ready", "base-ready", legacySummaryJSON, legacySummaryJSON, models.PullRequestHealthEnrichmentStatusNotRequested, nil, now, now,
		))

	service := &PRService{
		pullRequests: db.NewPullRequestStore(mock),
		logger:       zerolog.New(io.Discard),
	}

	resp, err := service.buildPullRequestHealthResponse(context.Background(), models.PullRequest{
		ID:             pullRequestID,
		OrgID:          orgID,
		GitHubPRNumber: 52,
		GitHubRepo:     "assembledhq/143",
		GitHubPRURL:    "https://github.com/assembledhq/143/pull/52",
		Status:         "open",
		MergeState:     models.PullRequestMergeStateUnknown,
		HealthVersion:  2,
	})
	require.NoError(t, err, "buildPullRequestHealthResponse should succeed")
	require.Len(t, resp.Checks, 3, "response should include all legacy checks")
	require.Equal(t, models.PullRequestCheckStatusFailed, resp.Checks[0].Status, "response should infer the first failing legacy test check as failed")
	require.Equal(t, models.PullRequestCheckStatusPending, resp.Checks[1].Status, "response should infer remaining legacy test checks as pending")
	require.Equal(t, models.PullRequestCheckStatusPending, resp.Checks[2].Status, "response should keep legacy non-test checks pending when no explicit failure signal exists")
	require.NoError(t, mock.ExpectationsWereMet(), "all health current queries should be executed")
}

func TestPRServiceBuildPullRequestHealthResponseRespectsStoredChecksConfirmed(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgx mock pool")
	defer mock.Close()

	orgID := uuid.New()
	pullRequestID := uuid.New()
	now := time.Now().UTC()
	summaryJSON := []byte(`{
		"merge_state":"clean",
		"has_conflicts":false,
		"failing_test_count":0,
		"needs_agent_action":false,
		"checks_confirmed":false,
		"checks":[]
	}`)

	mock.ExpectQuery("SELECT .+ FROM pull_request_health_current WHERE org_id = .+ AND pull_request_id = .+").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": pullRequestID}).
		WillReturnRows(pgxmock.NewRows(prHealthCurrentTestColumns).AddRow(
			pullRequestID, orgID, int64(2), "head-new", "base-new", summaryJSON, summaryJSON, models.PullRequestHealthEnrichmentStatusNotRequested, nil, now, now,
		))

	service := &PRService{
		pullRequests: db.NewPullRequestStore(mock),
		logger:       zerolog.New(io.Discard),
	}

	resp, err := service.buildPullRequestHealthResponse(context.Background(), models.PullRequest{
		ID:             pullRequestID,
		OrgID:          orgID,
		GitHubPRNumber: 42,
		GitHubRepo:     "assembledhq/143",
		GitHubPRURL:    "https://github.com/assembledhq/143/pull/42",
		Status:         "open",
		MergeState:     models.PullRequestMergeStateUnknown,
		HealthVersion:  2,
	})
	require.NoError(t, err, "buildPullRequestHealthResponse should succeed")
	require.False(t, resp.ChecksConfirmed, "response should preserve an unconfirmed check state from the stored summary")
	require.False(t, resp.CanMerge, "response should keep merge disabled until checks are confirmed")
	require.Contains(t, resp.Summary, "waiting for required checks", "response summary should describe the in-flight check state")
	require.NoError(t, mock.ExpectationsWereMet(), "all health current queries should be executed")
}

func TestPRServiceBuildPullRequestHealthResponseMarksConfirmedZeroChecksMergeable(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgx mock pool")
	defer mock.Close()

	orgID := uuid.New()
	pullRequestID := uuid.New()
	now := time.Now().UTC()
	summaryJSON := []byte(`{
		"merge_state":"clean",
		"has_conflicts":false,
		"failing_test_count":0,
		"needs_agent_action":false,
		"checks_confirmed":true,
		"checks":[]
	}`)

	mock.ExpectQuery("SELECT .+ FROM pull_request_health_current WHERE org_id = .+ AND pull_request_id = .+").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": pullRequestID}).
		WillReturnRows(pgxmock.NewRows(prHealthCurrentTestColumns).AddRow(
			pullRequestID, orgID, int64(4), "head-ready", "base-ready", summaryJSON, summaryJSON, models.PullRequestHealthEnrichmentStatusNotRequested, nil, now, now,
		))

	service := &PRService{
		pullRequests: db.NewPullRequestStore(mock),
		logger:       zerolog.New(io.Discard),
	}

	resp, err := service.buildPullRequestHealthResponse(context.Background(), models.PullRequest{
		ID:             pullRequestID,
		OrgID:          orgID,
		GitHubPRNumber: 52,
		GitHubRepo:     "assembledhq/143",
		GitHubPRURL:    "https://github.com/assembledhq/143/pull/52",
		Status:         "open",
		MergeState:     models.PullRequestMergeStateUnknown,
		HealthVersion:  2,
	})
	require.NoError(t, err, "buildPullRequestHealthResponse should succeed")
	require.True(t, resp.ChecksConfirmed, "response should mark current GitHub checks as confirmed even when none are configured")
	require.True(t, resp.CanMerge, "response should allow merge when GitHub confirmed a clean PR with no checks configured")
	require.Equal(t, "PR #52 is mergeable. No CI checks are configured for this repository.", resp.Summary, "response should explain the zero-check mergeable state")
	require.NoError(t, mock.ExpectationsWereMet(), "all health current queries should be executed")
}

func TestPRServiceGetPullRequestHealthEnqueuesSyncAndEnrichment(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgx mock pool")
	defer mock.Close()

	orgID := uuid.New()
	pullRequestID := uuid.New()
	now := time.Now().UTC()
	stale := now.Add(-5 * time.Minute)
	summary := models.PullRequestHealthSummary{
		MergeState:       models.PullRequestMergeStateClean,
		HasConflicts:     false,
		FailingTestCount: 2,
		NeedsAgentAction: true,
	}
	summaryJSON, err := json.Marshal(summary)
	require.NoError(t, err, "should marshal health summary")

	mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(prTestPullRequestColumns).AddRow(
			pullRequestID, nil, orgID, 42, "https://github.com/assembledhq/143/pull/42", "assembledhq/143",
			"Fix bug", (*string)(nil), "open", "pending", "app", "", nil, nil, nil,
			models.PullRequestMergeStateUnknown, false, 0, false, &stale, int64(2), models.PullRequestMergeWhenReadyStateOff, (*uuid.UUID)(nil), (*time.Time)(nil), "", (*int64)(nil), "", (*time.Time)(nil), (*time.Time)(nil), now, now,
		))
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgx.NamedArgs{
			"org_id":     orgID,
			"queue":      "default",
			"job_type":   "sync_pull_request_state",
			"payload":    pgxmock.AnyArg(),
			"priority":   6,
			"dedupe_key": pgxmock.AnyArg(),
		}).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
	mock.ExpectQuery("SELECT .+ FROM pull_request_health_current WHERE org_id = .+ AND pull_request_id = .+").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": pullRequestID}).
		WillReturnRows(pgxmock.NewRows(prHealthCurrentTestColumns).AddRow(
			pullRequestID, orgID, int64(3), "head", "base", summaryJSON, summaryJSON, models.PullRequestHealthEnrichmentStatusNotRequested, nil, now, now,
		))
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgx.NamedArgs{
			"org_id":     orgID,
			"queue":      "default",
			"job_type":   "enrich_pull_request_health",
			"payload":    pgxmock.AnyArg(),
			"priority":   4,
			"dedupe_key": pgxmock.AnyArg(),
		}).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))

	service := &PRService{
		pullRequests: db.NewPullRequestStore(mock),
		jobs:         db.NewJobStore(mock),
		logger:       zerolog.New(io.Discard),
	}

	resp, err := service.GetPullRequestHealth(context.Background(), orgID, pullRequestID)
	require.NoError(t, err, "GetPullRequestHealth should succeed for stale pull requests")
	require.Equal(t, 2, resp.FailingTestCount, "GetPullRequestHealth should return the current failing test count")
	require.True(t, resp.CanFixTests, "GetPullRequestHealth should advertise test repair when tests are failing")
	require.True(t, resp.EnrichmentRequested, "GetPullRequestHealth should mark enrichment as requested after enqueueing it")
	require.NoError(t, mock.ExpectationsWereMet(), "all stale health expectations should be met")
}

func TestPullRequestStateSyncDedupeKey(t *testing.T) {
	t.Parallel()

	prID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	tests := []struct {
		name     string
		scope    string
		expected string
	}{
		{
			name:     "generic per pull request sync key",
			scope:    "",
			expected: "sync_pull_request_state:11111111-1111-1111-1111-111111111111",
		},
		{
			name:     "check completion sync key",
			scope:    "check_run_completed",
			expected: "sync_pull_request_state:11111111-1111-1111-1111-111111111111:check_run_completed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.expected, pullRequestStateSyncDedupeKey(prID, tt.scope), "dedupe key should include the optional sync wake-up scope")
		})
	}
}

func TestPRServiceGetPullRequestHealthEnqueuesFollowUpWhenInlineMergeabilityPending(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgx mock pool")
	defer mock.Close()

	orgID := uuid.New()
	pullRequestID := uuid.New()
	repoID := uuid.New()
	integrationID := uuid.New()
	now := time.Now().UTC()
	headSHA := "head-pending"
	baseSHA := "base-pending"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/assembledhq/143/pulls/42":
			_, _ = w.Write([]byte(`{"number":42,"html_url":"https://github.com/assembledhq/143/pull/42","state":"open","mergeable":null,"mergeable_state":"unknown","head":{"ref":"feature","sha":"head-pending"},"base":{"ref":"main","sha":"base-pending"}}`))
		case "/repos/assembledhq/143/commits/head-pending/check-runs":
			_, _ = w.Write([]byte(`{"check_runs":[{"id":11,"name":"unit tests","html_url":"https://example.com/tests","conclusion":"success","status":"completed","details_url":"https://example.com/tests/details","app":{"slug":"github-actions"},"output":{"title":"","summary":"","text":"","annotations_count":0,"annotations_url":""}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	pendingSummary := models.PullRequestHealthSummary{
		MergeState:      models.PullRequestMergeStateMergeabilityPending,
		ChecksConfirmed: true,
		Checks: []models.PullRequestCheckSummary{
			{Name: "unit tests", Category: models.PullRequestCheckCategoryTest, Status: models.PullRequestCheckStatusPassed, Provider: "github-actions", DetailsURL: "https://example.com/tests/details"},
		},
	}
	pendingSummaryJSON, err := json.Marshal(pendingSummary)
	require.NoError(t, err, "should marshal pending health summary")

	mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(prTestPullRequestColumns).AddRow(
			pullRequestID, nil, orgID, 42, "https://github.com/assembledhq/143/pull/42", "assembledhq/143",
			"Pending mergeability", (*string)(nil), "open", "pending", "app", "", nil, nil, nil,
			models.PullRequestMergeStateUnknown, false, 0, false, (*time.Time)(nil), int64(0), models.PullRequestMergeWhenReadyStateOff, (*uuid.UUID)(nil), (*time.Time)(nil), "", (*int64)(nil), "", (*time.Time)(nil), (*time.Time)(nil), now, now,
		))
	mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(prTestPullRequestColumns).AddRow(
			pullRequestID, nil, orgID, 42, "https://github.com/assembledhq/143/pull/42", "assembledhq/143",
			"Pending mergeability", (*string)(nil), "open", "pending", "app", "", nil, nil, nil,
			models.PullRequestMergeStateUnknown, false, 0, false, (*time.Time)(nil), int64(0), models.PullRequestMergeWhenReadyStateOff, (*uuid.UUID)(nil), (*time.Time)(nil), "", (*int64)(nil), "", (*time.Time)(nil), (*time.Time)(nil), now, now,
		))
	mock.ExpectQuery("SELECT .+ FROM repositories WHERE org_id = .+ AND full_name = .+ AND status = 'active'").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "full_name": "assembledhq/143"}).
		WillReturnRows(pgxmock.NewRows(prTestRepoColumns).AddRow(
			repoID, orgID, integrationID, int64(1), "assembledhq/143", "main", false, nil, nil, "https://github.com/assembledhq/143.git", int64(123), "active", nil, nil, []byte(`{}`), now, now,
		))
	mock.ExpectQuery("SELECT .+ FROM pull_request_health_current").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": pullRequestID}).
		WillReturnRows(pgxmock.NewRows(prHealthCurrentTestColumns))
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .+ FROM pull_request_health_current").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": pullRequestID}).
		WillReturnRows(pgxmock.NewRows(prHealthCurrentTestColumns))
	mock.ExpectExec("INSERT INTO pull_request_health_snapshots").
		WithArgs(pgx.NamedArgs{
			"pull_request_id":   pullRequestID,
			"org_id":            orgID,
			"version":           int64(1),
			"head_sha":          headSHA,
			"base_sha":          baseSHA,
			"summary_json":      pgxmock.AnyArg(),
			"enrichment_status": models.PullRequestHealthEnrichmentStatusNotRequested,
		}).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec("UPDATE pull_request_repair_runs").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": pullRequestID, "version": int64(1)}).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectExec("INSERT INTO pull_request_health_current").
		WithArgs(pgx.NamedArgs{
			"pull_request_id":      pullRequestID,
			"org_id":               orgID,
			"version":              int64(1),
			"head_sha":             headSHA,
			"base_sha":             baseSHA,
			"summary_json":         pgxmock.AnyArg(),
			"summary_preview_json": pgxmock.AnyArg(),
			"enrichment_status":    models.PullRequestHealthEnrichmentStatusNotRequested,
			"enriched_at":          (*time.Time)(nil),
			"created_at":           pgxmock.AnyArg(),
			"updated_at":           pgxmock.AnyArg(),
		}).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec("UPDATE pull_requests").
		WithArgs(pgx.NamedArgs{
			"pull_request_id":    pullRequestID,
			"org_id":             orgID,
			"head_sha":           headSHA,
			"base_sha":           baseSHA,
			"merge_state":        models.PullRequestMergeStateMergeabilityPending,
			"has_conflicts":      false,
			"failing_test_count": 0,
			"needs_agent_action": false,
			"version":            int64(1),
		}).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()
	mock.ExpectExec("UPDATE pull_requests SET ci_status").
		WithArgs(pgx.NamedArgs{"id": pullRequestID, "org_id": orgID, "ci_status": models.PullRequestCIStatusSuccess}).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgx.NamedArgs{
			"org_id":     orgID,
			"queue":      "default",
			"job_type":   "sync_pull_request_state",
			"payload":    pgxmock.AnyArg(),
			"priority":   6,
			"dedupe_key": pgxmock.AnyArg(),
		}).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
	mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(prTestPullRequestColumns).AddRow(
			pullRequestID, nil, orgID, 42, "https://github.com/assembledhq/143/pull/42", "assembledhq/143",
			"Pending mergeability", (*string)(nil), "open", "success", "app", "", &headSHA, &baseSHA, nil,
			models.PullRequestMergeStateMergeabilityPending, false, 0, false, &now, int64(1), models.PullRequestMergeWhenReadyStateOff, (*uuid.UUID)(nil), (*time.Time)(nil), "", (*int64)(nil), "", (*time.Time)(nil), (*time.Time)(nil), now, now,
		))
	mock.ExpectQuery("SELECT .+ FROM pull_request_health_current WHERE org_id = .+ AND pull_request_id = .+").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": pullRequestID}).
		WillReturnRows(pgxmock.NewRows(prHealthCurrentTestColumns).AddRow(
			pullRequestID, orgID, int64(1), headSHA, baseSHA, pendingSummaryJSON, pendingSummaryJSON, models.PullRequestHealthEnrichmentStatusNotRequested, nil, now, now,
		))

	service := &PRService{
		tokenProvider:           &Service{cache: map[int64]*cachedToken{}},
		pullRequests:            db.NewPullRequestStore(mock),
		repos:                   db.NewRepositoryStore(mock),
		jobs:                    db.NewJobStore(mock),
		logger:                  zerolog.New(io.Discard),
		baseURL:                 server.URL,
		httpClient:              server.Client(),
		mergeabilityRetryDelays: []time.Duration{},
	}
	service.tokenProvider.cache[123] = &cachedToken{Token: "install-token", ExpiresAt: time.Now().Add(time.Hour)}

	resp, err := service.GetPullRequestHealth(context.Background(), orgID, pullRequestID)
	require.NoError(t, err, "GetPullRequestHealth should return pending mergeability without surfacing the retry sentinel")
	require.Equal(t, models.PullRequestMergeStateMergeabilityPending, resp.MergeState, "response should expose pending mergeability")
	require.False(t, resp.CanMerge, "response should keep merge disabled while mergeability is pending")
	require.NoError(t, mock.ExpectationsWereMet(), "all inline pending health expectations should be met")
}

func TestPRServiceSyncPullRequestState(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgx mock pool")
	defer mock.Close()

	orgID := uuid.New()
	pullRequestID := uuid.New()
	repoID := uuid.New()
	integrationID := uuid.New()
	now := time.Now().UTC()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/assembledhq/143/pulls/42":
			require.Equal(t, "token install-token", r.Header.Get("Authorization"), "GitHub requests should use the installation token")
			_, _ = w.Write([]byte(`{"number":42,"html_url":"https://github.com/assembledhq/143/pull/42","state":"open","mergeable":false,"mergeable_state":"dirty","head":{"ref":"feature","sha":"head-sync"},"base":{"ref":"main","sha":"base-sync"}}`))
		case "/repos/assembledhq/143/commits/head-sync/check-runs":
			_, _ = w.Write([]byte(`{"check_runs":[{"id":11,"name":"unit tests","html_url":"https://example.com/tests","conclusion":"failure","status":"completed","details_url":"https://example.com/tests/details","app":{"slug":"github-actions"},"output":{"title":"2 tests failed","summary":"something failed","text":"traceback","annotations_count":0,"annotations_url":""}},{"id":12,"name":"eslint","html_url":"https://example.com/lint","conclusion":"success","status":"completed","details_url":"https://example.com/lint/details","app":{"slug":"github-actions"},"output":{"title":"","summary":"","text":"","annotations_count":0,"annotations_url":""}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(prTestPullRequestColumns).AddRow(
			pullRequestID, nil, orgID, 42, "https://github.com/assembledhq/143/pull/42", "assembledhq/143",
			"Fix bug", (*string)(nil), "open", "pending", "app", "", nil, nil, nil,
			models.PullRequestMergeStateUnknown, false, 0, false, (*time.Time)(nil), int64(0), models.PullRequestMergeWhenReadyStateOff, (*uuid.UUID)(nil), (*time.Time)(nil), "", (*int64)(nil), "", (*time.Time)(nil), (*time.Time)(nil), now, now,
		))
	mock.ExpectQuery("SELECT .+ FROM repositories WHERE org_id = .+ AND full_name = .+ AND status = 'active'").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "full_name": "assembledhq/143"}).
		WillReturnRows(pgxmock.NewRows(prTestRepoColumns).AddRow(
			repoID, orgID, integrationID, int64(1), "assembledhq/143", "main", false, nil, nil, "https://github.com/assembledhq/143.git", int64(123), "active", nil, nil, []byte(`{}`), now, now,
		))
	mock.ExpectQuery("SELECT .+ FROM pull_request_health_current").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": pullRequestID}).
		WillReturnRows(pgxmock.NewRows(prHealthCurrentTestColumns))
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .+ FROM pull_request_health_current").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": pullRequestID}).
		WillReturnRows(pgxmock.NewRows(prHealthCurrentTestColumns))
	mock.ExpectExec("INSERT INTO pull_request_health_snapshots").
		WithArgs(pgx.NamedArgs{
			"pull_request_id":   pullRequestID,
			"org_id":            orgID,
			"version":           int64(1),
			"head_sha":          "head-sync",
			"base_sha":          "base-sync",
			"summary_json":      pgxmock.AnyArg(),
			"enrichment_status": models.PullRequestHealthEnrichmentStatusNotRequested,
		}).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec("UPDATE pull_request_repair_runs").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": pullRequestID, "version": int64(1)}).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectExec("INSERT INTO pull_request_health_current").
		WithArgs(pgx.NamedArgs{
			"pull_request_id":      pullRequestID,
			"org_id":               orgID,
			"version":              int64(1),
			"head_sha":             "head-sync",
			"base_sha":             "base-sync",
			"summary_json":         pgxmock.AnyArg(),
			"summary_preview_json": pgxmock.AnyArg(),
			"enrichment_status":    models.PullRequestHealthEnrichmentStatusNotRequested,
			"enriched_at":          (*time.Time)(nil),
			"created_at":           pgxmock.AnyArg(),
			"updated_at":           pgxmock.AnyArg(),
		}).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec("UPDATE pull_requests").
		WithArgs(pgx.NamedArgs{
			"pull_request_id":    pullRequestID,
			"org_id":             orgID,
			"head_sha":           "head-sync",
			"base_sha":           "base-sync",
			"merge_state":        models.PullRequestMergeStateConflicted,
			"has_conflicts":      true,
			"failing_test_count": 1,
			"needs_agent_action": true,
			"version":            int64(1),
		}).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()
	mock.ExpectExec("UPDATE pull_requests SET ci_status").
		WithArgs(pgx.NamedArgs{"id": pullRequestID, "org_id": orgID, "ci_status": models.PullRequestCIStatusFailure}).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	service := &PRService{
		tokenProvider: &Service{cache: map[int64]*cachedToken{}},
		pullRequests:  db.NewPullRequestStore(mock),
		repos:         db.NewRepositoryStore(mock),
		logger:        zerolog.New(io.Discard),
		baseURL:       server.URL,
		httpClient:    server.Client(),
	}
	service.tokenProvider.cache[123] = &cachedToken{Token: "install-token", ExpiresAt: time.Now().Add(time.Hour)}

	err = service.SyncPullRequestState(context.Background(), orgID, pullRequestID)
	require.NoError(t, err, "SyncPullRequestState should synchronize GitHub pull request state")
	require.NoError(t, mock.ExpectationsWereMet(), "all sync expectations should be met")
}

// When GitHub reports a PR closed-and-merged but our DB still has it open, the
// pull_request:closed webhook never landed. The sync must reconcile by flipping
// status to "merged" itself; otherwise reconciliation just keeps refreshing
// github_state_synced_at while leaving the PR row stuck on
// status=open/merge_state=clean — surfaced as "synced just now" + a green
// "Mergeable" badge for an already-merged PR.
func TestPRServiceSyncPullRequestStateSelfHealsMergedDrift(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgx mock pool")
	defer mock.Close()

	orgID := uuid.New()
	pullRequestID := uuid.New()
	repoID := uuid.New()
	integrationID := uuid.New()
	now := time.Now().UTC()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/assembledhq/143/pulls/42":
			_, _ = w.Write([]byte(`{"number":42,"html_url":"https://github.com/assembledhq/143/pull/42","state":"closed","merged":true,"merge_commit_sha":"merge-commit-sha","mergeable":true,"mergeable_state":"clean","head":{"ref":"feature","sha":"head-sync"},"base":{"ref":"main","sha":"base-sync"}}`))
		default:
			t.Fatalf("self-heal path should not call %s; closed PRs skip the check_runs and snapshot writes", r.URL.Path)
		}
	}))
	defer server.Close()

	mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(prTestPullRequestColumns).AddRow(
			pullRequestID, (*uuid.UUID)(nil), orgID, 42, "https://github.com/assembledhq/143/pull/42", "assembledhq/143",
			"Fix bug", (*string)(nil), "open", "pending", "app", "", nil, nil, nil,
			models.PullRequestMergeStateUnknown, false, 0, false, (*time.Time)(nil), int64(0), models.PullRequestMergeWhenReadyStateOff, (*uuid.UUID)(nil), (*time.Time)(nil), "", (*int64)(nil), "", (*time.Time)(nil), (*time.Time)(nil), now, now,
		))
	mock.ExpectQuery("SELECT .+ FROM repositories WHERE org_id = .+ AND full_name = .+ AND status = 'active'").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "full_name": "assembledhq/143"}).
		WillReturnRows(pgxmock.NewRows(prTestRepoColumns).AddRow(
			repoID, orgID, integrationID, int64(1), "assembledhq/143", "main", false, nil, nil, "https://github.com/assembledhq/143.git", int64(123), "active", nil, nil, []byte(`{}`), now, now,
		))
	// Self-heal must run UpdateStatus("merged"). The merged-status branch sets
	// merged_at = now() in the same statement (see PullRequestStore.UpdateStatus).
	mock.ExpectExec("UPDATE pull_requests SET status = .+ merged_at = now").
		WithArgs(pgx.NamedArgs{"id": pullRequestID, "org_id": orgID, "status": models.PullRequestStatusMerged}).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	// Service is wired with nil sessions/issues/deploys/jobs/orgs/previews so
	// runMergedPullRequestFollowUps short-circuits — those side effects already
	// have dedicated coverage in pr_handlers_test.go. This test focuses on the
	// status-drift reconciliation that was missing before.
	service := &PRService{
		tokenProvider: &Service{cache: map[int64]*cachedToken{}},
		pullRequests:  db.NewPullRequestStore(mock),
		repos:         db.NewRepositoryStore(mock),
		logger:        zerolog.New(io.Discard),
		baseURL:       server.URL,
		httpClient:    server.Client(),
	}
	service.tokenProvider.cache[123] = &cachedToken{Token: "install-token", ExpiresAt: time.Now().Add(time.Hour)}

	err = service.SyncPullRequestState(context.Background(), orgID, pullRequestID)
	require.NoError(t, err, "SyncPullRequestState should succeed when self-healing merged drift")
	require.NoError(t, mock.ExpectationsWereMet(), "self-heal should issue UpdateStatus(merged) and skip the health snapshot path")
}

// Same drift, but the PR was closed without merging. Sync should flip status
// to "closed" and skip the snapshot path. Distinct from the merged case
// because the close branch runs different follow-ups (no deploy row, no
// evaluate_experiment job).
func TestPRServiceSyncPullRequestStateSelfHealsClosedWithoutMergeDrift(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgx mock pool")
	defer mock.Close()

	orgID := uuid.New()
	pullRequestID := uuid.New()
	repoID := uuid.New()
	integrationID := uuid.New()
	now := time.Now().UTC()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/assembledhq/143/pulls/42":
			_, _ = w.Write([]byte(`{"number":42,"html_url":"https://github.com/assembledhq/143/pull/42","state":"closed","merged":false,"mergeable":null,"mergeable_state":"unknown","head":{"ref":"feature","sha":"head-sync"},"base":{"ref":"main","sha":"base-sync"}}`))
		default:
			t.Fatalf("self-heal path should not call %s; closed PRs skip the check_runs and snapshot writes", r.URL.Path)
		}
	}))
	defer server.Close()

	mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(prTestPullRequestColumns).AddRow(
			pullRequestID, (*uuid.UUID)(nil), orgID, 42, "https://github.com/assembledhq/143/pull/42", "assembledhq/143",
			"Fix bug", (*string)(nil), "open", "pending", "app", "", nil, nil, nil,
			models.PullRequestMergeStateUnknown, false, 0, false, (*time.Time)(nil), int64(0), models.PullRequestMergeWhenReadyStateOff, (*uuid.UUID)(nil), (*time.Time)(nil), "", (*int64)(nil), "", (*time.Time)(nil), (*time.Time)(nil), now, now,
		))
	mock.ExpectQuery("SELECT .+ FROM repositories WHERE org_id = .+ AND full_name = .+ AND status = 'active'").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "full_name": "assembledhq/143"}).
		WillReturnRows(pgxmock.NewRows(prTestRepoColumns).AddRow(
			repoID, orgID, integrationID, int64(1), "assembledhq/143", "main", false, nil, nil, "https://github.com/assembledhq/143.git", int64(123), "active", nil, nil, []byte(`{}`), now, now,
		))
	mock.ExpectExec("UPDATE pull_requests SET status").
		WithArgs(pgx.NamedArgs{"id": pullRequestID, "org_id": orgID, "status": models.PullRequestStatusClosed}).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	service := &PRService{
		tokenProvider: &Service{cache: map[int64]*cachedToken{}},
		pullRequests:  db.NewPullRequestStore(mock),
		repos:         db.NewRepositoryStore(mock),
		logger:        zerolog.New(io.Discard),
		baseURL:       server.URL,
		httpClient:    server.Client(),
	}
	service.tokenProvider.cache[123] = &cachedToken{Token: "install-token", ExpiresAt: time.Now().Add(time.Hour)}

	err = service.SyncPullRequestState(context.Background(), orgID, pullRequestID)
	require.NoError(t, err, "SyncPullRequestState should succeed when self-healing closed-without-merge drift")
	require.NoError(t, mock.ExpectationsWereMet(), "self-heal should issue UpdateStatus(closed) and skip the health snapshot path")
}

// When GitHub returns mergeable=null while it recomputes (without an explicit
// dirty/blocked label) on the same head SHA where we already know the PR is
// conflicted, persisting the new snapshot would clobber has_conflicts=true
// with false and break the "Resolve conflicts" repair button. The sync should
// skip the write and let the next sync pick up GitHub's resolved value.
func TestPRServiceSyncPullRequestStateSkipsIndeterminateMergeRegression(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgx mock pool")
	defer mock.Close()

	orgID := uuid.New()
	pullRequestID := uuid.New()
	repoID := uuid.New()
	integrationID := uuid.New()
	now := time.Now().UTC()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/assembledhq/143/pulls/42":
			_, _ = w.Write([]byte(`{"number":42,"html_url":"https://github.com/assembledhq/143/pull/42","state":"open","mergeable":null,"mergeable_state":"unknown","head":{"ref":"feature","sha":"head-flap"},"base":{"ref":"main","sha":"base-flap"}}`))
		case "/repos/assembledhq/143/commits/head-flap/check-runs":
			_, _ = w.Write([]byte(`{"check_runs":[]}`))
		case "/repos/assembledhq/143/branches/main":
			_, _ = w.Write([]byte(`{"protected":true,"protection":{"required_status_checks":{"contexts":["ci-build"]}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	priorSummary := models.PullRequestHealthSummary{
		MergeState:       models.PullRequestMergeStateConflicted,
		HasConflicts:     true,
		FailingTestCount: 0,
		NeedsAgentAction: true,
	}
	priorJSON, err := json.Marshal(priorSummary)
	require.NoError(t, err)

	mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(prTestPullRequestColumns).AddRow(
			pullRequestID, nil, orgID, 42, "https://github.com/assembledhq/143/pull/42", "assembledhq/143",
			"Flaky PR", (*string)(nil), "open", "pending", "app", "", nil, nil, nil,
			models.PullRequestMergeStateConflicted, true, 0, true, (*time.Time)(nil), int64(3), models.PullRequestMergeWhenReadyStateOff, (*uuid.UUID)(nil), (*time.Time)(nil), "", (*int64)(nil), "", (*time.Time)(nil), &now, now, now,
		))
	mock.ExpectQuery("SELECT .+ FROM repositories WHERE org_id = .+ AND full_name = .+ AND status = 'active'").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "full_name": "assembledhq/143"}).
		WillReturnRows(pgxmock.NewRows(prTestRepoColumns).AddRow(
			repoID, orgID, integrationID, int64(1), "assembledhq/143", "main", false, nil, nil, "https://github.com/assembledhq/143.git", int64(123), "active", nil, nil, []byte(`{}`), now, now,
		))
	mock.ExpectQuery("SELECT .+ FROM pull_request_health_current").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": pullRequestID}).
		WillReturnRows(pgxmock.NewRows(prHealthCurrentTestColumns).AddRow(
			pullRequestID, orgID, int64(3), "head-flap", "base-flap", priorJSON, priorJSON, models.PullRequestHealthEnrichmentStatusReady, &now, now, now,
		))

	service := &PRService{
		tokenProvider: &Service{cache: map[int64]*cachedToken{}},
		pullRequests:  db.NewPullRequestStore(mock),
		repos:         db.NewRepositoryStore(mock),
		logger:        zerolog.New(io.Discard),
		baseURL:       server.URL,
		httpClient:    server.Client(),
	}
	service.tokenProvider.cache[123] = &cachedToken{Token: "install-token", ExpiresAt: time.Now().Add(time.Hour)}

	err = service.SyncPullRequestState(context.Background(), orgID, pullRequestID)
	require.ErrorIs(t, err, ErrPullRequestMergeabilityPending, "SyncPullRequestState should ask the worker to retry indeterminate mergeability")
	require.NoError(t, mock.ExpectationsWereMet(), "no snapshot upsert should have been issued")
}

func TestPRServiceSyncPullRequestStatePersistsMergeabilityPendingAndRequestsRetry(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgx mock pool")
	defer mock.Close()

	orgID := uuid.New()
	pullRequestID := uuid.New()
	repoID := uuid.New()
	integrationID := uuid.New()
	now := time.Now().UTC()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/assembledhq/143/pulls/42":
			_, _ = w.Write([]byte(`{"number":42,"html_url":"https://github.com/assembledhq/143/pull/42","state":"open","mergeable":null,"mergeable_state":"unknown","head":{"ref":"feature","sha":"head-pending"},"base":{"ref":"main","sha":"base-pending"}}`))
		case "/repos/assembledhq/143/commits/head-pending/check-runs":
			_, _ = w.Write([]byte(`{"check_runs":[{"id":11,"name":"unit tests","html_url":"https://example.com/tests","conclusion":"success","status":"completed","details_url":"https://example.com/tests/details","app":{"slug":"github-actions"},"output":{"title":"","summary":"","text":"","annotations_count":0,"annotations_url":""}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(prTestPullRequestColumns).AddRow(
			pullRequestID, nil, orgID, 42, "https://github.com/assembledhq/143/pull/42", "assembledhq/143",
			"Pending mergeability", (*string)(nil), "open", "pending", "app", "", nil, nil, nil,
			models.PullRequestMergeStateUnknown, false, 0, false, (*time.Time)(nil), int64(0), models.PullRequestMergeWhenReadyStateOff, (*uuid.UUID)(nil), (*time.Time)(nil), "", (*int64)(nil), "", (*time.Time)(nil), (*time.Time)(nil), now, now,
		))
	mock.ExpectQuery("SELECT .+ FROM repositories WHERE org_id = .+ AND full_name = .+ AND status = 'active'").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "full_name": "assembledhq/143"}).
		WillReturnRows(pgxmock.NewRows(prTestRepoColumns).AddRow(
			repoID, orgID, integrationID, int64(1), "assembledhq/143", "main", false, nil, nil, "https://github.com/assembledhq/143.git", int64(123), "active", nil, nil, []byte(`{}`), now, now,
		))
	mock.ExpectQuery("SELECT .+ FROM pull_request_health_current").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": pullRequestID}).
		WillReturnRows(pgxmock.NewRows(prHealthCurrentTestColumns))
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .+ FROM pull_request_health_current").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": pullRequestID}).
		WillReturnRows(pgxmock.NewRows(prHealthCurrentTestColumns))
	mock.ExpectExec("INSERT INTO pull_request_health_snapshots").
		WithArgs(pgx.NamedArgs{
			"pull_request_id":   pullRequestID,
			"org_id":            orgID,
			"version":           int64(1),
			"head_sha":          "head-pending",
			"base_sha":          "base-pending",
			"summary_json":      pgxmock.AnyArg(),
			"enrichment_status": models.PullRequestHealthEnrichmentStatusNotRequested,
		}).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec("UPDATE pull_request_repair_runs").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": pullRequestID, "version": int64(1)}).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectExec("INSERT INTO pull_request_health_current").
		WithArgs(pgx.NamedArgs{
			"pull_request_id":      pullRequestID,
			"org_id":               orgID,
			"version":              int64(1),
			"head_sha":             "head-pending",
			"base_sha":             "base-pending",
			"summary_json":         pgxmock.AnyArg(),
			"summary_preview_json": pgxmock.AnyArg(),
			"enrichment_status":    models.PullRequestHealthEnrichmentStatusNotRequested,
			"enriched_at":          (*time.Time)(nil),
			"created_at":           pgxmock.AnyArg(),
			"updated_at":           pgxmock.AnyArg(),
		}).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec("UPDATE pull_requests").
		WithArgs(pgx.NamedArgs{
			"pull_request_id":    pullRequestID,
			"org_id":             orgID,
			"head_sha":           "head-pending",
			"base_sha":           "base-pending",
			"merge_state":        models.PullRequestMergeStateMergeabilityPending,
			"has_conflicts":      false,
			"failing_test_count": 0,
			"needs_agent_action": false,
			"version":            int64(1),
		}).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()
	mock.ExpectExec("UPDATE pull_requests SET ci_status").
		WithArgs(pgx.NamedArgs{"id": pullRequestID, "org_id": orgID, "ci_status": models.PullRequestCIStatusSuccess}).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	service := &PRService{
		tokenProvider:           &Service{cache: map[int64]*cachedToken{}},
		pullRequests:            db.NewPullRequestStore(mock),
		repos:                   db.NewRepositoryStore(mock),
		logger:                  zerolog.New(io.Discard),
		baseURL:                 server.URL,
		httpClient:              server.Client(),
		mergeabilityRetryDelays: []time.Duration{},
	}
	service.tokenProvider.cache[123] = &cachedToken{Token: "install-token", ExpiresAt: time.Now().Add(time.Hour)}

	err = service.SyncPullRequestState(context.Background(), orgID, pullRequestID)
	require.ErrorIs(t, err, ErrPullRequestMergeabilityPending, "SyncPullRequestState should persist pending state and request a retry")
	require.NoError(t, mock.ExpectationsWereMet(), "all pending mergeability sync expectations should be met")
}

// Same fix shape for the fix-tests path: when test-category checks are still
// in_progress on the same head SHA and the apparent failing-test count would
// regress below the prior snapshot's count, skip the write so the
// "Fix tests" button keeps working until checks finish.
func TestPRServiceSyncPullRequestStateSkipsIndeterminateTestRegression(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgx mock pool")
	defer mock.Close()

	orgID := uuid.New()
	pullRequestID := uuid.New()
	repoID := uuid.New()
	integrationID := uuid.New()
	now := time.Now().UTC()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/assembledhq/143/pulls/42":
			_, _ = w.Write([]byte(`{"number":42,"html_url":"https://github.com/assembledhq/143/pull/42","state":"open","mergeable":true,"mergeable_state":"clean","head":{"ref":"feature","sha":"head-rerun"},"base":{"ref":"main","sha":"base-rerun"}}`))
		case "/repos/assembledhq/143/commits/head-rerun/check-runs":
			// Rerun in progress: no conclusion yet, so it would not be counted as failing,
			// dropping FailingTestCount from the prior snapshot's value of 2 down to 0.
			_, _ = w.Write([]byte(`{"check_runs":[{"id":11,"name":"unit tests","status":"in_progress","conclusion":"","app":{"slug":"github-actions"},"output":{}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	priorSummary := models.PullRequestHealthSummary{
		MergeState:       models.PullRequestMergeStateClean,
		HasConflicts:     false,
		FailingTestCount: 2,
		NeedsAgentAction: true,
	}
	priorJSON, err := json.Marshal(priorSummary)
	require.NoError(t, err)

	mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(prTestPullRequestColumns).AddRow(
			pullRequestID, nil, orgID, 42, "https://github.com/assembledhq/143/pull/42", "assembledhq/143",
			"Tests rerun", (*string)(nil), "open", "pending", "app", "", nil, nil, nil,
			models.PullRequestMergeStateClean, false, 2, true, (*time.Time)(nil), int64(4), models.PullRequestMergeWhenReadyStateOff, (*uuid.UUID)(nil), (*time.Time)(nil), "", (*int64)(nil), "", (*time.Time)(nil), &now, now, now,
		))
	mock.ExpectQuery("SELECT .+ FROM repositories WHERE org_id = .+ AND full_name = .+ AND status = 'active'").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "full_name": "assembledhq/143"}).
		WillReturnRows(pgxmock.NewRows(prTestRepoColumns).AddRow(
			repoID, orgID, integrationID, int64(1), "assembledhq/143", "main", false, nil, nil, "https://github.com/assembledhq/143.git", int64(123), "active", nil, nil, []byte(`{}`), now, now,
		))
	mock.ExpectQuery("SELECT .+ FROM pull_request_health_current").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": pullRequestID}).
		WillReturnRows(pgxmock.NewRows(prHealthCurrentTestColumns).AddRow(
			pullRequestID, orgID, int64(4), "head-rerun", "base-rerun", priorJSON, priorJSON, models.PullRequestHealthEnrichmentStatusReady, &now, now, now,
		))

	service := &PRService{
		tokenProvider: &Service{cache: map[int64]*cachedToken{}},
		pullRequests:  db.NewPullRequestStore(mock),
		repos:         db.NewRepositoryStore(mock),
		logger:        zerolog.New(io.Discard),
		baseURL:       server.URL,
		httpClient:    server.Client(),
	}
	service.tokenProvider.cache[123] = &cachedToken{Token: "install-token", ExpiresAt: time.Now().Add(time.Hour)}

	err = service.SyncPullRequestState(context.Background(), orgID, pullRequestID)
	require.NoError(t, err, "SyncPullRequestState should not error when skipping an indeterminate test snapshot")
	require.NoError(t, mock.ExpectationsWereMet(), "no snapshot upsert should have been issued")
}

func TestPRServiceEnrichPullRequestHealth(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgx mock pool")
	defer mock.Close()

	orgID := uuid.New()
	pullRequestID := uuid.New()
	repoID := uuid.New()
	integrationID := uuid.New()
	now := time.Now().UTC()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/assembledhq/143/pulls/42":
			_, _ = w.Write([]byte(`{"number":42,"html_url":"https://github.com/assembledhq/143/pull/42","state":"open","mergeable":false,"mergeable_state":"dirty","head":{"ref":"feature","sha":"head-enrich"},"base":{"ref":"main","sha":"base-enrich"}}`))
		case "/repos/assembledhq/143/commits/head-enrich/check-runs":
			_, _ = w.Write([]byte(`{"check_runs":[{"id":21,"name":"unit tests","html_url":"https://example.com/tests","conclusion":"failure","status":"completed","details_url":"https://example.com/tests/details","app":{"slug":"github-actions"},"output":{"title":"2 tests failed","summary":"something failed","text":"traceback","annotations_count":1,"annotations_url":""}}]}`))
		case "/repos/assembledhq/143/check-runs/21/annotations":
			_, _ = w.Write([]byte(`[{"path":"internal/foo_test.go","start_line":12,"end_line":12,"annotation_level":"failure","message":"expected true"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(prTestPullRequestColumns).AddRow(
			pullRequestID, nil, orgID, 42, "https://github.com/assembledhq/143/pull/42", "assembledhq/143",
			"Fix bug", (*string)(nil), "open", "pending", "app", "", nil, nil, nil,
			models.PullRequestMergeStateUnknown, false, 1, true, (*time.Time)(nil), int64(5), models.PullRequestMergeWhenReadyStateOff, (*uuid.UUID)(nil), (*time.Time)(nil), "", (*int64)(nil), "", (*time.Time)(nil), (*time.Time)(nil), now, now,
		))
	mock.ExpectQuery("SELECT .+ FROM repositories WHERE org_id = .+ AND full_name = .+ AND status = 'active'").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "full_name": "assembledhq/143"}).
		WillReturnRows(pgxmock.NewRows(prTestRepoColumns).AddRow(
			repoID, orgID, integrationID, int64(1), "assembledhq/143", "main", false, nil, nil, "https://github.com/assembledhq/143.git", int64(123), "active", nil, nil, []byte(`{}`), now, now,
		))
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE pull_request_health_snapshots").
		WithArgs(pgx.NamedArgs{
			"org_id":                orgID,
			"pull_request_id":       pullRequestID,
			"version":               int64(5),
			"conflict_payload":      pgxmock.AnyArg(),
			"failing_tests_payload": pgxmock.AnyArg(),
			"payload_size_bytes":    pgxmock.AnyArg(),
			"enrichment_status":     models.PullRequestHealthEnrichmentStatusReady,
			"enriched_at":           pgxmock.AnyArg(),
		}).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE pull_request_health_current").
		WithArgs(pgx.NamedArgs{
			"org_id":            orgID,
			"pull_request_id":   pullRequestID,
			"version":           int64(5),
			"enrichment_status": models.PullRequestHealthEnrichmentStatusReady,
			"enriched_at":       pgxmock.AnyArg(),
		}).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()

	service := &PRService{
		tokenProvider: &Service{cache: map[int64]*cachedToken{}},
		pullRequests:  db.NewPullRequestStore(mock),
		repos:         db.NewRepositoryStore(mock),
		logger:        zerolog.New(io.Discard),
		baseURL:       server.URL,
		httpClient:    server.Client(),
	}
	service.tokenProvider.cache[123] = &cachedToken{Token: "install-token", ExpiresAt: time.Now().Add(time.Hour)}

	err = service.EnrichPullRequestHealth(context.Background(), orgID, pullRequestID, 5)
	require.NoError(t, err, "EnrichPullRequestHealth should persist conflict and failing-test payloads")
	require.NoError(t, mock.ExpectationsWereMet(), "all enrichment expectations should be met")
}

func TestPRServiceStartPullRequestRepairReusesExistingRun(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgx mock pool")
	defer mock.Close()

	orgID := uuid.New()
	pullRequestID := uuid.New()
	sessionID := uuid.New()
	repairRunID := uuid.New()
	now := time.Now().UTC()
	summary := models.PullRequestHealthSummary{
		MergeState:       models.PullRequestMergeStateConflicted,
		HasConflicts:     true,
		FailingTestCount: 0,
		NeedsAgentAction: true,
	}
	summaryJSON, err := json.Marshal(summary)
	require.NoError(t, err, "should marshal health summary")

	mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(prTestPullRequestColumns).AddRow(
			pullRequestID, nil, orgID, 42, "https://github.com/assembledhq/143/pull/42", "assembledhq/143",
			"Fix bug", (*string)(nil), "open", "pending", "app", "", nil, nil, nil,
			models.PullRequestMergeStateUnknown, false, 0, false, &now, int64(5), models.PullRequestMergeWhenReadyStateOff, (*uuid.UUID)(nil), (*time.Time)(nil), "", (*int64)(nil), "", (*time.Time)(nil), (*time.Time)(nil), now, now,
		))
	mock.ExpectQuery("SELECT .+ FROM pull_request_health_current WHERE org_id = .+ AND pull_request_id = .+").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": pullRequestID}).
		WillReturnRows(pgxmock.NewRows(prHealthCurrentTestColumns).AddRow(
			pullRequestID, orgID, int64(5), "head", "base", summaryJSON, summaryJSON, models.PullRequestHealthEnrichmentStatusReady, nil, now, now,
		))
	mock.ExpectQuery("SELECT .+ FROM pull_request_health_snapshots WHERE org_id = .+ AND pull_request_id = .+ AND version = .+").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": pullRequestID, "version": int64(5)}).
		WillReturnRows(pgxmock.NewRows(prHealthSnapshotTestColumns).AddRow(
			pullRequestID, orgID, int64(5), "head", "base", summaryJSON, nil, nil, 0, models.PullRequestHealthEnrichmentStatusReady, nil, now,
		))
	mock.ExpectQuery("SELECT .+ FROM pull_request_repair_runs").
		WithArgs(pgx.NamedArgs{
			"org_id":          orgID,
			"pull_request_id": pullRequestID,
			"action_type":     models.PullRequestRepairActionTypeResolveConflicts,
			"health_version":  int64(5),
		}).
		WillReturnRows(pgxmock.NewRows(prRepairRunTestColumns).AddRow(
			repairRunID, orgID, pullRequestID, sessionID, (*uuid.UUID)(nil), models.PullRequestRepairActionTypeResolveConflicts, int64(5), models.PullRequestRepairWorkspaceModeSnapshotContinuation, true, nil, now, now,
		))
	mock.ExpectQuery("SELECT .+ FROM sessions WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(prHealthSessionColumns).AddRow(
			newPRHealthSessionRow(sessionID, orgID, now, "running")...,
		))

	service := &PRService{
		pullRequests: db.NewPullRequestStore(mock),
		sessions:     db.NewSessionStore(mock),
		logger:       zerolog.New(io.Discard),
	}

	resp, err := service.StartPullRequestRepair(context.Background(), orgID, pullRequestID, uuid.New(), StartPullRequestRepairOptions{Action: models.PullRequestRepairActionTypeResolveConflicts})
	require.NoError(t, err, "StartPullRequestRepair should reuse an active in-flight repair run")
	require.Equal(t, sessionID, resp.SessionID, "StartPullRequestRepair should return the active repair session")
	require.True(t, resp.ReusedInFlight, "StartPullRequestRepair should mark reused in-flight runs")
	require.Equal(t, "existing", resp.Mode, "StartPullRequestRepair should report that it reused an existing session")
	require.NoError(t, mock.ExpectationsWereMet(), "all repair-run reuse expectations should be met")
}

func TestPRHealthServiceHelpers(t *testing.T) {
	t.Parallel()

	resp := &models.PullRequestHealthResponse{
		MergeState:       models.PullRequestMergeStateConflicted,
		HasConflicts:     true,
		FailingTestCount: 2,
	}
	derivePullRequestRepairActions(resp)
	require.True(t, resp.CanResolveConflicts, "derivePullRequestRepairActions should enable conflict repair for conflicted PRs")
	require.True(t, resp.CanFixTests, "derivePullRequestRepairActions should enable test repair for failing checks")

	resp = &models.PullRequestHealthResponse{
		MergeState:       models.PullRequestMergeStateClean,
		FailingTestCount: 0,
		Checks: []models.PullRequestCheckSummary{
			{Name: "backend", Category: models.PullRequestCheckCategoryUnknown, Status: models.PullRequestCheckStatusFailed},
		},
	}
	derivePullRequestRepairActions(resp)
	require.True(t, resp.CanFixTests, "derivePullRequestRepairActions should enable repair for failed checks without a legacy failing-test count")

	first := "first"
	second := "second"
	require.Equal(t, "value", firstNonEmpty("", "value", "other"), "firstNonEmpty should return the first non-empty string")
	require.Nil(t, firstNonNilString(nil, nil), "firstNonNilString should return nil when all values are nil")
	require.Equal(t, &first, firstNonNilString(nil, &first, &second), "firstNonNilString should return the first non-empty pointer")
	require.Equal(t, "text", truncateText("text", 0), "truncateText should leave values unchanged for non-positive limits")
	require.Equal(t, "hello world", stripWhitespace("  hello   world \n"), "stripWhitespace should collapse repeated whitespace")
	require.Equal(t, "Please resolve the conflicts.", repairPromptForAction(models.PullRequestRepairActionTypeResolveConflicts), "repairPromptForAction should specialize conflict repair prompts")
	require.Equal(t, "Please repair this pull request.", repairPromptForAction("other"), "repairPromptForAction should provide a default prompt")
	require.Equal(t, models.PullRequestMergeStateBlocked, normalizeRepairMergeState(models.PullRequestMergeStateUnknown, boolPtr(false), "blocked"), "normalizeRepairMergeState should preserve non-conflict blocked states")
	require.Equal(t, models.PullRequestMergeStateBehind, normalizeRepairMergeState(models.PullRequestMergeStateBehind, nil, ""), "normalizeRepairMergeState should fall back to the existing state when GitHub mergeability is unknown")
	require.True(t, isSessionTerminalStatus(models.SessionStatusCompleted), "isSessionTerminalStatus should recognize completed sessions")
	require.False(t, isSessionTerminalStatus(models.SessionStatusRunning), "isSessionTerminalStatus should reject active sessions")
	require.True(t, isUniqueActiveRepairRunViolation(&pgconn.PgError{Code: pgerrcode.UniqueViolation, ConstraintName: "idx_pull_request_repair_runs_active"}), "isUniqueActiveRepairRunViolation should recognize the active repair-run uniqueness constraint")
	require.False(t, isUniqueActiveRepairRunViolation(errors.New("boom")), "isUniqueActiveRepairRunViolation should reject unrelated errors")
	require.Equal(t, "Please fix these tests.", repairPromptForAction(models.PullRequestRepairActionTypeFixTests), "repairPromptForAction should specialize test repair prompts")
	require.Equal(t, "", firstNonEmpty("", "  "), "firstNonEmpty should return an empty string when every value is blank")
	require.Nil(t, firstNonNilString(strPtr("   ")), "firstNonNilString should skip blank pointed strings")
	require.Equal(t, "12345…", truncateText("123456", 5), "truncateText should append an ellipsis when trimming long strings")
}

func TestPRServiceResumeRepairSession(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		run  func(t *testing.T, mock pgxmock.PgxPoolIface, service *PRService, pr models.PullRequest, parentSession models.Session, userID uuid.UUID, now time.Time)
	}{
		{
			name: "resume repair session",
			run: func(t *testing.T, mock pgxmock.PgxPoolIface, service *PRService, pr models.PullRequest, parentSession models.Session, userID uuid.UUID, now time.Time) {
				threadID := uuid.New()
				mock.ExpectBegin()
				mock.ExpectQuery("UPDATE sessions").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnError(pgx.ErrNoRows)
				// ClaimForResume now binds the resumable-status set as a
				// runtime parameter, so the query carries an extra @statuses
				// arg compared to the legacy hardcoded IN (...) shape.
				mock.ExpectQuery("UPDATE sessions").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(prHealthSessionColumns).AddRow(newPRHealthSessionRow(parentSession.ID, pr.OrgID, now, models.SessionStatusRunning)...))
				mock.ExpectExec("UPDATE sessions.+SET revision_context").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				mock.ExpectQuery("SELECT .+ FROM session_threads WHERE org_id .+ AND session_id").
					WithArgs(pr.OrgID, parentSession.ID).
					WillReturnRows(
						pgxmock.NewRows(prHealthSessionThreadColumns).
							AddRow(newPRHealthSessionThreadRow(threadID, parentSession.ID, pr.OrgID, now)...),
					)
				mock.ExpectQuery("INSERT INTO session_messages").
					WithArgs(
						parentSession.ID,
						pr.OrgID,
						uuidPtrArg{want: threadID},
						pgxmock.AnyArg(),
						1,
						models.MessageRoleUser,
						"Please resolve the conflicts.",
						pgxmock.AnyArg(),
						pgxmock.AnyArg(),
						pgxmock.AnyArg(),
						pgxmock.AnyArg(),
					).
					WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(1), now))
				mock.ExpectQuery("INSERT INTO pull_request_repair_runs").
					WithArgs(pgx.NamedArgs{
						"org_id":               pr.OrgID,
						"pull_request_id":      pr.ID,
						"session_id":           parentSession.ID,
						"thread_id":            &threadID,
						"action_type":          models.PullRequestRepairActionTypeResolveConflicts,
						"health_version":       int64(9),
						"workspace_mode":       models.PullRequestRepairWorkspaceModePRHeadReconstruction,
						"active":               true,
						"obsoleted_by_version": (*int64)(nil),
					}).
					WillReturnRows(pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(uuid.New(), now, now))
				mock.ExpectQuery("INSERT INTO jobs").
					WithArgs(pgx.NamedArgs{
						"org_id":   pr.OrgID,
						"queue":    "agent",
						"job_type": "continue_session",
						"payload": repairJobPayloadArg{
							wantThreadID:      threadID,
							wantPullRequestID: pr.ID,
							wantAction:        models.PullRequestRepairActionTypeResolveConflicts,
							wantHealthVersion: 9,
						},
						"priority":   5,
						"dedupe_key": pgxmock.AnyArg(),
					}).
					WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
				mock.ExpectCommit()

				resp, err := service.resumeRepairSession(context.Background(), pr, parentSession, []byte(`{"repair":true}`), "Please resolve the conflicts.", userID, models.PullRequestRepairActionTypeResolveConflicts, 9, "head", "base", models.PullRequestRepairWorkspaceModePRHeadReconstruction, &threadID)
				require.NoError(t, err, "resumeRepairSession should continue an existing session")
				require.Equal(t, "reconstructed", resp.Mode, "resumeRepairSession should report reconstructed mode when no snapshot continuation is used")
				require.False(t, resp.ReusedInFlight, "resumeRepairSession should create a fresh active repair run for the resumed session")
				require.Equal(t, &threadID, resp.ThreadID, "resumeRepairSession should return the selected repair thread")
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create pgx mock pool")
			defer mock.Close()

			now := time.Now().UTC()
			parentSessionID := uuid.New()
			userID := uuid.New()
			pr := models.PullRequest{
				ID:        uuid.New(),
				OrgID:     uuid.New(),
				SessionID: &parentSessionID,
			}
			parentSession := models.Session{
				ID:    parentSessionID,
				OrgID: pr.OrgID,
				PrimaryIssueID: func() *uuid.UUID {
					id := uuid.New()
					return &id
				}(),
				AgentType:     "claude_code",
				Status:        models.SessionStatusCompleted,
				AutonomyLevel: "semi",
				TokenMode:     "low",
				Title:         strPtr("Repair PR"),
			}

			service := &PRService{
				pullRequests:    db.NewPullRequestStore(mock),
				sessions:        db.NewSessionStore(mock),
				sessionMessages: db.NewSessionMessageStore(mock),
				jobs:            db.NewJobStore(mock),
				logger:          zerolog.New(io.Discard),
			}

			tt.run(t, mock, service, pr, parentSession, userID, now)
			require.NoError(t, mock.ExpectationsWereMet(), "all repair session expectations should be met")
		})
	}
}

func TestPRServiceFetchGitHubHelpers(t *testing.T) {
	t.Parallel()

	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/assembledhq/143/pulls/42":
			attempts++
			if attempts == 1 {
				_, _ = w.Write([]byte(`{"number":42,"html_url":"https://github.com/assembledhq/143/pull/42","state":"open","mergeable":null,"mergeable_state":"unknown","head":{"ref":"feature","sha":"head"},"base":{"ref":"main","sha":"base"}}`))
				return
			}
			_, _ = w.Write([]byte(`{"number":42,"html_url":"https://github.com/assembledhq/143/pull/42","state":"open","mergeable":true,"mergeable_state":"clean","head":{"ref":"feature","sha":"head"},"base":{"ref":"main","sha":"base"}}`))
		case "/repos/assembledhq/143/check-runs/77/annotations":
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	service := &PRService{
		logger:     zerolog.New(io.Discard),
		baseURL:    server.URL,
		httpClient: server.Client(),
	}

	details, err := service.fetchPullRequestDetails(context.Background(), "token", "assembledhq", "143", 42)
	require.NoError(t, err, "fetchPullRequestDetails should retry until mergeable is known")
	require.NotNil(t, details.Mergeable, "fetchPullRequestDetails should return the final mergeability")
	require.Equal(t, 2, attempts, "fetchPullRequestDetails should retry when GitHub initially returns mergeable=null")

	annotations, err := service.fetchCheckRunAnnotations(context.Background(), "token", "assembledhq", "143", 77)
	require.NoError(t, err, "fetchCheckRunAnnotations should treat 404 annotation endpoints as empty")
	require.Nil(t, annotations, "fetchCheckRunAnnotations should return nil annotations for 404 responses")
}

func TestPRServiceFetchPullRequestDetailsUsesExponentialMergeabilityBackoff(t *testing.T) {
	t.Parallel()

	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/repos/assembledhq/143/pulls/42", r.URL.Path, "fetchPullRequestDetails should request the GitHub PR endpoint")
		attempts++
		if attempts < 4 {
			_, _ = w.Write([]byte(`{"number":42,"html_url":"https://github.com/assembledhq/143/pull/42","state":"open","mergeable":null,"mergeable_state":"unknown","head":{"ref":"feature","sha":"head"},"base":{"ref":"main","sha":"base"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"number":42,"html_url":"https://github.com/assembledhq/143/pull/42","state":"open","mergeable":true,"mergeable_state":"clean","head":{"ref":"feature","sha":"head"},"base":{"ref":"main","sha":"base"}}`))
	}))
	defer server.Close()

	var waits []time.Duration
	service := &PRService{
		logger:                  zerolog.New(io.Discard),
		baseURL:                 server.URL,
		httpClient:              server.Client(),
		mergeabilityRetryDelays: []time.Duration{250 * time.Millisecond, 500 * time.Millisecond, time.Second},
		mergeabilityRetryWait: func(ctx context.Context, d time.Duration) error {
			waits = append(waits, d)
			return ctx.Err()
		},
	}

	details, err := service.fetchPullRequestDetails(context.Background(), "token", "assembledhq", "143", 42)
	require.NoError(t, err, "fetchPullRequestDetails should retry until GitHub reports mergeability")
	require.NotNil(t, details.Mergeable, "fetchPullRequestDetails should return the resolved mergeability")
	require.Equal(t, []time.Duration{250 * time.Millisecond, 500 * time.Millisecond, time.Second}, waits, "fetchPullRequestDetails should use the configured exponential backoff delays")
	require.Equal(t, 4, attempts, "fetchPullRequestDetails should make one request per retry plus the initial request")
}

func TestPRServiceFetchPullRequestDetailsStopsBackoffForDefinitiveNullMergeabilityState(t *testing.T) {
	t.Parallel()

	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/repos/assembledhq/143/pulls/42", r.URL.Path, "fetchPullRequestDetails should request the GitHub PR endpoint")
		attempts++
		_, _ = w.Write([]byte(`{"number":42,"html_url":"https://github.com/assembledhq/143/pull/42","state":"open","mergeable":null,"mergeable_state":"dirty","head":{"ref":"feature","sha":"head"},"base":{"ref":"main","sha":"base"}}`))
	}))
	defer server.Close()

	service := &PRService{
		logger:                  zerolog.New(io.Discard),
		baseURL:                 server.URL,
		httpClient:              server.Client(),
		mergeabilityRetryDelays: []time.Duration{250 * time.Millisecond, 500 * time.Millisecond, time.Second},
		mergeabilityRetryWait: func(context.Context, time.Duration) error {
			require.Fail(t, "fetchPullRequestDetails should not wait when GitHub already reports a dirty merge state")
			return nil
		},
	}

	details, err := service.fetchPullRequestDetails(context.Background(), "token", "assembledhq", "143", 42)
	require.NoError(t, err, "fetchPullRequestDetails should accept dirty as a definitive mergeability state")
	require.Nil(t, details.Mergeable, "fetchPullRequestDetails should preserve GitHub's mergeable=null payload")
	require.Equal(t, 1, attempts, "fetchPullRequestDetails should not retry definitive null mergeability states")
}

func TestPRServiceBranchRequiresStatusChecks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		body     string
		expected bool
	}{
		{
			name:     "protected branch with required contexts",
			body:     `{"protected":true,"protection":{"required_status_checks":{"contexts":["ci-build"]}}}`,
			expected: true,
		},
		{
			name:     "protected branch without required contexts",
			body:     `{"protected":true,"protection":{"required_status_checks":{"contexts":[]}}}`,
			expected: false,
		},
		{
			name:     "unprotected branch",
			body:     `{"protected":false}`,
			expected: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "/repos/assembledhq/143/branches/main", r.URL.Path, "branchRequiresStatusChecks should query the base branch endpoint")
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			service := &PRService{
				logger:     zerolog.New(io.Discard),
				baseURL:    server.URL,
				httpClient: server.Client(),
			}

			required, err := service.branchRequiresStatusChecks(context.Background(), "token", "assembledhq", "143", "main")
			require.NoError(t, err, "branchRequiresStatusChecks should decode GitHub branch protection metadata")
			require.Equal(t, tt.expected, required, "branchRequiresStatusChecks should classify required status checks correctly")
		})
	}
}

func TestPRServiceBranchRequiresStatusChecksCached(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		cachedValue   string
		branchBody    string
		expected      bool
		expectGitHub  bool
		expectCached  bool
		expectedTTLAt time.Duration
	}{
		{
			name:         "uses cached required checks result",
			cachedValue:  `{"required":true,"checked_at":"2026-06-06T00:00:00Z"}`,
			expected:     true,
			expectCached: true,
		},
		{
			name:          "caches branch protection miss with shorter permissive ttl",
			branchBody:    `{"protected":false}`,
			expected:      false,
			expectGitHub:  true,
			expectCached:  true,
			expectedTTLAt: 6 * time.Hour,
		},
		{
			name:          "caches branch protection required checks with longer ttl",
			branchBody:    `{"protected":true,"protection":{"required_status_checks":{"contexts":["ci-build"]}}}`,
			expected:      true,
			expectGitHub:  true,
			expectCached:  true,
			expectedTTLAt: 24 * time.Hour,
		},
		{
			name:          "falls back to GitHub on corrupted cache entry",
			cachedValue:   `not-valid-json`,
			branchBody:    `{"protected":true,"protection":{"required_status_checks":{"contexts":["ci-build"]}}}`,
			expected:      true,
			expectGitHub:  true,
			expectCached:  true,
			expectedTTLAt: 24 * time.Hour,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			redisClient, mr := newPRHealthRedisClient(t)
			key := requiredStatusChecksCacheKey("org-1", "assembledhq/143", "main")
			if tt.cachedValue != "" {
				mr.Set(key, tt.cachedValue)
			}

			githubCalls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				githubCalls++
				require.Equal(t, "/repos/assembledhq/143/branches/main", r.URL.Path, "cache miss should query GitHub branch protection")
				_, _ = w.Write([]byte(tt.branchBody))
			}))
			defer server.Close()

			service := &PRService{
				redisClient: redisClient,
				logger:      zerolog.New(io.Discard),
				baseURL:     server.URL,
				httpClient:  server.Client(),
			}

			required, err := service.branchRequiresStatusChecksCached(context.Background(), "org-1", "token", "assembledhq", "143", "main")
			require.NoError(t, err, "cached branch protection lookup should succeed")
			require.Equal(t, tt.expected, required, "cached branch protection lookup should return expected required-checks result")
			if tt.expectGitHub {
				require.Equal(t, 1, githubCalls, "cache miss should query GitHub exactly once")
			} else {
				require.Equal(t, 0, githubCalls, "cache hit should not query GitHub")
			}
			if tt.expectCached {
				raw, err := mr.Get(key)
				require.NoError(t, err, "branch protection result should be cached in Redis")
				require.Contains(t, raw, `"required":`, "cached payload should include the required flag")
			}
			if tt.expectedTTLAt > 0 {
				ttl := mr.TTL(key)
				require.True(t, ttl > tt.expectedTTLAt-time.Minute && ttl <= tt.expectedTTLAt, "cached result should use the expected TTL")
			}
		})
	}
}

func TestPRServiceBranchRequiresStatusChecksCachedFallsBackWhenRedisUnavailable(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/repos/assembledhq/143/branches/main", r.URL.Path, "Redis outage should fall back to GitHub branch protection")
		_, _ = w.Write([]byte(`{"protected":true,"protection":{"required_status_checks":{"contexts":["ci-build"]}}}`))
	}))
	defer server.Close()

	service := &PRService{
		redisClient: &cache.Client{},
		logger:      zerolog.New(io.Discard),
		baseURL:     server.URL,
		httpClient:  server.Client(),
	}

	required, err := service.branchRequiresStatusChecksCached(context.Background(), "org-1", "token", "assembledhq", "143", "main")
	require.NoError(t, err, "Redis outage should not prevent a live GitHub lookup")
	require.True(t, required, "live GitHub lookup should still report required checks")
}

func newPRHealthRedisClient(t *testing.T) (*cache.Client, *miniredis.Miniredis) {
	t.Helper()

	mr := miniredis.RunT(t)
	client := cache.New(cache.Config{Topology: "standalone", URL: "redis://" + mr.Addr()}, zerolog.Nop(), nil)
	require.NotNil(t, client, "Redis client should initialize for PR health cache tests")
	t.Cleanup(func() {
		require.NoError(t, client.Close(), "Redis client should close cleanly")
	})
	return client, mr
}

func TestPRServiceBuildRepairRevisionContextIncludesFailingChecks(t *testing.T) {
	t.Parallel()

	service := &PRService{}
	revisionContextJSON, err := service.buildRepairRevisionContext(
		models.PullRequest{GitHubPRNumber: 42, GitHubRepo: "assembledhq/143"},
		models.PullRequestHealthCurrent{HeadSHA: "head", BaseSHA: "base"},
		models.PullRequestHealthSummary{MergeState: models.PullRequestMergeStateClean},
		models.PullRequestHealthSnapshot{
			FailingTestsPayload: []byte(`{"checks":[{"name":"unit tests","category":"test","summary":"2 tests failed","details_url":"https://example.com/check","log_excerpt":"panic: boom","annotations":["foo_test.go:12 expected true"]}]}`),
		},
		models.PullRequestRepairActionTypeFixTests,
	)
	require.NoError(t, err, "buildRepairRevisionContext should decode failing test payloads")

	var revisionContext agent.RevisionContext
	err = json.Unmarshal(revisionContextJSON, &revisionContext)
	require.NoError(t, err, "buildRepairRevisionContext should serialize a valid revision context")
	require.Len(t, revisionContext.RepairContext.FailingChecks, 1, "buildRepairRevisionContext should include failing checks from the snapshot payload")
	require.Equal(t, "unit tests", revisionContext.RepairContext.FailingChecks[0].Name, "buildRepairRevisionContext should preserve the check name")
	require.Equal(t, "panic: boom", revisionContext.RepairContext.FailingChecks[0].LogExcerpt, "buildRepairRevisionContext should preserve the log excerpt")
}

func TestPRServiceCanResumeRepairSession(t *testing.T) {
	t.Parallel()

	snapshotKey := "snapshot.tar"
	tests := []struct {
		name    string
		session models.Session
		want    bool
	}{
		{
			name:    "rejects sessions without snapshots",
			session: models.Session{Status: models.SessionStatusCompleted},
			want:    false,
		},
		{
			name: "rejects pending snapshot uploads",
			session: func() models.Session {
				pendingSnapshotKey := "snapshots/post-pr.tar.zst"
				return models.Session{Status: models.SessionStatusCompleted, SnapshotKey: &snapshotKey, PendingSnapshotKey: &pendingSnapshotKey, SandboxState: models.SandboxStateSnapshotted}
			}(),
			want: false,
		},
		{
			name:    "rejects destroyed sandboxes",
			session: models.Session{Status: models.SessionStatusCompleted, SnapshotKey: &snapshotKey, SandboxState: models.SandboxStateDestroyed},
			want:    false,
		},
		{
			name:    "accepts resumable completed session",
			session: models.Session{Status: models.SessionStatusCompleted, SnapshotKey: &snapshotKey, SandboxState: models.SandboxStateSnapshotted},
			want:    true,
		},
		{
			name:    "rejects running session",
			session: models.Session{Status: models.SessionStatusRunning, SnapshotKey: &snapshotKey, SandboxState: models.SandboxStateSnapshotted},
			want:    false,
		},
	}

	service := &PRService{}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, service.canResumeRepairSession(tt.session), "canResumeRepairSession should classify resumability correctly")
		})
	}
}

func TestPRServiceGetPullRequestHealthInlineSyncAndStartRepairErrors(t *testing.T) {
	t.Parallel()

	t.Run("inline sync for unsynced pull requests", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create pgx mock pool")
		defer mock.Close()

		orgID := uuid.New()
		pullRequestID := uuid.New()
		repoID := uuid.New()
		integrationID := uuid.New()
		now := time.Now().UTC()
		summary := models.PullRequestHealthSummary{
			MergeState:       models.PullRequestMergeStateConflicted,
			HasConflicts:     true,
			FailingTestCount: 1,
			NeedsAgentAction: true,
		}
		summaryJSON, err := json.Marshal(summary)
		require.NoError(t, err, "should marshal health summary")

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/repos/assembledhq/143/pulls/42":
				_, _ = w.Write([]byte(`{"number":42,"html_url":"https://github.com/assembledhq/143/pull/42","state":"open","mergeable":false,"mergeable_state":"dirty","head":{"ref":"feature","sha":"head-inline"},"base":{"ref":"main","sha":"base-inline"}}`))
			case "/repos/assembledhq/143/commits/head-inline/check-runs":
				_, _ = w.Write([]byte(`{"check_runs":[{"id":1,"name":"unit tests","html_url":"https://example.com/tests","conclusion":"failure","status":"completed","details_url":"https://example.com/tests/details","app":{"slug":"github-actions"},"output":{"title":"tests failed","summary":"bad","text":"trace","annotations_count":0,"annotations_url":""}}]}`))
			default:
				http.NotFound(w, r)
			}
		}))
		defer server.Close()

		mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE id").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows(prTestPullRequestColumns).AddRow(
				pullRequestID, nil, orgID, 42, "https://github.com/assembledhq/143/pull/42", "assembledhq/143",
				"Fix bug", (*string)(nil), "open", "pending", "app", "", nil, nil, nil,
				models.PullRequestMergeStateUnknown, false, 0, false, (*time.Time)(nil), int64(0), models.PullRequestMergeWhenReadyStateOff, (*uuid.UUID)(nil), (*time.Time)(nil), "", (*int64)(nil), "", (*time.Time)(nil), (*time.Time)(nil), now, now,
			))
		mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE id").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows(prTestPullRequestColumns).AddRow(
				pullRequestID, nil, orgID, 42, "https://github.com/assembledhq/143/pull/42", "assembledhq/143",
				"Fix bug", (*string)(nil), "open", "pending", "app", "", nil, nil, nil,
				models.PullRequestMergeStateUnknown, false, 0, false, (*time.Time)(nil), int64(0), models.PullRequestMergeWhenReadyStateOff, (*uuid.UUID)(nil), (*time.Time)(nil), "", (*int64)(nil), "", (*time.Time)(nil), (*time.Time)(nil), now, now,
			))
		mock.ExpectQuery("SELECT .+ FROM repositories WHERE org_id = .+ AND full_name = .+ AND status = 'active'").
			WithArgs(pgx.NamedArgs{"org_id": orgID, "full_name": "assembledhq/143"}).
			WillReturnRows(pgxmock.NewRows(prTestRepoColumns).AddRow(
				repoID, orgID, integrationID, int64(1), "assembledhq/143", "main", false, nil, nil, "https://github.com/assembledhq/143.git", int64(123), "active", nil, nil, []byte(`{}`), now, now,
			))
		mock.ExpectQuery("SELECT .+ FROM pull_request_health_current").
			WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": pullRequestID}).
			WillReturnRows(pgxmock.NewRows(prHealthCurrentTestColumns))
		mock.ExpectBegin()
		mock.ExpectQuery("SELECT .+ FROM pull_request_health_current").
			WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": pullRequestID}).
			WillReturnRows(pgxmock.NewRows(prHealthCurrentTestColumns))
		mock.ExpectExec("INSERT INTO pull_request_health_snapshots").
			WithArgs(pgx.NamedArgs{
				"pull_request_id":   pullRequestID,
				"org_id":            orgID,
				"version":           int64(1),
				"head_sha":          "head-inline",
				"base_sha":          "base-inline",
				"summary_json":      pgxmock.AnyArg(),
				"enrichment_status": models.PullRequestHealthEnrichmentStatusNotRequested,
			}).
			WillReturnResult(pgxmock.NewResult("INSERT", 1))
		mock.ExpectExec("UPDATE pull_request_repair_runs").
			WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": pullRequestID, "version": int64(1)}).
			WillReturnResult(pgxmock.NewResult("UPDATE", 0))
		mock.ExpectExec("INSERT INTO pull_request_health_current").
			WithArgs(pgx.NamedArgs{
				"pull_request_id":      pullRequestID,
				"org_id":               orgID,
				"version":              int64(1),
				"head_sha":             "head-inline",
				"base_sha":             "base-inline",
				"summary_json":         pgxmock.AnyArg(),
				"summary_preview_json": pgxmock.AnyArg(),
				"enrichment_status":    models.PullRequestHealthEnrichmentStatusNotRequested,
				"enriched_at":          (*time.Time)(nil),
				"created_at":           pgxmock.AnyArg(),
				"updated_at":           pgxmock.AnyArg(),
			}).
			WillReturnResult(pgxmock.NewResult("INSERT", 1))
		mock.ExpectExec("UPDATE pull_requests").
			WithArgs(pgx.NamedArgs{
				"pull_request_id":    pullRequestID,
				"org_id":             orgID,
				"head_sha":           "head-inline",
				"base_sha":           "base-inline",
				"merge_state":        models.PullRequestMergeStateConflicted,
				"has_conflicts":      true,
				"failing_test_count": 1,
				"needs_agent_action": true,
				"version":            int64(1),
			}).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))
		mock.ExpectCommit()
		mock.ExpectExec("UPDATE pull_requests SET ci_status").
			WithArgs(pgx.NamedArgs{"id": pullRequestID, "org_id": orgID, "ci_status": models.PullRequestCIStatusFailure}).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))
		mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE id").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows(prTestPullRequestColumns).AddRow(
				pullRequestID, nil, orgID, 42, "https://github.com/assembledhq/143/pull/42", "assembledhq/143",
				"Fix bug", (*string)(nil), "open", "pending", "app", "", strPtr("head-inline"), nil, strPtr("base-inline"),
				models.PullRequestMergeStateConflicted, true, 1, true, &now, int64(1), models.PullRequestMergeWhenReadyStateOff, (*uuid.UUID)(nil), (*time.Time)(nil), "", (*int64)(nil), "", (*time.Time)(nil), (*time.Time)(nil), now, now,
			))
		mock.ExpectQuery("SELECT .+ FROM pull_request_health_current WHERE org_id = .+ AND pull_request_id = .+").
			WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": pullRequestID}).
			WillReturnRows(pgxmock.NewRows(prHealthCurrentTestColumns).AddRow(
				pullRequestID, orgID, int64(1), "head-inline", "base-inline", summaryJSON, summaryJSON, models.PullRequestHealthEnrichmentStatusNotRequested, nil, now, now,
			))

		service := &PRService{
			tokenProvider: &Service{cache: map[int64]*cachedToken{}},
			pullRequests:  db.NewPullRequestStore(mock),
			repos:         db.NewRepositoryStore(mock),
			logger:        zerolog.New(io.Discard),
			baseURL:       server.URL,
			httpClient:    server.Client(),
		}
		service.tokenProvider.cache[123] = &cachedToken{Token: "install-token", ExpiresAt: time.Now().Add(time.Hour)}

		resp, err := service.GetPullRequestHealth(context.Background(), orgID, pullRequestID)
		require.NoError(t, err, "GetPullRequestHealth should inline-sync unsynced pull requests")
		require.Equal(t, int64(1), resp.HealthVersion, "GetPullRequestHealth should reflect the synced health version")
		require.NoError(t, mock.ExpectationsWereMet(), "all inline sync expectations should be met")
	})

	t.Run("get pull request health returns store and response errors", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create pgx mock pool")
		defer mock.Close()

		service := &PRService{
			pullRequests: db.NewPullRequestStore(mock),
			logger:       zerolog.New(io.Discard),
		}

		orgID := uuid.New()
		pullRequestID := uuid.New()
		mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE id").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnError(errors.New("load failed"))
		_, err = service.GetPullRequestHealth(context.Background(), orgID, pullRequestID)
		require.Error(t, err, "GetPullRequestHealth should return pull request load errors")

		now := time.Now().UTC()
		mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE id").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows(prTestPullRequestColumns).AddRow(
				pullRequestID, nil, orgID, 42, "https://github.com/assembledhq/143/pull/42", "assembledhq/143",
				"Fix bug", (*string)(nil), "closed", "pending", "app", "", nil, nil, nil,
				models.PullRequestMergeStateUnknown, false, 0, false, &now, int64(1), models.PullRequestMergeWhenReadyStateOff, (*uuid.UUID)(nil), (*time.Time)(nil), "", (*int64)(nil), "", (*time.Time)(nil), (*time.Time)(nil), now, now,
			))
		mock.ExpectQuery("SELECT .+ FROM pull_request_health_current WHERE org_id = .+ AND pull_request_id = .+").
			WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": pullRequestID}).
			WillReturnError(errors.New("current failed"))
		_, err = service.GetPullRequestHealth(context.Background(), orgID, pullRequestID)
		require.Error(t, err, "GetPullRequestHealth should return current-health lookup failures")
		require.Contains(t, err.Error(), "current failed", "GetPullRequestHealth should preserve current-health lookup errors")
	})

	t.Run("start repair returns validation and decode errors", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create pgx mock pool")
		defer mock.Close()

		service := &PRService{
			pullRequests: db.NewPullRequestStore(mock),
			sessions:     db.NewSessionStore(mock),
			logger:       zerolog.New(io.Discard),
		}

		_, err = service.StartPullRequestRepair(context.Background(), uuid.New(), uuid.New(), uuid.New(), StartPullRequestRepairOptions{Action: models.PullRequestRepairActionType("bad")})
		require.Error(t, err, "StartPullRequestRepair should reject invalid repair actions")

		orgID := uuid.New()
		pullRequestID := uuid.New()
		now := time.Now().UTC()
		mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE id").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows(prTestPullRequestColumns).AddRow(
				pullRequestID, nil, orgID, 42, "https://github.com/assembledhq/143/pull/42", "assembledhq/143",
				"Fix bug", (*string)(nil), "open", "pending", "app", "", nil, nil, nil,
				models.PullRequestMergeStateUnknown, false, 0, false, &now, int64(5), models.PullRequestMergeWhenReadyStateOff, (*uuid.UUID)(nil), (*time.Time)(nil), "", (*int64)(nil), "", (*time.Time)(nil), (*time.Time)(nil), now, now,
			))
		mock.ExpectQuery("SELECT .+ FROM pull_request_health_current WHERE org_id = .+ AND pull_request_id = .+").
			WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": pullRequestID}).
			WillReturnRows(pgxmock.NewRows(prHealthCurrentTestColumns).AddRow(
				pullRequestID, orgID, int64(5), "head", "base", []byte(`{"merge_state":`), []byte(`{"merge_state":`), models.PullRequestHealthEnrichmentStatusReady, nil, now, now,
			))
		mock.ExpectQuery("SELECT .+ FROM pull_request_health_snapshots WHERE org_id = .+ AND pull_request_id = .+ AND version = .+").
			WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": pullRequestID, "version": int64(5)}).
			WillReturnRows(pgxmock.NewRows(prHealthSnapshotTestColumns).AddRow(
				pullRequestID, orgID, int64(5), "head", "base", []byte(`{"merge_state":"clean"}`), nil, nil, 0, models.PullRequestHealthEnrichmentStatusReady, nil, now,
			))

		_, err = service.StartPullRequestRepair(context.Background(), orgID, pullRequestID, uuid.New(), StartPullRequestRepairOptions{Action: models.PullRequestRepairActionTypeResolveConflicts})
		require.Error(t, err, "StartPullRequestRepair should fail when the health summary cannot be decoded")
		require.Contains(t, err.Error(), "decode pull request health summary for repair", "StartPullRequestRepair should wrap health summary decode errors")
	})
}

func TestPRServiceReconcileAndRepairBranchCoverage(t *testing.T) {
	t.Parallel()

	t.Run("reconcile returns list errors and tolerates per-pr sync failures", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create pgx mock pool")
		defer mock.Close()

		service := &PRService{
			pullRequests: db.NewPullRequestStore(mock),
			repos:        db.NewRepositoryStore(mock),
			logger:       zerolog.New(io.Discard),
		}

		orgID := uuid.New()
		mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE org_id").
			WithArgs(pgx.NamedArgs{"org_id": orgID, "before": pgxmock.AnyArg(), "limit": 50}).
			WillReturnError(errors.New("list failed"))
		err = service.ReconcilePullRequestState(context.Background(), orgID, 50)
		require.Error(t, err, "ReconcilePullRequestState should return list failures")

		now := time.Now().UTC()
		prID := uuid.New()
		mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE org_id").
			WithArgs(pgx.NamedArgs{"org_id": orgID, "before": pgxmock.AnyArg(), "limit": 10}).
			WillReturnRows(pgxmock.NewRows(prTestPullRequestColumns).AddRow(
				prID, nil, orgID, 42, "https://github.com/assembledhq/143/pull/42", "assembledhq/143",
				"Fix bug", (*string)(nil), "open", "pending", "app", "", nil, nil, nil,
				models.PullRequestMergeStateUnknown, false, 0, false, &now, int64(1), models.PullRequestMergeWhenReadyStateOff, (*uuid.UUID)(nil), (*time.Time)(nil), "", (*int64)(nil), "", (*time.Time)(nil), (*time.Time)(nil), now, now,
			))
		mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE id").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows(prTestPullRequestColumns).AddRow(
				prID, nil, orgID, 42, "https://github.com/assembledhq/143/pull/42", "assembledhq/143",
				"Fix bug", (*string)(nil), "open", "pending", "app", "", nil, nil, nil,
				models.PullRequestMergeStateUnknown, false, 0, false, &now, int64(1), models.PullRequestMergeWhenReadyStateOff, (*uuid.UUID)(nil), (*time.Time)(nil), "", (*int64)(nil), "", (*time.Time)(nil), (*time.Time)(nil), now, now,
			))
		mock.ExpectQuery("SELECT .+ FROM repositories WHERE org_id = .+ AND full_name = .+ AND status = 'active'").
			WithArgs(pgx.NamedArgs{"org_id": orgID, "full_name": "assembledhq/143"}).
			WillReturnError(errors.New("repo failed"))
		mock.ExpectQuery("SELECT .+ FROM pull_requests[\\s\\S]*merge_when_ready_state = 'queued'[\\s\\S]*merge_when_ready_state = 'merging'").
			WithArgs(pgx.NamedArgs{"org_id": orgID, "stale_before": pgxmock.AnyArg(), "limit": 10}).
			WillReturnRows(pgxmock.NewRows(prTestPullRequestColumns))

		err = service.ReconcilePullRequestState(context.Background(), orgID, 10)
		require.NoError(t, err, "ReconcilePullRequestState should continue when individual PR syncs fail")
		require.NoError(t, mock.ExpectationsWereMet(), "all reconcile expectations should be met")
	})

	t.Run("start repair validates current health state", func(t *testing.T) {
		t.Parallel()

		tests := []struct {
			name    string
			action  models.PullRequestRepairActionType
			summary models.PullRequestHealthSummary
			wantErr string
		}{
			{
				name:    "resolve conflicts requires active conflicts",
				action:  models.PullRequestRepairActionTypeResolveConflicts,
				summary: models.PullRequestHealthSummary{MergeState: models.PullRequestMergeStateClean, HasConflicts: false},
				wantErr: "does not currently require conflict resolution",
			},
			{
				name:    "fix tests requires failing tests",
				action:  models.PullRequestRepairActionTypeFixTests,
				summary: models.PullRequestHealthSummary{MergeState: models.PullRequestMergeStateClean, HasConflicts: false, FailingTestCount: 0},
				wantErr: "does not currently have failing tests",
			},
		}

		for _, tt := range tests {
			tt := tt
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()

				mock, err := pgxmock.NewPool()
				require.NoError(t, err, "should create pgx mock pool")
				defer mock.Close()

				service := &PRService{
					pullRequests: db.NewPullRequestStore(mock),
					sessions:     db.NewSessionStore(mock),
					logger:       zerolog.New(io.Discard),
				}

				orgID := uuid.New()
				pullRequestID := uuid.New()
				now := time.Now().UTC()
				summaryJSON, err := json.Marshal(tt.summary)
				require.NoError(t, err, "should marshal health summary")

				mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(prTestPullRequestColumns).AddRow(
						pullRequestID, nil, orgID, 42, "https://github.com/assembledhq/143/pull/42", "assembledhq/143",
						"Fix bug", (*string)(nil), "open", "pending", "app", "", nil, nil, nil,
						models.PullRequestMergeStateUnknown, false, 0, false, &now, int64(5), models.PullRequestMergeWhenReadyStateOff, (*uuid.UUID)(nil), (*time.Time)(nil), "", (*int64)(nil), "", (*time.Time)(nil), (*time.Time)(nil), now, now,
					))
				mock.ExpectQuery("SELECT .+ FROM pull_request_health_current WHERE org_id = .+ AND pull_request_id = .+").
					WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": pullRequestID}).
					WillReturnRows(pgxmock.NewRows(prHealthCurrentTestColumns).AddRow(
						pullRequestID, orgID, int64(5), "head", "base", summaryJSON, summaryJSON, models.PullRequestHealthEnrichmentStatusReady, nil, now, now,
					))
				mock.ExpectQuery("SELECT .+ FROM pull_request_health_snapshots WHERE org_id = .+ AND pull_request_id = .+ AND version = .+").
					WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": pullRequestID, "version": int64(5)}).
					WillReturnRows(pgxmock.NewRows(prHealthSnapshotTestColumns).AddRow(
						pullRequestID, orgID, int64(5), "head", "base", summaryJSON, nil, nil, 0, models.PullRequestHealthEnrichmentStatusReady, nil, now,
					))

				_, err = service.StartPullRequestRepair(context.Background(), orgID, pullRequestID, uuid.New(), StartPullRequestRepairOptions{Action: tt.action})
				require.Error(t, err, "StartPullRequestRepair should reject ineligible repair actions")
				require.Contains(t, err.Error(), tt.wantErr, "StartPullRequestRepair should describe why the repair action is ineligible")
			})
		}
	})

	t.Run("start fix tests accepts failed check summaries without legacy test count", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create pgx mock pool")
		defer mock.Close()

		service := &PRService{
			pullRequests: db.NewPullRequestStore(mock),
			sessions:     db.NewSessionStore(mock),
			logger:       zerolog.New(io.Discard),
		}

		orgID := uuid.New()
		pullRequestID := uuid.New()
		now := time.Now().UTC()
		summary := models.PullRequestHealthSummary{
			MergeState:       models.PullRequestMergeStateClean,
			FailingTestCount: 0,
			Checks: []models.PullRequestCheckSummary{
				{Name: "backend", Category: models.PullRequestCheckCategoryUnknown, Status: models.PullRequestCheckStatusFailed},
			},
		}
		summaryJSON, err := json.Marshal(summary)
		require.NoError(t, err, "should marshal health summary")

		mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE id").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows(prTestPullRequestColumns).AddRow(
				pullRequestID, nil, orgID, 42, "https://github.com/assembledhq/143/pull/42", "assembledhq/143",
				"Fix bug", (*string)(nil), "open", "pending", "app", "", nil, nil, nil,
				models.PullRequestMergeStateUnknown, false, 0, false, &now, int64(5), models.PullRequestMergeWhenReadyStateOff, (*uuid.UUID)(nil), (*time.Time)(nil), "", (*int64)(nil), "", (*time.Time)(nil), (*time.Time)(nil), now, now,
			))
		mock.ExpectQuery("SELECT .+ FROM pull_request_health_current WHERE org_id = .+ AND pull_request_id = .+").
			WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": pullRequestID}).
			WillReturnRows(pgxmock.NewRows(prHealthCurrentTestColumns).AddRow(
				pullRequestID, orgID, int64(5), "head", "base", summaryJSON, summaryJSON, models.PullRequestHealthEnrichmentStatusReady, nil, now, now,
			))
		mock.ExpectQuery("SELECT .+ FROM pull_request_health_snapshots WHERE org_id = .+ AND pull_request_id = .+ AND version = .+").
			WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": pullRequestID, "version": int64(5)}).
			WillReturnRows(pgxmock.NewRows(prHealthSnapshotTestColumns).AddRow(
				pullRequestID, orgID, int64(5), "head", "base", summaryJSON, nil, nil, 0, models.PullRequestHealthEnrichmentStatusReady, nil, now,
			))
		mock.ExpectQuery("SELECT .+ FROM pull_request_repair_runs").
			WithArgs(pgx.NamedArgs{
				"org_id":          orgID,
				"pull_request_id": pullRequestID,
				"action_type":     models.PullRequestRepairActionTypeFixTests,
				"health_version":  int64(5),
			}).
			WillReturnRows(pgxmock.NewRows(prRepairRunTestColumns))

		_, err = service.StartPullRequestRepair(context.Background(), orgID, pullRequestID, uuid.New(), StartPullRequestRepairOptions{Action: models.PullRequestRepairActionTypeFixTests})
		require.Error(t, err, "StartPullRequestRepair should still require a canonical session after accepting the failed check state")
		require.Contains(t, err.Error(), "pull request is not linked to a canonical session", "StartPullRequestRepair should pass failed-check validation before requiring session context")
		require.NoError(t, mock.ExpectationsWereMet(), "all failed-check repair expectations should be met")
	})
}

func TestSelectRepairThreadID(t *testing.T) {
	t.Parallel()

	threadA := uuid.New()
	threadB := uuid.New()
	threads := []models.SessionThread{
		{ID: threadA},
		{ID: threadB},
	}

	t.Run("returns requested thread when found", func(t *testing.T) {
		t.Parallel()
		got, err := selectRepairThreadID(threads, &threadA)
		require.NoError(t, err)
		require.Equal(t, &threadA, got)
	})

	t.Run("returns error when requested thread not in session", func(t *testing.T) {
		t.Parallel()
		unknown := uuid.New()
		_, err := selectRepairThreadID(threads, &unknown)
		require.ErrorIs(t, err, ErrRepairThreadNotFound)
	})

	t.Run("falls back to first thread when no thread requested", func(t *testing.T) {
		t.Parallel()
		got, err := selectRepairThreadID(threads, nil)
		require.NoError(t, err)
		require.Equal(t, &threadA, got)
	})

	t.Run("returns nil when no thread requested and session has no threads", func(t *testing.T) {
		t.Parallel()
		got, err := selectRepairThreadID(nil, nil)
		require.NoError(t, err)
		require.Nil(t, got)
	})
}

func TestPRServiceDirectErrorBranches(t *testing.T) {
	t.Parallel()

	service := &PRService{logger: zerolog.New(io.Discard)}

	_, err := service.resumeRepairSession(context.Background(), models.PullRequest{}, models.Session{}, nil, "", uuid.New(), models.PullRequestRepairActionTypeFixTests, 1, "head", "base", models.PullRequestRepairWorkspaceModeSnapshotContinuation, nil)
	require.Error(t, err, "resumeRepairSession should require a session message store")
	require.Contains(t, err.Error(), "session message store not configured", "resumeRepairSession should explain the missing dependency")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/assembledhq/143/pulls/42":
			_, _ = w.Write([]byte(`{bad json`))
		case "/repos/assembledhq/143/commits/head/check-runs":
			_, _ = w.Write([]byte(`{bad json`))
		case "/repos/assembledhq/143/check-runs/1/annotations":
			_, _ = w.Write([]byte(`{bad json`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	service.baseURL = server.URL
	service.httpClient = server.Client()

	_, err = service.fetchPullRequestDetails(context.Background(), "token", "assembledhq", "143", 42)
	require.Error(t, err, "fetchPullRequestDetails should reject malformed GitHub JSON")
	require.Contains(t, err.Error(), "decode GitHub pull request details", "fetchPullRequestDetails should wrap decode failures")

	_, err = service.listCheckRunsForRef(context.Background(), "token", "assembledhq", "143", "head")
	require.Error(t, err, "listCheckRunsForRef should reject malformed GitHub JSON")
	require.Contains(t, err.Error(), "decode GitHub check runs", "listCheckRunsForRef should wrap decode failures")

	_, err = service.fetchCheckRunAnnotations(context.Background(), "token", "assembledhq", "143", 1)
	require.Error(t, err, "fetchCheckRunAnnotations should reject malformed GitHub JSON")
	require.Contains(t, err.Error(), "decode GitHub check run annotations", "fetchCheckRunAnnotations should wrap decode failures")
}

var prHealthCurrentTestColumns = []string{
	"pull_request_id", "org_id", "version", "head_sha", "base_sha", "summary_json",
	"summary_preview_json", "enrichment_status", "enriched_at", "created_at", "updated_at",
}

var prHealthSnapshotTestColumns = []string{
	"pull_request_id", "org_id", "version", "head_sha", "base_sha", "summary_json",
	"conflict_payload", "failing_tests_payload", "payload_size_bytes", "enrichment_status", "enriched_at", "created_at",
}

var prRepairRunTestColumns = []string{
	"id", "org_id", "pull_request_id", "session_id", "thread_id", "action_type", "health_version", "workspace_mode", "active", "obsoleted_by_version", "created_at", "updated_at",
}

var prHealthSessionThreadColumns = []string{
	"id", "session_id", "org_id", "agent_type", "model_override",
	"label", "instructions", "file_scope", "status", "agent_session_id",
	"current_turn", "last_activity_at",
	"result_summary", "diff", "failure_explanation", "failure_category",
	"started_at", "completed_at", "created_at",
	"archived_at", "base_snapshot_key", "cost_cents", "pending_message_count", "cancel_requested_at",
	"runtime_stop_reason", "runtime_graceful_stop_at", "recovery_state", "recovery_reason", "recovery_event_history",
}

var prHealthSessionColumns = []string{
	"id", "primary_issue_id", "org_id", "origin", "interaction_mode", "validation_policy", "agent_type", "status", "autonomy_level", "token_mode",
	"complexity_tier",
	"container_id", "worker_node_id", "turn_holding_container", "started_at", "completed_at", "token_usage",
	"failure_explanation", "failure_category", "failure_next_steps", "failure_retry_advised",
	"parent_session_id", "revision_context", "error", "result_summary", "diff",
	"pm_plan_id", "title", "pm_approach", "pm_reasoning",
	"project_task_id", "model_override", "reasoning_effort", "triggered_by_user_id",
	"agent_session_id", "current_turn", "last_activity_at", "sandbox_state", "workspace_generation", "snapshot_key", "pending_snapshot_key", "pending_snapshot_set_at",
	"runtime_soft_deadline_at", "runtime_hard_deadline_at", "runtime_last_progress_at", "runtime_last_progress_type", "runtime_last_progress_strength",
	"runtime_extension_count", "runtime_extension_seconds", "runtime_stop_reason", "runtime_graceful_stop_at",
	"checkpointed_at", "checkpoint_kind", "checkpoint_capability", "checkpoint_size_bytes", "checkpoint_error",
	"recovery_state", "recovery_queued_at", "recovery_started_at", "recovery_attempt_count",
	"target_branch", "working_branch", "base_commit_sha", "repository_id", "diff_stats", "diff_history", "input_manifest", "archived_at", "archived_by_user_id", "automation_run_id", "pr_creation_state", "pr_creation_error", "pr_push_state", "pr_push_error", "branch_creation_state", "branch_creation_error", "branch_url", "diff_collected_at", "latest_diff_snapshot_id", "workspace_revision", "workspace_revision_updated_at", "has_unpushed_changes",
	"linear_private", "linear_state_sync_disabled", "linear_identifier_hint", "linear_prepare_state",
	"deleted_at", "git_identity_source", "git_identity_user_id", "created_at",
}

func newPRHealthSessionRow(sessionID, orgID uuid.UUID, now time.Time, status models.SessionStatus) []any {
	issueID := uuid.New()
	return []any{
		sessionID, &issueID, orgID, models.SessionOriginIssueTrigger, models.SessionInteractionModeSingleRun, models.SessionValidationPolicyOnTurnComplete, "claude_code", status, "semi", "low",
		nil,
		nil, nil, false, &now, nil, nil,
		nil, nil, nil, false,
		nil, nil, nil, nil, nil,
		nil, nil, nil, nil,
		nil, nil, nil, nil,
		nil, 0, now, "snapshotted", int64(0), nil,
		nil, // pending_snapshot_key
		nil, // pending_snapshot_set_at
		nil, nil, nil, "", "",
		0, 0, "", nil,
		nil, "", "", int64(0), nil,
		"", nil, nil, 0,
		nil, nil, nil, nil, nil, nil, nil,
		nil, nil, nil, "idle", (*string)(nil), "idle", (*string)(nil), "idle", (*string)(nil), (*string)(nil), nil, nil,
		int64(0), now,
		false, false, false, (*string)(nil), models.LinearPrepareStateNone,
		nil, nil, nil, now,
	}
}

func newPRHealthSessionThreadRow(threadID, sessionID, orgID uuid.UUID, now time.Time) []any {
	return []any{
		threadID, sessionID, orgID, "claude_code", nil,
		"Main", nil, nil, models.ThreadStatusIdle, nil,
		0, nil,
		nil, nil, nil, nil,
		nil, nil, now,
		nil, nil, float64(0), 0, nil,
		"", nil, "", "", []byte(`[]`),
	}
}

type uuidPtrArg struct {
	want uuid.UUID
}

func (a uuidPtrArg) Match(value any) bool {
	got, ok := value.(*uuid.UUID)
	return ok && got != nil && *got == a.want
}

func setPRHealthSessionRowValue(row []any, column string, value any) {
	for i, col := range prHealthSessionColumns {
		if col == column {
			row[i] = value
			return
		}
	}
	panic("unknown PR health session column: " + column)
}

func strPtr(value string) *string {
	return &value
}

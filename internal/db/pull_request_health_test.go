package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

var prHealthCurrentColumns = []string{
	"pull_request_id", "org_id", "version", "head_sha", "base_sha", "summary_json",
	"summary_preview_json", "enrichment_status", "enriched_at", "created_at", "updated_at",
}

var prHealthSnapshotColumns = []string{
	"pull_request_id", "org_id", "version", "head_sha", "base_sha", "summary_json",
	"conflict_payload", "failing_tests_payload", "payload_size_bytes", "enrichment_status", "enriched_at", "created_at",
}

var prRepairRunColumns = []string{
	"id", "org_id", "pull_request_id", "session_id", "thread_id", "action_type", "health_version",
	"workspace_mode", "active", "obsoleted_by_version", "created_at", "updated_at", "head_sha", "base_sha",
	"auto_attempt", "trigger_reason", "triggered_by_source", "triggered_by_user_id",
}

func TestPullRequestStore_HealthQueries(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewPullRequestStore(mock)
	orgID := uuid.New()
	prID := uuid.New()
	now := time.Now().UTC()
	summaryJSON := json.RawMessage(`{"merge_state":"conflicted","has_conflicts":true,"failing_test_count":2}`)

	mock.ExpectQuery("SELECT .+ FROM pull_request_health_current").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": prID}).
		WillReturnRows(pgxmock.NewRows(prHealthCurrentColumns).AddRow(
			prID, orgID, int64(4), "head", "base", summaryJSON, summaryJSON, models.PullRequestHealthEnrichmentStatusReady, &now, now, now,
		))

	current, err := store.GetHealthCurrent(context.Background(), orgID, prID)
	require.NoError(t, err, "GetHealthCurrent should succeed")
	require.Equal(t, int64(4), current.Version, "GetHealthCurrent should decode the stored version")

	mock.ExpectQuery("SELECT .+ FROM pull_request_health_snapshots").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": prID, "version": int64(4)}).
		WillReturnRows(pgxmock.NewRows(prHealthSnapshotColumns).AddRow(
			prID, orgID, int64(4), "head", "base", summaryJSON, []byte(`{"conflict":true}`), []byte(`{"checks":[]}`), 24, models.PullRequestHealthEnrichmentStatusReady, &now, now,
		))

	snapshot, err := store.GetHealthSnapshot(context.Background(), orgID, prID, 4)
	require.NoError(t, err, "GetHealthSnapshot should succeed")
	require.Equal(t, 24, snapshot.PayloadSizeBytes, "GetHealthSnapshot should decode the payload size")

	mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE org_id").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "before": now, "limit": 50}).
		WillReturnRows(pgxmock.NewRows(prColumns).AddRow(newPRRow(prID, uuid.New(), orgID, now)...))

	prs, err := store.ListOpenStaleForHealthSync(context.Background(), orgID, now, 0)
	require.NoError(t, err, "ListOpenStaleForHealthSync should succeed")
	require.Len(t, prs, 1, "ListOpenStaleForHealthSync should return the matching pull requests")

	require.NoError(t, mock.ExpectationsWereMet(), "all health query expectations should be met")
}

func TestPullRequestStore_UpsertHealthSummary_InsertsNewVersion(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewPullRequestStore(mock)
	orgID := uuid.New()
	prID := uuid.New()
	summary := models.PullRequestHealthSummary{
		MergeState:       models.PullRequestMergeStateConflicted,
		HasConflicts:     true,
		FailingTestCount: 2,
		NeedsAgentAction: true,
	}

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .+ FROM pull_request_health_current").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": prID}).
		WillReturnRows(pgxmock.NewRows(prHealthCurrentColumns))
	mock.ExpectExec("INSERT INTO pull_request_health_snapshots").
		WithArgs(pgx.NamedArgs{
			"pull_request_id":   prID,
			"org_id":            orgID,
			"version":           int64(1),
			"head_sha":          "head-1",
			"base_sha":          "base-1",
			"summary_json":      pgxmock.AnyArg(),
			"enrichment_status": models.PullRequestHealthEnrichmentStatusNotRequested,
		}).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec("UPDATE pull_request_repair_runs").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": prID, "version": int64(1), "head_sha": "head-1"}).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectExec("INSERT INTO pull_request_health_current").
		WithArgs(pgx.NamedArgs{
			"pull_request_id":      prID,
			"org_id":               orgID,
			"version":              int64(1),
			"head_sha":             "head-1",
			"base_sha":             "base-1",
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
			"pull_request_id":    prID,
			"org_id":             orgID,
			"head_sha":           "head-1",
			"base_sha":           "base-1",
			"merge_state":        models.PullRequestMergeStateConflicted,
			"has_conflicts":      true,
			"failing_test_count": 2,
			"needs_agent_action": true,
			"version":            int64(1),
		}).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()

	current, err := store.UpsertHealthSummary(context.Background(), orgID, prID, "head-1", "base-1", summary, nil)
	require.NoError(t, err, "UpsertHealthSummary should insert a first health version")
	require.Equal(t, int64(1), current.Version, "UpsertHealthSummary should start at version 1")
	require.Equal(t, models.PullRequestHealthEnrichmentStatusNotRequested, current.EnrichmentStatus, "new health versions should start without enrichment")
	require.NoError(t, mock.ExpectationsWereMet(), "all insert expectations should be met")
}

func TestPullRequestStore_UpsertHealthSummary_OnlyObsoletesRepairsForDifferentHead(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		existingSHA string
		nextSHA     string
	}{
		{
			name:        "same head keeps in-flight repair active across health churn",
			existingSHA: "head-same",
			nextSHA:     "head-same",
		},
		{
			name:        "new head obsoletes in-flight repair for prior PR branch state",
			existingSHA: "head-old",
			nextSHA:     "head-new",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool")
			defer mock.Close()

			store := NewPullRequestStore(mock)
			orgID := uuid.New()
			prID := uuid.New()
			now := time.Now().UTC()
			existingSummary := models.PullRequestHealthSummary{
				MergeState:       models.PullRequestMergeStateBlocked,
				FailingTestCount: 1,
				NeedsAgentAction: true,
			}
			existingJSON, err := json.Marshal(existingSummary)
			require.NoError(t, err, "should marshal existing summary")
			nextSummary := models.PullRequestHealthSummary{
				MergeState:       models.PullRequestMergeStateBlocked,
				FailingTestCount: 2,
				NeedsAgentAction: true,
			}

			mock.ExpectBegin()
			mock.ExpectQuery("SELECT .+ FROM pull_request_health_current").
				WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": prID}).
				WillReturnRows(pgxmock.NewRows(prHealthCurrentColumns).AddRow(
					prID, orgID, int64(8), tt.existingSHA, "base", existingJSON, existingJSON, models.PullRequestHealthEnrichmentStatusReady, &now, now, now,
				))
			mock.ExpectExec("INSERT INTO pull_request_health_snapshots").
				WithArgs(pgx.NamedArgs{
					"pull_request_id":   prID,
					"org_id":            orgID,
					"version":           int64(9),
					"head_sha":          tt.nextSHA,
					"base_sha":          "base",
					"summary_json":      pgxmock.AnyArg(),
					"enrichment_status": models.PullRequestHealthEnrichmentStatusNotRequested,
				}).
				WillReturnResult(pgxmock.NewResult("INSERT", 1))
			mock.ExpectExec("UPDATE pull_request_repair_runs[\\s\\S]*head_sha IS DISTINCT FROM @head_sha").
				WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": prID, "version": int64(9), "head_sha": tt.nextSHA}).
				WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			mock.ExpectExec("INSERT INTO pull_request_health_current").
				WithArgs(pgx.NamedArgs{
					"pull_request_id":      prID,
					"org_id":               orgID,
					"version":              int64(9),
					"head_sha":             tt.nextSHA,
					"base_sha":             "base",
					"summary_json":         pgxmock.AnyArg(),
					"summary_preview_json": pgxmock.AnyArg(),
					"enrichment_status":    models.PullRequestHealthEnrichmentStatusNotRequested,
					"enriched_at":          (*time.Time)(nil),
					"created_at":           pgxmock.AnyArg(),
					"updated_at":           pgxmock.AnyArg(),
				}).
				WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			mock.ExpectExec("UPDATE pull_requests").
				WithArgs(pgx.NamedArgs{
					"pull_request_id":    prID,
					"org_id":             orgID,
					"head_sha":           tt.nextSHA,
					"base_sha":           "base",
					"merge_state":        models.PullRequestMergeStateBlocked,
					"has_conflicts":      false,
					"failing_test_count": 2,
					"needs_agent_action": true,
					"version":            int64(9),
				}).
				WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			mock.ExpectCommit()

			current, err := store.UpsertHealthSummary(context.Background(), orgID, prID, tt.nextSHA, "base", nextSummary, nil)
			require.NoError(t, err, "UpsertHealthSummary should persist the changed health summary")
			require.Equal(t, int64(9), current.Version, "UpsertHealthSummary should advance health version for changed health")
			require.NoError(t, mock.ExpectationsWereMet(), "all head-aware obsoletion expectations should be met")
		})
	}
}

func TestPullRequestStore_UpsertHealthSummary_ReusesExistingVersion(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewPullRequestStore(mock)
	orgID := uuid.New()
	prID := uuid.New()
	now := time.Now().UTC()
	summary := models.PullRequestHealthSummary{
		MergeState:       models.PullRequestMergeStateClean,
		HasConflicts:     false,
		FailingTestCount: 0,
		NeedsAgentAction: false,
	}
	summaryJSON, err := json.Marshal(summary)
	require.NoError(t, err, "should marshal the existing summary")

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .+ FROM pull_request_health_current").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": prID}).
		WillReturnRows(pgxmock.NewRows(prHealthCurrentColumns).AddRow(
			prID, orgID, int64(7), "head", "base", summaryJSON, summaryJSON, models.PullRequestHealthEnrichmentStatusReady, &now, now, now,
		))
	mock.ExpectExec("INSERT INTO pull_request_health_current").
		WithArgs(pgx.NamedArgs{
			"pull_request_id":      prID,
			"org_id":               orgID,
			"version":              int64(7),
			"head_sha":             "head",
			"base_sha":             "base",
			"summary_json":         pgxmock.AnyArg(),
			"summary_preview_json": pgxmock.AnyArg(),
			"enrichment_status":    models.PullRequestHealthEnrichmentStatusReady,
			"enriched_at":          &now,
			"created_at":           pgxmock.AnyArg(),
			"updated_at":           pgxmock.AnyArg(),
		}).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE pull_requests").
		WithArgs(pgx.NamedArgs{
			"pull_request_id":    prID,
			"org_id":             orgID,
			"head_sha":           "head",
			"base_sha":           "base",
			"merge_state":        models.PullRequestMergeStateClean,
			"has_conflicts":      false,
			"failing_test_count": 0,
			"needs_agent_action": false,
			"version":            int64(7),
		}).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()

	current, err := store.UpsertHealthSummary(context.Background(), orgID, prID, "head", "base", summary, summaryJSON)
	require.NoError(t, err, "UpsertHealthSummary should succeed when the summary is unchanged")
	require.Equal(t, int64(7), current.Version, "UpsertHealthSummary should keep the same version for unchanged summaries")
	require.Equal(t, models.PullRequestHealthEnrichmentStatusReady, current.EnrichmentStatus, "UpsertHealthSummary should preserve enrichment state when the summary is unchanged")
	require.NoError(t, mock.ExpectationsWereMet(), "all unchanged-summary expectations should be met")
}

func TestPullRequestStore_UpdateHealthEnrichmentAndRepairRuns(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewPullRequestStore(mock)
	orgID := uuid.New()
	prID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	runID := uuid.New()
	now := time.Now().UTC()

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE pull_request_health_snapshots").
		WithArgs(pgx.NamedArgs{
			"org_id":                orgID,
			"pull_request_id":       prID,
			"version":               int64(5),
			"conflict_payload":      json.RawMessage(`{"conflict":true}`),
			"failing_tests_payload": json.RawMessage(`{"checks":[]}`),
			"payload_size_bytes":    len(`{"conflict":true}`) + len(`{"checks":[]}`),
			"enrichment_status":     models.PullRequestHealthEnrichmentStatusReady,
			"enriched_at":           pgxmock.AnyArg(),
		}).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE pull_request_health_current").
		WithArgs(pgx.NamedArgs{
			"org_id":            orgID,
			"pull_request_id":   prID,
			"version":           int64(5),
			"enrichment_status": models.PullRequestHealthEnrichmentStatusReady,
			"enriched_at":       pgxmock.AnyArg(),
		}).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()

	err = store.UpdateHealthEnrichment(context.Background(), orgID, prID, 5, json.RawMessage(`{"conflict":true}`), json.RawMessage(`{"checks":[]}`), models.PullRequestHealthEnrichmentStatusReady)
	require.NoError(t, err, "UpdateHealthEnrichment should update both current and snapshot tables")

	mock.ExpectQuery("SELECT .+ FROM pull_request_repair_runs").
		WithArgs(pgx.NamedArgs{
			"org_id":          orgID,
			"pull_request_id": prID,
			"action_type":     models.PullRequestRepairActionTypeFixTests,
			"health_version":  int64(5),
		}).
		WillReturnRows(pgxmock.NewRows(prRepairRunColumns).AddRow(
			runID, orgID, prID, sessionID, &threadID, models.PullRequestRepairActionTypeFixTests, int64(5), models.PullRequestRepairWorkspaceModeSnapshotContinuation, true, nil, now, now, "head-5", "base-5", false, "", models.PullRequestRepairTriggerSourceManual, nil,
		))

	run, err := store.GetActiveRepairRun(context.Background(), orgID, prID, models.PullRequestRepairActionTypeFixTests, 5)
	require.NoError(t, err, "GetActiveRepairRun should return the active repair run")
	require.Equal(t, runID, run.ID, "GetActiveRepairRun should decode the repair run row")
	require.Equal(t, &threadID, run.ThreadID, "GetActiveRepairRun should decode the repair thread row")
	require.Equal(t, "head-5", run.HeadSHA, "GetActiveRepairRun should decode the repair head SHA")

	mock.ExpectQuery("SELECT .+ FROM pull_request_repair_runs").
		WithArgs(pgx.NamedArgs{
			"org_id":          orgID,
			"pull_request_id": prID,
			"action_type":     models.PullRequestRepairActionTypeFixTests,
			"head_sha":        "head-5",
		}).
		WillReturnRows(pgxmock.NewRows(prRepairRunColumns).AddRow(
			runID, orgID, prID, sessionID, &threadID, models.PullRequestRepairActionTypeFixTests, int64(4), models.PullRequestRepairWorkspaceModeSnapshotContinuation, true, nil, now, now, "head-5", "base-older", false, "", models.PullRequestRepairTriggerSourceManual, nil,
		))

	run, err = store.GetActiveRepairRunByHead(context.Background(), orgID, prID, models.PullRequestRepairActionTypeFixTests, "head-5")
	require.NoError(t, err, "GetActiveRepairRunByHead should return the active repair run for the current PR head")
	require.Equal(t, int64(4), run.HealthVersion, "GetActiveRepairRunByHead should return repairs from older health versions on the same head")
	require.Equal(t, "head-5", run.HeadSHA, "GetActiveRepairRunByHead should decode the repair head SHA")

	mock.ExpectQuery("INSERT INTO pull_request_repair_runs").
		WithArgs(pgx.NamedArgs{
			"org_id":               orgID,
			"pull_request_id":      prID,
			"session_id":           sessionID,
			"thread_id":            &threadID,
			"action_type":          models.PullRequestRepairActionTypeResolveConflicts,
			"health_version":       int64(6),
			"workspace_mode":       models.PullRequestRepairWorkspaceModePRHeadReconstruction,
			"active":               true,
			"obsoleted_by_version": (*int64)(nil),
			"head_sha":             "head-6",
			"base_sha":             "base-6",
			"auto_attempt":         false,
			"trigger_reason":       "",
			"triggered_by_source":  models.PullRequestRepairTriggerSourceManual,
			"triggered_by_user_id": (*uuid.UUID)(nil),
		}).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(runID, now, now))

	createRun := &models.PullRequestRepairRun{
		OrgID:         orgID,
		PullRequestID: prID,
		SessionID:     sessionID,
		ThreadID:      &threadID,
		ActionType:    models.PullRequestRepairActionTypeResolveConflicts,
		HealthVersion: 6,
		WorkspaceMode: models.PullRequestRepairWorkspaceModePRHeadReconstruction,
		Active:        true,
		HeadSHA:       "head-6",
		BaseSHA:       "base-6",
	}
	err = store.CreateRepairRun(context.Background(), createRun)
	require.NoError(t, err, "CreateRepairRun should insert a repair run row")
	require.Equal(t, runID, createRun.ID, "CreateRepairRun should populate the inserted ID")

	mock.ExpectExec("UPDATE pull_request_repair_runs").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "id": runID}).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.DeactivateRepairRun(context.Background(), orgID, runID)
	require.NoError(t, err, "DeactivateRepairRun should mark the repair run inactive")
	require.NoError(t, mock.ExpectationsWereMet(), "all enrichment and repair run expectations should be met")
}

func TestPullRequestStore_ListActiveRepairRuns(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewPullRequestStore(mock)
	orgID := uuid.New()
	prID := uuid.New()
	sessionA := uuid.New()
	sessionB := uuid.New()
	threadA := uuid.New()
	threadB := uuid.New()
	now := time.Now().UTC()

	mock.ExpectQuery("SELECT .+ FROM pull_request_repair_runs").
		WithArgs(pgx.NamedArgs{
			"org_id":          orgID,
			"pull_request_id": prID,
			"health_version":  int64(9),
		}).
		WillReturnRows(pgxmock.NewRows(prRepairRunColumns).
			AddRow(uuid.New(), orgID, prID, sessionA, &threadA, models.PullRequestRepairActionTypeFixTests, int64(9), models.PullRequestRepairWorkspaceModeSnapshotContinuation, true, nil, now, now, "head-9", "base-9", false, "", models.PullRequestRepairTriggerSourceManual, nil).
			AddRow(uuid.New(), orgID, prID, sessionB, &threadB, models.PullRequestRepairActionTypeResolveConflicts, int64(9), models.PullRequestRepairWorkspaceModePRHeadReconstruction, true, nil, now, now, "head-9", "base-9", true, "session_idle", models.PullRequestRepairTriggerSourceSystem, nil))

	runs, err := store.ListActiveRepairRuns(context.Background(), orgID, prID, 9)
	require.NoError(t, err, "ListActiveRepairRuns should return active repair runs for the current health version")
	require.Len(t, runs, 2, "ListActiveRepairRuns should return every active repair run for the pull request health version")
	require.Equal(t, sessionA, runs[0].SessionID, "ListActiveRepairRuns should decode the first repair session")
	require.Equal(t, &threadA, runs[0].ThreadID, "ListActiveRepairRuns should decode the first repair thread")
	require.Equal(t, sessionB, runs[1].SessionID, "ListActiveRepairRuns should decode the second repair session")
	require.Equal(t, &threadB, runs[1].ThreadID, "ListActiveRepairRuns should decode the second repair thread")

	mock.ExpectQuery("SELECT .+ FROM pull_request_repair_runs").
		WithArgs(pgx.NamedArgs{
			"org_id":          orgID,
			"pull_request_id": prID,
			"head_sha":        "head-10",
		}).
		WillReturnRows(pgxmock.NewRows(prRepairRunColumns).
			AddRow(uuid.New(), orgID, prID, sessionA, &threadA, models.PullRequestRepairActionTypeFixTests, int64(8), models.PullRequestRepairWorkspaceModeSnapshotContinuation, true, nil, now, now, "head-10", "base-8", false, "", models.PullRequestRepairTriggerSourceManual, nil).
			AddRow(uuid.New(), orgID, prID, sessionB, &threadB, models.PullRequestRepairActionTypeResolveConflicts, int64(10), models.PullRequestRepairWorkspaceModePRHeadReconstruction, true, nil, now, now, "head-10", "base-10", true, "session_idle", models.PullRequestRepairTriggerSourceSystem, nil))

	runs, err = store.ListActiveRepairRunsByHead(context.Background(), orgID, prID, "head-10")
	require.NoError(t, err, "ListActiveRepairRunsByHead should return active repairs for the current PR head")
	require.Len(t, runs, 2, "ListActiveRepairRunsByHead should include repairs launched on older health versions for the same head")
	require.Equal(t, int64(8), runs[0].HealthVersion, "ListActiveRepairRunsByHead should preserve each repair's launch health version")
	require.Equal(t, "head-10", runs[0].HeadSHA, "ListActiveRepairRunsByHead should decode repair head SHA")
	require.NoError(t, mock.ExpectationsWereMet(), "all active repair run expectations should be met")
}

func TestPullRequestStore_CountAutoRepairRunsByHead(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewPullRequestStore(mock)
	orgID := uuid.New()
	prID := uuid.New()

	mock.ExpectQuery("SELECT count.+ FROM pull_request_repair_runs").
		WithArgs(pgx.NamedArgs{
			"org_id":          orgID,
			"pull_request_id": prID,
			"action_type":     models.PullRequestRepairActionTypeFixTests,
			"head_sha":        "head-sha",
		}).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(2))

	count, err := store.CountAutoRepairRunsByHead(context.Background(), orgID, prID, models.PullRequestRepairActionTypeFixTests, "head-sha")
	require.NoError(t, err, "CountAutoRepairRunsByHead should succeed")
	require.Equal(t, 2, count, "CountAutoRepairRunsByHead should return the scanned count of automatic attempts")
	require.NoError(t, mock.ExpectationsWereMet(), "all auto-repair count expectations should be met")
}

func TestPullRequestStore_beginTxRequiresTxStarter(t *testing.T) {
	t.Parallel()

	store := &PullRequestStore{db: &noTxPullRequestHealthDB{}}
	_, err := store.beginTx(context.Background())
	require.Error(t, err, "beginTx should fail when the DB does not support transactions")
	require.Contains(t, err.Error(), "does not support transactions", "beginTx should explain the missing transaction capability")
}

type noTxPullRequestHealthDB struct{}

func (n *noTxPullRequestHealthDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func (n *noTxPullRequestHealthDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, nil
}

func (n *noTxPullRequestHealthDB) QueryRow(context.Context, string, ...any) pgx.Row {
	return nil
}

func (n *noTxPullRequestHealthDB) SendBatch(context.Context, *pgx.Batch) pgx.BatchResults {
	return nil
}

package db

import (
	"context"
	"encoding/json"
	"regexp"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestPRReadinessStore_CreateRun(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	repoID := uuid.New()
	userID := uuid.New()
	runID := uuid.New()
	snapshotKey := "snap-a"
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO pr_readiness_runs")).
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows([]string{"id", "started_at", "created_at", "updated_at"}).
			AddRow(runID, now, now, now))

	run := &models.PRReadinessRun{
		OrgID:                      orgID,
		SessionID:                  sessionID,
		RepositoryID:               &repoID,
		Status:                     models.PRReadinessRunStatusQueued,
		EvaluatedWorkspaceRevision: 7,
		EvaluatedSnapshotKey:       &snapshotKey,
		Summary:                    "Queued",
		TriggeredByUserID:          &userID,
	}
	err = NewPRReadinessStore(mock).CreateRun(context.Background(), run)
	require.NoError(t, err, "CreateRun should insert a queued readiness run")
	require.Equal(t, runID, run.ID, "CreateRun should scan the generated run id")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPRReadinessStore_GetLatestBySession(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	runID := uuid.New()
	checkID := uuid.New()
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	snapshotKey := "snap-a"
	packet := json.RawMessage(`{"checks":[]}`)

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	mock.ExpectQuery("FROM pr_readiness_runs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "session_id", "repository_id", "status",
			"evaluated_workspace_revision", "evaluated_snapshot_key", "summary", "review_packet",
			"triggered_by_user_id", "started_at", "completed_at", "created_at", "updated_at",
		}).AddRow(runID, orgID, sessionID, nil, models.PRReadinessRunStatusWarnings, int64(7), &snapshotKey, "Ready with warnings", packet, nil, now, &now, now, now))
	mock.ExpectQuery("FROM pr_readiness_checks").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "run_id", "session_id", "check_type", "status",
			"enforcement", "title", "summary", "details", "action", "created_at",
		}).AddRow(checkID, orgID, runID, sessionID, models.PRReadinessCheckTypeTestEvidencePresent, models.PRReadinessCheckStatusWarning, models.PRReadinessEnforcementAdvisory, "No test evidence found", "No captured test command.", nil, "Run tests", now))

	run, err := NewPRReadinessStore(mock).GetLatestBySession(context.Background(), orgID, sessionID)
	require.NoError(t, err, "GetLatestBySession should load the latest run and checks")
	require.NotNil(t, run, "GetLatestBySession should return a run")
	require.Equal(t, runID, run.ID, "GetLatestBySession should return the latest run")
	require.Equal(t, []models.PRReadinessCheck{
		{
			ID:          checkID,
			OrgID:       orgID,
			RunID:       runID,
			SessionID:   sessionID,
			CheckType:   models.PRReadinessCheckTypeTestEvidencePresent,
			Status:      models.PRReadinessCheckStatusWarning,
			Enforcement: models.PRReadinessEnforcementAdvisory,
			Title:       "No test evidence found",
			Summary:     "No captured test command.",
			Action:      "Run tests",
			CreatedAt:   now,
		},
	}, run.Checks, "GetLatestBySession should hydrate run checks")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

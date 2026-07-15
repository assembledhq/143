package db

import (
	"context"
	"encoding/json"
	"regexp"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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
		WillReturnRows(pgxmock.NewRows([]string{"id", "changeset_id", "started_at", "created_at", "updated_at"}).
			AddRow(runID, uuid.Nil, now, now, now))

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

func TestPRReadinessStore_MarkFailed(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	runID := uuid.New()
	summary := "Worker job failed before readiness completed."

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	mock.ExpectExec("UPDATE pr_readiness_runs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = NewPRReadinessStore(mock).MarkFailed(context.Background(), orgID, runID, summary)

	require.NoError(t, err, "MarkFailed should mark the readiness run failed")
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
			"check_key", "enforcement_builder", "enforcement_engineer", "enforcement_admin", "provenance", "source",
		}).AddRow(checkID, orgID, runID, sessionID, models.PRReadinessCheckTypeTestEvidencePresent, models.PRReadinessCheckStatusWarning, models.PRReadinessEnforcementAdvisory, "No test evidence found", "No captured test command.", nil, "Run tests", now,
			"test_evidence_present", models.PRReadinessEnforcementAdvisory, models.PRReadinessEnforcementAdvisory, models.PRReadinessEnforcementAdvisory, "builtin", ""))
	mock.ExpectQuery("FROM pr_readiness_bypasses").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "readiness_run_id", "session_id", "repository_id", "pull_request_id", "bypassed_by_user_id", "reason", "bypassed_checks", "created_at",
		}))

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
			EnforcementByRole: models.PRReadinessEnforcementByRole{
				Builder:  models.PRReadinessEnforcementAdvisory,
				Engineer: models.PRReadinessEnforcementAdvisory,
				Admin:    models.PRReadinessEnforcementAdvisory,
			},
			EnforcementBuilder:  models.PRReadinessEnforcementAdvisory,
			EnforcementEngineer: models.PRReadinessEnforcementAdvisory,
			EnforcementAdmin:    models.PRReadinessEnforcementAdvisory,
			CheckKey:            "test_evidence_present",
			Provenance:          "builtin",
			Title:               "No test evidence found",
			Summary:             "No captured test command.",
			Action:              "Run tests",
			CreatedAt:           now,
		},
	}, run.Checks, "GetLatestBySession should hydrate run checks")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPRReadinessStore_ResolvePolicyPrefersRepositoryOverride(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	repoConfig := models.DefaultPRReadinessPolicyConfig()
	repoConfig.Checks[models.PRReadinessCheckTypeRiskFlags] = models.PRReadinessCheckPolicy{
		Enforcement: models.PRReadinessEnforcementByRole{Builder: models.PRReadinessEnforcementBlocking},
	}
	configBytes, err := json.Marshal(repoConfig)
	require.NoError(t, err, "policy config should marshal for fixture rows")

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	mock.ExpectQuery("FROM pr_readiness_policies").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "repository_id", "config", "active", "created_by_user_id", "created_at",
		}).AddRow(uuid.New(), orgID, &repoID, configBytes, true, nil, now))

	policy, err := NewPRReadinessStore(mock).ResolvePolicy(context.Background(), orgID, &repoID)
	require.NoError(t, err, "ResolvePolicy should load the active repository override")
	require.Equal(t, models.PRReadinessEnforcementBlocking, policy.Config.EffectivePolicy().EnforcementFor(models.RoleBuilder, models.PRReadinessCheckTypeRiskFlags), "repository override should control effective builder enforcement")
	require.Equal(t, "repository", policy.Source, "ResolvePolicy should expose repository override provenance")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPRReadinessStore_SavePolicyUsesInsertOnlyActiveRows(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	userID := uuid.New()
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE pr_readiness_policies").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectQuery("INSERT INTO pr_readiness_policies").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "repository_id", "config", "active", "created_by_user_id", "created_at",
		}).AddRow(uuid.New(), orgID, &repoID, []byte(`{"enabled_for_builders":true}`), true, &userID, now))
	mock.ExpectCommit()

	policy, err := NewPRReadinessStore(mock).SavePolicy(context.Background(), orgID, &repoID, models.DefaultPRReadinessPolicyConfig(), &userID)
	require.NoError(t, err, "SavePolicy should atomically inactivate the old policy and insert a new active version")
	require.Equal(t, &repoID, policy.RepositoryID, "SavePolicy should preserve repository override scope")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPRReadinessStore_CreateBypassRequiresCompletedBlockingChecks(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	runID := uuid.New()
	userID := uuid.New()
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	mock.ExpectQuery("FROM pr_readiness_runs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "session_id", "repository_id", "status",
			"evaluated_workspace_revision", "evaluated_snapshot_key", "summary", "review_packet",
			"triggered_by_user_id", "started_at", "completed_at", "created_at", "updated_at",
		}).AddRow(runID, orgID, sessionID, nil, models.PRReadinessRunStatusBlocked, int64(7), nil, "Blocked", nil, nil, now, &now, now, now))
	mock.ExpectQuery("FROM pr_readiness_checks").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "run_id", "session_id", "check_key", "check_type", "status",
			"enforcement", "enforcement_builder", "enforcement_engineer", "enforcement_admin",
			"provenance", "source", "title", "summary", "details", "action", "created_at",
		}).AddRow(uuid.New(), orgID, runID, sessionID, "agent_review_clean", models.PRReadinessCheckTypeAgentReviewClean, models.PRReadinessCheckStatusFailed,
			models.PRReadinessEnforcementBlocking, models.PRReadinessEnforcementBlocking, models.PRReadinessEnforcementAdvisory, models.PRReadinessEnforcementAdvisory,
			"builtin", "", "Agent review not clean", "Run Review must complete cleanly.", nil, "Run review", now))
	mock.ExpectQuery("INSERT INTO pr_readiness_bypasses").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "readiness_run_id", "session_id", "repository_id", "pull_request_id", "bypassed_by_user_id", "reason", "bypassed_checks", "created_at",
		}).AddRow(uuid.New(), orgID, runID, sessionID, nil, nil, userID, "known false positive after manual review", []byte(`["agent_review_clean"]`), now))

	bypass, err := NewPRReadinessStore(mock).CreateBypass(context.Background(), orgID, runID, userID, "known false positive after manual review")
	require.NoError(t, err, "CreateBypass should allow completed current blocking checks to be bypassed with a reason")
	require.Equal(t, []string{"agent_review_clean"}, bypass.BypassedChecks, "CreateBypass should persist the bypassed blocking check keys")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPRReadinessStore_CreateBypassRejectsStaleOrRunningReadiness(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status models.PRReadinessRunStatus
	}{
		{name: "queued", status: models.PRReadinessRunStatusQueued},
		{name: "running", status: models.PRReadinessRunStatusRunning},
		{name: "passed", status: models.PRReadinessRunStatusPassed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			orgID := uuid.New()
			sessionID := uuid.New()
			runID := uuid.New()
			userID := uuid.New()
			now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "pgxmock should initialize")
			defer mock.Close()

			mock.ExpectQuery("FROM pr_readiness_runs").
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(pgxmock.NewRows([]string{
					"id", "org_id", "session_id", "repository_id", "status",
					"evaluated_workspace_revision", "evaluated_snapshot_key", "summary", "review_packet",
					"triggered_by_user_id", "started_at", "completed_at", "created_at", "updated_at",
				}).AddRow(runID, orgID, sessionID, nil, tt.status, int64(7), nil, string(tt.status), nil, nil, now, nil, now, now))

			_, err = NewPRReadinessStore(mock).CreateBypass(context.Background(), orgID, runID, userID, "not eligible")
			require.Error(t, err, "CreateBypass should reject non-blocked or still-running readiness states")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestPRReadinessStore_UpsertContext(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	userID := uuid.New()
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	mock.ExpectQuery("INSERT INTO pr_readiness_contexts").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"org_id", "session_id", "issue_less_reason", "created_by_user_id", "updated_by_user_id", "created_at", "updated_at",
		}).AddRow(orgID, sessionID, "maintenance follow-up", &userID, &userID, now, now))

	ctxValue, err := NewPRReadinessStore(mock).UpsertContext(context.Background(), orgID, sessionID, "maintenance follow-up", userID)
	require.NoError(t, err, "UpsertContext should persist an explicit issue-less readiness marker")
	require.Equal(t, "maintenance follow-up", ctxValue.IssueLessReason, "UpsertContext should return the stored reason")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPRReadinessStore_CreateBypassHonorsPolicy(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	runID := uuid.New()
	userID := uuid.New()
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	policy := models.DefaultPRReadinessPolicyConfig()
	policy.Bypass.AllowedRoles = []models.Role{models.RoleAdmin}

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()
	_ = sessionID
	_ = now

	_, err = NewPRReadinessStore(mock).CreateBypassWithPolicy(context.Background(), orgID, runID, userID, "not allowed for builder", models.RoleBuilder, policy)
	require.Error(t, err, "CreateBypassWithPolicy should reject roles excluded by policy")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPRReadinessStore_CreateBypassRejectsNonBypassableChecks(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	runID := uuid.New()
	userID := uuid.New()
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	policy := models.DefaultPRReadinessPolicyConfig()
	policy.Bypass.NonBypassableChecks = []string{"agent_review_clean"}

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	mock.ExpectQuery("FROM pr_readiness_runs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "session_id", "repository_id", "status",
			"evaluated_workspace_revision", "evaluated_snapshot_key", "summary", "review_packet",
			"triggered_by_user_id", "started_at", "completed_at", "created_at", "updated_at",
		}).AddRow(runID, orgID, sessionID, nil, models.PRReadinessRunStatusBlocked, int64(7), nil, "Blocked", nil, nil, now, &now, now, now))
	mock.ExpectQuery("FROM pr_readiness_checks").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "run_id", "session_id", "check_key", "check_type", "status",
			"enforcement", "enforcement_builder", "enforcement_engineer", "enforcement_admin",
			"provenance", "source", "title", "summary", "details", "action", "created_at",
		}).AddRow(uuid.New(), orgID, runID, sessionID, "agent_review_clean", models.PRReadinessCheckTypeAgentReviewClean, models.PRReadinessCheckStatusFailed,
			models.PRReadinessEnforcementBlocking, models.PRReadinessEnforcementBlocking, models.PRReadinessEnforcementAdvisory, models.PRReadinessEnforcementAdvisory,
			"builtin", "", "Agent review not clean", "Run Review must complete cleanly.", nil, "Run review", now))

	_, err = NewPRReadinessStore(mock).CreateBypassWithPolicy(context.Background(), orgID, runID, userID, "non-bypassable", models.RoleBuilder, policy)
	require.Error(t, err, "CreateBypassWithPolicy should reject policy non-bypassable checks")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPRReadinessStore_CreateBypassUsesCurrentRoleEnforcement(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	runID := uuid.New()
	userID := uuid.New()
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	policy := models.DefaultPRReadinessPolicyConfig()
	policy.Checks[models.PRReadinessCheckTypeRiskFlags] = models.PRReadinessCheckPolicy{
		Enforcement: models.PRReadinessEnforcementByRole{
			Builder:  models.PRReadinessEnforcementAdvisory,
			Engineer: models.PRReadinessEnforcementBlocking,
			Admin:    models.PRReadinessEnforcementAdvisory,
		},
	}

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	mock.ExpectQuery("FROM pr_readiness_runs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "session_id", "repository_id", "status",
			"evaluated_workspace_revision", "evaluated_snapshot_key", "summary", "review_packet",
			"triggered_by_user_id", "started_at", "completed_at", "created_at", "updated_at",
		}).AddRow(runID, orgID, sessionID, nil, models.PRReadinessRunStatusBlocked, int64(7), nil, "Blocked", nil, nil, now, &now, now, now))
	mock.ExpectQuery("FROM pr_readiness_checks").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "run_id", "session_id", "check_key", "check_type", "status",
			"enforcement", "enforcement_builder", "enforcement_engineer", "enforcement_admin",
			"provenance", "source", "title", "summary", "details", "action", "created_at",
		}).AddRow(uuid.New(), orgID, runID, sessionID, "risk_flags", models.PRReadinessCheckTypeRiskFlags, models.PRReadinessCheckStatusFailed,
			models.PRReadinessEnforcementAdvisory, models.PRReadinessEnforcementAdvisory, models.PRReadinessEnforcementBlocking, models.PRReadinessEnforcementAdvisory,
			"builtin", "", "Risk flags detected", "Sensitive path changed.", nil, "View files", now))
	mock.ExpectQuery("INSERT INTO pr_readiness_bypasses").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "readiness_run_id", "session_id", "repository_id", "pull_request_id", "bypassed_by_user_id", "reason", "bypassed_checks", "created_at",
		}).AddRow(uuid.New(), orgID, runID, sessionID, nil, nil, userID, "engineer reviewed the configured risk", []byte(`["risk_flags"]`), now))

	bypass, err := NewPRReadinessStore(mock).CreateBypassWithPolicy(context.Background(), orgID, runID, userID, "engineer reviewed the configured risk", models.RoleMember, policy)
	require.NoError(t, err, "CreateBypassWithPolicy should use the current role's blocking enforcement")
	require.Equal(t, []string{"risk_flags"}, bypass.BypassedChecks, "CreateBypassWithPolicy should bypass checks blocking the current role")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPRReadinessStore_AttachBypassesToPullRequest(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	runID := uuid.New()
	prID := uuid.New()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	mock.ExpectExec("UPDATE pr_readiness_bypasses").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = NewPRReadinessStore(mock).AttachBypassesToPullRequest(context.Background(), orgID, runID, prID)
	require.NoError(t, err, "AttachBypassesToPullRequest should link run bypasses to the PR row")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPRReadinessStore_UpdateCustomCheckInactivatesByID(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	checkID := uuid.New()
	userID := uuid.New()
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery("UPDATE pr_readiness_custom_checks[\\s\\S]*source = 'org_settings'").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"repository_id"}).AddRow(nil))
	mock.ExpectQuery("INSERT INTO pr_readiness_custom_checks").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "repository_id", "check_key", "name", "prompt", "path_filters", "enforcement", "source", "active", "created_by_user_id", "created_at",
		}).AddRow(uuid.New(), orgID, nil, "renamed_check", "Renamed check", "Prompt", []byte(`{}`), []byte(`{"builder":"advisory"}`), models.PRReadinessCustomCheckSourceOrgSettings, true, &userID, now))
	mock.ExpectCommit()

	updated, err := NewPRReadinessStore(mock).UpdateCustomCheck(context.Background(), orgID, checkID, models.PRReadinessCustomCheck{
		CheckKey: "renamed_check",
		Name:     "Renamed check",
		Prompt:   "Prompt",
		Source:   models.PRReadinessCustomCheckSourceOrgSettings,
	}, &userID)
	require.NoError(t, err, "UpdateCustomCheck should inactivate the addressed row before inserting the replacement")
	require.Equal(t, "renamed_check", updated.CheckKey, "UpdateCustomCheck should allow check key changes without leaving the old row active")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPRReadinessStore_UpdateCustomCheckRejectsRepoConfigRows(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	checkID := uuid.New()
	userID := uuid.New()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery("UPDATE pr_readiness_custom_checks[\\s\\S]*source = 'org_settings'").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"repository_id"}))
	mock.ExpectRollback()

	_, err = NewPRReadinessStore(mock).UpdateCustomCheck(context.Background(), orgID, checkID, models.PRReadinessCustomCheck{
		CheckKey: "renamed_check",
		Name:     "Renamed check",
		Prompt:   "Prompt",
		Source:   models.PRReadinessCustomCheckSourceOrgSettings,
	}, &userID)
	require.ErrorIs(t, err, pgx.ErrNoRows, "UpdateCustomCheck should not mutate repo-config managed checks")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPRReadinessStore_DeleteCustomCheckOnlyDeletesOrgSettingsRows(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	checkID := uuid.New()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	mock.ExpectExec("UPDATE pr_readiness_custom_checks[\\s\\S]*source = 'org_settings'").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = NewPRReadinessStore(mock).DeleteCustomCheck(context.Background(), orgID, checkID)
	require.NoError(t, err, "DeleteCustomCheck should delete settings-defined checks")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

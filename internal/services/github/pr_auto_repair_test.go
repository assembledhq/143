package github

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

func TestChooseAutoRepairAction(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		policy   autoRepairPolicy
		health   *models.PullRequestHealthResponse
		expected models.PullRequestRepairActionType
	}{
		{
			name:   "chooses conflicts before tests",
			policy: autoRepairPolicy{ResolveConflicts: true, FixTests: true},
			health: &models.PullRequestHealthResponse{
				CanResolveConflicts: true,
				CanFixTests:         true,
			},
			expected: models.PullRequestRepairActionTypeResolveConflicts,
		},
		{
			name:   "chooses tests when conflicts disabled",
			policy: autoRepairPolicy{ResolveConflicts: false, FixTests: true},
			health: &models.PullRequestHealthResponse{
				CanResolveConflicts: true,
				CanFixTests:         true,
			},
			expected: models.PullRequestRepairActionTypeFixTests,
		},
		{
			name:   "returns no action when no enabled blocker exists",
			policy: autoRepairPolicy{ResolveConflicts: true, FixTests: false},
			health: &models.PullRequestHealthResponse{
				CanResolveConflicts: false,
				CanFixTests:         true,
			},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual := chooseAutoRepairAction(tt.policy, tt.health)
			require.Equal(t, tt.expected, actual, "chooseAutoRepairAction should select the expected repair action")
		})
	}
}

func TestApplyAutoRepairPreference(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		orgDefault bool
		pref       models.AutomaticFollowThroughPreference
		expected   bool
	}{
		{name: "inherit preserves enabled org default", orgDefault: true, pref: models.AutomaticFollowThroughPreferenceInherit, expected: true},
		{name: "inherit preserves disabled org default", orgDefault: false, pref: models.AutomaticFollowThroughPreferenceInherit, expected: false},
		{name: "on enables when org disabled", orgDefault: false, pref: models.AutomaticFollowThroughPreferenceOn, expected: true},
		{name: "off disables when org enabled", orgDefault: true, pref: models.AutomaticFollowThroughPreferenceOff, expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual := applyAutoRepairPreference(tt.orgDefault, tt.pref)
			require.Equal(t, tt.expected, actual, "applyAutoRepairPreference should resolve user preference over org default")
		})
	}
}

func TestPRServiceMaybeStartAutoRepairForPullRequestDelegatesToLinkedSession(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	orgID := uuid.New()
	prID := uuid.New()
	sessionID := uuid.New()
	now := time.Now()
	row := handlerPRRow(prID, &sessionID, orgID, "org/repo", now)
	headSHA := "head-from-github"
	setAutoRepairPRRowValue(row, "head_sha", &headSHA)
	mock.ExpectQuery("FROM pull_requests").
		WithArgs(pgx.NamedArgs{"id": prID, "org_id": orgID}).
		WillReturnRows(pgxmock.NewRows(handlerPRColumns).AddRow(row...))
	expectAutoRepairSession(mock, orgID, sessionID, now, models.SessionStatusRunning)

	service := &PRService{
		pullRequests: db.NewPullRequestStore(mock),
		sessions:     db.NewSessionStore(mock),
		orgs:         db.NewOrganizationStore(mock),
	}
	decision, err := service.MaybeStartAutoRepairForPullRequest(context.Background(), orgID, prID, "github_pr_health_updated")

	require.NoError(t, err, "pull-request-triggered auto-repair evaluation should delegate without error")
	require.Equal(t, AutoRepairDecisionNotResumable, decision.Status, "delegation should apply the existing session eligibility checks")
	require.NoError(t, mock.ExpectationsWereMet(), "all pull request and session lookup expectations should be met")
}

func expectAutoRepairCount(mock pgxmock.PgxPoolIface, orgID, prID uuid.UUID, action models.PullRequestRepairActionType, headSHA string, count int) {
	mock.ExpectQuery("SELECT count.+ FROM pull_request_repair_runs").
		WithArgs(pgx.NamedArgs{
			"org_id":          orgID,
			"pull_request_id": prID,
			"action_type":     action,
			"head_sha":        headSHA,
		}).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(count))
}

func TestPRServiceAutoRepairBudgetExhausted(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	prID := uuid.New()

	t.Run("empty action is treated as exhausted without querying", func(t *testing.T) {
		t.Parallel()
		service := &PRService{}
		exhausted, err := service.autoRepairBudgetExhausted(context.Background(), orgID, prID, "", "head")
		require.NoError(t, err, "autoRepairBudgetExhausted should not error on empty action")
		require.True(t, exhausted, "an empty action should be considered exhausted so no repair starts")
	})

	t.Run("empty head is treated as exhausted without querying", func(t *testing.T) {
		t.Parallel()
		service := &PRService{}
		exhausted, err := service.autoRepairBudgetExhausted(context.Background(), orgID, prID, models.PullRequestRepairActionTypeFixTests, "")
		require.NoError(t, err, "autoRepairBudgetExhausted should not error on empty head")
		require.True(t, exhausted, "an empty head should be considered exhausted so no repair starts")
	})

	t.Run("no prior attempts leaves budget available", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create mock pool")
		defer mock.Close()
		expectAutoRepairCount(mock, orgID, prID, models.PullRequestRepairActionTypeFixTests, "head", 0)

		service := &PRService{pullRequests: db.NewPullRequestStore(mock)}
		exhausted, err := service.autoRepairBudgetExhausted(context.Background(), orgID, prID, models.PullRequestRepairActionTypeFixTests, "head")
		require.NoError(t, err, "autoRepairBudgetExhausted should succeed")
		require.False(t, exhausted, "no prior automatic attempts should leave budget available")
		require.NoError(t, mock.ExpectationsWereMet(), "all budget count expectations should be met")
	})

	t.Run("one prior attempt exhausts the per-head budget", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create mock pool")
		defer mock.Close()
		expectAutoRepairCount(mock, orgID, prID, models.PullRequestRepairActionTypeFixTests, "head", 1)

		service := &PRService{pullRequests: db.NewPullRequestStore(mock)}
		exhausted, err := service.autoRepairBudgetExhausted(context.Background(), orgID, prID, models.PullRequestRepairActionTypeFixTests, "head")
		require.NoError(t, err, "autoRepairBudgetExhausted should succeed")
		require.True(t, exhausted, "a single prior automatic attempt should exhaust the per-head budget")
		require.NoError(t, mock.ExpectationsWereMet(), "all budget count expectations should be met")
	})
}

func TestPRServiceBudgetExhaustedBeforeHealth(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	prID := uuid.New()

	t.Run("no enabled actions is never exhausted", func(t *testing.T) {
		t.Parallel()
		service := &PRService{}
		exhausted, err := service.budgetExhaustedBeforeHealth(context.Background(), orgID, prID, "head", autoRepairPolicy{})
		require.NoError(t, err, "budgetExhaustedBeforeHealth should not error when nothing is enabled")
		require.False(t, exhausted, "with no enabled actions there is nothing to exhaust")
	})

	t.Run("single enabled action with budget available is not exhausted", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create mock pool")
		defer mock.Close()
		expectAutoRepairCount(mock, orgID, prID, models.PullRequestRepairActionTypeResolveConflicts, "head", 0)

		service := &PRService{pullRequests: db.NewPullRequestStore(mock)}
		exhausted, err := service.budgetExhaustedBeforeHealth(context.Background(), orgID, prID, "head", autoRepairPolicy{ResolveConflicts: true})
		require.NoError(t, err, "budgetExhaustedBeforeHealth should succeed")
		require.False(t, exhausted, "an enabled action with remaining budget should not be exhausted")
		require.NoError(t, mock.ExpectationsWereMet(), "all budget count expectations should be met")
	})

	t.Run("not exhausted while any enabled action retains budget", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create mock pool")
		defer mock.Close()
		expectAutoRepairCount(mock, orgID, prID, models.PullRequestRepairActionTypeResolveConflicts, "head", 0)
		expectAutoRepairCount(mock, orgID, prID, models.PullRequestRepairActionTypeFixTests, "head", 1)

		service := &PRService{pullRequests: db.NewPullRequestStore(mock)}
		exhausted, err := service.budgetExhaustedBeforeHealth(context.Background(), orgID, prID, "head", autoRepairPolicy{ResolveConflicts: true, FixTests: true})
		require.NoError(t, err, "budgetExhaustedBeforeHealth should succeed")
		require.False(t, exhausted, "the pre-health short-circuit must not fire while any enabled action can still run")
		require.NoError(t, mock.ExpectationsWereMet(), "all budget count expectations should be met")
	})

	t.Run("exhausted only when every enabled action is exhausted", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create mock pool")
		defer mock.Close()
		expectAutoRepairCount(mock, orgID, prID, models.PullRequestRepairActionTypeResolveConflicts, "head", 1)
		expectAutoRepairCount(mock, orgID, prID, models.PullRequestRepairActionTypeFixTests, "head", 1)

		service := &PRService{pullRequests: db.NewPullRequestStore(mock)}
		exhausted, err := service.budgetExhaustedBeforeHealth(context.Background(), orgID, prID, "head", autoRepairPolicy{ResolveConflicts: true, FixTests: true})
		require.NoError(t, err, "budgetExhaustedBeforeHealth should succeed")
		require.True(t, exhausted, "the pre-health short-circuit fires only when all enabled actions are exhausted")
		require.NoError(t, mock.ExpectationsWereMet(), "all budget count expectations should be met")
	})
}

func TestPRServiceMaybeStartAutoRepairCheapNoOps(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		setup        func(mock pgxmock.PgxPoolIface, orgID, sessionID, prID uuid.UUID, now time.Time)
		wantStatus   AutoRepairDecisionStatus
		wantPRLinked bool
	}{
		{
			name: "disabled policy stops before budget or health reads",
			setup: func(mock pgxmock.PgxPoolIface, orgID, sessionID, prID uuid.UUID, now time.Time) {
				expectAutoRepairSession(mock, orgID, sessionID, now, models.SessionStatusCompleted)
				expectAutoRepairPullRequest(mock, orgID, sessionID, prID, now, "head-disabled")
				expectAutoRepairOrg(mock, orgID, json.RawMessage(`{}`), now)
			},
			wantStatus:   AutoRepairDecisionDisabled,
			wantPRLinked: true,
		},
		{
			name: "missing pull request stops before policy or health reads",
			setup: func(mock pgxmock.PgxPoolIface, orgID, sessionID, _ uuid.UUID, now time.Time) {
				expectAutoRepairSession(mock, orgID, sessionID, now, models.SessionStatusCompleted)
				mock.ExpectQuery("FROM pull_requests").
					WithArgs(pgx.NamedArgs{"session_id": sessionID, "org_id": orgID}).
					WillReturnRows(pgxmock.NewRows(handlerPRColumns))
			},
			wantStatus: AutoRepairDecisionNoPullRequest,
		},
		{
			name: "exhausted automatic budget stops before health reads",
			setup: func(mock pgxmock.PgxPoolIface, orgID, sessionID, prID uuid.UUID, now time.Time) {
				expectAutoRepairSession(mock, orgID, sessionID, now, models.SessionStatusCompleted)
				expectAutoRepairPullRequest(mock, orgID, sessionID, prID, now, "head-exhausted")
				expectAutoRepairOrg(mock, orgID, models.DefaultNewOrganizationSettings(), now)
				expectAutoRepairCount(mock, orgID, prID, models.PullRequestRepairActionTypeResolveConflicts, "head-exhausted", 1)
				expectAutoRepairCount(mock, orgID, prID, models.PullRequestRepairActionTypeFixTests, "head-exhausted", 1)
			},
			wantStatus:   AutoRepairDecisionBudgetExhausted,
			wantPRLinked: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool")
			defer mock.Close()

			orgID := uuid.New()
			sessionID := uuid.New()
			prID := uuid.New()
			now := time.Now()
			tt.setup(mock, orgID, sessionID, prID, now)

			service := &PRService{
				sessions:     db.NewSessionStore(mock),
				pullRequests: db.NewPullRequestStore(mock),
				orgs:         db.NewOrganizationStore(mock),
			}
			decision, err := service.MaybeStartAutoRepair(context.Background(), orgID, sessionID, "session_idle")
			require.NoError(t, err, "MaybeStartAutoRepair should treat cheap no-op cases as handled decisions")
			require.NotNil(t, decision, "MaybeStartAutoRepair should return a decision")
			require.Equal(t, tt.wantStatus, decision.Status, "MaybeStartAutoRepair should return the expected decision status")
			require.Equal(t, tt.wantPRLinked, decision.PullRequestID != nil, "MaybeStartAutoRepair should include a PR ID only after a PR is loaded")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestAutoRepairSessionCanStart(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		status   models.SessionStatus
		expected bool
	}{
		{name: "idle can start", status: models.SessionStatusIdle, expected: true},
		{name: "completed can resume", status: models.SessionStatusCompleted, expected: true},
		{name: "running cannot start", status: models.SessionStatusRunning, expected: false},
		{name: "pending cannot start", status: models.SessionStatusPending, expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual := autoRepairSessionCanStart(tt.status)
			require.Equal(t, tt.expected, actual, "autoRepairSessionCanStart should match idle/resumable session policy")
		})
	}
}

func expectAutoRepairSession(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID, now time.Time, status models.SessionStatus) {
	mock.ExpectQuery("FROM sessions").
		WithArgs(pgx.NamedArgs{"id": sessionID, "org_id": orgID}).
		WillReturnRows(pgxmock.NewRows(sessionColumns).AddRow(newPRHealthSessionRow(sessionID, orgID, now, status)...))
}

func expectAutoRepairPullRequest(mock pgxmock.PgxPoolIface, orgID, sessionID, prID uuid.UUID, now time.Time, headSHA string) {
	row := handlerPRRow(prID, &sessionID, orgID, "org/repo", now)
	setAutoRepairPRRowValue(row, "head_sha", &headSHA)
	mock.ExpectQuery("FROM pull_requests").
		WithArgs(pgx.NamedArgs{"session_id": sessionID, "org_id": orgID}).
		WillReturnRows(pgxmock.NewRows(handlerPRColumns).AddRow(row...))
}

func expectAutoRepairOrg(mock pgxmock.PgxPoolIface, orgID uuid.UUID, settings json.RawMessage, now time.Time) {
	mock.ExpectQuery("FROM organizations").
		WithArgs(pgx.NamedArgs{"id": orgID}).
		WillReturnRows(pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).AddRow(orgID, "Acme", settings, now, now))
}

func setAutoRepairPRRowValue(row []any, column string, value any) {
	for i, col := range handlerPRColumns {
		if col == column {
			row[i] = value
			return
		}
	}
	panic("unknown pull request column: " + column)
}

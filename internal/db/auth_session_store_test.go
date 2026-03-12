package db

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

var sessionColumns = []string{
	"id", "issue_id", "org_id", "agent_type", "status", "autonomy_level", "token_mode",
	"complexity_tier", "confidence_score", "confidence_reasoning", "risk_factors",
	"container_id", "started_at", "completed_at", "token_usage",
	"failure_explanation", "failure_category", "failure_next_steps", "failure_retry_advised",
	"parent_session_id", "revision_context", "error", "result_summary", "diff",
	"pm_plan_id", "pm_approach", "pm_reasoning",
	"project_task_id", "model_override",
	"created_at",
}

func newSessionRow(id, issueID, orgID uuid.UUID, now time.Time) []interface{} {
	return []interface{}{
		id, issueID, orgID, "fixer", "pending", "supervised", "standard",
		nil, nil, nil, []string{},
		nil, nil, nil, json.RawMessage(`{}`),
		nil, nil, []string{}, nil,
		nil, json.RawMessage(`{}`), nil, nil, nil,
		nil, nil, nil,
		nil, // project_task_id
		nil, // model_override
		now,
	}
}

func TestSessionStore_ListByOrg(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	runID1 := uuid.New()
	runID2 := uuid.New()
	issueID := uuid.New()
	now := time.Now()

	tests := []struct {
		name      string
		filters   SessionFilters
		setupMock func(mock pgxmock.PgxPoolIface)
		expected  int
		expectErr bool
	}{
		{
			name:    "returns agent runs for org",
			filters: SessionFilters{},
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE org_id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(sessionColumns).
							AddRow(newSessionRow(runID1, issueID, orgID, now)...).
							AddRow(newSessionRow(runID2, issueID, orgID, now)...),
					)
			},
			expected: 2,
		},
		{
			name:    "returns filtered agent runs by status",
			filters: SessionFilters{Status: models.SessionStatusRunning},
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE org_id .+ AND status").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(sessionColumns).
							AddRow(newSessionRow(runID1, issueID, orgID, now)...),
					)
			},
			expected: 1,
		},
		{
			name:    "returns only ad-hoc runs when AdHocOnly is true",
			filters: SessionFilters{AdHocOnly: true},
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE org_id .+ AND pm_plan_id IS NULL").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(sessionColumns).
							AddRow(newSessionRow(runID1, issueID, orgID, now)...),
					)
			},
			expected: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool")
			defer mock.Close()

			store := NewSessionStore(mock)
			tt.setupMock(mock)

			runs, err := store.ListByOrg(context.Background(), orgID, tt.filters)
			if tt.expectErr {
				require.Error(t, err, "ListByOrg should return an error")
				return
			}
			require.NoError(t, err, "ListByOrg should not return an error")
			require.Len(t, runs, tt.expected, "should return expected number of agent runs")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestSessionStore_GetByID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface, orgID, runID, issueID uuid.UUID, now time.Time)
		expectErr bool
	}{
		{
			name: "returns agent run when found",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, runID, issueID uuid.UUID, now time.Time) {
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(sessionColumns).
							AddRow(newSessionRow(runID, issueID, orgID, now)...),
					)
			},
		},
		{
			name: "returns error when agent run not found",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, runID, issueID uuid.UUID, now time.Time) {
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(sessionColumns))
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool")
			defer mock.Close()

			store := NewSessionStore(mock)
			orgID := uuid.New()
			runID := uuid.New()
			issueID := uuid.New()
			now := time.Now()
			tt.setupMock(mock, orgID, runID, issueID, now)

			run, err := store.GetByID(context.Background(), orgID, runID)
			if tt.expectErr {
				require.Error(t, err, "GetByID should return an error when agent run is not found")
				return
			}
			require.NoError(t, err, "GetByID should not return an error")
			require.Equal(t, runID, run.ID, "should return the correct agent run ID")
			require.Equal(t, issueID, run.IssueID, "should return the correct issue ID")
			require.Equal(t, models.AgentType("fixer"), run.AgentType, "should return the correct agent type")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestSessionStore_ListRecentByOrg(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	runID := uuid.New()
	issueID := uuid.New()
	now := time.Now()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	mock.ExpectQuery("SELECT .+ FROM sessions WHERE org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionColumns).
				AddRow(newSessionRow(runID, issueID, orgID, now)...),
		)

	store := NewSessionStore(mock)
	runs, err := store.ListRecentByOrg(context.Background(), orgID, []string{"completed", "failed"}, 20)
	require.NoError(t, err, "ListRecentByOrg should succeed")
	require.Len(t, runs, 1, "should return expected runs")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionStore_Create(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)
	now := time.Now()
	generatedID := uuid.New()

	run := &models.Session{
		IssueID:       uuid.New(),
		OrgID:         uuid.New(),
		AgentType:     "fixer",
		Status:        "pending",
		AutonomyLevel: "supervised",
		TokenMode:     "standard",
	}

	mock.ExpectQuery("INSERT INTO sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "created_at"}).
				AddRow(generatedID, now),
		)

	err = store.Create(context.Background(), run)
	require.NoError(t, err, "Create should not return an error")
	require.Equal(t, generatedID, run.ID, "should set the generated ID on the agent run")
	require.Equal(t, now, run.CreatedAt, "should set the created_at timestamp on the agent run")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionStore_Create_AllowsNilIssueID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)
	now := time.Now()
	generatedID := uuid.New()

	run := &models.Session{
		IssueID:       uuid.Nil,
		OrgID:         uuid.New(),
		AgentType:     "fixer",
		Status:        "pending",
		AutonomyLevel: "supervised",
		TokenMode:     "standard",
	}

	mock.ExpectQuery("INSERT INTO sessions").
		WithArgs(nil, pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "created_at"}).
				AddRow(generatedID, now),
		)

	err = store.Create(context.Background(), run)
	require.NoError(t, err, "Create should not return an error for nil issue ID")
	require.Equal(t, generatedID, run.ID, "should set the generated ID on the agent run")
	require.Equal(t, now, run.CreatedAt, "should set the created_at timestamp on the agent run")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionStore_UpdateStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		status  string
		queryRE string
	}{
		{
			name:    "sets started_at when transitioning to running",
			status:  "running",
			queryRE: "UPDATE sessions SET status .+ started_at",
		},
		{
			name:    "sets completed_at when transitioning to completed",
			status:  "completed",
			queryRE: "UPDATE sessions SET status .+ completed_at",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool")
			defer mock.Close()

			store := NewSessionStore(mock)
			orgID := uuid.New()
			runID := uuid.New()

			mock.ExpectExec(tt.queryRE).
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnResult(pgxmock.NewResult("UPDATE", 1))

			err = store.UpdateStatus(context.Background(), orgID, runID, tt.status)
			require.NoError(t, err, "UpdateStatus should not return an error")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestSessionStore_ListByIssue(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)
	orgID := uuid.New()
	issueID := uuid.New()
	runID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM sessions WHERE org_id .+ AND issue_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionColumns).
				AddRow(newSessionRow(runID, issueID, orgID, now)...),
		)

	runs, err := store.ListByIssue(context.Background(), orgID, issueID)
	require.NoError(t, err, "ListByIssue should not return an error")
	require.Len(t, runs, 1, "should return the agent run for the issue")
	require.Equal(t, runID, runs[0].ID, "should return the correct agent run ID")
	require.Equal(t, issueID, runs[0].IssueID, "should return the correct issue ID")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

// TestSessionSelectColumns_MatchesModel is a regression test ensuring that
// sessionSelectColumns includes every column expected by the Session model struct.
// Without this, adding a new field to models.Session but forgetting to add it to
// sessionSelectColumns causes GetByID/List queries to fail at runtime (the bug
// that caused "failed to load session details" for manual sessions).
func TestSessionSelectColumns_MatchesModel(t *testing.T) {
	t.Parallel()

	// Extract column names from the Session struct's db tags.
	typ := reflect.TypeOf(models.Session{})
	structCols := make(map[string]bool)
	for i := range typ.NumField() {
		tag := typ.Field(i).Tag.Get("db")
		if tag != "" && tag != "-" {
			structCols[tag] = true
		}
	}

	// Parse column names out of sessionSelectColumns.
	// Handles plain columns ("org_id") and aliased expressions ("COALESCE(...) AS issue_id").
	selectCols := make(map[string]bool)
	for _, part := range strings.Split(sessionSelectColumns, ",") {
		col := strings.TrimSpace(part)
		if col == "" {
			continue
		}
		// If there's an "AS alias", use the alias.
		if idx := strings.LastIndex(strings.ToUpper(col), " AS "); idx != -1 {
			col = strings.TrimSpace(col[idx+4:])
		} else if strings.Contains(col, "(") {
			// Expression without an alias (e.g. COALESCE(...)) — skip, the alias form is required.
			continue
		}
		selectCols[col] = true
	}

	for col := range structCols {
		require.Contains(t, selectCols, col,
			"sessionSelectColumns is missing column %q from models.Session — add it to the SELECT constant", col)
	}
}

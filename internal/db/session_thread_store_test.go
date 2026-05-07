package db

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

var sessionThreadTestColumns = []string{
	"id", "session_id", "org_id", "agent_type", "model_override",
	"label", "instructions", "file_scope", "status", "agent_session_id",
	"current_turn", "last_activity_at",
	"confidence_score", "result_summary", "diff", "failure_explanation", "failure_category",
	"started_at", "completed_at", "created_at",
	"archived_at", "base_snapshot_key", "cost_cents", "pending_message_count", "cancel_requested_at",
}

func newSessionThreadRow(threadID, sessionID, orgID uuid.UUID, label string, now time.Time) []interface{} {
	return []interface{}{
		threadID, sessionID, orgID, "claude_code", nil,
		label, nil, nil, "pending", nil,
		0, nil,
		nil, nil, nil, nil, nil,
		nil, nil, now,
		nil, nil, float64(0), 0, nil,
	}
}

func TestSessionThreadStore_Create(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionThreadStore(mock)
	threadID := uuid.New()
	sessionID := uuid.New()
	orgID := uuid.New()
	now := time.Now()

	// Create has 9 named args: session_id, org_id, agent_type, model_override, label, instructions, file_scope, status, max_threads
	mock.ExpectQuery("INSERT INTO session_threads").
		WithArgs(anyArgs(9)...).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(threadID, now))

	thread := &models.SessionThread{
		SessionID: sessionID,
		OrgID:     orgID,
		AgentType: models.AgentTypeClaudeCode,
		Label:     "Backend API",
		Status:    models.ThreadStatusPending,
	}

	err = store.Create(context.Background(), thread, 4)
	require.NoError(t, err, "Create should not return an error")
	require.Equal(t, threadID, thread.ID, "should set the thread ID from RETURNING clause")
	require.Equal(t, now, thread.CreatedAt, "should set the created_at from RETURNING clause")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionThreadStore_GetByID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface, orgID, threadID uuid.UUID)
		expectErr bool
	}{
		{
			name: "returns thread when found",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, threadID uuid.UUID) {
				sessionID := uuid.New()
				now := time.Now()
				// GetByID has 2 named args: id, org_id
				mock.ExpectQuery("SELECT .+ FROM session_threads WHERE id .+ AND org_id .+ archived_at IS NULL").
					WithArgs(anyArgs(2)...).
					WillReturnRows(
						pgxmock.NewRows(sessionThreadTestColumns).
							AddRow(newSessionThreadRow(threadID, sessionID, orgID, "Backend", now)...),
					)
			},
			expectErr: false,
		},
		{
			name: "returns error when not found",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, threadID uuid.UUID) {
				mock.ExpectQuery("SELECT .+ FROM session_threads WHERE id .+ AND org_id .+ archived_at IS NULL").
					WithArgs(anyArgs(2)...).
					WillReturnRows(pgxmock.NewRows(sessionThreadTestColumns))
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

			store := NewSessionThreadStore(mock)
			orgID := uuid.New()
			threadID := uuid.New()
			tt.setupMock(mock, orgID, threadID)

			thread, err := store.GetByID(context.Background(), orgID, threadID)
			if tt.expectErr {
				require.Error(t, err, "GetByID should return an error")
				return
			}
			require.NoError(t, err, "GetByID should not return an error")
			require.Equal(t, threadID, thread.ID, "should return the correct thread ID")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestSessionThreadStore_ListBySession(t *testing.T) {
	t.Parallel()

	threadID1 := uuid.New()
	threadID2 := uuid.New()
	now := time.Now().Truncate(time.Microsecond)

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID)
		expected  []models.SessionThread
		expectErr bool
	}{
		{
			name: "returns threads for session",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID) {
				// ListBySession has 2 named args: org_id, session_id
				mock.ExpectQuery("SELECT .+ FROM session_threads WHERE org_id .+ AND session_id .+ archived_at IS NULL").
					WithArgs(anyArgs(2)...).
					WillReturnRows(
						pgxmock.NewRows(sessionThreadTestColumns).
							AddRow(newSessionThreadRow(threadID1, sessionID, orgID, "Backend", now)...).
							AddRow(newSessionThreadRow(threadID2, sessionID, orgID, "Frontend", now)...),
					)
			},
			expected: nil, // set dynamically per-test since orgID/sessionID vary
		},
		{
			name: "returns empty for session with no threads",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID) {
				mock.ExpectQuery("SELECT .+ FROM session_threads WHERE org_id .+ AND session_id .+ archived_at IS NULL").
					WithArgs(anyArgs(2)...).
					WillReturnRows(pgxmock.NewRows(sessionThreadTestColumns))
			},
			expected: nil,
		},
		{
			name: "returns error on db failure",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID) {
				mock.ExpectQuery("SELECT .+ FROM session_threads WHERE org_id .+ AND session_id .+ archived_at IS NULL").
					WithArgs(anyArgs(2)...).
					WillReturnError(fmt.Errorf("connection refused"))
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

			store := NewSessionThreadStore(mock)
			orgID := uuid.New()
			sessionID := uuid.New()
			tt.setupMock(mock, orgID, sessionID)

			threads, err := store.ListBySession(context.Background(), orgID, sessionID)
			if tt.expectErr {
				require.Error(t, err, "ListBySession should return an error")
				return
			}
			require.NoError(t, err, "ListBySession should not return an error")

			// Build expected values based on test case name.
			switch tt.name {
			case "returns threads for session":
				expected := []models.SessionThread{
					{ID: threadID1, SessionID: sessionID, OrgID: orgID, AgentType: "claude_code", Label: "Backend", Status: "pending", CreatedAt: now},
					{ID: threadID2, SessionID: sessionID, OrgID: orgID, AgentType: "claude_code", Label: "Frontend", Status: "pending", CreatedAt: now},
				}
				require.Equal(t, expected, threads, "should return the expected threads for session")
			case "returns empty for session with no threads":
				require.Empty(t, threads, "should return empty slice for session with no threads")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestSessionThreadStore_ListStuckRunningThreads(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionThreadStore(mock)
	threadID1 := uuid.New()
	threadID2 := uuid.New()
	sessionID := uuid.New()
	orgID := uuid.New()
	now := time.Now().Truncate(time.Microsecond)
	startedAt := now.Add(-3 * time.Hour)

	// Build a 'running' row variant of the standard test row.
	row := func(id uuid.UUID) []interface{} {
		base := newSessionThreadRow(id, sessionID, orgID, "thread", now)
		base[8] = "running"   // status
		base[17] = &startedAt // started_at
		return base
	}

	// Predicate must filter status='running', non-null started_at, and the cutoff.
	mock.ExpectQuery("SELECT .+ FROM session_threads\\s+WHERE status = 'running'\\s+AND started_at IS NOT NULL\\s+AND started_at < @started_before").
		WithArgs(anyArgs(1)...).
		WillReturnRows(
			pgxmock.NewRows(sessionThreadTestColumns).
				AddRow(row(threadID1)...).
				AddRow(row(threadID2)...),
		)

	threads, err := store.ListStuckRunningThreads(context.Background(), now.Add(-time.Hour))
	require.NoError(t, err, "ListStuckRunningThreads should not return an error")
	require.Len(t, threads, 2, "should return both stuck threads")
	require.Equal(t, threadID1, threads[0].ID)
	require.Equal(t, threadID2, threads[1].ID)
	require.Equal(t, models.ThreadStatusRunning, threads[0].Status, "row mapper should hydrate status")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionThreadStore_ListStuckRunningThreads_QueryError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionThreadStore(mock)
	mock.ExpectQuery("SELECT .+ FROM session_threads\\s+WHERE status = 'running'").
		WithArgs(anyArgs(1)...).
		WillReturnError(fmt.Errorf("connection refused"))

	_, err = store.ListStuckRunningThreads(context.Background(), time.Now())
	require.Error(t, err, "ListStuckRunningThreads should propagate query errors")
}

func TestSessionThreadStore_CountBySession(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionThreadStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()

	// CountBySession has 2 named args: org_id, session_id
	mock.ExpectQuery("SELECT count\\(\\*\\) FROM session_threads WHERE org_id .+ AND session_id .+ archived_at IS NULL").
		WithArgs(anyArgs(2)...).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(3))

	count, err := store.CountBySession(context.Background(), orgID, sessionID)
	require.NoError(t, err, "CountBySession should not return an error")
	require.Equal(t, 3, count, "should return the correct thread count")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionThreadStore_Archive(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionThreadStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	now := time.Now()

	mock.ExpectQuery(`WITH visible_threads AS[\s\S]*FOR UPDATE[\s\S]*UPDATE session_threads[\s\S]*SET archived_at = now\(\)[\s\S]*WHERE id = @id[\s\S]*session_id = @session_id[\s\S]*org_id = @org_id[\s\S]*archived_at IS NULL[\s\S]*RETURNING`).
		WithArgs(anyArgs(3)...).
		WillReturnRows(
			pgxmock.NewRows(sessionThreadTestColumns).
				AddRow(
					threadID, sessionID, orgID, "claude_code", nil,
					"Review", nil, nil, "completed", nil,
					1, nil,
					nil, nil, nil, nil, nil,
					nil, nil, now,
					&now, nil, float64(0), 0, nil,
				),
		)

	thread, err := store.Archive(context.Background(), orgID, sessionID, threadID)
	require.NoError(t, err, "Archive should not return an error")
	require.Equal(t, threadID, thread.ID, "Archive should return the archived thread")
	require.NotNil(t, thread.ArchivedAt, "Archive should return the archived timestamp")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionThreadStore_UpdateStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		status    models.ThreadStatus
		expectSQL string
	}{
		{
			name:      "running sets started_at",
			status:    models.ThreadStatusRunning,
			expectSQL: "UPDATE session_threads SET status .+ started_at .+ cancel_requested_at = NULL",
		},
		{
			name:      "completed sets completed_at",
			status:    models.ThreadStatusCompleted,
			expectSQL: "UPDATE session_threads SET status .+ completed_at",
		},
		{
			name:      "idle only sets status",
			status:    models.ThreadStatusIdle,
			expectSQL: "UPDATE session_threads SET status",
		},
		{
			name:      "returns error when no rows affected",
			status:    models.ThreadStatusIdle,
			expectSQL: "UPDATE session_threads SET status",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool")
			defer mock.Close()

			store := NewSessionThreadStore(mock)
			orgID := uuid.New()
			threadID := uuid.New()

			rowsAffected := int64(1)
			if tt.name == "returns error when no rows affected" {
				rowsAffected = 0
			}

			// UpdateStatus has 3 named args: id, org_id, status
			mock.ExpectExec(tt.expectSQL).
				WithArgs(anyArgs(3)...).
				WillReturnResult(pgxmock.NewResult("UPDATE", rowsAffected))

			err = store.UpdateStatus(context.Background(), orgID, threadID, tt.status)
			if rowsAffected == 0 {
				require.Error(t, err, "UpdateStatus should return an error when no rows affected")
			} else {
				require.NoError(t, err, "UpdateStatus should not return an error")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestSessionThreadStore_UpdateEditable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface, orgID, sessionID, threadID uuid.UUID, now time.Time, model string)
		expectErr error
	}{
		{
			name: "updates blank idle threads",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID, threadID uuid.UUID, now time.Time, model string) {
				mock.ExpectQuery(`UPDATE session_threads[\s\S]*WHERE id = @id[\s\S]*org_id = @org_id[\s\S]*session_id = @session_id[\s\S]*status = 'idle'[\s\S]*current_turn = 0[\s\S]*RETURNING`).
					WithArgs(anyArgs(6)...).
					WillReturnRows(
						pgxmock.NewRows(sessionThreadTestColumns).
							AddRow(
								threadID, sessionID, orgID, "codex", &model,
								"Codex 2", nil, nil, "idle", nil,
								0, nil,
								nil, nil, nil, nil, nil,
								nil, nil, now,
								nil, nil, float64(0), 0, nil,
							),
					)
			},
		},
		{
			name: "returns no rows when thread is no longer editable",
			setupMock: func(mock pgxmock.PgxPoolIface, _ uuid.UUID, _ uuid.UUID, _ uuid.UUID, _ time.Time, _ string) {
				mock.ExpectQuery(`UPDATE session_threads[\s\S]*WHERE id = @id[\s\S]*org_id = @org_id[\s\S]*session_id = @session_id[\s\S]*status = 'idle'[\s\S]*current_turn = 0[\s\S]*RETURNING`).
					WithArgs(anyArgs(6)...).
					WillReturnRows(pgxmock.NewRows(sessionThreadTestColumns))
			},
			expectErr: pgx.ErrNoRows,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool")
			defer mock.Close()

			store := NewSessionThreadStore(mock)
			orgID := uuid.New()
			sessionID := uuid.New()
			threadID := uuid.New()
			now := time.Now()
			model := models.CodexModelGPT54

			tt.setupMock(mock, orgID, sessionID, threadID, now, model)

			thread := &models.SessionThread{
				ID:            threadID,
				SessionID:     sessionID,
				OrgID:         orgID,
				AgentType:     models.AgentTypeCodex,
				ModelOverride: &model,
				Label:         "Codex 2",
				Status:        models.ThreadStatusIdle,
			}

			err = store.UpdateEditable(context.Background(), thread)
			if tt.expectErr != nil {
				require.ErrorIs(t, err, tt.expectErr, "UpdateEditable should surface the guarded-write miss")
				require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
				return
			}

			require.NoError(t, err, "UpdateEditable should not return an error")
			require.Equal(t, models.AgentTypeCodex, thread.AgentType, "UpdateEditable should preserve the updated agent type")
			require.Equal(t, "Codex 2", thread.Label, "UpdateEditable should preserve the updated label")
			require.NotNil(t, thread.ModelOverride, "UpdateEditable should preserve the updated model override")
			require.Equal(t, model, *thread.ModelOverride, "UpdateEditable should preserve the updated model override")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestSessionThreadStore_ClaimIdleForSessionClearsCancelRequestedAt(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionThreadStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("UPDATE session_threads[\\s\\S]*SET status = 'running',[\\s\\S]*cancel_requested_at = NULL").
		WithArgs(anyArgs(5)...).
		WillReturnRows(
			pgxmock.NewRows(sessionThreadTestColumns).
				AddRow(newSessionThreadRow(threadID, sessionID, orgID, "Backend", now)...),
		)

	thread, err := store.ClaimIdleForSession(context.Background(), orgID, sessionID, threadID, models.MaxRunningThreadsPerSession)
	require.NoError(t, err, "ClaimIdleForSession should not return an error for an eligible thread")
	require.Equal(t, threadID, thread.ID, "ClaimIdleForSession should return the claimed thread")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionThreadStore_ClaimIdle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface, orgID, threadID uuid.UUID)
		expectErr bool
	}{
		{
			name: "claims idle thread successfully",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, threadID uuid.UUID) {
				sessionID := uuid.New()
				now := time.Now()
				row := newSessionThreadRow(threadID, sessionID, orgID, "Backend", now)
				row[8] = "running" // status after claim
				// ClaimIdle has 2 named args: id, org_id
				mock.ExpectQuery("UPDATE session_threads").
					WithArgs(anyArgs(2)...).
					WillReturnRows(pgxmock.NewRows(sessionThreadTestColumns).AddRow(row...))
			},
			expectErr: false,
		},
		{
			name: "returns error when thread is not idle",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, threadID uuid.UUID) {
				mock.ExpectQuery("UPDATE session_threads").
					WithArgs(anyArgs(2)...).
					WillReturnRows(pgxmock.NewRows(sessionThreadTestColumns))
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

			store := NewSessionThreadStore(mock)
			orgID := uuid.New()
			threadID := uuid.New()
			tt.setupMock(mock, orgID, threadID)

			thread, err := store.ClaimIdle(context.Background(), orgID, threadID)
			if tt.expectErr {
				require.Error(t, err, "ClaimIdle should return an error when thread is not idle")
				return
			}
			require.NoError(t, err, "ClaimIdle should not return an error")
			require.Equal(t, threadID, thread.ID, "should return the claimed thread")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestSessionThreadStore_ClaimIdleForSessionRejectsAtLimit(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionThreadStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()

	// First query: the CTE-based UPDATE finds no eligible row because the
	// session already has MaxRunningThreadsPerSession siblings active. Returns
	// no rows. Carries 5 named args: id, org_id, session_id, max_running,
	// claimable_statuses.
	mock.ExpectQuery("UPDATE session_threads").
		WithArgs(anyArgs(5)...).
		WillReturnRows(pgxmock.NewRows(sessionThreadTestColumns))

	// Second query: isAtRunningLimit re-inspects state outside the FOR UPDATE
	// lock. The target is still idle, sibling_active equals max_running, so
	// the store maps the empty result to ErrThreadRunningLimitReached.
	mock.ExpectQuery(`SELECT\s+COALESCE`).
		WithArgs(anyArgs(3)...).
		WillReturnRows(
			pgxmock.NewRows([]string{"target_status", "sibling_active"}).
				AddRow(string(models.ThreadStatusIdle), models.MaxRunningThreadsPerSession),
		)

	_, err = store.ClaimIdleForSession(context.Background(), orgID, sessionID, threadID, models.MaxRunningThreadsPerSession)
	require.ErrorIs(t, err, ErrThreadRunningLimitReached, "ClaimIdleForSession should surface the running-limit sentinel when at cap")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionThreadStore_ClaimIdleForSessionLocksSiblings(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionThreadStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	now := time.Now()
	row := newSessionThreadRow(threadID, sessionID, orgID, "Backend", now)
	row[8] = models.ThreadStatusRunning
	row[11] = &now
	row[17] = &now

	// Pin the guard explicitly: the CTE locks every session_threads row,
	// requires target.status to match the claimable-statuses list, counts
	// active siblings, and admits only when the cap is not yet reached. A
	// regression that drops the status check or reverses the cap inequality
	// must not pass.
	mock.ExpectQuery(`(?s)WITH locked_threads AS.*WHERE org_id = @org_id AND session_id = @session_id AND archived_at IS NULL.*FOR UPDATE.*target_claimable.*status\s*=\s*ANY\(@claimable_statuses\).*running_count.*id\s*<>\s*@id.*'pending',\s*'running',\s*'awaiting_input'.*eligible.*running_count\.n\s*<\s*@max_running.*UPDATE session_threads\s+SET status = 'running'.*archived_at IS NULL.*EXISTS\s*\(\s*SELECT 1 FROM eligible\s*\).*RETURNING`).
		WithArgs(anyArgs(5)...).
		WillReturnRows(pgxmock.NewRows(sessionThreadTestColumns).AddRow(row...))

	thread, err := store.ClaimIdleForSession(context.Background(), orgID, sessionID, threadID, models.MaxRunningThreadsPerSession)
	require.NoError(t, err, "ClaimIdleForSession should claim an eligible idle thread")
	require.Equal(t, threadID, thread.ID, "ClaimIdleForSession should return the claimed thread")
	require.Equal(t, models.ThreadStatusRunning, thread.Status, "ClaimIdleForSession should return the running status")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionThreadStore_ClaimForResumeInSession(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionThreadStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	now := time.Now()
	row := newSessionThreadRow(threadID, sessionID, orgID, "Backend", now)
	row[8] = models.ThreadStatusRunning
	row[11] = &now
	row[17] = &now

	// Pin the resume claim's distinguishing properties: it should clear
	// completed_at (so the row reflects the new in-flight turn rather than
	// the previous terminal state), preserve started_at (the original
	// thread start time stays meaningful), and only fire when status is in
	// the resumable set. The 5 named args mirror ClaimIdleForSession;
	// claimable_statuses carries models.ResumableThreadStatuses.
	mock.ExpectQuery(`(?s)WITH locked_threads AS.*WHERE org_id = @org_id AND session_id = @session_id AND archived_at IS NULL.*FOR UPDATE.*target_claimable.*status\s*=\s*ANY\(@claimable_statuses\).*UPDATE session_threads\s+SET status = 'running',\s+completed_at = NULL,\s+last_activity_at = now\(\),\s+cancel_requested_at = NULL.*archived_at IS NULL.*EXISTS\s*\(\s*SELECT 1 FROM eligible\s*\).*RETURNING`).
		WithArgs(anyArgs(5)...).
		WillReturnRows(pgxmock.NewRows(sessionThreadTestColumns).AddRow(row...))

	thread, err := store.ClaimForResumeInSession(context.Background(), orgID, sessionID, threadID, models.MaxRunningThreadsPerSession)
	require.NoError(t, err, "ClaimForResumeInSession should resume a thread in a resumable terminal status")
	require.Equal(t, threadID, thread.ID, "ClaimForResumeInSession should return the resumed thread")
	require.Equal(t, models.ThreadStatusRunning, thread.Status, "ClaimForResumeInSession should leave the thread in running status")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionThreadStore_ClaimForResumeInSessionRejectsAtLimit(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionThreadStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()

	// First query: the resume CTE finds no eligible row because the
	// per-session running cap is full.
	mock.ExpectQuery("UPDATE session_threads").
		WithArgs(anyArgs(5)...).
		WillReturnRows(pgxmock.NewRows(sessionThreadTestColumns))

	// Second query: isAtRunningLimit re-inspects without the FOR UPDATE
	// lock. Target is in a resumable status (completed) and sibling_active
	// equals max_running, so the store maps the empty result to
	// ErrThreadRunningLimitReached — same sentinel ClaimIdleForSession
	// surfaces, so the service can collapse both into a single "queue
	// against still-unclaimed thread" branch.
	mock.ExpectQuery(`SELECT\s+COALESCE`).
		WithArgs(anyArgs(3)...).
		WillReturnRows(
			pgxmock.NewRows([]string{"target_status", "sibling_active"}).
				AddRow(string(models.ThreadStatusCompleted), models.MaxRunningThreadsPerSession),
		)

	_, err = store.ClaimForResumeInSession(context.Background(), orgID, sessionID, threadID, models.MaxRunningThreadsPerSession)
	require.ErrorIs(t, err, ErrThreadRunningLimitReached, "ClaimForResumeInSession should surface the running-limit sentinel when at cap")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionThreadStore_UpdateResult(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		status       models.ThreadStatus
		rowsAffected int64
		expectErr    bool
	}{
		{
			name:         "success with completed status",
			status:       models.ThreadStatusCompleted,
			rowsAffected: 1,
		},
		{
			name:         "success with failed status",
			status:       models.ThreadStatusFailed,
			rowsAffected: 1,
		},
		{
			name:         "thread not found",
			status:       models.ThreadStatusCompleted,
			rowsAffected: 0,
			expectErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool")
			defer mock.Close()

			store := NewSessionThreadStore(mock)
			orgID := uuid.New()
			threadID := uuid.New()

			summary := "done"
			diff := "some diff"
			score := 0.95
			failErr := "something went wrong"
			failCat := "runtime"
			result := &models.SessionResult{
				ConfidenceScore: &score,
				ResultSummary:   &summary,
				Diff:            &diff,
				Error:           &failErr,
				FailureCategory: &failCat,
			}

			// UpdateResult has 8 named args: id, org_id, status, confidence_score, result_summary, diff, failure_explanation, failure_category
			mock.ExpectExec("UPDATE session_threads").
				WithArgs(anyArgs(8)...).
				WillReturnResult(pgxmock.NewResult("UPDATE", tt.rowsAffected))

			err = store.UpdateResult(context.Background(), orgID, threadID, tt.status, result)
			if tt.expectErr {
				require.Error(t, err, "UpdateResult should return an error when no rows affected")
			} else {
				require.NoError(t, err, "UpdateResult should not return an error")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestSessionThreadStore_UpdateResult_NilError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionThreadStore(mock)
	orgID := uuid.New()
	threadID := uuid.New()

	summary := "done"
	result := &models.SessionResult{
		ResultSummary: &summary,
	}

	mock.ExpectExec("UPDATE session_threads").
		WithArgs(anyArgs(8)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.UpdateResult(context.Background(), orgID, threadID, models.ThreadStatusCompleted, result)
	require.NoError(t, err, "UpdateResult should not return an error when Error is nil")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionThreadStore_UpdateTurnComplete(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		rowsAffected int64
		expectErr    bool
	}{
		{
			name:         "success",
			rowsAffected: 1,
		},
		{
			name:         "thread not found",
			rowsAffected: 0,
			expectErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool")
			defer mock.Close()

			store := NewSessionThreadStore(mock)
			orgID := uuid.New()
			threadID := uuid.New()

			summary := "turn done"
			diff := "some diff"
			score := 0.8
			result := &models.SessionResult{
				ConfidenceScore: &score,
				ResultSummary:   &summary,
				Diff:            &diff,
			}

			// UpdateTurnComplete has 7 named args: id, org_id, current_turn, agent_session_id, confidence_score, result_summary, diff
			mock.ExpectExec("UPDATE session_threads").
				WithArgs(anyArgs(7)...).
				WillReturnResult(pgxmock.NewResult("UPDATE", tt.rowsAffected))

			err = store.UpdateTurnComplete(context.Background(), orgID, threadID, 2, result, "sess-123")
			if tt.expectErr {
				require.Error(t, err, "UpdateTurnComplete should return an error when no rows affected")
			} else {
				require.NoError(t, err, "UpdateTurnComplete should not return an error")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

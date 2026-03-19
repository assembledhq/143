package db

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
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
}

func newSessionThreadRow(threadID, sessionID, orgID uuid.UUID, label string, now time.Time) []interface{} {
	return []interface{}{
		threadID, sessionID, orgID, "claude_code", nil,
		label, nil, nil, "pending", nil,
		0, nil,
		nil, nil, nil, nil, nil,
		nil, nil, now,
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
				mock.ExpectQuery("SELECT .+ FROM session_threads WHERE id .+ AND org_id").
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
				mock.ExpectQuery("SELECT .+ FROM session_threads WHERE id .+ AND org_id").
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
				mock.ExpectQuery("SELECT .+ FROM session_threads WHERE org_id .+ AND session_id").
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
				mock.ExpectQuery("SELECT .+ FROM session_threads WHERE org_id .+ AND session_id").
					WithArgs(anyArgs(2)...).
					WillReturnRows(pgxmock.NewRows(sessionThreadTestColumns))
			},
			expected: nil,
		},
		{
			name: "returns error on db failure",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID) {
				mock.ExpectQuery("SELECT .+ FROM session_threads WHERE org_id .+ AND session_id").
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

func TestSessionThreadStore_CountBySession(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionThreadStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()

	// CountBySession has 2 named args: org_id, session_id
	mock.ExpectQuery("SELECT count\\(\\*\\) FROM session_threads WHERE org_id .+ AND session_id").
		WithArgs(anyArgs(2)...).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(3))

	count, err := store.CountBySession(context.Background(), orgID, sessionID)
	require.NoError(t, err, "CountBySession should not return an error")
	require.Equal(t, 3, count, "should return the correct thread count")
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
			expectSQL: "UPDATE session_threads SET status .+ started_at",
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

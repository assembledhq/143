package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestCodeReviewHistoryAuthorizeSource(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		allowed  bool
		queryErr error
		wantErr  bool
	}{
		{"bound active orchestrator", true, nil, false},
		{"unbound or stale thread", false, nil, true},
		{"database unavailable", false, errors.New("unavailable"), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create isolated query mock")
			t.Cleanup(mock.Close)
			orgID, repoID, sessionID, threadID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			query := mock.ExpectQuery(`(?s)SELECT EXISTS.*m.org_id = @org_id AND m.repository_id = @repository_id AND m.session_id = @session_id.*m.status IN.*m.stale = false.*t.id = @thread_id.*p.role = 'orchestrator'.*review_history_thread_id`).
				WithArgs(pgx.NamedArgs{"org_id": orgID, "repository_id": repoID, "session_id": sessionID, "thread_id": threadID, "thread_id_text": threadID.String()})
			if tt.queryErr != nil {
				query.WillReturnError(tt.queryErr)
			} else {
				query.WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(tt.allowed))
			}
			err = NewCodeReviewHistoryStore(mock).AuthorizeSource(context.Background(), orgID, repoID, sessionID, threadID)
			if tt.wantErr {
				require.Error(t, err, "unauthorized or unavailable history must fail closed")
			} else {
				require.NoError(t, err, "the bound current orchestrator may read prior rounds")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "authorization must bind the tenant, repository, session, thread and trusted prompt record")
		})
	}
}

func TestCodeReviewHistoryListPriorRounds(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		withCursor bool
		fail       bool
	}{
		{"first page", false, false}, {"older page", true, false}, {"database failure", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create isolated query mock")
			t.Cleanup(mock.Close)
			orgID, repoID, currentID, priorID, policyID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
			var cursor *uuid.UUID
			if tt.withCursor {
				id := uuid.New()
				cursor = &id
			}
			now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			decision := models.CodeReviewDecisionBlocked
			completed := now.Add(time.Minute)
			query := mock.ExpectQuery(`(?s)prior.org_id = current.org_id.*prior.repository_id = current.repository_id AND prior.pull_request_id = current.pull_request_id.*current.org_id = @org_id.*prior.session_id <> current.session_id.*prior.created_at < source.created_at.*prior.completed_at < source.created_at.*c.org_id = current.org_id.*c.pull_request_id = current.pull_request_id.*ORDER BY prior.created_at DESC, prior.session_id DESC LIMIT @limit`).
				WithArgs(pgx.NamedArgs{"org_id": orgID, "repository_id": repoID, "current_session_id": currentID, "cursor": cursor, "limit": 11})
			if tt.fail {
				query.WillReturnError(errors.New("unavailable"))
			} else {
				query.WillReturnRows(pgxmock.NewRows([]string{"session_id", "assessment_id", "base_sha", "head_sha", "policy_id", "status", "decision", "created_at", "completed_at"}).AddRow(priorID, nil, "base", "head", policyID, "completed", &decision, now, &completed))
			}
			actual, err := NewCodeReviewStore(mock).ListPriorRounds(context.Background(), orgID, repoID, currentID, cursor, 11)
			if tt.fail {
				require.Error(t, err, "query errors should propagate")
			} else {
				require.NoError(t, err, "listing scoped prior rounds should succeed")
				require.Equal(t, []CodeReviewHistoryRound{{SessionID: priorID, BaseSHA: "base", HeadSHA: "head", PolicyID: policyID, Status: models.CodeReviewSessionStatusCompleted, Decision: &decision, CreatedAt: now, CompletedAt: &completed}}, actual, "the compact index should preserve exact prior round identities")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "all scopes and chronological bounds should be applied")
		})
	}
}

func TestCodeReviewHistoryListPriorMessages(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		cursor int64
		offset int
	}{{"first page", 0, 0}, {"remaining chunk", 7, 4000}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create isolated query mock")
			t.Cleanup(mock.Close)
			orgID, sessionID, threadID := uuid.New(), uuid.New(), uuid.New()
			cutoff := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
			created := cutoff.Add(-time.Hour)
			mock.ExpectQuery(`(?s)substring\(m.content.*FOR 4000.*t.org_id = m.org_id.*t.session_id = m.session_id.*m.org_id = @org_id AND m.session_id = @session_id AND m.thread_id = @thread_id.*m.created_at < @cutoff.*m.id > @cursor_id.*ORDER BY m.id LIMIT @limit`).
				WithArgs(pgx.NamedArgs{"org_id": orgID, "session_id": sessionID, "thread_id": threadID, "cutoff": cutoff, "cursor_id": tt.cursor, "offset": tt.offset, "limit": 11}).
				WillReturnRows(pgxmock.NewRows([]string{"id", "role", "turn_number", "content", "content_runes", "created_at"}).AddRow(int64(7), "assistant", 1, "remaining evidence", 4018, created))
			actual, err := NewCodeReviewHistoryStore(mock).ListPriorMessages(context.Background(), orgID, sessionID, threadID, cutoff, tt.cursor, tt.offset, 11)
			require.NoError(t, err, "bounded message retrieval should succeed")
			require.Equal(t, []CodeReviewHistoryMessage{{ID: 7, Role: models.MessageRoleAssistant, TurnNumber: 1, Content: "remaining evidence", ContentRunes: 4018, CreatedAt: created}}, actual, "chunk metadata should retain the complete message length")
			require.NoError(t, mock.ExpectationsWereMet(), "messages must stay bound to tenant, session, thread and source cutoff")
		})
	}
}

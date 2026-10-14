package db

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestCodeReviewStore_DeleteUnpostedFindingsScopesCleanup(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		keys      []string
		queryErr  error
		expectErr bool
	}{
		{name: "only requested unposted rows in the current tenant session and agent result", keys: []string{"old-key"}},
		{name: "no superseded keys makes no database call"},
		{name: "database failure is returned", keys: []string{"old-key"}, queryErr: errors.New("cleanup unavailable"), expectErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "cleanup should have an isolated database mock")
			defer mock.Close()
			orgID, sessionID, resultID := uuid.New(), uuid.New(), uuid.New()
			if len(tt.keys) > 0 {
				expectation := mock.ExpectExec(`(?s)DELETE FROM code_review_findings\s+WHERE org_id = @org_id\s+AND session_id = @session_id\s+AND agent_result_id = @agent_result_id\s+AND dedupe_key = ANY\(@dedupe_keys\)\s+AND github_comment_id IS NULL`).
					WithArgs(orgID, sessionID, resultID, tt.keys)
				if tt.queryErr != nil {
					expectation.WillReturnError(tt.queryErr)
				} else {
					expectation.WillReturnResult(pgxmock.NewResult("DELETE", 1))
				}
			}
			err = NewCodeReviewStore(mock).DeleteUnpostedFindings(context.Background(), orgID, sessionID, resultID, tt.keys)
			if tt.expectErr {
				require.ErrorIs(t, err, tt.queryErr, "cleanup errors should prevent the caller from publishing an unreconciled result")
			} else {
				require.NoError(t, err, "cleanup should remove only the scoped superseded rows")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "cleanup must filter by tenant, session, result, requested keys, and publication receipt")
		})
	}
}

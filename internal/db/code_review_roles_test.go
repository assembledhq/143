package db

import (
	"context"
	"errors"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestCodeReviewStoreResolveAgentRoleForThread(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		roles     []string
		queryErr  error
		wantRole  models.CodeReviewAgentRole
		wantFound bool
		wantErr   bool
	}{
		{name: "reviewer", roles: []string{"reviewer"}, wantRole: models.CodeReviewAgentRoleReviewer, wantFound: true},
		{name: "Main synthesis", roles: []string{"orchestrator"}, wantRole: models.CodeReviewAgentRoleOrchestrator, wantFound: true},
		{name: "no persisted role", roles: nil},
		{name: "ambiguous role", roles: []string{"reviewer", "orchestrator"}},
		{name: "invalid role", roles: []string{"admin"}, wantErr: true},
		{name: "database failure", queryErr: errors.New("database unavailable"), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID, sessionID, threadID := uuid.New(), uuid.New(), uuid.New()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "role query mock should initialize")
			defer mock.Close()
			expected := mock.ExpectQuery("(?s)FROM code_review_agent_results r.*JOIN code_review_session_metadata m.*r.org_id = @org_id.*r.session_id = @session_id.*structured_result->>'thread_id' = @thread_id.*m.stale = false.*m.status IN")
			expected.WithArgs(pgx.NamedArgs{"org_id": orgID, "session_id": sessionID, "thread_id": threadID.String()})
			if tt.queryErr != nil {
				expected.WillReturnError(tt.queryErr)
			} else {
				rows := pgxmock.NewRows([]string{"role"})
				for _, role := range tt.roles {
					rows.AddRow(role)
				}
				expected.WillReturnRows(rows)
			}
			role, found, err := NewCodeReviewStore(mock).ResolveAgentRoleForThread(context.Background(), orgID, sessionID, threadID)
			if tt.wantErr {
				require.Error(t, err, "invalid or unavailable role lookup should report an error")
			} else {
				require.NoError(t, err, "role lookup should read persisted metadata")
			}
			require.Equal(t, tt.wantRole, role, "role lookup should return only one validated role")
			require.Equal(t, tt.wantFound, found, "role lookup should fail closed on missing or ambiguous ownership")
			require.NoError(t, mock.ExpectationsWereMet(), "role query should filter by org, session, thread, and active review")
		})
	}
}

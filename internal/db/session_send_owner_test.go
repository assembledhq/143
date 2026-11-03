package db

import (
	"context"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestSessionSendOwner(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                                 string
		review, automation, pending, missing bool
	}{
		{name: "unowned"}, {name: "review owned", review: true}, {name: "automation owned", automation: true},
		{name: "release pending remains owned", automation: true, pending: true}, {name: "review takes precedence", review: true, automation: true},
		{name: "missing session", missing: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create isolated owner query mock")
			t.Cleanup(mock.Close)
			org, session, review, generation, automation, target := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
			row := []any{nil, nil, nil, nil, nil}
			if tt.review {
				row[0] = &review
			}
			if tt.automation {
				row[1], row[2], row[3], row[4] = &generation, &automation, &target, &tt.pending
			}
			rows := pgxmock.NewRows([]string{"review", "generation", "automation", "target", "pending"})
			if !tt.missing {
				rows.AddRow(row...)
			}
			mock.ExpectQuery(`(?s)SELECT s.code_review_owner_pr_id.*t.org_id=g.org_id.*g.org_id=s.org_id.*WHERE s.org_id=\$1 AND s.id=\$2`).WithArgs(pgx.QueryExecModeExec, org, session).WillReturnRows(rows)
			err = NewSessionStore(mock).RejectIfSendOwned(context.Background(), org, session)
			switch {
			case tt.missing:
				require.ErrorIs(t, err, pgx.ErrNoRows, "missing tenant/session must not look unowned")
			case tt.review:
				var actual *models.SessionCodeReviewOwnedError
				require.ErrorAs(t, err, &actual, "review owner must be rejected first")
				require.Equal(t, &models.SessionCodeReviewOwnedError{PullRequestID: review}, actual, "review ownership must retain exact PR identity")
			case tt.automation:
				var actual *models.SessionAutomationOwnedError
				require.ErrorAs(t, err, &actual, "automation ownership must be rejected")
				require.Equal(t, models.SessionAutomationOwner{GenerationID: generation, AutomationID: automation, TargetID: target, ReleasePending: tt.pending, ResetURL: models.SessionAutomationResetURL(automation, target)}, actual.Owner, "automation ownership must retain every field")
			default:
				require.NoError(t, err, "unowned session must remain sendable")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "owners must be checked in one scoped statement")
		})
	}
}

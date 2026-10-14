package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestCheckSyncHandlerWaitsForPredecessor(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name     string
		active   bool
		queryErr error
		wantSync int
	}{
		{name: "running predecessor defers without attempt", active: true},
		{name: "terminal predecessor permits fresh sync", wantSync: 1},
		{name: "lookup failure cannot permit sync", queryErr: fmt.Errorf("database unavailable")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create isolated predecessor fixture")
			defer mock.Close()
			orgID, prID, predecessorID := uuid.New(), uuid.New(), uuid.New()
			query := mock.ExpectQuery("SELECT EXISTS.*WHERE org_id = @org_id AND id = @predecessor_id.*payload->>'pull_request_id' = @pull_request_id").
				WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": prID.String(), "predecessor_id": predecessorID})
			if tt.queryErr != nil {
				query.WillReturnError(tt.queryErr)
			} else {
				query.WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(tt.active))
			}
			syncCalls := 0
			services := &Services{PR: &stubPRService{syncPullRequestStateFn: func(ctx context.Context, gotOrgID, gotPRID uuid.UUID) error {
				require.Equal(t, orgID, gotOrgID, "successor must retain tenant identity")
				require.Equal(t, prID, gotPRID, "successor must retain pull request identity")
				require.Equal(t, ghservice.PullRequestSyncReasonIncomplete, ghservice.PullRequestSyncReasonFromContext(ctx), "successor must retain check attribution")
				syncCalls++
				return nil
			}}}
			payload, err := json.Marshal(map[string]string{"org_id": orgID.String(), "pull_request_id": prID.String(), "sync_reason": "incomplete_check_set", "after_job_id": predecessorID.String()})
			require.NoError(t, err, "encode successor payload")
			err = newSyncPullRequestStateHandler(&Stores{Jobs: db.NewJobStore(mock)}, services, zerolog.Nop())(context.Background(), "sync_pull_request_state", payload)
			if tt.active {
				var retry *RetryableError
				require.ErrorAs(t, err, &retry, "active predecessor must defer overlapping GitHub reads")
				require.False(t, retry.ConsumeAttempt, "waiting for predecessor must preserve the attempt budget")
				require.True(t, retry.BypassMaxRetryDuration, "internal dependency wait must survive a predecessor cooldown longer than the ordinary retry budget")
				require.Equal(t, new(5*time.Second), retry.RetryAfter, "waiting successor should poll at a bounded interval")
			} else if tt.queryErr != nil {
				require.ErrorIs(t, err, tt.queryErr, "failed predecessor lookup cannot authorize a read")
			} else {
				require.NoError(t, err, "terminal predecessor permits authoritative read")
			}
			require.Equal(t, tt.wantSync, syncCalls, "only terminal predecessor allows successor execution")
			require.NoError(t, mock.ExpectationsWereMet(), "gate must check the exact predecessor within the tenant")
		})
	}
}

func TestCheckSyncHandlerRejectsInvalidPredecessor(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, reason, predecessor string
		stores                    bool
	}{
		{name: "urgent lane never waits", reason: "head_changed", predecessor: uuid.NewString(), stores: true},
		{name: "missing store fails closed", reason: "incomplete_check_set", predecessor: uuid.NewString()},
		{name: "invalid predecessor fails closed", reason: "incomplete_check_set", predecessor: "invalid", stores: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var stores *Stores
			if tt.stores {
				stores = &Stores{Jobs: db.NewJobStore(nil)}
			}
			payload, err := json.Marshal(map[string]string{"org_id": uuid.NewString(), "pull_request_id": uuid.NewString(), "sync_reason": tt.reason, "after_job_id": tt.predecessor})
			require.NoError(t, err, "encode invalid dependency fixture")
			err = newSyncPullRequestStateHandler(stores, &Services{}, zerolog.Nop())(context.Background(), "sync_pull_request_state", payload)
			require.Error(t, err, "invalid dependency cannot reach the GitHub reader")
		})
	}
}

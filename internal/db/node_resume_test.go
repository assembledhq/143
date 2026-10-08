package db

import (
	"context"
	"errors"
	"testing"

	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestResumeOnRestartRejectsInvalidOrUnrecordedRequests(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		params   ResumeNodeParams
		rows     int64
		queryErr error
		query    bool
	}{
		{name: "missing node", params: ResumeNodeParams{Reason: "rollback", RequestedBy: "operator"}},
		{name: "blank reason", params: ResumeNodeParams{NodeID: "worker", Reason: " ", RequestedBy: "operator"}},
		{name: "missing operator", params: ResumeNodeParams{NodeID: "worker", Reason: "rollback"}},
		{name: "unknown or already resumed node", params: ResumeNodeParams{NodeID: "worker", Reason: "rollback", RequestedBy: "operator"}, query: true},
		{name: "database failure", params: ResumeNodeParams{NodeID: "worker", Reason: "rollback", RequestedBy: "operator"}, query: true, queryErr: errors.New("database unavailable")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create isolated store mock")
			defer mock.Close()
			if tt.query {
				expectation := mock.ExpectExec("WITH resumed AS").WithArgs(tt.params.NodeID, pgxmock.AnyArg(), tt.params.RequestedBy, tt.params.Reason)
				if tt.queryErr != nil {
					expectation.WillReturnError(tt.queryErr)
				} else {
					expectation.WillReturnResult(pgxmock.NewResult("INSERT", tt.rows))
				}
			}
			err = NewNodeStore(mock).ResumeOnRestart(context.Background(), tt.params)
			require.Error(t, err, "resume must report invalid, missing or unrecorded requests")
			if tt.queryErr != nil {
				require.ErrorIs(t, err, tt.queryErr, "database errors must remain inspectable")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "invalid requests must not mutate node state")
		})
	}
}

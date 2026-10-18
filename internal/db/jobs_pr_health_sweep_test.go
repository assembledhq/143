package db

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func newPullRequestHealthSweepCheckpoint() PullRequestHealthSweepCheckpoint {
	return PullRequestHealthSweepCheckpoint{
		Version:                 PullRequestHealthSweepCheckpointVersion,
		RemainingPullRequestIDs: []uuid.UUID{uuid.New(), uuid.New()},
		Population:              20, PopulationAvailable: true, CandidateLimit: 50, SelectedCount: 2,
	}
}

func TestPullRequestHealthSweepCheckpointValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		change  func(*PullRequestHealthSweepCheckpoint)
		wantErr bool
	}{
		{name: "bounded selection"},
		{name: "explicit completed selection", change: func(c *PullRequestHealthSweepCheckpoint) { c.RemainingPullRequestIDs = []uuid.UUID{} }},
		{name: "empty initial selection", change: func(c *PullRequestHealthSweepCheckpoint) {
			c.RemainingPullRequestIDs = []uuid.UUID{}
			c.SelectedCount = 0
		}},
		{name: "population unavailable", change: func(c *PullRequestHealthSweepCheckpoint) { c.PopulationAvailable = false; c.Population = 0 }},
		{name: "maximum bounds", change: func(c *PullRequestHealthSweepCheckpoint) {
			c.Population = 1000
			c.CandidateLimit = 200
			c.SelectedCount = 200
		}},
		{name: "unsupported version", wantErr: true, change: func(c *PullRequestHealthSweepCheckpoint) { c.Version++ }},
		{name: "negative population", wantErr: true, change: func(c *PullRequestHealthSweepCheckpoint) { c.Population = -1 }},
		{name: "oversized population", wantErr: true, change: func(c *PullRequestHealthSweepCheckpoint) { c.Population = 1001 }},
		{name: "zero candidate limit", wantErr: true, change: func(c *PullRequestHealthSweepCheckpoint) { c.CandidateLimit = 0 }},
		{name: "oversized candidate limit", wantErr: true, change: func(c *PullRequestHealthSweepCheckpoint) { c.CandidateLimit = 201 }},
		{name: "negative selected count", wantErr: true, change: func(c *PullRequestHealthSweepCheckpoint) { c.SelectedCount = -1 }},
		{name: "selection exceeds candidate limit", wantErr: true, change: func(c *PullRequestHealthSweepCheckpoint) { c.SelectedCount = 51 }},
		{name: "nil remainder", wantErr: true, change: func(c *PullRequestHealthSweepCheckpoint) { c.RemainingPullRequestIDs = nil }},
		{name: "remainder exceeds selection", wantErr: true, change: func(c *PullRequestHealthSweepCheckpoint) { c.SelectedCount = 1 }},
		{name: "nil pull request identity", wantErr: true, change: func(c *PullRequestHealthSweepCheckpoint) { c.RemainingPullRequestIDs[0] = uuid.Nil }},
		{name: "duplicate pull request identity", wantErr: true, change: func(c *PullRequestHealthSweepCheckpoint) { c.RemainingPullRequestIDs[1] = c.RemainingPullRequestIDs[0] }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			checkpoint := newPullRequestHealthSweepCheckpoint()
			if tt.change != nil {
				tt.change(&checkpoint)
			}
			err := checkpoint.Validate()
			if tt.wantErr {
				require.Error(t, err, "invalid persisted work must fail before provider reads")
			} else {
				require.NoError(t, err, "bounded selections and explicit completion markers should validate")
			}
		})
	}
}

func TestGetPullRequestHealthSweepCheckpointWithLease(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		payloadType string
		present     bool
		raw         string
		complete    bool
		queryErr    error
		wantErr     bool
	}{
		{name: "absent checkpoint"},
		{name: "persisted remainder", present: true},
		{name: "completed checkpoint", present: true, complete: true},
		{name: "JSON null is not absent", present: true, raw: "null", wantErr: true},
		{name: "missing required fields", present: true, raw: `{"version":1}`, wantErr: true},
		{name: "invalid JSON", present: true, raw: `{`, wantErr: true},
		{name: "wrong field type", present: true, raw: `{"version":1,"remaining_pull_request_ids":[],"population":0,"population_available":"false","candidate_limit":50,"selected_count":0}`, wantErr: true},
		{name: "unsupported version", present: true, raw: `{"version":2,"remaining_pull_request_ids":[],"population":0,"population_available":false,"candidate_limit":50,"selected_count":0}`, wantErr: true},
		{name: "null remainder", present: true, raw: `{"version":1,"remaining_pull_request_ids":null,"population":0,"population_available":false,"candidate_limit":50,"selected_count":0}`, wantErr: true},
		{name: "unknown field", present: true, raw: `{"version":1,"remaining_pull_request_ids":[],"population":0,"population_available":false,"candidate_limit":50,"selected_count":0,"unexpected":true}`, wantErr: true},
		{name: "trailing JSON", present: true, raw: `{} {}`, wantErr: true},
		{name: "non object job payload", payloadType: "array", wantErr: true},
		{name: "lost lease", queryErr: pgx.ErrNoRows, wantErr: true},
		{name: "database failure", queryErr: errors.New("storage unavailable"), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create isolated checkpoint reader")
			defer mock.Close()
			orgID, jobID, token := uuid.New(), uuid.New(), uuid.New()
			checkpoint := newPullRequestHealthSweepCheckpoint()
			if tt.complete {
				checkpoint.RemainingPullRequestIDs = []uuid.UUID{}
			}
			raw, err := json.Marshal(checkpoint)
			require.NoError(t, err, "encode expected versioned remainder")
			if tt.raw != "" {
				raw = []byte(tt.raw)
			}
			if !tt.present {
				raw = nil
			}
			payloadType := tt.payloadType
			if payloadType == "" {
				payloadType = "object"
			}
			query := mock.ExpectQuery(`SELECT jsonb_typeof\(payload\), payload \? 'health_sweep', payload->'health_sweep'.*FROM jobs.*WHERE org_id = @org_id AND id = @job_id.*queue = 'default' AND job_type = 'reconcile_pull_request_state'.*status = 'running' AND lock_token = @lock_token`).
				WithArgs(pgx.NamedArgs{"org_id": orgID, "job_id": jobID, "lock_token": token})
			if tt.queryErr != nil {
				query.WillReturnError(tt.queryErr)
			} else {
				query.WillReturnRows(pgxmock.NewRows([]string{"payload_type", "present", "checkpoint"}).AddRow(payloadType, tt.present, raw))
			}
			got, err := NewJobStore(mock).GetPullRequestHealthSweepCheckpointWithLease(context.Background(), orgID, jobID, token)
			if tt.wantErr {
				require.Error(t, err, "unreadable or unowned selections must stop fresh candidate discovery")
				require.Nil(t, got, "errors must not return a usable checkpoint")
				if errors.Is(tt.queryErr, pgx.ErrNoRows) {
					require.ErrorIs(t, err, ErrPullRequestHealthSweepLeaseLost, "lost ownership must remain distinguishable from absent work")
				} else if tt.queryErr != nil {
					require.ErrorIs(t, err, tt.queryErr, "storage failures must retain their cause")
				}
			} else {
				require.NoError(t, err, "read only the current tenant and running owner selection")
				var want *PullRequestHealthSweepCheckpoint
				if tt.present {
					want = &checkpoint
				}
				require.Equal(t, want, got, "only an absent key permits a new selection; an empty saved remainder means completion")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "reader must fence the tenant, queue, type, status, and lock token")
		})
	}
}

func TestSavePullRequestHealthSweepCheckpointWithLease(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		complete   bool
		invalid    bool
		rows       int
		storageErr error
	}{
		{name: "bounded remainder", rows: 1},
		{name: "explicit completion", complete: true, rows: 1},
		{name: "validation precedes storage", invalid: true},
		{name: "lost lease"},
		{name: "database failure", storageErr: errors.New("storage unavailable")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create isolated checkpoint writer")
			defer mock.Close()
			orgID, jobID, token := uuid.New(), uuid.New(), uuid.New()
			checkpoint := newPullRequestHealthSweepCheckpoint()
			if tt.complete {
				checkpoint.RemainingPullRequestIDs = []uuid.UUID{}
			}
			if tt.invalid {
				checkpoint.RemainingPullRequestIDs = nil
			} else {
				raw, err := json.Marshal(checkpoint)
				require.NoError(t, err, "encode expected versioned remainder")
				query := mock.ExpectExec(`UPDATE jobs.*SET payload = jsonb_set\(payload, '\{health_sweep\}', @checkpoint::jsonb\),.*updated_at = now\(\).*WHERE org_id = @org_id AND id = @job_id.*queue = 'default' AND job_type = 'reconcile_pull_request_state'.*status = 'running' AND lock_token = @lock_token.*jsonb_typeof\(payload\) = 'object'`).
					WithArgs(pgx.NamedArgs{"org_id": orgID, "job_id": jobID, "lock_token": token, "checkpoint": raw})
				if tt.storageErr != nil {
					query.WillReturnError(tt.storageErr)
				} else if tt.rows == 1 {
					query.WillReturnResult(pgconn.NewCommandTag("UPDATE 1"))
				} else {
					query.WillReturnResult(pgconn.NewCommandTag("UPDATE 0"))
				}
			}
			err = NewJobStore(mock).SavePullRequestHealthSweepCheckpointWithLease(context.Background(), orgID, jobID, token, checkpoint)
			switch {
			case tt.invalid:
				require.Error(t, err, "invalid remainder must not reach storage")
			case tt.storageErr != nil:
				require.ErrorIs(t, err, tt.storageErr, "checkpoint persistence failures must retain their cause")
			case tt.rows == 0:
				require.ErrorIs(t, err, ErrPullRequestHealthSweepLeaseLost, "a zero-row update must stop the old owner")
			default:
				require.NoError(t, err, "persist a valid checkpoint under the current tenant and token")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "writer must preserve unrelated payload and fence all ownership fields")
		})
	}
}

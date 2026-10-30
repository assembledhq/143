package db

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestLifecycleConsumerRetainedRepairPayloadBudgetsPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		rows, bytes int
		incomplete  bool
	}{
		{name: "exact per-row limit", rows: 1, bytes: legacyJobRouteMaxBytes},
		{name: "oversized row is SQL NULL", rows: 1, bytes: legacyJobRouteMaxBytes + 1, incomplete: true},
		{name: "eight near-limit retained payloads fit", rows: 8, bytes: legacyJobRouteMaxBytes - 256},
		{name: "nine near-limit retained payloads overflow", rows: 9, bytes: legacyJobRouteMaxBytes - 256, incomplete: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := newLifecycleConsumerQueryPool(t)
			f := newLifecyclePredicateFixture(t, pool)
			const empty = `{"domain": "", "repair_run_id": null}`
			payload := json.RawMessage(`{"domain": "` + strings.Repeat("x", tt.bytes-len(empty)) + `", "repair_run_id": null}`)
			expectedJobs := make(map[uuid.UUID]bool, tt.rows)
			for range tt.rows {
				job := seedLifecycleConsumerJob(t, pool, f.org, models.JobStatusFailed)
				predicateExec(t, pool, "UPDATE jobs SET job_type='continue_session',payload=$3 WHERE org_id=$1 AND id=$2", f.org, job, payload)
				seedLifecyclePreparation(t, pool, f.org, f.session, &f.lane, job, "closed", "unknown")
				expectedJobs[job] = true
			}
			var nonterminal int
			require.NoError(t, pool.QueryRow(context.Background(), `SELECT count(*) FROM jobs WHERE org_id=$1
			 AND job_type IN ('run_agent','continue_session') AND status IN ('pending','running','awaiting_start_control')`, f.org).Scan(&nonterminal), "the retained-route proof must not borrow the ordinary nonterminal fallback")
			require.Zero(t, nonterminal, "every candidate job must be terminal while an actual unresolved preparation retains it")
			ctx, cancel := context.WithTimeout(context.Background(), LifecycleTransactionTimeout)
			defer cancel()
			started := time.Now()
			owner, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
			require.NoError(t, err, "the test caller must own one real READ COMMITTED observation unit")
			defer func() { _ = owner.Rollback(context.Background()) }()
			require.NoError(t, ConfigureLifecycleTransactionInTx(ctx, owner), "retain the production two-second owner,1500ms statement and250ms lock budgets")
			_, err = lockLifecycleScopeInTx(ctx, owner, f.org, lifecycleScope{sessionID: f.session, threadIDs: []uuid.UUID{f.lane}})
			require.NoError(t, err, "hold the actual canonical parent and requested lane before reading retained evidence")
			limits := defaultLifecycleConsumerLimits()
			rows, err := owner.Query(ctx, lifecycleConsumerEvidenceQuery, pgx.QueryExecModeExec, f.org, f.session, limits.structuredRows+1, lifecycleConsumerUUIDArray([]uuid.UUID{f.lane, f.sibling}), "{}")
			require.NoError(t, err, "read the actual canonical structured references to terminal jobs, not fabricated candidate DTOs")
			evidence, err := pgx.CollectRows(rows, scanLifecycleConsumerEvidence)
			require.NoError(t, err, "collect the actual unresolved preparation/job associations before the exact retained probe")
			actualJobs := make(map[uuid.UUID]bool, len(evidence))
			for _, v := range evidence {
				require.Equal(t, lifecycleConsumerPreparation, v.source, "only the seeded retained physical preparation may supply candidate identity")
				require.Equal(t, "failed", v.jobStatus, "a terminal label must not erase retained physical work")
				require.NotNil(t, v.jobID, "actual retained evidence must carry its exact job reference")
				require.Equal(t, v.jobID, v.foundJobID, "the tenant-qualified actual job join must agree with each retained reference")
				actualJobs[*v.jobID] = true
			}
			require.Equal(t, expectedJobs, actualJobs, "the retained probe must be anchored to exactly the seeded terminal jobs")
			actual, bytes, readErr := readRetainedLifecycleRepairJobsInTx(ctx, owner, f.org, evidence, limits)
			var one int
			probeErr := owner.QueryRow(ctx, "SELECT 1").Scan(&one)
			t.Logf("retained repair payload budget: rows=%d per_row_bytes=%d owner_elapsed=%s reader_error=%v probe_error=%v", tt.rows, tt.bytes, time.Since(started), readErr, probeErr)
			require.NoError(t, probeErr, "success and semantic overflow must both leave the unchanged-budget caller transaction usable")
			require.Equal(t, 1, one, "the retained reader must not settle or replace the supplied transaction")
			if tt.incomplete {
				require.ErrorIs(t, readErr, ErrLifecycleConsumerDiscoveryIncomplete, "per-row and remaining aggregate overflow must refuse before a partial repair result")
				require.Nil(t, actual, "failed retained discovery must expose no partial repair map")
				require.Zero(t, bytes, "failed retained discovery must expose no reusable byte accounting")
			} else {
				require.NoError(t, readErr, "complete retained payloads must fit the original budgets without extending any timeout")
				require.Equal(t, expectedJobs, actual, "every complete retained top-level repair marker must remain uncertain")
				require.Equal(t, tt.rows*tt.bytes, bytes, "successful retained reads must account for exact original PostgreSQL payload bytes")
			}
			require.NoError(t, owner.Rollback(ctx), "only the caller may settle the focused retained-reference observation")
		})
	}
}

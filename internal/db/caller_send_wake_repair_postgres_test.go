package db

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	testpostgres "github.com/assembledhq/143/internal/testutil/postgres"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCallerSendWakeRepairPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, history string
		reviewers     int
		shared        bool
	}{
		{"zero history without integrations", "", 2, true},
		{"deleted wake", "deleted", 2, true},
		{"dead letter wake", "dead_letter", 2, true},
		{"live wake suppresses duplicates", "pending", 2, true},
		{"bounded due selector", "", 105, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			pool := testpostgres.NewPool(t, ctx, "../../migrations", 2)
			_, err := pool.Exec(ctx, `UPDATE caller_send_intent_control SET intents_enabled=true WHERE singleton AND schema_ready AND dispatch_status_ready`)
			require.NoError(t, err, "enable only this isolated synthetic database")
			tx := callerSendSchemaBegin(t, ctx, pool)
			f := callerSendSchemaSeed(t, ctx, tx)
			expectedPayload := map[string]any{"org_id": f.org.String(), "session_id": f.session.String(), "review_output_key": "synthetic-original", "retained_marker": "synthetic private original payload"}
			for n := 0; n < tt.reviewers; n++ {
				reviewer := f
				reviewer.owner = uuid.New()
				payload := expectedPayload
				if !tt.shared {
					payload = map[string]any{"org_id": f.org.String(), "session_id": f.session.String(), "review_output_key": fmt.Sprintf("synthetic-%03d", n)}
				}
				wake, err := json.Marshal(map[string]any{"job_type": "run_code_review", "queue": "agent", "dedupe_key": "code_review:" + payload["review_output_key"].(string), "payload": payload})
				require.NoError(t, err, "encode synthetic frozen wake")
				shape := callerSendSchemaShape(callerSendSchemaOwners()[0], reviewer, "pending")
				shape["wake"] = string(wake)
				require.NoError(t, callerSendSchemaInsertOwner(ctx, tx, callerSendSchemaOwners()[0], reviewer, shape), "seed a legal retained reviewer with immutable original wake")
			}
			loop := f
			loop.owner, loop.pass = uuid.New(), uuid.New()
			require.NoError(t, callerSendSchemaInsertOwner(ctx, tx, callerSendSchemaOwners()[1], loop, callerSendSchemaShape(callerSendSchemaOwners()[1], loop, "pending")), "seed a genuine bound startup awaiting its wake")
			prepared := f
			prepared.owner, prepared.thread, prepared.pass = uuid.New(), uuid.New(), uuid.New()
			require.NoError(t, callerSendSchemaInsertMap(ctx, tx, "review_loop_start_intents", callerSendSchemaStartRow(prepared, "manual", uuid.Nil)), "seed prepared creation before its thread or loop exists")
			require.NoError(t, tx.Commit(ctx), "commit genuine durable owners before any repair")
			var integrations int
			require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM integrations WHERE org_id=$1`, f.org).Scan(&integrations), "read actual absence of active integration dependency")
			require.Zero(t, integrations, "repair must work in an organization with no integrations")
			store := NewCallerSendWakeStore(pool)
			if tt.history != "" {
				require.NoError(t, store.RepairMissingWakes(ctx), "create initial notifications for history fixture")
				if tt.history == "deleted" {
					_, err = pool.Exec(ctx, `DELETE FROM jobs WHERE org_id=$1`, f.org)
				} else if tt.history == "dead_letter" {
					_, err = pool.Exec(ctx, `UPDATE jobs SET status='dead_letter',completed_at=statement_timestamp() WHERE org_id=$1`, f.org)
				}
				require.NoError(t, err, "remove or exhaust notifications without touching retained owners")
			}
			require.NoError(t, store.RepairMissingWakes(ctx), "repair each due category independently of historical jobs")
			var count int
			require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE org_id=$1 AND status='pending'`, f.org).Scan(&count), "count actual pending notifications")
			expected := 3
			if !tt.shared {
				expected = 102
			}
			require.Equal(t, expected, count, "deduplicate shared reviewer wake and bound each category to at most one hundred candidates")
			require.NoError(t, store.RepairMissingWakes(ctx), "repeat recovery without duplicating any existing live notification")
			if !tt.shared {
				expected = 107
			}
			require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE org_id=$1 AND status='pending'`, f.org).Scan(&count), "read notifications after repeated repair")
			require.Equal(t, expected, count, "a later sweep advances beyond live jobs while preserving dedupe")
			if tt.shared {
				var actual json.RawMessage
				require.NoError(t, pool.QueryRow(ctx, `SELECT payload FROM jobs WHERE org_id=$1 AND job_type='run_code_review' AND status='pending'`, f.org).Scan(&actual), "read repaired original controller payload")
				expectedRaw, err := json.Marshal(expectedPayload)
				require.NoError(t, err, "encode complete expected retained payload")
				require.JSONEq(t, string(expectedRaw), string(actual), "repair must preserve every original payload field without rerendering")
			}
			var counts [3]int
			require.NoError(t, pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM code_review_agent_results WHERE org_id=$1 AND dispatch_state='pending'),(SELECT count(*) FROM session_review_loops WHERE org_id=$1 AND startup_send_state='pending'),(SELECT count(*) FROM review_loop_start_intents WHERE org_id=$1 AND state='prepared')`, f.org).Scan(&counts[0], &counts[1], &counts[2]), "read durable ownership after repair")
			require.Equal(t, [3]int{tt.reviewers, 1, 1}, counts, "wake repair cannot claim, send, mutate or abandon any owner")
		})
	}
}

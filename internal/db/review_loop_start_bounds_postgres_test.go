package db

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	testpostgres "github.com/assembledhq/143/internal/testutil/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestReviewLoopStartPreparationJSONBBoundsPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		input, expanded bool
	}{
		{name: "near envelope bound"},
		{name: "compact envelope fits but stored JSONB exceeds bound", expanded: true},
		{name: "near command bound", input: true},
		{name: "compact command fits but stored JSONB exceeds bound", input: true, expanded: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			defer cancel()
			pool := testpostgres.NewPool(t, ctx, "../../migrations", 2)
			_, err := pool.Exec(ctx, `UPDATE caller_send_intent_control SET intents_enabled=true WHERE singleton AND schema_ready AND dispatch_status_ready`)
			require.NoError(t, err, "enable only the isolated storage fixture")
			tx := callerSendSchemaBegin(t, ctx, pool)
			fixture := callerSendSchemaSeed(t, ctx, tx)
			require.NoError(t, tx.Commit(ctx), "commit real tenant and session before the bounded preparation unit")
			// These are deliberately SQL-shape fixtures. The complete codec
			// round-trip is verified separately; no fixture executes a command.
			input := callerSendSchemaInput(callerSendSchemaOwners()[1], fixture)
			envelope := `{"version":1}`
			padding := `"` + strings.Repeat("x", 8*1024*1024-65536) + `"`
			if tt.expanded {
				padding = `[` + strings.Repeat("9223372036854775807,", 410000) + `9223372036854775807]`
			}
			if tt.input {
				input = strings.TrimSuffix(input, "}") + `,"padding":` + padding + `}`
			} else {
				envelope = `{"padding":` + padding + `}`
			}
			raw := envelope
			if tt.input {
				raw = input
			}
			require.LessOrEqual(t, len(raw), 8*1024*1024, "compact JSON must fit before testing actual PostgreSQL presentation")
			var stored int
			require.NoError(t, pool.QueryRow(ctx, `SELECT octet_length($1::jsonb::text)`, raw).Scan(&stored), "measure the actual representation the restore path reads")
			if tt.expanded {
				require.Greater(t, stored, 8*1024*1024, "JSONB whitespace must make the compact-fit fixture exceed the actual bound")
			} else {
				require.Greater(t, stored, 8*1024*1024*95/100, "accepted fixture must exercise over ninety-five percent of the actual bound")
				require.LessOrEqual(t, stored, 8*1024*1024, "accepted JSONB presentation must still fit")
			}
			owner, err := NewThreadSendTransactions(pool)
			require.NoError(t, err, "use unchanged production transaction budgets")
			preparation := ReviewLoopStartPreparation{Scope: ReviewLoopStartScope{ID: fixture.owner, SessionID: fixture.session, Source: models.ReviewLoopSourceManual}, PassID: fixture.pass, ThreadID: fixture.thread, Envelope: json.RawMessage(envelope), Input: json.RawMessage(input), EnvelopeDigest: strings.Repeat("a", 64), StartupDigest: strings.Repeat("b", 64)}
			var actual ReviewLoopStartIntent
			err = owner.Run(ctx, func(ctx context.Context, tx pgx.Tx) error {
				var err error
				actual, err = PrepareReviewLoopStartInTx(ctx, fixture.org, tx, preparation)
				return err
			})
			if tt.expanded {
				require.ErrorIs(t, err, ErrReviewLoopStartTooLarge, "oversized actual presentation must be a known refusal before any durable checkpoint")
			} else {
				require.NoError(t, err, "near-bound original must commit under the unchanged owner budget")
				require.Equal(t, fixture.owner, actual.Scope().ID, "accepted preparation preserves the supplied original identity")
				var reread ReviewLoopStartIntent
				err = owner.Run(ctx, func(ctx context.Context, tx pgx.Tx) error {
					var found bool
					var err error
					reread, found, err = ReadReviewLoopStartInTx(ctx, fixture.org, fixture.session, fixture.owner, tx)
					if err == nil && !found {
						return pgx.ErrNoRows
					}
					return err
				})
				require.NoError(t, err, "near-bound frozen object must restore from actual JSONB without truncation")
				var expectedJSON, actualJSON any
				require.NoError(t, json.Unmarshal([]byte(raw), &expectedJSON), "decode complete expected SQL-shape fixture")
				restored := reread.Preparation().Envelope
				if tt.input {
					restored = reread.Preparation().Input
				}
				require.NoError(t, json.Unmarshal(restored, &actualJSON), "decode complete actual restored object")
				require.Equal(t, expectedJSON, actualJSON, "stored/read JSONB must retain the entire original object")
			}
			var graph [3]int
			require.NoError(t, pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM review_loop_start_intents WHERE org_id=$1),(SELECT count(*) FROM jobs WHERE org_id=$1),(SELECT count(*) FROM session_review_loops WHERE org_id=$1)`, fixture.org).Scan(&graph[0], &graph[1], &graph[2]), "read all preparation effects after the owner finishes")
			expected := [3]int{1, 1, 0}
			if tt.expanded {
				expected = [3]int{}
			}
			require.Equal(t, expected, graph, "known oversize refusal leaves no intent, wake or loop; accepted T0 has one intent and wake")
		})
	}
}

package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	testpostgres "github.com/assembledhq/143/internal/testutil/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

type startupTransitionSnapshot struct {
	state, detail         string
	attemptID             uuid.UUID
	attemptSeq, writerSeq int32
	attemptedAt           time.Time
	dueAt                 time.Time
	deadline              *time.Time
	attentionAt           *time.Time
	observedAt            *time.Time
	updatedAt             time.Time
	messageID             *int64
	resolution            *string
}

func readStartupTransitionSnapshot(t *testing.T, ctx context.Context, pool *pgxpool.Pool, f callerSendSchemaFixture) startupTransitionSnapshot {
	t.Helper()
	var actual startupTransitionSnapshot
	err := pool.QueryRow(ctx, `SELECT startup_send_state,COALESCE(startup_send_detail,''),startup_send_attempt_id,
 startup_send_attempt_seq,startup_send_writer_seq,startup_send_attempted_at,startup_send_due_at,
 startup_send_observe_deadline,startup_send_attention_at,startup_send_observed_at,startup_send_updated_at,
 startup_send_message_id,startup_send_resolution
 FROM session_review_loops WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.session, f.owner).Scan(
		&actual.state, &actual.detail, &actual.attemptID, &actual.attemptSeq, &actual.writerSeq, &actual.attemptedAt, &actual.dueAt,
		&actual.deadline, &actual.attentionAt, &actual.observedAt, &actual.updatedAt, &actual.messageID, &actual.resolution)
	require.NoError(t, err, "read all mutable startup outcome and attempt fields from the exact retained owner")
	return actual
}

// These fixtures use genuine migrated rows and legal historical transitions.
// Their command JSON is a SQL-shape fixture and is never executed. Actual
// stamped acceptance and service-level no-resend proofs live in reviewloop.
func TestReviewLoopStartupDueTransitionsPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, initial, expectedState, expectedDetail string
		stale, newerWriter                           bool
		delay                                        time.Duration
	}{
		{name: "stale attempting retains original attempt", initial: "attempting", expectedState: "unknown", expectedDetail: "stale_attempt", stale: true, delay: 5 * time.Second},
		{name: "expired negative observation enters attention", initial: "unknown", expectedState: "attention", expectedDetail: "unknown_ack", delay: time.Hour},
		{name: "attention reobservation preserves history", initial: "attention", expectedState: "attention", expectedDetail: "unknown_ack", delay: time.Hour},
		{name: "newer writer defeats stale negative candidate", initial: "unknown", newerWriter: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			defer cancel()
			pool := testpostgres.NewPool(t, ctx, "../../migrations", 2)
			_, err := pool.Exec(ctx, `UPDATE caller_send_intent_control SET intents_enabled=true WHERE singleton AND schema_ready AND dispatch_status_ready`)
			require.NoError(t, err, "enable only this disposable synthetic database")
			tx := callerSendSchemaBegin(t, ctx, pool)
			f := callerSendSchemaSeed(t, ctx, tx)
			owner := callerSendSchemaOwners()[1]
			callerSendSchemaReachOwner(t, ctx, tx, owner, f, tt.initial)
			require.NoError(t, tx.Commit(ctx), "commit the original legal historical owner before starting separate bounded controller units")
			transactions, err := NewThreadSendTransactions(pool)
			require.NoError(t, err, "use the unchanged production transaction owner and budgets")
			var original ReviewLoopStartupIntent
			err = transactions.Run(ctx, func(ctx context.Context, tx pgx.Tx) error {
				var found bool
				var err error
				original, found, err = ReadReviewLoopStartupInTx(ctx, f.org, f.session, f.owner, tx)
				if err == nil && !found {
					return pgx.ErrNoRows
				}
				return err
			})
			require.NoError(t, err, "read a supported original startup from actual persistence before releasing its checkout")
			require.True(t, original.Due(), "historical fixture must be positively due without changing any clock or production timeout")
			before := readStartupTransitionSnapshot(t, ctx, pool, f)
			if tt.newerWriter {
				err = transactions.Run(ctx, func(ctx context.Context, tx pgx.Tx) error {
					_, err := tx.Exec(ctx, `UPDATE session_review_loops SET latest_summary='Synthetic later progress',
 startup_send_writer_seq=startup_send_writer_seq+1,startup_send_updated_at=statement_timestamp()
 WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.session, f.owner)
					return err
				})
				require.NoError(t, err, "commit genuine later ancillary progress and writer sequence before applying the old candidate")
			}
			var beforeRow json.RawMessage
			require.NoError(t, pool.QueryRow(ctx, `SELECT to_jsonb(l) FROM session_review_loops l WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.session, f.owner).Scan(&beforeRow), "capture every current owner field after any newer writer commits")
			var lower, upper time.Time
			require.NoError(t, pool.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&lower), "bound only the upcoming real update timestamp with the database clock")
			outcome := ReviewLoopStartupNegativeObservation()
			if tt.stale {
				outcome = ReviewLoopStartupStale()
			}
			var changed bool
			err = transactions.Run(ctx, func(ctx context.Context, tx pgx.Tx) error {
				var err error
				_, changed, err = SettleReviewLoopStartupInTx(ctx, f.org, tx, original, outcome)
				return err
			})
			require.NoError(t, err, "apply the exact finite startup transition through the production helper")
			require.NoError(t, pool.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&upper), "bound the completed update using the unchanged database clock")
			var afterRow json.RawMessage
			require.NoError(t, pool.QueryRow(ctx, `SELECT to_jsonb(l) FROM session_review_loops l WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.session, f.owner).Scan(&afterRow), "read the complete current owner after settlement")
			if tt.newerWriter {
				require.False(t, changed, "an older writer tuple must not change a newer owner even when state and attempt still match")
				require.Equal(t, beforeRow, afterRow, "stale settlement must preserve every newer immutable, outcome and ancillary field exactly")
			} else {
				require.True(t, changed, "the due original must complete one finite state transition")
				actual := readStartupTransitionSnapshot(t, ctx, pool, f)
				require.WithinRange(t, actual.updatedAt, lower, upper, "new timestamps must come from the actual bounded update, not historical fixture times")
				expected := before
				expected.state, expected.detail = tt.expectedState, tt.expectedDetail
				expected.writerSeq++
				expected.updatedAt = actual.updatedAt
				expected.dueAt = actual.updatedAt.Add(tt.delay)
				if tt.stale {
					deadline := before.attemptedAt.Add(15 * time.Minute)
					expected.deadline = &deadline
				} else {
					observed := actual.updatedAt
					expected.observedAt = &observed
					if tt.initial == "unknown" {
						attention := actual.updatedAt
						expected.attentionAt = &attention
					}
				}
				require.Equal(t, expected, actual, "the complete transition must preserve the original attempt, deadline and receipt absence while setting exact observation scheduling")
				var beforeFields, afterFields map[string]any
				require.NoError(t, json.Unmarshal(beforeRow, &beforeFields), "decode all prior owner fields for non-outcome preservation")
				require.NoError(t, json.Unmarshal(afterRow, &afterFields), "decode all settled owner fields for non-outcome preservation")
				for _, field := range []string{"startup_send_state", "startup_send_detail", "startup_send_due_at", "startup_send_observe_deadline", "startup_send_attention_at", "startup_send_observed_at", "startup_send_writer_seq", "startup_send_updated_at"} {
					delete(beforeFields, field)
					delete(afterFields, field)
				}
				require.Equal(t, beforeFields, afterFields, "all frozen command, pass binding, attempt, status, progress and other owner fields remain byte-equivalent")
			}
			var claimChanged bool
			err = transactions.Run(ctx, func(ctx context.Context, tx pgx.Tx) error {
				current, found, err := ReadReviewLoopStartupInTx(ctx, f.org, f.session, f.owner, tx)
				if err != nil {
					return err
				}
				if !found {
					return pgx.ErrNoRows
				}
				_, claimChanged, err = ClaimReviewLoopStartupInTx(ctx, f.org, tx, current, uuid.New())
				return err
			})
			require.NoError(t, err, "non-pending outcomes must safely refuse a later new claim")
			require.False(t, claimChanged, "stale attempts and negative observations can never authorize a resend")
			var graph [4]int
			require.NoError(t, pool.QueryRow(ctx, `SELECT
 (SELECT count(*) FROM session_messages WHERE org_id=$1),
 (SELECT count(*) FROM thread_inbox_entries WHERE org_id=$1),
 (SELECT count(*) FROM caller_send_command_stamps WHERE org_id=$1),
 (SELECT count(*) FROM session_review_loop_passes WHERE org_id=$1 AND session_id=$2 AND loop_id=$3 AND review_message_id IS NOT NULL)`, f.org, f.session, f.owner).Scan(&graph[0], &graph[1], &graph[2], &graph[3]), "read all possible accepted-message effects and the bound pass pointer")
			require.Equal(t, [4]int{}, graph, "observation-only transitions create no messages, inbox entries, stamps or accepted pass pointer")
		})
	}
}

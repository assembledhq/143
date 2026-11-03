package db

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	testpostgres "github.com/assembledhq/143/internal/testutil/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// One parallel root owns a disposable full-schema database with at most three
// connections. Subcases are sequential because the actual rollback migration
// takes database-wide table locks and updates the global control singleton.
//
//nolint:paralleltest // Subcases share database-wide migration locks and the control singleton.
func TestCallerSendIntentSchemaReviewRegressionsPostgres(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	pool := testpostgres.NewPool(t, ctx, "../../migrations", 3)
	callerDispatchStatusPinOriginal389(t, ctx, pool)
	var ready, enabled bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT schema_ready,intents_enabled FROM caller_send_intent_control WHERE singleton`).Scan(&ready, &enabled), "read the actual highest migration's passive readiness state")
	require.Equal(t, [2]bool{true, false}, [2]bool{ready, enabled}, "the full schema must remain unenabled until this disposable fixture explicitly opts in")
	_, err := pool.Exec(ctx, `UPDATE caller_send_intent_control SET intents_enabled=true WHERE singleton AND schema_ready`)
	require.NoError(t, err, "enable only this isolated synthetic database for finite storage regressions")
	tests := []struct {
		name string
		run  func(*testing.T, context.Context, *pgxpool.Pool)
	}{
		{name: "loop status writer fence", run: callerSendReviewLoopStatusFence},
		{name: "cross state history", run: callerSendReviewCrossStateHistory},
		{name: "rollback scan contract", run: callerSendReviewRollbackScans},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { tt.run(t, ctx, pool) })
	}
}

func callerSendReviewPrepareLoop(t *testing.T, ctx context.Context, tx pgx.Tx, f callerSendSchemaFixture, state string) {
	t.Helper()
	owner := callerSendSchemaOwners()[1]
	if state == "legacy" {
		require.NoError(t, callerSendSchemaInsertOwner(ctx, tx, owner, f, nil), "create an actual legacy all-NULL loop before testing unchanged writer behavior")
		_, err := callerSendSchemaUpdate(ctx, tx, owner, f, `status='running'`)
		require.NoError(t, err, "legacy control starts from a distinct running status, not an already-terminal no-op")
		return
	}
	base := state
	if state == "pointer-conflict" {
		base = "attention"
	}
	callerSendSchemaReachOwner(t, ctx, tx, owner, f, base)
	if state == "pointer-conflict" {
		_, err := tx.Exec(ctx, `UPDATE session_review_loop_passes SET review_message_id=456 WHERE org_id=$1 AND session_id=$2 AND loop_id=$3 AND id=$4`, f.org, f.session, f.owner, f.pass)
		require.NoError(t, err, "pointer-conflict fixture must have positive evidence on the exact bound initial pass")
		_, err = callerSendSchemaUpdate(ctx, tx, owner, f, `startup_send_detail='pass_pointer_conflict',startup_send_writer_seq=startup_send_writer_seq+1`)
		require.NoError(t, err, "enter complete same-attempt pointer-conflicted attention before testing its status or history fence")
	}
}

func callerSendReviewLoopStatusFence(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	type testCase struct {
		name, state, status string
		aware, refused      bool
	}
	tests := []testCase{}
	for _, state := range []string{"pending", "attempting", "unknown", "attention", "pointer-conflict"} {
		for _, status := range []string{"failed", "clean", "needs_human_decision"} {
			tests = append(tests,
				testCase{name: state + "/" + status + "/unaware", state: state, status: status, refused: true},
				testCase{name: state + "/" + status + "/aware", state: state, status: status, aware: true})
		}
		tests = append(tests, testCase{name: state + "/cancelled/unaware", state: state, status: "cancelled"})
	}
	for _, state := range []string{"legacy", "accepted"} {
		for _, status := range []string{"failed", "clean", "needs_human_decision", "cancelled"} {
			tests = append(tests, testCase{name: state + "/" + status, state: state, status: status})
		}
	}
	owner := callerSendSchemaOwners()[1]
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx := callerSendSchemaBegin(t, ctx, pool)
			f := callerSendSchemaSeed(t, ctx, tx)
			callerSendReviewPrepareLoop(t, ctx, tx, f, tt.state)
			before := callerSendSchemaSnapshot(t, ctx, tx, owner, f)
			set := `status=$4`
			if tt.aware {
				set += `,startup_send_writer_seq=startup_send_writer_seq+1`
			}
			if tt.refused {
				err := callerSendSchemaProbe(t, ctx, tx, func(probe pgx.Tx) error {
					_, err := callerSendSchemaUpdate(ctx, probe, owner, f, set, tt.status)
					return err
				})
				callerSendSchemaSQLState(t, err, "55000", "an unaware non-cancelling status writer must not terminalize unresolved startup without the next writer sequence")
				require.Equal(t, before, callerSendSchemaSnapshot(t, ctx, tx, owner, f), "the refused status write must retain every startup and ordinary loop field unchanged")
				return
			}
			tag, err := callerSendSchemaUpdate(ctx, tx, owner, f, set, tt.status)
			require.NoError(t, err, "aware writers, cancellation, legacy loops, and settled startup retain their permitted status behavior")
			require.Equal(t, int64(1), tag.RowsAffected(), "the permitted control must actually update its exact retained loop")
			var oldRow, newRow map[string]any
			require.NoError(t, json.Unmarshal(before, &oldRow), "decode the complete status-control baseline")
			require.NoError(t, json.Unmarshal(callerSendSchemaSnapshot(t, ctx, tx, owner, f), &newRow), "read every field after the permitted status update")
			require.Equal(t, tt.status, newRow["status"], "the permitted writer must install its requested current status")
			if tt.aware {
				require.Equal(t, oldRow["startup_send_writer_seq"].(float64)+1, newRow["startup_send_writer_seq"], "the aware writer must advance exactly one sequence rather than reset or skip it")
			} else {
				require.Equal(t, oldRow["startup_send_writer_seq"], newRow["startup_send_writer_seq"], "the permitted unaware control must not acquire hidden writer authority")
			}
			delete(oldRow, "status")
			delete(newRow, "status")
			delete(oldRow, "startup_send_writer_seq")
			delete(newRow, "startup_send_writer_seq")
			require.Equal(t, oldRow, newRow, "status compatibility must never settle or rewrite retained startup intent and history")
		})
	}
}

func callerSendReviewCrossStateHistory(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	tests := []struct {
		name, from, target, detail, resolution string
		pointer                                bool
	}{
		{name: "unknown to attention", from: "unknown", target: "attention", detail: "unknown_ack"},
		{name: "unknown to pointer attention", from: "unknown", target: "attention", detail: "pass_pointer_conflict", pointer: true},
		{name: "attention to pointer attention", from: "attention", target: "attention", detail: "pass_pointer_conflict", pointer: true},
		{name: "unknown to observed acceptance", from: "unknown", target: "accepted", resolution: "observed_auto"},
		{name: "attention to observed acceptance", from: "attention", target: "accepted", resolution: "observed_manual"},
		{name: "pointer attention to observed acceptance", from: "pointer-conflict", target: "accepted", resolution: "observed_auto"},
	}
	owner := callerSendSchemaOwners()[1]
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx := callerSendSchemaBegin(t, ctx, pool)
			f := callerSendSchemaSeed(t, ctx, tx)
			callerSendReviewPrepareLoop(t, ctx, tx, f, tt.from)
			if tt.pointer {
				_, err := tx.Exec(ctx, `UPDATE session_review_loop_passes SET review_message_id=456 WHERE org_id=$1 AND session_id=$2 AND loop_id=$3 AND id=$4`, f.org, f.session, f.owner, f.pass)
				require.NoError(t, err, "pointer entry requires positive evidence on the retained pass rather than a generic attention escape")
			}
			_, err := callerSendSchemaUpdate(ctx, tx, owner, f, `startup_send_observed_at=$4,startup_send_due_at=$5,startup_send_writer_seq=startup_send_writer_seq+1`, f.at.Add(16*time.Minute), f.at.Add(time.Hour))
			require.NoError(t, err, "positively retain observed history and a later due time before every cross-state mutation probe")
			var deadline, observed time.Time
			var attention *time.Time
			require.NoError(t, tx.QueryRow(ctx, `SELECT startup_send_observe_deadline,startup_send_observed_at,startup_send_attention_at FROM session_review_loops WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.session, f.owner).Scan(&deadline, &observed, &attention), "read the actual previously retained history that each transition must preserve")
			require.Equal(t, f.at.Add(15*time.Minute), deadline.UTC(), "the fixture must retain its original attempt observation deadline")
			require.Equal(t, f.at.Add(16*time.Minute), observed.UTC(), "the fixture must have real non-NULL observed history before testing its integrity")
			if tt.from != "unknown" {
				require.NotNil(t, attention, "attention-origin transitions must start with a durable original attention timestamp")
				require.Equal(t, f.at.Add(15*time.Minute), attention.UTC(), "attention history must be positively retained before rewrite probes")
			}
			values := map[string]any{"state": tt.target, "observe_deadline": deadline, "observed_at": f.at.Add(18 * time.Minute)}
			messageID := int64(123)
			if tt.from == "pointer-conflict" {
				messageID = 456
			}
			if tt.target == "attention" {
				values["detail"], values["due_at"] = tt.detail, f.at.Add(2*time.Hour)
				values["attention_at"] = f.at.Add(17 * time.Minute)
				if attention != nil {
					values["attention_at"] = *attention
				}
			} else {
				// Acceptance legitimately clears due/detail, not retained history.
				values["message_id"], values["resolution"] = messageID, tt.resolution
				if attention != nil {
					values["attention_at"] = *attention
				}
			}
			mutations := []struct {
				name, field string
				value       any
			}{
				{name: "regress observed history", field: "observed_at", value: f.at.Add(15 * time.Minute)},
				{name: "rewrite deadline earlier", field: "observe_deadline", value: f.at.Add(14 * time.Minute)},
				{name: "rewrite deadline later", field: "observe_deadline", value: f.at.Add(16 * time.Minute)},
			}
			if tt.target == "attention" {
				// Clearing observed_at is shape-valid here; clearing a required
				// attention/deadline would only retest an existing shape CHECK.
				mutations = append(mutations,
					struct {
						name, field string
						value       any
					}{name: "clear observed history", field: "observed_at", value: nil},
					struct {
						name, field string
						value       any
					}{name: "move due time backward", field: "due_at", value: f.at.Add(30 * time.Minute)},
					struct {
						name, field string
						value       any
					}{name: "replace unknown reason", field: "detail", value: "stale_attempt"})
			} else {
				// These NULLs are accepted-state shape-valid, so a refusal is a
				// history guard rather than credit for the required-field CHECK.
				mutations = append(mutations, struct {
					name, field string
					value       any
				}{name: "clear original deadline", field: "observe_deadline", value: nil})
			}
			if attention != nil {
				mutations = append(mutations,
					struct {
						name, field string
						value       any
					}{name: "regress attention history", field: "attention_at", value: f.at.Add(14 * time.Minute)},
					struct {
						name, field string
						value       any
					}{name: "rewrite attention history forward", field: "attention_at", value: f.at.Add(16 * time.Minute)})
				if tt.target == "accepted" {
					mutations = append(mutations, struct {
						name, field string
						value       any
					}{name: "clear attention history", field: "attention_at", value: nil})
				}
			}
			before := callerSendSchemaSnapshot(t, ctx, tx, owner, f)
			for _, mutation := range mutations {
				t.Run(mutation.name, func(t *testing.T) {
					candidate := make(map[string]any, len(values))
					for key, value := range values {
						candidate[key] = value
					}
					candidate[mutation.field] = mutation.value
					err := callerSendSchemaProbe(t, ctx, tx, func(probe pgx.Tx) error {
						return callerSendReviewApplyHistory(ctx, probe, owner, f, candidate)
					})
					callerSendSchemaSQLState(t, err, "55000", "a cross-state or pointer-detail transition cannot erase or rewrite retained attempt observation history")
					require.Equal(t, before, callerSendSchemaSnapshot(t, ctx, tx, owner, f), "the refused history mutation must preserve the exact entire owner snapshot")
				})
			}
			require.NoError(t, callerSendReviewApplyHistory(ctx, tx, owner, f, values), "the valid forward transition must preserve history without blocking legitimate attention or observed settlement")
			var after map[string]any
			require.NoError(t, json.Unmarshal(callerSendSchemaSnapshot(t, ctx, tx, owner, f), &after), "read the actual forward transition before checking retained attempt identity")
			require.Equal(t, tt.target, after["startup_send_state"], "the permitted transition must actually install its requested state")
			var oldRow map[string]any
			require.NoError(t, json.Unmarshal(before, &oldRow), "decode the exact cross-state attempt baseline")
			for _, field := range []string{"startup_send_attempt_id", "startup_send_attempt_seq", "startup_send_attempted_at", "startup_send_observe_deadline"} {
				require.Equal(t, oldRow[field], after[field], "permitted forward transitions retain exact original attempt and deadline correlation")
			}
			if attention != nil {
				require.Equal(t, oldRow["startup_send_attention_at"], after["startup_send_attention_at"], "observed settlement and pointer-detail entry retain the original non-NULL attention timestamp")
			}
			var actualObserved time.Time
			var actualDue *time.Time
			var actualDetail, actualResolution *string
			var actualMessage *int64
			require.NoError(t, tx.QueryRow(ctx, `SELECT startup_send_observed_at,startup_send_due_at,startup_send_detail,startup_send_resolution,startup_send_message_id FROM session_review_loops WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.session, f.owner).Scan(&actualObserved, &actualDue, &actualDetail, &actualResolution, &actualMessage), "read the actual forward observation and settlement fields rather than rely on a same-state no-op")
			require.Equal(t, f.at.Add(18*time.Minute), actualObserved.UTC(), "the allowed forward transition must install the exact newer positive observation")
			if tt.target == "attention" {
				require.NotNil(t, actualDue, "unresolved attention must retain a future scheduled observation")
				require.Equal(t, f.at.Add(2*time.Hour), actualDue.UTC(), "the valid attention transition must advance to its exact requested due time")
				require.NotNil(t, actualDetail, "attention must retain its finite reason")
				require.Equal(t, tt.detail, *actualDetail, "pointer-detail entry must actually install its positively evidenced subtype")
				require.Nil(t, actualResolution, "attention does not manufacture a settled send resolution")
				require.Nil(t, actualMessage, "attention does not manufacture a receipt pointer")
			} else {
				require.Nil(t, actualDue, "observed acceptance may legitimately clear future observation work")
				require.Nil(t, actualDetail, "observed acceptance may legitimately clear its current uncertainty detail")
				require.NotNil(t, actualResolution, "positive observed acceptance must install its exact settlement resolution")
				require.Equal(t, tt.resolution, *actualResolution, "observed settlement retains the requested automated or manual proof kind")
				require.NotNil(t, actualMessage, "positive observed acceptance must retain a message pointer")
				require.Equal(t, messageID, *actualMessage, "the allowed forward transition must install the exact observed message without inventing a different bound-pass receipt")
			}
		})
	}
}

func callerSendReviewApplyHistory(ctx context.Context, tx pgx.Tx, owner callerSendSchemaOwner, f callerSendSchemaFixture, values map[string]any) error {
	fields := []string{"state", "detail", "due_at", "attention_at", "observed_at", "observe_deadline", "message_id", "resolution"}
	set, args := make([]string, len(fields)), make([]any, len(fields))
	for i, field := range fields {
		set[i], args[i] = owner.prefix+field+fmt.Sprintf("=$%d", i+4), values[field]
	}
	query := strings.Join(set, ",") + "," + owner.prefix + "writer_seq=" + owner.prefix + "writer_seq+1"
	_, err := callerSendSchemaUpdate(ctx, tx, owner, f, query, args...)
	return err
}

func callerSendReviewRollbackScans(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Run("assertion omits redundant hot table scans", func(t *testing.T) {
		var definition string
		require.NoError(t, pool.QueryRow(ctx, `SELECT pg_get_functiondef('caller143_assert_send_rollback_safe()'::regprocedure)`).Scan(&definition), "inspect the actual installed rollback assertion instead of a copied SQL implementation")
		for _, table := range []string{"automation_runs", "session_publications", "caller_send_command_stamps"} {
			t.Run(table, func(t *testing.T) {
				pattern := `(?i)\b(?:FROM|JOIN)\s+(?:public\.)?"?` + table + `"?\b`
				require.NotRegexp(t, pattern, definition, "the locked rollback assertion must not redundantly scan this hot table after its owner/start invariant already covers unresolved state")
			})
		}
	})
	tests := []struct {
		name, kind string
		settled    bool
	}{
		{name: "unresolved dispatch owner", kind: "dispatch"},
		{name: "unresolved startup owner", kind: "startup"},
		{name: "unresolved stamped dispatch", kind: "dispatch stamp"},
		{name: "unresolved stamped startup", kind: "startup stamp"},
		{name: "prepared manual start", kind: "manual"},
		{name: "prepared automation caller", kind: "automation"},
		{name: "prepared publication caller", kind: "publication"},
		{name: "created unresolved automation caller", kind: "created automation"},
		{name: "created unresolved publication caller", kind: "created publication"},
		{name: "settled stamped dispatch", kind: "dispatch stamp", settled: true},
		{name: "settled stamped startup", kind: "startup stamp", settled: true},
		{name: "created settled automation caller", kind: "created automation", settled: true},
		{name: "created settled publication caller", kind: "created publication", settled: true},
		{name: "known refused automation creation", kind: "automation", settled: true},
		{name: "known refused publication creation", kind: "publication", settled: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx := callerSendSchemaBegin(t, ctx, pool)
			f := callerSendSchemaSeed(t, ctx, tx)
			owner := callerSendSchemaOwners()[0]
			switch tt.kind {
			case "dispatch", "dispatch stamp", "startup", "startup stamp":
				if tt.kind == "startup" || tt.kind == "startup stamp" {
					owner = callerSendSchemaOwners()[1]
				}
				state := "unknown"
				if tt.settled {
					state = "accepted"
				}
				callerSendSchemaReachOwner(t, ctx, tx, owner, f, state)
				if !tt.settled {
					set := `status='timed_out',dispatch_writer_seq=dispatch_writer_seq+1`
					if owner.loop {
						set = `status='failed',startup_send_writer_seq=startup_send_writer_seq+1`
					}
					_, err := callerSendSchemaUpdate(ctx, tx, owner, f, set)
					require.NoError(t, err, "an aware ordinary terminal writer must not remove unresolved send ownership from rollback discovery")
				}
				if tt.kind == "dispatch stamp" || tt.kind == "startup stamp" {
					purpose := "code-review-reviewer-dispatch"
					if owner.loop {
						purpose = "review-loop-initial-send"
					}
					_, err := tx.Exec(ctx, `INSERT INTO caller_send_command_stamps(org_id,session_id,thread_id,client_message_id,purpose,owner_id,version,digest,message_id,created_at) VALUES($1,$2,$3,$4,$5,$6,1,repeat('a',64),123,$7)`, f.org, f.session, f.thread, owner.keyPrefix+f.owner.String(), purpose, f.owner, f.at)
					require.NoError(t, err, "retain a real matching stamp to prove rollback discovery is subsumed by its exact unresolved owner")
				}
			default:
				kind, created := tt.kind, false
				if kind == "created automation" {
					kind, created = "automation", true
				} else if kind == "created publication" {
					kind, created = "publication", true
				}
				var table string
				var callerID, caller uuid.UUID
				if kind != "manual" {
					table, callerID, caller = callerSendSchemaCaller(t, ctx, tx, f, kind)
				}
				require.NoError(t, callerSendSchemaInsertMap(ctx, tx, "review_loop_start_intents", callerSendSchemaStartRow(f, kind, caller)), "retain the original start independently of hot caller-table scans")
				if table != "" {
					_, err := tx.Exec(ctx, `UPDATE `+pgx.Identifier{table}.Sanitize()+` SET review_start_id=$3 WHERE org_id=$1 AND id=$2`, f.org, callerID, f.owner)
					require.NoError(t, err, "bind the real scoped caller so the rollback refusal is not explained by an absent relation")
				}
				if created {
					state := "unknown"
					if tt.settled {
						state = "accepted"
					}
					callerSendSchemaReachOwner(t, ctx, tx, callerSendSchemaOwners()[1], f, state)
					if !tt.settled {
						_, err := callerSendSchemaUpdate(ctx, tx, callerSendSchemaOwners()[1], f, `status='failed',startup_send_writer_seq=startup_send_writer_seq+1`)
						require.NoError(t, err, "created caller retention must survive an aware ordinary terminal loop status")
					}
				} else if tt.settled {
					_, err := tx.Exec(ctx, `UPDATE review_loop_start_intents SET state='refused',due_at=NULL,detail='session_unavailable' WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.session, f.owner)
					require.NoError(t, err, "a positively known before-creation refusal supplies the settled-start control")
				}
			}
			before := callerSendSchemaAllRows(t, ctx, tx, f.org)
			err := callerSendSchemaProbe(t, ctx, tx, func(probe pgx.Tx) error {
				_, err := probe.Exec(ctx, callerSendSchemaMigration(t, 389, "down"))
				if err == nil && tt.settled {
					var ready, enabled bool
					require.NoError(t, probe.QueryRow(ctx, `SELECT schema_ready,intents_enabled FROM caller_send_intent_control WHERE singleton`).Scan(&ready, &enabled), "read both gates inside the actual successful highest-down transaction")
					require.Equal(t, [2]bool{}, [2]bool{ready, enabled}, "safe settled rollback disables readiness and participation before releasing its locks")
				}
				return err
			})
			if tt.settled {
				require.NoError(t, err, "removing redundant scans must not turn settled historical caller/stamp rows into permanent rollback ownership")
			} else {
				callerSendSchemaSQLState(t, err, "55000", "the real ordered highest rollback must still discover every unresolved owner or retained start scope")
			}
			require.Equal(t, before, callerSendSchemaAllRows(t, ctx, tx, f.org), "savepoint rollback must retain exact owner, start, caller, stamp, and control evidence after either result")
		})
	}
}

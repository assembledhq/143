package db

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/assembledhq/143/internal/cache"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// This is the retained version-0 PR owner, not canonical Run. Its caller-context
// budget and provider-under-lock contract remain unchanged until A.5/G. Cases
// are sequential because one disposable database and its control singleton are
// shared. No fixture performs a send or creates an accepted-send receipt.
//
//nolint:paralleltest // Subcases share database-wide constraints and update the same fixture bindings.
func TestCodeReviewLegacyPRMetadataCompositionPostgres(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	pool, _, org, in := codeReviewDispatchMetadataPGPool(t, ctx, 2)
	_, err := pool.Exec(ctx, `ALTER TABLE code_review_pr_state ADD CONSTRAINT synthetic_legacy_pr_final_write CHECK(generation<9)`)
	require.NoError(t, err, "install a synthetic refusal at the actual owner's final schedule UPDATE, not at terminal metadata setup")
	tests := []struct {
		name, method string
		lateFailure  bool
		isolation    pgx.TxIsoLevel
	}{
		{name: "FailReview known commit", method: "fail"},
		{name: "FailReview late final-write rollback", method: "fail", lateFailure: true},
		{name: "FailReviewWithStatus known commit", method: "status"},
		{name: "FailReviewWithStatus late final-write rollback", method: "status", lateFailure: true},
		{name: "MarkStale known commit", method: "stale"},
		{name: "MarkStale late final-write rollback", method: "stale", lateFailure: true},
		{name: "unsupported outer Repeatable Read rolls back", method: "fail", isolation: pgx.RepeatableRead},
	}
	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// These leaves intentionally share the database-wide test-only CHECK.
			caseCtx, caseCancel := context.WithTimeout(ctx, 45*time.Second)
			defer caseCancel()
			if index != 0 {
				org, in = codeReviewDispatchPreparePGFixture(t, caseCtx, pool)
			}
			fixture := codeReviewLegacyPRSeed(t, caseCtx, pool, org, in)
			observer := codeReviewDispatchMetadataPGObserver(t, caseCtx, pool)
			baseline := codeReviewLegacyPRGraphAt(t, caseCtx, observer, fixture.orgs)
			owner := &codeReviewLegacyPRCountingOwner{Pool: pool, isolation: tt.isolation}
			store := NewCodeReviewScheduleStore(owner)
			mr := miniredis.RunT(t)
			client := cache.New(cache.Config{Topology: "standalone", URL: "redis://" + mr.Addr()}, zerolog.Nop(), nil)
			require.NotNil(t, client, "use an isolated real cache client for existing scheduler publication only")
			t.Cleanup(func() { require.NoError(t, client.Close(), "close only the isolated synthetic cache client") })
			streams := cache.NewCodeReviewStreams(client, zerolog.Nop())
			require.NoError(t, streams.PublishUpdated(caseCtx, org, models.CodeReviewUpdatedEvent{OrgID: org}), "prime only the local cache connection before the observed owner begins")
			store.SetStreams(streams, zerolog.Nop())
			commands := mr.CommandCount()
			acquisitions := pool.Stat().AcquireCount()
			var provisional codeReviewLegacyPRGraph
			var txClock, beforeTerminal, afterTerminal time.Time
			var returned []models.CodeReviewSessionMetadata
			err := store.WithLockedPR(caseCtx, org, fixture.repo, fixture.pr, func(tx pgx.Tx, state *models.CodeReviewPRState) error {
				require.Equal(t, int32(2), pool.Stat().AcquiredConns(), "one held observer and the sole real PR owner must saturate the entire pool")
				require.Equal(t, int32(0), pool.Stat().IdleConns(), "terminal composition must have no spare checkout for an accidental nested owner")
				var actualIsolation string
				require.NoError(t, tx.QueryRow(caseCtx, `SELECT current_setting('transaction_isolation'),transaction_timestamp(),statement_timestamp()`).Scan(&actualIsolation, &txClock, &beforeTerminal), "observe this exact legacy outer transaction's real isolation and database clocks")
				wantIsolation := string(pgx.ReadCommitted)
				if tt.isolation != "" {
					wantIsolation = string(tt.isolation)
				}
				require.Equal(t, wantIsolation, actualIsolation, "the facade must neither configure nor silently replace its supplied transaction")
				terminal, err := NewLegacyPRCodeReviewMetadataInTx(tx)
				if err != nil {
					return err
				}
				switch tt.method {
				case "fail":
					value, terminalErr := terminal.FailReview(caseCtx, org, in.SessionID, "  synthetic legacy failure  ")
					err, returned = terminalErr, []models.CodeReviewSessionMetadata{value}
				case "status":
					value, terminalErr := terminal.FailReviewWithStatus(caseCtx, org, FailCodeReviewParams{SessionID: in.SessionID, Reason: "  synthetic typed failure  ", Code: models.CodeReviewStatusCodeReviewerFailed, Message: "  Synthetic typed message.  ", Retryable: true})
					err, returned = terminalErr, []models.CodeReviewSessionMetadata{value}
				case "stale":
					returned, err = terminal.MarkStaleForPullRequestExceptHead(caseCtx, org, fixture.pr, "synthetic-current-head", &fixture.sibling)
				}
				if err != nil {
					return err
				}
				require.NoError(t, tx.QueryRow(caseCtx, `SELECT statement_timestamp()`).Scan(&afterTerminal), "bound the pending-only cancellation clock using the actual supplied backend")
				provisional = codeReviewLegacyPRGraphAt(t, caseCtx, tx, fixture.orgs)
				expected := codeReviewLegacyPRExpected(t, baseline, provisional, fixture, tt.method, txClock, beforeTerminal, afterTerminal)
				require.Equal(t, expected, provisional, "the real facade may change only exact terminal fields and original pending intent, before any outer settlement")
				require.Equal(t, baseline, codeReviewLegacyPRGraphAt(t, caseCtx, observer, fixture.orgs), "the held observer must see none of the provisional metadata, pending cancellation or staged owner state")
				require.Equal(t, commands, mr.CommandCount(), "neither the facade nor the legacy callback may publish a scheduler or metadata notification before known commit")
				state.Generation = 4
				if tt.lateFailure {
					state.Generation = 9
				}
				state.HeadSHA = "synthetic-final-head"
				return nil
			})
			rollback := tt.lateFailure || tt.isolation != ""
			if tt.isolation != "" {
				require.ErrorIs(t, err, ErrCodeReviewMetadataTerminalIsolation, "unsupported isolation must propagate through the actual legacy owner without fallback or partial work")
				require.Nil(t, provisional, "isolation refusal must occur before provisional terminal graph observation")
			} else if tt.lateFailure {
				var cause *pgconn.PgError
				require.True(t, errors.As(err, &cause), "the operation must reach the real final-state SQL refusal after actual terminal mutation")
				require.Equal(t, "23514", cause.Code, "the intended late CHECK refusal must be the actual failure, not missing terminal capability")
				require.Equal(t, "synthetic_legacy_pr_final_write", cause.ConstraintName, "the entire staged graph must roll back at the named outer final write")
				require.NotNil(t, provisional, "the late failure must follow actual terminal metadata and pending cancellation")
			} else {
				require.NoError(t, err, "the real retained WithLockedPR owner must commit its terminal metadata and schedule graph together")
			}
			actual := codeReviewLegacyPRGraphAt(t, caseCtx, observer, fixture.orgs)
			if rollback {
				require.Equal(t, baseline, actual, "outer refusal must restore every scoped and foreign row, immutable command, original wake, history and clock exactly")
				require.Equal(t, commands, mr.CommandCount(), "a failed outer unit cannot authorize any scheduling notification or replay")
			} else {
				expected := codeReviewLegacyPRExpected(t, baseline, actual, fixture, tt.method, txClock, beforeTerminal, afterTerminal)
				state := codeReviewLegacyPRFind(t, expected["schedule"], fixture.state)
				state["generation"] = codeReviewDispatchMetadataPGJSON(t, 4)
				state["head_sha"] = codeReviewDispatchMetadataPGJSON(t, "synthetic-final-head")
				observedState := codeReviewLegacyPRFind(t, actual["schedule"], fixture.state)
				require.True(t, codeReviewDispatchMetadataPGTime(t, observedState["updated_at"]).Equal(txClock), "the existing final-state write must retain the exact outer transaction clock")
				state["updated_at"] = observedState["updated_at"]
				require.Equal(t, expected, actual, "known commit must expose only the exact terminal/pending changes plus the final owner generation, head and transaction clock")
				require.Equal(t, commands+1, mr.CommandCount(), "only the existing scheduling update may publish once after known successful outer commit")
				require.Equal(t, []uuid.UUID{in.MetadataID}, codeReviewLegacyPRReturnedIDs(returned), "each facade method must return the exact original metadata owner rather than a sibling or foreign canary")
				var expectedMetadata models.CodeReviewSessionMetadata
				require.NoError(t, json.Unmarshal(codeReviewDispatchMetadataPGJSON(t, codeReviewLegacyPRFind(t, actual["metadata"], in.MetadataID)), &expectedMetadata), "decode the complete independently committed metadata row into its original public model")
				require.JSONEq(t, string(codeReviewDispatchMetadataPGJSON(t, expectedMetadata)), string(codeReviewDispatchMetadataPGJSON(t, returned[0])), "the facade's returned public metadata must match every original public field of the independently committed row")
			}
			wantCommits := 1
			if rollback {
				wantCommits = 0
			}
			require.Equal(t, [4]int{1, wantCommits, 1, 0}, owner.counts, "only the original owner may Begin, attempt Commit and deferred Rollback; terminal work may not begin a savepoint or settle anything")
			require.Equal(t, acquisitions+1, pool.Stat().AcquireCount(), "the complete legacy operation must acquire exactly one owner checkout while its observer remains held")
		})
	}
}

type codeReviewLegacyPRCountingOwner struct {
	*pgxpool.Pool
	isolation pgx.TxIsoLevel
	counts    [4]int // outer Begin, Commit, Rollback, nested Begin
}

func (o *codeReviewLegacyPRCountingOwner) Begin(ctx context.Context) (pgx.Tx, error) {
	o.counts[0]++
	var tx pgx.Tx
	var err error
	if o.isolation == "" {
		tx, err = o.Pool.Begin(ctx)
	} else {
		tx, err = o.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: o.isolation})
	}
	if err != nil {
		return nil, err
	}
	return &codeReviewLegacyPRCountingTx{Tx: tx, owner: o}, nil
}

type codeReviewLegacyPRCountingTx struct {
	pgx.Tx
	owner *codeReviewLegacyPRCountingOwner
}

func (tx *codeReviewLegacyPRCountingTx) Begin(ctx context.Context) (pgx.Tx, error) {
	tx.owner.counts[3]++
	return tx.Tx.Begin(ctx)
}
func (tx *codeReviewLegacyPRCountingTx) Commit(ctx context.Context) error {
	tx.owner.counts[1]++
	return tx.Tx.Commit(ctx)
}
func (tx *codeReviewLegacyPRCountingTx) Rollback(ctx context.Context) error {
	tx.owner.counts[2]++
	return tx.Tx.Rollback(ctx)
}

type codeReviewLegacyPRFixture struct {
	orgs                     []uuid.UUID
	in                       CodeReviewDispatchPreparation
	repo, pr, state, sibling uuid.UUID
}

func codeReviewLegacyPRSeed(t *testing.T, ctx context.Context, pool *pgxpool.Pool, org uuid.UUID, in CodeReviewDispatchPreparation) codeReviewLegacyPRFixture {
	t.Helper()
	f := codeReviewLegacyPRFixture{orgs: []uuid.UUID{org}, in: in}
	var policy uuid.UUID
	require.NoError(t, pool.QueryRow(ctx, `SELECT repository_id,pull_request_id,policy_id FROM code_review_session_metadata WHERE org_id=$1 AND id=$2`, org, in.MetadataID).Scan(&f.repo, &f.pr, &policy), "resolve actual seeded repository/PR ownership rather than inventing a PR-lock identity")
	foreignOrg, foreign := codeReviewDispatchPreparePGFixture(t, ctx, pool)
	f.orgs = append(f.orgs, foreignOrg)
	tx := callerSendStampPGBegin(t, ctx, pool, false)
	_, siblingSession, siblingThread := callerSendStampPGSession(t, ctx, tx, org)
	f.sibling = siblingSession
	siblingPR, siblingMetadata := uuid.New(), uuid.New()
	_, err := tx.Exec(ctx, `INSERT INTO pull_requests(id,org_id,session_id,github_pr_number,github_pr_url,github_repo,title,head_sha,base_sha) SELECT $1,$2,$3,2,'https://example.invalid/pr/2',github_repo,'Synthetic sibling PR','synthetic-head','synthetic-base' FROM pull_requests WHERE org_id=$2 AND id=$4`, siblingPR, org, siblingSession, f.pr)
	require.NoError(t, err, "seed a same-tenant sibling owner with genuine repository-name PR ownership but a different PR identity")
	_, err = tx.Exec(ctx, `UPDATE sessions SET origin='code_review' WHERE org_id=$1 AND id=$2`, org, siblingSession)
	require.NoError(t, err, "retain the actual sibling's code-review session origin")
	_, err = tx.Exec(ctx, `INSERT INTO code_review_session_metadata(id,org_id,session_id,repository_id,pull_request_id,policy_id,base_sha,head_sha,trigger_source,status,review_output_key) VALUES($3,$1,$2,$4,$5,$6,'synthetic-base','synthetic-head','slash_command','running',$3::uuid::text)`, org, siblingSession, siblingMetadata, f.repo, siblingPR, policy)
	require.NoError(t, err, "seed sibling metadata through the unmodified real schema")
	sibling := in
	sibling.ResultID, sibling.SessionID, sibling.ThreadID, sibling.MetadataID = uuid.New(), siblingSession, siblingThread, siblingMetadata
	sibling.Key, sibling.ReviewOutputKey = "cr-dispatch:v1:"+sibling.ResultID.String(), siblingMetadata.String()
	codeReviewDispatchPreparationJSON(t, org, &sibling)
	for _, scoped := range []struct {
		org uuid.UUID
		in  CodeReviewDispatchPreparation
	}{{org, in}, {org, sibling}, {foreignOrg, foreign}} {
		_, prepareErr := PrepareCodeReviewDispatchInTx(ctx, scoped.org, tx, scoped.in)
		require.NoError(t, prepareErr, "install each actual retained pending result and its frozen original wake with the production preparation helper")
	}
	for _, state := range []string{"attempting", "unknown"} {
		history := in
		history.ResultID = uuid.New()
		history.Key = "cr-dispatch:v1:" + history.ResultID.String()
		codeReviewDispatchPreparationJSON(t, org, &history)
		_, err = PrepareCodeReviewDispatchInTx(ctx, org, tx, history)
		require.NoError(t, err, "first install every historical canary as a genuine fresh pending result, never by disabling the schema guard")
		_, err = tx.Exec(ctx, `UPDATE code_review_agent_results SET dispatch_state='attempting',dispatch_attempt_id=$3,dispatch_attempt_seq=1,dispatch_attempted_at=statement_timestamp(),dispatch_due_at=statement_timestamp()+interval '1 hour',dispatch_writer_seq=dispatch_writer_seq+1,dispatch_updated_at=statement_timestamp() WHERE org_id=$1 AND id=$2`, org, history.ResultID, uuid.New())
		require.NoError(t, err, "construct an explicitly synthetic legal attempted history without claiming a provider call or accepted receipt")
		if state == "unknown" {
			_, err = tx.Exec(ctx, `UPDATE code_review_agent_results SET dispatch_state='unknown',dispatch_detail='unknown_ack',dispatch_due_at=statement_timestamp()+interval '1 minute',dispatch_observe_deadline=dispatch_attempted_at+interval '15 minutes',dispatch_writer_seq=dispatch_writer_seq+1,dispatch_updated_at=statement_timestamp() WHERE org_id=$1 AND id=$2`, org, history.ResultID)
			require.NoError(t, err, "retain an actual schema-valid unknown canary whose original attempt and observation deadline must never be cancelled")
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO code_review_agent_results(id,org_id,session_id,agent_provider,role,status,raw_output,structured_result) VALUES($1,$2,$3,$4,'reviewer','completed','Synthetic completed legacy business output','{"retain":"completed legacy history"}')`, uuid.New(), org, in.SessionID, in.AgentProvider)
	require.NoError(t, err, "retain completed legacy business history without dispatch fields or any fabricated accepted-send receipt")
	_, err = tx.Exec(ctx, `UPDATE code_review_session_metadata SET phase='reviewing',decision='blocked',acceptable=false,failure_reason='Synthetic earlier reason',status_code='github_unavailable',status_message='Synthetic earlier message',retry_at=now()+interval '1 hour',last_error_at=now()-interval '1 minute',retryable_failure=true WHERE org_id=$1 AND id=$2`, org, in.MetadataID)
	require.NoError(t, err, "seed material terminal-field canaries before the observed owner begins")
	require.NoError(t, tx.Commit(ctx), "commit only synthetic setup before the separately observed real legacy owner begins")
	require.NoError(t, NewCodeReviewScheduleStore(pool).WithLockedPR(ctx, org, f.repo, f.pr, func(_ pgx.Tx, state *models.CodeReviewPRState) error {
		state.Generation = 3
		state.HeadSHA = "synthetic-original-head"
		return nil
	}), "create the baseline PR reservation through its real ownership validation and outer commit")
	require.NoError(t, pool.QueryRow(ctx, `SELECT id FROM code_review_pr_state WHERE org_id=$1 AND pull_request_id=$2`, org, f.pr).Scan(&f.state), "retain the exact actual scheduling row for complete graph comparison")
	return f
}

type codeReviewLegacyPRGraph map[string][]map[string]json.RawMessage

func codeReviewLegacyPRGraphAt(t *testing.T, ctx context.Context, q DBTX, orgs []uuid.UUID) codeReviewLegacyPRGraph {
	t.Helper()
	var raw json.RawMessage
	require.NoError(t, q.QueryRow(ctx, `SELECT jsonb_build_object(
	'metadata',(SELECT COALESCE(jsonb_agg(to_jsonb(x) ORDER BY x.id),'[]') FROM code_review_session_metadata x WHERE org_id=ANY($1::uuid[])),
	'results',(SELECT COALESCE(jsonb_agg(to_jsonb(x) ORDER BY x.id),'[]') FROM code_review_agent_results x WHERE org_id=ANY($1::uuid[])),
	'jobs',(SELECT COALESCE(jsonb_agg(to_jsonb(x) ORDER BY x.id),'[]') FROM jobs x WHERE org_id=ANY($1::uuid[])),
	'sessions',(SELECT COALESCE(jsonb_agg(to_jsonb(x) ORDER BY x.id),'[]') FROM sessions x WHERE org_id=ANY($1::uuid[])),
	'threads',(SELECT COALESCE(jsonb_agg(to_jsonb(x) ORDER BY x.id),'[]') FROM session_threads x WHERE org_id=ANY($1::uuid[])),
	'schedule',(SELECT COALESCE(jsonb_agg(to_jsonb(x) ORDER BY x.id),'[]') FROM code_review_pr_state x WHERE org_id=ANY($1::uuid[])),
	'requests',(SELECT COALESCE(jsonb_agg(to_jsonb(x) ORDER BY x.id),'[]') FROM code_review_requests x WHERE org_id=ANY($1::uuid[])),
	'assessments',(SELECT COALESCE(jsonb_agg(to_jsonb(x) ORDER BY x.id),'[]') FROM code_review_revision_assessments x WHERE org_id=ANY($1::uuid[])),
	'findings',(SELECT COALESCE(jsonb_agg(to_jsonb(x) ORDER BY x.id),'[]') FROM code_review_findings x WHERE org_id=ANY($1::uuid[])),
	'pull_requests',(SELECT COALESCE(jsonb_agg(to_jsonb(x) ORDER BY x.id),'[]') FROM pull_requests x WHERE org_id=ANY($1::uuid[])),
	'repositories',(SELECT COALESCE(jsonb_agg(to_jsonb(x) ORDER BY x.id),'[]') FROM repositories x WHERE org_id=ANY($1::uuid[])),
	'messages',(SELECT COALESCE(jsonb_agg(to_jsonb(x) ORDER BY x.id),'[]') FROM session_messages x WHERE org_id=ANY($1::uuid[])),
	'inbox',(SELECT COALESCE(jsonb_agg(to_jsonb(x) ORDER BY x.id),'[]') FROM thread_inbox_entries x WHERE org_id=ANY($1::uuid[])),
	'stamps',(SELECT COALESCE(jsonb_agg(to_jsonb(x) ORDER BY x.client_message_id),'[]') FROM caller_send_command_stamps x WHERE org_id=ANY($1::uuid[])))`, orgs).Scan(&raw), "capture complete original and canary graphs, not counts or selected business fields")
	var graph codeReviewLegacyPRGraph
	require.NoError(t, json.Unmarshal(raw, &graph), "decode complete synthetic snapshots without floating-point coercion of identifiers or clocks")
	return graph
}

func codeReviewLegacyPRFind(t *testing.T, rows []map[string]json.RawMessage, id uuid.UUID) map[string]json.RawMessage {
	t.Helper()
	for _, row := range rows {
		if string(row["id"]) == `"`+id.String()+`"` {
			return row
		}
	}
	require.FailNow(t, "the exact original owner row must be retained in the complete graph")
	return nil
}

func codeReviewLegacyPRExpected(t *testing.T, baseline, observed codeReviewLegacyPRGraph, f codeReviewLegacyPRFixture, method string, txClock, lower, upper time.Time) codeReviewLegacyPRGraph {
	t.Helper()
	// Preserve every original RawMessage byte. A JSON marshal round trip
	// compacts nested JSONB values and would change untouched payload spelling.
	expected := make(codeReviewLegacyPRGraph, len(baseline))
	for table, rows := range baseline {
		copiedRows := make([]map[string]json.RawMessage, len(rows))
		for index, row := range rows {
			copiedRows[index] = make(map[string]json.RawMessage, len(row))
			for key, value := range row {
				copiedRows[index][key] = append(json.RawMessage(nil), value...)
			}
		}
		expected[table] = copiedRows
	}
	m := codeReviewLegacyPRFind(t, expected["metadata"], f.in.MetadataID)
	actualMetadata := codeReviewLegacyPRFind(t, observed["metadata"], f.in.MetadataID)
	m["phase"], m["retry_at"] = json.RawMessage("null"), json.RawMessage("null")
	require.True(t, codeReviewDispatchMetadataPGTime(t, actualMetadata["completed_at"]).Equal(txClock), "terminal completion must use the exact retained outer transaction clock")
	m["completed_at"] = actualMetadata["completed_at"]
	if method == "stale" {
		m["status"], m["stale"] = codeReviewDispatchMetadataPGJSON(t, "stale"), json.RawMessage("true")
		for _, key := range []string{"status_code", "status_message", "last_error_at"} {
			m[key] = json.RawMessage("null")
		}
		m["retryable_failure"] = json.RawMessage("false")
		m["superseded_by_session_id"] = codeReviewDispatchMetadataPGJSON(t, f.sibling)
	} else {
		m["status"] = codeReviewDispatchMetadataPGJSON(t, "failed")
		m["decision"], m["acceptable"] = json.RawMessage("null"), json.RawMessage("null")
		require.True(t, codeReviewDispatchMetadataPGTime(t, actualMetadata["last_error_at"]).Equal(txClock), "failure error clock must equal this exact supplied transaction's completion clock")
		m["last_error_at"] = actualMetadata["last_error_at"]
		reason, code, message, retryable := "synthetic legacy failure", models.CodeReviewStatusCodeWorkerFailed, "The code review stopped before it could finish.", false
		if method == "status" {
			reason, code, message, retryable = "synthetic typed failure", models.CodeReviewStatusCodeReviewerFailed, "Synthetic typed message.", true
		}
		m["failure_reason"], m["status_code"], m["status_message"], m["retryable_failure"] = codeReviewDispatchMetadataPGJSON(t, reason), codeReviewDispatchMetadataPGJSON(t, code), codeReviewDispatchMetadataPGJSON(t, message), codeReviewDispatchMetadataPGJSON(t, retryable)
	}
	r := codeReviewLegacyPRFind(t, expected["results"], f.in.ResultID)
	actual := codeReviewLegacyPRFind(t, observed["results"], f.in.ResultID)
	clock := codeReviewDispatchMetadataPGTime(t, actual["dispatch_updated_at"])
	require.False(t, clock.Before(lower), "pending cancellation must have a real statement clock at or after the owner began terminal work")
	require.False(t, clock.After(upper), "pending cancellation clock must precede the next actual supplied-backend statement")
	r["dispatch_state"], r["dispatch_resolution"], r["dispatch_due_at"], r["dispatch_writer_seq"], r["dispatch_updated_at"] = codeReviewDispatchMetadataPGJSON(t, "cancelled"), codeReviewDispatchMetadataPGJSON(t, "owner_cancelled"), json.RawMessage("null"), json.RawMessage("1"), actual["dispatch_updated_at"]
	return expected
}

func codeReviewLegacyPRReturnedIDs(metadata []models.CodeReviewSessionMetadata) []uuid.UUID {
	ids := make([]uuid.UUID, len(metadata))
	for i := range metadata {
		ids[i] = metadata[i].ID
	}
	return ids
}

package db

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/alicebob/miniredis/v2/server"
	"github.com/assembledhq/143/internal/cache"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type assessmentSettlementFixture struct {
	completion              models.CodeReviewRecheckTurnCompletion
	repositoryID, prID      uuid.UUID
	requestID, baselineID   uuid.UUID
	pending, differentOwner bool
}

func TestAssessmentSettlementFullSchemaPostgres(t *testing.T) {
	t.Parallel()
	pool := newSessionPersistencePostgresPool(t)
	installAssessmentSettlementLateFailure(t, pool)
	for _, tt := range []struct {
		name   string
		commit bool
	}{
		{name: "completed commit", commit: true},
		{name: "caller rollback"},
		{name: "late owner failure"},
		{name: "foreign org"},
		{name: "different assessment owner", commit: true},
		{name: "pending intent", commit: true},
		{name: "failed fallback", commit: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			fixture := seedAssessmentSettlement(t, ctx, pool, tt.name)
			before := requireAssessmentSettlementRows(t, ctx, pool, fixture)
			callerPool := newRecheckReconciliationCallerPool(t, ctx, pool)
			owner, err := callerPool.Begin(ctx)
			require.NoError(t, err, "the outer owner occupies the caller pool's only connection")
			defer func() { _ = owner.Rollback(context.Background()) }()
			tx := &sessionPersistenceOwnedTx{Tx: owner}
			var transactionID int64
			var stamp json.RawMessage
			require.NoError(t, owner.QueryRow(ctx, "SELECT txid_current(),to_jsonb(transaction_timestamp())").Scan(&transactionID, &stamp), "capture the owner's exact transaction and SQL timestamp")
			requestedOrg := fixture.completion.OrgID
			if tt.name == "foreign org" {
				requestedOrg = uuid.New()
			} else {
				stageAssessmentSettlementOwner(t, ctx, owner, fixture, tt.name == "failed fallback")
			}
			beforeSettlement := requireAssessmentSettlementRows(t, ctx, owner, fixture)
			if tt.name == "late owner failure" {
				_, err := owner.Exec(ctx, "INSERT INTO synthetic_assessment_settlement_rejections VALUES($1,$2,$3)", fixture.prID, fixture.completion.SessionID, fixture.completion.AssessmentID)
				require.NoError(t, err, "scope the late final scheduler-write failure to this exact synthetic owner")
			}
			publications := newAssessmentSettlementPublications(t, pool, fixture)
			store := NewCodeReviewScheduleStore(callerPool)
			store.SetStreams(publications.streams, zerolog.Nop())
			err = store.SettleAssessmentInTx(ctx, requestedOrg, tx, fixture.completion.AssessmentID)
			require.Empty(t, tx.settlements, "assessment settlement must not acquire, replace, release or abort the supplied transaction")
			require.Empty(t, publications.snapshot(), "even an enabled scheduler publisher must remain silent in every supplied-transaction path")
			require.Equal(t, before, requireAssessmentSettlementRows(t, ctx, pool, fixture), "independent readers must see every original root, assessment, request, scheduler and job row before settlement")
			if tt.name == "late owner failure" {
				require.ErrorContains(t, err, "synthetic late assessment settlement owner failure", "the final scheduler write must fail only after the trigger observes genuine root, assessment and request mutations")
				require.NotEqual(t, before, beforeSettlement, "the caller must have staged actual root and assessment writes before the late failure")
			} else if tt.name == "foreign org" {
				require.ErrorIs(t, err, pgx.ErrNoRows, "a foreign tenant must not discover the assessment's PR identity")
				require.Equal(t, before, requireAssessmentSettlementRows(t, ctx, owner, fixture), "a foreign scope must not stage any mutation even before rollback")
			} else {
				require.NoError(t, err, "the supplied transaction must compose the unchanged settlement policy")
				var afterID int64
				require.NoError(t, owner.QueryRow(ctx, "SELECT txid_current()").Scan(&afterID), "the outer owner must retain its usable transaction after settlement")
				require.Equal(t, transactionID, afterID, "settlement must not change the caller's transaction identity")
				staged := requireAssessmentSettlementRows(t, ctx, owner, fixture)
				requireAssessmentSettlementGraph(t, beforeSettlement, staged, stamp, fixture, tt.name == "failed fallback")
				require.NoError(t, store.SettleAssessmentInTx(ctx, requestedOrg, tx, fixture.completion.AssessmentID), "replaying the same settled assessment must remain harmless inside the original transaction")
				require.Equal(t, staged, requireAssessmentSettlementRows(t, ctx, owner, fixture), "same-transaction replay must preserve every staged pointer, request, clock and durable wake exactly")
				require.Empty(t, tx.settlements, "replay must not acquire or settle another transaction")
				require.Empty(t, publications.snapshot(), "replay must not acquire a new scheduling publication effect")
				if tt.commit {
					require.NoError(t, owner.Commit(ctx), "only the real outer owner commits its assessment/request/scheduler unit")
					require.Equal(t, staged, requireAssessmentSettlementRows(t, ctx, pool, fixture), "commit must expose exactly the graph staged in the original transaction")
					require.Empty(t, publications.snapshot(), "a supplied-tx settlement does not gain a new event even after its caller commits")
					return
				}
			}
			require.NoError(t, owner.Rollback(ctx), "only the outer owner aborts all staged writes after failure or caller rejection")
			require.Equal(t, before, requireAssessmentSettlementRows(t, ctx, pool, fixture), "outer rollback must restore every root, assessment, request, scheduler and wake column exactly")
			require.Empty(t, publications.snapshot(), "rejected settlement must not publish its discarded state")
		})
	}
}

func TestAssessmentSettlementLegacyPublicationFullSchemaPostgres(t *testing.T) {
	t.Parallel()
	pool := newSessionPersistencePostgresPool(t)
	installAssessmentSettlementLateFailure(t, pool)
	for _, tt := range []struct {
		name          string
		late, commit  bool
		commitFailure bool
	}{
		{name: "confirmed wrapper commit", commit: true},
		{name: "late wrapper failure", late: true},
		{name: "wrapper commit failure", commitFailure: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			fixture := seedAssessmentSettlement(t, ctx, pool, tt.name)
			stageAssessmentSettlementOwner(t, ctx, pool, fixture, false)
			if tt.late {
				_, err := pool.Exec(ctx, "INSERT INTO synthetic_assessment_settlement_rejections VALUES($1,$2,$3)", fixture.prID, fixture.completion.SessionID, fixture.completion.AssessmentID)
				require.NoError(t, err, "scope the legacy owner's late final-write failure to this generated graph")
			}
			before := requireAssessmentSettlementRows(t, ctx, pool, fixture)
			callerPool := newRecheckReconciliationCallerPool(t, ctx, pool)
			starter := &assessmentSettlementStarter{Pool: callerPool}
			if tt.commitFailure {
				starter.commitErr = errors.New("synthetic assessment settlement commit failure")
			}
			publications := newAssessmentSettlementPublications(t, pool, fixture)
			store := NewCodeReviewScheduleStore(starter)
			store.SetStreams(publications.streams, zerolog.Nop())
			err := store.SettleAssessment(ctx, fixture.completion.OrgID, fixture.completion.AssessmentID)
			require.Equal(t, 1, starter.begins, "the legacy wrapper must still acquire exactly one outer transaction after its initial identity read")
			require.Zero(t, starter.tx.nestedBegins, "the legacy locked PR and settlement policy must not introduce a nested savepoint")
			after := requireAssessmentSettlementRows(t, ctx, pool, fixture)
			if !tt.commit {
				if tt.commitFailure {
					require.ErrorIs(t, err, starter.commitErr, "an uncertain commit error must be returned unchanged without publishing accepted state")
					require.Equal(t, 1, starter.tx.commits, "commit failure must not cause a retry")
				} else {
					require.ErrorContains(t, err, "synthetic late assessment settlement owner failure", "the final scheduler write must retain the original error")
					require.Zero(t, starter.tx.commits, "failed settlement must not try to commit")
				}
				require.Equal(t, 1, starter.tx.rollbacks, "the unchanged wrapper must roll back its entire rejected transaction")
				require.Equal(t, before, after, "late write and injected commit failures must retain every original settlement row")
				require.Empty(t, publications.snapshot(), "rollback or commit uncertainty must suppress the unchanged post-commit event")
				return
			}
			require.NoError(t, err, "the legacy wrapper must retain successful atomic settlement")
			require.Equal(t, 1, starter.tx.commits, "only the legacy wrapper commits the unit")
			events := publications.snapshot()
			require.Len(t, events, 1, "the legacy wrapper must publish exactly one existing scheduling update after confirmed commit")
			require.NoError(t, events[0].err, "the synchronous publication observer must read committed state without errors")
			require.Equal(t, after, events[0].visible, "publication must see the committed request and scheduler from an independent connection")
			expected := models.CodeReviewUpdatedEvent{OrgID: fixture.completion.OrgID, PullRequestID: &fixture.prID, SchedulingState: models.CodeReviewScheduleCovered, UpdatedAt: events[0].event.UpdatedAt}
			require.False(t, expected.UpdatedAt.IsZero(), "the existing event retains its wall-clock observation timestamp")
			require.Equal(t, expected, events[0].event, "settlement must retain the exact existing event shape without inventing a new effect")
			require.Equal(t, "143:stream:{org:"+fixture.completion.OrgID.String()+"}:code_reviews", events[0].channel, "the existing event remains scoped to this tenant")
		})
	}
}

func seedAssessmentSettlement(t *testing.T, ctx context.Context, pool *pgxpool.Pool, variant string) assessmentSettlementFixture {
	t.Helper()
	fixture := assessmentSettlementFixture{completion: seedRecheckPersistenceCompletion(t, ctx, pool, "head only"), requestID: uuid.New(), pending: variant == "pending intent", differentOwner: variant == "different assessment owner"}
	require.NoError(t, pool.QueryRow(ctx, "SELECT repository_id,pull_request_id,source_assessment_id FROM code_review_revision_assessments WHERE org_id=$1 AND id=$2", fixture.completion.OrgID, fixture.completion.AssessmentID).Scan(&fixture.repositoryID, &fixture.prID, &fixture.baselineID), "resolve the synthetic assessment's exact full-schema relationships")
	active := fixture.completion.AssessmentID
	if fixture.differentOwner {
		active = fixture.baselineID
	}
	_, err := pool.Exec(ctx, `INSERT INTO code_review_requests(id,org_id,repository_id,pull_request_id,source_kind,source_identity,mode,input_hash,target_generation,session_id,assessment_id,status) VALUES($1,$2,$3,$4,'synthetic',$1::uuid::text,'review_now','synthetic-settlement',1,$5,$6,'joined')`, fixture.requestID, fixture.completion.OrgID, fixture.repositoryID, fixture.prID, fixture.completion.SessionID, fixture.completion.AssessmentID)
	require.NoError(t, err, "seed a real joined request bound to only the target assessment")
	_, err = pool.Exec(ctx, `INSERT INTO code_review_pr_state(org_id,repository_id,pull_request_id,active_session_id,active_assessment_id,current_assessment_id,head_sha,base_sha,base_ref,state,wait_reason,retry_at,eligible_at) VALUES($1,$2,$3,$4,$5,$6,'head','base','main','running','active_review',now()+interval '1 hour',now()+interval '2 hours')`, fixture.completion.OrgID, fixture.repositoryID, fixture.prID, fixture.completion.SessionID, active, fixture.completion.AssessmentID)
	require.NoError(t, err, "seed every existing owner/current pointer, comparison head and retry clock")
	if fixture.pending {
		pendingID := uuid.New()
		_, err := pool.Exec(ctx, `INSERT INTO code_review_requests(id,org_id,repository_id,pull_request_id,source_kind,source_identity,mode,input_hash,target_generation,status) VALUES($1,$2,$3,$4,'synthetic',$1::uuid::text,'review_now','synthetic-newer-request',2,'pending')`, pendingID, fixture.completion.OrgID, fixture.repositoryID, fixture.prID)
		require.NoError(t, err, "seed a distinct newer pending request that settlement must retain")
		_, err = pool.Exec(ctx, `UPDATE code_review_pr_state SET pending_request_id=$3,pending_input='{"mode":"review_now"}',first_pending_at=now() WHERE org_id=$1 AND pull_request_id=$2`, fixture.completion.OrgID, fixture.prID, pendingID)
		require.NoError(t, err, "attach the exact newer pending intent without weakening its original clocks")
	}
	_, err = pool.Exec(ctx, `INSERT INTO jobs(org_id,queue,job_type,payload,status,dedupe_key,run_at) VALUES($1,'agent','reconcile_code_review_schedule',jsonb_build_object('org_id',$1::uuid::text,'pull_request_id',$2::uuid::text),'pending',$3,now()+interval '1 hour')`, fixture.completion.OrgID, fixture.prID, "code_review_schedule:"+fixture.prID.String())
	require.NoError(t, err, "retain an existing durable scheduling wake for exact no-rewrite comparisons")
	return fixture
}

func stageAssessmentSettlementOwner(t *testing.T, ctx context.Context, conn DBTX, f assessmentSettlementFixture, failed bool) {
	t.Helper()
	_, err := conn.Exec(ctx, "UPDATE sessions SET status='completed',completed_at=now() WHERE org_id=$1 AND id=$2", f.completion.OrgID, f.completion.SessionID)
	require.NoError(t, err, "stage the outer caller's real parent mutation before composing scheduler settlement")
	status := "completed"
	if failed {
		status = "failed"
	}
	_, err = conn.Exec(ctx, `UPDATE code_review_revision_assessments SET status=$3,completed_at=now(),result_origin='evidence_only',decision='blocked',acceptable=false,structured_outcome='{}',rendered_body='Synthetic final assessment' WHERE org_id=$1 AND id=$2`, f.completion.OrgID, f.completion.AssessmentID, status)
	require.NoError(t, err, "stage the exact full-schema terminal assessment before its request and scheduler settlement")
}

func assessmentSettlementRows(ctx context.Context, conn DBTX, f assessmentSettlementFixture) (json.RawMessage, error) {
	graph, err := recheckPersistenceRows(ctx, conn, f.completion)
	if err != nil {
		return nil, err
	}
	var result json.RawMessage
	err = conn.QueryRow(ctx, `SELECT $3::jsonb || jsonb_build_object('schedule',(SELECT to_jsonb(s) FROM code_review_pr_state s WHERE org_id=$1 AND pull_request_id=$2),'requests',(SELECT jsonb_agg(to_jsonb(r) ORDER BY id) FROM code_review_requests r WHERE org_id=$1 AND pull_request_id=$2),'wakes',(SELECT jsonb_agg(to_jsonb(j) ORDER BY id) FROM jobs j WHERE org_id=$1 AND dedupe_key=$4))`, f.completion.OrgID, f.prID, graph, "code_review_schedule:"+f.prID.String()).Scan(&result)
	return result, err
}

func requireAssessmentSettlementRows(t *testing.T, ctx context.Context, conn DBTX, f assessmentSettlementFixture) json.RawMessage {
	t.Helper()
	rows, err := assessmentSettlementRows(ctx, conn, f)
	require.NoError(t, err, "capture every column in the complete conversation, assessment, request, scheduler and durable wake graph")
	return rows
}

func requireAssessmentSettlementGraph(t *testing.T, before, actual, stamp json.RawMessage, f assessmentSettlementFixture, failed bool) {
	t.Helper()
	var expected map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(before, &expected), "retain the complete graph staged by the outer owner before settlement")
	var requests []map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(expected["requests"], &requests), "retain every original request field, including any newer pending request")
	for _, request := range requests {
		if string(request["id"]) == `"`+f.requestID.String()+`"` {
			request["status"] = json.RawMessage(`"satisfied"`)
			if failed {
				request["status"] = json.RawMessage(`"failed"`)
			}
		}
	}
	var err error
	expected["requests"], err = json.Marshal(requests)
	require.NoError(t, err, "encode exactly the one assessment-bound request status change")
	var schedule map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(expected["schedule"], &schedule), "retain every original scheduler column and pending pointer")
	schedule["updated_at"] = stamp
	if !f.differentOwner {
		schedule["active_assessment_id"], schedule["active_session_id"] = json.RawMessage(`null`), json.RawMessage(`null`)
		current := f.completion.AssessmentID
		if failed {
			current = f.baselineID
		}
		schedule["current_assessment_id"], err = json.Marshal(current)
		require.NoError(t, err, "keep the completed assessment or the exact prior valid baseline as current")
		if !f.pending {
			schedule["state"] = json.RawMessage(`"covered"`)
			if failed {
				schedule["state"] = json.RawMessage(`"idle"`)
			}
			schedule["wait_reason"] = json.RawMessage(`""`)
			schedule["retry_at"], schedule["eligible_at"] = json.RawMessage(`null`), json.RawMessage(`null`)
		}
	}
	expected["schedule"], err = json.Marshal(schedule)
	require.NoError(t, err, "encode only the original settlement policy's owner, coverage and clock assignments")
	encoded, err := json.Marshal(expected)
	require.NoError(t, err, "encode the full expected settlement graph without dropping any unrelated rows")
	require.JSONEq(t, string(encoded), string(actual), "settlement must preserve every outer caller write, newer owner/intent, existing wake and unrelated domain column exactly")
}

func installAssessmentSettlementLateFailure(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `CREATE TABLE synthetic_assessment_settlement_rejections(pull_request_id uuid PRIMARY KEY,session_id uuid NOT NULL,assessment_id uuid NOT NULL);
CREATE FUNCTION synthetic_assessment_settlement_reject() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE expected synthetic_assessment_settlement_rejections%ROWTYPE;
BEGIN
 SELECT * INTO expected FROM synthetic_assessment_settlement_rejections WHERE pull_request_id=NEW.pull_request_id;
 IF NOT FOUND THEN RETURN NEW; END IF;
 IF NOT EXISTS(SELECT 1 FROM sessions WHERE org_id=NEW.org_id AND id=expected.session_id AND status='completed')
 OR NOT EXISTS(SELECT 1 FROM code_review_revision_assessments WHERE org_id=NEW.org_id AND id=expected.assessment_id AND status='completed')
 OR NOT EXISTS(SELECT 1 FROM code_review_requests WHERE org_id=NEW.org_id AND assessment_id=expected.assessment_id AND status='satisfied')
 THEN RAISE EXCEPTION 'synthetic settlement probe did not observe all earlier root assessment and request mutations'; END IF;
 RAISE EXCEPTION 'synthetic late assessment settlement owner failure';
END $$;
CREATE TRIGGER synthetic_assessment_settlement_reject BEFORE UPDATE ON code_review_pr_state FOR EACH ROW EXECUTE FUNCTION synthetic_assessment_settlement_reject();`)
	require.NoError(t, err, "install only a generated-database late scheduler failure that proves the complete earlier mutation sequence")
}

type assessmentSettlementStarter struct {
	*pgxpool.Pool
	begins    int
	tx        *assessmentSettlementTx
	commitErr error
}

func (s *assessmentSettlementStarter) Begin(ctx context.Context) (pgx.Tx, error) {
	s.begins++
	owner, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	s.tx = &assessmentSettlementTx{Tx: owner, commitErr: s.commitErr}
	return s.tx, nil
}

type assessmentSettlementTx struct {
	pgx.Tx
	nestedBegins, commits, rollbacks int
	commitErr                        error
}

func (tx *assessmentSettlementTx) Begin(context.Context) (pgx.Tx, error) {
	tx.nestedBegins++
	return nil, errors.New("nested assessment settlement transaction is forbidden")
}

func (tx *assessmentSettlementTx) Commit(ctx context.Context) error {
	tx.commits++
	if tx.commitErr != nil {
		return tx.commitErr
	}
	return tx.Tx.Commit(ctx)
}

func (tx *assessmentSettlementTx) Rollback(ctx context.Context) error {
	err := tx.Tx.Rollback(ctx)
	if err == nil {
		tx.rollbacks++
	}
	return err
}

func newAssessmentSettlementPublications(t *testing.T, pool *pgxpool.Pool, f assessmentSettlementFixture) *recheckReconciliationPublications {
	t.Helper()
	redis := miniredis.RunT(t)
	client := cache.New(cache.Config{Topology: "standalone", URL: "redis://" + redis.Addr()}, zerolog.Nop(), nil)
	require.NotNil(t, client, "enable a real scheduler publisher so absent wiring cannot conceal early publication")
	t.Cleanup(func() { require.NoError(t, client.Close(), "close this fixture's enabled Redis publisher") })
	observations := &recheckReconciliationPublications{streams: cache.NewCodeReviewStreams(client, zerolog.Nop())}
	redis.Server().SetPreHook(func(_ *server.Peer, command string, args ...string) bool {
		if !strings.EqualFold(command, "publish") {
			return false
		}
		observation := recheckReconciliationPublication{}
		if len(args) != 2 {
			observation.err = errors.New("unexpected synthetic scheduling publish argument count")
		} else {
			observation.channel = args[0]
			observation.err = json.Unmarshal([]byte(args[1]), &observation.event)
		}
		if observation.err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			observation.visible, observation.err = assessmentSettlementRows(ctx, pool, f)
			cancel()
		}
		observations.mu.Lock()
		observations.events = append(observations.events, observation)
		observations.mu.Unlock()
		return false
	})
	return observations
}

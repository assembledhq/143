package thread

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

type callerStampServiceFixture struct {
	owner   CallerSendOwnerScope
	command CallerSendCommand
	input   SendMessageInput
}

// These are real typed owner rows carrying the complete approved command, not
// mocks of stamp persistence or evidence that an owner may send. The enclosing
// service still performs its unchanged legacy guards and transaction ownership.
func seedCallerStampServiceOwner(t *testing.T, f sendPostgresFixture, purpose CallerSendPurpose, differentDigest bool) callerStampServiceFixture {
	t.Helper()
	ownerID, passID := uuid.New(), uuid.New()
	prefix := "cr-dispatch:v1:"
	if purpose == CallerSendReviewLoopInitialSend {
		prefix = "rl-startup:v1:"
	}
	owner := CallerSendOwnerScope{Purpose: purpose, OwnerID: ownerID, OrgID: f.org,
		SessionID: f.session, ThreadID: f.lanes[0], ClientMessageID: prefix + ownerID.String()}
	input := f.input(0, owner.ClientMessageID)
	continuation := "synthetic-caller-continuation:" + ownerID.String()
	input.ContinuationDedupeKeyOverride = &continuation
	command, err := NewCallerSendCommand(owner.Purpose, owner.OwnerID, input)
	require.NoError(t, err, "freeze the original complete effective input under independently retained caller scope")
	canonical, err := command.Encode()
	require.NoError(t, err, "retain the original canonical command before creating the real typed owner")
	proof, err := command.Proof()
	require.NoError(t, err, "attach only the immutable original nonsemantic command descriptor")
	input.CallerCommand = proof
	digest := command.Digest()
	if differentDigest {
		digest = strings.Repeat("b", 64)
		require.NotEqual(t, command.Digest(), digest, "the negative fixture must genuinely differ from the original retained semantic digest")
	}
	err = db.RunLifecycleTransaction(context.Background(), f.pool, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE caller_send_intent_control SET intents_enabled=true WHERE singleton AND schema_ready`); err != nil {
			return err
		}
		if purpose == CallerSendCodeReviewReviewerDispatch {
			_, err := tx.Exec(ctx, `INSERT INTO code_review_agent_results (
 id,org_id,session_id,agent_provider,role,status,dispatch_version,dispatch_key,dispatch_input,dispatch_digest,
 dispatch_thread_id,dispatch_state,dispatch_attempt_seq,dispatch_due_at,dispatch_writer_seq,dispatch_updated_at,dispatch_wake
) VALUES($1,$2,$3,$4,'reviewer','queued',1,$5,$6::jsonb,$7,$8,'pending',0,statement_timestamp(),0,statement_timestamp(),
 jsonb_build_object('job_type','run_code_review','queue','agent','dedupe_key',$9::text,'payload',jsonb_build_object('org_id',$2::uuid::text,'session_id',$3::uuid::text)))`,
				owner.OwnerID, owner.OrgID, owner.SessionID, string(models.DefaultDefaultAgentType), owner.ClientMessageID,
				string(canonical), digest, owner.ThreadID, "synthetic-stamp-owner:"+owner.OwnerID.String())
			return err
		}
		if purpose != CallerSendReviewLoopInitialSend {
			return errors.New("unsupported synthetic caller-stamp owner purpose")
		}
		if _, err := tx.Exec(ctx, `SET CONSTRAINTS ALL DEFERRED`); err != nil {
			return err
		}
		// The retained start's creation envelope is only a minimal SQL fixture;
		// startup_input itself is the full real codec envelope. This does not
		// claim to exercise a startup API or its unknown creation protocol.
		if _, err := tx.Exec(ctx, `INSERT INTO review_loop_start_intents (
 id,org_id,session_id,caller_kind,envelope,envelope_digest,pass_id,thread_id,startup_input,startup_digest,state,due_at,created_at,updated_at
) VALUES($1,$2,$3,'manual','{}'::jsonb,$4,$5,$6,$7::jsonb,$4,'prepared',statement_timestamp(),statement_timestamp(),statement_timestamp())`,
			owner.OwnerID, owner.OrgID, owner.SessionID, digest, passID, owner.ThreadID, string(canonical)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE review_loop_start_intents SET state='created',due_at=NULL WHERE org_id=$1 AND session_id=$2 AND id=$3 AND state='prepared'`, owner.OrgID, owner.SessionID, owner.OwnerID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO session_review_loops (
 id,org_id,session_id,thread_id,status,source,agent_type,max_passes,startup_send_version,startup_send_key,
 startup_send_input,startup_send_digest,startup_send_thread_id,startup_send_state,startup_send_attempt_seq,
 startup_send_due_at,startup_send_writer_seq,startup_send_updated_at,startup_send_pass_id
) VALUES($1,$2,$3,$4,'running','manual',$5,1,1,$6,$7::jsonb,$8,$4,'pending',0,statement_timestamp(),0,statement_timestamp(),$9)`,
			owner.OwnerID, owner.OrgID, owner.SessionID, owner.ThreadID, string(models.DefaultDefaultAgentType),
			owner.ClientMessageID, string(canonical), digest, passID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO session_review_loop_passes(id,org_id,session_id,loop_id,pass_index,status) VALUES($1,$2,$3,$4,1,'reviewing')`, passID, owner.OrgID, owner.SessionID, owner.OwnerID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`)
		return err
	})
	require.NoError(t, err, "commit only this synthetic typed owner and original full command before sending")
	return callerStampServiceFixture{owner: owner, command: command, input: input}
}

func callerStampDBScope(owner CallerSendOwnerScope) db.CallerSendStampScope {
	return db.CallerSendStampScope{SessionID: owner.SessionID, ThreadID: owner.ThreadID,
		ClientMessageID: owner.ClientMessageID, Purpose: db.CallerSendStampPurpose(owner.Purpose), OwnerID: owner.OwnerID}
}

type callerStampProvisional struct {
	graph       sendPostgresGraph
	stamp       db.CallerSendStamp
	found       bool
	ownerInput  []byte
	ownerDigest string
	jobID       uuid.UUID
	messageID   int64
}

// The only injection happens after the real enqueue has inserted its job.
// All observations use that exact supplied transaction, never another pool
// checkout or a substitute transaction runner. A regular error lets production
// settle the complete unit before the test checks rollback and effects.
type callerStampEnqueueProbe struct {
	LegacySendPersistence
	fixture     sendPostgresFixture
	owner       CallerSendOwnerScope
	cause       error
	calls       int
	provisional callerStampProvisional
}

func (p *callerStampEnqueueProbe) EnqueueInTx(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, opts db.EnqueueOpts) (uuid.UUID, error) {
	jobID, err := p.LegacySendPersistence.EnqueueInTx(ctx, tx, orgID, opts)
	if err != nil {
		return jobID, err
	}
	p.calls++
	p.provisional.jobID = jobID
	p.provisional.graph, err = readSendPostgresGraph(ctx, tx, p.fixture)
	if err != nil {
		return uuid.Nil, err
	}
	err = tx.QueryRow(ctx, `SELECT m.id FROM session_messages m
 JOIN thread_inbox_entries i ON i.org_id=m.org_id AND i.session_id=m.session_id AND i.thread_id=m.thread_id AND i.message_id=m.id
 WHERE m.org_id=$1 AND m.session_id=$2 AND m.thread_id=$3
 AND i.org_id=$1 AND i.session_id=$2 AND i.thread_id=$3 AND i.client_message_id=$4`,
		p.owner.OrgID, p.owner.SessionID, p.owner.ThreadID, p.owner.ClientMessageID).Scan(&p.provisional.messageID)
	if err != nil {
		return uuid.Nil, err
	}
	p.provisional.stamp, p.provisional.found, err = db.ReadCallerSendStampInTx(ctx, p.owner.OrgID, tx, callerStampDBScope(p.owner))
	if err != nil {
		return uuid.Nil, err
	}
	query := `SELECT dispatch_input::text,dispatch_digest FROM code_review_agent_results WHERE org_id=$1 AND session_id=$2 AND id=$3`
	if p.owner.Purpose == CallerSendReviewLoopInitialSend {
		query = `SELECT startup_send_input::text,startup_send_digest FROM session_review_loops WHERE org_id=$1 AND session_id=$2 AND id=$3`
	}
	err = tx.QueryRow(ctx, query, p.owner.OrgID, p.owner.SessionID, p.owner.OwnerID).Scan(&p.provisional.ownerInput, &p.provisional.ownerDigest)
	if err != nil {
		return uuid.Nil, err
	}
	if p.cause != nil {
		return uuid.Nil, p.cause
	}
	return jobID, nil
}

func requireCallerStampProvisional(t *testing.T, f sendPostgresFixture, fixture callerStampServiceFixture, probe *callerStampEnqueueProbe) {
	t.Helper()
	require.Equal(t, 1, probe.calls, "the real legacy unit must enqueue once without replay or compensation")
	require.NotEqual(t, uuid.Nil, probe.provisional.jobID, "the probe must follow an actual newly inserted PostgreSQL job")
	require.Equal(t, sendPostgresGraph{messages: 1, inbox: 1, continueJobs: 1,
		parentStatus: models.SessionStatusRunning, threadStatus: models.ThreadStatusRunning}, probe.provisional.graph,
		"the exact supplied transaction must contain the complete provisional claim/message/inbox/job graph")
	require.True(t, probe.provisional.found, "the same real legacy send unit must stamp its actual message/inbox before enqueue completes")
	require.NotZero(t, probe.provisional.stamp.CreatedAt, "the provisional stamp must retain a database-owned creation timestamp")
	require.Equal(t, fixture.command.Digest(), probe.provisional.ownerDigest, "the actual typed owner must retain the original semantic digest")
	decoded, err := DecodeCallerSendCommand(probe.provisional.ownerInput, fixture.command.Digest(), fixture.owner)
	require.NoError(t, err, "the actual typed owner's JSONB descriptor must match the original digest and independently retained scope")
	expectedCanonical, err := fixture.command.Encode()
	require.NoError(t, err, "the original command must remain encodable without minting a new identity")
	actualCanonical, err := decoded.Encode()
	require.NoError(t, err, "the complete persisted typed-owner descriptor should round-trip through the approved codec")
	require.Equal(t, expectedCanonical, actualCanonical, "all effective options and original values must remain unchanged in actual persisted JSONB")
	expectedInput := cloneSendMessageInput(fixture.input)
	expectedInput.CallerCommand = nil
	expectedInput.ResolveReviewCommentIDs = []uuid.UUID{}
	require.Equal(t, expectedInput, decoded.SendInput(), "persisted descriptor must preserve the complete originally supplied effective command")
	expected := db.CallerSendStamp{OrgID: f.org, CallerSendStampInput: db.CallerSendStampInput{
		Scope: callerStampDBScope(fixture.owner), Version: db.CallerSendStampVersion,
		Digest: fixture.command.Digest(), MessageID: probe.provisional.messageID}, CreatedAt: probe.provisional.stamp.CreatedAt}
	require.Positive(t, expected.MessageID, "the stamped command must name a real positive message identity")
	require.Equal(t, expected, probe.provisional.stamp, "the provisional stamp must bind exactly the original purpose/owner/scope/key/version/digest/message")
}

func callerStampServiceSnapshot(t *testing.T, f sendPostgresFixture) json.RawMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	graph, err := readSendPostgresSnapshot(ctx, f.pool, f)
	require.NoError(t, err, "retain every original claim, message, inbox, answer and job field for rollback comparison")
	var snapshot json.RawMessage
	err = f.pool.QueryRow(ctx, `SELECT jsonb_build_object(
 'graph',$3::jsonb,
 'stamps',(SELECT coalesce(jsonb_agg(to_jsonb(s) ORDER BY owner_id),'[]'::jsonb) FROM caller_send_command_stamps s WHERE org_id=$1 AND session_id=$2),
 'dispatch_owners',(SELECT coalesce(jsonb_agg(to_jsonb(r) ORDER BY id),'[]'::jsonb) FROM code_review_agent_results r WHERE org_id=$1 AND session_id=$2),
 'loop_owners',(SELECT coalesce(jsonb_agg(to_jsonb(l) ORDER BY id),'[]'::jsonb) FROM session_review_loops l WHERE org_id=$1 AND session_id=$2),
 'starts',(SELECT coalesce(jsonb_agg(to_jsonb(s) ORDER BY id),'[]'::jsonb) FROM review_loop_start_intents s WHERE org_id=$1 AND session_id=$2),
 'passes',(SELECT coalesce(jsonb_agg(to_jsonb(p) ORDER BY id),'[]'::jsonb) FROM session_review_loop_passes p WHERE org_id=$1 AND session_id=$2)
)`, f.org, f.session, graph).Scan(&snapshot)
	require.NoError(t, err, "include every typed caller/stamp/retained-start row rather than relying on cardinality-only rollback checks")
	return snapshot
}

func TestThreadSendLegacyCallerStampPersistedPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		purpose CallerSendPurpose
	}{
		{name: "reviewer dispatch", purpose: CallerSendCodeReviewReviewerDispatch},
		{name: "initial loop send", purpose: CallerSendReviewLoopInitialSend},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newSendPostgresFixtureWithMaxConns(t, false, 1, 1)
			require.Equal(t, int32(1), f.pool.Config().MaxConns, "the entire stamped legacy send must fit one actual database checkout")
			fixture := seedCallerStampServiceOwner(t, f, tt.purpose, false)
			service, effects := f.service(t, true)
			probe := &callerStampEnqueueProbe{LegacySendPersistence: service.send.Legacy, fixture: f, owner: fixture.owner}
			service.send.Legacy = probe
			result, err := service.SendMessage(context.Background(), fixture.input)
			require.NoError(t, err, "a correctly stamped legacy input should commit its complete real send unit")
			require.NotNil(t, result, "committed stamped input must return a send result")
			require.NotNil(t, result.Message, "committed stamped input must return its actual message")
			requireCallerStampProvisional(t, f, fixture, probe)
			require.Equal(t, result.Message.ID, probe.provisional.stamp.MessageID, "the same-unit stamp must name the actual returned message, not a fabricated pointer")
			var observed db.CallerSendStamp
			var found bool
			err = db.RunLifecycleTransaction(context.Background(), f.pool, func(ctx context.Context, tx pgx.Tx) error {
				var err error
				observed, found, err = db.ReadCallerSendStampInTx(ctx, f.org, tx, callerStampDBScope(fixture.owner))
				return err
			})
			require.NoError(t, err, "an independent postcommit transaction must observe the original exact stamp")
			require.True(t, found, "a committed stamped send must retain durable correlation")
			require.Equal(t, probe.provisional.stamp, observed, "postcommit readback must match every field observed in the original whole send unit")
			require.Equal(t, probe.provisional.graph, sendPostgresReadGraph(t, f), "independent committed graph must match the complete original supplied-tx graph")
			require.Equal(t, []int{1, 1, 0}, []int{len(effects.jobs), effects.claims, effects.pending}, "only a known commit may publish the established one job/one claim effects")
			require.Equal(t, []uuid.UUID{probe.provisional.jobID}, effects.jobs, "postcommit notification must name exactly the original real job")
			require.Empty(t, effects.lifecycle, "private correlation cannot invent an active-protocol receipt or lifecycle effect")
			require.Nil(t, result.Admission, "legacy stamped sends remain correlation rather than active admission receipts")
		})
	}
}

func TestThreadSendLegacyCallerStampLateEnqueueRollbackPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		purpose CallerSendPurpose
	}{
		{name: "reviewer dispatch", purpose: CallerSendCodeReviewReviewerDispatch},
		{name: "initial loop send", purpose: CallerSendReviewLoopInitialSend},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newSendPostgresFixtureWithMaxConns(t, false, 1, 1)
			fixture := seedCallerStampServiceOwner(t, f, tt.purpose, false)
			baseline := callerStampServiceSnapshot(t, f)
			service, effects := f.service(t, true)
			cause := errors.New("synthetic failure after real caller-stamped enqueue")
			probe := &callerStampEnqueueProbe{LegacySendPersistence: service.send.Legacy, fixture: f, owner: fixture.owner, cause: cause}
			service.send.Legacy = probe
			result, err := service.SendMessage(context.Background(), fixture.input)
			require.ErrorIs(t, err, ErrEnqueueFailed, "late failure must retain the real legacy enqueue domain boundary")
			require.ErrorIs(t, err, cause, "whole rollback must preserve the injected post-enqueue cause")
			require.Nil(t, result, "a rolled-back unit cannot return an accepted message or provisional stamp")
			requireCallerStampProvisional(t, f, fixture, probe)
			require.JSONEq(t, string(baseline), string(callerStampServiceSnapshot(t, f)), "one late failure must roll back the entire claim/message/inbox/stamp/job graph without changing any caller identity or owner field")
			requireSendPostgresNoEffects(t, effects)
		})
	}
}

func TestThreadSendLegacyCallerStampOwnerDigestRollbackPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		purpose CallerSendPurpose
	}{
		{name: "reviewer dispatch", purpose: CallerSendCodeReviewReviewerDispatch},
		{name: "initial loop send", purpose: CallerSendReviewLoopInitialSend},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newSendPostgresFixtureWithMaxConns(t, false, 1, 1)
			fixture := seedCallerStampServiceOwner(t, f, tt.purpose, true)
			baseline := callerStampServiceSnapshot(t, f)
			service, effects := f.service(t, true)
			probe := &callerStampEnqueueProbe{LegacySendPersistence: service.send.Legacy, fixture: f, owner: fixture.owner}
			service.send.Legacy = probe
			result, err := service.SendMessage(context.Background(), fixture.input)
			require.ErrorIs(t, err, db.ErrCallerSendStampStorage, "a different actual typed-owner digest must refuse stamp persistence inside the owning legacy unit")
			var pgErr *pgconn.PgError
			require.ErrorAs(t, err, &pgErr, "the owning unit must retain the actual PostgreSQL trigger refusal")
			require.Equal(t, "23503", pgErr.Code, "the real immutable owner-scope trigger must reject the original proof against a different retained digest")
			require.Nil(t, result, "a refused stamp must not return the provisionally created message")
			require.Equal(t, 0, probe.calls, "stamp insertion must be before enqueue rather than after a job can be accepted")
			require.JSONEq(t, string(baseline), string(callerStampServiceSnapshot(t, f)), "actual stamp refusal must roll back claims, messages and inbox while preserving every original caller/owner row")
			requireSendPostgresNoEffects(t, effects)
		})
	}
}

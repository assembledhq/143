package db

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	testpostgres "github.com/assembledhq/143/internal/testutil/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

type messageReplayProgressFixture struct {
	activatedLifecycleFixture
	command  MessageAdmission
	accepted Admission
	jobID    uuid.UUID
	nodeID   string
	token    uuid.UUID
	runtime  models.ThreadRuntime
	taskIDs  []int64
}

func newMessageReplayProgressFixture(t *testing.T, pool *pgxpool.Pool, record, apply bool, change func(*MessageAdmissionInput)) messageReplayProgressFixture {
	t.Helper()
	ctx := context.Background()
	f := messageReplayProgressFixture{activatedLifecycleFixture: seedActivatedLifecycleFixture(t, ctx, pool)}
	f.command = admissionPostgresCommand(t, f.activatedLifecycleFixture, "replay-after-recovery", func(input *MessageAdmissionInput) {
		input.References = models.SessionInputReferences{{Kind: models.SessionInputReferenceKindFile, Path: "synthetic.go", Display: "synthetic.go"}}
		input.Commands = models.SessionInputCommands{{Kind: "command", AgentType: models.DefaultDefaultAgentType, Name: "test", Token: "/test", Display: "/test", Source: models.SessionInputCommandSourceBuiltin}}
		if change != nil {
			change(input)
		}
	})
	var err error
	f.accepted, _, err = runAdmissionPostgres(t, pool, f.command)
	require.NoError(t, err, "accept one ordinary input and its immutable dispatch before executing any recovery writer")
	var hasJob bool
	f.jobID, hasJob = f.accepted.JobID()
	require.True(t, hasJob, "the idle admission must retain one exact job identity")
	f.taskIDs = []int64{f.accepted.Message().ID}
	f.nodeID = "synthetic-replay-worker-" + uuid.NewString()
	admissionPostgresExec(t, pool, `INSERT INTO nodes(id,mode,status,last_heartbeat_at) VALUES($1,'worker','active',now())`, f.nodeID)
	claimMessageReplayProgressJob(t, pool, &f)
	f.runtime = createSeedRuntime(t, pool, f.orgID, f.sessionID, f.threadID)
	if record {
		recordMessageReplayProgressAttempt(t, pool, f, f.taskIDs)
	}
	if apply {
		phase, batch, err := NewSessionActivityPhaseStore(pool).StartSeedPhase(ctx, f.orgID, f.sessionID, f.threadID, f.accepted.Message().TurnNumber, f.runtime.ID, f.runtime.LeaseToken, []int64{f.accepted.Message().ID}, time.Now())
		require.NoError(t, err, "the real seed transaction must acknowledge and apply the exact admitted input")
		require.Equal(t, &f.runtime.ID, phase.RuntimeID, "the provider phase must belong to the real owned runtime")
		require.NotNil(t, batch, "a positively applied seed must retain its actual acknowledgment batch")
		require.Equal(t, models.InboxDeliveryBatchStarted, batch.Status, "seed admission must persist its started acknowledgment batch")
		var actual struct {
			state   string
			runtime uuid.UUID
			applied bool
		}
		require.NoError(t, pool.QueryRow(ctx, `SELECT delivery_state,runtime_id,applied_at IS NOT NULL FROM thread_inbox_entries WHERE org_id=$1 AND id=$2`, f.orgID, f.accepted.InboxEntry().ID).Scan(&actual.state, &actual.runtime, &actual.applied), "inspect the durable input receipt produced by the actual seed writer")
		require.Equal(t, struct {
			state   string
			runtime uuid.UUID
			applied bool
		}{"acked", f.runtime.ID, true}, actual, "recovery must depend on real acknowledged applied input rather than a fabricated payload marker")
		_, err = NewSessionActivityPhaseStore(pool).CompletePhase(ctx, f.orgID, phase.ID, models.ActivityPhaseStatusInterrupted, models.ActivityPhaseBoundaryMaintenance, time.Now())
		require.NoError(t, err, "persist the actual interrupted provider phase before its runtime closes for recovery")
	}
	closeMessageReplayProgressRuntime(t, pool, f)
	return f
}

func claimMessageReplayProgressJob(t *testing.T, pool *pgxpool.Pool, f *messageReplayProgressFixture) {
	t.Helper()
	f.token = uuid.New()
	claimed, err := NewJobStore(pool).ClaimNextRunnable(context.Background(), f.nodeID, "synthetic-replay-owner", f.token, time.Minute)
	require.NoError(t, err, "the actual queue claimant must acquire the current job generation")
	require.NotNil(t, claimed, "the isolated queue must contain its admitted due job")
	require.Equal(t, f.jobID, claimed.ID, "each real claim must retain the exact immutable admission job")
}

func recordMessageReplayProgressAttempt(t *testing.T, pool *pgxpool.Pool, f messageReplayProgressFixture, messages []int64) {
	t.Helper()
	changed, err := NewJobStore(pool).RecordContinuationAttemptWithLease(context.Background(), f.orgID, f.jobID, f.token, f.sessionID, f.threadID, f.runtime.ID, f.runtime.LeaseToken, f.accepted.Message().ID, messages)
	require.NoError(t, err, "record the attempt using both real current job and runtime lease tokens")
	require.True(t, changed, "the actual fenced attempt writer must persist progress before replay is tested")
}

func closeMessageReplayProgressRuntime(t *testing.T, pool *pgxpool.Pool, f messageReplayProgressFixture) {
	t.Helper()
	changed, err := NewThreadRuntimeStore(pool).MarkTerminalWithLease(context.Background(), f.orgID, f.runtime.ID, f.runtime.LeaseToken, models.ThreadRuntimeStatusClosed, "synthetic retry boundary", "")
	require.NoError(t, err, "close the real owned runtime without changing its applied-input history")
	require.True(t, changed, "only the current runtime generation may close before retry or reclaim")
}

func TestMessageAdmissionReplayAfterRecoveryWritersPostgres(t *testing.T) {
	t.Parallel()
	for _, writer := range messageReplayProgressWriters() {
		t.Run(writer.name, func(t *testing.T) {
			t.Parallel()
			// ClaimNextRunnable and the global reclaim writers scan across tenants.
			// Each case owns a full-schema clone and at most two pool connections.
			pool := testpostgres.NewPool(t, context.Background(), "../../migrations", 2)
			f := newMessageReplayProgressFixture(t, pool, true, true, nil)
			for cycle := range 2 {
				if cycle != 0 {
					claimMessageReplayProgressJob(t, pool, &f)
					f.runtime = createSeedRuntime(t, pool, f.orgID, f.sessionID, f.threadID)
					// A later attempt retains the original task and a real newly
					// acknowledged input, without applying the original twice.
					message := models.SessionMessage{OrgID: f.orgID, SessionID: f.sessionID, ThreadID: &f.threadID, Role: models.MessageRoleUser, TurnNumber: f.accepted.Message().TurnNumber, Content: "Synthetic confirmed recovery input"}
					require.NoError(t, NewSessionMessageStore(pool).Create(context.Background(), &message), "persist a real additional same-lane instruction during the second owned attempt")
					_, err := NewThreadInboxStore(pool).AppendForMessage(context.Background(), f.orgID, AppendThreadInboxEntryParams{SessionID: f.sessionID, ThreadID: f.threadID, MessageID: message.ID, EntryType: models.ThreadInboxEntryTypeUserMessage, Payload: json.RawMessage(`{"content":"Synthetic confirmed recovery input"}`)})
					require.NoError(t, err, "retain the additional input through the actual inbox append writer")
					f.taskIDs = append(f.taskIDs, message.ID)
					recordMessageReplayProgressAttempt(t, pool, f, f.taskIDs)
					phase, _, err := NewSessionActivityPhaseStore(pool).StartSeedPhase(context.Background(), f.orgID, f.sessionID, f.threadID, message.TurnNumber, f.runtime.ID, f.runtime.LeaseToken, []int64{message.ID}, time.Now())
					require.NoError(t, err, "the second actual seed transaction must apply only the new input while retaining the original recovery context")
					_, err = NewSessionActivityPhaseStore(pool).CompletePhase(context.Background(), f.orgID, phase.ID, models.ActivityPhaseStatusInterrupted, models.ActivityPhaseBoundaryMaintenance, time.Now())
					require.NoError(t, err, "retain the actual second provider interruption before the next retry or reclaim")
					closeMessageReplayProgressRuntime(t, pool, f)
				}
				invokeMessageReplayProgressWriter(t, pool, f, writer)
				requireMessageReplayProgressPayload(t, pool, f, true, true)
				requireMessageReplayProgressReceipt(t, pool, f)
			}
		})
	}
}

type messageReplayProgressWriter struct {
	name     string
	executor bool
	expires  bool
	invoke   func(context.Context, *pgxpool.Pool, messageReplayProgressFixture) (bool, error)
}

func messageReplayProgressWriters() []messageReplayProgressWriter {
	return []messageReplayProgressWriter{
		{name: "RetryWithLease", invoke: func(ctx context.Context, pool *pgxpool.Pool, f messageReplayProgressFixture) (bool, error) {
			return NewJobStore(pool).RetryWithLease(ctx, f.jobID, f.token, "synthetic retry", time.Now())
		}},
		{name: "RetryWithLeaseAndTarget", invoke: func(ctx context.Context, pool *pgxpool.Pool, f messageReplayProgressFixture) (bool, error) {
			return NewJobStore(pool).RetryWithLeaseAndTarget(ctx, f.jobID, f.token, "synthetic retarget", time.Now(), &f.nodeID)
		}},
		{name: "RetryWithoutConsumingAttemptWithLease", invoke: func(ctx context.Context, pool *pgxpool.Pool, f messageReplayProgressFixture) (bool, error) {
			return NewJobStore(pool).RetryWithoutConsumingAttemptWithLease(ctx, f.jobID, f.token, "synthetic retry", time.Now())
		}},
		{name: "RetryWithoutConsumingAttemptWithLeaseAndResetWindow", invoke: func(ctx context.Context, pool *pgxpool.Pool, f messageReplayProgressFixture) (bool, error) {
			return NewJobStore(pool).RetryWithoutConsumingAttemptWithLeaseAndResetWindow(ctx, f.jobID, f.token, "synthetic retry", time.Now())
		}},
		{name: "RetryWithoutConsumingAttemptWithLeaseAndTarget", invoke: func(ctx context.Context, pool *pgxpool.Pool, f messageReplayProgressFixture) (bool, error) {
			return NewJobStore(pool).RetryWithoutConsumingAttemptWithLeaseAndTarget(ctx, f.jobID, f.token, "synthetic retarget", time.Now(), &f.nodeID)
		}},
		{name: "ReclaimLostRunningJobs", expires: true, invoke: func(ctx context.Context, pool *pgxpool.Pool, _ messageReplayProgressFixture) (bool, error) {
			count, err := NewJobStore(pool).ReclaimLostRunningJobs(ctx, time.Now().Add(-time.Minute), 1)
			return count == 1, err
		}},
		{name: "ReclaimLostRunningSessionJobsForSession", expires: true, invoke: func(ctx context.Context, pool *pgxpool.Pool, f messageReplayProgressFixture) (bool, error) {
			count, err := NewJobStore(pool).ReclaimLostRunningSessionJobsForSession(ctx, f.orgID, f.sessionID, time.Now().Add(-time.Minute), 1)
			return count == 1, err
		}},
		{name: "SessionExecutorReclaimLost", executor: true, expires: true, invoke: func(ctx context.Context, pool *pgxpool.Pool, _ messageReplayProgressFixture) (bool, error) {
			count, err := NewSessionExecutorStore(pool).ReclaimLost(ctx, time.Now().Add(-time.Minute), 1)
			return count == 1, err
		}},
		{name: "SessionExecutorReclaimLostForSession", executor: true, expires: true, invoke: func(ctx context.Context, pool *pgxpool.Pool, f messageReplayProgressFixture) (bool, error) {
			count, err := NewSessionExecutorStore(pool).ReclaimLostForSession(ctx, f.orgID, f.sessionID, time.Now().Add(-time.Minute), 1)
			return count == 1, err
		}},
	}
}

func messageReplayProgressWriterNamed(t *testing.T, name string) messageReplayProgressWriter {
	t.Helper()
	for _, writer := range messageReplayProgressWriters() {
		if writer.name == name {
			return writer
		}
	}
	t.Fatalf("actual recovery writer %q must be present in the fixed regression roster", name)
	return messageReplayProgressWriter{}
}

func invokeMessageReplayProgressWriter(t *testing.T, pool *pgxpool.Pool, f messageReplayProgressFixture, writer messageReplayProgressWriter) {
	t.Helper()
	ctx := context.Background()
	if writer.executor {
		store := NewSessionExecutorStore(pool)
		executorID, err := store.CreateStarting(ctx, f.orgID, models.CreateSessionExecutorParams{SessionID: f.sessionID, ThreadID: &f.threadID, JobID: f.jobID, JobType: "continue_session", HostNodeID: f.nodeID, OwnerID: "synthetic-replay-executor", LockToken: f.token})
		require.NoError(t, err, "create the executor through the real scoped ownership store")
		changed, err := NewJobStore(pool).HandoffToSessionExecutorWithLease(ctx, f.orgID, f.jobID, f.token, executorID)
		require.NoError(t, err, "handoff the actual current job lease to its durable executor")
		require.True(t, changed, "the current worker token must establish the real executor owner")
		changed, err = store.MarkRunningWithLease(ctx, f.orgID, executorID, f.token, time.Minute)
		require.NoError(t, err, "start the executor using the exact real lease token")
		require.True(t, changed, "the starting executor must become running before its lease is lost")
		// Expire only timing columns to reach the real reclaim path without a
		// sleep. No payload, owner, acknowledgment or recovery effect is forged.
		admissionPostgresExec(t, pool, `UPDATE session_executors SET heartbeat_at=now()-interval '2 minutes',lease_expires_at=now()-interval '1 second' WHERE org_id=$1 AND id=$2`, f.orgID, executorID)
	}
	if writer.expires {
		// The claimant above installed the real token and locked_at. Expiring
		// only its lease makes loss deterministic without changing that origin.
		admissionPostgresExec(t, pool, `UPDATE jobs SET lease_expires_at=now()-interval '1 second' WHERE org_id=$1 AND id=$2`, f.orgID, f.jobID)
	}
	changed, err := writer.invoke(ctx, pool, f)
	require.NoError(t, err, "the actual retry or reclaim writer must execute its normal ownership-fenced payload rewrite")
	require.True(t, changed, "the isolated fixture must actually be retried or reclaimed by the selected real writer")
	var status string
	require.NoError(t, pool.QueryRow(ctx, `SELECT status FROM jobs WHERE org_id=$1 AND id=$2`, f.orgID, f.jobID).Scan(&status), "read the current job after real retry or reclaim")
	require.Equal(t, "pending", status, "the actual writer must requeue the same accepted job")
}

func messageReplayProgressPayload(t *testing.T, pool *pgxpool.Pool, f messageReplayProgressFixture) map[string]json.RawMessage {
	t.Helper()
	var raw []byte
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT payload FROM jobs WHERE org_id=$1 AND id=$2`, f.orgID, f.jobID).Scan(&raw), "inspect only the exact synthetic tenant and accepted job")
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &fields), "decode the complete current execution payload without using its fields as authority")
	return fields
}

func requireMessageReplayProgressPayload(t *testing.T, pool *pgxpool.Pool, f messageReplayProgressFixture, continuation, recovery bool) {
	t.Helper()
	binding, opts, err := messageAdmissionJobOptions(f.command, f.accepted, f.jobID)
	require.NoError(t, err, "reconstruct the original immutable accepted envelope independently of execution progress")
	base, err := json.Marshal(opts.Payload)
	require.NoError(t, err, "encode only the immutable original admission fields")
	original, err := encodeLifecycleJobBindingPayload(base, binding)
	require.NoError(t, err, "retain every exact original lifecycle binding key")
	var expected map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(original, &expected), "decode the original envelope for an exact full-object comparison")
	set := func(key string, value any) {
		raw, marshalErr := json.Marshal(value)
		require.NoError(t, marshalErr, "encode the exact expected real-writer progress field")
		expected[key] = raw
	}
	if continuation {
		set("continuation_runtime_id", f.runtime.ID.String())
		set("continuation_message_id", fmt.Sprint(f.accepted.Message().ID))
		set("continuation_task_message_ids", f.taskIDs)
	}
	if recovery {
		set("execution_purpose", "checkpoint_recovery")
		set("recovery_runtime_id", f.runtime.ID.String())
		set("recovery_message_ids", f.taskIDs)
	}
	// Decode numbers consistently after JSONB's harmless whitespace changes.
	decode := func(fields map[string]json.RawMessage) map[string]any {
		raw, marshalErr := json.Marshal(fields)
		require.NoError(t, marshalErr, "normalize only JSON serialization for complete object equality")
		var values map[string]any
		require.NoError(t, json.Unmarshal(raw, &values), "decode every original and progress key for exact-value comparison")
		return values
	}
	require.Equal(t, decode(expected), decode(messageReplayProgressPayload(t, pool, f)), "real writers must add exactly the documented progress keys while leaving all original lane, queued-input, binding and domain identities unchanged")
}

func messageReplayProgressGraph(t *testing.T, pool *pgxpool.Pool, f messageReplayProgressFixture) string {
	t.Helper()
	var snapshot string
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT jsonb_build_object(
 'sessions',(SELECT COALESCE(jsonb_agg(to_jsonb(r) ORDER BY r.id),'[]'::jsonb) FROM sessions r WHERE org_id=$1 AND id=$2),
 'threads',(SELECT COALESCE(jsonb_agg(to_jsonb(r) ORDER BY r.id),'[]'::jsonb) FROM session_threads r WHERE org_id=$1 AND session_id=$2),
 'dispatches',(SELECT COALESCE(jsonb_agg(to_jsonb(r) ORDER BY r.id),'[]'::jsonb) FROM session_input_dispatches r WHERE org_id=$1 AND session_id=$2),
 'jobs',(SELECT COALESCE(jsonb_agg(to_jsonb(r) ORDER BY r.id),'[]'::jsonb) FROM jobs r WHERE org_id=$1),
 'messages',(SELECT COALESCE(jsonb_agg(to_jsonb(r) ORDER BY r.id),'[]'::jsonb) FROM session_messages r WHERE org_id=$1 AND session_id=$2),
 'inbox',(SELECT COALESCE(jsonb_agg(to_jsonb(r) ORDER BY r.id),'[]'::jsonb) FROM thread_inbox_entries r WHERE org_id=$1 AND session_id=$2),
 'batches',(SELECT COALESCE(jsonb_agg(to_jsonb(r) ORDER BY r.id),'[]'::jsonb) FROM thread_inbox_delivery_batches r WHERE org_id=$1 AND session_id=$2),
 'runtimes',(SELECT COALESCE(jsonb_agg(to_jsonb(r) ORDER BY r.id),'[]'::jsonb) FROM thread_runtimes r WHERE org_id=$1 AND session_id=$2),
 'phases',(SELECT COALESCE(jsonb_agg(to_jsonb(r) ORDER BY r.id),'[]'::jsonb) FROM session_activity_phases r WHERE org_id=$1 AND session_id=$2),
 'executors',(SELECT COALESCE(jsonb_agg(to_jsonb(r) ORDER BY r.id),'[]'::jsonb) FROM session_executors r WHERE org_id=$1 AND session_id=$2),
 'cutovers',(SELECT COALESCE(jsonb_agg(to_jsonb(r) ORDER BY r.version),'[]'::jsonb) FROM session_lifecycle_cutovers r WHERE org_id=$1 AND session_id=$2)
 )::text`, f.orgID, f.sessionID).Scan(&snapshot), "snapshot every persisted field of the synthetic admission and execution graph before read-only replay")
	return snapshot
}

func requireMessageReplayProgressReceipt(t *testing.T, pool *pgxpool.Pool, f messageReplayProgressFixture) {
	t.Helper()
	before := messageReplayProgressGraph(t, pool, f)
	replayed, effects, err := runAdmissionPostgres(t, pool, f.command)
	require.NoError(t, err, "the identical client key must survive legitimate progress by an actual production retry or reclaim writer")
	expected := f.accepted
	expected.disposition = MessageAdmissionReplayed
	// Inbox delivery is the sole current delivery truth; all acceptance fields,
	// including original scheduling options and the sealed claim receipt, remain
	// historical. That receipt is not a current start or own-consumer permit.
	currentInbox, err := NewThreadInboxStore(pool).GetByClientMessageID(context.Background(), f.orgID, f.threadID, *f.accepted.InboxEntry().ClientMessageID)
	require.NoError(t, err, "read the current exact delivery projection independently from the historical acceptance receipt")
	expected.inbox = currentInbox
	require.Equal(t, expected, replayed, "replay must expose exactly the original acceptance and options with only the current inbox delivery view, never execution-progress fields or new start permission")
	require.Empty(t, effects, "historical replay must not reissue notification, delivery, status or enqueue effects")
	require.Equal(t, before, messageReplayProgressGraph(t, pool, f), "replay must leave every field of the complete accepted and execution graph byte-identical")
}

func TestMessageAdmissionReplayRecoveryProofBranchesPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		record bool
		apply  bool
	}{
		{name: "applied input with no continuation record", apply: true},
		{name: "no applied input and no continuation record"},
		{name: "continuation record without applied input", record: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := testpostgres.NewPool(t, context.Background(), "../../migrations", 2)
			f := newMessageReplayProgressFixture(t, pool, tt.record, tt.apply, nil)
			before := messageReplayProgressPayload(t, pool, f)
			invokeMessageReplayProgressWriter(t, pool, f, messageReplayProgressWriterNamed(t, "ReclaimLostRunningSessionJobsForSession"))
			requireMessageReplayProgressPayload(t, pool, f, tt.record, tt.apply)
			if !tt.apply {
				require.Equal(t, before, messageReplayProgressPayload(t, pool, f), "reclaim without positively applied input must not manufacture recovery progress or recovery_unproven")
			}
			requireMessageReplayProgressReceipt(t, pool, f)
		})
	}
}

func TestMessageAdmissionReplayRecoveryExcludedDomainsPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		changeset bool
		action    bool
	}{
		{name: "changeset", changeset: true},
		{name: "post-success action", action: true},
		{name: "changeset and post-success action", changeset: true, action: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := testpostgres.NewPool(t, context.Background(), "../../migrations", 2)
			f := newMessageReplayProgressFixture(t, pool, true, true, func(input *MessageAdmissionInput) {
				if tt.changeset {
					changeset, err := NewSessionStore(pool).GetPrimaryChangesetID(context.Background(), input.OrgID, input.Fence.SessionID())
					require.NoError(t, err, "use the actual scoped primary changeset rather than an invented association")
					holder := uuid.New()
					input.ChangesetID, input.LeaseHolderID = &changeset, &holder
				}
				if tt.action {
					input.PostSuccessAction, input.PostSuccessAuthorMode = MessagePostSuccessPushPRChanges, MessagePostSuccessAuthorApp
				}
			})
			before := messageReplayProgressPayload(t, pool, f)
			invokeMessageReplayProgressWriter(t, pool, f, messageReplayProgressWriterNamed(t, "RetryWithLease"))
			require.Equal(t, before, messageReplayProgressPayload(t, pool, f), "changeset and post-success domains must remain excluded from the actual recovery rewrite")
			requireMessageReplayProgressPayload(t, pool, f, true, false)
			requireMessageReplayProgressReceipt(t, pool, f)
		})
	}
}

func TestMessageAdmissionReplayRecoveryProgressCorruptionPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		payloadSQL string
		columnSQL  string
	}{
		{name: "retry purpose", payloadSQL: `jsonb_set(payload,'{execution_purpose}','"checkpoint_retry"')`},
		{name: "continue purpose", payloadSQL: `jsonb_set(payload,'{execution_purpose}','"continue"')`},
		{name: "unknown purpose", payloadSQL: `jsonb_set(payload,'{execution_purpose}','"other"')`},
		{name: "empty purpose", payloadSQL: `jsonb_set(payload,'{execution_purpose}','""')`},
		{name: "null purpose", payloadSQL: `jsonb_set(payload,'{execution_purpose}','null')`},
		{name: "numeric purpose", payloadSQL: `jsonb_set(payload,'{execution_purpose}','1')`},
		{name: "boolean purpose", payloadSQL: `jsonb_set(payload,'{execution_purpose}','true')`},
		{name: "array purpose", payloadSQL: `jsonb_set(payload,'{execution_purpose}','[]')`},
		{name: "recovery without purpose", payloadSQL: `payload-'execution_purpose'`},
		{name: "purpose without recovery", payloadSQL: `payload-ARRAY['recovery_runtime_id','recovery_message_ids']::text[]`},
		{name: "missing recovery runtime", payloadSQL: `payload-'recovery_runtime_id'`},
		{name: "missing recovery messages", payloadSQL: `payload-'recovery_message_ids'`},
		{name: "null recovery runtime", payloadSQL: `jsonb_set(payload,'{recovery_runtime_id}','null')`},
		{name: "numeric recovery runtime", payloadSQL: `jsonb_set(payload,'{recovery_runtime_id}','1')`},
		{name: "zero recovery runtime", payloadSQL: `jsonb_set(payload,'{recovery_runtime_id}','"00000000-0000-0000-0000-000000000000"')`},
		{name: "noncanonical recovery runtime", payloadSQL: `jsonb_set(payload,'{recovery_runtime_id}','"00000000000040008000000000000001"')`},
		{name: "null recovery messages", payloadSQL: `jsonb_set(payload,'{recovery_message_ids}','null')`},
		{name: "scalar recovery messages", payloadSQL: `jsonb_set(payload,'{recovery_message_ids}','1')`},
		{name: "string recovery messages", payloadSQL: `jsonb_set(payload,'{recovery_message_ids}','"1"')`},
		{name: "object recovery messages", payloadSQL: `jsonb_set(payload,'{recovery_message_ids}','{}')`},
		{name: "empty recovery messages", payloadSQL: `jsonb_set(payload,'{recovery_message_ids}','[]')`},
		{name: "recovery messages omit original", payloadSQL: `jsonb_set(payload,'{recovery_message_ids}',jsonb_build_array($3::bigint+1))`},
		{name: "string element recovery message", payloadSQL: `jsonb_set(payload,'{recovery_message_ids}',jsonb_build_array($3::bigint::text))`},
		{name: "null element recovery message", payloadSQL: `jsonb_set(payload,'{recovery_message_ids}',jsonb_build_array($3::bigint,NULL))`},
		{name: "zero recovery message", payloadSQL: `jsonb_set(payload,'{recovery_message_ids}',jsonb_build_array($3::bigint,0))`},
		{name: "negative recovery message", payloadSQL: `jsonb_set(payload,'{recovery_message_ids}',jsonb_build_array($3::bigint,-1))`},
		{name: "fractional recovery message", payloadSQL: `jsonb_set(payload,'{recovery_message_ids}',jsonb_build_array($3::bigint,1.5))`},
		{name: "overflow recovery message", payloadSQL: `jsonb_set(payload,'{recovery_message_ids}',jsonb_build_array($3::bigint,9223372036854775808::numeric))`},
		{name: "nested recovery message", payloadSQL: `jsonb_set(payload,'{recovery_message_ids}',jsonb_build_array($3::bigint,'[]'::jsonb))`},
		{name: "missing continuation runtime", payloadSQL: `payload-'continuation_runtime_id'`},
		{name: "missing continuation message", payloadSQL: `payload-'continuation_message_id'`},
		{name: "missing continuation task", payloadSQL: `payload-'continuation_task_message_ids'`},
		{name: "null continuation runtime", payloadSQL: `jsonb_set(payload,'{continuation_runtime_id}','null')`},
		{name: "numeric continuation runtime", payloadSQL: `jsonb_set(payload,'{continuation_runtime_id}','1')`},
		{name: "zero continuation runtime", payloadSQL: `jsonb_set(payload,'{continuation_runtime_id}','"00000000-0000-0000-0000-000000000000"')`},
		{name: "noncanonical continuation runtime", payloadSQL: `jsonb_set(payload,'{continuation_runtime_id}','"00000000000040008000000000000001"')`},
		{name: "wrong continuation message", payloadSQL: `jsonb_set(payload,'{continuation_message_id}',to_jsonb(($3::bigint+1)::text))`},
		{name: "numeric continuation message", payloadSQL: `jsonb_set(payload,'{continuation_message_id}',to_jsonb($3::bigint))`},
		{name: "null continuation message", payloadSQL: `jsonb_set(payload,'{continuation_message_id}','null')`},
		{name: "null continuation task", payloadSQL: `jsonb_set(payload,'{continuation_task_message_ids}','null')`},
		{name: "nonarray continuation task", payloadSQL: `jsonb_set(payload,'{continuation_task_message_ids}','{}')`},
		{name: "empty continuation task", payloadSQL: `jsonb_set(payload,'{continuation_task_message_ids}','[]')`},
		{name: "continuation task omits original", payloadSQL: `jsonb_set(payload,'{continuation_task_message_ids}',jsonb_build_array($3::bigint+1))`},
		{name: "string continuation task id", payloadSQL: `jsonb_set(payload,'{continuation_task_message_ids}',jsonb_build_array($3::bigint::text))`},
		{name: "null continuation task id", payloadSQL: `jsonb_set(payload,'{continuation_task_message_ids}',jsonb_build_array($3::bigint,NULL))`},
		{name: "zero continuation task id", payloadSQL: `jsonb_set(payload,'{continuation_task_message_ids}',jsonb_build_array($3::bigint,0))`},
		{name: "negative continuation task id", payloadSQL: `jsonb_set(payload,'{continuation_task_message_ids}',jsonb_build_array($3::bigint,-1))`},
		{name: "fractional continuation task id", payloadSQL: `jsonb_set(payload,'{continuation_task_message_ids}',jsonb_build_array($3::bigint,1.5))`},
		{name: "overflow continuation task id", payloadSQL: `jsonb_set(payload,'{continuation_task_message_ids}',jsonb_build_array($3::bigint,9223372036854775808::numeric))`},
		{name: "unreachable recovery unproven", payloadSQL: `payload||'{"recovery_unproven":true}'`},
		{name: "unknown extension key", payloadSQL: `payload||'{"synthetic_unknown_progress":true}'`},
		{name: "changed queued input", payloadSQL: `jsonb_set(payload,'{queued_message_id}',to_jsonb(($3::bigint+1)::text))`},
		{name: "changed lane", payloadSQL: `jsonb_set(payload,'{thread_id}','"00000000-0000-4000-8000-000000000001"')`},
		{name: "changed tenant", payloadSQL: `jsonb_set(payload,'{org_id}','"00000000-0000-4000-8000-000000000001"')`},
		{name: "changed session", payloadSQL: `jsonb_set(payload,'{session_id}','"00000000-0000-4000-8000-000000000001"')`},
		{name: "changed dispatch binding", payloadSQL: `jsonb_set(payload,'{input_dispatch_ids}','["00000000-0000-4000-8000-000000000001"]')`},
		{name: "changed binding version", payloadSQL: `jsonb_set(payload,'{lifecycle_binding_version}','2')`},
		{name: "changed parent epoch", payloadSQL: `jsonb_set(payload,'{admission_session_epoch}','999')`},
		{name: "changed lane epoch", payloadSQL: `jsonb_set(payload,'{admission_thread_epoch}','999')`},
		{name: "changed dispatch kind", payloadSQL: `jsonb_set(payload,'{lifecycle_dispatch_kind}','"human_input"')`},
		{name: "new request binding", payloadSQL: `payload||'{"lifecycle_request_id":"00000000-0000-4000-8000-000000000001"}'`},
		{name: "changed action", payloadSQL: `payload||'{"post_success_action":"push_pr_changes"}'`},
		{name: "changed author", payloadSQL: `payload||'{"post_success_author_mode":"app"}'`},
		{name: "changed changeset", payloadSQL: `payload||'{"changeset_id":"00000000-0000-4000-8000-000000000001"}'`},
		{name: "changed holder", payloadSQL: `payload||'{"changeset_lease_holder_id":"00000000-0000-4000-8000-000000000001"}'`},
		{name: "changed queue", columnSQL: `queue='synthetic_other'`},
		{name: "changed job type", columnSQL: `job_type='run_agent'`},
		{name: "changed retry budget", columnSQL: `max_attempts=max_attempts+1`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := testpostgres.NewPool(t, context.Background(), "../../migrations", 2)
			f := newMessageReplayProgressFixture(t, pool, true, true, nil)
			invokeMessageReplayProgressWriter(t, pool, f, messageReplayProgressWriterNamed(t, "RetryWithLease"))
			requireMessageReplayProgressPayload(t, pool, f, true, true)
			if tt.payloadSQL != "" {
				// This corruption is deliberately synthetic and follows the real
				// positive writer proof; it must never become execution authority.
				admissionPostgresExec(t, pool, `UPDATE jobs SET payload=`+tt.payloadSQL+` WHERE org_id=$1 AND id=$2 AND $3::bigint>0`, f.orgID, f.jobID, f.accepted.Message().ID)
			} else {
				admissionPostgresExec(t, pool, `UPDATE jobs SET `+tt.columnSQL+` WHERE org_id=$1 AND id=$2`, f.orgID, f.jobID)
			}
			before := messageReplayProgressGraph(t, pool, f)
			replayed, effects, err := runAdmissionPostgres(t, pool, f.command)
			require.ErrorIs(t, err, ErrLifecycleConsumerReconciliation, "malformed progress or changed immutable identity must refuse even after legitimate real recovery")
			require.Equal(t, Admission{}, replayed, "a corrupted current job must expose no partial historical receipt")
			require.Empty(t, effects, "refusal must not manufacture notification, enqueue, release or start effects")
			require.Equal(t, before, messageReplayProgressGraph(t, pool, f), "corruption refusal must leave every persisted graph field unchanged")
		})
	}
}

func TestMessageAdmissionReplayRecoveryEnqueueStillExactPostgres(t *testing.T) {
	t.Parallel()
	pool := testpostgres.NewPool(t, context.Background(), "../../migrations", 2)
	f := newMessageReplayProgressFixture(t, pool, true, true, nil)
	binding, options, err := messageAdmissionJobOptions(f.command, f.accepted, f.jobID)
	require.NoError(t, err, "reconstruct the exact original binding and concrete enqueue options")
	changed, err := NewJobStore(pool).RetryWithLease(context.Background(), f.jobID, f.token, "synthetic retry", *options.RunAt)
	require.NoError(t, err, "use the actual retry writer while retaining original scheduling options to isolate payload equality")
	require.True(t, changed, "the exact owned job must really gain legitimate recovery progress")
	requireMessageReplayProgressPayload(t, pool, f, true, true)
	requireMessageReplayProgressReceipt(t, pool, f)
	var sameNonPayload bool
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT queue=$3 AND job_type=$4 AND priority=$5 AND dedupe_key IS NOT DISTINCT FROM $6::text AND target_node_id IS NOT DISTINCT FROM $7::text AND run_at=$8 AND max_attempts=$9 FROM jobs WHERE org_id=$1 AND id=$2`, f.orgID, f.jobID, options.Queue, options.JobType, options.Priority, options.DedupeKey, options.TargetNodeID, options.RunAt, options.MaxAttempts).Scan(&sameNonPayload), "independently pin every other exact enqueue option before testing the payload mismatch")
	require.True(t, sameNonPayload, "only legitimate progress payload fields may differ in this enqueue collision proof")
	before := messageReplayProgressGraph(t, pool, f)
	var enqueued lifecycleEnqueueResult
	require.NoError(t, RunLifecycleTransaction(context.Background(), pool, func(ctx context.Context, tx pgx.Tx) error {
		var enqueueErr error
		enqueued, enqueueErr = enqueueLifecycleJobInTx(ctx, tx, f.orgID, binding, options, nil)
		return enqueueErr
	}), "an exact-ID collision must finish without granting new consumer authority")
	require.Equal(t, lifecycleEnqueueResult{disposition: lifecycleEnqueuePendingReconciliation}, enqueued, "historical replay's allowance must not weaken canonical enqueue's complete payload equality or expose an adopted job")
	require.Equal(t, before, messageReplayProgressGraph(t, pool, f), "enqueue refusal must leave the complete accepted and execution graph unchanged")
}

func TestMessageAdmissionReplayRecoveryPayloadBudgetPostgres(t *testing.T) {
	t.Parallel()
	budget := defaultLifecycleConsumerLimits().payloadBytes
	tests := []struct {
		name     string
		repeats  int
		tooLarge bool
	}{
		{name: "near limit valid-shaped progress", repeats: (budget / 21) * 99 / 100},
		{name: "oversized valid-shaped progress", repeats: budget / 20, tooLarge: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := testpostgres.NewPool(t, context.Background(), "../../migrations", 2)
			f := newMessageReplayProgressFixture(t, pool, true, true, nil)
			invokeMessageReplayProgressWriter(t, pool, f, messageReplayProgressWriterNamed(t, "RetryWithLease"))
			requireMessageReplayProgressPayload(t, pool, f, true, true)
			// The large array is an explicit synthetic resource-bound fixture,
			// not evidence that these IDs exist or that this task may execute.
			// Preserve every immutable key and the valid real-writer triple while
			// varying only the positive-int64 recovery array's serialized size.
			// MaxInt64 repeats lower only element-proportional work; the original
			// accepted input remains explicit and the unchanged byte checks decide.
			admissionPostgresExec(t, pool, `UPDATE jobs SET payload=jsonb_set(payload,'{recovery_message_ids}',to_jsonb(array_prepend($3::bigint,array_fill(9223372036854775807::bigint,ARRAY[$4::integer])))) WHERE org_id=$1 AND id=$2`, f.orgID, f.jobID, f.accepted.Message().ID, tt.repeats)
			var size, arrayLength int
			var firstID int64
			require.NoError(t, pool.QueryRow(context.Background(), `SELECT octet_length(payload::text),(payload->'recovery_message_ids'->>0)::bigint,jsonb_array_length(payload->'recovery_message_ids') FROM jobs WHERE org_id=$1 AND id=$2`, f.orgID, f.jobID).Scan(&size, &firstID, &arrayLength), "measure the actual PostgreSQL canonical representation and explicit input membership used by the bounded replay projection")
			require.Equal(t, f.accepted.Message().ID, firstID, "the synthetic boundary array must explicitly retain the actual accepted input first")
			require.Equal(t, tt.repeats+1, arrayLength, "the canonical boundary array must contain exactly the requested dense repeats plus its original input")
			t.Logf("synthetic canonical progress payload: %d bytes; unchanged replay budget: %d bytes", size, budget)
			if tt.tooLarge {
				require.Greater(t, size, budget, "the oversize fixture must actually cross the unchanged replay payload cap")
				before := messageReplayProgressGraph(t, pool, f)
				replayed, effects, err := runAdmissionPostgres(t, pool, f.command)
				require.ErrorIs(t, err, ErrLifecycleConsumerReconciliation, "the same-row bounded projection must refuse oversized progress as reconciliation, not absence or a scan failure")
				require.Equal(t, Admission{}, replayed, "an oversized execution payload must expose no partial acceptance receipt")
				require.Empty(t, effects, "the payload cap must not grant any mutation or start effects")
				require.Equal(t, before, messageReplayProgressGraph(t, pool, f), "payload-budget refusal must preserve every persisted field")
				return
			}
			require.LessOrEqual(t, size, budget, "the large positive fixture must fit the real unchanged cap")
			require.Greater(t, size, budget*95/100, "the positive fixture must exercise a payload genuinely near the cap")
			requireMessageReplayProgressReceipt(t, pool, f)
		})
	}
}

type messageReplayProgressCancelTx struct {
	pgx.Tx
	cancel  context.CancelFunc
	scanned *bool
}

func (tx messageReplayProgressCancelTx) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	row := tx.Tx.QueryRow(ctx, query, args...)
	if strings.Contains(query, "CASE WHEN octet_length(payload::text)") && strings.Contains(query, "FROM jobs WHERE org_id=$1 AND id=$2") {
		return messageReplayProgressCancelRow{Row: row, cancel: tx.cancel, scanned: tx.scanned}
	}
	return row
}

type messageReplayProgressCancelRow struct {
	pgx.Row
	cancel  context.CancelFunc
	scanned *bool
}

func (row messageReplayProgressCancelRow) Scan(dest ...any) error {
	err := row.Row.Scan(dest...)
	if err == nil {
		*row.scanned = true
		row.cancel()
	}
	return err
}

func TestMessageAdmissionReplayRecoveryCancellationPrecedesRefusalPostgres(t *testing.T) {
	t.Parallel()
	pool := testpostgres.NewPool(t, context.Background(), "../../migrations", 2)
	f := newMessageReplayProgressFixture(t, pool, true, true, nil)
	invokeMessageReplayProgressWriter(t, pool, f, messageReplayProgressWriterNamed(t, "RetryWithLease"))
	requireMessageReplayProgressPayload(t, pool, f, true, true)
	admissionPostgresExec(t, pool, `UPDATE jobs SET payload=payload-'execution_purpose' WHERE org_id=$1 AND id=$2`, f.orgID, f.jobID)
	before := messageReplayProgressGraph(t, pool, f)
	var replayed Admission
	var found, scanned bool
	err := RunLifecycleTransaction(context.Background(), pool, func(ctx context.Context, tx pgx.Tx) error {
		child, cancel := context.WithCancel(ctx)
		defer cancel()
		var replayErr error
		replayed, found, replayErr = FindMessageAdmissionReplayInTx(child, messageReplayProgressCancelTx{Tx: tx, cancel: cancel, scanned: &scanned}, f.orgID, f.sessionID, f.threadID, f.command.Ingress())
		return replayErr
	})
	require.True(t, scanned, "the child context must be cancelled immediately after successfully scanning the real retained job row")
	require.ErrorIs(t, err, context.Canceled, "actual-row cancellation must take precedence over an otherwise malformed progress triple")
	require.NotErrorIs(t, err, ErrLifecycleConsumerReconciliation, "a finished read stream must not hide cancellation behind a semantic reconciliation result")
	require.Equal(t, Admission{}, replayed, "cancelled historical observation must expose no partial receipt")
	require.False(t, found, "cancelled observation must not claim a complete accepted receipt")
	require.Equal(t, before, messageReplayProgressGraph(t, pool, f), "cancellation after the real retained-job scan must leave every graph field unchanged")
}

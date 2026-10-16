package db

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

type continuationRecoveryFixture struct {
	org, session, thread, job, token uuid.UUID
	runtime                          models.ThreadRuntime
	message                          int64
}

func newContinuationRecoveryFixture(t *testing.T, pool *pgxpool.Pool, outcome string, legacy bool) continuationRecoveryFixture {
	t.Helper()
	ctx := context.Background()
	org, session, thread := seedRuntimeSeedThread(t, pool)
	message := &models.SessionMessage{OrgID: org, SessionID: session, ThreadID: &thread, Role: models.MessageRoleUser, TurnNumber: 1, Content: "Original task"}
	require.NoError(t, NewSessionMessageStore(pool).Create(ctx, message), "persist original task boundary")
	entry, err := NewThreadInboxStore(pool).AppendForMessage(ctx, org, AppendThreadInboxEntryParams{SessionID: session, ThreadID: thread, MessageID: message.ID, EntryType: models.ThreadInboxEntryTypeUserMessage, Payload: json.RawMessage(`{"content":"Original task"}`)})
	require.NoError(t, err, "accept original user instruction")
	f := continuationRecoveryFixture{org: org, session: session, thread: thread, job: uuid.New(), token: uuid.New(), message: message.ID}
	payload := map[string]string{"session_id": session.String(), "org_id": org.String(), "thread_id": thread.String()}
	payload["queued_message_id"] = messageIDString(message.ID)
	raw, err := json.Marshal(payload)
	require.NoError(t, err, "marshal original job input")
	_, err = pool.Exec(ctx, `INSERT INTO jobs(id,org_id,queue,job_type,payload,status,attempts,lock_token,locked_at,lease_expires_at) VALUES($1,$2,'agent','continue_session',$3,'running',1,$4,now()-interval '1 minute',now()-interval '1 second')`, f.job, org, raw, f.token)
	require.NoError(t, err, "own ordinary continuation before its provider runtime")
	_, err = pool.Exec(ctx, `UPDATE sessions SET status='running' WHERE org_id=$1 AND id=$2`, org, session)
	require.NoError(t, err, "mark original shared lifecycle running")
	_, err = pool.Exec(ctx, `UPDATE session_threads SET status='running' WHERE org_id=$1 AND id=$2`, org, thread)
	require.NoError(t, err, "mark original thread lifecycle running")
	if outcome == "prior same-turn response" {
		require.NoError(t, NewSessionMessageStore(pool).Create(ctx, &models.SessionMessage{OrgID: org, SessionID: session, ThreadID: &thread, Role: models.MessageRoleAssistant, TurnNumber: 1, Content: "Preceding in-flight response"}), "persist preceding response after queued input, before its own admission")
	}
	f.runtime = createSeedRuntime(t, pool, org, session, thread)
	if !legacy {
		changed, err := NewJobStore(pool).RecordContinuationAttemptWithLease(ctx, org, f.job, f.token, session, thread, f.runtime.ID, f.runtime.LeaseToken, message.ID, []int64{message.ID})
		require.NoError(t, err, "record exact job/runtime/input binding before admission")
		require.True(t, changed, "current job and runtime leases must record the attempt")
	}
	if outcome == "unknown input" {
		_, err = pool.Exec(ctx, `UPDATE thread_inbox_entries SET delivery_state='unknown_delivery' WHERE org_id=$1 AND id=$2`, org, entry.ID)
		require.NoError(t, err, "retain unknown input without provider admission")
	} else {
		phase, _, err := NewSessionActivityPhaseStore(pool).StartSeedPhase(ctx, org, session, thread, 1, f.runtime.ID, f.runtime.LeaseToken, []int64{message.ID}, time.Now())
		require.NoError(t, err, "admit original provider seed atomically")
		if outcome == "generic provider error" || outcome == "maintenance" || outcome == "completed" || outcome == "partial completion" {
			status, reason := models.ActivityPhaseStatusCompleted, models.ActivityPhaseBoundaryFinalResponse
			if outcome == "generic provider error" {
				status, reason = models.ActivityPhaseStatusFailed, models.ActivityPhaseBoundaryError
			}
			if outcome == "maintenance" {
				status, reason = models.ActivityPhaseStatusInterrupted, models.ActivityPhaseBoundaryMaintenance
			}
			_, err = NewSessionActivityPhaseStore(pool).CompletePhase(ctx, org, phase.ID, status, reason, time.Now())
			require.NoError(t, err, "persist exact provider phase boundary")
			if outcome == "completed" || outcome == "partial completion" {
				phaseID := phase.ID
				require.NoError(t, NewSessionMessageStore(pool).Create(ctx, &models.SessionMessage{OrgID: org, SessionID: session, ThreadID: &thread, Role: models.MessageRoleAssistant, TurnNumber: 1, Content: "Final provider response", ActivityPhaseID: &phaseID}), "retain actual later assistant output in recovery history")
			}
		}
	}
	changed, err := NewThreadRuntimeStore(pool).MarkTerminalWithLease(ctx, org, f.runtime.ID, f.runtime.LeaseToken, models.ThreadRuntimeStatusClosed, "attempt ended", "")
	require.NoError(t, err, "close only original owned runtime")
	require.True(t, changed, "original runtime must be durably terminal")
	if outcome == "completed" {
		_, err = pool.Exec(ctx, `UPDATE sessions SET status='idle',current_turn=1 WHERE org_id=$1 AND id=$2`, org, session)
		require.NoError(t, err, "persist shared completion after response")
		_, err = pool.Exec(ctx, `UPDATE session_threads SET status='idle',current_turn=1,last_activity_at=clock_timestamp() WHERE org_id=$1 AND id=$2`, org, thread)
		require.NoError(t, err, "persist thread completion after shared lifecycle")
	}
	return f
}

func messageIDString(id int64) string { return fmt.Sprint(id) }

func TestContinuationRecoveryPostgresAllRetryAndLostOwnershipProducers(t *testing.T) {
	t.Parallel()
	producers := []struct {
		name     string
		executor bool
		invoke   func(context.Context, *pgxpool.Pool, continuationRecoveryFixture) (bool, error)
	}{
		{name: "lease retry", invoke: func(ctx context.Context, p *pgxpool.Pool, f continuationRecoveryFixture) (bool, error) {
			return NewJobStore(p).RetryWithLease(ctx, f.job, f.token, "retry", time.Now())
		}},
		{name: "lease retry target", invoke: func(ctx context.Context, p *pgxpool.Pool, f continuationRecoveryFixture) (bool, error) {
			return NewJobStore(p).RetryWithLeaseAndTarget(ctx, f.job, f.token, "retry", time.Now(), nil)
		}},
		{name: "lease retry without attempt", invoke: func(ctx context.Context, p *pgxpool.Pool, f continuationRecoveryFixture) (bool, error) {
			return NewJobStore(p).RetryWithoutConsumingAttemptWithLease(ctx, f.job, f.token, "retry", time.Now())
		}},
		{name: "lease retry reset window", invoke: func(ctx context.Context, p *pgxpool.Pool, f continuationRecoveryFixture) (bool, error) {
			return NewJobStore(p).RetryWithoutConsumingAttemptWithLeaseAndResetWindow(ctx, f.job, f.token, "retry", time.Now())
		}},
		{name: "lease retry without attempt target", invoke: func(ctx context.Context, p *pgxpool.Pool, f continuationRecoveryFixture) (bool, error) {
			return NewJobStore(p).RetryWithoutConsumingAttemptWithLeaseAndTarget(ctx, f.job, f.token, "retry", time.Now(), nil)
		}},
		{name: "global lost jobs", invoke: func(ctx context.Context, p *pgxpool.Pool, _ continuationRecoveryFixture) (bool, error) {
			n, err := NewJobStore(p).ReclaimLostRunningJobs(ctx, time.Now().Add(-time.Minute), 1)
			return n == 1, err
		}},
		{name: "targeted lost jobs", invoke: func(ctx context.Context, p *pgxpool.Pool, f continuationRecoveryFixture) (bool, error) {
			n, err := NewJobStore(p).ReclaimLostRunningSessionJobsForSession(ctx, f.org, f.session, time.Now().Add(-time.Minute), 1)
			return n == 1, err
		}},
		{name: "global lost executor", executor: true, invoke: func(ctx context.Context, p *pgxpool.Pool, _ continuationRecoveryFixture) (bool, error) {
			n, err := NewSessionExecutorStore(p).ReclaimLost(ctx, time.Now().Add(-time.Minute), 1)
			return n == 1, err
		}},
		{name: "targeted lost executor", executor: true, invoke: func(ctx context.Context, p *pgxpool.Pool, f continuationRecoveryFixture) (bool, error) {
			n, err := NewSessionExecutorStore(p).ReclaimLostForSession(ctx, f.org, f.session, time.Now().Add(-time.Minute), 1)
			return n == 1, err
		}},
	}
	for _, producer := range producers {
		t.Run(producer.name, func(t *testing.T) {
			t.Parallel()
			pool := newFeedbackLifecyclePool(t)
			tests := []struct {
				outcome            string
				legacy             bool
				wantPurpose        bool
				completed, settled bool
			}{
				{outcome: "unfinished before checkpoint", wantPurpose: true},
				{outcome: "generic provider error", wantPurpose: true},
				{outcome: "maintenance", wantPurpose: true},
				{outcome: "prior same-turn response", wantPurpose: true},
				{outcome: "completed", wantPurpose: true, completed: true, settled: true},
				{outcome: "partial completion", wantPurpose: true, completed: true},
				{outcome: "unknown input"},
				{outcome: "legacy exact seed ownership", legacy: true, wantPurpose: true},
			}
			for _, tt := range tests {
				t.Run(tt.outcome, func(t *testing.T) {
					// Global reclaim intentionally scans every tenant in this isolated DB;
					// cases must finish one running job before the next fixture is inserted.
					ctx := context.Background()
					f := newContinuationRecoveryFixture(t, pool, tt.outcome, tt.legacy)
					if producer.executor {
						host := uuid.NewString()
						_, err := pool.Exec(ctx, `INSERT INTO nodes(id) VALUES($1)`, host)
						require.NoError(t, err, "create synthetic executor host")
						id, err := NewSessionExecutorStore(pool).CreateStarting(ctx, f.org, models.CreateSessionExecutorParams{SessionID: f.session, ThreadID: &f.thread, JobID: f.job, JobType: "continue_session", HostNodeID: host, OwnerID: "synthetic-executor", LockToken: f.token})
						require.NoError(t, err, "create owned executor attempt")
						changed, err := NewJobStore(pool).HandoffToSessionExecutorWithLease(ctx, f.org, f.job, f.token, id)
						require.NoError(t, err, "transfer exact job lease to executor")
						require.True(t, changed, "executor handoff must be fenced")
						_, err = pool.Exec(ctx, `UPDATE session_executors SET status='running',heartbeat_at=now()-interval '2 minutes',lease_expires_at=now()-interval '1 second' WHERE org_id=$1 AND id=$2`, f.org, id)
						require.NoError(t, err, "expire synthetic executor ownership")
					}
					changed, err := producer.invoke(ctx, pool, f)
					require.NoError(t, err, "requeue original ordinary continuation through supported producer")
					require.True(t, changed, "supported producer must reclaim or retry its owned job")
					var raw []byte
					var status string
					require.NoError(t, pool.QueryRow(ctx, `SELECT payload,status FROM jobs WHERE org_id=$1 AND id=$2`, f.org, f.job).Scan(&raw, &status), "read durable retry authority")
					var payload struct {
						Purpose string  `json:"execution_purpose"`
						Runtime string  `json:"recovery_runtime_id"`
						Message string  `json:"queued_message_id"`
						IDs     []int64 `json:"recovery_message_ids"`
					}
					require.NoError(t, json.Unmarshal(raw, &payload), "decode durable original input boundary")
					require.Equal(t, "pending", status, "recovery keeps accepted work queued")
					purpose := ""
					if tt.wantPurpose {
						purpose = "checkpoint_recovery"
					}
					require.Equal(t, purpose, payload.Purpose, "only proven applied attempts gain task recovery authority")
					if tt.wantPurpose {
						require.Equal(t, f.runtime.ID.String(), payload.Runtime, "recovery must retain the exact original attempt runtime")
						require.Equal(t, messageIDString(f.message), payload.Message, "newer queued input cannot replace original recovery boundary")
						completed, settled, err := NewThreadInboxStore(pool).InspectContinuationRecovery(ctx, f.org, f.session, f.thread, f.runtime.ID, f.message)
						require.NoError(t, err, "classify exact runtime and durable lifecycle separately")
						require.Equal(t, tt.completed, completed, "same-turn prior output and provider errors cannot suppress unfinished recovery")
						require.Equal(t, tt.settled, settled, "provider response alone cannot acknowledge missing lifecycle completion")
					}
				})
			}
		})
	}
}

func TestContinuationRecoveryPostgresExactAttemptAndRepeatedInputLineage(t *testing.T) {
	t.Parallel()
	pool := newFeedbackLifecyclePool(t)
	for _, jobType := range []string{"continue_session", "run_agent"} {
		t.Run(jobType, func(t *testing.T) { t.Parallel(); verifyContinuationRecoveryPostgresLineage(t, pool, jobType) })
	}
}

func verifyContinuationRecoveryPostgresLineage(t *testing.T, pool *pgxpool.Pool, jobType string) {
	t.Helper()
	ctx := context.Background()
	f := newContinuationRecoveryFixture(t, pool, "unfinished before checkpoint", false)
	_, typeErr := pool.Exec(ctx, `UPDATE jobs SET job_type=$3 WHERE org_id=$1 AND id=$2`, f.org, f.job, jobType)
	require.NoError(t, typeErr, "retain actual original job type throughout repeated checkpoint continuation recovery")
	// The first runtime accepted a combined seed and later confirmed steering.
	// Add another applied input on that exact owner to model confirmed steering.
	nextMessage := func(content string) int64 {
		message := &models.SessionMessage{OrgID: f.org, SessionID: f.session, ThreadID: &f.thread, Role: models.MessageRoleUser, TurnNumber: 1, Content: content}
		require.NoError(t, NewSessionMessageStore(pool).Create(ctx, message), "persist scoped confirmed steering")
		entry, err := NewThreadInboxStore(pool).AppendForMessage(ctx, f.org, AppendThreadInboxEntryParams{SessionID: f.session, ThreadID: f.thread, MessageID: message.ID, EntryType: models.ThreadInboxEntryTypeUserMessage, Payload: json.RawMessage(`{"content":"steering"}`)})
		require.NoError(t, err, "accept steering identity")
		_, err = pool.Exec(ctx, `UPDATE thread_inbox_entries SET delivery_state='acked',runtime_id=$3,applied_at=clock_timestamp() WHERE org_id=$1 AND id=$2`, f.org, entry.ID, f.runtime.ID)
		require.NoError(t, err, "retain positive admission receipt for confirmed steering")
		return message.ID
	}
	second := nextMessage("Second confirmed instruction")
	changed, err := NewJobStore(pool).RetryWithLease(ctx, f.job, f.token, "worker drain", time.Now())
	require.NoError(t, err, "requeue first admitted attempt")
	require.True(t, changed, "first attempt owns job lease")
	var payload struct {
		Runtime string  `json:"recovery_runtime_id"`
		IDs     []int64 `json:"recovery_message_ids"`
	}
	readPayload := func() {
		var raw []byte
		require.NoError(t, pool.QueryRow(ctx, `SELECT payload FROM jobs WHERE org_id=$1 AND id=$2`, f.org, f.job).Scan(&raw), "read exact recovery lineage")
		require.NoError(t, json.Unmarshal(raw, &payload), "decode preserved runtime and message identities")
	}
	readPayload()
	require.Equal(t, []int64{f.message, second}, payload.IDs, "combined task and confirmed steering survive first recovery")
	firstRuntime := f.runtime.ID
	// R1 resumes original task context without admitting those inputs again.
	_, err = pool.Exec(ctx, `UPDATE jobs SET status='running',lock_token=$3,locked_at=clock_timestamp(),lease_expires_at=now()+interval '1 minute' WHERE org_id=$1 AND id=$2`, f.org, f.job, f.token)
	require.NoError(t, err, "claim the same durable recovery job")
	f.runtime = createSeedRuntime(t, pool, f.org, f.session, f.thread)
	changed, err = NewJobStore(pool).RecordContinuationAttemptWithLease(ctx, f.org, f.job, f.token, f.session, f.thread, f.runtime.ID, f.runtime.LeaseToken, f.message, payload.IDs)
	require.NoError(t, err, "bind recovered task to R1 under current lease")
	require.True(t, changed, "R1 must have exact job and runtime authority")
	third := nextMessage("Steering accepted by R1")
	changed, err = NewThreadRuntimeStore(pool).MarkTerminalWithLease(ctx, f.org, f.runtime.ID, f.runtime.LeaseToken, models.ThreadRuntimeStatusClosed, "drain", "")
	require.NoError(t, err, "close only R1")
	require.True(t, changed, "R1 lease must remain owned")
	changed, err = NewJobStore(pool).RetryWithLease(ctx, f.job, f.token, "worker drain", time.Now())
	require.NoError(t, err, "preserve R0 and R1 input identities before R2")
	require.True(t, changed, "R1 job lease must remain owned")
	readPayload()
	require.Equal(t, []int64{f.message, second, third}, payload.IDs, "R0 to R1 to R2 must retain every confirmed input exactly once")
	require.NotEqual(t, firstRuntime.String(), payload.Runtime, "recovery receipt advances only through a fenced recorded attempt")
	// R2 has no newly admitted seed or steering. Its retry still needs the
	// complete R0/R1 task instead of falling back to just R0 or empty R2.
	_, err = pool.Exec(ctx, `UPDATE jobs SET status='running',lock_token=$3,locked_at=clock_timestamp(),lease_expires_at=now()+interval '1 minute' WHERE org_id=$1 AND id=$2`, f.org, f.job, f.token)
	require.NoError(t, err, "claim R2 without introducing new input")
	f.runtime = createSeedRuntime(t, pool, f.org, f.session, f.thread)
	changed, err = NewJobStore(pool).RecordContinuationAttemptWithLease(ctx, f.org, f.job, f.token, f.session, f.thread, f.runtime.ID, f.runtime.LeaseToken, f.message, payload.IDs)
	require.NoError(t, err, "bind preserved R0/R1 context to R2")
	require.True(t, changed, "R2 remains an owned continuation")
	changed, err = NewThreadRuntimeStore(pool).MarkTerminalWithLease(ctx, f.org, f.runtime.ID, f.runtime.LeaseToken, models.ThreadRuntimeStatusClosed, "drain", "")
	require.NoError(t, err, "close R2 without erasing task lineage")
	require.True(t, changed, "R2 closes only its current runtime")
	changed, err = NewJobStore(pool).RetryWithLease(ctx, f.job, f.token, "worker drain", time.Now())
	require.NoError(t, err, "retry R2 with no current-runtime inputs")
	require.True(t, changed, "R2 job fence must remain valid")
	readPayload()
	require.Equal(t, []int64{f.message, second, third}, payload.IDs, "intermediate confirmed steering survives a later empty runtime")
	require.Equal(t, f.runtime.ID.String(), payload.Runtime, "next recovery keeps exact R2 ownership while retaining all preceding task input")
	ids, err := NewThreadInboxStore(pool).ListAppliedRecoveryMessageIDs(ctx, f.org, f.session, f.thread, f.message, payload.IDs)
	require.NoError(t, err, "validate all preserved inputs before using them as task context")
	require.Equal(t, payload.IDs, ids, "recovery context is all-or-error rather than a lossy applied subset")
}

func TestContinuationRecoveryPostgresCannotAdoptNewerDrainedRuntime(t *testing.T) {
	t.Parallel()
	pool := newFeedbackLifecyclePool(t)
	ctx := context.Background()
	tests := []struct {
		name   string
		legacy bool
	}{{name: "recorded attempt"}, {name: "legacy exact queued input", legacy: true}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newContinuationRecoveryFixture(t, pool, "completed", tt.legacy)
			// A later turn on this same thread begins before the original job is acked.
			newer := createSeedRuntime(t, pool, f.org, f.session, f.thread)
			changed, err := NewJobStore(pool).RetryWithLease(ctx, f.job, f.token, "ownership lost", time.Now())
			require.NoError(t, err, "requeue original job while newer turn has its own runtime")
			require.True(t, changed, "original job owns its fence")
			var raw []byte
			require.NoError(t, pool.QueryRow(ctx, `SELECT payload FROM jobs WHERE org_id=$1 AND id=$2`, f.org, f.job).Scan(&raw), "read original job recovery receipt")
			var payload struct {
				Runtime string `json:"recovery_runtime_id"`
			}
			require.NoError(t, json.Unmarshal(raw, &payload), "decode runtime association")
			require.Equal(t, f.runtime.ID.String(), payload.Runtime, "newer drained runtime must never replace the original attempt identity")
			require.NotEqual(t, newer.ID.String(), payload.Runtime, "same-thread freshness alone is not ownership proof")
		})
	}
}

func TestContinuationRecoveryPostgresPreservedInputsRequireCompleteScope(t *testing.T) {
	t.Parallel()
	pool := newFeedbackLifecyclePool(t)
	tests := []struct {
		name                      string
		foreign, missing, unknown bool
	}{{name: "all applied inputs"}, {name: "foreign scope", foreign: true}, {name: "missing identity", missing: true}, {name: "unknown delivery", unknown: true}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			f := newContinuationRecoveryFixture(t, pool, "unfinished before checkpoint", false)
			requested := []int64{f.message}
			if tt.foreign {
				other := newContinuationRecoveryFixture(t, pool, "unfinished before checkpoint", false)
				requested = append(requested, other.message)
			}
			if tt.missing {
				requested = append(requested, 1<<50)
			}
			if tt.unknown {
				_, err := pool.Exec(ctx, `UPDATE thread_inbox_entries SET delivery_state='unknown_delivery',applied_at=NULL WHERE org_id=$1 AND message_id=$2`, f.org, f.message)
				require.NoError(t, err, "seed uncertain preserved input")
			}
			ids, err := NewThreadInboxStore(pool).ListAppliedRecoveryMessageIDs(ctx, f.org, f.session, f.thread, f.message, requested)
			if tt.foreign || tt.missing || tt.unknown {
				require.ErrorIs(t, err, ErrInboxBatchConflict, "invalid preserved identities must refuse the entire task context")
				require.Nil(t, ids, "no valid subset may silently replace the requested recovery task")
			} else {
				require.NoError(t, err, "exact applied task context must remain available")
				require.Equal(t, requested, ids, "exact recovery identities must remain complete")
			}
		})
	}
}

func TestContinuationRecoveryPostgresAttemptRecordFencesScopeAndGeneration(t *testing.T) {
	t.Parallel()
	pool := newFeedbackLifecyclePool(t)
	tests := []struct {
		name, scope string
		success     bool
	}{
		{name: "exact ordinary continuation", success: true},
		{name: "run_agent checkpoint recovery", scope: "run_agent", success: true},
		{name: "stale job token", scope: "job token"},
		{name: "stale runtime token", scope: "runtime token"},
		{name: "expired runtime lease", scope: "expired"},
		{name: "wrong tenant", scope: "org"},
		{name: "wrong session", scope: "session"},
		{name: "wrong thread", scope: "thread"},
		{name: "foreign preserved input", scope: "foreign input"},
		{name: "empty context cannot omit original", scope: "empty context"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			f := newContinuationRecoveryFixture(t, pool, "unfinished before checkpoint", false)
			current := createSeedRuntime(t, pool, f.org, f.session, f.thread)
			org, session, thread, jobToken, runtimeToken := f.org, f.session, f.thread, f.token, current.LeaseToken
			ids := []int64{f.message}
			switch tt.scope {
			case "run_agent":
				_, err := pool.Exec(ctx, `UPDATE jobs SET job_type='run_agent' WHERE org_id=$1 AND id=$2`, f.org, f.job)
				require.NoError(t, err, "model owned run_agent checkpoint recovery")
			case "job token":
				jobToken = uuid.New()
			case "runtime token":
				runtimeToken = uuid.New()
			case "org":
				org = uuid.New()
			case "session":
				session = uuid.New()
			case "thread":
				thread = uuid.New()
			case "foreign input":
				other := newContinuationRecoveryFixture(t, pool, "unfinished before checkpoint", false)
				ids = append(ids, other.message)
			case "empty context":
				ids = []int64{}
			case "expired":
				_, err := pool.Exec(ctx, `UPDATE thread_runtimes SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE org_id=$1 AND id=$2`, f.org, current.ID)
				require.NoError(t, err, "expire attempted runtime authority")
			}
			changed, err := NewJobStore(pool).RecordContinuationAttemptWithLease(ctx, org, f.job, jobToken, session, thread, current.ID, runtimeToken, f.message, ids)
			require.NoError(t, err, "scope and ownership refusal is a fenced no-op")
			require.Equal(t, tt.success, changed, "only exact current ownership and complete task scope can precede provider admission")
			var recorded string
			require.NoError(t, pool.QueryRow(ctx, `SELECT payload->>'continuation_runtime_id' FROM jobs WHERE org_id=$1 AND id=$2`, f.org, f.job).Scan(&recorded), "inspect exact durable runtime binding after admission attempt")
			expected := f.runtime.ID.String()
			if tt.success {
				expected = current.ID.String()
			}
			require.Equal(t, expected, recorded, "failed/fenced recording cannot redirect the original accepted task")
		})
	}
}

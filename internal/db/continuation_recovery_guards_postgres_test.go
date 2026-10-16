package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/stretchr/testify/require"
)

func TestContinuationRecoveryPostgresUnphasedNotesCannotCompletePhasedRuntime(t *testing.T) {
	t.Parallel()
	pool := newFeedbackLifecyclePool(t)
	tests := []struct {
		name, outcome      string
		legacy, otherPhase bool
		completed, settled bool
	}{
		{name: "running phase with preflight note", outcome: "unfinished before checkpoint"},
		{name: "failed phase with preflight note", outcome: "generic provider error"},
		{name: "interrupted phase with preflight note", outcome: "maintenance"},
		{name: "legacy final output without phases", outcome: "unknown input", legacy: true, completed: true},
		{name: "another runtime phase does not hide legacy output", outcome: "unknown input", legacy: true, otherPhase: true, completed: true},
		{name: "real phase final response remains authoritative", outcome: "completed", completed: true, settled: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			f := newContinuationRecoveryFixture(t, pool, tt.outcome, false)
			if tt.legacy {
				_, err := pool.Exec(ctx, `UPDATE thread_inbox_entries SET delivery_state='acked',runtime_id=$4,applied_at=clock_timestamp() WHERE org_id=$1 AND session_id=$2 AND message_id=$3`, f.org, f.session, f.message, f.runtime.ID)
				require.NoError(t, err, "retain historical pre-phase admission proof for the exact runtime")
			}
			if tt.otherPhase {
				other := createSeedRuntime(t, pool, f.org, f.session, f.thread)
				phase, err := NewSessionActivityPhaseStore(pool).StartPhase(ctx, f.org, f.session, f.thread, 2, &other.ID, &other.LeaseToken, models.ActivityPhaseTrigger{Kind: models.ActivityPhaseTriggerRecovery}, time.Now())
				require.NoError(t, err, "create phase for a distinct runtime in the same thread")
				_, err = NewSessionActivityPhaseStore(pool).CompletePhase(ctx, f.org, phase.ID, models.ActivityPhaseStatusFailed, models.ActivityPhaseBoundaryError, time.Now())
				require.NoError(t, err, "finish unrelated phase without giving the legacy runtime phase evidence")
			}
			note := &models.SessionMessage{OrgID: f.org, SessionID: f.session, ThreadID: &f.thread, Role: models.MessageRoleAssistant, TurnNumber: 2, Content: "Authentication could not be refreshed before execution. Please retry."}
			require.NoError(t, NewSessionMessageStore(pool).Create(ctx, note), "persist an unphased startup note after the original runtime started")
			completed, settled, err := NewThreadInboxStore(pool).InspectContinuationRecovery(ctx, f.org, f.session, f.thread, f.runtime.ID, f.message)
			require.NoError(t, err, "classify exact runtime outcome with real PostgreSQL scope rules")
			require.Equal(t, tt.completed, completed, "unphased notes may prove only legacy runtimes with no phase history")
			require.Equal(t, tt.settled, settled, "an unphased note cannot supply durable phase lifecycle completion")
		})
	}
}

func TestContinuationRecoveryPostgresAdmissionCannotMoveBetweenOrdinaryRuntimes(t *testing.T) {
	t.Parallel()
	pool := newFeedbackLifecyclePool(t)
	tests := []struct {
		name, purpose string
		promoted      bool
	}{
		{name: "ordinary continuation"},
		{name: "explicit continue purpose", purpose: "continue"},
		{name: "recorded recovery retains historical task authority", purpose: "checkpoint_recovery", promoted: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			f := newContinuationRecoveryFixture(t, pool, "unknown input", false)
			_, err := pool.Exec(ctx, `UPDATE thread_inbox_entries SET delivery_state='pending',runtime_id=NULL,applied_at=NULL WHERE org_id=$1 AND session_id=$2 AND message_id=$3`, f.org, f.session, f.message)
			require.NoError(t, err, "leave the recorded attempt unadmitted before another owner accepts its input")
			other := createSeedRuntime(t, pool, f.org, f.session, f.thread)
			phase, _, err := NewSessionActivityPhaseStore(pool).StartSeedPhase(ctx, f.org, f.session, f.thread, 1, other.ID, other.LeaseToken, []int64{f.message}, time.Now())
			require.NoError(t, err, "another runtime admits the user input under its own lease")
			_, err = NewSessionActivityPhaseStore(pool).CompletePhase(ctx, f.org, phase.ID, models.ActivityPhaseStatusCompleted, models.ActivityPhaseBoundaryFinalResponse, time.Now())
			require.NoError(t, err, "the other runtime completes its admitted provider phase")
			extra := map[string]any{"execution_purpose": tt.purpose}
			if tt.promoted {
				extra["recovery_runtime_id"] = other.ID.String()
			}
			extraRaw, err := json.Marshal(extra)
			require.NoError(t, err, "encode only the authority present on this request")
			_, err = pool.Exec(ctx, `UPDATE jobs SET payload=payload || $3::jsonb WHERE org_id=$1 AND id=$2`, f.org, f.job, extraRaw)
			require.NoError(t, err, "model trusted explicit recovery separately from ordinary input admission")
			changed, err := NewJobStore(pool).RetryWithLease(ctx, f.job, f.token, "retry recorded owner", time.Now())
			require.NoError(t, err, "retry under the exact original job lease")
			require.True(t, changed, "job ownership should be retried without adopting foreign runtime input")
			var raw []byte
			require.NoError(t, pool.QueryRow(ctx, `SELECT payload FROM jobs WHERE org_id=$1 AND id=$2`, f.org, f.job).Scan(&raw), "inspect the retry authority written by the actual queue producer")
			var payload struct {
				Purpose string `json:"execution_purpose"`
				Runtime string `json:"recovery_runtime_id"`
			}
			require.NoError(t, json.Unmarshal(raw, &payload), "decode exact retry proof")
			wantPurpose, wantRuntime := tt.purpose, ""
			if tt.promoted {
				wantPurpose, wantRuntime = "checkpoint_recovery", f.runtime.ID.String()
			}
			require.Equal(t, wantPurpose, payload.Purpose, "ordinary input requires admission by the recorded owner before gaining checkpoint recovery authority")
			require.Equal(t, wantRuntime, payload.Runtime, "only an existing explicit recovery marker may carry historically applied task input forward")
		})
	}
}

func TestContinuationRecoveryPostgresCheckpointRetryPreservesValidPayload(t *testing.T) {
	t.Parallel()
	pool := newFeedbackLifecyclePool(t)
	tests := []struct {
		name     string
		admitted bool
	}{
		{name: "pre-admission transient remains explicit retry"},
		{name: "admitted task retry preserves historical input lineage", admitted: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			f := newContinuationRecoveryFixture(t, pool, "unfinished before checkpoint", false)
			// The original task was applied by the preceding runtime. Explicit
			// checkpoint retry carries it as context, without admitting it again.
			steering := &models.SessionMessage{OrgID: f.org, SessionID: f.session, ThreadID: &f.thread, Role: models.MessageRoleUser, TurnNumber: 1, Content: "Previously confirmed task steering"}
			require.NoError(t, NewSessionMessageStore(pool).Create(ctx, steering), "retain confirmed historical steering beside the original task")
			entry, err := NewThreadInboxStore(pool).AppendForMessage(ctx, f.org, AppendThreadInboxEntryParams{SessionID: f.session, ThreadID: f.thread, MessageID: steering.ID, EntryType: models.ThreadInboxEntryTypeUserMessage, Payload: json.RawMessage(`{"content":"Previously confirmed task steering"}`)})
			require.NoError(t, err, "preserve the distinct steering message identity")
			_, err = pool.Exec(ctx, `UPDATE thread_inbox_entries SET delivery_state='acked',runtime_id=$3,applied_at=clock_timestamp() WHERE org_id=$1 AND id=$2`, f.org, entry.ID, f.runtime.ID)
			require.NoError(t, err, "retain prior-runtime application proof for the complete task")
			current := createSeedRuntime(t, pool, f.org, f.session, f.thread)
			original := map[string]any{"session_id": f.session.String(), "org_id": f.org.String(), "thread_id": f.thread.String(), "execution_purpose": "checkpoint_retry"}
			raw, err := json.Marshal(original)
			require.NoError(t, err, "encode explicit retry without newly queued input")
			_, err = pool.Exec(ctx, `UPDATE jobs SET payload=$3 WHERE org_id=$1 AND id=$2`, f.org, f.job, raw)
			require.NoError(t, err, "start from the actual checkpoint-retry handler contract")
			changed, err := NewJobStore(pool).RecordContinuationAttemptWithLease(ctx, f.org, f.job, f.token, f.session, f.thread, current.ID, current.LeaseToken, f.message, []int64{f.message, steering.ID})
			require.NoError(t, err, "record checkpoint retry under owned job and runtime leases")
			require.True(t, changed, "explicit checkpoint retry must retain exact attempt ownership")
			var recorded map[string]any
			require.NoError(t, pool.QueryRow(ctx, `SELECT payload FROM jobs WHERE org_id=$1 AND id=$2`, f.org, f.job).Scan(&raw), "read the actual recorded pre-admission payload")
			require.NoError(t, json.Unmarshal(raw, &recorded), "decode recorded continuation metadata")
			expected := map[string]any{
				"session_id": f.session.String(), "org_id": f.org.String(), "thread_id": f.thread.String(), "execution_purpose": "checkpoint_retry",
				"continuation_runtime_id": current.ID.String(), "continuation_message_id": messageIDString(f.message),
				"continuation_task_message_ids": []any{float64(f.message), float64(steering.ID)},
			}
			require.Equal(t, expected, recorded, "recording task context must retain its exact boundary without adding handler-invalid queued input")
			if tt.admitted {
				_, err = pool.Exec(ctx, `UPDATE session_activity_phases SET status='interrupted',boundary_reason='maintenance',completed_at=clock_timestamp() WHERE org_id=$1 AND runtime_id=$2 AND status='running'`, f.org, f.runtime.ID)
				require.NoError(t, err, "settle the interrupted original phase before starting the explicit retry")
				_, err = NewSessionActivityPhaseStore(pool).StartPhase(ctx, f.org, f.session, f.thread, 2, &current.ID, &current.LeaseToken, models.ActivityPhaseTrigger{Kind: models.ActivityPhaseTriggerRecovery}, time.Now())
				require.NoError(t, err, "admit historical task context to the current provider phase")
			}
			changed, err = NewThreadRuntimeStore(pool).MarkTerminalWithLease(ctx, f.org, current.ID, current.LeaseToken, models.ThreadRuntimeStatusClosed, "transient failure", "")
			require.NoError(t, err, "close the owned retry runtime without changing earlier task receipts")
			require.True(t, changed, "retry runtime must still be owned")
			changed, err = NewJobStore(pool).RetryWithLease(ctx, f.job, f.token, "transient failure", time.Now())
			require.NoError(t, err, "produce the next retry payload through the real queue mutation")
			require.True(t, changed, "owned queue retry must remain available")
			require.NoError(t, pool.QueryRow(ctx, `SELECT payload FROM jobs WHERE org_id=$1 AND id=$2`, f.org, f.job).Scan(&raw), "read the durable retry result")
			var retried map[string]any
			require.NoError(t, json.Unmarshal(raw, &retried), "decode the produced retry contract")
			if tt.admitted {
				expected["execution_purpose"] = "checkpoint_recovery"
				expected["recovery_runtime_id"] = current.ID.String()
				expected["recovery_message_ids"] = []any{float64(f.message), float64(steering.ID)}
				expected["queued_message_id"] = messageIDString(f.message)
			}
			require.Equal(t, expected, retried, "pre-admission retry must remain handler-valid, while admitted recovery preserves exact runtime and complete historical task lineage")
		})
	}
}

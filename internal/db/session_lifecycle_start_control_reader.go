package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type lifecycleStartControlReadLimits struct{ snapshotBytes, payloadBytes int }

const lifecycleStartControlMetadataBytes = 4096

func defaultLifecycleStartControlReadLimits() lifecycleStartControlReadLimits {
	return lifecycleStartControlReadLimits{snapshotBytes: 256 << 10, payloadBytes: legacyJobRouteMaxBytes}
}

// JSONB values are length-checked before their text is returned. The exact
// payload::text bytes are hashed by the DTO matcher; re-marshalling a Go map
// would lose immutable PostgreSQL numeric/text identity. Small finite metadata
// is bounded too, including owner strings that may only be inspected for NULL.
const lifecycleStartControlReadQuery = `SELECT
 d.id,d.org_id,d.session_id,d.thread_id,d.request_id,d.job_id,d.runtime_id,
 d.session_epoch,d.thread_epoch,d.kind,COALESCE(d.admission_state,''),COALESCE(d.consumer_kind,''),
 d.job_lock_token,d.runtime_lease_token,d.admission_claim_token,d.admission_claim_turn,
 CASE WHEN octet_length(d.payload_digest)<=$5 THEN d.payload_digest ELSE '' END,
 d.control_receipt_id,d.control_version,d.control_state,
 CASE WHEN octet_length(d.control_snapshot::text)<=$3 THEN d.control_snapshot::text END,
 d.control_next_attempt_at,d.control_refund_receipt_id,
 COALESCE(octet_length(d.control_snapshot::text),0),
 COALESCE(j.id,'00000000-0000-0000-0000-000000000000'),COALESCE(j.org_id,'00000000-0000-0000-0000-000000000000'),
 CASE WHEN octet_length(j.queue)<=$5 THEN j.queue ELSE '' END,
 CASE WHEN octet_length(j.job_type)<=$5 THEN j.job_type ELSE '' END,COALESCE(j.status,''),
 CASE WHEN octet_length(j.payload::text)<=$4 THEN j.payload::text END,
 CASE WHEN octet_length(j.locked_by_node_id)<=$5 THEN j.locked_by_node_id END,
 CASE WHEN octet_length(j.run_owner_id)<=$5 THEN j.run_owner_id END,
 j.lock_token,j.locked_at,j.lease_expires_at,j.completed_at,
 COALESCE(octet_length(j.payload::text),0),
 GREATEST(COALESCE(octet_length(d.payload_digest),0),COALESCE(octet_length(j.queue),0),
  COALESCE(octet_length(j.job_type),0),COALESCE(octet_length(j.locked_by_node_id),0),COALESCE(octet_length(j.run_owner_id),0)),
 r.id IS NOT NULL AND r.thread_id IS NOT DISTINCT FROM d.thread_id,
 d.thread_id IS NULL OR t.id IS NOT NULL
 FROM session_input_dispatches d
 LEFT JOIN jobs j ON j.org_id=d.org_id AND j.id=d.job_id
 LEFT JOIN session_human_input_requests r ON r.org_id=d.org_id AND r.session_id=d.session_id AND r.id=d.request_id
 LEFT JOIN session_threads t ON t.org_id=d.org_id AND t.session_id=d.session_id AND t.id=d.thread_id
 WHERE d.org_id=$1 AND d.id=$2`

// readLifecycleStartControlInTx reads one exact retained logical control, not a
// current preparation or execution permit. The caller already holds the exact
// higher-rank job and parent/explicit-lane locks in its configured bounded READ
// COMMITTED transaction. No locks, settings, transaction boundaries, retries or
// effects are introduced here. Due times and preparation lifetime do not expire
// retained identity. Receipt references remain structural; B/E must prove their
// physical/no-launch meaning before any later launch or historical exclusion.
func readLifecycleStartControlInTx(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, binding LifecycleJobBinding) (lifecycleStartControlSnapshot, error) {
	return readLifecycleStartControlWithLimitsInTx(ctx, tx, orgID, binding, defaultLifecycleStartControlReadLimits())
}

func readLifecycleStartControlWithLimitsInTx(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, binding LifecycleJobBinding, limits lifecycleStartControlReadLimits) (lifecycleStartControlSnapshot, error) {
	zero := lifecycleStartControlSnapshot{}
	if ctx == nil || nilLifecycleDependency(tx) {
		return zero, errors.New("retained control reader requires a context and supplied transaction")
	}
	if limits.snapshotBytes <= 0 || limits.payloadBytes <= 0 {
		return zero, errors.New("retained control read budgets must be positive")
	}
	var dispatchID uuid.UUID
	if len(binding.dispatchIDs) == 1 {
		dispatchID = binding.dispatchIDs[0]
	}
	bad := func() (lifecycleStartControlSnapshot, error) {
		return zero, reconcileLifecycleConsumer(lifecycleConsumerDispatch, dispatchID)
	}
	if orgID == uuid.Nil || binding.Validate() != nil || binding.orgID != orgID || len(binding.dispatchIDs) != 1 || (binding.kind != models.LifecycleDispatchAnswer && binding.kind != models.LifecycleDispatchDenial) {
		return bad()
	}
	if err := requireLifecycleScopeTransactionInTx(ctx, tx); err != nil {
		return zero, err
	}
	var dispatch lifecycleStartControlDispatchRow
	var job models.Job
	var snapshotText, payloadText *string
	var snapshotBytes, payloadBytes, metadataBytes int
	var requestMatches, laneMatches bool
	err := tx.QueryRow(ctx, lifecycleStartControlReadQuery, orgID, binding.dispatchIDs[0], limits.snapshotBytes, limits.payloadBytes, lifecycleStartControlMetadataBytes).Scan(
		&dispatch.ID, &dispatch.OrgID, &dispatch.SessionID, &dispatch.ThreadID, &dispatch.RequestID, &dispatch.JobID, &dispatch.RuntimeID,
		&dispatch.SessionEpoch, &dispatch.ThreadEpoch, &dispatch.Kind, &dispatch.AdmissionState, &dispatch.ConsumerKind,
		&dispatch.JobLockToken, &dispatch.RuntimeLeaseToken, &dispatch.AdmissionClaimToken, &dispatch.AdmissionClaimTurn, &dispatch.PayloadDigest,
		&dispatch.ControlReceiptID, &dispatch.ControlVersion, &dispatch.ControlState, &snapshotText, &dispatch.ControlNextAttemptAt, &dispatch.ControlRefundReceiptID,
		&snapshotBytes, &job.ID, &job.OrgID, &job.Queue, &job.JobType, &job.Status, &payloadText,
		&job.LockedByNodeID, &job.RunOwnerID, &job.LockToken, &job.LockedAt, &job.LeaseExpiresAt, &job.CompletedAt,
		&payloadBytes, &metadataBytes, &requestMatches, &laneMatches)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return bad()
		}
		return zero, fmt.Errorf("read retained lifecycle control rows: %w", err)
	}
	if snapshotBytes > limits.snapshotBytes || payloadBytes > limits.payloadBytes || metadataBytes > lifecycleStartControlMetadataBytes {
		return zero, &lifecycleConsumerError{cause: ErrLifecycleConsumerDiscoveryIncomplete, source: lifecycleConsumerDispatch, id: dispatch.ID}
	}
	if snapshotText == nil || payloadText == nil || !requestMatches || !laneMatches || job.Queue != "agent" || !slices.Contains([]string{"run_agent", "continue_session"}, job.JobType) {
		return bad()
	}
	dispatch.ControlSnapshot = json.RawMessage(*snapshotText)
	job.Payload = json.RawMessage(*payloadText)
	actual, err := decodeLifecycleJobBindingPayload(job.OrgID, job.ID, job.Payload)
	if err != nil || !sameLifecycleJobBinding(actual, binding) {
		return bad()
	}
	snapshot, err := decodeLifecycleStartControlRows(dispatch, job)
	if err != nil {
		return bad()
	}
	spec, err := snapshot.Spec()
	if err != nil || !sameLifecycleJobBinding(spec.Binding, binding) {
		return bad()
	}
	if err := ctx.Err(); err != nil {
		return zero, fmt.Errorf("retained lifecycle control context after read: %w", err)
	}
	return snapshot, nil
}

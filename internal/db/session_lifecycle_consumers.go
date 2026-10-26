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

var (
	ErrLifecycleConsumerReconciliation      = errors.New("lifecycle consumer requires scoped reconciliation")
	ErrLifecycleConsumerDiscoveryIncomplete = errors.New("lifecycle consumer discovery incomplete")
)

type lifecycleConsumerSource string

const (
	lifecycleConsumerDispatch    lifecycleConsumerSource = "dispatch"
	lifecycleConsumerRuntime     lifecycleConsumerSource = "runtime"
	lifecycleConsumerExecutor    lifecycleConsumerSource = "executor"
	lifecycleConsumerInbox       lifecycleConsumerSource = "inbox"
	lifecycleConsumerPreparation lifecycleConsumerSource = "preparation"
	lifecycleConsumerRecheck     lifecycleConsumerSource = "recheck"
	lifecycleConsumerAutomation  lifecycleConsumerSource = "automation"
	lifecycleConsumerHolder      lifecycleConsumerSource = "holder"
	lifecycleConsumerLegacyJob   lifecycleConsumerSource = "legacy_job"
)

func (s lifecycleConsumerSource) validate() error {
	switch s {
	case lifecycleConsumerDispatch, lifecycleConsumerRuntime, lifecycleConsumerExecutor,
		lifecycleConsumerInbox, lifecycleConsumerPreparation, lifecycleConsumerRecheck,
		lifecycleConsumerAutomation, lifecycleConsumerHolder, lifecycleConsumerLegacyJob:
		return nil
	default:
		return errors.New("unknown lifecycle consumer source")
	}
}

type lifecycleConsumerError struct {
	cause  error
	source lifecycleConsumerSource
	id     uuid.UUID
}

func (e *lifecycleConsumerError) Error() string {
	return fmt.Sprintf("%s: %s %s", e.cause, e.source, e.id)
}
func (e *lifecycleConsumerError) Unwrap() error { return e.cause }

func reconcileLifecycleConsumer(source lifecycleConsumerSource, id uuid.UUID) error {
	return &lifecycleConsumerError{cause: ErrLifecycleConsumerReconciliation, source: source, id: id}
}

// The exception is an exact immutable input binding and optionally its current
// canonical runtime allocation, never an ignore-job-ID escape hatch.
type lifecycleOwnConsumer struct {
	binding LifecycleJobBinding
	attempt *AttemptFence
}

type lifecycleConsumerLimits struct {
	structuredRows, legacyJobs, payloadBytes, laneAnchors int
}

// Fixed private work budgets are discovery limits, not product configuration.
// Overflow cannot become a partial negative result. B's legacy normalization
// must retire uninterpretable historical physical records before clear reads.
func defaultLifecycleConsumerLimits() lifecycleConsumerLimits {
	return lifecycleConsumerLimits{512, 512, 8 << 20, 4096}
}

type lifecycleConsumerEvidence struct {
	source                                                  lifecycleConsumerSource
	id, sessionID                                           uuid.UUID
	threadID, threadSessionID, jobID, runtimeID, foundJobID *uuid.UUID
	state, controlState, jobStatus                          string
	wholeSession, physical, ownerMatches                    bool
}

// Each branch is independently bounded before the final bounded union. Reverse
// references discover scope even when the job is absent or its payload is bad.
// Runtime/executor/inbox/holder membership uses both the recorded session and
// the actual tenant-scoped thread parent, including contradictory legacy rows.
const lifecycleConsumerEvidenceQuery = `WITH evidence AS (
 (SELECT 'dispatch'::text source,d.id,d.org_id,d.session_id,d.thread_id,t.session_id thread_session_id,
   d.job_id,d.runtime_id,COALESCE(d.admission_state,'') state,COALESCE(d.control_state,'') control_state,
   false whole_session,false physical,true owner_matches
  FROM session_input_dispatches d LEFT JOIN session_threads t ON t.org_id=d.org_id AND t.id=d.thread_id
  LEFT JOIN jobs j ON j.org_id=d.org_id AND j.id=d.job_id
  WHERE d.org_id=$1 AND (d.session_id=$2 OR t.session_id=$2)
    AND (d.admission_state='bound' OR d.control_state IN ('awaiting_control','retry_queued','superseded_delivery_uncertain')
      OR j.status IN ` + jobNonterminalInputConsumerStatusesSQL + `)
  ORDER BY d.id LIMIT $3)
 UNION ALL
 (SELECT 'runtime',r.id,r.org_id,r.session_id,r.thread_id,t.session_id,r.job_id,r.id,r.status,'',false,true,true
  FROM thread_runtimes r LEFT JOIN session_threads t ON t.org_id=r.org_id AND t.id=r.thread_id
  WHERE r.org_id=$1 AND (r.session_id=$2 OR t.session_id=$2) ORDER BY r.id LIMIT $3)
 UNION ALL
 (SELECT 'executor',e.id,e.org_id,e.session_id,e.thread_id,t.session_id,e.job_id,NULL::uuid,e.status,'',false,true,true
  FROM session_executors e LEFT JOIN session_threads t ON t.org_id=e.org_id AND t.id=e.thread_id
  WHERE e.org_id=$1 AND (e.session_id=$2 OR t.session_id=$2) ORDER BY e.id LIMIT $3)
 UNION ALL
 (SELECT 'inbox',i.id,i.org_id,i.session_id,i.thread_id,t.session_id,NULL::uuid,i.runtime_id,i.delivery_state,'',false,true,true
  FROM thread_inbox_entries i LEFT JOIN session_threads t ON t.org_id=i.org_id AND t.id=i.thread_id
  WHERE i.org_id=$1 AND (i.session_id=$2 OR t.session_id=$2) AND i.applied_at IS NULL
    AND i.delivery_state IN ('delivering','delivered','unknown_delivery','acked') ORDER BY i.id LIMIT $3)
 UNION ALL
 (SELECT 'preparation',p.id,p.org_id,p.session_id,p.thread_id,t.session_id,p.job_id,NULL::uuid,p.state,'',
   p.physical_operation_scope='whole_session',p.physical_operation_state<>'none',true
  FROM session_runtime_start_preparations p LEFT JOIN session_threads t ON t.org_id=p.org_id AND t.id=p.thread_id
  LEFT JOIN jobs j ON j.org_id=p.org_id AND j.id=p.job_id
  WHERE p.org_id=$1 AND (p.session_id=$2 OR t.session_id=$2)
    AND (p.state IN ('preparing','authorized') OR p.physical_operation_state<>'none'
      OR j.status IN ` + jobNonterminalInputConsumerStatusesSQL + `) ORDER BY p.id LIMIT $3)
 UNION ALL
 (SELECT 'recheck',d.assessment_id,d.org_id,d.session_id,d.thread_id,t.session_id,d.job_id,NULL::uuid,d.status,'',false,false,true
  FROM code_review_recheck_dispatches d LEFT JOIN session_threads t ON t.org_id=d.org_id AND t.id=d.thread_id
  LEFT JOIN jobs j ON j.org_id=d.org_id AND j.id=d.job_id
  WHERE d.org_id=$1 AND (d.session_id=$2 OR t.session_id=$2)
    AND (d.status IN ('pending','running') OR j.status IN ` + jobNonterminalInputConsumerStatusesSQL + `) ORDER BY d.assessment_id LIMIT $3)
 UNION ALL
 (SELECT 'automation',a.id,a.org_id,COALESCE(a.session_id,g.session_id,t.session_id),a.thread_id,t.session_id,a.job_id,NULL::uuid,a.status,'',false,false,
   a.session_id IS NOT NULL AND (g.session_id IS NULL OR g.session_id=a.session_id)
  FROM automation_runs a LEFT JOIN session_threads t ON t.org_id=a.org_id AND t.id=a.thread_id
  LEFT JOIN automation_target_sessions g ON g.org_id=a.org_id AND g.target_id=a.target_id AND g.generation=a.target_generation
  LEFT JOIN jobs j ON j.org_id=a.org_id AND j.id=a.job_id
  WHERE a.org_id=$1 AND (a.session_id=$2 OR t.session_id=$2 OR g.session_id=$2) AND a.job_id IS NOT NULL
    AND (a.status IN ('pending','running') OR a.dispatch_state='executing' OR j.status IN ` + jobNonterminalInputConsumerStatusesSQL + `) ORDER BY a.id LIMIT $3)
 UNION ALL
 (SELECT 'automation',a.id,a.org_id,l.session_id,NULL::uuid,NULL::uuid,a.job_id,NULL::uuid,a.status,'',false,false,true
  FROM session_automation_links l JOIN automation_runs a ON a.org_id=l.org_id AND a.id=l.automation_run_id
  LEFT JOIN jobs j ON j.org_id=a.org_id AND j.id=a.job_id
  WHERE l.org_id=$1 AND l.session_id=$2 AND a.session_id IS NULL AND a.job_id IS NOT NULL
    AND (a.status IN ('pending','running') OR a.dispatch_state='executing' OR j.status IN ` + jobNonterminalInputConsumerStatusesSQL + `) ORDER BY a.id LIMIT $3)
 UNION ALL
 (SELECT 'holder',h.id,h.org_id,h.session_id,r.thread_id,t.session_id,r.job_id,h.holder_id,COALESCE(r.status,''),'',false,true,
   h.holder_kind='thread_runtime' AND r.id IS NOT NULL AND r.session_id=h.session_id
    AND r.container_id=h.container_id AND r.owner_node_id=h.owner_node_id AND r.lease_token=h.lease_token
  FROM session_sandbox_holders h
  LEFT JOIN thread_runtimes r ON r.org_id=h.org_id AND r.id=h.holder_id AND h.holder_kind='thread_runtime'
  LEFT JOIN session_threads t ON t.org_id=r.org_id AND t.id=r.thread_id
  WHERE h.org_id=$1 AND (h.session_id=$2 OR r.session_id=$2 OR t.session_id=$2)
    AND h.holder_kind NOT IN ('preview','snapshot','operator','code_review') ORDER BY h.id LIMIT $3)
)
SELECT e.source,e.id,e.session_id,e.thread_id,e.thread_session_id,e.job_id,e.runtime_id,
 e.state,e.control_state,COALESCE(e.whole_session,false),e.physical,e.owner_matches,j.id,COALESCE(j.status,'')
FROM evidence e LEFT JOIN jobs j ON j.org_id=e.org_id AND j.id=e.job_id
LIMIT $3`

const lifecycleConsumerLegacyJobsQuery = `SELECT id,octet_length(payload::text),
 CASE WHEN octet_length(payload::text)<=$3 THEN payload ELSE NULL END
 FROM jobs WHERE org_id=$1 AND job_type IN ('run_agent','continue_session')
 AND status IN ` + jobNonterminalInputConsumerStatusesSQL + ` ORDER BY id LIMIT $2`

// HasCompetingInputConsumerInTx is the canonical observation/predicate for the
// supplied tenant/session/lane scope. The caller already holds the parent and
// explicitly selected lanes in canonical order, plus any required higher-rank
// locks. This reader acquires no discovered job locks, and never begins,
// configures, settles, retries, or performs external effects.
//
// A negative is meaningful only for participating admission/start writers that
// serialize through that same parent. G/H's complete writer/adapter floor is
// still required before activation or the first parked input. Epochs, due time,
// lease expiry and terminal labels never erase a possible physical predecessor.
// The starter/claimer floor must reject payload/job tenant mismatch before
// effects: this tenant-scoped scan cannot discover a different tenant's job row
// attempting to route here through legacy parseOrgID. Cross-tenant discovery is
// not a substitute for that prerequisite.
// B settlement/normalization must supply a trusted exact drain validator before
// uninterpretable terminal runtime/executor/preparation evidence can clear.
func HasCompetingInputConsumerInTx(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, fence AdmissionFence, own *lifecycleOwnConsumer) (bool, error) {
	return hasCompetingInputConsumerInTx(ctx, tx, orgID, fence, own, defaultLifecycleConsumerLimits())
}

func hasCompetingInputConsumerInTx(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, fence AdmissionFence, own *lifecycleOwnConsumer, limits lifecycleConsumerLimits) (bool, error) {
	if ctx == nil || nilLifecycleDependency(tx) {
		return false, errors.New("lifecycle consumer requires a context and supplied transaction")
	}
	if orgID == uuid.Nil {
		return false, errors.New("lifecycle consumer requires a nonzero organization")
	}
	if err := fence.Validate(); err != nil {
		return false, err
	}
	if limits.structuredRows <= 0 || limits.legacyJobs <= 0 || limits.payloadBytes <= 0 || limits.laneAnchors <= 0 {
		return false, errors.New("lifecycle consumer discovery budgets must be positive")
	}
	if err := requireLifecycleScopeTransactionInTx(ctx, tx); err != nil {
		return false, err
	}
	var recovery, member bool
	err := tx.QueryRow(ctx, `SELECT workspace_recovery_id IS NOT NULL OR workspace_recovery_token IS NOT NULL
	 OR workspace_recovery IS NOT NULL OR workspace_recovery_next_attempt_at IS NOT NULL,
	 (NOT $3::boolean OR EXISTS(SELECT 1 FROM session_threads t WHERE t.org_id=$1 AND t.session_id=$2 AND t.id=$4))
	 FROM sessions WHERE org_id=$1 AND id=$2`, orgID, fence.sessionID, fence.threaded, fence.threadID).Scan(&recovery, &member)
	if err != nil {
		return false, fmt.Errorf("read lifecycle consumer parent: %w", err)
	}
	if !member {
		return false, reconcileLifecycleConsumer(lifecycleConsumerDispatch, fence.threadID)
	}
	proof, err := proveOwnLifecycleConsumerInTx(ctx, tx, orgID, fence, own, limits.structuredRows)
	if err != nil {
		return false, err
	}
	rows, err := tx.Query(ctx, lifecycleConsumerEvidenceQuery, orgID, fence.sessionID, limits.structuredRows+1)
	if err != nil {
		return false, fmt.Errorf("read structured lifecycle consumers: %w", err)
	}
	evidence, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (lifecycleConsumerEvidence, error) {
		var v lifecycleConsumerEvidence
		err := row.Scan(&v.source, &v.id, &v.sessionID, &v.threadID, &v.threadSessionID, &v.jobID, &v.runtimeID, &v.state, &v.controlState, &v.wholeSession, &v.physical, &v.ownerMatches, &v.foundJobID, &v.jobStatus)
		return v, err
	})
	if err != nil {
		return false, fmt.Errorf("collect structured lifecycle consumers: %w", err)
	}
	if len(evidence) > limits.structuredRows {
		return false, &lifecycleConsumerError{cause: ErrLifecycleConsumerDiscoveryIncomplete, source: lifecycleConsumerDispatch}
	}
	blocked := recovery
	scopedJobs := make(map[uuid.UUID]bool)
	for _, v := range evidence {
		overlap, err := lifecycleConsumerEvidenceOverlaps(v, fence)
		if err != nil {
			return false, err
		}
		if !overlap {
			continue
		}
		if v.jobID != nil {
			scopedJobs[*v.jobID] = true
		}
		competes, err := classifyLifecycleConsumerEvidence(v, proof)
		if err != nil {
			return false, err
		}
		blocked = blocked || competes
	}
	legacyBlocked, err := readLegacyLifecycleConsumersInTx(ctx, tx, orgID, fence, proof, scopedJobs, limits)
	if err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, fmt.Errorf("lifecycle consumer context after discovery: %w", err)
	}
	return blocked || legacyBlocked, nil
}

func lifecycleConsumerEvidenceOverlaps(v lifecycleConsumerEvidence, fence AdmissionFence) (bool, error) {
	if err := v.source.validate(); err != nil {
		return false, err
	}
	if v.sessionID != fence.sessionID && (v.threadSessionID == nil || *v.threadSessionID != fence.sessionID) {
		return false, reconcileLifecycleConsumer(v.source, v.id)
	}
	if v.threadID != nil && (v.threadSessionID == nil || *v.threadSessionID != v.sessionID) {
		return false, reconcileLifecycleConsumer(v.source, v.id)
	}
	return !fence.threaded || v.wholeSession || v.threadID == nil || *v.threadID == fence.threadID, nil
}

type lifecycleOwnConsumerProof struct {
	binding   *LifecycleJobBinding
	runtimeID uuid.UUID
}

func classifyLifecycleConsumerEvidence(v lifecycleConsumerEvidence, own lifecycleOwnConsumerProof) (bool, error) {
	if !v.ownerMatches || (v.jobID != nil && v.foundJobID == nil) {
		return false, reconcileLifecycleConsumer(v.source, v.id)
	}
	ownedJob := own.binding != nil && v.jobID != nil && *v.jobID == own.binding.jobID
	switch v.source {
	case lifecycleConsumerDispatch:
		if v.controlState == "superseded_delivery_uncertain" {
			return false, reconcileLifecycleConsumer(v.source, v.id)
		}
		if v.controlState == "awaiting_control" || v.controlState == "retry_queued" {
			return true, nil
		}
		if v.state != "bound" {
			return false, reconcileLifecycleConsumer(v.source, v.id)
		}
		if v.jobID == nil || !models.JobStatus(v.jobStatus).IsNonterminalInputConsumer() {
			return false, reconcileLifecycleConsumer(v.source, v.id)
		}
		return !(ownedJob && slices.Contains(own.binding.dispatchIDs, v.id)), nil
	case lifecycleConsumerRuntime, lifecycleConsumerHolder:
		if !slices.Contains([]string{"starting", "live", "paused", "draining"}, v.state) {
			return false, reconcileLifecycleConsumer(v.source, v.id)
		}
		return !(ownedJob && v.runtimeID != nil && *v.runtimeID == own.runtimeID), nil
	case lifecycleConsumerExecutor:
		if !slices.Contains([]string{"starting", "running", "draining"}, v.state) {
			return false, reconcileLifecycleConsumer(v.source, v.id)
		}
		return true, nil
	case lifecycleConsumerInbox:
		// Delivery can survive terminal jobs/runtimes; never fold it into the
		// own-job exception. Acknowledgment alone is not positive application.
		return true, nil
	case lifecycleConsumerPreparation:
		if v.physical {
			return false, reconcileLifecycleConsumer(v.source, v.id)
		}
		return true, nil
	case lifecycleConsumerRecheck, lifecycleConsumerAutomation:
		if ownedJob {
			f := own.binding.fence
			if f.sessionID != v.sessionID || f.threaded != (v.threadID != nil) || (f.threaded && f.threadID != *v.threadID) {
				return false, reconcileLifecycleConsumer(v.source, v.id)
			}
		}
		return !ownedJob, nil
	default:
		return false, reconcileLifecycleConsumer(v.source, v.id)
	}
}

// proveOwnLifecycleConsumerInTx composes only durable identity proof within the
// caller's already configured, bounded and locked transaction. It is shared by
// the consumer reader and exact enqueue-collision replay; it is not an execution
// permit. Retained controls establish logical replay identity only. Typed
// launch/settlement joins must extend it before excluding physical evidence.
func proveOwnLifecycleConsumerInTx(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, fence AdmissionFence, own *lifecycleOwnConsumer, cap int) (lifecycleOwnConsumerProof, error) {
	if own == nil {
		return lifecycleOwnConsumerProof{}, nil
	}
	bad := func() (lifecycleOwnConsumerProof, error) {
		return lifecycleOwnConsumerProof{}, reconcileLifecycleConsumer(lifecycleConsumerLegacyJob, own.binding.jobID)
	}
	if len(own.binding.dispatchIDs) > cap {
		return lifecycleOwnConsumerProof{}, &lifecycleConsumerError{cause: ErrLifecycleConsumerDiscoveryIncomplete, source: lifecycleConsumerDispatch, id: own.binding.jobID}
	}
	if own.binding.Validate() != nil || own.binding.orgID != orgID || own.binding.fence != fence {
		return bad()
	}
	if own.attempt != nil && (own.attempt.Validate() != nil || !sameLifecycleJobBinding(own.binding, own.attempt.binding)) {
		return bad()
	}
	var payload json.RawMessage
	var status, queue, jobType string
	var token *uuid.UUID
	var jobLeaseCurrent bool
	var size int
	// Current leases use the database's fresh statement clock for both the job
	// and allocation probes, never the app clock or transaction-start now().
	err := tx.QueryRow(ctx, `SELECT CASE WHEN octet_length(payload::text)<=$3 THEN payload ELSE NULL END,
	 status,queue,job_type,lock_token,COALESCE(lease_expires_at>statement_timestamp(),false),octet_length(payload::text) FROM jobs WHERE org_id=$1 AND id=$2`, orgID, own.binding.jobID, legacyJobRouteMaxBytes).Scan(&payload, &status, &queue, &jobType, &token, &jobLeaseCurrent, &size)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return bad()
		}
		return lifecycleOwnConsumerProof{}, fmt.Errorf("read actual lifecycle own job: %w", err)
	}
	if size > legacyJobRouteMaxBytes {
		return lifecycleOwnConsumerProof{}, &lifecycleConsumerError{cause: ErrLifecycleConsumerDiscoveryIncomplete, source: lifecycleConsumerLegacyJob, id: own.binding.jobID}
	}
	actual, err := decodeLifecycleJobBindingPayload(orgID, own.binding.jobID, payload)
	if err != nil || !sameLifecycleJobBinding(actual, own.binding) || !models.JobStatus(status).IsNonterminalInputConsumer() || queue != "agent" || !slices.Contains([]string{"run_agent", "continue_session"}, jobType) {
		return bad()
	}
	parked := status == string(models.JobStatusAwaitingStartControl)
	// Historical/runtime-bound parking is not a live allocation exception.
	// Its raw revoked references need B's later historical physical proof.
	if parked && own.attempt != nil {
		return bad()
	}
	if own.attempt != nil && (status != "running" || token == nil || *token != own.attempt.jobToken || !jobLeaseCurrent) {
		return bad()
	}
	rows, err := tx.Query(ctx, `SELECT d.id,d.session_id,d.thread_id,d.session_epoch,d.thread_epoch,d.kind,d.request_id,
	 d.admission_state,d.consumer_kind,d.job_id,d.runtime_id,d.job_lock_token,d.runtime_lease_token,
	 d.request_id IS NULL OR (r.id IS NOT NULL AND r.thread_id IS NOT DISTINCT FROM d.thread_id)
	 FROM session_input_dispatches d
	 LEFT JOIN session_human_input_requests r ON r.org_id=d.org_id AND r.session_id=d.session_id AND r.id=d.request_id
	 WHERE d.org_id=$1 AND (d.id=ANY($2::uuid[]) OR d.job_id=$3) ORDER BY d.id LIMIT $4`, orgID, own.binding.dispatchIDs, own.binding.jobID, cap+1)
	if err != nil {
		return lifecycleOwnConsumerProof{}, fmt.Errorf("read own lifecycle dispatch references: %w", err)
	}
	type dispatchRef struct {
		id, sessionID                                                 uuid.UUID
		threadID, requestID, jobID, runtimeID, jobToken, runtimeToken *uuid.UUID
		sessionEpoch, threadEpoch                                     *int64
		kind                                                          models.LifecycleDispatchKind
		state, consumer                                               string
		requestMatches                                                bool
	}
	refs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (dispatchRef, error) {
		var v dispatchRef
		err := row.Scan(&v.id, &v.sessionID, &v.threadID, &v.sessionEpoch, &v.threadEpoch, &v.kind, &v.requestID, &v.state, &v.consumer, &v.jobID, &v.runtimeID, &v.jobToken, &v.runtimeToken, &v.requestMatches)
		return v, err
	})
	if err != nil {
		return lifecycleOwnConsumerProof{}, fmt.Errorf("collect own lifecycle dispatch references: %w", err)
	}
	if len(refs) > cap {
		return lifecycleOwnConsumerProof{}, &lifecycleConsumerError{cause: ErrLifecycleConsumerDiscoveryIncomplete, source: lifecycleConsumerDispatch}
	}
	if len(refs) != len(own.binding.dispatchIDs) {
		return bad()
	}
	for _, v := range refs {
		if !v.requestMatches || !slices.Contains(own.binding.dispatchIDs, v.id) || v.sessionID != fence.sessionID || (v.threadID != nil) != fence.threaded || (fence.threaded && *v.threadID != fence.threadID) || v.sessionEpoch == nil || *v.sessionEpoch != fence.sessionEpoch || (v.threadEpoch != nil) != fence.threaded || (fence.threaded && *v.threadEpoch != fence.threadEpoch) || v.kind != own.binding.kind || v.state != "bound" || v.jobID == nil || *v.jobID != own.binding.jobID || (v.requestID != nil) != (own.binding.requestID != uuid.Nil) || (v.requestID != nil && *v.requestID != own.binding.requestID) {
			return bad()
		}
		if v.consumer == "job" {
			if own.attempt != nil || v.runtimeID != nil || v.jobToken != nil || v.runtimeToken != nil {
				return bad()
			}
		} else if v.consumer == "runtime" {
			if own.attempt == nil || v.runtimeID == nil || *v.runtimeID != own.attempt.runtimeID || v.jobToken == nil || *v.jobToken != own.attempt.jobToken || v.runtimeToken == nil || *v.runtimeToken != own.attempt.runtimeToken {
				return bad()
			}
		} else {
			return bad()
		}
	}
	// All associations of this exact tenant/job are relevant to an exception,
	// even an association naming a different session or compatible sibling.
	// Merely skipping nonoverlapping rows would hide contradictory provenance.
	associationRows, err := tx.Query(ctx, `SELECT source,id,session_id,thread_id,runtime_id,consistent FROM (
	 SELECT 'runtime'::text source,id,session_id,thread_id,id runtime_id,true consistent FROM thread_runtimes WHERE org_id=$1 AND job_id=$2
	 UNION ALL SELECT 'executor',id,session_id,thread_id,NULL::uuid,true FROM session_executors WHERE org_id=$1 AND job_id=$2
	 UNION ALL SELECT 'preparation',id,session_id,thread_id,NULL::uuid,true FROM session_runtime_start_preparations WHERE org_id=$1 AND job_id=$2
	 UNION ALL SELECT 'recheck',assessment_id,session_id,thread_id,NULL::uuid,true FROM code_review_recheck_dispatches WHERE org_id=$1 AND job_id=$2
	 UNION ALL SELECT 'automation',a.id,COALESCE(a.session_id,g.session_id,l.session_id),a.thread_id,NULL::uuid,
	 (a.session_id IS NULL OR g.session_id IS NULL OR a.session_id=g.session_id)
	 AND (a.session_id IS NULL OR l.session_id IS NULL OR a.session_id=l.session_id)
	 AND (g.session_id IS NULL OR l.session_id IS NULL OR g.session_id=l.session_id)
	 FROM automation_runs a
	 LEFT JOIN automation_target_sessions g ON g.org_id=a.org_id AND g.target_id=a.target_id AND g.generation=a.target_generation
	 LEFT JOIN session_automation_links l ON l.org_id=a.org_id AND l.automation_run_id=a.id
	 WHERE a.org_id=$1 AND a.job_id=$2
	) references_for_job LIMIT $3`, orgID, actual.jobID, cap+1)
	if err != nil {
		return lifecycleOwnConsumerProof{}, fmt.Errorf("read lifecycle own job associations: %w", err)
	}
	associationCount := 0
	for associationRows.Next() {
		var source lifecycleConsumerSource
		var id uuid.UUID
		var sessionID, threadID, runtimeID *uuid.UUID
		var consistent bool
		if err := associationRows.Scan(&source, &id, &sessionID, &threadID, &runtimeID, &consistent); err != nil {
			associationRows.Close()
			return lifecycleOwnConsumerProof{}, fmt.Errorf("decode lifecycle own job association: %w", err)
		}
		associationCount++
		if associationCount > cap {
			associationRows.Close()
			return lifecycleOwnConsumerProof{}, &lifecycleConsumerError{cause: ErrLifecycleConsumerDiscoveryIncomplete, source: source, id: id}
		}
		if source.validate() != nil || !consistent || sessionID == nil || *sessionID != fence.sessionID || (threadID != nil) != fence.threaded || (fence.threaded && *threadID != fence.threadID) || (source == lifecycleConsumerRuntime && (own.attempt == nil || runtimeID == nil || *runtimeID != own.attempt.runtimeID)) {
			associationRows.Close()
			return bad()
		}
	}
	err = associationRows.Err()
	associationRows.Close()
	if err != nil {
		return lifecycleOwnConsumerProof{}, fmt.Errorf("collect lifecycle own job associations: %w", err)
	}
	proof := lifecycleOwnConsumerProof{binding: &actual}
	if parked {
		control, err := readLifecycleStartControlInTx(ctx, tx, orgID, actual)
		if err != nil {
			return lifecycleOwnConsumerProof{}, err
		}
		spec, err := control.Spec()
		if err != nil || spec.State != lifecycleStartControlAwaiting {
			return bad()
		}
		// Dispatch classification still counts retained control occupancy even
		// for this own logical identity. This proof permits exact replay only.
		return proof, nil
	}
	if own.attempt == nil {
		return proof, nil
	}
	a := own.attempt
	var runtimeJob, jobToken, runtimeToken, sessionID, threadID uuid.UUID
	var sessionEpoch, threadEpoch int64
	var digest, runtimeStatus string
	var ids []uuid.UUID
	var turn int
	var current, unresolved bool
	err = tx.QueryRow(ctx, `SELECT COALESCE(job_id,'00000000-0000-0000-0000-000000000000'),COALESCE(job_attempt_token,'00000000-0000-0000-0000-000000000000'),lease_token,session_id,thread_id,
	 COALESCE(admission_session_epoch,-1),COALESCE(admission_thread_epoch,-1),COALESCE(allocation_digest,''),COALESCE(input_dispatch_ids,'{}'::uuid[]),COALESCE(allocation_expected_turn,-1),status,
	 COALESCE(lease_expires_at>statement_timestamp(),false),drain_proof IS NOT NULL OR first_provider_launch_marker IS NOT NULL OR lease_loss_receipt IS NOT NULL
	 OR aborted_allocation_receipt IS NOT NULL OR provider_terminal_result IS NOT NULL
	 OR provider_terminal_result_digest IS NOT NULL OR provider_terminal_settled_at IS NOT NULL
	 FROM thread_runtimes WHERE org_id=$1 AND id=$2`, orgID, a.runtimeID).Scan(&runtimeJob, &jobToken, &runtimeToken, &sessionID, &threadID, &sessionEpoch, &threadEpoch, &digest, &ids, &turn, &runtimeStatus, &current, &unresolved)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return bad()
		}
		return lifecycleOwnConsumerProof{}, fmt.Errorf("read exact lifecycle own runtime allocation: %w", err)
	}
	expectedDigest, err := a.allocationDigest.Encode()
	if err != nil {
		return bad()
	}
	if !fence.threaded || runtimeJob != actual.jobID || jobToken != a.jobToken || runtimeToken != a.runtimeToken || sessionID != fence.sessionID || threadID != fence.threadID || sessionEpoch != fence.sessionEpoch || threadEpoch != fence.threadEpoch || digest != expectedDigest || turn != a.expectedTurn || !sameLifecycleDispatchSet(ids, actual.dispatchIDs) || !current || unresolved || !slices.Contains([]string{"starting", "live", "paused", "draining"}, runtimeStatus) {
		return bad()
	}
	proof.runtimeID = a.runtimeID
	return proof, nil
}

func sameLifecycleDispatchSet(left, right []uuid.UUID) bool {
	if len(left) == 0 || len(left) != len(right) {
		return false
	}
	seen := make(map[uuid.UUID]bool, len(left))
	for _, id := range left {
		if id == uuid.Nil || seen[id] {
			return false
		}
		seen[id] = true
	}
	for _, id := range right {
		if !seen[id] {
			return false
		}
		delete(seen, id)
	}
	return len(seen) == 0
}

type lifecycleLegacyConsumerJob struct {
	id    uuid.UUID
	route legacyJobRouteObservation
}

func readLegacyLifecycleConsumersInTx(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, fence AdmissionFence, own lifecycleOwnConsumerProof, scopedJobs map[uuid.UUID]bool, limits lifecycleConsumerLimits) (bool, error) {
	rows, err := tx.Query(ctx, lifecycleConsumerLegacyJobsQuery, orgID, limits.legacyJobs+1, legacyJobRouteMaxBytes)
	if err != nil {
		return false, fmt.Errorf("read bounded legacy lifecycle jobs: %w", err)
	}
	var jobs []lifecycleLegacyConsumerJob
	lanes := make(map[uuid.UUID]bool)
	bytes := 0
	for rows.Next() {
		var id uuid.UUID
		var size int
		var payload json.RawMessage
		if err := rows.Scan(&id, &size, &payload); err != nil {
			rows.Close()
			return false, fmt.Errorf("decode legacy lifecycle job row: %w", err)
		}
		bytes += size
		if len(jobs) >= limits.legacyJobs || size > legacyJobRouteMaxBytes || bytes > limits.payloadBytes {
			rows.Close()
			return false, &lifecycleConsumerError{cause: ErrLifecycleConsumerDiscoveryIncomplete, source: lifecycleConsumerLegacyJob, id: id}
		}
		route := decodeLegacyJobRoute(orgID, payload)
		if route.incomplete() {
			rows.Close()
			return false, &lifecycleConsumerError{cause: ErrLifecycleConsumerDiscoveryIncomplete, source: lifecycleConsumerLegacyJob, id: id}
		}
		for _, lane := range route.threadIDs() {
			lanes[lane] = true
			if len(lanes) > limits.laneAnchors {
				rows.Close()
				return false, &lifecycleConsumerError{cause: ErrLifecycleConsumerDiscoveryIncomplete, source: lifecycleConsumerLegacyJob, id: id}
			}
		}
		jobs = append(jobs, lifecycleLegacyConsumerJob{id, route})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, fmt.Errorf("collect bounded legacy lifecycle jobs: %w", err)
	}
	parents := make(map[uuid.UUID]uuid.UUID, len(lanes))
	if len(lanes) > 0 {
		ids := make([]uuid.UUID, 0, len(lanes))
		for id := range lanes {
			ids = append(ids, id)
		}
		laneRows, err := tx.Query(ctx, `SELECT id,session_id FROM session_threads WHERE org_id=$1 AND id=ANY($2::uuid[])`, orgID, ids)
		if err != nil {
			return false, fmt.Errorf("resolve legacy lifecycle lane anchors: %w", err)
		}
		for laneRows.Next() {
			var id, parent uuid.UUID
			if err := laneRows.Scan(&id, &parent); err != nil {
				laneRows.Close()
				return false, fmt.Errorf("decode legacy lifecycle lane membership: %w", err)
			}
			parents[id] = parent
		}
		err = laneRows.Err()
		laneRows.Close()
		if err != nil {
			return false, fmt.Errorf("collect legacy lifecycle lane membership: %w", err)
		}
	}
	blocked := false
	for _, job := range jobs {
		competes, err := classifyLegacyLifecycleConsumer(job, fence, parents, scopedJobs[job.id], own)
		if err != nil {
			return false, err
		}
		blocked = blocked || competes
	}
	return blocked, nil
}

func classifyLegacyLifecycleConsumer(job lifecycleLegacyConsumerJob, fence AdmissionFence, parents map[uuid.UUID]uuid.UUID, reverseScoped bool, own lifecycleOwnConsumerProof) (bool, error) {
	r := job.route
	scoped := reverseScoped || slices.Contains(r.session.anchors, fence.sessionID)
	for _, id := range r.thread.anchors {
		scoped = scoped || parents[id] == fence.sessionID
	}
	if !scoped {
		return false, nil
	}
	bad := func() (bool, error) { return false, reconcileLifecycleConsumer(lifecycleConsumerLegacyJob, job.id) }
	const ambiguous = legacyJobRouteDuplicate | legacyJobRouteAlias | legacyJobRouteWrongType | legacyJobRouteInvalidUUID | legacyJobRouteZeroUUID
	if r.problems != 0 || r.orgMismatch || r.routeDecodingFails || r.org.problems&ambiguous != 0 || r.session.problems&ambiguous != 0 || r.thread.problems&ambiguous != 0 || len(r.session.anchors) != 1 {
		return bad()
	}
	if r.session.anchors[0] != fence.sessionID {
		return bad()
	}
	if len(r.thread.anchors) > 0 {
		for _, id := range r.thread.anchors {
			if parents[id] != fence.sessionID {
				return bad()
			}
		}
		if fence.threaded && !slices.Contains(r.thread.anchors, fence.threadID) && !r.sessionWideFallback && !reverseScoped {
			return false, nil
		}
	}
	if own.binding != nil && job.id == own.binding.jobID {
		return false, nil
	}
	return true, nil
}

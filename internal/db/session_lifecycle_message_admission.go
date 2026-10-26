package db

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var (
	ErrMessageAdmissionAwaitingInput             = errors.New("ordinary message admission cannot answer an awaiting-input request")
	ErrMessageAdmissionBackpressure              = errors.New("message admission inbox limit reached")
	ErrMessageAdmissionNotAllowed                = errors.New("ordinary message admission is not authorized for this scope")
	ErrMessageAdmissionIdempotencyConflict       = errors.New("message admission ingress identity has different semantic intent")
	ErrMessageAdmissionActorForbidden            = errors.New("message admission actor lacks current tenant write access")
	ErrMessageAdmissionSpecialistControlRequired = errors.New("message admission requires retained specialist control")
	ErrMessageAdmissionBusy                      = errors.New("comment message requires an immediately admitted new job")
	ErrMessageAdmissionReplayObservationNeeded   = errors.New("message admission requires a fresh exact receipt observation after rollback")
)

const messageAdmissionDispatchLookup = `SELECT id,session_id,thread_id,session_epoch,thread_epoch,
 message_id,kind,payload_digest,CASE WHEN octet_length(validated_payload::text)<=8388608 THEN validated_payload ELSE NULL END,normalization_version
 FROM session_input_dispatches WHERE org_id=$1 AND ingress_kind=$2 AND ingress_key=$3`

// Only raw pending identity is relevant to the non-answer boundary. Readiness,
// generations and physical availability belong to exact A3/C2 answer admission.
// A valid explicit other lane remains independent; a threadless or contradictory
// request cannot be silently narrowed to a sibling or answered as "latest".
const messageAdmissionPendingAnswerLookupQuery = `SELECT
 EXISTS(SELECT 1 FROM session_questions WHERE (CASE WHEN status='pending' THEN org_id END)=$1
  AND (CASE WHEN status='pending' THEN session_id END)=$2)
 OR EXISTS(SELECT 1 FROM session_human_input_requests r WHERE r.org_id=$1 AND r.session_id=$2 AND r.status='pending'
  AND (r.thread_id IS NULL OR r.thread_id=$3 OR NOT EXISTS(
   SELECT 1 FROM session_threads t WHERE t.org_id=$1 AND t.id=r.thread_id AND t.session_id=$2)))`

// AdmitMessageInTx is the ordinary, non-answer admission owner. Its caller owns
// the configured bounded transaction, retries, settlement and postcommit effects.
// Every error requires WHOLE rollback, including errors after input acceptance.
// Exact replay precedes current policy; a new command never adopts fresh epochs.
// This component remains dark until the complete writer/activation joins exist.
func AdmitMessageInTx(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, command MessageAdmission) (AdmissionResult[Admission], error) {
	if ctx == nil || nilLifecycleDependency(tx) {
		return AdmissionResult[Admission]{}, errors.New("message admission requires a context and supplied transaction")
	}
	if err := command.Validate(); err != nil {
		return AdmissionResult[Admission]{}, err
	}
	if orgID == uuid.Nil || command.OrgID() != orgID {
		return AdmissionResult[Admission]{}, ErrMessageAdmissionNotAllowed
	}
	if err := requireLifecycleScopeTransactionInTx(ctx, tx); err != nil {
		return AdmissionResult[Admission]{}, err
	}
	input := command.Input()
	laneID, _ := input.Fence.ThreadID()
	if replay, found, err := FindMessageAdmissionReplayInTx(ctx, tx, orgID, input.Fence.SessionID(), laneID, input.Ingress); err != nil {
		return AdmissionResult[Admission]{}, err
	} else if found {
		return newAdmissionResult(replay, nil)
	}
	if err := RequireActiveThreadSendModeInTx(ctx, tx, orgID, input.Fence.SessionID()); err != nil {
		return AdmissionResult[Admission]{}, err
	}
	// Locate and acquire every known higher-ranked domain/job row BEFORE parent
	// and lane acquisition. Discovery itself grants no authority; locators are
	// re-read after parent lock, and a changed set aborts rather than locking late.
	before, err := discoverMessageAdmissionOwners(ctx, tx, orgID, input.Fence.SessionID(), laneID)
	if err != nil {
		return AdmissionResult[Admission]{}, err
	}
	if err := prelockMessageAdmissionOwners(ctx, tx, orgID, before); err != nil {
		return AdmissionResult[Admission]{}, err
	}
	scope, err := lockActiveThreadMessageScopeInTx(ctx, tx, orgID, input.Fence.SessionID(), laneID)
	if err != nil {
		return AdmissionResult[Admission]{}, err
	}
	// The canonical parent serializes same-key ordinary misses. READ COMMITTED
	// must see a winner committed while this attempt was acquiring parent locks.
	if _, found, err := lookupMessageAdmissionDispatch(ctx, tx, orgID, input.Fence.SessionID(), laneID, input.Ingress); err != nil {
		return AdmissionResult[Admission]{}, err
	} else if found {
		return AdmissionResult[Admission]{}, ErrMessageAdmissionReplayObservationNeeded
	}
	if err := verifyThreadMessageFence(scope, input.Fence); err != nil {
		return AdmissionResult[Admission]{}, err
	}
	after, err := discoverMessageAdmissionOwners(ctx, tx, orgID, input.Fence.SessionID(), laneID)
	if err != nil {
		return AdmissionResult[Admission]{}, err
	}
	if !sameMessageAdmissionOwners(before, after) {
		return AdmissionResult[Admission]{}, ErrMessageAdmissionConflict
	}
	if after.reviewPR != nil || after.automationGeneration != nil {
		// Raw retained markers are decisive, including dangling links, retired
		// generations and release-pending owners. Source labels never bypass.
		return AdmissionResult[Admission]{}, fmt.Errorf("%w: %w", ErrMessageAdmissionNotAllowed, ErrMessageAdmissionSpecialistControlRequired)
	}
	if err := authorizeOrdinaryMessage(ctx, tx, orgID, input); err != nil {
		return AdmissionResult[Admission]{}, err
	}
	state, err := readMessageAdmissionState(ctx, tx, orgID, input.Fence.SessionID(), laneID)
	if err != nil {
		return AdmissionResult[Admission]{}, err
	}
	// A5 has not yet installed a durable repair-scope/ownership proof. Retain
	// ordinary input for reconciliation instead of borrowing specialist control
	// or inferring that an active reservation is lane-local. Released historical
	// rows are not selected here; stale live work remains the shared predicate's
	// responsibility independently of reservation activity or current PR head.
	state.repairReconciliation = len(after.repairs) != 0
	if err := tx.QueryRow(ctx, messageAdmissionPendingAnswerLookupQuery, orgID, input.Fence.SessionID(), laneID).Scan(&state.pendingAnswer); err != nil {
		return AdmissionResult[Admission]{}, fmt.Errorf("read ordinary admission pending answer scope: %w", err)
	}
	decision, err := decideMessageAdmission(scope, state, laneID, len(input.ResolveReviewCommentIDs) > 0)
	if err != nil {
		return AdmissionResult[Admission]{}, err
	}
	if len(input.Message) > 256<<10 {
		return AdmissionResult[Admission]{}, ErrMessageAdmissionBackpressure
	}
	if err := requireMessageAdmissionInboxCapacity(ctx, tx, orgID, input.Fence.SessionID(), laneID); err != nil {
		return AdmissionResult[Admission]{}, err
	}
	blocked, consumerErr := HasCompetingInputConsumerInTx(ctx, tx, orgID, input.Fence, nil)
	if consumerErr != nil && !errors.Is(consumerErr, ErrLifecycleConsumerReconciliation) {
		return AdmissionResult[Admission]{}, consumerErr
	}
	if decision.disposition == MessageAdmissionNewJob && (blocked || consumerErr != nil || state.claimDispatch != nil || state.claimToken != nil) {
		decision.disposition, decision.reason = MessageAdmissionWaiting, "consumer_blocked"
		if consumerErr != nil {
			decision.reason = "consumer_reconciliation"
		}
		if state.claimDispatch != nil || state.claimToken != nil {
			decision.reason = "retained_lane_claim"
		}
		if errors.Is(consumerErr, errLifecycleRepairConsumerReconciliation) {
			decision.reason = "repair_reconciliation"
		}
	}
	if len(input.ResolveReviewCommentIDs) > 0 && decision.disposition != MessageAdmissionNewJob {
		return AdmissionResult[Admission]{}, ErrMessageAdmissionBusy
	}
	if input.ChangesetID != nil {
		// Association only: the freshly generated holder is a later worker lease
		// identity. Neither holder liveness nor workspace availability is read.
		var id uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT id FROM session_changesets WHERE org_id=$1 AND session_id=$2 AND id=$3 FOR KEY SHARE NOWAIT`, orgID, input.Fence.SessionID(), *input.ChangesetID).Scan(&id); err != nil {
			return AdmissionResult[Admission]{}, fmt.Errorf("validate admission changeset scope: %w", err)
		}
	}
	if err := prelockMessageAdmissionComments(ctx, tx, orgID, input.Fence.SessionID(), input.ResolveReviewCommentIDs); err != nil {
		return AdmissionResult[Admission]{}, err
	}
	content := input.Message
	if input.PlanMode {
		content = "[PLAN_MODE]\n" + content
	}
	message := models.SessionMessage{SessionID: input.Fence.SessionID(), OrgID: orgID, ThreadID: &laneID, UserID: input.ActorID,
		TurnNumber: decision.turn, Role: models.MessageRoleUser, Content: content, Attachments: input.Images, References: input.References, Commands: input.Commands, Source: input.Source}
	if err := NewSessionMessageStore(tx).CreateWithSource(ctx, &message); err != nil {
		return AdmissionResult[Admission]{}, fmt.Errorf("persist admitted message: %w", err)
	}
	inboxPayload, err := json.Marshal(map[string]any{"message_id": message.ID, "turn_number": message.TurnNumber, "plan_mode": input.PlanMode, "content": message.Content})
	if err != nil {
		return AdmissionResult[Admission]{}, fmt.Errorf("encode admitted inbox payload: %w", err)
	}
	inbox, err := NewThreadInboxStore(tx).AppendForMessage(ctx, orgID, AppendThreadInboxEntryParams{SessionID: input.Fence.SessionID(), ThreadID: laneID, MessageID: message.ID,
		ClientMessageID: messageAdmissionInboxKey(input.Ingress, orgID, input.Fence.SessionID(), laneID), EntryType: models.ThreadInboxEntryTypeUserMessage, Payload: inboxPayload})
	if err != nil {
		return AdmissionResult[Admission]{}, fmt.Errorf("persist admitted inbox input: %w", err)
	}
	if inbox.OrgID != orgID || inbox.SessionID != message.SessionID || inbox.ThreadID != laneID || inbox.MessageID != message.ID {
		return AdmissionResult[Admission]{}, ErrMessageAdmissionConflict
	}
	pass := int(state.parentCurrentTurn)
	if pass == 0 {
		pass = 1
	}
	comments, err := NewSessionReviewCommentStore(tx).ValidateAndResolveByIDs(ctx, orgID, input.Fence.SessionID(), input.ResolveReviewCommentIDs, pass)
	if err != nil {
		return AdmissionResult[Admission]{}, err
	}
	slices.SortFunc(comments, func(a, b models.SessionReviewComment) int { return bytes.Compare(a.ID[:], b.ID[:]) })
	dispatchID, err := uuid.NewRandom()
	if err != nil {
		return AdmissionResult[Admission]{}, fmt.Errorf("allocate admission dispatch identity: %w", err)
	}
	admission := Admission{orgID: orgID, fence: input.Fence, ingress: input.Ingress, dispatchID: dispatchID,
		disposition: decision.disposition, original: decision.disposition, message: cloneMessageAdmissionMessage(message), inbox: cloneMessageAdmissionInbox(inbox), comments: cloneMessageAdmissionComments(comments),
		options: input.Dispatch, holderID: cloneMessageAdmissionPointer(input.LeaseHolderID), targetNodeID: models.SessionWorkerTarget(&models.Session{ContainerID: state.containerID, WorkerNodeID: state.workerNodeID}), threadState: state.status, reason: decision.reason, valid: true}
	envelope, err := encodeMessageAdmissionEnvelope(command, admission)
	if err != nil {
		return AdmissionResult[Admission]{}, err
	}
	digest, err := input.Ingress.Digest().Encode()
	if err != nil {
		return AdmissionResult[Admission]{}, err
	}
	dispatchTag, err := tx.Exec(ctx, `INSERT INTO session_input_dispatches(id,org_id,session_id,thread_id,session_epoch,thread_epoch,message_id,kind,ingress_kind,ingress_key,payload_digest,validated_payload,normalization_version,admission_state,consumer_kind,next_attempt_at,reason)
 VALUES($1,$2,$3,$4,$5,$6,$7,'message',$8,$9,$10,$11,1,'waiting','none',statement_timestamp()+interval '5 seconds',$12)`, dispatchID, orgID, input.Fence.SessionID(), laneID, input.Fence.sessionEpoch, input.Fence.threadEpoch, message.ID, input.Ingress.Kind(), input.Ingress.Key(), digest, envelope, decision.reason)
	if err != nil {
		return AdmissionResult[Admission]{}, fmt.Errorf("persist admitted dispatch: %w", err)
	}
	if dispatchTag.RowsAffected() != 1 {
		return AdmissionResult[Admission]{}, ErrMessageAdmissionConflict
	}
	if decision.disposition == MessageAdmissionNewJob {
		jobID, err := uuid.NewRandom()
		if err != nil {
			return AdmissionResult[Admission]{}, fmt.Errorf("allocate admitted job identity: %w", err)
		}
		binding, opts, err := messageAdmissionJobOptions(command, admission, jobID)
		if err != nil {
			return AdmissionResult[Admission]{}, err
		}
		enqueued, err := enqueueLifecycleJobInTx(ctx, tx, orgID, binding, opts, nil)
		if err != nil {
			return AdmissionResult[Admission]{}, err
		}
		if enqueued.disposition != lifecycleEnqueueNewJob {
			if len(input.ResolveReviewCommentIDs) > 0 {
				return AdmissionResult[Admission]{}, ErrMessageAdmissionBusy
			}
			// A new dispatch can never adopt a foreign or merely equal-key job.
			// No running projection or claim has been written, so acceptance can
			// remain durably waiting without compensating another owner.
			admission.disposition, admission.original, admission.reason = MessageAdmissionWaiting, MessageAdmissionWaiting, "enqueue_reconciliation"
		} else {
			token, err := uuid.NewRandom()
			if err != nil {
				return AdmissionResult[Admission]{}, fmt.Errorf("allocate admission claim token: %w", err)
			}
			admission.authority, err = NewAdmissionAuthority(orgID, input.Fence, dispatchID, token, message.TurnNumber, input.Ingress)
			if err != nil {
				return AdmissionResult[Admission]{}, err
			}
			admission.jobID, admission.threadState = jobID, models.ThreadStatusRunning
			if err := bindMessageAdmissionClaim(ctx, tx, admission, state.status, scope.session.status); err != nil {
				return AdmissionResult[Admission]{}, err
			}
		}
	}
	if admission.original != MessageAdmissionNewJob {
		tag, err := tx.Exec(ctx, `UPDATE session_threads SET pending_message_count=pending_message_count+1
 WHERE org_id=$1 AND session_id=$2 AND id=$3 AND pending_message_count<2147483647`, orgID, input.Fence.SessionID(), laneID)
		if err != nil {
			return AdmissionResult[Admission]{}, fmt.Errorf("count admitted pending input: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return AdmissionResult[Admission]{}, ErrMessageAdmissionBackpressure
		}
	}
	envelope, err = encodeMessageAdmissionEnvelope(command, admission)
	if err != nil {
		return AdmissionResult[Admission]{}, err
	}
	dispatchTag, err = tx.Exec(ctx, `UPDATE session_input_dispatches SET validated_payload=$4,reason=$5,updated_at=now() WHERE org_id=$1 AND session_id=$2 AND id=$3`, orgID, input.Fence.SessionID(), dispatchID, envelope, admission.reason)
	if err != nil {
		return AdmissionResult[Admission]{}, fmt.Errorf("retain admission outcome envelope: %w", err)
	}
	if dispatchTag.RowsAffected() != 1 {
		return AdmissionResult[Admission]{}, ErrMessageAdmissionConflict
	}
	effects, err := messageAdmissionEffects(admission)
	if err != nil {
		return AdmissionResult[Admission]{}, err
	}
	if err := ctx.Err(); err != nil {
		return AdmissionResult[Admission]{}, err
	}
	return newAdmissionResult(admission, effects)
}

type messageAdmissionState struct {
	status                    models.ThreadStatus
	currentTurn               int64
	parentCurrentTurn         int64
	claimDispatch, claimToken *uuid.UUID
	containerID, workerNodeID *string
	repairReconciliation      bool
	pendingAnswer             bool
}

func readMessageAdmissionState(ctx context.Context, tx pgx.Tx, orgID, sessionID, laneID uuid.UUID) (messageAdmissionState, error) {
	var state messageAdmissionState
	err := tx.QueryRow(ctx, `SELECT t.status,t.current_turn,t.admission_claim_dispatch_id,t.admission_claim_token,s.container_id,s.worker_node_id,s.current_turn
 FROM session_threads t JOIN sessions s ON s.org_id=t.org_id AND s.id=t.session_id WHERE t.org_id=$1 AND t.session_id=$2 AND t.id=$3`, orgID, sessionID, laneID).
		Scan(&state.status, &state.currentTurn, &state.claimDispatch, &state.claimToken, &state.containerID, &state.workerNodeID, &state.parentCurrentTurn)
	if err != nil {
		return messageAdmissionState{}, fmt.Errorf("read locked admission projection: %w", err)
	}
	if (state.claimDispatch == nil) != (state.claimToken == nil) {
		return messageAdmissionState{}, ErrLifecycleConsumerReconciliation
	}
	if state.parentCurrentTurn < 0 || state.parentCurrentTurn > math.MaxInt32 {
		return messageAdmissionState{}, ErrMessageAdmissionConflict
	}
	return state, nil
}

type messageAdmissionDecision struct {
	disposition MessageAdmissionDisposition
	turn        int
	reason      string
}

func decideMessageAdmission(scope lockedLifecycleScope, state messageAdmissionState, laneID uuid.UUID, comments bool) (messageAdmissionDecision, error) {
	if state.status == models.ThreadStatusAwaitingInput || state.pendingAnswer {
		return messageAdmissionDecision{}, ErrMessageAdmissionAwaitingInput
	}
	if scope.session.epochs.EndEpoch != nil {
		return messageAdmissionDecision{}, ErrMessageAdmissionConflict
	}
	allowedParent := scope.session.status == models.SessionStatusIdle || scope.session.status == models.SessionStatusRunning || scope.session.status == models.SessionStatusPending || slices.Contains(models.ResumableSessionStatuses, scope.session.status)
	if !allowedParent {
		return messageAdmissionDecision{}, ErrMessageAdmissionNotAllowed
	}
	decision := messageAdmissionDecision{disposition: MessageAdmissionNewJob}
	offset := int64(1)
	if state.status == models.ThreadStatusPending || state.status == models.ThreadStatusRunning {
		if comments {
			return messageAdmissionDecision{}, ErrMessageAdmissionBusy
		}
		decision.disposition, decision.reason, offset = MessageAdmissionQueued, "lane_running", 2
	} else if state.status != models.ThreadStatusIdle && !slices.Contains(models.ResumableThreadStatuses, state.status) {
		return messageAdmissionDecision{}, ErrMessageAdmissionNotAllowed
	}
	active := 0
	for _, lane := range scope.threads {
		if lane.id == laneID && lane.epochs.EndEpoch != nil {
			return messageAdmissionDecision{}, ErrMessageAdmissionConflict
		}
		if lane.archivedAt == nil && (lane.status == models.ThreadStatusPending || lane.status == models.ThreadStatusRunning || lane.status == models.ThreadStatusAwaitingInput) {
			active++
		}
	}
	if decision.disposition == MessageAdmissionNewJob && active >= models.MaxRunningThreadsPerSession {
		decision.disposition, decision.reason = MessageAdmissionWaiting, "capacity"
	}
	if state.repairReconciliation {
		decision.disposition, decision.reason = MessageAdmissionWaiting, "repair_reconciliation"
	}
	if comments && decision.disposition != MessageAdmissionNewJob {
		return messageAdmissionDecision{}, ErrMessageAdmissionBusy
	}
	if state.currentTurn < 0 || state.currentTurn > math.MaxInt32-offset {
		return messageAdmissionDecision{}, errors.New("admitted turn exceeds PostgreSQL integer range")
	}
	decision.turn = int(state.currentTurn + offset)
	return decision, nil
}

func authorizeOrdinaryMessage(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, input MessageAdmissionInput) error {
	// Source is attribution, never typed specialist authority. Empty/nil actor is
	// the characterized trusted internal system-input path; external adapters
	// must still authenticate tenant and actor before constructing a command.
	if input.Source != "" && input.Source != models.SessionMessageSourceAgentTool {
		return ErrMessageAdmissionNotAllowed
	}
	return authorizeMessageAdmissionActor(ctx, tx, orgID, input.ActorID)
}

func authorizeMessageAdmissionActor(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, actorID *uuid.UUID) error {
	if actorID == nil {
		return nil
	}
	var role models.Role
	err := tx.QueryRow(ctx, `SELECT role FROM organization_memberships WHERE org_id=$1 AND user_id=$2 FOR SHARE NOWAIT`, orgID, *actorID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: %w", ErrMessageAdmissionNotAllowed, ErrMessageAdmissionActorForbidden)
	}
	if err != nil {
		return fmt.Errorf("validate ordinary message actor: %w", err)
	}
	if role != models.RoleAdmin && role != models.RoleMember && role != models.RoleBuilder {
		return fmt.Errorf("%w: %w", ErrMessageAdmissionNotAllowed, ErrMessageAdmissionActorForbidden)
	}
	return nil
}

func requireMessageAdmissionInboxCapacity(ctx context.Context, tx pgx.Tx, orgID, sessionID, laneID uuid.UUID) error {
	var lane, session int64
	err := tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE thread_id=$3),count(*) FROM thread_inbox_entries
 WHERE org_id=$1 AND session_id=$2 AND delivery_state IN ('pending','delivering','delivered','unknown_delivery')`, orgID, sessionID, laneID).Scan(&lane, &session)
	if err != nil {
		return fmt.Errorf("check locked admission inbox capacity: %w", err)
	}
	if lane >= 200 || session >= 1000 {
		return ErrMessageAdmissionBackpressure
	}
	return nil
}

func prelockMessageAdmissionComments(ctx context.Context, tx pgx.Tx, orgID, sessionID uuid.UUID, ids []uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}
	ordered := slices.Clone(ids)
	slices.SortFunc(ordered, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	rows, err := tx.Query(ctx, `SELECT id FROM session_review_comments WHERE org_id=$1 AND session_id=$2 AND id=ANY($3::uuid[]) ORDER BY id FOR UPDATE NOWAIT`, orgID, sessionID, ordered)
	if err != nil {
		return fmt.Errorf("prelock admission review comments: %w", err)
	}
	actual, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return fmt.Errorf("read locked admission review comments: %w", err)
	}
	if !slices.Equal(actual, ordered) {
		missing := []uuid.UUID{}
		for _, id := range ordered {
			if !slices.Contains(actual, id) {
				missing = append(missing, id)
			}
		}
		return &ErrReviewCommentsNotInSession{Missing: missing}
	}
	return nil
}

func bindMessageAdmissionClaim(ctx context.Context, tx pgx.Tx, admission Admission, previous models.ThreadStatus, parentPrevious models.SessionStatus) error {
	a := admission.authority
	// The token-bearing dispatch MUST precede the immediate token-bound lane FK.
	tag, err := tx.Exec(ctx, `UPDATE session_input_dispatches SET admission_state='bound',consumer_kind='job',job_id=$4,
 admission_claim_token=$5,admission_claim_turn=$6,next_attempt_at=NULL,reason=NULL,updated_at=now()
 WHERE org_id=$1 AND session_id=$2 AND id=$3 AND admission_state='waiting' AND consumer_kind='none'`, a.orgID, a.fence.sessionID, a.dispatchID, admission.jobID, a.claimToken, a.turn)
	if err != nil {
		return fmt.Errorf("bind admission dispatch to new job: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrMessageAdmissionConflict
	}
	tag, err = tx.Exec(ctx, `UPDATE session_threads SET admission_claim_dispatch_id=$4,admission_claim_token=$5,status='running',
 started_at=now(),completed_at=CASE WHEN $6::text='idle' THEN completed_at ELSE NULL END,last_activity_at=now(),cancel_requested_at=NULL
 WHERE org_id=$1 AND session_id=$2 AND id=$3 AND admission_claim_dispatch_id IS NULL AND admission_claim_token IS NULL`, a.orgID, a.fence.sessionID, a.fence.threadID, a.dispatchID, a.claimToken, previous)
	if err != nil {
		return fmt.Errorf("install exact lane admission claim: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrMessageAdmissionConflict
	}
	// No parent claim pointer exists here: running is only the active-sibling
	// projection, independently of each lane's logical admission ownership.
	assignments := "status='running',last_activity_at=now()"
	if parentPrevious == models.SessionStatusIdle {
		assignments += ",started_at=now(),completed_at=NULL,error=NULL,failure_explanation=NULL,failure_category=NULL,failure_next_steps=NULL,failure_retry_advised=false,runtime_soft_deadline_at=NULL,runtime_hard_deadline_at=NULL"
	} else if slices.Contains(models.ResumableSessionStatuses, parentPrevious) {
		assignments += ",started_at=now(),completed_at=NULL," + sessionResumeRuntimeResetAssignments
	}
	// Only the fixed server-owned assignment variants above are interpolated.
	// Keep the complete tenant-scoped statement together for source auditing.
	query := fmt.Sprintf(`UPDATE sessions SET %s
	WHERE org_id=$1 AND id=$2 AND EXISTS(SELECT 1 FROM session_threads t WHERE t.org_id=$1 AND t.session_id=$2 AND t.archived_at IS NULL AND t.status IN ('pending','running','awaiting_input'))`, assignments)
	tag, err = tx.Exec(ctx, query, a.orgID, a.fence.sessionID)
	if err != nil {
		return fmt.Errorf("derive admitted parent running projection: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrMessageAdmissionConflict
	}
	return nil
}

func messageAdmissionEffects(a Admission) ([]LifecycleEffect, error) {
	if err := a.Validate(); err != nil {
		return nil, err
	}
	if a.disposition == MessageAdmissionReplayed {
		return nil, nil
	}
	kinds := []LifecycleEffectKind{LifecycleEffectInboxChanged, LifecycleEffectThreadChanged}
	if a.original == MessageAdmissionNewJob {
		kinds = append(kinds, LifecycleEffectSessionChanged, LifecycleEffectWakeJobs)
	}
	effects := make([]LifecycleEffect, 0, len(kinds))
	for _, kind := range kinds {
		effect, err := newLifecycleEffect(kind, a.orgID, a.fence.sessionID, &a.fence.threadID)
		if err != nil {
			return nil, err
		}
		effects = append(effects, effect)
	}
	return effects, nil
}

func messageAdmissionInboxKey(ingress LifecycleIngress, orgID, sessionID, laneID uuid.UUID) string {
	if ingress.Kind() == messageAdmissionClientIngress {
		return strings.TrimPrefix(ingress.Key(), orgID.String()+":"+sessionID.String()+":"+laneID.String()+":")
	}
	return ingress.Key()
}

func messageAdmissionJobOptions(command MessageAdmission, admission Admission, jobID uuid.UUID) (LifecycleJobBinding, EnqueueOpts, error) {
	input := command.Input()
	laneID, _ := input.Fence.ThreadID()
	binding, err := NewLifecycleJobBinding(input.OrgID, input.Fence, jobID, []uuid.UUID{admission.dispatchID}, models.LifecycleDispatchMessage, nil)
	if err != nil {
		return LifecycleJobBinding{}, EnqueueOpts{}, err
	}
	dedupe := ContinueSessionDedupeKey(laneID)
	if input.ContinuationDedupeKeyOverride != nil {
		dedupe = *input.ContinuationDedupeKeyOverride
	}
	payload := map[string]string{"session_id": input.Fence.SessionID().String(), "thread_id": laneID.String(), "org_id": input.OrgID.String(), "queued_message_id": fmt.Sprint(admission.message.ID)}
	if input.ChangesetID != nil {
		payload["changeset_id"], payload["changeset_lease_holder_id"] = input.ChangesetID.String(), input.LeaseHolderID.String()
	}
	if input.PostSuccessAction != "" {
		payload["post_success_action"] = string(input.PostSuccessAction)
	}
	if input.PostSuccessAuthorMode != "" {
		payload["post_success_author_mode"] = string(input.PostSuccessAuthorMode)
	}
	runAt := input.Dispatch.RunAt
	return binding, EnqueueOpts{Queue: "agent", JobType: "continue_session", Payload: payload, Priority: 5, DedupeKey: &dedupe, TargetNodeID: cloneMessageAdmissionPointer(admission.targetNodeID), RunAt: &runAt, MaxAttempts: input.Dispatch.MaxAttempts}, nil
}

package db

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// The semantic JSON is a STRING inside JSONB, so JSONB's object-key ordering
// cannot change the canonical bytes covered by the client semantic digest.
// Server options/holder/target and the original outcome are retained separately;
// a later request cannot replace them merely because they are not client intent.
type messageAdmissionEnvelope struct {
	Version          int                           `json:"version"`
	SemanticPayload  string                        `json:"semantic_payload"`
	SessionEpoch     *int64                        `json:"session_epoch"`
	ThreadEpoch      *int64                        `json:"thread_epoch"`
	MessageID        int64                         `json:"message_id"`
	MessageTurn      *int                          `json:"message_turn"`
	RunAt            time.Time                     `json:"run_at"`
	MaxAttempts      int                           `json:"max_attempts"`
	LeaseHolderID    *uuid.UUID                    `json:"lease_holder_id"`
	TargetNodeID     *string                       `json:"target_node_id"`
	Disposition      MessageAdmissionDisposition   `json:"disposition"`
	ThreadStatus     models.ThreadStatus           `json:"thread_status"`
	InboxID          uuid.UUID                     `json:"inbox_id"`
	JobID            uuid.UUID                     `json:"job_id"`
	ClaimToken       uuid.UUID                     `json:"claim_token"`
	ClaimTurn        int                           `json:"claim_turn"`
	ResolvedComments []models.SessionReviewComment `json:"resolved_comments"`
	Reason           string                        `json:"reason"`
}

type messageAdmissionSemanticCommand struct {
	Version                       int                           `json:"version"`
	OrgID                         uuid.UUID                     `json:"org_id"`
	SessionID                     uuid.UUID                     `json:"session_id"`
	ThreadID                      uuid.UUID                     `json:"thread_id"`
	ActorID                       *uuid.UUID                    `json:"actor_id"`
	Source                        models.SessionMessageSource   `json:"source"`
	Message                       string                        `json:"message"`
	Images                        []string                      `json:"images"`
	References                    models.SessionInputReferences `json:"references"`
	Commands                      models.SessionInputCommands   `json:"commands"`
	ChangesetID                   *uuid.UUID                    `json:"changeset_id"`
	ResolveReviewCommentIDs       []uuid.UUID                   `json:"resolve_review_comment_ids"`
	PlanMode                      bool                          `json:"plan_mode"`
	PostSuccessAction             MessagePostSuccessAction      `json:"post_success_action"`
	PostSuccessAuthorMode         MessagePostSuccessAuthorMode  `json:"post_success_author_mode"`
	ContinuationDedupeKeyOverride *string                       `json:"continuation_dedupe_key_override"`
}

type messageAdmissionDispatchRow struct {
	id, sessionID                        uuid.UUID
	laneID                               *uuid.UUID
	sessionEpoch, threadEpoch, messageID *int64
	kind, digest                         string
	payload                              json.RawMessage
	normalizationVersion                 int
}

func encodeMessageAdmissionEnvelope(command MessageAdmission, admission Admission) (json.RawMessage, error) {
	envelope := messageAdmissionEnvelope{Version: 1, SemanticPayload: string(command.SemanticPayload()), RunAt: admission.options.RunAt, MaxAttempts: admission.options.MaxAttempts,
		SessionEpoch: cloneMessageAdmissionPointer(&admission.fence.sessionEpoch), ThreadEpoch: cloneMessageAdmissionPointer(&admission.fence.threadEpoch), MessageID: admission.message.ID, MessageTurn: cloneMessageAdmissionPointer(&admission.message.TurnNumber),
		LeaseHolderID: admission.holderID, TargetNodeID: admission.targetNodeID, Disposition: admission.original, ThreadStatus: admission.threadState, InboxID: admission.inbox.ID,
		JobID: admission.jobID, ClaimToken: admission.authority.claimToken, ClaimTurn: admission.authority.turn, ResolvedComments: admission.comments, Reason: admission.reason}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("encode original message admission envelope: %w", err)
	}
	if len(encoded) > defaultLifecycleConsumerLimits().payloadBytes {
		return nil, ErrMessageAdmissionBackpressure
	}
	return encoded, nil
}

func decodeMessageAdmissionEnvelope(row messageAdmissionDispatchRow, orgID, sessionID, laneID uuid.UUID, ingress LifecycleIngress) (messageAdmissionEnvelope, MessageAdmission, error) {
	if row.id == uuid.Nil || row.kind != "message" || row.sessionID != sessionID || row.laneID == nil || *row.laneID != laneID || row.messageID == nil || *row.messageID <= 0 || row.sessionEpoch == nil || row.threadEpoch == nil || row.normalizationVersion != 1 {
		return messageAdmissionEnvelope{}, MessageAdmission{}, ErrLifecycleConsumerReconciliation
	}
	if row.digest != mustMessageAdmissionDigest(ingress) {
		return messageAdmissionEnvelope{}, MessageAdmission{}, fmt.Errorf("%w: %w", ErrMessageAdmissionConflict, ErrMessageAdmissionIdempotencyConflict)
	}
	var envelope messageAdmissionEnvelope
	if len(row.payload) > defaultLifecycleConsumerLimits().payloadBytes || json.Unmarshal(row.payload, &envelope) != nil || envelope.Version != 1 {
		return messageAdmissionEnvelope{}, MessageAdmission{}, ErrLifecycleConsumerReconciliation
	}
	if envelope.SessionEpoch == nil || envelope.ThreadEpoch == nil || *envelope.SessionEpoch != *row.sessionEpoch || *envelope.ThreadEpoch != *row.threadEpoch || envelope.MessageID != *row.messageID || envelope.MessageTurn == nil || *envelope.MessageTurn <= 0 {
		return messageAdmissionEnvelope{}, MessageAdmission{}, ErrLifecycleConsumerReconciliation
	}
	if envelope.Disposition.Validate() != nil || envelope.Disposition == MessageAdmissionReplayed || envelope.ThreadStatus.Validate() != nil || envelope.InboxID == uuid.Nil {
		return messageAdmissionEnvelope{}, MessageAdmission{}, ErrLifecycleConsumerReconciliation
	}
	if envelope.Disposition == MessageAdmissionNewJob {
		if envelope.JobID == uuid.Nil || envelope.ClaimToken == uuid.Nil || envelope.ClaimTurn != *envelope.MessageTurn {
			return messageAdmissionEnvelope{}, MessageAdmission{}, ErrLifecycleConsumerReconciliation
		}
	} else if envelope.JobID != uuid.Nil || envelope.ClaimToken != uuid.Nil || envelope.ClaimTurn != 0 {
		return messageAdmissionEnvelope{}, MessageAdmission{}, ErrLifecycleConsumerReconciliation
	}
	var semantic messageAdmissionSemanticCommand
	if json.Unmarshal([]byte(envelope.SemanticPayload), &semantic) != nil || semantic.Version != 1 || semantic.OrgID != orgID || semantic.SessionID != sessionID || semantic.ThreadID != laneID {
		return messageAdmissionEnvelope{}, MessageAdmission{}, ErrLifecycleConsumerReconciliation
	}
	if envelope.Disposition != MessageAdmissionNewJob && len(semantic.ResolveReviewCommentIDs) > 0 {
		return messageAdmissionEnvelope{}, MessageAdmission{}, ErrLifecycleConsumerReconciliation
	}
	for _, comment := range envelope.ResolvedComments {
		if !slices.Contains(semantic.ResolveReviewCommentIDs, comment.ID) {
			return messageAdmissionEnvelope{}, MessageAdmission{}, ErrLifecycleConsumerReconciliation
		}
	}
	fence, err := NewAdmissionFence(sessionID, &laneID, *row.sessionEpoch, row.threadEpoch)
	if err != nil {
		return messageAdmissionEnvelope{}, MessageAdmission{}, ErrLifecycleConsumerReconciliation
	}
	command, err := NewMessageAdmission(MessageAdmissionInput{OrgID: orgID, Fence: fence, Ingress: ingress, ActorID: semantic.ActorID, Source: semantic.Source, Message: semantic.Message,
		Images: semantic.Images, References: semantic.References, Commands: semantic.Commands, ChangesetID: semantic.ChangesetID, LeaseHolderID: envelope.LeaseHolderID,
		ResolveReviewCommentIDs: semantic.ResolveReviewCommentIDs, PlanMode: semantic.PlanMode, PostSuccessAction: semantic.PostSuccessAction, PostSuccessAuthorMode: semantic.PostSuccessAuthorMode,
		ContinuationDedupeKeyOverride: semantic.ContinuationDedupeKeyOverride, Dispatch: MessageAdmissionDispatchOptions{RunAt: envelope.RunAt, MaxAttempts: envelope.MaxAttempts}})
	if err != nil || string(command.SemanticPayload()) != envelope.SemanticPayload {
		return messageAdmissionEnvelope{}, MessageAdmission{}, ErrLifecycleConsumerReconciliation
	}
	return envelope, command, nil
}

func mustMessageAdmissionDigest(ingress LifecycleIngress) string {
	// Every caller has validated ingress. Encoding cannot fail for a nonzero
	// fixed-size digest; no unvalidated or alternative textual form is adopted.
	digest := ingress.Digest()
	return fmt.Sprintf("%x", digest[:])
}

func lookupMessageAdmissionDispatch(ctx context.Context, tx pgx.Tx, orgID, sessionID, laneID uuid.UUID, ingress LifecycleIngress) (messageAdmissionDispatchRow, bool, error) {
	var row messageAdmissionDispatchRow
	err := tx.QueryRow(ctx, messageAdmissionDispatchLookup, orgID, ingress.Kind(), ingress.Key()).Scan(&row.id, &row.sessionID, &row.laneID, &row.sessionEpoch, &row.threadEpoch, &row.messageID, &row.kind, &row.digest, &row.payload, &row.normalizationVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return messageAdmissionDispatchRow{}, false, nil
	}
	if err != nil {
		return messageAdmissionDispatchRow{}, false, fmt.Errorf("find exact message admission dispatch: %w", err)
	}
	if row.sessionID != sessionID || row.laneID == nil || *row.laneID != laneID || row.digest != mustMessageAdmissionDigest(ingress) {
		return messageAdmissionDispatchRow{}, false, fmt.Errorf("%w: %w", ErrMessageAdmissionConflict, ErrMessageAdmissionIdempotencyConflict)
	}
	return row, true, nil
}

// FindMessageAdmissionReplayInTx is an exact-receipt observation, including the
// locks needed to prove current tenant/resource visibility and actor membership.
// A miss takes no parent lock. A hit discovers domain/receipt/job identities,
// locks them before parent/sorted lanes, then re-reads every exact association.
// Current epochs/end/status/owners/capacity are NOT new eligibility predicates.
// This never grants commit, execution, start or current claim authority and
// returns no effects. Transport authentication remains the caller's duty.
func FindMessageAdmissionReplayInTx(ctx context.Context, tx pgx.Tx, orgID, sessionID, laneID uuid.UUID, ingress LifecycleIngress) (Admission, bool, error) {
	if err := validateThreadSendScope(ctx, tx, orgID, sessionID, laneID); err != nil {
		return Admission{}, false, err
	}
	if err := ingress.Validate(); err != nil {
		return Admission{}, false, err
	}
	if ingress.Kind() != messageAdmissionClientIngress && ingress.Kind() != messageAdmissionServerIngress {
		return Admission{}, false, ErrMessageAdmissionNotAllowed
	}
	if err := requireLifecycleScopeTransactionInTx(ctx, tx); err != nil {
		return Admission{}, false, err
	}
	row, found, err := lookupMessageAdmissionDispatch(ctx, tx, orgID, sessionID, laneID, ingress)
	if err != nil || !found {
		return Admission{}, false, err
	}
	envelope, _, err := decodeMessageAdmissionEnvelope(row, orgID, sessionID, laneID, ingress)
	if err != nil {
		return Admission{}, false, err
	}
	originalRow := row
	// Replay ignores the current finite mode as admission eligibility, but the
	// shared cutover interlock must still precede every domain/receipt/job lock.
	// Direct callers cannot rely on an outer service having acquired it first.
	if _, err := ObserveThreadSendModeInTx(ctx, tx, orgID, sessionID); err != nil {
		return Admission{}, false, err
	}
	owners, err := discoverMessageAdmissionOwners(ctx, tx, orgID, sessionID, laneID)
	if err != nil {
		return Admission{}, false, err
	}
	owners.extraDispatchIDs = []uuid.UUID{row.id}
	if envelope.JobID != uuid.Nil {
		owners.extraJobIDs = []uuid.UUID{envelope.JobID}
	}
	if err := prelockMessageAdmissionOwners(ctx, tx, orgID, owners); err != nil {
		return Admission{}, false, err
	}
	// A replay observes raw scope rather than current admission epochs/status.
	scope, err := lockThreadSendScopeInTx(ctx, tx, orgID, sessionID, laneID)
	if err != nil {
		return Admission{}, false, err
	}
	if scope.session.deletedAt != nil || scope.session.archivedAt != nil {
		return Admission{}, false, pgx.ErrNoRows
	}
	for _, lane := range scope.threads {
		if lane.id == laneID && lane.archivedAt != nil {
			return Admission{}, false, pgx.ErrNoRows
		}
	}
	after, err := discoverMessageAdmissionOwners(ctx, tx, orgID, sessionID, laneID)
	if err != nil {
		return Admission{}, false, err
	}
	if !sameMessageAdmissionOwners(owners, after) {
		return Admission{}, false, ErrMessageAdmissionReplayObservationNeeded
	}
	row, found, err = lookupMessageAdmissionDispatch(ctx, tx, orgID, sessionID, laneID, ingress)
	if err != nil {
		return Admission{}, false, err
	}
	if !found {
		return Admission{}, false, ErrLifecycleConsumerReconciliation
	}
	if row.id != originalRow.id || row.sessionEpoch == nil || row.threadEpoch == nil || row.messageID == nil || *row.sessionEpoch != *originalRow.sessionEpoch || *row.threadEpoch != *originalRow.threadEpoch || *row.messageID != *originalRow.messageID || !bytes.Equal(row.payload, originalRow.payload) {
		return Admission{}, false, ErrMessageAdmissionReplayObservationNeeded
	}
	envelope, command, err := decodeMessageAdmissionEnvelope(row, orgID, sessionID, laneID, ingress)
	if err != nil {
		return Admission{}, false, err
	}
	if err := authorizeMessageAdmissionActor(ctx, tx, orgID, command.ActorID()); err != nil {
		return Admission{}, false, err
	}
	message, err := NewSessionMessageStore(tx).GetByID(ctx, orgID, *row.messageID)
	if err != nil {
		return Admission{}, false, fmt.Errorf("read exact admitted message: %w", err)
	}
	input := command.Input()
	content := input.Message
	if input.PlanMode {
		content = "[PLAN_MODE]\n" + content
	}
	if message.OrgID != orgID || message.SessionID != sessionID || message.ThreadID == nil || *message.ThreadID != laneID || message.TurnNumber != *envelope.MessageTurn || !sameMessageAdmissionUUID(message.UserID, input.ActorID) || message.Content != content || message.Source != input.Source ||
		!slices.Equal(message.Attachments, input.Images) || !slices.Equal(message.References, input.References) || !slices.Equal(message.Commands, input.Commands) {
		return Admission{}, false, ErrLifecycleConsumerReconciliation
	}
	rows, err := tx.Query(ctx, `SELECT `+threadInboxEntryColumns+` FROM thread_inbox_entries WHERE org_id=$1 AND session_id=$2 AND thread_id=$3 AND id=$4 AND message_id=$5`, orgID, sessionID, laneID, envelope.InboxID, message.ID)
	if err != nil {
		return Admission{}, false, fmt.Errorf("read exact admitted inbox input: %w", err)
	}
	inbox, err := pgx.CollectOneRow(rows, scanThreadInboxEntryRow)
	if err != nil {
		return Admission{}, false, fmt.Errorf("read exact admitted inbox association: %w", err)
	}
	if inbox.ClientMessageID == nil || *inbox.ClientMessageID != messageAdmissionInboxKey(ingress, orgID, sessionID, laneID) {
		return Admission{}, false, ErrLifecycleConsumerReconciliation
	}
	var inboxPayload struct {
		MessageID int64  `json:"message_id"`
		Turn      int    `json:"turn_number"`
		PlanMode  bool   `json:"plan_mode"`
		Content   string `json:"content"`
	}
	if json.Unmarshal(inbox.Payload, &inboxPayload) != nil || inboxPayload.MessageID != message.ID || inboxPayload.Turn != message.TurnNumber || inboxPayload.PlanMode != input.PlanMode || inboxPayload.Content != content {
		return Admission{}, false, ErrLifecycleConsumerReconciliation
	}
	a := Admission{orgID: orgID, fence: command.Fence(), ingress: ingress, dispatchID: row.id, disposition: MessageAdmissionReplayed, original: envelope.Disposition,
		message: cloneMessageAdmissionMessage(message), inbox: cloneMessageAdmissionInbox(inbox), comments: cloneMessageAdmissionComments(envelope.ResolvedComments), jobID: envelope.JobID,
		options: input.Dispatch, holderID: cloneMessageAdmissionPointer(input.LeaseHolderID), targetNodeID: cloneMessageAdmissionPointer(envelope.TargetNodeID), threadState: envelope.ThreadStatus, reason: envelope.Reason, valid: true}
	if a.original == MessageAdmissionNewJob {
		a.authority, err = NewAdmissionAuthority(orgID, a.fence, row.id, envelope.ClaimToken, envelope.ClaimTurn, ingress)
		if err != nil {
			return Admission{}, false, ErrLifecycleConsumerReconciliation
		}
		binding, opts, err := messageAdmissionJobOptions(command, a, a.jobID)
		if err != nil {
			return Admission{}, false, err
		}
		base, err := json.Marshal(opts.Payload)
		if err != nil {
			return Admission{}, false, fmt.Errorf("encode original admitted job: %w", err)
		}
		payload, err := encodeLifecycleJobBindingPayload(base, binding)
		if err != nil {
			return Admission{}, false, err
		}
		var exact bool
		var currentPayload []byte
		// Current scheduling/target/lease may legitimately change after this
		// acceptance. They cannot destroy an immutable accepted receipt; return
		// its original concrete options instead of adopting current ones.
		// Existing attempt/retry writers add only the six pinned progress keys.
		// All A1-written keys (including queued message, lane and complete binding)
		// and every other key remain exact. Bound returned bytes in this same-row
		// read; filtering an oversized row away would falsely report a missing job.
		err = tx.QueryRow(ctx, `SELECT queue='agent' AND job_type='continue_session' AND max_attempts=$4
 AND (payload-$5::text[])::text=($3::jsonb)::text,
 CASE WHEN octet_length(payload::text)<=$6 THEN payload ELSE NULL END
 FROM jobs WHERE org_id=$1 AND id=$2`, orgID, a.jobID, payload, a.options.MaxAttempts, messageAdmissionReplayProgressKeys[:], defaultLifecycleConsumerLimits().payloadBytes).Scan(&exact, &currentPayload)
		if err != nil {
			return Admission{}, false, fmt.Errorf("read retained admission job: %w", err)
		}
		if err := ctx.Err(); err != nil {
			return Admission{}, false, err
		}
		validProgress := exact && validMessageAdmissionReplayProgress(currentPayload, a.message.ID)
		if err := ctx.Err(); err != nil {
			return Admission{}, false, err
		}
		if !exact || !validProgress {
			return Admission{}, false, ErrLifecycleConsumerReconciliation
		}
	}
	if err := a.validateLifecycleReceipt(); err != nil {
		return Admission{}, false, fmt.Errorf("validate retained message admission: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return Admission{}, false, err
	}
	return a, true, nil
}

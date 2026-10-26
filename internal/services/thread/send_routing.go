package thread

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var ErrSendNormalizing = errors.New("THREAD_SEND_NORMALIZING")

// SendMessage observes the branch once, then uses only fixed-branch guards.
// Entry observation may retry DB-only work, but no captured fence escapes an
// aborted attempt. The active mutation runner never captures a new expectation.
func (s *Service) SendMessage(ctx context.Context, input SendMessageInput) (*SendMessageResult, error) {
	input = cloneSendMessageInput(input)
	// Correlation only, including legacy shapes that cannot form a valid v1
	// command. This UUID is never persisted or used as admission authority.
	ctx = context.WithValue(ctx, sendDiagnosticContextKey{}, sendDiagnostic{id: uuid.New(), clientKey: input.ClientMessageID})
	prepared, holder, preparationErr := prepareSendMessage(input)
	ctx = context.WithValue(ctx, sendIngressContextKey{}, prepared.Ingress)
	var mode db.ThreadSendMode
	var replay db.Admission
	var found bool
	var fence db.AdmissionFence
	err := s.runSendUnit(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
		mode, replay, found, fence = "", db.Admission{}, false, db.AdmissionFence{}
		var err error
		mode, err = db.ObserveThreadSendModeInTx(unitCtx, tx, input.OrgID, input.SessionID)
		if err != nil {
			return err
		}
		// This is the sole service mode router. Subsequent units may only
		// require their selected mode; a change is never a fallback.
		switch mode {
		case db.ThreadSendModeLegacy:
			return nil
		case db.ThreadSendModeNormalizing:
			return ErrSendNormalizing
		case db.ThreadSendModeActive:
			if preparationErr != nil {
				return preparationErr
			}
			replay, found, err = db.FindMessageAdmissionReplayInTx(unitCtx, tx, input.OrgID, input.SessionID, input.ThreadID, prepared.Ingress)
			if err != nil || found {
				return err
			}
			fence, err = db.CaptureOrdinaryMessageFenceInTx(unitCtx, tx, input.OrgID, input.SessionID, input.ThreadID)
			return err
		default:
			return fmt.Errorf("unsupported observed thread send mode: %q", mode)
		}
	})
	if err != nil {
		return nil, err
	}
	if mode == db.ThreadSendModeLegacy {
		if err := s.requireLegacyChangesetTarget(ctx, input); err != nil {
			return nil, err
		}
		// Legacy units have no canonical v1 receipt, even when this input
		// happened to pass v1 shape validation. Keep correlation only.
		legacyCtx := context.WithValue(ctx, sendIngressContextKey{}, db.LifecycleIngress{})
		return s.sendLegacyMessage(legacyCtx, input, holder)
	}
	if found {
		return resultForAdmission(replay), nil
	}
	prepared.Fence = fence
	command, err := db.NewMessageAdmission(prepared)
	if err != nil {
		return nil, err
	}
	var admission db.Admission
	var effects []db.LifecycleEffect
	err = s.runSendUnit(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
		admission, effects = db.Admission{}, nil
		if err := db.RequireActiveThreadSendModeInTx(unitCtx, tx, input.OrgID, input.SessionID); err != nil {
			return err
		}
		staged, err := db.AdmitMessageInTx(unitCtx, tx, input.OrgID, command)
		if err != nil {
			return err
		}
		admission, effects, err = staged.Unpack()
		return err
	})
	if err != nil {
		if errors.Is(err, ErrCommandOutcomeUnknown) || errors.Is(err, db.ErrMessageAdmissionReplayObservationNeeded) {
			// Only a positive exact receipt can resolve uncertainty or a
			// concurrent winner. Negative observation is never abort proof.
			if receipt, ok := s.observeSendReceipt(ctx, input, prepared.Ingress); ok {
				return resultForAdmission(receipt), nil
			}
		}
		return nil, mapAdmissionError(err)
	}
	if admission.Disposition() != db.MessageAdmissionReplayed {
		s.send.Effects.PublishLifecycle(s.logger.WithContext(ctx), input.OrgID, effects)
		if jobID, ok := admission.JobID(); ok {
			s.send.Effects.NotifyJob(ctx, jobID)
		}
	}
	return resultForAdmission(admission), nil
}

func (s *Service) observeSendReceipt(ctx context.Context, input SendMessageInput, ingress db.LifecycleIngress) (db.Admission, bool) {
	var receipt db.Admission
	var found bool
	err := s.runSendUnit(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
		receipt, found = db.Admission{}, false
		if err := db.RequireActiveThreadSendModeInTx(unitCtx, tx, input.OrgID, input.SessionID); err != nil {
			return err
		}
		var err error
		receipt, found, err = db.FindMessageAdmissionReplayInTx(unitCtx, tx, input.OrgID, input.SessionID, input.ThreadID, ingress)
		return err
	})
	if err != nil {
		s.logger.Warn().Err(err).Msg("could not confirm exact thread send receipt")
		return db.Admission{}, false
	}
	return receipt, found
}

func resultForAdmission(admission db.Admission) *SendMessageResult {
	message, inbox := admission.Message(), admission.InboxEntry()
	result := &SendMessageResult{Message: &message, InboxEntry: &inbox, ThreadStatus: admission.ThreadStatus(), DeliveryState: deliveryStateForInboxEntry(&inbox), Admission: &admission}
	// These legacy fields describe NEW postcommit audit work, not historical
	// receipt contents. The original resolved comments remain on Admission.
	if admission.Disposition() != db.MessageAdmissionReplayed {
		result.ResolvedComments = admission.ResolvedComments()
	}
	return result
}

// Preparation fixes server options, identity and changeset holder once per
// service call. The zero-epoch fence supplies scope only, never epoch authority.
// Active-only shape errors are deferred until routing so legacy policy order
// and acceptance rules remain unchanged.
func prepareSendMessage(input SendMessageInput) (db.MessageAdmissionInput, *uuid.UUID, error) {
	var holder *uuid.UUID
	if input.ChangesetID != nil {
		id := uuid.New()
		holder = &id
		if *input.ChangesetID == uuid.Nil {
			// This is an active-only deferred identity refusal. Legacy still
			// selects its original adapter policy after routing, and direct
			// legacy callers retain their previously generated holder shape.
			return db.MessageAdmissionInput{}, holder, ErrSendChangesetNotFound
		}
	}
	epoch := int64(0)
	fence, err := db.NewAdmissionFence(input.SessionID, &input.ThreadID, epoch, &epoch)
	if err != nil {
		return db.MessageAdmissionInput{}, holder, err
	}
	prepared := db.MessageAdmissionInput{OrgID: input.OrgID, Fence: fence, ActorID: input.UserID, Source: input.MessageSource, Message: input.Message, Images: input.Images, References: input.References, Commands: input.Commands, ChangesetID: input.ChangesetID, LeaseHolderID: holder, ResolveReviewCommentIDs: input.ResolveReviewCommentIDs, PlanMode: input.PlanMode, PostSuccessAction: db.MessagePostSuccessAction(input.PostSuccessAction), PostSuccessAuthorMode: db.MessagePostSuccessAuthorMode(input.PostSuccessAuthorMode), ContinuationDedupeKeyOverride: input.ContinuationDedupeKeyOverride, Dispatch: db.MessageAdmissionDispatchOptions{RunAt: time.Now().UTC().Truncate(time.Microsecond), MaxAttempts: 3}}
	prepared.Ingress, err = db.PrepareMessageAdmissionIngress(prepared, input.ClientMessageID)
	return prepared, holder, err
}

func mapAdmissionError(err error) error {
	if errors.Is(err, ErrCommandOutcomeUnknown) {
		return err
	}
	if errors.Is(err, db.ErrMessageAdmissionBusy) {
		return fmt.Errorf("%w: %w", ErrThreadNotIdle, err)
	}
	if errors.Is(err, db.ErrMessageAdmissionBackpressure) {
		return fmt.Errorf("%w: %w", ErrThreadInboxBackpressure, err)
	}
	return err
}

func cloneSendMessageInput(input SendMessageInput) SendMessageInput {
	input.Images = slices.Clone(input.Images)
	input.References = slices.Clone(input.References)
	input.Commands = slices.Clone(input.Commands)
	input.ResolveReviewCommentIDs = slices.Clone(input.ResolveReviewCommentIDs)
	if input.UserID != nil {
		id := *input.UserID
		input.UserID = &id
	}
	if input.ChangesetID != nil {
		id := *input.ChangesetID
		input.ChangesetID = &id
	}
	if input.ContinuationDedupeKeyOverride != nil {
		key := *input.ContinuationDedupeKeyOverride
		input.ContinuationDedupeKeyOverride = &key
	}
	return input
}

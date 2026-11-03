package reviewloop

import (
	"context"
	"errors"
	"fmt"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	threadsvc "github.com/assembledhq/143/internal/services/thread"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
)

func startupSendSettlement(result *threadsvc.SendMessageResult, err error) (db.ReviewLoopStartupSettlement, error) {
	if errors.Is(err, threadsvc.ErrCommandOutcomeUnknown) {
		return db.ReviewLoopStartupUnknown(), nil
	}
	if err == nil {
		if result == nil || result.Message == nil || result.Message.ID <= 0 {
			return db.ReviewLoopStartupUnknown(), nil
		}
		return db.ReviewLoopStartupAcknowledged(result.Message.ID)
	}
	var contention *db.LifecycleContentionError
	switch {
	case errors.As(err, &contention):
		return db.ReviewLoopStartupKnownAbort("contention")
	case errors.Is(err, threadsvc.ErrSendNormalizing):
		return db.ReviewLoopStartupKnownAbort("normalizing")
	case errors.Is(err, db.ErrThreadSendModeChanged):
		return db.ReviewLoopStartupKnownAbort("mode_changed")
	case errors.Is(err, db.ErrMessageAdmissionReplayObservationNeeded):
		return db.ReviewLoopStartupKnownAbort("replay_observation_needed")
	default:
		return db.ReviewLoopStartupRefused(errors.Is(err, threadsvc.ErrCallerCommandRequiresLegacy)), nil
	}
}

func (s *Service) settleStartup(ctx context.Context, orgID uuid.UUID, intent db.ReviewLoopStartupIntent, outcome db.ReviewLoopStartupSettlement) error {
	err := s.startTransactions.Run(ctx, func(ctx context.Context, tx pgx.Tx) error {
		_, _, err := db.SettleReviewLoopStartupInTx(ctx, orgID, tx, intent, outcome)
		return err
	})
	if !errors.Is(err, db.ErrReviewLoopStartupPointerConflict) {
		return err
	}
	// The failed acceptance unit released every lock before this separate CAS.
	return s.startTransactions.Run(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return db.MarkReviewLoopStartupPointerConflictInTx(ctx, orgID, tx, intent)
	})
}

func (s *Service) advanceStartup(ctx context.Context, orgID, sessionID, loopID uuid.UUID) error {
	var intent db.ReviewLoopStartupIntent
	var found bool
	err := s.startTransactions.Run(ctx, func(ctx context.Context, tx pgx.Tx) error {
		intent, found = db.ReviewLoopStartupIntent{}, false
		var err error
		intent, found, err = db.ReadReviewLoopStartupInTx(ctx, orgID, sessionID, loopID, tx)
		return err
	})
	if err != nil || !found {
		return err
	}
	owner := intent.Owner()
	switch owner.State() {
	case "refused":
		// New refusals retire atomically with settlement. An explicitly
		// recovered older receipt may still need the ordinary safe failure
		// transition; its durable refusal is the only authority for doing so.
		loop, err := s.store.GetRunningLoopBySession(ctx, orgID, sessionID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil || loop.ID != loopID {
			return err
		}
		if loop.AdmissionState == nil || *loop.AdmissionState != models.ReviewLoopAdmissionRefused {
			return db.ErrReviewLoopStartIdentity
		}
		return s.failLoop(ctx, orgID, loop, 1, "Review startup was refused.")
	case "pending":
		command, err := threadsvc.DecodeCallerSendCommand(owner.Raw(), owner.Digest(), threadsvc.CallerSendOwnerScope{
			Purpose: threadsvc.CallerSendReviewLoopInitialSend, OwnerID: loopID, OrgID: orgID, SessionID: sessionID,
			ThreadID: owner.ThreadID(), ClientMessageID: owner.Key()})
		if err != nil {
			return err
		}
		var claimed db.ReviewLoopStartupIntent
		var changed bool
		attempt := uuid.New()
		err = s.startTransactions.Run(ctx, func(ctx context.Context, tx pgx.Tx) error {
			claimed, changed = db.ReviewLoopStartupIntent{}, false
			var err error
			claimed, changed, err = db.ClaimReviewLoopStartupInTx(ctx, orgID, tx, intent, attempt)
			return err
		})
		if err != nil {
			return err
		}
		if !changed || claimed.Owner().State() != "attempting" {
			return nil
		}
		input := command.SendInput()
		proof, err := command.Proof()
		if err != nil {
			return err
		}
		input.CallerCommand = proof
		result, sendErr := s.runtime.SendMessage(ctx, input)
		if sendErr != nil {
			// Error strings may contain command text or transport proofs. Retain
			// the diagnostic type and stable owner, never the unbounded payload.
			zerolog.Ctx(ctx).Warn().Str("error_type", fmt.Sprintf("%T", sendErr)).Str("review_loop_id", loopID.String()).
				Msg("retained review startup send returned an error")
		}
		outcome, err := startupSendSettlement(result, sendErr)
		if err != nil {
			return err
		}
		return s.settleStartup(ctx, orgID, claimed, outcome)
	case "attempting":
		if intent.Due() {
			return s.settleStartup(ctx, orgID, intent, db.ReviewLoopStartupStale())
		}
	case "unknown", "attention":
		if !intent.Due() {
			return nil
		}
		observation, err := threadsvc.ObserveCallerSend(ctx, s.startTransactions, orgID, owner.Ref())
		if err != nil {
			return err
		}
		switch observation.Reason() {
		case threadsvc.CallerSendCommandMalformed, threadsvc.CallerSendVersionUnsupported, threadsvc.CallerSendOwnerLegacy, threadsvc.CallerSendOwnerMissing:
			return db.ErrReviewLoopStartIdentity
		}
		observed := observation.Owner()
		if observed.AttemptID() != owner.AttemptID() || observed.AttemptSeq() != owner.AttemptSeq() || observed.WriterSeq() != owner.WriterSeq() || observed.State() != owner.State() {
			return nil
		}
		outcome := db.ReviewLoopStartupNegativeObservation()
		if messageID, accepted := observation.MessageID(); accepted {
			outcome, err = db.ReviewLoopStartupObserved(observed, messageID, observation.StampCreatedAt())
			if err != nil {
				return err
			}
		}
		return s.settleStartup(ctx, orgID, intent, outcome)
	}
	return nil
}

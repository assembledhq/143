package reviewloop

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/prompts"
	threadsvc "github.com/assembledhq/143/internal/services/thread"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
)

var ErrReviewLoopStartPending = errors.New("REVIEW_LOOP_START_PENDING_CONFIRMATION")
var ErrReviewLoopStartRefused = errors.New("REVIEW_LOOP_START_REFUSED")

// StartPendingError exposes only the stable correlation the API may return.
// The underlying storage cause, envelope, prompt and key stay private.
type StartPendingError struct {
	LoopID uuid.UUID
	cause  error
}

func (e *StartPendingError) Error() string        { return ErrReviewLoopStartPending.Error() }
func (e *StartPendingError) Is(target error) bool { return target == ErrReviewLoopStartPending }
func (e *StartPendingError) Unwrap() error        { return e.cause }

type retainedStartEnvelope struct {
	Version           int                         `json:"version"`
	OrgID             uuid.UUID                   `json:"org_id"`
	SessionID         uuid.UUID                   `json:"session_id"`
	LoopID            uuid.UUID                   `json:"loop_id"`
	PassID            uuid.UUID                   `json:"pass_id"`
	PassIndex         int                         `json:"pass_index"`
	PassStatus        models.ReviewLoopPassStatus `json:"pass_status"`
	Source            models.ReviewLoopSource     `json:"source"`
	AutomationRunID   *uuid.UUID                  `json:"automation_run_id"`
	ChangesetID       *uuid.UUID                  `json:"changeset_id"`
	WorkspaceRevision *int64                      `json:"workspace_revision"`
	DesiredHeadSHA    *string                     `json:"desired_head_sha"`
	AgentType         models.AgentType            `json:"agent_type"`
	MaxPasses         int                         `json:"max_passes"`
	FixMode           models.ReviewLoopFixMode    `json:"fix_mode"`
	ReviewRequired    bool                        `json:"review_required"`
	StartedByUserID   *uuid.UUID                  `json:"started_by_user_id"`
	Checkpoint        string                      `json:"loop_start_checkpoint_key"`
	Thread            retainedStartThread         `json:"thread"`
	Startup           json.RawMessage             `json:"startup_command"`
}
type retainedStartThread struct {
	ID        uuid.UUID        `json:"id"`
	AgentType models.AgentType `json:"agent_type"`
	Model     *string          `json:"model"`
	Label     string           `json:"label"`
}

func (e retainedStartEnvelope) loop() models.SessionReviewLoop {
	return models.SessionReviewLoop{ID: e.LoopID, OrgID: e.OrgID, SessionID: e.SessionID, ThreadID: &e.Thread.ID,
		Source: e.Source, AutomationRunID: e.AutomationRunID, ChangesetID: e.ChangesetID, WorkspaceRevision: e.WorkspaceRevision,
		DesiredHeadSHA: e.DesiredHeadSHA, AgentType: e.AgentType, MaxPasses: e.MaxPasses, FixMode: e.FixMode, ReviewRequired: e.ReviewRequired,
		StartedByUserID: e.StartedByUserID, Status: models.ReviewLoopStatusRunning, LoopStartCheckpointKey: &e.Checkpoint, LatestCheckpointKey: &e.Checkpoint}
}

// Canonicalize the complete JSON object recursively so PostgreSQL's harmless
// JSONB field order and whitespace do not change the retained envelope digest.
func encodeRetainedStart(e retainedStartEnvelope) ([]byte, string, error) {
	raw, err := json.Marshal(e)
	if err != nil {
		return nil, "", err
	}
	if len(raw) > threadsvc.CallerSendCommandMaxEncodedBytes {
		return nil, "", db.ErrReviewLoopStartTooLarge
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var object map[string]any
	if err := decoder.Decode(&object); err != nil {
		return nil, "", err
	}
	canonical, err := json.Marshal(object)
	if err != nil {
		return nil, "", err
	}
	digest := sha256.Sum256(append([]byte("143/review-loop-start-envelope/v1\x00"), canonical...))
	return canonical, hex.EncodeToString(digest[:]), nil
}

func restoreRetainedStart(i db.ReviewLoopStartIntent) (retainedStartEnvelope, threadsvc.CallerSendCommand, error) {
	p := i.Preparation()
	var e retainedStartEnvelope
	decoder := json.NewDecoder(bytes.NewReader(p.Envelope))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&e); err != nil {
		return e, threadsvc.CallerSendCommand{}, db.ErrReviewLoopStartIdentity
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return e, threadsvc.CallerSendCommand{}, db.ErrReviewLoopStartIdentity
	}
	_, digest, err := encodeRetainedStart(e)
	if err != nil || digest != p.EnvelopeDigest || e.Version != 1 || e.OrgID != i.OrgID() || e.SessionID != p.Scope.SessionID ||
		e.LoopID != p.Scope.ID || e.PassID != p.PassID || e.Thread.ID != p.ThreadID || e.Source != p.Scope.Source ||
		e.PassIndex != 1 || e.PassStatus != models.ReviewLoopPassStatusReviewing || e.Checkpoint == "" ||
		e.AgentType != e.Thread.AgentType || e.AgentType.Validate() != nil || e.FixMode.Validate() != nil || e.MaxPasses < 1 || e.MaxPasses > MaxReviewPasses {
		return e, threadsvc.CallerSendCommand{}, db.ErrReviewLoopStartIdentity
	}
	if e.Source == models.ReviewLoopSourceAutomation && !sameRetainedStartUUID(e.AutomationRunID, p.Scope.AutomationRunID) ||
		e.Source == models.ReviewLoopSourcePublication && !sameRetainedStartUUID(e.ChangesetID, p.Scope.ChangesetID) {
		return e, threadsvc.CallerSendCommand{}, db.ErrReviewLoopStartIdentity
	}
	scope := threadsvc.CallerSendOwnerScope{Purpose: threadsvc.CallerSendReviewLoopInitialSend, OwnerID: e.LoopID, OrgID: e.OrgID,
		SessionID: e.SessionID, ThreadID: e.Thread.ID, ClientMessageID: "rl-startup:v1:" + e.LoopID.String()}
	command, err := threadsvc.DecodeCallerSendCommand(p.Input, p.StartupDigest, scope)
	if err != nil {
		return e, command, err
	}
	frozen, err := threadsvc.DecodeCallerSendCommand(e.Startup, p.StartupDigest, scope)
	if err != nil || frozen.Digest() != command.Digest() {
		return e, threadsvc.CallerSendCommand{}, db.ErrReviewLoopStartIdentity
	}
	return e, command, nil
}

func sameRetainedStartUUID(a, b *uuid.UUID) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

// WithStartTransactions supplies the existing bounded owner. It does not
// enable S1; each fresh start still reads the database control explicitly.
func WithStartTransactions(transactions threadsvc.SendTransactions) func(*Service) {
	return func(s *Service) { s.startTransactions = transactions }
}

func (s *Service) retainedStartEnabled(ctx context.Context, orgID uuid.UUID) (bool, error) {
	if s.startTransactions == nil {
		return false, nil
	}
	var enabled bool
	err := s.startTransactions.Run(ctx, func(ctx context.Context, tx pgx.Tx) error {
		enabled = false
		var err error
		enabled, err = db.ReviewLoopStartEnabledInTx(ctx, orgID, tx)
		return err
	})
	return enabled, err
}

func (s *Service) startRetained(ctx context.Context, loop models.SessionReviewLoop, model string) (*models.SessionReviewLoop, error) {
	loop.ID = uuid.New()
	threadID, passID := uuid.New(), uuid.New()
	loop.ThreadID = &threadID
	var modelOverride *string
	if model != "" {
		modelOverride = &model
	}
	input, err := s.initialReviewInput(ctx, loop, loop.StartedByUserID)
	if err != nil {
		return nil, err
	}
	command, err := threadsvc.NewCallerSendCommand(threadsvc.CallerSendReviewLoopInitialSend, loop.ID, input)
	if err != nil {
		return nil, err
	}
	raw, err := command.Encode()
	if err != nil {
		return nil, err
	}
	e := retainedStartEnvelope{Version: 1, OrgID: loop.OrgID, SessionID: loop.SessionID, LoopID: loop.ID, PassID: passID,
		PassIndex: 1, PassStatus: models.ReviewLoopPassStatusReviewing, Source: loop.Source, AutomationRunID: loop.AutomationRunID,
		ChangesetID: loop.ChangesetID, WorkspaceRevision: loop.WorkspaceRevision, DesiredHeadSHA: loop.DesiredHeadSHA,
		AgentType: loop.AgentType, MaxPasses: loop.MaxPasses, FixMode: loop.FixMode, ReviewRequired: loop.ReviewRequired,
		StartedByUserID: loop.StartedByUserID, Checkpoint: *loop.LoopStartCheckpointKey,
		Thread: retainedStartThread{ID: threadID, AgentType: loop.AgentType, Model: modelOverride, Label: reviewThreadLabel(loop.AgentType)}, Startup: raw}
	envelope, digest, err := encodeRetainedStart(e)
	if err != nil {
		return nil, err
	}
	scope := db.ReviewLoopStartScope{ID: loop.ID, SessionID: loop.SessionID, Source: loop.Source}
	if loop.Source == models.ReviewLoopSourceAutomation {
		scope.AutomationRunID = loop.AutomationRunID
	}
	if loop.Source == models.ReviewLoopSourcePublication {
		scope.ChangesetID = loop.ChangesetID
	}
	var prepared db.ReviewLoopStartIntent
	var preparationErr error
	err = s.startTransactions.Run(ctx, func(ctx context.Context, tx pgx.Tx) error {
		prepared = db.ReviewLoopStartIntent{}
		preparationErr = nil
		prepared, preparationErr = db.PrepareReviewLoopStartInTx(ctx, loop.OrgID, tx, db.ReviewLoopStartPreparation{Scope: scope,
			PassID: passID, ThreadID: threadID, Envelope: envelope, EnvelopeDigest: digest, Input: raw, StartupDigest: command.Digest()})
		return preparationErr
	})
	if err != nil {
		// These exact helper refusals precede every INSERT/caller CAS/wake.
		// A failed commit after successful preparation has preparationErr=nil
		// and must retain pending-confirmation semantics instead.
		if preparationErr == db.ErrReviewLoopStartTooLarge || preparationErr == db.ErrReviewLoopStartDisabled {
			return nil, preparationErr
		}
		return nil, &StartPendingError{LoopID: loop.ID, cause: err}
	}
	return s.RecoverStart(ctx, prepared.OrgID(), prepared.Scope().SessionID, prepared.Scope().ID)
}

// RecoverStart uses the original correlation and no fresh preflight, template,
// policy, head, snapshot or identity. Missing T0 evidence remains uncertainty.
func (s *Service) RecoverStart(ctx context.Context, orgID, sessionID, startID uuid.UUID) (*models.SessionReviewLoop, error) {
	if s.startTransactions == nil {
		return nil, &StartPendingError{LoopID: startID, cause: db.ErrReviewLoopStartDisabled}
	}
	var retained db.ReviewLoopStartIntent
	var found bool
	err := s.startTransactions.Run(ctx, func(ctx context.Context, tx pgx.Tx) error {
		retained, found = db.ReviewLoopStartIntent{}, false
		var err error
		retained, found, err = db.InspectReviewLoopStartInTx(ctx, orgID, sessionID, startID, tx)
		return err
	})
	if err != nil || !found {
		return nil, &StartPendingError{LoopID: startID, cause: err}
	}
	loop, recoverErr := s.recoverRetainedStart(ctx, retained)
	if retained.State() == "prepared" && recoverErr != nil && !errors.Is(recoverErr, ErrReviewLoopStartRefused) {
		// Repeated blocked E1/T1 recovery must not monopolize the oldest due
		// batch. The conditional metadata update cannot turn an unknown T1
		// acknowledgement into a claim that creation rolled back.
		deferErr := s.startTransactions.Run(ctx, func(ctx context.Context, tx pgx.Tx) error {
			return db.DeferPreparedReviewLoopStartInTx(ctx, orgID, tx, retained)
		})
		if deferErr != nil {
			zerolog.Ctx(ctx).Warn().Str("review_loop_id", startID.String()).Str("error_type", fmt.Sprintf("%T", deferErr)).Msg("prepared review startup recovery scheduling requires reconciliation")
		}
	}
	return loop, recoverErr
}

func (s *Service) recoverRetainedStart(ctx context.Context, retained db.ReviewLoopStartIntent) (*models.SessionReviewLoop, error) {
	e, _, err := restoreRetainedStart(retained)
	if err != nil {
		return nil, err
	}
	if retained.State() == "refused" {
		return nil, ErrReviewLoopStartRefused
	}
	if retained.State() == "prepared" {
		// No database checkout is held across this row-only service call.
		model := ""
		if e.Thread.Model != nil {
			model = *e.Thread.Model
		}
		if !retained.Cancelled() {
			input := threadsvc.CreateThreadInput{ID: e.Thread.ID, OrgID: e.OrgID, SessionID: e.SessionID,
				AgentType: string(e.Thread.AgentType), Model: model, Label: e.Thread.Label}
			_, err = s.runtime.CreateThread(ctx, input)
			if detail := retainedCreationRefusalDetail(err); detail != "" {
				retained, err = s.refuseRetainedStart(ctx, retained, detail)
				if err == nil && retained.State() == "prepared" {
					// The locked refusal unit found the original row. Validate and
					// adopt it once through E1; never allocate a replacement ID.
					_, err = s.runtime.CreateThread(ctx, input)
					if errors.Is(err, db.ErrThreadCreationIdentityConflict) {
						retained, err = s.refuseRetainedStart(ctx, retained, "thread_identity_conflict")
					}
				}
			}
			if err != nil {
				return nil, &StartPendingError{LoopID: e.LoopID, cause: err}
			}
		}
		if retained.State() == "refused" {
			return nil, ErrReviewLoopStartRefused
		}
		var created db.ReviewLoopStartIntent
		var creationErr error
		err = s.startTransactions.Run(ctx, func(ctx context.Context, tx pgx.Tx) error {
			created = db.ReviewLoopStartIntent{}
			creationErr = nil
			created, creationErr = db.CompleteReviewLoopStartInTx(ctx, e.OrgID, tx, retained, e.loop())
			return creationErr
		})
		if err != nil {
			// The runner returns the exact callback error only after a known
			// whole rollback. A wrapped/unknown acknowledgement cannot prove
			// that the competing insert lost and must stay pending.
			if creationErr != nil && err == creationErr && isRunningLoopConflict(creationErr) {
				created, err = s.refuseRetainedStart(ctx, retained, "running_conflict")
			}
			if err != nil {
				return nil, &StartPendingError{LoopID: e.LoopID, cause: err}
			}
		}
		retained = created
		if retained.State() == "refused" {
			return nil, ErrReviewLoopStartRefused
		}
	}
	var loop models.SessionReviewLoop
	err = s.startTransactions.Run(ctx, func(ctx context.Context, tx pgx.Tx) error {
		loop = models.SessionReviewLoop{}
		var err error
		loop, err = db.AdoptCreatedReviewLoopInTx(ctx, e.OrgID, tx, retained, e.loop())
		return err
	})
	if err != nil {
		return nil, &StartPendingError{LoopID: e.LoopID, cause: err}
	}
	if err := s.advanceStartup(ctx, e.OrgID, e.SessionID, e.LoopID); err != nil {
		zerolog.Ctx(ctx).Warn().Str("review_loop_id", e.LoopID.String()).Msg("retained review startup requires reconciliation")
	}
	var current models.SessionReviewLoop
	err = s.startTransactions.Run(ctx, func(ctx context.Context, tx pgx.Tx) error {
		current = models.SessionReviewLoop{}
		var err error
		current, err = db.AdoptCreatedReviewLoopInTx(ctx, e.OrgID, tx, retained, e.loop())
		return err
	})
	if err == nil {
		loop = current
	} else {
		zerolog.Ctx(ctx).Warn().Str("review_loop_id", e.LoopID.String()).Msg("returning last confirmed review startup admission state")
	}
	return &loop, nil
}

// Only errors proving that this E1 call did not create a row are finite
// refusals. Raw database/transport errors remain uncertain, including a commit
// whose acknowledgement was lost after an actual exact-ID insert.
func retainedCreationRefusalDetail(err error) string {
	var codeReviewOwned *models.SessionCodeReviewOwnedError
	switch {
	case errors.Is(err, db.ErrThreadCreationIdentityConflict):
		return "thread_identity_conflict"
	case errors.Is(err, db.ErrThreadLimitReached):
		return "creation_thread_capacity"
	case errors.Is(err, threadsvc.ErrSessionTerminal):
		return "creation_session_terminal"
	case errors.As(err, &codeReviewOwned):
		return "creation_code_review_owned"
	case errors.Is(err, models.ErrSessionAutomationOwned):
		return "creation_automation_owned"
	case errors.Is(err, threadsvc.ErrInvalidModel):
		return "creation_invalid_model"
	case errors.Is(err, threadsvc.ErrInvalidAgentType):
		return "creation_invalid_agent"
	default:
		return ""
	}
}

func (s *Service) refuseRetainedStart(ctx context.Context, expected db.ReviewLoopStartIntent, detail string) (db.ReviewLoopStartIntent, error) {
	var current db.ReviewLoopStartIntent
	err := s.startTransactions.Run(ctx, func(ctx context.Context, tx pgx.Tx) error {
		current = db.ReviewLoopStartIntent{}
		var found bool
		var err error
		current, found, err = db.ReadReviewLoopStartInTx(ctx, expected.OrgID(), expected.Scope().SessionID, expected.Scope().ID, tx)
		if err != nil {
			return err
		}
		if !found || current.Preparation().EnvelopeDigest != expected.Preparation().EnvelopeDigest ||
			current.Preparation().StartupDigest != expected.Preparation().StartupDigest {
			return db.ErrReviewLoopStartIdentity
		}
		if current.State() == "prepared" {
			if err := db.RefuseReviewLoopStartInTx(ctx, expected.OrgID(), tx, current, detail); err != nil {
				return err
			}
			current, found, err = db.ReadReviewLoopStartInTx(ctx, expected.OrgID(), expected.Scope().SessionID, expected.Scope().ID, tx)
			if err != nil {
				return err
			}
			if !found {
				return db.ErrReviewLoopStartIdentity
			}
		}
		return nil
	})
	return current, err
}

func (s *Service) initialReviewInput(ctx context.Context, loop models.SessionReviewLoop, userID *uuid.UUID) (threadsvc.SendMessageInput, error) {
	message := prompts.ReviewLoopReviewPrompt(prompts.ReviewLoopReviewPromptData{AgentType: loop.AgentType, FixMode: loop.FixMode})
	input := threadsvc.SendMessageInput{OrgID: loop.OrgID, SessionID: loop.SessionID, ThreadID: *loop.ThreadID, UserID: userID, Message: message}
	if agentHasBuiltinReviewCommand(loop.AgentType) {
		input.Commands = []models.SessionInputCommand{{Kind: "command", AgentType: loop.AgentType, Name: "review", Token: "/review", Display: "/review",
			Arguments: strings.TrimSpace(strings.TrimPrefix(message, "/review")), Source: models.SessionInputCommandSourceBuiltin}}
	} else {
		input.Message = naturalLanguageReviewPrompt(message)
	}
	if loop.ChangesetID != nil {
		primary, err := s.store.GetPrimaryChangesetID(ctx, loop.OrgID, loop.SessionID)
		if err != nil {
			return input, err
		}
		if primary != *loop.ChangesetID {
			input.ChangesetID = loop.ChangesetID
		}
	}
	return input, nil
}

func (s *Service) recoverCallerStart(ctx context.Context, orgID, sessionID uuid.UUID, req StartReviewLoopRequest) (*models.SessionReviewLoop, bool, error) {
	if s.startTransactions == nil {
		return nil, false, nil
	}
	var caller *uuid.UUID
	switch req.Source {
	case models.ReviewLoopSourcePublication:
		caller = req.ChangesetID
	case models.ReviewLoopSourceAutomation:
		caller = req.AutomationRunID
	default:
		return nil, false, nil
	}
	if caller == nil || *caller == uuid.Nil {
		return nil, false, nil
	}
	var id *uuid.UUID
	err := s.startTransactions.Run(ctx, func(ctx context.Context, tx pgx.Tx) error {
		id = nil
		var err error
		id, err = db.ReadReviewLoopCallerStartInTx(ctx, orgID, sessionID, tx, req.Source, *caller)
		return err
	})
	if err != nil {
		return nil, false, err
	}
	if id == nil {
		return nil, false, nil
	}
	loop, err := s.RecoverStart(ctx, orgID, sessionID, *id)
	return loop, true, err
}

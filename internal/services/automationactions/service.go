// Package automationactions executes independently resumable external actions.
// Uncertain sends require reconciliation rather than automatic retries.
package automationactions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

var ErrStaleHead = errors.New("pull request is closed or no longer at the requested head")
var ErrIntegrationNotReady = errors.New("action integration is not ready")
var ErrBudgetExhausted = errors.New("insufficient time for a bounded action send; resume the pending step")
var ErrInvalidRequest = errors.New("invalid action request")

type Store interface {
	Resolve(context.Context, uuid.UUID, models.AutomationActionActor) (models.AutomationActionScope, error)
	ResolveStatus(context.Context, uuid.UUID, models.AutomationActionActor) (models.AutomationActionScope, error)
	Expire(context.Context, uuid.UUID, models.AutomationActionScope, string) error
	List(context.Context, uuid.UUID, models.AutomationActionScope, string) ([]models.AutomationAction, error)
	Reserve(context.Context, uuid.UUID, models.AutomationActionActor, models.AutomationActionRequest, string, models.AutomationActionConfig, json.RawMessage) (models.AutomationAction, error)
	Claim(context.Context, uuid.UUID, models.AutomationActionActor, uuid.UUID) (models.AutomationAction, error)
	Finish(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, models.AutomationActionStatus, string, string, string) error
}
type PullRequest struct {
	Head   string `json:"head"`
	Open   bool   `json:"open"`
	Title  string `json:"title"`
	URL    string `json:"url"`
	Author string `json:"author"`
}
type Payload struct {
	Request models.AutomationActionRequest `json:"request"`
	PR      PullRequest                    `json:"pr"`
}
type Receipt struct{ ID, URL string }

// SendFailure is definitive only when the adapter knows no effect occurred.
type SendFailure struct {
	Code       string
	Definitive bool
}

func (e *SendFailure) Error() string { return e.Code }

type Provider interface {
	Inspect(context.Context, models.AutomationActionScope) (PullRequest, error)
	Preflight(context.Context, models.AutomationActionScope, models.AutomationActionRequest) error
	Send(context.Context, models.AutomationActionScope, models.AutomationAction, Payload) (Receipt, error)
}
type Service struct {
	store    Store
	provider Provider
	logger   zerolog.Logger
}

func New(store Store, provider Provider, logger zerolog.Logger) *Service {
	return &Service{store, provider, logger}
}
func (s *Service) Status(ctx context.Context, actor models.AutomationActionActor, operation string) (models.AutomationActionResult, error) {
	if err := models.ValidateAutomationOperationKey(operation); err != nil {
		return models.AutomationActionResult{}, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	scope, err := s.store.ResolveStatus(ctx, actor.OrgID, actor)
	if err != nil {
		return models.AutomationActionResult{}, err
	}
	actions, err := s.store.List(ctx, actor.OrgID, scope, operation)
	return models.AutomationActionResultFor(actions, time.Now()), err
}

// Execute admits or resumes exactly one step; other steps never affect its result.
func (s *Service) Execute(ctx context.Context, actor models.AutomationActionActor, key models.AutomationActionKey, request *models.AutomationActionRequest) (result models.AutomationActionResult, resultErr error) {
	if err := key.Validate(); err != nil {
		return result, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	if request != nil {
		if request.AutomationActionKey != key {
			return result, ErrInvalidRequest
		}
		if err := request.Validate(); err != nil {
			return result, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
		}
	}
	scope, err := s.store.Resolve(ctx, actor.OrgID, actor)
	if err != nil {
		return result, err
	}
	if err = s.store.Expire(ctx, actor.OrgID, scope, key.OperationKey); err != nil {
		return result, err
	}
	actions, err := s.store.List(ctx, actor.OrgID, scope, key.OperationKey)
	if err != nil {
		return result, err
	}
	sent := false
	// Return authoritative receipts even if cancellation interrupts persistence or the response.
	defer func() {
		readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		current, readErr := s.store.List(readCtx, actor.OrgID, scope, key.OperationKey)
		if readErr != nil {
			if resultErr == nil {
				resultErr = readErr
			}
			return
		}
		for _, a := range current {
			if a.ActionKey == key.ActionKey {
				result = models.AutomationActionResultFor([]models.AutomationAction{a}, time.Now())
				result.Reused = a.Status == models.AutomationActionSucceeded && !sent
				return
			}
		}
	}()
	var action models.AutomationAction
	for _, a := range actions {
		if a.ActionKey == key.ActionKey {
			action = a
			break
		}
	}
	if action.ID != uuid.Nil {
		if request != nil {
			digest, digestErr := requestDigest(*request, scope.Config)
			if digestErr != nil {
				return result, digestErr
			}
			if digest != action.RequestDigest {
				return result, db.ErrAutomationActionConflict
			}
		}
		if action.Status == models.AutomationActionSucceeded || action.Status == models.AutomationActionSending || action.EffectiveStatus(time.Now()) == models.AutomationActionUnknown {
			return result, nil
		}
	} else {
		if request == nil {
			return result, fmt.Errorf("%w: no recorded step for these keys", ErrInvalidRequest)
		}
		if err = request.ValidateFor(scope.Config); err != nil {
			return result, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
		}
		if err = validateTarget(scope, *request); err != nil {
			return result, err
		}

		raw, marshalErr := json.Marshal(Payload{Request: *request})
		if marshalErr != nil {
			return result, marshalErr
		}
		digest, digestErr := requestDigest(*request, scope.Config)
		if digestErr != nil {
			return result, digestErr
		}
		action, err = s.store.Reserve(ctx, actor.OrgID, actor, *request, digest, scope.Config, raw)
		if err != nil {
			return result, err
		}
		if action.Status != models.AutomationActionPending && action.Status != models.AutomationActionFailed {
			return result, nil
		}
	}
	var payload Payload
	if err = json.Unmarshal(action.Payload, &payload); err != nil {
		return result, err
	}
	if err = validateTarget(scope, payload.Request); err != nil {
		return result, err
	}
	if err = actionBudget(ctx); err != nil {
		return result, err
	}
	if err = s.provider.Preflight(ctx, scope, payload.Request); err != nil {
		return result, fmt.Errorf("%w: %v", ErrIntegrationNotReady, err)
	}
	if payload.Request.PRNumber > 0 {
		if err = actionBudget(ctx); err != nil {
			return result, err
		}
		scope.PRNumber = payload.Request.PRNumber
		pr, inspectErr := s.provider.Inspect(ctx, scope)
		if inspectErr != nil {
			return result, inspectErr
		}
		if !pr.Open || pr.Head != payload.Request.HeadSHA {
			return result, ErrStaleHead
		}
		payload.PR = pr
	}
	if err = actionBudget(ctx); err != nil {
		return result, err
	}
	claimed, err := s.store.Claim(ctx, actor.OrgID, actor, action.ID)
	if errors.Is(err, db.ErrAutomationActionBusy) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	sendCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	sent = true
	receipt, sendErr := s.provider.Send(sendCtx, scope, claimed, payload)
	cancel()
	status, code := models.AutomationActionSucceeded, ""
	if sendErr != nil {
		status, code = models.AutomationActionUnknown, "DELIVERY_UNKNOWN"
		var failure *SendFailure
		if errors.As(sendErr, &failure) {
			code = failure.Code
			if failure.Definitive {
				status = models.AutomationActionFailed
			}
		}
	}
	finishCtx, finishCancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	finishErr := s.store.Finish(finishCtx, actor.OrgID, claimed.ID, *claimed.SendToken, status, receipt.ID, receipt.URL, code)
	finishCancel()
	s.logger.Info().Str("org_id", actor.OrgID.String()).Str("action_id", claimed.ID.String()).Str("kind", string(claimed.Kind)).Str("provider_status", string(status)).Bool("receipt_recorded", finishErr == nil).Str("error_code", code).Msg("automation action attempt finished")
	if finishErr != nil {
		return result, fmt.Errorf("record action receipt: %w", finishErr)
	}
	return result, nil
}
func actionBudget(ctx context.Context) error {
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < 6*time.Second {
		return ErrBudgetExhausted
	}
	return nil
}

func validateTarget(scope models.AutomationActionScope, r models.AutomationActionRequest) error {
	if scope.PRNumber > 0 && r.PRNumber > 0 && (r.PRNumber != scope.PRNumber || r.HeadSHA != scope.HeadSHA) {
		return ErrStaleHead
	}
	if strings.HasPrefix(string(r.Kind), "github_") && !strings.EqualFold(scope.Config.Repository, scope.RepositoryName) {
		return db.ErrAutomationActionUnauthorized
	}
	return nil
}
func requestDigest(request models.AutomationActionRequest, config models.AutomationActionConfig) (string, error) {
	raw, err := json.Marshal(struct {
		Request models.AutomationActionRequest
		Config  models.AutomationActionConfig
	}{request, config.Canonical()})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

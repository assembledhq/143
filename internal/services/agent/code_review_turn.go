package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
)

var ErrCodeReviewRecheckLeaseLost = errors.New("code review recheck job lease or assessment fence lost")

type CodeReviewTurnContinueOptions struct {
	AssessmentID uuid.UUID
	ThreadID     uuid.UUID
	ExpectedTurn int
	MessageID    int64
}

type CodeReviewTurnStore interface {
	Claim(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID, int, int64) (bool, error)
	NativeResumeProvider(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID) (string, bool, error)
	Complete(context.Context, models.CodeReviewRecheckTurnCompletion) (int64, error)
	RecordAttemptUsage(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID, string, json.RawMessage) error
	Fail(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID, string) error
}

type codeReviewTurnState struct {
	options          CodeReviewTurnContinueOptions
	jobID            uuid.UUID
	lockToken        uuid.UUID
	resumeProviderID string
}

type codeReviewTurnContextKey struct{}

func isCodeReviewTurnContext(ctx context.Context) bool {
	value, _ := ctx.Value(codeReviewTurnContextKey{}).(bool)
	return value
}

// SetCodeReviewTurnStore attaches the assessment-scoped lease/receipt store.
// lint:allow-no-orgid reason="process-wide dependency injection for recheck execution"
func (o *Orchestrator) SetCodeReviewTurnStore(store CodeReviewTurnStore) { o.codeReviewTurns = store }

func (o *Orchestrator) beginCodeReviewTurn(ctx context.Context, session *models.Session, opts *CodeReviewTurnContinueOptions) (*codeReviewTurnState, error) {
	if opts == nil {
		return nil, nil
	}
	if o.codeReviewTurns == nil {
		return nil, errors.New("code review recheck turn store unavailable")
	}
	jobID, hasJob := jobctx.JobIDFromContext(ctx)
	token, hasToken := jobctx.LockTokenFromContext(ctx)
	if !hasJob || !hasToken || jobID == uuid.Nil || token == uuid.Nil || opts.AssessmentID == uuid.Nil || opts.ThreadID == uuid.Nil || opts.ExpectedTurn < 1 || opts.MessageID < 1 {
		return nil, ErrCodeReviewRecheckLeaseLost
	}
	owned, err := o.codeReviewTurns.Claim(ctx, session.OrgID, opts.AssessmentID, jobID, token, session.ID, opts.ThreadID, opts.ExpectedTurn, opts.MessageID)
	if err != nil {
		return nil, fmt.Errorf("claim code review recheck turn: %w", err)
	}
	if !owned {
		return nil, ErrCodeReviewRecheckLeaseLost
	}
	providerID, native, err := o.codeReviewTurns.NativeResumeProvider(ctx, session.OrgID, opts.AssessmentID, jobID, token)
	if err != nil {
		return nil, fmt.Errorf("check code review checkpoint provenance: %w", err)
	}
	state := &codeReviewTurnState{options: *opts, jobID: jobID, lockToken: token}
	if native {
		state.resumeProviderID = providerID
	}
	return state, nil
}

func (o *Orchestrator) recordCodeReviewAttemptUsage(ctx context.Context, session *models.Session, state *codeReviewTurnState, launch string, result *AgentResult) error {
	if state == nil {
		return nil
	}
	var usage json.RawMessage
	if result != nil && HasPersistableTokenUsage(result.TokenUsage) {
		encoded, err := json.Marshal(result.TokenUsage)
		if err != nil {
			return err
		}
		usage = encoded
	}
	return o.codeReviewTurns.RecordAttemptUsage(ctx, session.OrgID, state.options.AssessmentID, state.jobID, state.lockToken, state.lockToken.String()+":"+launch, usage)
}

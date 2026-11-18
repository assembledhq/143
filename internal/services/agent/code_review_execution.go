package agent

import (
	"context"

	"github.com/google/uuid"

	"github.com/assembledhq/143/internal/models"
)

type codeReviewExecutionContextKey struct{}

// WithCodeReviewExecution scopes adapter runtime defaults to a built-in review
// thread using the persisted session identity, never prompt or repository text.
// Unlike the recheck lease context, it also covers initial reviewers and synthesis.
func WithCodeReviewExecution(ctx context.Context, session *models.Session, threadID *uuid.UUID) context.Context {
	return context.WithValue(ctx, codeReviewExecutionContextKey{}, isCodeReviewThreadTurn(session, threadID))
}

// IsCodeReviewExecution reports whether this adapter invocation belongs to a
// built-in review thread. The marker follows retries without changing sandbox state.
func IsCodeReviewExecution(ctx context.Context) bool {
	review, _ := ctx.Value(codeReviewExecutionContextKey{}).(bool)
	return review
}

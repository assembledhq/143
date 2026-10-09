package worker

import (
	"context"
	"errors"

	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

func registerWorkspaceWaitDeadLetter(ctx context.Context, stores *Stores, logger zerolog.Logger, session models.Session, threadID uuid.UUID, turnBefore int) {
	jobID, hasJob := jobctx.JobIDFromContext(ctx)
	if stores == nil || stores.SessionThreads == nil || !hasJob || session.Origin != models.SessionOriginCodeReview || threadID == uuid.Nil {
		return
	}
	jobctx.RegisterDeadLetterHook(ctx, func(hookCtx context.Context, terminalErr error) {
		if !errors.Is(terminalErr, agent.ErrSandboxWorkspaceNotReady) {
			return
		}
		const detail = "The review could not finish preparing its workspace before automatic retries expired."
		failed, err := stores.SessionThreads.FailWorkspaceWaitForDeadLetter(hookCtx, session.OrgID, session.ID, threadID, jobID, turnBefore+1, detail)
		if err != nil {
			logger.Error().Err(err).Str("session_id", session.ID.String()).Str("thread_id", threadID.String()).Msg("failed to record exhausted review workspace wait")
		} else if failed {
			logger.Warn().Err(terminalErr).Str("session_id", session.ID.String()).Str("thread_id", threadID.String()).Msg("recorded exhausted review workspace wait for controller reconciliation")
		}
	})
}

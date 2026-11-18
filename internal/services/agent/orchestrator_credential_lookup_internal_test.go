package agent

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
)

type lookupDeadLetterStoreCall struct {
	orgID        uuid.UUID
	sessionID    uuid.UUID
	threadID     uuid.UUID
	jobID        uuid.UUID
	turn         int
	detail       string
	contextError error
	bounded      bool
}

type lookupDeadLetterStore struct {
	SessionThreadStore
	calls []lookupDeadLetterStoreCall
}

func (s *lookupDeadLetterStore) FailCredentialLookupForDeadLetter(ctx context.Context, orgID, sessionID, threadID, jobID uuid.UUID, expectedTurn int, detail string) (bool, error) {
	_, bounded := ctx.Deadline()
	s.calls = append(s.calls, lookupDeadLetterStoreCall{orgID: orgID, sessionID: sessionID, threadID: threadID, jobID: jobID, turn: expectedTurn, detail: detail, contextError: ctx.Err(), bounded: bounded})
	return true, nil
}

func TestPreExecutionCredentialLookupHookMatchesExactFailureAndTurn(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		terminal       string
		withoutJob     bool
		invalidTurn    bool
		mutateIdentity bool
		wantCall       bool
	}{
		{name: "exact failure", terminal: "exact", wantCall: true},
		{name: "wrapped exact failure", terminal: "wrapped", wantCall: true},
		{name: "cancelled attempt context is detached", terminal: "wrapped", mutateIdentity: true, wantCall: true},
		{name: "unrelated terminal failure", terminal: "unrelated"},
		{name: "another lookup with the same underlying cause", terminal: "another_lookup"},
		{name: "bare underlying cause", terminal: "bare_cause"},
		{name: "missing job identity", terminal: "exact", withoutJob: true},
		{name: "invalid expected turn", terminal: "exact", invalidTurn: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			orgID, sessionID, threadID, jobID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			expectedThread := threadID
			session := &models.Session{OrgID: orgID, ID: sessionID, Origin: models.SessionOriginCodeReview, CurrentTurn: 47}
			store := &lookupDeadLetterStore{}
			o := &Orchestrator{sessionThreads: store, logger: zerolog.Nop()}
			cause := errors.New("synthetic credential lookup unavailable")
			resolution := &resolvedAgentEnv{credentialLookupErr: cause}
			ctx := jobctx.WithDeadLetterHooks(context.Background())
			if !tt.withoutJob {
				ctx = jobctx.WithJobID(ctx, jobID)
			}
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			turn := 3
			if tt.invalidTurn {
				turn = 0
			}

			lookupErr := o.executionCredentialLookupError(ctx, session, &threadID, turn, resolution)
			require.ErrorIs(t, lookupErr, cause, "the startup error should preserve its actual lookup cause")
			var terminalErr error
			switch tt.terminal {
			case "exact":
				terminalErr = lookupErr
			case "wrapped":
				terminalErr = fmt.Errorf("synthetic retry window expired: %w", lookupErr)
			case "unrelated":
				terminalErr = errors.New("synthetic unrelated worker failure")
			case "another_lookup":
				terminalErr = &executionCredentialLookupFailure{cause: cause}
			case "bare_cause":
				terminalErr = cause
			}
			if tt.mutateIdentity {
				session.OrgID, session.ID, session.CurrentTurn = uuid.New(), uuid.New(), 99
				threadID = uuid.New()
				cancel()
			}
			jobctx.RunDeadLetterHooks(ctx, terminalErr)

			expected := []lookupDeadLetterStoreCall(nil)
			if tt.wantCall {
				expected = []lookupDeadLetterStoreCall{{orgID: orgID, sessionID: sessionID, threadID: expectedThread, jobID: jobID, turn: 3, detail: "Coding credentials could not be loaded because of a temporary infrastructure error. Retry the session.", bounded: true}}
			}
			require.Equal(t, expected, store.calls, "only the exact startup failure may request a guarded write for the captured tenant, job, thread, and message turn")
		})
	}
}

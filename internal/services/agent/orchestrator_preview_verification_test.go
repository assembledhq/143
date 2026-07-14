package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

type recordingSuccessfulTurnVerifier struct {
	inputs []SuccessfulTurnVerification
	err    error
}

func (v *recordingSuccessfulTurnVerifier) VerifySuccessfulTurn(_ context.Context, input SuccessfulTurnVerification) error {
	v.inputs = append(v.inputs, input)
	return v.err
}

func TestVerifySuccessfulTurn(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		diff             string
		expectedRevision int64
		verifierErr      error
	}{
		{name: "increments revision for changed workspace", diff: "diff --git a/page.tsx b/page.tsx", expectedRevision: 8},
		{name: "keeps revision for unchanged workspace", diff: "", expectedRevision: 7},
		// Automatic verification is advisory: a verifier failure must not abort
		// the successful turn, but it must still be invoked and recorded so its
		// evidence is durably captured and surfaced in the UI.
		{name: "tolerates verification failure without aborting the turn", diff: "diff --git a/page.tsx b/page.tsx", expectedRevision: 8, verifierErr: errors.New("browser check failed")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			verifier := &recordingSuccessfulTurnVerifier{err: tt.verifierErr}
			orchestrator := &Orchestrator{successfulTurnVerifier: verifier}
			session := &models.Session{ID: uuid.New(), WorkspaceRevision: 7}
			result := &AgentResult{Diff: tt.diff}

			orchestrator.verifySuccessfulTurn(context.Background(), session, &Sandbox{ID: "sandbox-1"}, result, zerolog.Nop())

			require.Len(t, verifier.inputs, 1, "successful coding turns should invoke preview verification exactly once")
			require.Equal(t, tt.expectedRevision, verifier.inputs[0].WorkspaceRevision, "verification evidence should use the resulting workspace revision")
			require.Equal(t, tt.diff, verifier.inputs[0].Diff, "verification should receive the adapter-produced workspace diff")
		})
	}
}

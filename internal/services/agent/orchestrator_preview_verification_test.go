package agent

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

type recordingSuccessfulTurnVerifier struct {
	inputs []SuccessfulTurnVerification
}

func (v *recordingSuccessfulTurnVerifier) VerifySuccessfulTurn(_ context.Context, input SuccessfulTurnVerification) error {
	v.inputs = append(v.inputs, input)
	return nil
}

func TestVerifySuccessfulTurn(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		diff             string
		expectedRevision int64
	}{
		{name: "increments revision for changed workspace", diff: "diff --git a/page.tsx b/page.tsx", expectedRevision: 8},
		{name: "keeps revision for unchanged workspace", diff: "", expectedRevision: 7},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			verifier := &recordingSuccessfulTurnVerifier{}
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

package agent

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type reviewEvidenceTransportFailureProvider struct {
	markerReadinessFailureProvider
}

func (p *reviewEvidenceTransportFailureProvider) ExecWithStdin(context.Context, *Sandbox, string, io.Reader, io.Writer, io.Writer) (int, error) {
	p.executions.Add(1)
	return -1, p.executionError
}

func TestReviewPreparationExecutionTransportDiagnostics(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name         string
		failure      error
		expected     error
		privateInput bool
	}{
		{name: "ordinary transport", failure: errors.New("provider failed with fake-private-diagnostic"), expected: ErrCodeReviewPreparationTransport},
		{name: "private input transport", failure: errors.New("provider failed with fake-private-diagnostic"), expected: ErrCodeReviewPreparationTransport, privateInput: true},
		{name: "ordinary cleanup uncertainty", failure: ErrCodeReviewPreparationCleanup, expected: ErrCodeReviewPreparationCleanup},
		{name: "private input cleanup uncertainty", failure: ErrCodeReviewPreparationCleanup, expected: ErrCodeReviewPreparationCleanup, privateInput: true},
		{name: "ordinary source integrity", failure: ErrCodeReviewSourceIntegrity, expected: ErrCodeReviewSourceIntegrity},
		{name: "private input source integrity", failure: ErrCodeReviewSourceIntegrity, expected: ErrCodeReviewSourceIntegrity, privateInput: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			provider := &reviewEvidenceTransportFailureProvider{markerReadinessFailureProvider: markerReadinessFailureProvider{executionError: test.failure}}
			var input io.Reader
			if test.privateInput {
				input = strings.NewReader("private input")
			}
			err := (&Orchestrator{provider: provider}).execReviewEvidenceInput(context.Background(), &Sandbox{ID: "shared", WorkDir: "/workspace"}, "inspect captured inputs", "synthetic-authentication", input)
			require.ErrorIs(t, err, test.expected, "execution failures must preserve their transport or fatal classification across both executor contracts")
			require.Equal(t, int64(1), provider.executions.Load(), "safe environments must invoke the selected executor exactly once")
			require.NotContains(t, err.Error(), "fake-private-diagnostic", "transport errors must not expose arbitrary provider details")
			if test.expected == ErrCodeReviewPreparationTransport {
				require.EqualError(t, err, "prepare review evidence: review preparation transport unavailable", "transport execution must expose only its finite classification")
			} else {
				require.NotErrorIs(t, err, ErrCodeReviewPreparationTransport, "cleanup and source-integrity failures must not become availability retries")
			}
		})
	}
}

func TestReviewPreparationEnvironmentAvailabilityClassification(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		validation error
		waiting    bool
	}{
		{name: "container unavailable", validation: ErrCodeReviewPreparationTransport, waiting: true},
		{name: "permission denied", validation: errors.New("environment access denied")},
		{name: "unsafe environment", validation: errors.New("environment contains stored credentials")},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			provider := &markerReadinessFailureProvider{validationError: test.validation}
			orchestrator := &Orchestrator{provider: provider}
			sandbox := &Sandbox{ID: "shared", WorkDir: "/workspace"}
			session := reviewInputsSession(t, strings.Repeat("b", 40), strings.Repeat("a", 40))
			ready, err := orchestrator.codeReviewInputsPreparationFinished(context.Background(), session, sandbox)
			require.False(t, ready, "a failed environment inspection must not admit a waiting reviewer")
			require.Error(t, err, "environment inspection failure must remain explicit")
			require.Zero(t, provider.executions.Load(), "failed environment validation must prevent marker execution")
			if test.waiting {
				require.ErrorIs(t, err, ErrSandboxWorkspaceNotReady, "typed container loss must preserve the waiting sibling retry")
				require.ErrorIs(t, err, ErrCodeReviewPreparationTransport, "readiness must preserve the finite transport classification")
			} else {
				require.NotErrorIs(t, err, ErrSandboxWorkspaceNotReady, "permission and trust failures must not become availability retries")
			}
			evidenceErr := orchestrator.execReviewEvidenceInput(context.Background(), sandbox, "inspect captured inputs", "synthetic-authentication", nil)
			if test.waiting {
				require.ErrorIs(t, evidenceErr, ErrCodeReviewPreparationTransport, "ordinary evidence execution must retain typed validator transport loss")
			} else {
				require.NotErrorIs(t, evidenceErr, ErrCodeReviewPreparationTransport, "ordinary evidence execution must not retag trust failures")
			}
			privateInputErr := orchestrator.execReviewEvidenceInput(context.Background(), sandbox, "inspect captured inputs", "synthetic-authentication", strings.NewReader("private input"))
			if test.waiting {
				require.ErrorIs(t, privateInputErr, ErrCodeReviewPreparationTransport, "private-input evidence execution must retain typed validator transport loss")
			} else {
				require.NotErrorIs(t, privateInputErr, ErrCodeReviewPreparationTransport, "private-input evidence execution must not retag trust failures")
			}
			require.Zero(t, provider.executions.Load(), "evidence execution must stop before launching commands when environment validation fails")
			require.NotContains(t, evidenceErr.Error(), "synthetic-authentication", "transport classification must not publish private authentication")
			require.NotContains(t, privateInputErr.Error(), "synthetic-authentication", "private-input transport classification must not publish authentication")
		})
	}
}

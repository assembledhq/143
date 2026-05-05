package agent_test

import (
	"context"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
)

type testAdapterDefaultCancel struct{}

func (testAdapterDefaultCancel) Name() models.AgentType { return models.AgentTypeCodex }
func (testAdapterDefaultCancel) PreparePrompt(context.Context, *agent.AgentInput) (*agent.AgentPrompt, error) {
	return nil, nil
}
func (testAdapterDefaultCancel) Execute(context.Context, *agent.Sandbox, *agent.AgentPrompt, chan<- agent.LogEntry) (*agent.AgentResult, error) {
	return nil, nil
}

type testAdapterEscapeCancel struct{ testAdapterDefaultCancel }

func (testAdapterEscapeCancel) CancellationSpec() agent.CancellationSpec {
	return agent.CancellationSpec{Method: agent.CancellationMethodEscape}
}

type testAdapterRuntimeProfile struct{ testAdapterDefaultCancel }

func (testAdapterRuntimeProfile) RuntimeProfile() agent.AgentRuntimeProfile {
	return agent.AgentRuntimeProfile{
		Cancellation:      agent.CancellationSpec{Method: agent.CancellationMethodEscape},
		RequiresTTY:       true,
		RequiresOpenStdin: true,
	}
}

func TestResolveCancellationSpec_DefaultsToCtrlC(t *testing.T) {
	t.Parallel()

	spec := agent.ResolveCancellationSpec(testAdapterDefaultCancel{})
	require.Equal(t, agent.CancellationMethodCtrlC, spec.Method, "adapters without an override should default to Ctrl+C")
}

func TestResolveCancellationSpec_UsesExplicitOverride(t *testing.T) {
	t.Parallel()

	spec := agent.ResolveCancellationSpec(testAdapterEscapeCancel{})
	require.Equal(t, agent.CancellationMethodEscape, spec.Method, "adapters should be able to override the default cancellation method")
}

func TestResolveCancellationSpec_RuntimeProfileWins(t *testing.T) {
	t.Parallel()

	spec := agent.ResolveCancellationSpec(testAdapterRuntimeProfile{})
	require.Equal(t, agent.CancellationMethodEscape, spec.Method, "ResolveCancellationSpec should honor RuntimeProfileProvider")
}

// testAdapterBothProviders implements both RuntimeProfileProvider and the
// older CancellationSpecProvider with conflicting values. The new path must
// win so adapters can express cancellation once via their full runtime
// profile without legacy hooks shadowing it.
type testAdapterBothProviders struct{ testAdapterDefaultCancel }

func (testAdapterBothProviders) RuntimeProfile() agent.AgentRuntimeProfile {
	return agent.AgentRuntimeProfile{
		Cancellation: agent.CancellationSpec{Method: agent.CancellationMethodEscape},
	}
}

func (testAdapterBothProviders) CancellationSpec() agent.CancellationSpec {
	return agent.CancellationSpec{Method: agent.CancellationMethodCtrlC}
}

func TestResolveCancellationSpec_RuntimeProfileBeatsLegacyProvider(t *testing.T) {
	t.Parallel()

	spec := agent.ResolveCancellationSpec(testAdapterBothProviders{})
	require.Equal(t, agent.CancellationMethodEscape, spec.Method,
		"RuntimeProfileProvider must win over the legacy CancellationSpecProvider when both are implemented")
}

func TestResolveRuntimeProfile_DefaultsToCtrlCNoTTY(t *testing.T) {
	t.Parallel()

	profile := agent.ResolveRuntimeProfile(testAdapterDefaultCancel{})
	require.Equal(t, agent.CancellationMethodCtrlC, profile.Cancellation.Method, "default profile should be Ctrl+C")
	require.False(t, profile.RequiresTTY, "default profile should not require a TTY")
	require.False(t, profile.RequiresOpenStdin, "default profile should not require open stdin")
}

func TestResolveRuntimeProfile_HonorsAdapterDeclaration(t *testing.T) {
	t.Parallel()

	profile := agent.ResolveRuntimeProfile(testAdapterRuntimeProfile{})
	require.True(t, profile.RequiresTTY, "TTY-requiring adapters should propagate that bit")
	require.True(t, profile.RequiresOpenStdin)
	require.Equal(t, agent.CancellationMethodEscape, profile.Cancellation.Method)
}

func TestNewCancelRegistry_Constructs(t *testing.T) {
	t.Parallel()

	reg := agent.NewCancelRegistry(zerolog.Nop())
	require.NotNil(t, reg, "cancel registry should still construct normally with the new cancellation abstraction")
}

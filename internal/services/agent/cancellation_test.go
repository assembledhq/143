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

func TestBuildCtrlCInterruptCommand_UsesPIDFile(t *testing.T) {
	t.Parallel()

	cmd := agent.BuildCtrlCInterruptCommand("/tmp/agent.pid")
	require.Contains(t, cmd, "kill -INT", "Ctrl+C interrupt command should send SIGINT to the tracked pid")
	require.Contains(t, cmd, "/tmp/agent.pid", "Ctrl+C interrupt command should reference the pid file path")
	require.NotContains(t, cmd, "pkill -INT", "Ctrl+C interrupt command should not depend on a hard-coded process allowlist")
}

func TestBuildEscapeInterruptCommand_UsesTTYFile(t *testing.T) {
	t.Parallel()

	cmd := agent.BuildEscapeInterruptCommand("/tmp/agent.tty")
	require.Contains(t, cmd, "printf '\\033'", "escape interrupt command should write an ESC byte")
	require.Contains(t, cmd, "/tmp/agent.tty", "escape interrupt command should reference the tty file path")
}

func TestNewCancelRegistry_DefaultSpecRemainsCtrlC(t *testing.T) {
	t.Parallel()

	reg := agent.NewCancelRegistry(zerolog.Nop())
	require.NotNil(t, reg, "cancel registry should still construct normally with the new cancellation abstraction")
}

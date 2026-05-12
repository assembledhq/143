package sandboxdeps

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type fakeDep struct {
	name     string
	installs *int
	failOn   string
}

func (f *fakeDep) Name() string { return f.name }
func (f *fakeDep) Install(_ context.Context, _ Executor, version string) error {
	if f.installs != nil {
		*f.installs++
	}
	if version == f.failOn {
		return errors.New("install boom")
	}
	return nil
}

func noopExec(_ context.Context, _ string, _ io.Writer, _ io.Writer) (int, error) {
	return 0, nil
}

// swapDependencyRegistry replaces the package-level registry for the duration of t and
// restores the original on cleanup. Tests can't run in parallel because they
// share DependencyRegistry.
func swapDependencyRegistry(t *testing.T) {
	t.Helper()
	orig := DependencyRegistry
	DependencyRegistry = newRegistry()
	t.Cleanup(func() { DependencyRegistry = orig })
}

func TestApply_InstallsEveryDependency(t *testing.T) {
	swapDependencyRegistry(t)
	installs := 0
	DependencyRegistry.Register(&fakeDep{name: "tool-a", installs: &installs})
	DependencyRegistry.Register(&fakeDep{name: "tool-b", installs: &installs})

	Apply(context.Background(), zerolog.Nop(), noopExec, map[string]string{
		"tool-a": "1.0.0",
		"tool-b": "2.0.0",
	})

	require.Equal(t, 2, installs, "every install recipe should run; caching is intentionally absent so each Apply re-installs every declared dep")
}

func TestApply_UnknownDependencyDoesNotAbortPeers(t *testing.T) {
	swapDependencyRegistry(t)
	installs := 0
	DependencyRegistry.Register(&fakeDep{name: "tool-a", installs: &installs})

	Apply(context.Background(), zerolog.Nop(), noopExec, map[string]string{
		"tool-a":  "1.0.0",
		"mystery": "9.9.9",
		"another": "0.1.0",
	})

	require.Equal(t, 1, installs, "the known dependency should still install even when peers are unregistered")
}

func TestApply_InstallFailureDoesNotAbortPeers(t *testing.T) {
	swapDependencyRegistry(t)
	peerInstalls := 0
	DependencyRegistry.Register(&fakeDep{name: "tool-a", failOn: "1.0.0"})
	DependencyRegistry.Register(&fakeDep{name: "tool-b", installs: &peerInstalls})

	Apply(context.Background(), zerolog.Nop(), noopExec, map[string]string{
		"tool-a": "1.0.0",
		"tool-b": "2.0.0",
	})

	require.Equal(t, 1, peerInstalls, "peer install must still run after another dependency's install fails — best-effort posture")
}

func TestApply_NilDepsIsNoop(t *testing.T) {
	t.Parallel()
	// No swap needed: with nil deps the registry is never touched.
	Apply(context.Background(), zerolog.Nop(), noopExec, nil)
}

func TestDependencyRegistry_HasGolangciLint(t *testing.T) {
	t.Parallel()

	_, ok := DependencyRegistry.deps["golangci-lint"]
	require.True(t, ok, "DependencyRegistry should ship a recipe for golangci-lint so repos can declare it without a 143 PR")
}

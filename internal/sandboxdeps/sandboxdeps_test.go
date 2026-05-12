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

func TestApply_InstallsEveryDependency(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	installs := 0
	r.Register(&fakeDep{name: "tool-a", installs: &installs})
	r.Register(&fakeDep{name: "tool-b", installs: &installs})

	results := Apply(context.Background(), zerolog.Nop(), r, noopExec, map[string]string{
		"tool-a": "1.0.0",
		"tool-b": "2.0.0",
	})

	require.Len(t, results, 2, "Apply should record one result per declared dependency")
	for _, res := range results {
		require.Equal(t, "installed", res.Status, "every known dependency should be installed unconditionally now that caching is gone")
	}
	require.Equal(t, 2, installs, "every install recipe should run; we are intentionally not short-circuiting on already-present")
}

func TestApply_UnknownDependencyDoesNotAbort(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	installs := 0
	r.Register(&fakeDep{name: "tool-a", installs: &installs})

	results := Apply(context.Background(), zerolog.Nop(), r, noopExec, map[string]string{
		"tool-a":  "1.0.0",
		"mystery": "9.9.9",
		"another": "0.1.0",
	})

	require.Len(t, results, 3, "Apply should record a result for every declared entry, including unknown ones")
	var sawUnknown, sawInstalled int
	for _, res := range results {
		switch res.Status {
		case "unknown":
			sawUnknown++
			require.Error(t, res.Err, "unknown dependencies should surface an error in the result so callers can log them")
		case "installed":
			sawInstalled++
		}
	}
	require.Equal(t, 2, sawUnknown, "both unregistered names should be reported as unknown")
	require.Equal(t, 1, sawInstalled, "the known dependency should still install despite unknown peers")
}

func TestApply_InstallFailureIsReportedButDoesNotAbortPeers(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	r.Register(&fakeDep{name: "tool-a", failOn: "1.0.0"})
	r.Register(&fakeDep{name: "tool-b"})

	results := Apply(context.Background(), zerolog.Nop(), r, noopExec, map[string]string{
		"tool-a": "1.0.0",
		"tool-b": "2.0.0",
	})

	statuses := map[string]string{}
	for _, res := range results {
		statuses[res.Name] = res.Status
	}
	require.Equal(t, "failed", statuses["tool-a"], "tool-a was configured to fail and should report failed")
	require.Equal(t, "installed", statuses["tool-b"], "peer install must still run after another dependency fails")
}

func TestApply_NilDepsReturnsNil(t *testing.T) {
	t.Parallel()

	results := Apply(context.Background(), zerolog.Nop(), NewRegistry(), noopExec, nil)
	require.Nil(t, results, "Apply should no-op cleanly when no dependencies are declared")
}

func TestDefaultRegistry_HasGolangciLint(t *testing.T) {
	t.Parallel()

	_, ok := Default.Lookup("golangci-lint")
	require.True(t, ok, "the default registry should ship a recipe for golangci-lint so repos can declare it without a 143 PR")
}

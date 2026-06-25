package preview

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/services/agent"
)

// scriptedResponse is one canned reply for a sandbox exec matched by substring.
type scriptedResponse struct {
	exitCode int
	err      error
	stderr   string
}

// scriptedDependencyCacheExec wraps the happy-path mock executor and lets a test
// inject a queue of failures for commands matching a substring. Unmatched calls
// (and calls past the end of a queue) fall through to the real mock behavior, so
// a retry that exhausts the failure queue exercises the success path.
type scriptedDependencyCacheExec struct {
	*dependencyCacheExec
	mu    sync.Mutex
	rules map[string][]scriptedResponse
	hits  map[string]int
}

func newScriptedExec(base *dependencyCacheExec) *scriptedDependencyCacheExec {
	return &scriptedDependencyCacheExec{
		dependencyCacheExec: base,
		rules:               map[string][]scriptedResponse{},
		hits:                map[string]int{},
	}
}

func (e *scriptedDependencyCacheExec) script(substr string, resp ...scriptedResponse) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.rules[substr] = resp
}

func (e *scriptedDependencyCacheExec) Exec(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
	e.mu.Lock()
	for substr, resps := range e.rules {
		if !strings.Contains(cmd, substr) {
			continue
		}
		i := e.hits[substr]
		if i >= len(resps) {
			continue
		}
		e.hits[substr]++
		r := resps[i]
		e.mu.Unlock()
		e.dependencyCacheExec.mu.Lock()
		e.dependencyCacheExec.execCalls = append(e.dependencyCacheExec.execCalls, cmd)
		e.dependencyCacheExec.mu.Unlock()
		if r.stderr != "" && stderr != nil {
			_, _ = stderr.Write([]byte(r.stderr))
		}
		return r.exitCode, r.err
	}
	e.mu.Unlock()
	return e.dependencyCacheExec.Exec(ctx, sb, cmd, stdout, stderr)
}

func newReliabilityTestCache(t *testing.T, exec SnapshotExecutor) *SharedDependencyCache {
	t.Helper()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	t.Cleanup(mock.Close)
	cache, err := NewDependencyCache(DependencyCacheConfig{
		Store:      db.NewPreviewStore(mock),
		Executor:   exec,
		BlobStore:  newMemorySnapshotStore(),
		Logger:     zerolog.Nop(),
		Prefix:     "deps",
		StagingDir: t.TempDir(),
	})
	require.NoError(t, err, "dependency cache should initialize")
	return cache
}

func countCalls(calls []string, substr string) int {
	n := 0
	for _, c := range calls {
		if strings.Contains(c, substr) {
			n++
		}
	}
	return n
}

// A SIGKILL/OOM (exit 137) on the archive is transient, so the save retries once
// and succeeds on the second attempt rather than degrading to a cold launch.
func TestStageSandboxArchive_RetriesTransientArchiveFailure(t *testing.T) {
	t.Parallel()
	base := &dependencyCacheExec{payload: makeDependencyCacheTarGz(t, map[string]string{"node_modules/.bin/next": "next"})}
	exec := newScriptedExec(base)
	exec.script("tar cf -", scriptedResponse{exitCode: 137, stderr: "Killed"})
	cache := newReliabilityTestCache(t, exec)

	blob, err := cache.stageSandboxArchive(context.Background(), &agent.Sandbox{WorkDir: "/workspace/repo"}, "cd '/workspace/repo' && tar cf - -- 'node_modules'")
	require.NoError(t, err, "transient OOM on the archive should be retried, not surfaced")
	require.NotNil(t, blob, "a successful retry should return a staged blob")
	defer blob.cleanup()
	require.Equal(t, 2, countCalls(exec.calls(), "tar cf -"), "archive should run twice: one transient failure then a success")
}

// A deterministic archive failure (exit 2) is not retried and surfaces the
// sandbox stderr instead of a bare "exited 2" with no diagnostic.
func TestStageSandboxArchive_SurfacesStderrAndDoesNotRetryDeterministicFailure(t *testing.T) {
	t.Parallel()
	exec := newScriptedExec(&dependencyCacheExec{})
	exec.script("tar cf -",
		scriptedResponse{exitCode: 2, stderr: "tar: node_modules: Cannot stat: No such file or directory"},
		scriptedResponse{exitCode: 2, stderr: "tar: node_modules: Cannot stat: No such file or directory"},
	)
	cache := newReliabilityTestCache(t, exec)

	blob, err := cache.stageSandboxArchive(context.Background(), &agent.Sandbox{WorkDir: "/workspace/repo"}, "cd '/workspace/repo' && tar cf - -- 'node_modules'")
	require.Error(t, err, "a deterministic tar failure should surface")
	require.Nil(t, blob)
	require.Contains(t, err.Error(), "archive stream exited 2")
	require.Contains(t, err.Error(), "Cannot stat", "the captured tar stderr should be in the error")
	require.NotContains(t, err.Error(), "%!w(<nil>)", "a nil wrapped error must not leak fmt noise")
	require.Equal(t, 1, countCalls(exec.calls(), "tar cf -"), "exit 2 is deterministic and must not be retried")
}

// The save probe currently discards stderr and prints a bare "exited 128"; the
// fix captures stderr (e.g. an OOM message) so the failure is diagnosable.
func TestSavePathCache_ProbeFailureSurfacesStderr(t *testing.T) {
	t.Parallel()
	exec := newScriptedExec(&dependencyCacheExec{})
	// 128 is transient, so both attempts must fail for the error to surface.
	exec.script("test -e",
		scriptedResponse{exitCode: 128, stderr: "bash: fork: Cannot allocate memory"},
		scriptedResponse{exitCode: 128, stderr: "bash: fork: Cannot allocate memory"},
	)
	cache := newReliabilityTestCache(t, exec)

	_, err := cache.Save(context.Background(), &agent.Sandbox{WorkDir: "/workspace/repo"}, strings.Repeat("a", 64), []string{"node_modules"}, DependencyCacheMetadata{
		OrgID:  uuid.New(),
		RepoID: uuid.New(),
	})
	require.Error(t, err, "a probe failure should abort the save")
	require.Contains(t, err.Error(), "probe effective paths exited 128")
	require.Contains(t, err.Error(), "Cannot allocate memory", "the captured probe stderr should be in the error")
	require.NotContains(t, err.Error(), "%!w(<nil>)")
}

func TestDependencyCacheExitTransient(t *testing.T) {
	t.Parallel()
	transient := []int{-1, 128, 137, 143}
	deterministic := []int{0, 1, 2, 127}
	for _, code := range transient {
		require.Truef(t, dependencyCacheExitTransient(code), "exit %d should be transient", code)
	}
	for _, code := range deterministic {
		require.Falsef(t, dependencyCacheExitTransient(code), "exit %d should be deterministic", code)
	}
}

func TestDependencyCacheExecError_NoNilWrapNoise(t *testing.T) {
	t.Parallel()
	withErr := dependencyCacheExecError("archive stream", 137, errors.New("attach exec"), "Killed")
	require.Contains(t, withErr.Error(), "archive stream exited 137")
	require.Contains(t, withErr.Error(), "attach exec")
	require.Contains(t, withErr.Error(), "Killed")

	noErr := dependencyCacheExecError("probe effective paths", 128, nil, "Cannot allocate memory")
	require.Contains(t, noErr.Error(), "probe effective paths exited 128")
	require.Contains(t, noErr.Error(), "Cannot allocate memory")
	require.NotContains(t, noErr.Error(), "%!w(<nil>)", "nil wrapped error must not produce fmt noise")
}

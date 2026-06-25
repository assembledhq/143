package agent_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/services/agent"
	"github.com/assembledhq/143/internal/testutil"
)

// brokenPipeRestoreErr models the docker-socket write failure that the real
// DockerProvider.Restore returns when the daemon drops the exec mid-stream.
func brokenPipeRestoreErr() error {
	return fmt.Errorf("write snapshot to container: %w", &net.OpError{Op: "write", Net: "unix", Err: syscall.EPIPE})
}

type fakeSnapshotStore struct {
	loadErr error
	payload []byte
}

func (f *fakeSnapshotStore) Save(ctx context.Context, key string, reader io.Reader) error {
	return nil
}

func (f *fakeSnapshotStore) Load(ctx context.Context, key string, writer io.Writer) error {
	if f.loadErr != nil {
		return f.loadErr
	}
	_, _ = writer.Write(f.payload)
	return nil
}

func (f *fakeSnapshotStore) Delete(ctx context.Context, key string) error { return nil }

func TestHydrateSandboxFromSnapshot_NilStore(t *testing.T) {
	t.Parallel()
	provider := testutil.NewMockSandboxProvider()
	_, err := agent.HydrateSandboxFromSnapshot(context.Background(), provider, nil, "key", agent.SandboxConfig{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "snapshot store is nil")
}

func TestHydrateSandboxFromSnapshot_EmptyKey(t *testing.T) {
	t.Parallel()
	provider := testutil.NewMockSandboxProvider()
	_, err := agent.HydrateSandboxFromSnapshot(context.Background(), provider, &fakeSnapshotStore{}, "", agent.SandboxConfig{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "snapshot key is empty")
}

func TestHydrateSandboxFromSnapshot_CreateFails(t *testing.T) {
	t.Parallel()
	provider := testutil.NewMockSandboxProvider()
	provider.CreateFn = func(context.Context, agent.SandboxConfig) (*agent.Sandbox, error) {
		return nil, errors.New("boom")
	}
	_, err := agent.HydrateSandboxFromSnapshot(context.Background(), provider, &fakeSnapshotStore{}, "key", agent.SandboxConfig{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "create")
}

func TestHydrateSandboxFromSnapshot_RestoreFailsDestroysSandbox(t *testing.T) {
	t.Parallel()
	provider := testutil.NewMockSandboxProvider()
	provider.RestoreFn = func(context.Context, *agent.Sandbox, io.Reader) error {
		return errors.New("restore boom")
	}
	_, err := agent.HydrateSandboxFromSnapshot(context.Background(), provider, &fakeSnapshotStore{payload: []byte("hello")}, "key", agent.SandboxConfig{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "restore")
	require.Equal(t, 1, provider.GetDestroyCalls())
}

func TestHydrateSandboxFromSnapshot_LoadFailsDestroysSandbox(t *testing.T) {
	t.Parallel()
	provider := testutil.NewMockSandboxProvider()
	// Drain reader so the Load goroutine's Write does not block indefinitely.
	provider.RestoreFn = func(ctx context.Context, sb *agent.Sandbox, r io.Reader) error {
		_, _ = io.Copy(io.Discard, r)
		return nil
	}
	store := &fakeSnapshotStore{loadErr: errors.New("load boom")}
	_, err := agent.HydrateSandboxFromSnapshot(context.Background(), provider, store, "key", agent.SandboxConfig{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "load snapshot")
	require.Equal(t, 1, provider.GetDestroyCalls())
}

func TestHydrateSandboxFromSnapshot_RetriesTransientRestoreFailure(t *testing.T) {
	t.Parallel()

	var restoreCalls int32
	provider := testutil.NewMockSandboxProvider()
	provider.RestoreFn = func(ctx context.Context, sb *agent.Sandbox, r io.Reader) error {
		// Always drain so the Load goroutine's Write never blocks.
		_, _ = io.Copy(io.Discard, r)
		// Fail the first two attempts with a transient broken pipe, then succeed.
		if atomic.AddInt32(&restoreCalls, 1) <= 2 {
			return brokenPipeRestoreErr()
		}
		return nil
	}

	sb, err := agent.HydrateSandboxFromSnapshot(context.Background(), provider, &fakeSnapshotStore{payload: []byte("archive-bytes")}, "key", agent.SandboxConfig{})
	require.NoError(t, err)
	require.NotNil(t, sb)
	require.Equal(t, int32(3), atomic.LoadInt32(&restoreCalls), "should retry until the third attempt succeeds")
	require.Equal(t, 2, provider.GetDestroyCalls(), "each failed attempt destroys its half-built sandbox")
}

func TestHydrateSandboxFromSnapshot_ExhaustsRetriesOnPersistentTransientFailure(t *testing.T) {
	t.Parallel()

	var restoreCalls int32
	provider := testutil.NewMockSandboxProvider()
	provider.RestoreFn = func(ctx context.Context, sb *agent.Sandbox, r io.Reader) error {
		_, _ = io.Copy(io.Discard, r)
		atomic.AddInt32(&restoreCalls, 1)
		return brokenPipeRestoreErr()
	}

	_, err := agent.HydrateSandboxFromSnapshot(context.Background(), provider, &fakeSnapshotStore{payload: []byte("archive-bytes")}, "key", agent.SandboxConfig{})
	require.Error(t, err)
	require.ErrorIs(t, err, agent.ErrSnapshotRestore)
	require.Equal(t, int32(3), atomic.LoadInt32(&restoreCalls), "bounded at hydrateMaxAttempts")
	require.Equal(t, 3, provider.GetDestroyCalls())
}

func TestHydrateSandboxFromSnapshot_DoesNotRetryDeterministicFailure(t *testing.T) {
	t.Parallel()

	var restoreCalls int32
	provider := testutil.NewMockSandboxProvider()
	provider.RestoreFn = func(ctx context.Context, sb *agent.Sandbox, r io.Reader) error {
		_, _ = io.Copy(io.Discard, r)
		atomic.AddInt32(&restoreCalls, 1)
		// A non-zero tar exit (corrupt archive / full disk) recurs on retry.
		return errors.New("restore tar exited with code 2: tar: Cannot write: No space left on device")
	}

	_, err := agent.HydrateSandboxFromSnapshot(context.Background(), provider, &fakeSnapshotStore{payload: []byte("archive-bytes")}, "key", agent.SandboxConfig{})
	require.Error(t, err)
	require.Equal(t, int32(1), atomic.LoadInt32(&restoreCalls), "deterministic failures must not be retried")
	require.Equal(t, 1, provider.GetDestroyCalls())
}

func TestHydrateSandboxFromSnapshot_Success(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var got []byte
	provider := testutil.NewMockSandboxProvider()
	provider.RestoreFn = func(ctx context.Context, sb *agent.Sandbox, r io.Reader) error {
		data, err := io.ReadAll(r)
		if err != nil {
			return err
		}
		mu.Lock()
		got = data
		mu.Unlock()
		return nil
	}
	sb, err := agent.HydrateSandboxFromSnapshot(context.Background(), provider, &fakeSnapshotStore{payload: []byte("archive-bytes")}, "key", agent.SandboxConfig{})
	require.NoError(t, err)
	require.NotNil(t, sb)
	require.Equal(t, []byte("archive-bytes"), got)
	require.Equal(t, 0, provider.GetDestroyCalls())
}

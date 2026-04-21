package agent_test

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/services/agent"
	"github.com/assembledhq/143/internal/testutil"
)

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

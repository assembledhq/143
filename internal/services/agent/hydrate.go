package agent

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/assembledhq/143/internal/services/storage"
)

// HydrateSandboxFromSnapshot creates a new sandbox container and restores a
// previously captured snapshot into it. It is the shared "bring a dormant
// session's workspace back to life" primitive — used by both the orchestrator
// (when continuing a turn whose snapshot is available) and the preview
// handler (when starting a preview on a session whose turn has already
// completed and the container was torn down).
//
// The io.Pipe + goroutine dance is what threads the snapshot blob from
// storage.SnapshotStore.Load directly into provider.Restore without
// materializing the whole archive in memory. If Restore fails we close the
// reader to unblock the Load goroutine; the WaitGroup ensures we don't return
// while the goroutine is still writing.
//
// On any failure, the partially-created sandbox is destroyed best-effort so
// callers don't have to worry about cleanup — they just get an error back.
// On success, callers own the returned Sandbox and are responsible for
// Destroy (typically through the turn/preview refcount).
func HydrateSandboxFromSnapshot(
	ctx context.Context,
	provider SandboxProvider,
	snapshots storage.SnapshotStore,
	snapshotKey string,
	cfg SandboxConfig,
) (*Sandbox, error) {
	if snapshots == nil {
		return nil, fmt.Errorf("hydrate sandbox: snapshot store is nil")
	}
	if snapshotKey == "" {
		return nil, fmt.Errorf("hydrate sandbox: snapshot key is empty")
	}

	sandbox, err := provider.Create(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("hydrate sandbox: create: %w", err)
	}

	reader, writer := io.Pipe()
	var loadErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		loadErr = snapshots.Load(ctx, snapshotKey, writer)
		_ = writer.Close() // intentionally ignored; real error is in loadErr
	}()

	if restoreErr := provider.Restore(ctx, sandbox, reader); restoreErr != nil {
		_ = reader.Close() // intentionally ignored; we already have restoreErr
		wg.Wait()
		// Tear down the partially-created sandbox so we don't leak a
		// container holding no useful state.
		_ = provider.Destroy(context.Background(), sandbox)
		return nil, fmt.Errorf("hydrate sandbox: restore: %w", restoreErr)
	}
	wg.Wait()
	if loadErr != nil {
		_ = provider.Destroy(context.Background(), sandbox)
		return nil, fmt.Errorf("hydrate sandbox: load snapshot: %w", loadErr)
	}

	return sandbox, nil
}

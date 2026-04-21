package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/assembledhq/143/internal/services/storage"
)

// ErrSnapshotMissing is returned by HydrateSandboxFromSnapshot when the
// snapshot blob referenced by snapshotKey is no longer present in the
// snapshot store (e.g. it was reaped, manually deleted, or never uploaded).
// Callers should treat this as "expired" and ask the user to start a new
// turn — a retry will produce the same result. Distinct from the generic
// hydrate failure so the HTTP layer can return 410 Gone instead of 500.
var ErrSnapshotMissing = errors.New("snapshot missing")

// ErrSnapshotRestore is returned when the snapshot blob exists but the
// provider failed to restore it into the new container (tar corruption,
// provider-side error, context cancellation mid-restore). Distinct from
// ErrSnapshotMissing so the HTTP layer can keep this as a 500 — a retry
// might succeed, or the snapshot archive itself may be bad and need ops
// attention.
var ErrSnapshotRestore = errors.New("snapshot restore failed")

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
		// If the restore side failed *because* the load side fed it nothing
		// (snapshot missing), prefer the missing-snapshot sentinel — it's
		// more actionable upstream than a generic restore failure. Otherwise
		// tag as a restore failure so the HTTP layer keeps this as 500.
		if errors.Is(loadErr, storage.ErrSnapshotNotFound) {
			return nil, fmt.Errorf("hydrate sandbox: %w (restore saw: %v)", ErrSnapshotMissing, restoreErr)
		}
		return nil, fmt.Errorf("hydrate sandbox: %w: %v", ErrSnapshotRestore, restoreErr)
	}
	wg.Wait()
	if loadErr != nil {
		_ = provider.Destroy(context.Background(), sandbox)
		if errors.Is(loadErr, storage.ErrSnapshotNotFound) {
			return nil, fmt.Errorf("hydrate sandbox: %w: %v", ErrSnapshotMissing, loadErr)
		}
		return nil, fmt.Errorf("hydrate sandbox: load snapshot: %w", loadErr)
	}

	return sandbox, nil
}

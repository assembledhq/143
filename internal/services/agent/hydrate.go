package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/assembledhq/143/internal/services/storage"
)

const (
	// hydrateMaxAttempts bounds how many times HydrateSandboxFromSnapshot
	// rebuilds the sandbox and re-streams the snapshot after a *transient*
	// restore failure — a broken pipe or reset on the docker socket, which in
	// practice means a momentarily overloaded or restarting daemon (the exact
	// shape of the incident this guards against). Deterministic failures
	// (missing snapshot, corrupt archive, disk full) are never retried.
	hydrateMaxAttempts = 3
)

// hydrateRetryBackoff is multiplied by the attempt number for a simple linear
// backoff between retries, giving a struggling daemon a moment to recover
// before we hit it with another multi-hundred-MB stream. A var so tests can
// shrink it.
var hydrateRetryBackoff = 50 * time.Millisecond

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

	for attempt := 1; ; attempt++ {
		sandbox, err := hydrateSandboxOnce(ctx, provider, snapshots, snapshotKey, cfg)
		if err == nil {
			return sandbox, nil
		}
		// Streaming a multi-hundred-MB snapshot from storage into a fresh
		// container can lose the docker-socket connection mid-write when the
		// daemon is overloaded or restarting. Each failed attempt has already
		// destroyed its half-built sandbox, so a retry is a clean fresh start
		// (new container + new download). Deterministic failures — missing or
		// corrupt snapshot, full disk — are not retried; they would recur.
		if attempt >= hydrateMaxAttempts || !isRetryableRestoreError(err) {
			return nil, err
		}
		if waitErr := waitForHydrateRetry(ctx, attempt); waitErr != nil {
			// Context died while backing off; report it with the cause that
			// triggered the retry so callers see why we were retrying at all.
			return nil, fmt.Errorf("hydrate sandbox: %w (last attempt: %v)", waitErr, err)
		}
	}
}

// hydrateSandboxOnce performs a single create+load+restore attempt. On any
// failure the partially-created sandbox is destroyed best-effort, so the caller
// can safely retry with a fresh call.
func hydrateSandboxOnce(
	ctx context.Context,
	provider SandboxProvider,
	snapshots storage.SnapshotStore,
	snapshotKey string,
	cfg SandboxConfig,
) (*Sandbox, error) {
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
		// Wrap restoreErr with %w (not %v) so the underlying cause — e.g. a
		// syscall.EPIPE behind "broken pipe" — stays inspectable via errors.Is
		// for the retry classifier below.
		return nil, fmt.Errorf("hydrate sandbox: %w: %w", ErrSnapshotRestore, restoreErr)
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

// isRetryableRestoreError reports whether a hydrate failure looks like a
// transient docker-socket hiccup that a fresh attempt could clear, as opposed
// to a deterministic failure that would recur identically.
//
// Retryable: connection-level write failures against the daemon (broken pipe,
// reset, closed connection, truncated stream) — typically an overloaded or
// restarting daemon dropping the exec mid-stream.
//
// Not retryable: a missing snapshot (the blob is gone), or a non-zero tar exit
// (corrupt archive, or the sandbox disk filled up) — re-streaming the same
// bytes into a new container produces the same outcome.
func isRetryableRestoreError(err error) bool {
	if err == nil {
		return false
	}
	// Missing snapshots are deterministic — only genuine restore failures are
	// candidates for a retry.
	if errors.Is(err, ErrSnapshotMissing) || errors.Is(err, storage.ErrSnapshotNotFound) {
		return false
	}
	if !errors.Is(err, ErrSnapshotRestore) {
		return false
	}
	// A non-zero tar exit is deterministic: a corrupt archive fails to inflate
	// every time, and a full disk stays full. Don't retry either.
	lower := strings.ToLower(err.Error())
	if strings.Contains(lower, "restore tar exited with code") ||
		strings.Contains(lower, "no space left on device") {
		return false
	}
	// Prefer typed matching on the wrapped cause; fall back to string markers
	// for providers/transports that surface a plain error.
	if errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.ErrClosedPipe) {
		return true
	}
	for _, marker := range []string{
		"broken pipe",
		"connection reset",
		"use of closed network connection",
		"unexpected eof",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// waitForHydrateRetry sleeps a linear backoff before the next hydrate attempt,
// returning early (with ctx.Err()) if the context is cancelled mid-wait.
func waitForHydrateRetry(ctx context.Context, attempt int) error {
	timer := time.NewTimer(time.Duration(attempt) * hydrateRetryBackoff)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Package storage provides interfaces and implementations for persisting
// sandbox snapshots to object storage.
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/google/uuid"
)

// ErrSnapshotNotFound is returned by Load when the requested key does not
// exist in the underlying store. Callers that need to distinguish "snapshot
// never existed / was deleted" from a transport error (network, permissions,
// bucket outage) should use errors.Is against this sentinel.
var ErrSnapshotNotFound = errors.New("snapshot not found")

// SnapshotStore abstracts snapshot persistence for sandbox state.
// Snapshots contain the workspace directory and agent-specific state
// (e.g., ~/.claude/, ~/.codex/, ~/.gemini/) compressed as a tar archive.
type SnapshotStore interface {
	// Save uploads a snapshot from the provided reader.
	Save(ctx context.Context, key string, reader io.Reader) error

	// Load downloads a snapshot and writes it to the provided writer.
	// Returns an error wrapping ErrSnapshotNotFound when the key does not
	// exist; all other errors are transport failures.
	Load(ctx context.Context, key string, writer io.Writer) error

	// Delete removes a snapshot. Safe to call if the key does not exist.
	Delete(ctx context.Context, key string) error
}

// SessionSnapshotClearer lets callers of CleanupSessionSnapshot drop the
// snapshot_key reference from the sessions row. Declared here to avoid
// importing the db package from storage.
type SessionSnapshotClearer interface {
	ClearSnapshotKey(ctx context.Context, orgID, sessionID uuid.UUID) error
}

// CleanupSessionSnapshot removes a session's snapshot file from the store and
// clears the pointer from the sessions row. Idempotent: a nil or empty key is
// a no-op, and a missing underlying file is treated as success so the row
// still gets updated.
func CleanupSessionSnapshot(
	ctx context.Context,
	store SnapshotStore,
	clearer SessionSnapshotClearer,
	orgID, sessionID uuid.UUID,
	snapshotKey *string,
) error {
	if snapshotKey == nil || *snapshotKey == "" {
		return nil
	}
	if err := store.Delete(ctx, *snapshotKey); err != nil && !errors.Is(err, ErrSnapshotNotFound) {
		return fmt.Errorf("delete snapshot %s: %w", *snapshotKey, err)
	}
	return clearer.ClearSnapshotKey(ctx, orgID, sessionID)
}

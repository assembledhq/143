// Package storage provides interfaces and implementations for persisting
// sandbox snapshots to object storage.
package storage

import (
	"context"
	"errors"
	"io"
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

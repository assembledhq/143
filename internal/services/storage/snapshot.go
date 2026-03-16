// Package storage provides interfaces and implementations for persisting
// sandbox snapshots to object storage.
package storage

import (
	"context"
	"io"
)

// SnapshotStore abstracts snapshot persistence for sandbox state.
// Snapshots contain the workspace directory and agent-specific state
// (e.g., ~/.claude/, ~/.codex/, ~/.gemini/) compressed as a tar archive.
type SnapshotStore interface {
	// Save uploads a snapshot from the provided reader.
	Save(ctx context.Context, key string, reader io.Reader) error

	// Load downloads a snapshot and writes it to the provided writer.
	Load(ctx context.Context, key string, writer io.Writer) error

	// Delete removes a snapshot. Safe to call if the key does not exist.
	Delete(ctx context.Context, key string) error
}

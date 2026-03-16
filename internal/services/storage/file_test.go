package storage

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFileSnapshotStore_LoadNotFound(t *testing.T) {
	t.Parallel()

	store := NewFileSnapshotStore(t.TempDir())
	var buf bytes.Buffer
	err := store.Load(context.Background(), "nonexistent/key", &buf)
	require.Error(t, err, "Load should fail for a missing snapshot")
	require.Contains(t, err.Error(), "open snapshot file")
}

func TestFileSnapshotStore_DeleteNotFound(t *testing.T) {
	t.Parallel()

	store := NewFileSnapshotStore(t.TempDir())
	err := store.Delete(context.Background(), "nonexistent/key")
	require.NoError(t, err, "Delete should succeed for a missing snapshot")
}

func TestFileSnapshotStore_SaveMkdirFails(t *testing.T) {
	t.Parallel()

	// Use a file as base dir so MkdirAll fails.
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "not-a-dir")
	require.NoError(t, os.WriteFile(filePath, []byte("x"), 0o600))

	store := NewFileSnapshotStore(filePath)
	err := store.Save(context.Background(), "sub/key", bytes.NewReader([]byte("data")))
	require.Error(t, err, "Save should fail when MkdirAll fails")
	require.Contains(t, err.Error(), "create snapshot dir")
}

func TestFileSnapshotStore_SaveLoadDelete(t *testing.T) {
	t.Parallel()

	baseDir := t.TempDir()
	store := NewFileSnapshotStore(baseDir)
	ctx := context.Background()
	key := filepath.Join("org-1", "session-1", "workspace.tar.gz")
	payload := []byte("snapshot-bytes")

	err := store.Save(ctx, key, bytes.NewReader(payload))
	require.NoError(t, err, "Save should persist the snapshot payload")

	var loaded bytes.Buffer
	err = store.Load(ctx, key, &loaded)
	require.NoError(t, err, "Load should read a saved snapshot payload")
	require.Equal(t, payload, loaded.Bytes(), "Load should return the exact saved payload")

	err = store.Delete(ctx, key)
	require.NoError(t, err, "Delete should remove the snapshot payload")

	_, statErr := os.Stat(filepath.Join(baseDir, key))
	require.Error(t, statErr, "deleted snapshot should no longer exist on disk")
	require.True(t, os.IsNotExist(statErr), "deleted snapshot should be removed from disk")
}

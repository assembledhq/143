package storage

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

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

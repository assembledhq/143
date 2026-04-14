package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestUploadReaper_ReapsOldFiles(t *testing.T) {
	t.Parallel()

	baseDir := t.TempDir()
	store := NewFileUploadStore(baseDir, "/uploads")

	// Create an old file (backdate mod time).
	oldDir := filepath.Join(baseDir, "org-1", "2025-01")
	require.NoError(t, os.MkdirAll(oldDir, 0o750))
	oldFile := filepath.Join(oldDir, "old-file.png")
	require.NoError(t, os.WriteFile(oldFile, []byte("old"), 0o600))
	oldTime := time.Now().Add(-48 * time.Hour)
	require.NoError(t, os.Chtimes(oldFile, oldTime, oldTime))

	// Create a recent file.
	newDir := filepath.Join(baseDir, "org-1", "2026-03")
	require.NoError(t, os.MkdirAll(newDir, 0o750))
	newFile := filepath.Join(newDir, "new-file.png")
	require.NoError(t, os.WriteFile(newFile, []byte("new"), 0o600))

	logger := zerolog.Nop()
	reaper := NewUploadReaper(store, 24*time.Hour, time.Minute, logger)

	// Run reap directly.
	reaper.reapFiles(context.Background(), store)

	// Old file should be deleted.
	_, err := os.Stat(oldFile)
	require.True(t, os.IsNotExist(err), "old file should be deleted")

	// New file should still exist.
	_, err = os.Stat(newFile)
	require.NoError(t, err, "new file should still exist")
}

func TestUploadReaper_CleansEmptyDirs(t *testing.T) {
	t.Parallel()

	baseDir := t.TempDir()
	store := NewFileUploadStore(baseDir, "/uploads")

	// Create a nested directory structure with an old file.
	dir := filepath.Join(baseDir, "org-1", "2025-01")
	require.NoError(t, os.MkdirAll(dir, 0o750))
	f := filepath.Join(dir, "file.png")
	require.NoError(t, os.WriteFile(f, []byte("x"), 0o600))
	oldTime := time.Now().Add(-48 * time.Hour)
	require.NoError(t, os.Chtimes(f, oldTime, oldTime))

	logger := zerolog.Nop()
	reaper := NewUploadReaper(store, 24*time.Hour, time.Minute, logger)

	reaper.reapFiles(context.Background(), store)

	// Both the file and empty parent dirs should be cleaned up.
	_, err := os.Stat(dir)
	require.True(t, os.IsNotExist(err), "empty directory should be cleaned up")
}

func TestUploadReaper_S3ModeNoop(t *testing.T) {
	t.Parallel()

	// S3UploadStore should cause the reaper to exit immediately.
	s3Store := NewS3UploadStore(nil, "bucket", "prefix")
	logger := zerolog.Nop()
	reaper := NewUploadReaper(s3Store, 24*time.Hour, time.Minute, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Run should return immediately for S3 mode (not block).
	done := make(chan struct{})
	go func() {
		reaper.Run(ctx)
		close(done)
	}()

	select {
	case <-done:
		// Expected: Run exits immediately for S3 mode.
	case <-time.After(2 * time.Second):
		t.Fatal("UploadReaper.Run should return immediately in S3 mode")
	}
}

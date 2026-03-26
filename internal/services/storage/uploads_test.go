package storage

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFileUploadStore_SaveAndURL(t *testing.T) {
	t.Parallel()

	baseDir := t.TempDir()
	store := NewFileUploadStore(baseDir, "/api/v1/uploads/files")
	ctx := context.Background()
	key := "org-1/2026-03/test-file.png"
	payload := []byte("fake-image-data")

	url, err := store.Save(ctx, key, bytes.NewReader(payload), "image/png")
	require.NoError(t, err, "Save should succeed")
	require.Equal(t, "/api/v1/uploads/files/"+key, url, "Save should return the correct URL")

	// Verify file on disk.
	data, err := os.ReadFile(filepath.Join(baseDir, key))
	require.NoError(t, err, "file should exist on disk")
	require.Equal(t, payload, data, "file content should match")
}

func TestFileUploadStore_URL(t *testing.T) {
	t.Parallel()

	store := NewFileUploadStore("/tmp/uploads", "/api/v1/uploads/files")
	require.Equal(t, "/api/v1/uploads/files/org-1/file.png", store.URL("org-1/file.png"))
}

func TestFileUploadStore_SaveMkdirFails(t *testing.T) {
	t.Parallel()

	// Use a file as base dir so MkdirAll fails.
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "not-a-dir")
	require.NoError(t, os.WriteFile(filePath, []byte("x"), 0o600))

	store := NewFileUploadStore(filePath, "/uploads")
	_, err := store.Save(context.Background(), "sub/key.png", bytes.NewReader([]byte("data")), "image/png")
	require.Error(t, err, "Save should fail when MkdirAll fails")
	require.Contains(t, err.Error(), "create upload dir")
}

func TestFileUploadStore_TrailingSlashTrimmed(t *testing.T) {
	t.Parallel()

	store := NewFileUploadStore("/tmp/uploads", "/api/v1/uploads/files/")
	require.Equal(t, "/api/v1/uploads/files/key.png", store.URL("key.png"),
		"trailing slash on baseURL should be trimmed")
}

func TestS3UploadStore_URL(t *testing.T) {
	t.Parallel()

	store := NewS3UploadStore(nil, "mybucket", "uploads", "https://mybucket.s3.amazonaws.com")
	require.Equal(t, "https://mybucket.s3.amazonaws.com/uploads/org-1/file.png", store.URL("org-1/file.png"))
}

func TestS3UploadStore_URL_NoPrefix(t *testing.T) {
	t.Parallel()

	store := NewS3UploadStore(nil, "mybucket", "", "https://mybucket.s3.amazonaws.com")
	require.Equal(t, "https://mybucket.s3.amazonaws.com/org-1/file.png", store.URL("org-1/file.png"))
}

func TestS3UploadStore_EndpointTrailingSlashTrimmed(t *testing.T) {
	t.Parallel()

	store := NewS3UploadStore(nil, "mybucket", "uploads", "https://mybucket.s3.amazonaws.com/")
	require.Equal(t, "https://mybucket.s3.amazonaws.com/uploads/file.png", store.URL("file.png"),
		"trailing slash on endpoint should be trimmed")
}

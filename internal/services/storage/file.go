package storage

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// FileSnapshotStore persists snapshots on the local filesystem.
// This is the default runtime implementation for single-node deployments.
type FileSnapshotStore struct {
	baseDir string
}

func NewFileSnapshotStore(baseDir string) *FileSnapshotStore {
	return &FileSnapshotStore{baseDir: baseDir}
}

func (s *FileSnapshotStore) Save(ctx context.Context, key string, reader io.Reader) error {
	path := s.fullPath(key)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create snapshot dir: %w", err)
	}

	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create snapshot file: %w", err)
	}
	defer file.Close()

	if _, err := io.Copy(file, reader); err != nil {
		return fmt.Errorf("write snapshot file: %w", err)
	}
	return ctx.Err()
}

func (s *FileSnapshotStore) Load(ctx context.Context, key string, writer io.Writer) error {
	file, err := os.Open(s.fullPath(key))
	if err != nil {
		return fmt.Errorf("open snapshot file: %w", err)
	}
	defer file.Close()

	if _, err := io.Copy(writer, file); err != nil {
		return fmt.Errorf("read snapshot file: %w", err)
	}
	return ctx.Err()
}

func (s *FileSnapshotStore) Delete(ctx context.Context, key string) error {
	if err := os.Remove(s.fullPath(key)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete snapshot file: %w", err)
	}
	return ctx.Err()
}

func (s *FileSnapshotStore) fullPath(key string) string {
	return filepath.Join(s.baseDir, filepath.Clean(key))
}

// Package sandbox provides file-reading capabilities for session sandboxes.
package sandbox

import (
	"context"
	"fmt"
)

// FileEntry represents a single entry in a directory listing.
type FileEntry struct {
	Path string `json:"path"`
	Type string `json:"type"` // "file" or "dir"
	Size int64  `json:"size"`
}

// FileContent holds the content of a file read from a sandbox.
type FileContent struct {
	Path     string `json:"path"`
	Content  string `json:"content"`
	Language string `json:"language"`
}

// FileLine is a single numbered line from a file.
type FileLine struct {
	Number  int    `json:"number"`
	Content string `json:"content"`
}

// FileReader reads files and directories from a sandbox environment.
type FileReader interface {
	// ListDir returns the entries in a directory inside the sandbox.
	ListDir(ctx context.Context, containerID, workDir, dirPath string) ([]FileEntry, error)

	// ReadFile returns the full content of a file inside the sandbox.
	ReadFile(ctx context.Context, containerID, workDir, filePath string) (string, error)

	// ReadFileContext returns a slice of lines around a specific line number.
	ReadFileContext(ctx context.Context, containerID, workDir, filePath string, line, above, below int) ([]FileLine, error)
}

// NoOpFileReader is a FileReader that always returns "unavailable" errors.
// Use this instead of nil when Docker is not available to avoid nil-pointer panics.
type NoOpFileReader struct{}

func (NoOpFileReader) ListDir(_ context.Context, _, _, _ string) ([]FileEntry, error) {
	return nil, fmt.Errorf("sandbox file browsing is not available")
}

func (NoOpFileReader) ReadFile(_ context.Context, _, _, _ string) (string, error) {
	return "", fmt.Errorf("sandbox file browsing is not available")
}

func (NoOpFileReader) ReadFileContext(_ context.Context, _, _, _ string, _, _, _ int) ([]FileLine, error) {
	return nil, fmt.Errorf("sandbox file browsing is not available")
}

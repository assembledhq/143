// Package sandbox provides file-reading capabilities for session sandboxes.
package sandbox

import (
	"context"
	"errors"
	"fmt"
)

// ErrFileNotFound is returned by FileReader implementations when the requested
// path does not exist inside the sandbox. Callers should use errors.Is to
// distinguish the expected "no such file" case from real read failures
// (docker exec error, context cancellation, sandbox gone).
var ErrFileNotFound = errors.New("file not found")

// FileEntry represents a single entry in a directory listing.
type FileEntry struct {
	Path string `json:"path"`
	Type string `json:"type"` // "file" or "dir"
	Size int64  `json:"size"`
}

// FileContent holds the content of a file read from a sandbox.
type FileContent struct {
	Path      string `json:"path"`
	Content   string `json:"content"`
	Language  string `json:"language"`
	Truncated bool   `json:"truncated"`
}

// FileLine is a single numbered line from a file.
type FileLine struct {
	Number  int    `json:"number"`
	Content string `json:"content"`
}

// FileContextResult is a directional file-context window plus metadata that
// lets the caller know whether more lines exist above or below it.
type FileContextResult struct {
	Lines        []FileLine `json:"lines"`
	StartLine    int        `json:"start_line"`
	EndLine      int        `json:"end_line"`
	HasMoreAbove bool       `json:"has_more_above"`
	HasMoreBelow bool       `json:"has_more_below"`
	TotalLines   int        `json:"total_lines"`
}

// FileReader reads files and directories from a sandbox environment.
type FileReader interface {
	// ListDir returns the entries in a directory inside the sandbox.
	ListDir(ctx context.Context, containerID, workDir, dirPath string) ([]FileEntry, error)

	// ReadFile returns the full content of a file inside the sandbox (up to 1MB).
	// The bool return indicates whether the content was truncated.
	ReadFile(ctx context.Context, containerID, workDir, filePath string) (string, bool, error)

	// ReadFileContext returns a slice of lines around a specific line number plus
	// enough metadata for directional expansion in the diff UI.
	ReadFileContext(ctx context.Context, containerID, workDir, filePath string, line, above, below int) (FileContextResult, error)
}

// NoOpFileReader is a FileReader used when Docker is unavailable so callers
// don't have to nil-check. Every method returns an error that wraps
// ErrFileNotFound, so callers using errors.Is(err, ErrFileNotFound) treat the
// no-Docker case the same as a genuinely missing path — which is what
// auto-detect callers (e.g. PreviewHandler.readWorkspacePreviewConfig) want.
type NoOpFileReader struct{}

func (NoOpFileReader) ListDir(_ context.Context, _, _, _ string) ([]FileEntry, error) {
	return nil, fmt.Errorf("sandbox file browsing is not available: %w", ErrFileNotFound)
}

func (NoOpFileReader) ReadFile(_ context.Context, _, _, _ string) (string, bool, error) {
	return "", false, fmt.Errorf("sandbox file browsing is not available: %w", ErrFileNotFound)
}

func (NoOpFileReader) ReadFileContext(_ context.Context, _, _, _ string, _, _, _ int) (FileContextResult, error) {
	return FileContextResult{}, fmt.Errorf("sandbox file browsing is not available: %w", ErrFileNotFound)
}

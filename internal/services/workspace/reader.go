// Package workspace provides session-bound, read-only access to a session's
// workspace files. The same Reader interface fronts two implementations: a
// live-container reader (talks to Docker) and a snapshot reader (reads from
// a persisted tar in object storage). Handlers depend on Reader, not on
// where the bytes come from.
package workspace

import (
	"context"
	"errors"

	"github.com/assembledhq/143/internal/services/sandbox"
)

// ErrSnapshotMissing is returned when a session has no usable workspace
// source at all — neither a live container nor a persisted snapshot. The
// HTTP handler maps this to the existing NO_SANDBOX 409 response.
var ErrSnapshotMissing = errors.New("session has no live container and no snapshot")

// ErrSnapshotUnreadable wraps low-level errors that surface when a
// snapshot exists in object storage but cannot be parsed or extracted —
// e.g. a corrupt gzip stream, a tar header that fails our safety
// validation, or an oversize cap breach. The HTTP handler maps this to
// 500 SNAPSHOT_UNREADABLE so the frontend distinguishes "snapshot is
// gone" (NO_SANDBOX, gracefully disabled UI) from "snapshot exists but
// the server cannot make sense of it" (an operational issue worth
// surfacing).
var ErrSnapshotUnreadable = errors.New("snapshot exists but cannot be read")

// ErrSnapshotUnavailable wraps object-store and staging failures when the
// snapshot key exists conceptually but the server cannot load the artifact
// right now — e.g. S3 is unavailable, credentials are wrong, or the local
// staging file cannot be written. The HTTP handler maps this to a 500-class
// response so operational failures do not look like missing source files.
var ErrSnapshotUnavailable = errors.New("snapshot could not be loaded")

// Reader is a session-bound, read-only view over a workspace. The session
// (container ID, workdir, snapshot key) is captured when the Reader is
// constructed; callers pass only paths.
type Reader interface {
	// ListDir returns the entries in a directory inside the workspace. The
	// path is workspace-relative; "" or "." means the workspace root.
	ListDir(ctx context.Context, dirPath string) ([]sandbox.FileEntry, error)

	// ReadFile returns the full content of a workspace file (capped to a
	// reasonable size, with the truncation flag set when the cap was hit).
	ReadFile(ctx context.Context, filePath string) (content string, truncated bool, err error)

	// ReadFileContext returns a directional line window plus enough metadata
	// (total line count, has-more-above/below) for the diff UI to render and
	// keep navigating without leaving the page.
	ReadFileContext(ctx context.Context, filePath string, line, above, below int) (sandbox.FileContextResult, error)
}

// liveContainerReader adapts the existing sandbox.FileReader (which is
// parameterized on containerID + workDir) into a session-bound Reader.
type liveContainerReader struct {
	inner       sandbox.FileReader
	containerID string
	workDir     string
}

type recursiveSandboxFileReader interface {
	ListDirRecursive(ctx context.Context, containerID, workDir string, maxEntries int, ignoredDirNames []string) ([]sandbox.FileEntry, error)
}

type recursiveLiveContainerReader struct {
	*liveContainerReader
}

// NewLiveContainerReader returns a Reader backed by a live Docker container.
func NewLiveContainerReader(inner sandbox.FileReader, containerID, workDir string) Reader {
	reader := &liveContainerReader{inner: inner, containerID: containerID, workDir: workDir}
	if _, ok := inner.(recursiveSandboxFileReader); ok {
		return &recursiveLiveContainerReader{liveContainerReader: reader}
	}
	return reader
}

func (r *liveContainerReader) ListDir(ctx context.Context, dirPath string) ([]sandbox.FileEntry, error) {
	return r.inner.ListDir(ctx, r.containerID, r.workDir, dirPath)
}

func (r *liveContainerReader) ReadFile(ctx context.Context, filePath string) (string, bool, error) {
	return r.inner.ReadFile(ctx, r.containerID, r.workDir, filePath)
}

func (r *liveContainerReader) ReadFileContext(ctx context.Context, filePath string, line, above, below int) (sandbox.FileContextResult, error) {
	return r.inner.ReadFileContext(ctx, r.containerID, r.workDir, filePath, line, above, below)
}

func (r *recursiveLiveContainerReader) ListDirRecursive(ctx context.Context, maxEntries int, ignoredDirNames []string) ([]sandbox.FileEntry, error) {
	return r.inner.(recursiveSandboxFileReader).ListDirRecursive(ctx, r.containerID, r.workDir, maxEntries, ignoredDirNames)
}

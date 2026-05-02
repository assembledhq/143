package workspace

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/assembledhq/143/internal/services/sandbox"
)

// snapshotReader serves file reads from an extracted session workspace
// snapshot on local disk. Construct via NewSnapshotReader; the cache
// handles the on-demand download/extract. Each read acquires a cache
// reference for the duration of that single operation, so concurrent
// LRU evictions cannot delete the extraction mid-read.
type snapshotReader struct {
	cache        *SnapshotCache
	snapshotKey  string
	workspaceRel string // in-tar relative path of the workspace dir
}

// NewSnapshotReader returns a Reader backed by a persisted workspace
// snapshot. workspaceRel is the in-tar path of the session's workspace
// — pass exactly the same path the producer used as a `tar -C / <path>`
// argument (with the leading slash stripped). For sessions with an
// attached repo this is `home/<user>/<slug>`; for sessions without a
// repo it is `workspace`.
func NewSnapshotReader(cache *SnapshotCache, snapshotKey, workspaceRel string) Reader {
	return &snapshotReader{
		cache:        cache,
		snapshotKey:  snapshotKey,
		workspaceRel: strings.Trim(workspaceRel, "/"),
	}
}

// maxSnapshotReadBytes mirrors sandbox.maxFileReadBytes — ReadFile returns
// at most this many bytes and signals truncation. Kept in sync so the live
// and snapshot paths produce consistent payload sizes.
const maxSnapshotReadBytes = 1 << 20 // 1 MiB

// ListDir returns the entries in a workspace directory. The path is
// workspace-relative; "" or "." means the workspace root.
func (r *snapshotReader) ListDir(ctx context.Context, dirPath string) ([]sandbox.FileEntry, error) {
	entry, abs, err := r.resolve(ctx, dirPath)
	if err != nil {
		return nil, err
	}
	defer entry.Close()

	dirEntries, err := os.ReadDir(abs)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("list directory %s: %w", dirPath, sandbox.ErrFileNotFound)
		}
		return nil, fmt.Errorf("list directory %s: %w", dirPath, err)
	}

	cleanDir := strings.Trim(filepath.ToSlash(filepath.Clean(dirPath)), "/")
	if cleanDir == "." {
		cleanDir = ""
	}

	out := make([]sandbox.FileEntry, 0, len(dirEntries))
	for _, e := range dirEntries {
		entryType := "file"
		var size int64
		if e.IsDir() {
			entryType = "dir"
		} else if info, infoErr := e.Info(); infoErr == nil {
			size = info.Size()
		}
		name := e.Name()
		if cleanDir != "" {
			name = cleanDir + "/" + name
		}
		out = append(out, sandbox.FileEntry{Path: name, Type: entryType, Size: size})
	}
	// os.ReadDir already returns entries in sorted order, but we want to
	// guarantee it on the contract level so the API is deterministic
	// independent of the underlying filesystem.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// ReadFile returns the workspace file content (capped at maxSnapshotReadBytes).
func (r *snapshotReader) ReadFile(ctx context.Context, filePath string) (string, bool, error) {
	entry, abs, err := r.resolve(ctx, filePath)
	if err != nil {
		return "", false, err
	}
	defer entry.Close()

	// abs is the host path returned by r.resolve, which validates the
	// caller-supplied path via safeWorkspaceJoin (rejects NUL, '..',
	// and any input that would resolve outside the workspace root).
	f, err := os.Open(abs) // #nosec G304 -- abs is bounded under the cache extraction root by safeWorkspaceJoin
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", false, fmt.Errorf("read file %s: %w", filePath, sandbox.ErrFileNotFound)
		}
		return "", false, fmt.Errorf("read file %s: %w", filePath, err)
	}
	defer f.Close()

	// Reject directories with a clear error rather than reading the
	// directory entry as a file (would surface as garbage on Linux).
	if info, statErr := f.Stat(); statErr == nil && info.IsDir() {
		return "", false, fmt.Errorf("read file %s: %w", filePath, sandbox.ErrFileNotFound)
	}

	// Read one byte past the cap so we can correctly set the truncated flag.
	buf := make([]byte, maxSnapshotReadBytes+1)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "", false, fmt.Errorf("read file %s: %w", filePath, err)
	}

	truncated := n > maxSnapshotReadBytes
	if truncated {
		n = maxSnapshotReadBytes
	}
	return string(buf[:n]), truncated, nil
}

// ReadFileContext returns a directional line window plus the metadata the
// diff UI needs (start/end, has-more flags, total). Mirrors the live
// reader's contract so the handler doesn't need to special-case which
// reader produced the result.
//
// Total-line counts are memoized on the SnapshotCache entry: the first
// call for a given file pays a forward scan, subsequent calls only read
// the window. Snapshots are immutable for the cache entry's lifetime, so
// caching the count is safe — a re-extracted snapshot lands in a fresh
// cacheEntry with an empty memo map.
func (r *snapshotReader) ReadFileContext(ctx context.Context, filePath string, line, above, below int) (sandbox.FileContextResult, error) {
	entry, abs, err := r.resolve(ctx, filePath)
	if err != nil {
		return sandbox.FileContextResult{}, err
	}
	defer entry.Close()

	startLine := line - above
	if startLine < 1 {
		startLine = 1
	}
	endLine := line + below
	if endLine < startLine {
		endLine = startLine
	}

	captured, total, err := r.readWindowAndCount(abs, entry, filePath, line, startLine, endLine)
	if err != nil {
		return sandbox.FileContextResult{}, err
	}

	result := sandbox.FileContextResult{
		Lines:      captured,
		TotalLines: total,
	}
	if len(captured) > 0 {
		result.StartLine = captured[0].Number
		result.EndLine = captured[len(captured)-1].Number
		result.HasMoreAbove = result.StartLine > 1
		result.HasMoreBelow = result.EndLine < total
	}
	return result, nil
}

// readWindowAndCount returns lines in [startLine, endLine] (1-indexed,
// inclusive) plus the file's total line count. Uses the SnapshotEntry's
// per-file memo to skip the full scan when the line count is already
// known from a prior call.
func (r *snapshotReader) readWindowAndCount(abs string, entry *SnapshotEntry, filePath string, line, startLine, endLine int) ([]sandbox.FileLine, int, error) {
	// abs has been validated by safeWorkspaceJoin in r.resolve before reaching
	// this function — it cannot escape the cache extraction root.
	f, err := os.Open(abs) // #nosec G304 -- abs is bounded under the cache extraction root by safeWorkspaceJoin
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, 0, fmt.Errorf("read context %s:%d: %w", filePath, line, sandbox.ErrFileNotFound)
		}
		return nil, 0, fmt.Errorf("read context %s:%d: %w", filePath, line, err)
	}
	defer f.Close()

	if info, statErr := f.Stat(); statErr == nil && info.IsDir() {
		return nil, 0, fmt.Errorf("read context %s:%d: %w", filePath, line, sandbox.ErrFileNotFound)
	}

	if cached, ok := entry.LineCount(abs); ok {
		captured, err := scanLineRange(f, startLine, endLine)
		if err != nil {
			return nil, 0, wrapScanError(err, filePath, line)
		}
		return captured, cached, nil
	}

	// Cold path: single forward scan captures the requested window AND
	// counts the file. Snapshots are bounded by maxPerEntryBytes (256 MiB)
	// so a full scan is acceptable; the memoization above means subsequent
	// reads of the same file skip this entirely.
	captured, total, err := scanLineWindow(f, startLine, endLine)
	if err != nil {
		return nil, 0, wrapScanError(err, filePath, line)
	}
	entry.StoreLineCount(abs, total)
	return captured, total, nil
}

// wrapScanError turns a bufio.Scanner failure into the right sentinel for
// the handler. A line longer than the scanner buffer (4 MiB) is a property
// of the file content, not a missing-file condition, so map it to
// ErrSnapshotUnreadable instead of letting the handler default to
// FILE_NOT_FOUND. Other scanner errors (I/O failures from the underlying
// disk) keep their original message but don't carry the unreadable flag —
// they're operational issues, not corrupt content.
func wrapScanError(err error, filePath string, line int) error {
	if errors.Is(err, bufio.ErrTooLong) {
		return errors.Join(ErrSnapshotUnreadable, fmt.Errorf("read context %s:%d: line exceeds scanner buffer", filePath, line))
	}
	return fmt.Errorf("read context %s:%d: %w", filePath, line, err)
}

// scanLineRange returns lines in [startLine, endLine] (1-indexed,
// inclusive) without computing a total. Used on the hot path when the
// total line count is already memoized.
func scanLineRange(src io.Reader, startLine, endLine int) ([]sandbox.FileLine, error) {
	const scannerBuf = 4 * 1024 * 1024
	sc := bufio.NewScanner(src)
	sc.Buffer(make([]byte, 64*1024), scannerBuf)

	var captured []sandbox.FileLine
	if endLine >= startLine {
		captured = make([]sandbox.FileLine, 0, endLine-startLine+1)
	}

	lineNum := 0
	for sc.Scan() {
		lineNum++
		if lineNum > endLine {
			// We can stop early on the hot path because the memoized total
			// already tells us how many lines exist.
			break
		}
		if lineNum >= startLine {
			captured = append(captured, sandbox.FileLine{Number: lineNum, Content: sc.Text()})
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan lines: %w", err)
	}
	return captured, nil
}

// scanLineWindow walks src once and returns lines in [startLine, endLine]
// (1-indexed, inclusive) plus the total line count. The line count is
// derived from the byte stream rather than from len(captured) so we can
// populate has_more_below correctly even when the requested window
// includes the last line of the file.
//
// We use bufio.Scanner with an enlarged buffer (matches Go's default but
// enough to handle multi-megabyte minified files). A line longer than
// the buffer triggers a scan error rather than silent truncation.
func scanLineWindow(src io.Reader, startLine, endLine int) ([]sandbox.FileLine, int, error) {
	const scannerBuf = 4 * 1024 * 1024 // 4 MiB max line — more than any real source file
	sc := bufio.NewScanner(src)
	sc.Buffer(make([]byte, 64*1024), scannerBuf)

	var captured []sandbox.FileLine
	if endLine >= startLine {
		captured = make([]sandbox.FileLine, 0, endLine-startLine+1)
	}

	lineNum := 0
	for sc.Scan() {
		lineNum++
		if lineNum >= startLine && lineNum <= endLine {
			// Scanner.Text reuses an internal buffer — copy via string()
			// implicitly so the FileLine retains a stable value.
			captured = append(captured, sandbox.FileLine{Number: lineNum, Content: sc.Text()})
		}
	}
	if err := sc.Err(); err != nil {
		return nil, 0, fmt.Errorf("scan lines: %w", err)
	}
	return captured, lineNum, nil
}

// resolve takes a workspace-relative path, validates it does not escape
// the snapshot's workspace root, and returns a refcounted SnapshotEntry
// plus the corresponding absolute host path. The cache is consulted
// lazily (on first call per request) so a request for a missing
// snapshot fails fast without doing any filesystem work.
//
// The returned entry's ref is the caller's responsibility to release;
// each operation method (ListDir/ReadFile/ReadFileContext) does so via
// defer entry.Close().
func (r *snapshotReader) resolve(ctx context.Context, relPath string) (*SnapshotEntry, string, error) {
	entry, err := r.cache.Open(ctx, r.snapshotKey, r.workspaceRel)
	if err != nil {
		return nil, "", err
	}
	resolved, ok := safeWorkspaceJoin(entry.WorkspaceRoot, relPath)
	if !ok {
		entry.Close()
		return nil, "", fmt.Errorf("invalid workspace path %q", relPath)
	}
	return entry, resolved, nil
}

// safeWorkspaceJoin joins a workspace-relative path under workspaceRoot,
// rejecting any input that would resolve outside of it. Mirrors the
// logic of sandbox.resolvePathInWorkDir but works on host filesystem
// paths, so we use the OS separator throughout.
func safeWorkspaceJoin(workspaceRoot, relPath string) (string, bool) {
	if relPath == "" || relPath == "." || relPath == "/" {
		return workspaceRoot, true
	}
	// Reject embedded NULs outright.
	if bytes.IndexByte([]byte(relPath), 0) != -1 {
		return "", false
	}
	clean := filepath.Clean(relPath)
	clean = strings.TrimPrefix(clean, string(filepath.Separator))
	clean = strings.TrimPrefix(clean, "/")
	joined := filepath.Join(workspaceRoot, clean)
	if joined != workspaceRoot && !strings.HasPrefix(joined, workspaceRoot+string(filepath.Separator)) {
		return "", false
	}
	return joined, true
}

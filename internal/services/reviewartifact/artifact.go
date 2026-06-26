// Package reviewartifact stores bounded head-side file text for fast diff
// context expansion after a session workspace snapshot has gone cold.
package reviewartifact

import (
	"bytes"
	"compress/gzip"
	"container/list"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/assembledhq/143/internal/services/sandbox"
	"github.com/assembledhq/143/internal/services/storage"
)

const (
	Version = 1

	DefaultPerFileMaxBytes      int64 = 2 * 1024 * 1024
	DefaultMaxUncompressedBytes       = 32 * 1024 * 1024
	DefaultMaxFiles                   = 150
	DefaultReadTimeout                = 3 * time.Second
	DefaultCacheBytes           int64 = 128 * 1024 * 1024

	SkipReasonBinary      = "binary"
	SkipReasonDeleted     = "deleted"
	SkipReasonInvalidPath = "invalid_path"
	SkipReasonMaxFiles    = "max_files"
	SkipReasonMissing     = "missing"
	SkipReasonNonUTF8     = "non_utf8"
	SkipReasonReadError   = "read_error"
	SkipReasonTooLarge    = "too_large"
)

// ExecFunc executes a shell command in the sandbox working directory.
type ExecFunc func(ctx context.Context, cmd string, stdout, stderr io.Writer) (int, error)

type Options struct {
	Key                  string
	MaxFiles             int
	PerFileMaxBytes      int64
	MaxUncompressedBytes int64
	ReadTimeout          time.Duration
}

type Metadata struct {
	Key               string
	Version           int
	CompressedBytes   int64
	UncompressedBytes int64
	FileCount         int
	SkippedCount      int
	Truncated         bool
}

type Artifact struct {
	Version   int             `json:"version"`
	SessionID string          `json:"session_id,omitempty"`
	Files     map[string]File `json:"files"`
	Skipped   []SkippedFile   `json:"skipped,omitempty"`
	Truncated bool            `json:"truncated,omitempty"`
}

type File struct {
	Path       string `json:"path"`
	Content    string `json:"content"`
	SizeBytes  int64  `json:"size_bytes"`
	TotalLines int    `json:"total_lines"`
}

type SkippedFile struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type ChangedFile struct {
	Path    string
	Deleted bool
	Binary  bool
}

func Capture(ctx context.Context, store storage.SnapshotStore, exec ExecFunc, orgID, sessionID uuid.UUID, diff string, opts Options) (Metadata, error) {
	if store == nil {
		return Metadata{}, errors.New("review artifact store is nil")
	}
	if exec == nil {
		return Metadata{}, errors.New("review artifact exec is nil")
	}
	if strings.TrimSpace(diff) == "" {
		return Metadata{}, nil
	}
	opts = normalizeOptions(opts)
	if opts.Key == "" {
		opts.Key = fmt.Sprintf("review-artifacts/%s/%s/%s.json.gz", orgID, sessionID, uuid.NewString())
	}

	artifact := Artifact{
		Version:   Version,
		SessionID: sessionID.String(),
		Files:     map[string]File{},
	}
	seen := map[string]struct{}{}
	for _, changed := range ParseChangedFiles(diff) {
		path, ok := cleanArtifactPath(changed.Path)
		if !ok {
			artifact.Skipped = append(artifact.Skipped, SkippedFile{Path: changed.Path, Reason: SkipReasonInvalidPath})
			artifact.Truncated = true
			continue
		}
		if _, exists := seen[path]; exists {
			continue
		}
		seen[path] = struct{}{}
		if changed.Deleted {
			artifact.Skipped = append(artifact.Skipped, SkippedFile{Path: path, Reason: SkipReasonDeleted})
			continue
		}
		if changed.Binary {
			artifact.Skipped = append(artifact.Skipped, SkippedFile{Path: path, Reason: SkipReasonBinary})
			continue
		}
		if len(artifact.Files) >= opts.MaxFiles {
			artifact.Skipped = append(artifact.Skipped, SkippedFile{Path: path, Reason: SkipReasonMaxFiles})
			artifact.Truncated = true
			continue
		}

		content, reason := readChangedFile(ctx, exec, path, opts)
		if reason != "" {
			artifact.Skipped = append(artifact.Skipped, SkippedFile{Path: path, Reason: reason})
			if reason == SkipReasonTooLarge {
				artifact.Truncated = true
			}
			continue
		}
		file := File{
			Path:       path,
			Content:    content,
			SizeBytes:  int64(len(content)),
			TotalLines: countLogicalLines(content),
		}
		artifact.Files[path] = file
		if uncompressedSize(artifact) > opts.MaxUncompressedBytes {
			delete(artifact.Files, path)
			artifact.Skipped = append(artifact.Skipped, SkippedFile{Path: path, Reason: SkipReasonTooLarge})
			artifact.Truncated = true
		}
	}

	var buf bytes.Buffer
	meta, err := Encode(&buf, artifact)
	if err != nil {
		return Metadata{}, err
	}
	meta.Key = opts.Key
	if err := store.Save(ctx, opts.Key, bytes.NewReader(buf.Bytes())); err != nil {
		return Metadata{}, fmt.Errorf("save review artifact: %w", err)
	}
	return meta, nil
}

func Encode(w io.Writer, artifact Artifact) (Metadata, error) {
	if artifact.Version == 0 {
		artifact.Version = Version
	}
	if artifact.Files == nil {
		artifact.Files = map[string]File{}
	}
	raw, err := json.Marshal(artifact)
	if err != nil {
		return Metadata{}, fmt.Errorf("marshal review artifact: %w", err)
	}
	gz := gzip.NewWriter(w)
	if _, err := gz.Write(raw); err != nil {
		_ = gz.Close()
		return Metadata{}, fmt.Errorf("gzip review artifact: %w", err)
	}
	if err := gz.Close(); err != nil {
		return Metadata{}, fmt.Errorf("close review artifact gzip: %w", err)
	}
	compressed := int64(-1)
	if sizer, ok := w.(interface{ Len() int }); ok {
		compressed = int64(sizer.Len())
	}
	return Metadata{
		Version:           artifact.Version,
		CompressedBytes:   compressed,
		UncompressedBytes: int64(len(raw)),
		FileCount:         len(artifact.Files),
		SkippedCount:      len(artifact.Skipped),
		Truncated:         artifact.Truncated,
	}, nil
}

func Load(ctx context.Context, store storage.SnapshotStore, key string, maxUncompressedBytes int64) (*Artifact, error) {
	if store == nil {
		return nil, errors.New("review artifact store is nil")
	}
	if key == "" {
		return nil, errors.New("review artifact key is empty")
	}
	if maxUncompressedBytes <= 0 {
		maxUncompressedBytes = DefaultMaxUncompressedBytes
	}
	var compressed bytes.Buffer
	if err := store.Load(ctx, key, &compressed); err != nil {
		return nil, fmt.Errorf("load review artifact: %w", err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(compressed.Bytes()))
	if err != nil {
		return nil, fmt.Errorf("open review artifact gzip: %w", err)
	}
	defer gz.Close()
	raw, err := io.ReadAll(io.LimitReader(gz, maxUncompressedBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read review artifact gzip: %w", err)
	}
	if int64(len(raw)) > maxUncompressedBytes {
		return nil, fmt.Errorf("review artifact exceeds max decoded size")
	}
	var artifact Artifact
	if err := json.Unmarshal(raw, &artifact); err != nil {
		return nil, fmt.Errorf("decode review artifact: %w", err)
	}
	if artifact.Files == nil {
		artifact.Files = map[string]File{}
	}
	return &artifact, nil
}

func ParseChangedFiles(diff string) []ChangedFile {
	var out []ChangedFile
	var current *ChangedFile
	flush := func() {
		if current == nil || current.Path == "" {
			current = nil
			return
		}
		out = append(out, *current)
		current = nil
	}

	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			flush()
			current = &ChangedFile{Path: parseDiffGitNewPath(line)}
		case current != nil && strings.HasPrefix(line, "+++ "):
			raw := strings.TrimSpace(strings.TrimPrefix(line, "+++ "))
			if raw == "/dev/null" {
				current.Deleted = true
			} else if path := stripDiffPathPrefix(raw, "b/"); path != "" {
				current.Path = path
			}
		case current != nil && strings.HasPrefix(line, "deleted file mode "):
			current.Deleted = true
		case current != nil && (strings.HasPrefix(line, "Binary files ") || strings.HasPrefix(line, "GIT binary patch")):
			current.Binary = true
		}
	}
	flush()
	return out
}

const readFileScript = `path=$1
limit=$2
if [ ! -f "$path" ]; then
  echo "reviewartifact:missing" >&2
  exit 2
fi
size=$(wc -c < "$path" | tr -d '[:space:]')
case "$size" in
  ''|*[!0-9]*)
    echo "reviewartifact:stat_failed" >&2
    exit 4
    ;;
esac
if [ "$size" -gt "$limit" ]; then
  echo "reviewartifact:too_large:$size" >&2
  exit 3
fi
cat -- "$path"`

func readChangedFile(ctx context.Context, exec ExecFunc, path string, opts Options) (string, string) {
	readCtx, cancel := context.WithTimeout(ctx, opts.ReadTimeout)
	defer cancel()

	cmd := fmt.Sprintf("sh -c %s reviewartifact-read %s %d", shellQuote(readFileScript), shellQuote(path), opts.PerFileMaxBytes)
	var stdout, stderr bytes.Buffer
	exitCode, err := exec(readCtx, cmd, &stdout, &stderr)
	if err != nil {
		return "", SkipReasonReadError
	}
	if exitCode != 0 {
		switch exitCode {
		case 2:
			return "", SkipReasonMissing
		case 3:
			return "", SkipReasonTooLarge
		default:
			return "", SkipReasonReadError
		}
	}
	body := stdout.Bytes()
	if int64(len(body)) > opts.PerFileMaxBytes {
		return "", SkipReasonTooLarge
	}
	if bytes.IndexByte(body, 0) >= 0 {
		return "", SkipReasonBinary
	}
	if !utf8.Valid(body) {
		return "", SkipReasonNonUTF8
	}
	return string(body), ""
}

func normalizeOptions(opts Options) Options {
	if opts.MaxFiles <= 0 {
		opts.MaxFiles = DefaultMaxFiles
	}
	if opts.PerFileMaxBytes <= 0 {
		opts.PerFileMaxBytes = DefaultPerFileMaxBytes
	}
	if opts.MaxUncompressedBytes <= 0 {
		opts.MaxUncompressedBytes = DefaultMaxUncompressedBytes
	}
	if opts.ReadTimeout <= 0 {
		opts.ReadTimeout = DefaultReadTimeout
	}
	return opts
}

func uncompressedSize(artifact Artifact) int64 {
	raw, err := json.Marshal(artifact)
	if err != nil {
		return 0
	}
	return int64(len(raw))
}

func countLogicalLines(content string) int {
	if content == "" {
		return 0
	}
	count := strings.Count(content, "\n")
	if strings.HasSuffix(content, "\n") {
		return count
	}
	return count + 1
}

func splitLogicalLines(content string) []string {
	if content == "" {
		return nil
	}
	lines := strings.Split(content, "\n")
	if strings.HasSuffix(content, "\n") {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func parseDiffGitNewPath(line string) string {
	rest := strings.TrimSpace(strings.TrimPrefix(line, "diff --git "))
	fields := splitGitFields(rest)
	if len(fields) < 2 {
		return ""
	}
	return stripDiffPathPrefix(fields[len(fields)-1], "b/")
}

func splitGitFields(s string) []string {
	var fields []string
	for len(s) > 0 {
		s = strings.TrimLeft(s, " \t")
		if s == "" {
			break
		}
		if s[0] == '"' {
			end := 1
			escaped := false
			for end < len(s) {
				if escaped {
					escaped = false
				} else if s[end] == '\\' {
					escaped = true
				} else if s[end] == '"' {
					break
				}
				end++
			}
			if end < len(s) {
				token := s[:end+1]
				if unquoted, err := strconv.Unquote(token); err == nil {
					fields = append(fields, unquoted)
				} else {
					fields = append(fields, strings.Trim(token, `"`))
				}
				s = s[end+1:]
				continue
			}
		}
		idx := strings.IndexAny(s, " \t")
		if idx < 0 {
			fields = append(fields, s)
			break
		}
		fields = append(fields, s[:idx])
		s = s[idx+1:]
	}
	return fields
}

func stripDiffPathPrefix(raw, prefix string) string {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, `"`) {
		if unquoted, err := strconv.Unquote(raw); err == nil {
			raw = unquoted
		}
	}
	if strings.HasPrefix(raw, prefix) {
		return strings.TrimPrefix(raw, prefix)
	}
	return raw
}

func cleanArtifactPath(raw string) (string, bool) {
	if raw == "" || strings.ContainsRune(raw, 0) {
		return "", false
	}
	raw = filepath.ToSlash(raw)
	if strings.HasPrefix(raw, "/") {
		return "", false
	}
	clean := filepath.ToSlash(filepath.Clean(raw))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", false
	}
	return clean, true
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

type CachedReader struct {
	store    storage.SnapshotStore
	maxBytes int64

	mu    sync.Mutex
	used  int64
	items map[string]*cacheItem
	order *list.List
}

type cacheItem struct {
	key      string
	artifact *Artifact
	size     int64
	element  *list.Element
}

func NewCachedReader(store storage.SnapshotStore, maxBytes int64) *CachedReader {
	if maxBytes <= 0 {
		maxBytes = DefaultCacheBytes
	}
	return &CachedReader{
		store:    store,
		maxBytes: maxBytes,
		items:    map[string]*cacheItem{},
		order:    list.New(),
	}
}

func (r *CachedReader) ReadFileContext(ctx context.Context, key, filePath string, line, above, below int) (sandbox.FileContextResult, bool, error) {
	if r == nil || r.store == nil || key == "" {
		return sandbox.FileContextResult{}, false, nil
	}
	artifact, err := r.load(ctx, key)
	if err != nil {
		return sandbox.FileContextResult{}, false, err
	}
	cleanPath, ok := cleanArtifactPath(filePath)
	if !ok {
		return sandbox.FileContextResult{}, false, nil
	}
	file, ok := artifact.Files[cleanPath]
	if !ok {
		return sandbox.FileContextResult{}, false, nil
	}
	return contextFromFile(file, line, above, below), true, nil
}

func (r *CachedReader) load(ctx context.Context, key string) (*Artifact, error) {
	r.mu.Lock()
	if item, ok := r.items[key]; ok {
		r.order.MoveToFront(item.element)
		artifact := item.artifact
		r.mu.Unlock()
		return artifact, nil
	}
	r.mu.Unlock()

	artifact, err := Load(ctx, r.store, key, DefaultMaxUncompressedBytes)
	if err != nil {
		return nil, err
	}
	size := artifactCacheSize(artifact)
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.items[key]; ok {
		r.order.MoveToFront(existing.element)
		return existing.artifact, nil
	}
	item := &cacheItem{key: key, artifact: artifact, size: size}
	item.element = r.order.PushFront(item)
	r.items[key] = item
	r.used += size
	for r.used > r.maxBytes && r.order.Len() > 0 {
		back := r.order.Back()
		if back == nil {
			break
		}
		victim := back.Value.(*cacheItem)
		r.order.Remove(back)
		delete(r.items, victim.key)
		r.used -= victim.size
	}
	return artifact, nil
}

func artifactCacheSize(artifact *Artifact) int64 {
	if artifact == nil {
		return 0
	}
	var size int64
	for _, file := range artifact.Files {
		size += int64(len(file.Content))
	}
	if size == 0 {
		size = uncompressedSize(*artifact)
	}
	return size
}

func contextFromFile(file File, line, above, below int) sandbox.FileContextResult {
	lines := splitLogicalLines(file.Content)
	total := file.TotalLines
	if total == 0 {
		total = len(lines)
	}
	startLine := line - above
	if startLine < 1 {
		startLine = 1
	}
	endLine := line + below
	if endLine < startLine {
		endLine = startLine
	}
	var captured []sandbox.FileLine
	if startLine <= total {
		last := endLine
		if last > total {
			last = total
		}
		captured = make([]sandbox.FileLine, 0, last-startLine+1)
		for i := startLine; i <= last; i++ {
			content := ""
			if i-1 >= 0 && i-1 < len(lines) {
				content = lines[i-1]
			}
			captured = append(captured, sandbox.FileLine{Number: i, Content: content})
		}
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
	return result
}

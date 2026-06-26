package reviewartifact

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestParseChangedFiles(t *testing.T) {
	t.Parallel()

	diff := strings.Join([]string{
		"diff --git a/src/app.ts b/src/app.ts",
		"index 111..222 100644",
		"--- a/src/app.ts",
		"+++ b/src/app.ts",
		"@@ -1 +1 @@",
		"-old",
		"+new",
		"diff --git a/old.txt b/old.txt",
		"deleted file mode 100644",
		"--- a/old.txt",
		"+++ /dev/null",
		"@@ -1 +0,0 @@",
		"-old",
		"diff --git a/logo.png b/logo.png",
		"index 111..222 100644",
		"Binary files a/logo.png and b/logo.png differ",
		"diff --git a/name.txt b/renamed.txt",
		"similarity index 98%",
		"rename from name.txt",
		"rename to renamed.txt",
		"--- a/name.txt",
		"+++ b/renamed.txt",
		"@@ -1 +1 @@",
		"-name",
		"+renamed",
		"diff --git a/new file.md b/new file.md",
		"new file mode 100644",
		"--- /dev/null",
		"+++ b/new file.md",
		"@@ -0,0 +1 @@",
		"+hello",
	}, "\n")

	got := ParseChangedFiles(diff)

	require.Equal(t, []ChangedFile{
		{Path: "src/app.ts"},
		{Path: "old.txt", Deleted: true},
		{Path: "logo.png", Binary: true},
		{Path: "renamed.txt"},
		{Path: "new file.md"},
	}, got, "ParseChangedFiles should return the head-side changed files with skip flags")
}

func TestCaptureStoresCompressedArtifact(t *testing.T) {
	t.Parallel()

	orgID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	sessionID := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	store := newMemoryStore()
	diff := strings.Join([]string{
		"diff --git a/src/app.ts b/src/app.ts",
		"--- a/src/app.ts",
		"+++ b/src/app.ts",
		"@@ -1 +1 @@",
		"-old",
		"+new",
		"diff --git a/deleted.go b/deleted.go",
		"--- a/deleted.go",
		"+++ /dev/null",
		"@@ -1 +0,0 @@",
		"-gone",
		"diff --git a/blob.bin b/blob.bin",
		"Binary files a/blob.bin and b/blob.bin differ",
	}, "\n")

	meta, err := Capture(context.Background(), store, execFromMap(map[string]string{
		"src/app.ts": "package main\n\nfunc main() {}\n",
	}), orgID, sessionID, diff, Options{Key: "review-artifacts/test/session/artifact.json.gz"})
	require.NoError(t, err, "Capture should store a compressed review artifact")
	require.Equal(t, "review-artifacts/test/session/artifact.json.gz", meta.Key, "Capture should use the configured key")
	require.Equal(t, Version, meta.Version, "Capture should record the artifact schema version")
	require.Equal(t, 1, meta.FileCount, "Capture should store changed text files")
	require.Equal(t, 2, meta.SkippedCount, "Capture should record skipped deleted and binary files")
	require.Greater(t, meta.CompressedBytes, int64(0), "Capture should report compressed size")
	require.Greater(t, meta.UncompressedBytes, int64(0), "Capture should report uncompressed size")

	artifact, err := Load(context.Background(), store, meta.Key, DefaultMaxUncompressedBytes)
	require.NoError(t, err, "stored artifact should load")
	file := artifact.Files["src/app.ts"]
	require.Equal(t, "package main\n\nfunc main() {}\n", file.Content, "artifact should contain the full head-side text")
	require.Equal(t, 3, file.TotalLines, "artifact should count logical lines like the file context readers")
	require.Equal(t, []SkippedFile{
		{Path: "deleted.go", Reason: SkipReasonDeleted},
		{Path: "blob.bin", Reason: SkipReasonBinary},
	}, artifact.Skipped, "artifact should preserve skip reasons")
}

func TestCaptureAppliesFileAndArtifactLimits(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	store := newMemoryStore()
	diff := strings.Join([]string{
		"diff --git a/huge.txt b/huge.txt",
		"--- a/huge.txt",
		"+++ b/huge.txt",
		"@@ -1 +1 @@",
		"-old",
		"+new",
		"diff --git a/one.txt b/one.txt",
		"--- a/one.txt",
		"+++ b/one.txt",
		"@@ -1 +1 @@",
		"-old",
		"+new",
		"diff --git a/two.txt b/two.txt",
		"--- a/two.txt",
		"+++ b/two.txt",
		"@@ -1 +1 @@",
		"-old",
		"+new",
	}, "\n")

	meta, err := Capture(context.Background(), store, execFromMap(map[string]string{
		"one.txt":  "one\n",
		"two.txt":  "two\n",
		"huge.txt": strings.Repeat("x", 16),
	}), orgID, sessionID, diff, Options{
		Key:                  "review-artifacts/test/limited.json.gz",
		MaxFiles:             1,
		PerFileMaxBytes:      8,
		MaxUncompressedBytes: DefaultMaxUncompressedBytes,
	})
	require.NoError(t, err, "Capture should not fail when limits skip files")
	require.True(t, meta.Truncated, "Capture should mark the artifact truncated when limits skip changed files")

	artifact, err := Load(context.Background(), store, meta.Key, DefaultMaxUncompressedBytes)
	require.NoError(t, err, "limited artifact should load")
	require.Contains(t, artifact.Files, "one.txt", "first file should be stored before max file count is reached")
	require.NotContains(t, artifact.Files, "two.txt", "second file should be skipped by max file count")
	require.NotContains(t, artifact.Files, "huge.txt", "oversized file should not be stored")
	require.Contains(t, artifact.Skipped, SkippedFile{Path: "two.txt", Reason: SkipReasonMaxFiles}, "artifact should record max-file skips")
	require.Contains(t, artifact.Skipped, SkippedFile{Path: "huge.txt", Reason: SkipReasonTooLarge}, "artifact should record per-file size skips")
}

func TestCachedReaderReturnsFileContext(t *testing.T) {
	t.Parallel()

	store := newMemoryStore()
	artifact := Artifact{
		Version: Version,
		Files: map[string]File{
			"src/app.ts": {
				Path:       "src/app.ts",
				Content:    "one\n\nthree\nfour",
				SizeBytes:  int64(len("one\n\nthree\nfour")),
				TotalLines: 4,
			},
		},
	}
	var buf bytes.Buffer
	meta, err := Encode(&buf, artifact)
	require.NoError(t, err, "test artifact should encode")
	store.saved["artifact-key"] = buf.Bytes()

	reader := NewCachedReader(store, 1024*1024)
	got, ok, err := reader.ReadFileContext(context.Background(), "artifact-key", "src/app.ts", 3, 1, 1)
	require.NoError(t, err, "ReadFileContext should load the artifact")
	require.True(t, ok, "ReadFileContext should report a stored file")
	require.Equal(t, int64(buf.Len()), meta.CompressedBytes, "Encode should return compressed size metadata")
	require.Equal(t, []int{2, 3, 4}, []int{got.Lines[0].Number, got.Lines[1].Number, got.Lines[2].Number}, "ReadFileContext should return the requested line window")
	require.Equal(t, "", got.Lines[0].Content, "ReadFileContext should preserve empty lines")
	require.Equal(t, 4, got.TotalLines, "ReadFileContext should report total lines")
	require.True(t, got.HasMoreAbove, "ReadFileContext should report lines above")
	require.False(t, got.HasMoreBelow, "ReadFileContext should report no lines below")

	_, ok, err = reader.ReadFileContext(context.Background(), "artifact-key", "missing.ts", 1, 0, 0)
	require.NoError(t, err, "missing artifact file should not be an error")
	require.False(t, ok, "missing artifact file should fall back to the workspace reader")
}

type memoryStore struct {
	saved   map[string][]byte
	deleted []string
	saveErr error
	loadErr error
}

func newMemoryStore() *memoryStore {
	return &memoryStore{saved: map[string][]byte{}}
}

func (s *memoryStore) Save(_ context.Context, key string, reader io.Reader) error {
	if s.saveErr != nil {
		return s.saveErr
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	s.saved[key] = body
	return nil
}

func (s *memoryStore) Load(_ context.Context, key string, writer io.Writer) error {
	if s.loadErr != nil {
		return s.loadErr
	}
	body, ok := s.saved[key]
	if !ok {
		return errors.New("missing")
	}
	_, err := writer.Write(body)
	return err
}

func (s *memoryStore) Delete(_ context.Context, key string) error {
	s.deleted = append(s.deleted, key)
	delete(s.saved, key)
	return nil
}

func execFromMap(files map[string]string) ExecFunc {
	return func(_ context.Context, cmd string, stdout, stderr io.Writer) (int, error) {
		for path, content := range files {
			if strings.Contains(cmd, path) {
				_, _ = io.WriteString(stdout, content)
				return 0, nil
			}
		}
		_, _ = io.WriteString(stderr, "reviewartifact:missing")
		return 2, nil
	}
}

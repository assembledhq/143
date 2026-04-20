package sandbox

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestNoOpFileReader_ReturnsErrFileNotFound pins the contract documented on
// NoOpFileReader and relied on by PreviewHandler.readWorkspacePreviewConfig:
// every method must wrap ErrFileNotFound so callers using errors.Is can treat
// the no-Docker case as "no file" and fall through to defaults instead of
// surfacing a 500.
func TestNoOpFileReader_ReturnsErrFileNotFound(t *testing.T) {
	t.Parallel()

	r := NoOpFileReader{}
	ctx := context.Background()

	_, listErr := r.ListDir(ctx, "c", "/w", ".")
	require.ErrorIs(t, listErr, ErrFileNotFound, "ListDir must wrap ErrFileNotFound")

	_, _, readErr := r.ReadFile(ctx, "c", "/w", "f")
	require.ErrorIs(t, readErr, ErrFileNotFound, "ReadFile must wrap ErrFileNotFound")

	_, ctxErr := r.ReadFileContext(ctx, "c", "/w", "f", 1, 0, 0)
	require.ErrorIs(t, ctxErr, ErrFileNotFound, "ReadFileContext must wrap ErrFileNotFound")
}

func TestResolvePathInWorkDir(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		workDir  string
		relPath  string
		expected string
	}{
		{
			name:     "empty path returns workDir",
			workDir:  "/workspace",
			relPath:  "",
			expected: "/workspace",
		},
		{
			name:     "dot path returns workDir",
			workDir:  "/workspace",
			relPath:  ".",
			expected: "/workspace",
		},
		{
			name:     "simple relative path",
			workDir:  "/workspace",
			relPath:  "src/main.go",
			expected: "/workspace/src/main.go",
		},
		{
			name:     "absolute path is treated as relative",
			workDir:  "/workspace",
			relPath:  "/src/main.go",
			expected: "/workspace/src/main.go",
		},
		{
			name:     "traversal attempt is blocked",
			workDir:  "/workspace",
			relPath:  "../../etc/passwd",
			expected: "/workspace",
		},
		{
			name:     "nested traversal attempt is blocked",
			workDir:  "/workspace",
			relPath:  "src/../../etc/passwd",
			expected: "/workspace",
		},
		{
			name:     "path with dot-dot that stays within workDir",
			workDir:  "/workspace",
			relPath:  "src/../lib/utils.go",
			expected: "/workspace/lib/utils.go",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := resolvePathInWorkDir(tt.workDir, tt.relPath)
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestValidateExecPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{"simple path is safe", "/workspace/src/main.go", false},
		{"path with spaces is safe", "/workspace/my dir/file.go", false},
		{"path with backtick is unsafe", "/workspace/$(whoami)/file.go", true},
		{"path with pipe is unsafe", "/workspace/file|evil", true},
		{"path with semicolon is unsafe", "/workspace/file;rm -rf /", true},
		{"path with backtick is unsafe", "/workspace/`whoami`", true},
		{"path with single quote is unsafe", "/workspace/it's", true},
		{"path with double quote is unsafe", "/workspace/he\"llo", true},
		{"normal nested path is safe", "/workspace/src/components/App.tsx", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateExecPath(tt.path)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

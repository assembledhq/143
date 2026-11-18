package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReadRepositoryFile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		rootPath string
		filePath string
		relative bool
		link     string
		wantErr  bool
	}{
		{name: "absolute repository paths", rootPath: "root", filePath: "root/nested/value.txt"},
		{name: "relative repository paths", rootPath: "root", filePath: "root/nested/value.txt", relative: true},
		{name: "normalized path inside repository", rootPath: "root", filePath: "root/nested/../nested/value.txt"},
		{name: "internal relative symlink", rootPath: "root", filePath: "root/link", link: "nested/value.txt"},
		{name: "outside repository path", rootPath: "root", filePath: "outside.txt", wantErr: true},
		{name: "relative outside repository path", rootPath: "root", filePath: "outside.txt", relative: true, wantErr: true},
		{name: "relative symlink escape", rootPath: "root", filePath: "root/link", link: "../outside.txt", wantErr: true},
		{name: "absolute symlink escape", rootPath: "root", filePath: "root/link", link: "absolute outside", wantErr: true},
		{name: "missing file", rootPath: "root", filePath: "root/missing.txt", wantErr: true},
		{name: "missing repository", rootPath: "missing", filePath: "missing/value.txt", wantErr: true},
		{name: "repository is a file", rootPath: "outside.txt", filePath: "outside.txt/value.txt", wantErr: true},
		{name: "cannot read a directory", rootPath: "root", filePath: "root/nested", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			base := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(base, "root", "nested"), 0o700), "fixture should create an isolated repository tree")
			expected := []byte("repository fixture\n")
			require.NoError(t, os.WriteFile(filepath.Join(base, "root", "nested", "value.txt"), expected, 0o600), "fixture should create the exact in-repository contents")
			require.NoError(t, os.WriteFile(filepath.Join(base, "outside.txt"), []byte("outside repository\n"), 0o600), "fixture should create an outside file so escapes cannot appear to fail only because of absence")
			if tt.link != "" {
				target := tt.link
				if target == "absolute outside" {
					target = filepath.Join(base, "outside.txt")
				}
				require.NoError(t, os.Symlink(target, filepath.Join(base, "root", "link")), "fixture should create the requested symlink without changing shared process state")
			}
			root := filepath.Join(base, filepath.FromSlash(tt.rootPath))
			path := filepath.Join(base, filepath.FromSlash(tt.filePath))
			if tt.relative {
				cwd, err := os.Getwd()
				require.NoError(t, err, "relative path fixture should discover the working directory without changing it")
				root, err = filepath.Rel(cwd, root)
				require.NoError(t, err, "relative root should refer to the same isolated repository")
				path, err = filepath.Rel(cwd, path)
				require.NoError(t, err, "relative file should refer to the same isolated target")
			}
			actual, err := readRepositoryFile(root, path)
			if tt.wantErr {
				require.Error(t, err, "outside, missing, and non-file targets must fail closed")
				require.Nil(t, actual, "failed reads must not expose partial or outside contents")
				return
			}
			require.NoError(t, err, "ordinary repository paths and internal relative symlinks should remain readable")
			require.Equal(t, expected, actual, "confined reads should return the exact in-repository contents")
		})
	}
}

func TestRepositoryReadersRejectEscapes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		filePath string
		target   string
		contents string
		read     func(string) error
	}{
		{
			name: "lifecycle manifest traversal", contents: `{"version":1,"scope":["fixture"],"limitations":["syntax only"],"entries":[]}`,
			read: func(root string) error { return run(root, "../outside", false, io.Discard) },
		},
		{
			name: "jobs status manifest traversal", contents: `{"version":1,"scope":["fixture"],"limitations":["syntax only"],"entries":[]}`,
			read: func(root string) error { return runJobStatus(root, "../outside", false, io.Discard) },
		},
		{
			name: "lifecycle source symlink", filePath: "internal/escape.go", target: "../../outside", contents: "package fixture\nfunc unrelated() {}\n",
			read: func(root string) error { _, err := scan(root); return err },
		},
		{
			name: "jobs status source symlink", filePath: "internal/escape.go", target: "../../outside", contents: "package fixture\nfunc unrelated() {}\n",
			read: func(root string) error { _, err := scanJobStatus(root); return err },
		},
		{
			name: "lifecycle migration symlink", filePath: "migrations/escape.sql", target: "../../outside", contents: "SELECT 1;\n",
			read: func(root string) error { _, err := scan(root); return err },
		},
		{
			name: "jobs status migration symlink", filePath: "migrations/escape.sql", target: "../../outside", contents: "SELECT 1;\n",
			read: func(root string) error { _, err := scanJobStatus(root); return err },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			base := t.TempDir()
			root := filepath.Join(base, "repository")
			for _, directory := range []string{"cmd", "internal", "migrations"} {
				require.NoError(t, os.MkdirAll(filepath.Join(root, directory), 0o700), "each reader fixture should have an isolated valid repository layout")
			}
			require.NoError(t, os.WriteFile(filepath.Join(base, "outside"), []byte(tt.contents), 0o600), "outside contents should be valid input so rejection proves confinement rather than parsing failure")
			if tt.filePath != "" {
				require.NoError(t, os.Symlink(tt.target, filepath.Join(root, filepath.FromSlash(tt.filePath))), "scanner fixture should link from a discovered source path to outside the repository")
			}
			require.ErrorContains(t, tt.read(root), "read repository file:", "every manifest, source, and migration reader must reject outside files through the shared confined read")
		})
	}
}

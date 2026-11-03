package providers

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateNPMSetupArchive(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		headers   []tar.Header
		manifests []string
		wantError string
	}{
		{
			name: "ordinary dependency files and directories",
			headers: []tar.Header{
				{Name: "node_modules/", Typeflag: tar.TypeDir},
				{Name: "node_modules/@fixture/dependency/package.json", Typeflag: tar.TypeReg, Size: 2},
				{Name: "node_modules/@fixture/dependency/index.js", Typeflag: tar.TypeReg, Size: 2},
			},
			manifests: []string{"package.json"},
		},
		{
			name: "nested workspace dependencies",
			headers: []tar.Header{
				{Name: "packages/widget/node_modules/dependency/index.js", Typeflag: tar.TypeReg},
			},
			manifests: []string{"package.json", "packages/widget/package.json"},
		},
		{
			name: "workspace and executable symlinks remain inside repository",
			headers: []tar.Header{
				{Name: "node_modules/@fixture/widget", Typeflag: tar.TypeSymlink, Linkname: "../../packages/widget"},
				{Name: "node_modules/.bin/fixture", Typeflag: tar.TypeSymlink, Linkname: "../fixture/bin/cli.js"},
			},
			manifests: []string{"package.json", "packages/widget/package.json"},
		},
		{
			name: "source file cannot be published",
			headers: []tar.Header{
				{Name: "src/index.js", Typeflag: tar.TypeReg},
			},
			manifests: []string{"package.json"},
			wantError: "npm setup output contains non-dependency files",
		},
		{
			name: "authentication configuration cannot be published",
			headers: []tar.Header{
				{Name: ".npmrc", Typeflag: tar.TypeReg},
			},
			manifests: []string{"package.json"},
			wantError: "npm setup output contains non-dependency files",
		},
		{
			name: "parent traversal",
			headers: []tar.Header{
				{Name: "../node_modules/dependency/index.js", Typeflag: tar.TypeReg},
			},
			manifests: []string{"package.json"},
			wantError: "npm setup output path invalid",
		},
		{
			name: "embedded parent traversal",
			headers: []tar.Header{
				{Name: "node_modules/../package.json", Typeflag: tar.TypeReg},
			},
			manifests: []string{"package.json"},
			wantError: "npm setup output path invalid",
		},
		{
			name: "absolute archive path",
			headers: []tar.Header{
				{Name: "/node_modules/dependency/index.js", Typeflag: tar.TypeReg},
			},
			manifests: []string{"package.json"},
			wantError: "npm setup output path invalid",
		},
		{
			name: "dependency directory requires its own parent manifest",
			headers: []tar.Header{
				{Name: "packages/undeclared/node_modules/dependency/index.js", Typeflag: tar.TypeReg},
			},
			manifests: []string{"package.json", "packages/widget/package.json"},
			wantError: "npm setup output parent has no manifest",
		},
		{
			name: "lockfile alone does not authorize dependency output",
			headers: []tar.Header{
				{Name: "node_modules/dependency/index.js", Typeflag: tar.TypeReg},
			},
			manifests: []string{"package-lock.json"},
			wantError: "npm setup output parent has no manifest",
		},
		{
			name: "hard links are rejected even with an internal target",
			headers: []tar.Header{
				{Name: "node_modules/dependency/copy.js", Typeflag: tar.TypeLink, Linkname: "node_modules/dependency/index.js"},
			},
			manifests: []string{"package.json"},
			wantError: "npm setup output contains unsupported files",
		},
		{
			name: "device files cannot be published",
			headers: []tar.Header{
				{Name: "node_modules/dependency/device", Typeflag: tar.TypeChar},
			},
			manifests: []string{"package.json"},
			wantError: "npm setup output contains unsupported files",
		},
		{
			name: "named pipes cannot be published",
			headers: []tar.Header{
				{Name: "node_modules/dependency/pipe", Typeflag: tar.TypeFifo},
			},
			manifests: []string{"package.json"},
			wantError: "npm setup output contains unsupported files",
		},
		{
			name: "symlink cannot target an absolute path",
			headers: []tar.Header{
				{Name: "node_modules/dependency", Typeflag: tar.TypeSymlink, Linkname: "/outside"},
			},
			manifests: []string{"package.json"},
			wantError: "npm setup output link escapes repository",
		},
		{
			name: "symlink cannot target repository parent",
			headers: []tar.Header{
				{Name: "node_modules/dependency", Typeflag: tar.TypeSymlink, Linkname: "../.."},
			},
			manifests: []string{"package.json"},
			wantError: "npm setup output link escapes repository",
		},
		{
			name: "nested symlink cannot escape repository",
			headers: []tar.Header{
				{Name: "packages/widget/node_modules/dependency", Typeflag: tar.TypeSymlink, Linkname: "../../../../outside"},
			},
			manifests: []string{"package.json", "packages/widget/package.json"},
			wantError: "npm setup output link escapes repository",
		},
		{
			name: "oversized file header is rejected before reading payload",
			headers: []tar.Header{
				{Name: "node_modules/dependency/index.js", Typeflag: tar.TypeReg, Size: 1<<30 + 1},
			},
			manifests: []string{"package.json"},
			wantError: "npm setup output exceeds capacity",
		},
		{
			name:      "empty archive cannot be published",
			manifests: []string{"package.json"},
			wantError: "npm setup output is empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			files := make(map[string]string, len(tt.manifests))
			for _, name := range tt.manifests {
				files[name] = `{}`
			}
			file := newNPMSetupArchiveFixture(t, tt.headers)

			err := validateNPMSetupArchive(file, files)
			if tt.wantError != "" {
				require.EqualError(t, err, tt.wantError, "unsafe dependency output should be rejected before publication")
				return
			}
			require.NoError(t, err, "ordinary npm dependency output and repository-local links should be accepted")
		})
	}
}

func TestValidateNPMSetupArchiveMalformed(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		data []byte
	}{
		{name: "non-archive output", data: []byte("synthetic installer output")},
		{name: "incomplete tar header", data: make([]byte, 511)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			file := newNPMSetupArchiveFile(t, bytes.NewReader(tt.data))
			err := validateNPMSetupArchive(file, map[string]string{"package.json": `{}`})
			require.EqualError(t, err, "npm setup output archive invalid", "malformed install output must not be accepted as an empty dependency tree")
		})
	}
}

func newNPMSetupArchiveFixture(t *testing.T, headers []tar.Header) *os.File {
	t.Helper()
	var data bytes.Buffer
	writer := tar.NewWriter(&data)
	for _, header := range headers {
		header.Mode = 0o644
		require.NoError(t, writer.WriteHeader(&header), "synthetic archive header should serialize")
		if header.Size > 1<<30 {
			// Deliberately omit the huge body: validation must reject its header first.
			break
		}
		if header.Size > 0 {
			_, err := writer.Write(bytes.Repeat([]byte("x"), int(header.Size)))
			require.NoError(t, err, "synthetic dependency file contents should serialize")
		}
	}
	if len(headers) == 0 || headers[len(headers)-1].Size <= 1<<30 {
		require.NoError(t, writer.Close(), "synthetic archive should be complete")
	}
	return newNPMSetupArchiveFile(t, &data)
}

func newNPMSetupArchiveFile(t *testing.T, data io.Reader) *os.File {
	t.Helper()
	file, err := os.Create(filepath.Join(t.TempDir(), "dependencies.tar"))
	require.NoError(t, err, "synthetic dependency archive should be created")
	t.Cleanup(func() {
		require.NoError(t, file.Close(), "synthetic dependency archive should close")
	})
	_, err = io.Copy(file, data)
	require.NoError(t, err, "synthetic dependency archive should be written")
	return file
}

package sandbox

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/stretchr/testify/require"
)

// mockDockerClient implements DockerClient for testing.
type mockDockerClient struct {
	createResp container.ExecCreateResponse
	createErr  error

	attachResp types.HijackedResponse
	attachErr  error

	inspectResp container.ExecInspect
	inspectErr  error

	createResps  []container.ExecCreateResponse
	attachResps  []types.HijackedResponse
	inspectResps []container.ExecInspect
	callIndex    int

	// lastExecOptions captures the last ExecOptions passed to
	// ContainerExecCreate so tests can assert on Env, Cmd, etc.
	lastExecOptions container.ExecOptions
}

func (m *mockDockerClient) ContainerExecCreate(_ context.Context, _ string, cfg container.ExecOptions) (container.ExecCreateResponse, error) {
	m.lastExecOptions = cfg
	if len(m.createResps) > 0 {
		resp := m.createResps[m.callIndex]
		return resp, m.createErr
	}
	return m.createResp, m.createErr
}

func (m *mockDockerClient) ContainerExecAttach(_ context.Context, _ string, _ container.ExecAttachOptions) (types.HijackedResponse, error) {
	if len(m.attachResps) > 0 {
		return m.attachResps[m.callIndex], m.attachErr
	}
	return m.attachResp, m.attachErr
}

func (m *mockDockerClient) ContainerExecInspect(_ context.Context, _ string) (container.ExecInspect, error) {
	if len(m.inspectResps) > 0 {
		resp := m.inspectResps[m.callIndex]
		m.callIndex++
		return resp, m.inspectErr
	}
	return m.inspectResp, m.inspectErr
}

// dockerStdoutFrame builds a Docker multiplexed stream frame for stdout (stream type 1).
// The Docker stdcopy protocol uses an 8-byte header: [stream_type, 0, 0, 0, size(4 big-endian)].
func dockerStdoutFrame(data string) []byte {
	payload := []byte(data)
	header := make([]byte, 8)
	header[0] = 1 // stdout
	binary.BigEndian.PutUint32(header[4:], uint32(len(payload)))
	return append(header, payload...)
}

// dockerStderrFrame builds a Docker multiplexed stream frame for stderr (stream type 2).
func dockerStderrFrame(data string) []byte {
	payload := []byte(data)
	header := make([]byte, 8)
	header[0] = 2 // stderr
	binary.BigEndian.PutUint32(header[4:], uint32(len(payload)))
	return append(header, payload...)
}

// mockConn implements net.Conn for testing HijackedResponse.
type mockConn struct {
	net.Conn
	reader *bytes.Reader
	closed bool
}

func newMockConn(data []byte) *mockConn {
	return &mockConn{reader: bytes.NewReader(data)}
}

func (c *mockConn) Read(p []byte) (int, error)         { return c.reader.Read(p) }
func (c *mockConn) Write(p []byte) (int, error)        { return len(p), nil }
func (c *mockConn) Close() error                       { c.closed = true; return nil }
func (c *mockConn) LocalAddr() net.Addr                { return nil }
func (c *mockConn) RemoteAddr() net.Addr               { return nil }
func (c *mockConn) SetDeadline(_ time.Time) error      { return nil }
func (c *mockConn) SetReadDeadline(_ time.Time) error  { return nil }
func (c *mockConn) SetWriteDeadline(_ time.Time) error { return nil }

// newHijackedResponse creates a HijackedResponse from raw bytes (Docker-multiplexed).
func newHijackedResponse(data []byte) types.HijackedResponse {
	conn := newMockConn(data)
	return types.HijackedResponse{
		Conn:   conn,
		Reader: bufio.NewReader(conn),
	}
}

// newMockClient creates a mock Docker client that returns the given stdout with exit code 0.
func newMockClient(stdout string, exitCode int) *mockDockerClient {
	var buf bytes.Buffer
	buf.Write(dockerStdoutFrame(stdout))

	return &mockDockerClient{
		createResp: container.ExecCreateResponse{ID: "exec-123"},
		attachResp: newHijackedResponse(buf.Bytes()),
		inspectResp: container.ExecInspect{
			ExitCode: exitCode,
		},
	}
}

// newMockClientWithStderr creates a mock Docker client that returns stderr with a non-zero exit code.
func newMockClientWithStderr(stderr string, exitCode int) *mockDockerClient {
	var buf bytes.Buffer
	buf.Write(dockerStderrFrame(stderr))

	return &mockDockerClient{
		createResp: container.ExecCreateResponse{ID: "exec-123"},
		attachResp: newHijackedResponse(buf.Bytes()),
		inspectResp: container.ExecInspect{
			ExitCode: exitCode,
		},
	}
}

func newMockSequenceClient(stdouts []string, exitCodes []int) *mockDockerClient {
	createResps := make([]container.ExecCreateResponse, len(stdouts))
	attachResps := make([]types.HijackedResponse, len(stdouts))
	inspectResps := make([]container.ExecInspect, len(stdouts))
	for i := range stdouts {
		var buf bytes.Buffer
		buf.Write(dockerStdoutFrame(stdouts[i]))
		createResps[i] = container.ExecCreateResponse{ID: fmt.Sprintf("exec-%d", i)}
		attachResps[i] = newHijackedResponse(buf.Bytes())
		inspectResps[i] = container.ExecInspect{ExitCode: exitCodes[i]}
	}
	return &mockDockerClient{
		createResps:  createResps,
		attachResps:  attachResps,
		inspectResps: inspectResps,
	}
}

func TestDockerFileReader_ListDir(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		client    *mockDockerClient
		dirPath   string
		expected  []FileEntry
		expectErr bool
	}{
		{
			name: "parses ls output with dirs and files",
			client: newMockClient(
				"src/\nmain.go\nREADME.md\n",
				0,
			),
			dirPath: ".",
			expected: []FileEntry{
				{Path: "src", Type: "dir", Size: 0},
				{Path: "main.go", Type: "file", Size: 0},
				{Path: "README.md", Type: "file", Size: 0},
			},
		},
		{
			name: "constructs relative paths with parent dir",
			client: newMockClient(
				"utils.go\n",
				0,
			),
			dirPath: "src/lib",
			expected: []FileEntry{
				{Path: "src/lib/utils.go", Type: "file", Size: 0},
			},
		},
		{
			name:      "returns error on non-zero exit code",
			client:    newMockClientWithStderr("not found", 1),
			dirPath:   "nonexistent",
			expectErr: true,
		},
		{
			name:      "returns error on exec create failure",
			dirPath:   ".",
			expectErr: true,
			client: &mockDockerClient{
				createErr: fmt.Errorf("container not found"),
			},
		},
		{
			name:    "returns empty list for empty directory",
			client:  newMockClient("", 0),
			dirPath: ".",
		},
		{
			name:      "returns error for unsafe path characters",
			client:    newMockClient("", 0),
			dirPath:   "src;rm -rf /",
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reader := NewDockerFileReader(tt.client)
			entries, err := reader.ListDir(context.Background(), "container-1", "/workspace", tt.dirPath)
			if tt.expectErr {
				require.Error(t, err, "ListDir should return an error")
				return
			}
			require.NoError(t, err, "ListDir should not return an error")
			require.Equal(t, tt.expected, entries, "ListDir should return the expected entries")
		})
	}
}

// TestRecursiveFindScript_RealShell runs the actual script through a real
// shell against a temp tree, since the Docker-level tests only assert against
// mocked exec output. Guards the busybox-portable pieces: positional arg
// handling after shift, prune args via "$@", literal-tab sed labels, and the
// head-based entry cap.
func TestRecursiveFindScript_RealShell(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available on this host")
	}

	root := t.TempDir()
	for _, dir := range []string{"docs", "src", "node_modules/pkg"} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, dir), 0o755), "test tree directories should be created")
	}
	for _, file := range []string{"README.md", "docs/guide.md", "src/app.ts", "node_modules/pkg/index.js"} {
		require.NoError(t, os.WriteFile(filepath.Join(root, file), nil, 0o644), "test tree files should be created")
	}

	run := func(maxEntries int) []FileEntry {
		argv := recursiveFindArgv(root, []string{"node_modules", ".git"}, maxEntries)
		out, err := exec.Command(argv[0], argv[1:]...).Output()
		require.NoError(t, err, "recursive find script should exit zero")
		return appendFindEntries(nil, root, string(out), maxEntries)
	}

	unlimited := run(0)
	byPath := map[string]string{}
	for _, entry := range unlimited {
		byPath[entry.Path] = entry.Type
	}
	require.Equal(t, map[string]string{
		"docs":          "dir",
		"src":           "dir",
		"README.md":     "file",
		"docs/guide.md": "file",
		"src/app.ts":    "file",
	}, byPath, "the script should label kinds, exclude the root itself, and prune ignored directories")

	capped := run(3)
	require.Len(t, capped, 3, "the head-based cap should bound the entry count")
	for _, entry := range capped {
		require.NotContains(t, entry.Path, "node_modules", "pruned directories should never appear in capped output")
	}
}

func TestDockerFileReader_ListDirRecursive(t *testing.T) {
	t.Parallel()

	client := newMockSequenceClient([]string{
		"dir\t/workspace/docs\nfile\t/workspace/README.md\nfile\t/workspace/docs/guide.md\ndir\t/workspace/src\nfile\t/workspace/src/app.ts\n",
	}, []int{0})

	reader := NewDockerFileReader(client)
	entries, err := reader.ListDirRecursive(context.Background(), "container-1", "/workspace", 3, []string{"node_modules", ".git"})
	require.NoError(t, err, "ListDirRecursive should not return an error")
	require.Equal(t, []FileEntry{
		{Path: "docs", Type: "dir", Size: 0},
		{Path: "README.md", Type: "file", Size: 0},
		{Path: "docs/guide.md", Type: "file", Size: 0},
	}, entries, "ListDirRecursive should parse workspace-relative entries and enforce the requested cap defensively")
	require.Equal(t, 1, client.callIndex, "ListDirRecursive should use one Docker exec call")
	require.Contains(t, client.lastExecOptions.Cmd, "3", "recursive find should receive the requested max entry cap")
	require.Contains(t, client.lastExecOptions.Cmd, "node_modules", "recursive find should prune ignored directory names")
}

func TestDockerFileReader_ReadFile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		client          *mockDockerClient
		filePath        string
		expected        string
		expectTruncated bool
		expectErr       bool
	}{
		{
			name:     "returns file contents",
			client:   newMockClient("package main\n\nfunc main() {}\n", 0),
			filePath: "main.go",
			expected: "package main\n\nfunc main() {}\n",
		},
		{
			name:      "returns error on non-zero exit code",
			client:    newMockClientWithStderr("No such file or directory", 1),
			filePath:  "missing.go",
			expectErr: true,
		},
		{
			name:      "returns error on exec create failure",
			filePath:  "main.go",
			expectErr: true,
			client: &mockDockerClient{
				createErr: fmt.Errorf("container not found"),
			},
		},
		{
			name:      "returns error for unsafe path",
			client:    newMockClient("", 0),
			filePath:  "file`whoami`",
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reader := NewDockerFileReader(tt.client)
			content, truncated, err := reader.ReadFile(context.Background(), "container-1", "/workspace", tt.filePath)
			if tt.expectErr {
				require.Error(t, err, "ReadFile should return an error")
				return
			}
			require.NoError(t, err, "ReadFile should not return an error")
			require.Equal(t, tt.expected, content, "ReadFile should return the expected content")
			require.Equal(t, tt.expectTruncated, truncated, "ReadFile truncation flag should match")
		})
	}
}

// TestDockerFileReader_ReadFile_NotFoundSentinel verifies that ENOENT stderr
// surfaces as ErrFileNotFound so callers can errors.Is against it instead of
// pattern-matching stderr text themselves.
func TestDockerFileReader_ReadFile_NotFoundSentinel(t *testing.T) {
	t.Parallel()

	client := newMockClientWithStderr("head: cannot open '/workspace/missing' for reading: No such file or directory", 1)
	reader := NewDockerFileReader(client)
	_, _, err := reader.ReadFile(context.Background(), "container-1", "/workspace", "missing")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrFileNotFound, "ENOENT from head must be surfaced as ErrFileNotFound")
}

// TestDockerFileReader_ReadFile_NonNotFoundNotSentinel ensures unrelated exec
// failures (non-ENOENT stderr) do NOT get miscategorized as ErrFileNotFound.
func TestDockerFileReader_ReadFile_NonNotFoundNotSentinel(t *testing.T) {
	t.Parallel()

	client := newMockClientWithStderr("head: permission denied", 1)
	reader := NewDockerFileReader(client)
	_, _, err := reader.ReadFile(context.Background(), "container-1", "/workspace", "locked")
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrFileNotFound, "non-ENOENT errors must not be classified as file-not-found")
}

// TestDockerFileReader_ReadFileContext_NotFoundSentinel verifies that ENOENT
// stderr from `sed` (used by ReadFileContext) surfaces as ErrFileNotFound,
// matching ReadFile's behavior so callers can use a single sentinel.
func TestDockerFileReader_ReadFileContext_NotFoundSentinel(t *testing.T) {
	t.Parallel()

	client := newMockClientWithStderr("sed: can't read /workspace/missing: No such file or directory", 1)
	reader := NewDockerFileReader(client)
	_, err := reader.ReadFileContext(context.Background(), "container-1", "/workspace", "missing", 5, 2, 2)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrFileNotFound, "ENOENT from sed must be surfaced as ErrFileNotFound")
}

// TestDockerFileReader_ForcesCLocale guards isNotFoundStderr: if the exec
// environment ever stops pinning LC_ALL=C, a non-English container locale
// would emit a translated ENOENT that our matcher would miss.
func TestDockerFileReader_ForcesCLocale(t *testing.T) {
	t.Parallel()

	client := newMockClient("ok\n", 0)
	reader := NewDockerFileReader(client)
	_, _, err := reader.ReadFile(context.Background(), "container-1", "/workspace", "f")
	require.NoError(t, err)
	require.Contains(t, client.lastExecOptions.Env, "LC_ALL=C", "exec must pin LC_ALL=C so ENOENT stderr stays in English")
	require.Contains(t, client.lastExecOptions.Env, "LANG=C", "exec must pin LANG=C as a belt-and-braces fallback")
}

func TestDockerFileReader_ReadFileContext(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		client           *mockDockerClient
		filePath         string
		line             int
		above            int
		below            int
		expected         []FileLine
		expectedTotal    int
		expectedHasAbove bool
		expectedHasBelow bool
		expectErr        bool
	}{
		{
			name:     "extracts line range",
			client:   newMockSequenceClient([]string{"line 8\nline 9\nline 10\nline 11\nline 12\n", "12 /workspace/main.go\n"}, []int{0, 0}),
			filePath: "main.go",
			line:     10,
			above:    2,
			below:    2,
			expected: []FileLine{
				{Number: 8, Content: "line 8"},
				{Number: 9, Content: "line 9"},
				{Number: 10, Content: "line 10"},
				{Number: 11, Content: "line 11"},
				{Number: 12, Content: "line 12"},
			},
			expectedTotal:    12,
			expectedHasAbove: true,
		},
		{
			name:     "clamps start line to 1",
			client:   newMockSequenceClient([]string{"line 1\nline 2\nline 3\n", "3 /workspace/main.go\n"}, []int{0, 0}),
			filePath: "main.go",
			line:     2,
			above:    5,
			below:    1,
			expected: []FileLine{
				{Number: 1, Content: "line 1"},
				{Number: 2, Content: "line 2"},
				{Number: 3, Content: "line 3"},
			},
			expectedTotal:    3,
			expectedHasBelow: false,
		},
		{
			name:     "counts final line without trailing newline",
			client:   newMockSequenceClient([]string{"line 2\nline 3", "3\n"}, []int{0, 0}),
			filePath: "main.go",
			line:     2,
			above:    0,
			below:    1,
			expected: []FileLine{
				{Number: 2, Content: "line 2"},
				{Number: 3, Content: "line 3"},
			},
			expectedTotal:    3,
			expectedHasAbove: true,
			expectedHasBelow: false,
		},
		{
			name:      "returns error for invalid path",
			client:    newMockClient("", 0),
			filePath:  "bad;rm -rf /",
			line:      1,
			above:     0,
			below:     0,
			expectErr: true,
		},
		{
			name:      "returns error on non-zero exit code",
			client:    newMockClientWithStderr("No such file", 1),
			filePath:  "missing.go",
			line:      10,
			above:     2,
			below:     2,
			expectErr: true,
		},
		{
			name: "returns error when line count exec fails",
			client: &mockDockerClient{
				createResps: []container.ExecCreateResponse{
					{ID: "exec-0"},
					{ID: "exec-1"},
				},
				attachResps: []types.HijackedResponse{
					newHijackedResponse(dockerStdoutFrame("line 1\n")),
				},
				inspectResps: []container.ExecInspect{
					{ExitCode: 0},
				},
				attachErr: fmt.Errorf("awk failed"),
			},
			filePath:  "main.go",
			line:      1,
			above:     0,
			below:     0,
			expectErr: true,
		},
		{
			name:      "returns error when line count exits non zero",
			client:    newMockSequenceClient([]string{"line 1\n", "missing"}, []int{0, 1}),
			filePath:  "main.go",
			line:      1,
			above:     0,
			below:     0,
			expectErr: true,
		},
		{
			name:      "returns error when line count cannot be parsed",
			client:    newMockSequenceClient([]string{"line 1\n", "not-a-number"}, []int{0, 0}),
			filePath:  "main.go",
			line:      1,
			above:     0,
			below:     0,
			expectErr: true,
		},
		{
			name:     "returns error on exec create failure",
			filePath: "main.go",
			line:     10,
			above:    2,
			below:    2,
			client: &mockDockerClient{
				createErr: fmt.Errorf("container not found"),
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reader := NewDockerFileReader(tt.client)
			result, err := reader.ReadFileContext(context.Background(), "container-1", "/workspace", tt.filePath, tt.line, tt.above, tt.below)
			if tt.expectErr {
				require.Error(t, err, "ReadFileContext should return an error")
				return
			}
			require.NoError(t, err, "ReadFileContext should not return an error")
			require.Equal(t, tt.expected, result.Lines, "ReadFileContext should return the expected lines")
			require.Equal(t, tt.expected[0].Number, result.StartLine, "ReadFileContext should report the returned start line")
			require.Equal(t, tt.expected[len(tt.expected)-1].Number, result.EndLine, "ReadFileContext should report the returned end line")
			require.Equal(t, tt.expectedTotal, result.TotalLines, "ReadFileContext should report the file's total logical line count")
			require.Equal(t, tt.expectedHasAbove, result.HasMoreAbove, "ReadFileContext should report whether more lines exist above the window")
			require.Equal(t, tt.expectedHasBelow, result.HasMoreBelow, "ReadFileContext should report whether more lines exist below the window")
		})
	}
}

package sandbox

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"net"
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

	// lastExecOptions captures the last ExecOptions passed to
	// ContainerExecCreate so tests can assert on Env, Cmd, etc.
	lastExecOptions container.ExecOptions
}

func (m *mockDockerClient) ContainerExecCreate(_ context.Context, _ string, cfg container.ExecOptions) (container.ExecCreateResponse, error) {
	m.lastExecOptions = cfg
	return m.createResp, m.createErr
}

func (m *mockDockerClient) ContainerExecAttach(_ context.Context, _ string, _ container.ExecAttachOptions) (types.HijackedResponse, error) {
	return m.attachResp, m.attachErr
}

func (m *mockDockerClient) ContainerExecInspect(_ context.Context, _ string) (container.ExecInspect, error) {
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
		name      string
		client    *mockDockerClient
		filePath  string
		line      int
		above     int
		below     int
		expected  []FileLine
		expectErr bool
	}{
		{
			name:     "extracts line range",
			client:   newMockClient("line 8\nline 9\nline 10\nline 11\nline 12\n", 0),
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
		},
		{
			name:     "clamps start line to 1",
			client:   newMockClient("line 1\nline 2\nline 3\n", 0),
			filePath: "main.go",
			line:     2,
			above:    5,
			below:    1,
			expected: []FileLine{
				{Number: 1, Content: "line 1"},
				{Number: 2, Content: "line 2"},
				{Number: 3, Content: "line 3"},
			},
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
			lines, err := reader.ReadFileContext(context.Background(), "container-1", "/workspace", tt.filePath, tt.line, tt.above, tt.below)
			if tt.expectErr {
				require.Error(t, err, "ReadFileContext should return an error")
				return
			}
			require.NoError(t, err, "ReadFileContext should not return an error")
			require.Equal(t, tt.expected, lines, "ReadFileContext should return the expected lines")
		})
	}
}

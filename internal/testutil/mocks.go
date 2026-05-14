// Package testutil provides shared test mocks and helpers.
package testutil

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/assembledhq/143/internal/services/agent"
)

// Compile-time guards: the mock implements both the SandboxProvider contract
// and the optional InteractiveSandboxProvider capability so adapter tests
// can drive the live-handle path without instantiating a Docker daemon.
var (
	_ agent.SandboxProvider            = (*MockSandboxProvider)(nil)
	_ agent.InteractiveSandboxProvider = (*MockSandboxProvider)(nil)
)

// MockSandboxProvider is a configurable mock for agent.SandboxProvider.
// Set the function fields to control behavior in tests.
type MockSandboxProvider struct {
	Name_        string
	CreateFn     func(ctx context.Context, cfg agent.SandboxConfig) (*agent.Sandbox, error)
	CloneRepoFn  func(ctx context.Context, sb *agent.Sandbox, repoURL, branch, token string) error
	ExecFn       func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error)
	ExecStreamFn func(ctx context.Context, sb *agent.Sandbox, cmd string, onLine func(line []byte), stderr io.Writer) (int, error)
	ReadFileFn   func(ctx context.Context, sb *agent.Sandbox, path string) ([]byte, error)
	WriteFileFn  func(ctx context.Context, sb *agent.Sandbox, path string, data []byte) error
	DestroyFn    func(ctx context.Context, sb *agent.Sandbox) error
	ConnInfoFn   func(ctx context.Context, sb *agent.Sandbox) (*agent.SandboxConnectionInfo, error)
	SnapshotFn   func(ctx context.Context, sb *agent.Sandbox) (io.ReadCloser, error)
	RestoreFn    func(ctx context.Context, sb *agent.Sandbox, reader io.Reader) error
	IsAliveFn    func(ctx context.Context, sb *agent.Sandbox) (bool, error)

	// StartInteractiveCommandFn lets tests override the live-handle path.
	// When nil, StartInteractiveCommand returns a MockInteractiveCommandHandle
	// whose stream is driven by ExecStreamFn / ExecFn for backward
	// compatibility with adapter tests that assert against ExecCalls.
	StartInteractiveCommandFn func(ctx context.Context, sb *agent.Sandbox, spec agent.InteractiveCommandSpec) (agent.InteractiveCommandHandle, error)

	Files     map[string][]byte
	ExecCalls []string

	mu           sync.Mutex
	destroyCalls int
}

// NewMockSandboxProvider creates a MockSandboxProvider with sensible defaults.
func NewMockSandboxProvider() *MockSandboxProvider {
	return &MockSandboxProvider{
		Name_: "mock",
		Files: make(map[string][]byte),
	}
}

func (m *MockSandboxProvider) Name() string { return m.Name_ }

func (m *MockSandboxProvider) Create(ctx context.Context, cfg agent.SandboxConfig) (*agent.Sandbox, error) {
	if m.CreateFn != nil {
		return m.CreateFn(ctx, cfg)
	}
	return &agent.Sandbox{ID: "test-sandbox", Provider: m.Name_, WorkDir: cfg.WorkDir, HomeDir: cfg.HomeDir, Env: cloneEnv(cfg.Env)}, nil
}

func (m *MockSandboxProvider) CloneRepo(ctx context.Context, sb *agent.Sandbox, repoURL, branch, token string) error {
	if m.CloneRepoFn != nil {
		return m.CloneRepoFn(ctx, sb, repoURL, branch, token)
	}
	return nil
}

func cloneEnv(env map[string]string) map[string]string {
	if len(env) == 0 {
		return nil
	}
	out := make(map[string]string, len(env))
	for k, v := range env {
		out[k] = v
	}
	return out
}

func (m *MockSandboxProvider) Exec(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
	m.mu.Lock()
	m.ExecCalls = append(m.ExecCalls, cmd)
	m.mu.Unlock()
	if m.ExecFn != nil {
		return m.ExecFn(ctx, sb, cmd, stdout, stderr)
	}
	return 0, nil
}

func (m *MockSandboxProvider) ReadFile(ctx context.Context, sb *agent.Sandbox, path string) ([]byte, error) {
	if m.ReadFileFn != nil {
		return m.ReadFileFn(ctx, sb, path)
	}
	data, ok := m.Files[path]
	if !ok {
		return nil, fmt.Errorf("file not found: %s", path)
	}
	return data, nil
}

func (m *MockSandboxProvider) WriteFile(ctx context.Context, sb *agent.Sandbox, path string, data []byte) error {
	if m.WriteFileFn != nil {
		return m.WriteFileFn(ctx, sb, path, data)
	}
	m.Files[path] = data
	return nil
}

func (m *MockSandboxProvider) Destroy(ctx context.Context, sb *agent.Sandbox) error {
	m.mu.Lock()
	m.destroyCalls++
	m.mu.Unlock()
	if m.DestroyFn != nil {
		return m.DestroyFn(ctx, sb)
	}
	return nil
}

func (m *MockSandboxProvider) ConnectionInfo(ctx context.Context, sb *agent.Sandbox) (*agent.SandboxConnectionInfo, error) {
	if m.ConnInfoFn != nil {
		return m.ConnInfoFn(ctx, sb)
	}
	return &agent.SandboxConnectionInfo{Provider: m.Name_, SandboxID: sb.ID}, nil
}

func (m *MockSandboxProvider) Snapshot(ctx context.Context, sb *agent.Sandbox) (io.ReadCloser, error) {
	if m.SnapshotFn != nil {
		return m.SnapshotFn(ctx, sb)
	}
	return io.NopCloser(bytes.NewReader(nil)), nil
}

func (m *MockSandboxProvider) Restore(ctx context.Context, sb *agent.Sandbox, reader io.Reader) error {
	if m.RestoreFn != nil {
		return m.RestoreFn(ctx, sb, reader)
	}
	return nil
}

func (m *MockSandboxProvider) IsAlive(ctx context.Context, sb *agent.Sandbox) (bool, error) {
	if m.IsAliveFn != nil {
		return m.IsAliveFn(ctx, sb)
	}
	return true, nil
}

func (m *MockSandboxProvider) ExecStream(ctx context.Context, sb *agent.Sandbox, cmd string, onLine func(line []byte), stderr io.Writer) (int, error) {
	m.mu.Lock()
	m.ExecCalls = append(m.ExecCalls, cmd)
	m.mu.Unlock()
	if m.ExecStreamFn != nil {
		return m.ExecStreamFn(ctx, sb, cmd, onLine, stderr)
	}
	// Default: delegate to ExecFn if set, writing each line of stdout to onLine.
	if m.ExecFn != nil {
		var buf bytes.Buffer
		code, err := m.ExecFn(ctx, sb, cmd, &buf, stderr)
		if err != nil {
			return code, err
		}
		for _, line := range bytes.Split(buf.Bytes(), []byte("\n")) {
			if len(line) > 0 {
				onLine(line)
			}
		}
		return code, nil
	}
	return 0, nil
}

// GetDestroyCalls returns the number of times Destroy has been called (thread-safe).
func (m *MockSandboxProvider) GetDestroyCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.destroyCalls
}

// StartInteractiveCommand satisfies agent.InteractiveSandboxProvider so the
// mock can drive adapter tests through the new live-handle path. When the
// caller sets StartInteractiveCommandFn it wins; otherwise we synthesize a
// MockInteractiveCommandHandle that runs the spec.Cmd through ExecStream and
// records the cmd in ExecCalls. This keeps adapter tests that assert against
// ExecCalls working without forcing every test to rebuild a custom handle.
//
// TTY parity: on TTY specs the real Docker handle exposes an empty Stderr()
// reader (the kernel PTY merges streams). The mock mirrors that exactly —
// the synthetic handle's Stderr() returns an empty reader and stderr writes
// from the test callback are routed to io.Discard. This way adapters that
// declare RequiresTTY behave identically against mock and Docker.
func (m *MockSandboxProvider) StartInteractiveCommand(ctx context.Context, sb *agent.Sandbox, spec agent.InteractiveCommandSpec) (agent.InteractiveCommandHandle, error) {
	if m.StartInteractiveCommandFn != nil {
		return m.StartInteractiveCommandFn(ctx, sb, spec)
	}
	m.mu.Lock()
	m.ExecCalls = append(m.ExecCalls, spec.Cmd)
	m.mu.Unlock()

	h := newMockInteractiveCommandHandle(spec)
	go func() {
		defer h.finish()
		if m.ExecStreamFn != nil {
			code, err := m.ExecStreamFn(ctx, sb, spec.Cmd, h.deliverStdoutLine, h.stderrSink())
			h.recordExit(code, err)
			return
		}
		if m.ExecFn != nil {
			var stdoutBuf bytes.Buffer
			code, err := m.ExecFn(ctx, sb, spec.Cmd, &stdoutBuf, h.stderrSink())
			if err == nil {
				for _, line := range bytes.Split(stdoutBuf.Bytes(), []byte("\n")) {
					if len(line) > 0 {
						h.deliverStdoutLine(line)
					}
				}
			}
			h.recordExit(code, err)
			return
		}
		h.recordExit(0, nil)
	}()
	return h, nil
}

// MockInteractiveCommandHandle is a lightweight in-memory implementation of
// agent.InteractiveCommandHandle. Adapter tests that need to assert against
// the live-handle API instantiate one directly via
// MockSandboxProvider.StartInteractiveCommandFn.
//
// TTY mode mirrors the real Docker handle: Stderr() returns an empty reader
// and stderr writes from the test callback are routed to io.Discard.
type MockInteractiveCommandHandle struct {
	id      string
	spec    agent.InteractiveCommandSpec
	stdoutR *io.PipeReader
	stdoutW *io.PipeWriter
	// stderrR/stderrW are nil for TTY specs.
	stderrR *io.PipeReader
	stderrW *io.PipeWriter

	mu         sync.Mutex
	closed     bool
	exitCode   int
	exitErr    error
	done       chan struct{}
	interrupts []agent.CancellationSpec
	killed     bool
	stdinBuf   bytes.Buffer
}

func newMockInteractiveCommandHandle(spec agent.InteractiveCommandSpec) *MockInteractiveCommandHandle {
	stdoutR, stdoutW := io.Pipe()
	h := &MockInteractiveCommandHandle{
		id:      fmt.Sprintf("mock-exec-%p", stdoutR),
		spec:    spec,
		stdoutR: stdoutR,
		stdoutW: stdoutW,
		done:    make(chan struct{}),
	}
	if !spec.TTY {
		h.stderrR, h.stderrW = io.Pipe()
	}
	return h
}

func (h *MockInteractiveCommandHandle) ID() string        { return h.id }
func (h *MockInteractiveCommandHandle) Stdout() io.Reader { return h.stdoutR }

// Stderr returns an empty reader on TTY transports (matching the real
// provider) and a pipe-backed reader otherwise.
func (h *MockInteractiveCommandHandle) Stderr() io.Reader {
	if h.stderrR == nil {
		return bytes.NewReader(nil)
	}
	return h.stderrR
}

// stderrSink returns the writer the synthetic Exec/ExecStream callback should
// write stderr bytes to. On TTY specs this is io.Discard so a test callback
// that writes diagnostics doesn't deadlock on an undrained pipe.
func (h *MockInteractiveCommandHandle) stderrSink() io.Writer {
	if h.stderrW == nil {
		return io.Discard
	}
	return h.stderrW
}

func (h *MockInteractiveCommandHandle) deliverStdoutLine(line []byte) {
	_, _ = h.stdoutW.Write(line)
	_, _ = h.stdoutW.Write([]byte{'\n'})
}

func (h *MockInteractiveCommandHandle) recordExit(code int, err error) {
	h.mu.Lock()
	h.exitCode = code
	h.exitErr = err
	h.mu.Unlock()
}

func (h *MockInteractiveCommandHandle) finish() {
	_ = h.stdoutW.Close()
	if h.stderrW != nil {
		_ = h.stderrW.Close()
	}
	h.mu.Lock()
	if !h.closed {
		h.closed = true
		close(h.done)
	}
	h.mu.Unlock()
}

func (h *MockInteractiveCommandHandle) WriteInput(_ context.Context, data []byte) error {
	if !h.spec.OpenStdin {
		return agent.ErrInputNotOpen
	}
	h.mu.Lock()
	h.stdinBuf.Write(data)
	h.mu.Unlock()
	return nil
}

func (h *MockInteractiveCommandHandle) CloseInput(_ context.Context) error { return nil }

func (h *MockInteractiveCommandHandle) Interrupt(_ context.Context, spec agent.CancellationSpec) error {
	h.mu.Lock()
	h.interrupts = append(h.interrupts, spec)
	h.mu.Unlock()
	return nil
}

func (h *MockInteractiveCommandHandle) Kill(_ context.Context) error {
	h.mu.Lock()
	h.killed = true
	h.mu.Unlock()
	h.finish()
	return nil
}

func (h *MockInteractiveCommandHandle) Wait(ctx context.Context) (int, error) {
	select {
	case <-ctx.Done():
		return -1, ctx.Err()
	case <-h.done:
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.exitCode, h.exitErr
}

func (h *MockInteractiveCommandHandle) Close() error {
	h.finish()
	return nil
}

// Interrupts returns the cancellation specs Interrupt was called with.
func (h *MockInteractiveCommandHandle) Interrupts() []agent.CancellationSpec {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]agent.CancellationSpec, len(h.interrupts))
	copy(out, h.interrupts)
	return out
}

// Killed reports whether Kill was called.
func (h *MockInteractiveCommandHandle) Killed() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.killed
}

// StdinBuffer returns the bytes written via WriteInput.
func (h *MockInteractiveCommandHandle) StdinBuffer() []byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]byte, h.stdinBuf.Len())
	copy(out, h.stdinBuf.Bytes())
	return out
}

// FinishWith terminates the handle's wait with the given exit code/err.
// Used by tests that drive the handle manually.
func (h *MockInteractiveCommandHandle) FinishWith(exitCode int, err error) {
	h.recordExit(exitCode, err)
	h.finish()
}

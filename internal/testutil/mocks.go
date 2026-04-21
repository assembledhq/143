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
	return &agent.Sandbox{ID: "test-sandbox", Provider: m.Name_, WorkDir: cfg.WorkDir, HomeDir: cfg.HomeDir}, nil
}

func (m *MockSandboxProvider) CloneRepo(ctx context.Context, sb *agent.Sandbox, repoURL, branch, token string) error {
	if m.CloneRepoFn != nil {
		return m.CloneRepoFn(ctx, sb, repoURL, branch, token)
	}
	return nil
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

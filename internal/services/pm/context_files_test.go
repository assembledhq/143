package pm

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/stretchr/testify/require"
)

type pmSandboxMock struct {
	readResult []byte
	readErr    error
	writeErr   error
	writePath  string
	writeData  []byte
}

func (m *pmSandboxMock) Name() string {
	return "mock"
}

func (m *pmSandboxMock) Create(ctx context.Context, cfg agent.SandboxConfig) (*agent.Sandbox, error) {
	return &agent.Sandbox{ID: "unused"}, nil
}

func (m *pmSandboxMock) CloneRepo(ctx context.Context, sb *agent.Sandbox, repoURL, branch, token string) error {
	return nil
}

func (m *pmSandboxMock) Exec(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
	return 0, nil
}

func (m *pmSandboxMock) ReadFile(ctx context.Context, sb *agent.Sandbox, path string) ([]byte, error) {
	if m.readErr != nil {
		return nil, m.readErr
	}
	return m.readResult, nil
}

func (m *pmSandboxMock) WriteFile(ctx context.Context, sb *agent.Sandbox, path string, data []byte) error {
	m.writePath = path
	m.writeData = append([]byte{}, data...)
	if m.writeErr != nil {
		return m.writeErr
	}
	return nil
}

func (m *pmSandboxMock) Destroy(ctx context.Context, sb *agent.Sandbox) error {
	return nil
}

func (m *pmSandboxMock) ConnectionInfo(ctx context.Context, sb *agent.Sandbox) (*agent.SandboxConnectionInfo, error) {
	return nil, nil
}

func (m *pmSandboxMock) Snapshot(ctx context.Context, sb *agent.Sandbox) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(nil)), nil
}

func (m *pmSandboxMock) Restore(ctx context.Context, sb *agent.Sandbox, reader io.Reader) error {
	return nil
}

func (m *pmSandboxMock) ExecStream(ctx context.Context, sb *agent.Sandbox, cmd string, onLine func(line []byte), stderr io.Writer) (int, error) {
	return 0, nil
}

func TestWriteProductContextToAgentsMD(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		pc        *models.ProductContext
		readBytes []byte
		readErr   error
		writeErr  error
		expectErr bool
		expects   []string
	}{
		{
			name:    "returns nil for nil product context",
			pc:      nil,
			expects: nil,
		},
		{
			name:      "appends context section to existing agents file",
			pc:        &models.ProductContext{Philosophy: "reliability", Direction: "stability", FocusAreas: []string{"timeouts"}, AvoidAreas: []string{"new features"}},
			readBytes: []byte("# existing"),
			expects:   []string{"# existing", "## Product Context", "**Philosophy:** reliability", "**Current direction:** stability", "**Focus areas:** timeouts", "**Avoid areas:** new features"},
		},
		{
			name:      "still writes section when read fails",
			pc:        &models.ProductContext{Philosophy: "ship", Direction: "payments"},
			readErr:   fmt.Errorf("missing file"),
			expects:   []string{"## Product Context", "**Philosophy:** ship", "**Current direction:** payments"},
		},
		{
			name:      "returns write error",
			pc:        &models.ProductContext{Philosophy: "ship", Direction: "payments"},
			writeErr:  fmt.Errorf("disk full"),
			expectErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			sandbox := &pmSandboxMock{readResult: tt.readBytes, readErr: tt.readErr, writeErr: tt.writeErr}
			svc := &Service{sandbox: sandbox}

			err := svc.writeProductContextToAgentsMD(context.Background(), &agent.Sandbox{ID: "sb"}, tt.pc)
			if tt.expectErr {
				require.Error(t, err, "writeProductContextToAgentsMD should return write errors")
				return
			}
			require.NoError(t, err, "writeProductContextToAgentsMD should not return an error")

			if tt.pc == nil {
				require.Empty(t, sandbox.writePath, "writeProductContextToAgentsMD should skip writes when context is nil")
				return
			}

			require.Equal(t, "/workspace/AGENTS.md", sandbox.writePath, "writeProductContextToAgentsMD should target AGENTS.md")
			written := string(sandbox.writeData)
			for _, expected := range tt.expects {
				require.Contains(t, written, expected, "writeProductContextToAgentsMD should include expected section content")
			}
		})
	}
}


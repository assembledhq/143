package providers

import (
	"bufio"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/services/agent"
)

func TestDockerReviewPreparationUsesTrustedShell(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		review        bool
		stdin         bool
		expectedShell string
	}{
		{name: "ordinary exec", expectedShell: "sh"},
		{name: "ordinary stdin", stdin: true, expectedShell: "sh"},
		{name: "review exec", review: true, expectedShell: "/bin/sh"},
		{name: "review stdin", review: true, stdin: true, expectedShell: "/bin/sh"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var configured container.ExecOptions
			conn, attached := newCapturingHijackedResponse()
			mock := &mockDockerClient{}
			mock.containerExecCreateFn = func(_ context.Context, _ string, config container.ExecOptions) (container.ExecCreateResponse, error) {
				configured = config
				return container.ExecCreateResponse{ID: "review-shell-exec"}, nil
			}
			mock.containerExecAttachFn = func(context.Context, string, container.ExecAttachOptions) (types.HijackedResponse, error) {
				return attached, nil
			}
			provider := NewDockerProvider(mock, newTestLogger())
			sandbox := &agent.Sandbox{ID: "review-shell", WorkDir: "/workspace/repo", Env: map[string]string{"PATH": "/workspace/writable/bin:/usr/bin"}}
			var exitCode int
			var err error
			switch {
			case tt.review && tt.stdin:
				exitCode, err = provider.ExecWithStdinForReviewPreparation(context.Background(), sandbox, "trusted preparation", strings.NewReader("fake-private-stdin"), io.Discard, io.Discard)
			case tt.stdin:
				exitCode, err = provider.ExecWithStdin(context.Background(), sandbox, "trusted preparation", strings.NewReader("fake-private-stdin"), io.Discard, io.Discard)
			case tt.review:
				exitCode, err = provider.ExecForReviewPreparation(context.Background(), sandbox, "trusted preparation", io.Discard, io.Discard)
			default:
				exitCode, err = provider.Exec(context.Background(), sandbox, "trusted preparation", io.Discard, io.Discard)
			}
			require.NoError(t, err, "review transport must retain normal successful execution and stdin streaming")
			require.Equal(t, 0, exitCode, "review transport must return the inspected process exit code")
			require.Equal(t, []string{tt.expectedShell, "-c", "trusted preparation"}, configured.Cmd, "review shell must ignore writable PATH while ordinary coding behavior is preserved")
			require.Equal(t, tt.stdin, configured.AttachStdin, "stdin must attach only for streaming execution")
			require.Equal(t, "/workspace/repo", configured.WorkingDir, "transport must preserve the sandbox working directory")
			require.Equal(t, []string{"PATH=/workspace/writable/bin:/usr/bin"}, configured.Env, "transport must preserve unrelated execution environment hints")
			if tt.stdin {
				require.Equal(t, []byte("fake-private-stdin"), conn.Captured(), "private preparation bytes must travel through stdin and remain out of Docker command arguments")
			} else {
				require.Equal(t, []byte{}, conn.Captured(), "non-streaming execution must not send stdin bytes")
			}
		})
	}
}

func TestDockerReviewStdinPreparationCancellation(t *testing.T) {
	t.Parallel()
	conn := newBlockingHijackedConn()
	defer conn.Close()
	entered := make(chan struct{})
	mock := &mockDockerClient{}
	mock.containerExecAttachFn = func(context.Context, string, container.ExecAttachOptions) (types.HijackedResponse, error) {
		close(entered)
		return types.HijackedResponse{Conn: conn, Reader: bufio.NewReader(conn)}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := NewDockerProvider(mock, newTestLogger()).ExecWithStdinForReviewPreparation(ctx, &agent.Sandbox{ID: "review-stdin"}, "trusted preparation", strings.NewReader("fake-private-stdin"), io.Discard, io.Discard)
		done <- err
	}()
	<-entered
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled, "review stdin transport must close canceled attachments and preserve the shared deadline signal")
	case <-time.After(time.Second):
		t.Fatal("canceled review stdin attachment remained blocked")
	}
}

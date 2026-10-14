package providers

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/services/agent"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/stretchr/testify/require"
)

type observedReviewSnapshotInput struct {
	*io.PipeReader
	entered chan struct{}
	once    sync.Once
}

func (r *observedReviewSnapshotInput) Read(data []byte) (int, error) {
	r.once.Do(func() { close(r.entered) })
	return r.PipeReader.Read(data)
}

func TestReviewPreparationExecClosesCancelledAttachment(t *testing.T) {
	t.Parallel()
	conn := newBlockingHijackedConn()
	defer conn.Close()
	attached := make(chan struct{})
	mock := &mockDockerClient{}
	mock.containerExecAttachFn = func(context.Context, string, container.ExecAttachOptions) (types.HijackedResponse, error) {
		close(attached)
		return types.HijackedResponse{Conn: conn, Reader: bufio.NewReader(conn)}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := NewDockerProvider(mock, newTestLogger()).ExecForReviewPreparation(ctx, &agent.Sandbox{ID: "review"}, "long clone", io.Discard, io.Discard)
		done <- err
	}()
	<-attached
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled, "review preparation must stop copying output when cancellation closes Docker's attachment")
	case <-time.After(time.Second):
		t.Fatal("cancelled review preparation output attachment remained blocked")
	}
}

func TestReviewPreparationRestoreInterruptsInputAndOutput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		blockedInput bool
	}{
		{name: "snapshot input before archive sniff", blockedInput: true},
		{name: "Docker output after snapshot input"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			conn := newBlockingHijackedConn()
			defer conn.Close()
			attached := make(chan struct{})
			mock := &mockDockerClient{}
			mock.containerExecAttachFn = func(context.Context, string, container.ExecAttachOptions) (types.HijackedResponse, error) {
				close(attached)
				return types.HijackedResponse{Conn: conn, Reader: bufio.NewReader(conn)}, nil
			}
			var source io.Reader = bytes.NewReader([]byte("plain archive input"))
			entered := attached
			if tt.blockedInput {
				reader, writer := io.Pipe()
				defer reader.Close()
				defer writer.Close()
				entered = make(chan struct{})
				source = &observedReviewSnapshotInput{PipeReader: reader, entered: entered}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				done <- NewDockerProvider(mock, newTestLogger()).RestoreForReviewPreparation(ctx, &agent.Sandbox{ID: "review"}, source)
			}()
			<-entered
			cancel()
			select {
			case err := <-done:
				require.ErrorIs(t, err, context.Canceled, "review snapshot restoration must stop at cancellation before or after Docker attaches")
			case <-time.After(time.Second):
				t.Fatal("cancelled review snapshot restoration remained blocked")
			}
		})
	}
}

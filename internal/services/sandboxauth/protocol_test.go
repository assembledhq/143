package sandboxauth

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// shortSockPath returns a test-unique AF_UNIX path that does not yet exist,
// short enough to fit the ~104-byte macOS sun_path limit (t.TempDir() is too
// long). The caller is responsible for listening on / cleaning up the path.
func shortSockPath(t *testing.T) string {
	t.Helper()
	f, err := os.CreateTemp("", "143auth-*.sock")
	require.NoError(t, err)
	sock := f.Name()
	require.NoError(t, f.Close())
	require.NoError(t, os.Remove(sock)) // listen wants the path to not exist
	t.Cleanup(func() { _ = os.Remove(sock) })
	return sock
}

func TestClient_Get_RetriesUntilListenerAppears(t *testing.T) {
	t.Parallel()

	// Socket file is absent at first (ENOENT), then a listener binds it mid-
	// flight — the exact shape of a worker re-binding the per-session socket
	// after a restart while the sandbox client keeps dialing.
	sock := shortSockPath(t)

	go func() {
		time.Sleep(150 * time.Millisecond)
		ln, err := net.Listen("unix", sock)
		if err != nil {
			return
		}
		defer ln.Close()
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		var req Request
		_ = json.NewDecoder(conn).Decode(&req)
		_ = json.NewEncoder(conn).Encode(&Response{Token: "tok-after-retry"})
	}()

	client := &Client{SocketPath: sock, retryBudget: 5 * time.Second, retryInterval: 25 * time.Millisecond}
	resp, err := client.Get(context.Background(), ActionPush)
	require.NoError(t, err, "Get should retry past the initial ENOENT and connect once the listener binds")
	require.Equal(t, "tok-after-retry", resp.Token)
}

func TestClient_Get_RetryGivesUpAfterBudget(t *testing.T) {
	t.Parallel()

	// No listener ever appears: Get should exhaust its (tiny) budget and
	// surface the dial error rather than hanging git indefinitely.
	sock := shortSockPath(t)

	client := &Client{SocketPath: sock, retryBudget: 80 * time.Millisecond, retryInterval: 20 * time.Millisecond}
	start := time.Now()
	_, err := client.Get(context.Background(), ActionPush)
	elapsed := time.Since(start)

	require.Error(t, err, "a socket that never gets a listener must fail")
	require.Contains(t, err.Error(), "dial "+sock, "error should preserve the familiar dial message")
	require.GreaterOrEqual(t, elapsed, 60*time.Millisecond, "Get should have retried for roughly the budget before giving up")
	require.Less(t, elapsed, 3*time.Second, "Get must not block far beyond its retry budget")
}

func TestClient_Get_StopsRetryingOnContextCancel(t *testing.T) {
	t.Parallel()

	// No listener exists, but the caller's context is cancelled well before
	// the retry budget. Get must abandon retries promptly on cancellation
	// rather than sleeping out the whole budget.
	sock := shortSockPath(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()

	client := &Client{SocketPath: sock, retryBudget: 10 * time.Second, retryInterval: 20 * time.Millisecond}
	start := time.Now()
	_, err := client.Get(ctx, ActionPush)
	elapsed := time.Since(start)

	require.Error(t, err, "a cancelled context with no listener must fail")
	require.Less(t, elapsed, 2*time.Second, "Get must stop retrying once the context is done")
}

func TestClient_Get_UsesEarlierContextDeadline(t *testing.T) {
	t.Parallel()

	sock := startSocketServer(t, func(conn net.Conn) {
		var req Request
		require.NoError(t, json.NewDecoder(conn).Decode(&req), "server should receive a valid request")
		require.NoError(t, json.NewEncoder(conn).Encode(&Response{Token: "tok"}), "server should encode a valid response")
	})

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(2*time.Second))
	defer cancel()

	resp, err := NewClient(sock).Get(ctx, ActionAPI)
	require.NoError(t, err, "Get should respect a caller deadline earlier than the default timeout")
	require.Equal(t, "tok", resp.Token, "Get should still decode the host response")
}

func TestClient_GetAPIToken(t *testing.T) {
	t.Parallel()

	t.Run("returns token", func(t *testing.T) {
		t.Parallel()

		sock := startSocketServer(t, func(conn net.Conn) {
			var req Request
			require.NoError(t, json.NewDecoder(conn).Decode(&req))
			require.Equal(t, ActionAPI, req.Action, "GetAPIToken must request the api scope")
			require.NoError(t, json.NewEncoder(conn).Encode(&Response{Token: "ghs_api"}))
		})

		tok, err := NewClient(sock).GetAPIToken(context.Background())
		require.NoError(t, err)
		require.Equal(t, "ghs_api", tok)
	})

	t.Run("transport error propagates", func(t *testing.T) {
		t.Parallel()

		// Tiny retry budget: a permanently-missing socket is ENOENT, which Get
		// now retries (a real session's socket reappears after a worker
		// re-bind). We only care that the error still surfaces, not that we
		// wait out the production-default budget.
		client := &Client{SocketPath: "/does/not/exist.sock", retryBudget: 20 * time.Millisecond, retryInterval: 5 * time.Millisecond}
		_, err := client.GetAPIToken(context.Background())
		require.Error(t, err, "missing socket should surface as a transport error")
	})

	t.Run("host error becomes go error", func(t *testing.T) {
		t.Parallel()

		sock := startSocketServer(t, func(conn net.Conn) {
			var req Request
			require.NoError(t, json.NewDecoder(conn).Decode(&req))
			require.NoError(t, json.NewEncoder(conn).Encode(&Response{Error: "no installation for repo"}))
		})

		tok, err := NewClient(sock).GetAPIToken(context.Background())
		require.Error(t, err, "Response.Error must surface as a Go error so callers don't quietly use an empty token")
		require.Empty(t, tok)
		require.Contains(t, err.Error(), "auth socket: no installation for repo")
	})
}

func TestClient_Get_TransportErrorBranches(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		handler func(conn net.Conn)
		wantErr string
	}{
		{
			name: "empty response",
			handler: func(conn net.Conn) {
				var req Request
				require.NoError(t, json.NewDecoder(conn).Decode(&req), "server should receive the request before simulating an empty response")
			},
			wantErr: "empty response",
		},
		{
			name: "decode error",
			handler: func(conn net.Conn) {
				var req Request
				require.NoError(t, json.NewDecoder(conn).Decode(&req), "server should receive the request before sending malformed JSON")
				_, _ = conn.Write([]byte("not-json\n"))
			},
			wantErr: "decode response",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			sock := startSocketServer(t, tt.handler)
			_, err := NewClient(sock).Get(context.Background(), ActionPush)
			require.Error(t, err, "Get should fail for %s", tt.name)
			require.Contains(t, err.Error(), tt.wantErr, "Get should report the expected failure mode")
		})
	}
}

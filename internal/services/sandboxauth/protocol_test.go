package sandboxauth

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestClient_Get_UsesEarlierContextDeadline(t *testing.T) {
	t.Parallel()

	sock := startSocketServer(t, func(conn net.Conn) {
		defer conn.Close()
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
				defer conn.Close()
				var req Request
				require.NoError(t, json.NewDecoder(conn).Decode(&req), "server should receive the request before simulating an empty response")
			},
			wantErr: "empty response",
		},
		{
			name: "decode error",
			handler: func(conn net.Conn) {
				defer conn.Close()
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

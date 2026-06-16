package sandboxauth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/auth"
	"github.com/assembledhq/143/internal/models"
)

func TestRemoteBrokerClient_AcquiresAndReleasesWithSignedWorkerRPC(t *testing.T) {
	t.Parallel()

	secret := "worker-secret"
	keyring, err := auth.NewPreviewTokenKeyring([]string{secret})
	require.NoError(t, err, "test keyring should be valid")

	orgID := uuid.New()
	sessionID := uuid.New()
	holderID := uuid.New()
	socketPath := "/var/run/143/sandbox-auth/" + sessionID.String() + "/" + SocketFileName
	var sawAcquire atomic.Bool
	releaseCh := make(chan BrokerReleaseRequest, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		claims, err := keyring.Validate(token)
		require.NoError(t, err, "remote broker requests should carry a valid token")
		require.Equal(t, orgID, claims.OrgID, "remote broker token should preserve org scope")
		require.Equal(t, "worker-1", claims.TargetNodeID, "remote broker token should target the worker")
		require.NotNil(t, claims.SessionID, "remote broker token should include the session id")
		require.Equal(t, sessionID, *claims.SessionID, "remote broker token should preserve the session id")

		switch r.URL.Path {
		case "/internal/sandbox-auth/acquire":
			require.Equal(t, "sandbox_auth_acquire", claims.Action, "acquire should sign the acquire action")
			var body BrokerAcquireRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body), "acquire should send a JSON body")
			require.Equal(t, BrokerAcquireRequest{OrgID: orgID, SessionID: sessionID, HolderID: holderID}, body, "acquire should send holder-scoped request")
			sawAcquire.Store(true)
			w.WriteHeader(http.StatusOK)
			require.NoError(t, json.NewEncoder(w).Encode(models.SingleResponse[BrokerAcquireResponse]{
				Data: BrokerAcquireResponse{SocketPath: socketPath},
			}), "acquire response should encode")
		case "/internal/sandbox-auth/release":
			require.Equal(t, "sandbox_auth_release", claims.Action, "release should sign the release action")
			var body BrokerReleaseRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body), "release should send a JSON body")
			releaseCh <- body
			w.WriteHeader(http.StatusOK)
			require.NoError(t, json.NewEncoder(w).Encode(models.SingleResponse[BrokerReleaseResponse]{
				Data: BrokerReleaseResponse{Released: true},
			}), "release response should encode")
		default:
			t.Fatalf("unexpected remote broker path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewRemoteBrokerClient(RemoteBrokerClientConfig{
		BaseURL:  server.URL,
		NodeID:   "worker-1",
		HolderID: holderID,
		Keyring:  keyring,
		Logger:   zerolog.Nop(),
	})

	gotSocketPath, err := client.Listen(context.Background(), sessionID, &models.Session{ID: sessionID, OrgID: orgID}, &models.Repository{}, models.OrgSettings{})
	require.NoError(t, err, "remote broker client should acquire a socket")
	require.Equal(t, socketPath, gotSocketPath, "remote broker client should return the worker socket path")
	require.True(t, sawAcquire.Load(), "remote broker server should receive the acquire request")

	client.Close(sessionID)
	select {
	case body := <-releaseCh:
		require.Equal(t, BrokerReleaseRequest{OrgID: orgID, SessionID: sessionID, HolderID: holderID}, body, "release should send the same holder identity")
	case <-time.After(time.Second):
		t.Fatal("remote broker client should send a release request on Close")
	}
}

func TestRemoteBrokerClient_CloseWithoutAcquireDoesNotCallWorker(t *testing.T) {
	t.Parallel()

	keyring, err := auth.NewPreviewTokenKeyring([]string{"worker-secret"})
	require.NoError(t, err, "test keyring should be valid")

	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls.Add(1)
	}))
	defer server.Close()

	client := NewRemoteBrokerClient(RemoteBrokerClientConfig{
		BaseURL:  server.URL,
		NodeID:   "worker-1",
		HolderID: uuid.New(),
		Keyring:  keyring,
		Logger:   zerolog.Nop(),
	})

	client.Close(uuid.New())
	require.Equal(t, int32(0), calls.Load(), "remote broker client should not release sessions it never acquired")
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestRemoteBrokerClient_AcquireRetriesUntilWorkerHTTPIsReady(t *testing.T) {
	t.Parallel()

	keyring, err := auth.NewPreviewTokenKeyring([]string{"worker-secret"})
	require.NoError(t, err, "test keyring should be valid")

	orgID := uuid.New()
	sessionID := uuid.New()
	holderID := uuid.New()
	socketPath := "/var/run/143/sandbox-auth/" + sessionID.String() + "/" + SocketFileName
	var calls atomic.Int32

	httpClient := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			call := calls.Add(1)
			if call < 3 {
				return nil, errors.New("dial tcp worker: connect: connection refused")
			}
			require.Equal(t, "/internal/sandbox-auth/acquire", req.URL.Path, "retry should preserve acquire path")
			token := strings.TrimSpace(strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer "))
			claims, err := keyring.Validate(token)
			require.NoError(t, err, "retry should preserve a valid worker token")
			require.Equal(t, BrokerActionAcquire, claims.Action, "retry should preserve acquire action")
			require.Equal(t, sessionID, *claims.SessionID, "retry should preserve session claim")
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"data":{"socket_path":"` + socketPath + `"}}`)),
			}, nil
		}),
	}
	client := NewRemoteBrokerClient(RemoteBrokerClientConfig{
		BaseURL:             "http://worker.internal",
		NodeID:              "worker-1",
		HolderID:            holderID,
		Keyring:             keyring,
		HTTPClient:          httpClient,
		AcquireRetryBackoff: func(int) time.Duration { return 0 },
	})

	gotSocketPath, err := client.Listen(context.Background(), sessionID, &models.Session{ID: sessionID, OrgID: orgID}, &models.Repository{}, models.OrgSettings{})
	require.NoError(t, err, "remote broker client should retry transient worker readiness failures")
	require.Equal(t, socketPath, gotSocketPath, "remote broker client should return socket path after retry succeeds")
	require.Equal(t, int32(3), calls.Load(), "remote broker client should retry until the worker is ready")
}

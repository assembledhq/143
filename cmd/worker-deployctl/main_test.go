package main

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/auth"
	"github.com/stretchr/testify/require"
)

type previewAuthCheckRoundTripper struct {
	delay       time.Duration
	keyring     auth.PreviewTokenKeyring
	inFlight    int32
	maxInFlight int32
}

func (rt *previewAuthCheckRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	current := atomic.AddInt32(&rt.inFlight, 1)
	defer atomic.AddInt32(&rt.inFlight, -1)

	for {
		max := atomic.LoadInt32(&rt.maxInFlight)
		if current <= max || atomic.CompareAndSwapInt32(&rt.maxInFlight, max, current) {
			break
		}
	}

	if req.URL.Path != "/internal/preview/auth-check" {
		return nil, fmt.Errorf("unexpected path %q", req.URL.Path)
	}
	token := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")
	claims, err := rt.keyring.Validate(token)
	if err != nil {
		return nil, fmt.Errorf("validate token: %w", err)
	}
	if claims.Action != "auth_check" {
		return nil, fmt.Errorf("unexpected action %q", claims.Action)
	}
	expectedTarget := strings.TrimSuffix(req.URL.Host, ".internal")
	if claims.TargetNodeID != expectedTarget {
		return nil, fmt.Errorf("unexpected target %q for host %q", claims.TargetNodeID, req.URL.Host)
	}

	timer := time.NewTimer(rt.delay)
	defer timer.Stop()
	select {
	case <-req.Context().Done():
		return nil, req.Context().Err()
	case <-timer.C:
	}

	return &http.Response{
		StatusCode: http.StatusNoContent,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("")),
		Request:    req,
	}, nil
}

func TestProbePreviewRPCAuthNodes_UsesBoundedConcurrency(t *testing.T) {
	t.Parallel()

	keyring, err := auth.NewPreviewTokenKeyring([]string{"preview-secret"})
	require.NoError(t, err, "NewPreviewTokenKeyring should accept the test secret")

	nodes := make([]previewAuthProbeNode, 0, 6)
	expected := make([]string, 0, 6)
	for i := 0; i < 6; i++ {
		nodeID := fmt.Sprintf("worker-%d", i)
		nodes = append(nodes, previewAuthProbeNode{
			ID:      nodeID,
			BaseURL: "http://" + nodeID + ".internal",
		})
		expected = append(expected, nodeID)
	}

	roundTripper := &previewAuthCheckRoundTripper{
		delay:   100 * time.Millisecond,
		keyring: keyring,
	}
	client := &http.Client{Transport: roundTripper}

	start := time.Now()
	checked, err := probePreviewRPCAuthNodes(nodes, keyring, time.Second, 6, client)
	elapsed := time.Since(start)

	require.NoError(t, err, "probePreviewRPCAuthNodes should accept healthy nodes")
	require.Equal(t, expected, checked, "probePreviewRPCAuthNodes should report checked nodes in input order")
	require.GreaterOrEqual(t, atomic.LoadInt32(&roundTripper.maxInFlight), int32(2), "probePreviewRPCAuthNodes should send more than one probe at a time")
	require.Less(t, elapsed, 500*time.Millisecond, "probePreviewRPCAuthNodes should not serialize per-node latency")
}

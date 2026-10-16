package providers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/stretchr/testify/require"
)

func TestDockerProviderConfirmSandboxContainerMissing(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, id                                        string
		inspectErr                                      error
		cancelled, cancelAfterInspect, missing, wantErr bool
		calls                                           int
	}{
		{name: "definitive Docker NotFound", inspectErr: cerrdefs.ErrNotFound, missing: true, calls: 1},
		{name: "wrapped Docker NotFound", inspectErr: fmt.Errorf("inspect: %w", cerrdefs.ErrNotFound), missing: true, calls: 1},
		{name: "stopped container still exists", calls: 1},
		{name: "transport failure is unknown", inspectErr: errors.New("daemon unavailable"), wantErr: true, calls: 1},
		{name: "untyped not found message is unknown", inspectErr: errors.New("container not found"), wantErr: true, calls: 1},
		{name: "reusable name is never inspected", id: "sandbox", wantErr: true},
		{name: "short prefix is never inspected", id: strings.Repeat("a", 12), wantErr: true},
		{name: "cancelled observation cannot prove absence", cancelled: true, inspectErr: cerrdefs.ErrNotFound, wantErr: true},
		{name: "cancellation racing NotFound cannot prove absence", cancelAfterInspect: true, inspectErr: cerrdefs.ErrNotFound, wantErr: true, calls: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			id := tt.id
			if id == "" {
				id = strings.Repeat("a", 64)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tt.cancelled {
				cancel()
			}
			calls := 0
			mock := &mockDockerClient{containerInspectFn: func(_ context.Context, actualID string) (container.InspectResponse, error) {
				calls++
				require.Equal(t, id, actualID, "presence inspection must use the complete exact ID")
				if tt.cancelAfterInspect {
					cancel()
				}
				return container.InspectResponse{}, tt.inspectErr
			}}
			missing, err := NewDockerProvider(mock, newTestLogger()).ConfirmSandboxContainerMissing(ctx, id)
			require.Equal(t, tt.missing, missing, "only a definitive uncancelled Docker NotFound may prove physical absence")
			require.Equal(t, tt.wantErr, err != nil, "unknown or malformed presence observations must fail closed")
			require.Equal(t, tt.calls, calls, "invalid and cancelled probes must never reach the daemon")
		})
	}
}

func TestDockerProviderLiveSandboxInventoryUsesOneDaemonSample(t *testing.T) {
	t.Parallel()
	a, b := strings.Repeat("a", 64), strings.Repeat("b", 64)
	listCalls := 0
	mock := &mockDockerClient{containerListFn: func(_ context.Context, options container.ListOptions) ([]container.Summary, error) {
		listCalls++
		require.False(t, options.All, "live inventory must count only daemon-listed running containers")
		require.Empty(t, options.Filters.Get("network"), "live inventory must include all managed networks and worker generations")
		return []container.Summary{{ID: a, Labels: map[string]string{"143.sandbox": "true"}}, {ID: b, Labels: map[string]string{SandboxLabelManaged: "true", SandboxLabelType: "sandbox"}}, {ID: "sidecar", Image: "143-sandbox-dns"}}, nil
	}}
	ids, err := NewDockerProvider(mock, newTestLogger()).LiveSandboxContainerIDs(context.Background())
	require.NoError(t, err, "complete live inventory should succeed")
	require.Equal(t, []string{a, b}, ids, "one physical inventory must include current and legacy managed sandboxes without sidecars")
	require.Equal(t, 1, listCalls, "IDs and count must originate from exactly one Docker list")
}

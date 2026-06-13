package preview

import (
	"context"
	"errors"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestReachabilityMonitorProbeMarksUnreachableRuntime(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	previewID := uuid.New()
	runtimeID := uuid.New()
	store := &reachabilityStoreStub{
		runtimes: []models.PreviewRuntime{{
			ID:                runtimeID,
			OrgID:             orgID,
			PreviewInstanceID: previewID,
			EndpointURL:       "http://worker.internal:8081",
		}},
	}
	monitor := NewReachabilityMonitor(ReachabilityMonitorConfig{
		Store: store,
		DialContext: func(context.Context, string, string) error {
			return errors.New("dial timeout")
		},
	})

	monitor.probeOnce(context.Background())

	require.Equal(t, []reachabilityMarkCall{{
		orgID:     orgID,
		previewID: previewID,
		runtimeID: runtimeID,
		reason:    "preview reachability probe failed: dial timeout",
	}}, store.markCalls, "reachability monitor should mark the unreachable runtime")
}

type reachabilityStoreStub struct {
	runtimes  []models.PreviewRuntime
	markCalls []reachabilityMarkCall
}

type reachabilityMarkCall struct {
	orgID     uuid.UUID
	previewID uuid.UUID
	runtimeID uuid.UUID
	reason    string
}

func (s *reachabilityStoreStub) ListActivePreviewRuntimesForReachability(context.Context, int) ([]models.PreviewRuntime, error) {
	return s.runtimes, nil
}

func (s *reachabilityStoreStub) MarkPreviewRuntimeUnreachable(_ context.Context, orgID, previewID, runtimeID uuid.UUID, reason string) (bool, error) {
	s.markCalls = append(s.markCalls, reachabilityMarkCall{
		orgID:     orgID,
		previewID: previewID,
		runtimeID: runtimeID,
		reason:    reason,
	})
	return true, nil
}

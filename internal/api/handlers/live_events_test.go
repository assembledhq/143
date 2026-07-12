package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/cache"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/liveevents"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type liveMembershipStub struct{ allowed bool }
type liveRepositoryStub struct{ ids []uuid.UUID }
type countingLiveMembershipStub struct{ calls atomic.Int32 }

func (s *countingLiveMembershipStub) Get(_ context.Context, userID, orgID uuid.UUID) (models.OrganizationMembership, error) {
	s.calls.Add(1)
	time.Sleep(10 * time.Millisecond)
	return models.OrganizationMembership{UserID: userID, OrgID: orgID}, nil
}

func (s liveRepositoryStub) ListIDsForLiveAuthorization(context.Context, uuid.UUID) ([]uuid.UUID, error) {
	return s.ids, nil
}

func (s liveMembershipStub) Get(_ context.Context, userID, orgID uuid.UUID) (models.OrganizationMembership, error) {
	if !s.allowed {
		return models.OrganizationMembership{}, context.Canceled
	}
	return models.OrganizationMembership{UserID: userID, OrgID: orgID}, nil
}

func TestLiveEventHandlerRejectsInvalidInputs(t *testing.T) {
	t.Parallel()
	userID := uuid.New()
	tests := []struct {
		name, target string
		status       int
		code         string
	}{
		{name: "missing org", target: "/api/v1/events/stream", status: http.StatusBadRequest, code: "INVALID_ORG_ID"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			handler := NewLiveEventHandler(nil, nil, liveMembershipStub{allowed: true}, nil, zerolog.Nop())
			req := httptest.NewRequest(http.MethodGet, tt.target, nil)
			req = req.WithContext(middleware.WithUser(req.Context(), &models.User{ID: userID}))
			rr := httptest.NewRecorder()
			handler.Stream(rr, req)
			require.Equal(t, tt.status, rr.Code, "handler should return the expected validation status")
			require.Contains(t, rr.Body.String(), tt.code, "handler should return the typed error code")
		})
	}
}

func TestLiveEventHandlerTelemetry(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, body string
		status     int
	}{
		{name: "accepts bounded samples", body: `{"samples":[{"name":"event_processed","lag_ms":12}]}`, status: http.StatusNoContent},
		{name: "rejects malformed body", body: `{`, status: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			handler := NewLiveEventHandler(nil, nil, nil, nil, zerolog.Nop())
			req := httptest.NewRequest(http.MethodPost, "/api/v1/events/telemetry", strings.NewReader(tt.body))
			rr := httptest.NewRecorder()
			handler.Telemetry(rr, req)
			require.Equal(t, tt.status, rr.Code, "telemetry endpoint should return the expected validation status")
		})
	}
}

func TestLiveEventHandlerMalformedCursorReturnsResync(t *testing.T) {
	t.Parallel()
	mr := miniredis.RunT(t)
	redisClient := cache.New(cache.Config{URL: "redis://" + mr.Addr()}, zerolog.Nop(), nil)
	orgID, userID := uuid.New(), uuid.New()
	manager := liveevents.NewManager(redisClient, 2, zerolog.Nop())
	manager.SetShardHealthyForTest(cache.LiveBusShard(orgID, 2), true)
	handler := NewLiveEventHandler(redisClient, manager, liveMembershipStub{allowed: true}, nil, zerolog.Nop())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/events/stream?org_id="+orgID.String()+"&last_event_id=bad", nil)
	req = req.WithContext(middleware.WithUser(req.Context(), &models.User{ID: userID}))
	rr := httptest.NewRecorder()
	handler.Stream(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, "malformed cursors should enter the SSE control path")
	require.Contains(t, rr.Body.String(), "event: live.resync", "malformed cursors should request canonical resynchronization")
	require.Contains(t, rr.Body.String(), `"cause":"malformed_cursor"`, "resync should identify the malformed cursor cause")
}

func TestLiveEventHandlerReadyUsesCursorlessInitialSync(t *testing.T) {
	t.Parallel()
	mr := miniredis.RunT(t)
	redisClient := cache.New(cache.Config{URL: "redis://" + mr.Addr()}, zerolog.Nop(), nil)
	require.NotNil(t, redisClient, "test Redis client should initialize")
	orgID, userID := uuid.New(), uuid.New()
	manager := liveevents.NewManager(redisClient, 2, zerolog.Nop())
	manager.SetShardHealthyForTest(cache.LiveBusShard(orgID, 2), true)
	handler := NewLiveEventHandler(redisClient, manager, liveMembershipStub{allowed: true}, nil, zerolog.Nop())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/events/stream?org_id="+orgID.String(), nil).WithContext(ctx)
	req = req.WithContext(middleware.WithUser(req.Context(), &models.User{ID: userID}))
	rr := newLockedRecorder()
	done := make(chan struct{})
	go func() { defer close(done); handler.Stream(rr, req) }()
	require.Eventually(t, func() bool { return strings.Contains(rr.BodyString(), "event: live.ready") }, 2*time.Second, 10*time.Millisecond, "cursorless stream should flush live.ready")
	require.Contains(t, rr.BodyString(), `"initial_sync_required":true`, "cursorless ready should require canonical synchronization")
	require.Contains(t, rr.BodyString(), `"through_stream_id":"0-0"`, "empty replay stream should advertise the sentinel checkpoint")
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stream did not stop after request cancellation")
	}
}

func TestLiveEventHandlerClosesAtAuthenticationExpiry(t *testing.T) {
	t.Parallel()
	mr := miniredis.RunT(t)
	redisClient := cache.New(cache.Config{URL: "redis://" + mr.Addr()}, zerolog.Nop(), nil)
	orgID, userID := uuid.New(), uuid.New()
	manager := liveevents.NewManager(redisClient, 2, zerolog.Nop())
	manager.SetShardHealthyForTest(manager.ShardForOrg(orgID), true)
	handler := NewLiveEventHandler(redisClient, manager, liveMembershipStub{allowed: true}, nil, zerolog.Nop())
	ctx := middleware.WithUser(context.Background(), &models.User{ID: userID})
	ctx = middleware.WithAuthSession(ctx, &models.AuthSession{UserID: userID, ExpiresAt: time.Now().Add(50 * time.Millisecond)})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/events/stream?org_id="+orgID.String(), nil).WithContext(ctx)
	rr := newLockedRecorder()
	done := make(chan struct{})
	go func() { defer close(done); handler.Stream(rr, req) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("live stream remained open after authentication expiry")
	}
	require.Contains(t, rr.BodyString(), "event: live.ready", "stream should establish before the short authentication expiry")
}

func TestLiveEventHandlerFiltersRepositoryAudienceFromSnapshot(t *testing.T) {
	t.Parallel()
	mr := miniredis.RunT(t)
	redisClient := cache.New(cache.Config{URL: "redis://" + mr.Addr()}, zerolog.Nop(), nil)
	orgID, userID, allowedRepo, deniedRepo := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	manager := liveevents.NewManager(redisClient, 2, zerolog.Nop())
	manager.SetShardHealthyForTest(manager.ShardForOrg(orgID), true)
	handler := NewLiveEventHandler(redisClient, manager, liveMembershipStub{allowed: true}, liveRepositoryStub{ids: []uuid.UUID{allowedRepo}}, zerolog.Nop())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/events/stream?org_id="+orgID.String(), nil).WithContext(ctx)
	req = req.WithContext(middleware.WithUser(req.Context(), &models.User{ID: userID}))
	rr := newLockedRecorder()
	done := make(chan struct{})
	go func() { defer close(done); handler.Stream(rr, req) }()
	require.Eventually(t, func() bool { return strings.Contains(rr.BodyString(), "event: live.ready") }, time.Second, 10*time.Millisecond, "stream should complete authorization handshake")
	deniedEventID := uuid.New()
	manager.DeliverForTest(cache.LiveBusMessage{StreamID: "1-0", Event: models.LiveEvent{EventID: deniedEventID, OrgID: orgID, Type: models.LiveEventCodeReviewUpdated, Scope: models.LiveEventScopeResource, Audience: models.LiveAudienceRepository, RepositoryID: &deniedRepo}})
	time.Sleep(20 * time.Millisecond)
	require.NotContains(t, rr.BodyString(), deniedEventID.String(), "repository events outside the authorization snapshot must be filtered")
	allowedEventID := uuid.New()
	manager.DeliverForTest(cache.LiveBusMessage{StreamID: "2-0", Event: models.LiveEvent{EventID: allowedEventID, OrgID: orgID, Type: models.LiveEventCodeReviewUpdated, Scope: models.LiveEventScopeResource, Audience: models.LiveAudienceRepository, RepositoryID: &allowedRepo}})
	require.Eventually(t, func() bool { return strings.Contains(rr.BodyString(), allowedEventID.String()) }, time.Second, 10*time.Millisecond, "authorized repository events should reach the browser")
	cancel()
	<-done
}

func TestLiveEventAuthorizationSnapshotIsSharedPerUserAndOrg(t *testing.T) {
	t.Parallel()
	memberships := &countingLiveMembershipStub{}
	handler := NewLiveEventHandler(nil, nil, memberships, liveRepositoryStub{}, zerolog.Nop())
	userID, orgID := uuid.New(), uuid.New()
	var wg sync.WaitGroup
	var allAllowed atomic.Bool
	allAllowed.Store(true)
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if !handler.authorizationSnapshot(context.Background(), userID, orgID).allowed {
				allAllowed.Store(false)
			}
		}()
	}
	wg.Wait()
	require.True(t, allAllowed.Load(), "shared snapshot should authorize every concurrent connection")
	require.Equal(t, int32(1), memberships.calls.Load(), "concurrent connections should share one membership authorization read")
}

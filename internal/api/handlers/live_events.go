package handlers

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/api/sse"
	"github.com/assembledhq/143/internal/cache"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/liveevents"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
	"golang.org/x/sync/singleflight"
)

const liveHeartbeatBaseInterval = 20 * time.Second

var (
	liveClientMeter      = otel.Meter("github.com/assembledhq/143/live_client")
	liveClientSamples, _ = liveClientMeter.Int64Counter("live_events.client_samples")
	liveClientLatency, _ = liveClientMeter.Float64Histogram("live_events.client_latency_ms", otelmetric.WithUnit("ms"))
)

var allowedLiveTelemetryNames = map[string]struct{}{
	"connection_health": {}, "event_processed": {}, "hidden_refetch_suppressed": {},
	"connection_duration": {},
	"refetch_completed":   {}, "initial_sync_completed": {}, "initial_sync_failed": {},
	"resync_completed": {}, "resync_failed": {}, "projection_rejected": {},
	"projection_rendered": {}, "refetch_started": {}, "visibility_catch_up": {},
	"canonical_rendered": {}, "leader_state": {}, "fallback_poll": {},
}

func nextLiveHeartbeatInterval() time.Duration {
	return liveHeartbeatBaseInterval - 5*time.Second + time.Duration(secureJitter(int64(10*time.Second)))
}

func secureJitter(limit int64) int64 {
	if limit <= 0 {
		return 0
	}
	value, err := cryptorand.Int(cryptorand.Reader, big.NewInt(limit))
	if err != nil {
		return 0
	}
	return value.Int64()
}

type liveEventMembershipStore interface {
	Get(context.Context, uuid.UUID, uuid.UUID) (models.OrganizationMembership, error)
}
type liveEventRepositoryStore interface {
	ListIDsForLiveAuthorization(context.Context, uuid.UUID) ([]uuid.UUID, error)
}

type LiveEventHandler struct {
	redis        *cache.Client
	manager      *liveevents.Manager
	memberships  liveEventMembershipStore
	repositories liveEventRepositoryStore
	logger       zerolog.Logger
	authMu       sync.Mutex
	authCache    map[string]liveAuthorizationCacheEntry
	authGroup    singleflight.Group
}

type liveAuthorizationCacheEntry struct {
	allowed      bool
	repositories map[uuid.UUID]struct{}
	expiresAt    time.Time
}

func NewLiveEventHandler(redisClient *cache.Client, manager *liveevents.Manager, memberships liveEventMembershipStore, repositories liveEventRepositoryStore, logger zerolog.Logger) *LiveEventHandler {
	return &LiveEventHandler{redis: redisClient, manager: manager, memberships: memberships, repositories: repositories, logger: logger, authCache: make(map[string]liveAuthorizationCacheEntry)}
}

func (h *LiveEventHandler) Telemetry(w http.ResponseWriter, r *http.Request) {
	_ = middleware.OrgIDFromContext(r.Context())
	var body struct {
		Samples []struct {
			Name       string   `json:"name"`
			DurationMS *float64 `json:"duration_ms,omitempty"`
			LagMS      *float64 `json:"lag_ms,omitempty"`
			LatencyMS  *float64 `json:"latency_ms,omitempty"`
		} `json:"samples"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Samples) > 100 {
		writeError(w, r, http.StatusBadRequest, "INVALID_LIVE_TELEMETRY", "telemetry samples must be a valid bounded batch")
		return
	}
	for _, sample := range body.Samples {
		if _, allowed := allowedLiveTelemetryNames[sample.Name]; !allowed {
			continue
		}
		attrs := otelmetric.WithAttributes(attribute.String("sample", sample.Name))
		liveClientSamples.Add(r.Context(), 1, attrs)
		if sample.DurationMS != nil {
			liveClientLatency.Record(r.Context(), *sample.DurationMS, attrs)
		}
		if sample.LagMS != nil {
			liveClientLatency.Record(r.Context(), *sample.LagMS, attrs)
		}
		if sample.LatencyMS != nil {
			liveClientLatency.Record(r.Context(), *sample.LatencyMS, attrs)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *LiveEventHandler) authorizationSnapshot(ctx context.Context, userID, orgID uuid.UUID) liveAuthorizationCacheEntry {
	key := userID.String() + ":" + orgID.String()
	now := time.Now()
	h.authMu.Lock()
	cached, ok := h.authCache[key]
	h.authMu.Unlock()
	if ok && now.Before(cached.expiresAt) {
		return cached
	}
	value, _, _ := h.authGroup.Do(key, func() (any, error) {
		_, err := h.memberships.Get(ctx, userID, orgID)
		entry := liveAuthorizationCacheEntry{allowed: err == nil, repositories: make(map[uuid.UUID]struct{}), expiresAt: time.Now().Add(60 * time.Second)}
		if err == nil && h.repositories != nil {
			ids, repositoriesErr := h.repositories.ListIDsForLiveAuthorization(ctx, orgID)
			if repositoriesErr != nil {
				entry.allowed = false
			} else {
				for _, id := range ids {
					entry.repositories[id] = struct{}{}
				}
			}
		}
		h.authMu.Lock()
		h.authCache[key] = entry
		h.authMu.Unlock()
		return entry, nil
	})
	entry, _ := value.(liveAuthorizationCacheEntry)
	return entry
}

type liveReady struct {
	ServerTime          time.Time `json:"server_time"`
	SchemaVersion       int       `json:"schema_version"`
	InitialSyncRequired bool      `json:"initial_sync_required"`
	ThroughStreamID     string    `json:"through_stream_id"`
	BusHealthEpoch      uint64    `json:"bus_health_epoch"`
}
type liveHeartbeat struct {
	ServerTime     time.Time `json:"server_time"`
	BusHealthEpoch uint64    `json:"bus_health_epoch"`
}
type liveResync struct {
	Cause           string `json:"cause"`
	ThroughStreamID string `json:"through_stream_id"`
}

func parseRedisStreamID(value string) (int64, int64, bool) {
	parts := strings.Split(value, "-")
	if len(parts) != 2 {
		return 0, 0, false
	}
	ms, err1 := strconv.ParseInt(parts[0], 10, 64)
	seq, err2 := strconv.ParseInt(parts[1], 10, 64)
	return ms, seq, err1 == nil && err2 == nil && ms >= 0 && seq >= 0
}
func compareRedisStreamID(a, b string) int {
	am, as, _ := parseRedisStreamID(a)
	bm, bs, _ := parseRedisStreamID(b)
	if am < bm || (am == bm && as < bs) {
		return -1
	}
	if am == bm && as == bs {
		return 0
	}
	return 1
}

func (h *LiveEventHandler) Stream(w http.ResponseWriter, r *http.Request) {
	_ = middleware.OrgIDFromContext(r.Context())
	orgID, err := uuid.Parse(r.URL.Query().Get("org_id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ORG_ID", "org_id is required and must be a UUID")
		return
	}
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return
	}
	if h.memberships == nil {
		writeError(w, r, http.StatusServiceUnavailable, "LIVE_EVENTS_UNAVAILABLE", "live events unavailable")
		return
	}
	if _, membershipErr := h.memberships.Get(r.Context(), user.ID, orgID); membershipErr != nil {
		if errors.Is(membershipErr, pgx.ErrNoRows) {
			writeError(w, r, http.StatusForbidden, "FORBIDDEN", "organization access denied")
		} else {
			writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to authorize live stream", membershipErr)
		}
		return
	}
	snapshot := h.authorizationSnapshot(r.Context(), user.ID, orgID)
	if !snapshot.allowed {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to build live authorization snapshot")
		return
	}
	cursor := r.URL.Query().Get("last_event_id")
	if cursor == "" {
		cursor = r.Header.Get("Last-Event-ID")
	}
	_, _, cursorValid := parseRedisStreamID(cursor)
	malformedCursor := cursor != "" && !cursorValid
	if h.redis == nil || h.manager == nil {
		writeError(w, r, http.StatusServiceUnavailable, "LIVE_EVENTS_UNAVAILABLE", "live events unavailable")
		return
	}
	healthy, epoch := h.manager.Healthy(orgID)
	if !healthy {
		w.Header().Set("Retry-After", "2")
		writeError(w, r, http.StatusServiceUnavailable, "LIVE_EVENTS_UNAVAILABLE", "live events temporarily unavailable")
		return
	}

	allow := func(event models.LiveEvent) bool {
		if event.Type == models.LiveEventAuthorizationChanged {
			return false
		}
		if event.Audience == models.LiveAudienceOrg {
			return true
		}
		if event.RepositoryID != nil {
			_, allowed := snapshot.repositories[*event.RepositoryID]
			return allowed
		}
		// Existing resource REST endpoints are org-member visible unless they
		// carry an explicit repository boundary in the event envelope.
		return event.Audience == models.LiveAudienceResource
	}
	subscriber, unsubscribe, err := h.manager.SubscribeForUser(orgID, user.ID, allow)
	if err != nil {
		w.Header().Set("Retry-After", "2")
		writeError(w, r, http.StatusServiceUnavailable, "LIVE_EVENTS_UNAVAILABLE", "live events temporarily unavailable")
		return
	}
	defer unsubscribe()
	bounds, err := h.redis.LiveReplayBounds(r.Context(), orgID)
	if err != nil {
		writeError(w, r, http.StatusServiceUnavailable, "LIVE_EVENTS_UNAVAILABLE", "live replay unavailable")
		return
	}

	sw := sse.NewWriter(w)
	if sw == nil {
		writeError(w, r, http.StatusInternalServerError, "SSE_NOT_SUPPORTED", "streaming not supported")
		return
	}
	writeTerminal := func(event string, payload any) {
		if err := sw.WriteEvent(sse.EventType(event), payload); err != nil {
			h.logger.Warn().Err(err).Str("event", event).Msg("failed to write terminal live event")
			return
		}
		if err := sw.Flush(); err != nil {
			h.logger.Warn().Err(err).Str("event", event).Msg("failed to flush terminal live event")
		}
	}
	initialSync := cursor == ""
	if malformedCursor {
		if err := sw.WriteEvent("live.resync", liveResync{Cause: "malformed_cursor", ThroughStreamID: bounds.Last}); err != nil {
			h.logger.Warn().Err(err).Msg("failed to write malformed-cursor resync")
			return
		}
		if err := sw.Flush(); err != nil {
			h.logger.Warn().Err(err).Msg("failed to flush malformed-cursor resync")
		}
		return
	}
	if cursor != "" && (compareRedisStreamID(cursor, bounds.Last) > 0 || (bounds.First != "0-0" && compareRedisStreamID(cursor, bounds.First) < 0)) {
		writeTerminal("live.resync", liveResync{Cause: "replay_window_missed", ThroughStreamID: bounds.Last})
		return
	}
	seen := make(map[uuid.UUID]struct{})
	rememberSeen := func(id uuid.UUID) {
		if len(seen) >= 4096 {
			clear(seen)
		}
		seen[id] = struct{}{}
	}
	if cursor != "" && compareRedisStreamID(cursor, bounds.Last) < 0 {
		replay, replayErr := h.redis.ReplayLiveEvents(r.Context(), orgID, cursor, bounds.Last, cache.LiveReplayLimit)
		if replayErr != nil {
			writeTerminal("live.resync", liveResync{Cause: "replay_too_large", ThroughStreamID: bounds.Last})
			return
		}
		for _, message := range replay {
			if !allow(message.Event) {
				continue
			}
			if _, ok := seen[message.Event.EventID]; ok {
				continue
			}
			rememberSeen(message.Event.EventID)
			if err := sw.WriteEventID("live.event", message.StreamID, message.Event); err != nil {
				return
			}
		}
	}
	buffered, resync := subscriber.Drain()
	if resync {
		writeTerminal("live.resync", liveResync{Cause: "client_mailbox_overflow", ThroughStreamID: bounds.Last})
		return
	}
	sort.Slice(buffered, func(i, j int) bool { return compareRedisStreamID(buffered[i].StreamID, buffered[j].StreamID) < 0 })
	for _, message := range buffered {
		if compareRedisStreamID(message.StreamID, bounds.Last) <= 0 {
			continue
		}
		if _, ok := seen[message.Event.EventID]; ok {
			continue
		}
		rememberSeen(message.Event.EventID)
		if err := sw.WriteEventID("live.event", message.StreamID, message.Event); err != nil {
			return
		}
	}
	if err := sw.WriteEvent("live.ready", liveReady{ServerTime: time.Now().UTC(), SchemaVersion: models.LiveEventSchemaVersion, InitialSyncRequired: initialSync, ThroughStreamID: bounds.Last, BusHealthEpoch: epoch}); err != nil {
		return
	}
	if err := sw.Flush(); err != nil {
		return
	}

	heartbeat := time.NewTimer(nextLiveHeartbeatInterval())
	defer heartbeat.Stop()
	maxAge := time.NewTimer(15*time.Minute + time.Duration(secureJitter(int64(15*time.Minute))))
	defer maxAge.Stop()
	authorizationCheck := time.NewTicker(60 * time.Second)
	defer authorizationCheck.Stop()
	var authExpiry <-chan time.Time
	var authExpiryTimer *time.Timer
	if session := middleware.AuthSessionFromContext(r.Context()); session != nil {
		delay := time.Until(session.ExpiresAt)
		if delay < 0 {
			delay = 0
		}
		authExpiryTimer = time.NewTimer(delay)
		authExpiry = authExpiryTimer.C
		defer authExpiryTimer.Stop()
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case <-maxAge.C:
			writeTerminal("server.draining", map[string]any{"retry_after_ms": 1000 + secureJitter(4000)})
			return
		case <-authExpiry:
			return
		case <-authorizationCheck.C:
			if !h.authorizationSnapshot(r.Context(), user.ID, orgID).allowed {
				return
			}
		case <-heartbeat.C:
			healthy, currentEpoch := h.manager.Healthy(orgID)
			if !healthy || currentEpoch != epoch {
				writeTerminal("live.degraded", map[string]any{"cause": "redis_bus_disconnected", "bus_shard": h.manager.ShardForOrg(orgID)})
				return
			}
			if err := sw.WriteEvent("live.heartbeat", liveHeartbeat{ServerTime: time.Now().UTC(), BusHealthEpoch: epoch}); err != nil {
				return
			}
			if err := sw.Flush(); err != nil {
				return
			}
			heartbeat.Reset(nextLiveHeartbeatInterval())
		case <-subscriber.Wake():
			if subscriber.Closed() {
				if subscriber.CloseReason() == "authorization_changed" {
					return
				}
				if subscriber.CloseReason() == "draining" {
					writeTerminal("server.draining", map[string]any{"retry_after_ms": 1000 + secureJitter(4000)})
				} else {
					writeTerminal("live.degraded", map[string]any{"cause": "redis_bus_disconnected", "bus_shard": h.manager.ShardForOrg(orgID)})
				}
				return
			}
			messages, overflow := subscriber.Drain()
			if overflow {
				latest, boundsErr := h.redis.LiveReplayBounds(r.Context(), orgID)
				if boundsErr != nil {
					h.logger.Warn().Err(boundsErr).Msg("failed to capture overflow replay checkpoint")
					return
				}
				writeTerminal("live.resync", liveResync{Cause: "client_mailbox_overflow", ThroughStreamID: latest.Last})
				return
			}
			sort.Slice(messages, func(i, j int) bool { return compareRedisStreamID(messages[i].StreamID, messages[j].StreamID) < 0 })
			for _, message := range messages {
				if _, ok := seen[message.Event.EventID]; ok {
					continue
				}
				rememberSeen(message.Event.EventID)
				if err := sw.WriteEventID("live.event", message.StreamID, message.Event); err != nil {
					return
				}
			}
			if err := sw.Flush(); err != nil {
				return
			}
		}
	}
}

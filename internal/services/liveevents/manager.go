package liveevents

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/assembledhq/143/internal/cache"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

const DefaultMailboxSize = 16
const (
	MaxConnectionsPerNode = 20_000
	MaxConnectionsPerOrg  = 5_000
	MaxConnectionsPerUser = 64
)

type Subscriber struct {
	OrgID       uuid.UUID
	UserID      uuid.UUID
	wake        chan struct{}
	mu          sync.Mutex
	pending     map[string]cache.LiveBusMessage
	needsResync bool
	closed      bool
	closeReason string
	allow       func(models.LiveEvent) bool
}

func newSubscriber(orgID uuid.UUID, allow func(models.LiveEvent) bool) *Subscriber {
	return &Subscriber{OrgID: orgID, wake: make(chan struct{}, 1), pending: make(map[string]cache.LiveBusMessage), allow: allow}
}

func eventKey(event models.LiveEvent) string {
	if event.ResourceID != nil {
		return string(event.Type) + ":" + event.ResourceID.String()
	}
	return string(event.Type) + ":" + string(event.Scope)
}

func (s *Subscriber) enqueue(message cache.LiveBusMessage) {
	if s.allow != nil && !s.allow(message.Event) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.needsResync {
		return
	}
	key := eventKey(message.Event)
	if old, ok := s.pending[key]; ok && old.Event.Version != nil && message.Event.Version != nil && *old.Event.Version >= *message.Event.Version {
		return
	}
	if _, exists := s.pending[key]; !exists && len(s.pending) >= DefaultMailboxSize {
		clear(s.pending)
		s.needsResync = true
		metrics.resync(context.Background(), "mailbox_overflow")
	} else {
		s.pending[key] = message
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Subscriber) Wake() <-chan struct{} { return s.wake }

func (s *Subscriber) Drain() ([]cache.LiveBusMessage, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	resync := s.needsResync
	result := make([]cache.LiveBusMessage, 0, len(s.pending))
	for _, message := range s.pending {
		result = append(result, message)
	}
	clear(s.pending)
	return result, resync
}

func (s *Subscriber) Closed() bool        { s.mu.Lock(); defer s.mu.Unlock(); return s.closed }
func (s *Subscriber) CloseReason() string { s.mu.Lock(); defer s.mu.Unlock(); return s.closeReason }

func (s *Subscriber) closeWithReason(reason string) {
	s.mu.Lock()
	s.closed = true
	s.closeReason = reason
	clear(s.pending)
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}
func (s *Subscriber) close() { s.closeWithReason("closed") }

type Manager struct {
	redis               *cache.Client
	logger              zerolog.Logger
	shards              int
	mu                  sync.RWMutex
	subs                map[uuid.UUID]map[*Subscriber]struct{}
	total               int
	health              []atomic.Bool
	epoch               []atomic.Uint64
	publisherHealthy    atomic.Bool
	publishProbeHealthy atomic.Bool
	publishFailures     atomic.Int32
	draining            atomic.Bool
	users               map[uuid.UUID]int
}

func NewManager(redisClient *cache.Client, shards int, logger zerolog.Logger) *Manager {
	if shards <= 0 {
		shards = cache.DefaultLiveBusShards
	}
	m := &Manager{redis: redisClient, logger: logger, shards: shards, subs: make(map[uuid.UUID]map[*Subscriber]struct{}), users: make(map[uuid.UUID]int), health: make([]atomic.Bool, shards), epoch: make([]atomic.Uint64, shards)}
	m.publisherHealthy.Store(true)
	m.publishProbeHealthy.Store(true)
	return m
}

func (m *Manager) StartHealthMonitor(ctx context.Context, store *db.LiveEventStore) {
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		high, low := 0, 0
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if !m.needsHealthSample() {
					high, low = 0, 0
					continue
				}
				age, err := store.OldestPendingAge(ctx)
				if err == nil {
					metrics.pending(ctx, age)
				}
				if !m.publishProbeHealthy.Load() && m.redis != nil {
					if probeErr := m.redis.ProbeLivePublish(ctx); probeErr == nil {
						m.publishProbeHealthy.Store(true)
					} else {
						m.logger.Debug().Err(probeErr).Msg("live event publish recovery probe failed")
					}
				}
				if err != nil || age > 2*time.Second {
					high++
					low = 0
				} else if age < 500*time.Millisecond && m.redis.Available() && m.publishProbeHealthy.Load() {
					low++
					high = 0
				} else {
					high, low = 0, 0
				}
				if high >= 2 {
					if m.publisherHealthy.Load() {
						m.logger.Warn().Dur("oldest_pending_age", age).Msg("live event outbox is degraded")
					}
					m.setPublisherHealthy(false)
				}
				if low >= 3 {
					m.setPublisherHealthy(true)
				}
			}
		}
	}()
}

// Healthy idle nodes do not poll Postgres. A degraded node must keep sampling
// even after it has closed its subscribers; otherwise it can never satisfy the
// recovery hysteresis needed to admit the next reconnecting client.
func (m *Manager) needsHealthSample() bool {
	return m.ActiveSubscribers() > 0 || !m.publisherHealthy.Load()
}

func (m *Manager) RecordPublishResult(success bool) {
	if success {
		m.publishFailures.Store(0)
		m.publishProbeHealthy.Store(true)
		return
	}
	m.publishProbeHealthy.Store(false)
	if m.publishFailures.Add(1) >= 3 {
		m.setPublisherHealthy(false)
	}
}

func (m *Manager) ActiveSubscribers() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.total
}

func (m *Manager) setPublisherHealthy(healthy bool) {
	if m.publisherHealthy.Swap(healthy) == healthy || healthy {
		return
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, subscribers := range m.subs {
		for subscriber := range subscribers {
			subscriber.closeWithReason("degraded")
		}
	}
}

func (m *Manager) Start(ctx context.Context) {
	for shard := 0; shard < m.shards; shard++ {
		go m.runShard(ctx, shard)
	}
}

func (m *Manager) runShard(ctx context.Context, shard int) {
	for ctx.Err() == nil {
		pubsub := m.redis.SubscribeLiveShard(ctx, shard)
		if pubsub == nil {
			return
		}
		if _, err := pubsub.Receive(ctx); err != nil {
			metrics.reconnect(ctx, shard)
			m.setHealthy(shard, false)
			if closeErr := pubsub.Close(); closeErr != nil {
				m.logger.Warn().Err(closeErr).Int("bus_shard", shard).Msg("failed to close unacknowledged live bus subscription")
			}
			if !waitForReconnect(ctx) {
				return
			}
			continue
		}
		m.setHealthy(shard, true)
		channel := pubsub.Channel()
		for {
			select {
			case <-ctx.Done():
				m.setHealthy(shard, false)
				if closeErr := pubsub.Close(); closeErr != nil {
					m.logger.Warn().Err(closeErr).Int("bus_shard", shard).Msg("failed to close live bus subscription during shutdown")
				}
				return
			case msg, ok := <-channel:
				if !ok {
					metrics.reconnect(ctx, shard)
					m.setHealthy(shard, false)
					if closeErr := pubsub.Close(); closeErr != nil {
						m.logger.Warn().Err(closeErr).Int("bus_shard", shard).Msg("failed to close disconnected live bus subscription")
					}
					goto reconnect
				}
				metrics.received(ctx, shard, len(msg.Payload))
				var event cache.LiveBusMessage
				if err := json.Unmarshal([]byte(msg.Payload), &event); err != nil {
					m.logger.Warn().Err(err).Int("bus_shard", shard).Msg("invalid live bus message")
					continue
				}
				if event.Probe {
					continue
				}
				m.deliver(event)
			}
		}
	reconnect:
		if !waitForReconnect(ctx) {
			return
		}
	}
}

func waitForReconnect(ctx context.Context) bool {
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (m *Manager) setHealthy(shard int, healthy bool) {
	previous := m.health[shard].Swap(healthy)
	if previous != healthy {
		m.epoch[shard].Add(1)
		if healthy {
			metrics.shardState(context.Background(), shard, 1)
		} else {
			metrics.shardState(context.Background(), shard, -1)
		}
	}
	if !healthy {
		m.mu.RLock()
		for orgID, subscribers := range m.subs {
			if cache.LiveBusShard(orgID, m.shards) != shard {
				continue
			}
			for subscriber := range subscribers {
				subscriber.closeWithReason("degraded")
			}
		}
		m.mu.RUnlock()
	}
}

func (m *Manager) deliver(message cache.LiveBusMessage) {
	if message.Event.Type == models.LiveEventAuthorizationChanged {
		metrics.revoked(context.Background(), message.Event.ChangedAt)
		var payload struct {
			UserID uuid.UUID `json:"user_id"`
		}
		if json.Unmarshal(message.Event.Payload, &payload) == nil && payload.UserID != uuid.Nil {
			m.mu.RLock()
			defer m.mu.RUnlock()
			for subscriber := range m.subs[message.Event.OrgID] {
				if subscriber.UserID == payload.UserID {
					subscriber.closeWithReason("authorization_changed")
				}
			}
		}
		return
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	delivered := 0
	for subscriber := range m.subs[message.Event.OrgID] {
		subscriber.enqueue(message)
		delivered++
	}
	metrics.delivered(context.Background(), string(message.Event.Type), delivered)
}

func (m *Manager) Subscribe(orgID uuid.UUID, allow func(models.LiveEvent) bool) (*Subscriber, func(), error) {
	return m.SubscribeForUser(orgID, uuid.Nil, allow)
}

func (m *Manager) SubscribeForUser(orgID, userID uuid.UUID, allow func(models.LiveEvent) bool) (*Subscriber, func(), error) {
	shard := cache.LiveBusShard(orgID, m.shards)
	if m.draining.Load() || !m.health[shard].Load() || !m.publisherHealthy.Load() {
		return nil, nil, fmt.Errorf("live bus shard unavailable")
	}
	subscriber := newSubscriber(orgID, allow)
	subscriber.UserID = userID
	m.mu.Lock()
	// Recheck while holding the fan-out lock. Shard and publisher degradation
	// store their unhealthy state before taking this lock to close subscribers,
	// so either this admission is visible to that sweep or it is rejected here.
	if m.draining.Load() || !m.health[shard].Load() || !m.publisherHealthy.Load() {
		m.mu.Unlock()
		return nil, nil, fmt.Errorf("live bus shard unavailable")
	}
	if m.total >= MaxConnectionsPerNode || len(m.subs[orgID]) >= MaxConnectionsPerOrg || (userID != uuid.Nil && m.users[userID] >= MaxConnectionsPerUser) {
		m.mu.Unlock()
		return nil, nil, fmt.Errorf("live connection capacity exceeded")
	}
	if m.subs[orgID] == nil {
		m.subs[orgID] = make(map[*Subscriber]struct{})
		metrics.group(context.Background(), 1)
	}
	m.subs[orgID][subscriber] = struct{}{}
	m.total++
	if userID != uuid.Nil {
		m.users[userID]++
	}
	m.mu.Unlock()
	metrics.connection(context.Background(), 1)
	var cancelOnce sync.Once
	cancel := func() {
		cancelOnce.Do(func() {
			m.mu.Lock()
			delete(m.subs[orgID], subscriber)
			m.total--
			if len(m.subs[orgID]) == 0 {
				delete(m.subs, orgID)
				metrics.group(context.Background(), -1)
			}
			if userID != uuid.Nil {
				m.users[userID]--
				if m.users[userID] <= 0 {
					delete(m.users, userID)
				}
			}
			m.mu.Unlock()
			metrics.connection(context.Background(), -1)
			subscriber.close()
		})
	}
	return subscriber, cancel, nil
}

func (m *Manager) Drain() {
	m.draining.Store(true)
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, subscribers := range m.subs {
		for subscriber := range subscribers {
			subscriber.closeWithReason("draining")
		}
	}
}

func (m *Manager) Healthy(orgID uuid.UUID) (bool, uint64) {
	shard := cache.LiveBusShard(orgID, m.shards)
	return m.health[shard].Load() && m.publisherHealthy.Load(), m.epoch[shard].Load()
}

func (m *Manager) ShardForOrg(orgID uuid.UUID) int { return cache.LiveBusShard(orgID, m.shards) }

func (m *Manager) SetShardHealthyForTest(shard int, healthy bool) { m.setHealthy(shard, healthy) }
func (m *Manager) DeliverForTest(message cache.LiveBusMessage)    { m.deliver(message) }

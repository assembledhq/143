package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
)

const (
	logStreamMaxLen          = 10000
	statusStreamMaxLen       = 100
	logRingBufferSize        = 1000
	perClientBufferSize      = 256
	maxRedisLogPayloadBytes  = 4 * 1024
	sessionStreamExpiryAfter = time.Hour
)

type SessionTerminalLister interface {
	// lint:allow-no-orgid reason="cross-org Redis cleanup scans terminal sessions across the whole fleet"
	ListTerminalEndedBefore(ctx context.Context, before time.Time, limit int) ([]models.Session, error)
}

type StreamedLog struct {
	StreamID string
	Log      models.SessionLog
}

type logSubscriber struct {
	ch     chan StreamedLog
	reason atomic.Value
}

type statusSubscriber struct {
	ch     chan models.Session
	reason atomic.Value
}

type logRingBuffer struct {
	items []StreamedLog
	full  bool
	next  int
}

func newLogRingBuffer(size int) *logRingBuffer {
	return &logRingBuffer{items: make([]StreamedLog, size)}
}

func (b *logRingBuffer) add(item StreamedLog) {
	if len(b.items) == 0 {
		return
	}
	b.items[b.next] = item
	b.next = (b.next + 1) % len(b.items)
	if b.next == 0 {
		b.full = true
	}
}

func (b *logRingBuffer) since(streamID string) ([]StreamedLog, bool) {
	if streamID == "" {
		return nil, true
	}
	want, err := ParseLogStreamID(streamID)
	if err != nil {
		return nil, false
	}

	ordered := b.snapshot()
	if len(ordered) == 0 {
		return nil, true
	}
	oldest, err := ParseLogStreamID(ordered[0].StreamID)
	if err != nil {
		return nil, false
	}
	if b.full && want < oldest {
		return nil, false
	}

	out := make([]StreamedLog, 0, len(ordered))
	for _, item := range ordered {
		itemID, parseErr := ParseLogStreamID(item.StreamID)
		if parseErr != nil {
			return nil, false
		}
		if itemID > want {
			out = append(out, item)
		}
	}
	return out, true
}

func (b *logRingBuffer) snapshot() []StreamedLog {
	if len(b.items) == 0 {
		return nil
	}
	if !b.full {
		out := make([]StreamedLog, 0, b.next)
		out = append(out, b.items[:b.next]...)
		return out
	}
	out := make([]StreamedLog, 0, len(b.items))
	out = append(out, b.items[b.next:]...)
	out = append(out, b.items[:b.next]...)
	return out
}

type logFanout struct {
	sessionID uuid.UUID
	streamKey string
	client    *Client
	logger    zerolog.Logger

	ctx     context.Context
	cancel  context.CancelFunc
	onExit  func()
	mu      sync.Mutex
	clients map[*logSubscriber]struct{}
	ring    *logRingBuffer
}

type statusFanout struct {
	sessionID uuid.UUID
	streamKey string
	client    *Client
	logger    zerolog.Logger

	ctx     context.Context
	cancel  context.CancelFunc
	onExit  func()
	mu      sync.Mutex
	clients map[*statusSubscriber]struct{}
}

type LogSubscription struct {
	C       <-chan StreamedLog
	client  *logSubscriber
	closeFn func()
}

func (s *LogSubscription) CloseReason() string {
	if s == nil || s.client == nil {
		return ""
	}
	v := s.client.reason.Load()
	if msg, ok := v.(string); ok {
		return msg
	}
	return ""
}

func (s *LogSubscription) Close() {
	if s != nil && s.closeFn != nil {
		s.closeFn()
	}
}

type StatusSubscription struct {
	C       <-chan models.Session
	client  *statusSubscriber
	closeFn func()
}

func (s *StatusSubscription) CloseReason() string {
	if s == nil || s.client == nil {
		return ""
	}
	v := s.client.reason.Load()
	if msg, ok := v.(string); ok {
		return msg
	}
	return ""
}

func (s *StatusSubscription) Close() {
	if s != nil && s.closeFn != nil {
		s.closeFn()
	}
}

type SessionStreams struct {
	client  *Client
	logger  zerolog.Logger
	metrics *Metrics

	logMu      sync.Mutex
	logFanouts map[uuid.UUID]*logFanout

	statusMu      sync.Mutex
	statusFanouts map[uuid.UUID]*statusFanout
}

func NewSessionStreams(client *Client, logger zerolog.Logger, metrics *Metrics) *SessionStreams {
	if client == nil {
		return nil
	}
	return &SessionStreams{
		client:        client,
		logger:        logger,
		metrics:       metrics,
		logFanouts:    make(map[uuid.UUID]*logFanout),
		statusFanouts: make(map[uuid.UUID]*statusFanout),
	}
}

func (s *SessionStreams) Available() bool {
	return s != nil && s.client != nil && s.client.Available()
}

func SessionLogStreamID(logID int64) string {
	return fmt.Sprintf("%d-0", logID)
}

func ParseLogStreamID(streamID string) (int64, error) {
	if streamID == "" {
		return 0, nil
	}
	parts := stringsSplit2(streamID, '-')
	if len(parts) == 0 || parts[0] == "" {
		return 0, fmt.Errorf("invalid stream id: %q", streamID)
	}
	return strconv.ParseInt(parts[0], 10, 64)
}

func (s *SessionStreams) PublishLog(ctx context.Context, log *models.SessionLog) error {
	if s == nil || s.client == nil || log == nil {
		return nil
	}

	payload := *log
	encoded, err := clampLogPayload(payload)
	if err != nil {
		return err
	}
	s.metrics.RecordLogEntryBytes(ctx, len(encoded))
	args := &redis.XAddArgs{
		Stream: maxLenStreamKey(logStreamKey(log.SessionID), logStreamMaxLen),
		ID:     SessionLogStreamID(log.ID),
		Values: map[string]any{"json": string(encoded)},
		MaxLen: logStreamMaxLen,
		Approx: true,
	}
	err = s.client.doCommand(ctx, "xadd", func() error {
		return s.client.raw().XAdd(ctx, args).Err()
	})
	if err != nil {
		s.client.logger.Warn().Err(err).Str("stream", logStreamKey(log.SessionID)).Msg("XADD failed: session logs")
		return err
	}
	return nil
}

func (s *SessionStreams) PublishStatus(ctx context.Context, session *models.Session) error {
	if s == nil || s.client == nil || session == nil {
		return nil
	}
	encoded, err := json.Marshal(session)
	if err != nil {
		return err
	}
	args := &redis.XAddArgs{
		Stream: statusStreamKey(session.ID),
		Values: map[string]any{"json": string(encoded)},
		MaxLen: statusStreamMaxLen,
		Approx: true,
	}
	err = s.client.doCommand(ctx, "xadd", func() error {
		return s.client.raw().XAdd(ctx, args).Err()
	})
	if err != nil {
		s.client.logger.Warn().Err(err).Str("stream", statusStreamKey(session.ID)).Msg("XADD failed: session status")
		return err
	}
	if isTerminalSessionStatus(session.Status) {
		return s.ScheduleExpiry(ctx, session.ID, terminalExpiryAt(session))
	}
	return nil
}

func (s *SessionStreams) ScheduleExpiry(ctx context.Context, sessionID uuid.UUID, expiry time.Time) error {
	if s == nil || s.client == nil {
		return nil
	}
	err := s.client.doCommand(ctx, "expireat", func() error {
		pipe := s.client.raw().Pipeline()
		pipe.ExpireAt(ctx, logStreamKey(sessionID), expiry)
		pipe.ExpireAt(ctx, statusStreamKey(sessionID), expiry)
		_, pipeErr := pipe.Exec(ctx)
		return pipeErr
	})
	if err != nil {
		return err
	}
	return nil
}

func (s *SessionStreams) DeleteSessionStreams(ctx context.Context, sessionID uuid.UUID) error {
	if s == nil || s.client == nil {
		return nil
	}
	return s.client.doCommand(ctx, "del", func() error {
		return s.client.raw().Del(ctx, logStreamKey(sessionID), statusStreamKey(sessionID)).Err()
	})
}

func (s *SessionStreams) ReplayBufferedLogs(sessionID uuid.UUID, lastStreamID string) ([]StreamedLog, bool) {
	if s == nil {
		return nil, false
	}
	s.logMu.Lock()
	f := s.logFanouts[sessionID]
	s.logMu.Unlock()
	if f == nil {
		return nil, false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ring.since(lastStreamID)
}

func (s *SessionStreams) RangeLogsSince(ctx context.Context, sessionID uuid.UUID, lastStreamID string, count int64) ([]StreamedLog, error) {
	if s == nil || s.client == nil {
		return nil, errors.New("redis unavailable")
	}
	start := "(" + lastStreamID
	if lastStreamID == "" {
		start = "-"
	}

	var entries []redis.XMessage
	err := s.client.doCommand(ctx, "xrange", func() error {
		var rangeErr error
		entries, rangeErr = s.client.raw().XRangeN(ctx, logStreamKey(sessionID), start, "+", count).Result()
		return rangeErr
	})
	if err != nil {
		return nil, err
	}
	out := make([]StreamedLog, 0, len(entries))
	for _, entry := range entries {
		log, decodeErr := decodeLogEntry(entry)
		if decodeErr != nil {
			s.logger.Warn().Err(decodeErr).Str("session_id", sessionID.String()).Str("stream_id", entry.ID).Msg("failed to decode Redis log entry")
			continue
		}
		out = append(out, StreamedLog{StreamID: entry.ID, Log: log})
	}
	return out, nil
}

func (s *SessionStreams) SubscribeLogs(sessionID uuid.UUID) (*LogSubscription, error) {
	if s == nil || s.client == nil {
		return nil, errors.New("redis unavailable")
	}
	f := s.ensureLogFanout(sessionID)
	sub := &logSubscriber{ch: make(chan StreamedLog, perClientBufferSize)}
	sub.reason.Store("")

	f.mu.Lock()
	f.clients[sub] = struct{}{}
	f.mu.Unlock()

	return &LogSubscription{
		C:      sub.ch,
		client: sub,
		closeFn: func() {
			f.removeClient(sub, "client_closed")
		},
	}, nil
}

func (s *SessionStreams) SubscribeStatus(sessionID uuid.UUID) (*StatusSubscription, error) {
	if s == nil || s.client == nil {
		return nil, errors.New("redis unavailable")
	}
	f := s.ensureStatusFanout(sessionID)
	sub := &statusSubscriber{ch: make(chan models.Session, perClientBufferSize)}
	sub.reason.Store("")

	f.mu.Lock()
	f.clients[sub] = struct{}{}
	f.mu.Unlock()

	return &StatusSubscription{
		C:      sub.ch,
		client: sub,
		closeFn: func() {
			f.removeClient(sub, "client_closed")
		},
	}, nil
}

func (s *SessionStreams) StartCleanup(ctx context.Context, lister SessionTerminalLister) {
	if s == nil || s.client == nil || lister == nil {
		return
	}
	go s.cleanupLoop(ctx, lister)
}

func (s *SessionStreams) cleanupLoop(ctx context.Context, lister SessionTerminalLister) {
	timer := time.NewTimer(10 * time.Minute)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}

		size, err := s.runCleanupBatch(ctx, lister)
		if err != nil {
			s.logger.Warn().Err(err).Msg("Redis session stream cleanup failed")
		}
		s.metrics.RecordCleanupBatch(ctx, size)
		if size >= 500 {
			timer.Reset(0)
		} else {
			timer.Reset(10 * time.Minute)
		}
	}
}

func (s *SessionStreams) runCleanupBatch(ctx context.Context, lister SessionTerminalLister) (int, error) {
	sessions, err := lister.ListTerminalEndedBefore(ctx, time.Now().Add(-sessionStreamExpiryAfter), 500)
	if err != nil {
		return 0, err
	}
	for _, session := range sessions {
		if err := s.DeleteSessionStreams(ctx, session.ID); err != nil {
			s.logger.Warn().Err(err).Str("session_id", session.ID.String()).Msg("failed to delete orphaned session streams")
		}
	}
	return len(sessions), nil
}

func (s *SessionStreams) ensureLogFanout(sessionID uuid.UUID) *logFanout {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	if f := s.logFanouts[sessionID]; f != nil {
		return f
	}
	ctx, cancel := context.WithCancel(context.Background())
	f := &logFanout{
		sessionID: sessionID,
		streamKey: logStreamKey(sessionID),
		client:    s.client,
		logger:    s.logger,
		ctx:       ctx,
		cancel:    cancel,
		clients:   make(map[*logSubscriber]struct{}),
		ring:      newLogRingBuffer(logRingBufferSize),
		onExit: func() {
			s.logMu.Lock()
			delete(s.logFanouts, sessionID)
			s.logMu.Unlock()
		},
	}
	s.logFanouts[sessionID] = f
	go f.run()
	return f
}

func (s *SessionStreams) ensureStatusFanout(sessionID uuid.UUID) *statusFanout {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	if f := s.statusFanouts[sessionID]; f != nil {
		return f
	}
	ctx, cancel := context.WithCancel(context.Background())
	f := &statusFanout{
		sessionID: sessionID,
		streamKey: statusStreamKey(sessionID),
		client:    s.client,
		logger:    s.logger,
		ctx:       ctx,
		cancel:    cancel,
		clients:   make(map[*statusSubscriber]struct{}),
		onExit: func() {
			s.statusMu.Lock()
			delete(s.statusFanouts, sessionID)
			s.statusMu.Unlock()
		},
	}
	s.statusFanouts[sessionID] = f
	go f.run()
	return f
}

func (f *logFanout) run() {
	defer f.onExit()
	lastID := "$"
	for {
		select {
		case <-f.ctx.Done():
			f.closeAll("retry")
			return
		default:
		}

		streams, err := f.client.raw().XRead(f.ctx, &redis.XReadArgs{
			Streams: []string{f.streamKey, lastID},
			Block:   30 * time.Second,
			Count:   100,
		}).Result()
		if err != nil {
			if errors.Is(err, context.Canceled) {
				f.closeAll("retry")
				return
			}
			if errors.Is(err, redis.Nil) {
				continue
			}
			f.client.breaker.ForceOpen()
			f.logger.Warn().Err(err).Str("session_id", f.sessionID.String()).Msg("Redis log fan-out reader failed")
			f.closeAll("retry")
			return
		}

		for _, stream := range streams {
			for _, msg := range stream.Messages {
				lastID = msg.ID
				log, decodeErr := decodeLogEntry(msg)
				if decodeErr != nil {
					f.logger.Warn().Err(decodeErr).Str("session_id", f.sessionID.String()).Str("stream_id", msg.ID).Msg("failed to decode Redis log stream entry")
					continue
				}
				entry := StreamedLog{StreamID: msg.ID, Log: log}
				f.mu.Lock()
				f.ring.add(entry)
				for sub := range f.clients {
					select {
					case sub.ch <- entry:
					default:
						f.closeClientLocked(sub, "slow_consumer")
					}
				}
				empty := len(f.clients) == 0
				f.mu.Unlock()
				if empty {
					f.cancel()
					return
				}
			}
		}
	}
}

func (f *statusFanout) run() {
	defer f.onExit()
	lastID := "$"
	for {
		select {
		case <-f.ctx.Done():
			f.closeAll("retry")
			return
		default:
		}

		streams, err := f.client.raw().XRead(f.ctx, &redis.XReadArgs{
			Streams: []string{f.streamKey, lastID},
			Block:   30 * time.Second,
			Count:   32,
		}).Result()
		if err != nil {
			if errors.Is(err, context.Canceled) {
				f.closeAll("retry")
				return
			}
			if errors.Is(err, redis.Nil) {
				continue
			}
			f.client.breaker.ForceOpen()
			f.logger.Warn().Err(err).Str("session_id", f.sessionID.String()).Msg("Redis status fan-out reader failed")
			f.closeAll("retry")
			return
		}

		for _, stream := range streams {
			for _, msg := range stream.Messages {
				lastID = msg.ID
				session, decodeErr := decodeStatusEntry(msg)
				if decodeErr != nil {
					f.logger.Warn().Err(decodeErr).Str("session_id", f.sessionID.String()).Str("stream_id", msg.ID).Msg("failed to decode Redis status stream entry")
					continue
				}
				f.mu.Lock()
				for sub := range f.clients {
					select {
					case sub.ch <- session:
					default:
						f.closeClientLocked(sub, "slow_consumer")
					}
				}
				empty := len(f.clients) == 0
				f.mu.Unlock()
				if empty {
					f.cancel()
					return
				}
			}
		}
	}
}

func (f *logFanout) closeAll(reason string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for sub := range f.clients {
		f.closeClientLocked(sub, reason)
	}
}

func (f *statusFanout) closeAll(reason string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for sub := range f.clients {
		f.closeClientLocked(sub, reason)
	}
}

func (f *logFanout) removeClient(sub *logSubscriber, reason string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.clients[sub]; !ok {
		return
	}
	f.closeClientLocked(sub, reason)
	if len(f.clients) == 0 {
		f.cancel()
	}
}

func (f *statusFanout) removeClient(sub *statusSubscriber, reason string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.clients[sub]; !ok {
		return
	}
	f.closeClientLocked(sub, reason)
	if len(f.clients) == 0 {
		f.cancel()
	}
}

func (f *logFanout) closeClientLocked(sub *logSubscriber, reason string) {
	delete(f.clients, sub)
	sub.reason.Store(reason)
	close(sub.ch)
}

func (f *statusFanout) closeClientLocked(sub *statusSubscriber, reason string) {
	delete(f.clients, sub)
	sub.reason.Store(reason)
	close(sub.ch)
}

func decodeLogEntry(entry redis.XMessage) (models.SessionLog, error) {
	raw, ok := entry.Values["json"]
	if !ok {
		return models.SessionLog{}, fmt.Errorf("missing json field")
	}
	var text string
	switch v := raw.(type) {
	case string:
		text = v
	default:
		text = fmt.Sprint(v)
	}
	var log models.SessionLog
	if err := json.Unmarshal([]byte(text), &log); err != nil {
		return models.SessionLog{}, err
	}
	return log, nil
}

func decodeStatusEntry(entry redis.XMessage) (models.Session, error) {
	raw, ok := entry.Values["json"]
	if !ok {
		return models.Session{}, fmt.Errorf("missing json field")
	}
	var text string
	switch v := raw.(type) {
	case string:
		text = v
	default:
		text = fmt.Sprint(v)
	}
	var session models.Session
	if err := json.Unmarshal([]byte(text), &session); err != nil {
		return models.Session{}, err
	}
	return session, nil
}

func clampLogPayload(log models.SessionLog) ([]byte, error) {
	encoded, err := json.Marshal(log)
	if err != nil {
		return nil, err
	}
	if len(encoded) <= maxRedisLogPayloadBytes {
		return encoded, nil
	}

	log.Metadata = nil
	encoded, err = json.Marshal(log)
	if err != nil {
		return nil, err
	}
	if len(encoded) <= maxRedisLogPayloadBytes {
		return encoded, nil
	}

	overflow := len(encoded) - maxRedisLogPayloadBytes
	if overflow < 0 {
		overflow = 0
	}
	const suffix = "… [truncated in Redis]"
	keep := len(log.Message) - overflow - len(suffix)
	if keep < 0 {
		keep = 0
	}
	if keep > len(log.Message) {
		keep = len(log.Message)
	}
	log.Message = log.Message[:keep] + suffix
	return json.Marshal(log)
}

func logStreamKey(sessionID uuid.UUID) string {
	return fmt.Sprintf("143:stream:{ses:%s}:logs", sessionID.String())
}

func statusStreamKey(sessionID uuid.UUID) string {
	return fmt.Sprintf("143:stream:{ses:%s}:status", sessionID.String())
}

func terminalExpiryAt(session *models.Session) time.Time {
	if session != nil && session.CompletedAt != nil {
		return session.CompletedAt.Add(sessionStreamExpiryAfter)
	}
	return time.Now().Add(sessionStreamExpiryAfter)
}

func isTerminalSessionStatus(status models.SessionStatus) bool {
	switch status {
	case models.SessionStatusCompleted, models.SessionStatusFailed, models.SessionStatusCancelled, models.SessionStatusPRCreated, models.SessionStatusSkipped:
		return true
	default:
		return false
	}
}

func stringsSplit2(s string, sep byte) []string {
	for i := 0; i < len(s); i++ {
		if s[i] == sep {
			return []string{s[:i], s[i+1:]}
		}
	}
	return []string{s}
}

func maxLenStreamKey(key string, _ int64) string {
	return key
}

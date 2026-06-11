package cache

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type jsonStringer string

func (s jsonStringer) String() string { return string(s) }

func testRedisClient(t *testing.T) (*Client, *miniredis.Miniredis) {
	t.Helper()

	mr := miniredis.RunT(t)
	metrics, err := NewMetrics()
	require.NoError(t, err, "Redis metrics should initialize")
	client := New(Config{Topology: "standalone", URL: "redis://" + mr.Addr()}, zerolog.Nop(), metrics)
	require.NotNil(t, client, "Redis client should initialize against miniredis")
	t.Cleanup(func() {
		err := client.Close()
		if err != nil && !strings.Contains(err.Error(), "client is closed") {
			require.NoError(t, err, "Redis test client should close cleanly")
		}
	})
	return client, mr
}

func TestSessionStreams_PublishLogAndRangeSince(t *testing.T) {
	t.Parallel()

	client, _ := testRedisClient(t)
	streams := NewSessionStreams(client, zerolog.Nop(), nil)
	sessionID := uuid.New()
	orgID := uuid.New()

	log1 := &models.SessionLog{ID: 101, SessionID: sessionID, OrgID: orgID, Level: "info", Message: "hello", TurnNumber: 1, Timestamp: time.Now()}
	log2 := &models.SessionLog{ID: 102, SessionID: sessionID, OrgID: orgID, Level: "info", Message: "world", TurnNumber: 1, Timestamp: time.Now()}

	require.NoError(t, streams.PublishLog(context.Background(), log1), "first log should publish to Redis")
	require.NoError(t, streams.PublishLog(context.Background(), log2), "second log should publish to Redis")

	got, err := streams.RangeLogsSince(context.Background(), sessionID, SessionLogStreamID(log1.ID), 100)
	require.NoError(t, err, "XRANGE catch-up should succeed")
	require.Len(t, got, 1, "catch-up should return only entries newer than the last seen ID")
	require.Equal(t, log2.ID, got[0].Log.ID, "catch-up should return the later log entry")
	require.Equal(t, SessionLogStreamID(log2.ID), got[0].StreamID, "stream ID should be derived from the durable DB log ID")
}

func TestSessionStreams_ClampsLargeLogPayload(t *testing.T) {
	t.Parallel()

	client, mr := testRedisClient(t)
	streams := NewSessionStreams(client, zerolog.Nop(), nil)
	sessionID := uuid.New()
	orgID := uuid.New()

	log := &models.SessionLog{
		ID:        201,
		SessionID: sessionID,
		OrgID:     orgID,
		Level:     "error",
		Message:   strings.Repeat("x", 6000),
		Metadata:  json.RawMessage(`{"tool_output":"` + strings.Repeat("y", 4000) + `"}`),
		Timestamp: time.Now(),
	}

	require.NoError(t, streams.PublishLog(context.Background(), log), "oversized log entry should still publish after truncation")
	entry, err := mr.Stream(logStreamKey(sessionID))
	require.NoError(t, err, "test should inspect the Redis stream")
	require.Len(t, entry, 1, "stream should contain the truncated log entry")
	require.Equal(t, SessionLogStreamID(log.ID), entry[0].ID, "stream should use the durable DB log ID as the Redis stream ID")

	raw := entry[0].Values[1]
	require.LessOrEqual(t, len(raw), maxRedisLogPayloadBytes, "stored Redis payload should respect the 4KB clamp")

	var payload struct {
		Message          string `json:"message"`
		MessageBytes     int    `json:"message_bytes"`
		MessageChars     int    `json:"message_chars"`
		MessageTruncated bool   `json:"message_truncated"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &payload), "stored Redis payload should decode as JSON")
	require.True(t, payload.MessageTruncated, "Redis-clamped payload should tell SSE clients the message is a preview")
	require.Equal(t, len([]byte(log.Message)), payload.MessageBytes, "Redis-clamped payload should preserve original byte length")
	require.Equal(t, len([]rune(log.Message)), payload.MessageChars, "Redis-clamped payload should preserve original character length")
	require.Less(t, len([]byte(payload.Message)), payload.MessageBytes, "Redis-clamped payload should contain a shorter preview message")

	ranged, err := streams.RangeLogsSince(context.Background(), sessionID, "", 100)
	require.NoError(t, err, "Redis range replay should decode the clamped payload")
	require.Len(t, ranged, 1, "Redis range replay should return the clamped log")
	require.True(t, ranged[0].Log.MessageTruncated, "decoded Redis log should keep truncation metadata for SSE")
	require.Equal(t, payload.MessageBytes, ranged[0].Log.MessageBytes, "decoded Redis log should keep original byte length for SSE")
	require.Equal(t, payload.MessageChars, ranged[0].Log.MessageChars, "decoded Redis log should keep original character length for SSE")
}

func TestSessionStreams_SubscribeLogsCloseRemovesClient(t *testing.T) {
	t.Parallel()

	client, _ := testRedisClient(t)
	streams := NewSessionStreams(client, zerolog.Nop(), nil)
	sessionID := uuid.New()

	sub, err := streams.SubscribeLogs(sessionID)
	require.NoError(t, err, "log subscription should succeed when Redis is available")
	sub.Close()
	require.Equal(t, "client_closed", sub.CloseReason(), "closing a subscription should record the close reason")
}

func TestSessionStreams_SubscribeLogs_OpensBreakerOnRedisReadFailure(t *testing.T) {
	t.Parallel()

	client, mr := testRedisClient(t)
	streams := NewSessionStreams(client, zerolog.Nop(), nil)
	sessionID := uuid.New()

	sub, err := streams.SubscribeLogs(sessionID)
	require.NoError(t, err, "log subscription should succeed when Redis is available")
	defer sub.Close()

	mr.Close()

	require.Eventually(t, func() bool {
		_, ok := <-sub.C
		return !ok
	}, 3*time.Second, 20*time.Millisecond, "log subscription should close when the Redis reader fails")
	require.Equal(t, breakerStateOpen, client.breaker.State(), "Redis fan-out read failures should open the breaker so reconnects fall back cleanly")
	require.False(t, client.Available(), "availability checks should fail immediately after the fan-out reader opens the breaker")
}

func TestJobNotifier_PublishDeliversToSubscriber(t *testing.T) {
	t.Parallel()

	client, _ := testRedisClient(t)
	notifier := NewJobNotifier(client, zerolog.Nop())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	delivered := make(chan struct{}, 1)
	notifier.Start(ctx, func() {
		select {
		case delivered <- struct{}{}:
		default:
		}
	})

	require.Eventually(t, func() bool {
		if err := notifier.Publish(context.Background()); err != nil {
			return false
		}
		select {
		case <-delivered:
			return true
		default:
			return false
		}
	}, 2*time.Second, 20*time.Millisecond, "publisher should deliver to the live subscriber")
}

func TestSessionStreams_PublishStatusSchedulesExpiryForTerminalSessions(t *testing.T) {
	t.Parallel()

	client, mr := testRedisClient(t)
	streams := NewSessionStreams(client, zerolog.Nop(), nil)
	sessionID := uuid.New()
	now := time.Now()
	issueID := uuid.New()
	session := &models.Session{
		ID:             sessionID,
		OrgID:          uuid.New(),
		PrimaryIssueID: &issueID,
		AgentType:      models.AgentType("codex"),
		Status:         models.SessionStatusCompleted,
		CompletedAt:    &now,
	}

	require.NoError(t, streams.PublishStatus(context.Background(), session), "terminal status publish should succeed")
	ttl := mr.TTL(statusStreamKey(sessionID))
	require.True(t, ttl > 0, "terminal status publish should set stream expiry")
}

func TestSessionStreams_PublishEventAndSubscribeEvents(t *testing.T) {
	t.Parallel()

	client, _ := testRedisClient(t)
	streams := NewSessionStreams(client, zerolog.Nop(), nil)
	sessionID := uuid.New()
	threadID := uuid.New()
	orgID := uuid.New()

	sub, err := streams.SubscribeEvents(sessionID)
	require.NoError(t, err, "event subscription should succeed when Redis is available")
	defer sub.Close()

	event := models.SessionStreamEvent{
		Type:      models.SessionStreamEventThreadInboxQueued,
		SessionID: sessionID,
		OrgID:     orgID,
		Data: models.ThreadInboxEvent{
			SessionID:           sessionID,
			ThreadID:            threadID,
			OrgID:               orgID,
			PendingMessageCount: 2,
		},
	}
	var got models.SessionStreamEvent
	require.Eventually(t, func() bool {
		require.NoError(t, streams.PublishEvent(context.Background(), event), "typed session event should publish to Redis")
		select {
		case got = <-sub.C:
			return true
		default:
			return false
		}
	}, 2*time.Second, 20*time.Millisecond, "event subscription should receive the published event")
	require.Equal(t, event.Type, got.Type, "subscriber should receive the published event type")
	require.Equal(t, sessionID, got.SessionID, "subscriber should receive the session id")
	require.Equal(t, orgID, got.OrgID, "subscriber should receive the org id")
	payload, ok := got.Data.(models.ThreadInboxEvent)
	require.True(t, ok, "subscriber should decode thread inbox payloads")
	require.Equal(t, threadID, payload.ThreadID, "subscriber should receive the thread id")
	require.Equal(t, 2, payload.PendingMessageCount, "subscriber should receive the pending count")
}

func TestParseLogStreamID(t *testing.T) {
	t.Parallel()

	got, err := ParseLogStreamID("12345-0")
	require.NoError(t, err, "valid stream ID should parse")
	require.Equal(t, int64(12345), got, "parser should return the durable log ID prefix")

	got, err = ParseLogStreamID("")
	require.NoError(t, err, "empty stream IDs should map to zero")
	require.Equal(t, int64(0), got, "empty stream IDs should parse as zero")
}

func TestSessionStreams_RangeLogsSince_RedisUnavailable(t *testing.T) {
	t.Parallel()

	streams := NewSessionStreams(nil, zerolog.Nop(), nil)
	_, err := streams.RangeLogsSince(context.Background(), uuid.New(), SessionLogStreamID(1), 10)
	require.Error(t, err, "XRANGE should fail cleanly when Redis is unavailable")
}

func TestSessionStreams_SubscribeStatusCloseRemovesClient(t *testing.T) {
	t.Parallel()

	client, _ := testRedisClient(t)
	streams := NewSessionStreams(client, zerolog.Nop(), nil)
	sessionID := uuid.New()
	sub, err := streams.SubscribeStatus(sessionID)
	require.NoError(t, err, "status subscription should succeed when Redis is available")
	sub.Close()
	require.Equal(t, "client_closed", sub.CloseReason(), "closing a status subscription should record the close reason")
}

func TestSessionStreams_ReplayBufferedLogsAndHelperFunctions(t *testing.T) {
	t.Parallel()

	client, _ := testRedisClient(t)
	streams := NewSessionStreams(client, zerolog.Nop(), nil)
	sessionID := uuid.New()

	fanout := streams.ensureLogFanout(sessionID)
	entry1 := StreamedLog{StreamID: SessionLogStreamID(10), Log: models.SessionLog{ID: 10}}
	entry2 := StreamedLog{StreamID: SessionLogStreamID(11), Log: models.SessionLog{ID: 11}}
	fanout.mu.Lock()
	fanout.ring.add(entry1)
	fanout.ring.add(entry2)
	fanout.mu.Unlock()

	replayed, ok := streams.ReplayBufferedLogs(sessionID, SessionLogStreamID(10))
	require.True(t, ok, "fan-out ring should replay recent buffered entries")
	require.Len(t, replayed, 1, "replay should only include entries newer than the last seen ID")
	require.Equal(t, int64(11), replayed[0].Log.ID, "replay should return the later buffered log")

	require.Equal(t, "143:stream:{ses:"+sessionID.String()+"}:logs", logStreamKey(sessionID), "log stream key should be stable")
	require.Equal(t, "143:stream:{ses:"+sessionID.String()+"}:status", statusStreamKey(sessionID), "status stream key should be stable")
	require.True(t, isTerminalSessionStatus(models.SessionStatusCompleted), "completed sessions should be terminal")
	require.False(t, isTerminalSessionStatus(models.SessionStatusRunning), "running sessions should not be terminal")
	require.Equal(t, []string{"123", "0"}, stringsSplit2("123-0", '-'), "stream ID splitter should split once")

	now := time.Now()
	require.WithinDuration(t, now.Add(sessionStreamExpiryAfter), terminalExpiryAt(&models.Session{CompletedAt: &now}), time.Second, "expiry should use completed time when available")
	require.WithinDuration(t, time.Now().Add(sessionStreamExpiryAfter), terminalExpiryAt(nil), time.Second, "nil sessions should default expiry from now")

	fanout.cancel()
}

func TestLogRingBufferAndSubscriptionHelpers(t *testing.T) {
	t.Parallel()

	emptyRing := newLogRingBuffer(0)
	emptyRing.add(StreamedLog{StreamID: SessionLogStreamID(1), Log: models.SessionLog{ID: 1}})
	require.Nil(t, emptyRing.snapshot(), "zero-sized rings should ignore added entries")
	gotEmpty, ok := emptyRing.since("")
	require.True(t, ok, "empty cursors should succeed even for zero-sized rings")
	require.Nil(t, gotEmpty, "empty cursors should not require buffered entries")

	ring := newLogRingBuffer(2)
	ring.add(StreamedLog{StreamID: SessionLogStreamID(1), Log: models.SessionLog{ID: 1}})
	ring.add(StreamedLog{StreamID: SessionLogStreamID(2), Log: models.SessionLog{ID: 2}})
	ring.add(StreamedLog{StreamID: SessionLogStreamID(3), Log: models.SessionLog{ID: 3}})

	got, ok := ring.since(SessionLogStreamID(2))
	require.True(t, ok, "wrapped rings should replay in-order entries when the cursor is still within the ring")
	require.Len(t, got, 1, "wrapped rings should return entries newer than the cursor")
	require.Equal(t, int64(3), got[0].Log.ID, "wrapped rings should preserve oldest-first ordering")

	_, ok = ring.since(SessionLogStreamID(1))
	require.False(t, ok, "wrapped rings should report a miss when the cursor has fallen behind the ring buffer")

	_, ok = ring.since("bad-id")
	require.False(t, ok, "invalid stream IDs should invalidate ring-buffer replay")

	var nilLogSub *LogSubscription
	var nilStatusSub *StatusSubscription
	require.Equal(t, "", nilLogSub.CloseReason(), "nil log subscriptions should report no close reason")
	require.Equal(t, "", nilStatusSub.CloseReason(), "nil status subscriptions should report no close reason")
	require.NotPanics(t, func() {
		nilLogSub.Close()
		nilStatusSub.Close()
	}, "nil subscriptions should close cleanly")
}

func TestSessionStreams_RunCleanupBatch_ListError(t *testing.T) {
	t.Parallel()

	client, _ := testRedisClient(t)
	streams := NewSessionStreams(client, zerolog.Nop(), nil)

	count, err := streams.runCleanupBatch(context.Background(), cleanupTestLister{err: context.DeadlineExceeded})
	require.Error(t, err, "cleanup batch should surface lister failures")
	require.Equal(t, 0, count, "cleanup batch should report zero work when listing fails")
}

func TestSessionStreams_FanoutRun_StopsOnCanceledContext(t *testing.T) {
	t.Parallel()

	client, _ := testRedisClient(t)

	logCtx, logCancel := context.WithCancel(context.Background())
	logSub := &logSubscriber{ch: make(chan StreamedLog, 1)}
	logSub.reason.Store("")
	logExited := make(chan struct{}, 1)
	logFanout := &logFanout{
		sessionID: uuid.New(),
		streamKey: "logs",
		client:    client,
		logger:    zerolog.Nop(),
		ctx:       logCtx,
		cancel:    logCancel,
		clients:   map[*logSubscriber]struct{}{logSub: {}},
		ring:      newLogRingBuffer(1),
		onExit: func() {
			logExited <- struct{}{}
		},
	}
	logCancel()
	logFanout.run()
	select {
	case <-logExited:
	case <-time.After(time.Second):
		t.Fatal("log fan-out should invoke onExit after context cancellation")
	}
	require.Equal(t, "retry", logSub.reason.Load(), "canceling log fan-out should close subscribers with retry")

	statusCtx, statusCancel := context.WithCancel(context.Background())
	statusSub := &statusSubscriber{ch: make(chan models.Session, 1)}
	statusSub.reason.Store("")
	statusExited := make(chan struct{}, 1)
	statusFanout := &statusFanout{
		sessionID: uuid.New(),
		streamKey: "status",
		client:    client,
		logger:    zerolog.Nop(),
		ctx:       statusCtx,
		cancel:    statusCancel,
		clients:   map[*statusSubscriber]struct{}{statusSub: {}},
		onExit: func() {
			statusExited <- struct{}{}
		},
	}
	statusCancel()
	statusFanout.run()
	select {
	case <-statusExited:
	case <-time.After(time.Second):
		t.Fatal("status fan-out should invoke onExit after context cancellation")
	}
	require.Equal(t, "retry", statusSub.reason.Load(), "canceling status fan-out should close subscribers with retry")
}

func TestSessionStreams_DecodeHelpers_NonStringJSONValue(t *testing.T) {
	t.Parallel()

	logPayload := jsonStringer(`{"id":77,"session_id":"` + uuid.New().String() + `","org_id":"` + uuid.New().String() + `","level":"info","message":"coerce"}`)
	logEntry, err := decodeLogEntry(redis.XMessage{Values: map[string]any{"json": logPayload}})
	require.NoError(t, err, "decoder should coerce non-string JSON payloads")
	require.Equal(t, int64(77), logEntry.ID, "decoder should unmarshal coerced log payloads")

	sessionID := uuid.New()
	orgID := uuid.New()
	issueID := uuid.New()
	statusPayload := jsonStringer(`{"id":"` + sessionID.String() + `","org_id":"` + orgID.String() + `","primary_issue_id":"` + issueID.String() + `","agent_type":"codex","status":"running"}`)
	session, err := decodeStatusEntry(redis.XMessage{Values: map[string]any{"json": statusPayload}})
	require.NoError(t, err, "decoder should coerce non-string status payloads")
	require.Equal(t, sessionID, session.ID, "decoder should unmarshal coerced status payloads")
}

func TestSessionStreams_NilAndDecodeHelpers(t *testing.T) {
	t.Parallel()

	var streams *SessionStreams
	require.False(t, streams.Available(), "nil stream helpers should report unavailable")
	require.Nil(t, NewSessionStreams(nil, zerolog.Nop(), nil), "constructor should return nil when Redis is disabled")
	require.Equal(t, "foo", maxLenStreamKey("foo", 10), "maxLen helper should currently return the original stream key")

	require.NoError(t, streams.PublishLog(context.Background(), nil), "nil stream helper should ignore nil log publishes")
	require.NoError(t, streams.PublishStatus(context.Background(), nil), "nil stream helper should ignore nil status publishes")
	require.NoError(t, streams.PublishEvent(context.Background(), models.SessionStreamEvent{}), "nil stream helper should ignore event publishes")
	require.NoError(t, streams.ScheduleExpiry(context.Background(), uuid.New(), time.Now()), "nil stream helper should ignore expiry scheduling")
	require.NoError(t, streams.DeleteSessionStreams(context.Background(), uuid.New()), "nil stream helper should ignore stream deletion")
}

type cleanupTestLister struct {
	sessions []models.Session
	err      error
}

func (l cleanupTestLister) ListTerminalEndedBefore(context.Context, time.Time, int) ([]models.Session, error) {
	return l.sessions, l.err
}

func TestSessionStreams_RunCleanupBatch(t *testing.T) {
	t.Parallel()

	client, mr := testRedisClient(t)
	streams := NewSessionStreams(client, zerolog.Nop(), nil)
	sessionID := uuid.New()

	_, err := mr.XAdd(logStreamKey(sessionID), "1-0", []string{"json", `{"id":1}`})
	require.NoError(t, err, "test should seed the log stream")
	_, err = mr.XAdd(statusStreamKey(sessionID), "1-0", []string{"json", `{"id":"` + sessionID.String() + `"}`})
	require.NoError(t, err, "test should seed the status stream")
	_, err = mr.XAdd(eventStreamKey(sessionID), "1-0", []string{"json", `{"type":"thread.inbox.queued","session_id":"` + sessionID.String() + `","org_id":"` + uuid.New().String() + `","data":{}}`})
	require.NoError(t, err, "test should seed the event stream")

	count, err := streams.runCleanupBatch(context.Background(), cleanupTestLister{
		sessions: []models.Session{{ID: sessionID}},
	})
	require.NoError(t, err, "cleanup batch should succeed")
	require.Equal(t, 1, count, "cleanup batch should report the deleted session stream count")
	require.False(t, mr.Exists(logStreamKey(sessionID)), "cleanup should delete the log stream")
	require.False(t, mr.Exists(statusStreamKey(sessionID)), "cleanup should delete the status stream")
	require.False(t, mr.Exists(eventStreamKey(sessionID)), "cleanup should delete the event stream")
}

func TestSessionStreams_StartCleanup_StopsOnCanceledContext(t *testing.T) {
	t.Parallel()

	client, _ := testRedisClient(t)
	streams := NewSessionStreams(client, zerolog.Nop(), nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	require.NotPanics(t, func() {
		streams.StartCleanup(ctx, cleanupTestLister{})
	}, "cleanup startup should tolerate already-canceled contexts")
	require.NotPanics(t, func() {
		streams.StartCleanup(context.Background(), nil)
	}, "cleanup startup should ignore nil listers")
}

func TestSessionStreams_DecodeStatusEntryAndInvalidStreamID(t *testing.T) {
	t.Parallel()

	issueID := uuid.New()
	want := models.Session{ID: uuid.New(), OrgID: uuid.New(), PrimaryIssueID: &issueID, Status: models.SessionStatusRunning}
	payload, err := json.Marshal(want)
	require.NoError(t, err, "test payload should marshal")

	got, err := decodeStatusEntry(redis.XMessage{Values: map[string]any{"json": string(payload)}})
	require.NoError(t, err, "decoder should unmarshal the JSON payload")
	require.Equal(t, want.ID, got.ID, "decoder should hydrate the session ID")

	_, err = ParseLogStreamID("bad")
	require.Error(t, err, "invalid stream IDs should return an error")
}

func TestSessionStreams_DecodeLogEntry(t *testing.T) {
	t.Parallel()

	want := models.SessionLog{ID: 42, SessionID: uuid.New(), OrgID: uuid.New(), Level: "info", Message: "hello", Timestamp: time.Now()}
	payload, err := json.Marshal(want)
	require.NoError(t, err, "test payload should marshal")

	got, err := decodeLogEntry(redis.XMessage{ID: SessionLogStreamID(want.ID), Values: map[string]any{"json": string(payload)}})
	require.NoError(t, err, "decoder should unmarshal the JSON payload")
	require.Equal(t, want.ID, got.ID, "decoder should hydrate the log ID")
}

func TestSessionStreams_DecodeHelpers_MissingJSON(t *testing.T) {
	t.Parallel()

	_, err := decodeLogEntry(redis.XMessage{Values: map[string]any{}})
	require.Error(t, err, "log decoder should reject missing payloads")

	_, err = decodeStatusEntry(redis.XMessage{Values: map[string]any{}})
	require.Error(t, err, "status decoder should reject missing payloads")
}

func TestSessionStreams_FanoutCloseHelpers(t *testing.T) {
	t.Parallel()

	logSub := &logSubscriber{ch: make(chan StreamedLog, 1)}
	logSub.reason.Store("")
	logFanout := &logFanout{clients: map[*logSubscriber]struct{}{logSub: {}}}
	logFanout.closeAll("retry")
	require.Equal(t, "retry", logSub.reason.Load(), "closeAll should store the close reason on log subscribers")

	statusSub := &statusSubscriber{ch: make(chan models.Session, 1)}
	statusSub.reason.Store("")
	statusFanout := &statusFanout{clients: map[*statusSubscriber]struct{}{statusSub: {}}, cancel: func() {}}
	statusFanout.removeClient(statusSub, "client_closed")
	require.Equal(t, "client_closed", statusSub.reason.Load(), "removeClient should store the close reason on status subscribers")

	eventSub := &eventSubscriber{ch: make(chan models.SessionStreamEvent, 1)}
	eventSub.reason.Store("")
	eventFanout := &eventFanout{clients: map[*eventSubscriber]struct{}{eventSub: {}}, cancel: func() {}}
	eventFanout.removeClient(eventSub, "client_closed")
	require.Equal(t, "client_closed", eventSub.reason.Load(), "removeClient should store the close reason on event subscribers")
}

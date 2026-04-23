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

func testRedisClient(t *testing.T) (*Client, *miniredis.Miniredis) {
	t.Helper()

	mr := miniredis.RunT(t)
	metrics, err := NewMetrics()
	require.NoError(t, err, "Redis metrics should initialize")
	client := New(Config{Topology: "standalone", URL: "redis://" + mr.Addr()}, zerolog.Nop(), metrics)
	require.NotNil(t, client, "Redis client should initialize against miniredis")
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
	session := &models.Session{
		ID:          sessionID,
		OrgID:       uuid.New(),
		IssueID:     uuid.New(),
		AgentType:   models.AgentType("codex"),
		Status:      string(models.SessionStatusCompleted),
		CompletedAt: &now,
	}

	require.NoError(t, streams.PublishStatus(context.Background(), session), "terminal status publish should succeed")
	ttl := mr.TTL(statusStreamKey(sessionID))
	require.True(t, ttl > 0, "terminal status publish should set stream expiry")
}

func TestParseLogStreamID(t *testing.T) {
	t.Parallel()

	got, err := ParseLogStreamID("12345-0")
	require.NoError(t, err, "valid stream ID should parse")
	require.Equal(t, int64(12345), got, "parser should return the durable log ID prefix")
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
	require.True(t, isTerminalSessionStatus(string(models.SessionStatusCompleted)), "completed sessions should be terminal")
	require.False(t, isTerminalSessionStatus(string(models.SessionStatusRunning)), "running sessions should not be terminal")
	require.Equal(t, []string{"123", "0"}, stringsSplit2("123-0", '-'), "stream ID splitter should split once")

	now := time.Now()
	require.WithinDuration(t, now.Add(sessionStreamExpiryAfter), terminalExpiryAt(&models.Session{CompletedAt: &now}), time.Second, "expiry should use completed time when available")
	require.WithinDuration(t, time.Now().Add(sessionStreamExpiryAfter), terminalExpiryAt(nil), time.Second, "nil sessions should default expiry from now")

	fanout.cancel()
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

	count, err := streams.runCleanupBatch(context.Background(), cleanupTestLister{
		sessions: []models.Session{{ID: sessionID}},
	})
	require.NoError(t, err, "cleanup batch should succeed")
	require.Equal(t, 1, count, "cleanup batch should report the deleted session stream count")
	require.False(t, mr.Exists(logStreamKey(sessionID)), "cleanup should delete the log stream")
	require.False(t, mr.Exists(statusStreamKey(sessionID)), "cleanup should delete the status stream")
}

func TestSessionStreams_DecodeStatusEntryAndInvalidStreamID(t *testing.T) {
	t.Parallel()

	want := models.Session{ID: uuid.New(), OrgID: uuid.New(), IssueID: uuid.New(), Status: string(models.SessionStatusRunning)}
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

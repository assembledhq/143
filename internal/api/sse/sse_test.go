package sse

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNewWriter_SupportsFlusher(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	sw := NewWriter(rec)
	require.NotNil(t, sw, "NewWriter should return a non-nil writer for httptest.ResponseRecorder")
	require.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"), "SSE should use the event-stream content type")
	require.Equal(t, "no-cache, no-transform", rec.Header().Get("Cache-Control"), "SSE should prevent caching and intermediary transformation")
	require.Equal(t, "keep-alive", rec.Header().Get("Connection"), "SSE should keep the transport connection alive")
}

// nonFlushWriter is an http.ResponseWriter that does NOT implement http.Flusher.
type nonFlushWriter struct{ http.ResponseWriter }

type failingWriteWriter struct{}

func (failingWriteWriter) Header() http.Header        { return make(http.Header) }
func (failingWriteWriter) WriteHeader(statusCode int) {}
func (failingWriteWriter) Flush()                     {}
func (failingWriteWriter) Write([]byte) (int, error)  { return 0, errors.New("write failed") }

type deadlineWriter struct {
	*httptest.ResponseRecorder
	deadlines []time.Time
}

type failingDeadlineWriter struct{ *httptest.ResponseRecorder }

func (w *failingDeadlineWriter) SetWriteDeadline(time.Time) error {
	return errors.New("deadline setup failed")
}

func (w *deadlineWriter) SetWriteDeadline(deadline time.Time) error {
	w.deadlines = append(w.deadlines, deadline)
	return nil
}

func TestNewWriter_NoFlusher(t *testing.T) {
	t.Parallel()

	sw := NewWriter(nonFlushWriter{httptest.NewRecorder()})
	require.Nil(t, sw, "NewWriter should return nil when ResponseWriter does not support Flusher")
}

func TestWriteEvent_DefaultEvent(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	sw := NewWriter(rec)
	require.NotNil(t, sw)

	err := sw.WriteEvent(EventLog, map[string]string{"msg": "hello"})
	require.NoError(t, err)

	body := rec.Body.String()
	require.NotContains(t, body, "event:")
	require.Contains(t, body, `data: {"msg":"hello"}`)
}

func TestWriteEvent_NamedEvent(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	sw := NewWriter(rec)
	require.NotNil(t, sw)

	err := sw.WriteEvent(EventStatus, map[string]string{"status": "running"})
	require.NoError(t, err)

	body := rec.Body.String()
	require.Contains(t, body, "event: status\n")
	require.Contains(t, body, `data: {"status":"running"}`)
}

func TestWriteData(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	sw := NewWriter(rec)
	require.NotNil(t, sw)

	err := sw.WriteData(map[string]int{"count": 1})
	require.NoError(t, err)
	require.Contains(t, rec.Body.String(), `data: {"count":1}`)
}

func TestWriteEventIDAndWriteDataID(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	sw := NewWriter(rec)
	require.NotNil(t, sw, "writer should initialize for response recorders")

	require.NoError(t, sw.WriteEventID(EventStatus, "evt-1", map[string]string{"status": "running"}), "named event with ID should write successfully")
	require.NoError(t, sw.WriteDataID("evt-2", map[string]int{"count": 2}), "default event with ID should write successfully")

	body := rec.Body.String()
	require.Contains(t, body, "id: evt-1\n", "named events should include their event ID")
	require.Contains(t, body, "event: status\n", "named events should include the event name")
	require.Contains(t, body, "id: evt-2\n", "default events should include their event ID")
}

func TestWriteEventID_WriteError(t *testing.T) {
	t.Parallel()

	sw := NewWriter(failingWriteWriter{})
	require.NotNil(t, sw, "writer should initialize for custom flusher response writers")

	err := sw.WriteEventID(EventStatus, "evt-1", map[string]string{"status": "running"})
	require.Error(t, err, "write failures should be surfaced")
	require.Contains(t, err.Error(), "event id", "error should identify the failing SSE write phase")
}

func TestWriteEvent_MarshalError(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	sw := NewWriter(rec)
	require.NotNil(t, sw)

	// channels cannot be marshaled to JSON
	err := sw.WriteEvent(EventLog, make(chan int))
	require.Error(t, err)
	require.Contains(t, err.Error(), "sse: marshal")
}

func TestFlush(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	sw := NewWriter(rec)
	require.NotNil(t, sw)

	// Should not panic
	sw.Flush()
}

func TestWriteHeartbeat(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	sw := NewWriter(rec)
	require.NotNil(t, sw)

	err := sw.WriteHeartbeat()
	require.NoError(t, err)
	// SSE comment lines start with a colon; browsers ignore them.
	require.Equal(t, ": ping\n\n", rec.Body.String())
}

func TestWriterBoundsEveryFrameWithAWriteDeadline(t *testing.T) {
	t.Parallel()
	w := &deadlineWriter{ResponseRecorder: httptest.NewRecorder()}
	sw := NewWriter(w)
	require.NotNil(t, sw, "deadline-capable writer should initialize")
	require.NoError(t, sw.WriteEvent(EventStatus, map[string]string{"status": "running"}), "event frame should write")
	require.NoError(t, sw.Flush(), "event frame should flush")
	require.GreaterOrEqual(t, len(w.deadlines), 3, "initial deadline clear, frame write, and flush should all set deadlines")
	require.True(t, w.deadlines[len(w.deadlines)-1].After(time.Now()), "flush deadline should bound a stalled consumer")
}

func TestWriterSurfacesInitialDeadlineFailure(t *testing.T) {
	t.Parallel()
	w := &failingDeadlineWriter{ResponseRecorder: httptest.NewRecorder()}
	sw := NewWriter(w)
	require.NotNil(t, sw, "deadline failure should remain observable through the writer")
	err := sw.WriteEvent(EventStatus, map[string]string{"status": "running"})
	require.Error(t, err, "initial deadline failure should prevent an apparently healthy frame")
	require.Contains(t, err.Error(), "clear connection write deadline", "error should identify connection deadline setup")
}

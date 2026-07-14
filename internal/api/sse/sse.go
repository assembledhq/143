// Package sse provides typed Server-Sent Events helpers.
package sse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"go.opentelemetry.io/otel"
	otelmetric "go.opentelemetry.io/otel/metric"
)

var (
	sseMeter            = otel.Meter("github.com/assembledhq/143/sse")
	sseFrameLatency, _  = sseMeter.Float64Histogram("live_events.sse_frame_write_ms", otelmetric.WithUnit("ms"))
	sseWriteFailures, _ = sseMeter.Int64Counter("live_events.sse_write_failures")
)

// EventType is the named event type sent over an SSE stream.
type EventType string

const (
	// EventLog is the default (unnamed) event carrying a session log entry.
	EventLog EventType = ""
	// EventStatus is sent when the session status changes.
	EventStatus EventType = "status"
	// EventDone is sent when the session reaches a terminal status.
	EventDone EventType = "done"
	// EventHumanInputCreated is sent when an agent creates a durable human-input request.
	EventHumanInputCreated EventType = "session_human_input.created"
	// EventHumanInputUpdated is sent when a durable human-input request is answered or cancelled.
	EventHumanInputUpdated EventType = "session_human_input.updated"
	// EventThreadInboxQueued is sent when a thread has queued inbox input waiting for runtime delivery.
	EventThreadInboxQueued EventType = "thread.inbox.queued"
	// EventThreadInboxCleared is sent when a thread drains queued inbox input.
	EventThreadInboxCleared EventType = "thread.inbox.cleared"
	// EventThreadRuntimeUpdated is sent when a thread runtime-visible state changes.
	EventThreadRuntimeUpdated EventType = "thread.runtime.updated"
	// EventSessionWorkspaceGenerationChanged is sent when a session workspace generation advances.
	EventSessionWorkspaceGenerationChanged EventType = "session.workspace.generation_changed"
)

// Writer wraps an http.ResponseWriter that supports SSE streaming.
type Writer struct {
	w             http.ResponseWriter
	flusher       http.Flusher
	frameDeadline time.Duration
	setupErr      error
}

// NewWriter creates an SSE writer after setting the required headers.
// Returns nil if the ResponseWriter does not support flushing.
func NewWriter(w http.ResponseWriter) *Writer {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("Connection", "keep-alive")

	// Clear the per-connection write deadline so Server.WriteTimeout does not
	// kill long-lived SSE streams mid-response. Without this, HTTP/2 clients
	// see the terminated stream as ERR_HTTP2_PROTOCOL_ERROR.
	setupErr := http.NewResponseController(w).SetWriteDeadline(time.Time{})
	if errors.Is(setupErr, http.ErrNotSupported) {
		setupErr = nil
	}

	return &Writer{w: w, flusher: flusher, frameDeadline: 5 * time.Second, setupErr: setupErr}
}

func (sw *Writer) beginFrame() error {
	if sw.setupErr != nil {
		return fmt.Errorf("sse: clear connection write deadline: %w", sw.setupErr)
	}
	if err := http.NewResponseController(sw.w).SetWriteDeadline(time.Now().Add(sw.frameDeadline)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return fmt.Errorf("sse: set frame write deadline: %w", err)
	}
	return nil
}

// WriteHeartbeat writes an SSE comment line that browsers silently ignore but
// that keeps the underlying TCP/HTTP2 connection active through idle-timeout
// proxies.
func (sw *Writer) WriteHeartbeat() error {
	if err := sw.beginFrame(); err != nil {
		return err
	}
	if _, err := fmt.Fprint(sw.w, ": ping\n\n"); err != nil {
		return fmt.Errorf("sse: write heartbeat: %w", err)
	}
	return nil
}

// WriteEvent marshals data as JSON and writes a named SSE event.
// For EventLog (the default event type), the event field is omitted.
func (sw *Writer) WriteEvent(eventType EventType, data any) error {
	return sw.WriteEventID(eventType, "", data)
}

// WriteEventID marshals data as JSON and writes a named SSE event with an optional event ID.
func (sw *Writer) WriteEventID(eventType EventType, id string, data any) error {
	startedAt := time.Now()
	defer func() {
		sseFrameLatency.Record(context.Background(), float64(time.Since(startedAt).Microseconds())/1000)
	}()
	if err := sw.beginFrame(); err != nil {
		return err
	}
	b, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("sse: marshal %s event: %w", eventType, err)
	}

	if id != "" {
		if _, err := fmt.Fprintf(sw.w, "id: %s\n", id); err != nil {
			sseWriteFailures.Add(context.Background(), 1)
			return fmt.Errorf("sse: write %s event id: %w", eventType, err)
		}
	}
	if eventType != EventLog {
		if _, err := fmt.Fprintf(sw.w, "event: %s\n", string(eventType)); err != nil {
			sseWriteFailures.Add(context.Background(), 1)
			return fmt.Errorf("sse: write %s event header: %w", eventType, err)
		}
	}
	if _, err := fmt.Fprintf(sw.w, "data: %s\n\n", b); err != nil {
		sseWriteFailures.Add(context.Background(), 1)
		return fmt.Errorf("sse: write %s event data: %w", eventType, err)
	}
	return nil
}

// WriteData is a convenience for writing the default (unnamed) event.
func (sw *Writer) WriteData(data any) error {
	return sw.WriteEvent(EventLog, data)
}

func (sw *Writer) WriteDataID(id string, data any) error {
	return sw.WriteEventID(EventLog, id, data)
}

// Flush sends any buffered data to the client.
func (sw *Writer) Flush() error {
	startedAt := time.Now()
	defer func() {
		sseFrameLatency.Record(context.Background(), float64(time.Since(startedAt).Microseconds())/1000)
	}()
	if err := sw.beginFrame(); err != nil {
		return err
	}
	if err := http.NewResponseController(sw.w).Flush(); err != nil {
		sseWriteFailures.Add(context.Background(), 1)
		return fmt.Errorf("sse: flush: %w", err)
	}
	return nil
}

// Package sse provides typed Server-Sent Events helpers.
package sse

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
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
	w       http.ResponseWriter
	flusher http.Flusher
}

// NewWriter creates an SSE writer after setting the required headers.
// Returns nil if the ResponseWriter does not support flushing.
func NewWriter(w http.ResponseWriter) *Writer {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Clear the per-connection write deadline so Server.WriteTimeout does not
	// kill long-lived SSE streams mid-response. Without this, HTTP/2 clients
	// see the terminated stream as ERR_HTTP2_PROTOCOL_ERROR.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})

	return &Writer{w: w, flusher: flusher}
}

// WriteHeartbeat writes an SSE comment line that browsers silently ignore but
// that keeps the underlying TCP/HTTP2 connection active through idle-timeout
// proxies.
func (sw *Writer) WriteHeartbeat() error {
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
	b, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("sse: marshal %s event: %w", eventType, err)
	}

	if id != "" {
		if _, err := fmt.Fprintf(sw.w, "id: %s\n", id); err != nil {
			return fmt.Errorf("sse: write %s event id: %w", eventType, err)
		}
	}
	if eventType != EventLog {
		if _, err := fmt.Fprintf(sw.w, "event: %s\n", string(eventType)); err != nil {
			return fmt.Errorf("sse: write %s event header: %w", eventType, err)
		}
	}
	if _, err := fmt.Fprintf(sw.w, "data: %s\n\n", b); err != nil {
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
func (sw *Writer) Flush() {
	sw.flusher.Flush()
}

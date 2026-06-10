package models

import "encoding/json"

// ListResponse is the standard envelope for list endpoints.
type ListResponse[T any] struct {
	Data []T            `json:"data"`
	Meta PaginationMeta `json:"meta"`
}

func (r ListResponse[T]) MarshalJSON() ([]byte, error) {
	type listResponse ListResponse[T]
	if r.Data == nil {
		r.Data = []T{}
	}
	return json.Marshal(listResponse(r))
}

// SingleResponse is the standard envelope for single-item endpoints.
type SingleResponse[T any] struct {
	Data T `json:"data"`
}

// ErrorResponse is the standard envelope for error responses.
type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail contains error information.
type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
}

// PaginationMeta contains cursor-based pagination info.
type PaginationMeta struct {
	NextCursor string `json:"next_cursor,omitempty"`
}

// ThreadMessageWindowMeta contains cursor and anchor metadata for bottom-first
// thread transcript loading.
type ThreadMessageWindowMeta struct {
	NextOlderCursor          string `json:"next_older_cursor,omitempty"`
	HasOlder                 bool   `json:"has_older"`
	NextNewerCursor          string `json:"next_newer_cursor,omitempty"`
	HasNewer                 bool   `json:"has_newer"`
	AnchorMessageID          int64  `json:"anchor_message_id,omitempty"`
	AnchorFound              bool   `json:"anchor_found"`
	LatestAssistantMessageID int64  `json:"latest_assistant_message_id,omitempty"`
	LiveEdgeMessageID        int64  `json:"live_edge_message_id,omitempty"`
	WindowPosition           string `json:"window_position"`
	ThreadStatus             string `json:"thread_status"`
}

// ThreadMessageWindowResponse is the envelope for a cursor-loaded thread
// message window.
type ThreadMessageWindowResponse struct {
	Data []SessionMessage        `json:"data"`
	Meta ThreadMessageWindowMeta `json:"meta"`
}

func (r ThreadMessageWindowResponse) MarshalJSON() ([]byte, error) {
	type response ThreadMessageWindowResponse
	if r.Data == nil {
		r.Data = []SessionMessage{}
	}
	return json.Marshal(response(r))
}

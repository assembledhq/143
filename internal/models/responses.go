package models

// ListResponse is the standard envelope for list endpoints.
type ListResponse[T any] struct {
	Data []T            `json:"data"`
	Meta PaginationMeta `json:"meta"`
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

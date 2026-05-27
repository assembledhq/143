package models

// RetrySessionRequest is the optional body for POST /api/v1/sessions/{id}/retry.
// An omitted mode defaults to SessionRetryModeCheckpoint at the handler layer.
type RetrySessionRequest struct {
	Mode SessionRetryMode `json:"mode,omitempty"`
}

package models

import "fmt"

type JobStatus string

const (
	JobStatusPending              JobStatus = "pending"
	JobStatusRunning              JobStatus = "running"
	JobStatusSucceeded            JobStatus = "succeeded"
	JobStatusFailed               JobStatus = "failed"
	JobStatusCancelled            JobStatus = "cancelled"
	JobStatusDeadLetter           JobStatus = "dead_letter"
	JobStatusAwaitingStartControl JobStatus = "awaiting_start_control"
)

func (s JobStatus) Validate() error {
	switch s {
	case JobStatusPending, JobStatusRunning, JobStatusSucceeded, JobStatusFailed, JobStatusCancelled, JobStatusDeadLetter, JobStatusAwaitingStartControl:
		return nil
	default:
		return fmt.Errorf("invalid JobStatus: %q", s)
	}
}

// IsRunnable classifies supported runnable statuses only. Validate persisted values
// before interpretation; an unknown value is not evidence of settled ownership.
func (s JobStatus) IsRunnable() bool { return s == JobStatusPending }

func (s JobStatus) IsLeaseOwned() bool { return s == JobStatusRunning }

func (s JobStatus) IsNonterminalInputConsumer() bool {
	return s == JobStatusPending || s == JobStatusRunning || s == JobStatusAwaitingStartControl
}

func (s JobStatus) IsTerminal() bool {
	return s == JobStatusSucceeded || s == JobStatusFailed || s == JobStatusCancelled || s == JobStatusDeadLetter
}

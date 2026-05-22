package models

import "fmt"

type JobStatus string

const (
	JobStatusPending    JobStatus = "pending"
	JobStatusRunning    JobStatus = "running"
	JobStatusSucceeded  JobStatus = "succeeded"
	JobStatusFailed     JobStatus = "failed"
	JobStatusCancelled  JobStatus = "cancelled"
	JobStatusDeadLetter JobStatus = "dead_letter"
)

func (s JobStatus) Validate() error {
	switch s {
	case JobStatusPending, JobStatusRunning, JobStatusSucceeded, JobStatusFailed, JobStatusCancelled, JobStatusDeadLetter:
		return nil
	default:
		return fmt.Errorf("invalid JobStatus: %q", s)
	}
}

package models

import "fmt"

// SandboxWorkloadClass identifies the capacity policy applied to a sandbox-
// producing job. It is persisted on every job so routing and worker-local
// admission make the same decision without inferring intent from payload JSON.
type SandboxWorkloadClass string

const (
	SandboxWorkloadClassInteractive SandboxWorkloadClass = "interactive"
	SandboxWorkloadClassCodeReview  SandboxWorkloadClass = "code_review"
)

func (c SandboxWorkloadClass) Validate() error {
	switch c {
	case SandboxWorkloadClassInteractive, SandboxWorkloadClassCodeReview:
		return nil
	default:
		return fmt.Errorf("invalid SandboxWorkloadClass: %q", c)
	}
}

// SandboxWorkloadClassForSession maps a session to its capacity class. Code
// review sessions are the only background sandbox workload today; all other
// origins retain interactive admission semantics.
func SandboxWorkloadClassForSession(session *Session) SandboxWorkloadClass {
	if session != nil && session.Origin == SessionOriginCodeReview {
		return SandboxWorkloadClassCodeReview
	}
	return SandboxWorkloadClassInteractive
}

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

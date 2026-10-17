package models

import "fmt"

// SessionExecutionPurpose distinguishes new input from explicitly requested
// task recovery. It never authorizes replay of unknown inbox delivery.
type SessionExecutionPurpose string

const (
	SessionExecutionPurposeContinue        SessionExecutionPurpose = "continue"
	SessionExecutionPurposeCheckpointRetry SessionExecutionPurpose = "checkpoint_retry"
	// CheckpointRecovery includes job attempts that ended before their first checkpoint.
	SessionExecutionPurposeCheckpointRecovery SessionExecutionPurpose = "checkpoint_recovery"
)

func (p SessionExecutionPurpose) Validate() error {
	switch p {
	case SessionExecutionPurposeContinue, SessionExecutionPurposeCheckpointRetry, SessionExecutionPurposeCheckpointRecovery:
		return nil
	default:
		return fmt.Errorf("invalid SessionExecutionPurpose: %q", p)
	}
}

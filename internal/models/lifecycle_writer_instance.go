package models

import "fmt"

// LifecycleWriterRole identifies the participating process, not a node or an
// executor allocation. This registry is a partial observation, not a writer floor.
type LifecycleWriterRole string

const LifecycleWriterRoleSessionExecutor LifecycleWriterRole = "session_executor"

func (r LifecycleWriterRole) Validate() error {
	switch r {
	case LifecycleWriterRoleSessionExecutor:
		return nil
	default:
		return fmt.Errorf("invalid LifecycleWriterRole: %q", r)
	}
}

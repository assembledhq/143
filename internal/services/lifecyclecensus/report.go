// Package lifecyclecensus collects a bounded, passive observation. It is not a
// rollout gate, a complete writer inventory, or proof that work has drained.
package lifecyclecensus

import (
	"fmt"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

// ObservedRole is report vocabulary, not registry eligibility or authorization.
// Operator is recognized for connection labels only; writer leases retain the
// four-role LifecycleWriterRole allowlist.
type ObservedRole string

const (
	RoleUnknown         ObservedRole = "unknown"
	RoleAPI             ObservedRole = ObservedRole(models.LifecycleWriterRoleAPI)
	RoleWorker          ObservedRole = ObservedRole(models.LifecycleWriterRoleWorker)
	RoleAll             ObservedRole = ObservedRole(models.LifecycleWriterRoleAll)
	RoleSessionExecutor ObservedRole = ObservedRole(models.LifecycleWriterRoleSessionExecutor)
	RoleOperator        ObservedRole = db.LifecycleOperatorConnectionRole
)

func (r ObservedRole) Validate() error {
	if r == RoleUnknown || r == RoleOperator {
		return nil
	}
	return models.LifecycleWriterRole(r).Validate()
}

type Visibility string

const (
	VisibilityFull       Visibility = "full"
	VisibilityRestricted Visibility = "restricted"
	VisibilityUnknown    Visibility = "unknown"
)

func (v Visibility) Validate() error {
	switch v {
	case VisibilityFull, VisibilityRestricted, VisibilityUnknown:
		return nil
	default:
		return fmt.Errorf("invalid census visibility")
	}
}

type CountsScope string

const (
	CountsObserved CountsScope = "observed_rows"
	CountsSample   CountsScope = "retained_sample"
)

func (s CountsScope) Validate() error {
	if s != CountsObserved && s != CountsSample {
		return fmt.Errorf("invalid census counts scope")
	}
	return nil
}

type ActivityState string

const (
	StateUnknown                  ActivityState = "unknown"
	StateDisabled                 ActivityState = "disabled"
	StateActive                   ActivityState = "active"
	StateIdle                     ActivityState = "idle"
	StateIdleInTransaction        ActivityState = "idle in transaction"
	StateIdleInAbortedTransaction ActivityState = "idle in transaction (aborted)"
	StateFastpath                 ActivityState = "fastpath function call"
)

func (s ActivityState) Validate() error {
	switch s {
	case StateUnknown, StateDisabled, StateActive, StateIdle, StateIdleInTransaction, StateIdleInAbortedTransaction, StateFastpath:
		return nil
	default:
		return fmt.Errorf("invalid census activity state")
	}
}

// Report contains only allowlisted aggregates, never connection identifiers or
// arbitrary database strings. Observation times belong to separate statements.
type Report struct {
	SchemaVersion int               `json:"schema_version"`
	Limitations   []string          `json:"limitations"`
	Writers       WriterSummary     `json:"writers"`
	Connections   ConnectionSummary `json:"connections"`
}

type WriterSummary struct {
	ObservedAt   time.Time     `json:"observed_at"`
	Truncated    bool          `json:"truncated"`
	CountsScope  CountsScope   `json:"counts_scope"`
	RowsObserved int           `json:"rows_observed"`
	Groups       []WriterGroup `json:"groups"`
}

type WriterGroup struct {
	Role    ObservedRole `json:"role"`
	Build   string       `json:"build"`
	Version int          `json:"writer_version"`
	Count   int          `json:"count"`
}

type ConnectionSummary struct {
	ObservedAt   time.Time         `json:"observed_at"`
	Visibility   Visibility        `json:"visibility"`
	Truncated    bool              `json:"truncated"`
	CountsScope  CountsScope       `json:"counts_scope"`
	RowsObserved int               `json:"rows_observed"`
	Groups       []ConnectionGroup `json:"groups"`
}

type ConnectionGroup struct {
	Role                        ObservedRole  `json:"role"`
	Build                       string        `json:"build"`
	State                       ActivityState `json:"state"`
	Count                       int           `json:"count"`
	UnknownBackendType          int           `json:"unknown_backend_type"`
	ObservedOpenTransactions    int           `json:"observed_open_transactions"`
	UnknownTransactionDetails   int           `json:"unknown_transaction_details"`
	OldestObservedTransactionAt *time.Time    `json:"oldest_observed_transaction_at"`
}

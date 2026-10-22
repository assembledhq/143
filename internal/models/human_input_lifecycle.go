package models

import "fmt"

// HumanInputReadinessState names a stored readiness classification. A stored
// classification alone does not establish eligibility or mutation authority.
type HumanInputReadinessState string

const (
	HumanInputReadinessStateUnclassified     HumanInputReadinessState = "unclassified"
	HumanInputReadinessStateReady            HumanInputReadinessState = "ready"
	HumanInputReadinessStateWaiting          HumanInputReadinessState = "waiting"
	HumanInputReadinessStateRecoveryRequired HumanInputReadinessState = "recovery_required"
)

func (s HumanInputReadinessState) Validate() error {
	switch s {
	case HumanInputReadinessStateUnclassified,
		HumanInputReadinessStateReady,
		HumanInputReadinessStateWaiting,
		HumanInputReadinessStateRecoveryRequired:
		return nil
	default:
		return fmt.Errorf("invalid HumanInputReadinessState: %q", s)
	}
}

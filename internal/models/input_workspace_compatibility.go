package models

import "fmt"

// InputWorkspaceCompatibilityState names stored compatibility metadata. A raw
// label is not a certificate, workspace authorization or admission eligibility.
type InputWorkspaceCompatibilityState string

const (
	InputWorkspaceCompatibilityStateCertified   InputWorkspaceCompatibilityState = "certified"
	InputWorkspaceCompatibilityStatePending     InputWorkspaceCompatibilityState = "pending"
	InputWorkspaceCompatibilityStateUnavailable InputWorkspaceCompatibilityState = "unavailable"
	InputWorkspaceCompatibilityStateUnprovable  InputWorkspaceCompatibilityState = "unprovable"
)

func (s InputWorkspaceCompatibilityState) Validate() error {
	switch s {
	case InputWorkspaceCompatibilityStateCertified,
		InputWorkspaceCompatibilityStatePending,
		InputWorkspaceCompatibilityStateUnavailable,
		InputWorkspaceCompatibilityStateUnprovable:
		return nil
	default:
		return fmt.Errorf("invalid InputWorkspaceCompatibilityState: %q", s)
	}
}

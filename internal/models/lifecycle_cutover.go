package models

import "fmt"

// LifecycleCutoverState names stored cutover metadata. The stored value alone
// does not establish a compatible writer floor, activation or admission authority.
type LifecycleCutoverState string

const (
	LifecycleCutoverStateShadow      LifecycleCutoverState = "shadow"
	LifecycleCutoverStateNormalizing LifecycleCutoverState = "normalizing"
	LifecycleCutoverStateActive      LifecycleCutoverState = "active"
)

func (s LifecycleCutoverState) Validate() error {
	switch s {
	case LifecycleCutoverStateShadow,
		LifecycleCutoverStateNormalizing,
		LifecycleCutoverStateActive:
		return nil
	default:
		return fmt.Errorf("invalid LifecycleCutoverState: %q", s)
	}
}

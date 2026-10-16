package ratelimit

import "context"

// BeforeExternal applies a known cooldown to a request whose response the host
// cannot authenticate. It never issues a recovery permit: sandbox observations
// must not gain authority to open, extend, or clear shared installation state.
// Once the retained cooldown and any trusted probe lease expire, external
// requests may proceed while trusted host requests retain recovery ownership.
func (c *Controller) BeforeExternal(ctx context.Context, scope Scope) error {
	if !c.Participates(scope.InstallationID) {
		return nil
	}
	now := c.now().UTC()
	state, degraded := c.get(ctx, c.key(scope.InstallationID))
	retryAt := strongestStateDeadline(state.BlockedUntil, state)
	if !retryAt.After(now) {
		return nil
	}
	deferral := deferralFromState(scope.InstallationID, state, retryAt)
	c.logDeferral(scope, deferral, degraded)
	if c.mode == ModeEnforce {
		return &deferral
	}
	return nil
}

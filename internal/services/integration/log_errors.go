package integration

import "errors"

var (
	ErrLogProviderUnconfigured = errors.New("no log provider configured")
	ErrLogProviderAmbiguous    = errors.New("multiple log providers configured, specify --provider")
	ErrLogProviderUnknown      = errors.New("unknown or unconfigured log provider")
	ErrLogAnchorInsufficient   = errors.New("anchor insufficient for this provider")
	ErrLogTimeBoundRequired    = errors.New("time bound required: provide --since or --start_time and --end_time")
	ErrLogProviderUnauthorized = errors.New("log provider unauthorized")
	ErrLogRateLimited          = errors.New("log provider rate limit exceeded")
	ErrLogCursorInvalid        = errors.New("cursor is invalid, expired, or does not match current query parameters")
	ErrLogStatsUnsupported     = errors.New("log stats not supported by this provider")
)

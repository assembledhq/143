package ratelimit

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/cache"
)

type Mode string

const (
	ModeOff     Mode = "off"
	ModeObserve Mode = "observe"
	ModeEnforce Mode = "enforce"
)

func (m Mode) Validate() error {
	switch m {
	case ModeOff, ModeObserve, ModeEnforce:
		return nil
	default:
		return fmt.Errorf("invalid GitHub rate-limit mode %q", m)
	}
}

type Config struct {
	Mode                  Mode
	Environment           string
	AppID                 int64
	InstallationAllowlist []int64
	Cache                 *cache.Client
	Logger                zerolog.Logger
	Now                   func() time.Time
	Owner                 string
	ProbeLease            time.Duration
	FallbackLimit         int
}

type Scope struct {
	InstallationID int64
	Caller         string
	Route          string
	SyncReason     string
}

type Permit struct {
	Scope             Scope
	Generation        int64
	Probe             bool
	ProbeOwner        string
	ProbeToken        string
	PredictedDeferral *Deferral
}

type Observation struct {
	StatusCode    int
	Header        http.Header
	Message       string
	GraphQLErrors []GraphQLError
	RequestErr    error
	BodyErr       error
}

type ObservationResult struct {
	Classification Classification
	Deferral       *Deferral
	EpisodeOpened  bool
	Degraded       bool
}

type Controller struct {
	mode        Mode
	environment string
	appID       int64
	allowlist   map[int64]struct{}
	cache       *cache.Client
	logger      zerolog.Logger
	now         func() time.Time
	owner       string
	probeLease  time.Duration
	fallback    *localStore
	degradedLog sync.Once
	probeSeq    atomic.Uint64
	beforeClaim func()
}

const (
	defaultProbeLease        = 30 * time.Second
	defaultFallbackLimit     = 1024
	recoveryStateTTL         = 2*time.Hour + 10*time.Minute
	internalRetryAtHeader    = "X-143-Internal-Github-Retry-At"
	internalGenerationHeader = "X-143-Internal-Github-Generation"
	internalKindHeader       = "X-143-Internal-Github-Limit-Kind"
)

func NewController(cfg Config) (*Controller, error) {
	if cfg.Mode == "" {
		cfg.Mode = ModeObserve
	}
	if err := cfg.Mode.Validate(); err != nil {
		return nil, err
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.ProbeLease <= 0 {
		cfg.ProbeLease = defaultProbeLease
	}
	// A claim must remain retained for its entire lease, including fallback.
	if cfg.ProbeLease > recoveryStateTTL {
		return nil, fmt.Errorf("GitHub probe lease %s exceeds recovery retention %s", cfg.ProbeLease, recoveryStateTTL)
	}
	if cfg.FallbackLimit <= 0 {
		cfg.FallbackLimit = defaultFallbackLimit
	}
	owner := strings.TrimSpace(cfg.Owner)
	if owner == "" {
		owner = "process"
	}
	// A deployment node label is useful for attribution but is not unique
	// across process restarts. Add an instance nonce so a restarted controller
	// cannot accidentally complete a predecessor's still-live probe lease.
	owner = fmt.Sprintf("%s:%d", owner, time.Now().UnixNano())
	allowlist := make(map[int64]struct{}, len(cfg.InstallationAllowlist))
	for _, installationID := range cfg.InstallationAllowlist {
		if installationID > 0 {
			allowlist[installationID] = struct{}{}
		}
	}
	return &Controller{
		mode: cfg.Mode, environment: sanitizeKeyPart(cfg.Environment), appID: cfg.AppID,
		allowlist: allowlist, cache: cfg.Cache, logger: cfg.Logger, now: cfg.Now,
		owner: owner, probeLease: cfg.ProbeLease, fallback: newLocalStore(cfg.FallbackLimit),
	}, nil
}

func (c *Controller) Mode() Mode {
	if c == nil {
		return ModeOff
	}
	return c.mode
}

func (c *Controller) Participates(installationID int64) bool {
	if c == nil || c.mode == ModeOff || installationID <= 0 || c.appID <= 0 {
		return false
	}
	if len(c.allowlist) == 0 {
		// Observation may cover all known installations, but enforcement is
		// deliberately fail-closed to an explicit canary allowlist.
		if c.mode == ModeEnforce {
			return false
		}
		return true
	}
	_, ok := c.allowlist[installationID]
	return ok
}

func (c *Controller) Before(ctx context.Context, scope Scope) (Permit, error) {
	permit := Permit{Scope: scope}
	if !c.Participates(scope.InstallationID) {
		return permit, nil
	}
	now := c.now().UTC()
	key := c.key(scope.InstallationID)
	state, degraded := c.get(ctx, key)
	if state.Generation == 0 || state.BlockedUntil.IsZero() {
		return permit, nil
	}
	permit.Generation = state.Generation
	if state.BlockedUntil.After(now) {
		deferral := deferralFromState(scope.InstallationID, state, state.BlockedUntil)
		permit.PredictedDeferral = &deferral
		c.logDeferral(scope, deferral, degraded)
		if c.mode == ModeEnforce {
			return permit, &deferral
		}
		return permit, nil
	}
	leaseUntil := now.Add(c.probeLease)
	probeToken := fmt.Sprintf("%s:probe:%d", c.owner, c.probeSeq.Add(1))
	if c.beforeClaim != nil {
		c.beforeClaim()
	}
	claimedState, claimed, claimDegraded := c.claim(ctx, key, probeToken, now, leaseUntil)
	degraded = degraded || claimDegraded
	if claimedState.Kind == "" {
		claimedState.Kind = state.Kind
		claimedState.Resource = state.Resource
		claimedState.Reason = state.Reason
		claimedState.EpisodeCount = state.EpisodeCount
	}
	permit.Generation = claimedState.Generation
	if claimed {
		permit.Probe = true
		permit.ProbeOwner = c.owner
		permit.ProbeToken = probeToken
		return permit, nil
	}
	if claimedState.Generation >= state.Generation && claimedState.BlockedUntil.IsZero() && claimedState.ProbeUntil.IsZero() {
		// Another controller completed recovery between the initial read and
		// atomic claim. Its generation-preserving tombstone is authoritative.
		return Permit{Scope: scope}, nil
	}
	retryAt := claimedState.BlockedUntil
	if claimedState.ProbeUntil.After(retryAt) {
		retryAt = claimedState.ProbeUntil
	}
	if !retryAt.After(now) {
		retryAt = leaseUntil
	}
	deferral := deferralFromState(scope.InstallationID, claimedState, retryAt)
	permit.PredictedDeferral = &deferral
	c.logDeferral(scope, deferral, degraded)
	if c.mode == ModeEnforce {
		return permit, &deferral
	}
	return permit, nil
}

// IsDefinitiveResponse identifies statuses that can prove provider recovery
// after throttle classification and complete body decoding. Application errors
// remain errors to callers; redirects and retryable failures prove no recovery.
func IsDefinitiveResponse(status int) bool {
	if status == http.StatusRequestTimeout || status == http.StatusTooEarly || status == http.StatusTooManyRequests {
		return false
	}
	return status >= 200 && status < 300 || status == http.StatusNotModified || status >= 400 && status < 500
}

func (c *Controller) Observe(ctx context.Context, permit Permit, observation Observation) ObservationResult {
	if !c.Participates(permit.Scope.InstallationID) {
		return ObservationResult{}
	}
	now := c.now().UTC()
	classification := ClassifyResponse(ResponseInput{
		StatusCode: observation.StatusCode, Header: observation.Header, Message: observation.Message,
		GraphQLErrors: observation.GraphQLErrors, Now: now,
	})
	result := ObservationResult{Classification: classification}
	key := c.key(permit.Scope.InstallationID)
	if classification.RateLimited {
		state, degraded := c.get(ctx, key)
		deadline := localDeadline(now, state.EpisodeCount, key)
		if classification.RetryAt != nil {
			deadline = classification.RetryAt.UTC()
		}
		stateTTL := recoveryStateTTL
		if minimumTTL := deadline.Sub(now) + time.Minute; minimumTTL > stateTTL {
			stateTTL = minimumTTL
		}
		observed, opened, applied, observeDegraded := c.observe(ctx, key, cache.GitHubRateLimitObservation{
			Now: now, BlockedUntil: deadline, Kind: string(classification.Kind), Resource: classification.Resource,
			Reason: classification.Reason, ProbeGeneration: probeGeneration(permit), ProbeToken: permit.ProbeToken, TTL: stateTTL, ProviderDeadline: classification.RetryAt != nil,
		})
		result.EpisodeOpened = opened
		result.Degraded = degraded || observeDegraded
		deferralDeadline := strongestStateDeadline(deadline, observed)
		deferral := deferralFromState(permit.Scope.InstallationID, observed, deferralDeadline)
		if !applied && deadline.After(observed.BlockedUntil) && deadline.After(observed.ProbeUntil) {
			// A fenced observation may still supply a later deadline for this
			// request, but it must not relabel stronger retained evidence.
			deferral.Kind, deferral.Resource, deferral.Reason = classification.Kind, classification.Resource, classification.Reason
		}
		result.Deferral = &deferral
		if opened {
			c.logger.Warn().Int64("github_installation_id", permit.Scope.InstallationID).
				Str("github_rate_limit_kind", string(classification.Kind)).
				Str("github_rate_limit_resource", classification.Resource).
				Int64("github_rate_limit_generation", observed.Generation).
				Int64("github_rate_limit_episode", observed.EpisodeCount).
				Str("github_rate_limit_mode", string(c.mode)).
				Time("github_retry_at", observed.BlockedUntil).
				Bool("github_rate_limit_coordination_degraded", result.Degraded).
				Msg("github rate limit episode transition")
		}
		return result
	}
	if permit.Probe {
		success := observation.RequestErr == nil && observation.BodyErr == nil && IsDefinitiveResponse(observation.StatusCode)
		completed, degraded := c.complete(ctx, key, permit.ProbeOwner, permit.ProbeToken, permit.Generation, success)
		result.Degraded = degraded
		if completed && success {
			c.logger.Info().Int64("github_installation_id", permit.Scope.InstallationID).
				Int64("github_rate_limit_generation", permit.Generation).
				Str("github_rate_limit_mode", string(c.mode)).
				Bool("github_rate_limit_coordination_degraded", degraded).
				Msg("github rate limit episode recovered")
		}
	}
	return result
}

// AttachInternalRetryMetadata carries an enforcement-selected deadline beside
// the original provider response. The header is added only after the request
// returns, so it is never sent to GitHub.
func AttachInternalRetryMetadata(header http.Header, deferral *Deferral) {
	if header == nil || deferral == nil {
		return
	}
	header.Set(internalRetryAtHeader, deferral.RetryAt.UTC().Format(time.RFC3339Nano))
	header.Set(internalGenerationHeader, strconv.FormatInt(deferral.Generation, 10))
	header.Set(internalKindHeader, string(deferral.Kind))
}

func InternalRetryMetadata(header http.Header, installationID int64) (*Deferral, bool) {
	if header == nil {
		return nil, false
	}
	retryAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(header.Get(internalRetryAtHeader)))
	if err != nil {
		return nil, false
	}
	generation, _ := strconv.ParseInt(strings.TrimSpace(header.Get(internalGenerationHeader)), 10, 64)
	kind := Kind(strings.TrimSpace(header.Get(internalKindHeader)))
	if kind.Validate() != nil {
		kind = KindUnknown
	}
	return &Deferral{Kind: kind, InstallationID: installationID, RetryAt: retryAt.UTC(), Generation: generation}, true
}

func (c *Controller) key(installationID int64) string {
	return fmt.Sprintf("{ghrl:v1:%s:app:%d:inst:%d}:state", c.environment, c.appID, installationID)
}

func (c *Controller) get(ctx context.Context, key string) (cache.GitHubRateLimitState, bool) {
	now := c.now().UTC()
	localState := c.fallback.get(key, now)
	if c.cache != nil {
		state, err := c.cache.GetGitHubRateLimitState(ctx, key)
		if err == nil {
			if localStateDominates(localState, state) {
				state, _, err = c.cache.ReconcileGitHubRateLimitState(ctx, key, localState, now, stateTTL(localState, now))
				if err != nil {
					c.logDegraded(err)
					return localState, true
				}
			}
			return c.fallback.remember(key, state, now), false
		}
		c.logDegraded(err)
	} else {
		c.logDegraded(fmt.Errorf("redis coordination is not configured"))
	}
	return localState, true
}

func (c *Controller) observe(ctx context.Context, key string, observation cache.GitHubRateLimitObservation) (cache.GitHubRateLimitState, bool, bool, bool) {
	if c.cache != nil {
		localState := c.fallback.get(key, observation.Now)
		state, opened, applied, err := c.cache.ObserveGitHubRateLimit(ctx, key, observation)
		if err == nil {
			if localStateDominates(localState, state) {
				_, _, reconcileErr := c.cache.ReconcileGitHubRateLimitState(ctx, key, localState, observation.Now, stateTTL(localState, observation.Now))
				if reconcileErr != nil {
					c.logDegraded(reconcileErr)
					state, opened, applied := c.fallback.observe(key, observation)
					return state, opened, applied, true
				}
				state, opened, applied, err = c.cache.ObserveGitHubRateLimit(ctx, key, observation)
				if err != nil {
					c.logDegraded(err)
					state, opened, applied := c.fallback.observe(key, observation)
					return state, opened, applied, true
				}
			}
			return c.fallback.remember(key, state, observation.Now), opened, applied, false
		}
		c.logDegraded(err)
	}
	state, opened, applied := c.fallback.observe(key, observation)
	return state, opened, applied, true
}

func (c *Controller) claim(ctx context.Context, key, token string, now, leaseUntil time.Time) (cache.GitHubRateLimitState, bool, bool) {
	if c.cache != nil {
		localState := c.fallback.get(key, now)
		state, claimed, err := c.cache.ClaimGitHubRateLimitProbe(ctx, key, c.owner, token, now, leaseUntil, recoveryStateTTL)
		if err == nil {
			if localStateDominates(localState, state) {
				reconciled, _, reconcileErr := c.cache.ReconcileGitHubRateLimitState(ctx, key, localState, now, stateTTL(localState, now))
				if reconcileErr != nil {
					c.logDegraded(reconcileErr)
					state, claimed := c.fallback.claim(key, c.owner, token, now, leaseUntil)
					return state, claimed, true
				}
				if reconciled.Generation >= localState.Generation && reconciled.BlockedUntil.IsZero() {
					return c.fallback.remember(key, reconciled, now), false, false
				}
				state, claimed, err = c.cache.ClaimGitHubRateLimitProbe(ctx, key, c.owner, token, now, leaseUntil, recoveryStateTTL)
				if err != nil {
					c.logDegraded(err)
					state, claimed := c.fallback.claim(key, c.owner, token, now, leaseUntil)
					return state, claimed, true
				}
			}
			state = c.fallback.remember(key, state, now)
			return state, claimed && state.ProbeToken == token, false
		}
		c.logDegraded(err)
	}
	state, claimed := c.fallback.claim(key, c.owner, token, now, leaseUntil)
	return state, claimed, true
}

func (c *Controller) complete(ctx context.Context, key, owner, token string, generation int64, success bool) (bool, bool) {
	now := c.now().UTC()
	if c.cache != nil {
		localState := c.fallback.get(key, now)
		completed, err := c.cache.CompleteGitHubRateLimitProbe(ctx, key, owner, token, generation, success, recoveryStateTTL)
		if err == nil {
			if !completed && localState.Generation == generation && localState.ProbeGeneration == generation && localState.ProbeOwner == owner && localState.ProbeToken == token {
				remoteState, getErr := c.cache.GetGitHubRateLimitState(ctx, key)
				if getErr != nil {
					c.logDegraded(getErr)
					return c.fallback.complete(key, owner, token, generation, success, now), true
				}
				if localStateDominates(localState, remoteState) {
					reconciled, _, reconcileErr := c.cache.ReconcileGitHubRateLimitState(ctx, key, localState, now, stateTTL(localState, now))
					if reconcileErr != nil {
						c.logDegraded(reconcileErr)
						return c.fallback.complete(key, owner, token, generation, success, now), true
					}
					if reconciled.Generation == generation && reconciled.ProbeGeneration == generation && reconciled.ProbeOwner == owner && reconciled.ProbeToken == token {
						completed, err = c.cache.CompleteGitHubRateLimitProbe(ctx, key, owner, token, generation, success, recoveryStateTTL)
						if err != nil {
							c.logDegraded(err)
							return c.fallback.complete(key, owner, token, generation, success, now), true
						}
					}
				} else {
					c.fallback.remember(key, remoteState, now)
				}
			}
			if completed {
				c.fallback.complete(key, owner, token, generation, success, now)
			}
			return completed, false
		}
		c.logDegraded(err)
	}
	return c.fallback.complete(key, owner, token, generation, success, now), true
}

func localStateDominates(localState, remoteState cache.GitHubRateLimitState) bool {
	if localState.Generation == 0 {
		return false
	}
	if remoteState.Generation >= localState.Generation && remoteState.BlockedUntil.IsZero() {
		return false
	}
	if localState.BlockedUntil.After(remoteState.BlockedUntil) {
		return true
	}
	if remoteState.Generation > localState.Generation {
		return false
	}
	if localState.Generation > remoteState.Generation {
		return true
	}
	return localState.Generation == remoteState.Generation &&
		localState.ProbeToken != "" && remoteState.ProbeToken == "" &&
		!remoteState.BlockedUntil.After(localState.BlockedUntil)
}

func stateTTL(state cache.GitHubRateLimitState, now time.Time) time.Duration {
	ttl := recoveryStateTTL
	retainedUntil := state.BlockedUntil
	if state.ProbeUntil.After(retainedUntil) {
		retainedUntil = state.ProbeUntil
	}
	if minimumTTL := retainedUntil.Sub(now) + time.Minute; minimumTTL > ttl {
		ttl = minimumTTL
	}
	return ttl
}

func strongestStateDeadline(providerDeadline time.Time, state cache.GitHubRateLimitState) time.Time {
	deadline := providerDeadline
	if state.BlockedUntil.After(deadline) {
		deadline = state.BlockedUntil
	}
	if state.ProbeUntil.After(deadline) {
		deadline = state.ProbeUntil
	}
	return deadline
}

func (c *Controller) logDegraded(err error) {
	c.degradedLog.Do(func() {
		c.logger.Warn().Err(err).Str("github_rate_limit_mode", string(c.mode)).Msg("github rate limit coordination degraded to process-local state")
	})
}

func (c *Controller) logDeferral(scope Scope, deferral Deferral, degraded bool) {
	c.logger.Info().Int64("github_installation_id", scope.InstallationID).
		Str("github_caller", scope.Caller).Str("github_rate_limit_kind", string(deferral.Kind)).
		Str("github_rate_limit_resource", deferral.Resource).Str("github_route", scope.Route).
		Str("github_sync_reason", scope.SyncReason).
		Str("github_rate_limit_mode", string(c.mode)).Time("github_retry_at", deferral.RetryAt).
		Bool("github_request_suppressed", c.mode == ModeEnforce).
		Bool("github_rate_limit_coordination_degraded", degraded).Msg("github request deferred")
}

func deferralFromState(installationID int64, state cache.GitHubRateLimitState, retryAt time.Time) Deferral {
	kind := Kind(state.Kind)
	if kind.Validate() != nil {
		kind = KindUnknown
	}
	return Deferral{Kind: kind, Resource: state.Resource, InstallationID: installationID, RetryAt: retryAt.UTC(), Generation: state.Generation, Reason: state.Reason}
}

func probeGeneration(permit Permit) int64 {
	if permit.Probe {
		return permit.Generation
	}
	return 0
}

func localDeadline(now time.Time, episodeCount int64, key string) time.Time {
	shift := episodeCount
	if shift > 3 {
		shift = 3
	}
	delay := time.Minute * time.Duration(1<<shift)
	if delay > 5*time.Minute {
		delay = 5 * time.Minute
	}
	digest := sha256.Sum256([]byte(key + ":" + strconv.FormatInt(episodeCount, 10)))
	jitter := time.Duration(binary.BigEndian.Uint32(digest[:4])%30) * time.Second
	return now.Add(delay + jitter).UTC()
}

func sanitizeKeyPart(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	var builder strings.Builder
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			builder.WriteRune(r)
		} else {
			builder.WriteByte('_')
		}
	}
	return builder.String()
}

type localEntry struct {
	state     cache.GitHubRateLimitState
	expiresAt time.Time
}

type localStore struct {
	mu      sync.Mutex
	limit   int
	entries map[string]localEntry
}

func newLocalStore(limit int) *localStore {
	return &localStore{limit: limit, entries: make(map[string]localEntry)}
}

func (s *localStore) get(key string, now time.Time) cache.GitHubRateLimitState {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune(now)
	return s.entries[key].state
}

func (s *localStore) remember(key string, state cache.GitHubRateLimitState, now time.Time) cache.GitHubRateLimitState {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune(now)
	entry := s.entries[key]
	if state.Generation == 0 || entry.state.Generation > state.Generation ||
		entry.state.Generation == state.Generation && !state.BlockedUntil.IsZero() && entry.state.BlockedUntil.After(state.BlockedUntil) {
		return entry.state
	}
	if state.Generation == entry.state.Generation && state.BlockedUntil.Equal(entry.state.BlockedUntil) &&
		!state.BlockedUntil.IsZero() && entry.state.ProbeToken != "" && entry.state.ProbeOwner != "" &&
		entry.state.ProbeGeneration == state.Generation && entry.state.ProbeUntil.After(now) &&
		(state.ProbeToken == "" || state.ProbeOwner == "" || state.ProbeGeneration != state.Generation || state.ProbeUntil.Before(entry.state.ProbeUntil)) {
		// A Redis read started before a concurrent claim can return afterward.
		// Equal cooldown evidence with absent or older authority cannot revoke a live claim;
		// newer generations, stronger deadlines, and completion tombstones can.
		state.ProbeOwner, state.ProbeToken = entry.state.ProbeOwner, entry.state.ProbeToken
		state.ProbeGeneration, state.ProbeUntil = entry.state.ProbeGeneration, entry.state.ProbeUntil
	}
	expiresAt := now.Add(stateTTL(state, now))
	if state.Generation == entry.state.Generation && state.BlockedUntil.IsZero() && entry.state.BlockedUntil.IsZero() {
		expiresAt = entry.expiresAt // Repeated reads must not extend tombstone retention.
	}
	if !state.BlockedUntil.IsZero() && entry.expiresAt.After(expiresAt) {
		expiresAt = entry.expiresAt
	}
	s.put(key, localEntry{state: state, expiresAt: expiresAt})
	return state
}

func (s *localStore) observe(key string, observation cache.GitHubRateLimitObservation) (cache.GitHubRateLimitState, bool, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune(observation.Now)
	entry := s.entries[key]
	state := entry.state
	opened := false
	fenced := observation.ProbeGeneration > 0 && (observation.ProbeGeneration != state.Generation || observation.ProbeGeneration != state.ProbeGeneration || observation.ProbeToken == "" || observation.ProbeToken != state.ProbeToken)
	if fenced && (!observation.ProviderDeadline || state.BlockedUntil.IsZero() || !observation.BlockedUntil.After(state.BlockedUntil) || !observation.BlockedUntil.After(observation.Now)) {
		return state, false, false
	}
	if !fenced && (observation.ProbeGeneration > 0 || !state.BlockedUntil.After(observation.Now)) {
		state.Generation++
		state.EpisodeCount++
		opened = true
	}
	if fenced || opened {
		state.ProbeOwner, state.ProbeToken, state.ProbeGeneration, state.ProbeUntil = "", "", 0, time.Time{}
	}
	if observation.BlockedUntil.After(state.BlockedUntil) {
		state.BlockedUntil = observation.BlockedUntil
		state.Kind, state.Resource, state.Reason = observation.Kind, observation.Resource, observation.Reason
	}
	expiresAt := observation.Now.Add(observation.TTL)
	if minimumExpiry := state.BlockedUntil.Add(time.Minute); minimumExpiry.After(expiresAt) {
		expiresAt = minimumExpiry
	}
	if entry.expiresAt.After(expiresAt) {
		expiresAt = entry.expiresAt
	}
	s.put(key, localEntry{state: state, expiresAt: expiresAt})
	return state, opened, true
}

func (s *localStore) claim(key, owner, token string, now, leaseUntil time.Time) (cache.GitHubRateLimitState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune(now)
	entry := s.entries[key]
	state := entry.state
	if state.Generation == 0 || state.BlockedUntil.IsZero() || state.BlockedUntil.After(now) || state.ProbeUntil.After(now) {
		return state, false
	}
	state.ProbeOwner, state.ProbeToken, state.ProbeGeneration, state.ProbeUntil = owner, token, state.Generation, leaseUntil
	s.put(key, localEntry{state: state, expiresAt: now.Add(recoveryStateTTL)})
	return state, true
}

func (s *localStore) complete(key, owner, token string, generation int64, success bool, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[key]
	if !ok || entry.state.Generation != generation || entry.state.ProbeGeneration != generation || entry.state.ProbeOwner != owner || entry.state.ProbeToken != token {
		return false
	}
	entry.state.ProbeOwner, entry.state.ProbeToken, entry.state.ProbeGeneration, entry.state.ProbeUntil = "", "", 0, time.Time{}
	if success {
		entry.state = cache.GitHubRateLimitState{Generation: generation, EpisodeCount: entry.state.EpisodeCount}
	} else {
		entry.state.Generation++ // Fence peers that retained the released lease.
	}
	entry.expiresAt = now.Add(recoveryStateTTL)
	s.entries[key] = entry
	return true
}

func (s *localStore) prune(now time.Time) {
	for key, entry := range s.entries {
		if !entry.expiresAt.After(now) {
			delete(s.entries, key)
		}
	}
}

func (s *localStore) put(key string, entry localEntry) {
	if _, exists := s.entries[key]; !exists && len(s.entries) >= s.limit {
		keys := make([]string, 0, len(s.entries))
		for existing := range s.entries {
			keys = append(keys, existing)
		}
		sort.Strings(keys)
		delete(s.entries, keys[0])
	}
	s.entries[key] = entry
}

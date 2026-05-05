package linear

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"

	"github.com/assembledhq/143/internal/models"
)

// refreshOutcome labels the result attribute on the refresh metric. Kept
// as a typed string so a typo in a call site fails the type checker
// instead of producing a silently-different label string in production.
type refreshOutcome string

const (
	// refreshOutcomeFresh: cached token returned without an /oauth/token
	// round trip. The hot path; should dominate by orders of magnitude.
	refreshOutcomeFresh refreshOutcome = "fresh"
	// refreshOutcomeRefreshed: proactive refresh succeeded and the new
	// access/refresh pair was persisted.
	refreshOutcomeRefreshed refreshOutcome = "refreshed"
	// refreshOutcomeRotatedByPeer: another node won the refresh race;
	// we re-read the row and used their rotated token instead of
	// declaring revocation. A spike here indicates >1 process/pod is
	// hammering the same org's credential — usually the worker + API
	// for the same MODE=all deploy.
	refreshOutcomeRotatedByPeer refreshOutcome = "rotated_by_peer"
	// refreshOutcomeRevoked: Linear returned invalid_grant; refresh
	// token is dead, integration flipped to errored, user must
	// reconnect. Should be rare in steady state.
	refreshOutcomeRevoked refreshOutcome = "revoked"
	// refreshOutcomeOAuthMisconfigured: invalid_client from /oauth/token
	// (rotated CLIENT_SECRET, typo, etc.). Operator action required —
	// graphs spiking here mean a deploy broke things and EVERY org's
	// next refresh will fail until env vars are corrected.
	refreshOutcomeOAuthMisconfigured refreshOutcome = "oauth_misconfigured"
	// refreshOutcomeNoRefreshToken: legacy install hit the refresh
	// window. Expected in low volume during the refresh-token rollout;
	// should taper to zero once all orgs have reconnected.
	refreshOutcomeNoRefreshToken refreshOutcome = "no_refresh_token"
	// refreshOutcomeCachedFallback: refresh failed transiently (network
	// blip, Linear 5xx) but the cached token is still inside its
	// validity window so we returned it. Sustained presence indicates
	// Linear-side incident or our own connectivity issue.
	refreshOutcomeCachedFallback refreshOutcome = "cached_fallback"
	// refreshOutcomeTransientFailure: refresh failed and the cached
	// token is also expired, so the call returned an error to the
	// caller. The most user-impacting bucket — every count here is a
	// session that ran without a Linear token.
	refreshOutcomeTransientFailure refreshOutcome = "transient_failure"
	// refreshOutcomeForceRefresh: post-401 force refresh from
	// withRefreshableClient, regardless of whether it succeeded (the
	// retry's success/failure shows up in the GraphQL-side metrics,
	// not here). Counts retries themselves so we can spot a Linear
	// outage that forces every read to take the slow path.
	refreshOutcomeForceRefresh refreshOutcome = "force_refresh"
)

// linearMeterName is the OTel meter scope for refresh-token metrics.
// Matches the scoping convention from internal/metrics/billing.go's
// meterName constant.
const linearMeterName = "github.com/assembledhq/143/linear"

// refreshMetrics groups the OTel instruments produced by ensureRefreshMetrics.
// Stored on the package-level metricsHolder so every Service in the process
// shares one set of registered instruments — registering twice produces
// duplicate counters with the same name, which OTel SDKs handle by emitting
// noisy warnings.
type refreshMetrics struct {
	totalCounter otelmetric.Int64Counter
}

// metricsHolder lazily wires the refresh OTel instruments on first use,
// using sync.Once so concurrent Services don't double-register. The Once
// is package-level (not per-Service) because OTel instruments are
// per-process; per-Service init would re-register on every test that
// constructs a fresh Service.
//
// Test note: the cached counter binds to whatever MeterProvider was
// installed on first use. Tests that swap MeterProviders mid-run will
// keep recording into the original meter — acceptable because metrics
// are best-effort observability and not asserted on by tests.
var (
	metricsOnce sync.Once
	metrics     *refreshMetrics
	metricsErr  error
)

// ensureRefreshMetrics returns the cached refreshMetrics instruments,
// registering them on first call. A registration failure is recorded once
// and observeRefreshOutcome becomes a no-op for the rest of the process —
// metrics are best-effort observability, never load-bearing for
// correctness.
func ensureRefreshMetrics() (*refreshMetrics, error) {
	metricsOnce.Do(func() {
		meter := otel.Meter(linearMeterName)
		counter, err := meter.Int64Counter(
			"linear.refresh_token.total",
			otelmetric.WithDescription("Linear OAuth refresh-token rotation outcomes, labeled by result"),
			otelmetric.WithUnit("{event}"),
		)
		if err != nil {
			metricsErr = err
			return
		}
		metrics = &refreshMetrics{totalCounter: counter}
	})
	return metrics, metricsErr
}

// observeRefreshOutcome bumps the linear.refresh_token.total counter with
// the outcome label. Best-effort: metric registration failures (rare;
// typically only happens with a misconfigured MeterProvider) silently
// drop the observation rather than perturbing the refresh path's error
// contract.
//
// org_id is intentionally NOT a label dimension. Adding it would cause
// counter cardinality to grow with the org count, which OTel handles
// poorly above ~10K labels per metric. Per-org refresh history is
// available via structured logs (the Info log on successful refresh
// includes org_id) and the integrations table's status column.
func (s *Service) observeRefreshOutcome(outcome refreshOutcome) {
	m, err := ensureRefreshMetrics()
	if err != nil || m == nil {
		return
	}
	m.totalCounter.Add(context.Background(), 1, otelmetric.WithAttributes(
		attribute.String("outcome", string(outcome)),
	))
}

// linearTokenURL is the OAuth token endpoint used for both the initial
// authorization-code exchange (handlers/integrations.go) and the
// refresh-token rotation done here. Kept in a single place so a future
// region/staging override can be threaded through both sites.
const linearTokenURL = "https://api.linear.app/oauth/token" // #nosec G101 -- OAuth endpoint URL, not credentials

// refreshGrantType is the OAuth 2.0 grant type for token refresh.
const refreshGrantType = "refresh_token"

// refreshWindow is how far before a token's stated expiry we proactively
// refresh. Five minutes is comfortably longer than any plausible round-trip
// + clock-skew envelope between this process, Linear, and the credential
// store, while still leaving a wide margin for the agent sandbox to consume
// the token before it expires mid-call.
const refreshWindow = 5 * time.Minute

// refreshHTTPTimeout caps a single refresh round-trip. Linear's /oauth/token
// is normally sub-second; the cap exists so a wedged endpoint can't hold the
// per-org refresh mutex forever and stall every concurrent caller.
const refreshHTTPTimeout = 10 * time.Second

// refreshErrorBodyLimit caps how much of the refresh-endpoint response body
// we read on error. Linear errors are tiny JSON shapes; reading a bounded
// prefix prevents a hostile or pathological response from consuming memory
// while still surfacing the operator-actionable head of the error message.
const refreshErrorBodyLimit = 8192

// ErrNoRefreshToken is returned when GetValidToken needs to refresh an
// expired access token but the credential row has no refresh token. This
// happens for legacy installs created before refresh-token fields were
// stored. Callers receiving this error should surface a "reconnect Linear"
// banner — refresh is not recoverable without user interaction.
var ErrNoRefreshToken = errors.New("linear credential has no refresh token; reconnect required")

// ErrRefreshTokenRevoked is returned when Linear rejects our refresh token
// (typically a 400 invalid_grant or a 401). The caller has already zeroed
// the refresh token from storage and flipped the integration to errored;
// callers should surface "reconnect Linear" to the user. Not retryable.
var ErrRefreshTokenRevoked = errors.New("linear refresh token revoked; reconnect required")

// ErrOAuthClientNotConfigured is returned when GetValidToken needs to refresh
// but the linear service was constructed without the OAuth client_id /
// client_secret. This is a misconfiguration, not a user-recoverable state —
// the operator must populate LINEAR_OAUTH_CLIENT_ID / LINEAR_OAUTH_CLIENT_SECRET.
var ErrOAuthClientNotConfigured = errors.New("linear oauth client not configured; cannot refresh token")

// CredentialWriter is the narrow surface the refresh path needs to persist
// rotated tokens. Implemented by *db.OrgCredentialStore in production. Held
// as a separate interface from CredentialReader so test harnesses that only
// need read access can omit the writer.
type CredentialWriter interface {
	Upsert(ctx context.Context, orgID uuid.UUID, cfg models.ProviderConfig) error
}

// CredentialCASWriter is the production-safe refresh persistence surface.
// It updates the Linear credential only if the stored refresh token still
// matches the token we just redeemed. A mismatch means a user reconnect or
// another process has already written a newer token chain, and the refresh
// path must use that current row rather than overwriting it.
type CredentialCASWriter interface {
	UpdateLinearConfigIfRefreshTokenMatches(ctx context.Context, orgID uuid.UUID, expectedRefreshToken string, cfg models.LinearConfig) (models.LinearConfig, bool, error)
}

// OAuthClientCreds holds the client credentials Linear requires to redeem a
// refresh token at /oauth/token. They are the same values used by the
// authorization-code exchange in the API handlers (LINEAR_OAUTH_CLIENT_ID /
// LINEAR_OAUTH_CLIENT_SECRET).
type OAuthClientCreds struct {
	ClientID     string
	ClientSecret string
}

// linearTokenRefreshResponse parses Linear's /oauth/token response shape for
// both authorization-code and refresh-token grants. The response is JSON; the
// API handlers' linearTokenResponse mirrors this shape and the two should
// stay aligned (any new field that affects long-lived state — e.g. an
// id_token-style claim — must be persisted in both flows or the refresh path
// will silently strip it).
type linearTokenRefreshResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
	ExpiresIn    int    `json:"expires_in"`
}

// orgRefreshMu returns a per-org mutex used to serialize concurrent refresh
// attempts within this process. Linear is org-singleton (one credential row
// per org), so per-org granularity is the correct lock unit.
//
// Race-safety: refreshMuRegistry is an atomic.Pointer. NewService stores
// a fresh sync.Map up front; direct &Service{} construction (tests) leaves
// the pointer nil, in which case we CAS in a registry. CAS losers throw
// away their fresh map and use the winner's — so all goroutines end up
// using the same sync.Map and there is no torn-write window.
//
// Cross-process coordination: per-process locks cannot prevent two nodes
// from racing to refresh the same credential. Linear rotates the refresh
// token on every redemption, so the slower node will see invalid_grant on
// its POST. refreshLinearToken handles this race by re-reading the
// credential after a refresh failure and falling back to the
// just-persisted token if a peer node won the race.
func (s *Service) orgRefreshMu(orgID uuid.UUID) *sync.Mutex {
	reg := s.refreshMuRegistry.Load()
	if reg == nil {
		fresh := &sync.Map{}
		// CompareAndSwap from nil: if another goroutine raced us, theirs
		// wins and we discard `fresh`. Either way, a subsequent Load is
		// guaranteed non-nil.
		s.refreshMuRegistry.CompareAndSwap(nil, fresh)
		reg = s.refreshMuRegistry.Load()
	}
	val, _ := reg.LoadOrStore(orgID.String(), &sync.Mutex{})
	return val.(*sync.Mutex)
}

// loadLinearCredential reads the org's Linear integration row and parses the
// stored credential into a typed LinearConfig. Returns ErrIntegrationNotFound
// (wrapped) when the org has no Linear integration row at all. Callers that
// only need the access token should prefer integrationFor / GetValidToken;
// this is the shared inner helper that returns the full typed config so the
// refresh path can read RefreshToken and ExpiresAt.
//
// Nil-safe two-layer guard, mirroring the auth_status.go pattern: nil
// receiver and nil integrations reader return ErrIntegrationNotFound
// rather than panicking so api-only / MODE=worker wiring paths that
// didn't hook up the integrations store degrade to "no integration
// installed" instead of crashing on first use.
//
// A nil credentials reader is NOT treated as ErrIntegrationNotFound —
// it would mask a misconfigured wiring path (integrations exists but
// credentials store is missing) as "user has no Linear" and silently
// drop the connection. We let the credentials.Get call proceed and
// the resulting nil-pointer surfaces as a real wiring bug instead.
// Tests that supply integrations but not credentials are exercising
// the integration-lookup path and stop before reaching credentials.
func (s *Service) loadLinearCredential(ctx context.Context, orgID uuid.UUID) (models.Integration, models.LinearConfig, error) {
	if s == nil || s.integrations == nil {
		return models.Integration{}, models.LinearConfig{}, fmt.Errorf("lookup linear integration: %w", ErrIntegrationNotFound)
	}
	integration, err := s.integrations.GetByOrgAndProvider(ctx, orgID, "linear")
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return models.Integration{}, models.LinearConfig{}, fmt.Errorf("lookup linear integration: %w", ErrIntegrationNotFound)
		}
		return models.Integration{}, models.LinearConfig{}, fmt.Errorf("lookup linear integration: %w", err)
	}
	if integration.Status != models.IntegrationStatusActive && integration.Status != models.IntegrationStatusError {
		return models.Integration{}, models.LinearConfig{}, fmt.Errorf("lookup linear integration: %w", ErrIntegrationNotFound)
	}
	if s.credentials == nil {
		return models.Integration{}, models.LinearConfig{}, fmt.Errorf("lookup linear credential: credentials store not wired")
	}
	cred, err := s.credentials.Get(ctx, orgID, models.ProviderLinear)
	if err != nil {
		return models.Integration{}, models.LinearConfig{}, fmt.Errorf("lookup linear credential: %w", err)
	}
	if cred == nil {
		return models.Integration{}, models.LinearConfig{}, fmt.Errorf("linear credential not found")
	}
	cfg, ok := cred.Config.(models.LinearConfig)
	if !ok {
		return models.Integration{}, models.LinearConfig{}, fmt.Errorf("linear credential config is wrong type: got %T", cred.Config)
	}
	return integration, cfg, nil
}

// GetValidAccessToken is the sandbox-injection convenience wrapper around
// GetValidToken. It returns ONLY the access token (the integration row is
// dropped) and suppresses the "no Linear integration installed" case to
// ("", nil) so callers can check `token == ""` without importing
// linear-specific sentinels.
//
// All other errors (transport failure, refresh-token revoked, OAuth client
// misconfigured) propagate as-is — callers who want to distinguish them
// can errors.Is against the package-level sentinels. Callers that need
// the integration row alongside the token should use the full
// GetValidToken instead.
func (s *Service) GetValidAccessToken(ctx context.Context, orgID uuid.UUID) (string, error) {
	_, token, err := s.GetValidToken(ctx, orgID)
	if errors.Is(err, ErrIntegrationNotFound) {
		return "", nil
	}
	return token, err
}

// GetValidToken returns the integration row plus a Linear access token that
// is valid right now: if the cached token's expiry is within the refresh
// window and a refresh token is available, it transparently rotates the
// token at Linear and persists the new access/refresh pair before returning.
//
// Behavior contract:
//
//   - Legacy installs (no refresh token, zero ExpiresAt): returns the cached
//     token unchanged. Refresh is impossible without a refresh token; user must
//     reconnect when the token finally hits a hard 401.
//   - Healthy installs with valid token: returns cached token (no API call).
//   - Healthy installs near expiry: refreshes, persists, returns new token.
//   - Refresh-time network/transient errors: falls back to the cached token
//     IFF it is not yet expired. Otherwise returns the error.
//   - Refresh token revoked: zeroes the refresh token, marks the integration
//     errored (so the UI shows "reconnect"), and returns ErrRefreshTokenRevoked.
//
// Error contract: on any non-nil error, the returned integration row is
// the zero value. Callers ignoring the error and reading integration.ID
// will see uuid.Nil rather than a half-populated row that lies about
// which integration the credential belonged to.
//
// Concurrency: per-org mutex serializes intra-process refreshes. Multi-node
// races are detected and recovered (see refreshLinearToken).
func (s *Service) GetValidToken(ctx context.Context, orgID uuid.UUID) (models.Integration, string, error) {
	integration, cfg, err := s.loadLinearCredential(ctx, orgID)
	if err != nil {
		return models.Integration{}, "", err
	}
	if !cfg.NeedsRefresh(refreshWindow) {
		// Either still well within validity, or a legacy install with no
		// refresh capability. Either way, the cached token is what callers
		// should use.
		if cfg.AccessToken == "" {
			return models.Integration{}, "", fmt.Errorf("linear credential has empty access token")
		}
		s.observeRefreshOutcome(refreshOutcomeFresh)
		return integration, cfg.AccessToken, nil
	}

	refreshed, err := s.refreshLinearToken(ctx, orgID, cfg, false /* skipFreshCheck */)
	if err != nil {
		// Graceful degradation: a transient refresh failure (network blip,
		// Linear 5xx) shouldn't take down the calling code path if the
		// cached token is still inside its validity window. The next call
		// will re-attempt refresh; meanwhile callers get a token that may
		// be slightly closer to expiry but still works.
		//
		// Hard failures (revoked token, no refresh token, missing OAuth
		// client) are NOT papered over — falling back to the cached token
		// would just queue up a 401 on the next call, and the user needs
		// to reconnect (or the operator needs to fix env vars).
		hardFailure := errors.Is(err, ErrRefreshTokenRevoked) ||
			errors.Is(err, ErrNoRefreshToken) ||
			errors.Is(err, ErrOAuthClientNotConfigured)
		if !hardFailure && !cfg.IsExpired() && cfg.AccessToken != "" {
			s.logger.Warn().Err(err).
				Str("org_id", orgID.String()).
				Time("expires_at", cfg.ExpiresAt).
				Msg("linear: token refresh failed; falling back to cached token still inside validity window")
			s.observeRefreshOutcome(refreshOutcomeCachedFallback)
			return integration, cfg.AccessToken, nil
		}
		return models.Integration{}, "", err
	}
	return integration, refreshed.AccessToken, nil
}

// refreshLinearToken performs the actual /oauth/token POST under the per-org
// mutex. Re-reads the credential after acquiring the lock so two goroutines
// that both observed NeedsRefresh do not both consume the refresh token
// (Linear rotates refresh tokens; double-redemption fails the slower one).
//
// When skipFreshCheck is false (the proactive refresh path), a re-read
// inside the lock that finds an already-fresh token short-circuits without
// burning a refresh-token redemption — this is the deduplication that
// makes per-org locking actually save round trips.
//
// When skipFreshCheck is true (the post-401 ForceRefreshToken path), the
// fresh-check is bypassed: the caller just observed a 401, so whatever the
// row claims about expiry is wrong and a redundant POST is required.
//
// On a 4xx response that signals a revoked refresh token, this method:
//  1. Zeroes the RefreshToken field in the persisted credential so subsequent
//     calls fall through to the "no refresh token" branch instead of looping.
//  2. Calls MarkIntegrationUnauthorized so the UI shows the reconnect banner.
//  3. Returns ErrRefreshTokenRevoked.
//
// The pre-zero re-read is a deliberate cross-node race recovery: if a peer
// process refreshed between our NeedsRefresh check and our 4xx response,
// its rotated token is now in the row and we should use it instead of
// concluding "revoked".
func (s *Service) refreshLinearToken(ctx context.Context, orgID uuid.UUID, observed models.LinearConfig, skipFreshCheck bool) (models.LinearConfig, error) {
	if observed.RefreshToken == "" {
		s.observeRefreshOutcome(refreshOutcomeNoRefreshToken)
		return observed, ErrNoRefreshToken
	}
	if s.oauthClient.ClientID == "" || s.oauthClient.ClientSecret == "" {
		s.observeRefreshOutcome(refreshOutcomeOAuthMisconfigured)
		return observed, ErrOAuthClientNotConfigured
	}

	mu := s.orgRefreshMu(orgID)
	mu.Lock()
	defer mu.Unlock()

	// Re-read after acquiring the lock. A concurrent goroutine on this
	// process (or another node) may have already rotated the token. If so,
	// avoid burning a refresh-token redemption on a redundant call.
	_, latest, err := s.loadLinearCredential(ctx, orgID)
	if err != nil {
		return observed, err
	}
	if !skipFreshCheck && !latest.NeedsRefresh(refreshWindow) && latest.AccessToken != "" {
		// Another goroutine in this process refreshed under the lock;
		// not strictly a peer-node race (we never POSTed) but the
		// outcome is the same: we're using a token a peer rotated.
		s.observeRefreshOutcome(refreshOutcomeRotatedByPeer)
		return latest, nil
	}
	// Even on the force-refresh path (skipFreshCheck), if the re-read shows
	// a different access token than the caller observed, a peer already
	// rotated under the lock. POSTing our (now-stale) refresh token would
	// just consume Linear's freshly-issued refresh token and yield a third
	// rotation. Use the peer's token instead.
	if skipFreshCheck && latest.AccessToken != "" && latest.AccessToken != observed.AccessToken && !latest.IsExpired() {
		s.observeRefreshOutcome(refreshOutcomeRotatedByPeer)
		return latest, nil
	}
	if latest.RefreshToken == "" {
		// Another goroutine zeroed the refresh token (likely after detecting
		// revocation). Nothing useful we can do here.
		s.observeRefreshOutcome(refreshOutcomeNoRefreshToken)
		return latest, ErrNoRefreshToken
	}

	refreshed, postErr := s.postLinearRefresh(ctx, latest.RefreshToken)
	if postErr != nil {
		// invalid_client → operator misconfiguration. Bubble up as
		// ErrOAuthClientNotConfigured so callers get a single, clearer
		// failure mode regardless of whether the misconfiguration was
		// "client_secret env var unset" (caught by the up-front guard)
		// or "client_secret rotated upstream and our deploy is stale"
		// (caught here, only after a real /oauth/token exchange). The
		// row is left alone — the next refresh after a config fix will
		// just succeed.
		if errors.Is(postErr, errLinearRefreshClientMisconfigured) {
			s.logger.Error().
				Err(postErr).
				Str("org_id", orgID.String()).
				Msg("linear: /oauth/token rejected our client credentials; check LINEAR_OAUTH_CLIENT_ID / LINEAR_OAUTH_CLIENT_SECRET")
			s.observeRefreshOutcome(refreshOutcomeOAuthMisconfigured)
			return latest, ErrOAuthClientNotConfigured
		}
		// Cross-node race recovery: if Linear rejected the *user's*
		// refresh token specifically (invalid_grant), re-check the row
		// in case a peer node already rotated it. A 4xx here is
		// suspicious but not authoritative — only after re-read confirms
		// no fresh token landed do we conclude the user must reconnect.
		if errors.Is(postErr, errLinearRefreshRejected) {
			if _, raceLatest, rerr := s.loadLinearCredential(ctx, orgID); rerr == nil {
				if raceLatest.AccessToken != "" && !raceLatest.IsExpired() && raceLatest.AccessToken != latest.AccessToken {
					s.logger.Info().
						Str("org_id", orgID.String()).
						Msg("linear: refresh raced with another node; using newly-persisted token")
					s.observeRefreshOutcome(refreshOutcomeRotatedByPeer)
					return raceLatest, nil
				}
			}
			// Confirmed revocation. Persist a config with the refresh token
			// zeroed so future GetValidToken calls don't loop on a doomed
			// refresh, and flip the integration to errored.
			s.markRefreshTokenRevoked(ctx, orgID, latest)
			s.observeRefreshOutcome(refreshOutcomeRevoked)
			return latest, ErrRefreshTokenRevoked
		}
		s.observeRefreshOutcome(refreshOutcomeTransientFailure)
		return latest, postErr
	}

	merged := mergeRefreshedConfig(latest, refreshed, s.logger, orgID)
	if s.credentialsWriter == nil {
		// No writer configured — expected in unit-test wiring that exercises
		// the refresh-decision logic without persistence. Production is
		// covered by a Build-time guarantee (BuildDeps.CredentialsWriter is
		// always set), so this branch should never execute in a real deploy.
		// Debug-level so test runs don't drown the log; if you ever see
		// this in production logs, the metric tag is refreshOutcomeRefreshed
		// (the refresh itself succeeded — just untracked).
		s.logger.Debug().Str("org_id", orgID.String()).Msg("linear: refresh succeeded but credentialsWriter is nil; refreshed token will not be persisted (test-only path)")
		s.observeRefreshOutcome(refreshOutcomeRefreshed)
		return merged, nil
	}
	casWriter, ok := s.credentialsWriter.(CredentialCASWriter)
	if !ok {
		s.logger.Error().
			Str("org_id", orgID.String()).
			Msg("linear: credentialsWriter does not support compare-and-swap refresh persistence")
		s.observeRefreshOutcome(refreshOutcomeTransientFailure)
		return latest, fmt.Errorf("persist refreshed linear credential: credentials writer does not support compare-and-swap")
	}
	current, updated, err := casWriter.UpdateLinearConfigIfRefreshTokenMatches(ctx, orgID, latest.RefreshToken, merged)
	if err != nil {
		// The refresh succeeded at Linear (the old refresh token has been
		// consumed and a new pair issued), but we couldn't persist. The
		// new refresh token is now lost — the next call will try to use
		// the old (already-consumed) one and fail with invalid_grant,
		// triggering revocation handling. Logged loudly so operators
		// chasing a "we keep getting revoked despite working refreshes"
		// support thread can land on this line.
		s.logger.Error().
			Err(err).
			Str("org_id", orgID.String()).
			Msg("linear: refresh succeeded at Linear but persisting the rotated tokens failed; the row's refresh token is now stale and will hit invalid_grant on next call")
		s.observeRefreshOutcome(refreshOutcomeTransientFailure)
		return latest, fmt.Errorf("persist refreshed linear credential: %w", err)
	}
	if !updated {
		if current.AccessToken != "" && !current.IsExpired() {
			s.logger.Info().
				Str("org_id", orgID.String()).
				Msg("linear: refresh raced with a newer credential write; using current token without overwriting it")
			s.observeRefreshOutcome(refreshOutcomeRotatedByPeer)
			return current, nil
		}
		s.observeRefreshOutcome(refreshOutcomeTransientFailure)
		return latest, fmt.Errorf("persist refreshed linear credential: stored refresh token changed before write")
	}

	// A successful refresh implies the integration is healthy again. Clear
	// any prior "Linear rejected the access token" banner left over from a
	// pre-reconnect 401. Best-effort; failures here are logged inside the
	// helper and never block the refresh.
	s.ClearIntegrationUnauthorized(ctx, orgID)

	// Per-refresh logging is intentionally Debug, not Info: at scale (one
	// refresh per org per ~hour) Info would dominate worker log volume
	// without telling operators anything they couldn't see in the metric.
	// The metric counter (linear.refresh_token.total{outcome="refreshed"})
	// is the primary observability surface for refresh health.
	s.logger.Debug().
		Str("org_id", orgID.String()).
		Bool("refresh_token_rotated", refreshed.RefreshToken != "" && refreshed.RefreshToken != latest.RefreshToken).
		Int("expires_in_seconds", refreshed.ExpiresIn).
		Time("expires_at", merged.ExpiresAt).
		Msg("linear: access token refreshed")

	s.observeRefreshOutcome(refreshOutcomeRefreshed)
	return merged, nil
}

// errLinearRefreshRejected is the sentinel returned by postLinearRefresh
// when Linear's /oauth/token responds with a 4xx that names the *refresh
// token itself* as the problem (i.e. invalid_grant). Lifted out of
// refreshLinearToken so the caller can distinguish it from generic
// transport errors and apply the cross-node-race recovery branch.
//
// Critically, this is NOT used for invalid_client — that signals a
// misconfigured LINEAR_OAUTH_CLIENT_SECRET, and treating it as
// "refresh token revoked" would zero every org's refresh token after a
// rotated-secret deploy, producing self-inflicted mass revocation.
var errLinearRefreshRejected = errors.New("linear refresh rejected by server")

// errLinearRefreshClientMisconfigured is returned when Linear's /oauth/token
// reports invalid_client. This means our LINEAR_OAUTH_CLIENT_ID /
// LINEAR_OAUTH_CLIENT_SECRET are wrong (rotated upstream, typo in the
// secrets file, etc.), not that the user's refresh token is bad. The
// caller must NOT zero the row's refresh token; the operator fixes the
// env var, the next refresh succeeds, and no user has to reconnect.
var errLinearRefreshClientMisconfigured = errors.New("linear oauth client_id/client_secret rejected by server")

// linearOAuthErrorBody is the standard OAuth 2.0 error response shape
// (RFC 6749 §5.2). Linear conforms to this; we parse it to disambiguate
// "your refresh token is revoked" (invalid_grant) from "your client
// credentials are wrong" (invalid_client) and a few others. Unparseable
// bodies fall through to a generic "rejected" classification — the safe
// default that does NOT zero the refresh token.
type linearOAuthErrorBody struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description,omitempty"`
}

// classifyOAuthErrorBody parses an OAuth error response and returns the
// sentinel that best describes the cause. Returns nil if the body is empty
// or cannot be parsed (caller falls back to a non-revocation generic
// error — the safe default).
//
// Per RFC 6749 §5.2 the relevant codes for refresh-token grants are:
//
//   - invalid_grant: refresh token expired/revoked/reused → revocation.
//   - invalid_client: client auth failed → server-side misconfiguration.
//   - invalid_request, unsupported_grant_type, invalid_scope: protocol
//     errors that indicate a bug on our side, not a revoked token. We
//     surface these as generic errors, not revocation.
//   - unauthorized_client: client not allowed to use refresh_token grant.
//     Same shape as invalid_client — the user's token isn't to blame.
func classifyOAuthErrorBody(body []byte) error {
	var parsed linearOAuthErrorBody
	if len(body) == 0 || json.Unmarshal(body, &parsed) != nil || parsed.Error == "" {
		return nil
	}
	switch parsed.Error {
	case "invalid_grant":
		return errLinearRefreshRejected
	case "invalid_client", "unauthorized_client":
		return errLinearRefreshClientMisconfigured
	}
	return nil
}

// postLinearRefresh issues the /oauth/token POST. Returns the parsed response
// on success; errLinearRefreshRejected (wrapped) on a 4xx that signals a
// revoked refresh token; or a generic error for transport / 5xx failures.
//
// Linear treats refresh requests as confidential-client OAuth, so client_id
// and client_secret are sent in the request body alongside the refresh
// token. Form encoding mirrors the authorization-code exchange in
// handlers/integrations.go.
func (s *Service) postLinearRefresh(ctx context.Context, refreshToken string) (linearTokenRefreshResponse, error) {
	data := url.Values{
		"grant_type":    {refreshGrantType},
		"refresh_token": {refreshToken},
		"client_id":     {s.oauthClient.ClientID},
		"client_secret": {s.oauthClient.ClientSecret},
	}

	reqCtx, cancel := context.WithTimeout(ctx, refreshHTTPTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, linearTokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return linearTokenRefreshResponse{}, fmt.Errorf("create linear refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	httpClient := s.refreshHTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return linearTokenRefreshResponse{}, fmt.Errorf("linear refresh request: %w", err)
	}
	defer resp.Body.Close()

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, refreshErrorBodyLimit))
	if readErr != nil {
		return linearTokenRefreshResponse{}, fmt.Errorf("read linear refresh response: %w", readErr)
	}

	switch {
	case resp.StatusCode == http.StatusOK:
		// fall through to parse
	case resp.StatusCode == http.StatusUnauthorized,
		resp.StatusCode == http.StatusBadRequest,
		resp.StatusCode == http.StatusForbidden:
		// 4xx from /oauth/token has multiple distinct causes per RFC 6749 §5.2.
		// We MUST distinguish them: a `invalid_grant` revocation requires
		// zeroing the user's refresh token, while `invalid_client` means
		// the operator botched LINEAR_OAUTH_CLIENT_SECRET and zeroing the
		// row would be self-inflicted mass revocation (one bad deploy
		// would force every connected org to manually reconnect).
		//
		// Unrecognized error codes fall through to the generic 4xx bucket
		// that does NOT zero the row — safer to leave the refresh token
		// in place for the next attempt than to invalidate it on an
		// ambiguous response.
		if classified := classifyOAuthErrorBody(body); classified != nil {
			return linearTokenRefreshResponse{}, fmt.Errorf("%w (status %d): %s", classified, resp.StatusCode, truncateErrorMessage(string(body)))
		}
		return linearTokenRefreshResponse{}, fmt.Errorf("linear refresh rejected with unrecognized error (status %d): %s", resp.StatusCode, truncateErrorMessage(string(body)))
	default:
		return linearTokenRefreshResponse{}, fmt.Errorf("linear refresh failed (status %d): %s", resp.StatusCode, truncateErrorMessage(string(body)))
	}

	var parsed linearTokenRefreshResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return linearTokenRefreshResponse{}, fmt.Errorf("parse linear refresh response: %w", err)
	}
	if parsed.AccessToken == "" {
		return linearTokenRefreshResponse{}, fmt.Errorf("linear refresh response missing access_token")
	}
	return parsed, nil
}

// mergeRefreshedConfig produces the LinearConfig to persist after a
// successful refresh. It preserves the bookkeeping fields (workspace
// metadata, webhook secret) and only overwrites the auth fields the refresh
// response speaks to. This is important because Linear's refresh response
// does not echo workspace_id / workspace_name back, and overwriting them
// with empty strings would corrupt the row.
//
// Linear may rotate the refresh token: if the response includes a new one
// we persist it. If the response omits refresh_token (some OAuth providers
// do this when the token is unchanged), we keep the existing one.
//
// expires_in handling: a successful refresh response without expires_in is
// against the spirit of RFC 6749 §5.1 but technically allowed. Earlier
// versions zeroed ExpiresAt on this path, which silently demoted the row
// to "use until 401" mode and disabled future proactive refreshes — a
// successful refresh that effectively turned refresh-on-expiry off forever.
// We now preserve the prior ExpiresAt instead. This means we'll re-refresh
// at the same window we would have otherwise; in the worst case (response
// systemically lacks expires_in) we end up refreshing more often than
// strictly necessary, which is far less harmful than silently going stale.
// A warning is logged so operators chasing "why does this org keep
// refreshing every minute?" can find this branch.
func mergeRefreshedConfig(prior models.LinearConfig, refreshed linearTokenRefreshResponse, logger zerolog.Logger, orgID uuid.UUID) models.LinearConfig {
	merged := prior
	merged.AccessToken = refreshed.AccessToken
	if refreshed.RefreshToken != "" {
		merged.RefreshToken = refreshed.RefreshToken
	}
	if refreshed.TokenType != "" {
		merged.TokenType = refreshed.TokenType
	}
	if refreshed.Scope != "" {
		merged.Scope = refreshed.Scope
	}
	if refreshed.ExpiresIn > 0 {
		merged.ExpiresAt = time.Now().Add(time.Duration(refreshed.ExpiresIn) * time.Second)
	} else {
		// Preserve the prior ExpiresAt (or zero, if it was a legacy install
		// being upgraded mid-flight). See doc above.
		logger.Warn().
			Str("org_id", orgID.String()).
			Time("preserved_expires_at", merged.ExpiresAt).
			Msg("linear: refresh response omitted expires_in; preserving prior ExpiresAt rather than demoting row to use-until-401 mode")
	}
	return merged
}

// ForceRefreshToken rotates the access token regardless of whether the
// cached one is within the refresh window. Used as the second line of
// defense after a 401 from a Linear API call: if our proactively-refreshed
// token was still rejected, the row is likely stale because another node
// rotated the refresh token (race) or the token aged out faster than its
// stated expiry (clock skew, server-side rotation policy change). A force
// refresh re-reads the row, then either returns a freshly-rotated token or
// surfaces ErrRefreshTokenRevoked when there really is no recovery.
//
// Returns ("", error) when refresh is impossible (no refresh token,
// missing OAuth client config, or upstream revocation). Callers should
// treat any error here as "let the original 401 propagate" rather than
// hiding it.
func (s *Service) ForceRefreshToken(ctx context.Context, orgID uuid.UUID) (string, error) {
	_, cfg, err := s.loadLinearCredential(ctx, orgID)
	if err != nil {
		return "", err
	}
	refreshed, err := s.refreshLinearToken(ctx, orgID, cfg, true /* skipFreshCheck */)
	if err != nil {
		return "", err
	}
	return refreshed.AccessToken, nil
}

// withRefreshableClient runs fn against a freshly-built Linear client and,
// on ErrUnauthorized, rebuilds the client from a force-refreshed token and
// retries fn exactly once. This is the canonical pattern for read-only
// service-layer entry points that hit Linear: the proactive refresh in
// GetValidToken handles the common case, and this retry handles the
// remainder (cross-node refresh-token races, clock skew, mid-chain
// expiry).
//
// MUTATIONS MUST NOT USE THIS WRAPPER. Linear's GraphQL mutations are not
// uniformly idempotent — e.g. CreateComment without a prior commentID
// produces a fresh comment on each call. A retry after a partial failure
// could append duplicate comments or attachments. The mutation paths in
// writes.go retain their existing "let the worker handler catch
// ErrUnauthorized and call MarkIntegrationUnauthorized" contract.
//
// Concurrency note: the retry path takes the same per-org refresh mutex
// inside refreshLinearToken, so concurrent reads racing on the same
// expiring token serialize through one refresh rather than fanning out
// N refresh requests at Linear.
func (s *Service) withRefreshableClient(ctx context.Context, orgID uuid.UUID, fn func(Client) error) error {
	_, token, err := s.GetValidToken(ctx, orgID)
	if err != nil {
		return err
	}

	client, err := s.clientFactory(ctx, token)
	if err != nil {
		return fmt.Errorf("build linear client: %w", err)
	}

	firstErr := fn(client)
	if firstErr == nil || !errors.Is(firstErr, ErrUnauthorized) {
		return firstErr
	}

	// First call hit a 401 despite our proactive refresh. Try a forced
	// refresh once. If it fails, return the original 401 — that error is
	// the more actionable one for callers (it triggers the existing
	// MarkIntegrationUnauthorized path in worker handlers), and the
	// refresh error would mask it.
	s.observeRefreshOutcome(refreshOutcomeForceRefresh)
	refreshedToken, refreshErr := s.ForceRefreshToken(ctx, orgID)
	if refreshErr != nil {
		s.logger.Debug().
			Err(refreshErr).
			Str("org_id", orgID.String()).
			Msg("linear: force refresh after 401 failed; surfacing original ErrUnauthorized")
		return firstErr
	}

	retryClient, err := s.clientFactory(ctx, refreshedToken)
	if err != nil {
		return fmt.Errorf("build linear client (post-refresh): %w", err)
	}
	if retryErr := fn(retryClient); retryErr != nil {
		if errors.Is(retryErr, ErrUnauthorized) {
			s.logger.Warn().
				Str("org_id", orgID.String()).
				Msg("linear: 401 persisted after force-refresh; access token genuinely revoked")
		}
		return retryErr
	}
	s.logger.Info().
		Str("org_id", orgID.String()).
		Msg("linear: successfully recovered from 401 via force-refresh + retry")
	return nil
}

// markRefreshTokenRevoked persists a config with the refresh token zeroed
// and flips the integration row to errored. Splitting the two writes is
// deliberate: the credential write removes our ability to keep retrying a
// dead refresh token (subsequent GetValidToken sees RefreshToken=="" and
// returns ErrNoRefreshToken cleanly), while MarkIntegrationUnauthorized
// surfaces the human-readable banner. Both are best-effort: a failure on
// either side logs and continues so the caller's original error contract
// (ErrRefreshTokenRevoked) isn't perturbed by a side-channel write hiccup.
func (s *Service) markRefreshTokenRevoked(ctx context.Context, orgID uuid.UUID, current models.LinearConfig) {
	if s.credentialsWriter != nil {
		zeroed := current
		zeroed.RefreshToken = ""
		if zeroed.ExpiresAt.IsZero() {
			// A revoked row with zero ExpiresAt would be misclassified as
			// a legacy "use until 401" credential by NeedsRefresh, masking
			// the revocation. Stamp a past expiry so the typed-revocation
			// signal — RefreshToken="" + ExpiresAt!=0 — survives the next
			// GetValidToken call and routes through ErrNoRefreshToken.
			// The integration row's status (set by MarkIntegrationUnauthorized
			// below) is what the UI surfaces; ExpiresAt here is purely
			// internal classification.
			zeroed.ExpiresAt = time.Now().Add(-time.Second)
		}
		// AccessToken is left in place so worker handlers that hold an
		// in-memory client built from this config still surface the
		// more-specific 401 path rather than failing earlier with
		// "empty access token".
		if err := s.credentialsWriter.Upsert(ctx, orgID, zeroed); err != nil {
			s.logger.Warn().Err(err).
				Str("org_id", orgID.String()).
				Msg("linear: failed to zero refresh_token after revocation; subsequent calls may loop on dead refresh token until next reconnect")
		}
	}
	s.MarkIntegrationUnauthorized(ctx, orgID)
}

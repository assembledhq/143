package pagerduty

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
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/models"
)

// pagerDutyTokenURL is the OAuth token endpoint used for refresh-token rotation.
// It mirrors the constant used by the authorization-code exchange in the API
// handlers; kept in sync so a future region/staging override threads through
// both sites.
const pagerDutyTokenURL = "https://identity.pagerduty.com/oauth/token" // #nosec G101 -- OAuth endpoint URL, not credentials

// refreshGrantType is the OAuth 2.0 grant type for token refresh.
const refreshGrantType = "refresh_token"

// pagerDutyRefreshWindow is how far before expiry we proactively refresh. It is
// generous relative to a sandbox turn so a multi-minute agent run does not cross
// the access-token expiry boundary mid-call.
const pagerDutyRefreshWindow = 5 * time.Minute

// pagerDutyRefreshHTTPTimeout caps a single refresh round trip so a wedged token
// endpoint cannot hold the per-credential mutex (and stall every concurrent
// caller) indefinitely.
const pagerDutyRefreshHTTPTimeout = 10 * time.Second

// pagerDutyRefreshErrorBodyLimit bounds how much of an error response we read.
const pagerDutyRefreshErrorBodyLimit = 8192

// ErrPagerDutyNoRefreshToken is returned when a near-expiry credential has no
// refresh token (legacy/classic install). Not recoverable without a reconnect.
var ErrPagerDutyNoRefreshToken = errors.New("pagerduty credential has no refresh token; reconnect required")

// ErrPagerDutyOAuthClientNotConfigured is returned when refresh is needed but the
// service was built without OAuth client credentials. Operator misconfiguration.
var ErrPagerDutyOAuthClientNotConfigured = errors.New("pagerduty oauth client not configured; cannot refresh token")

// ErrPagerDutyRefreshTokenRevoked is returned when PagerDuty rejects our refresh
// token (invalid_grant). The caller should surface a reconnect prompt.
var ErrPagerDutyRefreshTokenRevoked = errors.New("pagerduty refresh token revoked; reconnect required")

// pagerDutyCredentialCASWriter persists a refreshed credential by id only when
// the stored refresh token still matches the one we redeemed. Implemented by
// *db.OrgCredentialStore.
type pagerDutyCredentialCASWriter interface {
	UpdatePagerDutyConfigByIDIfRefreshTokenMatches(ctx context.Context, orgID, credentialID uuid.UUID, expectedRefreshToken string, cfg models.PagerDutyConfig) (models.PagerDutyConfig, bool, error)
}

// pagerDutyCredentialByIDReader reads a credential by id so the refresh path can
// re-read under the per-credential lock for cross-process race recovery.
type pagerDutyCredentialByIDReader interface {
	GetByID(ctx context.Context, orgID, id uuid.UUID) (*models.DecryptedCredential, error)
}

// PagerDutyOAuthClientCreds holds the confidential-client credentials PagerDuty
// requires to redeem a refresh token.
type PagerDutyOAuthClientCreds struct {
	ClientID     string
	ClientSecret string
}

// TokenService rotates near-expiry PagerDuty OAuth access tokens. It is
// process-local single-flighted per credential id; cross-process races are
// detected via the compare-and-swap writer and a post-failure re-read.
type TokenService struct {
	credentials pagerDutyCredentialByIDReader
	writer      pagerDutyCredentialCASWriter
	oauth       PagerDutyOAuthClientCreds
	httpClient  *http.Client
	tokenURL    string
	logger      zerolog.Logger

	// credLock holds one mutex per credential id to single-flight refreshes.
	// A sync.Map (rather than a map+mutex) keeps lookups lock-free on the hot
	// path; entries are not evicted, but the key space is bounded by the small
	// number of distinct PagerDuty credentials an org holds.
	credLock sync.Map // map[uuid.UUID]*sync.Mutex
}

// NewTokenService builds a PagerDuty token refresh service.
func NewTokenService(credentials pagerDutyCredentialByIDReader, writer pagerDutyCredentialCASWriter, oauth PagerDutyOAuthClientCreds, logger zerolog.Logger) *TokenService {
	return &TokenService{
		credentials: credentials,
		writer:      writer,
		oauth:       oauth,
		httpClient:  &http.Client{Timeout: pagerDutyRefreshHTTPTimeout},
		tokenURL:    pagerDutyTokenURL,
		logger:      logger,
	}
}

func (s *TokenService) lockFor(credentialID uuid.UUID) *sync.Mutex {
	lock, _ := s.credLock.LoadOrStore(credentialID, &sync.Mutex{})
	return lock.(*sync.Mutex)
}

// EnsureFresh returns a PagerDuty config whose access token is valid now. When
// the observed token is within the refresh window and a refresh token is
// available, it rotates the token at PagerDuty and persists the new pair. On a
// transient refresh failure it falls back to the observed token if it is still
// inside its validity window. It is a no-op (returns observed unchanged) for
// tokens that do not need refresh or for legacy rows without an expiry.
func (s *TokenService) EnsureFresh(ctx context.Context, orgID, credentialID uuid.UUID, observed models.PagerDutyConfig) (models.PagerDutyConfig, error) {
	if s == nil {
		return observed, nil
	}
	if !observed.NeedsRefresh(pagerDutyRefreshWindow) {
		return observed, nil
	}
	if observed.RefreshToken == "" {
		// Near expiry but unrecoverable without user reconnect. Return the
		// observed token (the caller may still get one last use out of it)
		// rather than failing the whole sandbox start.
		s.logger.Warn().Str("org_id", orgID.String()).Msg("pagerduty: token near expiry but no refresh token; reconnect required")
		return observed, nil
	}
	if s.oauth.ClientID == "" || s.oauth.ClientSecret == "" {
		s.logger.Warn().Str("org_id", orgID.String()).Msg("pagerduty: token near expiry but OAuth client not configured; cannot refresh")
		return observed, nil
	}

	lock := s.lockFor(credentialID)
	lock.Lock()
	defer lock.Unlock()

	// Re-read under the lock: a concurrent goroutine (or peer node) may have
	// already rotated the token, in which case we avoid burning a redemption.
	latest := observed
	if cred, err := s.credentials.GetByID(ctx, orgID, credentialID); err == nil && cred != nil {
		if cfg, ok := cred.Config.(models.PagerDutyConfig); ok {
			latest = cfg
			if !cfg.NeedsRefresh(pagerDutyRefreshWindow) && cfg.AccessToken != "" {
				return cfg, nil
			}
		}
	}
	if latest.RefreshToken == "" {
		return latest, nil
	}

	refreshed, err := s.post(ctx, latest.RefreshToken)
	if err != nil {
		// Cross-process race recovery: on rejection, re-read in case a peer
		// already rotated the token, and use theirs if it is fresh.
		if errors.Is(err, ErrPagerDutyRefreshTokenRevoked) {
			if cred, rerr := s.credentials.GetByID(ctx, orgID, credentialID); rerr == nil && cred != nil {
				if cfg, ok := cred.Config.(models.PagerDutyConfig); ok && cfg.AccessToken != "" && !cfg.IsExpired() && cfg.AccessToken != latest.AccessToken {
					return cfg, nil
				}
			}
			s.logger.Warn().Err(err).Str("org_id", orgID.String()).Msg("pagerduty: refresh token revoked; reconnect required")
			return latest, err
		}
		// Transient failure: fall back to the observed token if it is still
		// usable, so a network blip doesn't break the sandbox.
		if !latest.IsExpired() && latest.AccessToken != "" {
			s.logger.Warn().Err(err).Str("org_id", orgID.String()).Msg("pagerduty: token refresh failed; using cached token still inside validity window")
			return latest, nil
		}
		return latest, err
	}

	merged := mergeRefreshedPagerDutyConfig(latest, refreshed)
	if s.writer == nil {
		return merged, nil
	}
	current, updated, perr := s.writer.UpdatePagerDutyConfigByIDIfRefreshTokenMatches(ctx, orgID, credentialID, latest.RefreshToken, merged)
	if perr != nil {
		s.logger.Error().Err(perr).Str("org_id", orgID.String()).Msg("pagerduty: refresh succeeded but persisting rotated tokens failed; row's refresh token is now stale")
		return merged, nil
	}
	if !updated {
		if current.AccessToken != "" && !current.IsExpired() {
			return current, nil
		}
		return merged, nil
	}
	s.logger.Debug().Str("org_id", orgID.String()).Time("expires_at", merged.ExpiresAt).Msg("pagerduty: access token refreshed")
	return merged, nil
}

func (s *TokenService) post(ctx context.Context, refreshToken string) (pagerDutyOAuthTokenResponse, error) {
	data := url.Values{
		"grant_type":    {refreshGrantType},
		"refresh_token": {refreshToken},
		"client_id":     {s.oauth.ClientID},
		"client_secret": {s.oauth.ClientSecret},
	}
	reqCtx, cancel := context.WithTimeout(ctx, pagerDutyRefreshHTTPTimeout)
	defer cancel()

	endpoint := s.tokenURL
	if endpoint == "" {
		endpoint = pagerDutyTokenURL
	}
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, strings.NewReader(data.Encode()))
	if err != nil {
		return pagerDutyOAuthTokenResponse{}, fmt.Errorf("create pagerduty refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	client := s.httpClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return pagerDutyOAuthTokenResponse{}, fmt.Errorf("pagerduty refresh request: %w", err)
	}
	defer resp.Body.Close()

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, pagerDutyRefreshErrorBodyLimit))
	if readErr != nil {
		return pagerDutyOAuthTokenResponse{}, fmt.Errorf("read pagerduty refresh response: %w", readErr)
	}
	switch {
	case resp.StatusCode == http.StatusOK:
		// parse below
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusForbidden:
		// A 4xx naming the grant (invalid_grant) means the refresh token is
		// dead. Other 4xx (e.g. invalid_client) are operator misconfig and we
		// must NOT treat them as revocation (that would force mass reconnects).
		if isInvalidGrant(body) {
			return pagerDutyOAuthTokenResponse{}, fmt.Errorf("%w (status %d)", ErrPagerDutyRefreshTokenRevoked, resp.StatusCode)
		}
		return pagerDutyOAuthTokenResponse{}, fmt.Errorf("pagerduty refresh rejected (status %d)", resp.StatusCode)
	default:
		return pagerDutyOAuthTokenResponse{}, fmt.Errorf("pagerduty refresh failed (status %d)", resp.StatusCode)
	}

	var parsed pagerDutyOAuthTokenResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return pagerDutyOAuthTokenResponse{}, fmt.Errorf("parse pagerduty refresh response: %w", err)
	}
	if parsed.AccessToken == "" {
		return pagerDutyOAuthTokenResponse{}, fmt.Errorf("pagerduty refresh response missing access_token")
	}
	return parsed, nil
}

type pagerDutyOAuthTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
	ExpiresIn    int    `json:"expires_in"`
}

type oauthErrorBody struct {
	Error string `json:"error"`
}

func isInvalidGrant(body []byte) bool {
	var parsed oauthErrorBody
	if len(body) == 0 || json.Unmarshal(body, &parsed) != nil {
		return false
	}
	return parsed.Error == "invalid_grant"
}

// mergeRefreshedPagerDutyConfig overlays the refresh response onto the prior
// config, preserving bookkeeping fields (region, account, webhook secret) that
// the token endpoint does not echo back.
func mergeRefreshedPagerDutyConfig(prior models.PagerDutyConfig, refreshed pagerDutyOAuthTokenResponse) models.PagerDutyConfig {
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
	}
	return merged
}

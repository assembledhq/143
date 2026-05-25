// Package claudecodeauth implements the Anthropic Claude Code CLI subscription
// OAuth flow. Users connect a Claude Pro / Max / Team / Enterprise subscription
// via the authorization-code + PKCE (S256) flow, and the resulting tokens are
// written into each sandbox container so the Claude Code CLI authenticates
// against the subscription backend instead of the ANTHROPIC_API_KEY path.
//
// Flow shape:
//  1. InitiateOAuth generates a PKCE verifier + state, stores them on a
//     pending_auth row, and returns an authorize URL for the user to open
//     in a browser.
//  2. Anthropic authenticates the user and shows them `<code>#<state>`.
//  3. CompleteOAuth accepts that paste, verifies the state, POSTs to the
//     Anthropic token endpoint with the verifier, and upgrades the row to
//     active.
//
// Design notes:
//   - Subscription credentials live under ProviderAnthropic with a non-empty
//     label. An Anthropic API-key credential (if any) uses label="" and is
//     stored in the same provider; the two are mutually exclusive per row, so
//     one org can hold an API key alongside N labeled subscriptions.
//   - Round-robin selection uses ClaimNextLabeledRoundRobin which filters
//     `label != ”`, so the API-key row is never claimed for subscription use.
//   - The OAuth endpoints, client ID, and redirect URI below are Anthropic's
//     public Claude Code CLI values. If Anthropic changes these, update
//     only these constants — the service shape is stable.
package claudecodeauth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
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

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

const (
	// defaultAuthorizeURL is where we send the user's browser for login.
	defaultAuthorizeURL = "https://claude.com/cai/oauth/authorize"

	// defaultTokenURL is the Anthropic PKCE token endpoint used for both the
	// initial authorization_code exchange and subsequent refresh_token calls.
	defaultTokenURL = "https://platform.claude.com/v1/oauth/token" // #nosec G101 -- public OAuth endpoint URL, not a credential

	// defaultRedirectURI is the Anthropic-hosted callback the CLI uses; the
	// browser lands here after login and Anthropic renders `<code>#<state>`
	// for the user to paste back.
	defaultRedirectURI = "https://platform.claude.com/oauth/code/callback"

	// defaultClientID is the Claude Code CLI's public OAuth client_id.
	defaultClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"

	// defaultProfileURL is the Anthropic profile endpoint we query after a
	// token exchange to learn the subscription tier (account_type) and rate
	// limit tier. Best-effort: a failure here never fails the auth flow.
	defaultProfileURL = "https://api.anthropic.com/api/oauth/profile"

	// refreshGrantType is the OAuth 2.0 grant type for token refresh.
	refreshGrantType = "refresh_token"

	// refreshWindow is how far before expiry we proactively refresh.
	refreshWindow = 5 * time.Minute

	// verifierBytes is the entropy used for both the PKCE code_verifier
	// and the anti-CSRF state parameter. 32 bytes → 43 base64url chars,
	// comfortably above the 43-char minimum RFC 7636 requires.
	verifierBytes = 32

	// anthropicAPIVersion pins the Anthropic API version header sent on
	// requests to api.anthropic.com. The profile endpoint requires it, and
	// pinning a specific version avoids accidental breakage when the API
	// default rolls forward.
	anthropicAPIVersion = "2023-06-01"

	// maxLoggedBodyBytes caps how many bytes of an upstream error body we
	// embed into returned errors / logs. Upstream responses are assumed
	// trusted but not vetted, so we truncate to bound log size and avoid
	// accidentally echoing large or unexpected payloads.
	maxLoggedBodyBytes = 256

	// maxResponseBytes bounds how much data we read from upstream HTTP
	// responses (token endpoint, profile endpoint). Defense-in-depth against
	// an unexpected large payload from a trusted but unvetted upstream.
	maxResponseBytes = 1 << 20 // 1 MiB

	// pendingAuthTTL bounds how long a pending_auth row may live before
	// CompleteOAuth refuses to honor it. The CSRF state and PKCE verifier
	// should only be valid for the duration of a fresh login flow; a paste
	// that arrives hours later almost certainly means the user abandoned
	// the flow (or the row was resurrected by a replay attempt), so we
	// force them to re-initiate and get a fresh verifier + state.
	pendingAuthTTL = 10 * time.Minute
)

// defaultScopes is the minimum set of OAuth scopes the Claude Code CLI
// requires. Without user:sessions:claude_code the issued token cannot reach
// the Claude Code inference backend.
var defaultScopes = []string{
	"user:profile",
	"user:inference",
	"user:sessions:claude_code",
	"user:mcp_servers",
	"user:file_upload",
}

// CredentialStore defines the credential operations needed by the auth
// service. Every method takes models.Scope so a single Service instance can
// drive both org-scoped (admin) and personal-scoped (per-user) subscription
// flows. Production wires *db.ScopedCredentialStore.
//
// Subscription credentials live under ProviderAnthropic with a non-empty
// label. An Anthropic API-key credential uses label="" and shares the same
// provider; the two are mutually exclusive per row, so one scope can hold
// an API key alongside N labeled subscriptions.
type CredentialStore interface {
	UpsertWithLabel(ctx context.Context, scope models.Scope, createdBy *uuid.UUID, label string, cfg models.ProviderConfig) (*uuid.UUID, error)
	InsertPendingAuth(ctx context.Context, scope models.Scope, createdBy *uuid.UUID, label string, cfg models.ProviderConfig) (*uuid.UUID, error)
	GetByID(ctx context.Context, scope models.Scope, id uuid.UUID) (*models.DecryptedCredential, error)
	GetByProviderAndLabel(ctx context.Context, scope models.Scope, provider models.ProviderName, label string) (*models.DecryptedCredential, error)
	ListByProvider(ctx context.Context, scope models.Scope, provider models.ProviderName) ([]models.DecryptedCredential, error)
	ClaimNextLabeledRoundRobin(ctx context.Context, scope models.Scope, provider models.ProviderName) (*models.DecryptedCredential, error)
	DisableByID(ctx context.Context, scope models.Scope, id uuid.UUID) error
	UpdateStatusByID(ctx context.Context, scope models.Scope, id uuid.UUID, status models.CodingCredentialRowStatus) error
	UpsertByID(ctx context.Context, scope models.Scope, id uuid.UUID, cfg models.ProviderConfig) error
	ExistsForProviderByID(ctx context.Context, scope models.Scope, id uuid.UUID, provider models.ProviderName) (bool, error)
	// HasActiveLabeled reports whether at least one active labeled credential
	// exists for (scope, provider). Backs HasActiveSubscription with a cheap
	// EXISTS probe instead of listing every row.
	HasActiveLabeled(ctx context.Context, scope models.Scope, provider models.ProviderName) (bool, error)
	// DisableLabeled disables all subscription rows (label != '') at (scope,
	// provider) while leaving the API-key row (label='') intact. Used by
	// DisconnectAll so the caller doesn't lose their Anthropic API key when
	// disconnecting every Claude subscription.
	DisableLabeled(ctx context.Context, scope models.Scope, provider models.ProviderName) error
}

// ErrCredentialNotFound is returned when no credential exists for the given org/provider.
var ErrCredentialNotFound = fmt.Errorf("credential not found")

// ErrPendingAuthNotFound is returned when CompleteOAuth is called without a
// prior InitiateOAuth for the (org, label).
var ErrPendingAuthNotFound = fmt.Errorf("no pending auth flow — initiate first")

// ErrPendingAuthExpired is returned when CompleteOAuth finds a pending_auth
// row that is older than pendingAuthTTL. The user must re-initiate to get
// a fresh PKCE verifier and CSRF state before pasting again.
var ErrPendingAuthExpired = fmt.Errorf("pending auth flow expired — initiate again")

// ErrInvalidPaste is returned when the user's pasted code#state string is
// malformed or the state doesn't match the one we stored.
var ErrInvalidPaste = fmt.Errorf("pasted code is invalid or expired")

// InitiateResponse is returned by the /initiate endpoint. The caller hands
// AuthorizeURL to the user's browser; State is echoed back to the UI so the
// modal can verify the eventual paste matches this session. Label is not
// echoed back — the caller already owns that value and adding it here would
// be a no-op round trip.
type InitiateResponse struct {
	AuthorizeURL string `json:"authorize_url"`
	State        string `json:"state"`
}

// CompleteResponse is returned by the /complete endpoint once the auth code
// has been exchanged for tokens.
type CompleteResponse struct {
	AccountType string `json:"account_type,omitempty"`
}

// SubscriptionStatus is the public-facing status of a Claude Code subscription.
type SubscriptionStatus string

const (
	SubscriptionStatusActive      SubscriptionStatus = "active"
	SubscriptionStatusPendingAuth SubscriptionStatus = "pending_auth"
	SubscriptionStatusInvalid     SubscriptionStatus = "invalid"
	SubscriptionStatusDisabled    SubscriptionStatus = "disabled"
)

// SubscriptionInfo is the API-safe summary of a connected Claude subscription.
// Mirrors codexauth.SubscriptionInfo — omits all token material.
type SubscriptionInfo struct {
	ID          uuid.UUID          `json:"id"`
	Label       string             `json:"label"`
	AccountType string             `json:"account_type,omitempty"`
	Status      SubscriptionStatus `json:"status"`
	LastUsedAt  *time.Time         `json:"last_used_at,omitempty"`
	CreatedBy   *uuid.UUID         `json:"created_by,omitempty"`
	CreatedAt   time.Time          `json:"created_at,omitempty"`
}

// Service handles the Claude Code CLI subscription OAuth flow.
type Service struct {
	credentials  CredentialStore
	httpClient   *http.Client
	logger       zerolog.Logger
	authorizeURL string
	tokenURL     string
	profileURL   string
	redirectURI  string
	clientID     string
	scopes       []string
	// refreshMu entries intentionally persist across successful refreshes:
	// deleting a per-credential mutex after Unlock can race with concurrent
	// waiters that already loaded the old pointer and let a later caller create
	// a second mutex for the same credential. Growth is therefore bounded by the
	// number of credential IDs seen over the process lifetime, and entries are
	// reclaimed on disconnect/invalidation.
	refreshMu sync.Map // credential ID string -> *sync.Mutex
	initMu    sync.Map // pendingKey (orgID+label) -> *sync.Mutex
}

// NewService creates a new Claude Code subscription auth service.
func NewService(credentials CredentialStore, logger zerolog.Logger) *Service {
	scopes := make([]string, len(defaultScopes))
	copy(scopes, defaultScopes)
	return &Service{
		credentials:  credentials,
		httpClient:   &http.Client{Timeout: 10 * time.Second},
		logger:       logger,
		authorizeURL: defaultAuthorizeURL,
		tokenURL:     defaultTokenURL,
		profileURL:   defaultProfileURL,
		redirectURI:  defaultRedirectURI,
		clientID:     defaultClientID,
		scopes:       scopes,
	}
}

// SetHTTPClient replaces the default HTTP client (useful for testing).
func (s *Service) SetHTTPClient(client *http.Client) { s.httpClient = client }

// SetAuthorizeURL overrides the authorize endpoint (useful for testing).
func (s *Service) SetAuthorizeURL(u string) { s.authorizeURL = u }

// SetTokenURL overrides the token endpoint (useful for testing).
func (s *Service) SetTokenURL(u string) { s.tokenURL = u }

// SetProfileURL overrides the profile endpoint (useful for testing).
func (s *Service) SetProfileURL(u string) { s.profileURL = u }

// pendingKey returns the sync.Map key for init-side mutexes, namespaced by
// scope so personal and org flows for the same label never collide. The
// leading scope tag is part of the key so an org label like
// `u:<userID>:label` cannot impersonate a personal-scope key.
func pendingKey(scope models.Scope, label string) string {
	if scope.IsPersonal() {
		return "personal:" + scope.OrgID.String() + ":u:" + scope.UserID.String() + ":" + label
	}
	return "org:" + scope.OrgID.String() + ":" + label
}

// orgScope is a small constructor for org-only callers that haven't been
// updated to think in scopes yet (e.g. legacy GetValidToken path).
func orgScope(orgID uuid.UUID) models.Scope {
	return models.Scope{OrgID: orgID}
}

// randomURLSafe returns a cryptographically random base64url-encoded string
// with no padding, suitable for the PKCE code_verifier and OAuth state param.
func randomURLSafe() (string, error) {
	b := make([]byte, verifierBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// codeChallenge returns the PKCE S256 challenge for the given verifier:
// base64url(sha256(verifier)) with no padding.
func codeChallenge(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

// buildAuthorizeURL returns a fully-formed Claude authorize URL containing the
// PKCE challenge, state, redirect, and scopes.
func (s *Service) buildAuthorizeURL(challenge, state string) string {
	q := url.Values{}
	// code=true mirrors what the Claude Code CLI sends on its own authorize
	// request: it opts Anthropic's /cai/oauth/authorize into the "show the
	// user the <code>#<state> paste box" flow instead of doing a
	// traditional redirect to redirect_uri. Without it, Anthropic would
	// attempt a browser redirect to platform.claude.com/oauth/code/callback
	// and the user would never see the code they need to paste back here.
	q.Set("code", "true")
	q.Set("client_id", s.clientID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", s.redirectURI)
	q.Set("scope", strings.Join(s.scopes, " "))
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", state)
	return s.authorizeURL + "?" + q.Encode()
}

// InitiateOAuth starts a new PKCE auth flow at the given (scope, label).
// Generates a verifier + state, persists them on a pending_auth row, and
// returns the authorize URL for the caller to hand to the user's browser.
func (s *Service) InitiateOAuth(ctx context.Context, scope models.Scope, createdBy *uuid.UUID, label string) (*InitiateResponse, error) {
	// Serialize concurrent init calls for the same (scope, label) — otherwise
	// two racing initiations could both reach InsertPendingAuth, with the
	// slower request silently overwriting the faster one's verifier.
	pKey := pendingKey(scope, label)
	muVal, _ := s.initMu.LoadOrStore(pKey, &sync.Mutex{})
	mu := muVal.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()

	verifier, err := randomURLSafe()
	if err != nil {
		return nil, fmt.Errorf("generate verifier: %w", err)
	}
	state, err := randomURLSafe()
	if err != nil {
		return nil, fmt.Errorf("generate state: %w", err)
	}
	challenge := codeChallenge(verifier)
	authURL := s.buildAuthorizeURL(challenge, state)

	pendingCfg := models.AnthropicConfig{
		Subscription: &models.AnthropicSubscription{
			State:        state,
			CodeVerifier: verifier,
			AuthorizeURL: authURL,
		},
	}

	if s.credentials != nil {
		if _, err := s.credentials.InsertPendingAuth(ctx, scope, createdBy, label, pendingCfg); err != nil {
			return nil, fmt.Errorf("persist pending subscription: %w", err)
		}
	}

	s.logger.Info().
		Str("org_id", scope.OrgID.String()).
		Bool("personal", scope.IsPersonal()).
		Str("label", label).
		Msg("Claude Code OAuth initiated")

	return &InitiateResponse{
		AuthorizeURL: authURL,
		State:        state,
	}, nil
}

// CompleteOAuth exchanges the user's pasted "<code>#<state>" for tokens,
// verifies the returned state matches the one we stored, and upgrades the
// pending row to an active subscription.
func (s *Service) CompleteOAuth(ctx context.Context, scope models.Scope, label, codeAndState string) (*CompleteResponse, error) {
	pKey := pendingKey(scope, label)
	muVal, _ := s.initMu.LoadOrStore(pKey, &sync.Mutex{})
	mu := muVal.(*sync.Mutex)
	mu.Lock()
	// After Complete, the pending_auth row is either upgraded to active or
	// remains pending for another attempt; either way we drop the init-side
	// mutex so per-(org,label) entries don't persist for the lifetime of
	// the process. A racing Initiate that loaded mu before the Delete still
	// holds a valid pointer and serializes correctly; a later Initiate picks
	// up a fresh mutex via LoadOrStore, which is what we want.
	defer func() {
		mu.Unlock()
		s.initMu.Delete(pKey)
	}()

	code, returnedState, err := splitCodeAndState(codeAndState)
	if err != nil {
		return nil, err
	}

	if s.credentials == nil {
		return nil, fmt.Errorf("credential store not configured")
	}
	cred, err := s.credentials.GetByProviderAndLabel(ctx, scope, models.ProviderAnthropic, label)
	if err != nil {
		// Only "no row" should surface as ErrPendingAuthNotFound (→ 404).
		// Transient DB errors must bubble up as 500s so operators can see them.
		if isNotFoundError(err) {
			return nil, ErrPendingAuthNotFound
		}
		return nil, fmt.Errorf("lookup pending subscription: %w", err)
	}
	cfg, ok := cred.Config.(models.AnthropicConfig)
	if !ok || cfg.Subscription == nil {
		return nil, fmt.Errorf("pending row has unexpected config")
	}
	if cfg.Subscription.State == "" || cfg.Subscription.CodeVerifier == "" {
		return nil, ErrPendingAuthNotFound
	}
	// Only pending_auth rows should be completable. If the row is already
	// active or has been invalidated, fail closed rather than risking
	// overwriting live tokens with a replayed (and expired) code.
	if cred.Status != models.CredentialStatusPendingAuth {
		return nil, ErrPendingAuthNotFound
	}
	// Reject pastes that arrive after the TTL window. UpdatedAt moves
	// forward on every InsertPendingAuth upsert, so the window starts at
	// the most recent Initiate — users who re-click "start auth" still get
	// a fresh clock.
	if time.Since(cred.UpdatedAt) > pendingAuthTTL {
		return nil, ErrPendingAuthExpired
	}
	// Constant-time compare on the CSRF state to avoid leaking it via timing
	// side channels. ConstantTimeCompare also returns 0 for length mismatches.
	if subtle.ConstantTimeCompare([]byte(returnedState), []byte(cfg.Subscription.State)) != 1 {
		return nil, ErrInvalidPaste
	}

	tokens, err := s.exchangeAuthCode(ctx, code, returnedState, cfg.Subscription.CodeVerifier)
	if err != nil {
		return nil, err
	}

	sub := &models.AnthropicSubscription{
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		Scopes:       parseScopes(tokens.Scope),
	}
	if tokens.ExpiresIn > 0 {
		sub.ExpiresAt = time.Now().Add(time.Duration(tokens.ExpiresIn) * time.Second)
	}

	// Best-effort profile fetch to enrich the subscription with a
	// human-readable tier (AccountType) and the backend's rate_limit_tier.
	// These fields are display-only; a failure here must not block auth.
	if profile, perr := s.fetchProfile(ctx, sub.AccessToken); perr != nil {
		s.logger.Warn().Err(perr).Str("org_id", scope.OrgID.String()).Str("label", label).Msg("claude profile fetch failed; continuing without tier info")
	} else if profile != nil {
		sub.AccountType = profile.Organization.OrganizationType
		sub.RateLimitTier = profile.Organization.RateLimitTier
	}

	if err := s.credentials.UpsertByID(ctx, scope, cred.ID, models.AnthropicConfig{Subscription: sub}); err != nil {
		return nil, fmt.Errorf("store credential: %w", err)
	}

	s.logger.Info().
		Str("org_id", scope.OrgID.String()).
		Bool("personal", scope.IsPersonal()).
		Str("label", label).
		Bool("has_refresh_token", sub.RefreshToken != "").
		Int("expires_in", tokens.ExpiresIn).
		Msg("Claude Code OAuth completed")

	return &CompleteResponse{AccountType: sub.AccountType}, nil
}

// parseScopes splits a space-separated OAuth scope string into a slice.
// Empty or whitespace-only inputs return nil so we never store a []string{""}.
func parseScopes(raw string) []string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	return strings.Fields(trimmed)
}

// redactedBody bounds an upstream response body before we include it in an
// error or log line. Oversized bodies are truncated and tagged so operators
// can tell a body was cut. We don't attempt to strip token-shaped substrings
// — the token/profile response bodies shouldn't carry credentials we care
// about echoing, but the size cap keeps a misbehaving upstream from
// flooding logs with an unbounded payload.
func redactedBody(body []byte) string {
	if len(body) <= maxLoggedBodyBytes {
		return string(body)
	}
	return string(body[:maxLoggedBodyBytes]) + "…(truncated)"
}

// splitCodeAndState parses the "<code>#<state>" string Anthropic shows to the
// user. Whitespace is trimmed because users routinely paste with surrounding
// line breaks.
func splitCodeAndState(raw string) (string, string, error) {
	trimmed := strings.TrimSpace(raw)
	parts := strings.SplitN(trimmed, "#", 2)
	if len(parts) != 2 {
		return "", "", ErrInvalidPaste
	}
	code, state := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	if code == "" || state == "" {
		return "", "", ErrInvalidPaste
	}
	return code, state, nil
}

// tokenExchangeResponse holds the parsed tokens returned by the Anthropic
// token endpoint for both authorization_code and refresh_token grants.
// Anthropic's /v1/oauth/token response does not carry the subscription tier
// (we fetch that separately from the profile endpoint); Scope is a
// space-separated list of granted scopes.
type tokenExchangeResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
	ExpiresIn    int    `json:"expires_in"`
}

// profileResponse is the subset of Anthropic's /api/oauth/profile response
// we use to learn the subscription tier and rate-limit tier.
type profileResponse struct {
	Organization struct {
		OrganizationType string `json:"organization_type"`
		RateLimitTier    string `json:"rate_limit_tier"`
	} `json:"organization"`
}

// fetchProfile calls Anthropic's /api/oauth/profile with the issued access
// token to learn the subscription tier + rate limit tier. Returns (nil, nil)
// if profileURL isn't configured. Failures are non-fatal and surface as
// errors for the caller to log.
func (s *Service) fetchProfile(ctx context.Context, accessToken string) (*profileResponse, error) {
	if s.profileURL == "" || accessToken == "" {
		return nil, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.profileURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create profile request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("anthropic-version", anthropicAPIVersion)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("profile request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("read profile response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("profile fetch failed (status %d): %s", resp.StatusCode, redactedBody(body))
	}

	var profile profileResponse
	if err := json.Unmarshal(body, &profile); err != nil {
		return nil, fmt.Errorf("parse profile response: %w", err)
	}
	return &profile, nil
}

// exchangeAuthCode POSTs to the token endpoint with the auth code and PKCE
// verifier and returns the issued tokens.
func (s *Service) exchangeAuthCode(ctx context.Context, code, state, verifier string) (*tokenExchangeResponse, error) {
	reqBody, err := json.Marshal(map[string]string{
		"grant_type":    "authorization_code",
		"code":          code,
		"state":         state,
		"client_id":     s.clientID,
		"redirect_uri":  s.redirectURI,
		"code_verifier": verifier,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal token request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.tokenURL, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("create token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token exchange: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("read token response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token exchange failed (status %d): %s", resp.StatusCode, redactedBody(body))
	}

	var tokenResp tokenExchangeResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("parse token response: %w", err)
	}
	if tokenResp.AccessToken == "" {
		return nil, fmt.Errorf("token exchange returned empty access_token")
	}
	return &tokenResp, nil
}

// credRefreshMu returns a per-credential mutex for serializing refreshes.
// Prevents concurrent requests from both consuming the same refresh token
// and tripping refresh_token_reused at Anthropic.
func (s *Service) credRefreshMu(credID uuid.UUID) *sync.Mutex {
	val, _ := s.refreshMu.LoadOrStore(credID.String(), &sync.Mutex{})
	return val.(*sync.Mutex)
}

// RefreshTokenByID refreshes an expired access token for a specific credential.
//
// Scope must match the credential's owner — personal credentials require a
// scope with the matching UserID. The unified runtime path constructs scope
// from the picked credential's UserID (orchestrator.go); the legacy
// org-fallback GetValidToken path constructs an org scope.
func (s *Service) RefreshTokenByID(ctx context.Context, scope models.Scope, credID uuid.UUID) (*models.AnthropicSubscription, error) {
	mu := s.credRefreshMu(credID)
	mu.Lock()
	defer mu.Unlock()

	cred, err := s.credentials.GetByID(ctx, scope, credID)
	if err != nil {
		return nil, fmt.Errorf("get credential: %w", err)
	}

	cfg, ok := cred.Config.(models.AnthropicConfig)
	if !ok || cfg.Subscription == nil {
		return nil, fmt.Errorf("credential is not an Anthropic subscription")
	}

	sub := *cfg.Subscription

	// After acquiring the lock, check if another goroutine already refreshed.
	if !sub.NeedsRefresh(refreshWindow) && sub.AccessToken != "" {
		return &sub, nil
	}

	if sub.RefreshToken == "" {
		return nil, fmt.Errorf("no refresh token available — user must re-authenticate")
	}

	reqBody, err := json.Marshal(map[string]string{
		"grant_type":    refreshGrantType,
		"refresh_token": sub.RefreshToken,
		"client_id":     s.clientID,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal refresh request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.tokenURL, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("create refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("refresh request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("read refresh response: %w", err)
	}

	if isClaudeRefreshTokenReused(body) {
		s.logger.Warn().Str("cred_id", credID.String()).Msg("refresh token already used by another client; access token may still be valid")
		return nil, fmt.Errorf("refresh token already used by another client")
	}

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden || isClaudeInvalidGrant(body) {
		if err := s.credentials.UpdateStatusByID(ctx, scope, credID, models.CodingCredentialStatusInvalid); err != nil {
			s.logger.Warn().Err(err).Str("cred_id", credID.String()).Msg("failed to update credential status")
		}
		// Drop the per-credential refresh mutex so sync.Map doesn't grow
		// indefinitely as credentials churn through the "invalid" state.
		s.refreshMu.Delete(credID.String())
		return nil, fmt.Errorf("refresh token revoked (status %d)", resp.StatusCode)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("refresh failed (status %d): %s", resp.StatusCode, redactedBody(body))
	}

	var tokenResp tokenExchangeResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("parse refresh response: %w", err)
	}
	if tokenResp.AccessToken == "" {
		return nil, fmt.Errorf("refresh response returned empty access_token")
	}

	newSub := &models.AnthropicSubscription{
		AccessToken:   tokenResp.AccessToken,
		RefreshToken:  tokenResp.RefreshToken,
		ExpiresAt:     time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second),
		AccountType:   sub.AccountType,
		RateLimitTier: sub.RateLimitTier,
		Scopes:        sub.Scopes,
	}
	// If the refresh response carries a fresh scope string, prefer it so
	// scope changes (e.g. an added scope) are reflected.
	if refreshed := parseScopes(tokenResp.Scope); len(refreshed) > 0 {
		newSub.Scopes = refreshed
	}
	// Anthropic's refresh sometimes returns an empty refresh_token string,
	// meaning "keep the old one". Avoid clobbering the stored value in that
	// case — otherwise the next refresh has nothing to send.
	if newSub.RefreshToken == "" {
		newSub.RefreshToken = sub.RefreshToken
	}

	if err := s.credentials.UpsertByID(ctx, scope, credID, models.AnthropicConfig{Subscription: newSub}); err != nil {
		return nil, fmt.Errorf("store refreshed credential: %w", err)
	}

	s.logger.Debug().
		Str("cred_id", credID.String()).
		Msg("Claude Code OAuth token refreshed")

	return newSub, nil
}

func isClaudeRefreshTokenReused(body []byte) bool {
	return strings.Contains(string(body), "refresh_token_reused")
}

func isClaudeInvalidGrant(body []byte) bool {
	var payload struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err == nil && payload.Error == "invalid_grant" {
		return true
	}
	return strings.Contains(string(body), `"invalid_grant"`)
}

// maxRoundRobinAttempts caps how many distinct credentials GetValidToken
// will try before giving up. Same rationale as codexauth's constant.
const maxRoundRobinAttempts = 5

// GetValidToken returns a valid access token + its credential ID using
// round-robin across all active Claude Code subscriptions for the org.
// It auto-refreshes if needed and, when a credential's refresh fails and
// the cached token is already expired, marks that credential invalid and
// tries the next one in the rotation. Returns (nil, nil, nil) if no
// usable subscription exists — callers should treat that as "fall back
// to Anthropic API key".
func (s *Service) GetValidToken(ctx context.Context, orgID uuid.UUID) (*models.AnthropicSubscription, *uuid.UUID, error) {
	if s.credentials == nil {
		return nil, nil, nil
	}

	scope := orgScope(orgID)
	tried := make(map[uuid.UUID]struct{}, maxRoundRobinAttempts)
	var lastErr error

	for attempt := 0; attempt < maxRoundRobinAttempts; attempt++ {
		cred, err := s.credentials.ClaimNextLabeledRoundRobin(ctx, scope, models.ProviderAnthropic)
		if err != nil {
			if isNotFoundError(err) {
				if lastErr != nil {
					return nil, nil, fmt.Errorf("no usable Claude subscription: %w", lastErr)
				}
				return nil, nil, nil
			}
			return nil, nil, fmt.Errorf("get credential: %w", err)
		}

		if _, seen := tried[cred.ID]; seen {
			// Rotation wrapped back to an already-tried credential before we
			// found a usable one. Surface this so operators can tell the
			// "no usable subscription" failure from a genuine empty set
			// versus a set where every row got rejected this cycle.
			s.logger.Debug().
				Str("org_id", orgID.String()).
				Int("tried", len(tried)).
				Msg("claude subscription rotation exhausted before finding usable credential")
			break
		}
		tried[cred.ID] = struct{}{}

		cfg, ok := cred.Config.(models.AnthropicConfig)
		if !ok || cfg.Subscription == nil {
			lastErr = fmt.Errorf("credential %s is not an Anthropic subscription", cred.ID)
			continue
		}

		sub := *cfg.Subscription

		if sub.AccessToken == "" {
			lastErr = fmt.Errorf("credential %s has empty access token", cred.ID)
			continue
		}

		if sub.RefreshToken == "" || !sub.NeedsRefresh(refreshWindow) {
			credID := cred.ID
			return &sub, &credID, nil
		}

		refreshed, refreshErr := s.RefreshTokenByID(ctx, scope, cred.ID)
		if refreshErr == nil {
			credID := cred.ID
			return refreshed, &credID, nil
		}
		lastErr = refreshErr

		if !sub.IsExpired() {
			s.logger.Warn().Err(refreshErr).Str("cred_id", cred.ID.String()).Msg("token refresh failed; using cached token")
			credID := cred.ID
			return &sub, &credID, nil
		}

		s.logger.Warn().Err(refreshErr).Str("cred_id", cred.ID.String()).Msg("token refresh failed and cached token expired; marking invalid")
		if statusErr := s.credentials.UpdateStatusByID(ctx, scope, cred.ID, models.CodingCredentialStatusInvalid); statusErr != nil {
			s.logger.Warn().Err(statusErr).Str("cred_id", cred.ID.String()).Msg("failed to mark credential invalid")
		}
		// Drop the per-credential refresh mutex — the credential is out of
		// rotation now, and keeping the entry around would leak sync.Map
		// memory across the process lifetime.
		s.refreshMu.Delete(cred.ID.String())
	}

	if lastErr != nil {
		return nil, nil, fmt.Errorf("no usable Claude subscription after %d attempts: %w", len(tried), lastErr)
	}
	return nil, nil, nil
}

// HasActiveSubscription reports whether the org has at least one active
// Claude Code subscription row. Cheap existence check used by the
// orchestrator to decide whether to suppress the Anthropic API-key env var
// without claiming a round-robin slot (which would bump last_used_at and
// distort rotation). Delegates to the store's EXISTS probe so it's O(1)
// regardless of how many subscriptions the org holds.
func (s *Service) HasActiveSubscription(ctx context.Context, orgID uuid.UUID) (bool, error) {
	if s.credentials == nil {
		return false, nil
	}
	exists, err := s.credentials.HasActiveLabeled(ctx, orgScope(orgID), models.ProviderAnthropic)
	if err != nil {
		return false, fmt.Errorf("check anthropic subscription: %w", err)
	}
	return exists, nil
}

// Disconnect removes a specific Claude subscription by ID at the given scope.
// Ordering matches codexauth: DB first, then in-memory state, so a concurrent
// refresh cannot resurrect the row after we "forget" its mutex.
func (s *Service) Disconnect(ctx context.Context, scope models.Scope, credID uuid.UUID) error {
	if s.credentials != nil {
		if err := s.credentials.DisableByID(ctx, scope, credID); err != nil {
			return err
		}
	}
	s.refreshMu.Delete(credID.String())
	return nil
}

// DisconnectForOrg removes a credential by ID after verifying it belongs to
// the given scope. Returns ErrCredentialNotFound if the credential doesn't
// exist, belongs to a different scope, or belongs to a different provider.
//
// The name is preserved for backward compatibility with existing call sites;
// despite "ForOrg", scope can be either org or personal.
func (s *Service) DisconnectForOrg(ctx context.Context, scope models.Scope, credID uuid.UUID) error {
	if s.credentials == nil {
		return nil
	}
	cred, err := s.credentials.GetByID(ctx, scope, credID)
	if err != nil {
		if isNotFoundError(err) {
			return ErrCredentialNotFound
		}
		return fmt.Errorf("get credential: %w", err)
	}
	cfg, ok := cred.Config.(models.AnthropicConfig)
	if cred.Provider != models.ProviderAnthropic || cred.Label == "" || !ok || cfg.Subscription == nil {
		return ErrCredentialNotFound
	}
	return s.Disconnect(ctx, scope, credID)
}

// DisconnectAll removes every Claude subscription at the given scope,
// leaving any Anthropic API-key row (label="") in place. Ordering matches
// Disconnect: the DB rows are disabled first so a concurrent refresh cannot
// resurrect a credential after we've dropped its mutex; only then do we
// clean up the in-memory maps.
func (s *Service) DisconnectAll(ctx context.Context, scope models.Scope) error {
	if s.credentials == nil {
		// Still clean the init mutexes so test doubles without a store
		// don't leak entries.
		s.clearInitMutexesForScope(scope)
		return nil
	}

	// Snapshot the subscription IDs before disabling so we can clear their
	// refresh mutexes afterwards. ListByProvider errors are non-fatal here:
	// the DisableLabeled call below is the source of truth, but log the miss
	// so operators can correlate any leaked refresh mutexes.
	creds, err := s.credentials.ListByProvider(ctx, scope, models.ProviderAnthropic)
	if err != nil {
		s.logger.Warn().Err(err).Str("org_id", scope.OrgID.String()).Msg("failed to list claude subscriptions before disconnect cleanup")
	}

	if err := s.credentials.DisableLabeled(ctx, scope, models.ProviderAnthropic); err != nil {
		return err
	}

	for _, cred := range creds {
		if cred.Label == "" {
			continue // leave the API-key row alone
		}
		s.refreshMu.Delete(cred.ID.String())
	}
	s.clearInitMutexesForScope(scope)

	return nil
}

// clearInitMutexesForScope drops every init-side mutex scoped to the given
// scope. Used by DisconnectAll so a subsequent InitiateOAuth gets a fresh
// mutex instead of reusing a stale one that might still be held by an
// in-flight call. Keys carry a leading scope tag, so org and personal keys
// cannot collide even when a label contains scope-looking delimiters.
func (s *Service) clearInitMutexesForScope(scope models.Scope) {
	matches := func(k string) bool {
		if scope.IsPersonal() {
			return strings.HasPrefix(k, "personal:"+scope.OrgID.String()+":u:"+scope.UserID.String()+":")
		}
		return strings.HasPrefix(k, "org:"+scope.OrgID.String()+":")
	}
	s.initMu.Range(func(key, _ any) bool {
		if k, ok := key.(string); ok && matches(k) {
			s.initMu.Delete(key)
		}
		return true
	})
}

// ListSubscriptions returns all connected Claude subscriptions at the given
// scope. Skips the label="" row (which is the Anthropic API-key credential,
// not a subscription) so the subscriptions list doesn't leak an API key
// summary.
func (s *Service) ListSubscriptions(ctx context.Context, scope models.Scope) ([]SubscriptionInfo, error) {
	if s.credentials == nil {
		return nil, nil
	}

	creds, err := s.credentials.ListByProvider(ctx, scope, models.ProviderAnthropic)
	if err != nil {
		return nil, fmt.Errorf("list subscriptions: %w", err)
	}

	var subs []SubscriptionInfo
	for _, cred := range creds {
		if cred.Label == "" {
			continue
		}
		cfg, ok := cred.Config.(models.AnthropicConfig)
		if !ok || cfg.Subscription == nil {
			continue
		}
		subs = append(subs, SubscriptionInfo{
			ID:          cred.ID,
			Label:       cred.Label,
			AccountType: cfg.Subscription.AccountType,
			Status:      SubscriptionStatus(cred.Status),
			LastUsedAt:  cred.LastUsedAt,
			CreatedBy:   cred.CreatedBy,
			CreatedAt:   cred.CreatedAt,
		})
	}
	return subs, nil
}

// isNotFoundError reports whether err signals "no matching credential row".
// Three sentinels are checked because the credential store now spans both
// legacy and unified backends:
//   - pgx.ErrNoRows surfaces from the legacy OrgCredentialStore via
//     pgx.CollectOneRow on a missing-row read.
//   - ErrCredentialNotFound is the local sentinel returned by service-layer
//     ownership checks (DisconnectForOrg).
//   - db.ErrCodingCredentialNotFound surfaces from the unified
//     CodingCredentialStore on personal-scope reads.
func isNotFoundError(err error) bool {
	return errors.Is(err, pgx.ErrNoRows) ||
		errors.Is(err, ErrCredentialNotFound) ||
		errors.Is(err, db.ErrCodingCredentialNotFound)
}

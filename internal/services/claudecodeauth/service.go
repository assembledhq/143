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
//   - Subscription credentials live under ProviderAnthropicSubscription with
//     a non-empty label. Anthropic API-key credentials live in a separate
//     provider partition (ProviderAnthropic), so this service never sees
//     them; one org can hold an API key alongside N labeled subscriptions.
//   - Round-robin selection uses ClaimNextLabeledRoundRobin scoped to the
//     subscription provider, so API-key rows are never claimed for
//     subscription use.
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

	// harvestedTokenMaxLifetime bounds sandbox-originated Claude Code tokens
	// before they are persisted back to the credential store.
	harvestedTokenMaxLifetime = 24 * time.Hour

	// setupTokenDefaultLifetime is used when a pasted `claude setup-token`
	// value is not a JWT or has no parseable exp claim. Anthropic documents
	// setup tokens as one-year tokens.
	setupTokenDefaultLifetime = 365 * 24 * time.Hour
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
// Subscription credentials live under ProviderAnthropicSubscription with a
// non-empty label. Anthropic API-key credentials live in a separate provider
// partition (ProviderAnthropic) and are never touched by this service.
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
	// DisableLabeled disables all labeled rows (label != '') at (scope,
	// provider). Used by DisconnectAll; API-key credentials live under a
	// different provider (ProviderAnthropic) and are untouched.
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

// ErrInvalidOAuthToken is returned when a pasted setup-token value is empty,
// expired, or otherwise structurally unusable.
var ErrInvalidOAuthToken = fmt.Errorf("Claude Code OAuth token is invalid")

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
	AuthMode    string             `json:"auth_mode,omitempty"`
	ExpiresAt   *time.Time         `json:"expires_at,omitempty"`
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

	pendingCfg := models.AnthropicSubscriptionConfig{
		State:        state,
		CodeVerifier: verifier,
		AuthorizeURL: authURL,
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
	cred, err := s.credentials.GetByProviderAndLabel(ctx, scope, models.ProviderAnthropicSubscription, label)
	if err != nil {
		// Only "no row" should surface as ErrPendingAuthNotFound (→ 404).
		// Transient DB errors must bubble up as 500s so operators can see them.
		if isNotFoundError(err) {
			return nil, ErrPendingAuthNotFound
		}
		return nil, fmt.Errorf("lookup pending subscription: %w", err)
	}
	cfg, ok := cred.Config.(models.AnthropicSubscriptionConfig)
	if !ok {
		return nil, fmt.Errorf("pending row has unexpected config")
	}
	if cfg.State == "" || cfg.CodeVerifier == "" {
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
	if subtle.ConstantTimeCompare([]byte(returnedState), []byte(cfg.State)) != 1 {
		return nil, ErrInvalidPaste
	}

	tokens, err := s.exchangeAuthCode(ctx, code, returnedState, cfg.CodeVerifier)
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

	if err := s.credentials.UpsertByID(ctx, scope, cred.ID, models.FromAnthropicSubscription(*sub)); err != nil {
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

// StoreOAuthToken stores a long-lived CLAUDE_CODE_OAUTH_TOKEN generated by
// `claude setup-token`. Unlike the PKCE flow, 143 does not exchange or refresh
// this value; it is injected into Claude Code sandboxes as an environment
// variable and renewed by asking the user to rerun setup-token.
func (s *Service) StoreOAuthToken(ctx context.Context, scope models.Scope, createdBy *uuid.UUID, label, oauthToken string) (*CompleteResponse, error) {
	if s.credentials == nil {
		return nil, fmt.Errorf("credential store not configured")
	}

	oauthToken = strings.TrimSpace(oauthToken)
	if oauthToken == "" {
		return nil, ErrInvalidOAuthToken
	}

	expiresAt, err := setupTokenExpiresAt(time.Now(), oauthToken)
	if err != nil {
		return nil, err
	}
	cfg := models.AnthropicSubscriptionConfig{
		AuthMode:            models.AnthropicSubscriptionAuthModeSetupToken,
		OAuthToken:          oauthToken,
		OAuthTokenExpiresAt: expiresAt,
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidOAuthToken, err)
	}

	if _, err := s.credentials.UpsertWithLabel(ctx, scope, createdBy, label, cfg); err != nil {
		return nil, fmt.Errorf("store Claude Code OAuth token: %w", err)
	}

	s.logger.Info().
		Str("org_id", scope.OrgID.String()).
		Bool("personal", scope.IsPersonal()).
		Str("label", label).
		Time("expires_at", expiresAt).
		Msg("Claude Code setup-token OAuth credential stored")

	return &CompleteResponse{}, nil
}

func setupTokenExpiresAt(now time.Time, token string) (time.Time, error) {
	if exp, ok := parseJWTExpiration(token); ok {
		if !now.Before(exp) {
			return time.Time{}, fmt.Errorf("%w: token is expired", ErrInvalidOAuthToken)
		}
		return exp, nil
	}
	return now.Add(setupTokenDefaultLifetime), nil
}

func parseJWTExpiration(token string) (time.Time, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, false
	}
	var claims struct {
		Exp json.Number `json:"exp"`
	}
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.UseNumber()
	if err := dec.Decode(&claims); err != nil || claims.Exp == "" {
		return time.Time{}, false
	}
	expUnix, err := claims.Exp.Int64()
	if err != nil {
		return time.Time{}, false
	}
	if expUnix <= 0 {
		return time.Time{}, false
	}
	return time.Unix(expUnix, 0), true
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

// RefreshLocker is optionally implemented by the credential store to
// serialize token refreshes across processes. The in-process refreshMu only
// protects a single process; Anthropic refresh tokens are single-use, so two
// worker hosts refreshing the same credential concurrently make the loser
// look revoked (invalid_grant) and would nuke an otherwise healthy
// credential. Production wires *db.ScopedCredentialStore, which implements
// this via a Postgres advisory lock.
type RefreshLocker interface {
	WithRefreshLock(ctx context.Context, credID uuid.UUID, fn func(ctx context.Context) error) error
}

// maxRefreshAttempts bounds the refresh loop in refreshTokenLocked. A second
// attempt happens only when Anthropic rejects the refresh token but the
// stored token has rotated since this attempt loaded it — i.e. the rejection
// is stale, not a revocation.
const maxRefreshAttempts = 2

// runUnderRefreshLocks runs fn while holding the per-credential in-process
// mutex and, when the store supports it, the cross-host advisory lock. Every
// writer of a credential's token material (refresh, harvest write-back) must
// go through here so single-use refresh tokens are never double-spent.
func (s *Service) runUnderRefreshLocks(ctx context.Context, credID uuid.UUID, fn func(ctx context.Context) error) error {
	mu := s.credRefreshMu(credID)
	mu.Lock()
	defer mu.Unlock()

	if locker, ok := s.credentials.(RefreshLocker); ok {
		return locker.WithRefreshLock(ctx, credID, fn)
	}
	return fn(ctx)
}

// RefreshTokenByID refreshes an expired access token for a specific credential.
//
// Scope must match the credential's owner — personal credentials require a
// scope with the matching UserID. The unified runtime path constructs scope
// from the picked credential's UserID (orchestrator.go); the legacy
// org-fallback GetValidToken path constructs an org scope.
func (s *Service) RefreshTokenByID(ctx context.Context, scope models.Scope, credID uuid.UUID) (*models.AnthropicSubscription, error) {
	var refreshed *models.AnthropicSubscription
	err := s.runUnderRefreshLocks(ctx, credID, func(ctx context.Context) error {
		sub, err := s.refreshTokenLocked(ctx, scope, credID)
		refreshed = sub
		return err
	})
	if err != nil {
		return nil, err
	}
	return refreshed, nil
}

// refreshTokenLocked does the actual refresh. Callers must hold the
// per-credential in-process mutex and, when the store supports it, the
// cross-host refresh lock — the re-read at the top of the loop is what turns
// "another refresher beat us" into a cheap early return instead of a
// double-spend of the single-use refresh token.
func (s *Service) refreshTokenLocked(ctx context.Context, scope models.Scope, credID uuid.UUID) (*models.AnthropicSubscription, error) {
	for attempt := 1; ; attempt++ {
		cred, err := s.credentials.GetByID(ctx, scope, credID)
		if err != nil {
			return nil, fmt.Errorf("get credential: %w", err)
		}

		cfg, ok := cred.Config.(models.AnthropicSubscriptionConfig)
		if !ok {
			return nil, fmt.Errorf("credential is not an Anthropic subscription")
		}

		sub := cfg.AsAnthropicSubscription()

		// After acquiring the locks, check if another refresher (goroutine,
		// host, or a sandbox-harvest write-back) already rotated the token.
		if !sub.NeedsRefresh(refreshWindow) && sub.AccessToken != "" {
			return &sub, nil
		}

		if sub.RefreshToken == "" {
			return nil, fmt.Errorf("no refresh token available — user must re-authenticate")
		}

		statusCode, body, err := s.postRefresh(ctx, sub.RefreshToken)
		if err != nil {
			return nil, err
		}

		reused := isClaudeRefreshTokenReused(body)
		rejected := statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden || isClaudeInvalidGrant(body)

		// A reuse/rejection only proves the token we SENT is dead. If the
		// stored token rotated while this request was in flight (a sandbox
		// harvest or another writer landed), the verdict is stale — retry
		// once with the rotated token before acting on it.
		if (reused || rejected) && attempt < maxRefreshAttempts && s.storedRefreshTokenRotated(ctx, scope, credID, sub.RefreshToken) {
			s.logger.Warn().
				Str("cred_id", credID.String()).
				Int("status", statusCode).
				Int("attempt", attempt).
				Msg("refresh verdict was for a stale refresh token; retrying with the rotated token")
			continue
		}

		if reused {
			s.logger.Warn().Str("cred_id", credID.String()).Msg("refresh token already used by another client; access token may still be valid")
			return nil, fmt.Errorf("refresh token already used by another client")
		}

		if rejected {
			s.markCredentialInvalid(ctx, scope, credID,
				fmt.Sprintf("token endpoint rejected refresh (status %d): %s", statusCode, redactedBody(body)))
			return nil, fmt.Errorf("refresh token revoked (status %d)", statusCode)
		}

		if statusCode != http.StatusOK {
			return nil, fmt.Errorf("refresh failed (status %d): %s", statusCode, redactedBody(body))
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

		if err := s.credentials.UpsertByID(ctx, scope, credID, models.FromAnthropicSubscription(*newSub)); err != nil {
			return nil, fmt.Errorf("store refreshed credential: %w", err)
		}

		s.logger.Info().
			Str("cred_id", credID.String()).
			Msg("Claude Code OAuth token refreshed")

		return newSub, nil
	}
}

// postRefresh POSTs a refresh_token grant to the token endpoint and returns
// the raw status + bounded body for the caller to classify.
func (s *Service) postRefresh(ctx context.Context, refreshToken string) (int, []byte, error) {
	reqBody, err := json.Marshal(map[string]string{
		"grant_type":    refreshGrantType,
		"refresh_token": refreshToken,
		"client_id":     s.clientID,
	})
	if err != nil {
		return 0, nil, fmt.Errorf("marshal refresh request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.tokenURL, bytes.NewReader(reqBody))
	if err != nil {
		return 0, nil, fmt.Errorf("create refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("refresh request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return 0, nil, fmt.Errorf("read refresh response: %w", err)
	}
	return resp.StatusCode, body, nil
}

// storedRefreshTokenRotated re-reads the credential and reports whether its
// stored refresh token differs from the one a just-rejected refresh sent.
// True means another writer rotated the token mid-flight, so the rejection
// says nothing about the credential's health. Read errors report false: when
// we can't prove rotation, the caller falls through to its normal rejection
// handling.
func (s *Service) storedRefreshTokenRotated(ctx context.Context, scope models.Scope, credID uuid.UUID, sentToken string) bool {
	cred, err := s.credentials.GetByID(ctx, scope, credID)
	if err != nil {
		return false
	}
	cfg, ok := cred.Config.(models.AnthropicSubscriptionConfig)
	if !ok {
		return false
	}
	return cfg.RefreshToken != "" && cfg.RefreshToken != sentToken
}

// StoreTokenByID persists a Claude Code OAuth token that was refreshed by an
// external Claude Code CLI process. Scope must match the credential owner.
func (s *Service) StoreTokenByID(ctx context.Context, scope models.Scope, credID uuid.UUID, sub models.AnthropicSubscription) (bool, error) {
	if s.credentials == nil {
		return false, errors.New("credential store is not configured")
	}
	if sub.AccessToken == "" {
		return false, errors.New("access_token is required")
	}
	if sub.RefreshToken == "" {
		return false, errors.New("refresh_token is required")
	}
	if sub.ExpiresAt.IsZero() {
		return false, errors.New("expires_at is required")
	}
	if sub.IsExpired() {
		return false, errors.New("expires_at must be in the future")
	}
	if time.Until(sub.ExpiresAt) > harvestedTokenMaxLifetime {
		return false, errors.New("expires_at is implausibly far in the future")
	}

	// Harvest write-backs rotate the stored refresh token, so they take the
	// same locks as RefreshTokenByID — otherwise a harvest landing while
	// another host's refresh is mid-flight gets silently overwritten.
	var stored bool
	err := s.runUnderRefreshLocks(ctx, credID, func(ctx context.Context) error {
		cred, err := s.credentials.GetByID(ctx, scope, credID)
		if err != nil {
			return fmt.Errorf("get credential: %w", err)
		}
		cfg, ok := cred.Config.(models.AnthropicSubscriptionConfig)
		if !ok {
			return fmt.Errorf("credential is not an Anthropic subscription")
		}
		if !harvestedSubscriptionIsNewer(cfg.AsAnthropicSubscription(), sub) {
			return nil
		}

		if s.profileURL == "" {
			return errors.New("profile URL is required to validate harvested Claude token")
		}
		profile, err := s.fetchProfile(ctx, sub.AccessToken)
		if err != nil {
			return fmt.Errorf("validate harvested Claude token: %w", err)
		}
		if profile != nil {
			if profile.Organization.OrganizationType != "" {
				sub.AccountType = profile.Organization.OrganizationType
			}
			if profile.Organization.RateLimitTier != "" {
				sub.RateLimitTier = profile.Organization.RateLimitTier
			}
		}

		if err := s.credentials.UpsertByID(ctx, scope, credID, models.FromAnthropicSubscription(sub)); err != nil {
			return fmt.Errorf("store claude subscription credential: %w", err)
		}
		stored = true
		return nil
	})
	return stored, err
}

func harvestedSubscriptionIsNewer(current, harvested models.AnthropicSubscription) bool {
	if !harvested.ExpiresAt.After(current.ExpiresAt) {
		return false
	}
	return current.AccessToken != harvested.AccessToken ||
		current.RefreshToken != harvested.RefreshToken ||
		current.ExpiresAt.UnixMilli() != harvested.ExpiresAt.UnixMilli() ||
		current.AccountType != harvested.AccountType ||
		current.RateLimitTier != harvested.RateLimitTier ||
		!stringSlicesEqual(current.Scopes, harvested.Scopes)
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
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
		cred, err := s.credentials.ClaimNextLabeledRoundRobin(ctx, scope, models.ProviderAnthropicSubscription)
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

		cfg, ok := cred.Config.(models.AnthropicSubscriptionConfig)
		if !ok {
			// A corrupt config under provider=anthropic_subscription can never
			// be used. Mark it invalid so the unified resolver stops returning
			// it; otherwise PickRunnable keeps handing back this same
			// top-priority row and the tried-map below breaks the loop before
			// lower-priority healthy credentials are reached.
			lastErr = fmt.Errorf("credential %s is not an Anthropic subscription", cred.ID)
			s.markCredentialInvalid(ctx, scope, cred.ID, "stored config is not AnthropicSubscriptionConfig")
			continue
		}
		if cfg.IsSetupToken() {
			lastErr = fmt.Errorf("credential %s uses setup-token auth, which requires unified env injection", cred.ID)
			continue
		}

		sub := cfg.AsAnthropicSubscription()

		if sub.AccessToken == "" {
			// An active row with no access token is unusable; same rotation
			// hazard as the wrong-type case above.
			lastErr = fmt.Errorf("credential %s has empty access token", cred.ID)
			s.markCredentialInvalid(ctx, scope, cred.ID, "empty access token")
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

		s.markCredentialInvalid(ctx, scope, cred.ID, "token refresh failed and cached token expired")
	}

	if lastErr != nil {
		return nil, nil, fmt.Errorf("no usable Claude subscription after %d attempts: %w", len(tried), lastErr)
	}
	return nil, nil, nil
}

// markCredentialInvalid durably removes a credential from the round-robin by
// flipping its runtime status to invalid, which busts the unified resolver
// cache so the next ClaimNextLabeledRoundRobin (PickRunnable) skips it.
// Without this, PickRunnable re-returns the same top-priority row every
// iteration and the caller's tried-map breaks the loop before lower-priority
// healthy credentials are reached. Also drops the per-credential refresh mutex
// (the credential is out of rotation now, so keeping the entry would leak
// sync.Map memory across the process lifetime). Best-effort: a failed update
// is logged, not fatal.
func (s *Service) markCredentialInvalid(ctx context.Context, scope models.Scope, credID uuid.UUID, reason string) {
	// Error-level on purpose: invalidation takes the credential out of
	// rotation, so every session that depended on it starts failing. This
	// line is the breadcrumb operators search for when a user reports
	// "Claude auth suddenly stopped working".
	s.logger.Error().
		Str("org_id", scope.OrgID.String()).
		Bool("personal", scope.IsPersonal()).
		Str("cred_id", credID.String()).
		Str("reason", reason).
		Msg("marking claude subscription credential invalid")
	if statusErr := s.credentials.UpdateStatusByID(ctx, scope, credID, models.CodingCredentialStatusInvalid); statusErr != nil {
		s.logger.Error().Err(statusErr).Str("cred_id", credID.String()).Msg("failed to mark credential invalid")
	}
	s.refreshMu.Delete(credID.String())
}

// HasInvalidSubscription reports whether the scope holds at least one labeled
// subscription row whose status is invalid. The orchestrator uses this to
// tell "you never connected a credential" apart from "your credential was
// invalidated after a rejected refresh" when failing a Claude Code run.
func (s *Service) HasInvalidSubscription(ctx context.Context, scope models.Scope) (bool, error) {
	if s.credentials == nil {
		return false, nil
	}
	creds, err := s.credentials.ListByProvider(ctx, scope, models.ProviderAnthropicSubscription)
	if err != nil {
		return false, fmt.Errorf("list anthropic subscriptions: %w", err)
	}
	for _, cred := range creds {
		if cred.Label != "" && cred.Status == models.CredentialStatusInvalid {
			return true, nil
		}
	}
	return false, nil
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
	exists, err := s.credentials.HasActiveLabeled(ctx, orgScope(orgID), models.ProviderAnthropicSubscription)
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
	_, ok := cred.Config.(models.AnthropicSubscriptionConfig)
	if cred.Provider != models.ProviderAnthropicSubscription || cred.Label == "" || !ok {
		return ErrCredentialNotFound
	}
	return s.Disconnect(ctx, scope, credID)
}

// DisconnectAll removes every Claude subscription at the given scope.
// Anthropic API-key rows live under a separate provider partition
// (ProviderAnthropic) and are untouched. Ordering matches
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
	creds, err := s.credentials.ListByProvider(ctx, scope, models.ProviderAnthropicSubscription)
	if err != nil {
		s.logger.Warn().Err(err).Str("org_id", scope.OrgID.String()).Msg("failed to list claude subscriptions before disconnect cleanup")
	}

	if err := s.credentials.DisableLabeled(ctx, scope, models.ProviderAnthropicSubscription); err != nil {
		return err
	}

	for _, cred := range creds {
		if cred.Label == "" {
			continue // unlabeled rows are never disabled by DisableLabeled
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
// scope. Skips any label="" row defensively — subscription rows always carry
// a label, so an unlabeled row is malformed and not worth surfacing.
func (s *Service) ListSubscriptions(ctx context.Context, scope models.Scope) ([]SubscriptionInfo, error) {
	if s.credentials == nil {
		return nil, nil
	}

	creds, err := s.credentials.ListByProvider(ctx, scope, models.ProviderAnthropicSubscription)
	if err != nil {
		return nil, fmt.Errorf("list subscriptions: %w", err)
	}

	var subs []SubscriptionInfo
	for _, cred := range creds {
		if cred.Label == "" {
			continue
		}
		cfg, ok := cred.Config.(models.AnthropicSubscriptionConfig)
		if !ok {
			continue
		}
		authMode := string(cfg.AuthMode)
		var expiresAt *time.Time
		switch {
		case cfg.IsSetupToken():
			authMode = string(models.AnthropicSubscriptionAuthModeSetupToken)
			if !cfg.OAuthTokenExpiresAt.IsZero() {
				t := cfg.OAuthTokenExpiresAt
				expiresAt = &t
			}
		default:
			if authMode == "" {
				authMode = string(models.AnthropicSubscriptionAuthModeRotatingOAuth)
			}
			if !cfg.ExpiresAt.IsZero() {
				t := cfg.ExpiresAt
				expiresAt = &t
			}
		}
		subs = append(subs, SubscriptionInfo{
			ID:          cred.ID,
			Label:       cred.Label,
			AccountType: cfg.AccountType,
			AuthMode:    authMode,
			ExpiresAt:   expiresAt,
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

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

	"github.com/assembledhq/143/internal/models"
)

const (
	// defaultAuthorizeURL is where we send the user's browser for login.
	defaultAuthorizeURL = "https://claude.com/cai/oauth/authorize"

	// defaultTokenURL is the Anthropic PKCE token endpoint used for both the
	// initial authorization_code exchange and subsequent refresh_token calls.
	defaultTokenURL = "https://platform.claude.com/v1/oauth/token"

	// defaultRedirectURI is the Anthropic-hosted callback the CLI uses; the
	// browser lands here after login and Anthropic renders `<code>#<state>`
	// for the user to paste back.
	defaultRedirectURI = "https://platform.claude.com/oauth/code/callback"

	// defaultClientID is the Claude Code CLI's public OAuth client_id.
	defaultClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"

	// refreshGrantType is the OAuth 2.0 grant type for token refresh.
	refreshGrantType = "refresh_token"

	// refreshWindow is how far before expiry we proactively refresh.
	refreshWindow = 5 * time.Minute

	// verifierBytes is the entropy used for both the PKCE code_verifier
	// and the anti-CSRF state parameter. 32 bytes → 43 base64url chars,
	// comfortably above the 43-char minimum RFC 7636 requires.
	verifierBytes = 32
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

// CredentialStore defines the credential operations needed by the auth service.
// Mirrors codexauth.CredentialStore but uses the labeled round-robin variant
// because ProviderAnthropic mixes an API-key row (label="") with subscription
// rows (label!="").
type CredentialStore interface {
	Get(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) (*models.DecryptedCredential, error)
	UpsertWithLabel(ctx context.Context, orgID uuid.UUID, createdBy *uuid.UUID, label string, cfg models.ProviderConfig) (*uuid.UUID, error)
	InsertPendingAuth(ctx context.Context, orgID uuid.UUID, createdBy *uuid.UUID, label string, cfg models.ProviderConfig) (*uuid.UUID, error)
	GetByID(ctx context.Context, orgID uuid.UUID, id uuid.UUID) (*models.DecryptedCredential, error)
	GetByProviderAndLabel(ctx context.Context, orgID uuid.UUID, provider models.ProviderName, label string) (*models.DecryptedCredential, error)
	ListByProvider(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) ([]models.DecryptedCredential, error)
	ClaimNextLabeledRoundRobin(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) (*models.DecryptedCredential, error)
	DisableByID(ctx context.Context, orgID uuid.UUID, id uuid.UUID) error
	UpdateStatusByID(ctx context.Context, orgID uuid.UUID, id uuid.UUID, status string) error
	UpsertByID(ctx context.Context, orgID uuid.UUID, id uuid.UUID, cfg models.ProviderConfig) error
	ExistsForProviderByID(ctx context.Context, orgID uuid.UUID, id uuid.UUID, provider models.ProviderName) (bool, error)
	// DisableLabeled disables all subscription rows (label != '') for an org's
	// Anthropic credentials while leaving the API-key row (label='') intact.
	// Used by DisconnectAll so the user doesn't lose their Anthropic API key
	// when disconnecting every Claude subscription.
	DisableLabeled(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) error
}

// ErrCredentialNotFound is returned when no credential exists for the given org/provider.
var ErrCredentialNotFound = fmt.Errorf("credential not found")

// ErrPendingAuthNotFound is returned when CompleteOAuth is called without a
// prior InitiateOAuth for the (org, label).
var ErrPendingAuthNotFound = fmt.Errorf("no pending auth flow — initiate first")

// ErrInvalidPaste is returned when the user's pasted code#state string is
// malformed or the state doesn't match the one we stored.
var ErrInvalidPaste = fmt.Errorf("pasted code is invalid or expired")

// InitiateResponse is returned by the /initiate endpoint. The caller hands
// AuthorizeURL to the user's browser; State is echoed back to the UI so the
// modal can verify the eventual paste matches this session.
type InitiateResponse struct {
	AuthorizeURL string `json:"authorize_url"`
	State        string `json:"state"`
	Label        string `json:"label"`
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
	redirectURI  string
	clientID     string
	scopes       []string
	refreshMu    sync.Map // credential ID string -> *sync.Mutex
	initMu       sync.Map // pendingKey (orgID+label) -> *sync.Mutex
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

// pendingKey returns the sync.Map key for init-side mutexes scoped to org+label.
func pendingKey(orgID uuid.UUID, label string) string {
	return orgID.String() + ":" + label
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

// InitiateOAuth starts a new PKCE auth flow for the given org+label. Generates
// a verifier + state, persists them on a pending_auth row, and returns the
// authorize URL for the caller to hand to the user's browser.
func (s *Service) InitiateOAuth(ctx context.Context, orgID uuid.UUID, createdBy *uuid.UUID, label string) (*InitiateResponse, error) {
	// Serialize concurrent init calls for the same (org, label) — otherwise
	// two racing initiations could both reach InsertPendingAuth, with the
	// slower request silently overwriting the faster one's verifier.
	pKey := pendingKey(orgID, label)
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
		if _, err := s.credentials.InsertPendingAuth(ctx, orgID, createdBy, label, pendingCfg); err != nil {
			return nil, fmt.Errorf("persist pending subscription: %w", err)
		}
	}

	s.logger.Info().
		Str("org_id", orgID.String()).
		Str("label", label).
		Msg("Claude Code OAuth initiated")

	return &InitiateResponse{
		AuthorizeURL: authURL,
		State:        state,
		Label:        label,
	}, nil
}

// CompleteOAuth exchanges the user's pasted "<code>#<state>" for tokens,
// verifies the returned state matches the one we stored, and upgrades the
// pending row to an active subscription.
func (s *Service) CompleteOAuth(ctx context.Context, orgID uuid.UUID, label, codeAndState string) (*CompleteResponse, error) {
	pKey := pendingKey(orgID, label)
	muVal, _ := s.initMu.LoadOrStore(pKey, &sync.Mutex{})
	mu := muVal.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()

	code, returnedState, err := splitCodeAndState(codeAndState)
	if err != nil {
		return nil, err
	}

	if s.credentials == nil {
		return nil, fmt.Errorf("credential store not configured")
	}
	cred, err := s.credentials.GetByProviderAndLabel(ctx, orgID, models.ProviderAnthropic, label)
	if err != nil {
		return nil, ErrPendingAuthNotFound
	}
	cfg, ok := cred.Config.(models.AnthropicConfig)
	if !ok || cfg.Subscription == nil {
		return nil, fmt.Errorf("pending row has unexpected config")
	}
	if cfg.Subscription.State == "" || cfg.Subscription.CodeVerifier == "" {
		return nil, ErrPendingAuthNotFound
	}
	if returnedState != cfg.Subscription.State {
		return nil, ErrInvalidPaste
	}

	tokens, err := s.exchangeAuthCode(ctx, code, returnedState, cfg.Subscription.CodeVerifier)
	if err != nil {
		return nil, err
	}

	sub := &models.AnthropicSubscription{
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		AccountType:  tokens.AccountType,
	}
	if tokens.ExpiresIn > 0 {
		sub.ExpiresAt = time.Now().Add(time.Duration(tokens.ExpiresIn) * time.Second)
	}

	if err := s.credentials.UpsertByID(ctx, orgID, cred.ID, models.AnthropicConfig{Subscription: sub}); err != nil {
		return nil, fmt.Errorf("store credential: %w", err)
	}

	s.logger.Info().
		Str("org_id", orgID.String()).
		Str("label", label).
		Bool("has_refresh_token", sub.RefreshToken != "").
		Int("expires_in", tokens.ExpiresIn).
		Msg("Claude Code OAuth completed")

	return &CompleteResponse{AccountType: sub.AccountType}, nil
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
type tokenExchangeResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	AccountType  string `json:"account_type"`
	ExpiresIn    int    `json:"expires_in"`
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

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read token response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token exchange failed (status %d): %s", resp.StatusCode, string(body))
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
func (s *Service) RefreshTokenByID(ctx context.Context, orgID uuid.UUID, credID uuid.UUID) (*models.AnthropicSubscription, error) {
	mu := s.credRefreshMu(credID)
	mu.Lock()
	defer mu.Unlock()

	cred, err := s.credentials.GetByID(ctx, orgID, credID)
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

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read refresh response: %w", err)
	}

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		if strings.Contains(string(body), "refresh_token_reused") {
			s.logger.Warn().Str("cred_id", credID.String()).Msg("refresh token already used by another client; access token may still be valid")
			return nil, fmt.Errorf("refresh token already used by another client")
		}
		if err := s.credentials.UpdateStatusByID(ctx, orgID, credID, "invalid"); err != nil {
			s.logger.Warn().Err(err).Str("cred_id", credID.String()).Msg("failed to update credential status")
		}
		return nil, fmt.Errorf("refresh token revoked (status %d)", resp.StatusCode)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("refresh failed (status %d): %s", resp.StatusCode, string(body))
	}

	var tokenResp tokenExchangeResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("parse refresh response: %w", err)
	}

	newSub := &models.AnthropicSubscription{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		ExpiresAt:    time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second),
		AccountType:  sub.AccountType,
	}
	// Some OAuth servers return the account_type on refresh; prefer the fresh
	// value when present so tier upgrades are reflected.
	if tokenResp.AccountType != "" {
		newSub.AccountType = tokenResp.AccountType
	}
	// Anthropic's refresh sometimes returns an empty refresh_token string,
	// meaning "keep the old one". Avoid clobbering the stored value in that
	// case — otherwise the next refresh has nothing to send.
	if newSub.RefreshToken == "" {
		newSub.RefreshToken = sub.RefreshToken
	}

	if err := s.credentials.UpsertByID(ctx, orgID, credID, models.AnthropicConfig{Subscription: newSub}); err != nil {
		return nil, fmt.Errorf("store refreshed credential: %w", err)
	}

	s.logger.Debug().
		Str("cred_id", credID.String()).
		Msg("Claude Code OAuth token refreshed")

	return newSub, nil
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

	tried := make(map[uuid.UUID]struct{}, maxRoundRobinAttempts)
	var lastErr error

	for attempt := 0; attempt < maxRoundRobinAttempts; attempt++ {
		cred, err := s.credentials.ClaimNextLabeledRoundRobin(ctx, orgID, models.ProviderAnthropic)
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

		refreshed, refreshErr := s.RefreshTokenByID(ctx, orgID, cred.ID)
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
		if statusErr := s.credentials.UpdateStatusByID(ctx, orgID, cred.ID, "invalid"); statusErr != nil {
			s.logger.Warn().Err(statusErr).Str("cred_id", cred.ID.String()).Msg("failed to mark credential invalid")
		}
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
// distort rotation).
func (s *Service) HasActiveSubscription(ctx context.Context, orgID uuid.UUID) (bool, error) {
	if s.credentials == nil {
		return false, nil
	}
	creds, err := s.credentials.ListByProvider(ctx, orgID, models.ProviderAnthropic)
	if err != nil {
		return false, fmt.Errorf("list anthropic credentials: %w", err)
	}
	for _, cred := range creds {
		if cred.Label == "" || cred.Status != "active" {
			continue
		}
		if cfg, ok := cred.Config.(models.AnthropicConfig); ok && cfg.Subscription != nil && cfg.Subscription.AccessToken != "" {
			return true, nil
		}
	}
	return false, nil
}

// Disconnect removes a specific Claude subscription by ID for an org.
// Ordering matches codexauth: DB first, then in-memory state, so a concurrent
// refresh cannot resurrect the row after we "forget" its mutex.
func (s *Service) Disconnect(ctx context.Context, orgID uuid.UUID, credID uuid.UUID) error {
	if s.credentials != nil {
		if err := s.credentials.DisableByID(ctx, orgID, credID); err != nil {
			return err
		}
	}
	s.refreshMu.Delete(credID.String())
	return nil
}

// DisconnectForOrg removes a credential by ID after verifying it belongs to
// the given org. Returns ErrCredentialNotFound if the credential doesn't
// exist, belongs to a different org, or belongs to a different provider.
func (s *Service) DisconnectForOrg(ctx context.Context, orgID uuid.UUID, credID uuid.UUID) error {
	if s.credentials == nil {
		return nil
	}
	exists, err := s.credentials.ExistsForProviderByID(ctx, orgID, credID, models.ProviderAnthropic)
	if err != nil {
		return fmt.Errorf("check credential ownership: %w", err)
	}
	if !exists {
		return ErrCredentialNotFound
	}
	return s.Disconnect(ctx, orgID, credID)
}

// DisconnectAll removes every Claude subscription for the org, leaving any
// Anthropic API-key row (label="") in place.
func (s *Service) DisconnectAll(ctx context.Context, orgID uuid.UUID) error {
	orgPrefix := orgID.String() + ":"
	s.initMu.Range(func(key, _ any) bool {
		if k, ok := key.(string); ok && strings.HasPrefix(k, orgPrefix) {
			s.initMu.Delete(key)
		}
		return true
	})

	if s.credentials == nil {
		return nil
	}

	creds, _ := s.credentials.ListByProvider(ctx, orgID, models.ProviderAnthropic)
	for _, cred := range creds {
		if cred.Label == "" {
			continue // leave the API-key row alone
		}
		s.refreshMu.Delete(cred.ID.String())
	}

	return s.credentials.DisableLabeled(ctx, orgID, models.ProviderAnthropic)
}

// ListSubscriptions returns all connected Claude subscriptions for an org.
// Skips the label="" row (which is the Anthropic API-key credential, not
// a subscription) so the subscriptions list doesn't leak an API key summary.
func (s *Service) ListSubscriptions(ctx context.Context, orgID uuid.UUID) ([]SubscriptionInfo, error) {
	if s.credentials == nil {
		return nil, nil
	}

	creds, err := s.credentials.ListByProvider(ctx, orgID, models.ProviderAnthropic)
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
func isNotFoundError(err error) bool {
	return errors.Is(err, pgx.ErrNoRows) || errors.Is(err, ErrCredentialNotFound)
}

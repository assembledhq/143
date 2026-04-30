// Package codexauth implements the OpenAI Device Code Auth flow for ChatGPT OAuth.
// This allows users to authenticate with their ChatGPT subscription to access
// models like gpt-5.3-codex that are only available via ChatGPT-authenticated sessions.
package codexauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/models"
)

const (
	// DefaultIssuer is the OpenAI auth server base URL.
	DefaultIssuer = "https://auth.openai.com"

	// DefaultClientID is the Codex CLI's public OAuth client_id.
	// Used by the entire ecosystem (Cline, Roo Code, Kilo Code, OpenCode).
	DefaultClientID = "app_EMoamEEZ73f0CkXaXp7hrann"

	// DefaultVerificationURI is the URL where users enter their device code.
	DefaultVerificationURI = "https://auth.openai.com/codex/device"

	// defaultExpiresIn is the default device code expiration in seconds (15 min).
	defaultExpiresIn = 900

	// refreshGrantType is the OAuth 2.0 grant type for token refresh.
	refreshGrantType = "refresh_token"

	// refreshWindow is how far before expiry we proactively refresh.
	refreshWindow = 5 * time.Minute
)

// CredentialStore defines the credential operations needed by the auth service.
type CredentialStore interface {
	Upsert(ctx context.Context, orgID uuid.UUID, cfg models.ProviderConfig) error
	Get(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) (*models.DecryptedCredential, error)
	UpdateStatus(ctx context.Context, orgID uuid.UUID, provider models.ProviderName, status string) error
	Disable(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) error

	// Multi-credential methods for round-robin subscription support.
	UpsertWithLabel(ctx context.Context, orgID uuid.UUID, createdBy *uuid.UUID, label string, cfg models.ProviderConfig) (*uuid.UUID, error)
	InsertPendingAuth(ctx context.Context, orgID uuid.UUID, createdBy *uuid.UUID, label string, cfg models.ProviderConfig) (*uuid.UUID, error)
	GetByID(ctx context.Context, orgID uuid.UUID, id uuid.UUID) (*models.DecryptedCredential, error)
	GetByProviderAndLabel(ctx context.Context, orgID uuid.UUID, provider models.ProviderName, label string) (*models.DecryptedCredential, error)
	ListByProvider(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) ([]models.DecryptedCredential, error)
	ClaimNextRoundRobin(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) (*models.DecryptedCredential, error)
	DisableByID(ctx context.Context, orgID uuid.UUID, id uuid.UUID) error
	UpdateStatusByID(ctx context.Context, orgID uuid.UUID, id uuid.UUID, status string) error
	UpsertByID(ctx context.Context, orgID uuid.UUID, id uuid.UUID, cfg models.ProviderConfig) error
	ExistsForProviderByID(ctx context.Context, orgID uuid.UUID, id uuid.UUID, provider models.ProviderName) (bool, error)
}

// ErrCredentialNotFound is returned when no credential exists for the given org/provider.
var ErrCredentialNotFound = fmt.Errorf("credential not found")

// PendingAuth tracks an in-progress device code auth flow.
type PendingAuth struct {
	DeviceAuthID    string
	UserCode        string
	VerificationURI string
	ExpiresAt       time.Time
	Interval        int        // poll interval in seconds
	LastPollAt      time.Time  // tracks when we last polled OpenAI
	Label           string     // user-provided label for this subscription
	CredentialID    *uuid.UUID // DB credential ID once persisted
}

// DeviceAuthResponse is returned to the caller of InitiateDeviceAuth.
type DeviceAuthResponse struct {
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
}

// AuthStatus represents the current state of a device code auth flow.
type AuthStatus struct {
	Status      string `json:"status"` // "pending", "completed", "expired", "error", "none"
	AccountType string `json:"account_type,omitempty"`
	Message     string `json:"message,omitempty"`
}

// SubscriptionStatus is the public-facing status of a Codex subscription.
type SubscriptionStatus string

const (
	SubscriptionStatusActive      SubscriptionStatus = "active"
	SubscriptionStatusPendingAuth SubscriptionStatus = "pending_auth"
	SubscriptionStatusInvalid     SubscriptionStatus = "invalid"
	SubscriptionStatusDisabled    SubscriptionStatus = "disabled"
)

// SubscriptionInfo is the API-safe summary of a connected Codex subscription.
// Deliberately omits any token material: the access token is a JWT with a
// structurally-predictable prefix, so a "masked" view would leak only the
// high-entropy suffix — exactly the part we want to keep secret. Label +
// CreatedAt are enough for users to disambiguate subscriptions in the UI.
type SubscriptionInfo struct {
	ID          uuid.UUID          `json:"id"`
	Label       string             `json:"label"`
	AccountType string             `json:"account_type,omitempty"`
	Status      SubscriptionStatus `json:"status"`
	LastUsedAt  *time.Time         `json:"last_used_at,omitempty"`
	CreatedBy   *uuid.UUID         `json:"created_by,omitempty"`
	CreatedAt   time.Time          `json:"created_at,omitempty"`
}

// Service handles the OpenAI Device Code Auth flow.
type Service struct {
	credentials CredentialStore
	httpClient  *http.Client
	logger      zerolog.Logger
	issuer      string
	clientID    string
	pending     sync.Map // pendingKey (orgID+label) -> *PendingAuth
	refreshMu   sync.Map // credential ID string -> *sync.Mutex (per-credential refresh lock)
	// initMu entries accumulate per distinct (org, label) pair. Growth is bounded
	// in practice by the number of subscription labels an org ever uses, and
	// entries are reclaimed on DisconnectAll. Cleaning up inside InitiateDeviceAuth
	// would race with concurrent callers doing LoadOrStore on the same key, so we
	// accept the bounded growth rather than introduce a second mutex to guard it.
	initMu sync.Map // pendingKey (orgID+label) -> *sync.Mutex (per-(org,label) init lock)
}

// NewService creates a new Device Code Auth service.
func NewService(credentials CredentialStore, logger zerolog.Logger) *Service {
	return &Service{
		credentials: credentials,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		logger:   logger,
		issuer:   DefaultIssuer,
		clientID: DefaultClientID,
	}
}

// SetHTTPClient replaces the default HTTP client (useful for testing).
func (s *Service) SetHTTPClient(client *http.Client) {
	s.httpClient = client
}

// SetIssuer overrides the auth server URL (useful for testing).
func (s *Service) SetIssuer(issuer string) {
	s.issuer = issuer
}

// pendingKey returns the sync.Map key for pending auth state scoped to org+label.
func pendingKey(orgID uuid.UUID, label string) string {
	return orgID.String() + ":" + label
}

// restorePendingFromDB recovers pending-auth state after a server restart.
// Returns (terminalStatus, nil) when the DB row tells us the flow is already
// complete (active) or unusable (bad config), (nil, restoredPending) when a
// still-valid pending_auth row can resume polling, or (nil, nil) when there
// is nothing to recover and the caller should report "no pending auth flow".
func (s *Service) restorePendingFromDB(ctx context.Context, orgID uuid.UUID, label string) (*AuthStatus, *PendingAuth) {
	if s.credentials == nil {
		return nil, nil
	}
	cred, err := s.credentials.GetByProviderAndLabel(ctx, orgID, models.ProviderOpenAIChatGPT, label)
	if err != nil {
		return nil, nil
	}
	cfg, cfgOk := cred.Config.(models.OpenAIChatGPTConfig)
	switch cred.Status {
	case "active":
		if !cfgOk {
			return &AuthStatus{Status: "error", Message: "invalid credential config"}, nil
		}
		return &AuthStatus{Status: "completed", AccountType: cfg.AccountType}, nil
	case "pending_auth":
		if !cfgOk || cfg.DeviceAuthID == "" || !time.Now().Before(cfg.ExpiresAt) {
			return nil, nil
		}
		interval := cfg.PollInterval
		if interval <= 0 {
			interval = 5
		}
		return nil, &PendingAuth{
			DeviceAuthID:    cfg.DeviceAuthID,
			UserCode:        cfg.UserCode,
			VerificationURI: cfg.VerificationURI,
			ExpiresAt:       cfg.ExpiresAt,
			Interval:        interval,
			Label:           label,
			CredentialID:    &cred.ID,
		}
	}
	return nil, nil
}

// InitiateDeviceAuth starts a new device code auth flow for the given org.
// The label distinguishes multiple subscriptions (e.g. "Team A", "Team B").
// createdBy records which user added the subscription; pass nil if unknown.
func (s *Service) InitiateDeviceAuth(ctx context.Context, orgID uuid.UUID, createdBy *uuid.UUID, label string) (*DeviceAuthResponse, error) {
	// Serialize concurrent init calls for the same (org, label). Otherwise two
	// racing initiations could both reach InsertPendingAuth, with the slower
	// request overwriting the faster one's pending state (or worse, racing
	// against a still-in-flight OpenAI call).
	pKey := pendingKey(orgID, label)
	muVal, _ := s.initMu.LoadOrStore(pKey, &sync.Mutex{})
	mu := muVal.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()

	endpoint := s.issuer + "/api/accounts/deviceauth/usercode"

	reqBody, err := json.Marshal(map[string]string{
		"client_id": s.clientID,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("device auth request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("device auth failed (status %d): %s", resp.StatusCode, string(body))
	}

	var result struct {
		DeviceAuthID    string `json:"device_auth_id"`
		UserCode        string `json:"user_code"`
		VerificationURI string `json:"verification_uri"`
		ExpiresIn       int    `json:"expires_in"`
		Interval        any    `json:"interval"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse device auth response: %w", err)
	}

	interval, err := parsePollInterval(result.Interval)
	if err != nil {
		return nil, fmt.Errorf("parse device auth response interval: %w", err)
	}

	if interval <= 0 {
		interval = 5
	}
	if result.VerificationURI == "" {
		result.VerificationURI = DefaultVerificationURI
	}
	if result.ExpiresIn <= 0 {
		result.ExpiresIn = defaultExpiresIn
	}

	expiresAt := time.Now().Add(time.Duration(result.ExpiresIn) * time.Second)

	// Store pending auth state in memory.
	pending := &PendingAuth{
		DeviceAuthID:    result.DeviceAuthID,
		UserCode:        result.UserCode,
		VerificationURI: result.VerificationURI,
		ExpiresAt:       expiresAt,
		Interval:        interval,
		Label:           label,
	}
	s.pending.Store(pKey, pending)

	// Persist to DB so the pending state survives server restarts.
	// InsertPendingAuth refuses to overwrite a credential that already holds a
	// real access token, so an in-progress re-auth flow can never wipe a working
	// subscription.
	if s.credentials != nil {
		pendingCfg := models.OpenAIChatGPTConfig{
			DeviceAuthID:    result.DeviceAuthID,
			UserCode:        result.UserCode,
			VerificationURI: result.VerificationURI,
			ExpiresAt:       expiresAt,
			PollInterval:    interval,
		}
		credID, err := s.credentials.InsertPendingAuth(ctx, orgID, createdBy, label, pendingCfg)
		if err != nil {
			s.pending.Delete(pKey)
			return nil, fmt.Errorf("persist pending device auth: %w", err)
		}
		pending.CredentialID = credID
	}

	s.logger.Info().
		Str("org_id", orgID.String()).
		Str("user_code", result.UserCode).
		Msg("device code auth initiated")

	return &DeviceAuthResponse{
		UserCode:        result.UserCode,
		VerificationURI: result.VerificationURI,
		ExpiresIn:       result.ExpiresIn,
	}, nil
}

// PollForToken checks whether the user has completed authentication.
// It performs a single poll attempt and returns the status.
// The label identifies which subscription's auth flow to poll.
func (s *Service) PollForToken(ctx context.Context, orgID uuid.UUID, label string) (*AuthStatus, error) {
	pKey := pendingKey(orgID, label)
	val, ok := s.pending.Load(pKey)
	if !ok {
		status, restored := s.restorePendingFromDB(ctx, orgID, label)
		if status != nil {
			return status, nil
		}
		if restored != nil {
			s.pending.Store(pKey, restored)
			val = restored
			ok = true
		}
		if !ok {
			return &AuthStatus{Status: "none", Message: "no pending auth flow"}, nil
		}
	}

	pending := val.(*PendingAuth)

	// Check expiry.
	if time.Now().After(pending.ExpiresAt) {
		s.pending.Delete(pKey)
		return &AuthStatus{Status: "expired", Message: "device code expired, please try again"}, nil
	}

	// Rate-limit: skip the OpenAI call if we polled too recently.
	minInterval := time.Duration(pending.Interval) * time.Second
	if !pending.LastPollAt.IsZero() && time.Since(pending.LastPollAt) < minInterval {
		return &AuthStatus{Status: "pending", Message: "waiting for user to enter code"}, nil
	}
	pending.LastPollAt = time.Now()

	// Poll the token endpoint.
	endpoint := s.issuer + "/api/accounts/deviceauth/token"
	pollBody, err := json.Marshal(map[string]string{
		"device_auth_id": pending.DeviceAuthID,
		"user_code":      pending.UserCode,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal token request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(pollBody))
	if err != nil {
		return nil, fmt.Errorf("create token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token poll request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read token response: %w", err)
	}

	// OpenAI returns 403/404 while the user hasn't entered the code yet.
	// Treat these as "authorization pending" (matches Codex CLI behavior).
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound {
		return &AuthStatus{Status: "pending", Message: "waiting for user to enter code"}, nil
	}

	// Handle other non-success responses (standard OAuth error format).
	if resp.StatusCode != http.StatusOK {
		var errResp struct {
			Error string `json:"error"`
		}
		// Intentionally ignored: if unmarshal fails, errResp.Error stays empty and the default switch case handles it.
		_ = json.Unmarshal(body, &errResp)

		switch errResp.Error {
		case "authorization_pending":
			return &AuthStatus{Status: "pending", Message: "waiting for user to enter code"}, nil
		case "slow_down":
			// Increase poll interval.
			pending.Interval = pending.Interval * 2
			return &AuthStatus{Status: "pending", Message: "waiting for user to enter code"}, nil
		case "expired_token":
			s.pending.Delete(pKey)
			return &AuthStatus{Status: "expired", Message: "device code expired, please try again"}, nil
		case "access_denied":
			s.pending.Delete(pKey)
			return &AuthStatus{Status: "error", Message: "authentication denied by user"}, nil
		default:
			msg := errResp.Error
			if msg == "" {
				msg = fmt.Sprintf("unexpected response (HTTP %d)", resp.StatusCode)
			}
			return &AuthStatus{Status: "error", Message: fmt.Sprintf("auth error: %s", msg)}, nil
		}
	}

	// Success — the device code poll returns an authorization_code + code_verifier
	// that must be exchanged at /oauth/token for the actual access/refresh tokens.
	var pollResp struct {
		Status            string `json:"status"`
		AuthorizationCode string `json:"authorization_code"`
		CodeVerifier      string `json:"code_verifier"`
	}
	if err := json.Unmarshal(body, &pollResp); err != nil {
		return nil, fmt.Errorf("parse poll response: %w", err)
	}

	if pollResp.AuthorizationCode == "" {
		return &AuthStatus{Status: "error", Message: "device auth response missing authorization_code"}, nil
	}

	// Exchange the authorization code for access + refresh tokens (PKCE flow).
	tokenResp, err := s.exchangeAuthCode(ctx, pollResp.AuthorizationCode, pollResp.CodeVerifier)
	if err != nil {
		return &AuthStatus{Status: "error", Message: fmt.Sprintf("token exchange failed: %s", err)}, nil
	}

	storedCfg := models.OpenAIChatGPTConfig{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		IDToken:      tokenResp.IDToken,
	}
	if tokenResp.ExpiresIn > 0 {
		storedCfg.ExpiresAt = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
	}

	// Store credential. If we have a credential ID from the pending state, update
	// by ID to preserve the label. Otherwise fall back to label-based upsert.
	if pending.CredentialID != nil {
		if err := s.credentials.UpsertByID(ctx, orgID, *pending.CredentialID, storedCfg); err != nil {
			return nil, fmt.Errorf("store credential: %w", err)
		}
	} else {
		if _, err := s.credentials.UpsertWithLabel(ctx, orgID, nil, pending.Label, storedCfg); err != nil {
			return nil, fmt.Errorf("store credential: %w", err)
		}
	}

	// Clean up pending state.
	s.pending.Delete(pKey)

	s.logger.Info().
		Str("org_id", orgID.String()).
		Bool("has_refresh_token", tokenResp.RefreshToken != "").
		Int("expires_in", tokenResp.ExpiresIn).
		Msg("ChatGPT OAuth completed successfully")

	return &AuthStatus{
		Status:      "completed",
		AccountType: storedCfg.AccountType,
	}, nil
}

// tokenExchangeResponse holds the parsed tokens from /oauth/token.
type tokenExchangeResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	ExpiresIn    int    `json:"expires_in"`
}

// exchangeAuthCode exchanges an authorization_code + code_verifier for
// access and refresh tokens via the standard OAuth2 PKCE token endpoint.
func (s *Service) exchangeAuthCode(ctx context.Context, authCode, codeVerifier string) (*tokenExchangeResponse, error) {
	endpoint := s.issuer + "/oauth/token"

	reqBody, err := json.Marshal(map[string]string{
		"client_id":     s.clientID,
		"grant_type":    "authorization_code",
		"code":          authCode,
		"code_verifier": codeVerifier,
		"redirect_uri":  s.issuer + "/deviceauth/callback",
	})
	if err != nil {
		return nil, fmt.Errorf("marshal token exchange request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("create token exchange request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token exchange request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read token exchange response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token exchange failed (status %d): %s", resp.StatusCode, string(body))
	}

	var tokenResp tokenExchangeResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("parse token exchange response: %w", err)
	}

	if tokenResp.AccessToken == "" {
		return nil, fmt.Errorf("token exchange returned empty access_token")
	}

	return &tokenResp, nil
}

func parsePollInterval(raw any) (int, error) {
	if raw == nil {
		return 0, nil
	}

	switch v := raw.(type) {
	case float64:
		return int(v), nil
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return 0, fmt.Errorf("invalid interval value %q", v)
		}
		return parsed, nil
	default:
		return 0, fmt.Errorf("unsupported interval type %T", raw)
	}
}

// credRefreshMu returns a per-credential mutex for serializing token refreshes.
// This prevents concurrent requests from both consuming the same refresh
// token at OpenAI, which would cause refresh_token_reused errors.
func (s *Service) credRefreshMu(credID uuid.UUID) *sync.Mutex {
	val, _ := s.refreshMu.LoadOrStore(credID.String(), &sync.Mutex{})
	return val.(*sync.Mutex)
}

// RefreshTokenByID refreshes an expired access token for a specific credential.
// It serializes refreshes per-credential to prevent concurrent calls from consuming
// the same refresh token at OpenAI (which causes refresh_token_reused errors).
func (s *Service) RefreshTokenByID(ctx context.Context, orgID uuid.UUID, credID uuid.UUID) (*models.OpenAIChatGPTConfig, error) {
	mu := s.credRefreshMu(credID)
	mu.Lock()
	defer mu.Unlock()

	cred, err := s.credentials.GetByID(ctx, orgID, credID)
	if err != nil {
		return nil, fmt.Errorf("get credential: %w", err)
	}

	cfg, ok := cred.Config.(models.OpenAIChatGPTConfig)
	if !ok {
		return nil, fmt.Errorf("credential is not OpenAIChatGPTConfig")
	}

	// After acquiring the lock, check if another goroutine already refreshed.
	if !cfg.NeedsRefresh(refreshWindow) && cfg.AccessToken != "" {
		return &cfg, nil
	}

	if cfg.RefreshToken == "" {
		return nil, fmt.Errorf("no refresh token available — user must re-authenticate")
	}

	endpoint := s.issuer + "/oauth/token"
	reqBody, err := json.Marshal(map[string]string{
		"client_id":     s.clientID,
		"grant_type":    refreshGrantType,
		"refresh_token": cfg.RefreshToken,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal refresh request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("create refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

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

	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("parse refresh response: %w", err)
	}

	newCfg := models.OpenAIChatGPTConfig{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		IDToken:      tokenResp.IDToken,
		ExpiresAt:    time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second),
		AccountType:  cfg.AccountType,
	}

	if err := s.credentials.UpsertByID(ctx, orgID, credID, newCfg); err != nil {
		return nil, fmt.Errorf("store refreshed credential: %w", err)
	}

	s.logger.Debug().
		Str("cred_id", credID.String()).
		Msg("ChatGPT OAuth token refreshed")

	return &newCfg, nil
}

// maxRoundRobinAttempts caps how many distinct credentials GetValidToken
// will try before giving up. With healthy round-robin behavior we expect
// to succeed on the first attempt; the cap exists so a multi-subscription
// org with several broken credentials degrades to a clear error instead
// of an unbounded loop.
const maxRoundRobinAttempts = 5

// GetValidToken returns a valid access token using round-robin across all
// active ChatGPT credentials for the org. It auto-refreshes if needed and,
// when a credential's refresh fails AND its cached token has already
// expired, marks that credential invalid and tries the next one in the
// rotation. Returns nil, nil if no usable ChatGPT credential exists.
func (s *Service) GetValidToken(ctx context.Context, orgID uuid.UUID) (*models.OpenAIChatGPTConfig, error) {
	if s.credentials == nil {
		return nil, nil
	}

	tried := make(map[uuid.UUID]struct{}, maxRoundRobinAttempts)
	var lastErr error

	for attempt := 0; attempt < maxRoundRobinAttempts; attempt++ {
		cred, err := s.credentials.ClaimNextRoundRobin(ctx, orgID, models.ProviderOpenAIChatGPT)
		if err != nil {
			if isNotFoundError(err) {
				if lastErr != nil {
					return nil, fmt.Errorf("no usable codex credential: %w", lastErr)
				}
				return nil, nil
			}
			return nil, fmt.Errorf("get credential: %w", err)
		}

		// If round-robin handed us a credential we already tried this call
		// (e.g. only one credential exists, or all others were also marked
		// invalid this iteration), stop — no progress is possible.
		if _, seen := tried[cred.ID]; seen {
			break
		}
		tried[cred.ID] = struct{}{}

		cfg, ok := cred.Config.(models.OpenAIChatGPTConfig)
		if !ok {
			lastErr = fmt.Errorf("credential %s is not OpenAIChatGPTConfig", cred.ID)
			continue
		}

		if cfg.AccessToken == "" {
			lastErr = fmt.Errorf("credential %s has empty access token", cred.ID)
			continue
		}

		if cfg.RefreshToken == "" || !cfg.NeedsRefresh(refreshWindow) {
			return &cfg, nil
		}

		refreshed, refreshErr := s.RefreshTokenByID(ctx, orgID, cred.ID)
		if refreshErr == nil {
			return refreshed, nil
		}
		lastErr = refreshErr

		if !cfg.IsExpired() {
			// Refresh failed but the cached token is still valid — use it.
			s.logger.Warn().Err(refreshErr).Str("cred_id", cred.ID.String()).Msg("token refresh failed; using cached token")
			return &cfg, nil
		}

		// Cached token is expired and we couldn't refresh it. Mark the
		// credential invalid so it stops being claimed in the rotation,
		// then try the next one. RefreshTokenByID may have already done
		// this for some HTTP error paths; the second update is a no-op.
		s.logger.Warn().Err(refreshErr).Str("cred_id", cred.ID.String()).Msg("token refresh failed and cached token expired; marking invalid")
		if statusErr := s.credentials.UpdateStatusByID(ctx, orgID, cred.ID, "invalid"); statusErr != nil {
			s.logger.Warn().Err(statusErr).Str("cred_id", cred.ID.String()).Msg("failed to mark credential invalid")
		}
	}

	if lastErr != nil {
		return nil, fmt.Errorf("no usable codex credential after %d attempts: %w", len(tried), lastErr)
	}
	return nil, nil
}

// Disconnect removes a specific ChatGPT OAuth credential by ID for an org.
// It also cleans up any in-memory pending auth state for this credential.
//
// Ordering matters: the credential is disabled in the DB first, then in-memory
// mutex/pending state is cleaned up. If we deleted the mutex first, a concurrent
// refresh arriving between the Delete and DisableByID calls could acquire a
// fresh mutex, successfully refresh, and UpsertByID the now-disabled row back
// to active — silently resurrecting a credential the user just disconnected.
func (s *Service) Disconnect(ctx context.Context, orgID uuid.UUID, credID uuid.UUID) error {
	if s.credentials != nil {
		if err := s.credentials.DisableByID(ctx, orgID, credID); err != nil {
			return err
		}
	}

	s.refreshMu.Delete(credID.String())

	// Clean up any pending auth entries that reference this credential ID.
	s.pending.Range(func(key, val any) bool {
		if p, ok := val.(*PendingAuth); ok && p.CredentialID != nil && *p.CredentialID == credID {
			s.pending.Delete(key)
		}
		return true
	})

	return nil
}

// DisconnectForOrg removes a credential by ID after verifying it belongs to the given org.
// Returns ErrCredentialNotFound if the credential doesn't exist or belongs to a different
// org. Disconnecting an already-disabled credential is idempotent (returns nil) — this
// matches the user's mental model where clicking "Remove" twice shouldn't error.
func (s *Service) DisconnectForOrg(ctx context.Context, orgID uuid.UUID, credID uuid.UUID) error {
	if s.credentials == nil {
		return nil
	}
	// ExistsForProviderByID includes disabled rows, so this distinguishes
	// "not ours" (cross-org, bogus id, or another provider like an Anthropic
	// API key) from "already disconnected". The provider filter prevents the
	// codex-auth DELETE endpoint from disabling unrelated credentials.
	exists, err := s.credentials.ExistsForProviderByID(ctx, orgID, credID, models.ProviderOpenAIChatGPT)
	if err != nil {
		return fmt.Errorf("check credential ownership: %w", err)
	}
	if !exists {
		return ErrCredentialNotFound
	}
	return s.Disconnect(ctx, orgID, credID)
}

// DisconnectAll removes all ChatGPT OAuth credentials for the given org.
func (s *Service) DisconnectAll(ctx context.Context, orgID uuid.UUID) error {
	// Clean up in-memory pending auth and init mutexes for this org.
	orgPrefix := orgID.String() + ":"
	s.pending.Range(func(key, val any) bool {
		if k, ok := key.(string); ok && strings.HasPrefix(k, orgPrefix) {
			s.pending.Delete(key)
		}
		return true
	})
	s.initMu.Range(func(key, val any) bool {
		if k, ok := key.(string); ok && strings.HasPrefix(k, orgPrefix) {
			s.initMu.Delete(key)
		}
		return true
	})
	// Clean up refresh mutexes for all credentials belonging to this org.
	if s.credentials != nil {
		creds, _ := s.credentials.ListByProvider(ctx, orgID, models.ProviderOpenAIChatGPT)
		for _, cred := range creds {
			s.refreshMu.Delete(cred.ID.String())
		}
	}

	if s.credentials == nil {
		return nil
	}
	return s.credentials.Disable(ctx, orgID, models.ProviderOpenAIChatGPT)
}

// ListSubscriptions returns all connected Codex subscriptions for an org.
func (s *Service) ListSubscriptions(ctx context.Context, orgID uuid.UUID) ([]SubscriptionInfo, error) {
	if s.credentials == nil {
		return nil, nil
	}

	creds, err := s.credentials.ListByProvider(ctx, orgID, models.ProviderOpenAIChatGPT)
	if err != nil {
		return nil, fmt.Errorf("list subscriptions: %w", err)
	}

	var subs []SubscriptionInfo
	for _, cred := range creds {
		cfg, ok := cred.Config.(models.OpenAIChatGPTConfig)
		if !ok {
			continue
		}
		subs = append(subs, SubscriptionInfo{
			ID:          cred.ID,
			Label:       cred.Label,
			AccountType: cfg.AccountType,
			Status:      SubscriptionStatus(cred.Status),
			LastUsedAt:  cred.LastUsedAt,
			CreatedBy:   cred.CreatedBy,
			CreatedAt:   cred.CreatedAt,
		})
	}
	return subs, nil
}

// isNotFoundError reports whether err signals "no matching credential row".
// We rely on pgx's typed sentinel rather than string matching so the check
// keeps working if the wrapper text changes.
func isNotFoundError(err error) bool {
	return errors.Is(err, pgx.ErrNoRows) || errors.Is(err, ErrCredentialNotFound)
}

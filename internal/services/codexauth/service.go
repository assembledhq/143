// Package codexauth implements the OpenAI Device Code Auth flow for ChatGPT OAuth.
// This allows users to authenticate with their ChatGPT subscription to access
// models like gpt-5.3-codex that are only available via ChatGPT-authenticated sessions.
package codexauth

import (
	"bytes"
	"context"
	"encoding/json"
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
}

// ErrCredentialNotFound is returned when no credential exists for the given org/provider.
var ErrCredentialNotFound = fmt.Errorf("credential not found")

// PendingAuth tracks an in-progress device code auth flow.
type PendingAuth struct {
	DeviceAuthID    string
	UserCode        string
	VerificationURI string
	ExpiresAt       time.Time
	Interval        int       // poll interval in seconds
	LastPollAt      time.Time // tracks when we last polled OpenAI
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

// Service handles the OpenAI Device Code Auth flow.
type Service struct {
	credentials CredentialStore
	httpClient  *http.Client
	logger      zerolog.Logger
	issuer      string
	clientID    string
	pending     sync.Map // orgID string -> *PendingAuth
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

// InitiateDeviceAuth starts a new device code auth flow for the given org.
func (s *Service) InitiateDeviceAuth(ctx context.Context, orgID uuid.UUID) (*DeviceAuthResponse, error) {
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
		Interval        int    `json:"interval"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse device auth response: %w", err)
	}

	if result.Interval <= 0 {
		result.Interval = 5
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
		Interval:        result.Interval,
	}
	s.pending.Store(orgID.String(), pending)

	// Persist to DB so the pending state survives server restarts.
	if s.credentials != nil {
		pendingCfg := models.OpenAIChatGPTConfig{
			DeviceAuthID:    result.DeviceAuthID,
			UserCode:        result.UserCode,
			VerificationURI: result.VerificationURI,
			ExpiresAt:       expiresAt,
			PollInterval:    result.Interval,
		}
		if err := s.credentials.Upsert(ctx, orgID, pendingCfg); err != nil {
			s.logger.Warn().Err(err).Msg("failed to persist pending device auth to DB")
		} else {
			_ = s.credentials.UpdateStatus(ctx, orgID, models.ProviderOpenAIChatGPT, "pending_auth")
		}
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
func (s *Service) PollForToken(ctx context.Context, orgID uuid.UUID) (*AuthStatus, error) {
	val, ok := s.pending.Load(orgID.String())
	if !ok {
		// No in-memory state — check DB for persisted state.
		if s.credentials != nil {
			cred, err := s.credentials.Get(ctx, orgID, models.ProviderOpenAIChatGPT)
			if err == nil && cred.Status == "active" {
				cfg, ok := cred.Config.(models.OpenAIChatGPTConfig)
				if !ok {
					return &AuthStatus{Status: "error", Message: "invalid credential config"}, nil
				}
				return &AuthStatus{
					Status:      "completed",
					AccountType: cfg.AccountType,
				}, nil
			}
			// Restore pending auth from DB (survives server restart).
			if err == nil && cred.Status == "pending_auth" {
				cfg, cfgOk := cred.Config.(models.OpenAIChatGPTConfig)
				if cfgOk && cfg.DeviceAuthID != "" && time.Now().Before(cfg.ExpiresAt) {
					restored := &PendingAuth{
						DeviceAuthID:    cfg.DeviceAuthID,
						UserCode:        cfg.UserCode,
						VerificationURI: cfg.VerificationURI,
						ExpiresAt:       cfg.ExpiresAt,
						Interval:        cfg.PollInterval,
					}
					if restored.Interval <= 0 {
						restored.Interval = 5
					}
					s.pending.Store(orgID.String(), restored)
					val = restored
					ok = true
				}
			}
		}
		if !ok {
			return &AuthStatus{Status: "none", Message: "no pending auth flow"}, nil
		}
	}

	pending := val.(*PendingAuth)

	// Check expiry.
	if time.Now().After(pending.ExpiresAt) {
		s.pending.Delete(orgID.String())
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

	// Handle pending/slow_down/error responses.
	if resp.StatusCode != http.StatusOK {
		var errResp struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(body, &errResp)

		switch errResp.Error {
		case "authorization_pending":
			return &AuthStatus{Status: "pending", Message: "waiting for user to enter code"}, nil
		case "slow_down":
			// Increase poll interval.
			pending.Interval = pending.Interval * 2
			return &AuthStatus{Status: "pending", Message: "waiting for user to enter code"}, nil
		case "expired_token":
			s.pending.Delete(orgID.String())
			return &AuthStatus{Status: "expired", Message: "device code expired, please try again"}, nil
		case "access_denied":
			s.pending.Delete(orgID.String())
			return &AuthStatus{Status: "error", Message: "authentication denied by user"}, nil
		default:
			return &AuthStatus{Status: "error", Message: fmt.Sprintf("auth error: %s", errResp.Error)}, nil
		}
	}

	// Success — parse tokens.
	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		TokenType    string `json:"token_type"`
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("parse token response: %w", err)
	}

	// Store the credential.
	cfg := models.OpenAIChatGPTConfig{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		ExpiresAt:    time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second),
	}

	if err := s.credentials.Upsert(ctx, orgID, cfg); err != nil {
		return nil, fmt.Errorf("store credential: %w", err)
	}

	// Clean up pending state.
	s.pending.Delete(orgID.String())

	s.logger.Info().
		Str("org_id", orgID.String()).
		Msg("ChatGPT OAuth completed successfully")

	return &AuthStatus{
		Status:      "completed",
		AccountType: cfg.AccountType,
	}, nil
}

// RefreshToken refreshes an expired access token using the refresh token.
func (s *Service) RefreshToken(ctx context.Context, orgID uuid.UUID) (*models.OpenAIChatGPTConfig, error) {
	cred, err := s.credentials.Get(ctx, orgID, models.ProviderOpenAIChatGPT)
	if err != nil {
		return nil, fmt.Errorf("get credential: %w", err)
	}

	cfg, ok := cred.Config.(models.OpenAIChatGPTConfig)
	if !ok {
		return nil, fmt.Errorf("credential is not OpenAIChatGPTConfig")
	}

	endpoint := s.issuer + "/api/accounts/deviceauth/token"
	form := url.Values{
		"client_id":     {s.clientID},
		"grant_type":    {refreshGrantType},
		"refresh_token": {cfg.RefreshToken},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("create refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

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
		// Refresh token revoked or expired — mark credential as invalid.
		_ = s.credentials.UpdateStatus(ctx, orgID, models.ProviderOpenAIChatGPT, "invalid")
		return nil, fmt.Errorf("refresh token revoked (status %d)", resp.StatusCode)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("refresh failed (status %d): %s", resp.StatusCode, string(body))
	}

	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("parse refresh response: %w", err)
	}

	// Update stored credential.
	newCfg := models.OpenAIChatGPTConfig{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		ExpiresAt:    time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second),
		AccountType:  cfg.AccountType,
	}

	if err := s.credentials.Upsert(ctx, orgID, newCfg); err != nil {
		return nil, fmt.Errorf("store refreshed credential: %w", err)
	}

	s.logger.Debug().
		Str("org_id", orgID.String()).
		Msg("ChatGPT OAuth token refreshed")

	return &newCfg, nil
}

// GetValidToken returns a valid access token, auto-refreshing if needed.
// Returns nil, nil if no ChatGPT OAuth credential exists for this org.
func (s *Service) GetValidToken(ctx context.Context, orgID uuid.UUID) (*models.OpenAIChatGPTConfig, error) {
	if s.credentials == nil {
		return nil, nil
	}

	cred, err := s.credentials.Get(ctx, orgID, models.ProviderOpenAIChatGPT)
	if err != nil {
		// Distinguish "not found" from real errors (DB failures, decryption, etc.).
		if isNotFoundError(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get credential: %w", err)
	}

	if cred.Status != "active" {
		return nil, nil
	}

	cfg, ok := cred.Config.(models.OpenAIChatGPTConfig)
	if !ok {
		return nil, fmt.Errorf("credential is not OpenAIChatGPTConfig")
	}

	// Refresh if expiring within the refresh window.
	if cfg.NeedsRefresh(refreshWindow) {
		refreshed, err := s.RefreshToken(ctx, orgID)
		if err != nil {
			s.logger.Warn().Err(err).Str("org_id", orgID.String()).Msg("token refresh failed")
			// If refresh fails but token is still valid, use it.
			if !cfg.IsExpired() {
				return &cfg, nil
			}
			return nil, fmt.Errorf("token expired and refresh failed: %w", err)
		}
		return refreshed, nil
	}

	return &cfg, nil
}

// Disconnect removes the ChatGPT OAuth credential for the given org.
func (s *Service) Disconnect(ctx context.Context, orgID uuid.UUID) error {
	s.pending.Delete(orgID.String())
	if s.credentials == nil {
		return nil
	}
	return s.credentials.Disable(ctx, orgID, models.ProviderOpenAIChatGPT)
}

// isNotFoundError checks if an error represents a "not found" condition.
// This distinguishes missing credentials from real infrastructure errors.
func isNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "not found") || strings.Contains(msg, "no rows")
}

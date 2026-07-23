package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	githubtelemetry "github.com/assembledhq/143/internal/services/github/telemetry"
)

const (
	defaultGitHubOAuthBaseURL  = "https://github.com"
	githubAppUserRefreshWindow = 5 * time.Minute
)

var (
	ErrGitHubAppUserCredentialMissing = errors.New("github app user credential missing")
	ErrGitHubAppUserAuthorizationLost = errors.New("github app user authorization lost")
)

type appUserCredentialStore interface {
	GetForUser(ctx context.Context, orgID, userID uuid.UUID, provider models.ProviderName) (*models.DecryptedUserCredential, error)
	Upsert(ctx context.Context, userID, orgID uuid.UUID, cfg models.ProviderConfig) error
	Disable(ctx context.Context, orgID, userID uuid.UUID, provider models.ProviderName) error
}

type AppUserAuthService struct {
	credentials   appUserCredentialStore
	clientID      string
	clientSecret  string
	redirectURI   string
	oauthBaseURL  string
	apiBaseURL    string
	httpClient    *http.Client
	logger        zerolog.Logger
	now           func() time.Time
	refreshWindow time.Duration
}

func NewAppUserAuthService(credentials *db.UserCredentialStore, clientID, clientSecret, baseURL string, logger zerolog.Logger) *AppUserAuthService {
	return &AppUserAuthService{
		credentials:   credentials,
		clientID:      clientID,
		clientSecret:  clientSecret,
		redirectURI:   strings.TrimRight(baseURL, "/") + "/api/v1/users/me/github/callback",
		oauthBaseURL:  defaultGitHubOAuthBaseURL,
		apiBaseURL:    defaultGitHubAPI,
		httpClient:    githubtelemetry.NewHTTPClient(15*time.Second, logger),
		logger:        logger,
		now:           time.Now,
		refreshWindow: githubAppUserRefreshWindow,
	}
}

func (s *AppUserAuthService) SetOAuthBaseURL(baseURL string) {
	s.oauthBaseURL = strings.TrimRight(baseURL, "/")
}

func (s *AppUserAuthService) SetAPIBaseURL(baseURL string) {
	s.apiBaseURL = strings.TrimRight(baseURL, "/")
}

func (s *AppUserAuthService) ExchangeCode(ctx context.Context, code string) (*models.GitHubAppUserConfig, error) {
	values := url.Values{
		"client_id":     {s.clientID},
		"client_secret": {s.clientSecret},
		"code":          {code},
	}
	if s.redirectURI != "" {
		values.Set("redirect_uri", s.redirectURI)
	}
	return s.exchangeToken(ctx, values)
}

func (s *AppUserAuthService) HasValidCredential(ctx context.Context, orgID, userID uuid.UUID) (bool, error) {
	_, err := s.GetValidCredential(ctx, orgID, userID)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, ErrGitHubAppUserCredentialMissing) {
		return false, nil
	}
	return false, err
}

func (s *AppUserAuthService) GetValidCredential(ctx context.Context, orgID, userID uuid.UUID) (*models.GitHubAppUserConfig, error) {
	if s.credentials == nil {
		return nil, ErrGitHubAppUserCredentialMissing
	}
	cred, err := s.credentials.GetForUser(ctx, orgID, userID, models.ProviderGitHubAppUser)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrGitHubAppUserCredentialMissing
		}
		return nil, fmt.Errorf("get github app user credential: %w", err)
	}
	cfg, ok := cred.Config.(models.GitHubAppUserConfig)
	if !ok {
		return nil, fmt.Errorf("unexpected github app user credential config type %T", cred.Config)
	}

	refreshed := false
	if cfg.NeedsRefresh(s.refreshWindow) {
		var refreshErr error
		cfg, refreshErr = s.refreshStoredCredential(ctx, orgID, userID, cfg)
		if refreshErr != nil {
			return nil, refreshErr
		}
		refreshed = true
	}

	valid, err := s.validateAccessToken(ctx, cfg.AccessToken)
	if err != nil {
		return nil, err
	}
	if valid {
		return &cfg, nil
	}
	if !refreshed {
		cfg, err = s.refreshStoredCredential(ctx, orgID, userID, cfg)
		if err != nil {
			return nil, err
		}
		valid, err = s.validateAccessToken(ctx, cfg.AccessToken)
		if err != nil {
			return nil, err
		}
		if valid {
			return &cfg, nil
		}
	}
	if disableErr := s.credentials.Disable(ctx, orgID, userID, models.ProviderGitHubAppUser); disableErr != nil {
		s.logger.Warn().Err(disableErr).Str("org_id", orgID.String()).Str("user_id", userID.String()).Msg("failed to disable invalid github app user credential")
	}
	return nil, ErrGitHubAppUserCredentialMissing
}

func (s *AppUserAuthService) refreshStoredCredential(ctx context.Context, orgID, userID uuid.UUID, cfg models.GitHubAppUserConfig) (models.GitHubAppUserConfig, error) {
	if cfg.RefreshToken == "" || cfg.RefreshTokenExpired() {
		if disableErr := s.credentials.Disable(ctx, orgID, userID, models.ProviderGitHubAppUser); disableErr != nil {
			s.logger.Warn().Err(disableErr).Str("org_id", orgID.String()).Str("user_id", userID.String()).Msg("failed to disable expired github app user credential")
		}
		return models.GitHubAppUserConfig{}, ErrGitHubAppUserCredentialMissing
	}
	values := url.Values{
		"client_id":     {s.clientID},
		"client_secret": {s.clientSecret},
		"grant_type":    {"refresh_token"},
		"refresh_token": {cfg.RefreshToken},
	}
	refreshed, err := s.exchangeToken(ctx, values)
	if err != nil {
		if errors.Is(err, ErrGitHubAppUserAuthorizationLost) {
			if disableErr := s.credentials.Disable(ctx, orgID, userID, models.ProviderGitHubAppUser); disableErr != nil {
				s.logger.Warn().Err(disableErr).Str("org_id", orgID.String()).Str("user_id", userID.String()).Msg("failed to disable revoked github app user credential")
			}
			return models.GitHubAppUserConfig{}, ErrGitHubAppUserCredentialMissing
		}
		return models.GitHubAppUserConfig{}, err
	}
	if err := s.credentials.Upsert(ctx, userID, orgID, *refreshed); err != nil {
		return models.GitHubAppUserConfig{}, fmt.Errorf("persist refreshed github app user credential: %w", err)
	}
	return *refreshed, nil
}

func (s *AppUserAuthService) exchangeToken(ctx context.Context, values url.Values) (*models.GitHubAppUserConfig, error) {
	if s.clientID == "" || s.clientSecret == "" {
		return nil, errors.New("github app user auth is not configured")
	}
	ctx = githubtelemetry.WithRequestMetadata(ctx, githubtelemetry.RequestMetadata{
		Kind:     githubtelemetry.RequestKindOAuth,
		AuthType: githubtelemetry.AuthTypeOAuth,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.oauthBaseURL+"/login/oauth/access_token", strings.NewReader(values.Encode()))
	if err != nil {
		return nil, fmt.Errorf("build token exchange request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request github app user token: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read github app user token response: %w", err)
	}

	var parsed struct {
		AccessToken           string `json:"access_token"`
		TokenType             string `json:"token_type"`
		RefreshToken          string `json:"refresh_token"`
		ExpiresIn             int64  `json:"expires_in"`
		RefreshTokenExpiresIn int64  `json:"refresh_token_expires_in"`
		Error                 string `json:"error"`
		ErrorDescription      string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("decode github app user token response: %w", err)
	}
	if resp.StatusCode >= http.StatusBadRequest || parsed.Error != "" {
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusBadRequest {
			return nil, ErrGitHubAppUserAuthorizationLost
		}
		return nil, fmt.Errorf("github app user token exchange failed: %s", strings.TrimSpace(parsed.ErrorDescription))
	}
	if parsed.AccessToken == "" {
		return nil, fmt.Errorf("github app user token response missing access token")
	}

	now := s.now()
	cfg := &models.GitHubAppUserConfig{
		AccessToken:  parsed.AccessToken,
		RefreshToken: parsed.RefreshToken,
		TokenType:    parsed.TokenType,
	}
	if parsed.ExpiresIn > 0 {
		cfg.ExpiresAt = now.Add(time.Duration(parsed.ExpiresIn) * time.Second)
	}
	if parsed.RefreshTokenExpiresIn > 0 {
		cfg.RefreshTokenExpiresAt = now.Add(time.Duration(parsed.RefreshTokenExpiresIn) * time.Second)
	}
	return cfg, nil
}

func (s *AppUserAuthService) validateAccessToken(ctx context.Context, token string) (bool, error) {
	ctx = githubtelemetry.WithRequestMetadata(ctx, githubtelemetry.RequestMetadata{
		Kind:     githubtelemetry.RequestKindAPI,
		AuthType: githubtelemetry.AuthTypeUser,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.apiBaseURL+"/user", nil)
	if err != nil {
		return false, fmt.Errorf("build github user validation request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("request github user validation: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return false, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return false, fmt.Errorf("github user validation failed: %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return true, nil
}

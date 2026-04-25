package github

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type Service struct {
	appID      int64
	privateKey *rsa.PrivateKey
	httpClient *http.Client
	cache      map[int64]*cachedToken
	mu         sync.RWMutex
}

type cachedToken struct {
	Token     string
	ExpiresAt time.Time
}

func NewService(appID int64, privateKeyPEM string) (*Service, error) {
	// Env vars often encode newlines as literal "\n"; convert to real newlines
	// so PEM parsing succeeds.
	privateKeyPEM = strings.ReplaceAll(privateKeyPEM, `\n`, "\n")
	key, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(privateKeyPEM))
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	return &Service{
		appID:      appID,
		privateKey: key,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		cache:      make(map[int64]*cachedToken),
	}, nil
}

func (s *Service) GetInstallationToken(ctx context.Context, installationID int64) (string, error) {
	s.mu.RLock()
	cached, ok := s.cache[installationID]
	s.mu.RUnlock()
	if ok && time.Now().Add(5*time.Minute).Before(cached.ExpiresAt) {
		return cached.Token, nil
	}

	jwtToken, err := s.generateJWT()
	if err != nil {
		return "", err
	}

	token, expiresAt, err := s.exchangeForInstallationToken(ctx, jwtToken, installationID)
	if err != nil {
		return "", err
	}

	s.mu.Lock()
	s.cache[installationID] = &cachedToken{Token: token, ExpiresAt: expiresAt}
	s.mu.Unlock()

	return token, nil
}

func (s *Service) generateJWT() (string, error) {
	now := time.Now()
	claims := jwt.MapClaims{
		"iat": now.Add(-60 * time.Second).Unix(),
		"exp": now.Add(10 * time.Minute).Unix(),
		"iss": s.appID,
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	return token.SignedString(s.privateKey)
}

func (s *Service) exchangeForInstallationToken(ctx context.Context, jwtToken string, installationID int64) (string, time.Time, error) {
	path := fmt.Sprintf("/app/installations/%d/access_tokens", installationID)
	url := "https://api.github.com" + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Authorization", "Bearer "+jwtToken)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := s.httpClient.Do(req) // #nosec G704 -- URL is GitHub API endpoint from config
	if err != nil {
		return "", time.Time{}, fmt.Errorf("request installation token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return "", time.Time{}, &GitHubAPIError{
			Method:     http.MethodPost,
			Path:       path,
			StatusCode: resp.StatusCode,
			Body:       body,
		}
	}

	var result struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", time.Time{}, fmt.Errorf("decode response: %w", err)
	}

	return result.Token, result.ExpiresAt, nil
}

package github

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type Service struct {
	appID        int64
	privateKey   *rsa.PrivateKey
	httpClient   *http.Client
	apiBaseURL   string
	cache        map[int64]*cachedToken
	sandboxCache map[sandboxTokenCacheKey]*cachedToken
	mu           sync.RWMutex
}

type sandboxTokenCacheKey struct {
	InstallationID int64
	RepositoryID   int64
	Action         string
}

type installationTokenRequest struct {
	RepositoryIDs []int64           `json:"repository_ids"`
	Permissions   map[string]string `json:"permissions"`
}

type InstallationPermissions struct {
	Members      string `json:"members"`
	Issues       string `json:"issues"`
	PullRequests string `json:"pull_requests"`
	Statuses     string `json:"statuses"`
}

type InstallationDetails struct {
	ID      int64 `json:"id"`
	Account struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"account"`
	Permissions InstallationPermissions `json:"permissions"`
}

type OrgMember struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
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
		appID:        appID,
		privateKey:   key,
		httpClient:   &http.Client{Timeout: 10 * time.Second},
		apiBaseURL:   "https://api.github.com",
		cache:        make(map[int64]*cachedToken),
		sandboxCache: make(map[sandboxTokenCacheKey]*cachedToken),
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

// GetSandboxInstallationToken returns a repository-bound installation token
// with only the permissions required for the requested sandbox operation.
// In particular, neither profile grants pull_requests:write, so agents cannot
// bypass the server-owned publication workflow by opening a PR directly.
func (s *Service) GetSandboxInstallationToken(ctx context.Context, installationID, repositoryID int64, action string) (string, error) {
	if installationID <= 0 {
		return "", errors.New("sandbox installation ID must be positive")
	}
	if repositoryID <= 0 {
		return "", errors.New("sandbox repository ID must be positive")
	}

	permissions := map[string]string{}
	switch action {
	case "push":
		permissions["contents"] = "write"
		// GitHub requires this separate permission when a pushed commit adds
		// or modifies files under .github/workflows. The App already requests
		// it during installation; include it in the narrowed token without
		// granting any pull-request write authority.
		permissions["workflows"] = "write"
	case "api":
		permissions["contents"] = "read"
		permissions["pull_requests"] = "read"
	default:
		return "", fmt.Errorf("unsupported sandbox GitHub action %q", action)
	}

	key := sandboxTokenCacheKey{InstallationID: installationID, RepositoryID: repositoryID, Action: action}
	s.mu.RLock()
	cached, ok := s.sandboxCache[key]
	s.mu.RUnlock()
	if ok && time.Now().Add(5*time.Minute).Before(cached.ExpiresAt) {
		return cached.Token, nil
	}

	jwtToken, err := s.generateJWT()
	if err != nil {
		return "", err
	}
	token, expiresAt, err := s.exchangeForScopedInstallationToken(ctx, jwtToken, installationID, installationTokenRequest{
		RepositoryIDs: []int64{repositoryID},
		Permissions:   permissions,
	})
	if err != nil {
		return "", err
	}

	s.mu.Lock()
	if s.sandboxCache == nil {
		s.sandboxCache = make(map[sandboxTokenCacheKey]*cachedToken)
	}
	s.sandboxCache[key] = &cachedToken{Token: token, ExpiresAt: expiresAt}
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
	return s.exchangeForInstallationTokenRequest(ctx, jwtToken, installationID, nil)
}

func (s *Service) exchangeForScopedInstallationToken(ctx context.Context, jwtToken string, installationID int64, tokenRequest installationTokenRequest) (string, time.Time, error) {
	return s.exchangeForInstallationTokenRequest(ctx, jwtToken, installationID, &tokenRequest)
}

func (s *Service) exchangeForInstallationTokenRequest(ctx context.Context, jwtToken string, installationID int64, tokenRequest *installationTokenRequest) (string, time.Time, error) {
	path := fmt.Sprintf("/app/installations/%d/access_tokens", installationID)
	url := s.apiURL(path)
	var body io.Reader
	if tokenRequest != nil {
		encoded, err := json.Marshal(tokenRequest)
		if err != nil {
			return "", time.Time{}, fmt.Errorf("encode installation token request: %w", err)
		}
		body = strings.NewReader(string(encoded))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Authorization", "Bearer "+jwtToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	if tokenRequest != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := s.httpClient.Do(req) // #nosec G704 -- URL is GitHub API endpoint from config
	if err != nil {
		return "", time.Time{}, fmt.Errorf("request installation token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		return "", time.Time{}, newGitHubAPIResponseError(http.MethodPost, path, resp)
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

func (s *Service) GetInstallationDetails(ctx context.Context, installationID int64) (InstallationDetails, error) {
	jwtToken, err := s.generateJWT()
	if err != nil {
		return InstallationDetails{}, err
	}
	path := fmt.Sprintf("/app/installations/%d", installationID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.apiURL(path), nil)
	if err != nil {
		return InstallationDetails{}, err
	}
	req.Header.Set("Authorization", "Bearer "+jwtToken)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := s.httpClient.Do(req) // #nosec G704 -- URL is GitHub API endpoint from config
	if err != nil {
		return InstallationDetails{}, fmt.Errorf("request installation details: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return InstallationDetails{}, newGitHubAPIResponseError(http.MethodGet, path, resp)
	}
	var result InstallationDetails
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return InstallationDetails{}, fmt.Errorf("decode installation details: %w", err)
	}
	return result, nil
}

func (s *Service) ListOrgMembers(ctx context.Context, installationID int64, orgLogin string) ([]OrgMember, error) {
	token, err := s.GetInstallationToken(ctx, installationID)
	if err != nil {
		return nil, err
	}
	var members []OrgMember
	nextPath := fmt.Sprintf("/orgs/%s/members?per_page=100", urlPathEscape(orgLogin))
	for nextPath != "" {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.apiURL(nextPath), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/vnd.github+json")
		resp, err := s.httpClient.Do(req) // #nosec G704 -- URL is GitHub API endpoint from config
		if err != nil {
			return nil, fmt.Errorf("request org members: %w", err)
		}
		body, readErr := io.ReadAll(resp.Body)
		closeErr := resp.Body.Close()
		if readErr != nil {
			if closeErr != nil {
				return nil, fmt.Errorf("read org members response: %w", errors.Join(readErr, closeErr))
			}
			return nil, fmt.Errorf("read org members response: %w", readErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("close org members response: %w", closeErr)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, &GitHubAPIError{Method: http.MethodGet, Path: nextPath, StatusCode: resp.StatusCode, Body: body, Header: resp.Header.Clone()}
		}
		var page []OrgMember
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("decode org members: %w", err)
		}
		members = append(members, page...)
		nextPath = parseNextGitHubPath(resp.Header.Get("Link"))
	}
	return members, nil
}

func (s *Service) IsActiveOrgMember(ctx context.Context, installationID int64, orgLogin, username string) (bool, error) {
	token, err := s.GetInstallationToken(ctx, installationID)
	if err != nil {
		return false, err
	}
	path := fmt.Sprintf("/orgs/%s/memberships/%s", urlPathEscape(orgLogin), urlPathEscape(username))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.apiURL(path), nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := s.httpClient.Do(req) // #nosec G704 -- URL is GitHub API endpoint from config
	if err != nil {
		return false, fmt.Errorf("request org membership: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, newGitHubAPIResponseError(http.MethodGet, path, resp)
	}
	var result struct {
		State string `json:"state"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false, fmt.Errorf("decode org membership: %w", err)
	}
	return result.State == "active", nil
}

func newGitHubAPIResponseError(method, path string, resp *http.Response) error {
	body, readErr := io.ReadAll(resp.Body)
	apiErr := &GitHubAPIError{
		Method:     method,
		Path:       path,
		StatusCode: resp.StatusCode,
		Body:       body,
		Header:     resp.Header.Clone(),
	}
	if readErr != nil {
		return errors.Join(apiErr, fmt.Errorf("read GitHub error response: %w", readErr))
	}
	return apiErr
}

func (s *Service) apiURL(path string) string {
	base := strings.TrimRight(s.apiBaseURL, "/")
	if base == "" {
		base = "https://api.github.com"
	}
	return base + path
}

func urlPathEscape(value string) string {
	return url.PathEscape(value)
}

// parseNextGitHubPath extracts only the path+query portion of the "next" Link
// header URL. We strip the scheme and host so callers can prefix the
// configurable apiBaseURL rather than using the literal github.com URL.
func parseNextGitHubPath(link string) string {
	for _, part := range strings.Split(link, ",") {
		sections := strings.Split(part, ";")
		if len(sections) < 2 || !strings.Contains(sections[1], `rel="next"`) {
			continue
		}
		raw := strings.Trim(strings.TrimSpace(sections[0]), "<>")
		if raw == "" {
			continue
		}
		if idx := strings.Index(raw, "://"); idx >= 0 {
			afterScheme := raw[idx+3:]
			if slash := strings.Index(afterScheme, "/"); slash >= 0 {
				return afterScheme[slash:]
			}
		}
		if strings.HasPrefix(raw, "/") {
			return raw
		}
	}
	return ""
}

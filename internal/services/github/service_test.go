package github

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type errorOnCloseBody struct {
	*strings.Reader
	err error
}

func (b errorOnCloseBody) Close() error {
	return b.err
}

func testPrivateKeyPEM(t *testing.T) string {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err, "rsa key generation should not return an error")

	block := &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	}
	return string(pem.EncodeToMemory(block))
}

func TestNewService(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		privateKey string
		expectErr  bool
	}{
		{
			name:       "returns service for valid private key",
			privateKey: testPrivateKeyPEM(t),
		},
		{
			name:       "returns error for invalid private key",
			privateKey: "not-a-private-key",
			expectErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc, err := NewService(123, tt.privateKey)
			if tt.expectErr {
				require.Error(t, err, "NewService should return an error for invalid PEM data")
				return
			}

			require.NoError(t, err, "NewService should not return an error for valid PEM data")
			require.NotNil(t, svc, "NewService should return a non-nil service instance")
			require.NotNil(t, svc.httpClient, "NewService should initialize an HTTP client")
			require.NotNil(t, svc.cache, "NewService should initialize the token cache")
		})
	}
}

func TestServiceObserveRateLimitForTokenResolvesInstallation(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 21, 18, 0, 0, 0, time.UTC)
	store := &rateLimitStoreStub{}
	budget := NewRateBudget(store, zerolog.Nop())
	budget.now = func() time.Time { return now }
	svc := &Service{
		tokenInstallations: map[string]installationTokenMetadata{
			"ghs_installation": {InstallationID: 143, ExpiresAt: now.Add(time.Hour)},
		},
		rateLimitBudget: budget,
	}

	svc.ObserveRateLimitForToken(context.Background(), "ghs_installation", http.StatusOK, "core",
		githubRateHeaders("core", 5000, 4321, now.Add(time.Hour)))

	require.Equal(t, int64(143), store.observation.InstallationID, "token observations should be charged to the issuing installation")
	require.Equal(t, models.GitHubRateLimitResourceCore, store.observation.Resource, "REST responses should update the core budget")
	require.Equal(t, 4321, *store.observation.Remaining, "observer should persist GitHub's exact remaining quota")
}

func TestServiceFetchRateLimitSnapshotParsesResources(t *testing.T) {
	t.Parallel()

	reset := time.Date(2026, 7, 21, 19, 0, 0, 0, time.UTC)
	svc := &Service{
		httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			require.Equal(t, "https://api.github.test/rate_limit", req.URL.String(), "snapshot should use GitHub's rate-limit status endpoint")
			require.Equal(t, "Bearer cached-token", req.Header.Get("Authorization"), "snapshot should authenticate as the installation")
			body := `{"resources":{"core":{"limit":5000,"remaining":700,"reset":` + fmt.Sprint(reset.Unix()) + `},"graphql":{"limit":5000,"remaining":4500,"reset":` + fmt.Sprint(reset.Unix()) + `}}}`
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
		})},
		apiBaseURL:         "https://api.github.test",
		cache:              map[int64]*cachedToken{143: {Token: "cached-token", ExpiresAt: time.Now().Add(time.Hour)}},
		tokenInstallations: make(map[string]installationTokenMetadata),
	}

	observations, err := svc.FetchRateLimitSnapshot(context.Background(), 143)

	require.NoError(t, err, "valid rate-limit status should parse")
	require.Len(t, observations, 2, "snapshot should return each valid resource reported by GitHub")
	require.Equal(t, models.GitHubRateLimitResourceCore, observations[0].Resource, "core quota should be first for deterministic persistence")
	require.Equal(t, 700, *observations[0].Remaining, "snapshot should retain exact core remaining quota")
	require.Equal(t, models.GitHubRateLimitResourceGraphQL, observations[1].Resource, "snapshot should include GraphQL quota")
	require.True(t, reset.Equal(*observations[1].ResetAt), "snapshot should parse GitHub's reset epoch")
}

func TestService_ListOrgMembers_ReturnsBodyCloseError(t *testing.T) {
	t.Parallel()

	closeErr := errors.New("close failed")
	svc := &Service{
		httpClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				require.Equal(t, http.MethodGet, req.Method, "ListOrgMembers should use GET for organization members")
				require.Equal(t, "Bearer cached-token", req.Header.Get("Authorization"), "ListOrgMembers should use the cached installation token")

				return &http.Response{
					StatusCode: http.StatusOK,
					Body: errorOnCloseBody{
						Reader: strings.NewReader(`[{"id":1,"login":"octocat"}]`),
						err:    closeErr,
					},
					Header: make(http.Header),
				}, nil
			}),
		},
		apiBaseURL: "https://api.github.test",
		cache: map[int64]*cachedToken{
			42: {
				Token:     "cached-token",
				ExpiresAt: time.Now().Add(30 * time.Minute),
			},
		},
	}

	members, err := svc.ListOrgMembers(context.Background(), 42, "assembled")

	require.Error(t, err, "ListOrgMembers should return response body close errors")
	require.ErrorIs(t, err, closeErr, "ListOrgMembers should preserve the close error for callers")
	require.Nil(t, members, "ListOrgMembers should not return members when response cleanup fails")
}

func TestServiceListOrgMembersObservesHeaderlessSecondaryLimitBody(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 21, 18, 0, 0, 0, time.UTC)
	store := &rateLimitStoreStub{}
	budget := NewRateBudget(store, zerolog.Nop())
	budget.now = func() time.Time { return now }
	svc := &Service{
		httpClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusForbidden,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"message":"You have exceeded a secondary rate limit"}`)),
			}, nil
		})},
		apiBaseURL:         "https://api.github.test",
		cache:              map[int64]*cachedToken{143: {Token: "cached-token", ExpiresAt: time.Now().Add(time.Hour)}},
		tokenInstallations: make(map[string]installationTokenMetadata),
		rateLimitBudget:    budget,
	}

	_, err := svc.ListOrgMembers(context.Background(), 143, "assembled")

	require.Error(t, err, "secondary limit should remain visible to roster sync")
	require.NotNil(t, store.observation.BlockedUntil, "headerless secondary 403 should install a global block")
	require.Equal(t, now.Add(time.Minute), *store.observation.BlockedUntil, "secondary fallback should block new reviews for one minute")
}

func TestService_GetInstallationToken_UsesCache(t *testing.T) {
	t.Parallel()

	svc := &Service{
		cache: make(map[int64]*cachedToken),
	}
	svc.cache[42] = &cachedToken{
		Token:     "cached-token",
		ExpiresAt: time.Now().Add(30 * time.Minute),
	}

	token, err := svc.GetInstallationToken(context.Background(), 42)
	require.NoError(t, err, "GetInstallationToken should not return an error for a valid cached token")
	require.Equal(t, "cached-token", token, "GetInstallationToken should return the cached token when it is not close to expiry")
}

func TestService_GetInstallationToken_FetchesAndCaches(t *testing.T) {
	t.Parallel()

	svc, err := NewService(143, testPrivateKeyPEM(t))
	require.NoError(t, err, "NewService should create a valid GitHub service")

	callCount := 0
	svc.httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			callCount++
			require.True(t, strings.HasPrefix(req.Header.Get("Authorization"), "Bearer "), "GetInstallationToken should set bearer authorization when exchanging token")
			return &http.Response{
				StatusCode: http.StatusCreated,
				Body: io.NopCloser(strings.NewReader(
					`{"token":"ghs_cached","expires_at":"2030-01-01T00:00:00Z"}`,
				)),
				Header: make(http.Header),
			}, nil
		}),
	}

	first, err := svc.GetInstallationToken(context.Background(), 77)
	require.NoError(t, err, "first GetInstallationToken call should not return an error")
	require.Equal(t, "ghs_cached", first, "first GetInstallationToken call should return the exchanged token")

	second, err := svc.GetInstallationToken(context.Background(), 77)
	require.NoError(t, err, "second GetInstallationToken call should not return an error")
	require.Equal(t, "ghs_cached", second, "second GetInstallationToken call should return the cached token")
	require.Equal(t, 1, callCount, "GetInstallationToken should exchange only once and use cache on subsequent calls")
}

func TestService_GetInstallationToken_PreservesRateLimitHeaders(t *testing.T) {
	t.Parallel()

	header := make(http.Header)
	header.Set("Retry-After", "43")
	header.Set("X-RateLimit-Remaining", "0")
	svc, err := NewService(143, testPrivateKeyPEM(t))
	require.NoError(t, err, "NewService should create a valid GitHub service")
	svc.httpClient = &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusForbidden,
				Body:       io.NopCloser(strings.NewReader(`{"message":"API rate limit exceeded"}`)),
				Header:     header,
			}, nil
		}),
	}

	_, err = svc.GetInstallationToken(context.Background(), 77)

	var apiErr *GitHubAPIError
	require.ErrorAs(t, err, &apiErr, "installation token failure should retain typed GitHub response details")
	require.Equal(t, "43", apiErr.Header.Get("Retry-After"), "installation token failure should preserve GitHub's retry delay")
	require.Equal(t, "0", apiErr.Header.Get("X-RateLimit-Remaining"), "installation token failure should preserve GitHub's remaining budget")
}

func TestService_GetSandboxInstallationToken_ScopesRepositoryAndPermissions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		action              string
		expectedPermissions map[string]string
	}{
		{
			name:                "push can write contents and workflow files",
			action:              "push",
			expectedPermissions: map[string]string{"contents": "write", "workflows": "write"},
		},
		{
			name:                "api can only read contents and pull requests",
			action:              "api",
			expectedPermissions: map[string]string{"contents": "read", "pull_requests": "read"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc, err := NewService(143, testPrivateKeyPEM(t))
			require.NoError(t, err, "NewService should create a service for scoped sandbox tokens")
			callCount := 0
			svc.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				callCount++
				require.Equal(t, http.MethodPost, req.Method, "sandbox token exchange should use POST")
				require.Equal(t, "application/json", req.Header.Get("Content-Type"), "sandbox token exchange should send a JSON permission request")
				var body installationTokenRequest
				require.NoError(t, json.NewDecoder(req.Body).Decode(&body), "sandbox token request body should be valid JSON")
				require.Equal(t, []int64{9876}, body.RepositoryIDs, "sandbox token should be bound to the requested GitHub repository")
				require.Equal(t, tt.expectedPermissions, body.Permissions, "sandbox token should request the exact least-privilege permissions")
				if tt.action == "push" {
					require.NotContains(t, body.Permissions, "pull_requests", "push token should not receive pull request permissions")
				} else {
					require.Equal(t, "read", body.Permissions["pull_requests"], "API token should receive read-only pull request access")
				}
				return &http.Response{
					StatusCode: http.StatusCreated,
					Body: io.NopCloser(strings.NewReader(
						`{"token":"ghs_sandbox","expires_at":"2030-01-01T00:00:00Z"}`,
					)),
					Header: make(http.Header),
				}, nil
			})}

			first, err := svc.GetSandboxInstallationToken(context.Background(), 77, 9876, tt.action)
			require.NoError(t, err, "first sandbox token request should succeed")
			require.Equal(t, "ghs_sandbox", first, "sandbox token request should return GitHub's token")
			second, err := svc.GetSandboxInstallationToken(context.Background(), 77, 9876, tt.action)
			require.NoError(t, err, "cached sandbox token request should succeed")
			require.Equal(t, first, second, "cached sandbox token should match the exchanged token")
			require.Equal(t, 1, callCount, "sandbox token should be cached by installation, repository, and action")
		})
	}
}

func TestService_GetSandboxInstallationToken_RejectsInvalidScope(t *testing.T) {
	t.Parallel()

	svc := &Service{}
	tests := []struct {
		name           string
		installationID int64
		repositoryID   int64
		action         string
	}{
		{name: "missing installation", repositoryID: 1, action: "push"},
		{name: "missing repository", installationID: 1, action: "push"},
		{name: "unknown action", installationID: 1, repositoryID: 2, action: "write"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := svc.GetSandboxInstallationToken(context.Background(), tt.installationID, tt.repositoryID, tt.action)
			require.Error(t, err, "invalid sandbox token scope should be rejected before token exchange")
		})
	}
}

func TestService_GenerateJWT(t *testing.T) {
	t.Parallel()

	svc, err := NewService(143, testPrivateKeyPEM(t))
	require.NoError(t, err, "NewService should create a service for JWT generation")

	token, err := svc.generateJWT()
	require.NoError(t, err, "generateJWT should sign and return a JWT token")
	require.NotEmpty(t, token, "generateJWT should return a non-empty token string")
}

func TestService_ExchangeForInstallationToken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		status      int
		body        string
		expectErr   bool
		errContains string
		expectedTok string
	}{
		{
			name:        "returns token for successful response",
			status:      http.StatusCreated,
			body:        `{"token":"ghs_123","expires_at":"2030-01-01T00:00:00Z"}`,
			expectedTok: "ghs_123",
		},
		{
			name:        "returns error for non-201 response",
			status:      http.StatusUnauthorized,
			body:        `{"message":"bad credentials"}`,
			expectErr:   true,
			errContains: "returned 401",
		},
		{
			name:        "returns error for invalid JSON body",
			status:      http.StatusCreated,
			body:        `not-json`,
			expectErr:   true,
			errContains: "decode response",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc := &Service{
				httpClient: &http.Client{
					Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
						require.Equal(t, http.MethodPost, req.Method, "exchangeForInstallationToken should use POST")
						require.True(t, strings.Contains(req.URL.String(), "/app/installations/99/access_tokens"), "exchangeForInstallationToken should call the installation token endpoint")
						require.Equal(t, "Bearer jwt-token", req.Header.Get("Authorization"), "exchangeForInstallationToken should set Authorization header")

						return &http.Response{
							StatusCode: tt.status,
							Body:       io.NopCloser(strings.NewReader(tt.body)),
							Header:     make(http.Header),
						}, nil
					}),
				},
			}

			token, _, err := svc.exchangeForInstallationToken(context.Background(), "jwt-token", 99)
			if tt.expectErr {
				require.Error(t, err, "exchangeForInstallationToken should return an error for unsuccessful responses")
				require.Contains(t, err.Error(), tt.errContains, "exchangeForInstallationToken should include the expected error context")
				return
			}

			require.NoError(t, err, "exchangeForInstallationToken should not return an error for successful responses")
			require.Equal(t, tt.expectedTok, token, "exchangeForInstallationToken should return the token from the response")
		})
	}
}

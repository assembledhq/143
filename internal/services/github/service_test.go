package github

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

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

package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

type stubAppUserCredentialStore struct {
	getFunc     func(context.Context, uuid.UUID, uuid.UUID, models.ProviderName) (*models.DecryptedUserCredential, error)
	upsertFunc  func(context.Context, uuid.UUID, uuid.UUID, models.ProviderConfig) error
	disableFunc func(context.Context, uuid.UUID, uuid.UUID, models.ProviderName) error
}

func (s *stubAppUserCredentialStore) GetForUser(ctx context.Context, orgID, userID uuid.UUID, provider models.ProviderName) (*models.DecryptedUserCredential, error) {
	return s.getFunc(ctx, orgID, userID, provider)
}

func (s *stubAppUserCredentialStore) Upsert(ctx context.Context, userID, orgID uuid.UUID, cfg models.ProviderConfig) error {
	if s.upsertFunc != nil {
		return s.upsertFunc(ctx, userID, orgID, cfg)
	}
	return nil
}

func (s *stubAppUserCredentialStore) Disable(ctx context.Context, orgID, userID uuid.UUID, provider models.ProviderName) error {
	if s.disableFunc != nil {
		return s.disableFunc(ctx, orgID, userID, provider)
	}
	return nil
}

func TestAppUserAuthService_ExchangeCode(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/login/oauth/access_token", r.URL.Path, "token exchange should hit the GitHub OAuth token endpoint")
		require.NoError(t, r.ParseForm(), "token exchange request should parse as form data")
		require.Equal(t, "client-id", r.Form.Get("client_id"), "token exchange should include the app client id")
		require.Equal(t, "client-secret", r.Form.Get("client_secret"), "token exchange should include the app client secret")
		require.Equal(t, "auth-code", r.Form.Get("code"), "token exchange should include the auth code")
		require.Equal(t, "https://app.143.dev/api/v1/users/me/github/callback", r.Form.Get("redirect_uri"), "token exchange should echo the configured callback URL")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"ghu_test","token_type":"bearer","refresh_token":"ghr_test","expires_in":3600,"refresh_token_expires_in":7200}`))
	}))
	defer server.Close()

	svc := &AppUserAuthService{
		clientID:      "client-id",
		clientSecret:  "client-secret",
		redirectURI:   "https://app.143.dev/api/v1/users/me/github/callback",
		oauthBaseURL:  server.URL,
		httpClient:    server.Client(),
		logger:        zerolog.Nop(),
		now:           func() time.Time { return time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC) },
		refreshWindow: githubAppUserRefreshWindow,
	}

	cfg, err := svc.ExchangeCode(context.Background(), "auth-code")
	require.NoError(t, err, "ExchangeCode should succeed for a valid GitHub response")
	require.Equal(t, "ghu_test", cfg.AccessToken, "ExchangeCode should return the access token")
	require.Equal(t, "ghr_test", cfg.RefreshToken, "ExchangeCode should return the refresh token")
	require.Equal(t, time.Date(2026, 4, 22, 13, 0, 0, 0, time.UTC), cfg.ExpiresAt, "ExchangeCode should convert expires_in to an absolute timestamp")
	require.Equal(t, time.Date(2026, 4, 22, 14, 0, 0, 0, time.UTC), cfg.RefreshTokenExpiresAt, "ExchangeCode should convert refresh_token_expires_in to an absolute timestamp")
}

func TestAppUserAuthService_ExchangeCode_AcceptsNonRefreshingToken(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"ghu_test","token_type":"bearer"}`))
	}))
	defer server.Close()

	svc := &AppUserAuthService{
		clientID:      "client-id",
		clientSecret:  "client-secret",
		redirectURI:   "https://app.143.dev/api/v1/users/me/github/callback",
		oauthBaseURL:  server.URL,
		httpClient:    server.Client(),
		logger:        zerolog.Nop(),
		now:           func() time.Time { return time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC) },
		refreshWindow: githubAppUserRefreshWindow,
	}

	cfg, err := svc.ExchangeCode(context.Background(), "auth-code")
	require.NoError(t, err, "ExchangeCode should accept valid non-refreshing GitHub App user tokens")
	require.Equal(t, "ghu_test", cfg.AccessToken, "ExchangeCode should return the access token")
	require.Empty(t, cfg.RefreshToken, "non-refreshing tokens should not require a refresh token")
}

func TestAppUserAuthService_GetValidCredential_RefreshesExpiredToken(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	upserted := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login/oauth/access_token":
			require.NoError(t, r.ParseForm(), "refresh request should parse as form data")
			require.Equal(t, "refresh_token", r.Form.Get("grant_type"), "refresh should use the refresh_token grant")
			require.Equal(t, "ghr_old", r.Form.Get("refresh_token"), "refresh should use the stored refresh token")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"ghu_new","token_type":"bearer","refresh_token":"ghr_new","expires_in":3600,"refresh_token_expires_in":7200}`))
		case "/user":
			require.Equal(t, "Bearer ghu_new", r.Header.Get("Authorization"), "validation should use the refreshed access token")
			_, _ = w.Write([]byte(`{"login":"alice"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	store := &stubAppUserCredentialStore{
		getFunc: func(context.Context, uuid.UUID, uuid.UUID, models.ProviderName) (*models.DecryptedUserCredential, error) {
			return &models.DecryptedUserCredential{
				Config: models.GitHubAppUserConfig{
					AccessToken:           "ghu_old",
					RefreshToken:          "ghr_old",
					ExpiresAt:             time.Now().Add(-time.Minute),
					RefreshTokenExpiresAt: time.Now().Add(time.Hour),
				},
			}, nil
		},
		upsertFunc: func(_ context.Context, gotUserID, gotOrgID uuid.UUID, cfg models.ProviderConfig) error {
			upserted = true
			require.Equal(t, userID, gotUserID, "refresh should persist the credential for the same user")
			require.Equal(t, orgID, gotOrgID, "refresh should persist the credential for the same org")
			gotCfg, ok := cfg.(models.GitHubAppUserConfig)
			require.True(t, ok, "refresh should persist a GitHubAppUserConfig")
			require.Equal(t, "ghu_new", gotCfg.AccessToken, "refresh should persist the refreshed access token")
			return nil
		},
	}

	svc := &AppUserAuthService{
		credentials:   store,
		clientID:      "client-id",
		clientSecret:  "client-secret",
		oauthBaseURL:  server.URL,
		apiBaseURL:    server.URL,
		httpClient:    server.Client(),
		logger:        zerolog.Nop(),
		now:           time.Now,
		refreshWindow: githubAppUserRefreshWindow,
	}

	cfg, err := svc.GetValidCredential(context.Background(), orgID, userID)
	require.NoError(t, err, "GetValidCredential should refresh expired tokens")
	require.Equal(t, "ghu_new", cfg.AccessToken, "GetValidCredential should return the refreshed access token")
	require.True(t, upserted, "GetValidCredential should persist refreshed credentials")
}

func TestAppUserAuthService_HasValidCredential_DisablesRevokedCredential(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	disabled := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user":
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
		case "/login/oauth/access_token":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"expired refresh token"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	store := &stubAppUserCredentialStore{
		getFunc: func(context.Context, uuid.UUID, uuid.UUID, models.ProviderName) (*models.DecryptedUserCredential, error) {
			return &models.DecryptedUserCredential{
				Config: models.GitHubAppUserConfig{
					AccessToken:           "ghu_old",
					RefreshToken:          "ghr_old",
					ExpiresAt:             time.Now().Add(time.Hour),
					RefreshTokenExpiresAt: time.Now().Add(2 * time.Hour),
				},
			}, nil
		},
		disableFunc: func(_ context.Context, gotOrgID, gotUserID uuid.UUID, provider models.ProviderName) error {
			disabled = true
			require.Equal(t, orgID, gotOrgID, "disable should target the same org")
			require.Equal(t, userID, gotUserID, "disable should target the same user")
			require.Equal(t, models.ProviderGitHubAppUser, provider, "disable should target the github_app_user provider")
			return nil
		},
	}

	svc := &AppUserAuthService{
		credentials:   store,
		clientID:      "client-id",
		clientSecret:  "client-secret",
		oauthBaseURL:  server.URL,
		apiBaseURL:    server.URL,
		httpClient:    server.Client(),
		logger:        zerolog.Nop(),
		now:           time.Now,
		refreshWindow: githubAppUserRefreshWindow,
	}

	ok, err := svc.HasValidCredential(context.Background(), orgID, userID)
	require.NoError(t, err, "HasValidCredential should treat revoked credentials as disconnected, not as a hard error")
	require.False(t, ok, "HasValidCredential should return false for revoked credentials")
	require.True(t, disabled, "HasValidCredential should disable revoked stored credentials")
}

func TestAppUserAuthService_GetValidCredential_MissingCredential(t *testing.T) {
	t.Parallel()

	svc := &AppUserAuthService{
		credentials: &stubAppUserCredentialStore{
			getFunc: func(context.Context, uuid.UUID, uuid.UUID, models.ProviderName) (*models.DecryptedUserCredential, error) {
				return nil, pgx.ErrNoRows
			},
		},
		logger:        zerolog.Nop(),
		now:           time.Now,
		refreshWindow: githubAppUserRefreshWindow,
	}

	_, err := svc.GetValidCredential(context.Background(), uuid.New(), uuid.New())
	require.ErrorIs(t, err, ErrGitHubAppUserCredentialMissing, "missing credential should map to ErrGitHubAppUserCredentialMissing")
}

func TestNewAppUserAuthService_ConfiguresDefaultEndpoints(t *testing.T) {
	t.Parallel()

	svc := NewAppUserAuthService(nil, "client-id", "client-secret", "https://app.143.dev/", zerolog.Nop())
	require.Equal(t, "https://app.143.dev/api/v1/users/me/github/callback", svc.redirectURI, "constructor should derive the callback URL from the base URL")
	require.Equal(t, defaultGitHubOAuthBaseURL, svc.oauthBaseURL, "constructor should default the OAuth base URL")
	require.Equal(t, defaultGitHubAPI, svc.apiBaseURL, "constructor should default the API base URL")

	svc.SetOAuthBaseURL("https://github.example.com/")
	svc.SetAPIBaseURL("https://api.github.example.com/")
	require.Equal(t, "https://github.example.com", svc.oauthBaseURL, "SetOAuthBaseURL should trim trailing slashes")
	require.Equal(t, "https://api.github.example.com", svc.apiBaseURL, "SetAPIBaseURL should trim trailing slashes")
}

func TestAppUserAuthService_HasValidCredential_PropagatesUnexpectedErrors(t *testing.T) {
	t.Parallel()

	svc := &AppUserAuthService{
		credentials: &stubAppUserCredentialStore{
			getFunc: func(context.Context, uuid.UUID, uuid.UUID, models.ProviderName) (*models.DecryptedUserCredential, error) {
				return nil, context.DeadlineExceeded
			},
		},
		logger:        zerolog.Nop(),
		now:           time.Now,
		refreshWindow: githubAppUserRefreshWindow,
	}

	ok, err := svc.HasValidCredential(context.Background(), uuid.New(), uuid.New())
	require.False(t, ok, "HasValidCredential should report false when the lookup fails")
	require.Error(t, err, "HasValidCredential should propagate unexpected store failures")
}

func TestAppUserAuthService_GetValidCredential_ErrorPaths(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()

	tests := []struct {
		name      string
		setup     func() *AppUserAuthService
		expectErr error
		errSubstr string
	}{
		{
			name: "missing credential store",
			setup: func() *AppUserAuthService {
				return &AppUserAuthService{logger: zerolog.Nop(), now: time.Now, refreshWindow: githubAppUserRefreshWindow}
			},
			expectErr: ErrGitHubAppUserCredentialMissing,
		},
		{
			name: "unexpected config type",
			setup: func() *AppUserAuthService {
				return &AppUserAuthService{
					credentials: &stubAppUserCredentialStore{
						getFunc: func(context.Context, uuid.UUID, uuid.UUID, models.ProviderName) (*models.DecryptedUserCredential, error) {
							return &models.DecryptedUserCredential{Config: models.GitHubOAuthConfig{AccessToken: "token"}}, nil
						},
					},
					logger:        zerolog.Nop(),
					now:           time.Now,
					refreshWindow: githubAppUserRefreshWindow,
				}
			},
			errSubstr: "unexpected github app user credential config type",
		},
		{
			name: "validation request error",
			setup: func() *AppUserAuthService {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusInternalServerError)
					_, _ = w.Write([]byte("boom"))
				}))
				t.Cleanup(server.Close)
				return &AppUserAuthService{
					credentials: &stubAppUserCredentialStore{
						getFunc: func(context.Context, uuid.UUID, uuid.UUID, models.ProviderName) (*models.DecryptedUserCredential, error) {
							return &models.DecryptedUserCredential{Config: models.GitHubAppUserConfig{AccessToken: "ghu", ExpiresAt: time.Now().Add(time.Hour)}}, nil
						},
					},
					apiBaseURL:    server.URL,
					httpClient:    server.Client(),
					logger:        zerolog.Nop(),
					now:           time.Now,
					refreshWindow: githubAppUserRefreshWindow,
				}
			},
			errSubstr: "github user validation failed",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := tt.setup().GetValidCredential(context.Background(), orgID, userID)
			if tt.expectErr != nil {
				require.ErrorIs(t, err, tt.expectErr, "GetValidCredential should return the expected sentinel error")
				return
			}
			require.Error(t, err, "GetValidCredential should fail for this error path")
			require.Contains(t, err.Error(), tt.errSubstr, "GetValidCredential should include the expected context")
		})
	}
}

func TestAppUserAuthService_RefreshStoredCredential_ErrorPaths(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()

	t.Run("missing refresh token disables credential", func(t *testing.T) {
		t.Parallel()
		disabled := false
		svc := &AppUserAuthService{
			credentials: &stubAppUserCredentialStore{
				disableFunc: func(context.Context, uuid.UUID, uuid.UUID, models.ProviderName) error {
					disabled = true
					return nil
				},
			},
			logger: zerolog.Nop(),
		}

		_, err := svc.refreshStoredCredential(context.Background(), orgID, userID, models.GitHubAppUserConfig{})
		require.ErrorIs(t, err, ErrGitHubAppUserCredentialMissing, "refreshStoredCredential should reject missing refresh tokens")
		require.True(t, disabled, "refreshStoredCredential should disable unusable stored credentials")
	})

	t.Run("authorization lost disables credential", func(t *testing.T) {
		t.Parallel()
		disabled := false
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"revoked"}`))
		}))
		defer server.Close()

		svc := &AppUserAuthService{
			credentials: &stubAppUserCredentialStore{
				disableFunc: func(context.Context, uuid.UUID, uuid.UUID, models.ProviderName) error {
					disabled = true
					return nil
				},
			},
			clientID:      "client-id",
			clientSecret:  "client-secret",
			oauthBaseURL:  server.URL,
			httpClient:    server.Client(),
			logger:        zerolog.Nop(),
			now:           time.Now,
			refreshWindow: githubAppUserRefreshWindow,
		}

		_, err := svc.refreshStoredCredential(context.Background(), orgID, userID, models.GitHubAppUserConfig{
			AccessToken:           "ghu",
			RefreshToken:          "ghr",
			RefreshTokenExpiresAt: time.Now().Add(time.Hour),
		})
		require.ErrorIs(t, err, ErrGitHubAppUserCredentialMissing, "authorization loss should map to missing credential")
		require.True(t, disabled, "authorization loss should disable the stored credential")
	})

	t.Run("upsert failure returns error", func(t *testing.T) {
		t.Parallel()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"ghu_new","refresh_token":"ghr_new","token_type":"bearer","expires_in":3600}`))
		}))
		defer server.Close()

		svc := &AppUserAuthService{
			credentials: &stubAppUserCredentialStore{
				upsertFunc: func(context.Context, uuid.UUID, uuid.UUID, models.ProviderConfig) error {
					return context.DeadlineExceeded
				},
			},
			clientID:      "client-id",
			clientSecret:  "client-secret",
			oauthBaseURL:  server.URL,
			httpClient:    server.Client(),
			logger:        zerolog.Nop(),
			now:           time.Now,
			refreshWindow: githubAppUserRefreshWindow,
		}

		_, err := svc.refreshStoredCredential(context.Background(), orgID, userID, models.GitHubAppUserConfig{
			AccessToken:           "ghu",
			RefreshToken:          "ghr",
			RefreshTokenExpiresAt: time.Now().Add(time.Hour),
		})
		require.Error(t, err, "refreshStoredCredential should return persistence failures")
		require.Contains(t, err.Error(), "persist refreshed github app user credential", "refreshStoredCredential should include upsert context")
	})
}

func TestAppUserAuthService_ExchangeCode_ErrorPaths(t *testing.T) {
	t.Parallel()

	t.Run("not configured", func(t *testing.T) {
		t.Parallel()
		svc := &AppUserAuthService{}
		_, err := svc.ExchangeCode(context.Background(), "auth-code")
		require.Error(t, err, "ExchangeCode should fail when client credentials are missing")
	})

	t.Run("authorization lost", func(t *testing.T) {
		t.Parallel()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"bad_verification_code","error_description":"expired code"}`))
		}))
		defer server.Close()

		svc := &AppUserAuthService{
			clientID:      "client-id",
			clientSecret:  "client-secret",
			oauthBaseURL:  server.URL,
			httpClient:    server.Client(),
			logger:        zerolog.Nop(),
			now:           time.Now,
			refreshWindow: githubAppUserRefreshWindow,
		}
		_, err := svc.ExchangeCode(context.Background(), "auth-code")
		require.ErrorIs(t, err, ErrGitHubAppUserAuthorizationLost, "ExchangeCode should map revoked auth codes to authorization lost")
	})

	t.Run("missing access token", func(t *testing.T) {
		t.Parallel()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"token_type":"bearer"}`))
		}))
		defer server.Close()

		svc := &AppUserAuthService{
			clientID:      "client-id",
			clientSecret:  "client-secret",
			oauthBaseURL:  server.URL,
			httpClient:    server.Client(),
			logger:        zerolog.Nop(),
			now:           time.Now,
			refreshWindow: githubAppUserRefreshWindow,
		}
		_, err := svc.ExchangeCode(context.Background(), "auth-code")
		require.Error(t, err, "ExchangeCode should require an access token in the response")
		require.Contains(t, err.Error(), "missing access token", "ExchangeCode should surface the malformed response")
	})
}

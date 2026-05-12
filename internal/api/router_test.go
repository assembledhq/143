package api

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/assembledhq/143/internal/cache"
	"github.com/assembledhq/143/internal/config"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/services/claudecodeauth"
	"github.com/assembledhq/143/internal/services/codexauth"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestNewRouter_EncryptionKeyValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		masterKey string
		expectErr bool
	}{
		{
			name:      "returns error when encryption key is too short",
			masterKey: "short",
			expectErr: true,
		},
		{
			name:      "allows startup when encryption key is unset",
			masterKey: "",
			expectErr: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := &config.Config{EncryptionMasterKey: tt.masterKey}
			codexSvc := codexauth.NewService(nil, zerolog.Nop())
			claudeSvc := claudecodeauth.NewService(nil, zerolog.Nop())
			router, _, _, _, _, err := NewRouter(cfg, nil, zerolog.Nop(), nil, codexSvc, claudeSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			if tt.expectErr {
				require.Error(t, err, "NewRouter should return an error when encryption key is invalid")
				require.Nil(t, router, "NewRouter should not construct a router with an invalid encryption key")
				return
			}

			require.NoError(t, err, "NewRouter should not return an error when encryption key is unset")
			require.NotNil(t, router, "NewRouter should construct a router when encryption key is unset")
		})
	}
}

func TestSessionThreadPatchRouteIsBuilderWorkflowOnly(t *testing.T) {
	t.Parallel()

	source, err := os.ReadFile("router.go")
	require.NoError(t, err, "router.go should be readable for route grouping regression test")

	readGroupStart := strings.Index(string(source), `RequireRole("admin", "builder", "member", "viewer")`)
	builderGroupStart := strings.Index(string(source), `RequireRole("admin", "builder", "member")`)
	memberWriteGroupStart := strings.Index(string(source), `RequireRole("admin", "member")`)
	patchRoute := strings.Index(string(source), `r.Patch("/api/v1/sessions/{id}/threads/{tid}", sessionThreadHandler.UpdateThread)`)

	require.NotEqual(t, -1, readGroupStart, "router should still define an all-roles readable route group")
	require.NotEqual(t, -1, builderGroupStart, "router should still define a builder workflow route group")
	require.NotEqual(t, -1, memberWriteGroupStart, "router should still define an admin/member-only route group")
	require.NotEqual(t, -1, patchRoute, "thread update PATCH route should be registered")
	require.Greater(t, patchRoute, builderGroupStart, "thread update PATCH route should live in the builder workflow group")
	require.Less(t, patchRoute, memberWriteGroupStart, "thread update PATCH route should not be widened into the admin/member-only settings group")
	require.Greater(t, builderGroupStart, readGroupStart, "builder workflow route group should follow the all-roles readable group")
}

func testRouterPrivateKeyPEM(t *testing.T) string {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err, "rsa key generation should not return an error")

	block := &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	}
	return string(pem.EncodeToMemory(block))
}

func TestNewRouter_GitHubAppConfigBuildsRouter(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		GitHubAppID:         143,
		GitHubAppPrivateKey: testRouterPrivateKeyPEM(t),
	}
	codexSvc := codexauth.NewService(nil, zerolog.Nop())
	claudeSvc := claudecodeauth.NewService(nil, zerolog.Nop())

	router, _, _, _, _, err := NewRouter(cfg, nil, zerolog.Nop(), nil, codexSvc, claudeSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	require.NoError(t, err, "NewRouter should build successfully when GitHub App credentials are valid")
	require.NotNil(t, router, "NewRouter should construct a router when GitHub App credentials are valid")
}

func TestNewRouter_WithRedisWiringBuildsRouter(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{}
	codexSvc := codexauth.NewService(nil, zerolog.Nop())
	claudeSvc := claudecodeauth.NewService(nil, zerolog.Nop())

	router, _, _, _, _, err := NewRouter(cfg, nil, zerolog.Nop(), nil, codexSvc, claudeSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, &cache.Client{}, &cache.SessionStreams{})
	require.NoError(t, err, "router construction should accept optional Redis dependencies")
	require.NotNil(t, router, "router should still be constructed with Redis wiring enabled")
}

func TestResolveRouterCodingCredentialStoreUsesSharedStore(t *testing.T) {
	t.Parallel()

	shared := db.NewCodingCredentialStore(nil, nil)
	got := resolveRouterCodingCredentialStore(nil, nil, shared)

	require.Same(t, shared, got, "router should reuse the process-level coding credential store when one is supplied")
}

func TestResolveRouterCodingCredentialStoreCreatesFallback(t *testing.T) {
	t.Parallel()

	got := resolveRouterCodingCredentialStore(nil, nil, nil)

	require.NotNil(t, got, "router should create a coding credential store when no shared store is supplied")
}

func TestNewRouter_BuildsWithoutOptionalReviewWiring(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{}
	codexSvc := codexauth.NewService(nil, zerolog.Nop())
	claudeSvc := claudecodeauth.NewService(nil, zerolog.Nop())

	router, _, _, _, _, err := NewRouter(cfg, nil, zerolog.Nop(), nil, codexSvc, claudeSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	require.NoError(t, err, "NewRouter should still construct the router without session-review wiring")
	require.NotNil(t, router, "NewRouter should still construct the router without session-review wiring")
}

func TestNewRouter_InternalPreviewRoutesSkipGlobalBodyLimit(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Mode:          "worker",
		NodeID:        "worker-a",
		SessionSecret: "test-session-secret",
	}
	codexSvc := codexauth.NewService(nil, zerolog.Nop())
	claudeSvc := claudecodeauth.NewService(nil, zerolog.Nop())

	router, _, _, _, _, err := NewRouter(cfg, nil, zerolog.Nop(), nil, codexSvc, claudeSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	require.NoError(t, err, "NewRouter should build successfully for worker preview route tests")
	require.NotNil(t, router, "NewRouter should construct a router for worker preview route tests")

	req := httptest.NewRequest(http.MethodPost, "/internal/preview/start", bytes.NewReader(bytes.Repeat([]byte("a"), 2<<20)))
	req.RemoteAddr = "10.0.0.10:1234"
	rr := httptest.NewRecorder()

	router.ServeHTTP(rr, req)

	require.Equal(t, http.StatusUnauthorized, rr.Code, "internal preview routes should reach preview auth instead of the global body limit")
}

func TestNewRouter_InternalPreviewRoutesSkipGlobalRateLimit(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Mode:          "worker",
		NodeID:        "worker-a",
		SessionSecret: "test-session-secret",
	}
	codexSvc := codexauth.NewService(nil, zerolog.Nop())
	claudeSvc := claudecodeauth.NewService(nil, zerolog.Nop())

	router, _, _, _, _, err := NewRouter(cfg, nil, zerolog.Nop(), nil, codexSvc, claudeSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	require.NoError(t, err, "NewRouter should build successfully for worker preview route tests")
	require.NotNil(t, router, "NewRouter should construct a router for worker preview route tests")

	for i := 0; i < 25; i++ {
		req := httptest.NewRequest(http.MethodPost, "/internal/preview/start", http.NoBody)
		req.RemoteAddr = "10.0.0.10:1234"
		rr := httptest.NewRecorder()

		router.ServeHTTP(rr, req)

		require.Equal(t, http.StatusUnauthorized, rr.Code, "internal preview routes should bypass the global IP limiter on every request")
	}
}

package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

const testCSRFKey = "test-csrf-signing-key-32bytes!!"

func mustGenerateToken(t *testing.T, key string) string {
	t.Helper()

	token, err := GenerateSignedToken([]byte(key))
	require.NoError(t, err, "GenerateSignedToken should create a signed token")

	return token
}

func TestCSRF(t *testing.T) {
	t.Parallel()

	validToken := mustGenerateToken(t, testCSRFKey)
	otherValidToken := mustGenerateToken(t, testCSRFKey)
	emptyKeyToken := mustGenerateToken(t, "")

	parts := strings.SplitN(validToken, ".", 2)
	tamperedToken := parts[0] + ".deadbeef"

	tests := []struct {
		name               string
		useEmptySigningKey bool
		method             string
		url                string
		forwardedProto     string
		cookie             string
		header             string
		authHeader         string
		expectedCode       int
		expectCookie       bool
		cookieSecure       bool
	}{
		{
			name:         "GET sets non-secure cookie for HTTP",
			method:       http.MethodGet,
			expectedCode: http.StatusOK,
			expectCookie: true,
			cookieSecure: false,
		},
		{
			name:         "GET over HTTPS sets secure cookie",
			method:       http.MethodGet,
			url:          "https://example.com/",
			expectedCode: http.StatusOK,
			expectCookie: true,
			cookieSecure: true,
		},
		{
			name:           "GET with forwarded HTTPS sets secure cookie",
			method:         http.MethodGet,
			forwardedProto: "https",
			expectedCode:   http.StatusOK,
			expectCookie:   true,
			cookieSecure:   true,
		},
		{
			name:         "GET with valid cookie does not set a new cookie",
			method:       http.MethodGet,
			cookie:       validToken,
			expectedCode: http.StatusOK,
			expectCookie: false,
		},
		{
			name:         "HEAD passes through",
			method:       http.MethodHead,
			expectedCode: http.StatusOK,
			expectCookie: true,
			cookieSecure: false,
		},
		{
			name:         "OPTIONS passes through",
			method:       http.MethodOptions,
			expectedCode: http.StatusOK,
			expectCookie: true,
			cookieSecure: false,
		},
		{
			name:         "POST without cookie returns 403",
			method:       http.MethodPost,
			header:       validToken,
			expectedCode: http.StatusForbidden,
		},
		{
			name:         "POST without header returns 403",
			method:       http.MethodPost,
			cookie:       validToken,
			expectedCode: http.StatusForbidden,
		},
		{
			name:         "POST with mismatched cookie and header returns 403",
			method:       http.MethodPost,
			cookie:       validToken,
			header:       otherValidToken,
			expectedCode: http.StatusForbidden,
		},
		{
			name:         "POST with valid matching cookie and header returns 200",
			method:       http.MethodPost,
			cookie:       validToken,
			header:       validToken,
			expectedCode: http.StatusOK,
		},
		{
			name:         "POST with tampered signature returns 403",
			method:       http.MethodPost,
			cookie:       tamperedToken,
			header:       tamperedToken,
			expectedCode: http.StatusForbidden,
		},
		{
			name:         "POST with malformed token returns 403",
			method:       http.MethodPost,
			cookie:       "invalid-token",
			header:       "invalid-token",
			expectedCode: http.StatusForbidden,
		},
		{
			name:         "POST with Authorization Bearer header skips CSRF",
			method:       http.MethodPost,
			authHeader:   "Bearer some-api-token",
			expectedCode: http.StatusOK,
		},
		{
			name:         "POST with non-Bearer Authorization header still checks CSRF",
			method:       http.MethodPost,
			authHeader:   "Basic abc123",
			expectedCode: http.StatusForbidden,
		},
		{
			name:               "empty signing key still works functionally",
			useEmptySigningKey: true,
			method:             http.MethodPost,
			cookie:             emptyKeyToken,
			header:             emptyKeyToken,
			expectedCode:       http.StatusOK,
		},
		{
			name:         "PATCH with valid matching cookie and header returns 200",
			method:       http.MethodPatch,
			cookie:       validToken,
			header:       validToken,
			expectedCode: http.StatusOK,
		},
		{
			name:         "DELETE with valid matching cookie and header returns 200",
			method:       http.MethodDelete,
			cookie:       validToken,
			header:       validToken,
			expectedCode: http.StatusOK,
		},
		{
			name:         "PUT with valid matching cookie and header returns 200",
			method:       http.MethodPut,
			cookie:       validToken,
			header:       validToken,
			expectedCode: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			signingKey := testCSRFKey
			if tt.useEmptySigningKey {
				signingKey = ""
			}

			handler := CSRF(signingKey, zerolog.Nop())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))

			targetURL := tt.url
			if targetURL == "" {
				targetURL = "http://example.com/"
			}
			req := httptest.NewRequest(tt.method, targetURL, nil)
			if tt.forwardedProto != "" {
				req.Header.Set("X-Forwarded-Proto", tt.forwardedProto)
			}
			if tt.cookie != "" {
				req.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: tt.cookie})
			}
			if tt.header != "" {
				req.Header.Set(CSRFHeaderName, tt.header)
			}
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}

			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			require.Equal(t, tt.expectedCode, w.Code, "middleware should return expected status code")

			if tt.expectCookie {
				cookies := w.Result().Cookies()
				var found bool
				for _, c := range cookies {
					if c.Name == CSRFCookieName {
						found = true
						require.NotEmpty(t, c.Value, "CSRF cookie should have a value")
						require.False(t, c.HttpOnly, "CSRF cookie must not be HttpOnly")
						require.Contains(t, c.Value, ".", "CSRF cookie should be payload.signature format")
						require.Equal(t, tt.cookieSecure, c.Secure, "CSRF cookie Secure attribute should match request security")
					}
				}
				require.True(t, found, "response should set csrf_token cookie")
			}
		})
	}
}

func TestExtendCSRFCookie(t *testing.T) {
	t.Parallel()

	key := []byte(testCSRFKey)

	t.Run("preserves an existing valid cookie and refreshes MaxAge", func(t *testing.T) {
		t.Parallel()

		existing := mustGenerateToken(t, testCSRFKey)
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: existing})
		w := httptest.NewRecorder()

		require.NoError(t, ExtendCSRFCookie(w, req, key))

		resp := w.Result()
		defer resp.Body.Close()
		var got *http.Cookie
		for _, c := range resp.Cookies() {
			if c.Name == CSRFCookieName {
				got = c
			}
		}
		require.NotNil(t, got, "CSRF cookie should be re-emitted")
		require.Equal(t, existing, got.Value, "existing token value should be preserved to avoid breaking in-flight requests")
		require.Equal(t, int(SessionTTL.Seconds()), got.MaxAge, "re-emitted cookie should use the session TTL")
	})

	t.Run("issues a fresh token when no valid cookie is present", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		w := httptest.NewRecorder()

		require.NoError(t, ExtendCSRFCookie(w, req, key))

		resp := w.Result()
		defer resp.Body.Close()
		var got *http.Cookie
		for _, c := range resp.Cookies() {
			if c.Name == CSRFCookieName {
				got = c
			}
		}
		require.NotNil(t, got, "CSRF cookie should be issued when none is present")
		require.True(t, validSignedToken(got.Value, key), "issued token should be validly signed")
		require.Equal(t, int(SessionTTL.Seconds()), got.MaxAge, "issued cookie should use the session TTL")
	})

	t.Run("replaces an invalid cookie with a fresh token", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: "garbage.badsig"})
		w := httptest.NewRecorder()

		require.NoError(t, ExtendCSRFCookie(w, req, key))

		resp := w.Result()
		defer resp.Body.Close()
		var got *http.Cookie
		for _, c := range resp.Cookies() {
			if c.Name == CSRFCookieName {
				got = c
			}
		}
		require.NotNil(t, got, "CSRF cookie should be re-issued when existing is invalid")
		require.NotEqual(t, "garbage.badsig", got.Value, "invalid token should not be re-emitted")
		require.True(t, validSignedToken(got.Value, key), "replacement token should be validly signed")
	})
}

func TestGenerateSignedToken(t *testing.T) {
	t.Parallel()

	key := []byte(testCSRFKey)

	t.Run("generates valid signed token", func(t *testing.T) {
		t.Parallel()

		token, err := GenerateSignedToken(key)
		require.NoError(t, err, "GenerateSignedToken should return a token")
		require.Contains(t, token, ".", "token should contain a dot separator")

		parts := strings.SplitN(token, ".", 2)
		require.Len(t, parts, 2, "token should have exactly two parts")
		require.Len(t, parts[0], 64, "payload should be 64 hex chars (32 bytes)")
	})

	t.Run("tokens are unique", func(t *testing.T) {
		t.Parallel()

		t1, err := GenerateSignedToken(key)
		require.NoError(t, err, "GenerateSignedToken should create the first token")
		t2, err := GenerateSignedToken(key)
		require.NoError(t, err, "GenerateSignedToken should create the second token")
		require.NotEqual(t, t1, t2, "two generated tokens should not be identical")
	})
}

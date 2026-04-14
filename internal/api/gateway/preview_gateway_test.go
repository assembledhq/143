package gateway

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/assembledhq/143/internal/services/preview"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestExtractPreviewID(t *testing.T) {
	t.Parallel()
	id := uuid.New()

	tests := []struct {
		name    string
		host    string
		want    uuid.UUID
		wantErr bool
	}{
		{
			name: "valid preview hostname",
			host: id.String() + ".preview.143.dev",
			want: id,
		},
		{
			name: "valid with port",
			host: id.String() + ".preview.localhost:9090",
			want: id,
		},
		{
			name:    "invalid UUID",
			host:    "not-a-uuid.preview.143.dev",
			wantErr: true,
		},
		{
			name:    "no subdomain",
			host:    "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := extractPreviewID(tt.host)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tt.want, got)
			}
		})
	}
}

func TestEncodeDecode_CookieValue(t *testing.T) {
	t.Parallel()
	secret := []byte("test-secret-key-for-hmac")
	orgID := uuid.New()
	previewID := uuid.New()
	accessSessionID := uuid.New()

	encoded := encodeCookieValue(secret, orgID, previewID, accessSessionID)
	require.NotEmpty(t, encoded)

	gotOrg, gotPreview, gotAccess, err := decodeCookieValue(secret, encoded)
	require.NoError(t, err)
	require.Equal(t, orgID, gotOrg)
	require.Equal(t, previewID, gotPreview)
	require.Equal(t, accessSessionID, gotAccess)
}

func TestDecodeCookieValue_Invalid(t *testing.T) {
	t.Parallel()
	secret := []byte("test-secret-key-for-hmac")

	_, _, _, err := decodeCookieValue(secret, "not-valid-base64!!!")
	require.Error(t, err)

	_, _, _, err = decodeCookieValue(secret, "dHdvOnBhcnRz") // "two:parts" base64
	require.Error(t, err)
}

func TestDecodeCookieValue_WrongSecret(t *testing.T) {
	t.Parallel()
	secret1 := []byte("correct-secret")
	secret2 := []byte("wrong-secret")
	orgID := uuid.New()
	previewID := uuid.New()
	accessSessionID := uuid.New()

	encoded := encodeCookieValue(secret1, orgID, previewID, accessSessionID)
	_, _, _, err := decodeCookieValue(secret2, encoded)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid cookie signature")
}

func TestIsWebSocketUpgrade(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		connection string
		upgrade    string
		want       bool
	}{
		{"websocket upgrade", "Upgrade", "websocket", true},
		{"case insensitive", "upgrade", "WebSocket", true},
		{"no upgrade header", "", "websocket", false},
		{"no websocket value", "Upgrade", "", false},
		{"empty", "", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := &mockHTTPRequest{connection: tt.connection, upgrade: tt.upgrade}
			require.Equal(t, tt.want, isWebSocketUpgradeHelper(r.connection, r.upgrade))
		})
	}
}

type mockHTTPRequest struct {
	connection string
	upgrade    string
}

// isWebSocketUpgradeHelper duplicates the logic for unit testing without http.Request.
func isWebSocketUpgradeHelper(connection, upgrade string) bool {
	return len(connection) > 0 && len(upgrade) > 0 &&
		(connection == "Upgrade" || connection == "upgrade") &&
		(upgrade == "websocket" || upgrade == "WebSocket")
}

func TestIsWebSocketUpgrade_HTTPRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		connection string
		upgrade    string
		want       bool
	}{
		{"valid websocket", "Upgrade", "websocket", true},
		{"case insensitive", "upgrade", "WebSocket", true},
		{"missing connection", "", "websocket", false},
		{"missing upgrade", "Upgrade", "", false},
		{"empty headers", "", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.connection != "" {
				req.Header.Set("Connection", tt.connection)
			}
			if tt.upgrade != "" {
				req.Header.Set("Upgrade", tt.upgrade)
			}
			require.Equal(t, tt.want, isWebSocketUpgrade(req))
		})
	}
}

func TestInjectSecurityHeaders(t *testing.T) {
	t.Parallel()

	gw := NewGateway(GatewayConfig{AppOrigin: "https://app.143.dev"})
	h := make(http.Header)
	gw.injectSecurityHeaders(h)

	require.Contains(t, h.Get("X-Frame-Options"), "ALLOW-FROM https://app.143.dev")
	require.Contains(t, h.Get("Content-Security-Policy"), "frame-ancestors https://app.143.dev")
	require.Equal(t, "no-referrer", h.Get("Referrer-Policy"))
	require.Equal(t, "nosniff", h.Get("X-Content-Type-Options"))
	require.Equal(t, "same-origin", h.Get("Cross-Origin-Opener-Policy"))
	require.Contains(t, h.Get("Permissions-Policy"), "camera=()")
}

func TestStripSensitiveResponseHeaders(t *testing.T) {
	t.Parallel()

	h := make(http.Header)
	h.Set("Set-Cookie", "session=abc123")
	h.Set("Content-Type", "text/html")

	stripSensitiveResponseHeaders(h)

	require.Empty(t, h.Get("Set-Cookie"))
	require.Equal(t, "text/html", h.Get("Content-Type"))
}

func TestStripPreviewCookie(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "__Host-preview_session", Value: "secret"})
	req.AddCookie(&http.Cookie{Name: "other_cookie", Value: "keep"})

	stripPreviewCookie(req)

	cookies := req.Cookies()
	require.Len(t, cookies, 1)
	require.Equal(t, "other_cookie", cookies[0].Name)
}

func TestNewGateway(t *testing.T) {
	t.Parallel()

	gw := NewGateway(GatewayConfig{AppOrigin: "https://app.143.dev"})
	require.NotNil(t, gw)
	require.Equal(t, "https://app.143.dev", gw.appOrigin)
}

func TestInjectScriptsIntoHTML_GzipResponse(t *testing.T) {
	t.Parallel()

	gw := NewGateway(GatewayConfig{AppOrigin: "https://app.143.dev"})
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	_, err := writer.Write([]byte("<html><head></head><body>Hello</body></html>"))
	require.NoError(t, err, "gzip writer should accept the test HTML")
	require.NoError(t, writer.Close(), "gzip writer should close cleanly")

	resp := &http.Response{
		Header: make(http.Header),
		Body:   io.NopCloser(bytes.NewReader(compressed.Bytes())),
	}
	resp.Header.Set("Content-Type", "text/html; charset=utf-8")
	resp.Header.Set("Content-Encoding", "gzip")

	err = gw.injectScriptsIntoHTML(resp, uuid.New())
	require.NoError(t, err, "injectScriptsIntoHTML should handle gzip-encoded HTML")
	require.Equal(t, "gzip", resp.Header.Get("Content-Encoding"), "gzip encoding should be preserved after injection")

	reader, err := gzip.NewReader(resp.Body)
	require.NoError(t, err, "gzip reader should open the modified response body")
	body, err := io.ReadAll(reader)
	require.NoError(t, err, "should read the modified gzip response body")
	require.NoError(t, reader.Close(), "gzip reader should close cleanly")
	require.Contains(t, string(body), activityHeartbeatScript, "modified HTML should include the injected activity heartbeat script")
	require.Contains(t, string(body), preview.ComponentResolverScript, "modified HTML should include the component resolver script")
}

func TestInjectScriptsIntoHTML_UnsupportedEncodingReturnsErrorWithoutMutatingResponse(t *testing.T) {
	t.Parallel()

	gw := NewGateway(GatewayConfig{AppOrigin: "https://app.143.dev"})
	var compressed bytes.Buffer
	writer, err := flate.NewWriter(&compressed, flate.DefaultCompression)
	require.NoError(t, err, "deflate writer should be created")
	_, err = writer.Write([]byte("<html><head></head><body>Hello</body></html>"))
	require.NoError(t, err, "deflate writer should accept the test HTML")
	require.NoError(t, writer.Close(), "deflate writer should close cleanly")

	original := compressed.Bytes()
	resp := &http.Response{
		Header: make(http.Header),
		Body:   io.NopCloser(bytes.NewReader(original)),
	}
	resp.Header.Set("Content-Type", "text/html; charset=utf-8")
	resp.Header.Set("Content-Encoding", "br")

	err = gw.injectScriptsIntoHTML(resp, uuid.New())
	require.Error(t, err, "injectScriptsIntoHTML should reject unsupported content encodings")
	require.Contains(t, err.Error(), "unsupported content encoding", "unsupported encodings should return a clear error")
	require.Equal(t, "br", resp.Header.Get("Content-Encoding"), "unsupported encodings should remain unchanged")

	body, readErr := io.ReadAll(resp.Body)
	require.NoError(t, readErr, "response body should remain readable when injection is skipped")
	require.Equal(t, original, body, "unsupported encoded bodies should not be mutated")
}

func TestGateway_ServeHTTP_InvalidHost(t *testing.T) {
	t.Parallel()

	gw := NewGateway(GatewayConfig{AppOrigin: "https://app.143.dev"})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "not-a-uuid.preview.143.dev"
	w := httptest.NewRecorder()

	gw.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "invalid preview hostname")
}

func TestGateway_ServeHTTP_BootstrapPage(t *testing.T) {
	t.Parallel()

	gw := NewGateway(GatewayConfig{AppOrigin: "https://app.143.dev"})
	previewID := uuid.New()
	req := httptest.NewRequest(http.MethodGet, "/bootstrap", nil)
	req.Host = previewID.String() + ".preview.143.dev"
	w := httptest.NewRecorder()

	gw.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Header().Get("Content-Type"), "text/html")
	require.Contains(t, w.Body.String(), "https://app.143.dev")
	require.Contains(t, w.Body.String(), "preview_bootstrap_token")
}

func TestGateway_ServeHTTP_BootstrapExchange_MissingToken(t *testing.T) {
	t.Parallel()

	gw := NewGateway(GatewayConfig{AppOrigin: "https://app.143.dev"})
	previewID := uuid.New()

	// Empty JSON body (no token field).
	req := httptest.NewRequest(http.MethodPost, "/bootstrap/exchange", nil)
	req.Host = previewID.String() + ".preview.143.dev"
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	gw.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestGateway_ServeHTTP_Proxy_NoCookie(t *testing.T) {
	t.Parallel()

	gw := NewGateway(GatewayConfig{AppOrigin: "https://app.143.dev"})
	previewID := uuid.New()
	req := httptest.NewRequest(http.MethodGet, "/some-page", nil)
	req.Host = previewID.String() + ".preview.143.dev"
	w := httptest.NewRecorder()

	gw.ServeHTTP(w, req)
	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Contains(t, w.Body.String(), "preview session required")
}

func TestGateway_ServeHTTP_Proxy_InvalidCookie(t *testing.T) {
	t.Parallel()

	gw := NewGateway(GatewayConfig{AppOrigin: "https://app.143.dev"})
	previewID := uuid.New()
	req := httptest.NewRequest(http.MethodGet, "/some-page", nil)
	req.Host = previewID.String() + ".preview.143.dev"
	req.AddCookie(&http.Cookie{Name: "preview_session", Value: "invalid!!!"})
	w := httptest.NewRecorder()

	gw.ServeHTTP(w, req)
	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Contains(t, w.Body.String(), "invalid preview session")
}

func TestGateway_ServeHTTP_Proxy_CookieMismatch(t *testing.T) {
	t.Parallel()

	secret := []byte("test-secret")
	gw := NewGateway(GatewayConfig{AppOrigin: "https://app.143.dev", CookieSecret: secret})
	previewID := uuid.New()
	otherPreviewID := uuid.New()

	// Encode a cookie for a different preview ID.
	cookieVal := encodeCookieValue(secret, uuid.New(), otherPreviewID, uuid.New())

	req := httptest.NewRequest(http.MethodGet, "/some-page", nil)
	req.Host = previewID.String() + ".preview.143.dev"
	req.AddCookie(&http.Cookie{Name: "preview_session", Value: cookieVal})
	w := httptest.NewRecorder()

	gw.ServeHTTP(w, req)
	require.Equal(t, http.StatusForbidden, w.Code)
	require.Contains(t, w.Body.String(), "does not match")
}

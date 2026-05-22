package gateway

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/preview"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
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

func TestShouldRecordPreviewLastPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		method  string
		path    string
		headers map[string]string
		want    bool
	}{
		{
			name:   "records document navigation from fetch metadata",
			method: http.MethodGet,
			path:   "/login",
			headers: map[string]string{
				"Sec-Fetch-Dest": "document",
			},
			want: true,
		},
		{
			name:   "records document navigation from accept header",
			method: http.MethodGet,
			path:   "/sessions/abc",
			headers: map[string]string{
				"Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
			},
			want: true,
		},
		{
			name:   "skips root",
			method: http.MethodGet,
			path:   "/",
			headers: map[string]string{
				"Sec-Fetch-Dest": "document",
			},
			want: false,
		},
		{
			name:   "skips platform heartbeat",
			method: http.MethodGet,
			path:   "/__143_heartbeat",
			headers: map[string]string{
				"Sec-Fetch-Dest": "empty",
			},
			want: false,
		},
		{
			name:   "skips framework event stream",
			method: http.MethodGet,
			path:   "/framework-hmr",
			headers: map[string]string{
				"Accept": "text/event-stream",
			},
			want: false,
		},
		{
			name:   "skips script asset",
			method: http.MethodGet,
			path:   "/assets/app.js",
			headers: map[string]string{
				"Sec-Fetch-Dest": "script",
			},
			want: false,
		},
		{
			name:   "skips json fetch",
			method: http.MethodGet,
			path:   "/rpc/auth/me",
			headers: map[string]string{
				"Accept": "application/json",
			},
			want: false,
		},
		{
			name:   "skips image asset",
			method: http.MethodGet,
			path:   "/favicon.ico",
			headers: map[string]string{
				"Sec-Fetch-Dest": "image",
			},
			want: false,
		},
		{
			name:   "skips non-get request",
			method: http.MethodPost,
			path:   "/login",
			headers: map[string]string{
				"Accept": "text/html",
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(tt.method, tt.path, nil)
			for key, value := range tt.headers {
				req.Header.Set(key, value)
			}
			require.Equal(t, tt.want, shouldRecordPreviewLastPath(req), "gateway should only preserve user navigation paths for preview restore")
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

	// X-Frame-Options ALLOW-FROM is deprecated; only CSP frame-ancestors is set.
	require.Contains(t, h.Get("Content-Security-Policy"), "frame-ancestors https://app.143.dev")
	require.Equal(t, "no-referrer", h.Get("Referrer-Policy"))
	require.Equal(t, "nosniff", h.Get("X-Content-Type-Options"))
	require.Equal(t, "same-origin", h.Get("Cross-Origin-Opener-Policy"))
	require.Contains(t, h.Get("Permissions-Policy"), "camera=()")
}

func TestStripSensitiveResponseHeaders(t *testing.T) {
	t.Parallel()

	h := make(http.Header)
	h.Add("Set-Cookie", "__Host-preview_session=preview-secret; Path=/; Secure; HttpOnly")
	h.Add("Set-Cookie", "session_token=app-session; Path=/; HttpOnly")
	h.Add("Set-Cookie", "csrf_token=csrf-token; Path=/")
	h.Set("Content-Type", "text/html")

	stripSensitiveResponseHeaders(h)

	require.Equal(t, []string{
		"session_token=app-session; Path=/; HttpOnly",
		"csrf_token=csrf-token; Path=/",
	}, h.Values("Set-Cookie"))
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

func TestInjectScriptsIntoHTML_OversizedCompressedBodyPassesThroughUnchanged(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		contentEncoding string
		compress        func(t *testing.T, body []byte) []byte
		decompress      func(t *testing.T, body []byte) []byte
	}{
		{
			name:            "gzip",
			contentEncoding: "gzip",
			compress: func(t *testing.T, body []byte) []byte {
				t.Helper()

				var compressed bytes.Buffer
				writer := gzip.NewWriter(&compressed)
				_, err := writer.Write(body)
				require.NoError(t, err, "gzip writer should accept the oversized HTML body")
				require.NoError(t, writer.Close(), "gzip writer should close cleanly")
				return compressed.Bytes()
			},
			decompress: func(t *testing.T, body []byte) []byte {
				t.Helper()

				reader, err := gzip.NewReader(bytes.NewReader(body))
				require.NoError(t, err, "gzip reader should open the passthrough response body")
				decompressed, err := io.ReadAll(reader)
				require.NoError(t, err, "gzip reader should return the full passthrough body")
				require.NoError(t, reader.Close(), "gzip reader should close cleanly")
				return decompressed
			},
		},
		{
			name:            "deflate",
			contentEncoding: "deflate",
			compress: func(t *testing.T, body []byte) []byte {
				t.Helper()

				var compressed bytes.Buffer
				writer, err := flate.NewWriter(&compressed, flate.DefaultCompression)
				require.NoError(t, err, "deflate writer should be created")
				_, err = writer.Write(body)
				require.NoError(t, err, "deflate writer should accept the oversized HTML body")
				require.NoError(t, writer.Close(), "deflate writer should close cleanly")
				return compressed.Bytes()
			},
			decompress: func(t *testing.T, body []byte) []byte {
				t.Helper()

				reader := flate.NewReader(bytes.NewReader(body))
				decompressed, err := io.ReadAll(reader)
				require.NoError(t, err, "deflate reader should return the full passthrough body")
				require.NoError(t, reader.Close(), "deflate reader should close cleanly")
				return decompressed
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gw := NewGateway(GatewayConfig{AppOrigin: "https://app.143.dev"})
			oversizedHTML := []byte("<html><head></head><body>" + strings.Repeat("a", int(maxHTMLBodySize)+1024) + "</body></html>")
			originalCompressed := tt.compress(t, oversizedHTML)

			resp := &http.Response{
				Header: make(http.Header),
				Body:   io.NopCloser(bytes.NewReader(originalCompressed)),
			}
			resp.Header.Set("Content-Type", "text/html; charset=utf-8")
			resp.Header.Set("Content-Encoding", tt.contentEncoding)

			err := gw.injectScriptsIntoHTML(resp, uuid.New())
			require.NoError(t, err, "injectScriptsIntoHTML should preserve oversized compressed HTML")
			require.Equal(t, tt.contentEncoding, resp.Header.Get("Content-Encoding"), "compressed passthrough should preserve the original content encoding")

			passthroughCompressed, err := io.ReadAll(resp.Body)
			require.NoError(t, err, "response body should remain readable after passthrough")
			require.Equal(t, originalCompressed, passthroughCompressed, "oversized compressed HTML should be served byte-for-byte unchanged")

			passthroughHTML := tt.decompress(t, passthroughCompressed)
			require.Equal(t, oversizedHTML, passthroughHTML, "oversized compressed HTML should remain complete after passthrough")
			require.NotContains(t, string(passthroughHTML), activityHeartbeatScript, "oversized passthrough responses should skip script injection")
		})
	}
}

func TestInjectScriptsIntoHTML_UnsupportedEncodingSkipsInjectionWithoutMutatingResponse(t *testing.T) {
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
	require.NoError(t, err, "injectScriptsIntoHTML should pass through unsupported encodings unchanged")
	require.Equal(t, "br", resp.Header.Get("Content-Encoding"), "unsupported encodings should remain unchanged")

	body, readErr := io.ReadAll(resp.Body)
	require.NoError(t, readErr, "response body should remain readable when injection is skipped")
	require.Equal(t, original, body, "unsupported encoded bodies should not be mutated")
}

func TestInjectBeforeEndTag_InsertsBeforeRealHeadNotFlightPayload(t *testing.T) {
	t.Parallel()

	marker := "<script>preview marker</script>"
	body := []byte(`<!DOCTYPE html><html><head><title>Login</title></head><body>` +
		`<script>self.__next_f.push([1,"0:{\"children\":[\"$\",\"html\",null,{\"children\":[\"$\",\"body\",null,{\"children\":\"payload\"}]}]}"])</script>` +
		strings.Repeat("x", 8192) +
		`</body></html>`)

	out := string(injectBeforeEndTag(body, marker))
	markerIndex := strings.Index(out, marker)
	headCloseIndex := strings.Index(out, "</head>")
	flightIndex := strings.Index(out, "self.__next_f.push")

	require.NotEqual(t, -1, markerIndex, "injection should add the marker script")
	require.NotEqual(t, -1, headCloseIndex, "test document should contain a real head close tag")
	require.NotEqual(t, -1, flightIndex, "test document should contain a Next Flight script payload")
	require.Less(t, markerIndex, headCloseIndex, "injection should happen before the real closing head tag")
	require.Less(t, headCloseIndex, flightIndex, "the real head should close before the Flight script payload begins")
	require.NotContains(t, out, `[\"$\",<script>preview marker</script>`, "injection should not splice the marker into the escaped Flight payload")
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

func TestGateway_ServeHTTP_Proxy_RechecksRevokedCachedSession(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	previewID := uuid.New()
	accessSessionID := uuid.New()
	now := time.Now()
	expiresAt := now.Add(10 * time.Minute)
	secret := []byte("test-secret")

	store := db.NewPreviewStore(mock)
	manager := preview.NewManager(preview.ManagerConfig{
		Store:  store,
		Logger: zerolog.Nop(),
	})
	gw := NewGateway(GatewayConfig{
		Store:        store,
		Manager:      manager,
		Logger:       zerolog.Nop(),
		AppOrigin:    "https://app.143.dev",
		CookieSecret: secret,
	})

	cookieVal := encodeCookieValue(secret, orgID, previewID, accessSessionID)

	mock.ExpectQuery("SELECT .+ FROM preview_access_sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{
				"id", "org_id", "user_id", "preview_instance_id",
				"session_token_hash", "issued_at", "expires_at", "revoked_at", "last_accessed_at", "created_at",
			}).AddRow(accessSessionID, orgID, userID, previewID, "hash", now, expiresAt, nil, now, now),
		)
	mock.ExpectExec("UPDATE preview_instances SET last_accessed_at = now\\(\\), updated_at = now\\(\\) WHERE id = @id AND org_id = @org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	firstReq := httptest.NewRequest(http.MethodGet, "/__143_heartbeat", nil)
	firstReq.Host = previewID.String() + ".preview.143.dev"
	firstReq.AddCookie(&http.Cookie{Name: "preview_session", Value: cookieVal})
	firstResp := httptest.NewRecorder()

	gw.ServeHTTP(firstResp, firstReq)
	require.Equal(t, http.StatusNoContent, firstResp.Code, "first request should succeed and populate the cache")

	revokedAt := now.Add(30 * time.Second)
	mock.ExpectQuery("SELECT .+ FROM preview_access_sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{
				"id", "org_id", "user_id", "preview_instance_id",
				"session_token_hash", "issued_at", "expires_at", "revoked_at", "last_accessed_at", "created_at",
			}).AddRow(accessSessionID, orgID, userID, previewID, "hash", now, expiresAt, &revokedAt, now, now),
		)

	secondReq := httptest.NewRequest(http.MethodGet, "/__143_heartbeat", nil)
	secondReq.Host = previewID.String() + ".preview.143.dev"
	secondReq.AddCookie(&http.Cookie{Name: "preview_session", Value: cookieVal})
	secondResp := httptest.NewRecorder()

	gw.ServeHTTP(secondResp, secondReq)
	require.Equal(t, http.StatusUnauthorized, secondResp.Code, "gateway should reject a session revoked after it was cached")
	require.Contains(t, secondResp.Body.String(), "preview session has been revoked", "gateway should return the revoked-session error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestGateway_ProxyToWorker_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	sessionID := uuid.New()
	previewID := uuid.New()
	now := time.Now().UTC()

	workerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/internal/preview/"+previewID.String()+"/proxy/assets/app.js", r.URL.Path, "proxyToWorker should rewrite preview paths to the internal worker proxy endpoint")
		require.Equal(t, "Bearer ", r.Header.Get("Authorization")[:7], "proxyToWorker should attach a bearer token to worker requests")
		require.Empty(t, r.Header.Get("Cookie"), "proxyToWorker should strip preview cookies before proxying")
		w.Header().Set("Content-Type", "text/plain")
		_, writeErr := io.WriteString(w, "ok")
		require.NoError(t, writeErr, "worker test server should write a response body")
	}))
	defer workerServer.Close()

	metadata, err := json.Marshal(preview.WorkerNodeMetadata{
		PreviewCapable:         true,
		PreviewInternalBaseURL: workerServer.URL,
	})
	require.NoError(t, err, "worker metadata should marshal")

	store := db.NewPreviewStore(mock)
	nodeStore := db.NewNodeStore(mock)
	selector := preview.NewWorkerSelector(nodeStore, store)
	gw := NewGateway(GatewayConfig{
		Store:              store,
		WorkerSelector:     selector,
		Logger:             zerolog.Nop(),
		AppOrigin:          "https://app.143.dev",
		PreviewTokenSecret: "preview-secret",
	})

	mock.ExpectQuery("SELECT .+ FROM preview_instances WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{
				"id", "session_id", "preview_target_id", "org_id", "user_id", "profile_name", "name", "status",
				"provider", "worker_node_id", "preview_handle", "primary_service", "port",
				"config_digest", "base_commit_sha", "last_accessed_at", "expires_at", "stopped_at",
				"last_path", "memory_limit_mb", "cpu_limit_millis", "recycle_config", "recycle_sandbox", "current_phase", "request_id", "error", "created_at", "updated_at", "recycled_at", "recycle_scheduled_at",
				"preview_holding_container",
			}).AddRow(
				previewID, sessionID, nil, orgID, userID, "default", "preview", string(models.PreviewStatusReady),
				"docker", "worker-1", "handle-1", "web", 3000,
				"sha256:abc", "deadbeef", now, now.Add(time.Minute), nil,
				"/", 512, 500, []byte(`{}`), []byte(`{}`), "ready", "req-1", "", now, now, now, nil,
				false,
			),
		)
	mock.ExpectQuery("SELECT .+ FROM nodes WHERE id = @id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "mode", "host", "status", "metadata", "started_at", "last_heartbeat_at"}).
				AddRow("worker-1", "worker", "worker.internal", "active", metadata, now, now),
		)

	req := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
	req.AddCookie(&http.Cookie{Name: "preview_session", Value: "secret"})
	rr := httptest.NewRecorder()

	gw.proxyToWorker(rr, req, orgID, previewID)
	require.Equal(t, http.StatusOK, rr.Code, "proxyToWorker should relay successful worker responses")
	require.Equal(t, "ok", rr.Body.String(), "proxyToWorker should relay the worker response body")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestGateway_ProxyToWorker_Failures(t *testing.T) {
	t.Parallel()

	t.Run("preview lookup failure", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "pgxmock pool should be created")
		defer mock.Close()

		orgID := uuid.New()
		previewID := uuid.New()

		store := db.NewPreviewStore(mock)
		selector := preview.NewWorkerSelector(db.NewNodeStore(mock), store)
		gw := NewGateway(GatewayConfig{
			Store:              store,
			WorkerSelector:     selector,
			Logger:             zerolog.Nop(),
			AppOrigin:          "https://app.143.dev",
			PreviewTokenSecret: "preview-secret",
		})

		mock.ExpectQuery("SELECT .+ FROM preview_instances WHERE id").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{
				"id", "session_id", "preview_target_id", "org_id", "user_id", "profile_name", "name", "status",
				"provider", "worker_node_id", "preview_handle", "primary_service", "port",
				"config_digest", "base_commit_sha", "last_accessed_at", "expires_at", "stopped_at",
				"last_path", "memory_limit_mb", "cpu_limit_millis", "recycle_config", "recycle_sandbox", "current_phase", "request_id", "error", "created_at", "updated_at", "recycled_at", "recycle_scheduled_at",
				"preview_holding_container",
			}))

		req := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
		rr := httptest.NewRecorder()

		gw.proxyToWorker(rr, req, orgID, previewID)
		require.Equal(t, http.StatusBadGateway, rr.Code, "proxyToWorker should fail closed when the preview instance lookup fails")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	t.Run("worker resolve failure", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "pgxmock pool should be created")
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		sessionID := uuid.New()
		previewID := uuid.New()
		now := time.Now().UTC()

		store := db.NewPreviewStore(mock)
		selector := preview.NewWorkerSelector(db.NewNodeStore(mock), store)
		gw := NewGateway(GatewayConfig{
			Store:              store,
			WorkerSelector:     selector,
			Logger:             zerolog.Nop(),
			AppOrigin:          "https://app.143.dev",
			PreviewTokenSecret: "preview-secret",
		})

		mock.ExpectQuery("SELECT .+ FROM preview_instances WHERE id").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(
				pgxmock.NewRows([]string{
					"id", "session_id", "preview_target_id", "org_id", "user_id", "profile_name", "name", "status",
					"provider", "worker_node_id", "preview_handle", "primary_service", "port",
					"config_digest", "base_commit_sha", "last_accessed_at", "expires_at", "stopped_at",
					"last_path", "memory_limit_mb", "cpu_limit_millis", "recycle_config", "recycle_sandbox", "current_phase", "request_id", "error", "created_at", "updated_at", "recycled_at", "recycle_scheduled_at",
					"preview_holding_container",
				}).AddRow(
					previewID, sessionID, nil, orgID, userID, "default", "preview", string(models.PreviewStatusReady),
					"docker", "worker-missing", "handle-1", "web", 3000,
					"sha256:abc", "deadbeef", now, now.Add(time.Minute), nil,
					"/", 512, 500, []byte(`{}`), []byte(`{}`), "ready", "req-1", "", now, now, now, nil,
					false,
				),
			)
		mock.ExpectQuery("SELECT .+ FROM nodes WHERE id = @id").
			WithArgs(pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"id", "mode", "host", "status", "metadata", "started_at", "last_heartbeat_at"}))

		req := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
		rr := httptest.NewRecorder()

		gw.proxyToWorker(rr, req, orgID, previewID)
		require.Equal(t, http.StatusBadGateway, rr.Code, "proxyToWorker should fail closed when the worker cannot be resolved")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	t.Run("worker base url parse failure", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "pgxmock pool should be created")
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		sessionID := uuid.New()
		previewID := uuid.New()
		now := time.Now().UTC()

		metadata, err := json.Marshal(preview.WorkerNodeMetadata{
			PreviewCapable:         true,
			PreviewInternalBaseURL: "://bad-url",
		})
		require.NoError(t, err, "worker metadata should marshal")

		store := db.NewPreviewStore(mock)
		selector := preview.NewWorkerSelector(db.NewNodeStore(mock), store)
		gw := NewGateway(GatewayConfig{
			Store:              store,
			WorkerSelector:     selector,
			Logger:             zerolog.Nop(),
			AppOrigin:          "https://app.143.dev",
			PreviewTokenSecret: "preview-secret",
		})

		mock.ExpectQuery("SELECT .+ FROM preview_instances WHERE id").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(
				pgxmock.NewRows([]string{
					"id", "session_id", "preview_target_id", "org_id", "user_id", "profile_name", "name", "status",
					"provider", "worker_node_id", "preview_handle", "primary_service", "port",
					"config_digest", "base_commit_sha", "last_accessed_at", "expires_at", "stopped_at",
					"last_path", "memory_limit_mb", "cpu_limit_millis", "recycle_config", "recycle_sandbox", "current_phase", "request_id", "error", "created_at", "updated_at", "recycled_at", "recycle_scheduled_at",
					"preview_holding_container",
				}).AddRow(
					previewID, sessionID, nil, orgID, userID, "default", "preview", string(models.PreviewStatusReady),
					"docker", "worker-1", "handle-1", "web", 3000,
					"sha256:abc", "deadbeef", now, now.Add(time.Minute), nil,
					"/", 512, 500, []byte(`{}`), []byte(`{}`), "ready", "req-1", "", now, now, now, nil,
					false,
				),
			)
		mock.ExpectQuery("SELECT .+ FROM nodes WHERE id = @id").
			WithArgs(pgxmock.AnyArg()).
			WillReturnRows(
				pgxmock.NewRows([]string{"id", "mode", "host", "status", "metadata", "started_at", "last_heartbeat_at"}).
					AddRow("worker-1", "worker", "worker.internal", "active", metadata, now, now),
			)

		req := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
		rr := httptest.NewRecorder()

		gw.proxyToWorker(rr, req, orgID, previewID)
		require.Equal(t, http.StatusBadGateway, rr.Code, "proxyToWorker should fail closed when the worker base URL is invalid")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})
}

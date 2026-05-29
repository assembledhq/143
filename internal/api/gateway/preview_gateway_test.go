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

	"github.com/assembledhq/143/internal/auth"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/preview"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func gatewayStringPtr(value string) *string {
	return &value
}

var previewGatewayInstanceColumns = []string{
	"id", "session_id", "preview_target_id", "org_id", "user_id", "profile_name", "name", "status",
	"provider", "worker_node_id", "preview_handle", "primary_service", "port",
	"config_digest", "base_commit_sha", "last_accessed_at", "expires_at", "stopped_at",
	"last_path", "memory_limit_mb", "cpu_limit_millis", "disk_limit_mb", "recycle_config", "recycle_sandbox", "current_phase", "request_id", "error", "created_at", "updated_at", "recycled_at", "recycle_scheduled_at",
	"source_workspace_revision", "source_workspace_revision_updated_at", "unavailable_reason", "preview_holding_container",
}

func previewGatewayInstanceRow(id, sessionID uuid.UUID, targetID *uuid.UUID, orgID, userID uuid.UUID, status models.PreviewStatus, now time.Time, stoppedAt *time.Time) []any {
	return []any{
		id, sessionID, targetID, orgID, userID, "default", "preview", string(status),
		"docker", "worker-1", "handle-1", "web", 3000,
		"sha256:abc", "deadbeef", now, now.Add(time.Minute), stoppedAt,
		"/", 512, 500, 10240, []byte(`{}`), []byte(`{}`), string(status), nil, "", now, now, now, nil,
		(*int64)(nil), (*time.Time)(nil), "",
		false,
	}
}

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
	require.Contains(t, w.Body.String(), "preview_bootstrap_complete", "bootstrap page should notify the parent after the gateway sets the preview session cookie")
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

func TestGateway_ServeHTTP_BootstrapExchange_AllowsTargetHost(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	sessionID := uuid.New()
	targetID := uuid.New()
	previewID := uuid.New()
	accessSessionID := uuid.New()
	now := time.Now()
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

	mock.ExpectQuery("SELECT .+ FROM preview_access_sessions WHERE session_token_hash").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{
				"id", "org_id", "user_id", "preview_instance_id",
				"session_token_hash", "issued_at", "expires_at", "revoked_at", "last_accessed_at", "created_at",
			}).AddRow(accessSessionID, orgID, userID, previewID, "hash", now, now.Add(5*time.Minute), nil, now, now),
		)
	mock.ExpectExec("UPDATE preview_access_sessions SET last_accessed_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectQuery("SELECT .+ FROM preview_instances WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{
				"id", "session_id", "preview_target_id", "org_id", "user_id", "profile_name", "name", "status",
				"provider", "worker_node_id", "preview_handle", "primary_service", "port",
				"config_digest", "base_commit_sha", "last_accessed_at", "expires_at", "stopped_at",
				"last_path", "memory_limit_mb", "cpu_limit_millis", "disk_limit_mb", "recycle_config", "recycle_sandbox", "current_phase", "request_id", "error", "created_at", "updated_at", "recycled_at", "recycle_scheduled_at",
				"source_workspace_revision", "source_workspace_revision_updated_at", "unavailable_reason", "preview_holding_container",
			}).AddRow(
				previewID, sessionID, &targetID, orgID, userID, "default", "preview", string(models.PreviewStatusReady),
				"docker", "worker-1", "handle-1", "web", 3000,
				"sha256:abc", "deadbeef", now, now.Add(time.Minute), nil,
				"/", 512, 500, 10240, []byte(`{}`), []byte(`{}`), "ready", gatewayStringPtr("req-1"), "", now, now, now, nil,
				(*int64)(nil), (*time.Time)(nil), "",
				false,
			),
		)

	req := httptest.NewRequest(http.MethodPost, "/bootstrap/exchange", strings.NewReader(`{"token":"bootstrap-token"}`))
	req.Host = targetID.String() + ".preview.143.dev"
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "bootstrap exchange should accept a token minted for the runtime when the host is the preview target")
	require.Len(t, w.Result().Cookies(), 1, "bootstrap exchange should issue one preview session cookie")
	cookie := w.Result().Cookies()[0]
	_, cookieHostID, _, err := decodeCookieValue(secret, cookie.Value)
	require.NoError(t, err, "preview session cookie should decode")
	require.Equal(t, targetID, cookieHostID, "preview session cookie should be scoped to the public target host")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestGateway_ServeHTTP_Proxy_NoCookie(t *testing.T) {
	t.Parallel()

	gw := NewGateway(GatewayConfig{AppOrigin: "https://app.143.dev"})
	previewID := uuid.New()
	req := httptest.NewRequest(http.MethodGet, "/some-page", nil)
	req.Host = previewID.String() + ".preview.143.dev"
	w := httptest.NewRecorder()

	gw.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, "direct preview visits without a preview session should render the lightweight control overlay")
	require.Contains(t, w.Header().Get("Content-Type"), "text/html", "overlay response should be HTML")
	require.Contains(t, w.Body.String(), "Start preview", "overlay should expose the primary start action")
	require.Contains(t, w.Body.String(), "https://app.143.dev/previews/"+previewID.String(), "overlay should link back to the app-owned start flow")
}

func TestGateway_ServeHTTP_Proxy_NoCookieStoppedTarget(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	now := time.Date(2026, 5, 26, 20, 15, 0, 0, time.UTC)
	stoppedAt := now.Add(-10 * time.Minute)
	targetID := uuid.New()
	previewID := uuid.New()
	orgID := uuid.New()
	userID := uuid.New()
	sessionID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM preview_instances").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(previewGatewayInstanceColumns).AddRow(
				previewGatewayInstanceRow(previewID, sessionID, &targetID, orgID, userID, models.PreviewStatusStopped, now, &stoppedAt)...,
			),
		)

	gw := NewGateway(GatewayConfig{
		Store:     db.NewPreviewStore(mock),
		Logger:    zerolog.Nop(),
		AppOrigin: "https://app.143.dev",
	})
	req := httptest.NewRequest(http.MethodGet, "/some-page", nil)
	req.Host = targetID.String() + ".preview.143.dev"
	w := httptest.NewRecorder()

	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "stopped direct preview visits should render the lightweight control overlay")
	require.Contains(t, w.Body.String(), "Restart preview", "stopped target overlay should expose the restart action")
	require.Contains(t, w.Body.String(), "Status: Stopped", "stopped target overlay should show the terminal status")
	require.Contains(t, w.Body.String(), "May 26, 2026 20:05 UTC", "stopped target overlay should show when the preview stopped")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestGateway_ServeHTTP_Proxy_NoCookieActiveTargetRedirectsToLaunch(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	now := time.Now().UTC()
	targetID := uuid.New()
	previewID := uuid.New()
	orgID := uuid.New()
	userID := uuid.New()
	sessionID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM preview_instances").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(previewGatewayInstanceColumns).AddRow(
				previewGatewayInstanceRow(previewID, sessionID, &targetID, orgID, userID, models.PreviewStatusReady, now, nil)...,
			),
		)

	gw := NewGateway(GatewayConfig{
		Store:     db.NewPreviewStore(mock),
		Logger:    zerolog.Nop(),
		AppOrigin: "https://app.143.dev",
	})
	req := httptest.NewRequest(http.MethodGet, "/some-page", nil)
	req.Host = targetID.String() + ".preview.143.dev"
	w := httptest.NewRecorder()

	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusFound, w.Code, "active direct preview visits should redirect into the bootstrap launch flow")
	require.Equal(t, "https://app.143.dev/previews/"+targetID.String()+"?launch=1", w.Header().Get("Location"), "active target redirect should preserve the target host id")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestGateway_ServeHTTP_Proxy_StaleStoppedTargetCookieShowsRestart(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	now := time.Now().UTC()
	stoppedAt := now.Add(-10 * time.Minute)
	targetID := uuid.New()
	previewID := uuid.New()
	orgID := uuid.New()
	userID := uuid.New()
	sessionID := uuid.New()
	accessSessionID := uuid.New()
	secret := []byte("test-secret")

	mock.ExpectQuery("SELECT .+ FROM preview_access_sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{
				"id", "org_id", "user_id", "preview_instance_id",
				"session_token_hash", "issued_at", "expires_at", "revoked_at", "last_accessed_at", "created_at",
			}).AddRow(accessSessionID, orgID, userID, previewID, "hash", now, now.Add(5*time.Minute), nil, now, now),
		)
	mock.ExpectQuery("SELECT .+ FROM preview_instances WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(previewGatewayInstanceColumns).AddRow(
				previewGatewayInstanceRow(previewID, sessionID, &targetID, orgID, userID, models.PreviewStatusStopped, now, &stoppedAt)...,
			),
		)
	mock.ExpectQuery("SELECT .+ FROM preview_instances").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(previewGatewayInstanceColumns).AddRow(
				previewGatewayInstanceRow(previewID, sessionID, &targetID, orgID, userID, models.PreviewStatusStopped, now, &stoppedAt)...,
			),
		)

	gw := NewGateway(GatewayConfig{
		Store:        db.NewPreviewStore(mock),
		Logger:       zerolog.Nop(),
		AppOrigin:    "https://app.143.dev",
		CookieSecret: secret,
	})
	req := httptest.NewRequest(http.MethodGet, "/some-page", nil)
	req.Host = targetID.String() + ".preview.143.dev"
	req.AddCookie(&http.Cookie{Name: "preview_session", Value: encodeCookieValue(secret, orgID, targetID, accessSessionID)})
	w := httptest.NewRecorder()

	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "stale stopped target cookies should fall back to the restart overlay")
	require.Contains(t, w.Header().Values("Set-Cookie")[0], "preview_session=;", "stale preview cookie should be cleared")
	require.Contains(t, w.Header().Values("Set-Cookie")[0], "Max-Age=0", "cleared preview cookie should expire immediately")
	require.Contains(t, w.Body.String(), "Restart preview", "stopped target overlay should expose the restart action")
	require.Contains(t, w.Body.String(), "Status: Stopped", "stopped target overlay should show terminal status")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestGateway_ServeHTTP_Proxy_RevokedTargetCookieRedirectsToLaunch(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	now := time.Now().UTC()
	revokedAt := now.Add(-time.Minute)
	targetID := uuid.New()
	previewID := uuid.New()
	orgID := uuid.New()
	userID := uuid.New()
	sessionID := uuid.New()
	accessSessionID := uuid.New()
	secret := []byte("test-secret")

	mock.ExpectQuery("SELECT .+ FROM preview_access_sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{
				"id", "org_id", "user_id", "preview_instance_id",
				"session_token_hash", "issued_at", "expires_at", "revoked_at", "last_accessed_at", "created_at",
			}).AddRow(accessSessionID, orgID, userID, previewID, "hash", now, now.Add(5*time.Minute), &revokedAt, now, now),
		)
	mock.ExpectQuery("SELECT .+ FROM preview_instances").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(previewGatewayInstanceColumns).AddRow(
				previewGatewayInstanceRow(previewID, sessionID, &targetID, orgID, userID, models.PreviewStatusReady, now, nil)...,
			),
		)

	gw := NewGateway(GatewayConfig{
		Store:        db.NewPreviewStore(mock),
		Logger:       zerolog.Nop(),
		AppOrigin:    "https://app.143.dev",
		CookieSecret: secret,
	})
	req := httptest.NewRequest(http.MethodGet, "/some-page", nil)
	req.Host = targetID.String() + ".preview.143.dev"
	req.AddCookie(&http.Cookie{Name: "preview_session", Value: encodeCookieValue(secret, orgID, targetID, accessSessionID)})
	w := httptest.NewRecorder()

	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusFound, w.Code, "revoked target cookies should redirect through launch instead of showing a raw auth error")
	require.Equal(t, "https://app.143.dev/previews/"+targetID.String()+"?launch=1", w.Header().Get("Location"), "revoked target cookie redirect should preserve the target host id")
	require.Contains(t, w.Header().Values("Set-Cookie")[0], "preview_session=;", "revoked target cookie should be cleared")
	require.Contains(t, w.Header().Values("Set-Cookie")[0], "Max-Age=0", "cleared preview cookie should expire immediately")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
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

	mock.ExpectQuery("SELECT .+ FROM preview_runtimes").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{
				"id", "org_id", "preview_instance_id", "runtime_epoch", "worker_node_id",
				"endpoint_url", "preview_handle", "primary_port", "status", "lease_expires_at",
				"last_heartbeat_at", "drain_requested_at", "stopped_at", "error", "unavailable_reason", "created_at", "updated_at",
			}).AddRow(
				uuid.New(), orgID, previewID, 1, "worker-1",
				workerServer.URL, "handle-1", 3000, string(models.PreviewRuntimeStatusReady), now.Add(time.Minute),
				now, nil, nil, "", "", now, now,
			),
		)

	req := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
	req.AddCookie(&http.Cookie{Name: "preview_session", Value: "secret"})
	rr := httptest.NewRecorder()

	gw.proxyToWorker(rr, req, orgID, previewID)
	require.Equal(t, http.StatusOK, rr.Code, "proxyToWorker should relay successful worker responses")
	require.Equal(t, "ok", rr.Body.String(), "proxyToWorker should relay the worker response body")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestGateway_ProxyToWorker_RoutesByRuntimeEndpoint(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	orgID := uuid.New()
	previewID := uuid.New()
	runtimeID := uuid.New()
	now := time.Now().UTC()

	workerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/internal/preview/"+previewID.String()+"/proxy/api/v1/auth/login", r.URL.Path, "proxyToWorker should rewrite preview paths to the internal worker proxy endpoint")
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		claims, validateErr := auth.ValidatePreviewToken("preview-secret", token)
		require.NoError(t, validateErr, "worker request should include a valid preview token")
		require.NotNil(t, claims.RuntimeID, "worker token should carry the runtime ID")
		require.Equal(t, runtimeID, *claims.RuntimeID, "worker token should target the selected runtime")
		require.Equal(t, 7, claims.RuntimeEpoch, "worker token should target the selected runtime epoch")
		_, writeErr := io.WriteString(w, "ok")
		require.NoError(t, writeErr, "worker test server should write a response body")
	}))
	defer workerServer.Close()

	store := db.NewPreviewStore(mock)
	gw := NewGateway(GatewayConfig{
		Store:              store,
		WorkerSelector:     preview.NewWorkerSelector(db.NewNodeStore(mock), store),
		Logger:             zerolog.Nop(),
		AppOrigin:          "https://app.143.dev",
		PreviewTokenSecret: "preview-secret",
	})

	mock.ExpectQuery("SELECT .+ FROM preview_runtimes").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "preview_instance_id", "runtime_epoch", "worker_node_id",
			"endpoint_url", "preview_handle", "primary_port", "status", "lease_expires_at",
			"last_heartbeat_at", "drain_requested_at", "stopped_at", "error", "unavailable_reason", "created_at", "updated_at",
		}).AddRow(
			runtimeID, orgID, previewID, 7, "worker-runtime",
			workerServer.URL, "handle-1", 3000, string(models.PreviewRuntimeStatusReady), now.Add(time.Minute),
			now, nil, nil, "", "", now, now,
		))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{}`))
	req.AddCookie(&http.Cookie{Name: "preview_session", Value: "secret"})
	rr := httptest.NewRecorder()

	gw.proxyToWorker(rr, req, orgID, previewID)
	require.Equal(t, http.StatusOK, rr.Code, "proxyToWorker should relay successful worker responses")
	require.Equal(t, "ok", rr.Body.String(), "proxyToWorker should relay the worker response body")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestGateway_ProxyToWorker_UnavailableRuntime(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	orgID := uuid.New()
	previewID := uuid.New()
	store := db.NewPreviewStore(mock)
	gw := NewGateway(GatewayConfig{
		Store:              store,
		WorkerSelector:     preview.NewWorkerSelector(db.NewNodeStore(mock), store),
		Logger:             zerolog.Nop(),
		AppOrigin:          "https://app.143.dev",
		PreviewTokenSecret: "preview-secret",
	})

	mock.ExpectQuery("SELECT .+ FROM preview_runtimes").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "preview_instance_id", "runtime_epoch", "worker_node_id",
			"endpoint_url", "preview_handle", "primary_port", "status", "lease_expires_at",
			"last_heartbeat_at", "drain_requested_at", "stopped_at", "error", "unavailable_reason", "created_at", "updated_at",
		}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/login", nil)
	rr := httptest.NewRecorder()

	gw.proxyToWorker(rr, req, orgID, previewID)
	require.Equal(t, http.StatusServiceUnavailable, rr.Code, "proxyToWorker should return unavailable when no runtime can serve the preview")

	var resp models.ErrorResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp), "unavailable runtime response should be JSON")
	require.Equal(t, "PREVIEW_RUNTIME_UNAVAILABLE", resp.Error.Code, "unavailable runtime should return a preview-specific code")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestGateway_ProxyToWorker_TranslatesWorkerRuntimeMismatch(t *testing.T) {
	t.Parallel()

	workerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		err := json.NewEncoder(w).Encode(models.ErrorResponse{
			Error: models.ErrorDetail{
				Code:    "WRONG_PREVIEW_WORKER",
				Message: "preview token targets a different worker",
			},
		})
		require.NoError(t, err, "worker mismatch response should encode")
	}))
	defer workerServer.Close()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	orgID := uuid.New()
	previewID := uuid.New()
	runtimeID := uuid.New()
	now := time.Now().UTC()
	store := db.NewPreviewStore(mock)
	gw := NewGateway(GatewayConfig{
		Store:              store,
		WorkerSelector:     preview.NewWorkerSelector(db.NewNodeStore(mock), store),
		Logger:             zerolog.Nop(),
		AppOrigin:          "https://app.143.dev",
		PreviewTokenSecret: "preview-secret",
	})

	mock.ExpectQuery("SELECT .+ FROM preview_runtimes").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "preview_instance_id", "runtime_epoch", "worker_node_id",
			"endpoint_url", "preview_handle", "primary_port", "status", "lease_expires_at",
			"last_heartbeat_at", "drain_requested_at", "stopped_at", "error", "unavailable_reason", "created_at", "updated_at",
		}).AddRow(
			runtimeID, orgID, previewID, 1, "worker-runtime",
			workerServer.URL, "handle-1", 3000, string(models.PreviewRuntimeStatusReady), now.Add(time.Minute),
			now, nil, nil, "", "", now, now,
		))

	req := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
	rr := httptest.NewRecorder()

	gw.proxyToWorker(rr, req, orgID, previewID)
	require.Equal(t, http.StatusServiceUnavailable, rr.Code, "proxyToWorker should hide worker runtime mismatch errors")

	var resp models.ErrorResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp), "runtime mismatch translation should return JSON")
	require.Equal(t, "PREVIEW_RUNTIME_UNAVAILABLE", resp.Error.Code, "runtime mismatch should be translated to unavailable")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestGateway_ProxyToWorker_PreservesLargeNonMismatchForbiddenBody(t *testing.T) {
	t.Parallel()

	largeBody := strings.Repeat("x", 70*1024)
	workerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusForbidden)
		_, err := io.WriteString(w, largeBody)
		require.NoError(t, err, "worker test server should write the large body")
	}))
	defer workerServer.Close()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	orgID := uuid.New()
	previewID := uuid.New()
	runtimeID := uuid.New()
	now := time.Now().UTC()
	store := db.NewPreviewStore(mock)
	gw := NewGateway(GatewayConfig{
		Store:              store,
		WorkerSelector:     preview.NewWorkerSelector(db.NewNodeStore(mock), store),
		Logger:             zerolog.Nop(),
		AppOrigin:          "https://app.143.dev",
		PreviewTokenSecret: "preview-secret",
	})

	mock.ExpectQuery("SELECT .+ FROM preview_runtimes").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "preview_instance_id", "runtime_epoch", "worker_node_id",
			"endpoint_url", "preview_handle", "primary_port", "status", "lease_expires_at",
			"last_heartbeat_at", "drain_requested_at", "stopped_at", "error", "unavailable_reason", "created_at", "updated_at",
		}).AddRow(
			runtimeID, orgID, previewID, 1, "worker-runtime",
			workerServer.URL, "handle-1", 3000, string(models.PreviewRuntimeStatusReady), now.Add(time.Minute),
			now, nil, nil, "", "", now, now,
		))

	req := httptest.NewRequest(http.MethodGet, "/forbidden", nil)
	rr := httptest.NewRecorder()

	gw.proxyToWorker(rr, req, orgID, previewID)

	require.Equal(t, http.StatusForbidden, rr.Code, "proxyToWorker should preserve non-mismatch forbidden responses")
	require.Equal(t, largeBody, rr.Body.String(), "proxyToWorker should not truncate non-mismatch forbidden bodies")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestGateway_ProxyToWorker_InvalidRuntimeEndpoint(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	orgID := uuid.New()
	previewID := uuid.New()
	runtimeID := uuid.New()
	now := time.Now().UTC()

	store := db.NewPreviewStore(mock)
	gw := NewGateway(GatewayConfig{
		Store:              store,
		WorkerSelector:     preview.NewWorkerSelector(db.NewNodeStore(mock), store),
		Logger:             zerolog.Nop(),
		AppOrigin:          "https://app.143.dev",
		PreviewTokenSecret: "preview-secret",
	})

	mock.ExpectQuery("SELECT .+ FROM preview_runtimes").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "preview_instance_id", "runtime_epoch", "worker_node_id",
			"endpoint_url", "preview_handle", "primary_port", "status", "lease_expires_at",
			"last_heartbeat_at", "drain_requested_at", "stopped_at", "error", "unavailable_reason", "created_at", "updated_at",
		}).AddRow(
			runtimeID, orgID, previewID, 1, "worker-1",
			"", "handle-1", 3000, string(models.PreviewRuntimeStatusReady), now.Add(time.Minute),
			now, nil, nil, "", "", now, now,
		))

	req := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
	rr := httptest.NewRecorder()

	gw.proxyToWorker(rr, req, orgID, previewID)
	require.Equal(t, http.StatusServiceUnavailable, rr.Code, "proxyToWorker should return unavailable when the runtime endpoint is invalid")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

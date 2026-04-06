package gateway

import (
	"bufio"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/services/preview"
)

// Gateway serves the preview origin (e.g., <preview-id>.preview.143.dev).
// It validates preview access, proxies HTTP and WebSocket traffic, and
// injects security headers. It does NOT use the main app session middleware.
type Gateway struct {
	store        *db.PreviewStore
	manager      *preview.Manager
	logger       zerolog.Logger
	appOrigin    string
	cookieSecret []byte
}

// GatewayConfig holds initialization options.
type GatewayConfig struct {
	Store        *db.PreviewStore
	Manager      *preview.Manager
	Logger       zerolog.Logger
	AppOrigin    string // e.g. "https://app.143.dev"
	CookieSecret []byte // HMAC key for signing preview session cookies
}

// NewGateway creates a new preview gateway.
func NewGateway(cfg GatewayConfig) *Gateway {
	return &Gateway{
		store:        cfg.Store,
		manager:      cfg.Manager,
		logger:       cfg.Logger,
		appOrigin:    cfg.AppOrigin,
		cookieSecret: cfg.CookieSecret,
	}
}

// ServeHTTP implements http.Handler. Each request is routed based on the Host
// header (preview ID) and the request path.
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	previewID, err := extractPreviewID(r.Host)
	if err != nil {
		http.Error(w, "invalid preview hostname", http.StatusBadRequest)
		return
	}

	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/bootstrap":
		g.serveBootstrapPage(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/bootstrap/exchange":
		g.handleBootstrapExchange(w, r, previewID)
	default:
		g.handleProxy(w, r, previewID)
	}
}

// extractPreviewID parses the preview UUID from the first subdomain
// component of the Host header (e.g., "abc123.preview.143.dev" → abc123).
func extractPreviewID(host string) (uuid.UUID, error) {
	// Strip port if present.
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	parts := strings.SplitN(host, ".", 2)
	if len(parts) == 0 {
		return uuid.UUID{}, fmt.Errorf("no subdomain in host %q", host)
	}
	return uuid.Parse(parts[0])
}

// =============================================================================
// Bootstrap page
// =============================================================================

func (g *Gateway) serveBootstrapPage(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	// The bootstrap page signals readiness via postMessage, receives the token,
	// exchanges it for a session cookie, then navigates to the preview root.
	fmt.Fprintf(w, bootstrapHTML, g.appOrigin)
}

const bootstrapHTML = `<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>Preview Bootstrap</title></head>
<body>
<p>Connecting to preview…</p>
<script>
(function() {
  var appOrigin = %q;

  window.addEventListener('message', function(event) {
    if (event.origin !== appOrigin) return;
    var data = event.data;
    if (!data || data.type !== 'preview_bootstrap_token') return;

    fetch('/bootstrap/exchange', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({token: data.token}),
      credentials: 'same-origin'
    }).then(function(resp) {
      if (resp.ok) {
        window.location.href = '/';
      } else {
        document.body.textContent = 'Bootstrap failed: ' + resp.status;
      }
    }).catch(function(err) {
      document.body.textContent = 'Bootstrap error: ' + err.message;
    });
  });

  // Signal readiness to the parent (app origin).
  if (window.parent !== window) {
    window.parent.postMessage({type: 'preview_bootstrap_ready'}, appOrigin);
  }
})();
</script>
</body></html>`

// =============================================================================
// Bootstrap token exchange
// =============================================================================

type bootstrapExchangeRequest struct {
	Token string `json:"token"`
}

func (g *Gateway) handleBootstrapExchange(w http.ResponseWriter, r *http.Request, previewID uuid.UUID) {
	var body bootstrapExchangeRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if body.Token == "" {
		http.Error(w, "token is required", http.StatusBadRequest)
		return
	}

	// Bootstrap tokens are validated without org scoping because the gateway
	// does not have session middleware — the orgID is not known until the
	// token is exchanged. The token hash is cryptographically random (32 bytes)
	// so unscoped lookup is safe from collision.
	sess, err := g.manager.ValidateBootstrapTokenUnscoped(r.Context(), body.Token)
	if err != nil {
		g.logger.Warn().Err(err).Str("preview_id", previewID.String()).Msg("bootstrap exchange failed")
		http.Error(w, "invalid or expired bootstrap token", http.StatusUnauthorized)
		return
	}

	// Verify the token's preview matches the hostname's preview ID.
	if sess.PreviewInstanceID != previewID {
		http.Error(w, "token does not match this preview", http.StatusForbidden)
		return
	}

	// Set the preview session cookie (HMAC-signed).
	cookieValue := encodeCookieValue(g.cookieSecret, sess.OrgID, previewID, sess.ID)
	http.SetCookie(w, &http.Cookie{
		Name:     "__Host-preview_session",
		Value:    cookieValue,
		Path:     "/",
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteStrictMode,
		Expires:  sess.ExpiresAt,
	})

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(map[string]string{"status": "ok"}); err != nil {
		g.logger.Warn().Err(err).Msg("failed to write bootstrap exchange response")
	}
}

// =============================================================================
// Proxy
// =============================================================================

func (g *Gateway) handleProxy(w http.ResponseWriter, r *http.Request, previewID uuid.UUID) {
	// Read and validate the session cookie.
	cookie, err := r.Cookie("__Host-preview_session")
	if err != nil {
		http.Error(w, "preview session required — complete bootstrap first", http.StatusUnauthorized)
		return
	}

	orgID, cookiePreviewID, _, err := decodeCookieValue(g.cookieSecret, cookie.Value)
	if err != nil {
		http.Error(w, "invalid preview session", http.StatusUnauthorized)
		return
	}

	// Verify the cookie's preview ID matches the hostname's preview ID.
	if cookiePreviewID != previewID {
		http.Error(w, "preview session does not match this preview", http.StatusForbidden)
		return
	}

	// Record activity for idle timeout tracking.
	_ = g.manager.RecordAccess(r.Context(), orgID, previewID)
	if r.URL.Path != "" && r.URL.Path != "/" {
		_ = g.manager.RecordLastPath(r.Context(), orgID, previewID, r.URL.Path)
	}

	// Check for WebSocket upgrade.
	if isWebSocketUpgrade(r) {
		g.handleWebSocket(w, r, orgID, previewID)
		return
	}

	// HTTP reverse proxy.
	g.handleHTTPProxy(w, r, orgID, previewID)
}

func (g *Gateway) handleHTTPProxy(w http.ResponseWriter, r *http.Request, orgID, previewID uuid.UUID) {
	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = "http"
			req.URL.Host = "preview-target"
			// Strip the preview session cookie from forwarded requests.
			stripPreviewCookie(req)
		},
		Transport: &previewTransport{
			manager:   g.manager,
			orgID:     orgID,
			previewID: previewID,
		},
		ModifyResponse: func(resp *http.Response) error {
			injectSecurityHeaders(resp.Header, g.appOrigin)
			stripSensitiveResponseHeaders(resp.Header)
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			g.logger.Warn().Err(err).Str("preview_id", previewID.String()).Msg("proxy error")
			http.Error(w, "preview unavailable", http.StatusBadGateway)
		},
	}
	proxy.ServeHTTP(w, r)
}

func (g *Gateway) handleWebSocket(w http.ResponseWriter, r *http.Request, orgID, previewID uuid.UUID) {
	// Dial the preview backend.
	backendConn, err := g.manager.DialPreview(r.Context(), orgID, previewID)
	if err != nil {
		g.logger.Warn().Err(err).Str("preview_id", previewID.String()).Msg("websocket dial failed")
		http.Error(w, "preview unavailable", http.StatusBadGateway)
		return
	}
	defer backendConn.Close()

	// Hijack the client connection.
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "websocket hijack not supported", http.StatusInternalServerError)
		return
	}
	clientConn, clientBuf, err := hijacker.Hijack()
	if err != nil {
		g.logger.Warn().Err(err).Msg("websocket hijack failed")
		return
	}
	defer clientConn.Close()

	// Forward the original request to the backend.
	if err := r.Write(backendConn); err != nil {
		g.logger.Warn().Err(err).Msg("websocket: failed to forward upgrade request")
		return
	}
	// Also flush any buffered data.
	if clientBuf.Reader.Buffered() > 0 {
		buffered := make([]byte, clientBuf.Reader.Buffered())
		_, _ = clientBuf.Read(buffered)
		_, _ = backendConn.Write(buffered)
	}

	// Bidirectional copy.
	done := make(chan struct{})
	go func() {
		if _, err := io.Copy(clientConn, backendConn); err != nil {
			g.logger.Debug().Err(err).Str("preview_id", previewID.String()).Msg("websocket: backend→client copy ended")
		}
		close(done)
	}()
	if _, err := io.Copy(backendConn, clientConn); err != nil {
		g.logger.Debug().Err(err).Str("preview_id", previewID.String()).Msg("websocket: client→backend copy ended")
	}
	<-done
}

// =============================================================================
// Transport for httputil.ReverseProxy
// =============================================================================

// previewTransport implements http.RoundTripper by dialing the preview
// stream and performing the HTTP request over it.
type previewTransport struct {
	manager   *preview.Manager
	orgID     uuid.UUID
	previewID uuid.UUID
}

func (t *previewTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	conn, err := t.manager.DialPreview(req.Context(), t.orgID, t.previewID)
	if err != nil {
		return nil, fmt.Errorf("dial preview: %w", err)
	}

	// Write the request to the preview connection.
	if err := req.Write(conn); err != nil {
		_ = conn.Close() // best-effort cleanup; returning the write error
		return nil, fmt.Errorf("write request: %w", err)
	}

	// Read the response.
	resp, err := http.ReadResponse(
		bufio.NewReader(conn), req,
	)
	if err != nil {
		_ = conn.Close() // best-effort cleanup; returning the read error
		return nil, fmt.Errorf("read response: %w", err)
	}

	// The response body will close the connection when fully read.
	return resp, nil
}

// =============================================================================
// Security headers
// =============================================================================

func injectSecurityHeaders(h http.Header, appOrigin string) {
	h.Set("X-Frame-Options", "ALLOW-FROM "+appOrigin)
	h.Set("Content-Security-Policy", strings.Join([]string{
		"default-src 'self' blob: data:",
		"script-src 'self' 'unsafe-inline' 'unsafe-eval'",
		"style-src 'self' 'unsafe-inline'",
		"img-src 'self' data: blob:",
		"font-src 'self' data:",
		"connect-src 'self' wss://*.preview.143.dev",
		"form-action 'self'",
		"object-src 'none'",
		"base-uri 'none'",
		"frame-ancestors " + appOrigin,
		"worker-src 'none'",
	}, "; "))
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), clipboard-read=(), clipboard-write=()")
}

func stripSensitiveResponseHeaders(h http.Header) {
	// Remove Set-Cookie from sandbox responses. Our own session cookie is
	// set directly in the exchange handler, not via proxy responses.
	h.Del("Set-Cookie")
}

func stripPreviewCookie(req *http.Request) {
	cookies := req.Cookies()
	req.Header.Del("Cookie")
	for _, c := range cookies {
		if c.Name != "__Host-preview_session" {
			req.AddCookie(c)
		}
	}
}

// =============================================================================
// Cookie encoding
// =============================================================================

// encodeCookieValue produces an HMAC-signed, base64url-encoded cookie value.
// Format: base64url(orgID:previewID:accessSessionID:hmac_hex)
func encodeCookieValue(secret []byte, orgID, previewID, accessSessionID uuid.UUID) string {
	payload := fmt.Sprintf("%s:%s:%s", orgID, previewID, accessSessionID)
	sig := computeCookieHMAC(secret, payload)
	raw := payload + ":" + sig
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// decodeCookieValue decodes and verifies the HMAC signature on the cookie.
func decodeCookieValue(secret []byte, value string) (orgID, previewID, accessSessionID uuid.UUID, err error) {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return uuid.UUID{}, uuid.UUID{}, uuid.UUID{}, fmt.Errorf("decode cookie: %w", err)
	}
	parts := strings.SplitN(string(raw), ":", 4)
	if len(parts) != 4 {
		return uuid.UUID{}, uuid.UUID{}, uuid.UUID{}, fmt.Errorf("invalid cookie format")
	}

	// Verify HMAC before trusting any field values.
	payload := parts[0] + ":" + parts[1] + ":" + parts[2]
	expectedSig := computeCookieHMAC(secret, payload)
	if !hmac.Equal([]byte(parts[3]), []byte(expectedSig)) {
		return uuid.UUID{}, uuid.UUID{}, uuid.UUID{}, fmt.Errorf("invalid cookie signature")
	}

	orgID, err = uuid.Parse(parts[0])
	if err != nil {
		return uuid.UUID{}, uuid.UUID{}, uuid.UUID{}, fmt.Errorf("invalid org_id in cookie: %w", err)
	}
	previewID, err = uuid.Parse(parts[1])
	if err != nil {
		return uuid.UUID{}, uuid.UUID{}, uuid.UUID{}, fmt.Errorf("invalid preview_id in cookie: %w", err)
	}
	accessSessionID, err = uuid.Parse(parts[2])
	if err != nil {
		return uuid.UUID{}, uuid.UUID{}, uuid.UUID{}, fmt.Errorf("invalid access_session_id in cookie: %w", err)
	}
	return orgID, previewID, accessSessionID, nil
}

// computeCookieHMAC returns a hex-encoded HMAC-SHA256 of the given payload.
func computeCookieHMAC(secret []byte, payload string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

// =============================================================================
// Helpers
// =============================================================================

func isWebSocketUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Connection"), "upgrade") &&
		strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

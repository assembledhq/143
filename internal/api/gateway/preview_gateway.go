package gateway

import (
	"bufio"
	"bytes"
	"compress/flate"
	"compress/gzip"
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
	"time"

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
	hmrWatcher   *preview.HMRWatcher
	logger       zerolog.Logger
	appOrigin    string
	cookieSecret []byte
	cspHeader    string // pre-computed CSP header value
}

// GatewayConfig holds initialization options.
type GatewayConfig struct {
	Store        *db.PreviewStore
	Manager      *preview.Manager
	HMRWatcher   *preview.HMRWatcher // optional; enables HMR screenshot capture
	Logger       zerolog.Logger
	AppOrigin    string // e.g. "https://app.143.dev"
	CookieSecret []byte // HMAC key for signing preview session cookies
}

// NewGateway creates a new preview gateway.
func NewGateway(cfg GatewayConfig) *Gateway {
	return &Gateway{
		store:        cfg.Store,
		manager:      cfg.Manager,
		hmrWatcher:   cfg.HMRWatcher,
		logger:       cfg.Logger,
		appOrigin:    cfg.AppOrigin,
		cookieSecret: cfg.CookieSecret,
		cspHeader: strings.Join([]string{
			"default-src 'self' blob: data:",
			"script-src 'self' 'unsafe-inline' 'unsafe-eval'",
			"style-src 'self' 'unsafe-inline'",
			"img-src 'self' data: blob:",
			"font-src 'self' data:",
			"connect-src 'self' wss://*.preview.143.dev",
			"form-action 'self'",
			"object-src 'none'",
			"base-uri 'none'",
			"frame-ancestors " + cfg.AppOrigin,
			"worker-src 'none'",
		}, "; "),
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

	orgID, cookiePreviewID, accessSessionID, err := decodeCookieValue(g.cookieSecret, cookie.Value)
	if err != nil {
		http.Error(w, "invalid preview session", http.StatusUnauthorized)
		return
	}

	// Verify the cookie's preview ID matches the hostname's preview ID.
	if cookiePreviewID != previewID {
		http.Error(w, "preview session does not match this preview", http.StatusForbidden)
		return
	}

	// Refresh the session cookie expiry on each successful request so that
	// active users are not logged out after the initial 5-minute window.
	refreshedCookie := encodeCookieValue(g.cookieSecret, orgID, previewID, accessSessionID)
	http.SetCookie(w, &http.Cookie{
		Name:     "__Host-preview_session",
		Value:    refreshedCookie,
		Path:     "/",
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteStrictMode,
		Expires:  time.Now().Add(5 * time.Minute),
	})

	// Record activity for idle timeout tracking.
	if err := g.manager.RecordAccess(r.Context(), orgID, previewID); err != nil {
		g.logger.Warn().Err(err).Str("preview_id", previewID.String()).Msg("failed to record access")
	}
	if r.URL.Path != "" && r.URL.Path != "/" {
		if err := g.manager.RecordLastPath(r.Context(), orgID, previewID, r.URL.Path); err != nil {
			g.logger.Warn().Err(err).Str("preview_id", previewID.String()).Msg("failed to record last path")
		}
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
			req.Header.Del("Accept-Encoding")
			// Strip the preview session cookie from forwarded requests.
			stripPreviewCookie(req)
		},
		Transport: &previewTransport{
			manager:   g.manager,
			orgID:     orgID,
			previewID: previewID,
		},
		ModifyResponse: func(resp *http.Response) error {
			g.injectSecurityHeaders(resp.Header)
			stripSensitiveResponseHeaders(resp.Header)

			// Inject scripts into HTML responses.
			ct := resp.Header.Get("Content-Type")
			if strings.HasPrefix(ct, "text/html") {
				if err := g.injectScriptsIntoHTML(resp, previewID); err != nil {
					g.logger.Warn().Err(err).
						Str("preview_id", previewID.String()).
						Msg("failed to inject scripts into HTML response")
					// Non-fatal: the original response is already consumed,
					// so we cannot fall back. The error is logged and we
					// proceed with whatever body state we have.
				}
			}

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
		if g.hmrWatcher != nil {
			// Tee backend→client traffic through the HMR watcher so it
			// can detect HMR update messages and trigger screenshots.
			g.copyWithHMRSnoop(clientConn, backendConn, previewID)
		} else {
			if _, err := io.Copy(clientConn, backendConn); err != nil {
				g.logger.Debug().Err(err).Str("preview_id", previewID.String()).Msg("websocket: backend→client copy ended")
			}
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
// Script injection
// =============================================================================

// activityHeartbeatScript is injected into HTML pages to send periodic
// activity heartbeats while the preview tab is visible. This keeps the
// idle timeout tracker accurate even when the user is just viewing (not
// navigating) the preview.
const activityHeartbeatScript = `
(function() {
  "use strict";
  if (window.__143_heartbeat) return;
  window.__143_heartbeat = true;

  var INTERVAL_MS = 30000; // 30 seconds
  var timer = null;

  function sendHeartbeat() {
    var img = new Image();
    img.src = "/__143_heartbeat?t=" + Date.now();
  }

  function onVisibilityChange() {
    if (document.visibilityState === "visible") {
      sendHeartbeat();
      if (!timer) {
        timer = setInterval(sendHeartbeat, INTERVAL_MS);
      }
    } else {
      if (timer) {
        clearInterval(timer);
        timer = null;
      }
    }
  }

  document.addEventListener("visibilitychange", onVisibilityChange);
  // Start immediately if already visible.
  if (document.visibilityState === "visible") {
    sendHeartbeat();
    timer = setInterval(sendHeartbeat, INTERVAL_MS);
  }
})();
`

// injectScriptsIntoHTML reads the response body, injects the component
// resolver and activity heartbeat scripts, and replaces the body with the
// modified content. The Content-Length header is updated accordingly.
func (g *Gateway) injectScriptsIntoHTML(resp *http.Response, previewID uuid.UUID) error {
	// Read the original body and decode it when the upstream still responded
	// with a supported content encoding.
	var bodyBytes []byte
	contentEncoding := strings.TrimSpace(strings.ToLower(resp.Header.Get("Content-Encoding")))
	recompress := func(body []byte) ([]byte, error) {
		return body, nil
	}

	switch contentEncoding {
	case "", "identity":
	case "gzip":
		gr, err := gzip.NewReader(resp.Body)
		if err != nil {
			return fmt.Errorf("gzip reader: %w", err)
		}
		bodyBytes, err = io.ReadAll(gr)
		_ = gr.Close()
		_ = resp.Body.Close()
		if err != nil {
			return fmt.Errorf("read gzipped body: %w", err)
		}
		recompress = func(body []byte) ([]byte, error) {
			var buf bytes.Buffer
			gw := gzip.NewWriter(&buf)
			if _, err := gw.Write(body); err != nil {
				return nil, fmt.Errorf("gzip write: %w", err)
			}
			if err := gw.Close(); err != nil {
				return nil, fmt.Errorf("gzip close: %w", err)
			}
			return buf.Bytes(), nil
		}
	case "deflate":
		reader := flate.NewReader(resp.Body)
		var err error
		bodyBytes, err = io.ReadAll(reader)
		_ = reader.Close()
		_ = resp.Body.Close()
		if err != nil {
			return fmt.Errorf("read deflate body: %w", err)
		}
		recompress = func(body []byte) ([]byte, error) {
			var buf bytes.Buffer
			writer, err := flate.NewWriter(&buf, flate.DefaultCompression)
			if err != nil {
				return nil, fmt.Errorf("deflate writer: %w", err)
			}
			if _, err := writer.Write(body); err != nil {
				return nil, fmt.Errorf("deflate write: %w", err)
			}
			if err := writer.Close(); err != nil {
				return nil, fmt.Errorf("deflate close: %w", err)
			}
			return buf.Bytes(), nil
		}
	default:
		return fmt.Errorf("unsupported content encoding %q", contentEncoding)
	}

	if bodyBytes == nil {
		var err error
		bodyBytes, err = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			return fmt.Errorf("read body: %w", err)
		}
	}

	// Build the script block to inject.
	scriptBlock := "<script>" + preview.ComponentResolverScript + "</script>" +
		"<script>" + activityHeartbeatScript + "</script>"

	body := string(bodyBytes)
	bodyLower := strings.ToLower(body)
	injected := false

	// Prefer injecting before </head>.
	// NOTE: This substring-based matching can produce incorrect results if
	// "</head>" appears inside a string literal, JS template, or HTML comment
	// rather than as the actual closing head tag. A proper fix would require
	// an HTML parser, but we accept this limitation to avoid the dependency.
	if idx := strings.Index(bodyLower, "</head>"); idx != -1 {
		body = body[:idx] + scriptBlock + body[idx:]
		injected = true
	}

	// Fallback: inject before </body>.
	if !injected {
		if idx := strings.Index(bodyLower, "</body>"); idx != -1 {
			body = body[:idx] + scriptBlock + body[idx:]
			injected = true
		}
	}

	// Last resort: append to end of document.
	if !injected {
		body = body + scriptBlock
	}

	// Replace the response body.
	newBody := []byte(body)

	newBody, err := recompress(newBody)
	if err != nil {
		return err
	}
	if contentEncoding == "" || contentEncoding == "identity" {
		resp.Header.Del("Content-Encoding")
	}

	resp.Body = io.NopCloser(bytes.NewReader(newBody))
	resp.ContentLength = int64(len(newBody))
	resp.Header.Set("Content-Length", fmt.Sprintf("%d", len(newBody)))

	return nil
}

// =============================================================================
// WebSocket HMR snooping
// =============================================================================

// copyWithHMRSnoop copies data from src to dst while also passing each
// chunk to the HMR watcher for pattern detection. This is a best-effort
// approach — we forward raw TCP bytes (which may span multiple WebSocket
// frames) to the watcher. The watcher's pattern matching is substring-based
// and tolerates partial frames.
func (g *Gateway) copyWithHMRSnoop(dst io.Writer, src io.Reader, previewID uuid.UUID) {
	buf := make([]byte, 32*1024)
	for {
		n, readErr := src.Read(buf)
		if n > 0 {
			data := buf[:n]
			// Forward to HMR watcher for pattern detection.
			g.hmrWatcher.OnWebSocketMessage(previewID, data)
			// Write to the client.
			if _, writeErr := dst.Write(data); writeErr != nil {
				g.logger.Debug().Err(writeErr).
					Str("preview_id", previewID.String()).
					Msg("websocket: backend→client write ended")
				return
			}
		}
		if readErr != nil {
			if readErr != io.EOF {
				g.logger.Debug().Err(readErr).
					Str("preview_id", previewID.String()).
					Msg("websocket: backend→client read ended")
			}
			return
		}
	}
}

// =============================================================================
// Security headers
// =============================================================================

func (g *Gateway) injectSecurityHeaders(h http.Header) {
	h.Set("X-Frame-Options", "ALLOW-FROM "+g.appOrigin)
	h.Set("Content-Security-Policy", g.cspHeader)
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

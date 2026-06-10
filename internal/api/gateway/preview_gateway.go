package gateway

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	stdhtml "html"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
	xhtml "golang.org/x/net/html"

	"github.com/assembledhq/143/internal/auth"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/preview"
)

// sessionCacheEntry caches access session validation results to avoid
// hitting the database on every proxied request (including static assets).
type sessionCacheEntry struct {
	validUntil time.Time // when this cache entry expires
	revokedAt  *time.Time
	expiresAt  time.Time
}

// sessionCache is a simple TTL cache for access session lookups.
const sessionCacheTTL = 10 * time.Second

// expiryExtendThreshold controls how often we extend the DB session expiry.
// We only extend when the remaining time is below this threshold, avoiding
// a DB write on every single request.
const expiryExtendThreshold = 2 * time.Minute

// Gateway serves the preview origin (e.g., <preview-id>.preview.143.dev).
// It validates preview access, proxies HTTP and WebSocket traffic, and
// injects security headers. It does NOT use the main app session middleware.
type Gateway struct {
	store        *db.PreviewStore
	manager      *preview.Manager
	workerSelect *preview.WorkerSelector
	hmrWatcher   *preview.HMRWatcher
	logger       zerolog.Logger
	appOrigin    string
	cookieSecret []byte
	tokenSecret  string
	secureCookie bool   // true when preview origin uses https
	cspHeader    string // pre-computed CSP header value

	// sessionCache avoids a DB round-trip on every proxied request.
	sessionCacheMu sync.RWMutex
	sessionCache   map[uuid.UUID]*sessionCacheEntry
}

// GatewayConfig holds initialization options.
type GatewayConfig struct {
	Store                 *db.PreviewStore
	Manager               *preview.Manager
	WorkerSelector        *preview.WorkerSelector
	HMRWatcher            *preview.HMRWatcher // optional; enables HMR screenshot capture
	Logger                zerolog.Logger
	AppOrigin             string // e.g. "https://app.143.dev"
	CookieSecret          []byte // HMAC key for signing preview session cookies
	PreviewTokenSecret    string
	PreviewOriginTemplate string // e.g. "https://{id}.preview.143.dev"
}

// maxHTMLBodySize caps how much HTML we read into memory for script injection.
// Kept conservative to avoid excessive memory usage with concurrent previews.
const maxHTMLBodySize = 5 * 1024 * 1024 // 5 MB

// cookieName returns the session cookie name — __Host-prefixed over HTTPS,
// plain name over HTTP (localhost dev).
func (g *Gateway) cookieName() string {
	if g.secureCookie {
		return "__Host-preview_session"
	}
	return "preview_session"
}

// evictCachedSession removes a session entry from the cache.
func (g *Gateway) evictCachedSession(id uuid.UUID) {
	g.sessionCacheMu.Lock()
	defer g.sessionCacheMu.Unlock()
	delete(g.sessionCache, id)
}

// putCachedSession stores a session entry in the cache.
func (g *Gateway) putCachedSession(id uuid.UUID, entry *sessionCacheEntry) {
	g.sessionCacheMu.Lock()
	defer g.sessionCacheMu.Unlock()
	g.sessionCache[id] = entry
}

// NewGateway creates a new preview gateway.
func NewGateway(cfg GatewayConfig) *Gateway {
	return &Gateway{
		store:        cfg.Store,
		manager:      cfg.Manager,
		workerSelect: cfg.WorkerSelector,
		hmrWatcher:   cfg.HMRWatcher,
		logger:       cfg.Logger,
		appOrigin:    cfg.AppOrigin,
		cookieSecret: cfg.CookieSecret,
		tokenSecret:  cfg.PreviewTokenSecret,
		secureCookie: strings.HasPrefix(cfg.PreviewOriginTemplate, "https://"),
		sessionCache: make(map[uuid.UUID]*sessionCacheEntry),
		cspHeader: strings.Join([]string{
			"default-src 'self' blob: data:",
			"script-src 'self' 'unsafe-inline' 'unsafe-eval'",
			"style-src 'self' 'unsafe-inline'",
			"img-src 'self' data: blob:",
			"font-src 'self' data:",
			"connect-src 'self' " + deriveWSConnectSrc(cfg.PreviewOriginTemplate),
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
	case r.Method == http.MethodGet && r.URL.Path == previewControlStatusPath:
		g.servePreviewControlStatus(w, r, previewID)
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
        if (window.parent !== window) {
          window.parent.postMessage({type: 'preview_bootstrap_complete'}, appOrigin);
        }
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

	// Verify the token's runtime preview is valid for the public host ID.
	// Branch-preview links use the stable preview target ID as the hostname,
	// while session previews still use the runtime instance ID directly.
	if !g.bootstrapSessionMatchesHost(r, sess, previewID) {
		http.Error(w, "token does not match this preview", http.StatusForbidden)
		return
	}

	// Set the preview session cookie (HMAC-signed).
	cookieValue := encodeCookieValue(g.cookieSecret, sess.OrgID, previewID, sess.ID)
	http.SetCookie(w, previewSessionCookie(cookieValue, sess.ExpiresAt, g.secureCookie))

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(map[string]string{"status": "ok"}); err != nil {
		g.logger.Warn().Err(err).Msg("failed to write bootstrap exchange response")
	}
}

func (g *Gateway) bootstrapSessionMatchesHost(r *http.Request, sess *models.PreviewAccessSession, hostID uuid.UUID) bool {
	if sess.PreviewInstanceID == hostID {
		return true
	}
	if g.store == nil {
		return false
	}
	instance, err := g.store.GetPreviewInstance(r.Context(), sess.OrgID, sess.PreviewInstanceID)
	if err != nil {
		g.logger.Warn().Err(err).
			Str("preview_id", sess.PreviewInstanceID.String()).
			Str("host_id", hostID.String()).
			Msg("failed to resolve bootstrap preview host")
		return false
	}
	return instance.PreviewTargetID != nil && *instance.PreviewTargetID == hostID
}

// =============================================================================
// Proxy
// =============================================================================

func (g *Gateway) handleProxy(w http.ResponseWriter, r *http.Request, previewID uuid.UUID) {
	// Read and validate the session cookie.
	cookie, err := r.Cookie(g.cookieName())
	if err != nil {
		g.servePreviewControlOverlay(w, r, previewID)
		return
	}

	orgID, cookieHostID, accessSessionID, err := decodeCookieValue(g.cookieSecret, cookie.Value)
	if err != nil {
		http.Error(w, "invalid preview session", http.StatusUnauthorized)
		return
	}

	// Verify the cookie's public host ID matches the hostname. For branch
	// previews this is the stable target ID, not the runtime instance ID.
	if cookieHostID != previewID {
		http.Error(w, "preview session does not match this preview", http.StatusForbidden)
		return
	}

	// Re-check the access session in the database before honoring the cookie.
	// The local cache is only a short-lived mirror of the latest DB state; we
	// do not trust it across requests for revocation decisions.
	now := time.Now()
	sess, err := g.store.GetAccessSessionByID(r.Context(), orgID, accessSessionID)
	if err != nil {
		g.evictCachedSession(accessSessionID)
		http.Error(w, "preview session not found", http.StatusUnauthorized)
		return
	}
	cached := &sessionCacheEntry{
		validUntil: now.Add(sessionCacheTTL),
		revokedAt:  sess.RevokedAt,
		expiresAt:  sess.ExpiresAt,
	}
	g.putCachedSession(accessSessionID, cached)

	if cached.revokedAt != nil {
		g.evictCachedSession(accessSessionID)
		if g.handleStaleTargetSession(w, r, previewID, sess, accessSessionID) {
			return
		}
		http.Error(w, "preview session has been revoked", http.StatusUnauthorized)
		return
	}
	if now.After(cached.expiresAt) {
		g.evictCachedSession(accessSessionID)
		if g.handleStaleTargetSession(w, r, previewID, sess, accessSessionID) {
			return
		}
		http.Error(w, "preview session has expired", http.StatusUnauthorized)
		return
	}
	runtimePreviewID := sess.PreviewInstanceID
	if runtimePreviewID != previewID && !g.accessSessionMatchesTargetHost(r, sess, previewID) {
		g.evictCachedSession(accessSessionID)
		if g.handleStaleTargetSession(w, r, previewID, sess, accessSessionID) {
			return
		}
		http.Error(w, "preview session no longer matches this preview", http.StatusForbidden)
		return
	}

	// Only extend the DB session expiry when close to expiration, avoiding
	// a DB write on every single request.
	if time.Until(cached.expiresAt) < expiryExtendThreshold {
		newExpiry := now.Add(5 * time.Minute)
		if err := g.store.ExtendAccessSessionExpiry(r.Context(), orgID, accessSessionID, newExpiry); err != nil {
			if errors.Is(err, db.ErrSessionRevoked) {
				// Session was revoked between cache fill and extend — evict
				// the stale cache entry and deny access immediately.
				g.evictCachedSession(accessSessionID)
				if g.handleStaleTargetSession(w, r, previewID, sess, accessSessionID) {
					return
				}
				http.Error(w, "preview session has been revoked", http.StatusUnauthorized)
				return
			}
			g.logger.Warn().Err(err).Str("access_session_id", accessSessionID.String()).Msg("failed to extend access session expiry")
		} else {
			refreshedCookie := encodeCookieValue(g.cookieSecret, orgID, previewID, accessSessionID)
			http.SetCookie(w, previewSessionCookie(refreshedCookie, newExpiry, g.secureCookie))
			// Update the cache with the new expiry.
			g.putCachedSession(accessSessionID, &sessionCacheEntry{
				validUntil: now.Add(sessionCacheTTL),
				revokedAt:  cached.revokedAt,
				expiresAt:  newExpiry,
			})
		}
	}

	// Intercept heartbeat pings before recording activity so the heartbeat
	// URL does not overwrite last_path (which is used for navigation restore).
	if r.URL.Path == previewHeartbeatPath {
		// Still record access for idle timeout tracking.
		if err := g.manager.RecordAccess(r.Context(), orgID, runtimePreviewID); err != nil {
			g.logger.Warn().Err(err).Str("preview_id", runtimePreviewID.String()).Msg("failed to record access")
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Record activity for idle timeout tracking.
	if err := g.manager.RecordAccess(r.Context(), orgID, runtimePreviewID); err != nil {
		g.logger.Warn().Err(err).Str("preview_id", runtimePreviewID.String()).Msg("failed to record access")
	}
	if shouldRecordPreviewLastPath(r) {
		if err := g.manager.RecordLastPath(r.Context(), orgID, runtimePreviewID, r.URL.Path); err != nil {
			g.logger.Warn().Err(err).Str("preview_id", runtimePreviewID.String()).Msg("failed to record last path")
		}
	}

	g.proxyToWorker(w, r, orgID, runtimePreviewID)
}

func (g *Gateway) handleStaleTargetSession(w http.ResponseWriter, r *http.Request, previewID uuid.UUID, sess *models.PreviewAccessSession, accessSessionID uuid.UUID) bool {
	if sess == nil || sess.PreviewInstanceID == previewID {
		return false
	}
	g.evictCachedSession(accessSessionID)
	g.clearPreviewSessionCookie(w)
	g.servePreviewControlOverlay(w, r, previewID)
	return true
}

func (g *Gateway) clearPreviewSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     g.cookieName(),
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   g.secureCookie,
		SameSite: http.SameSiteStrictMode,
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
	})
}

func (g *Gateway) accessSessionMatchesTargetHost(r *http.Request, sess *models.PreviewAccessSession, hostID uuid.UUID) bool {
	if g.store == nil {
		return false
	}
	instance, err := g.store.GetPreviewInstance(r.Context(), sess.OrgID, sess.PreviewInstanceID)
	if err != nil {
		g.logger.Warn().Err(err).
			Str("preview_id", sess.PreviewInstanceID.String()).
			Str("host_id", hostID.String()).
			Msg("failed to resolve target-host preview session")
		return false
	}
	return instance.PreviewTargetID != nil && *instance.PreviewTargetID == hostID && instance.Status.IsActive()
}

func (g *Gateway) servePreviewControlOverlay(w http.ResponseWriter, r *http.Request, previewID uuid.UUID) {
	data := g.previewControlOverlayData(r, previewID)
	controlURL := g.previewControlURL(previewID)
	if data.AutoLaunch {
		http.Redirect(w, r, controlURL, http.StatusFound)
		return
	}
	statusURL := g.previewStatusURL(previewID)
	cfg, err := json.Marshal(map[string]any{
		"appOrigin":     g.resolvedAppOrigin(),
		"controlUrl":    controlURL,
		"statusPath":    previewControlStatusPath,
		"restartable":   data.Restartable,
		"initialStatus": string(data.Status),
	})
	if err != nil {
		cfg = []byte("null")
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if _, err := fmt.Fprintf(
		w,
		previewControlOverlayHTML,
		stdhtml.EscapeString(data.Title),
		stdhtml.EscapeString(data.Title),
		stdhtml.EscapeString(data.Description),
		stdhtml.EscapeString(data.StatusLine),
		stdhtml.EscapeString(controlURL),
		stdhtml.EscapeString(data.ActionLabel),
		stdhtml.EscapeString(statusURL),
		cfg,
	); err != nil {
		g.logger.Warn().Err(err).Str("preview_id", previewID.String()).Msg("failed to write preview control overlay")
	}
}

type previewControlOverlayData struct {
	Title       string
	Description string
	StatusLine  string
	ActionLabel string
	AutoLaunch  bool
	Restartable bool
	Status      models.PreviewStatus
}

func (g *Gateway) previewControlOverlayData(r *http.Request, previewID uuid.UUID) previewControlOverlayData {
	fallback := previewControlOverlayData{
		Title:       "Preview not connected",
		Description: "Start the preview from 143 and it will open here.",
		StatusLine:  "Not connected",
		ActionLabel: "Start preview",
	}
	if g.store == nil {
		return fallback
	}
	instance, err := g.store.GetPreviewForPublicHost(r.Context(), previewID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			g.logger.Warn().Err(err).Str("preview_id", previewID.String()).Msg("failed to load preview control overlay state")
		}
		return fallback
	}
	label := previewStatusLabel(instance.Status)
	statusLine := label
	if instance.StoppedAt != nil {
		statusLine += " · " + instance.StoppedAt.UTC().Format("Jan 2, 2006 15:04 MST")
	}
	if instance.Status.IsActive() {
		return previewControlOverlayData{
			Title:       "Open preview",
			Description: "This preview is running — opening it through 143.",
			StatusLine:  statusLine,
			ActionLabel: "Open preview",
			AutoLaunch:  true,
			Status:      instance.Status,
		}
	}
	return previewControlOverlayData{
		Title:       "Preview " + strings.ToLower(label),
		Description: "Restart it to pick up where you left off — this page reconnects automatically.",
		StatusLine:  statusLine,
		ActionLabel: "Restart preview",
		Restartable: true,
		Status:      instance.Status,
	}
}

// previewControlStatusResponse is the unauthenticated status payload polled by
// the control overlay while a restart is in flight. It exposes only what the
// overlay itself already renders without auth: the status and stop time.
type previewControlStatusResponse struct {
	Status    string  `json:"status"`
	Label     string  `json:"label"`
	Active    bool    `json:"active"`
	StoppedAt *string `json:"stopped_at,omitempty"`
}

func (g *Gateway) servePreviewControlStatus(w http.ResponseWriter, r *http.Request, previewID uuid.UUID) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	writeNotFound := func() {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(models.ErrorResponse{
			Error: models.ErrorDetail{Code: "PREVIEW_NOT_FOUND", Message: "preview not found"},
		})
	}
	if g.store == nil {
		writeNotFound()
		return
	}
	instance, err := g.store.GetPreviewForPublicHost(r.Context(), previewID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			g.logger.Warn().Err(err).Str("preview_id", previewID.String()).Msg("failed to load preview control status")
		}
		writeNotFound()
		return
	}
	resp := previewControlStatusResponse{
		Status: string(instance.Status),
		Label:  previewStatusLabel(instance.Status),
		Active: instance.Status.IsActive(),
	}
	if instance.StoppedAt != nil {
		stoppedAt := instance.StoppedAt.UTC().Format(time.RFC3339)
		resp.StoppedAt = &stoppedAt
	}
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		g.logger.Warn().Err(err).Str("preview_id", previewID.String()).Msg("failed to write preview control status")
	}
}

func previewStatusLabel(status models.PreviewStatus) string {
	switch status {
	case models.PreviewStatusStarting:
		return "Starting"
	case models.PreviewStatusReady:
		return "Ready"
	case models.PreviewStatusPartiallyReady:
		return "Partially ready"
	case models.PreviewStatusUnhealthy:
		return "Unhealthy"
	case models.PreviewStatusStopped:
		return "Stopped"
	case models.PreviewStatusFailed:
		return "Failed"
	case models.PreviewStatusExpired:
		return "Expired"
	default:
		return string(status)
	}
}

func (g *Gateway) previewControlURL(previewID uuid.UUID) string {
	return g.previewStatusURL(previewID) + "?launch=1"
}

func (g *Gateway) previewStatusURL(previewID uuid.UUID) string {
	return g.resolvedAppOrigin() + "/previews/" + previewID.String()
}

func (g *Gateway) resolvedAppOrigin() string {
	appOrigin := strings.TrimRight(g.appOrigin, "/")
	if appOrigin == "" {
		return "https://143.dev"
	}
	return appOrigin
}

const previewControlOverlayHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>%s</title>
<style>
:root { color-scheme: light dark; font-family: ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; }
body { margin: 0; min-height: 100vh; display: grid; place-items: center; background: Canvas; color: CanvasText; }
.panel { width: min(92vw, 380px); border: 1px solid color-mix(in srgb, CanvasText 14%%, transparent); border-radius: 12px; padding: 24px; box-shadow: 0 18px 60px color-mix(in srgb, CanvasText 12%%, transparent); }
.eyebrow { margin: 0 0 8px; font-size: 12px; color: color-mix(in srgb, CanvasText 56%%, transparent); }
h1 { margin: 0; font-size: 20px; line-height: 1.2; letter-spacing: 0; }
p { margin: 10px 0 0; font-size: 14px; line-height: 1.5; color: color-mix(in srgb, CanvasText 68%%, transparent); }
.status { color: CanvasText; font-weight: 600; }
.actions { display: flex; gap: 10px; margin-top: 20px; flex-wrap: wrap; }
a { display: inline-flex; align-items: center; justify-content: center; min-height: 36px; border-radius: 8px; padding: 0 14px; font-size: 14px; font-weight: 600; text-decoration: none; }
.primary { background: CanvasText; color: Canvas; }
.primary[aria-disabled="true"] { opacity: 0.6; pointer-events: none; }
.secondary { border: 1px solid color-mix(in srgb, CanvasText 16%%, transparent); color: CanvasText; }
</style>
</head>
<body>
<main class="panel">
<p class="eyebrow">143 preview</p>
<h1 id="pv-title">%s</h1>
<p id="pv-desc">%s</p>
<p class="status" id="pv-status">%s</p>
<div class="actions">
<a class="primary" id="pv-action" href="%s">%s</a>
<a class="secondary" href="%s">View status and logs</a>
</div>
</main>
<script>window.__143PreviewControl = %s;</script>
<script>
(function() {
  "use strict";
  var cfg = window.__143PreviewControl;
  if (!cfg || !cfg.restartable) return;
  var titleEl = document.getElementById("pv-title");
  var descEl = document.getElementById("pv-desc");
  var statusEl = document.getElementById("pv-status");
  var actionEl = document.getElementById("pv-action");
  var original = {
    title: titleEl.textContent,
    desc: descEl.textContent,
    status: statusEl.textContent,
    action: actionEl.textContent
  };
  var restarting = false;
  var popup = null;
  var pollTimer = null;
  var activeSince = 0;

  function setPanel(title, desc, status) {
    titleEl.textContent = title;
    descEl.textContent = desc;
    statusEl.textContent = status;
    document.title = title;
  }

  function stopPolling() {
    if (pollTimer) {
      clearInterval(pollTimer);
      pollTimer = null;
    }
  }

  function enableAction() {
    restarting = false;
    actionEl.textContent = original.action;
    actionEl.removeAttribute("aria-disabled");
  }

  function resetPanel() {
    stopPolling();
    enableAction();
    setPanel(original.title, original.desc, original.status);
  }

  function fallbackLaunch() {
    stopPolling();
    window.location.href = cfg.controlUrl;
  }

  // The popup posts this once it has connected the browser to the restarted
  // preview (the session cookie is set at that point). The cookie is scoped
  // to the host in data.url; when this overlay is on an alias host (runtime
  // instance ID vs stable target ID) we must navigate there instead of
  // reloading in place.
  window.addEventListener("message", function(event) {
    if (event.origin !== cfg.appOrigin) return;
    var data = event.data;
    if (!data || data.type !== "preview_launch_complete") return;
    stopPolling();
    statusEl.textContent = "Connected";
    var dest = null;
    try { dest = new URL(data.url); } catch (e) {}
    if (dest && (dest.protocol === "https:" || dest.protocol === "http:") && dest.origin !== window.location.origin) {
      window.location.href = dest.href;
      return;
    }
    window.location.reload();
  });

  function poll() {
    fetch(cfg.statusPath, {cache: "no-store"}).then(function(resp) {
      return resp.ok ? resp.json() : null;
    }).then(function(state) {
      if (!state || !restarting) return;
      if (state.status === cfg.initialStatus) {
        // The restart has not been picked up yet. If the popup was closed
        // without starting anything, put the panel back.
        if (popup && popup.closed) resetPanel();
        return;
      }
      if (state.status === "failed") {
        stopPolling();
        enableAction();
        setPanel("Preview failed to start", "Check status and logs for details, then try restarting again.", state.label);
        return;
      }
      if (state.active && state.status !== "starting") {
        // Ready. The popup finishes the browser handshake and notifies us;
        // if it is gone or stuck, fall back to the full launch flow.
        if (!activeSince) activeSince = Date.now();
        if (!popup || popup.closed || Date.now() - activeSince > 15000) {
          fallbackLaunch();
          return;
        }
        statusEl.textContent = "Connecting this browser…";
        return;
      }
      statusEl.textContent = state.label + "…";
    }).catch(function() {});
  }

  actionEl.addEventListener("click", function(event) {
    event.preventDefault();
    if (restarting) return;
    popup = window.open(cfg.controlUrl + "&popup=1", "143-preview-launch", "popup=yes,width=440,height=400");
    if (!popup) {
      // Popup blocked: fall back to the full-page launch flow.
      window.location.href = cfg.controlUrl;
      return;
    }
    restarting = true;
    activeSince = 0;
    setPanel("Restarting preview", "Keep this tab open — the preview opens here when it is ready.", "Restart requested…");
    actionEl.textContent = "Restarting…";
    actionEl.setAttribute("aria-disabled", "true");
    pollTimer = setInterval(poll, 2500);
    poll();
  });
})();
</script>
</body>
</html>`

const (
	previewPlatformPathPrefix = "/__143_"
	previewHeartbeatPath      = previewPlatformPathPrefix + "heartbeat"
	previewControlStatusPath  = previewPlatformPathPrefix + "control/status"
)

func shouldRecordPreviewLastPath(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}

	requestPath := r.URL.Path
	if requestPath == "" || requestPath == "/" {
		return false
	}
	if strings.HasPrefix(requestPath, previewPlatformPathPrefix) {
		return false
	}

	// Browser navigations advertise Sec-Fetch-Dest: document. Framework
	// internals, assets, EventSource/HMR, and fetch/XHR requests do not.
	if fetchDest := r.Header.Get("Sec-Fetch-Dest"); fetchDest != "" {
		return fetchDest == "document"
	}

	return acceptsHTMLDocument(r.Header.Get("Accept"))
}

func acceptsHTMLDocument(accept string) bool {
	for _, rawPart := range strings.Split(accept, ",") {
		mediaType := strings.ToLower(strings.TrimSpace(strings.SplitN(rawPart, ";", 2)[0]))
		if mediaType == "text/html" || mediaType == "application/xhtml+xml" {
			return true
		}
	}
	return false
}

func (g *Gateway) proxyToWorker(w http.ResponseWriter, r *http.Request, orgID, previewID uuid.UUID) {
	originalReq := r
	runtime, err := g.store.GetActivePreviewRuntime(r.Context(), orgID, previewID)
	if err != nil {
		addPreviewProxyLogFields(g.logger.Warn().Err(err), r, orgID, previewID, nil, "").
			Msg("failed to resolve active preview runtime")
		writeRuntimeUnavailable(w)
		return
	}
	upstreamPath := previewWorkerProxyPath(previewID, r.URL.Path)
	targetURL, err := url.Parse(runtime.EndpointURL)
	if err != nil || targetURL.Scheme == "" || targetURL.Host == "" || (targetURL.Scheme != "http" && targetURL.Scheme != "https") {
		addPreviewProxyLogFields(g.logger.Warn().Err(err), r, orgID, previewID, runtime, upstreamPath).
			Msg("failed to parse preview runtime endpoint url")
		writeRuntimeUnavailable(w)
		return
	}
	token, err := auth.GeneratePreviewToken(g.tokenSecret, auth.PreviewTokenClaims{
		OrgID:        orgID,
		TargetNodeID: runtime.WorkerNodeID,
		RuntimeID:    &runtime.ID,
		RuntimeEpoch: runtime.RuntimeEpoch,
		PreviewID:    &previewID,
		Action:       "proxy",
		ExpiresAt:    time.Now().Add(30 * time.Second),
	})
	if err != nil {
		addPreviewProxyLogFields(g.logger.Warn().Err(err), r, orgID, previewID, runtime, upstreamPath).
			Msg("failed to sign preview worker token")
		http.Error(w, "preview unavailable", http.StatusBadGateway)
		return
	}

	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = targetURL.Scheme
			req.URL.Host = targetURL.Host
			req.URL.Path = upstreamPath
			req.Host = targetURL.Host
			req.RequestURI = ""
			stripPreviewCookie(req)
			req.Header.Set("Authorization", "Bearer "+token)
		},
		ModifyResponse: func(resp *http.Response) error {
			if translateWorkerRuntimeMismatch(resp) {
				return nil
			}

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
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			addPreviewProxyLogFields(g.logger.Warn().Err(err), originalReq, orgID, previewID, runtime, upstreamPath).
				Msg("proxy error")
			http.Error(w, "preview unavailable", http.StatusBadGateway)
		},
	}
	proxy.ServeHTTP(w, r)
}

func previewWorkerProxyPath(previewID uuid.UUID, requestPath string) string {
	return fmt.Sprintf("/internal/preview/%s/proxy%s", previewID.String(), requestPath)
}

func addPreviewProxyLogFields(event *zerolog.Event, r *http.Request, orgID, previewID uuid.UUID, runtime *models.PreviewRuntime, upstreamPath string) *zerolog.Event {
	requestPath := ""
	requestHost := ""
	requestMethod := ""
	queryPresent := false
	secFetchDest := ""
	if r != nil {
		requestHost = r.Host
		requestMethod = r.Method
		secFetchDest = r.Header.Get("Sec-Fetch-Dest")
		if r.URL != nil {
			requestPath = r.URL.Path
			queryPresent = r.URL.RawQuery != ""
		}
	}

	event = event.
		Str("org_id", orgID.String()).
		Str("preview_id", previewID.String()).
		Str("request_method", requestMethod).
		Str("request_host", requestHost).
		Str("request_path", requestPath).
		Bool("query_present", queryPresent).
		Str("sec_fetch_dest", secFetchDest).
		Str("upstream_path", upstreamPath)

	if runtime == nil {
		return event
	}

	return event.
		Str("runtime_id", runtime.ID.String()).
		Int("runtime_epoch", runtime.RuntimeEpoch).
		Str("runtime_status", string(runtime.Status)).
		Str("worker_node_id", runtime.WorkerNodeID).
		Str("endpoint_url", runtime.EndpointURL).
		Str("preview_handle", runtime.PreviewHandle).
		Int("primary_port", runtime.PrimaryPort).
		Time("runtime_lease_expires_at", runtime.LeaseExpiresAt).
		Time("runtime_last_heartbeat_at", runtime.LastHeartbeatAt).
		Str("runtime_unavailable_reason", string(runtime.UnavailableReason))
}

func translateWorkerRuntimeMismatch(resp *http.Response) bool {
	if resp.StatusCode != http.StatusForbidden {
		return false
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return false
	}

	var parsed models.ErrorResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return false
	}
	switch parsed.Error.Code {
	case "WRONG_PREVIEW_WORKER", "PREVIEW_RUNTIME_MISMATCH":
	default:
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return false
	}

	replacement, _ := json.Marshal(models.ErrorResponse{
		Error: models.ErrorDetail{
			Code:    "PREVIEW_RUNTIME_UNAVAILABLE",
			Message: "preview runtime is unavailable; restart the preview",
		},
	})
	resp.StatusCode = http.StatusServiceUnavailable
	resp.Status = fmt.Sprintf("%d %s", http.StatusServiceUnavailable, http.StatusText(http.StatusServiceUnavailable))
	resp.Header = make(http.Header)
	resp.Header.Set("Content-Type", "application/json")
	resp.Header.Set("Cache-Control", "no-store")
	resp.ContentLength = int64(len(replacement))
	resp.Body = io.NopCloser(bytes.NewReader(replacement))
	return true
}

func writeRuntimeUnavailable(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(models.ErrorResponse{
		Error: models.ErrorDetail{
			Code:    "PREVIEW_RUNTIME_UNAVAILABLE",
			Message: "preview runtime is unavailable; restart the preview",
		},
	})
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
	// Skip injection for responses that declare a Content-Length larger than
	// our buffer limit. This avoids reading a partial body that we'd have to
	// serve truncated (the rest would be lost since we close the body).
	if resp.ContentLength > maxHTMLBodySize {
		return nil
	}

	// Read the original body and decode it when the upstream still responded
	// with a supported content encoding.
	var bodyBytes []byte
	var passthroughBody []byte
	contentEncoding := strings.TrimSpace(strings.ToLower(resp.Header.Get("Content-Encoding")))
	recompress := func(body []byte) ([]byte, error) {
		return body, nil
	}

	switch contentEncoding {
	case "", "identity":
	case "gzip":
		compressedBody, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			return fmt.Errorf("read gzipped response body: %w", err)
		}
		passthroughBody = compressedBody

		gr, err := gzip.NewReader(bytes.NewReader(compressedBody))
		if err != nil {
			return fmt.Errorf("gzip reader: %w", err)
		}
		bodyBytes, err = io.ReadAll(io.LimitReader(gr, maxHTMLBodySize+1))
		_ = gr.Close()
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
		compressedBody, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			return fmt.Errorf("read deflate response body: %w", err)
		}
		passthroughBody = compressedBody

		reader := flate.NewReader(bytes.NewReader(compressedBody))
		bodyBytes, err = io.ReadAll(io.LimitReader(reader, maxHTMLBodySize+1))
		_ = reader.Close()
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
		return nil
	}

	if bodyBytes == nil {
		var err error
		bodyBytes, err = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			return fmt.Errorf("read body: %w", err)
		}
	}

	// If the body exceeds the max size, skip injection but serve the
	// complete body so the browser gets a valid (uninstrumented) page.
	if int64(len(bodyBytes)) > maxHTMLBodySize {
		outBytes := bodyBytes
		if len(passthroughBody) > 0 {
			outBytes = passthroughBody
		} else {
			recompressedBody, recompErr := recompress(bodyBytes)
			if recompErr != nil {
				resp.Header.Del("Content-Encoding")
			} else {
				outBytes = recompressedBody
			}
		}
		resp.Body = io.NopCloser(bytes.NewReader(outBytes))
		resp.ContentLength = int64(len(outBytes))
		resp.Header.Set("Content-Length", fmt.Sprintf("%d", len(outBytes)))
		return nil
	}

	// Build the script block to inject.
	scriptBlock := "<script>" + preview.ComponentResolverScript + "</script>" +
		"<script>" + activityHeartbeatScript + "</script>"

	// Walk the HTML via tokenizer so we only match real end tags (not
	// "</head>"/"</body>" appearing inside string literals, JS templates, or
	// comments). Fall back to appending on parse failure.
	newBody := injectBeforeEndTag(bodyBytes, scriptBlock)

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

// injectBeforeEndTag injects scriptBlock immediately before the first real
// </head> or </body> end tag in the document, using an HTML tokenizer so we
// don't match literal tag text appearing inside scripts, attribute values,
// or comments. Falls back to appending scriptBlock when neither tag is
// present (e.g., HTML fragments).
func injectBeforeEndTag(body []byte, scriptBlock string) []byte {
	z := xhtml.NewTokenizer(bytes.NewReader(body))
	offset := -1
	consumed := 0
	for {
		tokenType := z.Next()
		raw := z.Raw()
		tokenStart := consumed
		consumed += len(raw)

		switch tokenType {
		case xhtml.ErrorToken:
			// End of input or parse error — no matching end tag found.
			if offset == -1 {
				return append(body, []byte(scriptBlock)...)
			}
			return spliceAt(body, offset, scriptBlock)
		case xhtml.EndTagToken:
			name, _ := z.TagName()
			if string(name) == "head" {
				return spliceAt(body, tokenStart, scriptBlock)
			}
			if string(name) == "body" && offset == -1 {
				// Remember body offset as a fallback if no </head> appears.
				offset = tokenStart
			}
		}
	}
}

// spliceAt inserts insert at position idx in body and returns a new slice.
func spliceAt(body []byte, idx int, insert string) []byte {
	if idx < 0 || idx > len(body) {
		return append(body, []byte(insert)...)
	}
	out := make([]byte, 0, len(body)+len(insert))
	out = append(out, body[:idx]...)
	out = append(out, []byte(insert)...)
	out = append(out, body[idx:]...)
	return out
}

// =============================================================================
// Security headers
// =============================================================================

func (g *Gateway) injectSecurityHeaders(h http.Header) {
	// X-Frame-Options ALLOW-FROM is deprecated and ignored by modern browsers.
	// The frame-ancestors CSP directive (set below) is the modern replacement.
	h.Set("Content-Security-Policy", g.cspHeader)
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), clipboard-read=(), clipboard-write=()")
}

func stripSensitiveResponseHeaders(h http.Header) {
	// Let sandbox apps manage their own preview-domain auth state (for example
	// session_token and csrf_token) while protecting the gateway-owned preview
	// access cookie from being replaced by the sandbox response.
	setCookies := h.Values("Set-Cookie")
	if len(setCookies) == 0 {
		return
	}

	h.Del("Set-Cookie")
	for _, raw := range setCookies {
		switch setCookieName(raw) {
		case "__Host-preview_session", "preview_session":
			continue
		default:
			h.Add("Set-Cookie", raw)
		}
	}
}

func setCookieName(raw string) string {
	parts := strings.SplitN(raw, ";", 2)
	first := strings.TrimSpace(parts[0])
	kv := strings.SplitN(first, "=", 2)
	if len(kv) != 2 {
		return ""
	}
	return strings.TrimSpace(kv[0])
}

func stripPreviewCookie(req *http.Request) {
	cookies := req.Cookies()
	req.Header.Del("Cookie")
	for _, c := range cookies {
		if c.Name != "__Host-preview_session" && c.Name != "preview_session" {
			req.AddCookie(c)
		}
	}
}

func previewSessionCookie(value string, expiresAt time.Time, secure bool) *http.Cookie {
	// The __Host- prefix requires Secure=true. When running over plain HTTP
	// (e.g., local dev on http://...preview.localhost), drop the prefix so
	// the browser will actually set the cookie.
	name := "__Host-preview_session"
	if !secure {
		name = "preview_session"
	}
	return &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
		Expires:  expiresAt,
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
		return uuid.UUID{}, uuid.UUID{}, uuid.UUID{}, fmt.Errorf("invalid cookie value")
	}
	previewID, err = uuid.Parse(parts[1])
	if err != nil {
		return uuid.UUID{}, uuid.UUID{}, uuid.UUID{}, fmt.Errorf("invalid cookie value")
	}
	accessSessionID, err = uuid.Parse(parts[2])
	if err != nil {
		return uuid.UUID{}, uuid.UUID{}, uuid.UUID{}, fmt.Errorf("invalid cookie value")
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

// deriveWSConnectSrc converts a preview origin template like
// "https://{id}.preview.143.dev" into a CSP connect-src value like
// "wss://*.preview.143.dev". Falls back to a permissive wildcard if the
// template is empty or unparseable.
func deriveWSConnectSrc(template string) string {
	if template == "" {
		return "wss://*.preview.143.dev"
	}
	// Replace {id} with * for wildcard matching.
	wildcard := strings.ReplaceAll(template, "{id}", "*")
	parsed, err := url.Parse(wildcard)
	if err != nil {
		return "wss://*.preview.143.dev"
	}
	scheme := "wss"
	if parsed.Scheme == "http" {
		scheme = "ws"
	}
	return scheme + "://" + parsed.Host
}

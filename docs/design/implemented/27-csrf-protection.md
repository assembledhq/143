# 27 — CSRF Protection

> **Status:** Implemented | **Last reviewed:** 2026-03-25

## Background

The application currently has **no explicit CSRF protection**. State-changing requests rely on:

- `SameSite=Lax` session cookies
- CORS origin whitelisting
- `HttpOnly` cookies (prevents XSS-based cookie theft, but not CSRF)

This is insufficient. `SameSite=Lax` allows cookies on top-level GET navigations from cross-origin sites, meaning any GET endpoint with side effects is vulnerable. It also does not protect against subdomain-based attacks, and older browsers may not enforce `SameSite` at all. A dedicated CSRF defense layer is needed.

## Threat Model

**CSRF (Cross-Site Request Forgery):** An attacker tricks an authenticated user's browser into making an unintended state-changing request to our API. The browser automatically attaches the session cookie, so the server cannot distinguish a legitimate request from a forged one.

**Attack vectors we must defend against:**

1. **Cross-origin form POST** — A hidden `<form>` on `evil.com` auto-submits a POST to our API. The browser sends the session cookie because `SameSite=Lax` does not block form submissions in all cases (especially with redirects or when the form targets the same browsing context).
2. **Subdomain attacks** — If an attacker controls `evil.ourdomain.com`, `SameSite=Lax` will not block the cookie from being sent.
3. **Legacy browsers** — Clients that do not support `SameSite` send cookies unconditionally on cross-origin requests.

**Vectors already defended:**

- **Webhook endpoints** — Use HMAC signature verification (`X-Hub-Signature-256`, `X-Sentry-Hook-Signature`, `X-Linear-Signature`). No browser session involved.
- **OAuth callbacks** — Use a per-flow state cookie (e.g. `github_oauth_state`, `google_oauth_state`, `linear_integration_oauth_state`) to prevent login CSRF.

## Approach: Double-Submit Cookie

We will use the **double-submit cookie** pattern. This is a stateless CSRF defense that does not require server-side token storage or database changes.

### How It Works

1. The server sets a random CSRF token in a **non-HttpOnly** cookie (`csrf_token`) on every response that sets or refreshes a session.
   The cookie `Secure` attribute is request-aware: set for HTTPS requests (including `X-Forwarded-Proto: https`), unset for plain HTTP local development.
2. The frontend reads this cookie and sends the same value back in a custom request header (`X-CSRF-Token`) on every state-changing request (POST, PUT, PATCH, DELETE).
3. The server middleware compares the cookie value to the header value. If they match, the request is legitimate. If not, it's rejected with `403 Forbidden`.

**Why this works:** An attacker on `evil.com` can trigger a cross-origin request that carries the cookie, but they **cannot read** the cookie value (blocked by the Same-Origin Policy) and therefore cannot set the matching header. The header-vs-cookie comparison proves the request originated from our own frontend.

### Why Double-Submit Instead of Synchronizer Token

| Consideration         | Synchronizer Token (server-side) | Double-Submit Cookie |
| --------------------- | -------------------------------- | -------------------- |
| Server-side storage   | Required (session or DB)         | None                 |
| DB schema changes     | Yes                              | No                   |
| Stateless/horizontal  | Harder (shared session store)    | Easy                 |
| Security              | Slightly stronger in theory      | Sufficient with HMAC |
| Implementation effort | Higher                           | Lower                |

We'll sign the CSRF cookie with HMAC-SHA256 using a server-side secret to prevent an attacker from forging their own cookie+header pair. This is the **signed double-submit cookie** variant, which closes the main theoretical weakness of plain double-submit.

## Implementation

### 1. CSRF Middleware (`internal/api/middleware/csrf.go`)

```go
package middleware

import (
    "crypto/hmac"
    "crypto/rand"
    "crypto/sha256"
    "encoding/hex"
    "net/http"
    "strings"
)

const (
    csrfCookieName = "csrf_token"
    csrfHeaderName = "X-CSRF-Token"
    csrfTokenBytes = 32
)

// CSRF returns middleware that enforces double-submit cookie CSRF protection
// on state-changing HTTP methods. Safe methods (GET, HEAD, OPTIONS) are
// skipped. The signingKey is used to HMAC-sign the token so that attackers
// cannot forge a valid cookie+header pair.
func CSRF(signingKey string) func(http.Handler) http.Handler {
    key := []byte(signingKey)
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            // Safe methods: skip CSRF validation but ensure token cookie exists.
            if isSafeMethod(r.Method) {
                ensureCSRFCookie(w, r, key)
                next.ServeHTTP(w, r)
                return
            }

            // State-changing method: validate token.
            cookie, err := r.Cookie(csrfCookieName)
            if err != nil {
                writeError(w, http.StatusForbidden, "CSRF_FAILED", "missing CSRF cookie")
                return
            }

            header := r.Header.Get(csrfHeaderName)
            if header == "" {
                writeError(w, http.StatusForbidden, "CSRF_FAILED", "missing CSRF header")
                return
            }

            if !validSignedToken(cookie.Value, key) {
                writeError(w, http.StatusForbidden, "CSRF_FAILED", "invalid CSRF token")
                return
            }

            // Compare cookie and header values (constant-time).
            if !hmac.Equal([]byte(cookie.Value), []byte(header)) {
                writeError(w, http.StatusForbidden, "CSRF_FAILED", "CSRF token mismatch")
                return
            }

            next.ServeHTTP(w, r)
        })
    }
}

func isSafeMethod(method string) bool {
    switch method {
    case http.MethodGet, http.MethodHead, http.MethodOptions:
        return true
    }
    return false
}

// ensureCSRFCookie sets the CSRF cookie if it doesn't already exist or
// if the existing token has an invalid signature.
func ensureCSRFCookie(w http.ResponseWriter, r *http.Request, key []byte) {
    if c, err := r.Cookie(csrfCookieName); err == nil && validSignedToken(c.Value, key) {
        return // already has a valid token
    }
    token := generateSignedToken(key)
    http.SetCookie(w, &http.Cookie{
        Name:     csrfCookieName,
        Value:    token,
        Path:     "/",
        MaxAge:   30 * 24 * 60 * 60, // match session cookie lifetime
        HttpOnly: false,              // frontend JS must read this
        SameSite: http.SameSiteLaxMode,
        Secure:   true,
    })
}

// generateSignedToken creates "random_hex.signature_hex".
func generateSignedToken(key []byte) string {
    raw := make([]byte, csrfTokenBytes)
    rand.Read(raw)
    payload := hex.EncodeToString(raw)
    sig := computeHMAC(payload, key)
    return payload + "." + sig
}

// validSignedToken checks that the token has the format "payload.signature"
// and the signature matches.
func validSignedToken(token string, key []byte) bool {
    parts := strings.SplitN(token, ".", 2)
    if len(parts) != 2 {
        return false
    }
    expected := computeHMAC(parts[0], key)
    return hmac.Equal([]byte(parts[1]), []byte(expected))
}

func computeHMAC(message string, key []byte) string {
    mac := hmac.New(sha256.New, key)
    mac.Write([]byte(message))
    return hex.EncodeToString(mac.Sum(nil))
}
```

### 2. Configuration (`internal/config/config.go`)

Add a `CSRFSigningKey` field to the `Config` struct:

```go
CSRFSigningKey string `env:"CSRF_SIGNING_KEY"`
```

In production, this must be a high-entropy secret (at least 32 bytes). If empty in development, fall back to a deterministic key derived from `SESSION_SECRET` or similar, so local dev works without extra setup.

### 3. Router Integration (`internal/api/router.go`)

Apply the CSRF middleware **only to the protected route group**, after auth middleware. Webhook routes and public auth routes are excluded by placement.

```go
// Protected routes (authenticated)
r.Group(func(r chi.Router) {
    r.Use(middleware.Auth(sessionStore, userStore, []byte(cfg.CSRFSigningKey), logger))
    r.Use(middleware.OrgContext)
    r.Use(middleware.CSRF(cfg.CSRFSigningKey))  // <-- ADD HERE

    // ... all protected routes unchanged
})
```

This means:
- **Health/readiness/metrics:** No CSRF (public, no session)
- **Webhook routes:** No CSRF (no session; signature-verified)
- **Public auth routes** (`/auth/providers`, `/auth/github/*`, `/auth/google/*`, `/auth/register`, `/auth/login`): No CSRF (pre-authentication)
- **All protected routes:** CSRF enforced on POST/PUT/PATCH/DELETE

The auth middleware now also performs sliding-window session refresh: when a cookie-based session is within the refresh window of its expiry, `expires_at` is pushed back out to the full session TTL and the session cookie is reissued. The CSRF cookie is re-emitted in lockstep via `ExtendCSRFCookie`, which preserves the existing signed token value when valid, so an active user never ends up with a live session but an expired CSRF cookie.

### 4. CORS Update (`internal/api/middleware/cors.go`)

Allow the CSRF header in preflight responses:

```go
// Before:
w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

// After:
w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-CSRF-Token")
```

### 5. Frontend Update (`frontend/src/lib/api.ts`)

Add a helper to read the CSRF cookie and include it as a header on all mutating requests:

```typescript
function getCSRFToken(): string {
  const match = document.cookie
    .split('; ')
    .find(row => row.startsWith('csrf_token='));
  return match ? decodeURIComponent(match.split('=')[1]) : '';
}

async function request<T>(path: string, options?: RequestInit): Promise<T> {
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    ...options?.headers as Record<string, string>,
  };

  // Attach CSRF token on state-changing requests.
  const method = options?.method?.toUpperCase() || 'GET';
  if (method !== 'GET' && method !== 'HEAD') {
    headers['X-CSRF-Token'] = getCSRFToken();
  }

  const res = await fetch(`${API_BASE}${path}`, {
    ...options,
    credentials: 'include',
    headers,
  });

  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    throw new ApiError(
      body?.error?.code || 'UNKNOWN',
      body?.error?.message || res.statusText,
      body?.error?.details
    );
  }

  return res.json();
}
```

No changes needed to the individual `post()`, `patch()`, `del()` wrappers — they all go through `request()`.

### 6. Set CSRF Cookie on Login

The CSRF cookie must be set when a session is established. Add a `setCSRFCookie` call to both `createSessionAndRespond` and `createSessionAndRedirect` in `internal/api/handlers/auth.go`. This can be a standalone helper:

```go
func setCSRFCookie(w http.ResponseWriter, signingKey string) {
    // Use the same generateSignedToken logic from middleware/csrf.go.
    // Export the function or share via an internal package.
    token := csrf.GenerateSignedToken(signingKey)
    http.SetCookie(w, &http.Cookie{
        Name:     "csrf_token",
        Value:    token,
        Path:     "/",
        MaxAge:   30 * 24 * 60 * 60,
        HttpOnly: false,
        SameSite: http.SameSiteLaxMode,
        Secure:   true,
    })
}
```

The CSRF middleware's `ensureCSRFCookie` on GET requests acts as a fallback (e.g., if the cookie expires or is cleared), but explicitly setting it at login time ensures the frontend has a token before making its first POST.

## Endpoint Inventory

### Endpoints Covered by CSRF (protected, state-changing)

| Method   | Path                                          | Role         | Notes                    |
| -------- | --------------------------------------------- | ------------ | ------------------------ |
| POST     | `/api/v1/auth/logout`                         | any authed   |                          |
| PATCH    | `/api/v1/repositories/{id}`                   | admin,member |                          |
| POST     | `/api/v1/issues/{id}/fix`                     | admin,member | Triggers agent run       |
| POST     | `/api/v1/runs/{id}/questions/{qid}/answer`    | admin,member |                          |
| DELETE   | `/api/v1/repositories/{id}`                   | admin        |                          |
| POST     | `/api/v1/issues/{id}/reprioritize`            | admin        |                          |
| PATCH    | `/api/v1/settings`                            | admin        |                          |
| PATCH    | `/api/v1/review-patterns/{id}`                | admin        |                          |
| PUT      | `/api/v1/review-patterns/{id}`                | admin        |                          |
| PUT      | `/api/v1/settings/credentials/{provider}`     | admin        |                          |
| DELETE   | `/api/v1/settings/credentials/{provider}`     | admin        |                          |
| POST     | `/api/v1/settings/codex-auth/initiate`        | admin        | Device code flow         |
| POST     | `/api/v1/settings/codex-auth/disconnect`      | admin        |                          |

### Endpoints NOT Covered (by design)

| Method | Path                                 | Reason                                      |
| ------ | ------------------------------------ | ------------------------------------------- |
| GET    | `/healthz`, `/readyz`, `/metrics`    | Public infrastructure, no session            |
| GET    | `/api/v1/auth/providers`             | Public, read-only                            |
| GET    | `/api/v1/auth/github/login`          | OAuth redirect, pre-auth                     |
| GET    | `/api/v1/auth/github/callback`       | OAuth callback, state-param protected        |
| GET    | `/api/v1/auth/google/login`          | OAuth redirect, pre-auth                     |
| GET    | `/api/v1/auth/google/callback`       | OAuth callback, state-param protected        |
| POST   | `/api/v1/auth/register`              | Pre-auth, no session cookie to exploit       |
| POST   | `/api/v1/auth/login`                 | Pre-auth, no session cookie to exploit       |
| POST   | `/api/v1/webhooks/github`            | External service, HMAC signature verified    |
| POST   | `/api/v1/webhooks/sentry`            | External service, HMAC signature verified    |
| POST   | `/api/v1/webhooks/linear`            | External service, HMAC signature verified    |
| GET    | All protected GET routes             | Safe method, no state change                 |

### Bearer Token Requests

The auth middleware also supports `Authorization: Bearer <token>` for API access. Requests using bearer tokens **do not carry a session cookie** and are therefore not susceptible to CSRF (the browser would not attach a bearer header automatically). The CSRF middleware should skip validation when the request was authenticated via bearer token rather than cookie.

Add to the CSRF middleware:

```go
// Skip CSRF for bearer-token authenticated requests (no cookie = no CSRF risk).
if r.Header.Get("Authorization") != "" {
    next.ServeHTTP(w, r)
    return
}
```

## Best Practices Checklist

These are standard CSRF defense best practices and how we address each:

| Best Practice                                 | Status                                               |
| --------------------------------------------- | ---------------------------------------------------- |
| Custom header on state-changing requests       | `X-CSRF-Token` header on POST/PUT/PATCH/DELETE       |
| Token is cryptographically random              | 32 bytes from `crypto/rand`                          |
| Token is HMAC-signed                           | Signed with server-side key, prevents forgery        |
| Token compared in constant time                | `hmac.Equal()` used for comparison                   |
| Token scoped to session lifetime               | Cookie `MaxAge` matches session (30 days)            |
| Token rotated on login                         | New token set in `createSessionAndRespond/Redirect`  |
| Token cleared on logout                        | Clear `csrf_token` cookie in `Logout` handler        |
| Safe methods (GET/HEAD/OPTIONS) are idempotent | GET endpoints are read-only, no side effects         |
| CORS restricts allowed origins                 | Already in place via `middleware.CORS`               |
| `SameSite=Lax` as defense-in-depth             | Already set on session cookie; also set on CSRF cookie |
| `Secure` flag on cookies                       | Set on CSRF cookie; should also be added to session cookie in production |

## Testing

### Unit Tests (`internal/api/middleware/csrf_test.go`)

1. **Safe methods pass through** — GET, HEAD, OPTIONS requests succeed without CSRF header.
2. **POST without cookie** — Returns 403 with `CSRF_FAILED`.
3. **POST without header** — Returns 403 with `CSRF_FAILED`.
4. **POST with mismatched cookie/header** — Returns 403 with `CSRF_FAILED`.
5. **POST with matching cookie/header** — Returns 200.
6. **POST with tampered signature** — Returns 403 with `CSRF_FAILED`.
7. **Bearer token requests skip CSRF** — POST with `Authorization: Bearer` and no CSRF header succeeds.
8. **Cookie is set on first GET** — Response includes `Set-Cookie: csrf_token=...`.

### Integration Tests

1. **Full login → POST flow** — Login via email, extract CSRF cookie, send POST with `X-CSRF-Token` header, verify success.
2. **Cross-origin POST without CSRF header** — Simulate a CSRF attack, verify 403.

## Rollout Plan

1. **Add `CSRF_SIGNING_KEY` to environment configuration** for all environments.
2. **Implement middleware and frontend changes** behind a feature flag if needed, or deploy backend-first in permissive mode (log warnings instead of blocking) for one release cycle.
3. **Deploy backend** with CSRF middleware active.
4. **Deploy frontend** with `X-CSRF-Token` header on all mutating requests.
5. **Monitor** for 403 CSRF errors in production logs. If error rate is high, check for missed endpoints or third-party integrations making cookie-authenticated requests.
6. **Remove permissive mode** once confirmed stable.

## Security Considerations

- **Key rotation:** If `CSRF_SIGNING_KEY` is rotated, all existing CSRF cookies become invalid. Users will get a new valid cookie on their next GET request, so this is a graceful degradation — at worst they'll see one failed POST and retry.
- **XSS undermines CSRF protection.** If an attacker can execute JavaScript in the app, they can read the CSRF cookie and forge requests. CSRF protection is not a substitute for XSS prevention. The existing `HttpOnly` session cookie + Content-Security-Policy (if added) remain the primary XSS defenses.
- **`Secure` flag:** The CSRF cookie sets `Secure: true` so it is only sent over HTTPS. The session cookie should also set `Secure: true` in production — this is a pre-existing gap to fix alongside this work.

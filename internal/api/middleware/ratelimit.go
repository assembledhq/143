package middleware

import (
	"context"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// RateLimitConfig defines rate limiting parameters.
type RateLimitConfig struct {
	OrgRequestsPerSecond int // max requests per second per org
	IPRequestsPerSecond  int // max requests per second per IP
}

// DefaultRateLimitConfig returns the default rate limiting config.
func DefaultRateLimitConfig() RateLimitConfig {
	return RateLimitConfig{
		OrgRequestsPerSecond: 100,
		IPRequestsPerSecond:  20,
	}
}

type tokenBucket struct {
	tokens     float64
	maxTokens  float64
	refillRate float64 // tokens per second
	lastRefill time.Time
	mu         sync.Mutex
}

func newTokenBucket(ratePerSecond int) *tokenBucket {
	return &tokenBucket{
		tokens:     float64(ratePerSecond),
		maxTokens:  float64(ratePerSecond),
		refillRate: float64(ratePerSecond),
		lastRefill: time.Now(),
	}
}

func (tb *tokenBucket) allow() bool {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(tb.lastRefill).Seconds()
	tb.tokens = min(tb.maxTokens, tb.tokens+elapsed*tb.refillRate)
	tb.lastRefill = now

	if tb.tokens < 1 {
		return false
	}
	tb.tokens--
	return true
}

type rateLimiter struct {
	orgBuckets map[uuid.UUID]*tokenBucket
	ipBuckets  map[string]*tokenBucket
	config     RateLimitConfig
	mu         sync.RWMutex
}

func newRateLimiter(config RateLimitConfig) *rateLimiter {
	rl := &rateLimiter{
		orgBuckets: make(map[uuid.UUID]*tokenBucket),
		ipBuckets:  make(map[string]*tokenBucket),
		config:     config,
	}
	// Start cleanup goroutine
	go rl.cleanup()
	return rl
}

func (rl *rateLimiter) getOrgBucket(orgID uuid.UUID) *tokenBucket {
	rl.mu.RLock()
	bucket, ok := rl.orgBuckets[orgID]
	rl.mu.RUnlock()
	if ok {
		return bucket
	}

	rl.mu.Lock()
	defer rl.mu.Unlock()
	// Double-check after acquiring write lock
	if bucket, ok := rl.orgBuckets[orgID]; ok {
		return bucket
	}
	bucket = newTokenBucket(rl.config.OrgRequestsPerSecond)
	rl.orgBuckets[orgID] = bucket
	return bucket
}

func (rl *rateLimiter) getIPBucket(ip string) *tokenBucket {
	rl.mu.RLock()
	bucket, ok := rl.ipBuckets[ip]
	rl.mu.RUnlock()
	if ok {
		return bucket
	}

	rl.mu.Lock()
	defer rl.mu.Unlock()
	if bucket, ok := rl.ipBuckets[ip]; ok {
		return bucket
	}
	bucket = newTokenBucket(rl.config.IPRequestsPerSecond)
	rl.ipBuckets[ip] = bucket
	return bucket
}

func (rl *rateLimiter) cleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		rl.mu.Lock()
		now := time.Now()
		for k, v := range rl.ipBuckets {
			v.mu.Lock()
			if now.Sub(v.lastRefill) > 10*time.Minute {
				delete(rl.ipBuckets, k)
			}
			v.mu.Unlock()
		}
		for k, v := range rl.orgBuckets {
			v.mu.Lock()
			if now.Sub(v.lastRefill) > 10*time.Minute {
				delete(rl.orgBuckets, k)
			}
			v.mu.Unlock()
		}
		rl.mu.Unlock()
	}
}

// RateLimit returns middleware that enforces per-org and per-IP rate limits.
func RateLimit(config RateLimitConfig) func(http.Handler) http.Handler {
	return RateLimitExcept(config, nil)
}

func RateLimitExcept(config RateLimitConfig, excludedPaths map[string]struct{}) func(http.Handler) http.Handler {
	limiter := newRateLimiter(config)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, excluded := excludedPaths[r.URL.Path]; excluded {
				next.ServeHTTP(w, r)
				return
			}
			// Check IP rate limit
			ip := extractIP(r)
			if !limiter.getIPBucket(ip).allow() {
				zerolog.Ctx(r.Context()).Warn().Str("ip", ip).Msg("IP rate limit exceeded")
				w.Header().Set("Retry-After", "1")
				http.Error(w, `{"error":{"code":"RATE_LIMITED","message":"too many requests"}}`, http.StatusTooManyRequests)
				return
			}

			// Check org rate limit if authenticated
			orgID := OrgIDFromContext(r.Context())
			if orgID != uuid.Nil {
				if !limiter.getOrgBucket(orgID).allow() {
					zerolog.Ctx(r.Context()).Warn().Str("org_id", orgID.String()).Msg("org rate limit exceeded")
					w.Header().Set("Retry-After", "1")
					http.Error(w, `{"error":{"code":"RATE_LIMITED","message":"org rate limit exceeded"}}`, http.StatusTooManyRequests)
					return
				}
			}

			next.ServeHTTP(w, r)
		})
	}
}

// ClaimRateLimit returns a dedicated rate limiter for the invitation claim
// endpoint. The general RateLimit middleware (20 req/s per IP = 1200/min)
// is permissive by design because most authenticated endpoints are called
// repeatedly during normal navigation; a token-guessing attacker needs far
// fewer attempts per minute than that to remain under the threshold.
//
// The claim endpoint is special because each request names an opaque token
// the server looks up against the invitations table — a brute-force attempt
// to find a valid token reduces to hammering this single endpoint. Keeping
// it at 10 attempts/minute per IP and per user forces any attacker into
// detectable territory quickly. Legitimate users click the email link once;
// a human retrying a copy-paste failure ten times in a minute is already an
// outlier.
//
// The limit is keyed jointly on IP *and* authenticated user: a logged-in
// attacker cycling through tokens from one workstation trips both. The
// per-user bucket means a shared NAT doesn't accidentally rate-limit
// concurrent legitimate users on the same IP below their own quota.
func ClaimRateLimit(perMinute int) func(http.Handler) http.Handler {
	ipBuckets := &sync.Map{}
	userBuckets := &sync.Map{}
	refillPerSecond := float64(perMinute) / 60.0

	getBucket := func(m *sync.Map, key string) *tokenBucket {
		if existing, ok := m.Load(key); ok {
			return existing.(*tokenBucket)
		}
		fresh := &tokenBucket{
			tokens:     float64(perMinute),
			maxTokens:  float64(perMinute),
			refillRate: refillPerSecond,
			lastRefill: time.Now(),
		}
		actual, _ := m.LoadOrStore(key, fresh)
		return actual.(*tokenBucket)
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Deliberately use r.RemoteAddr and ignore X-Forwarded-For here.
			// The general-purpose extractIP trusts XFF because it simplifies
			// bucketing behind a reverse proxy, but XFF is attacker-controlled
			// — rotating the header trivially bypasses an IP bucket. On the
			// invitation claim path that bucket is load-bearing anti-brute-
			// force, so we fall back to the TCP peer address and accept that
			// NAT gateways share a bucket. The per-user bucket below still
			// prevents single-user enumeration across proxied deployments.
			ip := remoteAddrIP(r)
			if !getBucket(ipBuckets, ip).allow() {
				zerolog.Ctx(r.Context()).Warn().Str("ip", ip).Msg("invitation claim rate limit exceeded (IP)")
				writeRateLimited(w, "CLAIM_RATE_LIMITED", "too many invitation claim attempts; try again in a minute", "60")
				return
			}

			if user := UserFromContext(r.Context()); user != nil {
				if !getBucket(userBuckets, user.ID.String()).allow() {
					zerolog.Ctx(r.Context()).Warn().Str("user_id", user.ID.String()).Msg("invitation claim rate limit exceeded (user)")
					writeRateLimited(w, "CLAIM_RATE_LIMITED", "too many invitation claim attempts; try again in a minute", "60")
					return
				}
			}

			next.ServeHTTP(w, r)
		})
	}
}

// CreateOrgRateLimit returns a per-hour rate limiter for POST /organizations.
// Sized much tighter than ClaimRateLimit: a legitimate user creates an org
// maybe once per onboarding and then never again, so any bucket that refills
// faster than once every few minutes is room for spam. 5/hour per user and
// per IP keeps the door open for the rare genuine multi-org case (a user
// creating two workspaces in the same session) while closing it on scripted
// creation from a single credential or host.
//
// Requires authentication: the user bucket is the primary defense and
// anonymous callers should already have been rejected by the auth middleware
// upstream; the IP bucket is a backstop for any future unauthenticated path.
//
// Bucket maps are pruned in the background to prevent unbounded memory growth
// over process lifetime — entries idle for more than 2h (twice the refill
// window) are discarded because a bucket that's been untouched that long is
// indistinguishable from a fresh one anyway. The prune goroutine exits when
// ctx is cancelled; in production this is the server lifetime, in tests it
// should be t.Context() so each test does not leak a goroutine for the life
// of the test binary.
func CreateOrgRateLimit(ctx context.Context, perHour int) func(http.Handler) http.Handler {
	ipBuckets := &sync.Map{}
	userBuckets := &sync.Map{}
	refillPerSecond := float64(perHour) / 3600.0

	getBucket := func(m *sync.Map, key string) *tokenBucket {
		if existing, ok := m.Load(key); ok {
			return existing.(*tokenBucket)
		}
		fresh := &tokenBucket{
			tokens:     float64(perHour),
			maxTokens:  float64(perHour),
			refillRate: refillPerSecond,
			lastRefill: time.Now(),
		}
		actual, _ := m.LoadOrStore(key, fresh)
		return actual.(*tokenBucket)
	}

	startPruneLoop(ctx, ipBuckets, userBuckets, 10*time.Minute, 2*time.Hour)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Order matters: IP first, user second. A request that passes IP
			// but fails the user check still consumes an IP token. That is
			// deliberate — we want scripted abuse from a single host to count
			// against the host even when the per-user cap already rejected
			// it, so rotating fresh user IDs on the same IP cannot dodge the
			// IP backstop. The practical cost is that legitimate users
			// sharing a NAT with a flooder may see IP rejections sooner; at
			// 5/hour across any shared egress, that tradeoff favors the cap.
			ip := remoteAddrIP(r)
			if !getBucket(ipBuckets, ip).allow() {
				zerolog.Ctx(r.Context()).Warn().Str("ip", ip).Msg("create-org rate limit exceeded (IP)")
				writeRateLimited(w, "CREATE_ORG_RATE_LIMITED", "too many organization-creation attempts; try again later", "3600")
				return
			}

			if user := UserFromContext(r.Context()); user != nil {
				if !getBucket(userBuckets, user.ID.String()).allow() {
					zerolog.Ctx(r.Context()).Warn().Str("user_id", user.ID.String()).Msg("create-org rate limit exceeded (user)")
					writeRateLimited(w, "CREATE_ORG_RATE_LIMITED", "too many organization-creation attempts; try again later", "3600")
					return
				}
			}

			next.ServeHTTP(w, r)
		})
	}
}

// startPruneLoop runs a background goroutine that evicts bucket-map entries
// whose lastRefill is older than staleAfter. Exits when ctx is cancelled.
// Exported as an internal helper so tests can drive pruning synchronously
// via pruneStaleBuckets.
func startPruneLoop(ctx context.Context, ipBuckets, userBuckets *sync.Map, interval, staleAfter time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				pruneStaleBuckets(ipBuckets, staleAfter)
				pruneStaleBuckets(userBuckets, staleAfter)
			}
		}
	}()
}

// writeRateLimited writes a 429 response with a JSON error body and a
// Retry-After header. Use instead of http.Error for rate-limit rejections so
// the Content-Type is application/json (http.Error would set text/plain and
// append a trailing newline, leaving a body that looks like JSON but is
// served as text).
func writeRateLimited(w http.ResponseWriter, code, message, retryAfterSeconds string) {
	w.Header().Set("Retry-After", retryAfterSeconds)
	writeError(w, http.StatusTooManyRequests, code, message)
}

// pruneStaleBuckets removes entries whose bucket has not refilled within the
// last staleAfter window. Safe to call concurrently with allow() because the
// bucket's own mutex guards lastRefill.
func pruneStaleBuckets(m *sync.Map, staleAfter time.Duration) {
	cutoff := time.Now().Add(-staleAfter)
	m.Range(func(key, value any) bool {
		bucket, ok := value.(*tokenBucket)
		if !ok {
			return true
		}
		bucket.mu.Lock()
		stale := bucket.lastRefill.Before(cutoff)
		bucket.mu.Unlock()
		if stale {
			m.Delete(key)
		}
		return true
	})
}

// extractIP returns the client's IP for rate-limit bucketing. Prefers the
// first entry in X-Forwarded-For when present (reverse-proxy deployments) and
// otherwise falls back to the TCP peer address. The XFF path trusts its input
// — callers on security-critical paths (e.g. ClaimRateLimit) should use
// remoteAddrIP instead so an attacker cannot rotate the header to sidestep
// the IP bucket.
func extractIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if comma := strings.IndexByte(xff, ','); comma >= 0 {
			return strings.TrimSpace(xff[:comma])
		}
		return strings.TrimSpace(xff)
	}
	return remoteAddrIP(r)
}

// remoteAddrIP returns the TCP peer IP (host portion of r.RemoteAddr) without
// consulting any request-controlled header. Use this for rate-limit buckets
// that guard against abuse; extractIP is fine for best-effort shaping.
func remoteAddrIP(r *http.Request) string {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}

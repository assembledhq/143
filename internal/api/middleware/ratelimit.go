package middleware

import (
	"net"
	"net/http"
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
	limiter := newRateLimiter(config)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

func extractIP(r *http.Request) string {
	// Check X-Forwarded-For header first (for reverse proxy setups)
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// Take the first IP (client IP)
		if idx := len(xff); idx > 0 {
			for i, c := range xff {
				if c == ',' {
					return xff[:i]
				}
				_ = i // suppress unused variable
			}
			return xff
		}
	}
	// Fall back to RemoteAddr
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}

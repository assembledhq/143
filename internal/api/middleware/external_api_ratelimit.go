package middleware

import (
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"
)

type ExternalAPIRateLimitConfig struct {
	ReadRequestsPerMinute     int
	MutatingRequestsPerMinute int
	Now                       func() time.Time
}

type ExternalAPIRateLimiter struct {
	cfg     ExternalAPIRateLimitConfig
	mu      sync.Mutex
	buckets map[string]externalAPIBucket
}

type externalAPIBucket struct {
	windowStart time.Time
	count       int
}

func DefaultExternalAPIRateLimitConfig() ExternalAPIRateLimitConfig {
	return ExternalAPIRateLimitConfig{
		ReadRequestsPerMinute:     600,
		MutatingRequestsPerMinute: 120,
		Now:                       time.Now,
	}
}

func NewExternalAPIRateLimiter(cfg ExternalAPIRateLimitConfig) *ExternalAPIRateLimiter {
	defaults := DefaultExternalAPIRateLimitConfig()
	if cfg.ReadRequestsPerMinute <= 0 {
		cfg.ReadRequestsPerMinute = defaults.ReadRequestsPerMinute
	}
	if cfg.MutatingRequestsPerMinute <= 0 {
		cfg.MutatingRequestsPerMinute = defaults.MutatingRequestsPerMinute
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &ExternalAPIRateLimiter{cfg: cfg, buckets: make(map[string]externalAPIBucket)}
}

func (l *ExternalAPIRateLimiter) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := APITokenFromContext(r.Context())
			client := APIClientFromContext(r.Context())
			if token == nil || client == nil {
				next.ServeHTTP(w, r)
				return
			}

			kind := "read"
			limit := l.cfg.ReadRequestsPerMinute
			if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
				kind = "mutation"
				limit = l.cfg.MutatingRequestsPerMinute
			}
			remaining, reset, allowed := l.allow(fmt.Sprintf("%s:%s:%s", client.OrgID, token.ID, kind), limit)
			setExternalRateLimitHeaders(w, limit, remaining, reset)
			if !allowed {
				w.Header().Set("Retry-After", strconv.FormatInt(reset-l.cfg.Now().Unix(), 10))
				writeError(w, http.StatusTooManyRequests, "RATE_LIMITED", "external API rate limit exceeded")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (l *ExternalAPIRateLimiter) allow(key string, limit int) (int, int64, bool) {
	now := l.cfg.Now()
	windowStart := now.Truncate(time.Minute)
	reset := windowStart.Add(time.Minute).Unix()

	l.mu.Lock()
	defer l.mu.Unlock()

	bucket := l.buckets[key]
	if bucket.windowStart != windowStart {
		bucket = externalAPIBucket{windowStart: windowStart}
	}
	bucket.count++
	l.buckets[key] = bucket
	remaining := limit - bucket.count
	if remaining < 0 {
		remaining = 0
	}
	return remaining, reset, bucket.count <= limit
}

func setExternalRateLimitHeaders(w http.ResponseWriter, limit, remaining int, reset int64) {
	w.Header().Set("X-RateLimit-Limit", strconv.Itoa(limit))
	w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(remaining))
	w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset, 10))
}

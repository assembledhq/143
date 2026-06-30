package middleware

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestRateLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		config       RateLimitConfig
		requests     func(handler http.Handler) *httptest.ResponseRecorder
		expectedCode int
		checkHeaders func(t *testing.T, w *httptest.ResponseRecorder)
	}{
		{
			name: "allows normal traffic within IP limit",
			config: RateLimitConfig{
				OrgRequestsPerSecond: 100,
				IPRequestsPerSecond:  20,
			},
			requests: func(handler http.Handler) *httptest.ResponseRecorder {
				req := httptest.NewRequest(http.MethodGet, "/", nil)
				req.RemoteAddr = "192.168.1.1:12345"
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, req)
				return w
			},
			expectedCode: http.StatusOK,
			checkHeaders: nil,
		},
		{
			name: "blocks excessive IP requests with 429 and Retry-After header",
			config: RateLimitConfig{
				OrgRequestsPerSecond: 100,
				IPRequestsPerSecond:  3,
			},
			requests: func(handler http.Handler) *httptest.ResponseRecorder {
				// Exhaust the IP limit
				for i := 0; i < 3; i++ {
					req := httptest.NewRequest(http.MethodGet, "/", nil)
					req.RemoteAddr = "10.0.0.1:12345"
					w := httptest.NewRecorder()
					handler.ServeHTTP(w, req)
				}
				// This request should be rate limited
				req := httptest.NewRequest(http.MethodGet, "/", nil)
				req.RemoteAddr = "10.0.0.1:12345"
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, req)
				return w
			},
			expectedCode: http.StatusTooManyRequests,
			checkHeaders: func(t *testing.T, w *httptest.ResponseRecorder) {
				t.Helper()
				require.Equal(t, "1", w.Header().Get("Retry-After"), "should set Retry-After header to 1 second")
			},
		},
		{
			name: "blocks excessive org requests with 429",
			config: RateLimitConfig{
				OrgRequestsPerSecond: 2,
				IPRequestsPerSecond:  100,
			},
			requests: func(handler http.Handler) *httptest.ResponseRecorder {
				orgID := uuid.MustParse("aaaaaaaa-0000-0000-0000-000000000001")
				// Exhaust the org limit
				for i := 0; i < 2; i++ {
					req := httptest.NewRequest(http.MethodGet, "/", nil)
					req.RemoteAddr = "10.0.0.1:12345"
					ctx := WithOrgID(req.Context(), orgID)
					req = req.WithContext(ctx)
					w := httptest.NewRecorder()
					handler.ServeHTTP(w, req)
				}
				// This request should be rate limited
				req := httptest.NewRequest(http.MethodGet, "/", nil)
				req.RemoteAddr = "10.0.0.1:12345"
				ctx := WithOrgID(req.Context(), orgID)
				req = req.WithContext(ctx)
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, req)
				return w
			},
			expectedCode: http.StatusTooManyRequests,
			checkHeaders: nil,
		},
		{
			name: "different IPs have separate rate limit buckets",
			config: RateLimitConfig{
				OrgRequestsPerSecond: 100,
				IPRequestsPerSecond:  2,
			},
			requests: func(handler http.Handler) *httptest.ResponseRecorder {
				// Exhaust IP 1 bucket
				for i := 0; i < 2; i++ {
					req := httptest.NewRequest(http.MethodGet, "/", nil)
					req.RemoteAddr = "10.0.0.1:12345"
					w := httptest.NewRecorder()
					handler.ServeHTTP(w, req)
				}
				// IP 2 should still have its own bucket
				req := httptest.NewRequest(http.MethodGet, "/", nil)
				req.RemoteAddr = "10.0.0.2:12345"
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, req)
				return w
			},
			expectedCode: http.StatusOK,
			checkHeaders: nil,
		},
		{
			name: "uses X-Forwarded-For header for IP extraction",
			config: RateLimitConfig{
				OrgRequestsPerSecond: 100,
				IPRequestsPerSecond:  2,
			},
			requests: func(handler http.Handler) *httptest.ResponseRecorder {
				// Exhaust rate limit for XFF IP
				for i := 0; i < 2; i++ {
					req := httptest.NewRequest(http.MethodGet, "/", nil)
					req.RemoteAddr = "10.0.0.99:12345"
					req.Header.Set("X-Forwarded-For", "203.0.113.1, 10.0.0.1")
					w := httptest.NewRecorder()
					handler.ServeHTTP(w, req)
				}
				// Next request with same XFF should be blocked
				req := httptest.NewRequest(http.MethodGet, "/", nil)
				req.RemoteAddr = "10.0.0.99:12345"
				req.Header.Set("X-Forwarded-For", "203.0.113.1, 10.0.0.1")
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, req)
				return w
			},
			expectedCode: http.StatusTooManyRequests,
			checkHeaders: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Not parallel: rate limiter tests share mutable state within each subtest's
			// handler instance, but each subtest creates its own limiter via RateLimit(config).
			// However, the cleanup goroutine makes parallel execution safe since each test
			// creates its own limiter. We can run parallel here.
			t.Parallel()

			handler := RateLimit(tt.config)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))

			w := tt.requests(handler)
			require.Equal(t, tt.expectedCode, w.Code, "should return expected HTTP status code")
			if tt.checkHeaders != nil {
				tt.checkHeaders(t, w)
			}
		})
	}
}

func TestExtractIP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		remoteAddr    string
		xForwardedFor string
		expectedIP    string
	}{
		{
			name:       "extracts IP from RemoteAddr",
			remoteAddr: "192.168.1.1:12345",
			expectedIP: "192.168.1.1",
		},
		{
			name:          "extracts first IP from X-Forwarded-For with multiple IPs",
			remoteAddr:    "10.0.0.1:12345",
			xForwardedFor: "203.0.113.50, 70.41.3.18, 10.0.0.1",
			expectedIP:    "203.0.113.50",
		},
		{
			name:          "extracts IP from X-Forwarded-For with single IP",
			remoteAddr:    "10.0.0.1:12345",
			xForwardedFor: "203.0.113.50",
			expectedIP:    "203.0.113.50",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = tt.remoteAddr
			if tt.xForwardedFor != "" {
				req.Header.Set("X-Forwarded-For", tt.xForwardedFor)
			}

			require.Equal(t, tt.expectedIP, extractIP(req), "should extract expected IP address")
		})
	}
}

// ClaimRateLimit is tuned tighter than the general rate limiter because the
// invitation claim endpoint is a brute-force target. The tests cover the
// three-key behavior the middleware promises: per-IP blocking, per-user
// blocking (so shared NATs don't false-positive each other), and a clean
// pass-through when neither bucket is exhausted.
func TestClaimRateLimit(t *testing.T) {
	t.Parallel()

	t.Run("allows traffic within the per-IP bucket", func(t *testing.T) {
		t.Parallel()
		handler := ClaimRateLimit(5)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		req := httptest.NewRequest(http.MethodPost, "/claim", nil)
		req.RemoteAddr = "10.0.1.1:1234"
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("blocks the IP after the bucket empties with 429 and Retry-After 60", func(t *testing.T) {
		t.Parallel()
		handler := ClaimRateLimit(2)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		for i := 0; i < 2; i++ {
			req := httptest.NewRequest(http.MethodPost, "/claim", nil)
			req.RemoteAddr = "10.0.2.1:1234"
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			require.Equal(t, http.StatusOK, w.Code)
		}
		req := httptest.NewRequest(http.MethodPost, "/claim", nil)
		req.RemoteAddr = "10.0.2.1:1234"
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		require.Equal(t, http.StatusTooManyRequests, w.Code)
		require.Equal(t, "60", w.Header().Get("Retry-After"))
		require.Contains(t, w.Body.String(), "CLAIM_RATE_LIMITED")
	})

	t.Run("ignores X-Forwarded-For so rotating it cannot bypass the IP bucket", func(t *testing.T) {
		t.Parallel()
		handler := ClaimRateLimit(2)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		// Same peer address; attacker cycles XFF each request.
		for i := 0; i < 2; i++ {
			req := httptest.NewRequest(http.MethodPost, "/claim", nil)
			req.RemoteAddr = "10.0.4.1:1234"
			req.Header.Set("X-Forwarded-For", fmt.Sprintf("203.0.113.%d", i+1))
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			require.Equal(t, http.StatusOK, w.Code)
		}
		// A fresh XFF must not reset the bucket — peer address is what counts.
		req := httptest.NewRequest(http.MethodPost, "/claim", nil)
		req.RemoteAddr = "10.0.4.1:1234"
		req.Header.Set("X-Forwarded-For", "203.0.113.99")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		require.Equal(t, http.StatusTooManyRequests, w.Code)
	})

	t.Run("blocks the user across IPs once their per-user bucket empties", func(t *testing.T) {
		t.Parallel()
		handler := ClaimRateLimit(2)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		user := &models.User{ID: uuid.New()}
		for i := 0; i < 2; i++ {
			req := httptest.NewRequest(http.MethodPost, "/claim", nil)
			req.RemoteAddr = fmt.Sprintf("10.0.3.%d:1234", i+1)
			req = req.WithContext(WithUser(req.Context(), user))
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			require.Equal(t, http.StatusOK, w.Code)
		}
		req := httptest.NewRequest(http.MethodPost, "/claim", nil)
		req.RemoteAddr = "10.0.3.99:1234"
		req = req.WithContext(WithUser(req.Context(), user))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		require.Equal(t, http.StatusTooManyRequests, w.Code)
		require.Contains(t, w.Body.String(), "CLAIM_RATE_LIMITED")
	})
}

func TestDemoEntryRateLimit(t *testing.T) {
	t.Parallel()

	okHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	t.Run("separates clients by X-Forwarded-For behind a proxy", func(t *testing.T) {
		t.Parallel()

		handler := DemoEntryRateLimit(1)(okHandler)

		first := httptest.NewRequest(http.MethodPost, "/api/v1/auth/demo", nil)
		first.RemoteAddr = "172.18.0.2:1234"
		first.Header.Set("X-Forwarded-For", "203.0.113.10")
		firstRecorder := httptest.NewRecorder()
		handler.ServeHTTP(firstRecorder, first)
		require.Equal(t, http.StatusOK, firstRecorder.Code, "first forwarded client should enter the demo")

		second := httptest.NewRequest(http.MethodPost, "/api/v1/auth/demo", nil)
		second.RemoteAddr = "172.18.0.2:5678"
		second.Header.Set("X-Forwarded-For", "203.0.113.11")
		secondRecorder := httptest.NewRecorder()
		handler.ServeHTTP(secondRecorder, second)
		require.Equal(t, http.StatusOK, secondRecorder.Code, "different forwarded client should have its own demo entry bucket")
	})

	t.Run("blocks repeated entries from the same forwarded client", func(t *testing.T) {
		t.Parallel()

		handler := DemoEntryRateLimit(1)(okHandler)

		first := httptest.NewRequest(http.MethodPost, "/api/v1/auth/demo", nil)
		first.RemoteAddr = "172.18.0.2:1234"
		first.Header.Set("X-Forwarded-For", "203.0.113.20")
		firstRecorder := httptest.NewRecorder()
		handler.ServeHTTP(firstRecorder, first)
		require.Equal(t, http.StatusOK, firstRecorder.Code, "first demo entry attempt should be allowed")

		second := httptest.NewRequest(http.MethodPost, "/api/v1/auth/demo", nil)
		second.RemoteAddr = "172.18.0.2:5678"
		second.Header.Set("X-Forwarded-For", "203.0.113.20")
		secondRecorder := httptest.NewRecorder()
		handler.ServeHTTP(secondRecorder, second)
		require.Equal(t, http.StatusTooManyRequests, secondRecorder.Code, "same forwarded client should be rate limited after the bucket empties")
		require.Contains(t, secondRecorder.Body.String(), "DEMO_ENTRY_RATE_LIMITED", "demo entry limiter should return a specific error code")
	})
}

func TestCreateOrgRateLimit(t *testing.T) {
	t.Parallel()

	// Silent "OK" handler that the middleware wraps; we only care about whether
	// the middleware admits or rejects the request.
	okHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	t.Run("allows the first N requests and blocks the N+1th with 429 + Retry-After", func(t *testing.T) {
		t.Parallel()

		const perHour = 3
		handler := CreateOrgRateLimit(t.Context(), perHour)(okHandler)
		user := &models.User{ID: uuid.New()}

		for i := 0; i < perHour; i++ {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/organizations", nil)
			req.RemoteAddr = "10.0.4.1:1234"
			req = req.WithContext(WithUser(req.Context(), user))
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			require.Equalf(t, http.StatusOK, w.Code, "request %d/%d inside the bucket should pass", i+1, perHour)
		}

		// First over-budget request.
		req := httptest.NewRequest(http.MethodPost, "/api/v1/organizations", nil)
		req.RemoteAddr = "10.0.4.1:1234"
		req = req.WithContext(WithUser(req.Context(), user))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		require.Equal(t, http.StatusTooManyRequests, w.Code)
		require.Equal(t, "3600", w.Header().Get("Retry-After"))
		require.Contains(t, w.Body.String(), "CREATE_ORG_RATE_LIMITED")
	})

	t.Run("per-user bucket trips even when IP bucket is fresh", func(t *testing.T) {
		t.Parallel()

		const perHour = 2
		handler := CreateOrgRateLimit(t.Context(), perHour)(okHandler)
		user := &models.User{ID: uuid.New()}

		// Exhaust the user bucket by cycling IP addresses, so the IP bucket
		// never becomes the blocker.
		for i := 0; i < perHour; i++ {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/organizations", nil)
			req.RemoteAddr = fmt.Sprintf("10.0.5.%d:1234", i+1)
			req = req.WithContext(WithUser(req.Context(), user))
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			require.Equal(t, http.StatusOK, w.Code)
		}

		// Fresh IP, same user → user bucket should still reject.
		req := httptest.NewRequest(http.MethodPost, "/api/v1/organizations", nil)
		req.RemoteAddr = "10.0.5.99:1234"
		req = req.WithContext(WithUser(req.Context(), user))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		require.Equal(t, http.StatusTooManyRequests, w.Code)
		require.Contains(t, w.Body.String(), "CREATE_ORG_RATE_LIMITED")
	})
}

func TestPruneStaleBuckets(t *testing.T) {
	t.Parallel()

	m := &sync.Map{}
	fresh := &tokenBucket{lastRefill: time.Now()}
	stale := &tokenBucket{lastRefill: time.Now().Add(-3 * time.Hour)}
	m.Store("fresh", fresh)
	m.Store("stale", stale)

	pruneStaleBuckets(m, 2*time.Hour)

	_, freshOK := m.Load("fresh")
	_, staleOK := m.Load("stale")
	require.True(t, freshOK, "fresh bucket should survive pruning")
	require.False(t, staleOK, "stale bucket should be evicted")
}

func TestDefaultRateLimitConfig(t *testing.T) {
	t.Parallel()

	cfg := DefaultRateLimitConfig()
	require.Equal(t, RateLimitConfig{
		OrgRequestsPerSecond: 100,
		IPRequestsPerSecond:  20,
	}, cfg, "should return default rate limit config values")
}

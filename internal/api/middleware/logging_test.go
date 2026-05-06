package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/observability"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestLogging(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		handler      http.HandlerFunc
		expectedCode int
		expectedBody string
	}{
		{
			name: "logs request and calls next handler",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			},
			expectedCode: http.StatusOK,
			expectedBody: "",
		},
		{
			name: "captures non-200 status code",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNotFound)
			},
			expectedCode: http.StatusNotFound,
			expectedBody: "",
		},
		{
			name: "defaults to 200 when WriteHeader is not called",
			handler: func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte("hello"))
			},
			expectedCode: http.StatusOK,
			expectedBody: "hello",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			logger := zerolog.Nop()
			handler := Logging(logger, nil)(tt.handler)

			req := httptest.NewRequest(http.MethodGet, "/test-path", nil)
			w := httptest.NewRecorder()

			handler.ServeHTTP(w, req)

			require.Equal(t, tt.expectedCode, w.Code, "should return expected HTTP status code")
			if tt.expectedBody != "" {
				require.Equal(t, tt.expectedBody, w.Body.String(), "should return expected response body")
			}
		})
	}
}

func TestLogging_ErrorResponsesAreLoggedAtErrorLevel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                 string
		handler              http.HandlerFunc
		expectedLevel        string
		expectedErrorCode    string
		expectedErrorMessage string
	}{
		{
			name: "500 response logs error details at error level",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":{"code":"AUTH_INITIATE_FAILED","message":"failed to initiate device auth"}}`))
			},
			expectedLevel:        "error",
			expectedErrorCode:    "AUTH_INITIATE_FAILED",
			expectedErrorMessage: "failed to initiate device auth",
		},
		{
			name: "400 response logs error details at warn level",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"code":"INVALID_BODY","message":"missing required field"}}`))
			},
			expectedLevel:        "warn",
			expectedErrorCode:    "INVALID_BODY",
			expectedErrorMessage: "missing required field",
		},
		{
			name: "401 response logs error details at warn level",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":{"code":"UNAUTHORIZED","message":"invalid session"}}`))
			},
			expectedLevel:        "warn",
			expectedErrorCode:    "UNAUTHORIZED",
			expectedErrorMessage: "invalid session",
		},
		{
			name: "404 response logs at warn level without error details",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte("not found"))
			},
			expectedLevel: "warn",
		},
		{
			name: "200 response stays info level",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			},
			expectedLevel: "info",
		},
		{
			name: "500 non-json response still logs as error",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/plain")
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte("internal server error"))
			},
			expectedLevel: "error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var logBuffer bytes.Buffer
			logger := zerolog.New(&logBuffer)
			handler := Logging(logger, nil)(tt.handler)

			req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/codex-auth/initiate", nil)
			w := httptest.NewRecorder()

			handler.ServeHTTP(w, req)

			var logEvent map[string]any
			require.NoError(t, json.Unmarshal(logBuffer.Bytes(), &logEvent), "logging middleware should emit valid JSON log event")
			require.Equal(t, tt.expectedLevel, logEvent["level"], "logging middleware should use expected log level")

			if tt.expectedErrorCode != "" {
				require.Equal(t, tt.expectedErrorCode, logEvent["error_code"], "logging middleware should include API error code for error responses")
			}
			if tt.expectedErrorMessage != "" {
				require.Equal(t, tt.expectedErrorMessage, logEvent["error_message"], "logging middleware should include API error message for error responses")
			}
		})
	}
}

func TestLogging_EmitsNumericDurationMilliseconds(t *testing.T) {
	t.Parallel()

	var logBuffer bytes.Buffer
	logger := zerolog.New(&logBuffer)
	handler := Logging(logger, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	var logEvent map[string]any
	require.NoError(t, json.Unmarshal(logBuffer.Bytes(), &logEvent), "logging middleware should emit valid JSON log event")
	require.Contains(t, logEvent, "duration", "logging middleware should keep the existing zerolog duration field")
	require.Equal(t, "2xx", logEvent["status_class"], "logging middleware should emit status_class for request-rate dashboards")
	durationMS, ok := logEvent["duration_ms"].(float64)
	require.True(t, ok, "logging middleware should emit numeric duration_ms for LogsQL percentile queries")
	require.GreaterOrEqual(t, durationMS, float64(0), "duration_ms should be non-negative")
}

func TestLogging_CapturesOnlyServerErrorsForObservability(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		handler           http.HandlerFunc
		expectedCaptures  int
		expectedErrorCode string
		expectedStatus    int
	}{
		{
			name: "captures 500 API error responses",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":{"code":"BROKEN","message":"something failed"}}`))
			},
			expectedCaptures:  1,
			expectedErrorCode: "BROKEN",
			expectedStatus:    http.StatusInternalServerError,
		},
		{
			name: "does not capture 400 responses",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"code":"INVALID","message":"bad input"}}`))
			},
			expectedCaptures: 0,
			expectedStatus:   http.StatusBadRequest,
		},
		{
			name: "does not capture success responses",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			},
			expectedCaptures: 0,
			expectedStatus:   http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reporter := &capturingReporter{}
			handler := Logging(zerolog.Nop(), reporter)(tt.handler)

			req := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil)
			w := httptest.NewRecorder()

			handler.ServeHTTP(w, req)

			require.Equal(t, tt.expectedStatus, w.Code, "logging middleware should preserve the response status")
			require.Len(t, reporter.requestErrors, tt.expectedCaptures, "logging middleware should capture only expected server errors")
			if tt.expectedCaptures == 1 {
				require.Equal(t, tt.expectedErrorCode, reporter.requestErrors[0].ErrorCode, "captured event should include the API error code")
				require.Equal(t, tt.expectedStatus, reporter.requestErrors[0].Status, "captured event should include the response status")
				require.Equal(t, "/api/v1/test", reporter.requestErrors[0].Path, "captured event should include the request path")
			}
		})
	}
}

func TestLogging_CapturesIdentityFromDownstreamAuthMiddleware(t *testing.T) {
	t.Parallel()

	reporter := &capturingReporter{}
	userID := uuid.MustParse("00000000-0000-0000-0000-000000000123")
	orgID := uuid.MustParse("00000000-0000-0000-0000-000000000456")

	authLikeMiddleware := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := WithOrgID(r.Context(), orgID)
			ctx = WithUser(ctx, &models.User{
				ID:        userID,
				OrgID:     orgID,
				Email:     "test@example.com",
				Name:      "Test User",
				Role:      "member",
				CreatedAt: time.Now(),
			})
			if recorder, ok := w.(interface{ SetResolvedIdentity(uuid.UUID, uuid.UUID) }); ok {
				recorder.SetResolvedIdentity(orgID, userID)
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}

	handler := Logging(zerolog.Nop(), reporter)(authLikeMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"code":"BROKEN","message":"something failed"}}`))
	})))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	require.Len(t, reporter.requestErrors, 1, "logging middleware should capture one server error event")
	require.Equal(t, orgID.String(), reporter.requestErrors[0].OrgID, "captured event should include org identity resolved by downstream auth middleware")
	require.Equal(t, userID.String(), reporter.requestErrors[0].UserID, "captured event should include user identity resolved by downstream auth middleware")
}

func TestBuildRequestErrorEvent_PrefersRequestContextIdentity(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil)
	req = req.WithContext(WithOrgID(req.Context(), testOrgA))
	req = req.WithContext(WithUser(req.Context(), &models.User{ID: testUserID}))

	event := buildRequestErrorEvent(req, &responseWriter{
		status: http.StatusInternalServerError,
		orgID:  testOrgB,
		userID: uuid.MustParse("00000000-0000-0000-0000-000000000999"),
	})

	require.Equal(t, testOrgA.String(), event.OrgID, "request context org should win over recorder fallback")
	require.Equal(t, testUserID.String(), event.UserID, "request context user should win over recorder fallback")
}

func TestRoutePattern_FallsBackToPathWhenUnavailable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		setupReq func(*http.Request) *http.Request
		expected string
	}{
		{
			name:     "returns path when no route context is present",
			setupReq: func(r *http.Request) *http.Request { return r },
			expected: "/api/v1/test/123",
		},
		{
			name: "returns path when route pattern is empty",
			setupReq: func(r *http.Request) *http.Request {
				routeCtx := chi.NewRouteContext()
				ctx := context.WithValue(r.Context(), chi.RouteCtxKey, routeCtx)
				return r.WithContext(ctx)
			},
			expected: "/api/v1/test/123",
		},
		{
			name: "returns route pattern when available",
			setupReq: func(r *http.Request) *http.Request {
				routeCtx := chi.NewRouteContext()
				routeCtx.RoutePatterns = []string{"/api/v1/test/{id}"}
				ctx := context.WithValue(r.Context(), chi.RouteCtxKey, routeCtx)
				return r.WithContext(ctx)
			},
			expected: "/api/v1/test/{id}",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodGet, "/api/v1/test/123", nil)
			req = tt.setupReq(req)

			require.Equal(t, tt.expected, routePattern(req), "routePattern should return the expected route identifier")
		})
	}
}

// TestResponseWriter_Unwrap is the regression guard for the production EOF
// that surfaced as 502 PREVIEW_WORKER_REQUEST_FAILED on long preview starts:
// when this wrapper does not implement Unwrap(), http.NewResponseController
// cannot reach the underlying conn, so handlers calling SetWriteDeadline
// silently no-op and the server's 15s WriteTimeout drops the connection
// mid-handler. Logging is the outermost server-mounted middleware, so the
// preview-start handler always sees this wrapper at the top of the stack.
func TestResponseWriter_Unwrap(t *testing.T) {
	t.Parallel()
	inner := httptest.NewRecorder()
	rw := &responseWriter{ResponseWriter: inner, status: http.StatusOK}
	unwrapper, ok := any(rw).(interface{ Unwrap() http.ResponseWriter })
	require.True(t, ok, "responseWriter must implement Unwrap so http.NewResponseController can reach the underlying conn")
	require.Equal(t, http.ResponseWriter(inner), unwrapper.Unwrap(), "Unwrap must return the wrapped ResponseWriter")
}

// TestMiddlewareChain_SetWriteDeadlineEscapesServerWriteTimeout is the
// end-to-end regression guard for the prod incident: a real http.Server
// with a tight WriteTimeout, the actual middleware chain (Logging wrapping
// Metrics) in front of a slow handler that calls SetWriteDeadline. Without
// Unwrap on both wrappers, http.NewResponseController would fail silently,
// the server's WriteTimeout would still fire while the handler slept, and
// the client would see a torn connection. With Unwrap in place, the
// SetWriteDeadline reaches the underlying conn, the timeout is suppressed,
// and the full response makes it back.
func TestMiddlewareChain_SetWriteDeadlineEscapesServerWriteTimeout(t *testing.T) {
	t.Parallel()

	const (
		writeTimeout = 100 * time.Millisecond
		handlerSleep = 400 * time.Millisecond
	)

	slowHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Mirror handlers/helpers.go's clearWriteDeadline. If Unwrap is
		// missing on either Logging or Metrics, http.NewResponseController
		// returns ErrNotSupported and the handler keeps running with the
		// server's WriteTimeout still armed.
		require.NoError(t,
			http.NewResponseController(w).SetWriteDeadline(time.Time{}),
			"SetWriteDeadline must reach underlying conn through middleware chain",
		)
		time.Sleep(handlerSleep)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	chain := Logging(zerolog.Nop(), nil)(Metrics(slowHandler))
	srv := httptest.NewUnstartedServer(chain)
	srv.Config.WriteTimeout = writeTimeout
	srv.Start()
	defer srv.Close()

	// Generous client timeout so the test fails on the server-side timeout
	// (the bug we're guarding against) and not on a client-side cutoff.
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(srv.URL)
	require.NoError(t, err, "client request should complete despite handler outliving WriteTimeout")
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, `{"ok":true}`, string(body))
}

type capturingReporter struct {
	requestErrors []observability.RequestErrorEvent
	panicEvents   []panicEvent
}

type panicEvent struct {
	recovered any
	stack     []byte
}

func (r *capturingReporter) CaptureRequestError(_ *http.Request, event observability.RequestErrorEvent) {
	r.requestErrors = append(r.requestErrors, event)
}

func (r *capturingReporter) CaptureRecoveredPanic(_ *http.Request, recovered any, stack []byte) {
	r.panicEvents = append(r.panicEvents, panicEvent{recovered: recovered, stack: stack})
}

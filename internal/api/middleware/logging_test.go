package middleware

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/observability"
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

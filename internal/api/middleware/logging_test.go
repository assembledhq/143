package middleware

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

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
			handler := Logging(logger)(tt.handler)

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
			handler := Logging(logger)(tt.handler)

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

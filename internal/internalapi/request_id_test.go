package internalapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeParentRequestID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "valid request id", value: " host.example/random-000123 ", want: "host.example/random-000123"},
		{name: "empty request id", value: " "},
		{name: "oversized request id", value: strings.Repeat("a", 129)},
		{name: "header injection", value: "request-id\nspoofed"},
		{name: "spaces inside id", value: "request id"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, NormalizeParentRequestID(tt.value), "parent request id should be safe for headers and structured logs")
		})
	}
}

func TestTrustedParentRequestID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		path   string
		header string
		want   string
	}{
		{name: "internal worker route is trusted", path: "/internal/preview/preview-id/observe", header: "public-request-123", want: "public-request-123"},
		{name: "internal session route is trusted", path: "/internal/sessions/session-id/cancel", header: "public-request-123", want: "public-request-123"},
		{name: "public api route is not trusted", path: "/api/v1/previews/preview-id/observe", header: "forged-request-123"},
		{name: "internal prefix must be a path segment", path: "/api/v1/internal/previews", header: "forged-request-123"},
		{name: "internal route still validates the value", path: "/internal/preview/preview-id/observe", header: "request-id\nspoofed"},
		{name: "missing header", path: "/internal/preview/preview-id/observe"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodPost, tt.path, nil)
			if tt.header != "" {
				req.Header.Set(ParentRequestIDHeader, tt.header)
			}
			require.Equal(t, tt.want, TrustedParentRequestID(req), "only the internal service-to-service surface should be able to set a correlation id")
		})
	}
}

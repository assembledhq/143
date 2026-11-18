package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPriorReviewHistoryTools(t *testing.T) {
	t.Parallel()
	const sessionID = "11111111-1111-4111-8111-111111111111"
	const threadID = "22222222-2222-4222-8222-222222222222"
	tests := []struct {
		name, tool, args, path, query string
	}{
		{"list", "code_review_history_prior_rounds", `{"limit":5,"cursor":"` + sessionID + `"}`, "/api/v1/internal/code-review-history/prior-rounds", "cursor=" + sessionID + "&limit=5"},
		{"report", "code_review_history_prior_round", `{"session_id":"` + sessionID + `"}`, "/api/v1/internal/code-review-history/prior-rounds/" + sessionID, ""},
		{"messages", "code_review_history_prior_messages", `{"session_id":"` + sessionID + `","thread_id":"` + threadID + `","limit":3,"cursor":"next"}`, "/api/v1/internal/code-review-history/prior-rounds/" + sessionID + "/threads/" + threadID + "/messages", "cursor=next&limit=3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			requests := make(chan *http.Request, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests <- r
				w.Header().Set("Content-Type", "application/json")
				if _, err := w.Write([]byte(`{"data":[]}`)); err != nil {
					t.Errorf("write fixture response: %v", err)
				}
			}))
			t.Cleanup(server.Close)
			transport := http.DefaultTransport.(*http.Transport).Clone()
			t.Cleanup(transport.CloseIdleConnections)
			source := NewInternalMetaToolSource(staticToolSource{}, "fixture-token", server.URL).(*internalMetaToolSource)
			source.client = &http.Client{Transport: transport}
			result := source.CallTool(context.Background(), tt.tool, json.RawMessage(tt.args))
			require.False(t, result.IsError, "history reads should reach the internal API")
			request := <-requests
			require.Equal(t, http.MethodGet, request.Method, "history tools must remain read-only")
			require.Equal(t, tt.path, request.URL.Path, "history tool should address its scoped endpoint")
			require.Equal(t, tt.query, request.URL.RawQuery, "pagination should survive CLI dispatch")
			require.Equal(t, "Bearer fixture-token", request.Header.Get("Authorization"), "internal authentication should be forwarded")
		})
	}
}

func TestPriorReviewHistoryToolArgumentValidation(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, tool, args string }{
		{"bad JSON", "code_review_history_prior_rounds", `{`},
		{"negative limit", "code_review_history_prior_rounds", `{"limit":-1}`},
		{"missing session", "code_review_history_prior_round", `{}`},
		{"invalid session", "code_review_history_prior_round", `{"session_id":"../other"}`},
		{"missing thread", "code_review_history_prior_messages", `{"session_id":"11111111-1111-4111-8111-111111111111"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			source := NewInternalMetaToolSource(staticToolSource{}, "token", "https://invalid.test")
			result := source.CallTool(context.Background(), tt.tool, json.RawMessage(tt.args))
			require.True(t, result.IsError, "invalid references should fail before making an HTTP request")
			require.True(t, strings.HasPrefix(result.Content[0].Text, "INVALID_ARGUMENTS:"), "invalid arguments should have a useful error code")
		})
	}
}

func TestPriorReviewHistoryIntrinsicToolsRespectAllowlist(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		allowlist []string
		expected  []string
	}{
		{"intrinsic reads", nil, []string{"code_review_history_prior_rounds", "code_review_history_prior_round", "code_review_history_prior_messages"}},
		{"empty allowlist", []string{}, []string{}},
		{"narrow allowlist", []string{"code-review-history:prior-round"}, []string{"code_review_history_prior_round"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			base := staticToolSource{tools: append(priorReviewHistoryTools(), Tool{Name: "code_review_history_get"}, Tool{Name: "code_review_history_update_policy"})}
			source := NewCapabilityFilteredToolSource(base, ToolCapabilityPolicy{ToolAllowlist: tt.allowlist})
			names := make([]string, 0)
			for _, tool := range source.ListTools() {
				names = append(names, tool.Name)
			}
			require.Equal(t, tt.expected, names, "intrinsic tools must not grant generic history or bypass a positive allowlist")
		})
	}
}

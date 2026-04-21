package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGeminiProvider_Complete_Success(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1beta/models/gemini-2.5-flash:generateContent", r.URL.Path, "should call generateContent on the requested model")
		require.Equal(t, "test-key", r.Header.Get("x-goog-api-key"), "should send API key header")

		var req geminiRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req), "should decode request body")
		require.NotNil(t, req.SystemInstruction, "system instruction should be sent when provided")
		require.Len(t, req.SystemInstruction.Parts, 1, "system instruction should have one text part")
		require.Equal(t, "system prompt", req.SystemInstruction.Parts[0].Text)
		require.Len(t, req.Contents, 1, "should send one user turn")
		require.Equal(t, "user", req.Contents[0].Role, "content role should be user")
		require.Equal(t, "user prompt", req.Contents[0].Parts[0].Text)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(geminiResponse{
			Candidates: []geminiCandidate{{
				Content: geminiContent{Parts: []geminiPart{{Text: "hello from gemini"}}},
			}},
		})
	}))
	defer server.Close()

	p := NewGeminiProvider("test-key", WithGeminiBaseURL(server.URL), WithGeminiHTTPClient(server.Client()))
	require.Equal(t, "gemini", p.Name(), "provider name should be gemini")

	resp, err := p.Complete(context.Background(), "gemini-2.5-flash", "system prompt", "user prompt", "")
	require.NoError(t, err, "should complete without error")
	require.Equal(t, "hello from gemini", resp, "should return text content from first candidate")
}

func TestGeminiProvider_Complete_OmitsSystemInstructionWhenEmpty(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req geminiRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req), "should decode request body")
		require.Nil(t, req.SystemInstruction, "system instruction should be omitted when empty")

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(geminiResponse{
			Candidates: []geminiCandidate{{
				Content: geminiContent{Parts: []geminiPart{{Text: "ok"}}},
			}},
		})
	}))
	defer server.Close()

	p := NewGeminiProvider("key", WithGeminiBaseURL(server.URL), WithGeminiHTTPClient(server.Client()))
	_, err := p.Complete(context.Background(), "gemini-2.5-flash", "", "user prompt", "")
	require.NoError(t, err)
}

func TestGeminiProvider_Complete_ThinkingBudgetForSupportedModel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		effort         ReasoningEffort
		expectedBudget int
	}{
		{effort: "low", expectedBudget: thinkingBudgetLow},
		{effort: "medium", expectedBudget: thinkingBudgetMedium},
		{effort: "high", expectedBudget: thinkingBudgetHigh},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(string(tt.effort), func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req geminiRequest
				require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
				require.NotNil(t, req.GenerationConfig, "generationConfig should be present for thinking-capable model")
				require.NotNil(t, req.GenerationConfig.ThinkingConfig, "thinkingConfig should be set for %s effort", tt.effort)
				require.Equal(t, tt.expectedBudget, req.GenerationConfig.ThinkingConfig.ThinkingBudget)

				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(geminiResponse{
					Candidates: []geminiCandidate{{Content: geminiContent{Parts: []geminiPart{{Text: "ok"}}}}},
				})
			}))
			defer server.Close()

			p := NewGeminiProvider("key", WithGeminiBaseURL(server.URL), WithGeminiHTTPClient(server.Client()))
			_, err := p.Complete(context.Background(), "gemini-2.5-pro", "sys", "user", tt.effort)
			require.NoError(t, err)
		})
	}
}

func TestGeminiProvider_Complete_NoThinkingBudgetForUnsupportedModel(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req geminiRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		if req.GenerationConfig != nil {
			require.Nil(t, req.GenerationConfig.ThinkingConfig, "thinkingConfig must not be sent for non-thinking model")
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(geminiResponse{
			Candidates: []geminiCandidate{{Content: geminiContent{Parts: []geminiPart{{Text: "ok"}}}}},
		})
	}))
	defer server.Close()

	p := NewGeminiProvider("key", WithGeminiBaseURL(server.URL), WithGeminiHTTPClient(server.Client()))
	_, err := p.Complete(context.Background(), "gemini-2.0-flash", "sys", "user", "high")
	require.NoError(t, err, "sending reasoning effort to a non-thinking model should not error; it should be dropped")
}

func TestGeminiProvider_Complete_NoReasoningEffortSendsNoThinkingConfig(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req geminiRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		if req.GenerationConfig != nil {
			require.Nil(t, req.GenerationConfig.ThinkingConfig, "no thinkingConfig should be sent when reasoning effort is empty")
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(geminiResponse{
			Candidates: []geminiCandidate{{Content: geminiContent{Parts: []geminiPart{{Text: "ok"}}}}},
		})
	}))
	defer server.Close()

	p := NewGeminiProvider("key", WithGeminiBaseURL(server.URL), WithGeminiHTTPClient(server.Client()))
	_, err := p.Complete(context.Background(), "gemini-2.5-pro", "sys", "user", "")
	require.NoError(t, err)
}

func TestGeminiProvider_Complete_RateLimit(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("quota exceeded"))
	}))
	defer server.Close()

	p := NewGeminiProvider("key", WithGeminiBaseURL(server.URL), WithGeminiHTTPClient(server.Client()))
	_, err := p.Complete(context.Background(), "gemini-2.5-flash", "sys", "user", "")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrRateLimit, "429 should map to ErrRateLimit")
}

func TestGeminiProvider_Complete_ServerError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("internal error"))
	}))
	defer server.Close()

	p := NewGeminiProvider("key", WithGeminiBaseURL(server.URL), WithGeminiHTTPClient(server.Client()))
	_, err := p.Complete(context.Background(), "gemini-2.5-flash", "sys", "user", "")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrServerError, "500 should map to ErrServerError")
}

func TestGeminiProvider_Complete_AuthError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("invalid key"))
	}))
	defer server.Close()

	p := NewGeminiProvider("bad-key", WithGeminiBaseURL(server.URL), WithGeminiHTTPClient(server.Client()))
	_, err := p.Complete(context.Background(), "gemini-2.5-flash", "sys", "user", "")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrAuthError, "401 should map to ErrAuthError")
}

func TestGeminiProvider_Complete_BadRequest(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("invalid argument"))
	}))
	defer server.Close()

	p := NewGeminiProvider("key", WithGeminiBaseURL(server.URL), WithGeminiHTTPClient(server.Client()))
	_, err := p.Complete(context.Background(), "gemini-2.5-flash", "sys", "user", "")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrBadRequest, "400 should map to ErrBadRequest")
}

func TestGeminiProvider_Complete_NoCandidates(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(geminiResponse{Candidates: nil})
	}))
	defer server.Close()

	p := NewGeminiProvider("key", WithGeminiBaseURL(server.URL), WithGeminiHTTPClient(server.Client()))
	_, err := p.Complete(context.Background(), "gemini-2.5-flash", "sys", "user", "")
	require.Error(t, err, "empty candidates should be an error")
	require.Contains(t, err.Error(), "no candidates")
}

func TestModelSupportsThinking(t *testing.T) {
	t.Parallel()
	tests := []struct {
		model string
		want  bool
	}{
		{"gemini-2.5-pro", true},
		{"gemini-2.5-flash", true},
		{"gemini-3-pro-preview", true},
		{"gemini-3-flash-preview", true},
		{"gemini-2.0-flash", false},
		{"gemini-1.5-pro", false},
	}
	for _, tt := range tests {
		require.Equalf(t, tt.want, modelSupportsThinking(tt.model), "modelSupportsThinking(%q)", tt.model)
	}
}

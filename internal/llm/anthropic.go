package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/rs/zerolog/log"
)

// AnthropicProvider implements Provider using the Anthropic Messages API.
//
// API shape:
//
//	POST /v1/messages
//	Headers: x-api-key, anthropic-version
//	Body: { model, system, messages: [{role:"user", content:"..."}], max_tokens }
//	Response: { content: [{type:"text", text:"..."}] }
type AnthropicProvider struct {
	apiKey  string
	baseURL string
	client  *http.Client
}

type AnthropicOption func(*AnthropicProvider)

func WithAnthropicBaseURL(url string) AnthropicOption {
	return func(p *AnthropicProvider) { p.baseURL = url }
}

func WithAnthropicHTTPClient(c *http.Client) AnthropicOption {
	return func(p *AnthropicProvider) { p.client = c }
}

func NewAnthropicProvider(apiKey string, opts ...AnthropicOption) *AnthropicProvider {
	p := &AnthropicProvider{
		apiKey:  apiKey,
		baseURL: "https://api.anthropic.com",
		client:  &http.Client{Timeout: 60 * time.Second},
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

func (p *AnthropicProvider) Name() string { return "anthropic" }

func (p *AnthropicProvider) Complete(ctx context.Context, model, systemPrompt, userPrompt string, reasoningEffort ReasoningEffort) (string, error) {
	if reasoningEffort != "" {
		// Anthropic does not support reasoning_effort; log so callers know it was ignored.
		log.Ctx(ctx).Warn().
			Str("provider", "anthropic").
			Str("model", model).
			Str("reasoning_effort", string(reasoningEffort)).
			Msg("reasoning_effort is not supported by Anthropic — ignoring")
	}
	reqBody := anthropicRequest{
		Model:     model,
		System:    systemPrompt,
		MaxTokens: 4096,
		Messages: []anthropicMessage{
			{Role: "user", Content: userPrompt},
		},
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/messages", bytes.NewReader(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", p.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := p.client.Do(req) // #nosec G704 -- URL is from provider config
	if err != nil {
		// Connection errors are retryable.
		return "", fmt.Errorf("%w: %s", ErrServerError, err.Error())
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", classifyHTTPError(resp.StatusCode, truncate(string(respBody), 500))
	}

	var result anthropicResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("unmarshal response: %w", err)
	}

	for _, block := range result.Content {
		if block.Type == "text" {
			return block.Text, nil
		}
	}
	return "", fmt.Errorf("no text content in response")
}

type anthropicRequest struct {
	Model     string             `json:"model"`
	System    string             `json:"system,omitempty"`
	Messages  []anthropicMessage `json:"messages"`
	MaxTokens int                `json:"max_tokens"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicResponse struct {
	Content []anthropicContentBlock `json:"content"`
}

type anthropicContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

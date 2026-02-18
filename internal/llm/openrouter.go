package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// OpenRouterProvider implements Provider using the OpenRouter API.
// OpenRouter proxies requests to many LLM providers through a single API key.
// It uses the OpenAI Chat Completions format with additional headers.
//
// API shape (OpenAI-compatible):
//
//	POST /api/v1/chat/completions
//	Headers: Authorization: Bearer $KEY, HTTP-Referer, X-Title
//	Body: { model, messages: [{role:"system",content:"..."},{role:"user",content:"..."}] }
//	Response: { choices: [{message: {content: "..."}}] }
//
// Model IDs use vendor prefix format: "anthropic/claude-sonnet-4-5",
// "openai/gpt-4o", "google/gemini-2.5-pro", etc.
type OpenRouterProvider struct {
	apiKey  string
	baseURL string
	appName string // sent as X-Title header
	siteURL string // sent as HTTP-Referer header
	client  *http.Client
}

type OpenRouterOption func(*OpenRouterProvider)

func WithOpenRouterBaseURL(url string) OpenRouterOption {
	return func(p *OpenRouterProvider) { p.baseURL = url }
}

func WithOpenRouterHTTPClient(c *http.Client) OpenRouterOption {
	return func(p *OpenRouterProvider) { p.client = c }
}

func WithOpenRouterAppName(name string) OpenRouterOption {
	return func(p *OpenRouterProvider) { p.appName = name }
}

func WithOpenRouterSiteURL(url string) OpenRouterOption {
	return func(p *OpenRouterProvider) { p.siteURL = url }
}

func NewOpenRouterProvider(apiKey string, opts ...OpenRouterOption) *OpenRouterProvider {
	p := &OpenRouterProvider{
		apiKey:  apiKey,
		baseURL: "https://openrouter.ai/api",
		client:  &http.Client{Timeout: 60 * time.Second},
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

func (p *OpenRouterProvider) Name() string { return "openrouter" }

func (p *OpenRouterProvider) Complete(ctx context.Context, model, systemPrompt, userPrompt string) (string, error) {
	// OpenRouter uses the OpenAI Chat Completions format.
	messages := []chatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	}

	reqBody := chatCompletionsRequest{
		Model:    model,
		Messages: messages,
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/chat/completions", bytes.NewReader(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	if p.siteURL != "" {
		req.Header.Set("HTTP-Referer", p.siteURL)
	}
	if p.appName != "" {
		req.Header.Set("X-Title", p.appName)
	}

	resp, err := p.client.Do(req)
	if err != nil {
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

	// OpenRouter returns the standard OpenAI Chat Completions response format.
	var result chatCompletionsResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("unmarshal response: %w", err)
	}

	if len(result.Choices) == 0 {
		return "", fmt.Errorf("no choices in response")
	}
	return result.Choices[0].Message.Content, nil
}

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

// OpenAIChatProvider implements Provider using the OpenAI Chat Completions API.
//
// API shape:
//
//	POST /v1/chat/completions
//	Headers: Authorization: Bearer $KEY
//	Body: { model, messages: [{role:"system",content:"..."},{role:"user",content:"..."}], max_tokens }
//	Response: { choices: [{message: {content: "..."}}] }
type OpenAIChatProvider struct {
	apiKey  string
	baseURL string
	client  *http.Client
}

type OpenAIChatOption func(*OpenAIChatProvider)

func WithOpenAIChatBaseURL(url string) OpenAIChatOption {
	return func(p *OpenAIChatProvider) { p.baseURL = url }
}

func WithOpenAIChatHTTPClient(c *http.Client) OpenAIChatOption {
	return func(p *OpenAIChatProvider) { p.client = c }
}

func NewOpenAIChatProvider(apiKey string, opts ...OpenAIChatOption) *OpenAIChatProvider {
	p := &OpenAIChatProvider{
		apiKey:  apiKey,
		baseURL: "https://api.openai.com",
		client:  &http.Client{Timeout: 60 * time.Second},
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

func (p *OpenAIChatProvider) Name() string { return "openai_chat" }

func (p *OpenAIChatProvider) Complete(ctx context.Context, model, systemPrompt, userPrompt string, reasoningEffort ReasoningEffort) (string, error) {
	messages := []chatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	}

	// Only include reasoning_effort for models that support it. Sending it
	// to non-reasoning models (e.g. gpt-4o) may cause a 400 error.
	var effort ReasoningEffort
	if modelSupportsReasoningEffort(model) {
		effort = reasoningEffort
	}

	reqBody := chatCompletionsRequest{
		Model:           model,
		Messages:        messages,
		ReasoningEffort: effort,
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

	resp, err := p.client.Do(req) // #nosec G704 -- URL is from provider config
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

	var result chatCompletionsResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("unmarshal response: %w", err)
	}

	if len(result.Choices) == 0 {
		return "", fmt.Errorf("no choices in response")
	}
	return result.Choices[0].Message.Content, nil
}

// chatCompletionsRequest is the OpenAI Chat Completions request body.
// It is also used by OpenRouterProvider, which uses the same API format.
type chatCompletionsRequest struct {
	Model           string          `json:"model"`
	Messages        []chatMessage   `json:"messages"`
	ReasoningEffort ReasoningEffort `json:"reasoning_effort,omitempty"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatCompletionsResponse struct {
	Choices []chatChoice `json:"choices"`
}

type chatChoice struct {
	Message chatMessage `json:"message"`
}

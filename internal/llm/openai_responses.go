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

// OpenAIResponsesProvider implements Provider using the OpenAI Responses API.
//
// API shape:
//
//	POST /v1/responses
//	Headers: Authorization: Bearer $KEY
//	Body: { model, instructions: "system prompt", input: "user prompt" }
//	Response: { output: [{type:"message", content:[{type:"output_text", text:"..."}]}] }
type OpenAIResponsesProvider struct {
	apiKey  string
	baseURL string
	client  *http.Client
}

type OpenAIResponsesOption func(*OpenAIResponsesProvider)

func WithOpenAIResponsesBaseURL(url string) OpenAIResponsesOption {
	return func(p *OpenAIResponsesProvider) { p.baseURL = url }
}

func WithOpenAIResponsesHTTPClient(c *http.Client) OpenAIResponsesOption {
	return func(p *OpenAIResponsesProvider) { p.client = c }
}

func NewOpenAIResponsesProvider(apiKey string, opts ...OpenAIResponsesOption) *OpenAIResponsesProvider {
	p := &OpenAIResponsesProvider{
		apiKey:  apiKey,
		baseURL: "https://api.openai.com",
		client:  &http.Client{Timeout: 60 * time.Second},
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

func (p *OpenAIResponsesProvider) Name() string { return "openai_responses" }

func (p *OpenAIResponsesProvider) Complete(ctx context.Context, model, systemPrompt, userPrompt string) (string, error) {
	reqBody := responsesRequest{
		Model:        model,
		Instructions: systemPrompt,
		Input:        userPrompt,
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/responses", bytes.NewReader(bodyBytes))
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

	var result responsesResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("unmarshal response: %w", err)
	}

	// Walk the output structure to find text content.
	for _, output := range result.Output {
		if output.Type == "message" {
			for _, content := range output.Content {
				if content.Type == "output_text" {
					return content.Text, nil
				}
			}
		}
	}
	return "", fmt.Errorf("no text content in response")
}

type responsesRequest struct {
	Model        string `json:"model"`
	Instructions string `json:"instructions,omitempty"`
	Input        string `json:"input"`
}

type responsesResponse struct {
	Output []responsesOutput `json:"output"`
}

type responsesOutput struct {
	Type    string                   `json:"type"`
	Content []responsesOutputContent `json:"content"`
}

type responsesOutputContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

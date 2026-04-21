package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// GeminiProvider implements Provider using the Google Gemini REST API.
//
// API shape:
//
//	POST /v1beta/models/{model}:generateContent
//	Headers: x-goog-api-key: $KEY
//	Body: {
//	  "systemInstruction": {"parts": [{"text": "..."}]},
//	  "contents": [{"role": "user", "parts": [{"text": "..."}]}],
//	  "generationConfig": {"thinkingConfig": {"thinkingBudget": 1024}}
//	}
//	Response: {"candidates": [{"content": {"parts": [{"text": "..."}]}}]}
type GeminiProvider struct {
	apiKey  string
	baseURL string
	client  *http.Client
}

type GeminiOption func(*GeminiProvider)

func WithGeminiBaseURL(url string) GeminiOption {
	return func(p *GeminiProvider) { p.baseURL = url }
}

func WithGeminiHTTPClient(c *http.Client) GeminiOption {
	return func(p *GeminiProvider) { p.client = c }
}

func NewGeminiProvider(apiKey string, opts ...GeminiOption) *GeminiProvider {
	p := &GeminiProvider{
		apiKey:  apiKey,
		baseURL: "https://generativelanguage.googleapis.com",
		client:  &http.Client{Timeout: 60 * time.Second},
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

func (p *GeminiProvider) Name() string { return "gemini" }

// Thinking budgets map ReasoningEffort onto Gemini's thinkingBudget token counts.
// Values are best-effort; tune without breaking callers.
const (
	thinkingBudgetLow    = 1024
	thinkingBudgetMedium = 4096
	thinkingBudgetHigh   = 16384
)

// modelSupportsThinking returns true if the Gemini model accepts a
// generationConfig.thinkingConfig block. Applies to gemini-2.5-* and
// gemini-3-* generations; legacy 2.0/1.5 models reject it.
func modelSupportsThinking(model string) bool {
	return strings.HasPrefix(model, "gemini-2.5-") || strings.HasPrefix(model, "gemini-3-")
}

func (p *GeminiProvider) Complete(ctx context.Context, model, systemPrompt, userPrompt string, reasoningEffort ReasoningEffort) (string, error) {
	reqBody := geminiRequest{
		Contents: []geminiContent{
			{Role: "user", Parts: []geminiPart{{Text: userPrompt}}},
		},
	}
	if systemPrompt != "" {
		reqBody.SystemInstruction = &geminiSystemInstruction{
			Parts: []geminiPart{{Text: systemPrompt}},
		}
	}
	if reasoningEffort != "" && modelSupportsThinking(model) {
		budget, ok := thinkingBudgetFor(reasoningEffort)
		if ok {
			reqBody.GenerationConfig = &geminiGenerationConfig{
				ThinkingConfig: &geminiThinkingConfig{ThinkingBudget: budget},
			}
		}
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	url := p.baseURL + "/v1beta/models/" + model + ":generateContent"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", p.apiKey)

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

	var result geminiResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("unmarshal response: %w", err)
	}

	if len(result.Candidates) == 0 {
		return "", fmt.Errorf("no candidates in response")
	}
	for _, part := range result.Candidates[0].Content.Parts {
		if part.Text != "" {
			return part.Text, nil
		}
	}
	return "", fmt.Errorf("no text in first candidate")
}

func thinkingBudgetFor(effort ReasoningEffort) (int, bool) {
	switch strings.ToLower(string(effort)) {
	case "low":
		return thinkingBudgetLow, true
	case "medium":
		return thinkingBudgetMedium, true
	case "high":
		return thinkingBudgetHigh, true
	default:
		return 0, false
	}
}

type geminiRequest struct {
	Contents          []geminiContent          `json:"contents"`
	SystemInstruction *geminiSystemInstruction `json:"systemInstruction,omitempty"`
	GenerationConfig  *geminiGenerationConfig  `json:"generationConfig,omitempty"`
}

type geminiSystemInstruction struct {
	Parts []geminiPart `json:"parts"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text string `json:"text"`
}

type geminiGenerationConfig struct {
	ThinkingConfig *geminiThinkingConfig `json:"thinkingConfig,omitempty"`
}

type geminiThinkingConfig struct {
	ThinkingBudget int `json:"thinkingBudget"`
}

type geminiResponse struct {
	Candidates []geminiCandidate `json:"candidates"`
}

type geminiCandidate struct {
	Content geminiContent `json:"content"`
}

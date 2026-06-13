// Package adapters contains implementations of the agent.AgentAdapter interface
// for specific coding agent CLIs.
package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
)

// OpenCodeAdapter runs the OpenCode CLI inside a sandbox.
//
// OpenCode can route to multiple model providers. 143 stores the credential as
// ProviderOpenCode and only then maps it to provider-specific env vars, making
// OpenCode keys explicit instead of silently borrowing Codex/Claude/Gemini rows.
type OpenCodeAdapter struct {
	logger zerolog.Logger
}

// NewOpenCodeAdapter creates a new adapter for running OpenCode CLI.
func NewOpenCodeAdapter(logger zerolog.Logger) *OpenCodeAdapter {
	return &OpenCodeAdapter{logger: logger}
}

// Name returns the agent identifier.
func (a *OpenCodeAdapter) Name() models.AgentType {
	return models.AgentTypeOpenCode
}

// ResumeMode reports that OpenCode can continue an upstream session by ID.
func (a *OpenCodeAdapter) ResumeMode() agent.SessionResumeMode {
	return agent.ResumeBySessionID
}

// RuntimeProfile declares OpenCode's non-TTY streaming runtime requirements.
func (a *OpenCodeAdapter) RuntimeProfile() agent.AgentRuntimeProfile {
	return openCodeRuntimeProfile
}

// PreparePrompt constructs the prompts for OpenCode based on the issue context.
func (a *OpenCodeAdapter) PreparePrompt(ctx context.Context, input *agent.AgentInput) (*agent.AgentPrompt, error) {
	if input == nil {
		return nil, fmt.Errorf("agent input is required")
	}

	maxTokens := resolveTokenLimit(input.TokenMode, input.ContextLimits)

	return &agent.AgentPrompt{
		SystemPrompt: buildSystemPrompt(input),
		UserPrompt:   buildUserPrompt(input),
		MaxTokens:    maxTokens,
		Files:        extractFileHints(input),
		UsageHint: agent.TokenUsageHint{
			AgentType:   models.AgentTypeOpenCode,
			BillingMode: agent.TokenBillingModeUnknown,
		},
	}, nil
}

// Execute runs the OpenCode CLI inside the sandbox and streams output.
func (a *OpenCodeAdapter) Execute(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
	return runStreamingAgent(ctx, openCodeStreamingConfig, a.logger, sandbox, prompt, logCh)
}

var openCodeStreamingConfig = streamingAgentConfig{
	DisplayName: "OpenCode",
	CLIName:     "opencode",
	BuildCmd: func(escapedPromptPath string) string {
		return fmt.Sprintf(
			"opencode run --format json --dangerously-skip-permissions --agent build --model \"${OPENCODE_MODEL:-%s}\" --dir \"$PWD\" \"$(cat '%s')\"",
			models.OpenCodeModelGPT54Mini,
			escapedPromptPath,
		)
	},
	BuildResumeCmd: func(escapedPromptPath, escapedResumeSessionID string) string {
		return fmt.Sprintf(
			"opencode run --format json --dangerously-skip-permissions --agent build --model \"${OPENCODE_MODEL:-%s}\" --session '%s' --dir \"$PWD\" \"$(cat '%s')\"",
			models.OpenCodeModelGPT54Mini,
			escapedResumeSessionID,
			escapedPromptPath,
		)
	},
	ParseLine: parseOpenCodeStreamLine,
	Profile:   openCodeRuntimeProfile,
}

var openCodeRuntimeProfile = agent.AgentRuntimeProfile{
	Cancellation:      agent.DefaultCancellationSpec,
	PreferSplitOutput: true,
}

type openCodeStreamEvent struct {
	Type           string          `json:"type"`
	Role           string          `json:"role,omitempty"`
	Content        string          `json:"content,omitempty"`
	Message        string          `json:"message,omitempty"`
	Text           string          `json:"text,omitempty"`
	Name           string          `json:"name,omitempty"`
	Tool           string          `json:"tool,omitempty"`
	Input          json.RawMessage `json:"input,omitempty"`
	Output         string          `json:"output,omitempty"`
	Result         json.RawMessage `json:"result,omitempty"`
	Error          string          `json:"error,omitempty"`
	ID             string          `json:"id,omitempty"`
	SessionID      string          `json:"session_id,omitempty"`
	SessionIDCamel string          `json:"sessionID,omitempty"`
	CostUSD        *float64        `json:"cost_usd,omitempty"`
	TotalCostUSD   *float64        `json:"total_cost_usd,omitempty"`
	Usage          *struct {
		InputTokens         int `json:"input_tokens"`
		CachedInputTokens   int `json:"cache_read_input_tokens"`
		CacheCreationTokens int `json:"cache_creation_input_tokens"`
		OutputTokens        int `json:"output_tokens"`
	} `json:"usage,omitempty"`
}

func parseOpenCodeStreamLine(line []byte, result *agent.AgentResult, logCh chan<- agent.LogEntry, summaryParts *[]string, lastAssistant *string) {
	var event openCodeStreamEvent
	if err := json.Unmarshal(line, &event); err != nil {
		text := string(line)
		logCh <- agent.LogEntry{Timestamp: time.Now(), Level: "output", Message: text}
		*summaryParts = append(*summaryParts, text)
		return
	}

	switch event.Type {
	case "session", "session_start", "started":
		if sessionID := firstNonEmpty(event.SessionID, event.SessionIDCamel, event.ID); sessionID != "" {
			result.AgentSessionID = sessionID
		}
		logCh <- agent.LogEntry{Timestamp: time.Now(), Level: "debug", Message: string(line)}
	case "assistant", "message", "text":
		content := firstNonEmpty(event.Content, event.Message, event.Text)
		if content == "" || (event.Type == "message" && event.Role != "" && event.Role != "assistant") {
			logCh <- agent.LogEntry{Timestamp: time.Now(), Level: "debug", Message: string(line)}
			return
		}
		logCh <- agent.LogEntry{Timestamp: time.Now(), Level: "output", Message: content}
		*summaryParts = append(*summaryParts, content)
		*lastAssistant = content
	case "tool_call", "tool_use":
		toolName := firstNonEmpty(event.Name, event.Tool)
		metadata := map[string]interface{}{"tool": toolName}
		if len(event.Input) > 0 {
			var inputMap map[string]interface{}
			if err := json.Unmarshal(event.Input, &inputMap); err == nil {
				metadata["input"] = inputMap
			}
		}
		logCh <- agent.LogEntry{
			Timestamp: time.Now(),
			Level:     "tool_use",
			Message:   fmt.Sprintf("using tool: %s", toolName),
			Metadata:  metadata,
		}
	case "tool_result":
		toolName := firstNonEmpty(event.Name, event.Tool)
		output := event.Output
		if output == "" && len(event.Result) > 0 {
			output = string(event.Result)
		}
		metadata := map[string]interface{}{"type": "tool_result"}
		if toolName != "" {
			metadata["tool"] = toolName
		}
		logCh <- agent.LogEntry{Timestamp: time.Now(), Level: "output", Message: output, Metadata: metadata}
	case "usage", "result", "done":
		if event.Usage != nil {
			mergeTokenUsage(&result.TokenUsage, agent.TokenUsage{
				Reported:            true,
				InputTokens:         event.Usage.InputTokens,
				CachedInputTokens:   event.Usage.CachedInputTokens,
				CacheCreationTokens: event.Usage.CacheCreationTokens,
				OutputTokens:        event.Usage.OutputTokens,
			})
		}
		if event.TotalCostUSD != nil {
			setDirectUSDCost(&result.TokenUsage, *event.TotalCostUSD, "opencode_total_cost_usd")
		}
		if event.CostUSD != nil {
			setDirectUSDCost(&result.TokenUsage, *event.CostUSD, "opencode_cost_usd")
		}
		logCh <- agent.LogEntry{Timestamp: time.Now(), Level: "info", Message: firstNonEmpty(event.Content, event.Message)}
	case "error":
		result.Error = firstNonEmpty(event.Error, event.Message, event.Content, "unknown error")
		logCh <- agent.LogEntry{Timestamp: time.Now(), Level: "error", Message: result.Error}
	case "permission", "permission_request":
		result.Error = openCodePermissionError(event)
		logCh <- agent.LogEntry{Timestamp: time.Now(), Level: "error", Message: result.Error}
	default:
		logCh <- agent.LogEntry{Timestamp: time.Now(), Level: "debug", Message: string(line)}
	}
}

func openCodePermissionError(event openCodeStreamEvent) string {
	message := firstNonEmpty(event.Error, event.Message, event.Content)
	if message != "" {
		return "OpenCode requested interactive permission: " + message
	}
	if tool := firstNonEmpty(event.Tool, event.Name); tool != "" {
		return "OpenCode requested interactive permission for " + tool
	}
	return "OpenCode requested interactive permission"
}

// Package adapters contains implementations of the agent.AgentAdapter interface
// for specific coding agent CLIs.
package adapters

import (
	"context"
	"fmt"

	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
)

// PiAdapter runs the Pi CLI (npm: @earendil-works/pi-coding-agent) inside a sandbox.
//
// Pi can route to many providers (Anthropic, OpenAI, Google, Moonshot, etc.)
// using a single CLI. The active model is selected via the --model flag, with
// PI_MODEL_CUSTOM (free-form) winning over PI_MODEL (curated dropdown). Pi
// auth is passed through PI_API_KEY from the dedicated Pi credential store.
type PiAdapter struct {
	logger zerolog.Logger
}

// NewPiAdapter creates a new adapter for running Pi CLI.
func NewPiAdapter(logger zerolog.Logger) *PiAdapter {
	return &PiAdapter{
		logger: logger,
	}
}

// Name returns the agent identifier.
func (a *PiAdapter) Name() models.AgentType {
	return models.AgentTypePi
}

// RuntimeProfile declares Pi's interactive runtime requirements. Pi only
// honors Esc as a cancel signal under raw-mode TTY input, so the runtime
// must allocate a TTY and keep stdin open for byte-level delivery.
func (a *PiAdapter) RuntimeProfile() agent.AgentRuntimeProfile {
	return agent.AgentRuntimeProfile{
		Cancellation:      agent.CancellationSpec{Method: agent.CancellationMethodEscape},
		RequiresTTY:       true,
		RequiresOpenStdin: true,
	}
}

// ResumeMode reports that Pi has no headless resume mechanism. Continuation
// turns rely on the restored sandbox filesystem state.
func (a *PiAdapter) ResumeMode() agent.SessionResumeMode {
	return agent.ResumeUnsupported
}

// PreparePrompt constructs the prompts for Pi based on the issue context.
func (a *PiAdapter) PreparePrompt(ctx context.Context, input *agent.AgentInput) (*agent.AgentPrompt, error) {
	if input == nil {
		return nil, fmt.Errorf("agent input is required")
	}

	maxTokens := resolveTokenLimit(input.TokenMode, input.ContextLimits)

	systemPrompt := buildSystemPrompt(input)
	userPrompt := buildUserPrompt(input)
	files := extractFileHints(input)

	return &agent.AgentPrompt{
		SystemPrompt: systemPrompt,
		UserPrompt:   userPrompt,
		MaxTokens:    maxTokens,
		Files:        files,
		UsageHint: agent.TokenUsageHint{
			AgentType:   models.AgentTypePi,
			BillingMode: agent.TokenBillingModeUnknown,
		},
	}, nil
}

// Execute runs the Pi CLI inside the sandbox and streams output. See
// runStreamingAgent for the shared continuation handling (Pi has no headless
// resume flag, same as Amp).
func (a *PiAdapter) Execute(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
	return runStreamingAgent(ctx, piStreamingConfig, a.logger, sandbox, prompt, logCh)
}

// piStreamingConfig wires runStreamingAgent for Pi.
//
// The active model is resolved inside the shell so PI_MODEL_CUSTOM can
// override PI_MODEL, with a curated default when both are unset. Both env vars
// are populated by the orchestrator from AgentEnvConfig["pi"].
var piStreamingConfig = streamingAgentConfig{
	DisplayName: "Pi",
	CLIName:     "pi",
	BuildCmd: func(escapedPromptPath string) string {
		return fmt.Sprintf(
			"pi -p \"$(cat '%s')\" --mode json --api-key \"${PI_API_KEY}\" --model \"${PI_MODEL_CUSTOM:-${PI_MODEL:-%s}}\"",
			escapedPromptPath,
			models.PiModelClaudeOpus47,
		)
	},
	ParseConfig: streamParseConfig{
		MessageAsAssistant: true,
		DoneAsResult:       true,
		CaptureToolModel:   true,
	},
	Profile: agent.AgentRuntimeProfile{
		Cancellation:      agent.CancellationSpec{Method: agent.CancellationMethodEscape},
		RequiresTTY:       true,
		RequiresOpenStdin: true,
	},
}

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

// AmpAdapter runs the Sourcegraph Amp CLI inside a sandbox.
//
// Amp uses agent "modes" (smart, deep, large, rush) instead of model names —
// each mode bundles a model, system prompt, and tool set. The mode is passed
// explicitly via `-m` at invocation time, resolved in-shell from AMP_MODE
// with a "smart" fallback.
type AmpAdapter struct {
	logger zerolog.Logger
}

// NewAmpAdapter creates a new adapter for running Amp CLI.
func NewAmpAdapter(logger zerolog.Logger) *AmpAdapter {
	return &AmpAdapter{
		logger: logger,
	}
}

// Name returns the agent identifier.
func (a *AmpAdapter) Name() models.AgentType {
	return models.AgentTypeAmp
}

// ResumeMode reports that Amp has no headless resume mechanism. Continuation
// turns rely on the restored sandbox filesystem state. The session_id Amp
// emits in its stream is captured for observability only and is never fed
// back into the CLI.
func (a *AmpAdapter) ResumeMode() agent.SessionResumeMode {
	return agent.ResumeUnsupported
}

// PreparePrompt constructs the prompts for Amp based on the issue context.
func (a *AmpAdapter) PreparePrompt(ctx context.Context, input *agent.AgentInput) (*agent.AgentPrompt, error) {
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
			AgentType:   models.AgentTypeAmp,
			BillingMode: agent.TokenBillingModeUnknown,
		},
	}, nil
}

// Execute runs the Amp CLI inside the sandbox and streams output. The shared
// runStreamingAgent helper handles continuation semantics — see that function
// for details on how follow-up turns are replayed against the restored
// filesystem without a headless resume flag.
func (a *AmpAdapter) Execute(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
	return runStreamingAgent(ctx, ampStreamingConfig, a.logger, sandbox, prompt, logCh)
}

// ampStreamingConfig wires runStreamingAgent for Amp.
//
// `--dangerously-allow-all` skips Amp's interactive permission prompts; the
// container is already isolated so additional in-CLI gating is noise. `-m` is
// resolved in-shell from AMP_MODE (set via per-run model override or
// agent_config.amp.AMP_MODE) with a curated default, so the active mode is
// visible in the command line (greppable in logs) and doesn't depend on Amp
// reading AMP_MODE from env itself.
var ampStreamingConfig = streamingAgentConfig{
	DisplayName: "Amp",
	CLIName:     "amp",
	BuildCmd: func(escapedPromptPath string) string {
		return fmt.Sprintf(
			"amp -x \"$(cat '%s')\" --stream-json --dangerously-allow-all -m \"${AMP_MODE:-%s}\"",
			escapedPromptPath,
			models.AmpModeSmart,
		)
	},
	ParseConfig: streamParseConfig{
		// Capture session_id into result.AgentSessionID for tracing/observability
		// only: Amp is lacksHeadlessResume, so we never feed this back to the CLI
		// to resume a conversation. Persisting it lets operators correlate a 143
		// session row with the upstream Amp run when debugging.
		CaptureSessionID: true,
	},
	Profile: agent.AgentRuntimeProfile{
		Cancellation:      agent.DefaultCancellationSpec,
		PreferSplitOutput: true,
	},
}

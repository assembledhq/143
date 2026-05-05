package pm

import (
	"context"
	"fmt"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
)

// PMAdapter wraps an existing AgentAdapter and overrides the prompt
// to use the PM system prompt and PM context payload.
type PMAdapter struct {
	inner          agent.AgentAdapter
	availableSlots int
	maxConcurrent  int
	maxTokens      int
}

func NewPMAdapter(inner agent.AgentAdapter, availableSlots int, maxConcurrent int) *PMAdapter {
	return &PMAdapter{inner: inner, availableSlots: availableSlots, maxConcurrent: maxConcurrent, maxTokens: defaultPMMaxTokens}
}

// NewPMAdapterWithLimits creates a PM adapter with a custom token limit from org settings.
func NewPMAdapterWithLimits(inner agent.AgentAdapter, availableSlots int, maxConcurrent int, maxTokens int) *PMAdapter {
	if maxTokens <= 0 {
		maxTokens = defaultPMMaxTokens
	}
	return &PMAdapter{inner: inner, availableSlots: availableSlots, maxConcurrent: maxConcurrent, maxTokens: maxTokens}
}

func (a *PMAdapter) Name() models.AgentType { return models.AgentTypePMAgent }

func (a *PMAdapter) PreparePrompt(ctx context.Context, input *agent.AgentInput) (*agent.AgentPrompt, error) {
	if input == nil {
		return nil, fmt.Errorf("pm adapter requires input")
	}
	return &agent.AgentPrompt{
		SystemPrompt: buildPMSystemPrompt(a.availableSlots, a.maxConcurrent, 0),
		UserPrompt:   input.PMContextJSON,
		MaxTokens:    a.maxTokens,
	}, nil
}

func (a *PMAdapter) Execute(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
	return a.inner.Execute(ctx, sandbox, prompt, logCh)
}

// ResumeMode delegates to the inner adapter — PM runs are scheduled on top of
// whichever coding agent the org configured, and resume capability is a
// property of that underlying CLI.
func (a *PMAdapter) ResumeMode() agent.SessionResumeMode {
	return a.inner.ResumeMode()
}

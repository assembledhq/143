package pm

import (
	"context"
	"fmt"

	"github.com/assembledhq/143/internal/services/agent"
)

// PMAdapter wraps an existing AgentAdapter and overrides the prompt
// to use the PM system prompt and PM context payload.
type PMAdapter struct {
	inner          agent.AgentAdapter
	availableSlots int
	maxConcurrent  int
}

func NewPMAdapter(inner agent.AgentAdapter, availableSlots int, maxConcurrent int) *PMAdapter {
	return &PMAdapter{inner: inner, availableSlots: availableSlots, maxConcurrent: maxConcurrent}
}

func (a *PMAdapter) Name() string { return "pm_agent" }

func (a *PMAdapter) PreparePrompt(ctx context.Context, input *agent.AgentInput) (*agent.AgentPrompt, error) {
	if input == nil {
		return nil, fmt.Errorf("pm adapter requires input")
	}
	return &agent.AgentPrompt{
		SystemPrompt: buildPMSystemPrompt(a.availableSlots, a.maxConcurrent, 0),
		UserPrompt:   input.PMContextJSON,
		MaxTokens:    pmMaxTokens,
	}, nil
}

func (a *PMAdapter) Execute(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
	return a.inner.Execute(ctx, sandbox, prompt, logCh)
}

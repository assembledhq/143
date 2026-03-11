package pm

import "github.com/assembledhq/143/internal/prompts"

func buildPMSystemPrompt(availableSlots, maxConcurrent, activeProjectCount int) string {
	return prompts.PMSystemPrompt(prompts.PMSystemPromptData{
		AvailableSlots:     availableSlots,
		MaxConcurrent:      maxConcurrent,
		ActiveProjectCount: activeProjectCount,
	})
}

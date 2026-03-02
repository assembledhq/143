package pm

import (
	_ "embed"
	"fmt"
)

//go:embed pm_system_prompt.template
var pmSystemPromptTemplate string

func buildPMSystemPrompt(availableSlots, maxConcurrent int) string {
	return fmt.Sprintf(pmSystemPromptTemplate, availableSlots, maxConcurrent)
}

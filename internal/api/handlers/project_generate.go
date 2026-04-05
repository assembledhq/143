package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/assembledhq/143/internal/llm"
	"github.com/assembledhq/143/internal/prompts"
)

// ProjectGenerateHandler uses an LLM to convert a natural-language description
// into structured project fields.
type ProjectGenerateHandler struct {
	llmClient llm.Client
}

func NewProjectGenerateHandler(llmClient llm.Client) *ProjectGenerateHandler {
	return &ProjectGenerateHandler{llmClient: llmClient}
}

type GenerateProjectRequest struct {
	Description string `json:"description"`
}

type GenerateProjectResponse struct {
	Title              string `json:"title"`
	Goal               string `json:"goal"`
	Scope              string `json:"scope,omitempty"`
	CompletionCriteria string `json:"completion_criteria,omitempty"`
	ExecutionMode      string `json:"execution_mode"`
}

func (h *ProjectGenerateHandler) Generate(w http.ResponseWriter, r *http.Request) {
	if h.llmClient == nil {
		writeError(w, r, http.StatusServiceUnavailable, "LLM_NOT_CONFIGURED", "AI project generation is not available — no LLM provider configured")
		return
	}

	var req GenerateProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}

	desc := strings.TrimSpace(req.Description)
	if desc == "" {
		writeError(w, r, http.StatusBadRequest, "MISSING_FIELD", "description is required")
		return
	}

	response, err := h.llmClient.Complete(r.Context(), prompts.ProjectGeneratePrompt(), desc)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LLM_ERROR", "failed to generate project: "+err.Error(), err)
		return
	}

	// Strip markdown code fences if the LLM included them.
	cleaned := strings.TrimSpace(response)
	if strings.HasPrefix(cleaned, "```") {
		if idx := strings.Index(cleaned[3:], "\n"); idx >= 0 {
			cleaned = cleaned[3+idx+1:]
		}
		cleaned = strings.TrimSuffix(cleaned, "```")
		cleaned = strings.TrimSpace(cleaned)
	}

	var result GenerateProjectResponse
	if err := json.Unmarshal([]byte(cleaned), &result); err != nil {
		writeError(w, r, http.StatusInternalServerError, "PARSE_ERROR", "failed to parse AI response", err)
		return
	}

	// Default execution mode if not set.
	if result.ExecutionMode == "" {
		result.ExecutionMode = "sequential"
	}

	writeJSON(w, http.StatusOK, map[string]GenerateProjectResponse{"data": result})
}

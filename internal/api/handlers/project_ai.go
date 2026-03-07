package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type ProjectAIHandler struct {
	projectStore    *db.ProjectStore
	specStore       *db.ProjectSpecStore
	attachmentStore *db.ProjectAttachmentStore
	taskStore       *db.ProjectTaskStore
}

func NewProjectAIHandler(
	projectStore *db.ProjectStore,
	specStore *db.ProjectSpecStore,
	attachmentStore *db.ProjectAttachmentStore,
	taskStore *db.ProjectTaskStore,
) *ProjectAIHandler {
	return &ProjectAIHandler{
		projectStore:    projectStore,
		specStore:       specStore,
		attachmentStore: attachmentStore,
		taskStore:       taskStore,
	}
}

// AIImprovementRequest is the request body for AI improvement suggestions.
type AIImprovementRequest struct {
	Target  string  `json:"target"`            // "spec", "design", "tasks"
	SpecID  *string `json:"spec_id,omitempty"`  // for spec improvements
	Prompt  *string `json:"prompt,omitempty"`   // additional user instruction
}

// AIImprovementResponse returns suggestions from the AI analysis.
type AIImprovementResponse struct {
	Suggestions []AISuggestion `json:"suggestions"`
	Summary     string         `json:"summary"`
}

type AISuggestion struct {
	Type        string `json:"type"`        // "addition", "revision", "question", "task"
	Title       string `json:"title"`
	Description string `json:"description"`
	Priority    string `json:"priority"`    // "high", "medium", "low"
}

// Improve generates AI suggestions for a project's specs, designs, or tasks.
func (h *ProjectAIHandler) Improve(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	projectID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "invalid project ID")
		return
	}

	project, err := h.projectStore.GetByID(r.Context(), orgID, projectID)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "project not found")
		return
	}

	var req AIImprovementRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}

	if req.Target == "" {
		writeError(w, http.StatusBadRequest, "MISSING_FIELD", "target is required (spec, design, or tasks)")
		return
	}

	// Gather context for the AI
	specs, _ := h.specStore.ListByProject(r.Context(), orgID, projectID)
	attachments, _ := h.attachmentStore.ListByProject(r.Context(), orgID, projectID)
	tasks, _ := h.taskStore.ListByProject(r.Context(), orgID, projectID, db.ProjectTaskFilters{})

	// Build suggestions based on analysis of current project state.
	// This is a structured analysis that can be consumed by an AI agent or returned as-is.
	suggestions := analyzeProject(project, specs, attachments, tasks, req)

	writeJSON(w, http.StatusOK, models.SingleResponse[AIImprovementResponse]{
		Data: AIImprovementResponse{
			Suggestions: suggestions,
			Summary:     buildSummary(project, specs, attachments, tasks, req.Target),
		},
	})
}

func analyzeProject(
	project models.Project,
	specs []models.ProjectSpec,
	attachments []models.ProjectAttachment,
	tasks []models.ProjectTask,
	req AIImprovementRequest,
) []AISuggestion {
	var suggestions []AISuggestion

	switch req.Target {
	case "spec":
		suggestions = analyzeSpecs(project, specs)
	case "design":
		suggestions = analyzeDesigns(project, attachments, specs)
	case "tasks":
		suggestions = analyzeTasks(project, tasks, specs)
	default:
		// General analysis covering all areas
		suggestions = append(suggestions, analyzeSpecs(project, specs)...)
		suggestions = append(suggestions, analyzeDesigns(project, attachments, specs)...)
		suggestions = append(suggestions, analyzeTasks(project, tasks, specs)...)
	}

	return suggestions
}

func analyzeSpecs(project models.Project, specs []models.ProjectSpec) []AISuggestion {
	var suggestions []AISuggestion

	if len(specs) == 0 {
		suggestions = append(suggestions, AISuggestion{
			Type:        "addition",
			Title:       "Add a Product Requirements Document",
			Description: "This project has no specs yet. Create a PRD to define the user stories, acceptance criteria, and technical requirements for: " + project.Goal,
			Priority:    "high",
		})
		return suggestions
	}

	for _, spec := range specs {
		if len(spec.Content) < 100 {
			suggestions = append(suggestions, AISuggestion{
				Type:        "revision",
				Title:       "Expand spec: " + spec.Title,
				Description: "This spec is very short. Consider adding user stories, acceptance criteria, edge cases, and technical constraints.",
				Priority:    "high",
			})
		}

		if spec.SpecType == "prd" {
			suggestions = append(suggestions, AISuggestion{
				Type:        "question",
				Title:       "Consider adding a technical spec",
				Description: "You have a PRD but no technical spec. A technical design document would help the AI agent understand implementation details, architecture decisions, and API contracts.",
				Priority:    "medium",
			})
		}
	}

	if project.CompletionCriteria == nil || *project.CompletionCriteria == "" {
		suggestions = append(suggestions, AISuggestion{
			Type:        "revision",
			Title:       "Define completion criteria",
			Description: "The project has no completion criteria. Adding measurable completion criteria will help the AI agent know when the project is done.",
			Priority:    "medium",
		})
	}

	return suggestions
}

func analyzeDesigns(project models.Project, attachments []models.ProjectAttachment, specs []models.ProjectSpec) []AISuggestion {
	var suggestions []AISuggestion

	if len(attachments) == 0 {
		suggestions = append(suggestions, AISuggestion{
			Type:        "addition",
			Title:       "Add design references",
			Description: "No screenshots or mockups have been added. Upload screenshots, wireframes, or Figma exports to give the AI agent visual context for the implementation.",
			Priority:    "medium",
		})
	}

	screenshotCount := 0
	mockupCount := 0
	for _, a := range attachments {
		switch a.Category {
		case "screenshot":
			screenshotCount++
		case "mockup", "wireframe":
			mockupCount++
		}
	}

	if screenshotCount > 0 && mockupCount == 0 {
		suggestions = append(suggestions, AISuggestion{
			Type:        "addition",
			Title:       "Add mockups or wireframes",
			Description: "You have screenshots showing the current state, but no mockups showing the desired state. Adding mockups or wireframes will help clarify the target design.",
			Priority:    "medium",
		})
	}

	// Check if any attachments lack captions
	uncaptionedCount := 0
	for _, a := range attachments {
		if a.Caption == nil || *a.Caption == "" {
			uncaptionedCount++
		}
	}
	if uncaptionedCount > 0 {
		suggestions = append(suggestions, AISuggestion{
			Type:        "revision",
			Title:       "Add captions to attachments",
			Description: "Some attachments don't have captions. Adding descriptions helps the AI agent understand what each screenshot or mockup represents.",
			Priority:    "low",
		})
	}

	return suggestions
}

func analyzeTasks(project models.Project, tasks []models.ProjectTask, specs []models.ProjectSpec) []AISuggestion {
	var suggestions []AISuggestion

	if len(tasks) == 0 && len(specs) > 0 {
		suggestions = append(suggestions, AISuggestion{
			Type:        "task",
			Title:       "Break specs into tasks",
			Description: "You have specs but no tasks. Consider breaking the requirements into discrete, implementable tasks that the AI agent can work on.",
			Priority:    "high",
		})
	}

	failedCount := 0
	for _, t := range tasks {
		if t.Status == models.ProjectTaskStatusFailed {
			failedCount++
		}
	}

	if failedCount > 0 {
		suggestions = append(suggestions, AISuggestion{
			Type:        "revision",
			Title:       "Review failed tasks",
			Description: "Some tasks have failed. Review the failure notes, consider simplifying the approach, or break them into smaller tasks.",
			Priority:    "high",
		})
	}

	noDescriptionCount := 0
	for _, t := range tasks {
		if t.Description == nil || *t.Description == "" {
			noDescriptionCount++
		}
	}
	if noDescriptionCount > 2 {
		suggestions = append(suggestions, AISuggestion{
			Type:        "revision",
			Title:       "Add descriptions to tasks",
			Description: "Several tasks lack descriptions. Adding detailed descriptions with acceptance criteria will improve agent success rates.",
			Priority:    "medium",
		})
	}

	return suggestions
}

func buildSummary(
	project models.Project,
	specs []models.ProjectSpec,
	attachments []models.ProjectAttachment,
	tasks []models.ProjectTask,
	target string,
) string {
	completedTasks := 0
	failedTasks := 0
	for _, t := range tasks {
		if t.Status == models.ProjectTaskStatusCompleted {
			completedTasks++
		}
		if t.Status == models.ProjectTaskStatusFailed {
			failedTasks++
		}
	}

	return "Project: " + project.Title +
		" | Specs: " + itoa(len(specs)) +
		" | Designs: " + itoa(len(attachments)) +
		" | Tasks: " + itoa(len(tasks)) +
		" (" + itoa(completedTasks) + " done, " + itoa(failedTasks) + " failed)" +
		" | Focus: " + target
}

func itoa(n int) string {
	return fmt.Sprintf("%d", n)
}

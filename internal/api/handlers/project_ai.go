package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// ProjectAnalysisHandler provides rule-based project analysis.
// It examines specs, designs, and tasks to produce structured improvement
// suggestions using heuristics (not an LLM).
type ProjectAnalysisHandler struct {
	projectStore    *db.ProjectStore
	specStore       *db.ProjectSpecStore
	attachmentStore *db.ProjectAttachmentStore
	taskStore       *db.ProjectTaskStore
}

func NewProjectAnalysisHandler(
	projectStore *db.ProjectStore,
	specStore *db.ProjectSpecStore,
	attachmentStore *db.ProjectAttachmentStore,
	taskStore *db.ProjectTaskStore,
) *ProjectAnalysisHandler {
	return &ProjectAnalysisHandler{
		projectStore:    projectStore,
		specStore:       specStore,
		attachmentStore: attachmentStore,
		taskStore:       taskStore,
	}
}

// AnalysisRequest is the request body for project analysis suggestions.
type AnalysisRequest struct {
	Target string  `json:"target"`            // "spec", "design", "tasks", or "all"
	SpecID *string `json:"spec_id,omitempty"` // for spec-specific analysis
	Prompt *string `json:"prompt,omitempty"`  // additional user instruction
}

// AnalysisResponse returns suggestions from the rule-based analysis.
type AnalysisResponse struct {
	Suggestions []AnalysisSuggestion `json:"suggestions"`
	Summary     string               `json:"summary"`
}

type AnalysisSuggestion struct {
	Type        string `json:"type"` // "addition", "revision", "question", "task"
	Title       string `json:"title"`
	Description string `json:"description"`
	Priority    string `json:"priority"` // "high", "medium", "low"
}

// Improve generates rule-based suggestions for a project's specs, designs, or tasks.
func (h *ProjectAnalysisHandler) Improve(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	projectID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid project ID")
		return
	}

	project, err := h.projectStore.GetByID(r.Context(), orgID, projectID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "project not found")
		return
	}

	var req AnalysisRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}

	if req.Target == "" {
		writeError(w, r, http.StatusBadRequest, "MISSING_FIELD", "target is required (spec, design, or tasks)")
		return
	}

	specs, err := h.specStore.ListByProject(r.Context(), orgID, projectID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LIST_SPECS_FAILED", "failed to list project specs", err)
		return
	}
	attachments, err := h.attachmentStore.ListByProject(r.Context(), orgID, projectID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LIST_ATTACHMENTS_FAILED", "failed to list project attachments", err)
		return
	}
	tasks, err := h.taskStore.ListByProject(r.Context(), orgID, projectID, db.ProjectTaskFilters{})
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LIST_TASKS_FAILED", "failed to list project tasks", err)
		return
	}

	suggestions := analyzeProject(project, specs, attachments, tasks, req)

	writeJSON(w, http.StatusOK, models.SingleResponse[AnalysisResponse]{
		Data: AnalysisResponse{
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
	req AnalysisRequest,
) []AnalysisSuggestion {
	var suggestions []AnalysisSuggestion

	switch req.Target {
	case "spec":
		suggestions = analyzeSpecs(project, specs)
	case "design":
		suggestions = analyzeDesigns(project, attachments, specs)
	case "tasks":
		suggestions = analyzeTasks(project, tasks, specs)
	default:
		suggestions = append(suggestions, analyzeSpecs(project, specs)...)
		suggestions = append(suggestions, analyzeDesigns(project, attachments, specs)...)
		suggestions = append(suggestions, analyzeTasks(project, tasks, specs)...)
	}

	return suggestions
}

func analyzeSpecs(project models.Project, specs []models.ProjectSpec) []AnalysisSuggestion {
	var suggestions []AnalysisSuggestion

	if len(specs) == 0 {
		suggestions = append(suggestions, AnalysisSuggestion{
			Type:        "addition",
			Title:       "Add a Product Requirements Document",
			Description: "This project has no specs yet. Create a PRD to define the user stories, acceptance criteria, and technical requirements for: " + project.Goal,
			Priority:    "high",
		})
		return suggestions
	}

	hasTechnicalSpec := false
	for _, spec := range specs {
		if spec.SpecType == "technical" {
			hasTechnicalSpec = true
		}
	}

	for _, spec := range specs {
		if len(spec.Content) < 100 {
			suggestions = append(suggestions, AnalysisSuggestion{
				Type:        "revision",
				Title:       "Expand spec: " + spec.Title,
				Description: "This spec is very short. Consider adding user stories, acceptance criteria, edge cases, and technical constraints.",
				Priority:    "high",
			})
		}
	}

	if !hasTechnicalSpec {
		hasPRD := false
		for _, spec := range specs {
			if spec.SpecType == "prd" {
				hasPRD = true
				break
			}
		}
		if hasPRD {
			suggestions = append(suggestions, AnalysisSuggestion{
				Type:        "question",
				Title:       "Consider adding a technical spec",
				Description: "You have a PRD but no technical spec. A technical design document would help the AI agent understand implementation details, architecture decisions, and API contracts.",
				Priority:    "medium",
			})
		}
	}

	if project.CompletionCriteria == nil || *project.CompletionCriteria == "" {
		suggestions = append(suggestions, AnalysisSuggestion{
			Type:        "revision",
			Title:       "Define completion criteria",
			Description: "The project has no completion criteria. Adding measurable completion criteria will help the AI agent know when the project is done.",
			Priority:    "medium",
		})
	}

	return suggestions
}

func analyzeDesigns(project models.Project, attachments []models.ProjectAttachment, specs []models.ProjectSpec) []AnalysisSuggestion {
	var suggestions []AnalysisSuggestion

	if len(attachments) == 0 {
		suggestions = append(suggestions, AnalysisSuggestion{
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
		suggestions = append(suggestions, AnalysisSuggestion{
			Type:        "addition",
			Title:       "Add mockups or wireframes",
			Description: "You have screenshots showing the current state, but no mockups showing the desired state. Adding mockups or wireframes will help clarify the target design.",
			Priority:    "medium",
		})
	}

	uncaptionedCount := 0
	for _, a := range attachments {
		if a.Caption == nil || *a.Caption == "" {
			uncaptionedCount++
		}
	}
	if uncaptionedCount > 0 {
		suggestions = append(suggestions, AnalysisSuggestion{
			Type:        "revision",
			Title:       "Add captions to attachments",
			Description: "Some attachments don't have captions. Adding descriptions helps the AI agent understand what each screenshot or mockup represents.",
			Priority:    "low",
		})
	}

	return suggestions
}

func analyzeTasks(project models.Project, tasks []models.ProjectTask, specs []models.ProjectSpec) []AnalysisSuggestion {
	var suggestions []AnalysisSuggestion

	if len(tasks) == 0 && len(specs) > 0 {
		suggestions = append(suggestions, AnalysisSuggestion{
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
		suggestions = append(suggestions, AnalysisSuggestion{
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
		suggestions = append(suggestions, AnalysisSuggestion{
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
		" | Specs: " + strconv.Itoa(len(specs)) +
		" | Designs: " + strconv.Itoa(len(attachments)) +
		" | Tasks: " + strconv.Itoa(len(tasks)) +
		" (" + strconv.Itoa(completedTasks) + " done, " + strconv.Itoa(failedTasks) + " failed)" +
		" | Focus: " + target
}

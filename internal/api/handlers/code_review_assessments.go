package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (h *CodeReviewHandler) SetAssessments(assessments *db.CodeReviewAssessmentStore, rechecks *db.CodeReviewRecheckStore, prs *db.PullRequestStore) {
	h.assessments = assessments
	h.rechecks = rechecks
	h.assessmentPRs = prs
}

func (h *CodeReviewHandler) loadAssessment(w http.ResponseWriter, r *http.Request) (models.CodeReviewAssessment, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, 400, "INVALID_ID", "invalid assessment ID")
		return models.CodeReviewAssessment{}, false
	}
	if h.assessments == nil {
		writeError(w, r, 503, "CODE_REVIEW_ASSESSMENTS_UNAVAILABLE", "review assessments are unavailable")
		return models.CodeReviewAssessment{}, false
	}
	assessment, err := h.assessments.GetByID(r.Context(), middleware.OrgIDFromContext(r.Context()), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, 404, "CODE_REVIEW_NOT_FOUND", "review assessment not found")
		return assessment, false
	}
	if err != nil {
		writeError(w, r, 500, "CODE_REVIEW_ASSESSMENT_FAILED", "failed to load review assessment", err)
		return assessment, false
	}
	return assessment, true
}

func (h *CodeReviewHandler) GetAssessment(w http.ResponseWriter, r *http.Request) {
	assessment, ok := h.loadAssessment(w, r)
	if !ok {
		return
	}
	pr, err := h.assessmentPRs.GetByID(r.Context(), middleware.OrgIDFromContext(r.Context()), assessment.PullRequestID)
	if err != nil {
		writeError(w, r, 500, "CODE_REVIEW_ASSESSMENT_FAILED", "failed to load assessment target", err)
		return
	}
	writeJSON(w, 200, map[string]any{"data": struct {
		models.CodeReviewAssessment
		GitHubRepo       string `json:"github_repo"`
		GitHubPRNumber   int    `json:"github_pr_number"`
		PullRequestTitle string `json:"pull_request_title"`
	}{assessment, pr.GitHubRepo, pr.GitHubPRNumber, pr.Title}})
}

func (h *CodeReviewHandler) AssessmentEvidence(w http.ResponseWriter, r *http.Request) {
	assessment, ok := h.loadAssessment(w, r)
	if !ok {
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	sourceID := assessment.ID
	if assessment.SourceAssessmentID != nil {
		sourceID = *assessment.SourceAssessmentID
	}
	results, err := h.assessments.ListAgentResults(r.Context(), orgID, sourceID)
	if err != nil {
		writeError(w, r, 500, "CODE_REVIEW_EVIDENCE_FAILED", "failed to load code review results", err)
		return
	}
	findings, err := h.assessments.ListFindings(r.Context(), orgID, sourceID)
	if err != nil {
		writeError(w, r, 500, "CODE_REVIEW_EVIDENCE_FAILED", "failed to load code review findings", err)
		return
	}
	records, err := h.assessments.ListPromptRecords(r.Context(), orgID, assessment.ID)
	if err != nil {
		writeError(w, r, 500, "CODE_REVIEW_EVIDENCE_FAILED", "failed to load assessment evidence", err)
		return
	}
	var dispatch *models.CodeReviewRecheckDispatch
	if assessment.ReviewScope == models.CodeReviewScopeEvidenceOnly && h.rechecks != nil {
		value, loadErr := h.rechecks.Get(r.Context(), orgID, assessment.ID)
		if loadErr != nil && !errors.Is(loadErr, pgx.ErrNoRows) {
			writeError(w, r, 500, "CODE_REVIEW_EVIDENCE_FAILED", "failed to load assessment execution", loadErr)
			return
		}
		if loadErr == nil {
			dispatch = &value
		}
	}
	var citationResults []models.CodeReviewAgentResult
	if assessment.ReviewScope == models.CodeReviewScopeFull {
		citationResults = results
	}
	visual, cited, err := codeReviewVisualEvidenceForAPI(orgID, assessment.SessionID, records, citationResults)
	if err != nil {
		writeError(w, r, 500, "CODE_REVIEW_EVIDENCE_FAILED", "failed to load visual evidence", err)
		return
	}
	outcome, textEvidence, err := codeReviewAssessmentAuditFields(assessment)
	if err != nil {
		writeError(w, r, 500, "CODE_REVIEW_EVIDENCE_FAILED", "failed to load assessment audit evidence", err)
		return
	}
	if visual != nil && assessment.ReviewScope == models.CodeReviewScopeEvidenceOnly {
		seen := make(map[string]bool)
		for _, requirement := range outcome.Synthesis.DescriptionAssessments {
			for _, id := range requirement.EvidenceIDs {
				if !seen[id] {
					cited = append(cited, id)
					seen[id] = true
				}
			}
		}
		visualIDs := make(map[string]bool, len(visual.Evidence))
		for _, item := range visual.Evidence {
			visualIDs[item.EvidenceID] = true
		}
		for _, reassessment := range outcome.FindingReassessments {
			for _, citation := range reassessment.EvidenceCitations {
				if visualIDs[citation.EvidenceID] && !seen[citation.EvidenceID] {
					cited = append(cited, citation.EvidenceID)
					seen[citation.EvidenceID] = true
				}
			}
		}
		for _, reassessment := range outcome.RequirementReassessments {
			for _, citation := range reassessment.EvidenceCitations {
				if visualIDs[citation.EvidenceID] && !seen[citation.EvidenceID] {
					cited = append(cited, citation.EvidenceID)
					seen[citation.EvidenceID] = true
				}
			}
		}
	}
	writeJSON(w, 200, map[string]any{"data": map[string]any{"assessment": assessment, "source_assessment_id": sourceID, "agent_results": results, "findings": findings, "source_findings": findings, "finding_reassessments": outcome.FindingReassessments, "requirement_reassessments": outcome.RequirementReassessments, "text_evidence": textEvidence, "prompt_records": records, "execution": dispatch, "visual_evidence": visual, "cited_visual_evidence_ids": cited}})
}

type codeReviewAssessmentAuditOutcome struct {
	FindingReassessments     []models.CodeReviewFindingReassessment     `json:"finding_reassessments"`
	RequirementReassessments []models.CodeReviewRequirementReassessment `json:"requirement_reassessments"`
	Synthesis                struct {
		DescriptionAssessments []struct {
			EvidenceIDs []string `json:"evidence_ids"`
		} `json:"description_assessments"`
	} `json:"synthesis"`
}

func codeReviewAssessmentAuditFields(assessment models.CodeReviewAssessment) (codeReviewAssessmentAuditOutcome, json.RawMessage, error) {
	var outcome codeReviewAssessmentAuditOutcome
	if len(assessment.StructuredOutcome) > 0 {
		if err := json.Unmarshal(assessment.StructuredOutcome, &outcome); err != nil {
			return outcome, nil, err
		}
	}
	var manifest struct {
		TextEvidence json.RawMessage `json:"text_evidence"`
	}
	if len(assessment.InputManifest) > 0 {
		if err := json.Unmarshal(assessment.InputManifest, &manifest); err != nil {
			return outcome, nil, err
		}
	}
	return outcome, manifest.TextEvidence, nil
}

func (h *CodeReviewHandler) withAssessmentSummaries(ctx context.Context, orgID uuid.UUID, items []models.CodeReviewListItem) ([]models.CodeReviewListItem, error) {
	if h.assessments == nil || len(items) == 0 {
		return items, nil
	}
	ids := make([]uuid.UUID, 0, len(items))
	seen := make(map[uuid.UUID]bool)
	for _, item := range items {
		if !seen[item.PullRequestID] {
			ids = append(ids, item.PullRequestID)
			seen[item.PullRequestID] = true
		}
	}
	current, active, failed, err := h.assessments.ListCurrentForPRs(ctx, orgID, ids)
	if err != nil {
		return nil, err
	}
	updated := append([]models.CodeReviewListItem(nil), items...)
	for i, item := range updated {
		if a, ok := current[item.PullRequestID]; ok {
			updated[i].CurrentAssessment = &a
		}
		if a, ok := active[item.PullRequestID]; ok {
			updated[i].ActiveAssessment = &a
		}
		if a, ok := failed[item.PullRequestID]; ok {
			updated[i].LatestFailedAssessment = &a
		}
	}
	return updated, nil
}

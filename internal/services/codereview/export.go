package codereview

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
)

type HistoricalExportStore interface {
	Status(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (models.CodeReviewExportStatus, error)
	Release(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, models.CodeReviewExportArchiveProof) (models.CodeReviewExportStatus, error)
	Acquire(context.Context, uuid.UUID, models.CodeReviewExportFilters) (models.CodeReviewExportAcquisition, error)
	Page(context.Context, uuid.UUID, models.CodeReviewExportCursor, int) (models.CodeReviewExportAcquisition, []models.CodeReviewExportSource, error)
}
type HistoricalExportService struct{ store HistoricalExportStore }

func NewHistoricalExportService(store HistoricalExportStore) *HistoricalExportService {
	return &HistoricalExportService{store: store}
}

var ErrHistoricalExportCursor = fmt.Errorf("invalid historical export cursor or filter scope")

func (s *HistoricalExportService) Export(ctx context.Context, orgID uuid.UUID, filters models.CodeReviewExportFilters, cursor string, limit int) (models.CodeReviewExportPage, error) {
	if err := filters.Validate(); err != nil {
		return models.CodeReviewExportPage{}, err
	}
	if limit < 1 || limit > 100 {
		return models.CodeReviewExportPage{}, ErrHistoricalExportCursor
	}
	var c models.CodeReviewExportCursor
	if cursor == "" {
		a, err := s.store.Acquire(ctx, orgID, filters)
		if err != nil {
			return models.CodeReviewExportPage{}, err
		}
		c.AcquisitionID = a.ID
	} else {
		if len(cursor) > 1024 {
			return models.CodeReviewExportPage{}, ErrHistoricalExportCursor
		}
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil {
			return models.CodeReviewExportPage{}, ErrHistoricalExportCursor
		}
		if err = json.Unmarshal(raw, &c); err != nil || c.AcquisitionID == uuid.Nil || c.Position < 0 {
			return models.CodeReviewExportPage{}, ErrHistoricalExportCursor
		}
	}
	a, rows, err := s.store.Page(ctx, orgID, c, limit)
	if err != nil {
		// A concurrent release/expiry can land after Acquire returns but before
		// the first page locks the header. Explicit old cursors still fail gone.
		if cursor == "" && (errors.Is(err, db.ErrReviewExportReleased) || errors.Is(err, db.ErrReviewExportExpired)) {
			return models.CodeReviewExportPage{}, db.ErrReviewExportBusy
		}
		return models.CodeReviewExportPage{}, err
	}
	if !reflect.DeepEqual(a.Filters, filters) {
		return models.CodeReviewExportPage{}, ErrHistoricalExportCursor
	}
	page := models.CodeReviewExportPage{Data: make([]models.CodeReviewExportRecord, 0, len(rows)), Acquisition: a}
	for _, row := range rows {
		page.Data = append(page.Data, historicalExportRecord(row))
	}
	page.Meta.Position = c.Position + int64(len(rows))
	page.Meta.Returned = len(rows)
	page.Meta.Complete = page.Meta.Position == a.Counts.Records
	if !page.Meta.Complete {
		next, err := json.Marshal(models.CodeReviewExportCursor{AcquisitionID: a.ID, Position: page.Meta.Position})
		if err != nil {
			return models.CodeReviewExportPage{}, err
		}
		encoded := base64.RawURLEncoding.EncodeToString(next)
		page.Meta.NextCursor = &encoded
	}
	return page, nil
}

func (s *HistoricalExportService) Status(ctx context.Context, orgID, repositoryID, id uuid.UUID) (models.CodeReviewExportStatus, error) {
	return s.store.Status(ctx, orgID, repositoryID, id)
}

func (s *HistoricalExportService) Release(ctx context.Context, orgID, repositoryID, id uuid.UUID, proof models.CodeReviewExportArchiveProof) (models.CodeReviewExportStatus, error) {
	return s.store.Release(ctx, orgID, repositoryID, id, proof)
}

func exportObject(raw json.RawMessage) map[string]any {
	var value map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil
	}
	return value
}
func exportMap(value any) map[string]any { m, _ := value.(map[string]any); return m }
func exportArray(value any) []any        { a, _ := value.([]any); return a }
func exportString(value any) string      { s, _ := value.(string); return s }
func exportEvidence(value any, availability models.ReviewEvidenceAvailability, provenance string) models.ReviewExportEvidence {
	if value == nil || value == "" || ((reflect.ValueOf(value).Kind() == reflect.Map || reflect.ValueOf(value).Kind() == reflect.Slice) && reflect.ValueOf(value).IsNil()) {
		return models.ReviewExportEvidence{Availability: models.ReviewEvidenceUnavailable, Provenance: provenance}
	}
	return models.ReviewExportEvidence{Value: value, Availability: availability, Provenance: provenance}
}
func recorded(value any, path string) models.ReviewExportEvidence {
	return exportEvidence(value, models.ReviewEvidenceRecorded, path)
}
func retainedPolicyEvidence(value any, path string) models.ReviewExportEvidence {
	return exportEvidence(value, models.ReviewEvidenceReconstructed, path+"; retained policy row observed at acquisition; migrations may have added defaults after assessment; historical evaluation not established")
}
func unavailable(reason string) models.ReviewExportEvidence {
	return exportEvidence(nil, models.ReviewEvidenceUnavailable, reason)
}

func historicalExportRecord(raw models.CodeReviewExportSource) models.CodeReviewExportRecord {
	src, policy, pr := exportObject(raw.Source), exportObject(raw.Policy), exportObject(raw.PullRequest)
	evidence := make(map[string]models.ReviewExportEvidence)
	for _, field := range []string{"repository_id", "pull_request_id", "session_id", "generation", "metadata_id", "source_assessment_id", "previous_assessment_id", "previous_published_assessment_id", "superseded_by_assessment_id", "superseded_by_session_id", "head_sha", "base_sha", "base_ref", "decision", "acceptable", "review_scope", "result_origin", "coverage_complete", "status", "created_at", "completed_at", "superseded_at", "failure_detail", "failure_reason", "from_fork", "stale", "risk_reason_details", "structured_outcome", "input_manifest", "publication_state", "publication_receipt", "submitted_commit_sha", "github_review_id", "github_review_url", "policy_id"} {
		evidence[field] = recorded(src[field], "raw.source."+field)
	}
	key := "publication_key"
	if raw.Kind == models.ReviewExportLegacy {
		key = "review_output_key"
	}
	evidence["publication_key"] = recorded(src[key], "raw.source."+key)
	evidence["repository_full_name"] = recorded(src["repository_full_name"], "assessment capture")
	if raw.Kind == models.ReviewExportLegacy {
		evidence["repository_full_name"] = exportEvidence(pr["repository"], models.ReviewEvidenceReconstructed, "raw.pull_request.repository at acquisition; rename history unavailable")
	}
	evidence["pr_number"] = recorded(pr["number"], "raw.pull_request.number at acquisition")
	evidence["policy_version"] = recorded(policy["version"], "raw.policy.version; retained version selected by source policy_id")
	evidence["policy_snapshot"] = retainedPolicyEvidence(policy, "raw.policy; retained version selected by source policy_id, no active-policy fallback")
	manifest := exportMap(src["input_manifest"])
	contract := exportMap(manifest["contract"])
	evidence["captured_policy_version"] = recorded(contract["policy_version"], "raw.source.input_manifest.contract.policy_version; compare with retained policy version without substituting")
	evidence["captured_policy_digest"] = recorded(contract["policy_digest"], "raw.source.input_manifest.contract.policy_digest")
	evidence["provider_submitted_at"] = unavailable(raw.PublicationTimeProvenance)
	if raw.PublicationTime != nil {
		evidence["provider_submitted_at"] = exportEvidence(*raw.PublicationTime, models.ReviewEvidenceReconstructed, raw.PublicationTimeProvenance)
	}
	evidence["provider_observations"] = recorded(exportArrayJSON(raw.ProviderObservations), "raw.provider_observations; retained provider objects may predate updates, have a different head, or lack a publication marker; only publication_time binds this source")
	evidence["pr_merged_at"] = recorded(pr["merged_at"], "raw.pull_request.merged_at; provider-synchronized observation at acquisition, not assessment-time PR state")
	evidence["pr_closed_at"] = unavailable("pull_requests does not retain provider close timestamps; updated_at is not a substitute")
	evidence["pr_state"] = exportEvidence(pr["status"], models.ReviewEvidenceReconstructed, "raw.pull_request.status at acquisition; no historical state reconstruction")
	evidence["agent_results"] = exportEvidence(exportArrayJSON(raw.AgentResults), models.ReviewEvidenceReconstructed, "raw.agent_results matched by assessment_id, or unassigned legacy session results; mutable status/output is acquisition-time evidence, not an immutable historical finding/count snapshot")
	evidence["historical_blocking_findings"] = unavailable("no complete immutable finding-state/count snapshot; current finding rows are intentionally not joined")
	evidence["substantive_review_completion"] = recorded(map[string]any{"coverage_complete": src["coverage_complete"], "review_scope": src["review_scope"], "result_origin": src["result_origin"], "status": src["status"], "source_assessment_id": src["source_assessment_id"]}, "raw.source coverage and lineage only; evidence-only coverage does not prove a substantive full review; incomplete/unknown coverage cannot establish hypothetical approval")
	if raw.Kind == models.ReviewExportLegacy {
		evidence["substantive_review_completion"] = unavailable("legacy completion does not retain full-review coverage; session completion cannot establish substantive review")
	}
	outcome := exportMap(src["structured_outcome"])
	for _, field := range []string{"finding_reassessments", "requirement_reassessments", "description_assessments"} {
		evidence[field] = recorded(outcome[field], "raw.source.structured_outcome."+field)
	}
	safeguards := exportSafeguards(src, policy)
	for _, g := range safeguards {
		if g.ID == "blocking_findings" && g.Result == models.ReviewSafeguardFailed {
			evidence["historical_blocking_findings"] = g.Inputs
		}
	}
	return models.CodeReviewExportRecord{SourceIdentity: string(raw.Kind) + ":" + raw.ID.String(), Raw: raw, Evidence: evidence, Safeguards: safeguards}
}
func exportArrayJSON(raw json.RawMessage) any {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil
	}
	return value
}

// Each known safeguard is present even when no evaluation was retained. Failure
// reasons are positive evidence of failure only: their absence never proves pass.
func exportSafeguards(src, policy map[string]any) []models.ReviewExportSafeguard {
	ids := strings.Fields("reviewer_disabled context_unavailable head_changed files_limit_exceeded lines_limit_exceeded additions_limit_exceeded deletions_limit_exceeded checks_failing required_check_failing description_failed branch_out_of_date fork_ineligible author_ineligible unresolved_human_review blocking_findings reviewer_disagreement scope_mismatch unresolved_uncertainty prompt_injection sensitive_path path_outside_scope blocked_path policy_path_changed excluded_category reviewer_quorum orchestrator_synthesis_invalid orchestrator_escalation orchestrator_context_stale architecture ownership operational_risk sensitive_change policy_requirement screenshot")
	reasons := exportArray(src["risk_reason_details"])
	byCode := make(map[string][]any)
	for _, reason := range reasons {
		m := exportMap(reason)
		if code := exportString(m["code"]); code != "" {
			byCode[code] = append(byCode[code], reason)
		}
	}
	known := make(map[string]bool, len(ids))
	for _, id := range ids {
		known[id] = true
	}
	var extra []string
	for code := range byCode {
		if !known[code] {
			extra = append(extra, code)
		}
	}
	sort.Strings(extra)
	ids = append(ids, extra...)
	thresholds := map[string]string{"files_limit_exceeded": "max_files_changed", "lines_limit_exceeded": "max_lines_changed", "additions_limit_exceeded": "max_additions", "deletions_limit_exceeded": "max_deletions", "checks_failing": "require_passing_checks", "required_check_failing": "required_checks", "branch_out_of_date": "require_up_to_date", "fork_ineligible": "allow_forks", "author_ineligible": "eligible_authors", "sensitive_path": "sensitive_paths", "path_outside_scope": "allowed_path_patterns", "blocked_path": "blocked_path_patterns"}
	risk := exportMap(policy["risk_policy"])
	out := make([]models.ReviewExportSafeguard, 0, len(ids))
	for _, id := range ids {
		g := models.ReviewExportSafeguard{ID: id, Result: models.ReviewSafeguardUnknown, Inputs: unavailable("measured inputs not retained"), Threshold: unavailable("threshold not retained"), Applicability: unavailable("evaluation/applicability not retained"), Provenance: "absent failure reason does not establish passed or not_evaluated"}
		if key := thresholds[id]; key != "" {
			g.Threshold = retainedPolicyEvidence(risk[key], "raw.policy.risk_policy."+key+"; retained setting, not proof of evaluation")
		}
		if found := byCode[id]; len(found) > 0 {
			g.Result = models.ReviewSafeguardFailed
			g.Inputs = recorded(found, "raw.source.risk_reason_details; exact retained reason objects including actual/limit/subject when present")
			g.Provenance = "explicit retained failure reason; missing numeric keys remain unknown, including omitted zero values"
		}
		if id == "reviewer_disabled" {
			g.Threshold = retainedPolicyEvidence(policy["enabled"], "raw.policy.enabled")
		}
		if id == "reviewer_quorum" {
			g.Threshold = retainedPolicyEvidence(exportMap(policy["agent_roster"])["require_reviewer_quorum"], "raw.policy.agent_roster.require_reviewer_quorum")
		}
		if id == "author_ineligible" && (risk["eligible_authors"] != nil || risk["eligible_author_teams"] != nil) {
			g.Threshold = retainedPolicyEvidence(map[string]any{"eligible_authors": risk["eligible_authors"], "eligible_author_teams": risk["eligible_author_teams"]}, "raw.policy.risk_policy eligible author/teams settings; no current team membership substitution")
		}
		if id == "screenshot" {
			g.Threshold = retainedPolicyEvidence(policy["description_policy"], "raw.policy.description_policy; visual requirements and applicability, no inferred screenshot result")
		}
		out = append(out, g)
	}
	// Description requirements have stable policy keys. Export assessment-time
	// statuses (including not_applicable) and evidence citations without re-running
	// today's description policy. Legacy mutable synthesis stays in raw only.
	outcome := exportMap(src["structured_outcome"])
	assessments := exportArray(outcome["description_assessments"])
	if nested := exportMap(outcome["synthesis"]); nested != nil {
		assessments = exportArray(nested["description_assessments"])
	}
	descriptions := exportMap(policy["description_policy"])
	requirementKeys := make(map[string]bool)
	var keys []string
	requirements := make(map[string]any)
	for _, requirement := range exportArray(descriptions["requirements"]) {
		req := exportMap(requirement)
		key := exportString(req["key"])
		if key == "" {
			continue
		}
		if requirementKeys[key] {
			requirements[key] = nil
			continue
		}
		requirementKeys[key] = true
		keys = append(keys, key)
		requirements[key] = req
	}
	var orphanKeys []string
	for _, assessment := range assessments {
		key := exportString(exportMap(assessment)["key"])
		if key != "" && !requirementKeys[key] {
			requirementKeys[key] = true
			orphanKeys = append(orphanKeys, key)
		}
	}
	sort.Strings(orphanKeys)
	keys = append(keys, orphanKeys...)
	for _, key := range keys {
		req := exportMap(requirements[key])
		g := models.ReviewExportSafeguard{ID: "description:" + key, Result: models.ReviewSafeguardUnknown, Inputs: unavailable("assessment-time description evaluation not retained"), Threshold: retainedPolicyEvidence(req, "raw.policy.description_policy.requirements"), Applicability: retainedPolicyEvidence(map[string]any{"applies_when": req["applies_when"], "applicability": req["applicability"], "evidence_kind": req["evidence_kind"], "required": req["required"]}, "retained requirement applicability configuration; not a current applicability verdict"), Provenance: "no default applicability or pass inferred"}
		if req == nil {
			g.Applicability = unavailable("requirement policy applicability missing or conflicting")
		}
		var matching []any
		for _, assessment := range assessments {
			a := exportMap(assessment)
			if exportString(a["key"]) == key {
				matching = append(matching, assessment)
			}
		}
		if len(matching) == 1 {
			a := exportMap(matching[0])
			g.Inputs = recorded(a, "raw.source.structured_outcome description assessment")
			switch exportString(a["status"]) {
			case "satisfied":
				g.Result = models.ReviewSafeguardPassed
			case "missing":
				g.Result = models.ReviewSafeguardFailed
			case "not_applicable":
				g.Result = models.ReviewSafeguardNotEvaluated
			}
			g.Applicability = recorded(a["status"], "raw.source.structured_outcome description status; not_applicable is explicitly not_evaluated")
			g.Provenance = "explicit assessment-time description status; requirement result alone does not establish complete substantive review"
		}
		out = append(out, g)
	}
	return out
}

package codereview

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
)

var (
	ErrCodeReviewPolicyComparisonOrder = errors.New("newer policy version must be newer than the comparison version")
	ErrCodeReviewPolicyAlreadyActive   = errors.New("cannot restore the active policy version")
)

type CodeReviewPolicyRestoreConflictError struct {
	CurrentVersion int
}

func (e *CodeReviewPolicyRestoreConflictError) Error() string {
	return fmt.Sprintf("code review policy changed; current version is %d", e.CurrentVersion)
}

type PolicyHistoryStore interface {
	ListPolicyVersions(ctx context.Context, orgID uuid.UUID, beforeVersion *int, limit int) ([]models.CodeReviewPolicyRecord, error)
	GetPolicyByID(ctx context.Context, orgID, policyID uuid.UUID) (models.CodeReviewPolicyRecord, error)
	ResolvePolicy(ctx context.Context, orgID uuid.UUID) (models.CodeReviewResolvedPolicy, error)
	SavePolicyExpectingVersion(ctx context.Context, orgID uuid.UUID, config models.CodeReviewPolicyConfig, expectedVersion int, createdByUserID *uuid.UUID) (models.CodeReviewPolicyRecord, error)
}

type PolicyHistoryAuditStore interface {
	ListLatestByResourceIDs(ctx context.Context, orgID uuid.UUID, resourceType models.AuditResourceType, resourceIDs []string) ([]models.AuditLogWithActorName, error)
}

type CodeReviewPolicyHistoryPage struct {
	Versions   []models.CodeReviewPolicyVersionSummary
	NextCursor string
}

type PolicyHistoryService struct {
	policies PolicyHistoryStore
	audits   PolicyHistoryAuditStore
	logger   zerolog.Logger
}

func NewPolicyHistoryService(policies PolicyHistoryStore, audits PolicyHistoryAuditStore, logger zerolog.Logger) *PolicyHistoryService {
	return &PolicyHistoryService{policies: policies, audits: audits, logger: logger}
}

func (s *PolicyHistoryService) List(ctx context.Context, orgID uuid.UUID, beforeVersion *int, limit int) (CodeReviewPolicyHistoryPage, error) {
	if limit <= 0 || limit > 50 {
		limit = 15
	}
	records, err := s.policies.ListPolicyVersions(ctx, orgID, beforeVersion, limit+1)
	if err != nil {
		return CodeReviewPolicyHistoryPage{}, err
	}
	visibleCount := len(records)
	if visibleCount > limit {
		visibleCount = limit
	}
	visible := records[:visibleCount]
	auditsByResource, err := s.auditMap(ctx, orgID, visible)
	if err != nil {
		return CodeReviewPolicyHistoryPage{}, err
	}

	versions := make([]models.CodeReviewPolicyVersionSummary, 0, visibleCount)
	for i := range visible {
		var previous *models.CodeReviewPolicyRecord
		if i+1 < len(records) {
			previous = &records[i+1]
		}
		versions = append(versions, s.summary(visible[i], previous, auditsByResource[visible[i].ID.String()]))
	}

	nextCursor := ""
	if len(records) > limit && visibleCount > 0 {
		nextCursor = fmt.Sprintf("%d", visible[visibleCount-1].Version)
	}
	return CodeReviewPolicyHistoryPage{Versions: versions, NextCursor: nextCursor}, nil
}

func (s *PolicyHistoryService) Compare(ctx context.Context, orgID, newerID, olderID uuid.UUID) (models.CodeReviewPolicyComparison, error) {
	newer, err := s.policies.GetPolicyByID(ctx, orgID, newerID)
	if err != nil {
		return models.CodeReviewPolicyComparison{}, err
	}
	older, err := s.policies.GetPolicyByID(ctx, orgID, olderID)
	if err != nil {
		return models.CodeReviewPolicyComparison{}, err
	}
	if newer.RepositoryID != nil || older.RepositoryID != nil {
		return models.CodeReviewPolicyComparison{}, pgx.ErrNoRows
	}
	if newer.Version <= older.Version {
		return models.CodeReviewPolicyComparison{}, ErrCodeReviewPolicyComparisonOrder
	}
	auditsByResource, err := s.auditMap(ctx, orgID, []models.CodeReviewPolicyRecord{newer, older})
	if err != nil {
		return models.CodeReviewPolicyComparison{}, err
	}
	changes, err := diffCodeReviewPolicyConfigs(older.Config(), newer.Config())
	if err != nil {
		return models.CodeReviewPolicyComparison{}, err
	}
	return models.CodeReviewPolicyComparison{
		Newer:   s.summary(newer, &older, auditsByResource[newer.ID.String()]),
		Older:   s.summary(older, nil, auditsByResource[older.ID.String()]),
		Changes: changes,
	}, nil
}

func (s *PolicyHistoryService) Restore(ctx context.Context, orgID, policyID, userID uuid.UUID, expectedVersion int) (models.CodeReviewPolicyRestoreResult, error) {
	target, err := s.policies.GetPolicyByID(ctx, orgID, policyID)
	if err != nil {
		return models.CodeReviewPolicyRestoreResult{}, err
	}
	if target.RepositoryID != nil {
		return models.CodeReviewPolicyRestoreResult{}, pgx.ErrNoRows
	}
	if target.Active {
		return models.CodeReviewPolicyRestoreResult{}, ErrCodeReviewPolicyAlreadyActive
	}
	restored, err := s.policies.SavePolicyExpectingVersion(ctx, orgID, target.Config(), expectedVersion, &userID)
	if err != nil {
		if errors.Is(err, db.ErrCodeReviewPolicyVersionConflict) {
			current, resolveErr := s.policies.ResolvePolicy(ctx, orgID)
			if resolveErr != nil {
				return models.CodeReviewPolicyRestoreResult{}, fmt.Errorf("resolve current policy after restore conflict: %w", resolveErr)
			}
			currentVersion := 0
			if current.Policy != nil {
				currentVersion = current.Policy.Version
			}
			return models.CodeReviewPolicyRestoreResult{}, &CodeReviewPolicyRestoreConflictError{CurrentVersion: currentVersion}
		}
		return models.CodeReviewPolicyRestoreResult{}, err
	}
	return models.CodeReviewPolicyRestoreResult{Policy: restored, RestoredFrom: target}, nil
}

func (s *PolicyHistoryService) auditMap(ctx context.Context, orgID uuid.UUID, records []models.CodeReviewPolicyRecord) (map[string]*models.CodeReviewPolicyVersionAudit, error) {
	resourceIDs := make([]string, 0, len(records))
	for _, record := range records {
		resourceIDs = append(resourceIDs, record.ID.String())
	}
	entries, err := s.audits.ListLatestByResourceIDs(ctx, orgID, models.AuditResourceCodeReviewPolicy, resourceIDs)
	if err != nil {
		return nil, err
	}
	result := make(map[string]*models.CodeReviewPolicyVersionAudit, len(entries))
	for _, entry := range entries {
		if entry.ResourceID == nil {
			continue
		}
		var details struct {
			Source   string `json:"source"`
			Reason   string `json:"reason"`
			ToolName string `json:"tool_name"`
		}
		if len(entry.Details) > 0 && !bytes.Equal(entry.Details, []byte("null")) {
			if decodeErr := json.Unmarshal(entry.Details, &details); decodeErr != nil {
				s.logger.Warn().Err(decodeErr).Int64("audit_log_id", entry.ID).Msg("failed to decode code review policy audit details")
			}
		}
		ipAddress := ""
		if entry.IPAddress != nil {
			ipAddress = entry.IPAddress.String()
		}
		actorName := ""
		if entry.ActorName != nil {
			actorName = *entry.ActorName
		}
		result[*entry.ResourceID] = &models.CodeReviewPolicyVersionAudit{
			ID: entry.ID, ActorType: entry.ActorType, ActorID: entry.ActorID, ActorName: actorName, UserID: entry.UserID,
			Source: details.Source, Reason: details.Reason, ToolName: details.ToolName,
			RequestID: entry.RequestID, IPAddress: ipAddress, UserAgent: entry.UserAgent,
			SessionID: entry.SessionID, CreatedAt: entry.CreatedAt,
		}
	}
	return result, nil
}

func (s *PolicyHistoryService) summary(record models.CodeReviewPolicyRecord, previous *models.CodeReviewPolicyRecord, audit *models.CodeReviewPolicyVersionAudit) models.CodeReviewPolicyVersionSummary {
	summary := models.CodeReviewPolicyVersionSummary{
		ID: record.ID, Version: record.Version, Active: record.Active,
		CreatedByUserID: record.CreatedByUserID, CreatedAt: record.CreatedAt, Audit: audit,
		ChangedFields: []models.CodeReviewPolicyChangedField{},
	}
	if previous == nil {
		if record.Version == 1 {
			summary.Summary = "Initial organization policy created"
		} else {
			summary.Summary = fmt.Sprintf("Version %d", record.Version)
		}
		return summary
	}
	previousID := previous.ID
	previousVersion := previous.Version
	summary.PreviousPolicyID = &previousID
	summary.PreviousPolicyVersion = &previousVersion
	changes, err := diffCodeReviewPolicyConfigs(previous.Config(), record.Config())
	if err != nil {
		s.logger.Warn().Err(err).Int("policy_version", record.Version).Msg("failed to summarize code review policy version")
		summary.Summary = "Policy updated"
		return summary
	}
	summary.ChangedFields = make([]models.CodeReviewPolicyChangedField, 0, len(changes))
	for _, change := range changes {
		summary.ChangedFields = append(summary.ChangedFields, change.CodeReviewPolicyChangedField)
	}
	summary.Summary = codeReviewPolicyChangeSummary(changes)
	return summary
}

func diffCodeReviewPolicyConfigs(before, after models.CodeReviewPolicyConfig) ([]models.CodeReviewPolicyFieldChange, error) {
	beforeMap, err := codeReviewPolicyConfigMap(before)
	if err != nil {
		return nil, err
	}
	afterMap, err := codeReviewPolicyConfigMap(after)
	if err != nil {
		return nil, err
	}
	changes := make([]models.CodeReviewPolicyFieldChange, 0)
	diffCodeReviewPolicyValue("", beforeMap, afterMap, &changes)
	sort.Slice(changes, func(i, j int) bool {
		left, right := policyFieldSortKey(changes[i].Path), policyFieldSortKey(changes[j].Path)
		if left == right {
			return changes[i].Path < changes[j].Path
		}
		return left < right
	})
	return changes, nil
}

func codeReviewPolicyConfigMap(config models.CodeReviewPolicyConfig) (map[string]any, error) {
	encoded, err := json.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("marshal code review policy config for diff: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var result map[string]any
	if err := decoder.Decode(&result); err != nil {
		return nil, fmt.Errorf("decode code review policy config for diff: %w", err)
	}
	return result, nil
}

func diffCodeReviewPolicyValue(path string, before, after any, changes *[]models.CodeReviewPolicyFieldChange) {
	if reflect.DeepEqual(before, after) {
		return
	}
	beforeMap, beforeIsMap := before.(map[string]any)
	afterMap, afterIsMap := after.(map[string]any)
	if beforeIsMap && afterIsMap {
		keys := make(map[string]struct{}, len(beforeMap)+len(afterMap))
		for key := range beforeMap {
			keys[key] = struct{}{}
		}
		for key := range afterMap {
			keys[key] = struct{}{}
		}
		sortedKeys := make([]string, 0, len(keys))
		for key := range keys {
			sortedKeys = append(sortedKeys, key)
		}
		sort.Strings(sortedKeys)
		for _, key := range sortedKeys {
			childPath := key
			if path != "" {
				childPath = path + "." + key
			}
			diffCodeReviewPolicyValue(childPath, beforeMap[key], afterMap[key], changes)
		}
		return
	}
	kind := models.CodeReviewPolicyChangeKindValue
	if path == models.CodeReviewPolicyFieldReviewInstructions || path == models.CodeReviewPolicyFieldAutomatedApprovalPolicy {
		kind = models.CodeReviewPolicyChangeKindText
	} else if isJSONList(before) || isJSONList(after) {
		kind = models.CodeReviewPolicyChangeKindList
	}
	*changes = append(*changes, models.CodeReviewPolicyFieldChange{
		CodeReviewPolicyChangedField: models.CodeReviewPolicyChangedField{Path: path, Label: policyFieldLabel(path), Kind: kind},
		Before:                       before,
		After:                        after,
	})
}

func isJSONList(value any) bool {
	_, ok := value.([]any)
	return ok
}

func codeReviewPolicyChangeSummary(changes []models.CodeReviewPolicyFieldChange) string {
	if len(changes) == 0 {
		return "Policy saved with no effective changes"
	}
	if len(changes) == 1 {
		verb := "changed"
		if changes[0].Kind == models.CodeReviewPolicyChangeKindText {
			verb = "edited"
		}
		return fmt.Sprintf("%s %s", changes[0].Label, verb)
	}
	return fmt.Sprintf("%d policy fields changed", len(changes))
}

var codeReviewPolicyFieldLabels = map[string]string{
	"enabled":                                      "Code reviews",
	"approval_mode":                                "Review outcome",
	"review_instructions":                          "Review instructions",
	"automated_approval_policy":                    "Automated approval policy",
	"description_policy.requirements":              "Description requirements",
	"risk_policy.max_files_changed":                "Maximum files changed",
	"risk_policy.max_lines_changed":                "Maximum lines changed",
	"risk_policy.semantic_dedupe_cooldown_seconds": "Duplicate review cooldown",
	"risk_policy.stop_after_deterministic_failure": "Stop after deterministic failure",
	"risk_policy.require_passing_checks":           "Require passing checks",
	"risk_policy.exclude_sensitive_paths":          "Exclude sensitive paths",
	"risk_policy.sensitive_paths":                  "Sensitive paths",
	"risk_policy.allowed_path_patterns":            "Allowed paths",
	"risk_policy.blocked_path_patterns":            "Blocked paths",
	"risk_policy.require_up_to_date":               "Require up-to-date branch",
	"risk_policy.allow_forks":                      "Allow forks",
	"risk_policy.eligible_authors":                 "Eligible authors",
	"risk_policy.eligible_author_teams":            "Eligible author teams",
	"risk_policy.required_checks":                  "Required checks",
	"agent_roster.reviewers":                       "Reviewers",
	"agent_roster.orchestrator":                    "Orchestrator",
	"agent_roster.reviewer_models":                 "Reviewer models",
	"agent_roster.reviewer_reasoning_efforts":      "Reviewer reasoning",
	"agent_roster.orchestrator_model":              "Orchestrator model",
	"agent_roster.reasoning_effort":                "Orchestrator reasoning",
	"agent_roster.disagreement_blocks":             "Block on reviewer disagreement",
	"agent_roster.require_reviewer_quorum":         "Reviewer quorum",
	"agent_roster.timeout_seconds":                 "Review timeout",
	"inline_comment_limit":                         "Inline comment limit",
}

func policyFieldLabel(path string) string {
	if label, ok := codeReviewPolicyFieldLabels[path]; ok {
		return label
	}
	segments := strings.Split(path, ".")
	label := segments[len(segments)-1]
	label = strings.ReplaceAll(label, "_", " ")
	if label == "" {
		return "Policy"
	}
	return strings.ToUpper(label[:1]) + label[1:]
}

var codeReviewPolicyFieldOrder = []string{
	"enabled", "approval_mode", "automated_approval_policy", "review_instructions",
	"description_policy", "risk_policy", "agent_roster", "inline_comment_limit",
}

func policyFieldSortKey(path string) int {
	for index, prefix := range codeReviewPolicyFieldOrder {
		if path == prefix || strings.HasPrefix(path, prefix+".") {
			return index
		}
	}
	return len(codeReviewPolicyFieldOrder)
}

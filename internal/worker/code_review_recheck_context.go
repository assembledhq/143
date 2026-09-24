package worker

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
)

// Only records linked to the immutable source assessment may fill an out-of-line
// result. The caller applies the context budget after hydration, so a large
// baseline falls back to full review instead of silently dropping its evidence.
func recheckHydrateBaselineResults(baseline models.CodeReviewAssessment, results []models.CodeReviewAgentResult, records []models.CodeReviewPromptRecord) ([]models.CodeReviewAgentResult, error) {
	byKey := make(map[string]models.CodeReviewPromptRecord, len(records))
	for _, record := range records {
		byKey[record.RecordKey] = record
	}
	hydrated := append([]models.CodeReviewAgentResult(nil), results...)
	for i, result := range hydrated {
		if result.OrgID != baseline.OrgID || result.SessionID != baseline.SessionID {
			return nil, fmt.Errorf("baseline result belongs to another assessment target")
		}
		var reference struct {
			RawRecordKey       string `json:"raw_record_key"`
			LegacyRawRecordKey string `json:"raw_artifact_key"`
		}
		if len(result.StructuredResult) > 0 {
			if err := json.Unmarshal(result.StructuredResult, &reference); err != nil {
				return nil, fmt.Errorf("invalid baseline result reference: %w", err)
			}
		}
		key := reference.RawRecordKey
		if key == "" {
			key = reference.LegacyRawRecordKey
		}
		if key == "" {
			if result.RawOutput != nil && strings.Contains(*result.RawOutput, "[truncated: prompt record store unavailable]") {
				return nil, fmt.Errorf("baseline output is incomplete")
			}
			continue
		}
		record, ok := byKey[key]
		var metadata struct {
			ResultID uuid.UUID `json:"result_id"`
		}
		if !ok || record.OrgID != baseline.OrgID || record.SessionID != baseline.SessionID || record.Role != string(result.Role)+"_output" || record.AgentProvider != result.AgentProvider || strings.TrimSpace(record.Content) == "" || json.Unmarshal(record.Metadata, &metadata) != nil || metadata.ResultID != result.ID {
			return nil, fmt.Errorf("baseline output record is missing or does not match its result")
		}
		content := record.Content
		hydrated[i].RawOutput = &content
	}
	return hydrated, nil
}

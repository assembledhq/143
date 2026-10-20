package codereview

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestHistoricalExportEvidencePreservesUncertainty(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, source, policy         string
		result                       models.ReviewSafeguardResult
		inputs, threshold            models.ReviewExportEvidence
		policyAvailability, blocking models.ReviewEvidenceAvailability
	}{
		{name: "absent reasons cannot prove pass", source: `{"acceptable":true,"status":"completed","review_scope":"full","coverage_complete":false}`, policy: `{"version":3,"risk_policy":{"max_additions":750}}`, result: models.ReviewSafeguardUnknown, inputs: unavailable("measured inputs not retained"), threshold: recorded(json.Number("750"), "raw.policy.risk_policy.max_additions; retained setting, not proof of evaluation"), policyAvailability: models.ReviewEvidenceRecorded, blocking: models.ReviewEvidenceUnavailable},
		{name: "explicit failure retains measurement and threshold", source: `{"risk_reason_details":[{"code":"additions_limit_exceeded","actual":900,"limit":750},{"code":"blocking_findings"}]}`, policy: `null`, result: models.ReviewSafeguardFailed, inputs: recorded([]any{map[string]any{"code": "additions_limit_exceeded", "actual": json.Number("900"), "limit": json.Number("750")}}, "raw.source.risk_reason_details; exact retained reason objects including actual/limit/subject when present"), threshold: unavailable("raw.policy.risk_policy.max_additions; retained setting, not proof of evaluation"), policyAvailability: models.ReviewEvidenceUnavailable, blocking: models.ReviewEvidenceRecorded},
		{name: "malformed legacy reason shape remains unknown", source: `{"risk_reason_details":42,"completed_at":"2026-08-01T00:00:00Z"}`, policy: `null`, result: models.ReviewSafeguardUnknown, inputs: unavailable("measured inputs not retained"), threshold: unavailable("raw.policy.risk_policy.max_additions; retained setting, not proof of evaluation"), policyAvailability: models.ReviewEvidenceUnavailable, blocking: models.ReviewEvidenceUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			raw := models.CodeReviewExportSource{Kind: models.ReviewExportAssessment, ID: uuid.New(), Source: json.RawMessage(tt.source), Policy: json.RawMessage(tt.policy)}
			record := historicalExportRecord(raw)
			require.Equal(t, raw, record.Raw, "all original source evidence survives projection")
			var actual models.ReviewExportSafeguard
			for _, g := range record.Safeguards {
				if g.ID == "additions_limit_exceeded" {
					actual = g
				}
			}
			require.Equal(t, tt.result, actual.Result, "only explicit evaluation evidence sets a safeguard result")
			require.Equal(t, tt.inputs, actual.Inputs, "missing measurements are not defaulted to zero")
			require.Equal(t, tt.threshold, actual.Threshold, "only retained versioned thresholds are exported")
			require.Equal(t, tt.policyAvailability, record.Evidence["policy_snapshot"].Availability, "missing policy never falls back to current policy")
			require.Equal(t, tt.blocking, record.Evidence["historical_blocking_findings"].Availability, "absence of blocking reasons does not prove zero historical blockers")
			require.Equal(t, models.ReviewEvidenceUnavailable, record.Evidence["provider_submitted_at"].Availability, "completion is never provider submission time")
			require.Equal(t, models.ReviewEvidenceUnavailable, record.Evidence["head_sha"].Availability, "current PR head is never a substitute for missing assessment head")
			require.Equal(t, models.ReviewEvidenceUnavailable, record.Evidence["pr_closed_at"].Availability, "current updated_at is not a close timestamp")
		})
	}
}
func TestHistoricalExportDescriptionApplicability(t *testing.T) {
	t.Parallel()
	tests := []struct {
		status string
		result models.ReviewSafeguardResult
	}{
		{"satisfied", models.ReviewSafeguardPassed}, {"missing", models.ReviewSafeguardFailed}, {"not_applicable", models.ReviewSafeguardNotEvaluated}, {"unrecognized", models.ReviewSafeguardUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			t.Parallel()
			raw := models.CodeReviewExportSource{Kind: models.ReviewExportAssessment, ID: uuid.New(), Source: json.RawMessage(`{"review_scope":"evidence_only","coverage_complete":true,"structured_outcome":{"synthesis":{"description_assessments":[{"key":"visual","status":"` + tt.status + `","evidence_ids":["screenshot-1"]}]}}}`), Policy: json.RawMessage(`{"description_policy":{"requirements":[{"key":"visual","required":true,"evidence_kind":"visual","applies_when":"ui_changes"}]}}`)}
			record := historicalExportRecord(raw)
			results := map[string]models.ReviewSafeguardResult{}
			for _, g := range record.Safeguards {
				if g.ID == "description:visual" || g.ID == "screenshot" {
					results[g.ID] = g.Result
				}
			}
			require.Equal(t, map[string]models.ReviewSafeguardResult{"description:visual": tt.result, "screenshot": models.ReviewSafeguardUnknown}, results, "requirement status preserves applicability without inventing an aggregate screenshot verdict")
			require.Equal(t, "evidence_only", record.Evidence["substantive_review_completion"].Value.(map[string]any)["review_scope"], "evidence-only coverage remains explicitly distinct from substantive full review")
		})
	}
}

type exportServiceStub struct {
	a        models.CodeReviewExportAcquisition
	rows     []models.CodeReviewExportSource
	acquires int
}

func (s *exportServiceStub) Acquire(context.Context, uuid.UUID, models.CodeReviewExportFilters) (models.CodeReviewExportAcquisition, error) {
	s.acquires++
	return s.a, nil
}
func (s *exportServiceStub) Page(_ context.Context, _ uuid.UUID, c models.CodeReviewExportCursor, limit int) (models.CodeReviewExportAcquisition, []models.CodeReviewExportSource, error) {
	return s.a, s.rows[int(c.Position):min(int(c.Position)+limit, len(s.rows))], nil
}
func TestHistoricalExportPaginationAndScope(t *testing.T) {
	t.Parallel()
	from := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
	filters := models.CodeReviewExportFilters{RepositoryID: uuid.New(), From: from, Until: time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC), TimeBasis: models.ReviewExportCreatedOrCompleted, IncludeUnresolvedPublicationTime: true}
	stub := &exportServiceStub{a: models.CodeReviewExportAcquisition{ID: uuid.New(), SchemaVersion: 1, Filters: filters, Counts: models.CodeReviewExportCounts{Records: 2}}, rows: []models.CodeReviewExportSource{{Kind: models.ReviewExportAssessment, ID: uuid.New(), Source: json.RawMessage(`{}`)}, {Kind: models.ReviewExportLegacy, ID: uuid.New(), Source: json.RawMessage(`{}`)}}}
	service := NewHistoricalExportService(stub)
	org := uuid.New()
	first, err := service.Export(context.Background(), org, filters, "", 1)
	require.NoError(t, err, "acquire first bounded page")
	require.Equal(t, int64(1), first.Meta.Position, "cursor advances by returned source identities")
	require.False(t, first.Meta.Complete, "first page is explicitly incomplete")
	require.NotNil(t, first.Meta.NextCursor, "incomplete export has a continuation cursor")
	second, err := service.Export(context.Background(), org, filters, *first.Meta.NextCursor, 1)
	require.NoError(t, err, "continue acquisition")
	require.Equal(t, models.CodeReviewExportPageMeta{Returned: 1, Position: 2, Complete: true}, second.Meta, "last page has explicit completeness and no next cursor")
	require.Equal(t, 1, stub.acquires, "continuation never reacquires current evidence")
	require.Equal(t, 96*24*time.Hour, filters.Until.Sub(filters.From), "explicit dates retain the full 96 day interval")
	changed := filters
	changed.TimeBasis = models.ReviewExportPublished
	_, err = service.Export(context.Background(), org, changed, *first.Meta.NextCursor, 1)
	require.ErrorIs(t, err, ErrHistoricalExportCursor, "cursor rejects changed time basis")
	raw, marshalErr := json.Marshal(models.CodeReviewExportCursor{AcquisitionID: stub.a.ID, Position: -1})
	require.NoError(t, marshalErr, "encode negative cursor fixture")
	_, err = service.Export(context.Background(), org, filters, base64.RawURLEncoding.EncodeToString(raw), 1)
	require.ErrorIs(t, err, ErrHistoricalExportCursor, "negative cursor cannot page evidence")
}

func TestHistoricalExportOrphanedRequirementEvidence(t *testing.T) {
	t.Parallel()
	raw := models.CodeReviewExportSource{Kind: models.ReviewExportAssessment, ID: uuid.New(), Source: json.RawMessage(`{"structured_outcome":{"description_assessments":[{"key":"retained-without-policy","status":"not_applicable"}]}}`), Policy: json.RawMessage(`null`)}
	record := historicalExportRecord(raw)
	var actual []models.ReviewSafeguardResult
	for _, g := range record.Safeguards {
		if g.ID == "description:retained-without-policy" {
			actual = append(actual, g.Result)
			require.Equal(t, models.ReviewEvidenceUnavailable, g.Threshold.Availability, "lost policy snapshot remains unknown even with retained requirement evaluation")
		}
	}
	require.Equal(t, []models.ReviewSafeguardResult{models.ReviewSafeguardNotEvaluated}, actual, "requirement evidence survives missing policy snapshots")
}

func TestHistoricalExportProviderIDPrecision(t *testing.T) {
	t.Parallel()
	raw := models.CodeReviewExportSource{Kind: models.ReviewExportLegacy, ID: uuid.New(), Source: json.RawMessage(`{"github_review_id":9007199254740993}`)}
	record := historicalExportRecord(raw)
	require.Equal(t, json.Number("9007199254740993"), record.Evidence["github_review_id"].Value, "provider identifiers never round through float64")
}

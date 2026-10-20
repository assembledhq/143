package models

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCodeReviewExportEnums(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		validate func() error
		valid    bool
	}{
		{"recorded", ReviewEvidenceRecorded.Validate, true}, {"reconstructed", ReviewEvidenceReconstructed.Validate, true}, {"unavailable", ReviewEvidenceUnavailable.Validate, true}, {"bad availability", ReviewEvidenceAvailability("guessed").Validate, false},
		{"passed", ReviewSafeguardPassed.Validate, true}, {"failed", ReviewSafeguardFailed.Validate, true}, {"not evaluated", ReviewSafeguardNotEvaluated.Validate, true}, {"unknown", ReviewSafeguardUnknown.Validate, true}, {"bad result", ReviewSafeguardResult("probably_passed").Validate, false},
		{"created", ReviewExportCreated.Validate, true}, {"completed", ReviewExportCompleted.Validate, true}, {"union", ReviewExportCreatedOrCompleted.Validate, true}, {"published", ReviewExportPublished.Validate, true}, {"bad basis", ReviewExportTimeBasis("updated").Validate, false},
		{"frozen consistency", ReviewExportFrozenPayloads.Validate, true}, {"unknown consistency", ReviewExportConsistency("live").Validate, false},
		{"assessment", ReviewExportAssessment.Validate, true}, {"legacy", ReviewExportLegacy.Validate, true}, {"bad source", ReviewExportSourceKind("session").Validate, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if tt.valid {
				require.NoError(t, tt.validate(), "documented enum is valid")
			} else {
				require.Error(t, tt.validate(), "unrecognized enum is rejected")
			}
		})
	}
}
func TestCodeReviewExportFilterBounds(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		days       int
		repository bool
		unresolved bool
		valid      bool
	}{
		{"explicit cohort", 96, true, true, true}, {"maximum window", 366, true, true, true}, {"oversized window", 367, true, true, false}, {"empty window", 0, true, true, false}, {"missing repository", 96, false, true, false}, {"cannot drop unresolved", 96, true, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := CodeReviewExportFilters{From: time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC), TimeBasis: ReviewExportCreated, IncludeUnresolvedPublicationTime: tt.unresolved}
			f.Until = f.From.Add(time.Duration(tt.days) * 24 * time.Hour)
			if tt.repository {
				f.RepositoryID = uuid.New()
			}
			if tt.valid {
				require.NoError(t, f.Validate(), "valid bounded acquisition is accepted")
			} else {
				require.Error(t, f.Validate(), "invalid acquisition cannot start")
			}
		})
	}
}

func TestCodeReviewExportPublicationBinding(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, commit, key, time, created string
		id                               int64
		conflicting                      bool
		expected                         *time.Time
	}{
		{name: "exact provider submission", commit: "head", key: "output", time: "2026-08-01T12:00:00Z", id: 101, expected: exportTime("2026-08-01T12:00:00Z")},
		{name: "different head", commit: "other", key: "output", time: "2026-08-01T12:00:00Z", id: 101},
		{name: "different output on same head", commit: "head", key: "other-output", time: "2026-08-01T12:00:00Z", id: 101},
		{name: "different provider review", commit: "head", key: "output", time: "2026-08-01T12:00:00Z", id: 102},
		{name: "old provider object updated after new assessment", commit: "head", key: "output", time: "2026-07-31T12:00:00Z", id: 101},
		{name: "invalid retained time", commit: "head", key: "output", time: "unknown", id: 101},
		{name: "after acquisition", commit: "head", key: "output", time: "2026-10-21T00:00:00Z", id: 101},
		{name: "conflicting retained timestamps", commit: "head", key: "output", time: "2026-08-01T12:00:00Z", id: 101, conflicting: true},
		{name: "missing source lifecycle", commit: "head", key: "output", time: "2026-08-01T12:00:00Z", id: 101, created: "missing"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			digest := sha256.Sum256([]byte(tt.key))
			marker := fmt.Sprintf("<!-- 143-code-review-output:%x -->", digest[:])
			created := "2026-08-01T00:00:00Z"
			if tt.created != "" {
				created = tt.created
			}
			source, err := json.Marshal(map[string]any{"head_sha": "head", "github_review_id": 101, "publication_key": "output", "created_at": created})
			require.NoError(t, err, "encode synthetic source identity")
			events := []map[string]any{{"review_id": tt.id, "commit_sha": tt.commit, "body": marker, "submitted_at": tt.time}}
			if tt.conflicting {
				events = append(events, map[string]any{"review_id": tt.id, "commit_sha": tt.commit, "body": marker, "submitted_at": "2026-08-02T12:00:00Z"})
			}
			observations, err := json.Marshal(events)
			require.NoError(t, err, "encode retained provider observations")
			raw := CodeReviewExportSource{Kind: ReviewExportAssessment, ID: uuid.New(), Source: source, ProviderObservations: observations}
			got := ResolveCodeReviewExportPublicationTime(raw, *exportTime("2026-10-20T00:00:00Z"))
			require.Equal(t, tt.expected, got.PublicationTime, "only unambiguous submission evidence binds to this assessment")
			require.Equal(t, json.RawMessage(source), got.Source, "binding never rewrites original source evidence")
			require.Equal(t, json.RawMessage(observations), got.ProviderObservations, "unmatched and conflicting provider observations remain available for audit")
		})
	}
}
func exportTime(value string) *time.Time {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		panic(err)
	}
	return &parsed
}

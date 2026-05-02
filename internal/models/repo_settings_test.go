package models

import (
	"encoding/json"
	"testing"
)

func TestParseRepoSettings_Empty(t *testing.T) {
	t.Parallel()
	s, err := ParseRepoSettings(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.PM != nil {
		t.Fatal("expected nil PM for empty settings")
	}
}

func TestParseRepoSettings_WithPM(t *testing.T) {
	t.Parallel()
	raw := json.RawMessage(`{"pm":{"pm_schedule_hours":2,"pm_model":"claude-opus-4-7"}}`)
	s, err := ParseRepoSettings(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.PM == nil {
		t.Fatal("expected PM to be parsed")
	}
	if *s.PM.PMScheduleHours != 2 {
		t.Fatalf("expected pm_schedule_hours=2, got %d", *s.PM.PMScheduleHours)
	}
	if *s.PM.PMModel != "claude-opus-4-7" {
		t.Fatalf("expected pm_model=claude-opus-4-7, got %s", *s.PM.PMModel)
	}
}

func TestParseRepoSettings_InvalidJSON(t *testing.T) {
	t.Parallel()
	_, err := ParseRepoSettings(json.RawMessage(`{invalid`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestMergeRepoPMSettings_NilPM(t *testing.T) {
	t.Parallel()
	org, err := ParseOrgSettings(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	repo := RepoSettings{}
	merged := MergeRepoPMSettings(org, repo)
	if merged.PMScheduleHours != DefaultPMScheduleHours {
		t.Fatalf("expected default schedule hours, got %d", merged.PMScheduleHours)
	}
}

func TestMergeRepoPMSettings_Overrides(t *testing.T) {
	t.Parallel()
	org, err := ParseOrgSettings(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	hours := 2
	model := "claude-opus-4-7"
	threshold := 50.0
	repo := RepoSettings{
		PM: &RepoPMSettings{
			PMScheduleHours:      &hours,
			PMModel:              &model,
			MinPriorityThreshold: &threshold,
			ProductContext: &ProductContext{
				Philosophy: "move fast",
				Direction:  "payments",
				FocusAreas: []string{"checkout"},
			},
		},
	}
	merged := MergeRepoPMSettings(org, repo)

	if merged.PMScheduleHours != 2 {
		t.Fatalf("expected schedule hours=2, got %d", merged.PMScheduleHours)
	}
	if merged.PMModel != "claude-opus-4-7" {
		t.Fatalf("expected pm_model=claude-opus-4-7, got %s", merged.PMModel)
	}
	if merged.MinPriorityThreshold != 50.0 {
		t.Fatalf("expected threshold=50, got %f", merged.MinPriorityThreshold)
	}
	if merged.ProductContext == nil || merged.ProductContext.Philosophy != "move fast" {
		t.Fatal("expected product context to be overridden")
	}
	// Org defaults should be preserved for non-overridden fields.
	if merged.MaxConcurrentRuns != DefaultMaxConcurrentRuns {
		t.Fatalf("expected default max_concurrent_runs, got %d", merged.MaxConcurrentRuns)
	}
}

func TestMergeRepoPMSettings_PartialOverride(t *testing.T) {
	t.Parallel()
	org, err := ParseOrgSettings(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	hours := 6
	repo := RepoSettings{
		PM: &RepoPMSettings{
			PMScheduleHours: &hours,
		},
	}
	merged := MergeRepoPMSettings(org, repo)

	if merged.PMScheduleHours != 6 {
		t.Fatalf("expected schedule hours=6, got %d", merged.PMScheduleHours)
	}
	// PM model should stay as org default.
	if merged.PMModel != DefaultPMModel {
		t.Fatalf("expected default pm_model, got %s", merged.PMModel)
	}
}

func TestValidateRepoPMSettings_ValidModel(t *testing.T) {
	t.Parallel()
	model := "claude-opus-4-7"
	pm := RepoPMSettings{PMModel: &model}
	if err := ValidateRepoPMSettings(pm); err != nil {
		t.Fatalf("expected valid, got: %v", err)
	}
}

func TestValidateRepoPMSettings_InvalidModel(t *testing.T) {
	t.Parallel()
	model := "nonexistent-model"
	pm := RepoPMSettings{PMModel: &model}
	if err := ValidateRepoPMSettings(pm); err == nil {
		t.Fatal("expected error for invalid model")
	}
}

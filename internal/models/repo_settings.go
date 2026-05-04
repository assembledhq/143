package models

import (
	"encoding/json"
	"fmt"
)

// RepoPMSettings holds per-repository PM agent overrides.
// All fields are pointers — nil means "inherit from org defaults".
type RepoPMSettings struct {
	ProductContext       *ProductContext  `json:"product_context,omitempty"`
	PMScheduleHours      *int             `json:"pm_schedule_hours,omitempty"`
	PMModel              *string          `json:"pm_model,omitempty"`
	PriorityWeights      *PriorityWeights `json:"priority_weights,omitempty"`
	MinPriorityThreshold *float64         `json:"min_priority_threshold,omitempty"`
}

// RepoSettings is the strongly-typed representation of repositories.settings JSONB.
type RepoSettings struct {
	PM *RepoPMSettings `json:"pm,omitempty"`
}

// ParseRepoSettings deserializes a repository's settings JSONB.
func ParseRepoSettings(raw json.RawMessage) (RepoSettings, error) {
	var s RepoSettings
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &s); err != nil {
			return s, fmt.Errorf("unmarshal repo settings: %w", err)
		}
	}
	return s, nil
}

// MergeRepoPMSettings returns a copy of the org settings with any repo-level
// PM overrides applied. Fields that are nil in the repo settings are left
// unchanged (inherited from org).
func MergeRepoPMSettings(org OrgSettings, repo RepoSettings) OrgSettings {
	merged := org
	if repo.PM == nil {
		return merged
	}
	pm := repo.PM
	if pm.PMScheduleHours != nil {
		merged.PMScheduleHours = *pm.PMScheduleHours
	}
	if pm.PMModel != nil {
		merged.PMModel = *pm.PMModel
	}
	if pm.MinPriorityThreshold != nil {
		merged.MinPriorityThreshold = *pm.MinPriorityThreshold
	}
	if pm.PriorityWeights != nil {
		merged.PriorityWeights = *pm.PriorityWeights
	}
	if pm.ProductContext != nil {
		merged.ProductContext = pm.ProductContext
	}
	return merged
}

// ValidateRepoPMSettings validates the model references in repo PM settings.
func ValidateRepoPMSettings(pm RepoPMSettings) error {
	if pm.PMModel != nil {
		if err := ValidatePMModel(*pm.PMModel); err != nil {
			return fmt.Errorf("pm.pm_model: %w", err)
		}
	}
	return nil
}

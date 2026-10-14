package models

import (
	"encoding/json"
	"fmt"
	"strings"
)

type AutomationLabelModelSelection struct {
	Label     string    `json:"label"`
	Model     string    `json:"model"`
	AgentType AgentType `json:"agent_type"`
	Labels    []string  `json:"labels"`
}

func (f AutomationGitHubEventFilters) ValidateLabelModels() error {
	if len(f.LabelModelOverrides) == 0 {
		return nil
	}
	if f.CheckRun == nil {
		return fmt.Errorf("label_model_overrides requires check_run")
	}
	seen := map[string]bool{}
	for label, model := range f.LabelModelOverrides {
		key := strings.ToLower(label)
		if label == "" || strings.TrimSpace(label) != label || seen[key] {
			return fmt.Errorf("label_model_overrides requires distinct nonempty labels without surrounding whitespace")
		}
		seen[key] = true
		if model == "" || strings.TrimSpace(model) != model {
			return fmt.Errorf("label_model_overrides requires nonempty exact model IDs")
		}
		eligible := false
		for _, candidate := range f.Labels {
			if strings.EqualFold(candidate, label) {
				eligible = true
			}
		}
		if !eligible {
			return fmt.Errorf("mapped label %q must be an eligible filter label", label)
		}
	}
	for _, label := range f.Labels {
		if !seen[strings.ToLower(label)] {
			return fmt.Errorf("eligible label %q requires a model mapping", label)
		}
	}
	return nil
}

func (a Automation) ValidateLabelModelOverrides() error {
	var filters AutomationGitHubEventFilters
	if err := json.Unmarshal(a.GitHubEventFilters, &filters); err != nil {
		return err
	}
	if err := filters.Validate(); err != nil {
		return err
	}
	if len(filters.LabelModelOverrides) == 0 {
		return nil
	}
	if a.AgentType == nil {
		return fmt.Errorf("label_model_overrides requires an explicit agent_type")
	}
	for _, model := range filters.LabelModelOverrides {
		if err := ValidateModelForAgentType(AgentType(*a.AgentType), model); err != nil {
			return err
		}
	}
	return nil
}

func (a Automation) SelectLabelModel(filters AutomationGitHubEventFilters, labels []string) (Automation, *AutomationLabelModelSelection, error) {
	if len(filters.LabelModelOverrides) == 0 {
		return a, nil, nil
	}
	if err := a.ValidateLabelModelOverrides(); err != nil {
		return a, nil, err
	}
	var selected *AutomationLabelModelSelection
	for label, model := range filters.LabelModelOverrides {
		matched := false
		for _, current := range labels {
			if strings.EqualFold(label, current) {
				matched = true
			}
		}
		if !matched {
			continue
		}
		if selected != nil {
			return a, nil, fmt.Errorf("multiple model labels match the current PR")
		}
		selected = &AutomationLabelModelSelection{Label: label, Model: model, AgentType: AgentType(*a.AgentType), Labels: append([]string(nil), labels...)}
	}
	if selected == nil {
		return a, nil, fmt.Errorf("no model label matches the current PR")
	}
	a.ModelOverride = &selected.Model
	return a, selected, nil
}

func AutomationWithRunLabelModel(a Automation, run AutomationRun) (Automation, error) {
	if len(run.ConfigSnapshot) == 0 {
		return a, nil
	}
	var snapshot struct {
		Selection *AutomationLabelModelSelection `json:"label_model_selection"`
	}
	if err := json.Unmarshal(run.ConfigSnapshot, &snapshot); err != nil {
		return a, err
	}
	if snapshot.Selection == nil {
		return a, nil
	}
	selected := snapshot.Selection
	if err := ValidateModelForAgentType(selected.AgentType, selected.Model); err != nil {
		return a, err
	}
	found := false
	for _, label := range selected.Labels {
		if strings.EqualFold(label, selected.Label) {
			found = true
		}
	}
	if !found || selected.Label == "" {
		return a, fmt.Errorf("label model snapshot lacks matching label evidence")
	}
	agent := string(selected.AgentType)
	a.AgentType, a.ModelOverride = &agent, &selected.Model
	return a, nil
}

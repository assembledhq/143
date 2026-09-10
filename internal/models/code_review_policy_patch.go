package models

import (
	"bytes"
	"encoding/json"
)

// ApplyCodeReviewPolicyMergePatch applies only supplied fields. Decode strictly
// after merging so unknown fields and fractional durations cannot be discarded.
func ApplyCodeReviewPolicyMergePatch(current CodeReviewPolicyConfig, patch json.RawMessage) (CodeReviewPolicyConfig, error) {
	var changes map[string]any
	if err := json.Unmarshal(patch, &changes); err != nil || changes == nil {
		return CodeReviewPolicyConfig{}, codeReviewPolicyFieldError("config", "policy patch must be an object")
	}
	var shape CodeReviewPolicyConfig
	strict := json.NewDecoder(bytes.NewReader(patch))
	strict.DisallowUnknownFields()
	if err := strict.Decode(&shape); err != nil {
		return CodeReviewPolicyConfig{}, codeReviewPolicyFieldError("config", err.Error())
	}
	raw, err := json.Marshal(current)
	if err != nil {
		return CodeReviewPolicyConfig{}, err
	}
	var base map[string]any
	if err := json.Unmarshal(raw, &base); err != nil {
		return CodeReviewPolicyConfig{}, err
	}
	merged := mergeJSONObjects(base, changes)
	defaultRaw, err := json.Marshal(DefaultCodeReviewPolicyConfig())
	if err != nil {
		return CodeReviewPolicyConfig{}, err
	}
	var defaults map[string]any
	if err := json.Unmarshal(defaultRaw, &defaults); err != nil {
		return CodeReviewPolicyConfig{}, err
	}
	raw, err = json.Marshal(mergeJSONObjects(defaults, merged))
	if err != nil {
		return CodeReviewPolicyConfig{}, err
	}
	var result CodeReviewPolicyConfig
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return result, codeReviewPolicyFieldError("config", err.Error())
	}
	result = ResolveCodeReviewPolicyConfig(&result)
	return result, result.Validate()
}

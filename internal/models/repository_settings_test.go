package models

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseRepositorySettings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		raw      json.RawMessage
		expected RepositorySettings
		wantErr  bool
	}{
		{name: "absent mode defaults to pre publish", raw: json.RawMessage(`{"unrelated":true}`), expected: RepositorySettings{PRHandoffMode: PRHandoffModePrePublish}},
		{name: "draft first is accepted", raw: json.RawMessage(`{"pr_handoff_mode":"draft_first"}`), expected: RepositorySettings{PRHandoffMode: PRHandoffModeDraftFirst}},
		{name: "invalid mode is rejected", raw: json.RawMessage(`{"pr_handoff_mode":"instant"}`), wantErr: true},
		{name: "non object is rejected", raw: json.RawMessage(`[]`), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			actual, err := ParseRepositorySettings(tt.raw)
			if tt.wantErr {
				require.Error(t, err, "repository settings parsing should reject invalid policy input")
				return
			}
			require.NoError(t, err, "repository settings parsing should accept valid policy input")
			require.Equal(t, tt.expected, actual, "repository settings should resolve the expected handoff mode")
		})
	}
}

func TestApplyRepositorySettingsMergePatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		current      json.RawMessage
		patch        json.RawMessage
		expectedJSON map[string]any
		expectedMode PRHandoffMode
		wantErr      bool
	}{
		{
			name:         "handoff update preserves unrelated nested keys",
			current:      json.RawMessage(`{"preview":{"enabled":true},"pr_handoff_mode":"pre_publish"}`),
			patch:        json.RawMessage(`{"pr_handoff_mode":"draft_first"}`),
			expectedJSON: map[string]any{"preview": map[string]any{"enabled": true}, "pr_handoff_mode": "draft_first"},
			expectedMode: PRHandoffModeDraftFirst,
		},
		{
			name:         "null removes override and restores default",
			current:      json.RawMessage(`{"pr_handoff_mode":"draft_first","other":1}`),
			patch:        json.RawMessage(`{"pr_handoff_mode":null}`),
			expectedJSON: map[string]any{"other": float64(1)},
			expectedMode: PRHandoffModePrePublish,
		},
		{name: "legacy PM settings remain rejected", current: json.RawMessage(`{}`), patch: json.RawMessage(`{"pm":null}`), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			merged, settings, err := ApplyRepositorySettingsMergePatch(tt.current, tt.patch)
			if tt.wantErr {
				require.Error(t, err, "repository settings merge should reject removed or invalid settings")
				return
			}
			require.NoError(t, err, "repository settings merge should accept a valid merge patch")
			var actual map[string]any
			require.NoError(t, json.Unmarshal(merged, &actual), "merged repository settings should be valid JSON")
			require.Equal(t, tt.expectedJSON, actual, "merge patch should preserve unrelated repository settings")
			require.Equal(t, tt.expectedMode, settings.PRHandoffMode, "merge patch should resolve the effective handoff mode")
		})
	}
}

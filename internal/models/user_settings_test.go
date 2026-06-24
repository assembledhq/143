package models

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseUserSettings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     json.RawMessage
		want    UserSettings
		wantErr bool
	}{
		{
			name: "empty settings returns zero value",
			raw:  nil,
			want: UserSettings{},
		},
		{
			name: "parses supported agent defaults",
			raw:  json.RawMessage(`{"coding_agent_model_default":"claude-opus-4-7","coding_agent_reasoning_defaults":{"codex":"xhigh","claude_code":"max"}}`),
			want: UserSettings{
				CodingAgentModelDefault: "claude-opus-4-7",
				CodingAgentReasoningDefaults: map[AgentType]ReasoningEffort{
					AgentTypeCodex:      ReasoningEffortXHigh,
					AgentTypeClaudeCode: ReasoningEffortMax,
				},
			},
		},
		{
			name: "parses diff viewer full screen preference",
			raw:  json.RawMessage(`{"diff_viewer_full_screen":true}`),
			want: UserSettings{DiffViewerFullScreen: true},
		},
		{
			name: "parses manual session planes hidden preference",
			raw:  json.RawMessage(`{"manual_session_planes_hidden":true}`),
			want: UserSettings{ManualSessionPlanesHidden: true},
		},
		{
			name:    "rejects malformed json",
			raw:     json.RawMessage(`{"coding_agent_reasoning_defaults":`),
			wantErr: true,
		},
		{
			name:    "rejects unknown top-level fields",
			raw:     json.RawMessage(`{"unknown":true}`),
			wantErr: true,
		},
		{
			name:    "rejects unsupported effort for agent",
			raw:     json.RawMessage(`{"coding_agent_reasoning_defaults":{"codex":"max"}}`),
			wantErr: true,
		},
		{
			name:    "rejects unsupported agent type",
			raw:     json.RawMessage(`{"coding_agent_reasoning_defaults":{"deprecated_agent":"high"}}`),
			wantErr: true,
		},
		{
			name:    "rejects unsupported default model",
			raw:     json.RawMessage(`{"coding_agent_model_default":"not-a-model"}`),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParseUserSettings(tt.raw)
			if tt.wantErr {
				require.Error(t, err, "ParseUserSettings should reject invalid settings")
				return
			}
			require.NoError(t, err, "ParseUserSettings should accept valid settings")
			require.Equal(t, tt.want, got, "ParseUserSettings should return the expected settings")
		})
	}
}

func TestApplyUserSettingsMergePatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		current json.RawMessage
		patch   json.RawMessage
		want    UserSettings
		wantErr string
	}{
		{
			name:    "sets a field without touching the others",
			current: json.RawMessage(`{"coding_agent_model_default":"claude-opus-4-7","coding_agent_reasoning_defaults":{"codex":"xhigh"}}`),
			patch:   json.RawMessage(`{"manual_session_planes_hidden":true}`),
			want: UserSettings{
				CodingAgentModelDefault: "claude-opus-4-7",
				CodingAgentReasoningDefaults: map[AgentType]ReasoningEffort{
					AgentTypeCodex: ReasoningEffortXHigh,
				},
				ManualSessionPlanesHidden: true,
			},
		},
		{
			name:    "merges nested reasoning defaults per agent",
			current: json.RawMessage(`{"coding_agent_reasoning_defaults":{"codex":"xhigh"}}`),
			patch:   json.RawMessage(`{"coding_agent_reasoning_defaults":{"claude_code":"max"}}`),
			want: UserSettings{
				CodingAgentReasoningDefaults: map[AgentType]ReasoningEffort{
					AgentTypeCodex:      ReasoningEffortXHigh,
					AgentTypeClaudeCode: ReasoningEffortMax,
				},
			},
		},
		{
			name:    "null clears a top-level field",
			current: json.RawMessage(`{"coding_agent_model_default":"claude-opus-4-7","diff_viewer_full_screen":true}`),
			patch:   json.RawMessage(`{"coding_agent_model_default":null}`),
			want:    UserSettings{DiffViewerFullScreen: true},
		},
		{
			name:    "null clears a nested reasoning default",
			current: json.RawMessage(`{"coding_agent_reasoning_defaults":{"codex":"xhigh","claude_code":"max"}}`),
			patch:   json.RawMessage(`{"coding_agent_reasoning_defaults":{"codex":null}}`),
			want: UserSettings{
				CodingAgentReasoningDefaults: map[AgentType]ReasoningEffort{
					AgentTypeClaudeCode: ReasoningEffortMax,
				},
			},
		},
		{
			name:  "applies to an empty current document",
			patch: json.RawMessage(`{"diff_viewer_full_screen":true}`),
			want:  UserSettings{DiffViewerFullScreen: true},
		},
		{
			name:    "empty patch is a no-op",
			current: json.RawMessage(`{"diff_viewer_full_screen":true}`),
			patch:   json.RawMessage(`{}`),
			want:    UserSettings{DiffViewerFullScreen: true},
		},
		{
			name:    "rejects non-object patch",
			patch:   json.RawMessage(`"settings"`),
			wantErr: "must be a JSON object",
		},
		{
			name:    "rejects null patch",
			patch:   json.RawMessage(`null`),
			wantErr: "must be a JSON object",
		},
		{
			name:    "rejects unknown patch field",
			patch:   json.RawMessage(`{"unknown":null}`),
			wantErr: `unknown settings field "unknown"`,
		},
		{
			name:    "rejects invalid merged value",
			current: json.RawMessage(`{"coding_agent_reasoning_defaults":{"claude_code":"max"}}`),
			patch:   json.RawMessage(`{"coding_agent_reasoning_defaults":{"codex":"max"}}`),
			wantErr: "is not supported",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ApplyUserSettingsMergePatch(tt.current, tt.patch)
			if tt.wantErr != "" {
				require.Error(t, err, "ApplyUserSettingsMergePatch should reject the patch")
				require.Contains(t, err.Error(), tt.wantErr, "ApplyUserSettingsMergePatch should explain the rejection")
				return
			}
			require.NoError(t, err, "ApplyUserSettingsMergePatch should accept the patch")
			require.Equal(t, tt.want, got, "ApplyUserSettingsMergePatch should return the merged settings")
		})
	}
}

func TestUserSettings_MarshalJSONB(t *testing.T) {
	t.Parallel()

	t.Run("marshals valid settings", func(t *testing.T) {
		t.Parallel()

		raw, err := (UserSettings{
			CodingAgentModelDefault: "claude-opus-4-7",
			CodingAgentReasoningDefaults: map[AgentType]ReasoningEffort{
				AgentTypeClaudeCode: ReasoningEffortMax,
			},
		}).MarshalJSONB()
		require.NoError(t, err, "MarshalJSONB should accept valid settings")
		require.JSONEq(t, `{"coding_agent_model_default":"claude-opus-4-7","coding_agent_reasoning_defaults":{"claude_code":"max"}}`, string(raw), "MarshalJSONB should encode the settings document")
	})

	t.Run("omits diff viewer full screen when false", func(t *testing.T) {
		t.Parallel()

		raw, err := (UserSettings{DiffViewerFullScreen: false}).MarshalJSONB()
		require.NoError(t, err, "MarshalJSONB should accept zero-value settings")
		require.JSONEq(t, `{}`, string(raw), "MarshalJSONB should omit the default full screen preference")

		raw, err = (UserSettings{DiffViewerFullScreen: true}).MarshalJSONB()
		require.NoError(t, err, "MarshalJSONB should accept full screen preference")
		require.JSONEq(t, `{"diff_viewer_full_screen":true}`, string(raw), "MarshalJSONB should encode the full screen preference")
	})

	t.Run("omits manual session planes hidden when false", func(t *testing.T) {
		t.Parallel()

		raw, err := (UserSettings{ManualSessionPlanesHidden: false}).MarshalJSONB()
		require.NoError(t, err, "MarshalJSONB should accept zero-value settings")
		require.JSONEq(t, `{}`, string(raw), "MarshalJSONB should omit the default planes preference")

		raw, err = (UserSettings{ManualSessionPlanesHidden: true}).MarshalJSONB()
		require.NoError(t, err, "MarshalJSONB should accept planes preference")
		require.JSONEq(t, `{"manual_session_planes_hidden":true}`, string(raw), "MarshalJSONB should encode the planes preference")
	})

	t.Run("rejects invalid settings", func(t *testing.T) {
		t.Parallel()

		_, err := (UserSettings{
			CodingAgentReasoningDefaults: map[AgentType]ReasoningEffort{
				AgentTypeCodex: "",
			},
		}).MarshalJSONB()
		require.Error(t, err, "MarshalJSONB should reject invalid settings")
		require.Contains(t, err.Error(), "reasoning effort cannot be empty", "MarshalJSONB should return validation failures")
	})
}

func TestUserSettings_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		settings UserSettings
		wantErr  string
	}{
		{
			name: "rejects invalid agent type",
			settings: UserSettings{
				CodingAgentReasoningDefaults: map[AgentType]ReasoningEffort{
					AgentType("bogus"): ReasoningEffortHigh,
				},
			},
			wantErr: "invalid agent type",
		},
		{
			name: "rejects invalid default model",
			settings: UserSettings{
				CodingAgentModelDefault: "not-a-model",
			},
			wantErr: "unknown model",
		},
		{
			name: "rejects invalid effort enum",
			settings: UserSettings{
				CodingAgentReasoningDefaults: map[AgentType]ReasoningEffort{
					AgentTypeCodex: "turbo",
				},
			},
			wantErr: "invalid reasoning effort",
		},
		{
			name: "rejects empty effort",
			settings: UserSettings{
				CodingAgentReasoningDefaults: map[AgentType]ReasoningEffort{
					AgentTypeClaudeCode: "",
				},
			},
			wantErr: "reasoning effort cannot be empty",
		},
		{
			name: "rejects unsupported effort for agent",
			settings: UserSettings{
				CodingAgentReasoningDefaults: map[AgentType]ReasoningEffort{
					AgentTypeCodex: ReasoningEffortMax,
				},
			},
			wantErr: "is not supported",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.settings.Validate()
			require.Error(t, err, "Validate should reject invalid user settings")
			require.Contains(t, err.Error(), tt.wantErr, "Validate should explain the invalid setting")
		})
	}
}

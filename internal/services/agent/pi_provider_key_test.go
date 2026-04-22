package agent

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

// TestCheckPiProviderKey covers each arm of the switch directly so a new
// provider prefix added upstream doesn't silently regress the end-to-end
// orchestrator tests, which only exercise one branch each.
func TestCheckPiProviderKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		env         map[string]string
		wantErr     bool
		wantErrSubs []string
	}{
		{
			name: "anthropic model with anthropic key passes",
			env: map[string]string{
				"PI_MODEL":          models.PiModelClaudeSonnet46,
				"ANTHROPIC_API_KEY": "sk-ant",
			},
		},
		{
			name: "anthropic model without anthropic key fails",
			env: map[string]string{
				"PI_MODEL":       models.PiModelClaudeSonnet46,
				"OPENAI_API_KEY": "sk-openai", // present but irrelevant
			},
			wantErr:     true,
			wantErrSubs: []string{"ANTHROPIC_API_KEY", models.PiModelClaudeSonnet46},
		},
		{
			name: "openai model with openai key passes",
			env: map[string]string{
				"PI_MODEL":       models.PiModelGPT54,
				"OPENAI_API_KEY": "sk-openai",
			},
		},
		{
			name: "openai model without openai key fails",
			env: map[string]string{
				"PI_MODEL":          models.PiModelGPT54,
				"ANTHROPIC_API_KEY": "sk-ant",
			},
			wantErr:     true,
			wantErrSubs: []string{"OPENAI_API_KEY", models.PiModelGPT54},
		},
		{
			name: "google prefix with gemini key passes",
			env: map[string]string{
				"PI_MODEL_CUSTOM": "google/gemini-2.5-pro",
				"GEMINI_API_KEY":  "gem",
			},
		},
		{
			name: "google prefix without gemini key fails",
			env: map[string]string{
				"PI_MODEL_CUSTOM": "google/gemini-2.5-pro",
				"OPENAI_API_KEY":  "sk-openai",
			},
			wantErr:     true,
			wantErrSubs: []string{"GEMINI_API_KEY", "google/gemini-2.5-pro"},
		},
		{
			name: "gemini prefix alias also looks at GEMINI_API_KEY",
			env: map[string]string{
				"PI_MODEL_CUSTOM": "gemini/experimental",
			},
			wantErr:     true,
			wantErrSubs: []string{"GEMINI_API_KEY", "gemini/experimental"},
		},
		{
			name: "PI_MODEL_CUSTOM wins over PI_MODEL",
			env: map[string]string{
				"PI_MODEL":          models.PiModelClaudeSonnet46,
				"PI_MODEL_CUSTOM":   models.PiModelGPT54,
				"ANTHROPIC_API_KEY": "sk-ant",
			},
			wantErr:     true,
			wantErrSubs: []string{"OPENAI_API_KEY", models.PiModelGPT54},
		},
		{
			name: "unknown prefix with at least one inherited key passes",
			env: map[string]string{
				"PI_MODEL_CUSTOM":   "moonshot/kimi-k2",
				"ANTHROPIC_API_KEY": "sk-ant", // any one is enough
			},
		},
		{
			name: "unknown prefix with no inherited keys fails with Pi-scoped error",
			env: map[string]string{
				"PI_MODEL_CUSTOM": "moonshot/kimi-k2",
			},
			wantErr:     true,
			wantErrSubs: []string{"Pi"},
		},
		{
			name:        "empty model falls back to default, needs anthropic key",
			env:         map[string]string{},
			wantErr:     true,
			wantErrSubs: []string{"ANTHROPIC_API_KEY", models.PiModelClaudeOpus47},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := checkPiProviderKey(models.AgentTypePi, tc.env)
			if !tc.wantErr {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			for _, sub := range tc.wantErrSubs {
				require.Contains(t, err.Error(), sub)
			}
		})
	}
}

// TestNarrowPiProviderKeys verifies the narrowing strips sibling provider keys
// for known prefixes and is a no-op for unknown prefixes (so moonshot/etc.
// routes continue to get every inherited key as a best-effort).
func TestNarrowPiProviderKeys(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		env               map[string]string
		want              map[string]string
		absent            []string
		wantUnknownPrefix string
	}{
		{
			name: "anthropic model keeps only ANTHROPIC_API_KEY",
			env: map[string]string{
				"PI_MODEL":          models.PiModelClaudeSonnet46,
				"ANTHROPIC_API_KEY": "sk-ant",
				"OPENAI_API_KEY":    "sk-openai",
				"GEMINI_API_KEY":    "gem",
			},
			want: map[string]string{
				"PI_MODEL":          models.PiModelClaudeSonnet46,
				"ANTHROPIC_API_KEY": "sk-ant",
			},
			absent: []string{"OPENAI_API_KEY", "GEMINI_API_KEY"},
		},
		{
			name: "openai model keeps only OPENAI_API_KEY",
			env: map[string]string{
				"PI_MODEL":          models.PiModelGPT54,
				"ANTHROPIC_API_KEY": "sk-ant",
				"OPENAI_API_KEY":    "sk-openai",
				"GEMINI_API_KEY":    "gem",
			},
			want: map[string]string{
				"PI_MODEL":       models.PiModelGPT54,
				"OPENAI_API_KEY": "sk-openai",
			},
			absent: []string{"ANTHROPIC_API_KEY", "GEMINI_API_KEY"},
		},
		{
			name: "google prefix keeps only GEMINI_API_KEY",
			env: map[string]string{
				"PI_MODEL_CUSTOM":   "google/gemini-2.5-pro",
				"ANTHROPIC_API_KEY": "sk-ant",
				"OPENAI_API_KEY":    "sk-openai",
				"GEMINI_API_KEY":    "gem",
			},
			want: map[string]string{
				"PI_MODEL_CUSTOM": "google/gemini-2.5-pro",
				"GEMINI_API_KEY":  "gem",
			},
			absent: []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY"},
		},
		{
			name: "PI_MODEL_CUSTOM wins over PI_MODEL for narrowing",
			env: map[string]string{
				"PI_MODEL":          models.PiModelClaudeSonnet46,
				"PI_MODEL_CUSTOM":   models.PiModelGPT54,
				"ANTHROPIC_API_KEY": "sk-ant",
				"OPENAI_API_KEY":    "sk-openai",
			},
			want: map[string]string{
				"PI_MODEL":        models.PiModelClaudeSonnet46,
				"PI_MODEL_CUSTOM": models.PiModelGPT54,
				"OPENAI_API_KEY":  "sk-openai",
			},
			absent: []string{"ANTHROPIC_API_KEY"},
		},
		{
			name: "unknown prefix keeps all inherited keys and reports prefix",
			env: map[string]string{
				"PI_MODEL_CUSTOM":   "moonshot/kimi-k2",
				"ANTHROPIC_API_KEY": "sk-ant",
				"OPENAI_API_KEY":    "sk-openai",
				"GEMINI_API_KEY":    "gem",
			},
			want: map[string]string{
				"PI_MODEL_CUSTOM":   "moonshot/kimi-k2",
				"ANTHROPIC_API_KEY": "sk-ant",
				"OPENAI_API_KEY":    "sk-openai",
				"GEMINI_API_KEY":    "gem",
			},
			wantUnknownPrefix: "moonshot",
		},
		{
			name: "empty env falls back to default prefix (anthropic)",
			env: map[string]string{
				"ANTHROPIC_API_KEY": "sk-ant",
				"OPENAI_API_KEY":    "sk-openai",
				"GEMINI_API_KEY":    "gem",
			},
			want: map[string]string{
				"ANTHROPIC_API_KEY": "sk-ant",
			},
			absent: []string{"OPENAI_API_KEY", "GEMINI_API_KEY"},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotUnknownPrefix := narrowPiProviderKeys(tc.env)
			require.Equal(t, tc.wantUnknownPrefix, gotUnknownPrefix, "unknown-prefix return value")
			for k, v := range tc.want {
				require.Equal(t, v, tc.env[k], "key %s should be kept with expected value", k)
			}
			for _, k := range tc.absent {
				_, present := tc.env[k]
				require.False(t, present, "key %s should have been removed", k)
			}
		})
	}
}

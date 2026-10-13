package repoconfig

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/stretchr/testify/require"
)

func TestParseReviewSection(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, config string
		expected     *ReviewSection
		bootstrap    []string
	}{
		{name: "absent retains bootstrap", config: `{"bootstrap":{"commands":["npm ci"]}}`, bootstrap: []string{"npm ci"}},
		{name: "empty review explicitly replaces bootstrap", config: `{"bootstrap":{"commands":["npm ci"]},"review":{}}`, expected: &ReviewSection{}, bootstrap: []string{"npm ci"}},
		{name: "review source declarations normalize", config: `{"review":{"commands":[" setup "],"verify_commands":[" check "],"evidence":[{"path":" .143/cache/go/pkg/mod ","description":" Pinned Go source ","category":"go_dependencies"}]}}`, expected: &ReviewSection{Commands: []string{"setup"}, VerifyCommands: []string{"check"}, Evidence: []ReviewEvidenceEntry{{Path: ".143/cache/go/pkg/mod", Description: "Pinned Go source", Category: models.CodeReviewEvidenceGoDependencies}}}},
		{name: "Ruby source category", config: `{"review":{"evidence":[{"path":".143/cache/ruby/gems","description":"Ruby source","category":"ruby_dependencies"}]}}`, expected: &ReviewSection{Evidence: []ReviewEvidenceEntry{{Path: ".143/cache/ruby/gems", Description: "Ruby source", Category: models.CodeReviewEvidenceRubyDependencies}}}},
		{name: "SDK credential-shaped fixtures remain data", config: `{"review":{"evidence":[{"path":".143/cache/sdk/test/.npmrc","description":"SDK fixture","category":"generated_artifacts"}]}}`, expected: &ReviewSection{Evidence: []ReviewEvidenceEntry{{Path: ".143/cache/sdk/test/.npmrc", Description: "SDK fixture", Category: models.CodeReviewEvidenceGeneratedArtifacts}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := Parse([]byte(tt.config))
			require.NoError(t, err, "review configuration should accept explicit source contracts without changing coding bootstrap")
			require.Equal(t, tt.expected, cfg.Review, "review configuration should preserve normalized bounded source declarations")
			require.Equal(t, tt.bootstrap, cfg.Bootstrap.Commands, "review-only configuration must not overwrite ordinary bootstrap commands")
		})
	}
}

func TestParseRejectsInvalidReviewSchema(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, config, expected string }{
		{name: "null review", config: `{"review":null}`, expected: "review must be an object"},
		{name: "array review", config: `{"review":[]}`, expected: "review must be an object"},
		{name: "unknown review field", config: `{"review":{"bootstrap":[]}}`, expected: `unknown field "bootstrap"`},
		{name: "unknown evidence field", config: `{"review":{"evidence":[{"path":"cache","description":"source","category":"go_dependencies","command":"unsafe"}]}}`, expected: `unknown field "command"`},
		{name: "null commands", config: `{"review":{"commands":null}}`, expected: "review.commands must be an array"},
		{name: "nonarray verifiers", config: `{"review":{"verify_commands":"check"}}`, expected: "review.verify_commands must be an array"},
		{name: "null evidence", config: `{"review":{"evidence":null}}`, expected: "review.evidence must be an array"},
		{name: "blank verifier", config: `{"review":{"verify_commands":[" "]}}`, expected: "review.verify_commands[0]"},
		{name: "missing description", config: `{"review":{"evidence":[{"path":"cache","category":"go_dependencies"}]}}`, expected: "description"},
		{name: "missing category", config: `{"review":{"evidence":[{"path":"cache","description":"source"}]}}`, expected: "category"},
		{name: "unknown category", config: `{"review":{"evidence":[{"path":"cache","description":"source","category":"all_languages"}]}}`, expected: "category"},
		{name: "duplicate source roots", config: `{"review":{"evidence":[{"path":"cache","description":"source","category":"go_dependencies"},{"path":"cache","description":"source","category":"python_dependencies"}]}}`, expected: "path is duplicated"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := Parse([]byte(tt.config))
			require.Error(t, err, "invalid review schema must fail before repository setup runs")
			require.Contains(t, err.Error(), tt.expected, "invalid review schema should identify its bounded field contract")
		})
	}
}

func TestReviewEvidencePathsRejectEscapesAndWorkspaceCredentials(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, path string }{
		{name: "workspace root", path: "."}, {name: "absolute", path: "/tmp/sources"}, {name: "parent", path: "../sources"},
		{name: "internal parent", path: "cache/../sources"}, {name: "noncanonical", path: "cache//sources"},
		{name: "Git metadata", path: "cache/.git/config"}, {name: "Windows drive", path: "C:/sources"}, {name: "backslash", path: `cache\sources`},
		{name: "root env", path: ".env.production"}, {name: "root npm credentials", path: ".npmrc"}, {name: "root ssh", path: ".ssh/config"},
		{name: "root GitHub CLI credentials", path: ".config/gh/hosts.yml"}, {name: "control bytes", path: "cache\nsource"},
		{name: "overlong", path: strings.Repeat("a", MaxReviewEvidencePathBytes+1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Error(t, ValidateReviewEvidencePath(tt.path), "evidence source roots must remain inside safe repository paths")
		})
	}
}

func TestReviewSectionBounds(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		review   any
		expected string
	}{
		{name: "too many setup commands", review: map[string]any{"commands": make([]string, MaxReviewCommands+1)}, expected: "exceeds 32 commands"},
		{name: "too many verifiers", review: map[string]any{"verify_commands": make([]string, MaxReviewCommands+1)}, expected: "exceeds 32 commands"},
		{name: "overlong command", review: map[string]any{"commands": []string{strings.Repeat("a", MaxReviewCommandBytes+1)}}, expected: "review.commands[0]"},
		{name: "NUL command", review: map[string]any{"commands": []string{"read\x00file"}}, expected: "without NUL"},
		{name: "too many roots", review: map[string]any{"evidence": make([]ReviewEvidenceEntry, MaxReviewEvidenceEntries+1)}, expected: "exceeds 32 entries"},
		{name: "overlong description", review: map[string]any{"evidence": []ReviewEvidenceEntry{{Path: "cache", Description: strings.Repeat("a", MaxReviewEvidenceDescriptionBytes+1), Category: models.CodeReviewEvidenceGoDependencies}}}, expected: "description"},
		{name: "oversize review section", review: map[string]any{"commands": []string{strings.Repeat("a", MaxReviewSectionBytes)}}, expected: "at most 65536 bytes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			data, err := json.Marshal(map[string]any{"review": tt.review})
			require.NoError(t, err, "isolated review-bound fixture should serialize")
			_, err = Parse(data)
			require.Error(t, err, "unbounded review declarations must fail before setup")
			require.Contains(t, err.Error(), tt.expected, "review limits should identify the rejected field")
		})
	}
}

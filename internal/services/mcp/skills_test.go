package mcp

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/services/integration"
)

func TestGenerateSkillsDoc_WithIntegrations(t *testing.T) {
	t.Parallel()

	tr := NewToolRegistry(buildTestRegistry())
	doc := GenerateSkillsDoc(tr)

	if doc == "" {
		t.Fatal("expected non-empty skills doc")
	}

	// Check structure.
	if !strings.Contains(doc, "# Integration Tools") {
		t.Error("missing header")
	}
	if !strings.Contains(doc, "143-tools") {
		t.Error("missing CLI name")
	}
	if !strings.Contains(doc, "## Quick Reference") {
		t.Error("missing quick reference section")
	}

	// Check that tools are listed.
	if !strings.Contains(doc, "sentry_list_errors") {
		t.Error("missing sentry tool")
	}
	if !strings.Contains(doc, "linear_create_task") {
		t.Error("missing linear tool")
	}

	// Check examples include provider sections.
	if !strings.Contains(doc, "## Sentry") {
		t.Error("missing Sentry section header")
	}
	if !strings.Contains(doc, "## Linear") {
		t.Error("missing Linear section header")
	}

	// Check tips section.
	if !strings.Contains(doc, "## Tips") {
		t.Error("missing tips section")
	}
	if !strings.Contains(doc, "jq") {
		t.Error("missing jq tip")
	}
}

func TestGenerateSkillsDoc_Empty(t *testing.T) {
	t.Parallel()

	tr := NewToolRegistry(integration.NewRegistry())
	doc := GenerateSkillsDoc(tr)

	if doc != "" {
		t.Errorf("expected empty string for no integrations, got: %s", doc)
	}
}

func TestGenerateSkillsDoc_TokenEfficiency(t *testing.T) {
	t.Parallel()

	tr := NewToolRegistry(buildTestRegistry())
	doc := GenerateSkillsDoc(tr)

	// The skills doc should be compact. With 2 integrations (9 tools),
	// we expect roughly 400-1000 words (~500-1200 tokens).
	words := len(strings.Fields(doc))
	if words > 1200 {
		t.Errorf("skills doc is too verbose: %d words (keep under 1200 for token efficiency)", words)
	}
	if words < 50 {
		t.Errorf("skills doc is suspiciously short: %d words", words)
	}
}

func TestGenerateSkillsDoc_ExamplesIncludeFlags(t *testing.T) {
	t.Parallel()

	tr := NewToolRegistry(buildTestRegistry())
	doc := GenerateSkillsDoc(tr)

	// Examples should include common flags.
	if !strings.Contains(doc, "--severity") {
		t.Error("examples missing --severity flag")
	}
	if !strings.Contains(doc, "--error-id") {
		t.Error("examples missing --error-id flag")
	}
	if !strings.Contains(doc, "--team-key") {
		t.Error("examples missing --team-key flag for create_task")
	}
}

func TestGenerateSkillsDoc_SessionTabsIncludeCoordinationGuidanceAndKebabFlags(t *testing.T) {
	t.Parallel()

	tr := NewToolRegistry(buildFullTestRegistry())
	doc := GenerateSkillsDoc(tr)

	require.Contains(t, doc, "Use a new tab for parallel review/testing/investigation in the same branch.", "session tab tools should teach when to create sibling tabs")
	require.Contains(t, doc, "Use a new session only when work needs an independent branch or PR.", "session tab tools should distinguish tabs from sessions")
	require.Contains(t, doc, "--tab-id", "session tab examples should prefer kebab-case flags")
	require.NotContains(t, doc, "--tab_id", "session tab examples should not expose snake_case flags")
}

func TestGenerateSkillsDoc_SentryOnly(t *testing.T) {
	t.Parallel()

	reg := integration.NewRegistry()
	reg.RegisterErrorTracker(&mockErrorTracker{name: "sentry"})
	tr := NewToolRegistry(reg)
	doc := GenerateSkillsDoc(tr)

	if doc == "" {
		t.Fatal("expected non-empty skills doc with sentry only")
	}
	if !strings.Contains(doc, "## Sentry") {
		t.Error("missing Sentry section")
	}
	if strings.Contains(doc, "## Linear") {
		t.Error("Linear section should not appear when only Sentry is configured")
	}
	if !strings.Contains(doc, "sentry_list_errors") {
		t.Error("missing sentry tools")
	}
	if strings.Contains(doc, "linear_") {
		t.Error("linear tools should not appear when only Sentry is configured")
	}
}

func TestGenerateSkillsDoc_LinearOnly(t *testing.T) {
	t.Parallel()

	reg := integration.NewRegistry()
	reg.RegisterTaskManager(&mockTaskManager{name: "linear"})
	tr := NewToolRegistry(reg)
	doc := GenerateSkillsDoc(tr)

	if doc == "" {
		t.Fatal("expected non-empty skills doc with linear only")
	}
	if !strings.Contains(doc, "## Linear") {
		t.Error("missing Linear section")
	}
	if strings.Contains(doc, "## Sentry") {
		t.Error("Sentry section should not appear when only Linear is configured")
	}
	if strings.Contains(doc, "sentry_") {
		t.Error("sentry tools should not appear when only Linear is configured")
	}
	if strings.Contains(doc, "143-tools sentry_list_errors") {
		t.Error("sentry-specific usage tips should not appear when only Linear is configured")
	}
}

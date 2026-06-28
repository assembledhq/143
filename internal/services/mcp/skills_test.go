package mcp

import (
	"strings"
	"testing"

	"github.com/assembledhq/143/internal/services/integration"
	"github.com/stretchr/testify/require"
)

func TestGenerateSkillsDoc_WithIntegrations(t *testing.T) {
	t.Parallel()

	tr := NewToolRegistry(buildFullCLIRegistry())
	doc := GenerateSkillsDoc(tr)

	require.NotEmpty(t, doc, "skills doc should be generated when integrations exist")
	require.Contains(t, doc, "# Integration Tools", "skills doc should include the section header")
	require.Contains(t, doc, "143-tools <namespace> <action>", "skills doc should teach hierarchical command shape")
	require.Contains(t, doc, "`sentry`", "skills doc should include configured Sentry namespace")
	require.Contains(t, doc, "`linear`", "skills doc should include configured Linear namespace")
	require.Contains(t, doc, "`logs`", "skills doc should include configured logs namespace")
	require.Contains(t, doc, "143-tools sentry list_errors", "skills doc should include high-value hierarchical examples")
	require.Contains(t, doc, "143-tools linear get_task", "skills doc should include high-value hierarchical examples")
	require.Contains(t, doc, "143-tools logs query", "skills doc should include high-value hierarchical examples")
	require.Contains(t, doc, "143-tools preview", "skills doc should teach the built-in preview namespace")
	require.Contains(t, doc, "--session-id", "preview guidance should steer agents toward session previews while editing")
	require.NotContains(t, doc, "sentry_list_errors", "skills doc should not mention old flat command names")
	require.NotContains(t, doc, "linear_create_task", "skills doc should not mention old flat command names")
	require.NotContains(t, doc, "143-tools <tool_name>", "skills doc should not teach old flat command shape")
	require.Contains(t, doc, "Run `143-tools <namespace> --help`", "skills doc should guide agents toward lazy discovery")
}

func TestGenerateSkillsDoc_Empty(t *testing.T) {
	t.Parallel()

	tr := NewToolRegistry(integration.NewRegistry())
	doc := GenerateSkillsDoc(tr)

	require.Empty(t, doc, "skills doc should be empty when no integrations are configured")
}

func TestGenerateSkillsDoc_TokenEfficiency(t *testing.T) {
	t.Parallel()

	tr := NewToolRegistry(buildFullCLIRegistry())
	doc := GenerateSkillsDoc(tr)

	words := len(strings.Fields(doc))
	require.LessOrEqual(t, words, 500, "skills doc should stay compact by summarizing namespaces instead of listing every tool")
	require.Greater(t, words, 50, "skills doc should contain enough discovery guidance to be useful")
}

func TestGenerateSkillsDoc_SessionTabsIncludeCoordinationGuidanceAndKebabFlags(t *testing.T) {
	t.Parallel()

	tr := NewToolRegistry(buildFullTestRegistry())
	doc := GenerateSkillsDoc(tr)

	require.Contains(t, doc, "Use a new tab for parallel review/testing/investigation in the same branch.", "session tab tools should teach when to create sibling tabs")
	require.Contains(t, doc, "Use a new session only when work needs an independent branch or PR.", "session tab tools should distinguish tabs from sessions")
	require.Contains(t, doc, "`session-tabs`", "session tab tools should appear under the hierarchical session-tabs namespace")
	require.Contains(t, doc, "143-tools session-tabs", "session tab examples should use hierarchical commands")
	require.Contains(t, doc, "--tab-id", "session tab examples should prefer kebab-case flags")
	require.NotContains(t, doc, "--tab_id", "session tab examples should not expose snake_case flags")
	require.NotContains(t, doc, "session_tabs_", "skills doc should not expose old flat session tab command names")
}

func TestGenerateSkillsDoc_SentryOnly(t *testing.T) {
	t.Parallel()

	reg := integration.NewRegistry()
	reg.RegisterErrorTracker(&mockErrorTracker{name: "sentry"})
	tr := NewToolRegistry(reg)
	doc := GenerateSkillsDoc(tr)

	require.NotEmpty(t, doc, "skills doc should be generated for Sentry")
	require.Contains(t, doc, "`sentry`", "skills doc should include Sentry namespace")
	require.Contains(t, doc, "143-tools sentry list_errors", "skills doc should include Sentry example")
	require.NotContains(t, doc, "`linear`", "skills doc should omit unconfigured Linear namespace")
	require.NotContains(t, doc, "143-tools linear", "skills doc should omit unconfigured Linear examples")
	require.NotContains(t, doc, "sentry_", "skills doc should omit old flat Sentry command names")
}

func TestGenerateSkillsDoc_LinearOnly(t *testing.T) {
	t.Parallel()

	reg := integration.NewRegistry()
	reg.RegisterTaskManager(&mockTaskManager{name: "linear"})
	tr := NewToolRegistry(reg)
	doc := GenerateSkillsDoc(tr)

	require.NotEmpty(t, doc, "skills doc should be generated for Linear")
	require.Contains(t, doc, "`linear`", "skills doc should include Linear namespace")
	require.Contains(t, doc, "143-tools linear get_task", "skills doc should include Linear example")
	require.NotContains(t, doc, "`sentry`", "skills doc should omit unconfigured Sentry namespace")
	require.NotContains(t, doc, "143-tools sentry", "skills doc should omit unconfigured Sentry examples")
	require.NotContains(t, doc, "linear_", "skills doc should omit old flat Linear command names")
}

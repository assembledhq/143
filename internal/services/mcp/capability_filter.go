package mcp

import (
	"context"
	"encoding/json"

	"github.com/assembledhq/143/internal/models"
)

type ToolCapabilityPolicy struct {
	Capabilities []models.AgentCapabilitySnapshotItem
}

type capabilityFilteredToolSource struct {
	base    ToolSource
	allowed map[string]bool
}

func NewCapabilityFilteredToolSource(base ToolSource, policy ToolCapabilityPolicy) ToolSource {
	allowed := make(map[string]bool)
	for _, capability := range policy.Capabilities {
		addAllowedToolPaths(allowed, capability)
	}
	return &capabilityFilteredToolSource{base: base, allowed: allowed}
}

func (s *capabilityFilteredToolSource) ListTools() []Tool {
	baseTools := s.base.ListTools()
	out := make([]Tool, 0, len(baseTools))
	for _, tool := range baseTools {
		if s.toolAllowed(tool.Name) {
			out = append(out, tool)
		}
	}
	return out
}

func (s *capabilityFilteredToolSource) CallTool(ctx context.Context, name string, args json.RawMessage) *ToolCallResult {
	if !s.toolAllowed(name) {
		return ErrorResult("CAPABILITY_DENIED: tool is not enabled for this agent run")
	}
	return s.base.CallTool(ctx, name, args)
}

func (s *capabilityFilteredToolSource) toolAllowed(name string) bool {
	namespace, action, ok := cliPathForTool(name)
	if !ok {
		return false
	}
	if namespace == NamespaceCapability {
		return true
	}
	return s.allowed[string(namespace)+" "+string(action)]
}

func addAllowedToolPaths(allowed map[string]bool, capability models.AgentCapabilitySnapshotItem) {
	add := func(namespace CLINamespace, actions ...CLIAction) {
		for _, action := range actions {
			allowed[string(namespace)+" "+string(action)] = true
		}
	}
	switch capability.ID {
	case models.AgentCapabilitySessionHistory:
		add(NamespaceSessionHistory, ActionSearch, ActionGet, ActionMessages)
	case models.AgentCapabilityPRHistory:
		add(CLINamespace("github"), CLIAction("list_recent_prs"), CLIAction("get_pr_reviews"))
	case models.AgentCapabilityCIHistory:
		add(CLINamespace("circleci"), CLIAction("list_flaky_tests"), CLIAction("get_recent_test_failures"), CLIAction("get_job_test_results"))
	case models.AgentCapabilityIssueSources:
		add(CLINamespace("sentry"), CLIAction("list_errors"), CLIAction("get_error"), CLIAction("get_error_trend"), CLIAction("find_related_errors"))
		add(CLINamespace("linear"), CLIAction("list_tasks"), CLIAction("get_task"), CLIAction("find_related_tasks"))
	case models.AgentCapabilityTeamDocs:
		add(CLINamespace("notion"), CLIAction("search_documents"), CLIAction("get_document"))
		add(CLINamespace("slack"), CLIAction("search_messages"), CLIAction("get_thread"))
	case models.AgentCapabilityProductionDiagnostics:
		add(NamespaceLogs, CLIAction("query"), CLIAction("context"), CLIAction("fields"), CLIAction("stats"))
	case models.AgentCapabilityExternalComments:
		add(CLINamespace("linear"), CLIAction("update_task"), CLIAction("create_task"))
	case models.AgentCapabilityProjectProposals:
		add(NamespaceProject, ActionPropose)
	case models.AgentCapabilityEvalAuthoring:
		add(NamespaceEval, ActionAdd)
	case models.AgentCapabilityPublishing:
		add(NamespacePR, ActionCreate)
	}
}

package mcp

import (
	"context"
	"encoding/json"

	"github.com/assembledhq/143/internal/models"
)

type ToolCapabilityPolicy struct {
	Capabilities []models.AgentCapabilitySnapshotItem
	// ToolAllowlist, when non-nil, is a positive list of "namespace:action"
	// tools (design doc 125): a tool is callable only when it is in the
	// list and its capability is granted, and the namespaces that bypass
	// the capability check (capability, goal improvement, preview) are
	// subject to the list too.
	ToolAllowlist []string
}

type capabilityFilteredToolSource struct {
	base      ToolSource
	allowed   map[string]bool
	allowlist map[string]bool
}

func NewCapabilityFilteredToolSource(base ToolSource, policy ToolCapabilityPolicy) ToolSource {
	allowed := make(map[string]bool)
	for _, capability := range policy.Capabilities {
		addAllowedToolPaths(allowed, capability)
	}
	var allowlist map[string]bool
	if policy.ToolAllowlist != nil {
		allowlist = make(map[string]bool, len(policy.ToolAllowlist))
		for _, tool := range policy.ToolAllowlist {
			allowlist[tool] = true
		}
	}
	return &capabilityFilteredToolSource{base: base, allowed: allowed, allowlist: allowlist}
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
	if s.allowlist != nil {
		if !s.allowlist[string(namespace)+":"+string(action)] {
			return false
		}
		// capability:list is the only bypass namespace the allowlist keeps
		// and needs no grant; every other allowlisted tool still needs its
		// capability.
		if namespace == NamespaceCapability {
			return true
		}
		return s.allowed[string(namespace)+" "+string(action)]
	}
	if namespace == NamespaceCapability {
		return true
	}
	if namespace == NamespaceAutomationGoalImprovement && action == ActionComplete {
		return true
	}
	// Preview tools are intrinsic to the current coding session and are
	// independently constrained by session-scoped token capabilities.
	if namespace == NamespacePreview {
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
	case models.AgentCapabilityReviewFeedback:
		add(NamespaceCodeReviewHistory, ActionList, ActionGet, ActionPolicy)
	case models.AgentCapabilityCodeReviewPolicy:
		// Policy read rides along with the write grant so an agent can learn
		// the expected_version its update must carry even when the org has
		// not granted review_feedback; the internal API mirrors this.
		add(NamespaceCodeReviewHistory, ActionPolicy, ActionUpdatePolicy)
	case models.AgentCapabilityPRHistory:
		add(CLINamespace("github"), CLIAction("list_recent_prs"), CLIAction("get_pr_reviews"))
	case models.AgentCapabilityCIHistory:
		add(CLINamespace("circleci"), CLIAction("list_flaky_tests"), CLIAction("get_recent_test_failures"), CLIAction("get_job_test_results"))
	case models.AgentCapabilityIssueSources:
		add(CLINamespace("sentry"), CLIAction("list_errors"), CLIAction("get_error"), CLIAction("get_error_trend"), CLIAction("find_related_errors"))
		add(CLINamespace("linear"), CLIAction("list_tasks"), CLIAction("get_task"), CLIAction("find_related_tasks"))
		add(CLINamespace("pagerduty"), CLIAction("list_incidents"), CLIAction("get_incident"), CLIAction("list_notes"), CLIAction("list_log_entries"), CLIAction("get_service"), CLIAction("list_oncalls"), CLIAction("find_related_incidents"))
	case models.AgentCapabilityTeamDocs:
		add(CLINamespace("notion"), CLIAction("search_documents"), CLIAction("get_document"))
		add(CLINamespace("slack"), CLIAction("search_messages"), CLIAction("get_thread"))
	case models.AgentCapabilityProductionDiagnostics:
		add(NamespaceLogs, CLIAction("query"), CLIAction("context"), CLIAction("fields"), CLIAction("stats"))
	case models.AgentCapabilityExternalComments:
		add(CLINamespace("linear"), CLIAction("update_task"), CLIAction("create_task"))
		add(CLINamespace("pagerduty"), CLIAction("add_note"), CLIAction("create_status_update"))
	case models.AgentCapabilitySlackNotifications:
		add(CLINamespace("slack"), ActionSend)
	case models.AgentCapabilityAutomationManagement:
		add(NamespaceAutomation, ActionCreate, ActionUpdate, ActionRun, ActionPause, ActionResume)
	case models.AgentCapabilityEvalAuthoring:
		add(NamespaceEval, ActionAdd)
	case models.AgentCapabilityPublishing:
		add(NamespacePR, ActionCreate)
	}
}

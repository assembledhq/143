package models

import (
	"slices"
	"strings"
)

// Per-target automation turns (design doc 125, "Scope") run with a
// positive tool allowlist rather than a subtractive one: the capability
// filter has grants and bypasses a subtractive list misses. The allowlist
// is applied three times with one definition: the run's capability
// snapshot is restricted at dispatch, the session token carries the tool
// scopes and no preview scope, and the internal API refuses any call whose
// route is not in the list. The agent keeps shell and network access inside
// the sandbox.

// PerTargetToolAllowlistScope marks a session token as allowlisted: every
// internal API route then requires its own tool scope.
const PerTargetToolAllowlistScope = "tool-allowlist:v1"

// PerTargetToolAllowlist is the exact set of tools a per-target turn may
// call, as "namespace:action" identifiers.
var PerTargetToolAllowlist = []string{
	"session-history:search", "session-history:get", "session-history:messages",
	"code-review-history:list", "code-review-history:get", "code-review-history:policy",
	"github:list_recent_prs", "github:get_pr_reviews",
	"linear:list_tasks", "linear:get_task", "linear:find_related_tasks",
	"pagerduty:list_incidents", "pagerduty:get_incident", "pagerduty:list_notes", "pagerduty:list_log_entries",
	"pagerduty:get_service", "pagerduty:list_oncalls", "pagerduty:find_related_incidents",
	"notion:search_documents", "notion:get_document",
	"slack:search_messages", "slack:get_thread",
	"logs:query", "logs:context", "logs:fields", "logs:stats",
	"capability:list",
}

// perTargetAllowedCapabilities are the capabilities that contribute at
// least one allowlisted tool; they are kept at read level. Every other
// capability (publishing, automation management, code review policy
// writes, Slack sends, Linear and PagerDuty writes, eval authoring) is
// dropped from a per-target turn's snapshot.
var perTargetAllowedCapabilities = map[AgentCapabilityID]bool{
	AgentCapabilitySessionHistory:        true,
	AgentCapabilityReviewFeedback:        true,
	AgentCapabilityPRHistory:             true,
	AgentCapabilityIssueSources:          true,
	AgentCapabilityTeamDocs:              true,
	AgentCapabilityProductionDiagnostics: true,
}

// ToolScope is the token scope that grants one allowlisted tool.
func ToolScope(tool string) string {
	return "tool:" + tool
}

// PerTargetToolScopes are the scopes a per-target turn's session token
// carries: the allowlist marker and one scope per allowlisted tool.
func PerTargetToolScopes() []string {
	scopes := make([]string, 0, len(PerTargetToolAllowlist)+1)
	scopes = append(scopes, PerTargetToolAllowlistScope)
	for _, tool := range PerTargetToolAllowlist {
		scopes = append(scopes, ToolScope(tool))
	}
	return scopes
}

// HasToolScope reports whether scopes contain the given scope.
func HasToolScope(scopes []string, scope string) bool {
	return slices.Contains(scopes, scope)
}

// ToolAllowlistFromScopes returns the tools an allowlisted token grants,
// or nil when the token is not allowlisted.
func ToolAllowlistFromScopes(scopes []string) []string {
	if !HasToolScope(scopes, PerTargetToolAllowlistScope) {
		return nil
	}
	tools := make([]string, 0, len(scopes))
	for _, s := range scopes {
		if tool, ok := strings.CutPrefix(s, "tool:"); ok && tool != "" {
			tools = append(tools, tool)
		}
	}
	return tools
}

// RestrictCapabilitySnapshotForPerTargetTurn applies the positive allowlist
// to a resolved capability snapshot: capabilities that contribute no
// allowlisted tool are dropped, and the rest are capped at read access.
// The result is what the turn executes with, whatever the organization
// granted.
func RestrictCapabilitySnapshotForPerTargetTurn(items []AgentCapabilitySnapshotItem) []AgentCapabilitySnapshotItem {
	out := make([]AgentCapabilitySnapshotItem, 0, len(items))
	for _, item := range items {
		if !perTargetAllowedCapabilities[item.ID] {
			continue
		}
		restricted := item
		restricted.AccessLevel = AgentCapabilityAccessRead
		out = append(out, restricted)
	}
	return out
}

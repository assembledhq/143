package agent

import (
	"strings"

	"github.com/assembledhq/143/internal/models"
)

func setClaudeCodePermissionMode(sandbox *Sandbox, mode string) {
	if sandbox == nil {
		return
	}
	if mode != ClaudeCodePermissionModeAuto {
		mode = ClaudeCodePermissionModeAcceptEdits
	}
	if sandbox.Metadata == nil {
		sandbox.Metadata = make(map[string]string, 1)
	}
	sandbox.Metadata[SandboxMetadataClaudeCodePermissionMode] = mode
}

func claudeCodePermissionModeForAuth(billingMode TokenBillingMode, accountType string, model string) string {
	if !claudeCodeModelSupportsAuto(model) {
		return ClaudeCodePermissionModeAcceptEdits
	}
	switch billingMode {
	case TokenBillingModeAPIKey:
		return ClaudeCodePermissionModeAuto
	case TokenBillingModeSubscription:
		if claudeCodeSubscriptionSupportsAuto(accountType) {
			return ClaudeCodePermissionModeAuto
		}
	}
	return ClaudeCodePermissionModeAcceptEdits
}

func restrictClaudeCodePermissionModeForModel(sandbox *Sandbox, model string) {
	if sandbox == nil || sandbox.Metadata == nil {
		return
	}
	if sandbox.Metadata[SandboxMetadataClaudeCodePermissionMode] != ClaudeCodePermissionModeAuto {
		return
	}
	if !claudeCodeModelSupportsAuto(model) {
		setClaudeCodePermissionMode(sandbox, ClaudeCodePermissionModeAcceptEdits)
	}
}

func claudeCodeModelSupportsAuto(model string) bool {
	normalized := strings.TrimSpace(strings.ToLower(model))
	if normalized == "" {
		return true
	}
	normalized = strings.TrimPrefix(normalized, "anthropic/")
	switch normalized {
	case models.ClaudeCodeModelOpus47, models.ClaudeCodeModelOpus46, models.ClaudeCodeModelSonnet46:
		return true
	default:
		return false
	}
}

func claudeCodeSubscriptionSupportsAuto(accountType string) bool {
	normalized := strings.ToLower(strings.TrimSpace(accountType))
	if normalized == "" {
		return false
	}
	normalized = strings.ReplaceAll(normalized, "-", "_")
	if strings.Contains(normalized, "pro") {
		return false
	}
	return strings.Contains(normalized, "max") ||
		strings.Contains(normalized, "team") ||
		strings.Contains(normalized, "enterprise")
}

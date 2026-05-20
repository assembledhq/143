package agent

import (
	"bytes"
	"context"
	"regexp"
	"strconv"
	"strings"

	"github.com/rs/zerolog"
)

const (
	claudeCodeAutoPermissionMinMajor = 2
	claudeCodeAutoPermissionMinMinor = 1
	claudeCodeAutoPermissionMinPatch = 0
)

var claudeCodeVersionPattern = regexp.MustCompile(`\b(\d+)\.(\d+)\.(\d+)\b`)

func setClaudeCodePermissionMode(sandbox *Sandbox, mode string) {
	if sandbox == nil {
		return
	}
	if mode != ClaudeCodePermissionModeBypassPermissions {
		mode = ClaudeCodePermissionModeBypassPermissions
	}
	if sandbox.Metadata == nil {
		sandbox.Metadata = make(map[string]string, 1)
	}
	sandbox.Metadata[SandboxMetadataClaudeCodePermissionMode] = mode
}

func claudeCodePermissionModeForAuth(billingMode TokenBillingMode, accountType string, model string, cliVersion string) string {
	return ClaudeCodePermissionModeBypassPermissions
}

func claudeCodeModelSupportsAuto(model string) bool {
	normalized := strings.TrimSpace(strings.ToLower(model))
	if normalized == "" {
		return true
	}
	normalized = strings.TrimPrefix(normalized, "anthropic/")

	parts := strings.Split(normalized, "-")
	if len(parts) < 4 || parts[0] != "claude" {
		return false
	}
	switch parts[1] {
	case "opus", "sonnet":
	default:
		return false
	}

	major, err := strconv.Atoi(parts[2])
	if err != nil {
		return false
	}
	minor, err := strconv.Atoi(parts[3])
	if err != nil {
		return false
	}
	return major > 4 || (major == 4 && minor >= 6)
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

func parseClaudeCodeVersion(output string) string {
	match := claudeCodeVersionPattern.FindStringSubmatch(output)
	if len(match) != 4 {
		return ""
	}
	return match[0]
}

func detectClaudeCodeVersion(ctx context.Context, sandbox *Sandbox, provider SandboxProvider, logger zerolog.Logger) string {
	if sandbox == nil {
		return ""
	}
	if sandbox.Metadata != nil {
		if version := sandbox.Metadata[SandboxMetadataClaudeCodeVersion]; version != "" {
			return version
		}
	}
	if provider == nil {
		return ""
	}

	var stdout, stderr bytes.Buffer
	exitCode, err := provider.Exec(ctx, sandbox, "claude --version", &stdout, &stderr)
	if err != nil || exitCode != 0 {
		logger.Debug().
			Err(err).
			Int("exit_code", exitCode).
			Str("stderr", strings.TrimSpace(stderr.String())).
			Msg("failed to detect Claude Code CLI version")
		return ""
	}
	version := parseClaudeCodeVersion(stdout.String())
	if version == "" {
		logger.Debug().
			Str("stdout", strings.TrimSpace(stdout.String())).
			Msg("could not parse Claude Code CLI version")
		return ""
	}
	if sandbox.Metadata == nil {
		sandbox.Metadata = make(map[string]string, 1)
	}
	sandbox.Metadata[SandboxMetadataClaudeCodeVersion] = version
	return version
}

func claudeCodeCLISupportsAuto(version string) bool {
	parsed := parseClaudeCodeVersion(version)
	if parsed == "" {
		return false
	}
	parts := strings.Split(parsed, ".")
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return false
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return false
	}
	patch, err := strconv.Atoi(parts[2])
	if err != nil {
		return false
	}

	if major != claudeCodeAutoPermissionMinMajor {
		return major > claudeCodeAutoPermissionMinMajor
	}
	if minor != claudeCodeAutoPermissionMinMinor {
		return minor > claudeCodeAutoPermissionMinMinor
	}
	return patch >= claudeCodeAutoPermissionMinPatch
}

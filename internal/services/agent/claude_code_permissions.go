package agent

import (
	"bytes"
	"context"
	"regexp"
	"strings"

	"github.com/rs/zerolog"
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

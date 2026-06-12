package agentdiagnostics

import (
	"strings"
	"time"
)

// ClassifyBenignCodexDiagnostic identifies Codex CLI/runtime diagnostics that
// are useful for debugging but should not be rendered as user-facing failures.
func ClassifyBenignCodexDiagnostic(msg string) (kind string, multiline bool, ok bool) {
	trimmed := strings.TrimSpace(msg)
	if strings.Contains(trimmed, "codex_core::tools::router:") {
		switch {
		case strings.Contains(trimmed, "write_stdin failed:"):
			if strings.Contains(trimmed, "stdin is closed for this session") {
				return "closed_stdin", false, true
			}
			return "write_stdin_failed", false, true
		case strings.Contains(trimmed, "apply_patch verification failed: Failed to find expected lines"):
			return "apply_patch_verification_failed", true, true
		case strings.Contains(trimmed, "failed to parse function arguments:"):
			return "tool_argument_parse_failed", false, true
		case strings.Contains(trimmed, "unable to process image at"):
			return "tool_image_processing_failed", false, true
		}
	}
	if isRecoverableCodexResponsesWebsocketDiagnostic(trimmed) {
		return "responses_websocket_retry", false, true
	}
	if strings.Contains(trimmed, "codex_core_skills::loader: failed to stat skills path") {
		return "skills_loader_missing_path", false, true
	}
	if strings.Contains(trimmed, "codex_models_manager::manager: failed to refresh available models:") {
		return "models_refresh_failed", false, true
	}
	if trimmed == "Reading additional input from stdin..." {
		return "stdin_notice", false, true
	}
	return "", false, false
}

func IsBenignCodexDiagnostic(msg string) bool {
	_, _, ok := ClassifyBenignCodexDiagnostic(msg)
	return ok
}

func IsCodexLogRecordStart(trimmed string) bool {
	fields := strings.Fields(trimmed)
	if len(fields) >= 3 {
		if _, err := time.Parse(time.RFC3339Nano, fields[0]); err == nil &&
			isCodexLogLevel(fields[1]) &&
			isCodexRustTarget(fields[2]) {
			return true
		}
	}
	return len(fields) >= 2 && isCodexLogLevel(fields[0]) && isCodexRustTarget(fields[1])
}

func isRecoverableCodexResponsesWebsocketDiagnostic(msg string) bool {
	trimmed := strings.TrimSpace(msg)
	if strings.HasPrefix(trimmed, "Reconnecting... ") &&
		strings.Contains(trimmed, "stream disconnected before completion:") {
		return true
	}
	return strings.Contains(trimmed, "codex_api::endpoint::responses_websocket:") &&
		strings.Contains(trimmed, "failed to connect to websocket:")
}

func isCodexRustTarget(value string) bool {
	return strings.HasPrefix(value, "codex_core::") ||
		strings.HasPrefix(value, "codex_api::") ||
		strings.HasPrefix(value, "codex_core_skills::") ||
		strings.HasPrefix(value, "codex_models_manager::")
}

func isCodexLogLevel(value string) bool {
	switch value {
	case "ERROR", "WARN", "INFO", "DEBUG", "TRACE":
		return true
	default:
		return false
	}
}

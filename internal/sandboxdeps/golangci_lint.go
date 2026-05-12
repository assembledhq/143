package sandboxdeps

import (
	"context"
	"fmt"
	"strings"
)

func init() {
	Default.Register(golangciLint{})
}

type golangciLint struct{}

func (golangciLint) Name() string { return "golangci-lint" }

func (golangciLint) Check(ctx context.Context, exec Executor, version string) (bool, error) {
	stdout, _, code, err := captureExecStderr(ctx, exec, "golangci-lint --version")
	if err != nil || code != 0 {
		return false, err
	}
	// Output looks like:
	//   golangci-lint has version 1.64.8 built from ...
	return strings.Contains(stdout, " "+version+" ") || strings.Contains(stdout, " "+version+"\n"), nil
}

func (golangciLint) Install(ctx context.Context, exec Executor, version string) error {
	// The official install script is pinned to a Git tag, so we anchor on a
	// release tag (v<version>) and direct it at the unprivileged
	// ~/.local/bin that the Dockerfile already adds to PATH. -B/-b creates
	// the directory if missing.
	cmd := fmt.Sprintf(
		"mkdir -p \"$HOME/.local/bin\" && curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b \"$HOME/.local/bin\" v%s",
		shellEscapeSingleQuote(version),
	)
	stdout, stderr, code, err := captureExecStderr(ctx, exec, cmd)
	if err != nil {
		return fmt.Errorf("golangci-lint install: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("golangci-lint install exited %d: %s", code, firstNonEmpty(stderr, stdout))
	}
	return nil
}

// shellEscapeSingleQuote wraps s for safe interpolation inside a double-quoted
// shell context. Versions come from .143/config.json which Parse already
// trims and rejects empty/"latest", but a malformed pin shouldn't be able to
// inject extra shell syntax.
func shellEscapeSingleQuote(s string) string {
	// Versions are a constrained alphabet (digits, dots, -beta etc.) so
	// stripping anything outside [A-Za-z0-9._-] is safer and simpler than
	// trying to quote-escape inside a double-quoted heredoc.
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
			b.WriteRune(r)
		}
	}
	return b.String()
}

func firstNonEmpty(a, b string) string {
	a = strings.TrimSpace(a)
	if a != "" {
		return a
	}
	return strings.TrimSpace(b)
}

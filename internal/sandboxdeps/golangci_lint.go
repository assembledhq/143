package sandboxdeps

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"strings"
)

func init() {
	Default.Register(golangciLint{})
}

// golangciLintVersion matches the version strings we accept (e.g. 1.64.8,
// 2.0.0-beta.1). Anything else is rejected before we interpolate into shell.
var golangciLintVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.\-]+)?$`)

type golangciLint struct{}

func (golangciLint) Name() string { return "golangci-lint" }

func (golangciLint) Install(ctx context.Context, exec Executor, version string) error {
	if !golangciLintVersion.MatchString(version) {
		return fmt.Errorf("golangci-lint: invalid version pin %q", version)
	}
	// Install into ~/.local/bin because the sandbox user has no sudo;
	// /usr/local/bin would be read-only at runtime. The Dockerfile already
	// puts ~/.local/bin on PATH.
	cmd := fmt.Sprintf(
		`mkdir -p "$HOME/.local/bin" && curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b "$HOME/.local/bin" v%s`,
		version,
	)
	var stdout, stderr bytes.Buffer
	code, err := exec(ctx, cmd, &stdout, &stderr)
	if err != nil {
		return fmt.Errorf("golangci-lint install: %w", err)
	}
	if code != 0 {
		out := strings.TrimSpace(stderr.String())
		if out == "" {
			out = strings.TrimSpace(stdout.String())
		}
		return fmt.Errorf("golangci-lint install exited %d: %s", code, out)
	}
	return nil
}

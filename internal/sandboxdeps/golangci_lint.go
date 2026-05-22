package sandboxdeps

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"strings"
)

func init() {
	DependencyRegistry.Register(golangciLint{})
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
	// /usr/local/bin would be read-only at runtime. The orchestrator prepends
	// this directory to PATH in the runtime sandbox env.
	cmd := fmt.Sprintf(
		`set -eu
tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT
os="$(uname -s)"
case "$os" in
  Linux|linux) os=linux ;;
  Darwin|darwin) os=darwin ;;
  *) echo "unsupported OS: $os" >&2; exit 1 ;;
esac
arch="$(uname -m)"
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) echo "unsupported architecture: $arch" >&2; exit 1 ;;
esac
archive="$tmp_dir/golangci-lint.tar.gz"
url="https://github.com/golangci/golangci-lint/releases/download/v%[1]s/golangci-lint-%[1]s-${os}-${arch}.tar.gz"
curl -fsSL --connect-timeout 10 --max-time 120 -o "$archive" "$url"
tar -xzf "$archive" -C "$tmp_dir"
mkdir -p "$HOME/.local/bin"
cp "$tmp_dir/golangci-lint-%[1]s-${os}-${arch}/golangci-lint" "$HOME/.local/bin/golangci-lint"
chmod 0755 "$HOME/.local/bin/golangci-lint"`,
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

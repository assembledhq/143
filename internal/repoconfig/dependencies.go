package repoconfig

import (
	"fmt"
	"regexp"
)

// dependencyInstaller renders the shell command that installs a dependency at
// the given version inside a sandbox. The sandbox base image already provides
// curl, jq, tar, and dpkg, so installers can rely on those.
type dependencyInstaller func(version string) string

var dependencyInstallers = map[string]dependencyInstaller{
	"golangci-lint": golangciLintInstaller,
}

// semverLike accepts the version strings golangci-lint actually publishes
// (e.g. "2.5.0", "1.62.2"). Anchored to keep shell metacharacters out of the
// rendered command.
var semverLike = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)

func golangciLintInstaller(version string) string {
	return fmt.Sprintf(`set -e
ARCH=$(dpkg --print-architecture)
case "$ARCH" in
    amd64) GCL_ARCH=amd64 ;;
    arm64) GCL_ARCH=arm64 ;;
    *) echo "unsupported arch: $ARCH" >&2; exit 1 ;;
esac
TMPDIR=$(mktemp -d)
trap 'rm -rf "$TMPDIR"' EXIT
curl -fsSL "https://github.com/golangci/golangci-lint/releases/download/v%[1]s/golangci-lint-%[1]s-linux-${GCL_ARCH}.tar.gz" -o "$TMPDIR/gcl.tgz"
tar -xzf "$TMPDIR/gcl.tgz" -C "$TMPDIR"
mkdir -p "$HOME/.local/bin"
install -m 0755 "$TMPDIR/golangci-lint-%[1]s-linux-${GCL_ARCH}/golangci-lint" "$HOME/.local/bin/golangci-lint"
"$HOME/.local/bin/golangci-lint" --version`, version)
}

// InstallCommands returns the shell snippets needed to provision the given
// dependencies inside a sandbox, in declaration order. Each snippet is a
// self-contained sh -c script. Returns an error if any dependency is unknown
// or has an unacceptable version string — both cases are caught at parse time
// today, so this is defense in depth for callers that build Configs directly.
func InstallCommands(deps []Dependency) ([]string, error) {
	commands := make([]string, 0, len(deps))
	for _, dep := range deps {
		installer, ok := dependencyInstallers[dep.Name]
		if !ok {
			return nil, fmt.Errorf("unsupported dependency %q", dep.Name)
		}
		if !semverLike.MatchString(dep.Version) {
			return nil, fmt.Errorf("dependency %q version %q must be MAJOR.MINOR.PATCH", dep.Name, dep.Version)
		}
		commands = append(commands, installer(dep.Version))
	}
	return commands, nil
}

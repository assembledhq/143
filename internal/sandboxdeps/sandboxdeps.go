// Package sandboxdeps installs declared per-repo tooling (golangci-lint,
// formatters, language toolchains) into a sandbox so bootstrap/validation
// commands can call them. Repos declare what they want in
// .143/config.json `dependencies` as {name: exact-version}; 143 owns how
// each dependency is installed.
package sandboxdeps

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/rs/zerolog"
)

// Executor runs a shell command inside the sandbox.
type Executor func(ctx context.Context, cmd string, stdout, stderr io.Writer) (int, error)

// Dependency knows how to install one named tool at an exact version.
type Dependency interface {
	Name() string
	Install(ctx context.Context, exec Executor, version string) error
}

// registry holds the install recipes 143 ships built-in. Recipes register
// themselves from their own init().
type registry struct {
	deps map[string]Dependency
}

func newRegistry() *registry {
	return &registry{deps: map[string]Dependency{}}
}

func (r *registry) Register(d Dependency) {
	r.deps[d.Name()] = d
}

// DependencyRegistry holds the install recipes that recipe files (e.g.
// golangci_lint.go) register into via init(). Apply consults it for every
// declared dependency. Tests may swap it via t.Cleanup.
var DependencyRegistry = newRegistry()

// Apply installs every dependency in deps using exec. Unknown names and
// install failures are logged but do not abort — sandbox dependency setup is
// best-effort so a bad entry can't take down a session.
//
// Caching is intentionally absent. Every Apply call re-runs every recipe's
// Install. This is fine while the registry is small (golangci-lint is a ~20MB
// download). We should iterate when it becomes a bottleneck.
func Apply(ctx context.Context, log zerolog.Logger, exec Executor, deps map[string]string) {
	if len(deps) == 0 {
		return
	}

	// Stable order so logs don't flap.
	names := make([]string, 0, len(deps))
	for name := range deps {
		names = append(names, name)
	}
	sort.Strings(names)

	var installed, failed, unknown int
	var failures []string

	for _, name := range names {
		version := deps[name]
		dep, ok := DependencyRegistry.deps[name]
		if !ok {
			unknown++
			failures = append(failures, fmt.Sprintf("%s@%s: no install recipe registered", name, version))
			log.Warn().Str("dependency", name).Str("version", version).Msg("unknown sandbox dependency; skipping")
			continue
		}
		if err := dep.Install(ctx, exec, version); err != nil {
			failed++
			failures = append(failures, fmt.Sprintf("%s@%s: %v", name, version, err))
			log.Warn().Err(err).Str("dependency", name).Str("version", version).Msg("sandbox dependency install failed")
			continue
		}
		installed++
	}

	event := log.Info().Int("installed", installed).Int("failed", failed).Int("unknown", unknown)
	if len(failures) > 0 {
		event = event.Str("failures", strings.Join(failures, "; "))
	}
	event.Msg("sandbox dependencies applied")
}

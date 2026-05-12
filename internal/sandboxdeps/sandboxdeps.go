// Package sandboxdeps installs declared per-repo tooling (golangci-lint,
// formatters, language toolchains) into a sandbox so bootstrap/validation
// commands can call them. Repos declare what they want in
// .143/config.json `dependencies` as {name: exact-version}; 143 owns how
// each dependency is installed and how to detect when it is already present.
package sandboxdeps

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/rs/zerolog"
)

// Executor runs a shell command inside the sandbox and writes its stdout and
// stderr to the provided writers. Either writer may be nil to discard.
// Returns the process exit code and any transport-level error.
type Executor func(ctx context.Context, cmd string, stdout, stderr io.Writer) (int, error)

// Dependency knows how to install one named tool at an exact version, and how
// to verify that the desired version is already present so re-runs of Apply
// short-circuit without re-downloading.
type Dependency interface {
	Name() string
	Check(ctx context.Context, exec Executor, version string) (bool, error)
	Install(ctx context.Context, exec Executor, version string) error
}

// Registry maps dependency names to their install recipe. The default
// registry is populated via Register from package init blocks.
type Registry struct {
	deps map[string]Dependency
}

func NewRegistry() *Registry {
	return &Registry{deps: map[string]Dependency{}}
}

func (r *Registry) Register(d Dependency) {
	r.deps[d.Name()] = d
}

func (r *Registry) Lookup(name string) (Dependency, bool) {
	d, ok := r.deps[name]
	return d, ok
}

// Default is the shared registry the orchestrator consults at session start.
// Built-in recipes register themselves into Default in their own init().
var Default = NewRegistry()

// Result describes the outcome of installing one dependency.
type Result struct {
	Name    string
	Version string
	// Status is one of: "installed", "already-present", "unknown", "failed".
	Status string
	Err    error
}

// Apply installs every dependency in deps using exec. Unknown names and
// install failures are recorded in the returned results but do not abort the
// run — sandbox dependency setup is best-effort so a bad entry can't take
// down a session. Callers should log the aggregated outcome.
func Apply(ctx context.Context, log zerolog.Logger, registry *Registry, exec Executor, deps map[string]string) []Result {
	if len(deps) == 0 {
		return nil
	}
	if registry == nil {
		registry = Default
	}

	// Stable order so logs and tests don't flap.
	names := make([]string, 0, len(deps))
	for name := range deps {
		names = append(names, name)
	}
	sort.Strings(names)

	results := make([]Result, 0, len(names))
	for _, name := range names {
		version := deps[name]
		res := Result{Name: name, Version: version}

		dep, ok := registry.Lookup(name)
		if !ok {
			res.Status = "unknown"
			res.Err = fmt.Errorf("no install recipe registered for dependency %q", name)
			log.Warn().Str("dependency", name).Str("version", version).Msg("unknown sandbox dependency; skipping")
			results = append(results, res)
			continue
		}

		present, checkErr := dep.Check(ctx, exec, version)
		if checkErr != nil {
			log.Debug().Err(checkErr).Str("dependency", name).Str("version", version).Msg("dependency check errored; will attempt install")
		}
		if present {
			res.Status = "already-present"
			results = append(results, res)
			continue
		}

		if err := dep.Install(ctx, exec, version); err != nil {
			res.Status = "failed"
			res.Err = err
			log.Warn().Err(err).Str("dependency", name).Str("version", version).Msg("sandbox dependency install failed")
			results = append(results, res)
			continue
		}
		res.Status = "installed"
		results = append(results, res)
	}

	logSummary(log, results)
	return results
}

func logSummary(log zerolog.Logger, results []Result) {
	var installed, present, failed, unknown int
	var failures []string
	for _, r := range results {
		switch r.Status {
		case "installed":
			installed++
		case "already-present":
			present++
		case "failed":
			failed++
			failures = append(failures, fmt.Sprintf("%s@%s: %v", r.Name, r.Version, r.Err))
		case "unknown":
			unknown++
			failures = append(failures, fmt.Sprintf("%s@%s: unknown dependency", r.Name, r.Version))
		}
	}
	event := log.Info().
		Int("installed", installed).
		Int("already_present", present).
		Int("failed", failed).
		Int("unknown", unknown)
	if len(failures) > 0 {
		event = event.Str("failures", strings.Join(failures, "; "))
	}
	event.Msg("sandbox dependencies applied")
}

// captureExec is a small helper for recipe authors: runs cmd, returns
// (stdoutString, exitCode, err). Discards stderr unless capture-stderr is
// also requested via captureExecStderr.
func captureExec(ctx context.Context, exec Executor, cmd string) (string, int, error) {
	var stdout bytes.Buffer
	code, err := exec(ctx, cmd, &stdout, io.Discard)
	return stdout.String(), code, err
}

func captureExecStderr(ctx context.Context, exec Executor, cmd string) (string, string, int, error) {
	var stdout, stderr bytes.Buffer
	code, err := exec(ctx, cmd, &stdout, &stderr)
	return stdout.String(), stderr.String(), code, err
}

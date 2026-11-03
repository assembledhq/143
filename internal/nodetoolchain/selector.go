// Package nodetoolchain selects a preinstalled Node/npm pair from repository
// declarations. It never executes repository configuration or downloads tools.
package nodetoolchain

import (
	"embed"
	"encoding/json"
	"errors"
	"path"
	"regexp"
	"strings"

	"github.com/Masterminds/semver/v3"
)

//go:embed catalog.json
var catalogFS embed.FS

// Toolchain identifies one immutable, preinstalled runtime pair.
type Toolchain struct {
	ID          string `json:"id"`
	NodeVersion string `json:"node_version"`
	NPMVersion  string `json:"npm_version"`
}

func (t Toolchain) BinDir() string   { return "/opt/143/node/" + t.NodeVersion + "/bin" }
func (t Toolchain) NodePath() string { return t.BinDir() + "/node" }
func (t Toolchain) NPMPath() string {
	return "/opt/143/node/" + t.NodeVersion + "/lib/node_modules/npm/bin/npm-cli.js"
}

var (
	ErrInvalidDeclaration   = errors.New("invalid repository Node/npm toolchain declaration")
	ErrUnsupportedToolchain = errors.New("repository Node/npm requirements do not match a supported toolchain")
	versionFilePattern      = regexp.MustCompile(`^v?(0|[1-9][0-9]*)(\.(0|[1-9][0-9]*)){0,2}$`)
	packageManagerPattern   = regexp.MustCompile(`^npm@((0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?)(\+sha(224|256|384|512)\.([a-f0-9]+))?$`)
)

type requirement struct {
	node       bool
	constraint *semver.Constraints
}

// Select intersects declarations from the repository root and every explicitly
// configured target, then returns the first compatible catalog entry. With no
// declarations the first entry is the default. Error messages contain no input.
func Select(files map[string]string, directories []string) (Toolchain, error) {
	var requirements []requirement
	seen := map[string]bool{}
	roots := append([]string{"."}, directories...)
	for _, directory := range roots {
		if directory == "" || path.IsAbs(directory) || path.Clean(directory) != directory || directory == ".." || strings.HasPrefix(directory, "../") || strings.Contains(directory, "\\") {
			return Toolchain{}, ErrInvalidDeclaration
		}
		if seen[directory] {
			continue
		}
		seen[directory] = true
		if raw, ok := files[path.Join(directory, "package.json")]; ok {
			parsed, err := manifestRequirements(raw)
			if err != nil {
				return Toolchain{}, err
			}
			requirements = append(requirements, parsed...)
		}
		for _, name := range []string{".nvmrc", ".node-version"} {
			if raw, ok := files[path.Join(directory, name)]; ok {
				value := strings.TrimSpace(raw)
				if !versionFilePattern.MatchString(value) {
					return Toolchain{}, ErrInvalidDeclaration
				}
				constraint, err := semver.NewConstraint(value)
				if err != nil {
					return Toolchain{}, ErrInvalidDeclaration
				}
				requirements = append(requirements, requirement{node: true, constraint: constraint})
			}
		}
	}
	raw, err := catalogFS.ReadFile("catalog.json")
	if err != nil {
		return Toolchain{}, errors.New("supported Node/npm toolchain catalog unavailable")
	}
	var catalog struct {
		Toolchains []Toolchain `json:"toolchains"`
	}
	if json.Unmarshal(raw, &catalog) != nil {
		return Toolchain{}, errors.New("supported Node/npm toolchain catalog invalid")
	}
	for _, candidate := range catalog.Toolchains {
		node, nodeErr := semver.StrictNewVersion(candidate.NodeVersion)
		npm, npmErr := semver.StrictNewVersion(candidate.NPMVersion)
		if nodeErr != nil || npmErr != nil {
			return Toolchain{}, errors.New("supported Node/npm toolchain catalog invalid")
		}
		compatible := true
		for _, requirement := range requirements {
			version := npm
			if requirement.node {
				version = node
			}
			if !requirement.constraint.Check(version) {
				compatible = false
				break
			}
		}
		if compatible {
			return candidate, nil
		}
	}
	return Toolchain{}, ErrUnsupportedToolchain
}

func manifestRequirements(raw string) ([]requirement, error) {
	var manifest map[string]json.RawMessage
	if len(raw) > 4*1024*1024 || json.Unmarshal([]byte(raw), &manifest) != nil || manifest == nil {
		return nil, ErrInvalidDeclaration
	}
	var result []requirement
	if enginesRaw, ok := manifest["engines"]; ok {
		var engines map[string]json.RawMessage
		if json.Unmarshal(enginesRaw, &engines) != nil || engines == nil {
			return nil, ErrInvalidDeclaration
		}
		for _, name := range []string{"node", "npm"} {
			if valueRaw, ok := engines[name]; ok {
				var value string
				if json.Unmarshal(valueRaw, &value) != nil || strings.TrimSpace(value) == "" || len(value) > 256 {
					return nil, ErrInvalidDeclaration
				}
				constraint, err := semver.NewConstraint(value)
				if err != nil {
					return nil, ErrInvalidDeclaration
				}
				result = append(result, requirement{node: name == "node", constraint: constraint})
			}
		}
	}
	if managerRaw, ok := manifest["packageManager"]; ok {
		var manager string
		if json.Unmarshal(managerRaw, &manager) != nil || len(manager) > 512 {
			return nil, ErrInvalidDeclaration
		}
		match := packageManagerPattern.FindStringSubmatch(manager)
		if match == nil {
			return nil, ErrInvalidDeclaration
		}
		if match[6] != "" {
			lengths := map[string]int{"224": 56, "256": 64, "384": 96, "512": 128}
			if len(match[8]) != lengths[match[7]] {
				return nil, ErrInvalidDeclaration
			}
		}
		if _, err := semver.StrictNewVersion(match[1]); err != nil {
			return nil, ErrInvalidDeclaration
		}
		constraint, err := semver.NewConstraint("=" + match[1])
		if err != nil {
			return nil, ErrInvalidDeclaration
		}
		result = append(result, requirement{constraint: constraint})
	}
	return result, nil
}

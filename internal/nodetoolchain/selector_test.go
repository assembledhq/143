package nodetoolchain

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSelect(t *testing.T) {
	t.Parallel()
	defaultToolchain := Toolchain{ID: "node24-npm11", NodeVersion: "24.22.2", NPMVersion: "11.19.1"}
	node22 := Toolchain{ID: "node22-npm10", NodeVersion: "22.23.4", NPMVersion: "10.9.9"}
	node22NPM11 := Toolchain{ID: "node22-npm11", NodeVersion: "22.23.4", NPMVersion: "11.21.1", Directory: "22.23.4-npm11.21.1"}
	tests := []struct {
		name        string
		files       map[string]string
		directories []string
		expected    Toolchain
		expectedErr error
	}{
		{name: "no declarations selects default", expected: defaultToolchain},
		{name: "empty manifest selects default", files: map[string]string{"package.json": `{}`}, expected: defaultToolchain},
		{name: "repository desired versions", files: map[string]string{"package.json": `{"engines":{"node":"^22","npm":"^10"}}`}, expected: node22},
		{
			name: "Node 22 and npm 11 for root and nested setup targets",
			files: map[string]string{
				".nvmrc":                "v22.23.4\n",
				"package.json":          `{"engines":{"node":"^22","npm":"^11"},"packageManager":"npm@11.21.1"}`,
				"frontend/package.json": `{"engines":{"node":"^22","npm":"^11"}}`,
			},
			directories: []string{".", "frontend"},
			expected:    node22NPM11,
		},
		{name: "compatible ranges use ordered default", files: map[string]string{"package.json": `{"engines":{"node":">=22 <25","npm":">=10 <12"}}`}, expected: defaultToolchain},
		{name: "range alternatives", files: map[string]string{"package.json": `{"engines":{"node":"^22 || ^24"}}`}, expected: defaultToolchain},
		{name: "root and target intersect", files: map[string]string{"package.json": `{"engines":{"node":">=22"}}`, "frontend/package.json": `{"engines":{"npm":"10.x"}}`}, directories: []string{"frontend"}, expected: node22},
		{name: "unconfigured workspace ignored", files: map[string]string{"ignored/package.json": `{"engines":{"node":"^18"}}`}, expected: defaultToolchain},
		{name: "duplicate root target", files: map[string]string{"package.json": `{"engines":{"node":"22"}}`}, directories: []string{".", "."}, expected: node22},
		{name: "nvm major", files: map[string]string{".nvmrc": "v22\n"}, expected: node22},
		{name: "version file exact", files: map[string]string{".node-version": "22.23.4\n"}, expected: node22},
		{name: "target version file", files: map[string]string{"frontend/.node-version": "24.22"}, directories: []string{"frontend"}, expected: defaultToolchain},
		{name: "npm exact pin", files: map[string]string{"package.json": `{"packageManager":"npm@10.9.9"}`}, expected: node22},
		{name: "npm integrity pin", files: map[string]string{"package.json": `{"packageManager":"npm@10.9.9+sha512.` + strings.Repeat("a", 128) + `"}`}, expected: node22},
		{name: "conflicting root and target", files: map[string]string{"package.json": `{"engines":{"node":"^24"}}`, "frontend/package.json": `{"engines":{"node":"^22"}}`}, directories: []string{"frontend"}, expectedErr: ErrUnsupportedToolchain},
		{name: "conflicting version files", files: map[string]string{".nvmrc": "22", ".node-version": "24"}, expectedErr: ErrUnsupportedToolchain},
		{name: "npm conflict", files: map[string]string{"package.json": `{"engines":{"npm":"^11"},"packageManager":"npm@10.9.9"}`}, expectedErr: ErrUnsupportedToolchain},
		{name: "exact unavailable node", files: map[string]string{".nvmrc": "22.21.0"}, expectedErr: ErrUnsupportedToolchain},
		{name: "exact unavailable npm", files: map[string]string{"package.json": `{"packageManager":"npm@10.9.4"}`}, expectedErr: ErrUnsupportedToolchain},
		{name: "other package manager", files: map[string]string{"package.json": `{"packageManager":"pnpm@10.9.9"}`}, expectedErr: ErrInvalidDeclaration},
		{name: "package manager range", files: map[string]string{"package.json": `{"packageManager":"npm@^10"}`}, expectedErr: ErrInvalidDeclaration},
		{name: "noncanonical integrity separator", files: map[string]string{"package.json": `{"packageManager":"npm@10.9.9#sha512.` + strings.Repeat("a", 128) + `"}`}, expectedErr: ErrInvalidDeclaration},
		{name: "short integrity", files: map[string]string{"package.json": `{"packageManager":"npm@10.9.9+sha512.abc"}`}, expectedErr: ErrInvalidDeclaration},
		{name: "invalid engine range", files: map[string]string{"package.json": `{"engines":{"node":"not-a-version"}}`}, expectedErr: ErrInvalidDeclaration},
		{name: "engine shell text", files: map[string]string{"package.json": `{"engines":{"node":"22; echo secret"}}`}, expectedErr: ErrInvalidDeclaration},
		{name: "engine null", files: map[string]string{"package.json": `{"engines":{"node":null}}`}, expectedErr: ErrInvalidDeclaration},
		{name: "engine array", files: map[string]string{"package.json": `{"engines":{"node":["22"]}}`}, expectedErr: ErrInvalidDeclaration},
		{name: "engines wrong type", files: map[string]string{"package.json": `{"engines":"22"}`}, expectedErr: ErrInvalidDeclaration},
		{name: "invalid manifest", files: map[string]string{"package.json": `not json`}, expectedErr: ErrInvalidDeclaration},
		{name: "manifest null", files: map[string]string{"package.json": `null`}, expectedErr: ErrInvalidDeclaration},
		{name: "nvm alias rejected", files: map[string]string{".nvmrc": "lts/*"}, expectedErr: ErrInvalidDeclaration},
		{name: "nvm shell substitution rejected", files: map[string]string{".nvmrc": "$(echo 22)"}, expectedErr: ErrInvalidDeclaration},
		{name: "nvm multiline rejected", files: map[string]string{".nvmrc": "22\n24"}, expectedErr: ErrInvalidDeclaration},
		{name: "nvm unavailable minor", files: map[string]string{".nvmrc": "22.21"}, expectedErr: ErrUnsupportedToolchain},
		{name: "version file leading zero rejected", files: map[string]string{".node-version": "022"}, expectedErr: ErrInvalidDeclaration},
		{name: "target escapes root", directories: []string{"../private"}, expectedErr: ErrInvalidDeclaration},
		{name: "target absolute", directories: []string{"/private"}, expectedErr: ErrInvalidDeclaration},
		{name: "target unclean", directories: []string{"frontend/../frontend"}, expectedErr: ErrInvalidDeclaration},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			actual, err := Select(tt.files, tt.directories)
			if tt.expectedErr != nil {
				require.ErrorIs(t, err, tt.expectedErr, "invalid or unsatisfied declarations should retain a safe error class")
				require.Equal(t, tt.expectedErr.Error(), err.Error(), "failure messages should contain no repository values")
			} else {
				require.NoError(t, err, "compatible declarations should select an installed toolchain")
			}
			require.Equal(t, tt.expected, actual, "selection should match the complete expected toolchain")
		})
	}
}

func TestToolchainPaths(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, npmVersion, directory string
	}{
		{name: "bundled npm", npmVersion: "10.9.9", directory: "22.23.4"},
		{name: "alternate npm", npmVersion: "11.21.1", directory: "22.23.4-npm11.21.1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			selected, err := Select(map[string]string{
				".nvmrc":       "22.23.4",
				"package.json": `{"packageManager":"npm@` + tt.npmVersion + `"}`,
			}, nil)
			require.NoError(t, err, "the exact Node and npm pair should be selectable")
			prefix := "/opt/143/node/" + tt.directory
			require.Equal(t, prefix+"/bin", selected.BinDir(), "PATH should reference the selected immutable runtime")
			require.Equal(t, prefix+"/bin/node", selected.NodePath(), "execution should use the selected Node binary")
			require.Equal(t, prefix+"/lib/node_modules/npm/bin/npm-cli.js", selected.NPMPath(), "npm should be executed explicitly by the selected Node binary")
		})
	}
}

func TestCatalog(t *testing.T) {
	t.Parallel()
	raw, err := catalogFS.ReadFile("catalog.json")
	require.NoError(t, err, "embedded catalog must be available to selection and image provisioning")
	var catalog struct {
		Toolchains []struct {
			Toolchain
			SHA256 map[string]string `json:"sha256"`
		} `json:"toolchains"`
	}
	require.NoError(t, json.Unmarshal(raw, &catalog), "catalog should be valid JSON")
	require.Equal(t, 3, len(catalog.Toolchains), "catalog should contain only the reviewed runtime pairs")
	hashes := regexp.MustCompile(`^[a-f0-9]{64}$`)
	paths := make(map[string]bool, len(catalog.Toolchains))
	for _, entry := range catalog.Toolchains {
		require.False(t, paths[entry.BinDir()], "each runtime pair should have its own installation directory")
		paths[entry.BinDir()] = true
		t.Run(entry.ID, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, 2, len(entry.SHA256), "each runtime should pin both supported Linux architectures")
			require.Regexp(t, hashes, entry.SHA256["linux-x64"], "x64 archive should have a pinned SHA256")
			require.Regexp(t, hashes, entry.SHA256["linux-arm64"], "arm64 archive should have a pinned SHA256")
			selected, err := Select(map[string]string{
				".node-version": entry.NodeVersion,
				"package.json":  `{"packageManager":"npm@` + entry.NPMVersion + `"}`,
			}, nil)
			require.NoError(t, err, "each catalog runtime should be selectable by its exact Node and npm versions")
			require.Equal(t, entry.Toolchain, selected, "the exact catalog runtime should round-trip")
		})
	}
}

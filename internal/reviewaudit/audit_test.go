package reviewaudit

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func newAuditRepository(config string) Repository {
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(config)))
	return Repository{
		Repository: "example/repo", Revision: strings.Repeat("a", 40), ConfigPresent: config != "", ConfigJSON: config,
		ConfigSHA256: hash, Owner: "repository-maintainers", RequiredEvidence: []EvidenceCategory{"git"},
	}
}

func reviewedRecipe(repo Repository, coverage ...EvidenceCategory) *RecipeReview {
	return &RecipeReview{Revision: repo.Revision, ConfigSHA256: repo.ConfigSHA256, Evidence: "private recipe review reference", PreservesTrackedFiles: true, SetupOnly: true, CoveredEvidence: coverage}
}

func TestAuditRecipeGates(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                 string
		config               string
		change               func(*Repository)
		tools                []string
		bootstrap            int
		reviewCommands       int
		verificationCommands int
		declarations         []EvidenceDeclaration
		required             []EvidenceCategory
		covered              []EvidenceCategory
		issues               []string
	}{
		{name: "explicit git-only repo needs no invented bootstrap", required: []EvidenceCategory{"git"}, covered: []EvidenceCategory{"git"}, issues: []string{}},
		{
			name: "reviewed pinned setup covers explicit sources", config: `{"dependencies":{"golangci-lint":"2.1.6"},"review":{"commands":["bash .143/review.sh"],"verify_commands":["bash .143/review.sh --verify"],"evidence":[{"path":"node_modules","description":"Locked JavaScript sources","category":"javascript_dependencies"},{"path":".143/cache/go","description":"Locked Go sources","category":"go_dependencies"}]}}`,
			change: func(r *Repository) {
				r.RequiredEvidence = []EvidenceCategory{"javascript_dependencies", "git", "go_dependencies"}
				r.RecipeReview = reviewedRecipe(*r, "javascript_dependencies", "go_dependencies")
			},
			tools: []string{"golangci-lint"}, reviewCommands: 1, verificationCommands: 1, declarations: []EvidenceDeclaration{{Path: ".143/cache/go", Category: "go_dependencies"}, {Path: "node_modules", Category: "javascript_dependencies"}}, required: []EvidenceCategory{"git", "go_dependencies", "javascript_dependencies"}, covered: []EvidenceCategory{"git", "go_dependencies", "javascript_dependencies"}, issues: []string{},
		},
		{name: "unknown owner closes gate", change: func(r *Repository) { r.Owner = "" }, required: []EvidenceCategory{"git"}, covered: []EvidenceCategory{"git"}, issues: []string{"a recipe/config owner is required"}},
		{name: "placeholder owner cannot open gate", change: func(r *Repository) { r.Owner = "TBD" }, required: []EvidenceCategory{"git"}, covered: []EvidenceCategory{"git"}, issues: []string{"a recipe/config owner is required"}},
		{
			name: "preview install does not count as review source setup", config: `{"preview":{"install":{"command":["npm","ci"]}}}`,
			change:   func(r *Repository) { r.RequiredEvidence = []EvidenceCategory{"git", "javascript_dependencies"} },
			required: []EvidenceCategory{"git", "javascript_dependencies"}, covered: []EvidenceCategory{"git"}, issues: []string{"a revision-bound review-safe recipe attestation is required", "missing explicit review recipe for source/artifact evidence: javascript_dependencies"},
		},
		{
			name: "git-only bootstrap still needs source safety review", config: `{"bootstrap":{"commands":["bash setup.sh"]}}`,
			bootstrap: 1, required: []EvidenceCategory{"git"}, covered: []EvidenceCategory{"git"}, issues: []string{"publish an explicit review section before worker rollout; legacy bootstrap cannot reuse review readiness", "a revision-bound review-safe recipe attestation is required"},
		},
		{
			name: "attested git-only legacy bootstrap still closes rollout gate", config: `{"bootstrap":{"commands":["true"]}}`,
			change:    func(r *Repository) { r.RecipeReview = reviewedRecipe(*r, "git") },
			bootstrap: 1, required: []EvidenceCategory{"git"}, covered: []EvidenceCategory{"git"}, issues: []string{"publish an explicit review section before worker rollout; legacy bootstrap cannot reuse review readiness"},
		},
		{
			name: "explicit empty review bypasses ordinary bootstrap for git-only review", config: `{"bootstrap":{"commands":["go test ./..."]},"review":{}}`,
			bootstrap: 1, required: []EvidenceCategory{"git"}, covered: []EvidenceCategory{"git"}, issues: []string{},
		},
		{
			name: "unsupported tool is a config blocker", config: `{"dependencies":{"unknown-tool":"1.0.0"}}`,
			change: func(r *Repository) { r.RecipeReview = reviewedRecipe(*r, "git") },
			tools:  []string{"unknown-tool"}, required: []EvidenceCategory{"git"}, covered: []EvidenceCategory{"git"}, issues: []string{"declared tools are unsupported: required sandbox dependency unknown-tool: no install recipe registered"},
		},
		{name: "misspelled top-level setup key fails", config: `{"boostrap":{"commands":["true"]}}`, required: []EvidenceCategory{"git"}, covered: []EvidenceCategory{"git"}, issues: []string{`config schema is invalid: json: unknown field "boostrap"`}},
		{name: "null config is not a setup declaration", config: `null`, required: []EvidenceCategory{"git"}, covered: []EvidenceCategory{"git"}, issues: []string{"config schema must be a JSON object"}},
		{name: "misspelled setup section key fails", config: `{"bootstrap":{"command":["true"]}}`, required: []EvidenceCategory{"git"}, covered: []EvidenceCategory{"git"}, issues: []string{`config schema is invalid: json: unknown field "command"`}},
		{
			name: "invalid config cannot report partially decoded source coverage", config: `{"review":{"verify_commands":["true"],"evidence":[{"path":"vendor/gems","description":"Ruby sources","category":"ruby_dependencies"}]},"typo":true}`,
			change: func(r *Repository) {
				r.RequiredEvidence = []EvidenceCategory{"git", "ruby_dependencies"}
				r.RecipeReview = reviewedRecipe(*r, "ruby_dependencies")
			},
			required: []EvidenceCategory{"git", "ruby_dependencies"}, covered: []EvidenceCategory{"git"}, issues: []string{`config schema is invalid: json: unknown field "typo"`, "missing explicit review recipe for source/artifact evidence: ruby_dependencies"},
		},
		{
			name: "stale attestation cannot establish coverage", config: `{"review":{"commands":["true"],"verify_commands":["true"],"evidence":[{"path":".143/cache/go","description":"Go sources","category":"go_dependencies"}]}}`,
			change: func(r *Repository) {
				r.RequiredEvidence = []EvidenceCategory{"git", "go_dependencies"}
				r.RecipeReview = reviewedRecipe(*r, "go_dependencies")
				r.RecipeReview.Revision = strings.Repeat("b", 40)
			},
			reviewCommands: 1, verificationCommands: 1, declarations: []EvidenceDeclaration{{Path: ".143/cache/go", Category: "go_dependencies"}}, required: []EvidenceCategory{"git", "go_dependencies"}, covered: []EvidenceCategory{"git"}, issues: []string{"recipe review must cite evidence and match the repository revision and config hash", "recipe review does not establish required evidence: go_dependencies"},
		},
		{
			name: "snapshot hash cannot be changed", change: func(r *Repository) { r.ConfigSHA256 = strings.Repeat("0", 64) },
			required: []EvidenceCategory{"git"}, covered: []EvidenceCategory{"git"}, issues: []string{"config snapshot presence/hash is inconsistent"},
		},
		{
			name: "tracked source changes disqualify recipe", config: `{"bootstrap":{"commands":["true"]}}`, change: func(r *Repository) {
				r.RecipeReview = reviewedRecipe(*r, "git")
				r.RecipeReview.PreservesTrackedFiles = false
			},
			bootstrap: 1, required: []EvidenceCategory{"git"}, covered: []EvidenceCategory{"git"}, issues: []string{"publish an explicit review section before worker rollout; legacy bootstrap cannot reuse review readiness", "recipe review must verify that setup preserves tracked files"},
		},
		{
			name: "validation commands cannot be attested as review setup", config: `{"bootstrap":{"commands":["go test ./..."]}}`,
			change:    func(r *Repository) { r.RecipeReview = reviewedRecipe(*r, "git"); r.RecipeReview.SetupOnly = false },
			bootstrap: 1, required: []EvidenceCategory{"git"}, covered: []EvidenceCategory{"git"}, issues: []string{"publish an explicit review section before worker rollout; legacy bootstrap cannot reuse review readiness", "recipe review must verify setup does not run tests, builds, or services"},
		},
		{
			name: "duplicate categories fail", change: func(r *Repository) { r.RequiredEvidence = []EvidenceCategory{"git", "git"} },
			required: []EvidenceCategory{"git", "git"}, covered: []EvidenceCategory{"git"}, issues: []string{"required evidence: categories must be recognized and unique"},
		},
		{
			name: "generated artifacts are separately required", config: `{"review":{"commands":["true"],"verify_commands":["true"],"evidence":[{"path":"node_modules","description":"JavaScript sources","category":"javascript_dependencies"}]}}`, change: func(r *Repository) {
				r.RequiredEvidence = []EvidenceCategory{"git", "generated_artifacts"}
				r.RecipeReview = reviewedRecipe(*r, "javascript_dependencies")
			},
			reviewCommands: 1, verificationCommands: 1, declarations: []EvidenceDeclaration{{Path: "node_modules", Category: "javascript_dependencies"}}, required: []EvidenceCategory{"generated_artifacts", "git"}, covered: []EvidenceCategory{"git", "javascript_dependencies"}, issues: []string{"review recipe must declare in-repo evidence paths for: generated_artifacts"},
		},
		{
			name: "legacy bootstrap attestation cannot hide an external cache", config: `{"bootstrap":{"commands":["npm ci && go mod download"]}}`, change: func(r *Repository) {
				r.RequiredEvidence = []EvidenceCategory{"git", "go_dependencies"}
				r.RecipeReview = reviewedRecipe(*r, "go_dependencies")
			},
			bootstrap: 1, required: []EvidenceCategory{"git", "go_dependencies"}, covered: []EvidenceCategory{"git"}, issues: []string{"publish an explicit review section before worker rollout; legacy bootstrap cannot reuse review readiness", "missing explicit review recipe for source/artifact evidence: go_dependencies"},
		},
		{
			name: "source declaration without verifier closes gate", config: `{"review":{"commands":["true"],"evidence":[{"path":".143/cache/go","description":"Go sources","category":"go_dependencies"}]}}`, change: func(r *Repository) {
				r.RequiredEvidence = []EvidenceCategory{"git", "go_dependencies"}
				r.RecipeReview = reviewedRecipe(*r, "go_dependencies")
			},
			reviewCommands: 1, declarations: []EvidenceDeclaration{{Path: ".143/cache/go", Category: "go_dependencies"}}, required: []EvidenceCategory{"git", "go_dependencies"}, covered: []EvidenceCategory{"git"}, issues: []string{"review recipe must declare local verification commands for: go_dependencies"},
		},
		{
			name: "review recipe overrides ordinary validation bootstrap", config: `{"bootstrap":{"commands":["go test ./..."]},"review":{"commands":["true"],"verify_commands":["true"],"evidence":[{"path":".143/cache/go","description":"Go sources","category":"go_dependencies"}]}}`, change: func(r *Repository) {
				r.RequiredEvidence = []EvidenceCategory{"git", "go_dependencies"}
				r.RecipeReview = reviewedRecipe(*r, "go_dependencies")
			},
			bootstrap: 1, reviewCommands: 1, verificationCommands: 1, declarations: []EvidenceDeclaration{{Path: ".143/cache/go", Category: "go_dependencies"}}, required: []EvidenceCategory{"git", "go_dependencies"}, covered: []EvidenceCategory{"git", "go_dependencies"}, issues: []string{},
		},
		{
			name: "Ruby source evidence is explicitly required", config: `{"review":{"verify_commands":["true"],"evidence":[{"path":"vendor/gems","description":"Vendored Ruby sources","category":"ruby_dependencies"}]}}`, change: func(r *Repository) {
				r.RequiredEvidence = []EvidenceCategory{"git", "ruby_dependencies"}
				r.RecipeReview = reviewedRecipe(*r, "ruby_dependencies")
			},
			verificationCommands: 1, declarations: []EvidenceDeclaration{{Path: "vendor/gems", Category: "ruby_dependencies"}}, required: []EvidenceCategory{"git", "ruby_dependencies"}, covered: []EvidenceCategory{"git", "ruby_dependencies"}, issues: []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := newAuditRepository(tt.config)
			if tt.change != nil {
				tt.change(&repo)
			}
			tools := tt.tools
			if tools == nil {
				tools = []string{}
			}
			expected := Report{Ready: len(tt.issues) == 0, Repositories: []RepositoryReport{{
				Repository: repo.Repository, Revision: repo.Revision, ConfigSHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(repo.ConfigJSON))), Owner: repo.Owner,
				Ready: len(tt.issues) == 0, BootstrapCommandCount: tt.bootstrap, ReviewCommandCount: tt.reviewCommands, VerificationCommandCount: tt.verificationCommands, DeclaredEvidence: tt.declarations, DeclaredTools: tools, RequiredEvidence: tt.required, CoveredEvidence: tt.covered, Issues: tt.issues,
			}}}
			require.Equal(t, expected, Audit(Inventory{Version: 1, Repositories: []Repository{repo}}), "audit should establish only reviewed declarations and explicit evidence coverage")
		})
	}
}

func TestDecodeInventoryRejectsInvalidReleaseTargets(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, raw, expected string }{
		{name: "empty targets", raw: `{"version":1,"repositories":[]}`, expected: "audit requires version 1 and 1 to 100 explicit repositories"},
		{name: "unknown manifest field", raw: `{"version":1,"repositories":[],"typo":true}`, expected: `decode audit inventory: json: unknown field "typo"`},
		{name: "trailing JSON", raw: `{"version":1,"repositories":[]} {}`, expected: "decode audit inventory: expected exactly one JSON value"},
		{name: "branch ref instead of pinned revision", raw: `{"version":1,"repositories":[{"repository":"example/repo","revision":"main"}]}`, expected: "audit repository identity must be unique and pinned to a complete Git object ID"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := DecodeInventory(strings.NewReader(tt.raw))
			require.EqualError(t, err, tt.expected, "invalid release target inventory should fail before a readiness report")
		})
	}
}

func TestAuditReportHasDeterministicRepositoryOrder(t *testing.T) {
	t.Parallel()
	a, b := newAuditRepository(""), newAuditRepository("")
	a.Repository, b.Repository = "example/z", "example/a"
	inventory, err := json.Marshal(Inventory{Version: 1, Repositories: []Repository{a, b}})
	require.NoError(t, err, "explicit inventory should encode")
	decoded, err := DecodeInventory(strings.NewReader(string(inventory)))
	require.NoError(t, err, "pinned inventory should decode")
	actual := Audit(decoded)
	require.Equal(t, []string{"example/a", "example/z"}, []string{actual.Repositories[0].Repository, actual.Repositories[1].Repository}, "reports should sort repositories regardless of input order")
}

// Package reviewaudit checks pinned repository setup declarations before a
// code-review rollout. It never executes repository commands or treats a
// declaration as proof of installed dependencies.
package reviewaudit

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/repoconfig"
	"github.com/assembledhq/143/internal/sandboxdeps"
)

const MaxInventoryBytes = 2 * 1024 * 1024

var repositoryName = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
var objectID = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

type EvidenceCategory = models.CodeReviewEvidenceCategory

// Inventory is a private, explicit release target list. ConfigJSON preserves
// the original snapshot bytes so the review attestation can bind to its hash.
type Inventory struct {
	Version      int          `json:"version"`
	Repositories []Repository `json:"repositories"`
}

type Repository struct {
	Repository       string             `json:"repository"`
	Revision         string             `json:"revision"`
	ConfigPresent    bool               `json:"config_present"`
	ConfigJSON       string             `json:"config_json"`
	ConfigSHA256     string             `json:"config_sha256"`
	Owner            string             `json:"owner"`
	RequiredEvidence []EvidenceCategory `json:"required_evidence"`
	RecipeReview     *RecipeReview      `json:"recipe_review,omitempty"`
}

// RecipeReview records an explicit human/source review, not an approval that
// this command can infer from shell text. Evidence should reference that review.
type RecipeReview struct {
	Revision              string             `json:"revision"`
	ConfigSHA256          string             `json:"config_sha256"`
	Evidence              string             `json:"evidence"`
	PreservesTrackedFiles bool               `json:"preserves_tracked_files"`
	SetupOnly             bool               `json:"setup_only"`
	CoveredEvidence       []EvidenceCategory `json:"covered_evidence"`
}

type Report struct {
	Ready        bool               `json:"ready"`
	Repositories []RepositoryReport `json:"repositories"`
}

type RepositoryReport struct {
	Repository               string                `json:"repository"`
	Revision                 string                `json:"revision"`
	ConfigSHA256             string                `json:"config_sha256"`
	Owner                    string                `json:"owner"`
	Ready                    bool                  `json:"ready"`
	BootstrapCommandCount    int                   `json:"bootstrap_command_count"`
	ReviewCommandCount       int                   `json:"review_command_count,omitempty"`
	VerificationCommandCount int                   `json:"verification_command_count,omitempty"`
	DeclaredEvidence         []EvidenceDeclaration `json:"declared_evidence,omitempty"`
	DeclaredTools            []string              `json:"declared_tools"`
	RequiredEvidence         []EvidenceCategory    `json:"required_evidence"`
	CoveredEvidence          []EvidenceCategory    `json:"covered_evidence"`
	Issues                   []string              `json:"issues"`
}

type EvidenceDeclaration struct {
	Path     string           `json:"path"`
	Category EvidenceCategory `json:"category"`
}

func DecodeInventory(reader io.Reader) (Inventory, error) {
	data, err := io.ReadAll(io.LimitReader(reader, MaxInventoryBytes+1))
	if err != nil {
		return Inventory{}, fmt.Errorf("read audit inventory: %w", err)
	}
	if len(data) > MaxInventoryBytes {
		return Inventory{}, fmt.Errorf("audit inventory exceeds %d bytes", MaxInventoryBytes)
	}
	var inventory Inventory
	if err := decodeStrict(data, &inventory); err != nil {
		return Inventory{}, fmt.Errorf("decode audit inventory: %w", err)
	}
	if inventory.Version != 1 || len(inventory.Repositories) == 0 || len(inventory.Repositories) > 100 {
		return Inventory{}, fmt.Errorf("audit requires version 1 and 1 to 100 explicit repositories")
	}
	seen := make(map[string]bool, len(inventory.Repositories))
	for _, repo := range inventory.Repositories {
		key := strings.ToLower(repo.Repository)
		if !repositoryName.MatchString(repo.Repository) || !objectID.MatchString(repo.Revision) || seen[key] {
			return Inventory{}, fmt.Errorf("audit repository identity must be unique and pinned to a complete Git object ID")
		}
		seen[key] = true
	}
	return inventory, nil
}

func Audit(inventory Inventory) Report {
	report := Report{Ready: len(inventory.Repositories) > 0, Repositories: make([]RepositoryReport, 0, len(inventory.Repositories))}
	for _, repo := range inventory.Repositories {
		result := auditRepository(repo)
		report.Repositories = append(report.Repositories, result)
		report.Ready = report.Ready && result.Ready
	}
	slices.SortFunc(report.Repositories, func(a, b RepositoryReport) int { return strings.Compare(a.Repository, b.Repository) })
	return report
}

func auditRepository(repo Repository) RepositoryReport {
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(repo.ConfigJSON)))
	result := RepositoryReport{
		Repository: repo.Repository, Revision: repo.Revision, ConfigSHA256: hash, Owner: strings.TrimSpace(repo.Owner),
		DeclaredTools: []string{}, RequiredEvidence: slices.Clone(repo.RequiredEvidence), CoveredEvidence: []EvidenceCategory{models.CodeReviewEvidenceGit}, Issues: []string{},
	}
	issue := func(message string) { result.Issues = append(result.Issues, message) }
	if repo.ConfigSHA256 != hash || (!repo.ConfigPresent && repo.ConfigJSON != "") {
		issue("config snapshot presence/hash is inconsistent")
	}
	if missingReference(result.Owner) || len(result.Owner) > 200 {
		issue("a recipe/config owner is required")
	}
	if err := validateCategories(repo.RequiredEvidence); err != nil {
		issue("required evidence: " + err.Error())
	}
	if len(repo.RequiredEvidence) == 0 {
		issue("required evidence must explicitly include git and any dependency/artifact requirements")
	} else if !slices.Contains(repo.RequiredEvidence, "git") {
		issue("required evidence must include git comparison files")
	}
	var cfg repoconfig.Config
	if repo.ConfigPresent {
		if !strings.HasPrefix(strings.TrimSpace(repo.ConfigJSON), "{") {
			issue("config schema must be a JSON object")
		} else if len(repo.ConfigJSON) > 64*1024 {
			issue("config snapshot exceeds 64 KiB")
		} else if err := decodeStrict([]byte(repo.ConfigJSON), &cfg); err != nil {
			issue("config schema is invalid: " + err.Error())
			cfg = repoconfig.Config{}
		} else {
			var err error
			cfg, err = repoconfig.Parse([]byte(repo.ConfigJSON))
			if err != nil {
				issue("config setup is invalid: " + err.Error())
			}
		}
	}
	result.BootstrapCommandCount = len(cfg.Bootstrap.Commands)
	if result.BootstrapCommandCount > 0 && cfg.Review == nil {
		issue("publish an explicit review section before worker rollout; legacy bootstrap cannot reuse review readiness")
	}
	preparationCommands := cfg.Bootstrap.Commands
	if cfg.Review != nil {
		preparationCommands = cfg.Review.Commands
		result.ReviewCommandCount = len(cfg.Review.Commands)
		result.VerificationCommandCount = len(cfg.Review.VerifyCommands)
		for _, evidence := range cfg.Review.Evidence {
			result.DeclaredEvidence = append(result.DeclaredEvidence, EvidenceDeclaration{Path: evidence.Path, Category: evidence.Category})
		}
		slices.SortFunc(result.DeclaredEvidence, func(a, b EvidenceDeclaration) int { return strings.Compare(a.Path, b.Path) })
	}
	for name := range cfg.Dependencies {
		result.DeclaredTools = append(result.DeclaredTools, name)
	}
	slices.Sort(result.DeclaredTools)
	if _, _, err := sandboxdeps.RequiredCommands(cfg.Dependencies); err != nil {
		issue("declared tools are unsupported: " + err.Error())
	}
	needsRecipe := len(preparationCommands) > 0 || len(cfg.Dependencies) > 0 || result.VerificationCommandCount > 0 || len(result.DeclaredEvidence) > 0
	for _, category := range repo.RequiredEvidence {
		needsRecipe = needsRecipe || category != "git"
	}
	if needsRecipe && repo.RecipeReview == nil {
		issue("a revision-bound review-safe recipe attestation is required")
	}
	var reviewedCoverage []EvidenceCategory
	if review := repo.RecipeReview; review != nil {
		valid := true
		if review.Revision != repo.Revision || review.ConfigSHA256 != hash || missingReference(review.Evidence) || len(review.Evidence) > 2000 {
			issue("recipe review must cite evidence and match the repository revision and config hash")
			valid = false
		}
		if !review.PreservesTrackedFiles {
			issue("recipe review must verify that setup preserves tracked files")
			valid = false
		}
		if !review.SetupOnly {
			issue("recipe review must verify setup does not run tests, builds, or services")
			valid = false
		}
		if err := validateCategories(review.CoveredEvidence); err != nil {
			issue("covered evidence: " + err.Error())
			valid = false
		}
		if valid {
			reviewedCoverage = slices.Clone(review.CoveredEvidence)
		}
	}
	categories := make(map[EvidenceCategory]bool)
	for _, group := range [][]EvidenceCategory{repo.RequiredEvidence, reviewedCoverage} {
		for _, category := range group {
			if category != models.CodeReviewEvidenceGit && category.Validate() == nil {
				categories[category] = true
			}
		}
	}
	orderedCategories := make([]EvidenceCategory, 0, len(categories))
	for category := range categories {
		orderedCategories = append(orderedCategories, category)
	}
	slices.Sort(orderedCategories)
	for _, category := range orderedCategories {
		declared := slices.ContainsFunc(result.DeclaredEvidence, func(evidence EvidenceDeclaration) bool { return evidence.Category == category })
		switch {
		case cfg.Review == nil:
			issue("missing explicit review recipe for source/artifact evidence: " + string(category))
		case !declared:
			issue("review recipe must declare in-repo evidence paths for: " + string(category))
		case result.VerificationCommandCount == 0:
			issue("review recipe must declare local verification commands for: " + string(category))
		case !slices.Contains(reviewedCoverage, category):
			issue("recipe review does not establish required evidence: " + string(category))
		default:
			result.CoveredEvidence = append(result.CoveredEvidence, category)
		}
	}
	slices.Sort(result.RequiredEvidence)
	slices.Sort(result.CoveredEvidence)
	result.Ready = len(result.Issues) == 0
	return result
}

func validateCategories(categories []EvidenceCategory) error {
	seen := make(map[EvidenceCategory]bool, len(categories))
	for _, category := range categories {
		if category.Validate() != nil || seen[category] {
			return fmt.Errorf("categories must be recognized and unique")
		}
		seen[category] = true
	}
	return nil
}

func missingReference(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "unknown", "unassigned", "tbd", "none", "n/a", "not reviewed", "pending":
		return true
	default:
		return false
	}
}

func decodeStrict(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("expected exactly one JSON value")
	}
	return nil
}

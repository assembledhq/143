package models

import "fmt"

// CodeReviewEvidenceCategory identifies the source/artifact coverage a pinned
// repository setup audit requires. Git coverage does not imply other categories.
type CodeReviewEvidenceCategory string

const (
	CodeReviewEvidenceGit                    CodeReviewEvidenceCategory = "git"
	CodeReviewEvidenceJavaScriptDependencies CodeReviewEvidenceCategory = "javascript_dependencies"
	CodeReviewEvidenceGoDependencies         CodeReviewEvidenceCategory = "go_dependencies"
	CodeReviewEvidencePythonDependencies     CodeReviewEvidenceCategory = "python_dependencies"
	CodeReviewEvidenceRubyDependencies       CodeReviewEvidenceCategory = "ruby_dependencies"
	CodeReviewEvidenceGeneratedArtifacts     CodeReviewEvidenceCategory = "generated_artifacts"
	CodeReviewEvidenceSubmodules             CodeReviewEvidenceCategory = "submodules"
	CodeReviewEvidenceLFS                    CodeReviewEvidenceCategory = "lfs"
	CodeReviewEvidenceExternalCaches         CodeReviewEvidenceCategory = "external_caches"
)

func (c CodeReviewEvidenceCategory) Validate() error {
	switch c {
	case CodeReviewEvidenceGit, CodeReviewEvidenceJavaScriptDependencies,
		CodeReviewEvidenceGoDependencies, CodeReviewEvidencePythonDependencies, CodeReviewEvidenceRubyDependencies,
		CodeReviewEvidenceGeneratedArtifacts, CodeReviewEvidenceSubmodules,
		CodeReviewEvidenceLFS, CodeReviewEvidenceExternalCaches:
		return nil
	default:
		return fmt.Errorf("invalid review evidence category %q", c)
	}
}

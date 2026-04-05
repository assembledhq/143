package models

import (
	"encoding/hex"
	"fmt"

	"github.com/google/uuid"
)

// InputManifest captures everything needed to reconstruct the exact inputs
// for an agent run, enabling reproducibility and eval comparisons.
type InputManifest struct {
	// ServerDeploySHA pins all prompt templates (embedded in the binary).
	// To get exact text: git show <sha>:internal/prompts/templates/<name>.template
	ServerDeploySHA string `json:"server_deploy_sha"`

	// PMDocumentSetPinID references the frozen set of PM documents.
	PMDocumentSetPinID *uuid.UUID `json:"pm_document_set_pin_id,omitempty"`

	// OrgSettingsVersionID is the row ID of the active org settings at run time.
	OrgSettingsVersionID *uuid.UUID `json:"org_settings_version_id,omitempty"`

	// ProductContextHash is a SHA-256 of the org settings context fields.
	ProductContextHash string `json:"product_context_hash,omitempty"`

	// RepoBaseCommitSHA is the git commit of the customer repo at run start.
	RepoBaseCommitSHA string `json:"repo_base_commit_sha,omitempty"`

	// Model and config used for the run.
	Model       string      `json:"model,omitempty"`
	ModelConfig ModelConfig `json:"model_config,omitempty"`

	// SandboxImageDigest is the content-addressed image digest (sha256:...).
	SandboxImageDigest string `json:"sandbox_image_digest,omitempty"`

	// MemorySnapshot captures which memories were injected into context.
	MemorySnapshot *MemorySnapshot `json:"memory_snapshot,omitempty"`

	// IntegrationSkillsHash is a SHA-256 of the generated skills doc.
	IntegrationSkillsHash string `json:"integration_skills_hash,omitempty"`

	// CredentialSources records which credential resolution path was used
	// without storing secrets.
	CredentialSources map[string]string `json:"credential_sources,omitempty"`
}

// ModelConfig captures LLM configuration for reproducibility.
type ModelConfig struct {
	ReasoningEffort string  `json:"reasoning_effort,omitempty"`
	Temperature     float64 `json:"temperature,omitempty"`
}

// MemorySnapshot captures the selected memories at run time.
type MemorySnapshot struct {
	SelectedMemoryIDs []uuid.UUID `json:"selected_memory_ids"`
	ContentHash       string      `json:"content_hash"`
	TokenBudgetUsed   int         `json:"token_budget_used"`
}

// isHexSHA checks that s is a valid lowercase hex string of the given byte length.
func isHexSHA(s string, byteLen int) bool {
	if len(s) != byteLen*2 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// Validate checks that required fields are present and well-formed.
func (im *InputManifest) Validate() error {
	if im.ServerDeploySHA == "" {
		return fmt.Errorf("ServerDeploySHA is required")
	}
	// Allow "dev" for local builds; otherwise require a 40-char hex git SHA.
	if im.ServerDeploySHA != "dev" && !isHexSHA(im.ServerDeploySHA, 20) {
		return fmt.Errorf("ServerDeploySHA must be a 40-character hex git SHA or \"dev\"")
	}
	return nil
}

package repoconfig

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"strings"
	"unicode"

	"github.com/assembledhq/143/internal/models"
)

const (
	MaxReviewSectionBytes             = 64 * 1024
	MaxReviewCommands                 = 32
	MaxReviewCommandBytes             = 8 * 1024
	MaxReviewEvidenceEntries          = 32
	MaxReviewEvidencePathBytes        = 512
	MaxReviewEvidenceDescriptionBytes = 2 * 1024
)

// ReviewSection supplies setup-only recipes and discoverable source evidence
// for reviews. Presence replaces Bootstrap.Commands only in review sessions.
// VerifyCommands are repository commands, not inherently read-only operations;
// the platform never executes them after the shared preparation deadline.
type ReviewSection struct {
	Commands       []string              `json:"commands,omitempty"`
	VerifyCommands []string              `json:"verify_commands,omitempty"`
	Evidence       []ReviewEvidenceEntry `json:"evidence,omitempty"`
}

type ReviewEvidenceEntry struct {
	Path        string                            `json:"path"`
	Description string                            `json:"description"`
	Category    models.CodeReviewEvidenceCategory `json:"category"`
}

func (s *ReviewSection) UnmarshalJSON(data []byte) error {
	if len(data) > MaxReviewSectionBytes || !bytes.HasPrefix(bytes.TrimSpace(data), []byte("{")) {
		return fmt.Errorf("review must be an object of at most %d bytes", MaxReviewSectionBytes)
	}
	type plain ReviewSection
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return fmt.Errorf("review schema: %w", err)
	}
	for _, name := range []string{"commands", "verify_commands", "evidence"} {
		if raw, present := fields[name]; present && !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("[")) {
			return fmt.Errorf("review.%s must be an array", name)
		}
	}
	var decoded plain
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return fmt.Errorf("review schema: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("review must contain exactly one object")
	}
	*s = ReviewSection(decoded)
	return nil
}

func (s *ReviewSection) normalize() error {
	for _, section := range []struct {
		name     string
		commands *[]string
	}{{"review.commands", &s.Commands}, {"review.verify_commands", &s.VerifyCommands}} {
		if len(*section.commands) > MaxReviewCommands {
			return fmt.Errorf("%s exceeds %d commands", section.name, MaxReviewCommands)
		}
		for index, command := range *section.commands {
			command = strings.TrimSpace(command)
			if command == "" || len(command) > MaxReviewCommandBytes || strings.ContainsRune(command, 0) {
				return fmt.Errorf("%s[%d] must be a non-empty command of at most %d bytes without NUL", section.name, index, MaxReviewCommandBytes)
			}
			(*section.commands)[index] = command
		}
	}
	if len(s.Evidence) > MaxReviewEvidenceEntries {
		return fmt.Errorf("review.evidence exceeds %d entries", MaxReviewEvidenceEntries)
	}
	seen := make(map[string]bool, len(s.Evidence))
	for index := range s.Evidence {
		entry := &s.Evidence[index]
		entry.Path = strings.TrimSpace(entry.Path)
		entry.Description = strings.TrimSpace(entry.Description)
		if err := ValidateReviewEvidencePath(entry.Path); err != nil {
			return fmt.Errorf("review.evidence[%d].path: %w", index, err)
		}
		if seen[entry.Path] {
			return fmt.Errorf("review.evidence[%d].path is duplicated", index)
		}
		seen[entry.Path] = true
		if entry.Description == "" || len(entry.Description) > MaxReviewEvidenceDescriptionBytes || strings.ContainsRune(entry.Description, 0) {
			return fmt.Errorf("review.evidence[%d].description must be a non-empty string of at most %d bytes", index, MaxReviewEvidenceDescriptionBytes)
		}
		if err := entry.Category.Validate(); err != nil {
			return fmt.Errorf("review.evidence[%d].category: %w", index, err)
		}
	}
	return nil
}

// ValidateReviewEvidencePath permits canonical workspace-relative source roots.
// Runtime validation additionally rejects symlinks escaping the workspace.
// Credential restrictions concern workspace-root locations, not SDK fixtures.
func ValidateReviewEvidencePath(value string) error {
	if value == "" || len(value) > MaxReviewEvidencePathBytes || value == "." || path.IsAbs(value) || path.Clean(value) != value || strings.ContainsAny(value, "\\:") || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return fmt.Errorf("must be a canonical relative path of at most %d bytes", MaxReviewEvidencePathBytes)
	}
	for _, part := range strings.Split(value, "/") {
		if part == ".." || part == ".git" {
			return fmt.Errorf("must stay inside the repository and exclude Git metadata")
		}
	}
	root := strings.Split(value, "/")[0]
	if root == ".." || root == ".env" || strings.HasPrefix(root, ".env.") || root == ".npmrc" || root == ".pypirc" || root == ".netrc" || root == ".git-credentials" || root == ".ssh" || root == ".aws" || root == ".azure" || value == ".config/gh" || strings.HasPrefix(value, ".config/gh/") {
		return fmt.Errorf("must exclude workspace credential locations")
	}
	return nil
}

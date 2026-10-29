package models

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

const CodeReviewGuidanceMaxBytes = 64 * 1024

// ErrInvalidCodeReviewGuidanceFile requires a repository change, not a retry.
var ErrInvalidCodeReviewGuidanceFile = errors.New("invalid REVIEW.md")

// CodeReviewRepositoryGuidance captures root REVIEW.md from the repository's
// default branch. Missing files are explicit; nil denotes a historical capture
// made before repository guidance was supported.
type CodeReviewRepositoryGuidance struct {
	Branch  string `json:"branch"`
	Present bool   `json:"present"`
	BlobSHA string `json:"blob_sha,omitempty"`
	Content string `json:"content,omitempty"`
}

func (g CodeReviewRepositoryGuidance) Validate() error {
	if strings.TrimSpace(g.Branch) == "" {
		return fmt.Errorf("repository review guidance requires a default branch")
	}
	if !g.Present {
		if g.BlobSHA != "" || g.Content != "" {
			return fmt.Errorf("absent repository review guidance cannot contain a blob")
		}
		return nil
	}
	if len(g.BlobSHA) != 40 || strings.Trim(g.BlobSHA, "0123456789abcdef") != "" {
		return fmt.Errorf("repository review guidance requires a Git blob SHA")
	}
	if len(g.Content) > CodeReviewGuidanceMaxBytes || !utf8.ValidString(g.Content) || strings.ContainsRune(g.Content, 0) {
		return fmt.Errorf("%w: must be UTF-8 text of at most %d bytes", ErrInvalidCodeReviewGuidanceFile, CodeReviewGuidanceMaxBytes)
	}
	return nil
}

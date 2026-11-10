package models

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"
)

const CodeReviewGuidanceMaxBytes = 64 * 1024
const CodeReviewGuidanceMaxFiles = 64

// ErrInvalidCodeReviewGuidanceFile requires a repository change, not a retry.
var ErrInvalidCodeReviewGuidanceFile = errors.New("invalid REVIEW.md")

type CodeReviewRepositoryGuidance struct {
	Branch      string                   `json:"branch"`
	Present     bool                     `json:"present"`
	BlobSHA     string                   `json:"blob_sha,omitempty"`
	Content     string                   `json:"content,omitempty"`
	NestedFiles []CodeReviewGuidanceFile `json:"nested_files,omitempty"`
}

type CodeReviewGuidanceFile struct {
	Path    string `json:"path"`
	BlobSHA string `json:"blob_sha"`
	Content string `json:"content"`
}

func (g CodeReviewRepositoryGuidance) Validate() error {
	if strings.TrimSpace(g.Branch) == "" {
		return fmt.Errorf("repository review guidance requires a default branch")
	}
	if !g.Present {
		if g.BlobSHA != "" || g.Content != "" {
			return fmt.Errorf("absent repository review guidance cannot contain a blob")
		}
	} else if err := validateCodeReviewGuidanceBlob(g.BlobSHA, g.Content); err != nil {
		return err
	}
	fileCount := len(g.NestedFiles)
	if g.Present {
		fileCount++
	}
	if fileCount > CodeReviewGuidanceMaxFiles {
		return fmt.Errorf("%w: at most %d files are allowed", ErrInvalidCodeReviewGuidanceFile, CodeReviewGuidanceMaxFiles)
	}
	totalBytes := len(g.Content)
	previousPath := ""
	for _, file := range g.NestedFiles {
		if !fs.ValidPath(file.Path) || file.Path == "REVIEW.md" || path.Base(file.Path) != "REVIEW.md" ||
			len(file.Path) > 1024 || strings.Contains(file.Path, "\\") || strings.ContainsFunc(file.Path, unicode.IsControl) {
			return fmt.Errorf("%w: invalid nested review guidance path", ErrInvalidCodeReviewGuidanceFile)
		}
		if file.Path <= previousPath {
			return fmt.Errorf("%w: nested paths must be unique and sorted", ErrInvalidCodeReviewGuidanceFile)
		}
		if err := validateCodeReviewGuidanceBlob(file.BlobSHA, file.Content); err != nil {
			return err
		}
		totalBytes += len(file.Content)
		previousPath = file.Path
	}
	if totalBytes > CodeReviewGuidanceMaxBytes {
		return fmt.Errorf("%w: combined content must be at most %d bytes", ErrInvalidCodeReviewGuidanceFile, CodeReviewGuidanceMaxBytes)
	}
	return nil
}

func validateCodeReviewGuidanceBlob(blobSHA, content string) error {
	if len(blobSHA) != 40 || strings.Trim(blobSHA, "0123456789abcdef") != "" {
		return fmt.Errorf("repository review guidance requires a Git blob SHA")
	}
	if len(content) > CodeReviewGuidanceMaxBytes || !utf8.ValidString(content) || strings.ContainsRune(content, 0) {
		return fmt.Errorf("%w: must be UTF-8 text of at most %d bytes", ErrInvalidCodeReviewGuidanceFile, CodeReviewGuidanceMaxBytes)
	}
	return nil
}

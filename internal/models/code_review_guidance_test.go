package models

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCodeReviewRepositoryGuidanceValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		guidance CodeReviewRepositoryGuidance
		wantErr  bool
	}{
		{"absent", CodeReviewRepositoryGuidance{Branch: "master"}, false},
		{"empty", CodeReviewRepositoryGuidance{Branch: "main", Present: true, BlobSHA: strings.Repeat("a", 40)}, false},
		{"maximum size", CodeReviewRepositoryGuidance{Branch: "main", Present: true, BlobSHA: strings.Repeat("a", 40), Content: strings.Repeat("a", CodeReviewGuidanceMaxBytes)}, false},
		{"missing branch", CodeReviewRepositoryGuidance{}, true},
		{"absent with content", CodeReviewRepositoryGuidance{Branch: "main", Content: "stale"}, true},
		{"absent with blob", CodeReviewRepositoryGuidance{Branch: "main", BlobSHA: strings.Repeat("a", 40)}, true},
		{"missing blob", CodeReviewRepositoryGuidance{Branch: "main", Present: true}, true},
		{"invalid blob", CodeReviewRepositoryGuidance{Branch: "main", Present: true, BlobSHA: strings.Repeat("z", 40)}, true},
		{"oversized", CodeReviewRepositoryGuidance{Branch: "main", Present: true, BlobSHA: strings.Repeat("a", 40), Content: strings.Repeat("a", CodeReviewGuidanceMaxBytes+1)}, true},
		{"non UTF8", CodeReviewRepositoryGuidance{Branch: "main", Present: true, BlobSHA: strings.Repeat("a", 40), Content: "\xff"}, true},
		{"NUL", CodeReviewRepositoryGuidance{Branch: "main", Present: true, BlobSHA: strings.Repeat("a", 40), Content: "\x00"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.guidance.Validate()
			if tt.wantErr {
				require.Error(t, err, "invalid guidance must be rejected")
			} else {
				require.NoError(t, err, "complete guidance or explicit absence must be accepted")
			}
		})
	}
}

func TestCodeReviewRepositoryGuidanceNestedFiles(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		root      bool
		rootBytes int
		fileCount int
		fileBytes int
		wantErr   bool
	}{
		{name: "nested without root", fileCount: 1, fileBytes: 32},
		{name: "root and nested", root: true, rootBytes: 32, fileCount: 2, fileBytes: 32},
		{name: "maximum total bytes", root: true, rootBytes: 1, fileCount: 1, fileBytes: CodeReviewGuidanceMaxBytes - 1},
		{name: "aggregate exceeds budget", root: true, rootBytes: 1, fileCount: 1, fileBytes: CodeReviewGuidanceMaxBytes, wantErr: true},
		{name: "nested files exceed budget", fileCount: 2, fileBytes: CodeReviewGuidanceMaxBytes/2 + 1, wantErr: true},
		{name: "nested file exceeds budget", fileCount: 1, fileBytes: CodeReviewGuidanceMaxBytes + 1, wantErr: true},
		{name: "maximum files without root", fileCount: CodeReviewGuidanceMaxFiles},
		{name: "maximum files with root", root: true, fileCount: CodeReviewGuidanceMaxFiles - 1},
		{name: "root counts toward limit", root: true, fileCount: CodeReviewGuidanceMaxFiles, wantErr: true},
		{name: "too many nested files", fileCount: CodeReviewGuidanceMaxFiles + 1, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			guidance := CodeReviewRepositoryGuidance{Branch: "main", Present: tt.root}
			if tt.root {
				guidance.BlobSHA = strings.Repeat("a", 40)
				guidance.Content = strings.Repeat("a", tt.rootBytes)
			}
			for i := 0; i < tt.fileCount; i++ {
				guidance.NestedFiles = append(guidance.NestedFiles, CodeReviewGuidanceFile{
					Path: fmt.Sprintf("area%02d/REVIEW.md", i), BlobSHA: strings.Repeat("b", 40), Content: strings.Repeat("b", tt.fileBytes),
				})
			}
			err := guidance.Validate()
			if tt.wantErr {
				require.ErrorIs(t, err, ErrInvalidCodeReviewGuidanceFile, "oversized guidance must require a repository correction")
			} else {
				require.NoError(t, err, "bounded nested guidance must work with or without a root rubric")
			}
		})
	}
}

func TestCodeReviewRepositoryGuidanceNestedPaths(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		paths   []string
		wantErr bool
	}{
		{"nested ancestry", []string{"api/REVIEW.md", "api/storage/REVIEW.md"}, false},
		{"root duplicated as nested", []string{"REVIEW.md"}, true},
		{"absolute", []string{"/api/REVIEW.md"}, true},
		{"parent traversal", []string{"api/../REVIEW.md"}, true},
		{"dot component", []string{"api/./REVIEW.md"}, true},
		{"empty component", []string{"api//REVIEW.md"}, true},
		{"wrong filename", []string{"api/Review.md"}, true},
		{"backslash", []string{"api\\area/REVIEW.md"}, true},
		{"control character", []string{"api\narea/REVIEW.md"}, true},
		{"unicode control", []string{"api\u0085area/REVIEW.md"}, true},
		{"invalid UTF8", []string{"api\xff/REVIEW.md"}, true},
		{"maximum path", []string{strings.Repeat("a", 1014) + "/REVIEW.md"}, false},
		{"oversized path", []string{strings.Repeat("a", 1015) + "/REVIEW.md"}, true},
		{"duplicate", []string{"api/REVIEW.md", "api/REVIEW.md"}, true},
		{"unsorted", []string{"web/REVIEW.md", "api/REVIEW.md"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			guidance := CodeReviewRepositoryGuidance{Branch: "main"}
			for _, filePath := range tt.paths {
				guidance.NestedFiles = append(guidance.NestedFiles, CodeReviewGuidanceFile{Path: filePath, BlobSHA: strings.Repeat("a", 40)})
			}
			err := guidance.Validate()
			if tt.wantErr {
				require.ErrorIs(t, err, ErrInvalidCodeReviewGuidanceFile, "ambiguous or unsafe nested paths must be rejected")
			} else {
				require.NoError(t, err, "canonical nested paths must be accepted")
			}
		})
	}
}

func TestCodeReviewRepositoryGuidanceNestedBlob(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, blobSHA, content string
	}{
		{"missing blob", "", "guidance"},
		{"invalid blob", strings.Repeat("z", 40), "guidance"},
		{"invalid UTF8", strings.Repeat("a", 40), "\xff"},
		{"binary content", strings.Repeat("a", 40), "\x00"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			guidance := CodeReviewRepositoryGuidance{Branch: "main", NestedFiles: []CodeReviewGuidanceFile{{Path: "api/REVIEW.md", BlobSHA: tt.blobSHA, Content: tt.content}}}
			require.Error(t, guidance.Validate(), "nested blobs must meet the same integrity and text contract as root guidance")
		})
	}
}

func TestCodeReviewRepositoryGuidanceLegacyJSON(t *testing.T) {
	t.Parallel()
	const original = `{"branch":"main","present":true,"blob_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","content":"Original guidance"}`
	var guidance CodeReviewRepositoryGuidance
	require.NoError(t, json.Unmarshal([]byte(original), &guidance), "historical root-only captures must remain readable")
	require.NoError(t, guidance.Validate(), "historical root-only guidance must remain valid")
	raw, err := json.Marshal(guidance)
	require.NoError(t, err, "historical guidance must serialize")
	require.Equal(t, original, string(raw), "new optional nested fields must preserve historical contract digest bytes")
}

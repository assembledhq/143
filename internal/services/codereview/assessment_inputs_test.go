package codereview

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func reviewTestCapture() ReviewInputCapture {
	d := strings.Repeat("a", 64)
	return ReviewInputCapture{
		Code: ReviewCodeInput{OrgID: uuid.New(), RepositoryID: uuid.New(), PullRequestID: uuid.New(), HeadSHA: "head", BaseSHA: "base", BaseRef: "main", FilesComplete: true,
			Files: []ReviewChangedFile{{Path: "a.go", Status: "modified", PatchDigest: d}}},
		Contract: ReviewContractInput{PolicyID: uuid.New(), PolicyVersion: 1, PolicyDigest: d, RosterDigest: d,
			ModelConfigurationDigest: d, PromptContractVersion: "v1", PromptContentDigest: d, InstructionsDigest: d, ExternalInputsComplete: true},
		Title: "Add card", Description: "Add card\n", Visual: ReviewVisualInput{CaptureComplete: true, SourceProvenanceComplete: true},
		Gates: ReviewGateInput{SnapshotDigest: d, Complete: true},
	}
}

func TestBuildReviewInputManifest(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		mutate       func(*ReviewInputCapture)
		wantErr      bool
		wantEligible bool
	}{
		{"complete", func(*ReviewInputCapture) {}, false, true},
		{"missing patch", func(c *ReviewInputCapture) { c.Code.Files[0].PatchDigest = "" }, true, false},
		{"missing gates", func(c *ReviewInputCapture) { c.Gates.Complete = false }, true, false},
		{"malformed image", func(c *ReviewInputCapture) { c.Description += "![screenshot](bad url)\n" }, false, false},
		{"HTML image", func(c *ReviewInputCapture) { c.Description += "<img src=x>\n" }, false, false},
		{"code fence", func(c *ReviewInputCapture) { c.Description += "```md\n![x](url)\n```\n" }, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := reviewTestCapture()
			tt.mutate(&c)
			m, err := BuildReviewInputManifest(c)
			if tt.wantErr {
				require.Error(t, err, "incomplete capture must fail closed")
				return
			}
			require.NoError(t, err, "complete capture should produce a manifest")
			require.Equal(t, tt.wantEligible, m.ReuseEligible, "Markdown ambiguity must control reuse eligibility")
			require.True(t, validManifest(m), "built manifest should validate")
		})
	}
}

func TestReviewIntentImageNormalization(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, before, after string
		same, eligible      bool
	}{
		{"standalone addition", "Purpose\n", "Purpose\n![new](https://x/y.png)\n", true, true},
		{"standalone replacement", "Purpose\n![old](https://x/a.png)\n", "Purpose\n![new](https://x/b.png)\n", true, true},
		{"inline replacement", "See ![old](https://x/a.png) here\n", "See ![new](https://x/b.png) here\n", true, true},
		{"prose changes", "Purpose\n", "Different purpose\n![new](https://x/y.png)\n", false, true},
		{"caption changes", "Purpose\n![old](https://x/a.png)\nBefore\n", "Purpose\n![new](https://x/b.png)\nAfter\n", false, true},
		{"reference syntax", "Purpose\n", "Purpose\n![x][ref]\n", false, false},
		{"nested URL", "Purpose\n", "Purpose\n![x](https://x/a(b).png)\n", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			before, err := normalizeReviewIntent(tt.before)
			require.NoError(t, err, "baseline Markdown should parse")
			after, err := normalizeReviewIntent(tt.after)
			require.Equal(t, tt.eligible, err == nil, "new Markdown eligibility should match")
			require.Equal(t, tt.same, err == nil && before == after, "only image node changes may retain intent")
		})
	}
}

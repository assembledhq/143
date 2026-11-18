package models

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCodeReviewExportReleaseValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		proof CodeReviewExportArchiveProof
		valid bool
	}{
		{"complete", releaseTestProof(3, 3), true}, {"empty", releaseTestProof(0, 0), true}, {"mismatch", releaseTestProof(3, 2), false}, {"negative", releaseTestProof(-1, -1), false},
		{"missing counts", CodeReviewExportArchiveProof{ManifestSHA256: strings.Repeat("a", 64), Verified: true}, false},
		{"invalid hash", CodeReviewExportArchiveProof{ManifestSHA256: "x"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if tt.valid {
				require.NoError(t, tt.proof.Validate(), "complete archive proof validates")
			} else {
				require.Error(t, tt.proof.Validate(), "invalid or incomplete proof is rejected")
			}
		})
	}
}
func releaseTestProof(records, position int64) CodeReviewExportArchiveProof {
	return CodeReviewExportArchiveProof{ManifestSHA256: strings.Repeat("a", 64), ArchivedRecords: &records, FinalPosition: &position, Verified: true}
}
func TestReviewExportStateValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		state ReviewExportState
		valid bool
	}{{ReviewExportActive, true}, {ReviewExportReleased, true}, {ReviewExportExpired, true}, {ReviewExportState("unknown"), false}}
	for _, tt := range tests {
		t.Run(string(tt.state), func(t *testing.T) {
			t.Parallel()
			if tt.valid {
				require.NoError(t, tt.state.Validate(), "documented acquisition state validates")
			} else {
				require.Error(t, tt.state.Validate(), "unknown lifecycle state is rejected")
			}
		})
	}
}

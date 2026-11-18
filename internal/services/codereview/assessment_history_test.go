package codereview

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReviewHistoryRequestFingerprint(t *testing.T) {
	t.Parallel()
	input := reviewTestCapture()
	baseline, err := BuildReviewInputManifest(input)
	require.NoError(t, err, "ordinary request should have a valid manifest")
	raw, err := json.Marshal(baseline.Request)
	require.NoError(t, err, "ordinary request should serialize")
	require.JSONEq(t, `{"substantive_text":"","dispute_routed":false}`, string(raw), "default-off history must preserve historical request fingerprints")
	input.Request.UseReviewHistory = true
	withHistory, err := BuildReviewInputManifest(input)
	require.NoError(t, err, "history-enabled request should have a valid manifest")
	require.NotEqual(t, baseline.RequestDigest, withHistory.RequestDigest, "history enrollment must be part of immutable request identity")
	require.NotEqual(t, baseline.InputDigest, withHistory.InputDigest, "request enrollment must invalidate exact input reuse")
	require.Equal(t, baseline.CodeDigest, withHistory.CodeDigest, "history enrollment does not change reviewed code")
	require.NoError(t, ValidateReviewInputManifest(withHistory), "enrolled request must retain round-trip validation")
}

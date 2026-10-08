package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/assembledhq/143/internal/jobctx"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestCodeReviewStageOutcome(t *testing.T) {
	t.Parallel()

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		name    string
		ctx     context.Context
		err     error
		outcome string
	}{
		{name: "completed", ctx: context.Background(), outcome: "succeeded"},
		{name: "retry", ctx: context.Background(), err: &RetryableError{Err: errors.New("still running")}, outcome: "waiting"},
		{name: "failure", ctx: context.Background(), err: errors.New("failed"), outcome: "failed"},
		{name: "cancelled", ctx: cancelledCtx, err: context.Canceled, outcome: "cancelled"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.outcome, codeReviewStageOutcome(tt.ctx, tt.err), "review stage should classify the durable controller attempt")
		})
	}
}

func TestCodeReviewTimingLoggerCorrelation(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	reviewID := uuid.New()
	lockToken := uuid.New()
	tests := []struct {
		name       string
		reviewID   uuid.UUID
		wantReview bool
	}{
		{name: "known review", reviewID: reviewID, wantReview: true},
		{name: "unknown review", wantReview: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var output bytes.Buffer
			base := zerolog.New(&output)
			ctx := jobctx.WithLockToken(context.Background(), lockToken)
			log := codeReviewTimingLogger(ctx, base, runCodeReviewPayload{
				OrgID: orgID, SessionID: sessionID, MetadataID: tt.reviewID, HeadSHA: "head",
			})
			log.Info().Msg("test")
			var event map[string]any
			require.NoError(t, json.Unmarshal(output.Bytes(), &event), "timing log should be valid JSON")
			require.Equal(t, orgID.String(), event["org_id"], "timing log should identify the organization")
			require.Equal(t, sessionID.String(), event["session_id"], "timing log should identify the review session")
			require.Equal(t, lockToken.String(), event["lock_token"], "timing log should use the dispatcher's lock token field")
			if tt.wantReview {
				require.Equal(t, reviewID.String(), event["review_id"], "timing log should identify known review metadata")
			} else {
				require.NotContains(t, event, "review_id", "timing log should omit absent review metadata")
			}
		})
	}
}

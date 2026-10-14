package worker

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/feedback"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type classificationFailureLLM struct {
	response string
	err      error
}

func (s classificationFailureLLM) Complete(context.Context, string, string) (string, error) {
	return s.response, s.err
}

func TestProcessReviewCommentHandlerReturnsClassificationFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, response  string
		providerFailure bool
	}{
		{name: "provider failure", providerFailure: true},
		{name: "malformed provider JSON", response: `{"actionable":`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID, commentID := uuid.New(), uuid.New()
			providerErr := errors.New("provider unavailable")
			llm := classificationFailureLLM{response: tt.response}
			if tt.providerFailure {
				llm.err = providerErr
			}
			writes, reads := 0, 0
			comments := &testFeedbackCommentStore{
				getByIDFn: func(ctx context.Context, gotOrg, gotID uuid.UUID) (models.ReviewComment, error) {
					require.Equal(t, orgID, gotOrg, "worker must retain the owning tenant")
					require.Equal(t, commentID, gotID, "worker must retain the requested comment")
					reads++
					return models.ReviewComment{ID: commentID, OrgID: orgID, FilterStatus: "pending", Reviewer: "human", Body: "Check the returned error before dereferencing this value."}, nil
				},
				updateClassificationFn: func(context.Context, uuid.UUID, uuid.UUID, string, *string, bool, bool, *string, *string) error {
					writes++
					return nil
				},
			}
			memories := &testFeedbackMemoryStore{}
			svc := feedback.NewService(comments, memories, &testFeedbackJobStore{}, llm, zerolog.Nop())
			payload, err := json.Marshal(map[string]string{"org_id": orgID.String(), "comment_id": commentID.String(), "repo": "example/repo"})
			require.NoError(t, err, "worker payload should encode")
			err = newProcessReviewCommentHandler(&Services{Feedback: svc}, zerolog.Nop())(context.Background(), "process_review_comment", payload)
			require.Error(t, err, "failed classification must not return the nil result that marks the job successful")
			if tt.providerFailure {
				require.ErrorIs(t, err, providerErr, "worker must preserve provider error identity")
			} else {
				var syntax *json.SyntaxError
				require.ErrorAs(t, err, &syntax, "worker must preserve malformed response errors")
			}
			require.Equal(t, 1, reads, "failure should stop after the classification load")
			require.Equal(t, 0, writes, "classification failures must leave the comment pending")
			require.Equal(t, 0, memories.createCalls, "classification failures must not teach fabricated conventions")
		})
	}
}

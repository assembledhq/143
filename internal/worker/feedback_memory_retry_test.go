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

func TestProcessReviewCommentHandlerMemoryRecovery(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name                                     string
		failedRead, failedWrite, alreadyAccepted bool
	}{
		{name: "read failure after classification", failedRead: true},
		{name: "memory write failure after classification", failedWrite: true},
		{name: "worker crashed after classification committed", alreadyAccepted: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID, commentID := uuid.New(), uuid.New()
			rule, category := "Always check errors.", "logic_bug"
			stored := models.ReviewComment{ID: commentID, OrgID: orgID, FilterStatus: "pending", Reviewer: "human", Body: "Please check this error before using the returned value."}
			if tt.alreadyAccepted {
				stored.FilterStatus = "accepted"
				stored.Actionable = true
				stored.Generalizable = true
				stored.GeneralizedRule = &rule
				stored.Category = &category
			}
			reads, writes, applications := 0, 0, 0
			failure := errors.New("transient database failure")
			comments := &testFeedbackCommentStore{
				getByIDFn: func(context.Context, uuid.UUID, uuid.UUID) (models.ReviewComment, error) {
					reads++
					if tt.failedRead && reads == 2 {
						return models.ReviewComment{}, failure
					}
					return stored, nil
				},
				updateClassificationFn: func(_ context.Context, _ uuid.UUID, _ uuid.UUID, status string, cat *string, actionable, generalizable bool, r, s *string) error {
					writes++
					stored.FilterStatus = status
					stored.Category = cat
					stored.Actionable = actionable
					stored.Generalizable = generalizable
					stored.GeneralizedRule = r
					stored.Summary = s
					return nil
				},
			}
			memories := &testFeedbackMemoryStore{applyFn: func(_ context.Context, o, c uuid.UUID, repo, r, cat string) error {
				applications++
				require.Equal(t, []uuid.UUID{orgID, commentID}, []uuid.UUID{o, c}, "retries retain tenant and source identity")
				require.Equal(t, []string{"org/repo", rule, category}, []string{repo, r, cat}, "retries use persisted classification")
				if tt.failedWrite && applications == 1 {
					return failure
				}
				return nil
			}}
			svc := feedback.NewService(comments, memories, &testFeedbackJobStore{}, classificationFailureLLM{response: `{"actionable":true,"category":"logic_bug","generalizable":true,"generalized_rule":"Always check errors."}`}, zerolog.Nop())
			payload, err := json.Marshal(map[string]string{"org_id": orgID.String(), "comment_id": commentID.String(), "repo": "org/repo"})
			require.NoError(t, err, "encode feedback job")
			handler := newProcessReviewCommentHandler(&Services{Feedback: svc}, zerolog.Nop())
			err = handler(context.Background(), "process_review_comment", payload)
			if tt.failedRead || tt.failedWrite {
				require.ErrorIs(t, err, failure, "failure after accepted classification must reach worker retry")
				require.NoError(t, handler(context.Background(), "process_review_comment", payload), "retry must finish the accepted comment's memory work")
			} else {
				require.NoError(t, err, "accepted classification must recover after a prior crash")
			}
			expectedApplications := 1
			if tt.failedWrite {
				expectedApplications = 2
			}
			require.Equal(t, expectedApplications, applications, "accepted comments must retry memory application instead of skipping it")
			expectedWrites := 1
			if tt.alreadyAccepted {
				expectedWrites = 0
			}
			require.Equal(t, expectedWrites, writes, "retries must reuse durable classification")
		})
	}
}

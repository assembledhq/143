package github

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestPRService_PublishCodeReviewDisputeReplyValidatesBeforeGitHubAccess(t *testing.T) {
	t.Parallel()

	valid := CodeReviewDisputeReplyRequest{
		OrgID: uuid.New(), InstallationID: 143, Repository: "acme/app",
		PullRequestNumber: 42, Body: "Acknowledged.\n\n" + prFeedbackHiddenMarker,
	}
	tests := []struct {
		name   string
		mutate func(*CodeReviewDisputeReplyRequest)
	}{
		{name: "missing organization", mutate: func(req *CodeReviewDisputeReplyRequest) { req.OrgID = uuid.Nil }},
		{name: "missing installation", mutate: func(req *CodeReviewDisputeReplyRequest) { req.InstallationID = 0 }},
		{name: "invalid pull request number", mutate: func(req *CodeReviewDisputeReplyRequest) { req.PullRequestNumber = 0 }},
		{name: "invalid repository", mutate: func(req *CodeReviewDisputeReplyRequest) { req.Repository = "acme" }},
		{name: "missing loop marker", mutate: func(req *CodeReviewDisputeReplyRequest) { req.Body = "Acknowledged." }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := valid
			tt.mutate(&req)
			commentID, err := (&PRService{}).PublishCodeReviewDisputeReply(context.Background(), req)

			require.Error(t, err, "invalid dispute reply should fail before requesting a GitHub token")
			require.Zero(t, commentID, "invalid dispute reply should not return a GitHub comment id")
		})
	}
}

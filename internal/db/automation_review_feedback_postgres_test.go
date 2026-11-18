package db

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestAutomationReviewPostgresOwnFeedback(t *testing.T) {
	t.Parallel()

	pool, actor := actionPostgresWithConfig(t, true, json.RawMessage(`{"actions":["github_pull_request_review"],"repository":"owner/repo"}`))
	store := NewAutomationActionStore(pool)
	ctx := context.Background()
	scope, err := store.Resolve(ctx, actor.OrgID, actor)
	require.NoError(t, err, "resolve the authorized continuous automation")

	request := models.AutomationActionRequest{
		AutomationActionKey: models.AutomationActionKey{OperationKey: "review", ActionKey: "publish"},
		Kind:                models.AutomationActionGitHubReview,
		PRNumber:            scope.PRNumber,
		HeadSHA:             scope.HeadSHA,
		Comments:            []models.AutomationActionReviewComment{{Path: "src/app.ts", Line: 17, Body: "Reuse the button.\r\nThe same component is used elsewhere."}},
	}
	payload, err := json.Marshal(struct {
		Request models.AutomationActionRequest `json:"request"`
	}{request})
	require.NoError(t, err, "encode the reserved review request")
	review, err := store.Reserve(ctx, actor.OrgID, actor, request, strings.Repeat("b", 64), scope.Config, payload)
	require.NoError(t, err, "reserve the advisory review")

	const reviewID int64 = 902
	comment := request.Comments[0]
	summary := models.AutomationActionReviewFeedback{
		ReviewID: reviewID, PRAuthor: "pr-author", Body: models.AutomationReviewSummary(request.Text, "pr-author", review.ID),
	}
	inline := models.AutomationActionReviewFeedback{
		ReviewID: reviewID, Inline: true, Path: comment.Path, Line: &comment.Line,
		Body: strings.ReplaceAll(models.AutomationReviewMarkedBody(comment.Body, review.ID), "\r\n", "\n"),
	}
	found, err := store.FindAutomationActionReviewOwner(ctx, actor.OrgID, actor.RepositoryID, scope.PRNumber, summary)
	require.NoError(t, err, "check whether the reserved action has sent feedback")
	require.Equal(t, uuid.Nil, found, "an action that has never sent cannot suppress feedback")

	claimed, err := store.Claim(ctx, actor.OrgID, actor, review.ID)
	require.NoError(t, err, "start the review send before looking up early webhooks")
	tests := []struct {
		name     string
		feedback models.AutomationActionReviewFeedback
		orgID    uuid.UUID
		repoID   uuid.UUID
		pr       int
		want     uuid.UUID
	}{
		{"review submitted before receipt", summary, actor.OrgID, actor.RepositoryID, scope.PRNumber, scope.AutomationID},
		{"inline before receipt with normalized newlines", inline, actor.OrgID, actor.RepositoryID, scope.PRNumber, scope.AutomationID},
		{"wrong tenant", inline, uuid.New(), actor.RepositoryID, scope.PRNumber, uuid.Nil},
		{"wrong repository", inline, actor.OrgID, uuid.New(), scope.PRNumber, uuid.Nil},
		{"wrong pull request", inline, actor.OrgID, actor.RepositoryID, scope.PRNumber + 1, uuid.Nil},
	}
	t.Run("in flight", func(t *testing.T) {
		// Complete the in-flight lookups before recording the shared action's receipt.
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				owner, err := store.FindAutomationActionReviewOwner(ctx, tt.orgID, tt.repoID, tt.pr, tt.feedback)
				require.NoError(t, err, "look up the scoped review action")
				require.Equal(t, tt.want, owner, "only the action that sent matching feedback owns it")
			})
		}
	})

	require.NoError(t, store.Finish(ctx, actor.OrgID, review.ID, *claimed.SendToken, models.AutomationActionSucceeded, "902", "", ""), "record the delivered review")
	wrongReview := inline
	wrongReview.ReviewID++
	wrongBody := inline
	wrongBody.Body = "copied " + wrongBody.Body
	wrongPath := inline
	wrongPath.Path = "src/another.ts"
	wrongLine := inline
	line := *inline.Line + 1
	wrongLine.Line = &line
	receiptTests := []struct {
		name     string
		feedback models.AutomationActionReviewFeedback
		want     uuid.UUID
	}{
		{"matching review", inline, scope.AutomationID},
		{"different review", wrongReview, uuid.Nil},
		{"copied marker", wrongBody, uuid.Nil},
		{"different path", wrongPath, uuid.Nil},
		{"different line", wrongLine, uuid.Nil},
	}
	for _, tt := range receiptTests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			owner, err := store.FindAutomationActionReviewOwner(ctx, actor.OrgID, actor.RepositoryID, scope.PRNumber, tt.feedback)
			require.NoError(t, err, "look up feedback after the review receipt was recorded")
			require.Equal(t, tt.want, owner, "match the delivered review and the original inline finding")
		})
	}
}

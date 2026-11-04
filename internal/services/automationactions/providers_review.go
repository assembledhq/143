package automationactions

import (
	"context"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
)

var githubReviewAuthorPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,99}(?:\[bot\])?$`)

type githubReviewInlineComment struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Side string `json:"side"`
	Body string `json:"body"`
}

type githubReviewRequest struct {
	CommitID string                      `json:"commit_id"`
	Event    string                      `json:"event"`
	Body     string                      `json:"body"`
	Comments []githubReviewInlineComment `json:"comments"`
}

type githubReviewResponse struct {
	ID       int64  `json:"id"`
	URL      string `json:"html_url"`
	CommitID string `json:"commit_id"`
	State    string `json:"state"`
}

func (p *Providers) submitGitHubReview(ctx context.Context, scope models.AutomationActionScope, token string, action models.AutomationAction, payload Payload) (Receipt, error) {
	request := payload.Request
	if request.Kind != models.AutomationActionGitHubReview || request.ValidateFor(scope.Config) != nil ||
		request.PRNumber != scope.PRNumber || !strings.EqualFold(scope.Config.Repository, scope.RepositoryName) ||
		ValidateRepositoryName(scope.RepositoryName) != nil || action.ID == uuid.Nil ||
		!githubReviewAuthorPattern.MatchString(payload.PR.Author) {
		return Receipt{}, &SendFailure{Code: "GITHUB_REVIEW_TARGET_INVALID", Definitive: true}
	}
	if !payload.PR.Open || payload.PR.Head != request.HeadSHA {
		return Receipt{}, &SendFailure{Code: "GITHUB_REVIEW_STALE_HEAD", Definitive: true, Err: ErrStaleHead}
	}

	comments := make([]githubReviewInlineComment, 0, len(request.Comments))
	for _, comment := range request.Comments {
		comments = append(comments, githubReviewInlineComment{
			Path: comment.Path,
			Line: comment.Line,
			Side: "RIGHT",
			Body: models.AutomationReviewMarkedBody(comment.Body, action.ID),
		})
	}
	body := githubReviewRequest{
		CommitID: request.HeadSHA,
		Event:    "COMMENT",
		Body:     models.AutomationReviewSummary(request.Text, payload.PR.Author, action.ID),
		Comments: comments,
	}
	if err := ctx.Err(); err != nil {
		return Receipt{}, &SendFailure{Code: "GITHUB_REVIEW_SEND_CANCELED", Definitive: true, Err: err}
	}
	var review githubReviewResponse
	if err := p.request(ctx, http.MethodPost, p.githubPath(scope, "pulls")+"/reviews", token, "", body, &review); err != nil {
		return Receipt{}, err
	}
	reviewID := strconv.FormatInt(review.ID, 10)
	expectedURL := "https://github.com/" + scope.RepositoryName + "/pull/" + strconv.Itoa(scope.PRNumber) + "#pullrequestreview-" + reviewID
	if review.ID <= 0 || !strings.EqualFold(review.URL, expectedURL) || review.State != "COMMENTED" || review.CommitID != request.HeadSHA {
		return Receipt{}, &SendFailure{Code: "GITHUB_REVIEW_RECEIPT_INVALID"}
	}
	return Receipt{ID: reviewID, URL: expectedURL}, nil
}

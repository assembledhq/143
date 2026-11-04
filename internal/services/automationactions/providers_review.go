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
	Body     string `json:"body"`
	User     struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"user"`
	App struct {
		ID int64 `json:"id"`
	} `json:"performed_via_github_app"`
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
	receipt := review.receipt(scope, request.HeadSHA)
	if receipt.ID == "" {
		return Receipt{}, &SendFailure{Code: "GITHUB_REVIEW_RECEIPT_INVALID"}
	}
	return receipt, nil
}

func (p *Providers) ReconcileGitHubReview(ctx context.Context, scope models.AutomationActionScope, action models.AutomationAction) (Receipt, error) {
	if !scope.Config.Allows(models.AutomationActionGitHubReview) || action.Kind != models.AutomationActionGitHubReview ||
		action.ID == uuid.Nil || action.PRNumber <= 0 || scope.PRNumber != action.PRNumber ||
		!strings.EqualFold(scope.Config.Repository, scope.RepositoryName) || ValidateRepositoryName(scope.RepositoryName) != nil {
		return Receipt{}, &SendFailure{Code: "GITHUB_REVIEW_TARGET_INVALID", Definitive: true}
	}
	ctx = githubRequestContext(ctx, scope)
	token, err := p.githubToken(ctx, scope)
	if err != nil {
		return Receipt{}, err
	}
	const pageSize = 50
	const maxPages = 4
	endpoint := p.githubPath(scope, "pulls") + "/reviews?per_page=" + strconv.Itoa(pageSize) + "&page="
	for page := 1; page <= maxPages; page++ {
		var reviews []githubReviewResponse
		if err := p.request(ctx, http.MethodGet, endpoint+strconv.Itoa(page), token, "", nil, &reviews); err != nil {
			return Receipt{}, err
		}
		for _, review := range reviews {
			marker := models.AutomationActionReviewFeedback{Body: review.Body}
			if marker.ActionID() != action.ID || !strings.EqualFold(review.User.Type, "Bot") ||
				!models.IsAutomationReviewFromApp(p.githubAppID, p.githubAppSlug, review.App.ID, review.User.Login, review.User.Type) {
				continue
			}
			if receipt := review.receipt(scope, action.HeadSHA); receipt.ID != "" {
				return receipt, nil
			}
		}
		if len(reviews) < pageSize {
			break
		}
	}
	return Receipt{}, nil
}

func (review githubReviewResponse) receipt(scope models.AutomationActionScope, head string) Receipt {
	id := strconv.FormatInt(review.ID, 10)
	expectedURL := "https://github.com/" + scope.RepositoryName + "/pull/" + strconv.Itoa(scope.PRNumber) + "#pullrequestreview-" + id
	if review.ID <= 0 || !strings.EqualFold(review.URL, expectedURL) || review.State != "COMMENTED" || review.CommitID != head {
		return Receipt{}
	}
	return Receipt{ID: id, URL: expectedURL}
}

package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type codeReviewReassessmentWebhook struct {
	Action string `json:"action"`
	Number int    `json:"number"`
	Sender struct {
		Login string `json:"login"`
	} `json:"sender"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	PullRequest struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
		Title   string `json:"title"`
		Body    string `json:"body"`
		User    struct {
			Login string `json:"login"`
		} `json:"user"`
		Head struct {
			SHA  string `json:"sha"`
			Ref  string `json:"ref"`
			Repo struct {
				Fork bool `json:"fork"`
			} `json:"repo"`
		} `json:"head"`
		Base struct {
			SHA string `json:"sha"`
		} `json:"base"`
	} `json:"pull_request"`
	Review struct {
		ID       int64  `json:"id"`
		State    string `json:"state"`
		Body     string `json:"body"`
		CommitID string `json:"commit_id"`
		User     struct {
			Login string `json:"login"`
		} `json:"user"`
		PerformedViaGitHubApp *codeReviewGitHubAppIdentity `json:"performed_via_github_app"`
	} `json:"review"`
	Comment struct {
		ID       int64  `json:"id"`
		Body     string `json:"body"`
		Path     string `json:"path"`
		Line     *int   `json:"line"`
		CommitID string `json:"commit_id"`
		User     struct {
			Login string `json:"login"`
		} `json:"user"`
		PerformedViaGitHubApp *codeReviewGitHubAppIdentity `json:"performed_via_github_app"`
	} `json:"comment"`
	Thread struct {
		NodeID string `json:"node_id"`
	} `json:"thread"`
	CheckSuite struct {
		ID           int64   `json:"id"`
		Conclusion   *string `json:"conclusion"`
		PullRequests []struct {
			Number int `json:"number"`
		} `json:"pull_requests"`
	} `json:"check_suite"`
	CheckRun struct {
		ID           int64   `json:"id"`
		Conclusion   *string `json:"conclusion"`
		PullRequests []struct {
			Number int `json:"number"`
		} `json:"pull_requests"`
	} `json:"check_run"`
	SHA     string `json:"sha"`
	State   string `json:"state"`
	Context string `json:"context"`
}

type codeReviewGitHubAppIdentity struct {
	ID   int64  `json:"id"`
	Slug string `json:"slug"`
}

func (h *WebhookHandler) reassessCodeReviewsForGitHubEvent(ctx context.Context, owner db.GitHubRepoOwner, eventType string, body []byte, _ string) error {
	if h.codeReviews == nil || h.pullRequests == nil || owner.OrgID == uuid.Nil || owner.RepositoryID == uuid.Nil {
		return nil
	}
	var event codeReviewReassessmentWebhook
	if err := json.Unmarshal(body, &event); err != nil {
		return fmt.Errorf("decode code review reassessment event: %w", err)
	}
	if !codeReviewEventChangesAssessment(eventType, event) {
		return nil
	}
	if h.codeReviewReassessmentIsSelfAuthored(eventType, event) {
		return nil
	}

	numbers := codeReviewReassessmentPullRequestNumbers(eventType, event)
	if eventType == "status" {
		prs, err := h.pullRequests.ListOpenByOrgRepoAndHeadSHA(ctx, owner.OrgID, event.Repository.FullName, event.SHA)
		if err != nil {
			return fmt.Errorf("list pull requests for code review status reassessment: %w", err)
		}
		for _, pr := range prs {
			if err := h.reassessCodeReviewTarget(ctx, owner, eventType, event, pr); err != nil {
				return err
			}
		}
		return nil
	}
	for _, number := range numbers {
		pr, err := h.pullRequests.GetByOrgRepoAndNumber(ctx, owner.OrgID, event.Repository.FullName, number)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return fmt.Errorf("load pull request for code review reassessment: %w", err)
		}
		if err := h.reassessCodeReviewTarget(ctx, owner, eventType, event, pr); err != nil {
			return err
		}
	}
	return nil
}

func (h *WebhookHandler) reassessCodeReviewTarget(ctx context.Context, owner db.GitHubRepoOwner, eventType string, event codeReviewReassessmentWebhook, pr models.PullRequest) error {
	if strings.TrimSpace(event.PullRequest.Head.SHA) != "" {
		snapshot := db.PullRequestGitHubSnapshot{
			GitHubPRURL: event.PullRequest.HTMLURL,
			Title:       event.PullRequest.Title,
			Body:        nilIfEmpty(event.PullRequest.Body),
			HeadSHA:     nilIfEmpty(event.PullRequest.Head.SHA),
			HeadRef:     nilIfEmpty(event.PullRequest.Head.Ref),
			BaseSHA:     nilIfEmpty(event.PullRequest.Base.SHA),
		}
		if err := h.pullRequests.UpdateGitHubSnapshot(ctx, owner.OrgID, pr.ID, snapshot); err != nil {
			return fmt.Errorf("refresh pull request mirror for code review reassessment: %w", err)
		}
		pr.GitHubPRURL = snapshot.GitHubPRURL
		pr.Title = snapshot.Title
		pr.Body = snapshot.Body
		pr.HeadSHA = snapshot.HeadSHA
		pr.HeadRef = snapshot.HeadRef
		pr.BaseSHA = snapshot.BaseSHA
	}
	changeKey, err := codeReviewMaterialChangeKey(eventType, event, pr)
	if err != nil {
		return fmt.Errorf("build code review material change key: %w", err)
	}
	_, err = h.codeReviews.QueueReviewChanged(ctx, codereviewsvc.ReviewChangedInput{
		OrgID:             owner.OrgID,
		RepositoryID:      owner.RepositoryID,
		PullRequestID:     pr.ID,
		GitHubRepo:        pr.GitHubRepo,
		GitHubPRNumber:    pr.GitHubPRNumber,
		GitHubPRURL:       pr.GitHubPRURL,
		PullRequestTitle:  pr.Title,
		PullRequestAuthor: event.PullRequest.User.Login,
		BaseSHA:           codeReviewStringValue(pr.BaseSHA),
		HeadSHA:           codeReviewStringValue(pr.HeadSHA),
		FromFork:          event.PullRequest.Head.Repo.Fork,
		ChangeKey:         changeKey,
		ChangeReason:      eventType + "." + event.Action,
	})
	if err != nil {
		return fmt.Errorf("queue code review reassessment: %w", err)
	}
	return nil
}

func (h *WebhookHandler) codeReviewReassessmentIsSelfAuthored(eventType string, event codeReviewReassessmentWebhook) bool {
	switch eventType {
	case "pull_request_review":
		if event.Action == "dismissed" {
			return false
		}
		// Standalone inline comments can produce an empty-body COMMENTED review
		// container, so app identity must be authoritative when no marker exists.
		return codereviewsvc.IsCodeReviewAuthoredBody(event.Review.Body) ||
			h.codeReviewActorIsOwnApp(firstNonEmptyString(event.Review.User.Login, event.Sender.Login), event.Review.PerformedViaGitHubApp)
	case "pull_request_review_comment":
		if event.Action == "deleted" {
			return false
		}
		return codereviewsvc.IsCodeReviewAuthoredBody(event.Comment.Body) ||
			h.codeReviewActorIsOwnApp(firstNonEmptyString(event.Comment.User.Login, event.Sender.Login), event.Comment.PerformedViaGitHubApp)
	default:
		return false
	}
}

func (h *WebhookHandler) codeReviewActorIsOwnApp(login string, app *codeReviewGitHubAppIdentity) bool {
	if h == nil || h.cfg == nil {
		return false
	}
	if app != nil && h.cfg.GitHubAppID > 0 && app.ID == h.cfg.GitHubAppID {
		return true
	}
	ownSlug := canonicalCodeReviewBotLogin(h.cfg.GitHubAppSlug)
	if ownSlug == "" {
		return false
	}
	return canonicalCodeReviewBotLogin(login) == ownSlug ||
		(app != nil && canonicalCodeReviewBotLogin(app.Slug) == ownSlug)
}

func canonicalCodeReviewBotLogin(login string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(login)), "[bot]")
}

type codeReviewMaterialPullRequestState struct {
	HeadSHA          string                         `json:"head_sha"`
	BaseSHA          string                         `json:"base_sha"`
	Title            string                         `json:"title"`
	Body             string                         `json:"body"`
	Status           models.PullRequestStatus       `json:"status"`
	ReviewStatus     models.PullRequestReviewStatus `json:"review_status"`
	CIStatus         models.PullRequestCIStatus     `json:"ci_status"`
	MergeState       models.PullRequestMergeState   `json:"merge_state"`
	HasConflicts     bool                           `json:"has_conflicts"`
	FailingTestCount int                            `json:"failing_test_count"`
}

type codeReviewMaterialEventState struct {
	Class       string `json:"class"`
	ObjectID    string `json:"object_id,omitempty"`
	State       string `json:"state,omitempty"`
	Body        string `json:"body,omitempty"`
	Path        string `json:"path,omitempty"`
	Line        int    `json:"line,omitempty"`
	CommitID    string `json:"commit_id,omitempty"`
	AuthorLogin string `json:"author_login,omitempty"`
}

type codeReviewMaterialAssessmentState struct {
	PullRequest codeReviewMaterialPullRequestState `json:"pull_request"`
	Event       codeReviewMaterialEventState       `json:"event"`
}

func codeReviewMaterialChangeKey(eventType string, event codeReviewReassessmentWebhook, pr models.PullRequest) (string, error) {
	state := codeReviewMaterialAssessmentState{
		PullRequest: codeReviewMaterialPullRequestState{
			HeadSHA:          codeReviewStringValue(pr.HeadSHA),
			BaseSHA:          codeReviewStringValue(pr.BaseSHA),
			Title:            strings.TrimSpace(pr.Title),
			Body:             strings.TrimSpace(codeReviewStringValue(pr.Body)),
			Status:           pr.Status,
			ReviewStatus:     pr.ReviewStatus,
			CIStatus:         pr.CIStatus,
			MergeState:       pr.MergeState,
			HasConflicts:     pr.HasConflicts,
			FailingTestCount: pr.FailingTestCount,
		},
	}
	switch eventType {
	case "pull_request":
		state.Event = codeReviewMaterialEventState{Class: "pull_request", State: codeReviewPullRequestEventState(event.Action)}
	case "pull_request_review":
		reviewState := strings.ToLower(strings.TrimSpace(event.Review.State))
		if event.Action == "dismissed" {
			reviewState = "dismissed"
		}
		state.Event = codeReviewMaterialEventState{
			Class:       "review",
			ObjectID:    fmt.Sprintf("%d", event.Review.ID),
			State:       reviewState,
			Body:        strings.TrimSpace(event.Review.Body),
			CommitID:    strings.TrimSpace(event.Review.CommitID),
			AuthorLogin: canonicalCodeReviewBotLogin(event.Review.User.Login),
		}
	case "pull_request_review_comment":
		commentState := "present"
		if event.Action == "deleted" {
			commentState = "deleted"
		}
		state.Event = codeReviewMaterialEventState{
			Class:       "review_comment",
			ObjectID:    fmt.Sprintf("%d", event.Comment.ID),
			State:       commentState,
			Body:        strings.TrimSpace(event.Comment.Body),
			Path:        strings.TrimSpace(event.Comment.Path),
			Line:        codeReviewIntValue(event.Comment.Line),
			CommitID:    strings.TrimSpace(event.Comment.CommitID),
			AuthorLogin: canonicalCodeReviewBotLogin(event.Comment.User.Login),
		}
	case "pull_request_review_thread":
		state.Event = codeReviewMaterialEventState{
			Class:    "review_thread",
			ObjectID: strings.TrimSpace(event.Thread.NodeID),
			State:    strings.ToLower(strings.TrimSpace(event.Action)),
		}
	case "check_suite":
		state.Event = codeReviewMaterialCheckEventState(
			pr.CIStatus,
			fmt.Sprintf("check_suite:%d", event.CheckSuite.ID),
			event.CheckSuite.Conclusion,
			"",
		)
	case "check_run":
		state.Event = codeReviewMaterialCheckEventState(
			pr.CIStatus,
			fmt.Sprintf("check_run:%d", event.CheckRun.ID),
			event.CheckRun.Conclusion,
			"",
		)
	case "status":
		state.Event = codeReviewMaterialCheckEventState(
			pr.CIStatus,
			"status:"+strings.ToLower(strings.TrimSpace(event.Context)),
			nil,
			event.State,
		)
	default:
		state.Event = codeReviewMaterialEventState{Class: eventType, State: strings.ToLower(strings.TrimSpace(event.Action))}
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return "", fmt.Errorf("marshal material assessment state: %w", err)
	}
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("material:%x", sum[:]), nil
}

func codeReviewPullRequestEventState(action string) string {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "ready_for_review":
		return "ready"
	case "converted_to_draft":
		return "draft"
	default:
		// Synchronize, edit, and reopen events are fully represented by the
		// mirrored pull-request state included in the material key.
		return ""
	}
}

func codeReviewCheckState(conclusion *string, statusState string) string {
	value := strings.ToLower(strings.TrimSpace(statusState))
	if conclusion != nil {
		value = strings.ToLower(strings.TrimSpace(*conclusion))
	}
	switch value {
	case "success", "neutral", "skipped":
		return "success"
	case "failure", "error", "cancelled", "timed_out", "action_required", "startup_failure", "stale":
		return "failure"
	case "pending", "queued", "in_progress", "requested", "waiting":
		return "pending"
	default:
		return "unknown"
	}
}

func codeReviewMaterialCheckEventState(prCIStatus models.PullRequestCIStatus, objectID string, conclusion *string, statusState string) codeReviewMaterialEventState {
	state := codeReviewCheckState(conclusion, statusState)
	event := codeReviewMaterialEventState{Class: "checks", State: state}
	if codeReviewStoredCIState(prCIStatus) != state {
		event.ObjectID = strings.TrimSpace(objectID)
	}
	return event
}

func codeReviewStoredCIState(status models.PullRequestCIStatus) string {
	switch status {
	case models.PullRequestCIStatusSuccess:
		return "success"
	case models.PullRequestCIStatusFailure:
		return "failure"
	case models.PullRequestCIStatusPending:
		return "pending"
	default:
		return "unknown"
	}
}

func codeReviewIntValue(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func codeReviewStringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func codeReviewEventChangesAssessment(eventType string, event codeReviewReassessmentWebhook) bool {
	switch eventType {
	case "pull_request":
		switch event.Action {
		case "synchronize", "edited", "reopened", "ready_for_review", "converted_to_draft":
			return true
		}
	case "pull_request_review":
		return event.Action == "submitted" || event.Action == "edited" || event.Action == "dismissed"
	case "pull_request_review_comment":
		return event.Action == "created" || event.Action == "edited" || event.Action == "deleted"
	case "pull_request_review_thread":
		return event.Action == "resolved" || event.Action == "unresolved"
	case "check_suite", "check_run":
		return event.Action == "completed"
	case "status":
		return strings.TrimSpace(event.SHA) != ""
	}
	return false
}

func codeReviewReassessmentPullRequestNumbers(eventType string, event codeReviewReassessmentWebhook) []int {
	seen := make(map[int]struct{})
	add := func(numbers []int, number int) []int {
		if number <= 0 {
			return numbers
		}
		if _, ok := seen[number]; ok {
			return numbers
		}
		seen[number] = struct{}{}
		return append(numbers, number)
	}
	numbers := make([]int, 0, 2)
	switch eventType {
	case "pull_request":
		numbers = add(numbers, event.Number)
	case "pull_request_review", "pull_request_review_comment", "pull_request_review_thread":
		numbers = add(numbers, event.PullRequest.Number)
	case "check_suite":
		for _, ref := range event.CheckSuite.PullRequests {
			numbers = add(numbers, ref.Number)
		}
	case "check_run":
		for _, ref := range event.CheckRun.PullRequests {
			numbers = add(numbers, ref.Number)
		}
	}
	return numbers
}

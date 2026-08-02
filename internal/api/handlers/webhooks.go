package handlers

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/config"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type WebhookHandler struct {
	cfg                 *config.Config
	orgStore            *db.OrganizationStore
	userStore           *db.UserStore
	repoStore           *db.RepositoryStore
	integrationStore    *db.IntegrationStore
	githubInstallations *db.GitHubInstallationStore
	prService           *ghservice.PRService
	pullRequests        *db.PullRequestStore
	codeReviews         *codereviewsvc.Service
	codeReviewDisputes  *codereviewsvc.DisputeService
	codeReviewPRs       codeReviewPullRequestLoader
}

type codeReviewPullRequestLoader interface {
	GetCodeReviewPullRequestSnapshot(ctx context.Context, orgID, repositoryID uuid.UUID, number int) (ghservice.CodeReviewPullRequestSnapshot, error)
}

func NewWebhookHandler(cfg *config.Config, orgStore *db.OrganizationStore, userStore *db.UserStore, repoStore *db.RepositoryStore, integrationStore *db.IntegrationStore, prService *ghservice.PRService) *WebhookHandler {
	return &WebhookHandler{
		cfg:              cfg,
		orgStore:         orgStore,
		userStore:        userStore,
		repoStore:        repoStore,
		integrationStore: integrationStore,
		prService:        prService,
		codeReviewPRs:    prService,
	}
}

func (h *WebhookHandler) SetGitHubInstallationStore(store *db.GitHubInstallationStore) {
	h.githubInstallations = store
}

func (h *WebhookHandler) SetCodeReviewService(service *codereviewsvc.Service, pullRequests *db.PullRequestStore) {
	h.codeReviews = service
	h.pullRequests = pullRequests
}

func (h *WebhookHandler) SetCodeReviewDisputeService(service *codereviewsvc.DisputeService) {
	h.codeReviewDisputes = service
}

func (h *WebhookHandler) HandleGitHub(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "READ_FAILED", "failed to read request body")
		return
	}

	// Validate HMAC-SHA256 signature
	signature := r.Header.Get("X-Hub-Signature-256")
	if !h.verifySignature(body, signature) {
		writeError(w, r, http.StatusUnauthorized, "INVALID_SIGNATURE", "webhook signature verification failed")
		return
	}

	event := r.Header.Get("X-GitHub-Event")
	switch event {
	case "installation":
		h.handleInstallation(w, r, body)
	case "organization":
		h.handleOrganization(w, r, body)
	case "installation_repositories":
		h.handleInstallationRepositories(w, r, body)
	case "push":
		h.handlePush(w, r, body)
	case "pull_request":
		h.handlePullRequest(w, r, body)
	case "pull_request_review":
		h.handlePullRequestReview(w, r, body)
	case "pull_request_review_comment":
		h.handlePullRequestReviewComment(w, r, body)
	case "pull_request_review_thread":
		h.handlePullRequestReviewThread(w, r, body)
	case "issue_comment":
		h.handleIssueComment(w, r, body)
	case "check_suite":
		h.handleCheckSuite(w, r, body)
	case "check_run":
		h.handleCheckRun(w, r, body)
	case "status":
		h.handleStatus(w, r, body)
	default:
		writeJSON(w, http.StatusOK, map[string]string{"status": "ignored", "event": event})
	}
}

func (h *WebhookHandler) handlePullRequestReviewThread(w http.ResponseWriter, r *http.Request, body []byte) {
	if h.prService == nil {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ignored", "reason": "pr_service_not_configured"})
		return
	}
	var event ghservice.PullRequestReviewThreadEvent
	if err := json.Unmarshal(body, &event); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "failed to parse pull_request_review_thread event")
		return
	}
	metadata, err := feedbackWebhookMetadata(r, body, "pull_request_review_thread")
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "WEBHOOK_METADATA_FAILED", "failed to capture webhook metadata", err)
		return
	}
	event.FeedbackMetadata = metadata
	owner, ok := h.githubWebhookRepoActiveOwner(w, r, event.Repository.ID)
	if !ok {
		return
	}
	if owner.OrgID != uuid.Nil {
		event.OwnerOrgID = &owner.OrgID
	}
	if err := h.prService.HandlePullRequestReviewThreadEvent(r.Context(), event); err != nil {
		writeError(w, r, http.StatusInternalServerError, "REVIEW_THREAD_EVENT_FAILED", "failed to process pull_request_review_thread event", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "processed"})
}

func (h *WebhookHandler) handleIssueComment(w http.ResponseWriter, r *http.Request, body []byte) {
	if h.prService == nil {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ignored", "reason": "pr_service_not_configured"})
		return
	}

	var event ghservice.IssueCommentEvent
	if err := json.Unmarshal(body, &event); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "failed to parse issue_comment event")
		return
	}
	metadata, err := feedbackWebhookMetadata(r, body, "issue_comment")
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "WEBHOOK_METADATA_FAILED", "failed to capture webhook metadata", err)
		return
	}
	event.FeedbackMetadata = metadata
	event.DeliveryID = metadata.DeliveryID
	owner, ok := h.githubWebhookRepoActiveOwner(w, r, event.Repository.ID)
	if !ok {
		return
	}
	if owner.OrgID != uuid.Nil {
		event.OwnerOrgID = &owner.OrgID
	}

	if (event.Action == "created" || event.Action == "edited") && event.Issue.PullRequest != nil && h.codeReviews != nil && h.pullRequests != nil {
		ok, captured := h.handleCodeReviewMentioned(w, r, event, owner)
		if !ok {
			return
		}
		event.RecordOnly = captured
	}
	if err := h.prService.HandleIssueCommentEvent(r.Context(), event); err != nil {
		writeError(w, r, http.StatusInternalServerError, "ISSUE_COMMENT_EVENT_FAILED", "failed to process issue_comment event", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "processed"})
}

func (h *WebhookHandler) handleCodeReviewMentioned(w http.ResponseWriter, r *http.Request, event ghservice.IssueCommentEvent, owner db.GitHubRepoOwner) (bool, bool) {
	if !codereviewsvc.HasGitHubTeamMention(event.Comment.Body) {
		return true, false
	}
	repo := strings.TrimSpace(event.Repository.FullName)
	if repo == "" {
		repo = owner.FullName
	}
	matched, err := h.codeReviews.MatchesReviewMention(r.Context(), owner.OrgID, owner.RepositoryID, repo, event.Comment.Body)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "CODE_REVIEW_MENTION_MATCH_FAILED", "failed to match code review mention", err)
		return false, false
	}
	if !matched {
		return true, false
	}
	if !codeReviewMentionAuthorTrusted(event) &&
		(h.codeReviewDisputes == nil || event.Comment.PerformedViaGitHubApp != nil ||
			strings.EqualFold(strings.TrimSpace(event.Comment.User.Type), "bot") ||
			strings.EqualFold(strings.TrimSpace(event.Sender.Type), "bot")) {
		return true, false
	}
	if h.codeReviewPRs == nil {
		writeError(w, r, http.StatusInternalServerError, "CODE_REVIEW_PR_LOAD_FAILED", "failed to load pull request for code review mention")
		return false, false
	}
	remote, err := h.codeReviewPRs.GetCodeReviewPullRequestSnapshot(r.Context(), owner.OrgID, owner.RepositoryID, event.Issue.Number)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "CODE_REVIEW_PR_LOAD_FAILED", "failed to load pull request for code review mention", err)
		return false, false
	}
	if state := strings.TrimSpace(remote.State); state != "" && !strings.EqualFold(state, "open") {
		return true, false
	}
	number := remote.Number
	if number <= 0 {
		number = event.Issue.Number
	}
	snapshot := db.PullRequestGitHubSnapshot{
		GitHubPRURL: remote.HTMLURL,
		Title:       remote.Title,
		Body:        nilIfEmpty(remote.Body),
		HeadSHA:     nilIfEmpty(remote.HeadSHA),
		HeadRef:     nilIfEmpty(remote.HeadRef),
		BaseSHA:     nilIfEmpty(remote.BaseSHA),
	}
	pr, err := h.pullRequests.GetByOrgRepoAndNumber(r.Context(), owner.OrgID, repo, number)
	if errors.Is(err, pgx.ErrNoRows) {
		created := &models.PullRequest{
			OrgID:          owner.OrgID,
			GitHubPRNumber: number,
			GitHubPRURL:    snapshot.GitHubPRURL,
			GitHubRepo:     repo,
			Title:          snapshot.Title,
			Body:           snapshot.Body,
			Status:         models.PullRequestStatusOpen,
			ReviewStatus:   models.PullRequestReviewStatusPending,
			AuthoredBy:     models.GitIdentitySourceUser,
			HeadSHA:        snapshot.HeadSHA,
			HeadRef:        snapshot.HeadRef,
			BaseSHA:        snapshot.BaseSHA,
		}
		if err := h.pullRequests.Create(r.Context(), created); err != nil {
			writeError(w, r, http.StatusInternalServerError, "PR_MIRROR_CREATE_FAILED", "failed to create pull request mirror", err)
			return false, false
		}
		pr = *created
	} else if err != nil {
		writeError(w, r, http.StatusInternalServerError, "PR_LOAD_FAILED", "failed to load pull request mirror", err)
		return false, false
	} else {
		if err := h.pullRequests.UpdateGitHubSnapshot(r.Context(), owner.OrgID, pr.ID, snapshot); err != nil {
			writeError(w, r, http.StatusInternalServerError, "PR_MIRROR_UPDATE_FAILED", "failed to update pull request mirror", err)
			return false, false
		}
		pr.GitHubPRURL = snapshot.GitHubPRURL
		pr.Title = snapshot.Title
		pr.Body = snapshot.Body
		pr.HeadSHA = snapshot.HeadSHA
		pr.HeadRef = snapshot.HeadRef
		pr.BaseSHA = snapshot.BaseSHA
	}

	if h.codeReviewDisputes != nil && codereviewsvc.IsLikelyDisputeMention(event.Comment.Body) {
		privateRepo := false
		if h.repoStore != nil {
			repository, repoErr := h.repoStore.GetByID(r.Context(), owner.OrgID, owner.RepositoryID)
			if repoErr != nil {
				writeError(w, r, http.StatusInternalServerError, "REPOSITORY_LOAD_FAILED", "failed to load repository for code review dispute", repoErr)
				return false, false
			}
			privateRepo = repository.Private
		}
		authorType := models.PRFeedbackAuthorType(event.Comment.User.Type)
		if authorType.Validate() != nil {
			authorType = models.PRFeedbackAuthorTypeUnknown
		}
		_, captured, disputeErr := h.codeReviewDisputes.FileFromGitHub(r.Context(), codereviewsvc.FileGitHubCodeReviewDisputeInput{
			OrgID: owner.OrgID, PullRequestID: pr.ID, AuthorLogin: event.Comment.User.Login,
			AuthorType: authorType, AuthorAssociation: event.Comment.AuthorAssociation,
			RepositoryPrivate: privateRepo, Body: event.Comment.Body,
			GitHubCommentID: event.Comment.ID, SourceVersion: codeReviewCommentSourceVersion(event),
		})
		if disputeErr != nil {
			if errors.Is(disputeErr, codereviewsvc.ErrCodeReviewDisputeInvalidBody) {
				return true, false
			}
			writeError(w, r, http.StatusInternalServerError, "CODE_REVIEW_DISPUTE_CAPTURE_FAILED", "failed to capture code review dispute", disputeErr)
			return false, false
		}
		if captured {
			return true, true
		}
	}
	if !codeReviewMentionAuthorTrusted(event) {
		return true, false
	}

	_, err = h.codeReviews.HandleReviewMentioned(r.Context(), codereviewsvc.ReviewMentionedInput{
		ReviewRequestedInput: codereviewsvc.ReviewRequestedInput{
			OrgID:             owner.OrgID,
			RepositoryID:      owner.RepositoryID,
			PullRequestID:     pr.ID,
			GitHubRepo:        repo,
			GitHubPRNumber:    number,
			GitHubPRURL:       remote.HTMLURL,
			PullRequestTitle:  remote.Title,
			PullRequestAuthor: remote.AuthorLogin,
			BaseSHA:           remote.BaseSHA,
			HeadSHA:           remote.HeadSHA,
			FromFork:          remote.FromFork,
			DeliveryID:        event.DeliveryID,
		},
		CommentID:     event.Comment.ID,
		CommentAuthor: event.Comment.User.Login,
		CommentBody:   event.Comment.Body,
		CommentURL:    event.Comment.HTMLURL,
	})
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "CODE_REVIEW_REQUEST_FAILED", "failed to process code review mention", err)
		return false, false
	}
	return true, false
}

func codeReviewCommentSourceVersion(event ghservice.IssueCommentEvent) int64 {
	return codeReviewSourceVersion(event.Comment.UpdatedAt, event.Comment.Body)
}

func codeReviewSourceVersion(updatedAt *time.Time, body string) int64 {
	seed := strings.TrimSpace(body)
	if updatedAt != nil {
		seed = updatedAt.UTC().Format(time.RFC3339Nano) + "\x00" + seed
	}
	digest := sha256.Sum256([]byte(seed))
	// Assemble a positive 63-bit value from conversions that are each
	// representable in int64. Keeping the sign bit clear also satisfies the
	// database's positive source-version contract without an overflowing cast.
	high := int64(binary.BigEndian.Uint32(digest[:4]) & 0x7fffffff)
	low := int64(binary.BigEndian.Uint32(digest[4:8]))
	version := high<<32 | low
	if version == 0 {
		return 1
	}
	return version
}

func codeReviewMentionAuthorTrusted(event ghservice.IssueCommentEvent) bool {
	if event.Comment.PerformedViaGitHubApp != nil ||
		strings.EqualFold(strings.TrimSpace(event.Comment.User.Type), "bot") ||
		strings.EqualFold(strings.TrimSpace(event.Sender.Type), "bot") {
		return false
	}
	switch strings.ToUpper(strings.TrimSpace(event.Comment.AuthorAssociation)) {
	case "OWNER", "MEMBER", "COLLABORATOR":
		return true
	default:
		return false
	}
}

func (h *WebhookHandler) verifySignature(payload []byte, signature string) bool {
	if h.cfg.GitHubWebhookSecret == "" {
		return h.cfg.Env != "production"
	}
	if !strings.HasPrefix(signature, "sha256=") {
		return false
	}
	sig, err := hex.DecodeString(strings.TrimPrefix(signature, "sha256="))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(h.cfg.GitHubWebhookSecret))
	mac.Write(payload)
	expected := mac.Sum(nil)
	return hmac.Equal(sig, expected)
}

type installationEvent struct {
	Action       string              `json:"action"`
	Installation installationPayload `json:"installation"`
	Repositories []webhookRepo       `json:"repositories"`
}

type installationPayload struct {
	ID      int64          `json:"id"`
	Account webhookAccount `json:"account"`
}

type webhookAccount struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	Type  string `json:"type"`
}

type webhookRepo struct {
	ID       int64  `json:"id"`
	FullName string `json:"full_name"`
	Private  bool   `json:"private"`
}

type installationReposEvent struct {
	Action              string              `json:"action"`
	Installation        installationPayload `json:"installation"`
	RepositoriesAdded   []webhookRepo       `json:"repositories_added"`
	RepositoriesRemoved []webhookRepo       `json:"repositories_removed"`
}

type organizationEvent struct {
	Action       string              `json:"action"`
	Installation installationPayload `json:"installation"`
	Organization webhookAccount      `json:"organization"`
	Membership   struct {
		User webhookAccount `json:"user"`
	} `json:"membership"`
}

func (h *WebhookHandler) handleInstallation(w http.ResponseWriter, r *http.Request, body []byte) {
	var event installationEvent
	if err := json.Unmarshal(body, &event); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "failed to parse installation event")
		return
	}

	ctx := r.Context()

	switch event.Action {
	case "created", "new_permissions_accepted":
		if h.githubInstallations != nil {
			inst := &models.GitHubInstallation{
				InstallationID: event.Installation.ID,
				AccountID:      event.Installation.Account.ID,
				AccountLogin:   event.Installation.Account.Login,
				AccountType:    nilIfEmpty(event.Installation.Account.Type),
				Status:         "active",
			}
			if err := h.githubInstallations.UpsertInstallation(ctx, inst); err != nil {
				writeError(w, r, http.StatusInternalServerError, "INSTALLATION_UPSERT_FAILED", "failed to record github installation", err)
				return
			}
			if err := h.githubInstallations.RefreshOrgLinkAccountLogin(ctx, event.Installation.ID, event.Installation.Account.Login); err != nil {
				writeError(w, r, http.StatusInternalServerError, "INSTALLATION_LINK_UPDATE_FAILED", "failed to refresh github installation links", err)
				return
			}
		}

		if event.Action == "new_permissions_accepted" {
			writeJSON(w, http.StatusOK, map[string]string{"status": "installation permissions accepted"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "installation created"})

	case "deleted":
		if h.githubInstallations != nil {
			if err := h.githubInstallations.SetInstallationStatus(ctx, event.Installation.ID, "deleted"); err != nil {
				writeError(w, r, http.StatusInternalServerError, "INSTALLATION_UPDATE_FAILED", "failed to update github installation", err)
				return
			}
			if err := h.githubInstallations.DeactivateOrgLinksByInstallationID(ctx, event.Installation.ID); err != nil {
				writeError(w, r, http.StatusInternalServerError, "INSTALLATION_LINK_UPDATE_FAILED", "failed to deactivate github installation links", err)
				return
			}
			if err := h.githubInstallations.ClearRosterForInstallation(ctx, event.Installation.ID); err != nil {
				writeError(w, r, http.StatusInternalServerError, "INSTALLATION_ROSTER_CLEAR_FAILED", "failed to clear github organization roster", err)
				return
			}
		}
		// Disconnect all repos for this installation
		if err := h.repoStore.DisconnectByInstallationID(ctx, event.Installation.ID); err != nil {
			writeError(w, r, http.StatusInternalServerError, "DISCONNECT_FAILED", "failed to disconnect repositories", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "installation deleted"})

	default:
		writeJSON(w, http.StatusOK, map[string]string{"status": "ignored", "action": event.Action})
	}
}

func (h *WebhookHandler) handleOrganization(w http.ResponseWriter, r *http.Request, body []byte) {
	if h.githubInstallations == nil {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ignored", "reason": "github_installation_store_not_configured"})
		return
	}
	var event organizationEvent
	if err := json.Unmarshal(body, &event); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "failed to parse organization event")
		return
	}
	switch event.Action {
	case "member_added":
		if err := h.githubInstallations.UpsertOrgMember(r.Context(), event.Installation.ID, event.Membership.User.ID, event.Membership.User.Login); err != nil {
			writeError(w, r, http.StatusInternalServerError, "ORG_MEMBER_UPSERT_FAILED", "failed to update github organization roster", err)
			return
		}
	case "member_removed":
		if event.Membership.User.ID == 0 {
			writeJSON(w, http.StatusOK, map[string]string{"status": "organization updated"})
			return
		}
		if err := h.githubInstallations.DeleteOrgMember(r.Context(), event.Installation.ID, event.Membership.User.ID); err != nil {
			writeError(w, r, http.StatusInternalServerError, "ORG_MEMBER_DELETE_FAILED", "failed to update github organization roster", err)
			return
		}
	case "renamed":
		if event.Organization.Login != "" {
			if err := h.githubInstallations.RefreshOrgLinkAccountLogin(r.Context(), event.Installation.ID, event.Organization.Login); err != nil {
				writeError(w, r, http.StatusInternalServerError, "INSTALLATION_LINK_UPDATE_FAILED", "failed to refresh github organization name", err)
				return
			}
		}
	default:
		writeJSON(w, http.StatusOK, map[string]string{"status": "ignored", "action": event.Action})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "organization updated"})
}

func (h *WebhookHandler) handleInstallationRepositories(w http.ResponseWriter, r *http.Request, body []byte) {
	var event installationReposEvent
	if err := json.Unmarshal(body, &event); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "failed to parse installation_repositories event")
		return
	}

	ctx := r.Context()

	// Added repositories expand the GitHub installation's accessible set, but
	// they do not become active in a 143 organization until an admin explicitly
	// claims them from the integrations UI.
	_ = event.RepositoriesAdded

	// For removed repos, mark as disconnected
	for _, whRepo := range event.RepositoriesRemoved {
		if err := h.repoStore.DisconnectByGitHubID(ctx, event.Installation.ID, whRepo.ID); err != nil {
			writeError(w, r, http.StatusInternalServerError, "REPOSITORY_DISCONNECT_FAILED", "failed to disconnect repository", err)
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "repositories updated"})
}

func (h *WebhookHandler) handlePush(w http.ResponseWriter, r *http.Request, body []byte) {
	if h.prService == nil {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ignored", "reason": "pr_service_not_configured"})
		return
	}
	var event ghservice.PushEvent
	if err := json.Unmarshal(body, &event); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "failed to parse push event")
		return
	}
	owner, ok := h.githubWebhookRepoActiveOwner(w, r, event.Repository.ID)
	if !ok {
		return
	}
	if owner.OrgID != uuid.Nil {
		event.OwnerOrgID = &owner.OrgID
	}
	if err := h.prService.HandlePushEvent(r.Context(), event); err != nil {
		writeError(w, r, http.StatusInternalServerError, "PUSH_EVENT_FAILED", "failed to process push event", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "processed"})
}

func (h *WebhookHandler) handlePullRequest(w http.ResponseWriter, r *http.Request, body []byte) {
	if h.prService == nil {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ignored", "reason": "pr_service_not_configured"})
		return
	}

	var event ghservice.PullRequestEvent
	if err := json.Unmarshal(body, &event); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "failed to parse pull_request event")
		return
	}
	event.DeliveryID = strings.TrimSpace(r.Header.Get("X-GitHub-Delivery"))
	owner, ok := h.githubWebhookRepoActiveOwner(w, r, event.Repository.ID)
	if !ok {
		return
	}
	if owner.OrgID != uuid.Nil {
		event.OwnerOrgID = &owner.OrgID
	}

	if err := h.prService.HandlePullRequestEvent(r.Context(), event); err != nil {
		writeError(w, r, http.StatusInternalServerError, "PR_EVENT_FAILED", "failed to process pull_request event", err)
		return
	}
	if event.Action == "review_requested" && h.codeReviews != nil && h.pullRequests != nil {
		if ok := h.handleCodeReviewRequested(w, r, body, owner); !ok {
			return
		}
	}
	if err := h.reassessCodeReviewsForGitHubEvent(r.Context(), owner, "pull_request", body, r.Header.Get("X-GitHub-Delivery")); err != nil {
		writeError(w, r, http.StatusInternalServerError, "CODE_REVIEW_REASSESSMENT_FAILED", "failed to reassess code review", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "processed"})
}

type codeReviewRequestedWebhook struct {
	Action     string `json:"action"`
	Number     int    `json:"number"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	RequestedReviewer *struct {
		Login string `json:"login"`
	} `json:"requested_reviewer"`
	RequestedTeam *struct {
		Slug string `json:"slug"`
	} `json:"requested_team"`
	PullRequest struct {
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
}

func (h *WebhookHandler) handleCodeReviewRequested(w http.ResponseWriter, r *http.Request, body []byte, owner db.GitHubRepoOwner) bool {
	var event codeReviewRequestedWebhook
	if err := json.Unmarshal(body, &event); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "failed to parse review_requested event")
		return false
	}
	snapshot := db.PullRequestGitHubSnapshot{
		GitHubPRURL: event.PullRequest.HTMLURL,
		Title:       event.PullRequest.Title,
		Body:        nilIfEmpty(event.PullRequest.Body),
		HeadSHA:     nilIfEmpty(event.PullRequest.Head.SHA),
		HeadRef:     nilIfEmpty(event.PullRequest.Head.Ref),
		BaseSHA:     nilIfEmpty(event.PullRequest.Base.SHA),
	}
	pr, err := h.pullRequests.GetByOrgRepoAndNumber(r.Context(), owner.OrgID, event.Repository.FullName, event.Number)
	if errors.Is(err, pgx.ErrNoRows) {
		created := &models.PullRequest{
			OrgID:          owner.OrgID,
			GitHubPRNumber: event.Number,
			GitHubPRURL:    snapshot.GitHubPRURL,
			GitHubRepo:     event.Repository.FullName,
			Title:          snapshot.Title,
			Body:           snapshot.Body,
			Status:         models.PullRequestStatusOpen,
			ReviewStatus:   models.PullRequestReviewStatusPending,
			AuthoredBy:     models.GitIdentitySourceUser,
			HeadSHA:        snapshot.HeadSHA,
			HeadRef:        snapshot.HeadRef,
			BaseSHA:        snapshot.BaseSHA,
		}
		if err := h.pullRequests.Create(r.Context(), created); err != nil {
			writeError(w, r, http.StatusInternalServerError, "PR_MIRROR_CREATE_FAILED", "failed to create pull request mirror", err)
			return false
		}
		pr = *created
	} else if err != nil {
		writeError(w, r, http.StatusInternalServerError, "PR_LOAD_FAILED", "failed to load pull request mirror", err)
		return false
	} else {
		if err := h.pullRequests.UpdateGitHubSnapshot(r.Context(), owner.OrgID, pr.ID, snapshot); err != nil {
			writeError(w, r, http.StatusInternalServerError, "PR_MIRROR_UPDATE_FAILED", "failed to update pull request mirror", err)
			return false
		}
		pr.GitHubPRURL = snapshot.GitHubPRURL
		pr.Title = snapshot.Title
		pr.Body = snapshot.Body
		pr.HeadSHA = snapshot.HeadSHA
		pr.HeadRef = snapshot.HeadRef
		pr.BaseSHA = snapshot.BaseSHA
	}
	requestedLogin := ""
	if event.RequestedReviewer != nil {
		requestedLogin = event.RequestedReviewer.Login
	}
	requestedTeam := ""
	if event.RequestedTeam != nil {
		requestedTeam = event.RequestedTeam.Slug
	}
	headSHA := event.PullRequest.Head.SHA
	if headSHA == "" && pr.HeadSHA != nil {
		headSHA = *pr.HeadSHA
	}
	result, err := h.codeReviews.HandleReviewRequested(r.Context(), codereviewsvc.ReviewRequestedInput{
		OrgID:             owner.OrgID,
		RepositoryID:      owner.RepositoryID,
		PullRequestID:     pr.ID,
		GitHubRepo:        event.Repository.FullName,
		GitHubPRNumber:    event.Number,
		GitHubPRURL:       event.PullRequest.HTMLURL,
		PullRequestTitle:  event.PullRequest.Title,
		PullRequestAuthor: event.PullRequest.User.Login,
		BaseSHA:           event.PullRequest.Base.SHA,
		HeadSHA:           headSHA,
		FromFork:          event.PullRequest.Head.Repo.Fork,
		RequestedLogin:    requestedLogin,
		RequestedTeam:     requestedTeam,
		DeliveryID:        strings.TrimSpace(r.Header.Get("X-GitHub-Delivery")),
	})
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "CODE_REVIEW_REQUEST_FAILED", "failed to process code review request", err)
		return false
	}
	if !result.Processed {
		// Non-matching reviewer requests are valid GitHub events; they are just
		// not for this product surface.
		return true
	}
	return true
}

func (h *WebhookHandler) handlePullRequestReview(w http.ResponseWriter, r *http.Request, body []byte) {
	if h.prService == nil {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ignored", "reason": "pr_service_not_configured"})
		return
	}

	var event ghservice.PullRequestReviewEvent
	if err := json.Unmarshal(body, &event); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "failed to parse pull_request_review event")
		return
	}
	metadata, err := feedbackWebhookMetadata(r, body, "pull_request_review")
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "WEBHOOK_METADATA_FAILED", "failed to capture webhook metadata", err)
		return
	}
	event.FeedbackMetadata = metadata
	event.DeliveryID = metadata.DeliveryID
	owner, ok := h.githubWebhookRepoActiveOwner(w, r, event.Repository.ID)
	if !ok {
		return
	}
	if owner.OrgID != uuid.Nil {
		event.OwnerOrgID = &owner.OrgID
	}

	if err := h.prService.HandlePullRequestReviewEvent(r.Context(), event); err != nil {
		writeError(w, r, http.StatusInternalServerError, "REVIEW_EVENT_FAILED", "failed to process pull_request_review event", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "processed"})
}

func (h *WebhookHandler) handlePullRequestReviewComment(w http.ResponseWriter, r *http.Request, body []byte) {
	if h.prService == nil {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ignored", "reason": "pr_service_not_configured"})
		return
	}

	var event ghservice.PullRequestReviewCommentEvent
	if err := json.Unmarshal(body, &event); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "failed to parse pull_request_review_comment event")
		return
	}
	metadata, err := feedbackWebhookMetadata(r, body, "pull_request_review_comment")
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "WEBHOOK_METADATA_FAILED", "failed to capture webhook metadata", err)
		return
	}
	event.FeedbackMetadata = metadata
	event.DeliveryID = metadata.DeliveryID
	owner, ok := h.githubWebhookRepoActiveOwner(w, r, event.Repository.ID)
	if !ok {
		return
	}
	if owner.OrgID != uuid.Nil {
		event.OwnerOrgID = &owner.OrgID
	}

	if (event.Action == "created" || event.Action == "edited") && event.Comment.InReplyToID != nil && h.codeReviewDisputes != nil && h.pullRequests != nil {
		ok, captured := h.handleCodeReviewInlineDispute(w, r, event, owner)
		if !ok {
			return
		}
		event.RecordOnly = captured
	}
	if err := h.prService.HandlePullRequestReviewCommentEvent(r.Context(), event); err != nil {
		writeError(w, r, http.StatusInternalServerError, "REVIEW_COMMENT_EVENT_FAILED", "failed to process pull_request_review_comment event", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "processed"})
}

func (h *WebhookHandler) handleCodeReviewInlineDispute(w http.ResponseWriter, r *http.Request, event ghservice.PullRequestReviewCommentEvent, owner db.GitHubRepoOwner) (bool, bool) {
	if event.Comment.PerformedViaGitHubApp != nil ||
		strings.EqualFold(strings.TrimSpace(event.Comment.User.Type), "bot") ||
		strings.EqualFold(strings.TrimSpace(event.Sender.Type), "bot") {
		return true, false
	}
	pr, err := h.pullRequests.GetByOrgRepoAndNumber(r.Context(), owner.OrgID, event.Repository.FullName, event.PullRequest.Number)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, false
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "PR_LOAD_FAILED", "failed to load pull request for code review dispute", err)
		return false, false
	}
	privateRepo := false
	if h.repoStore != nil {
		repo, repoErr := h.repoStore.GetByID(r.Context(), owner.OrgID, owner.RepositoryID)
		if repoErr != nil {
			writeError(w, r, http.StatusInternalServerError, "REPOSITORY_LOAD_FAILED", "failed to load repository for code review dispute", repoErr)
			return false, false
		}
		privateRepo = repo.Private
	}
	authorType := models.PRFeedbackAuthorType(event.Comment.User.Type)
	if authorType.Validate() != nil {
		authorType = models.PRFeedbackAuthorTypeUnknown
	}
	_, captured, err := h.codeReviewDisputes.FileFromGitHub(r.Context(), codereviewsvc.FileGitHubCodeReviewDisputeInput{
		OrgID: owner.OrgID, PullRequestID: pr.ID, InlineThreadRootID: event.Comment.InReplyToID,
		AuthorLogin: event.Comment.User.Login, AuthorType: authorType,
		AuthorAssociation: event.Comment.AuthorAssociation, RepositoryPrivate: privateRepo,
		Body: event.Comment.Body, GitHubCommentID: event.Comment.ID, SourceVersion: codeReviewSourceVersion(event.Comment.UpdatedAt, event.Comment.Body),
	})
	if err != nil {
		if errors.Is(err, codereviewsvc.ErrCodeReviewDisputeInvalidBody) {
			return true, false
		}
		writeError(w, r, http.StatusInternalServerError, "CODE_REVIEW_DISPUTE_CAPTURE_FAILED", "failed to capture code review dispute", err)
		return false, false
	}
	return true, captured
}

func feedbackWebhookMetadata(r *http.Request, body []byte, eventType string) (ghservice.FeedbackWebhookMetadata, error) {
	headers, err := json.Marshal(r.Header)
	if err != nil {
		return ghservice.FeedbackWebhookMetadata{}, fmt.Errorf("marshal GitHub webhook headers: %w", err)
	}
	return ghservice.FeedbackWebhookMetadata{DeliveryID: r.Header.Get("X-GitHub-Delivery"), EventType: eventType, Payload: append([]byte(nil), body...), Headers: headers}, nil
}

func (h *WebhookHandler) handleCheckSuite(w http.ResponseWriter, r *http.Request, body []byte) {
	if h.prService == nil {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ignored", "reason": "pr_service_not_configured"})
		return
	}

	var event ghservice.CheckSuiteEvent
	if err := json.Unmarshal(body, &event); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "failed to parse check_suite event")
		return
	}
	event.DeliveryID = strings.TrimSpace(r.Header.Get("X-GitHub-Delivery"))
	owner, ok := h.githubWebhookRepoActiveOwner(w, r, event.Repository.ID)
	if !ok {
		return
	}
	if owner.OrgID != uuid.Nil {
		event.OwnerOrgID = &owner.OrgID
	}

	if err := h.prService.HandleCheckSuiteEvent(r.Context(), event); err != nil {
		writeError(w, r, http.StatusInternalServerError, "CHECK_SUITE_FAILED", "failed to process check_suite event", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "processed"})
}

func (h *WebhookHandler) handleCheckRun(w http.ResponseWriter, r *http.Request, body []byte) {
	if h.prService == nil {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ignored", "reason": "pr_service_not_configured"})
		return
	}

	var event ghservice.CheckRunEvent
	if err := json.Unmarshal(body, &event); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "failed to parse check_run event")
		return
	}
	event.DeliveryID = strings.TrimSpace(r.Header.Get("X-GitHub-Delivery"))
	owner, ok := h.githubWebhookRepoActiveOwner(w, r, event.Repository.ID)
	if !ok {
		return
	}
	if owner.OrgID != uuid.Nil {
		event.OwnerOrgID = &owner.OrgID
	}

	if err := h.prService.HandleCheckRunEvent(r.Context(), event); err != nil {
		writeError(w, r, http.StatusInternalServerError, "CHECK_RUN_FAILED", "failed to process check_run event", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "processed"})
}

func (h *WebhookHandler) handleStatus(w http.ResponseWriter, r *http.Request, body []byte) {
	if h.prService == nil {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ignored", "reason": "pr_service_not_configured"})
		return
	}

	var event ghservice.StatusEvent
	if err := json.Unmarshal(body, &event); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "failed to parse status event")
		return
	}
	event.DeliveryID = strings.TrimSpace(r.Header.Get("X-GitHub-Delivery"))
	owner, ok := h.githubWebhookRepoActiveOwner(w, r, event.Repository.ID)
	if !ok {
		return
	}
	if owner.OrgID != uuid.Nil {
		event.OwnerOrgID = &owner.OrgID
	}

	if err := h.prService.HandleStatusEvent(r.Context(), event); err != nil {
		writeError(w, r, http.StatusInternalServerError, "STATUS_FAILED", "failed to process status event", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "processed"})
}

func (h *WebhookHandler) githubWebhookRepoActiveOwner(w http.ResponseWriter, r *http.Request, githubID int64) (db.GitHubRepoOwner, bool) {
	if githubID == 0 {
		return db.GitHubRepoOwner{}, true
	}
	owner, err := h.repoStore.GetActiveOwnerByGitHubID(r.Context(), githubID)
	if err == nil {
		return owner, true
	}
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ignored", "reason": "repo_not_claimed"})
		return db.GitHubRepoOwner{}, false
	}
	writeError(w, r, http.StatusInternalServerError, "REPOSITORY_OWNER_LOOKUP_FAILED", "failed to look up repository owner", err)
	return db.GitHubRepoOwner{}, false
}

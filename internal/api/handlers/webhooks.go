package handlers

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/assembledhq/143/internal/config"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
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
}

func NewWebhookHandler(cfg *config.Config, orgStore *db.OrganizationStore, userStore *db.UserStore, repoStore *db.RepositoryStore, integrationStore *db.IntegrationStore, prService *ghservice.PRService) *WebhookHandler {
	return &WebhookHandler{
		cfg:              cfg,
		orgStore:         orgStore,
		userStore:        userStore,
		repoStore:        repoStore,
		integrationStore: integrationStore,
		prService:        prService,
	}
}

func (h *WebhookHandler) SetGitHubInstallationStore(store *db.GitHubInstallationStore) {
	h.githubInstallations = store
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
	case "installation_repositories":
		h.handleInstallationRepositories(w, r, body)
	case "pull_request":
		h.handlePullRequest(w, r, body)
	case "pull_request_review":
		h.handlePullRequestReview(w, r, body)
	case "pull_request_review_comment":
		h.handlePullRequestReviewComment(w, r, body)
	case "check_suite":
		h.handleCheckSuite(w, r, body)
	case "check_run":
		h.handleCheckRun(w, r, body)
	default:
		writeJSON(w, http.StatusOK, map[string]string{"status": "ignored", "event": event})
	}
}

func (h *WebhookHandler) verifySignature(payload []byte, signature string) bool {
	if h.cfg.GitHubWebhookSecret == "" {
		return true // no secret configured, skip verification
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

func (h *WebhookHandler) handleInstallation(w http.ResponseWriter, r *http.Request, body []byte) {
	var event installationEvent
	if err := json.Unmarshal(body, &event); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "failed to parse installation event")
		return
	}

	ctx := r.Context()

	switch event.Action {
	case "created":
		if h.githubInstallations != nil {
			inst := &models.GitHubInstallation{
				InstallationID: event.Installation.ID,
				AccountID:      event.Installation.Account.ID,
				AccountLogin:   event.Installation.Account.Login,
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

	writeJSON(w, http.StatusOK, map[string]string{"status": "processed"})
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
	owner, ok := h.githubWebhookRepoActiveOwner(w, r, event.Repository.ID)
	if !ok {
		return
	}
	if owner.OrgID != uuid.Nil {
		event.OwnerOrgID = &owner.OrgID
	}

	if err := h.prService.HandlePullRequestReviewCommentEvent(r.Context(), event); err != nil {
		writeError(w, r, http.StatusInternalServerError, "REVIEW_COMMENT_EVENT_FAILED", "failed to process pull_request_review_comment event", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "processed"})
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

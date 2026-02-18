package handlers

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/assembledhq/143/internal/config"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	ghservice "github.com/assembledhq/143/internal/services/github"
)

type WebhookHandler struct {
	cfg              *config.Config
	orgStore         *db.OrganizationStore
	repoStore        *db.RepositoryStore
	integrationStore *db.IntegrationStore
	prService        *ghservice.PRService
}

func NewWebhookHandler(cfg *config.Config, orgStore *db.OrganizationStore, repoStore *db.RepositoryStore, integrationStore *db.IntegrationStore, prService *ghservice.PRService) *WebhookHandler {
	return &WebhookHandler{
		cfg:              cfg,
		orgStore:         orgStore,
		repoStore:        repoStore,
		integrationStore: integrationStore,
		prService:        prService,
	}
}

func (h *WebhookHandler) HandleGitHub(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "READ_FAILED", "failed to read request body")
		return
	}

	// Validate HMAC-SHA256 signature
	signature := r.Header.Get("X-Hub-Signature-256")
	if !h.verifySignature(body, signature) {
		writeError(w, http.StatusUnauthorized, "INVALID_SIGNATURE", "webhook signature verification failed")
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
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "failed to parse installation event")
		return
	}

	ctx := r.Context()

	switch event.Action {
	case "created":
		// Create a default org for the installation (or find existing)
		org := &models.Organization{
			Name:     event.Installation.Account.Login,
			Slug:     event.Installation.Account.Login,
			Settings: json.RawMessage(`{}`),
		}
		// Try to get existing org by slug first
		existingOrg, err := h.orgStore.GetBySlug(ctx, org.Slug)
		if err == nil {
			org = &existingOrg
		} else {
			if createErr := h.orgStore.Create(ctx, org); createErr != nil {
				writeError(w, http.StatusInternalServerError, "ORG_CREATE_FAILED", "failed to create organization")
				return
			}
		}

		// Create integration
		configJSON, _ := json.Marshal(map[string]any{
			"installation_id": event.Installation.ID,
			"account_login":   event.Installation.Account.Login,
		})
		integration := &models.Integration{
			OrgID:    org.ID,
			Provider: "github",
			Config:   configJSON,
			Status:   "active",
		}
		if err := h.integrationStore.Create(ctx, integration); err != nil {
			writeError(w, http.StatusInternalServerError, "INTEGRATION_CREATE_FAILED", "failed to create integration")
			return
		}

		// Create repositories
		for _, whRepo := range event.Repositories {
			repo := &models.Repository{
				OrgID:          org.ID,
				IntegrationID:  integration.ID,
				GitHubID:       whRepo.ID,
				FullName:       whRepo.FullName,
				DefaultBranch:  "main",
				Private:        whRepo.Private,
				CloneURL:       "https://github.com/" + whRepo.FullName + ".git",
				InstallationID: event.Installation.ID,
				Status:         "active",
				Settings:       json.RawMessage(`{}`),
			}
			if err := h.repoStore.UpsertFromGitHub(ctx, repo); err != nil {
				writeError(w, http.StatusInternalServerError, "REPOSITORY_UPSERT_FAILED", "failed to upsert repository")
				return
			}
		}

		writeJSON(w, http.StatusOK, map[string]string{"status": "installation created"})

	case "deleted":
		// Disconnect all repos for this installation
		if err := h.repoStore.DisconnectByInstallationID(ctx, event.Installation.ID); err != nil {
			writeError(w, http.StatusInternalServerError, "DISCONNECT_FAILED", "failed to disconnect repositories")
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
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "failed to parse installation_repositories event")
		return
	}

	ctx := r.Context()

	// For added repos, upsert them
	for _, whRepo := range event.RepositoriesAdded {
		repo := &models.Repository{
			GitHubID:       whRepo.ID,
			FullName:       whRepo.FullName,
			DefaultBranch:  "main",
			Private:        whRepo.Private,
			CloneURL:       "https://github.com/" + whRepo.FullName + ".git",
			InstallationID: event.Installation.ID,
			Status:         "active",
			Settings:       json.RawMessage(`{}`),
		}
		if err := h.repoStore.UpsertFromGitHub(ctx, repo); err != nil {
			writeError(w, http.StatusInternalServerError, "REPOSITORY_UPSERT_FAILED", "failed to upsert repository")
			return
		}
	}

	// For removed repos, mark as disconnected
	for range event.RepositoriesRemoved {
		// TODO: look up by github_id and update status to disconnected
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
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "failed to parse pull_request event")
		return
	}

	if err := h.prService.HandlePullRequestEvent(r.Context(), event); err != nil {
		writeError(w, http.StatusInternalServerError, "PR_EVENT_FAILED", "failed to process pull_request event")
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
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "failed to parse pull_request_review event")
		return
	}

	if err := h.prService.HandlePullRequestReviewEvent(r.Context(), event); err != nil {
		writeError(w, http.StatusInternalServerError, "REVIEW_EVENT_FAILED", "failed to process pull_request_review event")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "processed"})
}

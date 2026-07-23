package codereview

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	ghservice "github.com/assembledhq/143/internal/services/github"
	githubtelemetry "github.com/assembledhq/143/internal/services/github/telemetry"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
)

const defaultGitHubAPIBaseURL = "https://api.github.com"

var (
	ErrGitHubTriggerAuthRequired       = errors.New("github user authorization required")
	ErrGitHubTriggerPermissionRequired = errors.New("github permissions required for code review trigger setup")
)

type GitHubTriggerStore interface {
	GetActiveGitHubTrigger(ctx context.Context, orgID, repositoryID uuid.UUID) (models.CodeReviewGitHubTriggerSetting, error)
	SaveGitHubTrigger(ctx context.Context, orgID uuid.UUID, params db.SaveCodeReviewGitHubTriggerParams) (models.CodeReviewGitHubTriggerSetting, error)
	DeactivateGitHubTrigger(ctx context.Context, orgID, repositoryID uuid.UUID, createdByUserID *uuid.UUID) error
}

type GitHubTriggerRepositoryStore interface {
	GetByID(ctx context.Context, orgID, repoID uuid.UUID) (models.Repository, error)
}

type GitHubTriggerAppUserAuth interface {
	GetValidCredential(ctx context.Context, orgID, userID uuid.UUID) (*models.GitHubAppUserConfig, error)
}

type GitHubTriggerSetupService struct {
	triggers    GitHubTriggerStore
	repos       GitHubTriggerRepositoryStore
	appUserAuth GitHubTriggerAppUserAuth
	httpClient  *http.Client
	apiBaseURL  string
	logger      zerolog.Logger
}

type GitHubTriggerSetupInput struct {
	OrgID        uuid.UUID
	UserID       uuid.UUID
	RepositoryID uuid.UUID
}

type githubTriggerTeam struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

func NewGitHubTriggerSetupService(triggers GitHubTriggerStore, repos GitHubTriggerRepositoryStore, appUserAuth GitHubTriggerAppUserAuth, logger zerolog.Logger) *GitHubTriggerSetupService {
	return &GitHubTriggerSetupService{
		triggers:    triggers,
		repos:       repos,
		appUserAuth: appUserAuth,
		httpClient:  githubtelemetry.NewHTTPClient(15*time.Second, logger),
		apiBaseURL:  defaultGitHubAPIBaseURL,
		logger:      logger,
	}
}

func (s *GitHubTriggerSetupService) SetAPIBaseURLForTest(baseURL string) {
	s.apiBaseURL = strings.TrimRight(baseURL, "/")
}

func (s *GitHubTriggerSetupService) SetHTTPClientForTest(client *http.Client) {
	if client != nil {
		s.httpClient = client
	}
}

func (s *GitHubTriggerSetupService) Status(ctx context.Context, orgID, userID, repositoryID uuid.UUID) (models.CodeReviewGitHubTriggerResponse, error) {
	repo, err := s.loadRepository(ctx, orgID, repositoryID)
	if err != nil {
		return models.CodeReviewGitHubTriggerResponse{}, err
	}
	resp := defaultGitHubTriggerResponse(repo)
	setting, err := s.triggers.GetActiveGitHubTrigger(ctx, orgID, repositoryID)
	if err == nil {
		resp.Status = models.CodeReviewGitHubTriggerStatusReady
		resp.Trigger = &setting
		resp.TeamSlug = setting.TeamSlug
		resp.TeamName = setting.TeamName
		resp.RepoPermission = setting.RepoPermission
		resp.TeamReviewer = "@" + resp.GitHubOrg + "/" + setting.TeamSlug
		return resp, nil
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return models.CodeReviewGitHubTriggerResponse{}, fmt.Errorf("load code review GitHub trigger: %w", err)
	}
	if _, err := s.validUserCredential(ctx, orgID, userID); err != nil {
		if errors.Is(err, ErrGitHubTriggerAuthRequired) {
			resp.Status = models.CodeReviewGitHubTriggerStatusAuthRequired
			resp.Message = "Connect your GitHub account before creating the reviewer team."
			return resp, nil
		}
		return models.CodeReviewGitHubTriggerResponse{}, err
	}
	return resp, nil
}

func (s *GitHubTriggerSetupService) Setup(ctx context.Context, input GitHubTriggerSetupInput) (models.CodeReviewGitHubTriggerResponse, error) {
	if input.OrgID == uuid.Nil || input.UserID == uuid.Nil || input.RepositoryID == uuid.Nil {
		return models.CodeReviewGitHubTriggerResponse{}, fmt.Errorf("org_id, user_id, and repository_id are required")
	}
	repo, err := s.loadRepository(ctx, input.OrgID, input.RepositoryID)
	if err != nil {
		return models.CodeReviewGitHubTriggerResponse{}, err
	}
	owner, name, err := splitGitHubFullName(repo.FullName)
	if err != nil {
		return models.CodeReviewGitHubTriggerResponse{}, err
	}
	cred, err := s.validUserCredential(ctx, input.OrgID, input.UserID)
	if err != nil {
		return models.CodeReviewGitHubTriggerResponse{}, err
	}

	team, err := s.getOrCreateTeam(ctx, cred.AccessToken, owner)
	if err != nil {
		return models.CodeReviewGitHubTriggerResponse{}, err
	}
	if err := s.grantTeamRepository(ctx, cred.AccessToken, owner, team.Slug, owner, name); err != nil {
		return models.CodeReviewGitHubTriggerResponse{}, err
	}

	setting, err := s.triggers.SaveGitHubTrigger(ctx, input.OrgID, db.SaveCodeReviewGitHubTriggerParams{
		RepositoryID:    input.RepositoryID,
		InstallationID:  repo.InstallationID,
		TeamSlug:        team.Slug,
		TeamName:        firstNonEmpty(team.Name, models.DefaultCodeReviewGitHubTriggerTeamName),
		TeamID:          team.ID,
		RepoPermission:  models.DefaultCodeReviewGitHubTriggerRepoPerm,
		CreatedByUserID: &input.UserID,
	})
	if err != nil {
		return models.CodeReviewGitHubTriggerResponse{}, fmt.Errorf("save code review GitHub trigger: %w", err)
	}

	resp := defaultGitHubTriggerResponse(repo)
	resp.Status = models.CodeReviewGitHubTriggerStatusReady
	resp.Trigger = &setting
	resp.TeamSlug = setting.TeamSlug
	resp.TeamName = setting.TeamName
	resp.RepoPermission = setting.RepoPermission
	resp.TeamReviewer = "@" + owner + "/" + setting.TeamSlug
	return resp, nil
}

func (s *GitHubTriggerSetupService) Deactivate(ctx context.Context, orgID, userID, repositoryID uuid.UUID) error {
	if orgID == uuid.Nil || userID == uuid.Nil || repositoryID == uuid.Nil {
		return fmt.Errorf("org_id, user_id, and repository_id are required")
	}
	if _, err := s.loadRepository(ctx, orgID, repositoryID); err != nil {
		return err
	}
	return s.triggers.DeactivateGitHubTrigger(ctx, orgID, repositoryID, &userID)
}

func (s *GitHubTriggerSetupService) loadRepository(ctx context.Context, orgID, repositoryID uuid.UUID) (models.Repository, error) {
	if s.repos == nil {
		return models.Repository{}, fmt.Errorf("repository store is not configured")
	}
	repo, err := s.repos.GetByID(ctx, orgID, repositoryID)
	if err != nil {
		return models.Repository{}, err
	}
	return repo, nil
}

func (s *GitHubTriggerSetupService) validUserCredential(ctx context.Context, orgID, userID uuid.UUID) (*models.GitHubAppUserConfig, error) {
	if s.appUserAuth == nil {
		return nil, ErrGitHubTriggerAuthRequired
	}
	cred, err := s.appUserAuth.GetValidCredential(ctx, orgID, userID)
	if err != nil {
		if errors.Is(err, ghservice.ErrGitHubAppUserCredentialMissing) {
			return nil, ErrGitHubTriggerAuthRequired
		}
		return nil, fmt.Errorf("get GitHub user authorization: %w", err)
	}
	if cred == nil || strings.TrimSpace(cred.AccessToken) == "" {
		return nil, ErrGitHubTriggerAuthRequired
	}
	return cred, nil
}

func (s *GitHubTriggerSetupService) getOrCreateTeam(ctx context.Context, token, org string) (githubTriggerTeam, error) {
	team, err := s.getTeam(ctx, token, org, models.DefaultCodeReviewGitHubTriggerTeamSlug)
	if err == nil {
		return team, nil
	}
	var apiErr *ghservice.GitHubAPIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusNotFound {
		return githubTriggerTeam{}, err
	}
	return s.createTeam(ctx, token, org)
}

func (s *GitHubTriggerSetupService) getTeam(ctx context.Context, token, org, slug string) (githubTriggerTeam, error) {
	path := fmt.Sprintf("/orgs/%s/teams/%s", url.PathEscape(org), url.PathEscape(slug))
	var team githubTriggerTeam
	if err := s.githubJSON(ctx, http.MethodGet, path, token, nil, http.StatusOK, &team); err != nil {
		return githubTriggerTeam{}, err
	}
	return normalizeGitHubTriggerTeam(team), nil
}

func (s *GitHubTriggerSetupService) createTeam(ctx context.Context, token, org string) (githubTriggerTeam, error) {
	path := fmt.Sprintf("/orgs/%s/teams", url.PathEscape(org))
	body := map[string]any{
		"name":                 models.DefaultCodeReviewGitHubTriggerTeamName,
		"description":          "Trigger team for 143 Code Reviewer pull request reviews.",
		"privacy":              "closed",
		"notification_setting": "notifications_disabled",
	}
	var team githubTriggerTeam
	if err := s.githubJSON(ctx, http.MethodPost, path, token, body, http.StatusCreated, &team); err != nil {
		return githubTriggerTeam{}, err
	}
	return normalizeGitHubTriggerTeam(team), nil
}

func (s *GitHubTriggerSetupService) grantTeamRepository(ctx context.Context, token, org, teamSlug, owner, repo string) error {
	path := fmt.Sprintf("/orgs/%s/teams/%s/repos/%s/%s", url.PathEscape(org), url.PathEscape(teamSlug), url.PathEscape(owner), url.PathEscape(repo))
	body := map[string]any{"permission": models.DefaultCodeReviewGitHubTriggerRepoPerm}
	return s.githubJSON(ctx, http.MethodPut, path, token, body, http.StatusNoContent, nil)
}

func (s *GitHubTriggerSetupService) githubJSON(ctx context.Context, method, path, token string, body any, expectedStatus int, out any) error {
	ctx = githubtelemetry.WithRequestMetadata(ctx, githubtelemetry.RequestMetadata{
		Kind:     githubtelemetry.RequestKindAPI,
		AuthType: githubtelemetry.AuthTypeUser,
	})
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal GitHub request: %w", err)
		}
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(s.apiBaseURL, "/")+path, reader)
	if err != nil {
		return fmt.Errorf("build GitHub request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.httpClient.Do(req) // #nosec G107 -- base URL is fixed to GitHub except in tests.
	if err != nil {
		return fmt.Errorf("request GitHub %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != expectedStatus {
		raw, readErr := io.ReadAll(resp.Body)
		apiErr := &ghservice.GitHubAPIError{Method: method, Path: path, StatusCode: resp.StatusCode, Body: raw, Header: resp.Header.Clone()}
		var responseErr error = apiErr
		if readErr != nil {
			responseErr = errors.Join(apiErr, fmt.Errorf("read GitHub error response: %w", readErr))
		}
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound {
			return fmt.Errorf("%w: %w", ErrGitHubTriggerPermissionRequired, responseErr)
		}
		return responseErr
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode GitHub response: %w", err)
	}
	return nil
}

func defaultGitHubTriggerResponse(repo models.Repository) models.CodeReviewGitHubTriggerResponse {
	owner, _, _ := splitGitHubFullName(repo.FullName)
	resp := models.CodeReviewGitHubTriggerResponse{
		Status:             models.CodeReviewGitHubTriggerStatusUnconfigured,
		RepositoryID:       repo.ID,
		RepositoryFullName: repo.FullName,
		GitHubOrg:          owner,
		TeamSlug:           models.DefaultCodeReviewGitHubTriggerTeamSlug,
		TeamName:           models.DefaultCodeReviewGitHubTriggerTeamName,
		RepoPermission:     models.DefaultCodeReviewGitHubTriggerRepoPerm,
	}
	if owner != "" {
		resp.TeamReviewer = "@" + owner + "/" + resp.TeamSlug
	}
	return resp
}

func normalizeGitHubTriggerTeam(team githubTriggerTeam) githubTriggerTeam {
	if strings.TrimSpace(team.Slug) == "" {
		team.Slug = models.DefaultCodeReviewGitHubTriggerTeamSlug
	}
	if strings.TrimSpace(team.Name) == "" {
		team.Name = models.DefaultCodeReviewGitHubTriggerTeamName
	}
	return team
}

func splitGitHubFullName(fullName string) (string, string, error) {
	owner, name, ok := strings.Cut(strings.TrimSpace(fullName), "/")
	if !ok || strings.TrimSpace(owner) == "" || strings.TrimSpace(name) == "" {
		return "", "", fmt.Errorf("repository full_name must be owner/repo")
	}
	return strings.TrimSpace(owner), strings.TrimSpace(name), nil
}

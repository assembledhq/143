package handlers

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestCodeReviewHandler_SetupGitHubTriggerMapsMissingUserAuth(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	repoID := uuid.New()
	handler := NewCodeReviewHandler(nil, nil)
	handler.SetGitHubTriggerSetupService(codereviewsvc.NewGitHubTriggerSetupService(
		&codeReviewTriggerHandlerStoreStub{},
		&codeReviewTriggerHandlerRepoStub{repo: models.Repository{ID: repoID, OrgID: orgID, FullName: "acme/api"}},
		&codeReviewTriggerHandlerAuthStub{err: ghservice.ErrGitHubAppUserCredentialMissing},
		zerolog.Nop(),
	))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/code-review-github-trigger/setup", bytes.NewBufferString(`{"repository_id":"`+repoID.String()+`"}`))
	ctx := middleware.WithOrgID(req.Context(), orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID, Role: models.RoleAdmin})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.SetupGitHubTrigger(rr, req)

	require.Equal(t, http.StatusConflict, rr.Code, "missing GitHub user authorization should return a conflict")
	require.Contains(t, rr.Body.String(), "GITHUB_USER_AUTH_REQUIRED", "response should expose the reconnect error code")
}

type codeReviewTriggerHandlerStoreStub struct{}

func (s *codeReviewTriggerHandlerStoreStub) GetActiveGitHubTrigger(context.Context, uuid.UUID, uuid.UUID) (models.CodeReviewGitHubTriggerSetting, error) {
	return models.CodeReviewGitHubTriggerSetting{}, pgx.ErrNoRows
}

func (s *codeReviewTriggerHandlerStoreStub) SaveGitHubTrigger(context.Context, uuid.UUID, db.SaveCodeReviewGitHubTriggerParams) (models.CodeReviewGitHubTriggerSetting, error) {
	return models.CodeReviewGitHubTriggerSetting{}, nil
}

func (s *codeReviewTriggerHandlerStoreStub) DeactivateGitHubTrigger(context.Context, uuid.UUID, uuid.UUID, *uuid.UUID) error {
	return nil
}

type codeReviewTriggerHandlerRepoStub struct {
	repo models.Repository
}

func (s *codeReviewTriggerHandlerRepoStub) GetByID(context.Context, uuid.UUID, uuid.UUID) (models.Repository, error) {
	return s.repo, nil
}

type codeReviewTriggerHandlerAuthStub struct {
	err error
}

func (s *codeReviewTriggerHandlerAuthStub) GetValidCredential(context.Context, uuid.UUID, uuid.UUID) (*models.GitHubAppUserConfig, error) {
	return nil, s.err
}

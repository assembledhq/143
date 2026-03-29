package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/models"
)

// --- test doubles ---

type stubGHCredentialStore struct {
	cred *models.DecryptedUserCredential
	err  error
}

func (s *stubGHCredentialStore) GetForUser(_ context.Context, _, _ uuid.UUID, _ models.ProviderName) (*models.DecryptedUserCredential, error) {
	return s.cred, s.err
}
func (s *stubGHCredentialStore) Disable(_ context.Context, _, _ uuid.UUID, _ models.ProviderName) error {
	return nil
}

type stubGHOrgReader struct {
	org models.Organization
	err error
}

func (s *stubGHOrgReader) GetByID(_ context.Context, _ uuid.UUID) (models.Organization, error) {
	return s.org, s.err
}

// --- tests ---

func TestGitHubStatusHandler_GetStatus_Connected(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	login := "testuser"

	credStore := &stubGHCredentialStore{
		cred: &models.DecryptedUserCredential{
			Config: models.GitHubOAuthConfig{
				AccessToken: "gho_test",
				Scope:       "repo,read:org",
			},
		},
	}
	orgReader := &stubGHOrgReader{
		org: models.Organization{
			Settings: json.RawMessage(`{"pr_authorship":"user_preferred"}`),
		},
	}

	handler := NewGitHubStatusHandler(credStore, orgReader)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/users/me/github-status", nil)
	ctx := middleware.WithUser(req.Context(), &models.User{ID: userID, OrgID: orgID, GitHubLogin: &login})
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	handler.GetStatus(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)

	var resp GitHubStatusResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.True(t, resp.Connected)
	require.True(t, resp.HasRepoScope)
	require.Equal(t, "testuser", resp.GitHubLogin)
	require.Equal(t, "user_preferred", resp.PRAuthorshipMode)
}

func TestGitHubStatusHandler_GetStatus_NotConnected(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()

	credStore := &stubGHCredentialStore{
		err: context.DeadlineExceeded, // simulate no credential found
	}
	orgReader := &stubGHOrgReader{
		org: models.Organization{
			Settings: json.RawMessage(`{"pr_authorship":"app_only"}`),
		},
	}

	handler := NewGitHubStatusHandler(credStore, orgReader)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/users/me/github-status", nil)
	ctx := middleware.WithUser(req.Context(), &models.User{ID: userID, OrgID: orgID})
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	handler.GetStatus(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)

	var resp GitHubStatusResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.False(t, resp.Connected)
	require.False(t, resp.HasRepoScope)
	require.Equal(t, "app_only", resp.PRAuthorshipMode)
}

func TestHasRepoScope_Handler(t *testing.T) {
	t.Parallel()

	require.True(t, hasRepoScope("repo"))
	require.True(t, hasRepoScope("repo read:org"))
	require.True(t, hasRepoScope("read:user,repo,user:email"))
	require.False(t, hasRepoScope("read:user,user:email"))
	require.False(t, hasRepoScope(""))
	require.False(t, hasRepoScope("public_repo"))
}

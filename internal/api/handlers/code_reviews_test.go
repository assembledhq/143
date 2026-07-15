package handlers

import (
	"bytes"
	"context"
	"errors"
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

func TestCodeReviewHandler_ListRejectsInvalidOutcome(t *testing.T) {
	t.Parallel()

	handler := NewCodeReviewHandler(nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/code-reviews?outcome=bogus", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), uuid.New()))
	rr := httptest.NewRecorder()

	handler.List(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code, "an invalid outcome filter should return a bad request")
	require.Contains(t, rr.Body.String(), "INVALID_OUTCOME", "the response should identify the invalid outcome filter")
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

func TestCodeReviewHandler_streamOrgIDFromRequest(t *testing.T) {
	t.Parallel()

	ctxOrgID := uuid.New()
	userID := uuid.New()
	requestedOrgID := uuid.New()

	tests := []struct {
		name             string
		query            string
		setupMemberships func() *stubPullRequestMembershipStore
		expectedOrgID    uuid.UUID
		expectedErr      error
	}{
		{
			name:          "uses active org when request does not override it",
			expectedOrgID: ctxOrgID,
		},
		{
			name:  "uses requested org when user belongs to it",
			query: "?org_id=" + requestedOrgID.String(),
			setupMemberships: func() *stubPullRequestMembershipStore {
				return &stubPullRequestMembershipStore{
					getFunc: func(_ context.Context, gotUserID, gotOrgID uuid.UUID) (models.OrganizationMembership, error) {
						require.Equal(t, userID, gotUserID, "streamOrgIDFromRequest should validate membership for the current user")
						require.Equal(t, requestedOrgID, gotOrgID, "streamOrgIDFromRequest should validate the explicitly requested org")
						return models.OrganizationMembership{UserID: gotUserID, OrgID: gotOrgID, Role: models.RoleMember}, nil
					},
				}
			},
			expectedOrgID: requestedOrgID,
		},
		{
			name:        "rejects malformed requested org IDs",
			query:       "?org_id=not-a-uuid",
			expectedErr: errCodeReviewStreamOrgInvalid,
		},
		{
			name:  "rejects requested orgs the user is not a member of",
			query: "?org_id=" + requestedOrgID.String(),
			setupMemberships: func() *stubPullRequestMembershipStore {
				return &stubPullRequestMembershipStore{
					getFunc: func(context.Context, uuid.UUID, uuid.UUID) (models.OrganizationMembership, error) {
						return models.OrganizationMembership{}, pgx.ErrNoRows
					},
				}
			},
			expectedErr: errCodeReviewStreamOrgForbidden,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			handler := NewCodeReviewHandler(nil, nil)
			if tt.setupMemberships != nil {
				handler.SetMembershipStore(tt.setupMemberships())
			}

			req := httptest.NewRequest(http.MethodGet, "/api/v1/code-reviews/stream"+tt.query, nil)
			req = req.WithContext(middleware.WithOrgID(req.Context(), ctxOrgID))
			req = req.WithContext(middleware.WithUser(req.Context(), &models.User{ID: userID, OrgID: ctxOrgID}))

			orgID, err := handler.streamOrgIDFromRequest(req)
			if tt.expectedErr != nil {
				require.Error(t, err, "streamOrgIDFromRequest should reject invalid explicit org selections")
				require.True(t, errors.Is(err, tt.expectedErr), "streamOrgIDFromRequest should return the expected error sentinel")
				return
			}

			require.NoError(t, err, "streamOrgIDFromRequest should resolve the stream org without error")
			require.Equal(t, tt.expectedOrgID, orgID, "streamOrgIDFromRequest should resolve the expected org ID")
		})
	}
}

func TestCodeReviewHandler_streamOrgIDFromRequest_AdditionalErrors(t *testing.T) {
	t.Parallel()

	ctxOrgID := uuid.New()
	requestedOrgID := uuid.New()

	tests := []struct {
		name           string
		withUser       bool
		membershipErr  error
		expectedErr    error
		expectedSubstr string
	}{
		{
			name:        "returns unauthorized when explicit org requested without user",
			withUser:    false,
			expectedErr: errCodeReviewStreamUnauthorized,
		},
		{
			name:           "returns config error when membership store missing",
			withUser:       true,
			expectedSubstr: "membership store not configured",
		},
		{
			name:           "returns membership lookup errors",
			withUser:       true,
			membershipErr:  errors.New("db down"),
			expectedSubstr: "db down",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			handler := NewCodeReviewHandler(nil, nil)
			if tt.membershipErr != nil {
				handler.SetMembershipStore(&stubPullRequestMembershipStore{
					getFunc: func(context.Context, uuid.UUID, uuid.UUID) (models.OrganizationMembership, error) {
						return models.OrganizationMembership{}, tt.membershipErr
					},
				})
			}

			req := httptest.NewRequest(http.MethodGet, "/api/v1/code-reviews/stream?org_id="+requestedOrgID.String(), nil)
			req = req.WithContext(middleware.WithOrgID(req.Context(), ctxOrgID))
			if tt.withUser {
				req = req.WithContext(middleware.WithUser(req.Context(), &models.User{ID: uuid.New(), OrgID: ctxOrgID}))
			}

			_, err := handler.streamOrgIDFromRequest(req)
			require.Error(t, err, "streamOrgIDFromRequest should fail for this scenario")
			if tt.expectedErr != nil {
				require.True(t, errors.Is(err, tt.expectedErr), "streamOrgIDFromRequest should return the expected sentinel error")
			}
			if tt.expectedSubstr != "" {
				require.Contains(t, err.Error(), tt.expectedSubstr, "streamOrgIDFromRequest should preserve the underlying error context")
			}
		})
	}
}
